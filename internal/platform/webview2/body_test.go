package webview2

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// Request bodies are read on a worker: these tests hold the fake stream's
// reads on a gate and play the UI thread with pump. They model WebView2's
// streams and COM initialization; native execution is the Windows matrix's.

func bodyRequest(f *fakeNative, body string) {
	f.request.uri, f.request.method = startURL+"api/echo", "POST"
	f.request.headers = nil
	f.request.body = []byte(body)
}

// The worker's read is held while the UI thread keeps serving: another
// request is admitted and answered, and posted work runs. The held body
// finishes once the read returns.
func TestHeldBodyReadLeavesTheUIThreadServing(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{
		{ID: 1, Action: platform.RequestRead, ReadChunk: 4},
		{ID: 2, Action: platform.RequestWait}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	f.gate = make(chan struct{})
	bodyRequest(f, "abcdef")
	b.receive(510)
	f.awaitRead()
	if !f.did("worker") || f.count("deferral-complete") != 0 || len(f.deferrals) != 1 {
		t.Fatalf("deferral not taken before the worker started: %v", f.calls)
	}
	// The UI thread, with the read held.
	f.request.body = nil
	f.request.uri = startURL + "api/other"
	b.receive(511)
	if !b.Respond(2, platform.BrowserResponse{Status: 204}) || f.count("deferral-complete") != 1 {
		t.Fatalf("second request not served while the read was held: %v", f.calls)
	}
	ran := false
	n.post(func() { ran = true })
	f.flush()
	if !ran || len(host.bodies) != 0 {
		t.Fatal("posted work blocked, or the held read delivered data")
	}
	if s := b.AuditionState(); s.Reads != 1 {
		t.Fatalf("reader not counted: %+v", s)
	}
	f.request.body = []byte("abcdef")
	close(f.gate)
	f.pump(func() bool { return len(b.reads) == 0 })
	if len(host.bodies) != 2 || string(host.bodies[0]) != "abcd" || string(host.bodies[1]) != "ef" || host.ends != 1 {
		t.Fatalf("bodies %q ends %d", host.bodies, host.ends)
	}
	if b.requests[1] == nil || f.count("release body stream") != 1 {
		t.Fatalf("body not handed on to wait for its response, or stream not released once: %v", f.calls)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bufs) != 2 || f.bufs[0] != f.bufs[1] {
		t.Fatalf("not one buffer per read: %v", f.bufs)
	}
}

// Close during a read settles native interception at once and stops further
// reads, but the worker keeps its stream, buffer and the endpoint's admission
// until the read returns; its late result is dropped; release waits for it.
func TestCloseDuringABodyReadWaitsForTheWorker(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 7, Action: platform.RequestRead, ReadChunk: 4}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	f.gate = make(chan struct{})
	bodyRequest(f, "abcdefgh")
	b.receive(520)
	f.awaitRead() // close lands during a claimed read
	b.Close()
	if got := closeObservation(b); got != "claimed-reads=1" {
		t.Fatalf("close observed %q, not the claimed read", got)
	}
	if f.count("deferral-complete") != 1 || f.count("put-response") != 0 || len(host.releasedR) != 0 {
		t.Fatalf("close did not settle interception, or released the admission early: %v %v", f.calls, host.releasedR)
	}
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	if host.released != 0 || f.count("release body stream") != 0 {
		t.Fatal("released while the worker still held its stream")
	}
	f.gate <- struct{}{} // the read in flight returns
	f.pump(func() bool { return len(b.reads) == 0 && host.released == 1 })
	if len(host.bodies) != 0 || host.ends != 0 {
		t.Fatalf("late result reached the endpoint: bodies %q ends %d", host.bodies, host.ends)
	}
	if f.count("read") != 1 || f.count("release body stream") != 1 || len(host.releasedR) != 1 || host.releasedR[0] != 7 {
		t.Fatalf("reads %d stream releases %d released %v", f.count("read"), f.count("release body stream"), host.releasedR)
	}
	if f.count("deferral-complete") != 1 || f.count("navigate "+startURL) != 1 {
		t.Fatalf("late result settled again or navigated: %v", f.calls)
	}
}

// Failures are request-local: the request is aborted without a response and
// released once, its deferral settled once and its stream released once.
func TestBodyReadFailuresAbortTheRequestOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setup      func(*fakeNative)
		worker     bool   // a worker started
		reads      int    // stream reads attempted
		deferrals  int    // deferrals settled
		event      string // a recorded policy event
		streamFree int
	}{
		{"read error", func(f *fakeNative) {
			n.streamRead = func(uintptr, []byte) (int, bool, hresult) { return 0, true, eFail }
		}, true, 0, 1, "", 1},
		{"unsupported agility", func(f *fakeNative) { f.agileHR = eNoInterface }, false, 0, 0, "body-unsupported", 1},
		{"COM initialization", func(f *fakeNative) { f.initErr = errors.New("CoInitializeEx failed") }, true, 0, 1, "", 1},
		{"get_Content failed", func(f *fakeNative) { f.contentHR = eFail }, false, 0, 0, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 9, Action: platform.RequestRead, ReadChunk: 4}}}
			f, b := newTestBrowser(t, host, platform.BrowserInProcess)
			started(t, f, b)
			bodyRequest(f, "abcd")
			tc.setup(f)
			b.receive(530)
			f.pump(func() bool { return len(b.reads) == 0 && len(host.releasedR) > 0 })
			if f.did("worker") != tc.worker || f.count("read") != tc.reads {
				t.Fatalf("worker %t reads %d: %v", f.did("worker"), f.count("read"), f.calls)
			}
			if len(host.releasedR) != 1 || host.releasedR[0] != 9 || host.ends != 1 || len(host.bodies) != 0 {
				t.Fatalf("released %v ends %d bodies %q", host.releasedR, host.ends, host.bodies)
			}
			if f.count("deferral-complete") != tc.deferrals || len(f.deferrals) != 0 || f.count("put-response") != 0 || len(b.requests) != 0 {
				t.Fatalf("native request not settled once without a response: %v", f.calls)
			}
			if f.count("release body stream") != tc.streamFree {
				t.Fatalf("stream released %d times", f.count("release body stream"))
			}
			if tc.name == "COM initialization" && (f.count("worker-finished") != 1 || f.did("uninitialize")) {
				// The failure is reported once; COM, never joined, is not left.
				t.Fatalf("failed initialization: finished %d, uninitialized %t", f.count("worker-finished"), f.did("uninitialize"))
			}
			if tc.event != "" && !strings.Contains(b.AuditionState().Events[len(b.AuditionState().Events)-1].Kind, tc.event) {
				t.Fatalf("no %s event", tc.event)
			}
			if tc.name == "get_Content failed" && host.requests[0].BodyLength != -1 {
				t.Fatalf("a failed getter was admitted as an empty body: %+v", host.requests[0])
			}
			b.Close()
			f.invoke("BrowserProcessExited", 0, exitKindNormal)
			f.flush()
			if host.released != 1 || len(host.releasedR) != 1 {
				t.Fatalf("released %d, requests %v", host.released, host.releasedR)
			}
		})
	}
}

// A successful null stream is no body (NoBody, the declaration kept), distinct
// from a failed getter.
func TestNullContentIsAnEmptyBody(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 5, Action: platform.RequestWait}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	f.request.uri, f.request.method, f.request.body = startURL+"api/echo", "POST", nil
	b.receive(540)
	if in := host.requests[0]; !in.NoBody || in.BodyLength != -1 || f.did("worker") || b.requests[5] == nil {
		t.Fatalf("null content not an empty body: %+v %v", host.requests[0], f.calls)
	}
	events := b.AuditionState().Events
	if e := events[len(events)-1]; e != (platform.BrowserEvent{Seq: e.Seq, Kind: "request-content", Detail: "get_Content S_OK null=true"}) {
		t.Fatalf("getter result not recorded alone: %+v", e)
	}
}

// A failed getter is recorded with its HRESULT, distinct from a null stream.
func TestFailedContentGetterIsRecorded(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 6, Action: platform.RequestRead, ReadChunk: 4}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	bodyRequest(f, "abcd")
	f.contentHR = eFail
	b.receive(541)
	found := false
	for _, e := range b.AuditionState().Events {
		found = found || e.Kind == "request-content" && e.Detail == "get_Content E_FAIL (0x80004005) null=true"
	}
	if !found {
		t.Fatalf("failed getter not recorded: %+v", b.AuditionState().Events)
	}
}

// The request-content event outlives the request call, so it keeps only the
// getter's result: neither a near-limit URL nor a custom method survives in
// the event history.
func TestRequestContentEventRetainsNoRequestStrings(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 8, Action: platform.RequestWait}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	const method = "DFX2-N4-DISTINCTIVE-METHOD"
	marker := "dfx2-n4-distinctive-url-"
	base := startURL + "api/" + marker
	pad := int(b.metadata) - platform.RequestRecordOverhead - len(method) - len(base) - 8
	f.request.uri, f.request.method, f.request.headers, f.request.body = base+strings.Repeat("u", pad), method, nil, nil
	b.receive(542)
	if len(host.requests) != 1 || host.requests[0].MetadataSize < b.metadata-16 || host.requests[0].MetadataSize > b.metadata {
		t.Fatalf("not a near-limit admitted request: %+v", host.requests)
	}
	var found []platform.BrowserEvent
	for _, e := range b.AuditionState().Events {
		if e.Kind == "request-content" {
			found = append(found, e)
		}
		if text := fmt.Sprintf("%+v", e); strings.Contains(text, marker) || strings.Contains(text, method) {
			t.Fatalf("a request string survives in the event history: %.200s", text)
		}
	}
	if len(found) != 1 || found[0] != (platform.BrowserEvent{Seq: found[0].Seq, Kind: "request-content", Detail: "get_Content S_OK null=true"}) {
		t.Fatalf("request-content events: %+v", found)
	}
}

// closeObservation is the close event's record of the claimed reads close
// stopped, or "" if the browser recorded no close.
func closeObservation(b *browser) string {
	detail := ""
	for _, e := range b.AuditionState().Events {
		if e.Kind == "close" {
			detail = e.Detail
		}
	}
	return detail
}
