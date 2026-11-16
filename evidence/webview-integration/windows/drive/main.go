//go:build windows

// Command drive is the Windows browser matrix for webview integration stage
// 3. It launches the real audition host (examples/webview) and drives its
// real Win32 shells and WebView2 attachments through the host's console
// protocol; a collector and an attachment service run here, on 127.0.0.1, so
// their lifetimes and records are independent of the browser. Results are
// page-driven: the page posts probe results to its own handler, which the
// host reports as events; the host evaluates no JavaScript. Cross-origin
// attempts are judged by the collector's records and the window's URI, never
// by what the page sees. Each host gets its own TEMP, so the profile and
// loader directories it leaves are its own to find. Timeouts are hang guards
// and count as failures.
//
//	go build -o "$env:TEMP\dfx2-webview.exe" .\examples\webview
//	go build -o "$env:TEMP\dfx2-wvdrive.exe" .\evidence\webview-integration\windows\drive
//	& "$env:TEMP\dfx2-wvdrive.exe" -exe "$env:TEMP\dfx2-webview.exe" -out evidence\webview-integration\windows\stage-3\run
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

var (
	exe     = flag.String("exe", "", "the webview example executable")
	out     = flag.String("out", "evidence/webview-integration/windows/stage-3/run", "directory for logs, results and captures")
	static  = flag.String("static", "examples/webview/static", "the audition page's files, served by the attachment service")
	only    = flag.String("only", "", "comma-separated case names (default: all)")
	timeout = flag.Duration("step-timeout", 40*time.Second, "hang guard for each expected event or report")
)

type check struct {
	Check  string         `json:"check"`
	OK     bool           `json:"ok"`
	Detail map[string]any `json:"detail,omitempty"`
}

type result struct {
	Case     string         `json:"case"`
	OK       bool           `json:"ok"`
	Failure  string         `json:"failure,omitempty"`
	Untested string         `json:"untested,omitempty"` // the case could not run here, and why
	Trace    string         `json:"trace,omitempty"`
	Checks   []check        `json:"checks"`
	Detail   map[string]any `json:"detail,omitempty"`
	Hosts    []hostExit     `json:"hosts"`
	Seconds  float64        `json:"seconds"`
}

type hostExit struct {
	Host        string   `json:"host"`
	Exit        int      `json:"exit"`
	Error       string   `json:"error"`
	Stderr      []string `json:"stderr,omitempty"`
	Diagnostics []string `json:"shutdown_diagnostics,omitempty"`
	Leftovers   []string `json:"temp_leftovers"`
	BrowserPIDs []uint32 `json:"browser_pids,omitempty"`
	BrowsersUp  []uint32 `json:"browser_processes_remaining,omitempty"`
	Problems    []string `json:"problems,omitempty"`
}

// matrix runs the cases, one fresh host per case (or more), each in its own
// temporary directory under the run's scratch root.
type matrix struct {
	outDir, scratch string
	hosts           []*host
	results         []*result
	current         *result
	runtime         string
	pids            map[*host]map[uint32]bool
}

func (m *matrix) check(name string, ok bool, kv ...any) bool {
	c := check{Check: name, OK: ok, Detail: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		c.Detail[kv[i].(string)] = kv[i+1]
	}
	m.current.Checks = append(m.current.Checks, c)
	return ok
}

func (m *matrix) detail(k string, v any) { m.current.Detail[k] = v }

// untested marks the current case as not executed on this host, with the
// reason; it is neither a pass nor a failure.
func (m *matrix) untested(reason string) { m.current.Untested = reason }

// host starts a resident host by default: a window application with no
// window after setup exits at once, by the last-window rule.
func (m *matrix) host(name string, resident bool, env []string, flags ...string) *host {
	tmp := filepath.Join(m.scratch, m.current.Case, name)
	h := startHost(m.outDir, tmp, m.current.Case+"-"+name, resident, env, flags...)
	m.hosts = append(m.hosts, h)
	m.pids[h] = map[uint32]bool{}
	return h
}

// note remembers the browser processes a host reported, so their exit can be
// checked after the host's.
func (m *matrix) note(h *host, s event) {
	for _, w := range s.sub("windows") {
		if pid := event(w.(map[string]any)).num("browser_pid"); pid != 0 {
			m.pids[h][uint32(pid)] = true
		}
		if rt := event(w.(map[string]any)).sub("browser").str("Runtime"); rt != "" && m.runtime == "" {
			m.runtime = rt
		}
	}
}

// done ends a host and checks its exit, stderr, browser processes and
// temporary storage. expectError names a failure Run must report.
func (m *matrix) done(h *host, expectError string, kill bool) hostExit {
	if h.running() && !kill {
		if s := h.tryStatus(); s != nil {
			m.note(h, s)
		}
	}
	code, ret := h.finish(kill)
	x := hostExit{Host: h.name, Exit: code, Error: ret}
	for pid := range m.pids[h] {
		x.BrowserPIDs = append(x.BrowserPIDs, pid)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		x.BrowsersUp = nil
		for _, pid := range x.BrowserPIDs {
			if processAlive(pid) {
				x.BrowsersUp = append(x.BrowsersUp, pid)
			}
		}
		if len(x.BrowsersUp) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(m.outDir, h.name+".stderr")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			switch l = strings.TrimSpace(l); {
			case l == "":
			case strings.Contains(l, "dfx2 shutdown outstanding"):
				// The supervisor's five-second diagnostic: owned work still
				// pending, reported as designed. Recorded, not a failure.
				x.Diagnostics = append(x.Diagnostics, l)
			default:
				x.Stderr = append(x.Stderr, l)
			}
		}
	}
	x.Leftovers = tempEntries(h.tmp)
	switch {
	case kill:
	case expectError == "":
		if code != 0 || ret != "<nil>" {
			x.Problems = append(x.Problems, fmt.Sprintf("exit %d error %s", code, ret))
		}
		if len(x.Stderr) != 0 {
			x.Problems = append(x.Problems, "stderr not empty")
		}
	default:
		if code == 0 || !strings.Contains(ret, expectError) {
			x.Problems = append(x.Problems, fmt.Sprintf("expected failure %q, got exit %d error %s", expectError, code, ret))
		}
	}
	for _, l := range x.Stderr {
		if strings.Contains(l, "panic") {
			x.Problems = append(x.Problems, "panic on stderr")
		}
	}
	if len(x.BrowsersUp) != 0 {
		x.Problems = append(x.Problems, fmt.Sprintf("browser processes remain: %v", x.BrowsersUp))
	}
	if len(x.Leftovers) != 0 && !kill {
		x.Problems = append(x.Problems, fmt.Sprintf("temporary storage left: %v", x.Leftovers))
	}
	m.current.Hosts = append(m.current.Hosts, x)
	require(len(x.Problems) == 0, "%s: %s", h.name, strings.Join(x.Problems, "; "))
	return x
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

func (m *matrix) run(name string, body func()) {
	if !wanted(name) {
		return
	}
	r := &result{Case: name, Checks: []check{}, Detail: map[string]any{}}
	m.current = r
	m.hosts = nil
	started := time.Now()
	func() {
		defer func() {
			if p := recover(); p != nil {
				if f, ok := p.(failure); ok {
					r.Failure = f.msg
				} else {
					r.Failure = fmt.Sprint("panic: ", p)
					r.Trace = string(debug.Stack())
				}
				for _, h := range m.hosts {
					if h.running() {
						h.finish(true)
					}
				}
			}
		}()
		body()
	}()
	r.Seconds = float64(time.Since(started).Round(100*time.Millisecond)) / float64(time.Second)
	r.OK = r.Failure == ""
	for _, c := range r.Checks {
		r.OK = r.OK && c.OK
	}
	b, _ := json.MarshalIndent(r, "", " ")
	os.WriteFile(filepath.Join(m.outDir, name+".result.json"), append(b, '\n'), 0o644)
	m.results = append(m.results, r)
	var failed []string
	for _, c := range r.Checks {
		if !c.OK {
			failed = append(failed, c.Check)
		}
	}
	line, _ := json.Marshal(map[string]any{"case": name, "ok": r.OK, "failure": r.Failure, "untested": r.Untested, "failed_checks": failed, "seconds": r.Seconds})
	fmt.Println(string(line))
}

func environment() map[string]any {
	env := map[string]any{"time": time.Now().Format(time.RFC3339), "exe": *exe, "go": strings.TrimSpace(output("go", "version"))}
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

func main() {
	flag.Parse()
	pSetProcessDpiContext.Call(^uintptr(3)) // per-monitor v2, so metrics are in pixels
	if *exe == "" {
		fmt.Fprintln(os.Stderr, "-exe is required: the built examples/webview executable")
		os.Exit(2)
	}
	if abs, err := filepath.Abs(*exe); err == nil {
		*exe = abs
	}
	if abs, err := filepath.Abs(*static); err == nil {
		*static = abs
	}
	if _, err := os.Stat(filepath.Join(*static, "index.html")); err != nil {
		fmt.Fprintln(os.Stderr, "the audition page's files were not found; run from the repository root or pass -static:", err)
		os.Exit(2)
	}
	os.MkdirAll(*out, 0o755)
	scratch, err := os.MkdirTemp("", "dfx2-wv-matrix-")
	if err != nil {
		panic(err)
	}
	m := &matrix{outDir: *out, scratch: scratch, pids: map[*host]map[uint32]bool{}}
	envb, _ := json.MarshalIndent(environment(), "", " ")
	os.WriteFile(filepath.Join(*out, "environment.json"), append(envb, '\n'), 0o644)
	cases := []struct {
		name string
		body func(*matrix)
	}{
		{"owned_basics", ownedBasics}, {"in_process_alone", inProcessAlone}, {"confinement", confinement}, {"storage", storage},
		{"error_pages_loopback", errorPagesLoopback}, {"error_actions_same_uri", errorActionsSameURI}, {"attachment", attachment},
		{"response_race", responseRace}, {"close_cases", closeCases}, {"close_one_keep_one", closeOneKeepOne},
		{"lazy_loading", lazyLoading}, {"profile_lifecycle", profileLifecycle}, {"size_move", sizeMove},
		{"browser_failure", browserFailure}, {"missing_runtime", missingRuntime}, {"exe_only", exeOnly},
	}
	for _, c := range cases {
		m.run(c.name, func() { c.body(m) })
	}
	passed := true
	var failed, untested []string
	for _, r := range m.results {
		if r.Untested != "" {
			untested = append(untested, r.Case)
			continue
		}
		if !r.OK {
			passed = false
			failed = append(failed, r.Case)
		}
	}
	summary := map[string]any{"passed": passed, "untested": untested, "runtime": m.runtime, "session_state": sessionState(), "cases": m.results}
	b, _ := json.MarshalIndent(summary, "", " ")
	os.WriteFile(filepath.Join(*out, "summary.json"), append(b, '\n'), 0o644)
	os.RemoveAll(scratch)
	line, _ := json.Marshal(map[string]any{"passed": passed, "failed": failed, "untested": untested, "runtime": m.runtime})
	fmt.Println(string(line))
	if !passed {
		os.Exit(1)
	}
}
