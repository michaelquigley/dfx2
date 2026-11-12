//go:build linux

package main

// The handler-safety follow-up. Three questions, each a disposable process:
//
//   - suspension-transition: does JavaScriptCore send its GC suspend signal
//     to a host thread that used the JSC API and has returned to Go? Phases
//     on the one UI-like thread alternate JavaScript, pure Go on the
//     goroutine stack, and a blocking C call on the thread's own stack, each
//     announced by a distinct raw syscall so strace can attribute deliveries
//     to the phase the thread was in.
//   - narrow: the explicit reservation, forced JSC initialization, then
//     SA_ONSTACK added only to SIGSEGV/SIGBUS handlers whose identity as
//     JSC's fault handler is verified; anything else is left alone and
//     reported.
//   - with -signal-webkit, narrow also watches SIGXCPU's disposition, the
//     process's real-time threads and RLIMIT_RTTIME while a page runs.
//
// Experiment only: no dfx2 API, and nothing here is approved for production.

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"

	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// reserve validates and applies an explicit reservation as the reservation
// probe does: sanity checks, release from Go, SIG_DFL, then the private
// configuration call. It reports why it refused.
func (p *probe) reserve(sig int, ds []disposition) (bool, string) {
	for _, c := range p.validateReservation(sig, ds) {
		if !c.Passed {
			return false, "reservation refused: " + c.Check
		}
	}
	signal.Ignore(syscall.Signal(sig))
	if err := setDisposition(sig, kernelSigaction{}); err != nil {
		return false, err.Error()
	}
	if !p.jsc.configure(int32(sig)) {
		return false, "JSConfigureSignalForGC refused"
	}
	return true, ""
}

// stackMapping is the process's [stack] mapping: the main thread's own
// stack, which WTF records as that thread's bounds when it adopts it.
func stackMapping() (uint64, uint64) {
	for _, m := range mappings() {
		if m.path == "[stack]" {
			return m.lo, m.hi
		}
	}
	return 0, 0
}

type threadStat struct {
	TID   int    `json:"tid"`
	Name  string `json:"name"`
	Ticks int    `json:"cpu_ticks"`
	Pol   int    `json:"policy"` // 0 SCHED_OTHER, 2 SCHED_RR
}

// threads reads every task's name, CPU ticks and scheduling policy.
func threads() []threadStat {
	dirs, _ := os.ReadDir("/proc/self/task")
	var out []threadStat
	for _, d := range dirs {
		b, err := os.ReadFile("/proc/self/task/" + d.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || close < open {
			continue
		}
		fields := strings.Fields(s[close+2:])
		tid, _ := strconv.Atoi(d.Name())
		utime, _ := strconv.Atoi(fields[11])
		stime, _ := strconv.Atoi(fields[12])
		pol, _ := strconv.Atoi(fields[38])
		out = append(out, threadStat{tid, s[open+1 : close], utime + stime, pol})
	}
	return out
}

func ticksOf(ts []threadStat, name string) int {
	n := 0
	for _, t := range ts {
		if strings.HasPrefix(t.Name, name) {
			n += t.Ticks
		}
	}
	return n
}

// Phase markers: cheap syscalls strace shows on the calling thread.
func markJS() { syscall.RawSyscall(syscall.SYS_GETGID, 0, 0, 0) }
func markGo() { syscall.RawSyscall(syscall.SYS_GETPPID, 0, 0, 0) }
func markC()  { syscall.RawSyscall(syscall.SYS_GETUID, 0, 0, 0) }

var spinSink uint64

func runHandlerSafetyProbe(variant string, reserved int, fault string, viaWebKit bool, order string) error {
	runtime.LockOSThread()
	p := &probe{report: map[string]any{"variant": variant, "pid": os.Getpid(), "reserve_flag": reserved}}
	p.notified = []syscall.Signal{syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGHUP}
	declared := make(chan os.Signal, 8)
	signal.Notify(declared, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGHUP)
	urg, _ := sigaction(int(syscall.SIGURG))
	p.goHandler = urg.handler
	p.snapshot("go-start")
	if _, err := p.loadJSC(false); err != nil {
		return err
	}
	loaded := p.snapshot("after-dlopen")
	p.report["libc_sigrtmin"], p.report["libc_sigrtmax"] = p.rtmin, p.rtmax
	ok, reason := p.reserve(reserved, loaded)
	p.report["reserved_signal"] = reserved
	if !ok {
		p.report["ready"], p.report["reason"] = false, reason
		return p.emit()
	}
	configured := p.snapshot("before-jsc-init")
	switch variant {
	case "suspension-transition":
		return p.transition(reserved)
	case "narrow", "narrow-foreign":
		return p.narrow(reserved, configured, fault, viaWebKit, order, declared)
	}
	return fmt.Errorf("unknown variant %q", variant)
}

// transition runs JavaScript on this locked thread, then leaves JSC: first
// for pure Go on the goroutine stack, then for a blocking C call on the
// thread's own stack. JSC registered the thread when it took the API lock
// and keeps it registered; once the lock is released, the collector thread
// may stop the world and scan this thread's stack by suspending it.
func (p *probe) transition(sig int) error {
	var usleep func(uint32) int32
	libc, _ := purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	addr, err := purego.Dlsym(libc, "usleep")
	if err != nil {
		return err
	}
	purego.RegisterFunc(&usleep, addr)
	lo, hi := stackMapping()
	var local int
	here := uint64(uintptr(unsafe.Pointer(&local)))
	p.report["thread_stack"] = map[string]any{"mapping_lo": fmt.Sprintf("0x%x", lo), "mapping_hi": fmt.Sprintf("0x%x", hi),
		"goroutine_local": fmt.Sprintf("0x%x", here), "goroutine_stack_inside_mapping": here >= lo && here < hi}
	ctx := p.jsc.globalContextCreate(0)
	p.snapshot("after-jsc-init")
	// A large retained graph makes every collection's marking long; churn
	// keeps requesting collections.
	script := p.jsc.stringCreate(`(function(){if(!globalThis.keep)globalThis.keep=[];const k=globalThis.keep;for(let i=0;i<150000;i++){k.push({i,s:'r'+i,a:[i,i]});}if(k.length>900000)k.splice(0,300000);let t=0;for(let i=0;i<150000;i++){t+=({i}).i;}return k.length+t%2;})()`)
	defer p.jsc.stringRelease(script)
	type phase struct {
		Cycle    int               `json:"cycle"`
		JSms     float64           `json:"js_ms"`
		Goms     float64           `json:"go_ms"`
		Cms      float64           `json:"c_ms,omitempty"`
		Scanners map[string][2]int `json:"scanner_ticks_go_c"` // CPU ticks during the Go and the C phase
	}
	var phases []phase
	// Threads that conservatively scan stacks: the parallel marking helpers
	// and the collector thread (its comm reads "ollector Thread" here).
	scanners := []string{"HeapHelper", "ollector Thread"}
	tick := func() map[string]int {
		ts, out := threads(), map[string]int{}
		for _, s := range scanners {
			out[s] = ticksOf(ts, s)
		}
		return out
	}
	for cycle := 0; cycle < 12; cycle++ {
		ph := phase{Cycle: cycle}
		markJS()
		t := time.Now()
		var exc uintptr
		p.jsc.evaluate(ctx, script, 0, 0, 1, &exc)
		ph.JSms = float64(time.Since(t).Microseconds()) / 1000
		before := tick()
		markGo()
		t = time.Now()
		for time.Since(t) < 250*time.Millisecond { // Go only, on the goroutine stack
			for i := 0; i < 1<<14; i++ {
				spinSink += uint64(i) * 2654435761
			}
		}
		ph.Goms = float64(time.Since(t).Microseconds()) / 1000
		mid := tick()
		markC()
		t = time.Now()
		usleep(250000) // blocked in C, on this thread's own stack
		ph.Cms = float64(time.Since(t).Microseconds()) / 1000
		end := tick()
		ph.Scanners = map[string][2]int{}
		for _, s := range scanners {
			ph.Scanners[s] = [2]int{mid[s] - before[s], end[s] - mid[s]}
		}
		phases = append(phases, ph)
	}
	markJS() // closes the last phase for the trace parser
	p.report["phases"] = phases
	p.report["threads"] = threads()
	p.jsc.globalContextRelease(ctx)
	p.report["ready"] = true
	return p.emit()
}

// faultHandlerIdentity checks that SIGSEGV and SIGBUS carry JSC's fault
// handler as WTF::SignalHandlers::finalize installs it (Signals.cpp,
// webkitgtk-2.52.6): one sigaction for both signals, SA_SIGINFO only, and a
// mask of every signal except the GC suspend signal. sigfillset leaves out
// glibc's two internal signals and the kernel cannot block SIGKILL or SIGSTOP,
// so those four are absent from the mask too. Each check is recorded; any
// failure means the handlers are not touched.
func (p *probe) faultHandlerIdentity(gc int, before, after []disposition) ([]reservationCheck, bool) {
	segv, _ := sigaction(int(syscall.SIGSEGV))
	bus, _ := sigaction(int(syscall.SIGBUS))
	suspend, _ := sigaction(gc)
	var want uint64 = ^uint64(0)
	for _, s := range []int{9, 19, 32, 33, gc} {
		want &^= 1 << (s - 1)
	}
	installedNow := func(sig int) bool {
		b, a := find(before, sig), find(after, sig)
		return b.Owner == "go" && strings.HasPrefix(a.Owner, "native:libjavascriptcoregtk")
	}
	checks := []reservationCheck{
		{Check: "installed by the forced JSC initialization", Passed: installedNow(7) && installedNow(11),
			Detail: find(after, 11).Owner},
		{Check: "one handler for SIGSEGV and SIGBUS", Passed: segv.handler == bus.handler && segv.handler > 1},
		{Check: "not the suspend handler", Passed: segv.handler != suspend.handler},
		{Check: "flags SA_SIGINFO only (plus libc's SA_RESTORER)", Passed: segv.flags == 0x04000004 && bus.flags == 0x04000004,
			Detail: fmt.Sprintf("0x%x 0x%x", segv.flags, bus.flags)},
		{Check: "mask is every signal except the GC suspend signal", Passed: segv.mask == want && bus.mask == want,
			Detail: fmt.Sprintf("0x%x, expected 0x%x", segv.mask, want)},
	}
	for _, c := range checks {
		if !c.Passed {
			return checks, false
		}
	}
	return checks, true
}

func (p *probe) narrow(gc int, configured []disposition, fault string, viaWebKit bool, order string, declared chan os.Signal) error {
	p.report["order"] = order
	webkitFirst := viaWebKit && order == "webkit-first"
	if webkitFirst {
		// WebKit initializes before the forced JSC initialization, as the
		// earlier run that first saw SIGXCPU did.
		if err := p.webkitInit(); err != nil {
			return err
		}
		configured = p.snapshot("after-webkit-before-jsc")
	}
	ctx := p.jsc.globalContextCreate(0)
	initialized := p.snapshot("after-jsc-init")
	p.report["changed_by_jsc_init"] = changed(configured, initialized)
	if p.report["variant"] == "narrow-foreign" {
		// Injected: another component changes SIGBUS after JSC installed it.
		setDisposition(int(syscall.SIGBUS), kernelSigaction{handler: 1})
		p.report["injected"] = "SIGBUS set to SIG_IGN after JSC initialization"
		initialized = p.snapshot("after-injected-sigbus-change")
	}
	checks, identified := p.faultHandlerIdentity(gc, configured, initialized)
	p.report["fault_handler_identity"] = checks
	p.report["fault_handlers_identified"] = identified
	if identified {
		// Only SA_ONSTACK is added; handler, mask and other flags stay JSC's.
		for _, s := range []int{int(syscall.SIGSEGV), int(syscall.SIGBUS)} {
			a, _ := sigaction(s)
			a.flags |= 0x08000000
			setDisposition(s, a)
		}
		initialized = p.snapshot("after-narrow-onstack")
	}
	if viaWebKit {
		if !webkitFirst {
			if err := p.webkitInit(); err != nil {
				return err
			}
		}
		// Watch for later installations while the page runs: SIGXCPU's
		// handler arrives from the main loop when WebKit first registers a
		// real-time thread. Nothing found here is patched.
		var timeline []map[string]any
		glib, _ := purego.Dlopen("libglib-2.0.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		var iterate func(uintptr, int32) int32
		addr, _ := purego.Dlsym(glib, "g_main_context_iteration")
		purego.RegisterFunc(&iterate, addr)
		for second := 0; second < 6; second++ {
			for end := time.Now().Add(time.Second); time.Now().Before(end); {
				iterate(0, 0)
				time.Sleep(2 * time.Millisecond)
			}
			ds := p.snapshot(fmt.Sprintf("page-%ds", second+1))
			var rt []string
			for _, t := range threads() {
				if t.Pol == 2 {
					rt = append(rt, fmt.Sprintf("%d %s", t.TID, t.Name))
				}
			}
			x := find(ds, int(syscall.SIGXCPU))
			timeline = append(timeline, map[string]any{"second": second + 1, "sigxcpu": x, "sched_rr_threads": rt})
		}
		limits, _ := os.ReadFile("/proc/self/limits")
		for _, l := range strings.Split(string(limits), "\n") {
			if strings.HasPrefix(l, "Max realtime timeout") || strings.HasPrefix(l, "Max cpu time") {
				p.report[strings.Fields(l)[1]+"_"+strings.Fields(l)[2]+"_limit"] = strings.Join(strings.Fields(l)[3:], " ")
			}
		}
		p.report["page_timeline"] = timeline
		after := p.snapshot("after-webkit")
		p.report["changed_by_webkit"] = changed(initialized, after)
		initialized = after
	}
	p.report["unprotected_native_handlers"] = unprotected(initialized)
	p.report["audit_reserved"] = p.auditReservation(gc, initialized)
	p.report["delivery"] = []map[string]any{deliver(declared, syscall.SIGUSR2), deliver(declared, syscall.SIGHUP), deliver(declared, syscall.SIGUSR1)}
	p.jsc.globalContextRelease(ctx)
	p.report["ready"] = identified
	if !identified {
		p.report["reason"] = "fault handlers not identified; left unmodified"
	}
	if err := p.emit(); err != nil || fault == "" {
		return err
	}
	// After the report: a Go fault on a goroutine. A process that dies here
	// prints nothing more.
	fmt.Println(`{"event":"before-fault","fault":"` + fault + `"}`)
	fmt.Println(`{"event":"fault-result","recovered":` + strconv.Quote(faultAndRecover(fault)) + `}`)
	return nil
}
