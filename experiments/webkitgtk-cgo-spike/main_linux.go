//go:build linux && cgo

package main

// A VM-free GTK/WebKit host built with cgo, for comparison with the purego
// host in ../webkitgtk-spike. One disposable process runs one scripted
// sequence and prints JSON lines; see probe.py.
//
//	before-webkit  Go subscribers, endpoint and progress running; no GTK yet
//	after-load     GTK initialized, a window loaded the endpoint's page
//	after-reopen   that window destroyed, a second loaded
//	after-teardown every window destroyed, main loop still running
//
// At each phase: signal dispositions, per-thread blocked masks, alternate
// stacks, delivery to Go subscribers for every watched signal Go still owns,
// recovered Go faults and handler faults behind the HTTP endpoint.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"dfx2webview2spike/feasibility"
)

func init() { runtime.LockOSThread() } // main stays on the process's first thread: GTK's

var out sync.Mutex

func emit(event string, kv map[string]any) {
	kv["event"] = event
	kv["t"] = time.Now().Format("15:04:05.000")
	b, _ := json.Marshal(kv)
	out.Lock()
	fmt.Println(string(b))
	out.Unlock()
}

type callbackRecord struct {
	Callback string `json:"callback"`
	TID      int    `json:"tid"`
	Main     bool   `json:"main_thread"`
}

func main() {
	stray := flag.String("stray", "", "hazard: after the page loads, send this signal number by -stray-mode and report survival")
	strayMode := flag.String("stray-mode", "external", "self, external or native")
	vm := flag.Bool("vm-control", false, "positive control: create a JSC context in this process after the page loads")
	gcEnv := flag.Int("gc-env", 0, "explicit reservation through JSC_SIGNAL_FOR_GC (undocumented; named in WTF's warning)")
	gcPrivate := flag.Int("gc-private", 0, "explicit reservation through the private JSConfigureSignalForGC")
	faults := flag.Bool("faults", true, "run the fault probes at each phase")
	flag.Parse()

	urg, _ := sigaction(int(syscall.SIGURG))
	goHandler = urg.handler
	pid := os.Getpid()
	subscribe()
	reserved := *gcEnv + *gcPrivate
	if reserved != 0 {
		// The application's explicit reservation: not one of its own
		// subscriptions (above), released from Go, then given to JSC.
		if _, mine := subscribers[reserved]; mine || reserved < 35 || reserved > 64 {
			emit("refused", map[string]any{"reserved": reserved})
			os.Exit(2)
		}
		signal.Ignore(syscall.Signal(reserved))
		setDefault(reserved)
		if *gcEnv != 0 {
			os.Setenv("JSC_SIGNAL_FOR_GC", strconv.Itoa(*gcEnv)) // reaches C getenv through cgo's setenv hook
		}
	}

	var progress atomic.Int64
	go func() {
		for {
			progress.Add(1)
			time.Sleep(10 * time.Millisecond)
		}
	}()

	page := feasibility.NewPage()
	mux := http.NewServeMux()
	mux.HandleFunc("/fault/", func(w http.ResponseWriter, r *http.Request) {
		busFault(r.URL.Path[len("/fault/"):]) // net/http recovers the panic
		w.Write([]byte("no fault\n"))
	})
	mux.Handle("/", page)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	base := "http://" + ln.Addr().String()
	server := &http.Server{Handler: mux, ErrorLog: nil}
	go server.Serve(ln)

	var cbMu sync.Mutex
	var callbacks []callbackRecord
	record := func(name string) {
		tid := gettid()
		cbMu.Lock()
		callbacks = append(callbacks, callbackRecord{name, tid, tid == pid})
		cbMu.Unlock()
	}

	phase := func(name string) map[string]any {
		r := map[string]any{"phase": name, "progress": progress.Load(), "dispositions": dispositions(reserved),
			"threads": threadSummary(), "altstack_go_locked": lockedAltstack()}
		var deliveries []map[string]any
		d := r["dispositions"].(map[string]disposition)
		for sname, sig := range map[string]int{"SIGHUP": 1, "SIGUSR1": 10, "SIGUSR2": 12, "SIGXCPU": 24} {
			if d[sname].Owner != "go" {
				deliveries = append(deliveries, map[string]any{"signal": sig, "skipped": "owner " + d[sname].Owner})
				continue
			}
			for _, mode := range []string{"self", "external", "native"} {
				deliveries = append(deliveries, deliver(sig, mode))
			}
		}
		r["deliveries"] = deliveries
		if *faults {
			r["go_faults"] = map[string]string{"nil": faultRecover("nil"), "bus": faultRecover("bus")}
			r["http_faults"] = httpFaults(base)
		}
		r["progress_after"] = progress.Load()
		return r
	}

	// What the dynamic linker loaded before any GTK or WebKit call.
	var loaded []string
	seen := map[string]bool{}
	for _, m := range mappings() {
		if (strings.Contains(m.path, "webkit") || strings.Contains(m.path, "javascriptcore") || strings.Contains(m.path, "libgtk-3")) && !seen[m.path] {
			seen[m.path] = true
			loaded = append(loaded, m.path)
		}
	}
	emit("start", map[string]any{"pid": pid, "go": runtime.Version(), "reserved": reserved, "loaded_before_gtk": loaded})
	before := phase("before-webkit")
	before["altstack_main"] = altstack()
	emit("phase", before)

	if *gcPrivate != 0 {
		emit("private-gc-signal", map[string]any{"signal": *gcPrivate, "configured": privateGCSignal(*gcPrivate)})
	}
	if !gtkInit() {
		emit("error", map[string]any{"error": "GTK could not open the display"})
		os.Exit(1)
	}
	emit("versions", map[string]any{"runtime": versions()})

	loads := make(chan [2]int, 16)
	native.onLoad = func(id, event int) {
		record("load-changed")
		loads <- [2]int{id, event}
	}
	onMain := func(f func()) {
		done := make(chan struct{})
		post(func() { record("idle"); f(); close(done) })
		<-done
	}
	waitLoaded := func(id int) error {
		deadline := time.After(30 * time.Second)
		for {
			select {
			case l := <-loads:
				if l[0] == id && l[1] == 3 {
					return nil
				}
			case <-deadline:
				return fmt.Errorf("window %d did not finish loading", id)
			}
		}
	}

	go func() {
		defer onMain(gtkQuit)
		onMain(func() { openWindow(1, base+"/", "dfx2 cgo probe 1") })
		if err := waitLoaded(1); err != nil {
			emit("error", map[string]any{"error": err.Error()})
			return
		}
		time.Sleep(1500 * time.Millisecond) // WebKit installs some handlers from main-loop callbacks
		if *vm {
			var result int
			onMain(func() { result = vmControl() })
			emit("vm-control", map[string]any{"result": result})
		}
		p := phase("after-load")
		onMain(func() { p["altstack_main"] = altstack() })
		emit("phase", p)
		if *stray != "" {
			sig, _ := strconv.Atoi(*stray)
			emit("stray-sending", map[string]any{"signal": sig, "mode": *strayMode, "disposition": dispositions(reserved)})
			r := deliver(sig, *strayMode)
			time.Sleep(time.Second)
			emit("stray-survived", map[string]any{"delivery": r, "progress": progress.Load(),
				"endpoint": httpFaults(base)["/api/query?x=alive"]})
			return
		}
		onMain(func() { closeWindow(1) })
		onMain(func() { openWindow(2, base+"/", "dfx2 cgo probe 2") })
		if err := waitLoaded(2); err != nil {
			emit("error", map[string]any{"error": err.Error()})
			return
		}
		time.Sleep(time.Second)
		emit("phase", phase("after-reopen"))
		onMain(func() { closeWindow(2) })
		time.Sleep(time.Second)
		emit("phase", phase("after-teardown"))
	}()
	gtkMain()
	for id := range native.windows {
		closeWindow(id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	server.Shutdown(ctx)
	cancel()
	cbMu.Lock()
	summary := map[string]int{}
	for _, c := range callbacks {
		summary[fmt.Sprintf("%s main_thread=%v", c.Callback, c.Main)]++
	}
	cbMu.Unlock()
	emit("exit", map[string]any{"callbacks": summary, "handler": page.Counts(), "progress": progress.Load(),
		"dispositions": dispositions(reserved)})
}
