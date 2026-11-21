package webview2

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/endpoint"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// readEndpointHost forwards request ownership to the real endpoint. These
// aborted reads must never start a handler or publish a response.
type readEndpointHost struct {
	fakeHost
	t       *testing.T
	f       *endpoint.Finite
	drained int
}

func (h *readEndpointHost) Request(r platform.BrowserRequest) platform.RequestDecision {
	return h.f.Request(r)
}
func (h *readEndpointHost) RequestBody(id platform.ID, b []byte) platform.RequestDecision {
	return h.f.RequestBody(id, b)
}
func (h *readEndpointHost) RequestBodyEnd(id platform.ID, err error) platform.RequestDecision {
	return h.f.RequestBodyEnd(id, err)
}
func (h *readEndpointHost) RequestReleased(id platform.ID) {
	h.fakeHost.RequestReleased(id)
	h.f.RequestReleased(id)
}
func (h *readEndpointHost) Begin() (platform.ID, error) {
	h.t.Fatal("aborted read started a handler")
	return 0, nil
}
func (h *readEndpointHost) Publish(platform.ID, func(bool) error) {
	h.t.Error("aborted read published a completion")
}
func (h *readEndpointHost) Respond(platform.ID, platform.BrowserResponse) bool {
	h.t.Error("aborted read published a response")
	return false
}
func (h *readEndpointHost) Fail(err error) { h.t.Errorf("endpoint failed: %v", err) }
func (h *readEndpointHost) Drained()       { h.drained++ }

func newReadEndpoint(t *testing.T) (*readEndpointHost, context.CancelFunc) {
	t.Helper()
	f, err := endpoint.NewFinite(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("aborted read reached handler") }), endpoint.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	h := &readEndpointHost{t: t, f: f}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := f.Start(ctx, h, &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err != nil {
		t.Fatal(err)
	}
	f.Bind("https://dfx2.invalid:443")
	return h, cancel
}

func TestBodyReadAbortReleasesRealEndpoint(t *testing.T) {
	for _, name := range []string{"read failure", "cancel at EOF", "cancel with final bytes"} {
		t.Run(name, func(t *testing.T) {
			native := installFake(t)
			h, cancel := newReadEndpoint(t)
			b := &browser{host: h}
			d := h.Request(platform.BrowserRequest{Method: "POST", URL: "https://dfx2.invalid/body", BodyLength: -1})
			if d.Action != platform.RequestRead || d.ID == 0 {
				t.Fatalf("not admitted for reading: %+v", d)
			}
			var calls atomic.Int32
			n.streamRead = func(_ uintptr, buf []byte) (int, bool, hresult) {
				if calls.Add(1) != 1 {
					t.Error("read continued after abort")
				}
				switch name {
				case "read failure":
					return 0, false, eFail
				case "cancel with final bytes":
					buf[0] = 'x'
					return 1, true, sOK
				}
				return 0, true, sOK
			}
			if !b.act(701, 700, nil, d) {
				t.Fatal("body read not started")
			}
			// The worker's result is posted; the window closes its endpoint
			// before the UI thread takes the result to it.
			native.awaitPost()
			if name != "read failure" {
				cancel()
				h.f.Close()
				if h.drained != 0 {
					t.Fatal("endpoint drained before native release")
				}
			}
			native.pump(func() bool { return len(b.reads) == 0 })
			if calls.Load() != 1 || len(h.releasedR) != 1 || h.releasedR[0] != d.ID {
				t.Fatalf("read calls=%d released=%v, want [%d]", calls.Load(), h.releasedR, d.ID)
			}
			for _, field := range []string{"admitted=0/", "native=0 ", "reserved=0/"} {
				if !strings.Contains(h.f.Status(), field) {
					t.Fatalf("ownership remains: %s", h.f.Status())
				}
			}
			cancel()
			h.f.Close()
			b.Close()
			native.flush()
			b.Close()
			if h.drained != 1 || len(h.releasedR) != 1 {
				t.Fatalf("drains=%d releases=%v", h.drained, h.releasedR)
			}
			if native.count("put-response") != 0 || len(native.deferrals) != 0 || native.count("release body stream") != 1 {
				t.Fatalf("abort installed native work: %v", native.calls)
			}
		})
	}
}

func TestInitialAbortWithoutIDReleasesNothing(t *testing.T) {
	native := installFake(t)
	h, cancel := newReadEndpoint(t)
	cancel()
	h.f.Close()
	d := h.Request(platform.BrowserRequest{Method: "POST", URL: "https://dfx2.invalid/body", BodyLength: -1})
	if d.Action != platform.RequestAbort || d.ID != 0 {
		t.Fatalf("not an ID-less refusal: %+v", d)
	}
	b := &browser{host: h}
	b.act(701, 0, nil, d)
	b.Close()
	native.flush()
	if len(h.releasedR) != 0 || h.drained != 1 {
		t.Fatalf("released=%v drained=%d", h.releasedR, h.drained)
	}
}

// With the real endpoint: a close during a held read keeps the request's
// admission, read-buffer reservation and native record until the worker's
// read actually returns; only then does the endpoint drain.
func TestCloseDuringReadHoldsEndpointAdmissionUntilTheWorkerReturns(t *testing.T) {
	h, cancel := newReadEndpoint(t)
	f, b := newTestBrowser(t, &h.fakeHost, platform.BrowserInProcess)
	h.fakeHost.ready = true
	b.host = h
	started(t, f, b)
	f.gate = make(chan struct{})
	bodyRequest(f, "abcdefgh")
	b.receive(550)
	f.awaitRead() // close lands during a claimed read
	held := h.f.Status()
	if !strings.Contains(held, "admitted=1/") || !strings.Contains(held, "reading=1") || strings.Contains(held, "reserved=0/") {
		t.Fatalf("not admitted for reading: %s", held)
	}
	// The core's close order: the window context, the endpoint, the browser.
	cancel()
	h.f.Close()
	b.Close()
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	ownership := func(s string) string { // admission, native record, reading and reservation
		i, j := strings.Index(s, "admitted="), strings.Index(s, " closed=")
		return s[i:j]
	}
	if s := h.f.Status(); ownership(s) != ownership(held) || h.drained != 0 || h.fakeHost.released != 0 {
		t.Fatalf("ownership released before the read returned: %s (drained %d, released %d)", s, h.drained, h.fakeHost.released)
	}
	close(f.gate)
	f.pump(func() bool { return len(b.reads) == 0 && h.fakeHost.released == 1 })
	for _, field := range []string{"admitted=0/", "native=0 ", "reading=0", "reserved=0/"} {
		if !strings.Contains(h.f.Status(), field) {
			t.Fatalf("ownership remains after the worker returned: %s", h.f.Status())
		}
	}
	if h.drained != 1 || len(h.releasedR) != 1 || f.count("release body stream") != 1 || f.count("deferral-complete") != 1 {
		t.Fatalf("drained %d released %v stream %d deferrals %d", h.drained, h.releasedR, f.count("release body stream"), f.count("deferral-complete"))
	}
}

// With the real endpoint: close stops a grant the worker has queued but not
// yet claimed, whether the first or a later one. The unclaimed grant is never
// read; the worker still holds its stream, and the endpoint its admission,
// until the worker returns.
func TestCloseStopsQueuedGrantsBeforeTheyAreClaimed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reads int // reads claimed before close
	}{{"initial grant", 0}, {"later grant", 1}, {"posted result", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			h, cancel := newReadEndpoint(t)
			f, b := newTestBrowser(t, &h.fakeHost, platform.BrowserInProcess)
			h.fakeHost.ready = true
			b.host = h
			started(t, f, b)
			bodyRequest(f, strings.Repeat("x", 100000)) // more than one 64 KiB grant
			release := make(chan struct{})
			switch tc.name {
			case "initial grant":
				f.startGate = release // the worker has not claimed its first grant
				b.receive(560)
			case "posted result":
				f.gate = make(chan struct{})
				b.receive(560)
				f.mu.Lock()
				f.postHold = release // the worker, its first chunk posted and not yet handled
				f.mu.Unlock()
				close(f.gate)
				f.awaitPost()
			default:
				f.gate = make(chan struct{})
				b.receive(560)
				f.mu.Lock()
				f.postHold = release // the worker, after posting its first chunk
				f.mu.Unlock()
				close(f.gate)
				f.awaitPost()
				f.flush() // the endpoint takes the chunk and queues the next grant
				if f.count("read") != 1 || !strings.Contains(h.f.Status(), "reading=1") {
					t.Fatalf("next grant not queued: %s", h.f.Status())
				}
			}
			cancel()
			h.f.Close()
			b.Close()
			if got := closeObservation(b); got != "claimed-reads=0" {
				t.Fatalf("close observed %q: no read was claimed", got)
			}
			f.invoke("BrowserProcessExited", 0, exitKindNormal)
			f.flush() // a posted result is discarded
			if h.drained != 0 || h.fakeHost.released != 0 || len(b.reads) != 1 ||
				!strings.Contains(h.f.Status(), "reading=1") || strings.Contains(h.f.Status(), "reserved=0/") {
				// The read buffer, queued in a grant or posted in a result,
				// is still covered by the request's read reservation.
				t.Fatalf("released before the worker returned: %s (drained %d)", h.f.Status(), h.drained)
			}
			close(release)
			f.pump(func() bool { return len(b.reads) == 0 && h.fakeHost.released == 1 })
			if got := f.count("read"); got != tc.reads {
				t.Fatalf("%d reads, %d claimed before close: a queued grant was read after close", got, tc.reads)
			}
			for _, field := range []string{"admitted=0/", "native=0 ", "reading=0", "reserved=0/"} {
				if !strings.Contains(h.f.Status(), field) {
					t.Fatalf("ownership remains after the worker returned: %s", h.f.Status())
				}
			}
			if h.drained != 1 || len(h.releasedR) != 1 || f.count("release body stream") != 1 || f.count("deferral-complete") != 1 {
				t.Fatalf("drained %d released %v stream %d deferrals %d", h.drained, h.releasedR, f.count("release body stream"), f.count("deferral-complete"))
			}
		})
	}
}

// With the real endpoint: a worker counts as finished only after it has left
// COM. Close lands during a read; with the read returned and the worker held
// inside CoUninitialize, the UI keeps serving while the reader, the
// endpoint's admission and the window's release all stay pending. Once
// uninitialization returns, the worker finishes once and the window releases
// once.
func TestWorkerFinishesOnlyAfterLeavingCOM(t *testing.T) {
	h, cancel := newReadEndpoint(t)
	f, b := newTestBrowser(t, &h.fakeHost, platform.BrowserInProcess)
	h.fakeHost.ready = true
	b.host = h
	started(t, f, b)
	f.gate = make(chan struct{})
	teardown := make(chan struct{})
	f.uninitGate = teardown
	bodyRequest(f, "abcdefgh")
	b.receive(570)
	f.awaitRead()
	cancel()
	h.f.Close()
	b.Close()
	if got := closeObservation(b); got != "claimed-reads=1" {
		t.Fatalf("close observed %q, not the claimed read", got)
	}
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	close(f.gate) // the read returns; the worker releases its stream and leaves COM
	f.awaitCall("uninitialize")
	for range 3 { // play the UI thread while teardown is held
		f.flush()
		time.Sleep(5 * time.Millisecond)
	}
	if len(b.reads) != 1 || h.drained != 0 || h.fakeHost.released != 0 || len(h.releasedR) != 0 ||
		!strings.Contains(h.f.Status(), "admitted=1/") || f.did("worker-finished") {
		t.Fatalf("finished during COM teardown: readers %d drained %d released %d %v: %s",
			len(b.reads), h.drained, h.fakeHost.released, h.releasedR, h.f.Status())
	}
	if f.count("release body stream") != 1 {
		t.Fatal("the stream was not released before leaving COM")
	}
	close(teardown)
	f.pump(func() bool { return len(b.reads) == 0 && h.fakeHost.released == 1 })
	f.flush()
	if f.count("worker-finished") != 1 || f.count("uninitialize") != 1 || h.fakeHost.released != 1 ||
		h.drained != 1 || len(h.releasedR) != 1 || f.count("deferral-complete") != 1 {
		t.Fatalf("finished %d uninitialized %d released %d drained %d requests %v deferrals %d",
			f.count("worker-finished"), f.count("uninitialize"), h.fakeHost.released, h.drained, h.releasedR, f.count("deferral-complete"))
	}
}

// servingHost joins the browser to the real finite endpoint with a handler
// that answers: completions are posted to the fake UI thread, and responses
// go back to the browser as the core would deliver them.
type servingHost struct {
	fakeHost
	t       *testing.T
	f       *endpoint.Finite
	b       *browser
	ops     atomic.Int64
	drained int
	seen    []platform.BrowserRequest // what reached the endpoint
}

func (h *servingHost) Request(r platform.BrowserRequest) platform.RequestDecision {
	h.seen = append(h.seen, r)
	return h.f.Request(r)
}
func (h *servingHost) RequestBody(id platform.ID, b []byte) platform.RequestDecision {
	return h.f.RequestBody(id, b)
}
func (h *servingHost) RequestBodyEnd(id platform.ID, err error) platform.RequestDecision {
	return h.f.RequestBodyEnd(id, err)
}
func (h *servingHost) RequestReleased(id platform.ID) {
	h.fakeHost.RequestReleased(id)
	h.f.RequestReleased(id)
}
func (h *servingHost) Begin() (platform.ID, error) { return platform.ID(h.ops.Add(1)), nil }
func (h *servingHost) Publish(_ platform.ID, settle func(bool) error) {
	n.post(func() { settle(false) })
}
func (h *servingHost) Respond(id platform.ID, r platform.BrowserResponse) bool {
	return h.b.Respond(id, r)
}
func (h *servingHost) Fail(err error) { h.t.Errorf("endpoint failed: %v", err) }
func (h *servingHost) Drained()       { h.drained++ }

// freeStreamBody drops the engine's reference to the response with this body.
func (f *fakeNative) freeStreamBody(body string) {
	f.t.Helper()
	for s, st := range f.streams {
		if string(st.body) == body {
			delete(f.streams, s)
			st.released()
			f.flush()
			return
		}
	}
	f.t.Fatalf("no response %q held", body)
}

// With the real endpoint admitting one request: the slot is held until both
// the native response and the body worker's teardown have finished, in either
// order, and is reported released once. A second request meanwhile gets the
// endpoint's 503; one after the later of the two is admitted.
func TestBodyWorkerAndResponseBothHoldAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readErr   bool // the read fails: aborted without a response
		heldFirst bool // the worker is held in teardown past native delivery
	}{
		{"EOF, response released at once", false, true},
		{"read error", true, true},
		{"worker finishes first", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fe, err := endpoint.NewFinite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("ok " + r.URL.Path))
			}), endpoint.Limits{Requests: 1})
			if err != nil {
				t.Fatal(err)
			}
			h := &servingHost{t: t, f: fe}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if err := fe.Start(ctx, h, &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err != nil {
				t.Fatal(err)
			}
			fe.Bind("https://dfx2.invalid:443")
			f, b := newTestBrowser(t, &h.fakeHost, platform.BrowserInProcess)
			h.fakeHost.ready = true
			b.host, h.b = h, b
			started(t, f, b)
			teardown := make(chan struct{})
			if tc.heldFirst {
				f.uninitGate = teardown
			}
			if tc.readErr {
				n.streamRead = func(uintptr, []byte) (int, bool, hresult) { return 0, true, eFail }
			}
			bodyRequest(f, "abcd")
			b.receive(590)
			first := platform.ID(1)
			if tc.readErr {
				f.pump(func() bool { return f.did("deferral-complete") })
			} else {
				f.pump(func() bool { return f.did("put-response") })
				if !tc.heldFirst {
					f.pump(func() bool { return len(b.reads) == 0 }) // the worker finished; the response is still held
				} else {
					f.freeStreamBody("ok /api/echo") // native delivery finishes first
				}
			}
			if tc.heldFirst {
				f.awaitCall("uninitialize")
			}
			f.flush()
			second := func(path string) int {
				f.request.uri, f.request.method, f.request.body = startURL+path, "GET", nil
				before := len(f.statuses)
				b.receive(591)
				f.pump(func() bool { return len(f.statuses) > before })
				return f.statuses[len(f.statuses)-1]
			}
			if got := second("api/while-held"); got != http.StatusServiceUnavailable {
				t.Fatalf("second request answered %d while the first still held its slot: %s, released %v", got, fe.Status(), h.releasedR)
			}
			f.freeStreamBody(string(failureBody(t, f))) // the refusal, released under its own ID
			releases := func(id platform.ID) (n int) {
				for _, r := range h.releasedR {
					if r == id {
						n++
					}
				}
				return n
			}
			if releases(first) != 0 {
				t.Fatalf("first request reported released early: %v", h.releasedR)
			}
			if tc.heldFirst {
				close(teardown)
				f.pump(func() bool { return len(b.reads) == 0 })
			} else {
				f.freeStreamBody("ok /api/echo")
			}
			f.flush()
			if releases(first) != 1 {
				t.Fatalf("first request released %v, not once", h.releasedR)
			}
			if got := second("api/after"); got != http.StatusOK {
				t.Fatalf("a request after the slot freed answered %d: %s", got, fe.Status())
			}
			f.freeStreamBody("ok /api/after")
			if releases(first) != 1 || !strings.Contains(fe.Status(), "admitted=0/1") {
				t.Fatalf("releases %v: %s", h.releasedR, fe.Status())
			}
		})
	}
}

// failureBody is the body of the most recent fixed refusal the fake holds.
func failureBody(t *testing.T, f *fakeNative) []byte {
	t.Helper()
	for _, st := range f.streams {
		if !strings.HasPrefix(string(st.body), "ok ") {
			return st.body
		}
	}
	t.Fatal("no refusal held")
	return nil
}

// Close settles a request waiting on its handler at once, but while that
// request's body worker is still in teardown, the endpoint's release, and
// with it the slot, waits for the worker.
func TestCloseOwesTheReleaseOfAWaitingRequestToItsWorker(t *testing.T) {
	handler := make(chan struct{})
	fe, err := endpoint.NewFinite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-handler
	}), endpoint.Limits{Requests: 1})
	if err != nil {
		t.Fatal(err)
	}
	h := &servingHost{t: t, f: fe}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := fe.Start(ctx, h, &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err != nil {
		t.Fatal(err)
	}
	fe.Bind("https://dfx2.invalid:443")
	f, b := newTestBrowser(t, &h.fakeHost, platform.BrowserInProcess)
	h.fakeHost.ready = true
	b.host, h.b = h, b
	started(t, f, b)
	teardown := make(chan struct{})
	f.uninitGate = teardown
	bodyRequest(f, "abcd")
	b.receive(595)
	f.pump(func() bool { return b.requests[1] != nil }) // the body ended; the handler runs
	f.awaitCall("uninitialize")
	b.Close()
	if got := closeObservation(b); got != "claimed-reads=0" {
		t.Fatalf("close observed %q: the worker was leaving COM, not reading", got)
	}
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	if f.count("deferral-complete") != 1 || len(h.releasedR) != 0 || !strings.Contains(fe.Status(), "admitted=1/1") || h.released != 0 {
		t.Fatalf("close: deferrals %d released %v %s, window released %d", f.count("deferral-complete"), h.releasedR, fe.Status(), h.released)
	}
	close(teardown)
	f.pump(func() bool { return len(b.reads) == 0 })
	if len(h.releasedR) != 1 || h.releasedR[0] != 1 {
		t.Fatalf("released %v, not once after teardown", h.releasedR)
	}
	close(handler)
	f.pump(func() bool { return strings.Contains(fe.Status(), "admitted=0/1") && h.released == 1 })
}

// Through the WebView2 adapter and the real endpoint: the declared length
// and the stream's presence reach the endpoint separately, which refuses a
// contradiction before any handler runs.
func TestDeclaredLengthAndStreamReachTheEndpointSeparately(t *testing.T) {
	for _, tc := range []struct {
		name    string
		length  string // Content-Length, "" for none
		body    []byte // nil: no stream
		status  int    // a fixed answer before the handler; 0 when served
		content hresult
	}{
		{"positive declaration, no stream", "10", nil, http.StatusBadRequest, sOK},
		{"zero declaration, stream with a byte", "0", []byte("x"), http.StatusBadRequest, sOK},
		{"zero declaration, empty stream", "0", []byte{}, 0, sOK},
		{"zero declaration, no stream", "0", nil, 0, sOK},
		{"undeclared, no stream", "", nil, 0, sOK},
		{"undeclared, stream", "", []byte("abc"), 0, sOK},
		{"over the limit", strconv.Itoa(endpoint.DefaultRequestBody + 1), []byte("x"), http.StatusRequestEntityTooLarge, sOK},
		{"failed getter", "", nil, -1, eFail}, // -1: aborted, no response
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := make(chan string, 1)
			fe, err := endpoint.NewFinite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				served <- string(b)
			}), endpoint.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			h := &servingHost{t: t, f: fe}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if err := fe.Start(ctx, h, &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err != nil {
				t.Fatal(err)
			}
			fe.Bind("https://dfx2.invalid:443")
			f, b := newTestBrowser(t, &h.fakeHost, platform.BrowserInProcess)
			h.fakeHost.ready = true
			b.host, h.b = h, b
			started(t, f, b)
			f.request.uri, f.request.method, f.request.body = startURL+"api/echo", "POST", tc.body
			f.request.headers = nil
			if tc.length != "" {
				f.request.headers = [][2]string{{"Content-Length", tc.length}}
			}
			f.contentHR = tc.content
			b.receive(600)
			declared, noBody := int64(-1), tc.body == nil && !tc.content.failed()
			if tc.length != "" && !tc.content.failed() {
				declared, _ = strconv.ParseInt(tc.length, 10, 64)
			}
			if seen := h.seen[0]; seen.BodyLength != declared || seen.NoBody != noBody {
				t.Fatalf("endpoint saw length %d no-body %t, want %d %t", seen.BodyLength, seen.NoBody, declared, noBody)
			}
			if tc.status < 0 {
				f.pump(func() bool { return len(h.releasedR) == 1 })
				if len(f.statuses) != 0 || f.did("put-response") {
					t.Fatalf("a failed getter was answered: %v", f.statuses)
				}
				select {
				case got := <-served:
					t.Fatalf("handler ran with %q", got)
				case <-time.After(20 * time.Millisecond):
				}
				return
			}
			if tc.status != 0 {
				f.pump(func() bool { return len(f.statuses) > 0 && len(b.reads) == 0 })
				if f.statuses[0] != tc.status {
					t.Fatalf("answered %v, want %d", f.statuses, tc.status)
				}
				select {
				case got := <-served:
					t.Fatalf("handler ran with %q", got)
				case <-time.After(20 * time.Millisecond):
				}
				return
			}
			var got string
			f.pump(func() bool {
				select {
				case got = <-served:
					return true
				default:
					return false
				}
			})
			if got != string(tc.body) {
				t.Fatalf("handler body %q, want %q", got, tc.body)
			}
			// The handler's completion is published from its goroutine: wait
			// for its delivery before the test, and its fake, end.
			f.pump(func() bool { return f.did("put-response") })
			if tc.body == nil && f.did("worker") {
				t.Fatal("a worker started for a request with no stream")
			}
		})
	}
}
