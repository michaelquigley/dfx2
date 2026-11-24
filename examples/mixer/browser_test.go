package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// A browser view's endpoints, over the running mixer with its two native
// windows: polls show the model, edits travel the same ordered command path as
// native edits, and showing a snapshot never submits.

func (b *bench) state(h http.Handler, query string) stateJSON {
	b.t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state"+query, nil))
	if rec.Code != http.StatusOK {
		b.t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	var s stateJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		b.t.Fatal(err)
	}
	return s
}

func (b *bench) post(h http.Handler, path, body string) (int, map[string]any) {
	b.t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func channelOf(s stateJSON, id string) channelJSON {
	for _, c := range s.Channels {
		if c.ID == id {
			return c
		}
	}
	return channelJSON{}
}

func TestBrowserEditsTravelTheOrderedCommandPathBothWays(t *testing.T) {
	b := runMixer(t)
	h := b.m.handler("W")
	s := b.state(h, "")
	if s.View != "W" || len(s.Channels) != 6 || s.Channels[0].Name != "Kick" || channelOf(s, "a").ShownGain != 0.5 {
		t.Fatalf("first poll: %+v", s)
	}

	// Browser to native: the edit is a command, pending everywhere until the
	// device acknowledges it.
	code, out := b.post(h, "/api/edit", `{"channel":"a","control":"gain","value":0.3}`)
	if code != http.StatusOK || out["accepted"] != true || out["sequence"] != 1.0 {
		t.Fatalf("edit: %d %v", code, out)
	}
	b.waitIdle()
	b.wantShown("A", "a", 0.3, false)
	b.wantShown("B", "a", 0.3, false)
	if c := channelOf(b.state(h, ""), "a"); !c.GainPending || c.PendingGain != 0.3 || c.GainSeq != 1 || c.Gain != 0.5 || c.ShownGain != 0.3 {
		t.Fatalf("pending in the browser view: %+v", c)
	}
	b.ack(1)
	if c := channelOf(b.state(h, ""), "a"); c.GainPending || c.Gain != 0.3 {
		t.Fatalf("applied: %+v", c)
	}

	// Native to browser: a native toggle is pending in the browser view too.
	b.input("A", keyDown(platform.KeyTab), keyDown(platform.KeyTab))
	b.input("A", space()...)
	if c := channelOf(b.state(h, ""), "a"); !c.MutePending || !c.PendingMute || !c.ShownMute || c.Muted {
		t.Fatalf("native edit in the browser view: %+v", c)
	}
	b.ack(2)

	// A device rejection is shown to both kinds of view.
	b.m.model.setRejectNext(1)
	b.waitIdle()
	b.post(h, "/api/edit", `{"channel":"b","control":"gain","value":0.9}`)
	b.ack(3)
	s = b.state(h, "")
	if c := channelOf(s, "b"); c.GainPending || c.Gain != 0.5 || !strings.Contains(s.Error, "#3 b gain 0.90 rejected") || s.Commands.Rejected != 1 {
		t.Fatalf("rejection: %+v %q", c, s.Error)
	}
	b.wantLabel("A", "#3 b gain 0.90 rejected")

	// Polling never submits.
	for range 20 {
		b.state(h, "")
	}
	b.wantRecords(nil, "#1 accepted gain=0.30", "#1 applied gain=0.30", "#2 accepted mute=on", "#2 applied mute=on",
		"#3 accepted gain=0.90", "#3 rejected gain=0.90")
	if !strings.Contains(b.log.String(), `"window":"W"`) {
		t.Fatal("the browser view's edits are not attributed to it")
	}
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
	// After shutdown a browser edit is refused at once, never queued.
	if _, out := b.post(h, "/api/edit", `{"channel":"a","control":"mute","value":true}`); out["accepted"] != false || out["error"] != errShuttingDown.Error() {
		t.Fatalf("edit after shutdown: %v", out)
	}
}

func TestReorderAndRemovalReachTheBrowserView(t *testing.T) {
	b := runMixer(t)
	h := b.m.handler("W")
	for _, line := range []string{"reorder", "remove c"} {
		if _, err := b.m.model.external(line); err != nil {
			t.Fatal(err)
		}
	}
	s := b.state(h, "")
	var order []string
	for _, c := range s.Channels {
		order = append(order, c.ID)
	}
	if strings.Join(order, "") != "fedba" {
		t.Fatalf("order %v", order)
	}
	// An edit for the removed channel still travels the queue; the device
	// rejects it, as for a native view.
	b.post(h, "/api/edit", `{"channel":"c","control":"mute","value":true}`)
	if r := b.ack(1); r.State != "rejected" || r.Reason != "channel removed" {
		t.Fatalf("record %+v", r)
	}
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserEditsAreValidatedAndReportsNeedTheAudition(t *testing.T) {
	b := runMixer(t)
	h := b.m.handler("W")
	for _, body := range []string{`{`, `{"channel":"a","control":"gain","value":1.5}`, `{"channel":"a","control":"gain","value":"x"}`,
		`{"channel":"a","control":"pan","value":0.5}`, `{"channel":"","control":"mute","value":true}`,
		`{"channel":"abcdefghi","control":"mute","value":true}`, `{"channel":"a","control":"mute","value":1}`} {
		if code, _ := b.post(h, "/api/edit", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/edit", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET edit: %d", rec.Code)
	}
	if code, _ := b.post(h, "/api/report", `{}`); code != http.StatusNotFound {
		t.Errorf("report without -audition: %d", code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `src="mixer.js"`) {
		t.Errorf("page: %d", rec.Code)
	}
	b.wantRecords(nil)
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// The attachment fixture serves every attached window; each page names its
// view, and a name that is not one becomes "attach".
func TestAttachedPagesNameThemselves(t *testing.T) {
	b := runMixer(t)
	h := b.m.handler("")
	if s := b.state(h, "?view=C"); s.View != "C" {
		t.Fatalf("view %q", s.View)
	}
	if s := b.state(h, "?view=%3Cscript%3E"); s.View != "attach" {
		t.Fatalf("view %q", s.View)
	}
	b.post(h, "/api/edit?view=C", `{"channel":"a","control":"mute","value":true}`)
	b.ack(1)
	if !strings.Contains(b.log.String(), `"window":"C"`) {
		t.Fatal("the attached view's edit is not attributed to it")
	}
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// The attach fixture server is the application's, not a window's: it serves
// while windows close and stops when the application drains.
func TestTheAttachFixtureServerOutlivesWindowsAndStopsWithTheApplication(t *testing.T) {
	b := runMixer(t)
	b.m.c.attachAddr = "127.0.0.1:0"
	var err error
	b.onUIThread(func() { err = b.m.startFixture(b.app) })
	if err != nil {
		t.Fatal(err)
	}
	get := func() (int, error) {
		r, err := http.Get(b.m.fixture + "/api/state?view=C")
		if err != nil {
			return 0, err
		}
		r.Body.Close()
		return r.StatusCode, nil
	}
	if code, err := get(); err != nil || code != http.StatusOK {
		t.Fatalf("fixture: %d %v", code, err)
	}
	b.onUIThread(func() { b.m.windows["A"].ui.Close() })
	b.waitIdle()
	if code, err := get(); err != nil || code != http.StatusOK {
		t.Fatalf("fixture after a window closed: %d %v", code, err)
	}
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := get(); err == nil {
		t.Fatal("the fixture server outlived the application")
	}
}

// Shutdown never declares the fixture drained early: with a handler held
// inside a request, the application cannot finish until it returns, however
// long that takes, and the held request still completes.
func TestTheAttachFixtureServerWaitsForAHeldHandler(t *testing.T) {
	b := runMixer(t)
	b.m.c.attachAddr = "127.0.0.1:0"
	var err error
	b.onUIThread(func() { err = b.m.startFixture(b.app) })
	if err != nil {
		t.Fatal(err)
	}
	// The edit's publication holds the handler; every later publication, such
	// as the worker's cancellations at shutdown, passes straight through.
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	b.m.model.mu.Lock()
	b.m.model.collected = func() {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	b.m.model.mu.Unlock()
	answered := make(chan int, 1)
	go func() {
		r, err := http.Post(b.m.fixture+"/api/edit?view=C", "application/json", strings.NewReader(`{"channel":"a","control":"mute","value":true}`))
		if err != nil {
			answered <- 0
			return
		}
		r.Body.Close()
		answered <- r.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(hang):
		t.Fatal("the request never reached the held publication")
	}
	b.onUIThread(func() {
		for _, w := range b.m.windows {
			w.ui.Close()
		}
	})
	select {
	case err := <-b.done:
		t.Fatalf("Run returned while a fixture handler was still running: %v", err)
	case <-time.After(3 * time.Second): // beyond any shutdown deadline
	}
	close(release)
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("the held request was not completed: %d", code)
	}
	if err := b.wait(); err != nil {
		t.Fatal(err)
	}
}

// A fixture task the application refuses leaves no listener behind.
func TestARefusedFixtureTaskClosesItsListener(t *testing.T) {
	b := runMixer(t)
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
	b.m.c.attachAddr = "127.0.0.1:0"
	if err := b.m.startFixture(b.app); err == nil {
		t.Fatal("a drained application accepted the fixture task")
	}
	if c, err := net.DialTimeout("tcp", strings.TrimPrefix(b.m.fixture, "http://"), time.Second); err == nil {
		c.Close()
		t.Fatal("the refused fixture's listener is still open")
	}
}

// When Serve fails on its own, the task still drains what the server admitted:
// with a handler held, the application cannot finish until it returns, and
// Run reports the serving failure.
func TestAFailedFixtureServerDrainsItsHandlers(t *testing.T) {
	b := runMixer(t)
	var listener net.Listener
	listenFixture = func(network, address string) (net.Listener, error) {
		l, err := net.Listen(network, address)
		listener = l
		return l, err
	}
	t.Cleanup(func() { listenFixture = net.Listen })
	b.m.c.attachAddr = "127.0.0.1:0"
	var err error
	b.onUIThread(func() { err = b.m.startFixture(b.app) })
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	b.m.model.mu.Lock()
	b.m.model.collected = func() {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	b.m.model.mu.Unlock()
	answered := make(chan int, 1)
	go func() {
		r, err := http.Post(b.m.fixture+"/api/edit?view=C", "application/json", strings.NewReader(`{"channel":"a","control":"mute","value":true}`))
		if err != nil {
			answered <- 0
			return
		}
		r.Body.Close()
		answered <- r.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(hang):
		t.Fatal("the request never reached the held publication")
	}
	listener.Close() // Serve fails underneath the held handler
	select {
	case err := <-b.done:
		t.Fatalf("Run returned while a fixture handler was still running: %v", err)
	case <-time.After(time.Second):
	}
	close(release)
	if code := <-answered; code != http.StatusOK {
		t.Fatalf("the held request was not completed: %d", code)
	}
	select {
	case err := <-b.done:
		if err == nil || !strings.Contains(err.Error(), "attach fixture server") {
			t.Fatalf("Run did not report the serving failure: %v", err)
		}
	case <-time.After(hang):
		t.Fatal("Run did not return after the handler finished")
	}
}

// A client that sends its headers and then stalls its body is bounded by the
// fixture's read timeout: the handler's read fails and it returns, so shutdown,
// which still waits for every handler, can finish.
func TestAStalledFixtureBodyCannotHoldShutdownOpen(t *testing.T) {
	b := runMixer(t)
	fixtureReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { fixtureReadTimeout = 10 * time.Second })
	b.m.c.attachAddr = "127.0.0.1:0"
	var err error
	b.onUIThread(func() { err = b.m.startFixture(b.app) })
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", strings.TrimPrefix(b.m.fixture, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Complete headers, then 5 of the 50 declared body bytes, then nothing.
	if _, err := io.WriteString(c, "POST /api/edit?view=C HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 50\r\n\r\n{\"cha"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the handler is reading the body
	b.onUIThread(func() {
		for _, w := range b.m.windows {
			w.ui.Close()
		}
	})
	select {
	case err := <-b.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled request body held the application open")
	}
	b.wantRecords(nil) // the incomplete edit was never submitted
}
