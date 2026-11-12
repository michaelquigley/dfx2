//go:build linux

package main

// Pass A of the integration feasibility spike: can a no-cgo Go host give
// JavaScriptCore a thread suspend/resume signal without taking one Go or the
// application relies on? Each variant runs in its own disposable process,
// started by signal_probe.py; nothing here changes the ordinary host.
//
// Dispositions are read from the kernel with rt_sigaction, not from libc or
// Go's own bookkeeping. A handler is classified by address: Go's (the handler
// on SIGURG, which the Go runtime always owns), default, ignore, or a native
// handler, named by the mapping that contains it.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// kernelSigaction is struct kernel_sigaction on linux/amd64.
type kernelSigaction struct {
	handler, flags, restorer uintptr
	mask                     uint64
}

var flagNames = []struct {
	bit  uintptr
	name string
}{{0x4, "SA_SIGINFO"}, {0x08000000, "SA_ONSTACK"}, {0x10000000, "SA_RESTART"}, {0x40000000, "SA_NODEFER"},
	{0x80000000, "SA_RESETHAND"}, {0x04000000, "SA_RESTORER"}}

type disposition struct {
	Signal  int      `json:"signal"`
	Owner   string   `json:"owner"`            // go, default, ignore, or native:<library>
	Offset  string   `json:"offset,omitempty"` // handler address minus its library's lowest mapping
	Flags   []string `json:"flags,omitempty"`
	Blocked bool     `json:"blocked,omitempty"`
}

func sigaction(sig int) (kernelSigaction, error) {
	var old kernelSigaction
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), 0, uintptr(unsafe.Pointer(&old)), 8, 0, 0)
	if e != 0 {
		return old, e
	}
	return old, nil
}

// setDisposition writes a handler and flags with rt_sigaction. Only signals
// this probe owns are written: one Go has released, or JSC's own handler
// with SA_ONSTACK added.
func setDisposition(sig int, a kernelSigaction) error {
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&a)), 0, 8, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

// threadBlocked is the calling thread's blocked set.
func threadBlocked() uint64 {
	var old uint64
	syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, 0, 0, uintptr(unsafe.Pointer(&old)), 8, 0, 0)
	return old
}

type mapping struct {
	lo, hi uint64
	path   string
}

func mappings() []mapping {
	f, err := os.Open("/proc/self/maps")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []mapping
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 6 {
			continue
		}
		bounds := strings.SplitN(fields[0], "-", 2)
		lo, _ := strconv.ParseUint(bounds[0], 16, 64)
		hi, _ := strconv.ParseUint(bounds[1], 16, 64)
		out = append(out, mapping{lo, hi, fields[5]})
	}
	return out
}

func offsetOf(handler uintptr, maps []mapping) string {
	if handler <= 1 {
		return ""
	}
	for _, m := range maps {
		if uint64(handler) >= m.lo && uint64(handler) < m.hi {
			base := m.lo
			for _, o := range maps {
				if o.path == m.path && o.lo < base {
					base = o.lo
				}
			}
			return fmt.Sprintf("0x%x", uint64(handler)-base)
		}
	}
	return ""
}

func ownerOf(handler, goHandler uintptr, maps []mapping) string {
	switch handler {
	case 0:
		return "default"
	case 1:
		return "ignore"
	case goHandler:
		return "go"
	}
	for _, m := range maps {
		if uint64(handler) >= m.lo && uint64(handler) < m.hi {
			name := m.path[strings.LastIndex(m.path, "/")+1:]
			return "native:" + name
		}
	}
	return fmt.Sprintf("native:0x%x", handler)
}

type probe struct {
	goHandler uintptr
	report    map[string]any
	stages    []map[string]any
	notified  []syscall.Signal
	jsc       struct {
		configure               func(int32) bool
		globalContextCreate     func(uintptr) uintptr
		globalContextRelease    func(uintptr)
		stringCreate            func(string) uintptr
		stringRelease           func(uintptr)
		evaluate                func(uintptr, uintptr, uintptr, uintptr, int32, *uintptr) uintptr
		valueToNumber           func(uintptr, uintptr, *uintptr) float64
		garbageCollect          func(uintptr)
		synchronousCollectDebug func(uintptr)
		groupCreate             func() uintptr
		createInGroup           func(uintptr, uintptr) uintptr
	}
	rtmin, rtmax int
}

func (p *probe) snapshot(stage string) []disposition {
	maps := mappings()
	blocked := threadBlocked()
	var out []disposition
	for sig := 1; sig <= 64; sig++ {
		if sig == int(syscall.SIGKILL) || sig == int(syscall.SIGSTOP) {
			continue
		}
		a, err := sigaction(sig)
		if err != nil {
			continue
		}
		d := disposition{Signal: sig, Owner: ownerOf(a.handler, p.goHandler, maps), Blocked: blocked&(1<<(sig-1)) != 0}
		if strings.HasPrefix(d.Owner, "native:") {
			d.Offset = offsetOf(a.handler, maps)
		}
		if a.handler > 1 {
			for _, f := range flagNames {
				if a.flags&f.bit != 0 {
					d.Flags = append(d.Flags, f.name)
				}
			}
		}
		out = append(out, d)
	}
	p.stages = append(p.stages, map[string]any{"stage": stage, "dispositions": out})
	return out
}

func find(ds []disposition, sig int) disposition {
	for _, d := range ds {
		if d.Signal == sig {
			return d
		}
	}
	return disposition{Signal: sig, Owner: "unread"}
}

// changed lists signals whose owner or flags differ between two snapshots.
func changed(before, after []disposition) []map[string]any {
	var out []map[string]any
	for _, a := range after {
		b := find(before, a.Signal)
		if b.Owner != a.Owner || !slices.Equal(b.Flags, a.Flags) {
			out = append(out, map[string]any{"signal": a.Signal, "before": b, "after": a})
		}
	}
	return out
}

// selectSignal applies the proposed policy: a real-time signal inside libc's
// usable range (libc reserves those below SIGRTMIN for its own threading),
// searched from SIGRTMAX down, whose kernel disposition is still the Go
// runtime's own handler (no other native component installed one), not
// blocked on this thread, and not one the application has asked Go to
// deliver. A default disposition is not accepted as evidence of a free
// signal: in a Go process every catchable signal starts with Go's handler,
// so anything else means another owner.
func (p *probe) selectSignal(ds []disposition, injectNone bool) (int, []map[string]any) {
	var rejected []map[string]any
	for sig := p.rtmax; sig >= p.rtmin; sig-- {
		d := find(ds, sig)
		reason := ""
		switch {
		case injectNone:
			reason = "injected: discovery reports every candidate unavailable"
		case d.Owner != "go":
			reason = "owner is " + d.Owner
		case d.Blocked:
			reason = "blocked on the initializing thread"
		case slices.Contains(p.notified, syscall.Signal(sig)):
			reason = "the application asked Go to deliver it"
		}
		if reason == "" {
			return sig, rejected
		}
		if len(rejected) < 8 || injectNone {
			rejected = append(rejected, map[string]any{"signal": sig, "reason": reason})
		}
	}
	return 0, rejected
}

func (p *probe) loadJSC(injectMissing bool) (bool, error) {
	libc, err := purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return false, err
	}
	var rtmin, rtmax func() int32
	for name, dest := range map[string]any{"__libc_current_sigrtmin": &rtmin, "__libc_current_sigrtmax": &rtmax} {
		addr, err := purego.Dlsym(libc, name)
		if err != nil {
			return false, err
		}
		purego.RegisterFunc(dest, addr)
	}
	p.rtmin, p.rtmax = int(rtmin()), int(rtmax())
	h, err := purego.Dlopen("libjavascriptcoregtk-4.1.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return false, err
	}
	for name, dest := range map[string]any{
		"JSGlobalContextCreate": &p.jsc.globalContextCreate, "JSGlobalContextRelease": &p.jsc.globalContextRelease,
		"JSStringCreateWithUTF8CString": &p.jsc.stringCreate, "JSStringRelease": &p.jsc.stringRelease,
		"JSContextGroupCreate": &p.jsc.groupCreate, "JSGlobalContextCreateInGroup": &p.jsc.createInGroup,
		"JSEvaluateScript": &p.jsc.evaluate, "JSValueToNumber": &p.jsc.valueToNumber, "JSGarbageCollect": &p.jsc.garbageCollect,
		// Private, exported but absent from the installed public headers.
		"JSSynchronousGarbageCollectForDebugging": &p.jsc.synchronousCollectDebug,
	} {
		addr, err := purego.Dlsym(h, name)
		if err != nil {
			return false, fmt.Errorf("%s: %w", name, err)
		}
		purego.RegisterFunc(dest, addr)
	}
	if injectMissing {
		return false, nil
	}
	// Private, exported on this build but absent from the public headers.
	addr, err := purego.Dlsym(h, "JSConfigureSignalForGC")
	if err != nil {
		return false, nil
	}
	purego.RegisterFunc(&p.jsc.configure, addr)
	return true, nil
}

// jsWorkload evaluates allocation-heavy JavaScript in the host process and
// requests collections, on this locked thread, while Go goroutines allocate,
// collect and run a tight loop that only asynchronous preemption can stop.
func (p *probe) workload(ctx uintptr, seconds float64) map[string]any {
	var stop atomic.Bool
	var wg sync.WaitGroup
	var goCollections, goAllocated atomic.Int64
	wg.Add(2)
	go func() { // Go allocation and explicit collections
		defer wg.Done()
		var keep [][]byte
		for !stop.Load() {
			for i := 0; i < 2000; i++ {
				keep = append(keep, make([]byte, 512))
			}
			goAllocated.Add(2000 * 512)
			keep = keep[:0]
			runtime.GC()
			goCollections.Add(1)
		}
	}()
	var spins atomic.Int64
	go func() { // only SIGURG-based asynchronous preemption interrupts this
		defer wg.Done()
		n := int64(0)
		for !stop.Load() {
			for i := 0; i < 1<<20; i++ {
				n += int64(i)
			}
			spins.Add(1)
		}
		_ = n
	}()
	script := p.jsc.stringCreate(`(function(){let keep=[];let total=0;for(let i=0;i<200000;i++){const o={i:i,s:'x'+i,a:[i,i+1,i+2]};if(i%7===0)keep.push(o);total+=o.a.length;}keep=null;return total;})()`)
	defer p.jsc.stringRelease(script)
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	rounds, collections := 0, 0
	var jsTotal float64
	for time.Now().Before(deadline) {
		var exc uintptr
		v := p.jsc.evaluate(ctx, script, 0, 0, 1, &exc)
		if exc != 0 || v == 0 {
			return map[string]any{"error": "JavaScript threw"}
		}
		jsTotal = p.jsc.valueToNumber(ctx, v, &exc)
		rounds++
		if rounds%5 == 0 {
			p.jsc.garbageCollect(ctx)
			collections++
		}
		if rounds%20 == 0 {
			p.jsc.synchronousCollectDebug(ctx)
			collections++
		}
	}
	stop.Store(true)
	wg.Wait()
	var stats debug.GCStats
	debug.ReadGCStats(&stats)
	return map[string]any{"js_rounds": rounds, "js_result": jsTotal, "js_collections_requested": collections,
		"go_collections": goCollections.Load(), "go_bytes_allocated": goAllocated.Load(), "go_numgc": stats.NumGC,
		"tight_loop_rounds": spins.Load()}
}

// deliver raises sig at this process and reports whether the application's
// Go channel received it. A hang here is the hazard, so the driver bounds it.
func deliver(ch chan os.Signal, sig syscall.Signal) map[string]any {
	for len(ch) > 0 {
		<-ch
	}
	syscall.Kill(os.Getpid(), sig)
	select {
	case got := <-ch:
		return map[string]any{"signal": int(sig), "received_by_go": got == sig}
	case <-time.After(2 * time.Second):
		return map[string]any{"signal": int(sig), "received_by_go": false}
	}
}

// runSignalProbe is one disposable subprocess. Variants:
//
//	baseline    no configuration: the runtime's default choice
//	configured  the proposed policy, then JSConfigureSignalForGC before initialization
//	configured-onstack  as configured, then SA_ONSTACK added to JSC's fault handlers
//	configured-onstack-all  as configured-onstack, including the suspend signal (a hazard probe)
//	late        initialize first, then attempt configuration (a native refusal)
//	env         JSC_SIGNAL_FOR_GC was set by the caller: respect it, configure nothing
//	no-symbol   injected: discovery reports the private entry point missing
//	no-signal   injected: discovery reports no usable signal
func runSignalProbe(variant, fault string, viaWebKit bool, order string, jsThreads int) error {
	runtime.LockOSThread()
	p := &probe{report: map[string]any{"variant": variant, "pid": os.Getpid()}}
	// The application's own signal use, which must survive JSC.
	p.notified = []syscall.Signal{syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGHUP}
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGHUP)
	urg, _ := sigaction(int(syscall.SIGURG))
	p.goHandler = urg.handler
	start := p.snapshot("go-start")
	available, err := p.loadJSC(variant == "no-symbol")
	if err != nil {
		return err
	}
	loaded := p.snapshot("after-dlopen")
	p.report["libc_sigrtmin"], p.report["libc_sigrtmax"] = p.rtmin, p.rtmax
	p.report["configure_symbol"] = map[bool]string{true: "present", false: "absent"}[available]
	if variant == "no-symbol" {
		p.report["configure_symbol"] = "absent (injected)"
	}
	p.report["env_JSC_SIGNAL_FOR_GC"] = os.Getenv("JSC_SIGNAL_FOR_GC")
	ready, selected := true, 0
	reason := ""
	switch {
	case variant == "env":
		// An operator's JSC_SIGNAL_FOR_GC is respected, not overridden: JSC
		// reads it itself. The host still has to release that signal from Go,
		// or JSC replaces Go's handler; one it cannot release is refused.
		env, err := strconv.Atoi(os.Getenv("JSC_SIGNAL_FOR_GC"))
		d := find(loaded, env)
		switch {
		case err != nil || env < p.rtmin || env > p.rtmax:
			ready, reason = false, "JSC_SIGNAL_FOR_GC is not a real-time signal in libc's usable range; refusing"
		case d.Owner != "go" || d.Blocked || slices.Contains(p.notified, syscall.Signal(env)):
			ready, reason = false, "JSC_SIGNAL_FOR_GC names a signal with another owner or application use; refusing"
		default:
			signal.Ignore(syscall.Signal(env))
			if err := setDisposition(env, kernelSigaction{}); err != nil {
				return err
			}
			selected, reason = env, "operator-selected JSC_SIGNAL_FOR_GC released from Go; JSC configures itself"
		}
	case variant == "baseline":
		reason = "baseline: no configuration attempted"
		ready = false
	case variant == "late":
	case !available:
		ready, reason = false, "configuration entry point unavailable; refusing to start a browser with the default collision"
	default:
		var rejected []map[string]any
		selected, rejected = p.selectSignal(loaded, variant == "no-signal")
		p.report["rejected_candidates"] = rejected
		if selected == 0 {
			ready, reason = false, "no usable signal; refusing to start a browser with the default collision"
			break
		}
		// Release the signal from Go through Go's own API, then return it to
		// the default disposition. JSC warns about, and silently replaces,
		// anything other than SIG_DFL, including the SIG_IGN that Ignore
		// leaves; a default disposition after an explicit release is the
		// only state in which it takes nothing from anyone.
		signal.Ignore(syscall.Signal(selected))
		p.snapshot("after-go-ignore")
		if err := setDisposition(selected, kernelSigaction{}); err != nil {
			return err
		}
		p.snapshot("after-release-to-default")
		ok := p.jsc.configure(int32(selected))
		p.report["configure_result"] = ok
		if !ok {
			ready, reason = false, "JSConfigureSignalForGC refused the signal"
		}
	}
	p.report["selected_signal"] = selected
	configured := p.snapshot("before-jsc-init")
	if variant == "no-symbol" || variant == "no-signal" || (!ready && variant != "baseline") {
		p.report["ready"], p.report["reason"] = false, reason
		p.report["changed_before_init"] = changed(start, configured)
		return p.emit()
	}
	// Proposed order (jsc-first): force JSC's one-time initialization in
	// this process before WebKit, so its handlers exist at a known point,
	// then protect them. webkit-first lets WebKit initialize JSC itself.
	protectGC := variant == "configured-onstack-all"
	protecting := (variant == "configured-onstack" || protectGC) && ready
	if order == "webkit-first" && viaWebKit {
		if err := p.webkitInit(); err != nil {
			return err
		}
	}
	ctx := p.jsc.globalContextCreate(0)
	initialized := p.snapshot("after-jsc-init")
	if variant == "late" {
		ok := p.jsc.configure(int32(p.rtmax))
		p.report["configure_result_after_init"] = ok
		if !ok {
			ready, reason = false, "JSConfigureSignalForGC refused after initialization; the default collision remains"
		}
		initialized = p.snapshot("after-late-configure")
		// Refused: report the remaining collision and stop. Continuing would
		// only repeat the baseline's hazards.
		p.report["ready"], p.report["reason"] = ready, reason
		p.report["changed_since_start"] = changed(start, initialized)
		return p.emit()
	}
	if protecting {
		p.report["onstack_added"] = p.protect(configured, initialized, selected, protectGC)
		initialized = p.snapshot("after-onstack")
	}
	if order != "webkit-first" && viaWebKit {
		if err := p.webkitInit(); err != nil {
			return err
		}
		after := p.snapshot("after-webkit")
		p.report["changed_by_webkit"] = changed(initialized, after)
		if protecting {
			// Anything installed after the protection step is reported, then
			// protected the same way.
			p.report["onstack_added_after_webkit"] = p.protect(initialized, after, selected, protectGC)
			after = p.snapshot("after-webkit-onstack")
		}
		initialized = after
	}
	p.report["unprotected_native_handlers"] = unprotected(initialized)
	p.report["order"] = order
	if fault != "" {
		// After initialization, a Go goroutine faults and recovers. Go turns
		// SIGSEGV into a nil-pointer panic and, with SetPanicOnFault, SIGBUS
		// into a recoverable fault; JSC's handlers now see them first.
		fmt.Println(`{"event":"before-fault","fault":"` + fault + `"}`)
		p.report["fault"] = map[string]any{"kind": fault, "recovered": faultAndRecover(fault)}
		p.report["ready"], p.report["reason"] = ready, reason
		return p.emit()
	}
	p.report["ready"], p.report["reason"] = ready, reason
	p.report["changed_by_jsc_init"] = changed(configured, initialized)
	p.report["changed_since_start"] = changed(start, initialized)
	p.report["workload"] = p.workload(ctx, 3)
	if jsThreads > 1 {
		// A second Go thread runs JavaScript in the same context group. JSC
		// registers both threads; a collection on one scans the other's stack
		// by suspending it with the configured signal.
		p.report["second_thread"] = p.secondThread(3)
	}
	after := p.snapshot("after-workload")
	p.report["changed_by_workload"] = changed(initialized, after)
	p.report["delivery"] = []map[string]any{deliver(ch, syscall.SIGUSR2), deliver(ch, syscall.SIGHUP)}
	p.jsc.globalContextRelease(ctx)
	// SIGUSR1 last: with the default collision, JSC's handler treats it as a
	// suspend request and the thread it lands on may never resume.
	fmt.Println(`{"event":"before-sigusr1"}`)
	p.report["delivery"] = append(p.report["delivery"].([]map[string]any), deliver(ch, syscall.SIGUSR1))
	return p.emit()
}

func (p *probe) emit() error {
	p.report["stages"] = p.stages
	libs := map[string]bool{}
	for _, m := range mappings() {
		for _, name := range []string{"libwebkit2gtk", "libjavascriptcoregtk", "libgtk-3", "libsoup-3", "libglib-2.0", "libc.so"} {
			if strings.Contains(m.path, name) {
				libs[m.path] = true
			}
		}
	}
	p.report["libraries"] = slices.Sorted(maps.Keys(libs))
	b, err := json.Marshal(map[string]any{"event": "signal-probe", "report": p.report})
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

var faultSink byte

// faultAndRecover provokes a fault on an ordinary goroutine and reports
// whether Go recovered it as a panic. A process that dies here never reports.
func faultAndRecover(kind string) string {
	done := make(chan string, 1)
	go func() {
		defer func() {
			r := recover()
			done <- fmt.Sprint(r)
		}()
		switch kind {
		case "segv":
			var p *struct{ x int }
			p.x = 1
		case "bus":
			debug.SetPanicOnFault(true)
			f, err := os.CreateTemp("", "dfx2-sigbus-")
			if err != nil {
				panic(err)
			}
			defer os.Remove(f.Name())
			f.Truncate(4096)
			b, err := syscall.Mmap(int(f.Fd()), 0, 4096, syscall.PROT_READ, syscall.MAP_SHARED)
			if err != nil {
				panic(err)
			}
			f.Truncate(0)
			faultSink += b[0] // the page is now beyond end of file: SIGBUS
		}
	}()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		return "no recovery within 5s"
	}
}

// webkitInit brings up GTK and a WebKit view whose page allocates in a loop,
// snapshotting dispositions at each step, and pumps the main context so the
// web process starts and runs the page.
func (p *probe) webkitInit() error {
	if err := loadNative(); err != nil {
		return err
	}
	glib, err := purego.Dlopen("libglib-2.0.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	var iterate func(uintptr, int32) int32
	addr, err := purego.Dlsym(glib, "g_main_context_iteration")
	if err != nil {
		return err
	}
	purego.RegisterFunc(&iterate, addr)
	pump := func(d time.Duration) {
		for end := time.Now().Add(d); time.Now().Before(end); {
			iterate(0, 0)
			time.Sleep(2 * time.Millisecond)
		}
	}
	if n.gtkInitCheck(0, 0) == 0 {
		return fmt.Errorf("GTK could not open the display")
	}
	p.snapshot("after-gtk-init")
	webctx := n.webkitWebContextNewEphemeral()
	p.snapshot("after-webkit-context")
	window := n.gtkWindowNew(0)
	n.gtkWindowSetDefaultSize(window, 400, 300)
	view := n.webkitWebViewNewWithContext(webctx)
	n.gtkContainerAdd(window, view)
	n.gtkWidgetShowAll(window)
	p.snapshot("after-webkit-view")
	n.webkitWebViewLoadURI(view, "data:text/html,<title>signals</title><script>let keep=[];setInterval(()=>{for(let i=0;i<50000;i++){keep.push({i,s:'x'+i});}if(keep.length>400000)keep=[];document.title='n'+keep.length},20)</script>")
	pump(3 * time.Second)
	p.report["webkit_page_title"] = n.webkitWebViewGetURI(view) != ""
	p.snapshot("after-webkit-page")
	return nil
}

// protect adds SA_ONSTACK to handlers that JSC or WebKit installed between
// two snapshots. JSC installs its fault handlers without it; a Go fault then
// reaches Go's handler, chained by JSC, on a goroutine stack, and Go refuses
// that. With SA_ONSTACK the handler, mask and other flags stay JSC's and
// delivery moves to the signal stack Go registers on each of its threads.
// The suspend signal is excluded unless includeGC: JSC's suspend handler only
// suspends a thread whose stack pointer is within the thread's own stack.
func (p *probe) protect(before, after []disposition, gcSignal int, includeGC bool) []int {
	var added []int
	for _, c := range changed(before, after) {
		d := c["after"].(disposition)
		switch {
		case !strings.HasPrefix(d.Owner, "native:libjavascriptcoregtk") && !strings.HasPrefix(d.Owner, "native:libwebkit2gtk"),
			slices.Contains(d.Flags, "SA_ONSTACK"),
			d.Signal == gcSignal && !includeGC:
			continue
		}
		a, err := sigaction(d.Signal)
		if err != nil {
			continue
		}
		a.flags |= 0x08000000
		if setDisposition(d.Signal, a) == nil {
			added = append(added, d.Signal)
		}
	}
	return added
}

// unprotected lists native handlers that lack SA_ONSTACK.
func unprotected(ds []disposition) []disposition {
	var out []disposition
	for _, d := range ds {
		if strings.HasPrefix(d.Owner, "native:") && !slices.Contains(d.Flags, "SA_ONSTACK") {
			out = append(out, d)
		}
	}
	return out
}

// secondThread runs JavaScript on two locked Go threads in one context
// group, so each collection must scan the other thread's stack.
func (p *probe) secondThread(seconds float64) map[string]any {
	group := p.jsc.groupCreate()
	var wg sync.WaitGroup
	rounds := make([]int, 2)
	for t := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			ctx := p.jsc.createInGroup(group, 0)
			script := p.jsc.stringCreate(`(function(){let k=[];for(let i=0;i<100000;i++){k.push({i,s:'y'+i});}return k.length})()`)
			for end := time.Now().Add(time.Duration(seconds * float64(time.Second))); time.Now().Before(end); {
				var exc uintptr
				p.jsc.evaluate(ctx, script, 0, 0, 1, &exc)
				rounds[t]++
				if rounds[t]%3 == 0 {
					p.jsc.synchronousCollectDebug(ctx)
				}
				// Go work between JavaScript calls, on this goroutine's stack.
				buf := make([]byte, 4096)
				for i := range buf {
					buf[i] = byte(i)
				}
				faultSink += buf[rounds[t]%4096]
			}
			p.jsc.stringRelease(script)
			p.jsc.globalContextRelease(ctx)
		}()
	}
	wg.Wait()
	return map[string]any{"rounds": rounds}
}
