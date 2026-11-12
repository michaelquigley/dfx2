//go:build windows

// Command drive runs passes B-D of the integration feasibility spike against
// the Windows host (dfx2-webview2-spike.exe -feasibility). Each scenario
// starts its own host process and drives it through console commands; a
// controlled collector and a stoppable attachment server run here, on
// 127.0.0.1, so their lifetimes and counters are independent of the browser.
// Judgments use server-side counters and native events, never only what a
// page displays. Timeouts are hang guards and count as failures. The endpoint
// credential never appears here: external checks use none or a wrong one, and
// the right one is exercised only by the host's own selftest.
//
//	go build -o feas-drive.exe ./evidence/windows/integration-feasibility/drive
//	.\feas-drive.exe -exe .\dfx2-webview2-spike.exe -out evidence\windows\integration-feasibility\run
//	.\feas-drive.exe -exe .\dfx2-webview2-spike.exe -out evidence\windows\integration-feasibility\run\manual -manual
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
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

const credential = "dfx2-endpoint"

var (
	exe     = flag.String("exe", "dfx2-webview2-spike.exe", "the spike executable")
	out     = flag.String("out", "evidence/windows/integration-feasibility/run", "directory for logs, results and captures")
	root    = flag.String("root", "", `experiment-owned directory for the hosts' profiles (default %TEMP%\dfx2-feasibility)`)
	static  = flag.String("static", "feasibility/static", "the probe page's files, served by the attachment server")
	only    = flag.String("only", "", "comma-separated scenario names (default: all)")
	manual  = flag.Bool("manual", false, "run Michael's two-step manual check instead of the scenarios")
	timeout = flag.Duration("step-timeout", 30*time.Second, "hang guard for each expected event")
)

type event map[string]any

func (e event) str(k string) string {
	s, _ := e[k].(string)
	return s
}

func (e event) num(k string) float64 {
	f, _ := e[k].(float64)
	return f
}

func (e event) boolean(k string) bool {
	b, _ := e[k].(bool)
	return b
}

func (e event) sub(k string) event {
	m, _ := e[k].(map[string]any)
	return event(m)
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
	hwnds       map[string]uintptr
	browserPIDs map[uint32]bool
}

func startHost(outDir string, flags ...string) *host {
	h := &host{out: outDir, done: make(chan struct{}), origins: map[string]string{}, hwnds: map[string]uintptr{}, browserPIDs: map[uint32]bool{}}
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
				if hw, err := strconv.ParseUint(strings.TrimPrefix(e.str("hwnd"), "0x"), 16, 64); err == nil {
					h.hwnds[e.str("window")] = uintptr(hw)
				}
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
// match; nothing is consumed. kv alternates field names and values.
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

func (h *host) open(label, mode, u string, loaded bool) int {
	m := h.send(strings.TrimSpace("open " + label + " " + mode + " " + u))
	h.expect("opened", m, *timeout, "window", label)
	if loaded {
		h.expect("load", m, *timeout, "window", label, "success", true)
	}
	return m
}

func (h *host) load(label, u string) event {
	m := h.send("load " + label + " " + u)
	return h.expect("load", m, *timeout, "window", label, "success", true)
}

// page loads the probe page with ?run=...&id=rid and waits for its report:
// a host event for owned windows, the attachment server's record otherwise.
func (h *host) page(label, query, rid string, srv *server) event {
	since := 0
	if srv != nil {
		since = len(srv.reports())
	}
	m := h.send("load " + label + " " + h.origins[label] + "/?" + query + "&id=" + rid)
	return h.awaitReport(label, rid, m, srv, since)
}

func (h *host) awaitReport(label, rid string, mark int, srv *server, since int) event {
	deadline := time.Now().Add(*timeout)
	if srv != nil {
		for time.Now().Before(deadline) {
			for _, r := range srv.reports()[since:] {
				if r.str("id") == rid {
					return r
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		fail("timed out waiting for report %s from the attachment server", rid)
	}
	for {
		e := h.expect("report", mark, time.Until(deadline), "window", label)
		r := e.sub("report")
		if r.str("id") == rid {
			if r.str("error") != "" {
				fail("report %s: %s", rid, r.str("error"))
			}
			return r
		}
		mark = h.last
	}
}

func (h *host) reload(label, rid string) event {
	m := h.send("reload " + label)
	return h.awaitReport(label, rid, m, nil, 0)
}

func (h *host) click(label string, at []int) event {
	m := h.send(fmt.Sprintf("click %s %d %d", label, at[0], at[1]))
	return h.expect("click", m, *timeout, "window", label)
}

func (h *host) status() map[string]event {
	e := h.expect("status", h.send("status"), *timeout)
	out := map[string]event{}
	for k, v := range e.sub("windows") {
		m, _ := v.(map[string]any)
		out[k] = event(m)
	}
	return out
}

func (h *host) close(label string) event {
	return h.expect("closed", h.send("close "+label), 60*time.Second, "window", label)
}

func (h *host) snapshot(label, path string) event {
	return h.expect("snapshot", h.send("snapshot "+label+" "+path), *timeout, "window", label)
}

// errorPage waits for a top-level load failure that showed the host's page,
// and for that page to finish loading. It returns the failure and the
// error-page event, whose retry_at and start_at give the button positions.
func (h *host) errorPage(label string, since int) (event, event) {
	failed := h.expect("load-failed", since, *timeout, "window", label, "error_page", true)
	page := h.expect("error-page", since, *timeout, "window", label, "uri", failed.str("uri"))
	h.expect("load", h.last, *timeout, "window", label, "error_page", true)
	return failed, page
}

func at(e event, k string) []int {
	l, _ := e[k].([]any)
	out := make([]int, len(l))
	for i, v := range l {
		out[i] = int(v.(float64))
	}
	return out
}

type exitInfo struct {
	HostExit          int      `json:"host_exit"`
	TimedOut          bool     `json:"timed_out"`
	Killed            bool     `json:"killed"`
	BrowserPIDs       []uint32 `json:"browser_pids"`
	BrowserRemaining  []uint32 `json:"browser_processes_remaining"`
	StderrSuspicious  []string `json:"stderr_suspicious"`
	CallbacksLive     int      `json:"callback_objects_live"`
	OffUIThread       int      `json:"off_ui_thread"`
	LateCreation      int      `json:"late_creation_callbacks"`
	ProfilesRemaining []string `json:"profiles_remaining"`
}

// finish quits the host, waits for it, and records what was left behind:
// browser processes the host reported, profile folders under the root with
// its pid, and anything alarming on stderr.
func (h *host) finish(to time.Duration, kill bool) exitInfo {
	var info exitInfo
	if kill {
		h.cmd.Process.Kill()
		info.Killed = true
	} else {
		select {
		case <-h.done:
		default:
			h.send("quit")
		}
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
	// Browser processes take a moment to go after the host; give them it.
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
		info.CallbacksLive, info.OffUIThread, info.LateCreation = int(e.num("callback_objects_live")), int(e.num("off_ui_thread")), int(e.num("late_creation_callbacks"))
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
				info.ProfilesRemaining = append(info.ProfilesRemaining, en.Name())
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
	const queryLimited = 0x1000
	h, _, _ := pOpenProcess.Call(queryLimited, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer pCloseHandle.Call(h)
	var code uint32
	pGetExitCodeProcess.Call(h, uintptr(unsafe.Pointer(&code)))
	return code == 259 // STILL_ACTIVE
}

// ------------------------------------------------------------------ servers

type record struct {
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Credential bool     `json:"credential"`
	AppCookies []string `json:"app_cookies"`
	Origin     string   `json:"origin,omitempty"`
	Upgrade    string   `json:"upgrade,omitempty"`
	Remote     string   `json:"remote"` // the client port tells two connections apart
	T          string   `json:"t"`
}

// server is a loopback HTTP server whose requests this driver records. Every
// reply closes its connection, so a stopped server is really unreachable:
// a kept-alive connection would otherwise keep answering.
type server struct {
	name   string
	handle func(w http.ResponseWriter, r *http.Request, body []byte)
	port   int
	mu     sync.Mutex
	reqs   []record
	reps   []event
	srv    *http.Server
	ln     net.Listener
	cached int
}

func newServer(name string, handle func(http.ResponseWriter, *http.Request, []byte)) *server {
	s := &server{name: name, handle: handle}
	s.start()
	return s
}

func (s *server) start() {
	ln, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(s.port))
	if err != nil {
		panic(err)
	}
	s.ln = ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		rec := record{Method: r.Method, Path: r.URL.RequestURI(), Origin: r.Header.Get("Origin"), Upgrade: r.Header.Get("Upgrade"), Remote: r.RemoteAddr, T: time.Now().Format("15:04:05.000")}
		for _, c := range r.Cookies() {
			if c.Name == credential {
				rec.Credential = true
			} else {
				rec.AppCookies = append(rec.AppCookies, c.Name)
			}
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, rec)
		s.mu.Unlock()
		w.Header().Set("Connection", "close")
		s.handle(w, r, body)
	}), ReadHeaderTimeout: 10 * time.Second}
	s.srv.SetKeepAlivesEnabled(false)
	go s.srv.Serve(ln)
}

func (s *server) stop() { s.srv.Close() }

func (s *server) origin() string { return "http://127.0.0.1:" + strconv.Itoa(s.port) }

func (s *server) count(method, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.reqs {
		p := strings.SplitN(r.Path, "?", 2)[0]
		if (method == "" || r.Method == method) && (path == "" || p == path) {
			n++
		}
	}
	return n
}

func (s *server) snapshot() []record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]record(nil), s.reqs...)
}

func (s *server) reports() []event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event(nil), s.reps...)
}

func reply(w http.ResponseWriter, status int, body []byte, ctype string, headers map[string]string) {
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

func collectorHandle(w http.ResponseWriter, r *http.Request, body []byte) {
	reply(w, 200, []byte("collected\n"), "text/plain", nil)
}

var types = map[string]string{"index.html": "text/html; charset=utf-8", "probe.js": "text/javascript", "probe.css": "text/css", "logo.svg": "image/svg+xml"}

// attachmentHandle serves the probe page from disk plus the error-page
// routes: /broken and /broken-post drop the connection without a response.
func (s *server) attachmentHandle(w http.ResponseWriter, r *http.Request, body []byte) {
	path := r.URL.Path
	name := strings.TrimPrefix(path, "/")
	if name == "" {
		name = "index.html"
	}
	switch {
	case r.Method == "GET" && types[name] != "":
		b, err := os.ReadFile(filepath.Join(*static, name))
		if err != nil {
			reply(w, 500, []byte(err.Error()), "text/plain", nil)
			return
		}
		reply(w, 200, b, types[name], map[string]string{"Cache-Control": "no-store"})
	case path == "/api/query":
		reply(w, 200, []byte(`{"x":`+strconv.Quote(r.URL.Query().Get("x"))+`}`), "application/json", nil)
	case path == "/api/echo":
		reply(w, 200, []byte(`{"length":`+strconv.Itoa(len(body))+`}`), "application/json", nil)
	case path == "/api/cached":
		s.mu.Lock()
		s.cached++
		n := strconv.Itoa(s.cached)
		s.mu.Unlock()
		reply(w, 200, []byte("cached "+n), "text/plain", map[string]string{"Cache-Control": "max-age=600", "X-Count": n})
	case path == "/api/form":
		w.Header().Set("Location", "/?posted=1")
		reply(w, 303, nil, "", nil)
	case path == "/post-303":
		w.Header().Set("Location", "/broken")
		reply(w, 303, nil, "", nil)
	case path == "/post-307":
		w.Header().Set("Location", "/broken-post")
		reply(w, 307, nil, "", nil)
	case path == "/broken" || path == "/broken-post":
		// No response at all: a transport failure.
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				c.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	case path == "/missing":
		reply(w, 404, []byte("<title>404 from the server</title><h1>404 from the server</h1>"), "text/html", nil)
	case path == "/e500":
		reply(w, 500, []byte("<title>500 from the server</title><h1>500 from the server</h1>"), "text/html", nil)
	case path == "/api/report" && r.Method == "POST":
		var e event
		if json.Unmarshal(body, &e) == nil {
			s.mu.Lock()
			s.reps = append(s.reps, e)
			s.mu.Unlock()
		}
		reply(w, 204, nil, "", nil)
	default:
		reply(w, 404, []byte("not found\n"), "text/plain", nil)
	}
}

func newAttachment() *server {
	s := &server{name: "attachment"}
	s.handle = s.attachmentHandle
	s.start()
	return s
}

func httpStatus(addr, path string, headers map[string]string) int {
	req, _ := http.NewRequest("GET", addr+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	tr := &http.Transport{DisableKeepAlives: true}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

// ------------------------------------------------------------------ runner

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
	Ready    event          `json:"ready,omitempty"`
}

func (r *result) check(name string, ok bool, kv ...any) bool {
	c := check{Check: name, OK: ok, Detail: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		c.Detail[kv[i].(string)] = kv[i+1]
	}
	r.Checks = append(r.Checks, c)
	return ok
}

type run struct {
	results []*result
}

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

func (ru *run) scenario(name string, flags []string, body func(h *host, r *result, dir string), crash bool) {
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
		r.Ready = h.ready
		body(h, r, dir)
	}()
	if h != nil {
		info := h.finish(60*time.Second, crash)
		r.Exit = &info
	}
	r.Seconds = float64(time.Since(started).Round(100*time.Millisecond)) / float64(time.Second)
	clean := r.Exit != nil && !r.Exit.TimedOut && len(r.Exit.BrowserRemaining) == 0 && r.Exit.CallbacksLive == 0 && r.Exit.OffUIThread == 0
	if crash {
		clean = r.Exit != nil && r.Exit.Killed
	} else {
		clean = clean && r.Exit.HostExit == 0 && len(r.Exit.StderrSuspicious) == 0 && len(r.Exit.ProfilesRemaining) == 0
	}
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

func loopbackBasics(h *host, r *result, dir string) {
	h.open("A", "loopback", "", true)
	first := h.status()["A"]
	r.check("first document load admitted by the credential installed before navigation",
		first.sub("endpoint").num("admitted") >= 1 && len(first.sub("endpoint").sub("refused")) == 0, "endpoint", first["endpoint"], "handler", first["handler"])
	checks := h.page("A", "run=checks", "c", nil).sub("result")
	r.check("assets, query and POST through the credential", checks.boolean("css") && checks.boolean("img") && checks.str("query") == "é&ü" && checks.num("echoLength") == 1005, "checks", checks)
	r.check("credential invisible to JavaScript", checks["cookieVisible"] == false)
	again := h.reload("A", "c").sub("result")
	installs := len(h.events("cookie-installed", 0, "window", "A"))
	r.check("native reload keeps authentication without reprovisioning", again.boolean("css") && again.str("query") == "é&ü" && installs == 1, "installs", installs)
	before := h.status()["A"]
	loaded := h.load("A", h.origins["A"]+"/api/redirect")
	after := h.status()["A"]
	r.check("same-origin redirect authenticated, no credential in the URL",
		strings.HasSuffix(loaded.str("source"), "/api/query?x=redirected") && after.sub("handler").num("GET /api/query") == before.sub("handler").num("GET /api/query")+1 &&
			fmt.Sprint(after.sub("endpoint")["refused"]) == fmt.Sprint(before.sub("endpoint")["refused"]), "source", loaded["source"])
	chunksRaw, _ := h.page("A", "run=stream", "s", nil)["result"].([]any)
	texts := ""
	for _, c := range chunksRaw {
		texts += event(c.(map[string]any)).str("text")
	}
	r.check("loopback stream: chunk 1 visible before the handler finished", len(chunksRaw) >= 2 && strings.Contains(texts, "chunk-2 after ack"), "chunks", chunksRaw)
	addr := h.origins["A"]
	statuses := map[string]int{
		"none":                        httpStatus(addr, "/api/query?x=external", nil),
		"wrong credential":            httpStatus(addr, "/api/query?x=external", map[string]string{"Cookie": credential + "=wrong"}),
		"wrong credential and Origin": httpStatus(addr, "/api/query?x=external", map[string]string{"Cookie": credential + "=wrong", "Origin": "http://127.0.0.1:1"}),
	}
	self := h.expect("selftest", h.send("selftest A"), *timeout, "window", "A").sub("statuses")
	st := h.status()["A"]
	r.check("external clients without the right credential refused", statuses["none"] == 403 && statuses["wrong credential"] == 403 && statuses["wrong credential and Origin"] == 403, "statuses", statuses)
	r.check("right credential: wrong Host or Origin refused; exact or absent Origin admitted",
		self.num("right credential") == 200 && self.num("right credential, wrong Host") == 403 && self.num("right credential, wrong Origin") == 403 && self.num("right credential, exact Origin") == 200, "selftest", self)
	refused := st.sub("endpoint").sub("refused")
	r.check("refusals never reached the handler", refused.num("no-credential") == 1 && refused.num("wrong-credential") == 2 && refused.num("host") == 1 && refused.num("origin") == 1 &&
		st.sub("endpoint").num("handled") == st.sub("endpoint").num("admitted"), "endpoint", st["endpoint"])
	h.snapshot("A", filepath.Join(dir, "loopback-page.png"))
	r.Detail["status"] = st
	r.Detail["cookie"] = h.events("cookie-installed", 0, "window", "A")
}

func confinement(filterOn, cspOn bool) func(h *host, r *result, dir string) {
	return func(h *host, r *result, dir string) {
		collector := newServer("collector", collectorHandle)
		defer collector.stop()
		h.open("A", "loopback", "", true)
		h.open("B", "loopback", "", true)
		home := h.origins["A"] + "/"
		q := url.QueryEscape
		bBefore := h.status()["B"].sub("handler")
		attackMark := h.mark()
		seen := h.page("A", "run=attacks&collector="+q(collector.origin())+"&other="+q(h.origins["B"]), "atk", nil)["result"]
		marks, final := map[string]int{}, map[string]string{}
		for _, how := range []string{"location", "redirect", "form"} {
			marks[how] = h.send("load A " + home + "?run=armed&id=nav-" + how + "&then=navigate&how=" + how + "&collector=" + q(collector.origin()))
			h.awaitReport("A", "nav-"+how, marks[how], nil, 0)
			time.Sleep(1500 * time.Millisecond)
			final[how] = h.status()["A"].str("uri")
			if !strings.HasPrefix(final[how], home) {
				h.load("A", home)
			}
		}
		marks["popup"] = h.mark()
		popup := h.page("A", "run=popup&collector="+q(collector.origin()), "popup", nil)["result"]
		time.Sleep(time.Second)
		got := collector.snapshot()
		bAfter := h.status()["B"].sub("handler")
		enforced := filterOn || cspOn
		leaked := 0
		for _, g := range got {
			if g.Credential {
				leaked++
			}
		}
		received := make([]string, 0, len(got))
		for _, g := range got {
			received = append(received, fmt.Sprintf("%s %s credential=%v", g.Method, g.Path, g.Credential))
		}
		if enforced {
			r.check("collector received nothing", len(got) == 0, "received", received)
			r.check("no credential reached the collector", leaked == 0, "leaked", leaked)
		} else {
			r.check("control: collector receives requests", len(got) > 0, "received", received)
			r.check("control: credential leaks without enforcement", leaked > 0, "leaked", leaked)
		}
		r.check("A's page never invoked B's handler", bAfter.num("GET /api/count") == bBefore.num("GET /api/count"), "b_before", bBefore, "b_after", bAfter)
		refusedNav := func(how, path string) bool {
			for _, e := range h.events("navigation-refused", marks[how], "window", "A") {
				if strings.HasPrefix(e.str("uri"), collector.origin()+path) {
					return true
				}
			}
			return false
		}
		blockedNative := func(since int, path string) bool {
			for _, e := range h.events("blocked", since, "window", "A") {
				if strings.HasPrefix(e.str("uri"), collector.origin()+path) {
					return true
				}
			}
			return false
		}
		arrived := func(path string) bool {
			for _, g := range got {
				if strings.HasPrefix(g.Path, path) {
					return true
				}
			}
			return false
		}
		attribute := func(how, path string) string {
			switch {
			case refusedNav(how, path):
				return "navigation policy"
			case blockedNative(marks[how], path):
				return "native interception"
			case arrived(path):
				return "NOT REFUSED"
			default:
				return "silent"
			}
		}
		mechanism := map[string]string{
			"location": attribute("location", "/nav"), "redirect": attribute("redirect", "/redirect-nav"), "form": attribute("form", "/form"),
		}
		if len(h.events("popup-refused", marks["popup"], "window", "A")) > 0 {
			mechanism["popup"] = "NewWindowRequested handled"
		} else {
			mechanism["popup"] = fmt.Sprintf("silent (window.open returned %v)", popup)
		}
		// Per request class: how each attack was stopped, or that it arrived.
		classes := map[string]string{}
		for _, path := range []string{"/img", "/script", "/css", "/font", "/fetch", "/xhr", "/beacon", "/ws", "/sse", "/prefetch", "/iframe", "/redirect-img", "/redirect-fetch", "/ping"} {
			switch {
			case arrived(path):
				classes[path] = "REACHED"
			case blockedNative(attackMark, path):
				classes[path] = "native interception"
			case len(h.events("navigation-refused", attackMark, "window", "A", "kind", "subframe")) > 0 && path == "/iframe":
				classes[path] = "subframe navigation refused"
			default:
				classes[path] = "not observed" + map[bool]string{true: " (CSP or the page)", false: ""}[cspOn]
			}
		}
		allHome := true
		for _, u := range final {
			allHome = allHome && strings.HasPrefix(u, home)
		}
		notRefused := false
		for _, m := range mechanism {
			notRefused = notRefused || m == "NOT REFUSED"
		}
		r.check("top-level navigation, redirect, popup and form to another port never left the origin", allHome && !notRefused, "mechanism", mechanism, "final_uris", final)
		r.Detail["page_view"] = seen
		r.Detail["mechanism"] = mechanism
		r.Detail["classes"] = classes
		r.Detail["collector"] = got
		r.Detail["blocked_by_context"] = h.status()["A"]["blocked"]
		r.Detail["refused"] = h.status()["A"]["refused"]
	}
}

func inprocessIsolation(h *host, r *result, dir string) {
	collector := newServer("collector", collectorHandle)
	defer collector.stop()
	h.open("I1", "inprocess", "", true)
	h.open("I2", "inprocess", "", true)
	checks := h.page("I1", "run=checks", "c", nil).sub("result")
	r.check("in-process page, assets, query and POST", checks.boolean("css") && checks.str("query") == "é&ü" && checks.num("echoLength") == 1005, "checks", checks)
	before := h.status()
	h.page("I1", "run=count&url="+url.QueryEscape(h.origins["I1"]+"/api/count"), "count", nil)
	after := h.status()
	r.check("naming the shared origin reaches only the window's own handler",
		after["I1"].sub("handler").num("GET /api/count") == before["I1"].sub("handler").num("GET /api/count")+1 &&
			after["I2"].sub("handler").num("GET /api/count") == before["I2"].sub("handler").num("GET /api/count"), "i1", after["I1"]["handler"], "i2", after["I2"]["handler"])
	h.page("I1", "run=attacks&collector="+url.QueryEscape(collector.origin()), "atk", nil)
	time.Sleep(time.Second)
	r.check("an in-process page cannot reach the collector", len(collector.snapshot()) == 0, "received", collector.snapshot())
	i1 := h.page("I1", "run=storageSet&tag=I1", "set", nil)["result"]
	i2 := h.page("I2", "run=storageRead", "read", nil).sub("result")
	r.check("same in-process origin, separate sessions: I2 cannot read I1's storage", i2.str("cookie") == "" && i2["local"] == nil && i2["session"] == nil, "i1", i1, "i2", i2)
	st := h.status()
	r.Detail["profiles"] = map[string]any{"I1": st["I1"]["profile"], "I2": st["I2"]["profile"], "pids": []any{st["I1"]["browser_pid"], st["I2"]["browser_pid"]}}
}

func cookieCloseRace(h *host, r *result, dir string) {
	m := h.send("open A loopback")
	h.send("close A")
	closed := h.expect("closed", m, 60*time.Second, "window", "A")
	late := h.events("late-callback", m, "window", "A")
	late = append(late, h.events("cookie-late", m, "window", "A")...)
	r.check("close during creation: no navigation, ownership drained, profile deleted",
		len(h.events("navigate", m, "window", "A")) == 0 && closed.num("native_async") == 0 && closed.sub("endpoint").num("handled") == 0 && closed.boolean("profile_removed"),
		"late", late, "closed", closed)
	r.Detail["stage"] = closed["stage"]
}

func closeDuring(stage string) func(h *host, r *result, dir string) {
	return func(h *host, r *result, dir string) {
		m := h.send("open A loopback")
		closed := h.expect("closed", m, 60*time.Second, "window", "A")
		late := h.events("late-callback", m, "window", "A")
		late = append(late, h.events("cookie-late", m, "window", "A")...)
		r.check("close during "+stage+": no navigation, ownership drained, profile deleted",
			len(h.events("navigate", m, "window", "A")) == 0 && closed.num("native_async") == 0 && closed.sub("endpoint").num("handled") == 0 && closed.boolean("profile_removed") &&
				len(h.events("test", m, "window", "A")) == 1, "late", late, "closed", closed, "hook", h.events("test", m))
		r.Detail["stage"] = closed["stage"]
	}
}

func cookieFailure(h *host, r *result, dir string) {
	m := h.send("open A loopback")
	failed := h.expect("cookie-failed", m, *timeout, "window", "A")
	closed := h.expect("closed", m, 60*time.Second, "window", "A")
	r.check("injected installation failure: visible, no navigation, drained",
		strings.Contains(failed.str("error"), "injected") && len(h.events("navigate", m, "window", "A")) == 0 && closed.num("native_async") == 0 && closed.sub("endpoint").num("handled") == 0,
		"failed", failed, "closed", closed)
}

func removalFailure(h *host, r *result, dir string) {
	h.open("A", "loopback", "", true)
	m := h.send("close A")
	removal := h.expect("profile-removal", m, 60*time.Second, "window", "A")
	closed := h.expect("closed", m, 60*time.Second, "window", "A")
	rm := removal.sub("removal")
	profile := rm.str("path")
	_, statErr := os.Stat(profile)
	r.check("injected post-release deletion failure: labeled, retried, named as residue, never reported as deleted",
		rm.boolean("injected") && !rm.boolean("removed") && rm.num("attempts") == 20 && strings.Contains(removal.str("error"), profile) &&
			removal.boolean("released") && removal.boolean("browser_exit_observed") && !closed.boolean("profile_removed") && closed.str("removal_error") != "" && statErr == nil,
		"removal", removal, "closed", closed)
	r.Detail["residue"] = profile
	r.Detail["driver_cleanup"] = fmt.Sprint(os.RemoveAll(profile)) // the driver's own cleanup, after the checks
}

func storage(h *host, r *result, dir string) {
	attach := newAttachment()
	defer attach.stop()
	u := attach.origin() + "/"
	h.open("A", "attach", u, true)
	h.open("B", "attach", u, true)
	a := h.page("A", "run=storageSet&tag=A", "a-set", attach)["result"]
	b := h.page("B", "run=storageRead", "b-read", attach).sub("result")
	r.check("B cannot read A's cookie, local or session storage", b.str("cookie") == "" && b["local"] == nil && b["session"] == nil, "a", a, "b", b)
	h.page("B", "run=storageSet&tag=B", "b-set", attach)
	cached := func() int { return attach.count("GET", "/api/cached") }
	n0 := cached()
	h.page("A", "run=cached", "a-cached-1", attach)
	h.page("A", "run=cached", "a-cached-2", attach)
	n1 := cached()
	h.page("A", "run=cachedImage", "a-image", attach)
	n2 := cached()
	h.page("B", "run=cachedImage", "b-image", attach)
	n3 := cached()
	r.check("B's request for A's cached resource reaches the server", n3 > n2, "server_requests", map[string]int{"A fetch x2": n1 - n0, "A image x2": n2 - n1, "B image x2": n3 - n2})
	since := len(attach.reports())
	h.page("A", "run=storageRead", "a-read", attach)
	m := h.send("reload A")
	reloaded := h.awaitReport("A", "a-read", m, attach, since+1).sub("result")
	r.check("native reload keeps A's session", reloaded.str("cookie") == "vis=A" && reloaded.str("local") == "A" && reloaded.str("session") == "A", "a", reloaded)
	m = h.send("close A")
	during := []any{}
	for i := 0; i < 3; i++ {
		during = append(during, h.page("B", fmt.Sprintf("run=query&x=during%d", i), fmt.Sprintf("b-during-%d", i), attach).sub("result").str("x"))
	}
	closedA := h.expect("closed", m, 60*time.Second, "window", "A")
	r.check("B kept working while A closed", fmt.Sprint(during) == "[during0 during1 during2]", "during", during, "closed_a", closedA)
	h.open("A2", "attach", u, true)
	a2 := h.page("A2", "run=storageRead", "a2-read", attach).sub("result")
	bAfter := h.page("B", "run=storageRead", "b-read-2", attach).sub("result")
	r.check("reopened A has a fresh session", a2.str("cookie") == "" && a2["local"] == nil && a2["session"] == nil, "a2", a2)
	r.check("B is unchanged", bAfter.str("cookie") == "vis=B" && bAfter.str("local") == "B" && bAfter.str("session") == "B", "b", bAfter)
	closedB := h.close("B")
	closedA2 := h.close("A2")
	survived := httpStatus(attach.origin(), "/api/query?x=after-browser", nil)
	r.check("the attachment server outlives its browsers", survived == 200, "status", survived)
	var names [][]string
	for _, rec := range attach.snapshot() {
		if strings.HasPrefix(rec.Path, "/api/cached") {
			names = append(names, rec.AppCookies)
		}
	}
	r.Detail["app_cookie_names_per_cached_request"] = names
	r.Detail["release"] = map[string]any{"A": closedA, "B": closedB, "A2": closedA2}
}

// navigationsTo counts the window's allowed top-level navigations to uri
// since mark, redirects included.
func navigationsTo(h *host, label string, since int, uri string) int {
	n := 0
	for _, e := range h.events("navigation-allowed", since, "window", label) {
		if e.str("uri") == uri {
			n++
		}
	}
	return n
}

func noAction(h *host, label string, since int) bool {
	time.Sleep(2 * time.Second)
	return len(h.events("error-action", since, "window", label)) == 0
}

func errorPages(h *host, r *result, dir string) {
	attach := newAttachment()
	defer attach.stop()
	u := attach.origin() + "/"
	q := url.QueryEscape
	h.open("E", "attach", u, true)
	since := len(attach.reports())
	m := h.send("load E " + u + "?run=armed&id=g1&delay=1500&then=go&to=" + q("api/query?x=later"))
	h.awaitReport("E", "g1", m, attach, since)
	attach.stop()
	failed, page := h.errorPage("E", m)
	h.snapshot("E", filepath.Join(dir, "error-get.png"))
	capture(h.hwnds["E"], filepath.Join(dir, "screen-error-get.png"))
	r.check("failed GET: Retry and Return to start, no script", page.boolean("retry") && !page.boolean("script") && failed.str("method") == "GET", "failed", failed, "page", page)
	attach.start()
	gets := attach.count("GET", "/api/query")
	m = h.send("status")
	h.click("E", at(page, "retry_at"))
	h.expect("error-action", m, *timeout, "window", "E", "action", "retry")
	h.expect("load", m, *timeout, "window", "E", "success", true)
	r.check("clicked Retry repeats the failed GET once the service returns", attach.count("GET", "/api/query") == gets+1, "gets_before", gets, "gets_after", attach.count("GET", "/api/query"))
	since = len(attach.reports())
	m = h.send("load E " + u + "?run=armed&id=forge&then=go&to=" + q("https://dfx2-error.invalid/retry/forged"))
	h.awaitReport("E", "forge", m, attach, since)
	h.expect("error-action-refused", m, *timeout, "window", "E")
	r.check("a page-supplied action is refused", true)
	since = len(attach.reports())
	m = h.send("load E " + u + "?run=armed&id=p1&delay=1500&then=submit")
	h.awaitReport("E", "p1", m, attach, since)
	attach.stop()
	failedPost, postPage := h.errorPage("E", m)
	h.snapshot("E", filepath.Join(dir, "error-post.png"))
	capture(h.hwnds["E"], filepath.Join(dir, "screen-error-post.png"))
	r.check("failed POST shows only Return to start", !postPage.boolean("retry") && failedPost.str("method") == "POST", "failed", failedPost, "page", postPage)
	attach.start()
	m = h.send("status")
	h.click("E", at(postPage, "retry_at"))
	r.check("a click where Retry would be does nothing on the POST page", noAction(h, "E", m))
	m = h.send("status")
	h.click("E", at(postPage, "start_at"))
	h.expect("error-action", m, *timeout, "window", "E", "action", "start")
	h.expect("load", m, *timeout, "window", "E", "success", true)
	edits := func() []record {
		var out []record
		for _, rec := range attach.snapshot() {
			if rec.Method == "POST" && !strings.HasPrefix(rec.Path, "/api/report") {
				out = append(out, rec)
			}
		}
		return out
	}
	r.check("clicked Return to start is a GET and replays nothing", len(edits()) == 0, "edits", edits())
	m = h.send("load E " + u + "?run=armed&id=p303&then=submit&action=" + q("/post-303"))
	failed303, page303 := h.errorPage("E", m)
	r.check("a GET reached by 303 after a POST offers Retry of the GET", failed303.str("method") == "GET" && failed303.boolean("from_post") && page303.boolean("retry"), "failed", failed303)
	brokenBefore := attach.count("GET", "/broken")
	postsBeforeRetry := attach.count("POST", "/post-303")
	m = h.send("status")
	h.click("E", at(page303, "retry_at"))
	h.expect("error-action", m, *timeout, "window", "E", "action", "retry")
	_, pageAgain := h.errorPage("E", m)
	// Retry is judged by navigations: one allowed, non-redirect navigation to
	// the failed GET after the click. Server counts are recorded, not
	// asserted: the browser's transport re-sends a request whose connection
	// closed without a response, so one navigation reaches the server more
	// than once (observed on Linux too).
	retryNavs := navigationsTo(h, "E", m, attach.origin()+"/broken")
	r.check("Retry after the 303 is one GET navigation and replays no POST", postsBeforeRetry > 0 && attach.count("POST", "/post-303") == postsBeforeRetry && retryNavs == 1 &&
		len(h.events("document-request", m, "window", "E", "method", "POST")) == 0,
		"post_303_before_retry", postsBeforeRetry, "post_303_server_count", attach.count("POST", "/post-303"), "retry_navigations", retryNavs,
		"broken_server_requests", map[string]int{"first navigation": brokenBefore, "retry": attach.count("GET", "/broken") - brokenBefore})
	m = h.send("status")
	h.click("E", at(pageAgain, "start_at"))
	h.expect("load", m, *timeout, "window", "E", "success", true, "source", u)
	m = h.send("load E " + u + "?run=armed&id=p307&then=submit&action=" + q("/post-307"))
	failed307, page307 := h.errorPage("E", m)
	brokenPostNavs := navigationsTo(h, "E", m, attach.origin()+"/broken-post")
	brokenPosts := attach.count("POST", "/broken-post")
	postsBeforeStart := attach.count("POST", "/post-307")
	r.check("a POST preserved by 307 offers no Retry", failed307.str("method") == "POST" && !page307.boolean("retry") &&
		postsBeforeStart > 0 && brokenPosts > 0 && brokenPostNavs == 1, "failed", failed307, "post_307_server_count", postsBeforeStart,
		"broken_post_navigations", brokenPostNavs)
	// Transport delivery is an observation, not an at-most-once contract.
	// Judge the error-page action against counts captured before its click.
	r.Detail["redirected_post_delivery"] = map[string]int{"server_requests": brokenPosts, "navigations": brokenPostNavs}
	m = h.send("status")
	h.click("E", at(page307, "start_at"))
	h.expect("error-action", m, *timeout, "window", "E", "action", "start")
	h.expect("load", m, *timeout, "window", "E", "success", true, "source", u)
	r.check("Return to start after the 307 is one GET navigation and replays no POST",
		navigationsTo(h, "E", m, u) == 1 && len(h.events("document-request", m, "window", "E", "method", "POST")) == 0 &&
			attach.count("POST", "/post-307") == postsBeforeStart && attach.count("POST", "/broken-post") == brokenPosts,
		"post_307_before_start", postsBeforeStart, "post_307_after_start", attach.count("POST", "/post-307"),
		"broken_post_before_start", brokenPosts, "broken_post_after_start", attach.count("POST", "/broken-post"))
	m = h.send("status")
	titled := func(path string) string {
		// An HTTP error page completes its navigation with IsSuccess false;
		// what matters is that the server's page is shown, not replaced.
		h.expect("load", h.send("load E "+attach.origin()+path), *timeout, "window", "E")
		time.Sleep(300 * time.Millisecond)
		return h.status()["E"].str("title")
	}
	missing, e500 := titled("/missing"), titled("/e500")
	broken := h.page("E", "run=broken", "broken", attach).sub("result")
	r.check("404 and 500 display; a failed fetch or asset leaves the page", missing == "404 from the server" && e500 == "500 from the server" &&
		len(h.events("load-failed", m, "window", "E", "error_page", true)) == 0 && strings.HasPrefix(broken.str("fetched"), "rejected") && broken.str("path") == "/" && broken.boolean("form"),
		"missing", missing, "e500", e500, "broken", broken)
	attach.stop()
	m = h.open("F", "attach", u, false)
	initial, initialPage := h.errorPage("F", m)
	capture(h.hwnds["F"], filepath.Join(dir, "screen-error-initial.png"))
	attach.start()
	m = h.send("status")
	h.click("F", at(initialPage, "retry_at"))
	h.expect("error-action", m, *timeout, "window", "F", "action", "retry")
	loaded := h.expect("load", m, *timeout, "window", "F", "success", true)
	r.check("initial-load failure, then clicked Retry", loaded.str("source") == u && len(h.events("load-failed", m, "window", "F", "error_page", true)) == 0, "initial", initial)
	r.Detail["edits"] = edits()
	r.Detail["document_requests"] = h.events("document-request", 0, "window", "E")
}

func ownedLoadFailure(h *host, r *result, dir string) {
	for _, lm := range [][2]string{{"L", "loopback"}, {"P", "inprocess"}} {
		label, mode := lm[0], lm[1]
		h.open(label, mode, "", true)
		m := h.send("load " + label + " " + h.origins[label] + "/api/abort")
		failed, page := h.errorPage(label, m)
		h.snapshot(label, filepath.Join(dir, "error-"+mode+".png"))
		capture(h.hwnds[label], filepath.Join(dir, "screen-error-"+mode+".png"))
		r.check(mode+": native load failure shows the error page", page.boolean("retry") && !page.boolean("script"), "failed", failed)
		m = h.send("status")
		h.click(label, at(page, "start_at"))
		h.expect("error-action", m, *timeout, "window", label, "action", "start")
		loaded := h.expect("load", m, *timeout, "window", label, "success", true)
		r.check(mode+": clicked Return to start reloads the start page", loaded.str("source") == h.origins[label]+"/", "source", loaded["source"])
	}
}

func shutdown(mode, how string) func(h *host, r *result, dir string) {
	return func(h *host, r *result, dir string) {
		h.open("S", mode, "", true)
		if how == "inspector" {
			h.expect("inspector", h.send("inspect S"), *timeout, "window", "S", "opened", true)
			time.Sleep(1500 * time.Millisecond)
		}
		if how == "pending" || how == "inspector" {
			h.page("S", "run=slow", "slow", nil)
			time.Sleep(300 * time.Millisecond)
		}
		closed := h.close("S")
		pending := closed.sub("handler").num("GET /api/slow")
		r.check(how+" close drains and releases", closed.num("native_async") == 0 && closed.num("pending_requests") == 0 && closed.boolean("handlers_drained") &&
			closed.boolean("browser_exit_observed") && closed.boolean("profile_removed") && (how == "ordinary" || pending == 1), "closed", closed)
		r.Detail["release"] = closed
	}
}

func combined(h *host, r *result, dir string) {
	h.open("A", "loopback", "", true)
	h.open("I", "inprocess", "", true)
	for _, label := range []string{"A", "I"} {
		c := h.page(label, "run=checks", label+"-checks", nil).sub("result")
		r.check(label+": page, assets, query and POST", c.boolean("css") && c.str("query") == "é&ü" && c.num("echoLength") == 1005)
	}
	h.page("A", "run=storageSet&tag=A", "A-set", nil)
	h.page("A", "run=slow", "A-slow", nil)
	h.page("I", "run=slow", "I-slow", nil)
	time.Sleep(300 * time.Millisecond)
	m := h.send("close A")
	during := []any{}
	for i := 0; i < 3; i++ {
		during = append(during, h.page("I", fmt.Sprintf("run=query&x=during%d", i), fmt.Sprintf("I-during-%d", i), nil).sub("result").str("x"))
	}
	closed := h.expect("closed", m, 60*time.Second, "window", "A")
	r.check("A closed with its request pending and drained, while I kept working",
		closed.num("native_async") == 0 && closed.boolean("handlers_drained") && closed.sub("handler").num("GET /api/slow") == 1 && fmt.Sprint(during) == "[during0 during1 during2]",
		"closed", closed, "during", during)
	// The reopened window gets its own label: status is keyed by label.
	h.open("A2", "loopback", "", true)
	fresh := h.page("A2", "run=storageRead", "A2-read", nil).sub("result")
	r.check("reopened A: fresh session at a new endpoint", fresh.str("cookie") == "" && fresh["local"] == nil && fresh["session"] == nil, "fresh", fresh)
	for _, label := range []string{"A2", "I"} {
		before := h.status()[label].sub("handler").num("GET /api/abort")
		m := h.send("load " + label + " " + h.origins[label] + "/api/abort")
		failed, page := h.errorPage(label, m)
		firstPage := h.last
		mid := h.status()[label].sub("handler").num("GET /api/abort")
		m = h.send("status")
		h.click(label, at(page, "retry_at"))
		h.expect("error-action", m, *timeout, "window", label, "action", "retry")
		again, page2 := h.errorPage(label, m)
		after := h.status()[label].sub("handler").num("GET /api/abort")
		navs := 0
		for _, e := range h.events("navigation-allowed", m, "window", label) {
			if e.str("uri") == failed.str("uri") {
				navs++
			}
		}
		idle := 0
		for _, e := range h.events("navigation-allowed", firstPage, "window", label) {
			if e.str("uri") == failed.str("uri") {
				idle++
			}
		}
		idle -= navs
		handlerOK := after == before
		if label == "A2" {
			handlerOK = mid > before
		}
		r.check(label+": clicked Retry repeats the failed GET once, nothing automatic", again.str("uri") == failed.str("uri") && navs == 1 && idle == 0 && handlerOK,
			"retry_navigations", navs, "handler_calls", map[string]float64{"first navigation": mid - before, "retry": after - mid})
		m = h.send("status")
		h.click(label, at(page2, "start_at"))
		h.expect("load", m, *timeout, "window", label, "success", true, "source", h.origins[label]+"/")
	}
	h.page("I", "run=slow", "I-slow-2", nil)
	h.page("A2", "run=slow", "A2-slow-2", nil)
	time.Sleep(300 * time.Millisecond)
	for _, label := range []string{"I", "A2"} {
		c := h.close(label)
		r.check(label+": final close with a request pending drains and releases", c.num("native_async") == 0 && c.num("pending_requests") == 0 && c.boolean("handlers_drained") &&
			c.boolean("browser_exit_observed") && c.boolean("profile_removed"), "closed", c)
	}
}

// forcedCrash kills a host that owns a profile folder: the residue stays,
// named by its process; the next host uses a fresh folder and neither scans
// nor deletes another instance's. No scavenger runs.
func forcedCrash(h *host, r *result, dir string) {
	h.open("A", "loopback", "", true)
	st := h.status()["A"]
	profile := st.str("profile")
	pid := uint32(st.num("browser_pid"))
	h.cmd.Process.Kill()
	<-h.done
	time.Sleep(2 * time.Second)
	_, err := os.Stat(profile)
	r.check("a killed host leaves its own profile folder, named by its pid", err == nil && strings.Contains(filepath.Base(profile), strconv.Itoa(h.cmd.Process.Pid)), "profile", profile)
	r.Detail["browser_after_kill_alive"] = processAlive(pid)
	second := filepath.Join(dir, "second")
	os.MkdirAll(second, 0o755)
	h2 := startHost(second)
	var fresh string
	func() {
		defer func() {
			if p := recover(); p != nil {
				r.Failure = fmt.Sprint(p)
			}
		}()
		h2.open("B", "loopback", "", true)
		fresh = h2.status()["B"].str("profile")
	}()
	info := h2.finish(60*time.Second, false)
	r.check("the next host starts with its own fresh folder", fresh != "" && fresh != profile && strings.Contains(filepath.Base(fresh), strconv.Itoa(h2.cmd.Process.Pid)), "fresh", fresh)
	_, errResidue := os.Stat(profile)
	_, errFresh := os.Stat(fresh)
	r.check("the next host removed only its own folder; the residue remains", errResidue == nil && os.IsNotExist(errFresh) && info.HostExit == 0, "second_exit", info)
	r.Detail["residue"] = profile
	r.Detail["fresh"] = fresh
	r.Detail["driver_cleanup"] = fmt.Sprint(os.RemoveAll(profile)) // the driver's own cleanup of its experiment residue, after the checks
}

// ------------------------------------------------------------------ manual

func manualCheck() int {
	dir := *out
	os.MkdirAll(dir, 0o755)
	attach := newAttachment()
	defer attach.stop()
	u := attach.origin() + "/"
	q := url.QueryEscape
	h := startHost(dir)
	res := map[string]any{"steps": []any{}}
	say := func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}
	defer func() {
		if p := recover(); p != nil {
			res["failure"] = fmt.Sprint(p)
		}
		res["exit"] = h.finish(60*time.Second, false)
		b, _ := json.MarshalIndent(res, "", " ")
		os.WriteFile(filepath.Join(dir, "result.json"), append(b, '\n'), 0o644)
		say("recorded %s", filepath.Join(dir, "result.json"))
	}()
	h.open("M", "attach", u, true)
	say("A window titled 'dfx2 feasibility M — attach' is open. Hands off until a STEP line appears.")
	// Step 1: failed GET, then Retry.
	since := len(attach.reports())
	m := h.send("load M " + u + "?run=armed&id=g1&delay=1500&then=go&to=" + q("api/query?x=later"))
	h.awaitReport("M", "g1", m, attach, since)
	attach.stop()
	failed, page := h.errorPage("M", m)
	attach.start()
	gets, posts := attach.count("GET", "/api/query"), attach.count("POST", "")
	say("STEP 1 of 2: the page failed to load (GET %s). The error page should offer Retry and Return to start. The server is back. Click Retry.", failed.str("uri"))
	m = h.send("status")
	act := h.expect("error-action", m, 15*time.Minute, "window", "M", "action", "retry")
	loaded := h.expect("load", m, *timeout, "window", "M", "success", true)
	step1 := map[string]any{"offered_retry": page.boolean("retry"), "action": act, "loaded": loaded.str("source"),
		"gets_added": attach.count("GET", "/api/query") - gets, "posts_added": attach.count("POST", "") - posts}
	res["steps"] = append(res["steps"].([]any), step1)
	say("  recorded: Retry loaded %s; GETs added %v, POSTs added %v", loaded.str("source"), step1["gets_added"], step1["posts_added"])
	// Step 2: failed POST, then Return to start.
	since = len(attach.reports())
	m = h.send("load M " + u + "?run=armed&id=p1&delay=1500&then=submit")
	h.awaitReport("M", "p1", m, attach, since)
	attach.stop()
	failedPost, postPage := h.errorPage("M", m)
	attach.start()
	posts = attach.count("POST", "/api/form")
	say("STEP 2 of 2: the form POST failed (%s). The error page should offer only Return to start. The server is back. Click Return to start.", failedPost.str("uri"))
	m = h.send("status")
	act = h.expect("error-action", m, 15*time.Minute, "window", "M", "action", "start")
	loaded = h.expect("load", m, *timeout, "window", "M", "success", true)
	step2 := map[string]any{"offered_retry": postPage.boolean("retry"), "action": act, "loaded": loaded.str("source"),
		"form_posts_added": attach.count("POST", "/api/form") - posts, "form_posts_total_reaching_server": attach.count("POST", "/api/form")}
	res["steps"] = append(res["steps"].([]any), step2)
	say("  recorded: Return to start loaded %s; form POSTs that reached the server: %v (added %v)", loaded.str("source"), step2["form_posts_total_reaching_server"], step2["form_posts_added"])
	say("Done. Report what you saw: the buttons offered on each page and where each click took you.")
	return 0
}

// ------------------------------------------------------------------ capture

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	gdi32                   = syscall.NewLazyDLL("gdi32.dll")
	pGetWindowRect          = user32.NewProc("GetWindowRect")
	pSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	pGetDC                  = user32.NewProc("GetDC")
	pReleaseDC              = user32.NewProc("ReleaseDC")
	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pGetDIBits              = gdi32.NewProc("GetDIBits")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pVirtualAlloc           = kernel32.NewProc("VirtualAlloc")
	pVirtualFree            = kernel32.NewProc("VirtualFree")
)

type rect struct{ left, top, right, bottom int32 }

// capture copies the window's screen area: what reached the display. A
// minimized Remote Desktop session captures black.
func capture(hwnd uintptr, path string) {
	if hwnd == 0 {
		return
	}
	pSetForegroundWindow.Call(hwnd)
	time.Sleep(400 * time.Millisecond)
	var wr rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	w, h := int(wr.right-wr.left), int(wr.bottom-wr.top)
	if w <= 0 || h <= 0 {
		return
	}
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bmp, _, _ := pCreateCompatibleBitmap.Call(screen, uintptr(w), uintptr(h))
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(mem, bmp)
	pBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), screen, uintptr(wr.left), uintptr(wr.top), 0x00CC0020|0x40000000)
	pSelectObject.Call(mem, old)
	type bmi struct {
		size                   uint32
		width, height          int32
		planes, bitCount       uint16
		compression, sizeImage uint32
		x, y                   int32
		used, important        uint32
		colors                 [4]uint32
	}
	info := bmi{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	info.size = 40
	buf, _, _ := pVirtualAlloc.Call(0, uintptr(w*h*4), 0x3000, 0x04)
	defer pVirtualFree.Call(buf, 0, 0x8000)
	px := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), buf)), w*h*4)
	if n, _, _ := pGetDIBits.Call(mem, bmp, 0, uintptr(h), buf, uintptr(unsafe.Pointer(&info)), 0); n == 0 {
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = px[i*4+2], px[i*4+1], px[i*4+0], 255
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	png.Encode(f, img)
	f.Close()
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
	dpi, _, _ := pGetDpiForSystem.Call()
	cx, _, _ := pGetSystemMetrics.Call(0)
	cy, _, _ := pGetSystemMetrics.Call(1)
	env["system_dpi"], env["screen"] = dpi, fmt.Sprintf("%dx%d", cx, cy)
	return env
}

func output(name string, args ...string) string {
	b, _ := exec.Command(name, args...).CombinedOutput()
	return string(b)
}

func main() {
	flag.Parse()
	pSetProcessDpiContext.Call(^uintptr(3)) // per-monitor v2, so metrics and clicks are in pixels
	if *root == "" {
		*root = filepath.Join(os.TempDir(), "dfx2-feasibility")
	}
	if abs, err := filepath.Abs(*exe); err == nil {
		*exe = abs
	}
	if _, err := os.Stat(filepath.Join(*static, "index.html")); err != nil {
		fmt.Fprintln(os.Stderr, "the probe page's files were not found; run from experiments\\webview2-spike or pass -static:", err)
		os.Exit(2)
	}
	os.MkdirAll(*out, 0o755)
	os.MkdirAll(*root, 0o755)
	envb, _ := json.MarshalIndent(environment(), "", " ")
	os.WriteFile(filepath.Join(*out, "environment.json"), append(envb, '\n'), 0o644)
	if *manual {
		os.Exit(manualCheck())
	}
	ru := &run{}
	ru.scenario("loopback-basics", nil, loopbackBasics, false)
	ru.scenario("confinement-filter-csp", nil, confinement(true, true), false)
	ru.scenario("confinement-filter-only", []string{"-feas-csp=false"}, confinement(true, false), false)
	ru.scenario("confinement-csp-only", []string{"-feas-filter=false"}, confinement(false, true), false)
	ru.scenario("confinement-control", []string{"-feas-filter=false", "-feas-csp=false"}, confinement(false, false), false)
	ru.scenario("inprocess-isolation", nil, inprocessIsolation, false)
	ru.scenario("cookie-close-race", nil, cookieCloseRace, false)
	ru.scenario("close-during-controller", []string{"-feas-close-during", "controller"}, closeDuring("controller creation"), false)
	ru.scenario("close-during-cookie", []string{"-feas-close-during", "cookie"}, closeDuring("cookie installation"), false)
	ru.scenario("cookie-failure-injected", []string{"-feas-cookie-fail"}, cookieFailure, false)
	ru.scenario("removal-failure-injected", []string{"-feas-remove-fail"}, removalFailure, false)
	ru.scenario("storage", nil, storage, false)
	ru.scenario("error-pages", nil, errorPages, false)
	ru.scenario("owned-load-failure", nil, ownedLoadFailure, false)
	for _, mode := range []string{"loopback", "inprocess"} {
		for _, how := range []string{"ordinary", "pending", "inspector"} {
			ru.scenario("shutdown-"+mode+"-"+how, nil, shutdown(mode, how), false)
		}
	}
	ru.scenario("combined", nil, combined, false)
	ru.scenario("forced-crash", nil, forcedCrash, true)
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
