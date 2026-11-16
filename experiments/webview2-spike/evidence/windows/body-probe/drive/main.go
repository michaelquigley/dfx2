//go:build windows

// Command drive runs the request-body threading probe against the spike's
// feasibility host (-feas-body) and records what each incoming stream is,
// where it was read, whether the bytes survived, and how a held read and a
// close interact. See ../README.md.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	exe     = flag.String("exe", "", "the spike executable (dfx2-webview2-spike.exe)")
	out     = flag.String("out", "evidence/windows/body-probe/run", "directory for logs and results")
	root    = flag.String("root", "", `profiles' root (default %TEMP%\dfx2-feasibility)`)
	only    = flag.String("only", "", "comma-separated scenario names (default: all)")
	timeout = flag.Duration("step-timeout", 40*time.Second, "hang guard for each expected event")
)

// ------------------------------------------------------------------ events

type event map[string]any

func (e event) str(k string) string {
	v, _ := e[k].(string)
	return v
}

func (e event) num(k string) float64 {
	v, _ := e[k].(float64)
	return v
}

func (e event) boolean(k string) bool {
	v, _ := e[k].(bool)
	return v
}

func (e event) sub(k string) event {
	m, _ := e[k].(map[string]any)
	return event(m)
}

func (e event) list(k string) []any {
	l, _ := e[k].([]any)
	return l
}

type failure struct{ msg string }

func (f failure) Error() string { return f.msg }

func fail(format string, args ...any) { panic(failure{fmt.Sprintf(format, args...)}) }

// ------------------------------------------------------------------ host

type host struct {
	out   string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	log   *os.File
	mu    sync.Mutex
	cond  *sync.Cond
	all   []event
	done  chan struct{}
	exit  int
	ready event
	last  int

	origins     map[string]string
	browserPIDs map[uint32]bool
}

func startHost(outDir string, flags ...string) *host {
	h := &host{out: outDir, done: make(chan struct{}), origins: map[string]string{}, browserPIDs: map[uint32]bool{}}
	h.cond = sync.NewCond(&h.mu)
	args := append([]string{"-feasibility", "-feas-root", *root}, flags...)
	h.cmd = exec.Command(*exe, args...)
	h.cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	stderr, err := os.Create(filepath.Join(outDir, "host.stderr"))
	if err != nil {
		panic(err)
	}
	h.cmd.Stderr = stderr
	h.stdin, err = h.cmd.StdinPipe()
	if err != nil {
		panic(err)
	}
	h.log, err = os.Create(filepath.Join(outDir, "host.jsonl"))
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(h.log, "# %s %s\n", filepath.Base(*exe), strings.Join(args, " "))
	if err := h.cmd.Start(); err != nil {
		panic(err)
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(h.log, line)
			var e event
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			h.mu.Lock()
			h.all = append(h.all, e)
			switch e.str("event") {
			case "opened":
				h.origins[e.str("window")] = e.str("origin")
			case "controller":
				h.browserPIDs[uint32(e.num("browser_pid"))] = true
			}
			h.cond.Broadcast()
			h.mu.Unlock()
		}
		err := h.cmd.Wait()
		stderr.Close()
		h.mu.Lock()
		h.exit = 0
		if ee, ok := err.(*exec.ExitError); ok {
			h.exit = ee.ExitCode()
		}
		h.mu.Unlock()
		close(h.done)
		h.mu.Lock()
		h.cond.Broadcast()
		h.mu.Unlock()
	}()
	h.ready = h.expect("ready", 0, 60*time.Second)
	return h
}

// send writes a command and returns the event mark before it.
func (h *host) send(line string) int {
	h.mu.Lock()
	mark := len(h.all)
	h.mu.Unlock()
	fmt.Fprintln(h.stdin, line)
	return mark
}

// expect returns the first event named name at or after since whose fields
// match; nothing is consumed.
func (h *host) expect(name string, since int, to time.Duration, kv ...any) event {
	deadline := time.Now().Add(to)
	timer := time.AfterFunc(to, func() { h.mu.Lock(); h.cond.Broadcast(); h.mu.Unlock() })
	defer timer.Stop()
	h.mu.Lock()
	defer h.mu.Unlock()
	i := since
	for {
		for ; i < len(h.all); i++ {
			if e := h.all[i]; e.str("event") == name && matches(e, kv) {
				h.last = i + 1
				return e
			}
		}
		select {
		case <-h.done:
			fail("host exited while waiting for %s %v", name, kv)
		default:
		}
		if time.Now().After(deadline) {
			fail("timed out waiting for %s %v", name, kv)
		}
		h.cond.Wait()
	}
}

// maybe returns the first matching event at or after since, waiting up to
// to, or nil.
func (h *host) maybe(name string, since int, to time.Duration, kv ...any) (e event) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(failure); !ok {
				panic(p)
			}
			e = nil
		}
	}()
	return h.expect(name, since, to, kv...)
}

func matches(e event, kv []any) bool {
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		switch v := kv[i+1].(type) {
		case string:
			if e.str(k) != v {
				return false
			}
		case bool:
			if e.boolean(k) != v {
				return false
			}
		case int:
			if int(e.num(k)) != v {
				return false
			}
		}
	}
	return true
}

func (h *host) events(name string, since int, kv ...any) []event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []event
	for _, e := range h.all[since:] {
		if e.str("event") == name && matches(e, kv) {
			out = append(out, e)
		}
	}
	return out
}

func (h *host) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.all)
}

func (h *host) open(label, mode string) int {
	m := h.send("open " + label + " " + mode)
	h.expect("opened", m, *timeout, "window", label)
	h.expect("load", m, *timeout, "window", label, "success", true)
	return m
}

// page loads the probe page with ?run=...&id=rid and waits for its report.
func (h *host) page(label, query, rid string) event {
	m := h.send("load " + label + " " + h.origins[label] + "/?" + query + "&id=" + rid)
	deadline := time.Now().Add(*timeout)
	for {
		e := h.expect("report", m, time.Until(deadline), "window", label)
		r := e.sub("report")
		if r.str("id") == rid {
			if r.str("error") != "" {
				fail("report %s: %s", rid, r.str("error"))
			}
			return r
		}
		m = h.last
	}
}

func (h *host) status() event {
	return h.expect("status", h.send("status"), *timeout)
}

func (h *host) window(label string) event {
	return h.status().sub("windows").sub(label)
}

func (h *host) close(label string) event {
	return h.expect("closed", h.send("close "+label), 60*time.Second, "window", label)
}

type exitInfo struct {
	HostExit         int      `json:"host_exit"`
	TimedOut         bool     `json:"timed_out"`
	BrowserPIDs      []uint32 `json:"browser_pids"`
	BrowserRemaining []uint32 `json:"browser_processes_remaining"`
	StderrSuspicious []string `json:"stderr_suspicious"`
	CallbacksLive    int      `json:"callback_objects_live"`
	OffUIThread      int      `json:"off_ui_thread"`
	BodyWorkersLive  int      `json:"body_workers_live"`
	GateStreamsLive  int      `json:"gate_streams_live"`
	ProfilesLeft     []string `json:"profiles_remaining"`
}

// finish quits the host, waits for it, and records what was left behind.
func (h *host) finish(to time.Duration) exitInfo {
	var info exitInfo
	select {
	case <-h.done:
	default:
		h.send("quit")
	}
	select {
	case <-h.done:
	case <-time.After(to):
		info.TimedOut = true
		h.cmd.Process.Kill()
		<-h.done
	}
	h.stdin.Close()
	h.log.Close()
	h.mu.Lock()
	info.HostExit = h.exit
	for pid := range h.browserPIDs {
		info.BrowserPIDs = append(info.BrowserPIDs, pid)
	}
	h.mu.Unlock()
	sort.Slice(info.BrowserPIDs, func(i, j int) bool { return info.BrowserPIDs[i] < info.BrowserPIDs[j] })
	deadline := time.Now().Add(5 * time.Second)
	for {
		info.BrowserRemaining = nil
		for _, pid := range info.BrowserPIDs {
			if processAlive(pid) {
				info.BrowserRemaining = append(info.BrowserRemaining, pid)
			}
		}
		if len(info.BrowserRemaining) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ex := h.events("exit", 0); len(ex) > 0 {
		e := ex[len(ex)-1]
		info.CallbacksLive, info.OffUIThread = int(e.num("callback_objects_live")), int(e.num("off_ui_thread"))
		info.BodyWorkersLive, info.GateStreamsLive = int(e.num("body_workers_live")), int(e.num("gate_streams_live"))
	}
	if b, err := os.ReadFile(filepath.Join(h.out, "host.stderr")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, "panic") || strings.Contains(l, "error:") || strings.Contains(l, "fatal") {
				info.StderrSuspicious = append(info.StderrSuspicious, strings.TrimSpace(l))
			}
		}
	}
	pid := strconv.Itoa(h.cmd.Process.Pid)
	if entries, err := os.ReadDir(*root); err == nil {
		for _, en := range entries {
			if strings.HasPrefix(en.Name(), "profile-"+pid+"-") {
				info.ProfilesLeft = append(info.ProfilesLeft, en.Name())
			}
		}
	}
	return info
}

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	pOpenProcess          = kernel32.NewProc("OpenProcess")
	pGetExitCodeProcess   = kernel32.NewProc("GetExitCodeProcess")
	pCloseHandle          = kernel32.NewProc("CloseHandle")
	pGetDpiForSystem      = syscall.NewLazyDLL("user32.dll").NewProc("GetDpiForSystem")
	pGetSystemMetrics     = syscall.NewLazyDLL("user32.dll").NewProc("GetSystemMetrics")
	pSetProcessDpiContext = syscall.NewLazyDLL("user32.dll").NewProc("SetProcessDpiAwarenessContext")
)

func processAlive(pid uint32) bool {
	h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer pCloseHandle.Call(h)
	var code uint32
	pGetExitCodeProcess.Call(h, uintptr(unsafe.Pointer(&code)))
	return code == 259
}

// ------------------------------------------------------------------ results

type check struct {
	Check  string         `json:"check"`
	OK     bool           `json:"ok"`
	Detail map[string]any `json:"detail,omitempty"`
}

type result struct {
	Scenario string         `json:"scenario"`
	Flags    []string       `json:"flags"`
	Checks   []check        `json:"checks"`
	OK       bool           `json:"ok"`
	Failure  string         `json:"failure,omitempty"`
	Detail   map[string]any `json:"detail,omitempty"`
	Exit     *exitInfo      `json:"exit,omitempty"`
	Seconds  float64        `json:"seconds"`
	Runtime  string         `json:"runtime"`
}

func (r *result) check(name string, ok bool, kv ...any) bool {
	c := check{Check: name, OK: ok, Detail: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		c.Detail[kv[i].(string)] = kv[i+1]
	}
	r.Checks = append(r.Checks, c)
	return ok
}

// note records an observation that is not judged.
func (r *result) note(name string, kv ...any) { r.check(name, true, kv...) }

type run struct{ results []*result }

func wanted(name string) bool {
	if *only == "" {
		return true
	}
	for _, s := range strings.Split(*only, ",") {
		if s == name {
			return true
		}
	}
	return false
}

func (ru *run) scenario(name string, flags []string, body func(h *host, r *result)) {
	if !wanted(name) {
		return
	}
	dir := filepath.Join(*out, name)
	os.MkdirAll(dir, 0o755)
	r := &result{Scenario: name, Flags: flags, Checks: []check{}, Detail: map[string]any{}}
	started := time.Now()
	var h *host
	func() {
		defer func() {
			if p := recover(); p != nil {
				if f, ok := p.(failure); ok {
					r.Failure = f.msg
				} else {
					r.Failure = fmt.Sprint("panic: ", p)
				}
			}
		}()
		h = startHost(dir, flags...)
		r.Runtime = h.ready.str("runtime")
		body(h, r)
	}()
	if h != nil {
		info := h.finish(60 * time.Second)
		r.Exit = &info
	}
	r.Seconds = float64(time.Since(started).Round(100*time.Millisecond)) / float64(time.Second)
	clean := r.Exit != nil && !r.Exit.TimedOut && len(r.Exit.BrowserRemaining) == 0 && r.Exit.CallbacksLive == 0 && r.Exit.OffUIThread == 0 &&
		r.Exit.BodyWorkersLive == 0 && r.Exit.GateStreamsLive == 0 && r.Exit.HostExit == 0 && len(r.Exit.StderrSuspicious) == 0 && len(r.Exit.ProfilesLeft) == 0
	r.OK = r.Failure == "" && clean
	for _, c := range r.Checks {
		r.OK = r.OK && c.OK
	}
	b, _ := json.MarshalIndent(r, "", " ")
	os.WriteFile(filepath.Join(dir, "result.json"), append(b, '\n'), 0o644)
	ru.results = append(ru.results, r)
	var failed []string
	for _, c := range r.Checks {
		if !c.OK {
			failed = append(failed, c.Check)
		}
	}
	line, _ := json.Marshal(map[string]any{"scenario": name, "ok": r.OK, "failure": r.Failure, "failed_checks": failed, "exit": r.Exit, "seconds": r.Seconds})
	fmt.Println(string(line))
}

// ------------------------------------------------------------------ scenarios

const (
	smallBody = 65536
	largeBody = 900 * 1024 // the integration matrix's upload size
)

type bodyCase struct {
	kind  string
	size  int
	stall int
}

var allKinds = []bodyCase{{"string", smallBody, 0}, {"json", smallBody, 0}, {"form", smallBody, 0}, {"blob", smallBody, 0},
	{"multipart", smallBody, 0}, {"string", largeBody, 0}, {"blob", largeBody, 0}, {"multipart", largeBody, 0}, {"stream", smallBody, 0}}

// bodies sends each kind of request body from the page and compares the
// page's digest of what it sent with what the handler received and what the
// host's read saw, then judges where the read ran by the mode.
func bodies(mode string, cases []bodyCase) func(h *host, r *result) {
	return func(h *host, r *result) {
		h.open("A", "inprocess")
		table := []map[string]any{}
		for i, c := range cases {
			name := fmt.Sprintf("%s/%d", c.kind, c.size)
			if c.stall > 0 {
				name += fmt.Sprintf("/stall%d", c.stall)
			}
			rid := fmt.Sprintf("%s-%d-%d", c.kind, c.size, i)
			m := h.mark()
			rep := h.page("A", fmt.Sprintf("run=body&kind=%s&size=%d&stall=%d", c.kind, c.size, c.stall), rid).sub("result")
			var stream, read event
			for _, e := range h.events("body-stream", m, "kind", c.kind) {
				stream = e
			}
			for _, e := range h.events("body-read", m, "kind", c.kind) {
				read = e
			}
			stall := h.events("ui-stall", m, "kind", c.kind)
			row := map[string]any{"case": name, "page": rep, "stream": stream, "read": read}
			if len(stall) > 0 {
				row["stall"] = stall[len(stall)-1]
			}
			table = append(table, row)
			if rep.str("error") != "" {
				// The page could not send it: a fact about the transport, not
				// a failed read. The stream kind is where this is expected.
				r.check(name+": the page could send the body", c.kind == "stream", "error", rep.str("error"), "stream_event", stream != nil, "read_event", read != nil)
				continue
			}
			echo := rep.sub("echo")
			if c.kind == "stream" && stream == nil {
				// A streaming body the browser sent without any content stream
				// in the intercepted request: recorded, not judged here.
				r.note(name+": the intercepted request carried no content stream for a ReadableStream body", "status", rep.num("status"), "handler_length", echo.num("length"),
					"expected_length", rep.num("expectedLength"), "page_ms", rep.num("ms"))
				continue
			}
			got := echo.str("sha256")
			lengthOK := int(echo.num("length")) == int(rep.num("expectedLength"))
			if c.kind == "multipart" {
				lengthOK = true
				got = ""
				for _, p := range echo.list("parts") {
					if pm := event(p.(map[string]any)); pm.str("filename") != "" {
						got = pm.str("sha256")
						lengthOK = int(pm.num("length")) == int(rep.num("expectedLength"))
					}
				}
			}
			r.check(name+": exact bytes reached the handler", int(rep.num("status")) == 200 && got == rep.str("expectedSha") && lengthOK,
				"status", rep.num("status"), "expected_sha256", rep.str("expectedSha"), "handler_sha256", got, "handler_length", echo.num("length"), "expected_length", rep.num("expectedLength"))
			r.check(name+": the host's read is the same body the handler received", read != nil && read.str("sha256") == echo.str("sha256") && int(read.num("bytes")) == int(echo.num("length")) && read.str("error") == "",
				"read_sha256", read.str("sha256"), "read_bytes", read.num("bytes"), "read_error", read.str("error"))
			r.check(name+": stream facts recorded on the UI thread before the read", stream != nil && stream.sub("facts").str("agile") != "" && stream.sub("facts").num("position") == 0, "facts", stream["facts"])
			switch mode {
			case "inspect":
				r.check(name+": read on the UI thread (production path)", read != nil && !read.boolean("off_ui") && read.str("branch") == "ui", "reader_thread", read.num("reader_thread"), "ui_thread", read.num("ui_thread"))
			case "worker":
				agile := stream.sub("facts").str("agile") == "S_OK"
				r.check(name+": branch follows the stream's advertised agility", read != nil && ((agile && read.str("branch") == "agile") || (!agile && read.str("branch") == "marshal")),
					"agile", stream.sub("facts").str("agile"), "branch", read.str("branch"))
				r.check(name+": read on a separate multithreaded-apartment thread", read != nil && read.boolean("off_ui") && (read.str("coinit") == "S_OK"),
					"reader_thread", read.num("reader_thread"), "ui_thread", read.num("ui_thread"), "coinit", read.str("coinit"))
				if read.str("branch") == "marshal" {
					r.note(name+": marshal branch detail", "unmarshal", read.str("unmarshal"), "same_pointer", read.boolean("same_pointer"))
				}
			}
			if c.stall > 0 {
				r.check(name+": the read completed while the UI thread was blocked without pumping", read != nil && read.boolean("read_done_during_stall"),
					"stall_ms", read.num("stall_ms"), "total_read_ms", read.num("total_read_ms"), "took_ms", read.num("took_ms"))
			}
			r.note(name+": read timing", "chunks", read.num("chunks"), "first_read_ms", read.num("first_read_ms"), "total_read_ms", read.num("total_read_ms"),
				"max_read_gap_ms", read.num("max_read_gap_ms"), "ticks_during", read.num("ticks_during"), "took_ms", read.num("took_ms"), "stat_size", stream.sub("facts").num("stat_size"))
		}
		r.Detail["bodies"] = table
		h.close("A")
	}
}

// gate: a modeled agile stream whose read is held. The UI thread, another
// window and the host's commands keep working; a close settles nothing
// twice and the window's release waits for the read.
func gate(h *host, r *result) {
	h.open("A", "inprocess")
	h.open("B", "inprocess")
	m := h.send("gate-read A 1048576")
	started := h.expect("gate-read-started", m, *timeout, "window", "A")
	worker := h.expect("body-worker", m, *timeout, "window", "A")
	r.check("the modeled stream advertises IAgileObject", started.sub("facts").str("agile") == "S_OK", "facts", started["facts"])
	r.check("the worker is a multithreaded-apartment thread, not the UI thread", worker.str("coinit") == "S_OK" && worker.num("thread") != started.num("ui_thread"), "worker", worker)
	time.Sleep(500 * time.Millisecond)
	pong := h.expect("pong", h.send("ping"), *timeout)
	r.check("the UI thread answers while the read is held", pong.num("body_workers") == 1, "pong", pong)
	rep := h.page("B", "run=checks", "b-checks")
	r.check("another window's page loads and its requests complete while the read is held", rep.sub("result").num("echoLength") == 1005, "report", rep)
	st := h.status()
	r.check("the read is in flight for A alone", st.sub("windows").sub("A").num("reads_in_flight") == 1 && st.sub("windows").sub("B").num("reads_in_flight") == 0 && st.num("body_workers") == 1, "status", st)
	ticksBefore := st.num("ticks")
	time.Sleep(300 * time.Millisecond)
	st2 := h.status()
	r.check("the UI timer keeps ticking while the read is held", st2.num("ticks") > ticksBefore, "before", ticksBefore, "after", st2.num("ticks"))

	cm := h.send("close A")
	closing := h.expect("closing", cm, *timeout, "window", "A")
	retained := h.expect("read-retained-at-close", cm, *timeout, "window", "A")
	waiting := h.expect("release-waiting-reads", cm, 20*time.Second, "window", "A")
	r.check("close found the read in flight and retained it", closing.num("reads_in_flight") == 1 && !retained.boolean("read_done"), "closing", closing, "retained", retained)
	r.check("the window's release waits for the read", waiting != nil && len(h.events("closed", cm, "window", "A")) == 0, "waiting", waiting)
	time.Sleep(1500 * time.Millisecond)
	r.check("still not released 1.5 s later, with the browser process already gone", len(h.events("closed", cm, "window", "A")) == 0 && len(h.events("browser-process-exited", cm, "window", "A")) == 1,
		"closed_events", len(h.events("closed", cm, "window", "A")), "exits", len(h.events("browser-process-exited", cm, "window", "A")))
	pong2 := h.expect("pong", h.send("ping"), *timeout)
	r.check("the UI thread still answers", pong2.num("body_workers") == 1, "pong", pong2)

	rm := h.send("gate-release")
	read := h.expect("body-read-after-close", rm, *timeout, "window", "A")
	released := h.expect("gate-stream-released", rm, *timeout, "window", "A")
	closed := h.expect("closed", rm, 30*time.Second, "window", "A")
	threads := read.list("read_callback_threads")
	onWorker := len(threads) > 0
	for _, t := range threads {
		onWorker = onWorker && t.(float64) == worker.num("thread")
	}
	r.check("Read ran on the worker thread with no proxy in between", onWorker && read.boolean("off_ui"), "read_callback_threads", threads, "worker_thread", worker.num("thread"))
	r.check("the read returned the whole body after the gate opened", int(read.num("bytes")) == 1048576 && read.str("error") == "" && read.boolean("closed_early"), "read", read)
	r.check("the stream was released exactly once, by the worker", released.num("final_releases") == 1 && released.num("thread") == worker.num("thread") && len(h.events("gate-stream-released", 0)) == 1, "released", released)
	r.check("the window closed once, after the read", len(h.events("closed", 0, "window", "A")) == 1 && closed.num("pending_requests") == 0, "closed", closed)
	r.note("timing", "after_close_ms", read.num("after_close_ms"), "since_close", closed.str("since_close"))
	h.close("B")
}

// closeDuringUpload closes a window while the page is sending the large
// body. Where the close lands relative to the read is up to timing; what
// is checked is that every read in flight at close is settled once and the
// window releases once, after it.
func closeDuringUpload(h *host, r *result) {
	delays := []time.Duration{0, 20 * time.Millisecond, 60 * time.Millisecond, 150 * time.Millisecond}
	outcomes := []map[string]any{}
	hit := false
	for i, d := range delays {
		label := fmt.Sprintf("U%d", i)
		h.open(label, "inprocess")
		m := h.send(fmt.Sprintf("load %s %s/?run=body&kind=blob&size=%d&stall=0&id=u%d", label, h.origins[label], largeBody, i))
		time.Sleep(d)
		cm := h.send("close " + label)
		closed := h.expect("closed", cm, 60*time.Second, "window", label)
		closing := h.events("closing", cm, "window", label)[0]
		streams := h.events("body-stream", m, "window", label, "kind", "blob")
		before := h.events("body-read", m, "window", label, "kind", "blob")
		after := h.events("body-read-after-close", m, "window", label)
		retained := h.events("read-retained-at-close", m, "window", label)
		o := map[string]any{"delay_ms": d.Milliseconds(), "body_stream_seen": len(streams), "reads_done_before_close": len(before), "reads_in_flight_at_close": closing.num("reads_in_flight"),
			"retained": retained, "reads_after_close": after, "closed": closed}
		outcomes = append(outcomes, o)
		if closing.num("reads_in_flight") > 0 {
			hit = true
			r.check(fmt.Sprintf("delay %v: the read in flight at close was settled once and returned after close", d), len(retained) == 1 && len(after) == 1 && int(after[0].num("settled")) == 1 && after[0].boolean("closed_early"), "outcome", o)
		}
		r.check(fmt.Sprintf("delay %v: the window closed once with nothing pending", d), len(h.events("closed", 0, "window", label)) == 1 && closed.num("pending_requests") == 0, "closed", closed)
	}
	r.Detail["outcomes"] = outcomes
	r.note("a close landed during an in-flight read", "hit", hit)
}

// ------------------------------------------------------------------ main

func environment() map[string]any {
	env := map[string]any{"time": time.Now().Format(time.RFC3339), "exe": *exe, "root": *root, "go": strings.TrimSpace(output("go", "version"))}
	if b, err := os.ReadFile(*exe); err == nil {
		s := sha256.Sum256(b)
		env["exe_sha256"] = hex.EncodeToString(s[:])
	}
	env["revision"] = strings.TrimSpace(output("git", "rev-parse", "HEAD"))
	env["dirty"] = strings.Split(strings.TrimSpace(output("git", "status", "--short")), "\n")
	env["os"] = strings.TrimSpace(output("cmd", "/c", "ver"))
	env["session"] = os.Getenv("SESSIONNAME")
	env["session_state"] = sessionState()
	dpi, _, _ := pGetDpiForSystem.Call()
	cx, _, _ := pGetSystemMetrics.Call(0)
	cy, _, _ := pGetSystemMetrics.Call(1)
	env["system_dpi"], env["screen"] = dpi, fmt.Sprintf("%dx%d", cx, cy)
	return env
}

func sessionState() string {
	for _, line := range strings.Split(output("query", "session"), "\n") {
		if strings.HasPrefix(line, ">") {
			f := strings.Fields(line)
			if len(f) >= 3 {
				return f[len(f)-1]
			}
		}
	}
	return "unknown"
}

func output(name string, args ...string) string {
	b, _ := exec.Command(name, args...).CombinedOutput()
	return string(b)
}

func main() {
	flag.Parse()
	pSetProcessDpiContext.Call(^uintptr(3))
	if *exe == "" {
		fmt.Fprintln(os.Stderr, "-exe is required: the built dfx2-webview2-spike.exe")
		os.Exit(2)
	}
	if *root == "" {
		*root = filepath.Join(os.TempDir(), "dfx2-feasibility")
	}
	if abs, err := filepath.Abs(*exe); err == nil {
		*exe = abs
	}
	os.MkdirAll(*out, 0o755)
	os.MkdirAll(*root, 0o755)
	envb, _ := json.MarshalIndent(environment(), "", " ")
	os.WriteFile(filepath.Join(*out, "environment.json"), append(envb, '\n'), 0o644)
	ru := &run{}
	ru.scenario("inspect", []string{"-feas-body", "inspect"}, bodies("inspect", allKinds))
	ru.scenario("worker", []string{"-feas-body", "worker"}, bodies("worker", allKinds))
	ru.scenario("worker-stall", []string{"-feas-body", "worker"}, bodies("worker", []bodyCase{{"blob", largeBody, 1500}, {"string", largeBody, 1500}, {"multipart", smallBody, 1500}}))
	ru.scenario("gate", []string{"-feas-body", "worker"}, gate)
	ru.scenario("close-during-upload", []string{"-feas-body", "worker"}, closeDuringUpload)
	passed := true
	var failed []string
	for _, r := range ru.results {
		if !r.OK {
			passed = false
			failed = append(failed, r.Scenario)
		}
	}
	b, _ := json.MarshalIndent(map[string]any{"passed": passed, "scenarios": ru.results}, "", " ")
	os.WriteFile(filepath.Join(*out, "summary.json"), append(b, '\n'), 0o644)
	line, _ := json.Marshal(map[string]any{"passed": passed, "failed": failed})
	fmt.Println(string(line))
	if !passed {
		os.Exit(1)
	}
}
