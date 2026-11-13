package endpoint

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/platform"
)

const testOrigin = "fake://w1"

type finiteFixture struct {
	f      *Finite
	host   *testHost
	cancel context.CancelFunc
}

func newFinite(t *testing.T, handler http.Handler, limits Limits) finiteFixture {
	t.Helper()
	f, err := NewFinite(handler, limits)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := ownerContext(t)
	host := newHost()
	if err := f.Start(ctx, host, &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err != nil {
		t.Fatal(err)
	}
	f.Bind(testOrigin)
	return finiteFixture{f, host, cancel}
}

func get(path string) platform.BrowserRequest {
	return platform.BrowserRequest{Method: http.MethodGet, URL: testOrigin + path}
}

func (x finiteFixture) reserved() int64 {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	return x.f.reserved
}

func (x finiteFixture) admitted() int {
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	return x.f.admitted
}

// held is a handler blocked on a barrier, reporting entry.
type held struct {
	entered chan *http.Request
	release chan struct{}
	once    sync.Once
}

func newHeld(t *testing.T) *held {
	h := &held{entered: make(chan *http.Request, 16), release: make(chan struct{})}
	t.Cleanup(h.free)
	return h
}

func (h *held) free() { h.once.Do(func() { close(h.release) }) }

func (h *held) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.entered <- r
	<-h.release
	io.WriteString(w, "held")
}

func TestFiniteServesACopiedRequestOffTheUIThread(t *testing.T) {
	seen := make(chan *http.Request, 1)
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo", r.Header.Get("X-Test"))
		fmt.Fprintf(w, "<html>%s %s %s</html>", r.Method, r.URL.RequestURI(), body)
	}), Limits{})
	d := x.f.Request(platform.BrowserRequest{Method: http.MethodPost, URL: testOrigin + "/api/edit?x=1", BodyLength: 5,
		Header: []platform.Header{{Name: "x-test", Value: "one"}, {Name: "Content-Type", Value: "text/plain"}}})
	if d.Action != platform.RequestRead || d.ID == 0 || d.ReadChunk != 5 {
		t.Fatalf("decision %+v", d)
	}
	if got := x.reserved(); got < 5+2*DefaultResponseBody {
		t.Fatalf("admission reserved only %d bytes before reading", got)
	}
	if next := x.f.RequestBody(d.ID, []byte("he")); next.Action != platform.RequestRead {
		t.Fatalf("body decision %+v", next)
	}
	x.f.RequestBody(d.ID, []byte("llo"))
	if end := x.f.RequestBodyEnd(d.ID, nil); end.Action != platform.RequestWait {
		t.Fatalf("end decision %+v", end)
	}
	r := receive(t, seen)
	if r.Host != "w1" || r.RemoteAddr != "" || r.TLS != nil || r.RequestURI != "/api/edit?x=1" || r.URL.Host != "" || r.ContentLength != 5 {
		t.Fatalf("request %+v", r)
	}
	x.host.settleNext(t, false)
	resp, ok := x.host.response(d.ID)
	if !ok || resp.Status != 200 || string(resp.Body) != "<html>POST /api/edit?x=1 hello</html>" {
		t.Fatalf("response %+v %q", resp, resp.Body)
	}
	headers := map[string][]string{}
	for _, h := range resp.Header {
		headers[h.Name] = append(headers[h.Name], h.Value)
	}
	if headers["X-Echo"][0] != "one" || headers["Content-Length"][0] != "37" || !strings.HasPrefix(headers["Content-Type"][0], "text/html") ||
		headers["Content-Security-Policy"][0] != ContentSecurityPolicy {
		t.Fatalf("headers %v", headers)
	}
	if x.admitted() != 1 {
		t.Fatal("admission released while the browser still held the response")
	}
	x.f.RequestReleased(d.ID)
	if x.admitted() != 0 || x.reserved() != 0 {
		t.Fatalf("after release: admitted %d reserved %d", x.admitted(), x.reserved())
	}
}

func TestFiniteLimitFailuresAreRequestLocalAndRecover(t *testing.T) {
	big := strings.Repeat("x", 100)
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			w.Write([]byte(big)) // an ignored error must still fail the response
			w.Write([]byte(big))
			return
		}
		if r.URL.Path == "/meta" {
			w.Header().Set("X-Large", strings.Repeat("m", 600))
		}
		io.WriteString(w, "ok")
	}), Limits{RequestBody: 10, ResponseBody: 150, Metadata: 1024})
	status := func(d platform.RequestDecision) int {
		t.Helper()
		if d.Action != platform.RequestRespond {
			t.Fatalf("expected a fixed failure, got %+v", d)
		}
		return d.Response.Status
	}
	if s := status(x.f.Request(platform.BrowserRequest{Method: "POST", URL: testOrigin + "/", BodyLength: 11})); s != 413 {
		t.Fatalf("declared oversize: %d", s)
	}
	unknown := x.f.Request(platform.BrowserRequest{Method: "POST", URL: testOrigin + "/", BodyLength: -1})
	x.f.RequestBody(unknown.ID, []byte("0123456789"))
	if s := status(x.f.RequestBody(unknown.ID, []byte("!"))); s != 413 {
		t.Fatalf("detected oversize: %d", s)
	}
	if s := status(x.f.Request(platform.BrowserRequest{Method: "GET", URL: testOrigin + "/",
		Header: []platform.Header{{Name: "Cookie", Value: strings.Repeat("c", 1000)}}})); s != 431 {
		t.Fatalf("metadata: %d", s)
	}
	big1 := x.f.Request(get("/big"))
	x.host.settleNext(t, false)
	if r, _ := x.host.response(big1.ID); r.Status != 502 || string(r.Body) != failureBody(502) {
		t.Fatalf("oversized response published %d %q", r.Status, r.Body)
	}
	meta := x.f.Request(get("/meta"))
	x.host.settleNext(t, false)
	if r, _ := x.host.response(meta.ID); r.Status != 502 {
		t.Fatalf("oversized response metadata: %d", r.Status)
	}
	// Every failure stays local: the next ordinary request succeeds.
	ok := x.f.Request(get("/"))
	x.host.settleNext(t, false)
	if r, _ := x.host.response(ok.ID); r.Status != 200 || string(r.Body) != "ok" {
		t.Fatalf("recovery: %d %q", r.Status, r.Body)
	}
	for _, id := range []platform.ID{unknown.ID, big1.ID, meta.ID, ok.ID} {
		x.f.RequestReleased(id)
	}
	// The refusals were answered without admission; release them too.
	for id := range x.f.requests {
		x.f.RequestReleased(id)
	}
	if x.admitted() != 0 || x.reserved() != 0 || x.f.refusals != 0 {
		t.Fatalf("leaked: admitted %d reserved %d refusals %d", x.admitted(), x.reserved(), x.f.refusals)
	}
}

func TestFiniteBodyMustMatchItsDeclarationAndReadContract(t *testing.T) {
	x := newFinite(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Limits{})
	short := x.f.Request(platform.BrowserRequest{Method: "POST", URL: testOrigin + "/", BodyLength: 4})
	x.f.RequestBody(short.ID, []byte("ab"))
	if d := x.f.RequestBodyEnd(short.ID, nil); d.Response.Status != 400 {
		t.Fatalf("short body: %+v", d)
	}
	long := x.f.Request(platform.BrowserRequest{Method: "POST", URL: testOrigin + "/", BodyLength: 1})
	x.f.RequestBody(long.ID, []byte("a"))
	if d := x.f.RequestBody(long.ID, []byte("b")); d.Response.Status != 400 {
		t.Fatalf("long body: %+v", d)
	}
	failed := x.f.Request(platform.BrowserRequest{Method: "POST", URL: testOrigin + "/", BodyLength: 2})
	if d := x.f.RequestBodyEnd(failed.ID, errInjected); d.Action != platform.RequestAbort {
		t.Fatalf("read error: %+v", d)
	}
	oversized := x.f.Request(platform.BrowserRequest{Method: "POST", URL: testOrigin + "/", BodyLength: 2})
	if d := x.f.RequestBody(oversized.ID, make([]byte, 3)); d.Action != platform.RequestAbort {
		t.Fatalf("read beyond ReadChunk: %+v", d)
	}
	x.host.noPublication(t)
	for _, id := range []platform.ID{short.ID, long.ID, failed.ID, oversized.ID} {
		x.f.RequestReleased(id)
	}
	if x.admitted() != 0 || x.reserved() != 0 {
		t.Fatalf("leaked: admitted %d reserved %d", x.admitted(), x.reserved())
	}
}

func TestFiniteAdmissionAndAggregatePressureRefuseWithoutQueueing(t *testing.T) {
	h := newHeld(t)
	x := newFinite(t, h, Limits{Requests: 2})
	a, b := x.f.Request(get("/a")), x.f.Request(get("/b"))
	receive(t, h.entered)
	receive(t, h.entered)
	if c := x.f.Request(get("/c")); c.Action != platform.RequestRespond || c.Response.Status != 503 {
		t.Fatalf("admission full: %+v", c)
	}
	h.free()
	x.host.settleNext(t, false)
	x.host.settleNext(t, false)
	x.f.RequestReleased(a.ID)
	x.f.RequestReleased(b.ID)
	if d := x.f.Request(get("/d")); d.Action != platform.RequestWait {
		t.Fatalf("recovery after release: %+v", d)
	}
	x.host.settleNext(t, false)

	// A near-limit URL and full body reservation fill enough of a one-worst-
	// case budget to refuse a second request despite a free admission slot.
	limits, _ := Limits{RequestBody: 1000, ResponseBody: 1000, Metadata: 1024}.Normalize()
	limits.Buffers = limits.reserveRefusals() + limits.worstCase()
	h2 := newHeld(t)
	y := newFinite(t, h2, limits)
	first := y.f.Request(platform.BrowserRequest{Method: "PUT", URL: testOrigin + "/" + strings.Repeat("p", 850), BodyLength: -1})
	if first.Action != platform.RequestRead {
		t.Fatalf("first: %+v", first)
	}
	if second := y.f.Request(get("/")); second.Response.Status != 503 {
		t.Fatalf("aggregate pressure: %+v", second)
	}
	if y.reserved() > y.f.budget() {
		t.Fatalf("reserved %d beyond budget %d", y.reserved(), y.f.budget())
	}
	y.f.RequestReleased(first.ID)
	if next := y.f.Request(get("/")); next.Action != platform.RequestWait {
		t.Fatalf("aggregate recovery: %+v", next)
	}
}

func TestFiniteRefusalReserveIsBoundedAndMeasured(t *testing.T) {
	for status := range failures {
		r := failureResponse(status)
		if size := int64(len(r.Body)) + metadataSize(fmt.Sprint(status), "", r.Header); size > refusalReserve {
			t.Fatalf("fixed %d response is %d bytes, beyond the %d reserve", status, size, refusalReserve)
		}
	}
	x := newFinite(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), Limits{})
	var held []platform.ID
	for range Refusals {
		d := x.f.Request(platform.BrowserRequest{Method: "GET", URL: "fake://other/"})
		if d.Action != platform.RequestRespond || d.Response.Status != 403 {
			t.Fatalf("refusal: %+v", d)
		}
		held = append(held, d.ID)
	}
	if d := x.f.Request(platform.BrowserRequest{Method: "GET", URL: "fake://other/"}); d.Action != platform.RequestAbort || d.ID != 0 {
		t.Fatalf("exhausted refusal delivery must abort owing nothing: %+v", d)
	}
	x.f.RequestReleased(held[0])
	if d := x.f.Request(platform.BrowserRequest{Method: "GET", URL: "fake://other/"}); d.Response.Status != 403 {
		t.Fatalf("refusal capacity did not recover: %+v", d)
	}
}

func TestFiniteAbandonedHandlerKeepsAdmissionUntilItReturns(t *testing.T) {
	h := newHeld(t)
	x := newFinite(t, h, Limits{Requests: 1})
	a := x.f.Request(get("/slow"))
	receive(t, h.entered)
	// Fetch abort or reload: the browser releases its request first.
	x.f.RequestReleased(a.ID)
	if x.admitted() != 1 {
		t.Fatal("abandoned handler released its admission before returning")
	}
	if d := x.f.Request(get("/next")); d.Response.Status != 503 {
		t.Fatalf("abandoned handler's slot reused: %+v", d)
	}
	h.free()
	x.host.settleNext(t, false)
	if _, delivered := x.host.response(a.ID); delivered {
		t.Fatal("result delivered to a released request")
	}
	if x.admitted() != 0 || x.reserved() != 0 {
		t.Fatalf("after return: admitted %d reserved %d", x.admitted(), x.reserved())
	}
}

func TestFiniteCloseCancelsAndDrainsOnlyAfterHandlersReturn(t *testing.T) {
	stubborn := newHeld(t)
	cancellations := make(chan error, 1)
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/polite" {
			<-r.Context().Done()
			cancellations <- r.Context().Err()
			return
		}
		stubborn.ServeHTTP(w, r)
	}), Limits{})
	polite := x.f.Request(get("/polite"))
	slow := x.f.Request(get("/stubborn"))
	receive(t, stubborn.entered)
	x.cancel()
	x.f.Close()
	if receive(t, cancellations) != context.Canceled {
		t.Fatal("handler context not canceled by close")
	}
	if d := x.f.Request(get("/late")); d.Action != platform.RequestAbort || d.ID != 0 {
		t.Fatalf("admission after close: %+v", d)
	}
	// The browser cancels its native requests independently of handlers.
	x.f.RequestReleased(polite.ID)
	x.f.RequestReleased(slow.ID)
	x.host.settleNext(t, true)
	if x.host.isDrained() {
		t.Fatal("drained while a handler that ignores cancellation still runs")
	}
	stubborn.free()
	x.host.settleNext(t, true)
	if !x.host.isDrained() || x.host.drains != 1 {
		t.Fatalf("drains %d", x.host.drains)
	}
	if _, ok := x.host.response(slow.ID); ok {
		t.Fatal("late result attached after close")
	}
}

func TestFiniteLateResultAfterCloseIsReleasedNotAttached(t *testing.T) {
	h := newHeld(t)
	x := newFinite(t, h, Limits{})
	a := x.f.Request(get("/"))
	receive(t, h.entered)
	h.free()
	p := receive(t, x.host.published)
	// The window closed before the UI thread settled the published result.
	x.cancel()
	x.f.Close()
	p.settle(true)
	if _, ok := x.host.response(a.ID); ok {
		t.Fatal("late result delivered")
	}
	if x.host.isDrained() {
		t.Fatal("drained while the browser still held its request")
	}
	x.f.RequestReleased(a.ID)
	closed(t, x.host.drained)
}

func TestFinitePanicsFailTheApplicationWithoutReachingThePage(t *testing.T) {
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "staged=1")
		switch r.URL.Path {
		case "/abort":
			panic(http.ErrAbortHandler)
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "deliberate")
		default:
			panic("handler exploded")
		}
	}), Limits{})
	boom := x.f.Request(get("/boom"))
	x.host.settleNext(t, false)
	r, _ := x.host.response(boom.ID)
	if r.Status != 500 || strings.Contains(string(r.Body), "exploded") || strings.Contains(string(r.Body), "goroutine") {
		t.Fatalf("panic response %d %q", r.Status, r.Body)
	}
	for _, h := range r.Header {
		if h.Name == "Set-Cookie" {
			t.Fatal("panic response carried the handler's staged headers")
		}
	}
	failures := x.host.failed()
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), "handler exploded") || !strings.Contains(failures[0].Error(), "goroutine") {
		t.Fatalf("failure %v", failures)
	}
	abort := x.f.Request(get("/abort"))
	deliberate := x.f.Request(get("/500"))
	x.host.settleNext(t, false)
	x.host.settleNext(t, false)
	if r, _ := x.host.response(abort.ID); r.Status != 502 {
		t.Fatalf("deliberate abort: %d", r.Status)
	}
	if r, _ := x.host.response(deliberate.ID); r.Status != 500 || string(r.Body) != "deliberate" {
		t.Fatalf("ordinary 500: %d %q", r.Status, r.Body)
	}
	if len(x.host.failed()) != 1 {
		t.Fatal("ErrAbortHandler or an ordinary 500 was treated as an application failure")
	}
}

func TestFiniteWriterOffersNoStreamingOrUpgrade(t *testing.T) {
	checked := make(chan error, 1)
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, flusher := w.(http.Flusher)
		_, hijacker := w.(http.Hijacker)
		_, unwrapper := w.(interface{ Unwrap() http.ResponseWriter })
		err := http.NewResponseController(w).Flush()
		if flusher || hijacker || unwrapper || err == nil {
			checked <- fmt.Errorf("flusher=%t hijacker=%t unwrap=%t flush=%v", flusher, hijacker, unwrapper, err)
			return
		}
		checked <- nil
		if r.URL.Path == "/early" {
			w.WriteHeader(http.StatusEarlyHints)
		}
	}), Limits{})
	plain := x.f.Request(get("/"))
	if err := receive(t, checked); err != nil {
		t.Fatal(err)
	}
	x.host.settleNext(t, false)
	early := x.f.Request(get("/early"))
	receive(t, checked)
	x.host.settleNext(t, false)
	if r, _ := x.host.response(plain.ID); r.Status != 200 {
		t.Fatalf("plain %d", r.Status)
	}
	if r, _ := x.host.response(early.ID); r.Status != 502 {
		t.Fatalf("informational response must fail locally: %d", r.Status)
	}
	head := x.f.Request(platform.BrowserRequest{Method: http.MethodHead, URL: testOrigin + "/"})
	x.host.settleNext(t, false)
	if r, _ := x.host.response(head.ID); r.Status != 200 || len(r.Body) != 0 {
		t.Fatalf("HEAD carried a body: %d %q", r.Status, r.Body)
	}
}

func TestFiniteRoutesOnlyItsOwnOrigin(t *testing.T) {
	f, _ := NewFinite(http.NotFoundHandler(), Limits{})
	ctx, _ := ownerContext(t)
	host := newHost()
	f.Start(ctx, host, &platform.BrowserConfig{Mode: platform.BrowserInProcess})
	if d := f.Request(get("/")); d.Action != platform.RequestAbort || d.ID != 0 {
		t.Fatalf("unbound endpoint admitted: %+v", d)
	}
	f.Bind(testOrigin)
	for _, raw := range []string{"fake://w2/", "fake://w1.evil/", "http://w1/", "fake://W1:9/"} {
		if d := f.Request(platform.BrowserRequest{Method: "GET", URL: raw}); d.Response.Status != 403 {
			t.Fatalf("%s: %+v", raw, d)
		}
	}
	if d := f.Request(platform.BrowserRequest{Method: "G ET", URL: testOrigin + "/"}); d.Response.Status != 400 {
		t.Fatalf("bad method: %+v", d)
	}
	if err := f.Start(ctx, host, &platform.BrowserConfig{Mode: platform.BrowserLoopback}); err == nil {
		t.Fatal("finite endpoint started in loopback mode")
	}
}

func TestFiniteReentrantReleaseFromRespondDoesNotDoubleCount(t *testing.T) {
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "x") }), Limits{})
	x.host.respond = func(id platform.ID, _ platform.BrowserResponse) bool {
		x.f.RequestReleased(id) // the browser frees its copy at once
		return true
	}
	d := x.f.Request(get("/"))
	x.host.settleNext(t, false)
	if x.reserved() != 0 || x.admitted() != 0 || len(x.f.requests) != 0 {
		t.Fatalf("reserved %d admitted %d records %d", x.reserved(), x.admitted(), len(x.f.requests))
	}
	_ = d
}

func TestFiniteBeginFailureAbortsWithoutStartingWork(t *testing.T) {
	started := make(chan struct{}, 1)
	x := newFinite(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { started <- struct{}{} }), Limits{})
	x.host.beginErr = errInjected
	d := x.f.Request(get("/"))
	if d.Action != platform.RequestAbort || d.ID == 0 {
		t.Fatalf("decision %+v", d)
	}
	select {
	case <-started:
		t.Fatal("handler started without a registered completion")
	default:
	}
	x.f.RequestReleased(d.ID)
	if x.admitted() != 0 || x.reserved() != 0 {
		t.Fatal("leaked admission")
	}
}

func TestLimitsNormalize(t *testing.T) {
	l, err := Limits{}.Normalize()
	if err != nil || l.RequestBody != 1<<20 || l.ResponseBody != 1<<20 || l.Metadata != 64<<10 || l.Requests != 16 || l.Buffers != 64<<20 {
		t.Fatalf("defaults %+v %v", l, err)
	}
	// Sixteen worst-case requests fit the default budget alongside the reserve.
	if 16*l.worstCase()+l.reserveRefusals() > l.Buffers {
		t.Fatal("defaults cannot hold their own admission limit")
	}
	for _, bad := range []Limits{{RequestBody: -1}, {Buffers: -1}, {Metadata: 1023}, {Buffers: 1 << 20},
		// Positive limits whose reservation arithmetic would overflow.
		{ResponseBody: 1 << 62}, {RequestBody: math.MaxInt64}, {Metadata: math.MaxInt},
		{Buffers: math.MaxInt64}, {Buffers: math.MaxInt64, ResponseBody: 1 << 62}, {Buffers: 1<<50 + 1}} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	s, err := ServerLimits{}.Normalize()
	if err != nil || s.Connections != 32 || s.Handlers != 16 || s.HeaderBytes != 64<<10 {
		t.Fatalf("server defaults %+v %v", s, err)
	}
	if _, err := (ServerLimits{Handlers: -1}).Normalize(); err == nil {
		t.Fatal("negative server limit accepted")
	}
}

func TestFiniteCloseDuringBodyReadingAndAwaitingNativeRelease(t *testing.T) {
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "done") }), Limits{})
	reading := x.f.Request(platform.BrowserRequest{Method: "POST", URL: testOrigin + "/", BodyLength: 10})
	x.f.RequestBody(reading.ID, []byte("abc"))
	answered := x.f.Request(get("/"))
	x.host.settleNext(t, false) // delivered; the browser still holds its copy
	x.cancel()
	x.f.Close()
	if d := x.f.RequestBody(reading.ID, []byte("def")); d.Action != platform.RequestAbort || d.ID != reading.ID {
		t.Fatalf("close did not cancel the native body read: %+v", d)
	}
	if x.host.isDrained() {
		t.Fatal("drained with a native body read and a native response copy outstanding")
	}
	x.f.RequestReleased(reading.ID) // the browser cancels its native read
	if x.host.isDrained() {
		t.Fatal("drained while the browser still held a response copy")
	}
	x.f.RequestReleased(answered.ID)
	closed(t, x.host.drained)
	if x.reserved() != 0 || x.admitted() != 0 {
		t.Fatalf("reserved %d admitted %d", x.reserved(), x.admitted())
	}
	x.host.noPublication(t)
}

func TestFiniteOversizedResponseMetadataIsNeverCopied(t *testing.T) {
	w := &writer{method: "GET", limit: 100, metadata: 2048, header: make(http.Header)}
	for i := range 200 {
		w.Header().Add(fmt.Sprintf("X-Many-%d", i), "v")
	}
	w.WriteHeader(http.StatusOK)
	if w.written != nil {
		t.Fatal("the adapter copied response metadata beyond its limit")
	}
	if r := w.finish(); r.Status != http.StatusBadGateway {
		t.Fatalf("oversized metadata published: %d", r.Status)
	}
}

func TestFiniteEmptyHeaderEntriesCannotBypassMetadataLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
	}{{"nil", nil}, {"empty", []string{}}} {
		t.Run(tc.name, func(t *testing.T) {
			w := &writer{method: "GET", limit: 100, metadata: 1024, header: make(http.Header)}
			for i := range 4096 {
				w.Header()[fmt.Sprintf("X-Empty-%04d", i)] = tc.values
			}
			w.WriteHeader(http.StatusOK)
			if w.written != nil {
				t.Fatalf("copied %d empty header entries beyond the metadata limit", len(w.written))
			}
			if r := w.finish(); r.Status != http.StatusBadGateway {
				t.Fatalf("oversized empty metadata published: %d", r.Status)
			}
		})
	}
}

func TestFiniteEmptyContentTypeStillSuppressesSniffing(t *testing.T) {
	w := &writer{method: "GET", limit: 100, metadata: 1024, header: make(http.Header)}
	w.Header()["Content-Type"] = nil
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	r := w.finish()
	if r.Status != http.StatusOK || string(r.Body) != "hello" {
		t.Fatalf("small response failed: %+v", r)
	}
	for _, h := range r.Header {
		if strings.EqualFold(h.Name, "Content-Type") {
			t.Fatalf("empty Content-Type did not suppress sniffing: %+v", h)
		}
	}
}

func TestFiniteDeliveryStaysWithinTheAdmissionReservation(t *testing.T) {
	limits, err := Limits{RequestBody: 1024, ResponseBody: 8000, Metadata: 2048}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	limits.Buffers = limits.reserveRefusals() + limits.worstCase()
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Near-limit metadata and a body grown in small writes, so its buffer's
		// capacity exceeds its length.
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Padding", strings.Repeat("p", 1300))
		for range 79 {
			w.Write([]byte(strings.Repeat("b", 100)))
		}
	}), limits)
	var during, charged, capacity int64
	x.host.respond = func(id platform.ID, r platform.BrowserResponse) bool {
		x.f.mu.Lock()
		during, charged, capacity = x.f.reserved, x.f.requests[id].resp, int64(cap(r.Body))
		x.f.mu.Unlock()
		return true
	}
	d := x.f.Request(get("/"))
	x.host.settleNext(t, false)
	if r, _ := x.host.response(d.ID); r.Status != 0 {
		t.Fatalf("respond hook bypassed: %d", r.Status)
	}
	if during > x.f.budget() || charged > limits.responseAllowance() {
		t.Fatalf("delivery charged %d (allowance %d), total %d beyond budget %d", charged, limits.responseAllowance(), during, x.f.budget())
	}
	if capacity <= 7900 || charged < capacity+7900 {
		t.Fatalf("Go body capacity %d not charged alongside the browser's copy: %d", capacity, charged)
	}
	x.f.RequestReleased(d.ID)
	if x.reserved() != 0 {
		t.Fatalf("leaked %d", x.reserved())
	}
}

func TestFiniteOversizedURLIsRefusedBeforeParsing(t *testing.T) {
	x := newFinite(t, http.NotFoundHandler(), Limits{Metadata: 1024})
	// Unparseable and oversized: only a check that precedes parsing answers 431.
	huge := testOrigin + "/%zz" + strings.Repeat("%41", 1000)
	if d := x.f.Request(platform.BrowserRequest{Method: "GET", URL: huge}); d.Response.Status != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("decision %+v", d)
	}
	if d := x.f.Request(platform.BrowserRequest{Method: "GET", URL: testOrigin + "/%zz"}); d.Response.Status != http.StatusBadRequest {
		t.Fatalf("small malformed URL: %+v", d)
	}
}

func TestFiniteEncodedURLStorageFitsMetadataReservation(t *testing.T) {
	h := newHeld(t)
	x := newFinite(t, h, Limits{})
	path := "/" + strings.Repeat("%61%2f", 2000)
	in := get(path + "?view=" + strings.Repeat("q", 1000))
	d := x.f.Request(in)
	if d.Action != platform.RequestWait {
		t.Fatalf("request not admitted: %+v", d)
	}
	req := receive(t, h.entered)
	if req.URL.Path != "/"+strings.Repeat("a/", 2000) || req.RequestURI != path+"?view="+strings.Repeat("q", 1000) {
		t.Fatal("encoded URL semantics changed")
	}
	x.f.mu.Lock()
	r := x.f.requests[d.ID]
	charged := r.meta
	// Inspect the real string backing ranges, merging aliases. Scheme starts
	// at the cloned input's base for this lowercase fixture; include the full
	// clone, not just the substring exposed through RawPath.
	type span struct{ start, end uintptr }
	base := uintptr(unsafe.Pointer(unsafe.StringData(r.url.Scheme)))
	spans := []span{{base, base + uintptr(len(in.URL))}}
	for _, s := range []string{r.method, r.url.Path, r.url.RawPath, r.url.RawQuery, req.RequestURI} {
		start := uintptr(unsafe.Pointer(unsafe.StringData(s)))
		spans = append(spans, span{start, start + uintptr(len(s))})
	}
	slices.SortFunc(spans, func(a, b span) int {
		if a.start < b.start {
			return -1
		}
		if a.start > b.start {
			return 1
		}
		return 0
	})
	var retained int64
	var end uintptr
	for _, s := range spans {
		if s.end > end {
			retained += int64(s.end - max(end, s.start))
			end = s.end
		}
	}
	runtime.KeepAlive(req)
	runtime.KeepAlive(r)
	x.f.mu.Unlock()
	if retained > charged {
		t.Fatalf("retained URL/method strings use %d bytes, metadata reserves %d", retained, charged)
	}
	h.free()
	x.host.settleNext(t, false)
	x.f.RequestReleased(d.ID)
	if x.reserved() != 0 {
		t.Fatalf("URL reservation leaked: %d", x.reserved())
	}
}

func TestFiniteAggregateAdmissionPrecedesURLParsing(t *testing.T) {
	limits, err := Limits{Requests: 1000, RequestBody: 1024, ResponseBody: 1024, Metadata: 1024}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	limits.Buffers = limits.reserveRefusals() + limits.worstCase()
	x := newFinite(t, http.NotFoundHandler(), limits)
	var ids []platform.ID
	for range 100 {
		d := x.f.Request(platform.BrowserRequest{Method: "PUT", URL: testOrigin + "/", BodyLength: -1})
		ids = append(ids, d.ID)
		if d.Response.Status == http.StatusServiceUnavailable {
			break
		}
		if d.Action != platform.RequestRead {
			t.Fatalf("decision: %+v", d)
		}
	}
	if len(ids) == 100 {
		t.Fatal("did not reach aggregate pressure")
	}
	// Under aggregate pressure, even a small malformed URL is refused before
	// parsing can classify it as 400. No handler or body read is queued.
	d := x.f.Request(platform.BrowserRequest{Method: "PUT", URL: testOrigin + "/%zz", BodyLength: -1})
	if d.Response.Status != http.StatusServiceUnavailable {
		t.Fatalf("parsed before aggregate admission: %+v", d)
	}
	ids = append(ids, d.ID)
	for _, id := range ids {
		x.f.RequestReleased(id)
	}
	bad := x.f.Request(get("/%zz"))
	if bad.Response.Status != http.StatusBadRequest {
		t.Fatalf("recovery: %+v", bad)
	}
	x.f.RequestReleased(bad.ID)
	if x.reserved() != 0 || x.admitted() != 0 {
		t.Fatal("refused URL leaked admission")
	}
}

func TestLimitsAtTheirCeilingKeepAdmissionArithmeticExact(t *testing.T) {
	l, err := Limits{Buffers: 1 << 50, RequestBody: 1 << 48, ResponseBody: 1 << 48}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if w := l.worstCase(); w <= 0 || w > l.Buffers {
		t.Fatalf("worst case %d", w)
	}
	x := newFinite(t, http.NotFoundHandler(), Limits{Buffers: 1 << 50, RequestBody: 1 << 48, ResponseBody: 1 << 48})
	d := x.f.Request(platform.BrowserRequest{Method: "PUT", URL: testOrigin + "/", BodyLength: -1})
	if d.Action != platform.RequestRead || d.ReadChunk != ReadChunk || x.reserved() <= 0 || x.reserved() > x.f.budget() {
		t.Fatalf("decision %+v reserved %d", d, x.reserved())
	}
}

func TestFiniteBodyIsAllocatedOnceWithinItsReservation(t *testing.T) {
	limits, err := Limits{RequestBody: 1 << 20, ResponseBody: 1024, Metadata: 1024}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	limits.Buffers = limits.reserveRefusals() + limits.worstCase()
	got := make(chan int, 1)
	x := newFinite(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- len(body)
	}), limits)
	d := x.f.Request(platform.BrowserRequest{Method: "PUT", URL: testOrigin + "/", BodyLength: -1})
	chunk := make([]byte, d.ReadChunk)
	var first *byte
	for range 12 { // 768 KiB in 64 KiB reads: growth would have reallocated
		x.f.RequestBody(d.ID, chunk)
		r := x.f.requests[d.ID]
		if first == nil {
			first = &r.body[:1][0]
		}
		if &r.body[:1][0] != first || int64(cap(r.body)) != r.bodyCap {
			t.Fatal("the body buffer was reallocated while reading")
		}
		if x.reserved() > x.f.budget() {
			t.Fatalf("reserved %d beyond budget %d", x.reserved(), x.f.budget())
		}
	}
	if e := x.f.RequestBodyEnd(d.ID, nil); e.Action != platform.RequestWait {
		t.Fatalf("end %+v", e)
	}
	if n := receive(t, got); n != 12*ReadChunk {
		t.Fatalf("body %d", n)
	}
	x.host.settleNext(t, false)
	x.f.RequestReleased(d.ID)
	if x.reserved() != 0 {
		t.Fatalf("leaked %d", x.reserved())
	}
}
