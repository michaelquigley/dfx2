//go:build linux && cgo

package main

// Signal records, delivery probes and fault probes. Only reads, except the
// explicit reservation's release (signal.Ignore then SIG_DFL), which the
// probe performs only when asked to reserve a signal for JSC.

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type kernelSigaction struct {
	handler, flags, restorer uintptr
	mask                     uint64
}

func sigaction(sig int) (kernelSigaction, error) {
	var old kernelSigaction
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), 0, uintptr(unsafe.Pointer(&old)), 8, 0, 0)
	if e != 0 {
		return old, e
	}
	return old, nil
}

func setDefault(sig int) error {
	var a kernelSigaction
	_, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&a)), 0, 8, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

var flagNames = []struct {
	bit  uintptr
	name string
}{{0x4, "SA_SIGINFO"}, {0x08000000, "SA_ONSTACK"}, {0x10000000, "SA_RESTART"}, {0x40000000, "SA_NODEFER"}}

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
		b := strings.SplitN(fields[0], "-", 2)
		lo, _ := strconv.ParseUint(b[0], 16, 64)
		hi, _ := strconv.ParseUint(b[1], 16, 64)
		out = append(out, mapping{lo, hi, fields[5]})
	}
	return out
}

var goHandler uintptr

type disposition struct {
	Owner  string   `json:"owner"`
	Offset string   `json:"offset,omitempty"`
	Flags  []string `json:"flags,omitempty"`
}

func dispositionOf(sig int, maps []mapping) disposition {
	a, err := sigaction(sig)
	if err != nil {
		return disposition{Owner: "error: " + err.Error()}
	}
	d := disposition{}
	switch a.handler {
	case 0:
		d.Owner = "default"
	case 1:
		d.Owner = "ignore"
	case goHandler:
		d.Owner = "go"
	default:
		d.Owner = fmt.Sprintf("native:0x%x", a.handler)
		for _, m := range maps {
			if uint64(a.handler) >= m.lo && uint64(a.handler) < m.hi {
				base := m.lo
				for _, o := range maps {
					if o.path == m.path && o.lo < base {
						base = o.lo
					}
				}
				d.Owner = "native:" + m.path[strings.LastIndex(m.path, "/")+1:]
				d.Offset = fmt.Sprintf("0x%x", uint64(a.handler)-base)
			}
		}
	}
	if a.handler > 1 {
		for _, f := range flagNames {
			if a.flags&f.bit != 0 {
				d.Flags = append(d.Flags, f.name)
			}
		}
	}
	return d
}

var watched = map[string]int{"SIGHUP": 1, "SIGBUS": 7, "SIGUSR1": 10, "SIGSEGV": 11, "SIGUSR2": 12, "SIGXCPU": 24}

func dispositions(reserved int) map[string]disposition {
	maps := mappings()
	out := map[string]disposition{}
	for name, sig := range watched {
		out[name] = dispositionOf(sig, maps)
	}
	if reserved != 0 {
		out["reserved "+strconv.Itoa(reserved)] = dispositionOf(reserved, maps)
	}
	return out
}

type threadInfo struct {
	TID     int    `json:"tid"`
	Name    string `json:"name"`
	Blocked string `json:"blocked"` // SigBlk, hex
}

func threads() []threadInfo {
	entries, _ := os.ReadDir("/proc/self/task")
	var out []threadInfo
	for _, e := range entries {
		b, err := os.ReadFile("/proc/self/task/" + e.Name() + "/status")
		if err != nil {
			continue
		}
		t := threadInfo{}
		t.TID, _ = strconv.Atoi(e.Name())
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "Name:\t"); ok {
				t.Name = v
			} else if v, ok := strings.CutPrefix(line, "SigBlk:\t"); ok {
				t.Blocked = v
			}
		}
		out = append(out, t)
	}
	return out
}

// threadSummary groups threads by name and blocked mask.
func threadSummary() map[string]int {
	out := map[string]int{}
	for _, t := range threads() {
		out[t.Name+" blocked="+strings.TrimLeft(t.Blocked, "0")]++
	}
	return out
}

// nativeThread picks a thread not named like the process: Go's threads keep
// the process name, GLib and WebKit name theirs.
func nativeThread() (int, string) {
	comm, _ := os.ReadFile("/proc/self/comm")
	self := strings.TrimSpace(string(comm))
	for _, t := range threads() {
		if t.Name != self {
			return t.TID, t.Name
		}
	}
	return 0, ""
}

var subscribers = map[int]chan os.Signal{}

func subscribe() {
	for _, sig := range []int{1, 10, 12, 24} {
		ch := make(chan os.Signal, 8)
		signal.Notify(ch, syscall.Signal(sig))
		subscribers[sig] = ch
	}
}

// deliver sends sig one way and reports whether its Go subscriber heard it.
// self: kill from a goroutine (the kernel prefers the sending thread);
// external: /usr/bin/kill, a separate process (process-directed);
// native: tgkill to a thread Go did not create.
func deliver(sig int, mode string) map[string]any {
	ch := subscribers[sig]
	for len(ch) > 0 {
		<-ch
	}
	out := map[string]any{"signal": sig, "mode": mode}
	switch mode {
	case "self":
		syscall.Kill(os.Getpid(), syscall.Signal(sig))
	case "external":
		err := exec.Command("/usr/bin/kill", "-s", strconv.Itoa(sig), strconv.Itoa(os.Getpid())).Run()
		if err != nil {
			out["error"] = err.Error()
		}
	case "native":
		tid, name := nativeThread()
		out["target"] = name
		if tid == 0 {
			out["error"] = "no native thread"
			return out
		}
		syscall.Tgkill(os.Getpid(), tid, syscall.Signal(sig))
	}
	select {
	case <-ch:
		out["received_by_go"] = true
	case <-time.After(time.Second):
		out["received_by_go"] = false
	}
	return out
}

var faultSink byte

// faultRecover runs a fault on a fresh goroutine and returns what recover
// saw. bus maps a file page beyond its end, as the purego probe does.
func faultRecover(kind string) string {
	done := make(chan string, 1)
	go func() {
		defer func() {
			r := recover()
			if a, ok := r.(interface{ Addr() uintptr }); ok {
				// A fault-derived panic: the address tells a nil dereference
				// (near 0) from the mapped page beyond end of file.
				done <- fmt.Sprintf("%v (fault address 0x%x)", r, a.Addr())
				return
			}
			done <- fmt.Sprint(r)
		}()
		busFault(kind)
	}()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		return "no recovery within 5s"
	}
}

func busFault(kind string) {
	switch kind {
	case "nil":
		var p *struct{ x int }
		p.x = 1
	case "bus":
		debug.SetPanicOnFault(true)
		f, err := os.CreateTemp("", "dfx2-cgo-sigbus-")
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
		faultSink += b[0]
	}
}

// httpFaults asks the host's own endpoint to fault in its handler: net/http
// recovers a handler panic and drops the connection. Then the endpoint must
// still answer an ordinary request.
func httpFaults(base string) map[string]any {
	out := map[string]any{}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	for _, path := range []string{"/fault/nil", "/fault/bus", "/api/query?x=alive"} {
		resp, err := client.Get(base + path)
		if err != nil {
			out[path] = "error: " + err.Error()
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		out[path] = fmt.Sprintf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return out
}

// lockedAltstack reports a goroutine-locked Go thread's alternate stack.
func lockedAltstack() map[string]any {
	ch := make(chan map[string]any)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		ch <- altstack()
	}()
	return <-ch
}
