//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type event map[string]any

func (e event) str(k string) string   { s, _ := e[k].(string); return s }
func (e event) num(k string) float64  { f, _ := e[k].(float64); return f }
func (e event) boolean(k string) bool { b, _ := e[k].(bool); return b }
func (e event) sub(k string) event    { m, _ := e[k].(map[string]any); return event(m) }
func (e event) list(k string) []event { return events(e[k]) }
func (e event) has(k string) bool     { _, ok := e[k]; return ok }
func events(v any) (out []event) {
	l, _ := v.([]any)
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, event(m))
		}
	}
	return out
}

type failure struct{ msg string }

func (f failure) Error() string { return f.msg }

func fail(format string, args ...any) { panic(failure{fmt.Sprintf(format, args...)}) }

func require(ok bool, format string, args ...any) {
	if !ok {
		fail(format, args...)
	}
}

// host is one audition process. Each host gets its own TEMP, so the profile
// and loader directories it leaves are its own to find.
type host struct {
	name    string
	tmp     string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	log     *os.File
	mu      sync.Mutex
	cond    *sync.Cond
	all     []event
	done    chan struct{}
	exit    int
	exitErr string
	last    int
	pid     int
}

func startHost(out, tmp, name string, resident bool, env []string, flags ...string) *host {
	h := &host{name: name, tmp: tmp, done: make(chan struct{})}
	h.cond = sync.NewCond(&h.mu)
	os.MkdirAll(tmp, 0o755)
	args := flags
	if resident {
		args = append([]string{"-resident"}, args...)
	}
	h.cmd = exec.Command(*exe, args...)
	h.cmd.Env = append(append(os.Environ(), "TEMP="+tmp, "TMP="+tmp), env...)
	h.cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	stderr, err := os.Create(filepath.Join(out, name+".stderr"))
	if err != nil {
		panic(err)
	}
	h.cmd.Stderr = stderr
	h.stdin, err = h.cmd.StdinPipe()
	if err != nil {
		panic(err)
	}
	h.log, err = os.Create(filepath.Join(out, name+".jsonl"))
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(h.log, "# %s %s (TEMP=%s)\n", filepath.Base(*exe), strings.Join(args, " "), tmp)
	if err := h.cmd.Start(); err != nil {
		panic(err)
	}
	h.pid = h.cmd.Process.Pid
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(h.log, line)
			var e event
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			h.mu.Lock()
			h.all = append(h.all, e)
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
	h.expect("ready", 0, 60*time.Second)
	return h
}

// send writes a command and returns the event mark before it.
func (h *host) send(line string) int {
	h.mu.Lock()
	mark := len(h.all)
	h.mu.Unlock()
	fmt.Fprintf(h.log, "{\"command\":%q}\n", line)
	fmt.Fprintln(h.stdin, line)
	return mark
}

func (h *host) mark() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.all)
}

// expect returns the first event named name at or after since whose fields
// match; nothing is consumed. kv alternates field names and values. A host
// "error" event in the range fails the case unless the caller expects one.
func (h *host) expect(name string, since int, to time.Duration, kv ...any) event {
	deadline := time.Now().Add(to)
	timer := time.AfterFunc(to, func() { h.mu.Lock(); h.cond.Broadcast(); h.mu.Unlock() })
	defer timer.Stop()
	h.mu.Lock()
	defer h.mu.Unlock()
	i := since
	for {
		for ; i < len(h.all); i++ {
			e := h.all[i]
			if e.str("event") == name && matches(e, kv) {
				h.last = i + 1
				return e
			}
			if e.str("event") == "error" && name != "error" {
				fail("host error: %v", e)
			}
		}
		if time.Now().After(deadline) {
			fail("%s: no %s %v within %v", h.name, name, kv, to)
		}
		select {
		case <-h.done:
			fail("%s exited before %s %v", h.name, name, kv)
		default:
		}
		h.cond.Wait()
	}
}

// quiet fails if a matching event arrives within d.
func (h *host) quiet(name string, since int, d time.Duration, kv ...any) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(h.events(name, since, kv...)) > 0 {
			fail("%s: unexpected %s %v", h.name, name, kv)
		}
		time.Sleep(100 * time.Millisecond)
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

// status asks the host for its snapshot.
func (h *host) status() event {
	return h.expect("status", h.send("status"), *timeout)
}

func (h *host) window(label string) event { return h.status().sub("windows").sub(label) }

// browserEvents returns a window's attachment events, oldest first.
func (h *host) browserEvents(label string) []event {
	return h.window(label).sub("browser").list("Events")
}

// open opens a window and waits until its first page has loaded: driven
// navigation is refused while the browser initializes.
func (h *host) open(label, mode string, rest ...string) string {
	m := h.send(strings.TrimSpace("open " + label + " " + mode + " " + strings.Join(rest, " ")))
	start := h.expect("opened", m, 30*time.Second, "window", label).str("start")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		w := h.window(label)
		if w.sub("browser").boolean("Navigated") && strings.HasPrefix(w.str("uri"), start) && w.str("title") != "" {
			return start
		}
		time.Sleep(200 * time.Millisecond)
	}
	fail("%s: window %s did not load %s", h.name, label, start)
	return ""
}

// openClose opens a window that closes itself at an initialization stage.
func (h *host) openClose(label, mode, stage string) event {
	m := h.send("open-close " + label + " " + mode + " " + stage)
	return h.expect("opened", m, 30*time.Second, "window", label)
}

// run loads the page as ?run=NAME and returns its report.
func (h *host) run(label, start, name string, query map[string]string) event {
	id := fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
	u := start + "?run=" + name + "&id=" + id
	for k, v := range query {
		u += "&" + k + "=" + queryEscape(v)
	}
	m := h.send("go " + label + " " + u)
	return h.awaitReport(label, id, m)
}

func (h *host) awaitReport(label, id string, mark int) event {
	deadline := time.Now().Add(*timeout)
	for {
		e := h.expect("report", mark, time.Until(deadline), "window", label)
		r := e.sub("report")
		if r.str("id") == id {
			require(r.str("error") == "", "report %s: %s", id, r.str("error"))
			return r
		}
		mark = h.last
	}
}

// waitBrowserEvent polls a window.s attachment events for a kind numbered
// after `after`, matching an optional predicate; it returns the event and its
// sequence number. The host keeps only recent events, so sequence numbers,
// not positions, tell new events from seen ones.
func (h *host) waitBrowserEvent(label, kind string, after uint64, match func(event) bool) (event, uint64) {
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		for _, e := range h.browserEvents(label) {
			if uint64(e.num("seq")) > after && e.str("kind") == kind && (match == nil || match(e)) {
				return e, uint64(e.num("seq"))
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	fail("%s: window %s had no %s event", h.name, label, kind)
	return nil, 0
}

func (h *host) waitTitle(label, title string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if h.window(label).str("title") == title {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	fail("%s: window %s never showed %q (title %q)", h.name, label, title, h.window(label).str("title"))
}

func (h *host) close(label string) event {
	return h.expect("closed", h.send("close "+label), 60*time.Second, "window", label)
}

func (h *host) running() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// finish quits the host (unless it already exited or kill is set) and
// returns its exit code and Run's result.
func (h *host) finish(kill bool) (int, string) {
	if kill {
		h.cmd.Process.Kill()
	} else if h.running() {
		h.send("quit")
	}
	select {
	case <-h.done:
	case <-time.After(60 * time.Second):
		h.exitErr = "timed out: killed"
		h.cmd.Process.Kill()
		<-h.done
	}
	h.stdin.Close()
	h.log.Close()
	ret := ""
	if r := h.events("return", 0); len(r) > 0 {
		ret = r[len(r)-1].str("error")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exit, ret
}

// seq is the sequence number of a window's latest attachment event.
func (h *host) seq(label string) uint64 {
	var last uint64
	for _, e := range h.browserEvents(label) {
		last = max(last, uint64(e.num("seq")))
	}
	return last
}

// tryStatus asks for a snapshot but tolerates a host that exits meanwhile.
func (h *host) tryStatus() (s event) {
	defer func() {
		if recover() != nil {
			s = nil
		}
	}()
	if len(h.events("return", 0)) > 0 {
		return nil
	}
	return h.status()
}
