package webview2

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// These tests replace the native surface with recorders; they create no COM
// object and need no WebView2 Runtime. Native behavior is the Windows
// matrix's. Handles are tokens: env 100, env5 105, controller 200, web view
// 300 (ICoreWebView2_2 302, _4 304), cookie manager 400, cookie 401, list
// 402; request objects and streams are allocated as the tests go.

type fakeHandler struct {
	name    string
	invoke  func(a, b uintptr) hresult
	dropped func()
	used    bool // invoked, or dropped
}

func (h *fakeHandler) ptr() uintptr { return 0xbad }
func (h *fakeHandler) release()     {}

type fakeStream struct {
	body     []byte
	released func()
}

type fakeNative struct {
	t         *testing.T
	calls     []string
	handlers  []*fakeHandler
	posted    []func()
	streams   map[uintptr]*fakeStream
	responses []uintptr // response objects created
	next      uintptr
	loaded    int
	freed     int
	users     []uintptr
	profile   string // what the runtime reports
	cookie    struct{ found, httpOnly bool }
	request   struct {
		uri, method string
		context     int32
		headers     [][2]string
		body        []byte
		pos         int
	}
	deferrals map[uintptr]uintptr
	cleanups  []string
}

func installFake(t *testing.T) *fakeNative {
	t.Helper()
	savedAPI, savedLoader, savedDigest, savedRemove := n, loader, loaderDigest, removeAll
	t.Cleanup(func() { n, loader, loaderDigest, removeAll = savedAPI, savedLoader, savedDigest, savedRemove })
	loader = &loaderState{}
	blob := []byte("loader bytes")
	loaderDigest = digest(blob)
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	f := &fakeNative{t: t, streams: map[uintptr]*fakeStream{}, next: 1000, deferrals: map[uintptr]uintptr{}, profile: "<profile>"}
	f.cookie.found, f.cookie.httpOnly = true, true
	call := func(s string) { f.calls = append(f.calls, s) }
	obj := func() uintptr { f.next++; return f.next }
	removeAll = func(path string) error { call("remove " + path); return nil }
	n = api{
		initCOM:    func() error { return nil },
		loaderBlob: func() []byte { return blob },
		loadLibrary: func(path string) (uintptr, error) {
			if _, err := os.Stat(path); err != nil {
				return 0, err
			}
			f.loaded++
			return 77, nil
		},
		freeLibrary:    func(uintptr) error { f.freed++; return nil },
		runtimeVersion: func(uintptr) (string, error) { return "test-runtime", nil },
		post:           func(fn func()) { f.posted = append(f.posted, fn) },
		newHandler: func(name string, _ guid, invoke func(a, b uintptr) hresult, dropped func()) handler {
			h := &fakeHandler{name: name, invoke: invoke, dropped: dropped}
			f.handlers = append(f.handlers, h)
			return h
		},
		queryInterface: func(o uintptr, iid *guid) (uintptr, hresult) {
			switch {
			case o == 100 && *iid == iidIEnvironment5:
				return 105, sOK
			case o == 300 && *iid == iidICoreWebView2_2:
				return 302, sOK
			case o == 300 && *iid == iidICoreWebView2_4:
				return 304, sOK
			}
			return 0, eNoInterface
		},
		addRef: func(o uintptr) { call("addref") },
		release: func(o uintptr) {
			call("release")
			if s := f.streams[o]; s != nil {
				// The host's own reference; the engine's are released by the test.
			}
		},
		createEnvironment: func(lib uintptr, userData string, cb handler) hresult {
			call("create-environment " + userData)
			f.users = append(f.users, lib)
			return sOK
		},
		envVersion:    func(uintptr) string { return "151.test" },
		envCreateCtrl: func(env, parent uintptr, cb handler) hresult { call("create-controller"); return sOK },
		envCreateResponse: func(env, body uintptr, status int, reason, headers string) (uintptr, hresult) {
			r := obj()
			f.responses = append(f.responses, r)
			call(strings.TrimSpace("response " + reason + " " + strings.TrimSuffix(strings.ReplaceAll(headers, "\r\n", " | "), " | ")))
			return r, sOK
		},
		env5AddExited:    func(uintptr, handler) (int64, hresult) { return 9, sOK },
		env5RemoveExited: func(uintptr, int64) { call("remove-exited") },
		exitedArgs:       func(args uintptr) (int32, uint32) { return int32(args), 4242 },
		ctrlWebView:      func(uintptr) (uintptr, hresult) { return 300, sOK },
		ctrlVisible:      func(_ uintptr, v bool) { call(map[bool]string{true: "visible", false: "hidden"}[v]) },
		ctrlBounds:       func(_ uintptr, w, h int32) { call("bounds") },
		ctrlNotifyMoved:  func(uintptr) {},
		ctrlFocus:        func(uintptr) { call("focus") },
		ctrlClose:        func(uintptr) hresult { call("controller-close"); return sOK },
		wvBrowserPID:     func(uintptr) uint32 { return 555 },
		wvSettings:       func(_ uintptr, devtools bool) hresult { call("settings"); return sOK },
		wvProfilePath:    func(uintptr) (string, bool) { return f.profile, true },
		wvAddEvent:       func(_ uintptr, add int, cb handler) (int64, hresult) { return int64(add), sOK },
		wvRemoveEvent:    func(_ uintptr, remove int, token int64) { call("remove-event") },
		wvAddFilter:      func(_ uintptr, pattern string, context int32) hresult { call("filter " + pattern); return sOK },
		wvNavigate:       func(_ uintptr, uri string) hresult { call("navigate " + uri); return sOK },
		wvNavigateHTML:   func(_ uintptr, html string) hresult { call("error-page"); return sOK },
		wvStop:           func(uintptr) { call("stop") },
		wvSource:         func(uintptr) string { return "source" },
		wvTitle:          func(uintptr) string { return "title" },
		wvOpenDevTools:   func(uintptr) {},
		wvCookieManager:  func(uintptr) (uintptr, hresult) { return 400, sOK },
		navStarting: func(args uintptr) (string, uint64, bool) {
			f.t.Fatal("navStarting read through the fake")
			return "", 0, false
		},
		navCancel: func(uintptr) { call("cancel") },
		navCompleted: func(uintptr) (bool, int32, uint64, int32) {
			f.t.Fatal("navCompleted read through the fake")
			return false, 0, 0, 0
		},
		newWindowHandled: func(uintptr) { call("popup-handled") },
		processFailed:    func(args uintptr) (int32, int32, int32, string) { return int32(args), 3, 1, "process" },
		permissionDeny:   func(uintptr) { call("permission-deny") },
		downloadCancel:   func(uintptr) { call("download-cancel") },
		responseReceived: func(uintptr) (string, int32) { f.t.Fatal("responseReceived read through the fake"); return "", 0 },
		requestInfo: func(args uintptr) (uintptr, string, string, int32) {
			return 600, f.request.uri, f.request.method, f.request.context
		},
		requestHeaders: func(uintptr) (uintptr, hresult) { f.request.pos = 0; return 601, sOK },
		iterHas:        func(uintptr) bool { return f.request.pos < len(f.request.headers) },
		iterCurrent: func(uintptr) (*uint16, *uint16, bool) {
			h := f.request.headers[f.request.pos]
			return wstr(h[0]), wstr(h[1]), true
		},
		iterNext: func(uintptr) { f.request.pos++ },
		wlen:     func(p *uint16) int { return wcount(p) },
		take:     func(p *uint16) string { return string(utf16.Decode(wslice(p))) },
		free:     func(*uint16) {},
		requestContent: func(uintptr) uintptr {
			if f.request.body == nil {
				return 0
			}
			return 700
		},
		streamRead: func(_ uintptr, buf []byte) (int, bool, hresult) {
			n := copy(buf, f.request.body)
			f.request.body = f.request.body[n:]
			return n, len(f.request.body) == 0, sOK
		},
		deferral: func(args uintptr) (uintptr, hresult) {
			d := obj()
			f.deferrals[d] = args
			return d, sOK
		},
		deferralDone: func(d uintptr) hresult { call("deferral-complete"); delete(f.deferrals, d); return sOK },
		putResponse:  func(args, resp uintptr) hresult { call("put-response"); return sOK },
		newBodyStream: func(body []byte, released func()) uintptr {
			s := obj()
			f.streams[s] = &fakeStream{body: body, released: released}
			return s
		},
		cookieCreate: func(_ uintptr, name, value, domain, path string) (uintptr, hresult) {
			call("cookie " + name + " " + domain + path)
			return 401, sOK
		},
		cookieSecure: func(uintptr) { call("cookie-secure") },
		cookieAdd:    func(uintptr, uintptr) hresult { call("cookie-add"); return sOK },
		cookieList:   func(uintptr, string, handler) hresult { call("get-cookies"); return sOK },
		cookieFound:  func(uintptr, string) (bool, bool, int32) { return f.cookie.found, f.cookie.httpOnly, 2 },
	}
	return f
}

func wstr(s string) *uint16 {
	u := append(utf16.Encode([]rune(s)), 0)
	return &u[0]
}

func wcount(p *uint16) int {
	n := 0
	for s := wslice(p); n < len(s); n++ {
	}
	return n
}

// wslice views a NUL-terminated UTF-16 string the fake allocated.
func wslice(p *uint16) []uint16 {
	var out []uint16
	for i := 0; ; i++ {
		c := *(*uint16)(ptrAdd(p, i))
		if c == 0 {
			return out
		}
		out = append(out, c)
	}
}

func (f *fakeNative) did(name string) bool { return slices.Contains(f.calls, name) }

func (f *fakeNative) count(name string) int {
	c := 0
	for _, s := range f.calls {
		if s == name {
			c++
		}
	}
	return c
}

// handler finds the oldest pending handler of a name, in creation order.
func (f *fakeNative) handler(name string) *fakeHandler {
	f.t.Helper()
	for _, h := range f.handlers {
		if h.name == name && !h.used {
			return h
		}
	}
	f.t.Fatalf("no pending %s handler", name)
	return nil
}

// invoke completes a handler as the engine would.
func (f *fakeNative) invoke(name string, a, b uintptr) {
	f.t.Helper()
	h := f.handler(name)
	h.used = true
	h.invoke(a, b)
}

// drop releases a handler without invoking it, as Close does to a pending
// completion.
func (f *fakeNative) drop(name string) {
	f.t.Helper()
	h := f.handler(name)
	h.used = true
	if h.dropped == nil {
		f.t.Fatalf("%s has no dropped settlement", name)
	}
	h.dropped()
}

// flush runs everything posted to the UI thread.
func (f *fakeNative) flush() {
	for len(f.posted) > 0 {
		fn := f.posted[0]
		f.posted = f.posted[1:]
		fn()
	}
}

// freeStream releases a response body as the engine dropping its last
// reference would, then runs the hop to the UI thread.
func (f *fakeNative) freeStream() {
	f.t.Helper()
	for s, st := range f.streams {
		delete(f.streams, s)
		st.released()
		f.flush()
		return
	}
	f.t.Fatal("no response body held")
}

type fakeHost struct {
	ready     bool
	readies   int
	failed    []error
	released  int
	cleanups  []string
	decisions []platform.RequestDecision
	requests  []platform.BrowserRequest
	bodies    [][]byte
	ends      int
	releasedR []platform.ID
}

func (h *fakeHost) Ready() bool    { h.readies++; return h.ready }
func (h *fakeHost) Failed(e error) { h.failed = append(h.failed, e) }
func (h *fakeHost) Released()      { h.released++ }
func (h *fakeHost) Cleanup(name string, work func() error) error {
	h.cleanups = append(h.cleanups, name)
	return work()
}
func (h *fakeHost) Request(r platform.BrowserRequest) platform.RequestDecision {
	h.requests = append(h.requests, r)
	d := h.decisions[0]
	h.decisions = h.decisions[1:]
	return d
}
func (h *fakeHost) RequestBody(id platform.ID, data []byte) platform.RequestDecision {
	h.bodies = append(h.bodies, append([]byte(nil), data...))
	return platform.RequestDecision{ID: id, Action: platform.RequestRead, ReadChunk: 4}
}
func (h *fakeHost) RequestBodyEnd(id platform.ID, err error) platform.RequestDecision {
	h.ends++
	if err != nil {
		return platform.RequestDecision{Action: platform.RequestAbort} // real endpoint convention
	}
	return platform.RequestDecision{ID: id, Action: platform.RequestWait}
}
func (h *fakeHost) RequestReleased(id platform.ID) { h.releasedR = append(h.releasedR, id) }

func newTestBrowser(t *testing.T, host *fakeHost, mode platform.BrowserMode) (*fakeNative, *browser) {
	t.Helper()
	f := installFake(t)
	config := platform.BrowserConfig{Mode: mode, RequestMetadata: 64 << 10}
	if mode == platform.BrowserLoopback {
		config.StartURL, config.Origins = "http://127.0.0.1:4000/", []string{"http://127.0.0.1:4000"}
		config.Credential = &platform.BrowserCookie{Name: "c", Value: "v", Origin: "http://127.0.0.1:4000"}
	}
	if mode == platform.BrowserAttach {
		config.StartURL, config.Origins = "http://127.0.0.1:9/", []string{"http://127.0.0.1:9"}
	}
	b, err := newBrowser(platform.BrowserParent{Window: 1, Handle: 77, Surface: platform.Surface{Width: 10, Height: 10, Scale: 1, Visible: true}}, config, host)
	if err != nil {
		t.Fatal(err)
	}
	f.profile = filepath.Join(b.profile, "EBWebView", "Default")
	return f, b
}

// started takes an attachment through environment and controller creation.
func started(t *testing.T, f *fakeNative, b *browser) {
	t.Helper()
	b.Start()
	f.invoke("EnvironmentCompleted", 0, 100)
	f.invoke("ControllerCompleted", 0, 200)
}

// Emulations of event arguments: the fake reads them from these overrides.
func (f *fakeNative) navigation(uri string, id uint64, redirected bool) {
	n.navStarting = func(uintptr) (string, uint64, bool) { return uri, id, redirected }
}

func (f *fakeNative) completion(success bool, status int32, id uint64, http int32) {
	n.navCompleted = func(uintptr) (bool, int32, uint64, int32) { return success, status, id, http }
}

func (f *fakeNative) response(uri string, status int32) {
	n.responseReceived = func(uintptr) (string, int32) { return uri, status }
}

func TestFirstNavigationWaitsForCredentialAndReadiness(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserLoopback)
	b.Start()
	if !strings.HasPrefix(f.calls[0], "create-environment "+b.profile) || f.did("navigate http://127.0.0.1:4000/") {
		t.Fatalf("calls %v", f.calls)
	}
	f.invoke("EnvironmentCompleted", 0, 100)
	if !f.did("create-controller") || f.did("settings") {
		t.Fatalf("controller not requested from the environment callback: %v", f.calls)
	}
	f.invoke("ControllerCompleted", 0, 200)
	if !f.did("settings") || !f.did("filter *") || !f.did("cookie c 127.0.0.1/") || !f.did("cookie-secure") || !f.did("cookie-add") || !f.did("get-cookies") {
		t.Fatalf("controller setup: %v", f.calls)
	}
	if f.did("navigate http://127.0.0.1:4000/") || host.readies != 0 {
		t.Fatal("navigated before the credential was confirmed")
	}
	f.invoke("GetCookiesCompleted", 0, 402)
	if !f.did("navigate http://127.0.0.1:4000/") || host.readies != 1 || len(host.failed) != 0 {
		t.Fatalf("calls %v readies %d failed %v", f.calls, host.readies, host.failed)
	}
	if b.AuditionState().Runtime != "151.test" {
		t.Fatal("runtime version not recorded from the environment")
	}
}

func TestInProcessNavigatesWithoutACredentialAndClosingRefusesReadiness(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	if f.did("get-cookies") || !f.did("navigate "+startURL) || b.Origin() != Origin {
		t.Fatalf("in-process start: %v", f.calls)
	}
	host2 := &fakeHost{ready: false} // the core reports the window closing
	f2, b2 := newTestBrowser(t, host2, platform.BrowserInProcess)
	started(t, f2, b2)
	if f2.did("navigate " + startURL) {
		t.Fatal("navigated although the core refused readiness")
	}
}

// A close before the environment callback: the late environment is released,
// never attached; no controller is created; release waits for the callback,
// then removes the profile and the loader.
func TestCloseDuringEnvironmentCreationReleasesLateResults(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	b.Start()
	b.Close()
	if host.released != 0 || len(f.posted) != 0 {
		t.Fatal("released while the environment callback is still owed")
	}
	f.invoke("EnvironmentCompleted", 0, 100)
	if f.did("create-controller") || f.did("addref") {
		t.Fatalf("late environment attached: %v", f.calls)
	}
	f.flush() // afterRelease
	if host.released != 1 || len(host.cleanups) != 2 || !strings.HasPrefix(host.cleanups[0], "WebView2 loader ") || !strings.HasPrefix(host.cleanups[1], "browser profile "+b.profile) {
		t.Fatalf("released %d cleanups %v", host.released, host.cleanups)
	}
	if f.freed != 1 || !f.did("remove "+b.profile) {
		t.Fatalf("loader freed %d, calls %v", f.freed, f.calls)
	}
}

// A close before the controller callback: the late controller is closed, not
// attached, and release waits for its browser process to exit.
func TestCloseDuringControllerCreationClosesTheLateController(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserLoopback)
	b.Start()
	f.invoke("EnvironmentCompleted", 0, 100)
	b.Close()
	f.invoke("ControllerCompleted", 0, 200)
	if !f.did("controller-close") || f.did("settings") || f.did("navigate http://127.0.0.1:4000/") {
		t.Fatalf("late controller: %v", f.calls)
	}
	f.flush()
	if host.released != 0 {
		t.Fatal("released before the browser process exited")
	}
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	if host.released != 1 || !f.did("remove-exited") || len(host.failed) != 0 {
		t.Fatalf("released %d calls %v failed %v", host.released, f.calls, host.failed)
	}
}

// Close releases a pending GetCookies handler without invoking it; the
// dropped settlement, not an invocation, lets release proceed.
func TestDroppedCompletionSettlesOnce(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserLoopback)
	started(t, f, b)
	b.Close()
	if !f.did("stop") || !f.did("controller-close") || f.count("remove-event") != 9 {
		t.Fatalf("close: %v", f.calls)
	}
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	if host.released != 0 {
		t.Fatal("released while the cookie completion is still owed")
	}
	f.drop("GetCookiesCompleted")
	f.flush()
	if host.released != 1 || b.AuditionState().Asyncs != 0 {
		t.Fatalf("released %d state %+v", host.released, b.AuditionState())
	}
	if !slices.ContainsFunc(b.AuditionState().Events, func(e platform.BrowserEvent) bool { return e.Kind == "completion-dropped" }) {
		t.Fatal("dropped completion not recorded")
	}
}

func TestCloseSettlesRequestsAndReleasesOnlyAfterResponseStreamsFree(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{
		{ID: 1, Action: platform.RequestWait}, {ID: 2, Action: platform.RequestWait}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	f.request.uri, f.request.method = startURL+"api/a", "GET"
	b.receive(501)
	b.receive(502)
	if len(host.requests) != 2 || len(f.deferrals) != 2 {
		t.Fatalf("requests %d deferrals %d", len(host.requests), len(f.deferrals))
	}
	if !b.Respond(1, platform.BrowserResponse{Status: 200, Header: []platform.Header{{Name: "Content-Type", Value: "text/plain"}}, Body: []byte("ok")}) {
		t.Fatal("response refused")
	}
	if !f.did("response OK Content-Type: text/plain") || !f.did("put-response") || f.count("deferral-complete") != 1 || len(f.streams) != 1 {
		t.Fatalf("delivery: %v", f.calls)
	}
	if len(host.releasedR) != 0 {
		t.Fatal("request released while the engine holds its body")
	}
	b.Close()
	if len(host.releasedR) != 1 || host.releasedR[0] != 2 || f.count("deferral-complete") != 2 || f.count("put-response") != 1 {
		t.Fatalf("waiting request not settled without a response at close: %v %v", host.releasedR, f.calls)
	}
	if b.Respond(2, platform.BrowserResponse{Status: 200}) {
		t.Fatal("response accepted after close")
	}
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	if host.released != 1 {
		t.Fatal("not released: the engine's response body does not use the parent")
	}
	f.freeStream()
	if len(host.releasedR) != 2 || host.releasedR[1] != 1 || b.AuditionState().Copies != 0 {
		t.Fatalf("released %v copies %d", host.releasedR, b.AuditionState().Copies)
	}
}

func TestBodyIsReadInGrantedChunksAndFixedRefusalsHoldUntilReleased(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{
		{ID: 3, Action: platform.RequestRead, ReadChunk: 4},
		{ID: 4, Action: platform.RequestRespond, Response: platform.BrowserResponse{Status: 503, Body: []byte("busy")}},
		{Action: platform.RequestAbort}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	f.request.uri, f.request.method = startURL+"api/echo", "POST"
	f.request.headers = [][2]string{{"Content-Length", "10"}, {"X-Test", "é"}}
	f.request.body = []byte("0123456789")
	b.receive(510)
	in := host.requests[0]
	if in.BodyLength != 10 || len(in.Header) != 2 || in.Header[1].Value != "é" || in.MetadataSize != int64(platform.RequestRecordOverhead+len("POST")+len(in.URL)+2*platform.RequestHeaderOverhead+len("Content-Length")+len("10")+len("X-Test")+len("é")) {
		t.Fatalf("request %+v", in)
	}
	if len(host.bodies) != 3 || string(host.bodies[0]) != "0123" || string(host.bodies[2]) != "89" || host.ends != 1 || len(f.deferrals) != 1 {
		t.Fatalf("bodies %q ends %d deferrals %d", host.bodies, host.ends, len(f.deferrals))
	}
	// A fixed refusal with a body is held by the engine like any response.
	f.request.body, f.request.headers = nil, nil
	b.receive(511)
	if !f.did("response Server Error") || len(f.streams) != 1 || len(host.releasedR) != 0 {
		t.Fatalf("refusal: %v", f.calls)
	}
	f.freeStream()
	if len(host.releasedR) != 1 || host.releasedR[0] != 4 {
		t.Fatalf("refusal released %v", host.releasedR)
	}
	// An abort without an ID holds nothing.
	b.receive(512)
	if len(host.releasedR) != 1 || f.count("put-response") != 1 {
		t.Fatalf("abort installed something: %v", f.calls)
	}
}

func TestOversizedHeadersAreMeasuredWithoutConversion(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 0, Action: platform.RequestAbort}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	b.metadata = 200
	f.request.uri, f.request.method = startURL, "GET"
	huge := strings.Repeat("v", 10000)
	f.request.headers = [][2]string{{"A", "1"}, {"Big", huge}, {"After", "2"}}
	taken := 0
	take := n.take
	n.take = func(p *uint16) string { taken++; return take(p) }
	b.receive(520)
	in := host.requests[0]
	if in.Header != nil || in.MetadataSize <= 200 || taken != 2 {
		t.Fatalf("oversized request: headers %v size %d converted %d", in.Header, in.MetadataSize, taken)
	}
}

func TestOffOriginRequestsAreAnsweredNativelyAndAttachmentsOnlyObserve(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserLoopback)
	started(t, f, b)
	f.request.uri, f.request.method, f.request.context = "http://127.0.0.1:1/collector", "GET", 3
	b.receive(530)
	if !f.did("response Forbidden Content-Type: text/plain; charset=utf-8 | Cache-Control: no-store | X-Content-Type-Options: nosniff | Content-Security-Policy: default-src 'none'") || len(host.requests) != 0 {
		t.Fatalf("off-origin request not answered natively: %v", f.calls)
	}
	f.request.uri, f.request.context = "http://127.0.0.1:4000/api", 1
	b.receive(531)
	if f.count("put-response") != 1 || len(host.requests) != 0 {
		t.Fatal("an on-origin loopback request was answered natively")
	}
	host2 := &fakeHost{ready: true}
	f2, b2 := newTestBrowser(t, host2, platform.BrowserAttach)
	started(t, f2, b2)
	f2.request.uri, f2.request.method, f2.request.context = "http://127.0.0.1:1/collector", "POST", 1
	b2.receive(532)
	if f2.did("put-response") || b2.nav.method != "" {
		t.Fatalf("attachment acted on a request: %v", f2.calls)
	}
}

func TestErrorPageActionsAreSingleUseRetryOnlyForGET(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserLoopback)
	started(t, f, b)
	f.invoke("GetCookiesCompleted", 0, 402)
	const failing = "http://127.0.0.1:4000/api/fail"
	f.navigation(failing, 7, false)
	b.onNavigationStarting(1)
	f.request.uri, f.request.method, f.request.context = failing, "GET", resourceContextDocument
	b.receive(540)
	f.completion(false, 3, 7, 0)
	b.onNavigationCompleted(2)
	if !f.did("error-page") || b.failure.method != "GET" || !b.errorPending {
		t.Fatalf("no error page: %v %+v", f.calls, b.failure)
	}
	good := b.failure.nonce
	// The error document's own navigation is admitted, and its completion is
	// not a load failure.
	f.navigation("data:text/html;charset=utf-8;base64,PCF", 8, false)
	b.onNavigationStarting(3)
	f.completion(true, 0, 8, 0)
	b.onNavigationCompleted(4)
	if b.errorPending || f.count("cancel") != 0 || f.count("error-page") != 1 {
		t.Fatalf("error document navigation mishandled: %v", f.calls)
	}
	f.navigation(errorOrigin+"/retry/forged", 9, false)
	b.onNavigationStarting(5)
	if f.did("navigate "+failing) || f.count("cancel") != 1 {
		t.Fatal("forged action followed, or not canceled")
	}
	f.navigation(errorOrigin+"/retry/"+good, 10, false)
	b.onNavigationStarting(6)
	if !f.did("navigate "+failing) || f.count("cancel") != 2 {
		t.Fatalf("Retry not followed: %v", f.calls)
	}
	f.calls = nil
	f.navigation(errorOrigin+"/start/"+good, 11, false)
	b.onNavigationStarting(7)
	if f.did("navigate http://127.0.0.1:4000/") {
		t.Fatal("a used error page's action was followed again")
	}
	// A failed POST: no Retry, Return to start loads the start page.
	f.navigation(failing, 12, false)
	b.onNavigationStarting(8)
	f.request.method = "POST"
	b.receive(541)
	f.completion(false, 3, 12, 0)
	b.onNavigationCompleted(9)
	n2 := b.failure.nonce
	if n2 == "" || n2 == good || b.failure.method != "POST" {
		t.Fatalf("failure %+v", b.failure)
	}
	f.navigation(errorOrigin+"/retry/"+n2, 13, false)
	b.onNavigationStarting(10)
	if f.did("navigate " + failing) {
		t.Fatal("Retry repeated a POST")
	}
	f.navigation(errorOrigin+"/start/"+n2, 14, false)
	b.onNavigationStarting(11)
	if !f.did("navigate http://127.0.0.1:4000/") {
		t.Fatal("Return to start not followed")
	}
}

func TestNewNavigationInvalidatesSameURIErrorActions(t *testing.T) {
	for _, action := range []string{"retry", "start"} {
		t.Run(action, func(t *testing.T) {
			f, b := newTestBrowser(t, &fakeHost{ready: true}, platform.BrowserLoopback)
			started(t, f, b)
			f.invoke("GetCookiesCompleted", 0, 402)
			uri := "http://127.0.0.1:4000/api/fail"
			f.navigation(uri, 7, false)
			b.onNavigationStarting(1)
			f.request.uri, f.request.method, f.request.context = uri, "GET", resourceContextDocument
			b.receive(550)
			f.completion(false, 3, 7, 0)
			b.onNavigationCompleted(2)
			old := b.failure.nonce
			// An ordinary navigation to the same URI, before any action.
			f.navigation(uri, 8, false)
			b.onNavigationStarting(3)
			f.calls = nil
			f.navigation(errorOrigin+"/"+action+"/"+old, 9, false)
			b.onNavigationStarting(4)
			if f.did("navigate "+uri) || f.did("navigate http://127.0.0.1:4000/") || b.failure.nonce != "" {
				t.Fatalf("stale %s acted: %v", action, f.calls)
			}
		})
	}
}

// An older same-URL response arriving after a newer navigation to that URL
// failed cannot suppress the error page or change its actions: it belongs to
// an earlier generation (a fetch is never a document request at all).
func TestLateSameURLResponseCannotSuppressTheErrorPage(t *testing.T) {
	f, b := newTestBrowser(t, &fakeHost{ready: true}, platform.BrowserLoopback)
	started(t, f, b)
	f.invoke("GetCookiesCompleted", 0, 402)
	const race = "http://127.0.0.1:4000/api/race"
	// Navigation 1 to the URL: its document request is held by the server.
	f.navigation(race, 1, false)
	b.onNavigationStarting(1)
	f.request.uri, f.request.method, f.request.context = race, "GET", resourceContextDocument
	b.receive(560)
	// Navigation 2 to the same URL supersedes it and fails outright.
	f.navigation(race, 2, false)
	b.onNavigationStarting(2)
	b.receive(561)
	f.completion(false, webErrorCanceled, 1, 0)
	b.onNavigationCompleted(3) // the superseded navigation's cancellation
	f.completion(false, 3, 2, 0)
	b.onNavigationCompleted(4)
	nonce := b.failure.nonce
	if nonce == "" || b.failure.method != "GET" {
		t.Fatalf("no error page for the failed navigation: %+v", b.failure)
	}
	// The held response finally arrives, and a 303 for the same URL too.
	f.response(race, 200)
	b.onResponseReceived(5)
	f.response(race, 303)
	b.onResponseReceived(6)
	if b.nav.responded || b.failure.nonce != nonce || !b.errorPending {
		t.Fatalf("late response changed the failure: responded %t nonce %q pending %t", b.nav.responded, b.failure.nonce, b.errorPending)
	}
	// Even a completion for the superseded navigation, arriving now, is not
	// the current one's.
	f.completion(true, 0, 1, 200)
	b.onNavigationCompleted(7)
	if b.failure.nonce != nonce {
		t.Fatal("an older navigation's success retired the error page")
	}
	// A redirect's status is not a response to the final document request.
	f.navigation("http://127.0.0.1:4000/post-303", 20, false)
	b.onNavigationStarting(8)
	f.request.uri, f.request.method = "http://127.0.0.1:4000/post-303", "POST"
	b.receive(562)
	f.response("http://127.0.0.1:4000/post-303", 303)
	b.onResponseReceived(9)
	f.navigation("http://127.0.0.1:4000/broken", 20, true)
	b.onNavigationStarting(10)
	f.request.uri, f.request.method = "http://127.0.0.1:4000/broken", "GET"
	b.receive(563)
	f.completion(false, 3, 20, 303)
	b.onNavigationCompleted(11)
	if b.failure.uri != "http://127.0.0.1:4000/broken" || b.failure.method != "GET" || !retryable(b.failure.method) {
		t.Fatalf("303 after POST: %+v", b.failure)
	}
}

func TestProcessFailuresAreClassified(t *testing.T) {
	for kind, fatal := range map[int32]bool{failedBrowserProcess: true, failedRenderProcess: true, failedFrameRenderProcess: true,
		failedRenderUnresponsive: false, failedGPUProcess: false, failedUtilityProcess: false} {
		host := &fakeHost{ready: true}
		f, b := newTestBrowser(t, host, platform.BrowserInProcess)
		started(t, f, b)
		b.onProcessFailed(uintptr(kind))
		if (len(host.failed) == 1) != fatal {
			t.Errorf("kind %d: failed %v", kind, host.failed)
		}
		if fatal && !strings.Contains(host.failed[0].Error(), "reason crashed, exit code 1, process") {
			t.Errorf("kind %d: %v", kind, host.failed[0])
		}
	}
	// The browser process exiting before close is fatal once, with the
	// ProcessFailed report for the same event.
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	b.onProcessFailed(failedBrowserProcess)
	b.onBrowserProcessExited(1)
	if len(host.failed) != 1 {
		t.Fatalf("browser exit reported %d times: %v", len(host.failed), host.failed)
	}
	host2 := &fakeHost{ready: true}
	f2, b2 := newTestBrowser(t, host2, platform.BrowserInProcess)
	started(t, f2, b2)
	b2.onBrowserProcessExited(1)
	if len(host2.failed) != 1 || !strings.Contains(host2.failed[0].Error(), "browser process 4242 exited") {
		t.Fatalf("unexpected exit not fatal: %v", host2.failed)
	}
}

func TestProfileOutsideTheWindowDirectoryIsFatal(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	b.Start()
	f.invoke("EnvironmentCompleted", 0, 100)
	f.profile = filepath.Join(t.TempDir(), "EBWebView", "Default")
	f.invoke("ControllerCompleted", 0, 200)
	if len(host.failed) != 1 || !strings.Contains(host.failed[0].Error(), "profile isolation") || f.did("navigate "+startURL) {
		t.Fatalf("foreign profile accepted: %v %v", host.failed, f.calls)
	}
}

func TestLoaderIsSharedAndUnloadedAfterTheLastUser(t *testing.T) {
	host1, host2 := &fakeHost{ready: true}, &fakeHost{ready: true}
	f, b1 := newTestBrowser(t, host1, platform.BrowserInProcess)
	if f.loaded != 1 || loader.users != 1 {
		t.Fatalf("loaded %d users %d", f.loaded, loader.users)
	}
	config := platform.BrowserConfig{Mode: platform.BrowserInProcess, RequestMetadata: 1024}
	b2, err := newBrowser(platform.BrowserParent{Window: 2, Handle: 78}, config, host2)
	if err != nil {
		t.Fatal(err)
	}
	if f.loaded != 1 || loader.users != 2 || b1.profile == b2.profile {
		t.Fatalf("loaded %d users %d profiles %q %q", f.loaded, loader.users, b1.profile, b2.profile)
	}
	for _, b := range []*browser{b1, b2} {
		started(t, f, b)
	}
	b1.Close()
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	if host1.released != 1 || f.freed != 0 || len(host1.cleanups) != 1 || !strings.HasPrefix(host1.cleanups[0], "browser profile ") {
		t.Fatalf("first release: freed %d cleanups %v", f.freed, host1.cleanups)
	}
	b2.Close()
	f.invoke("BrowserProcessExited", 0, exitKindNormal)
	f.flush()
	if host2.released != 1 || f.freed != 1 || len(host2.cleanups) != 2 || loader.users != 0 || loader.lib != 0 {
		t.Fatalf("last release: freed %d cleanups %v users %d", f.freed, host2.cleanups, loader.users)
	}
	// A later attachment extracts afresh.
	b3, err := newBrowser(platform.BrowserParent{Window: 3, Handle: 79}, config, &fakeHost{ready: true})
	if err != nil || f.loaded != 2 || b3.profile == b1.profile {
		t.Fatalf("reload: %v loaded %d", err, f.loaded)
	}
}

func TestMissingRuntimeAndFailedCreationLeaveNothingLoaded(t *testing.T) {
	f := installFake(t)
	n.runtimeVersion = func(uintptr) (string, error) { return "", errors.New("WebView2 Runtime not found") }
	_, err := newBrowser(platform.BrowserParent{Window: 1, Handle: 77}, platform.BrowserConfig{Mode: platform.BrowserInProcess}, &fakeHost{})
	if err == nil || !strings.Contains(err.Error(), "Runtime not found") || f.freed != 1 || loader.users != 0 {
		t.Fatalf("missing runtime: %v freed %d users %d", err, f.freed, loader.users)
	}
	if !slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "remove ") && strings.Contains(c, loaderPrefix) }) {
		t.Fatalf("extracted loader not removed: %v", f.calls)
	}
	n.runtimeVersion = func(uintptr) (string, error) { return "v", nil }
	n.createEnvironment = func(uintptr, string, handler) hresult { return eFail }
	host := &fakeHost{ready: true}
	b, err := newBrowser(platform.BrowserParent{Window: 1, Handle: 77}, platform.BrowserConfig{Mode: platform.BrowserInProcess}, host)
	if err != nil {
		t.Fatal(err)
	}
	b.Start()
	if len(host.failed) != 1 || b.envPending {
		t.Fatalf("synchronous creation failure: %v", host.failed)
	}
	b.Close()
	f.flush()
	if host.released != 1 || f.freed != 2 {
		t.Fatalf("release after a failed start: released %d freed %d", host.released, f.freed)
	}
}

func TestBoundsFocusAndRefusalsFollowThePolicy(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	b.Bounds(platform.Surface{Width: 5, Height: 5, Visible: false}) // before the controller: kept
	b.Focus()
	if f.did("bounds") || f.did("focus") {
		t.Fatal("native calls before the controller exists")
	}
	started(t, f, b)
	if !f.did("bounds") || !f.did("hidden") {
		t.Fatalf("stored surface not applied at creation: %v", f.calls)
	}
	b.Bounds(platform.Surface{Width: 9, Height: 9, Visible: true})
	b.Focus()
	if !f.did("visible") || !f.did("focus") {
		t.Fatalf("bounds/focus: %v", f.calls)
	}
	f.navigation("https://example.com/", 3, false)
	b.onNavigationStarting(1)
	b.onFrameNavigationStarting(2)
	b.onNewWindowRequested(3)
	b.onPermissionRequested(4)
	b.onDownloadStarting(5)
	s := b.AuditionState()
	if f.count("cancel") != 2 || !f.did("popup-handled") || !f.did("permission-deny") || !f.did("download-cancel") ||
		s.Refused != 2 || s.Popups != 1 || s.Permissions != 1 || s.Downloads != 1 {
		t.Fatalf("policy: %v %+v", f.calls, s)
	}
	if err := b.AuditionCloseAt("environment", func() {}); err == nil {
		t.Fatal("a passed stage was accepted")
	}
	if err := b.AuditionCloseAt("later", func() {}); err == nil {
		t.Fatal("an unknown stage was accepted")
	}
}

func TestStagedCloseHooksFire(t *testing.T) {
	for _, stage := range []string{"environment", "controller", "cookie", "navigation"} {
		t.Run(stage, func(t *testing.T) {
			host := &fakeHost{ready: true}
			f, b := newTestBrowser(t, host, platform.BrowserLoopback)
			closed := false
			if err := b.AuditionCloseAt(stage, func() { closed = true; b.Close() }); err != nil {
				t.Fatal(err)
			}
			b.Start()
			if stage != "environment" {
				f.invoke("EnvironmentCompleted", 0, 100)
			}
			if stage == "cookie" || stage == "navigation" {
				f.invoke("ControllerCompleted", 0, 200)
			}
			if stage == "navigation" {
				f.invoke("GetCookiesCompleted", 0, 402)
			}
			if !closed || f.did("navigate http://127.0.0.1:4000/") {
				t.Fatalf("stage %s: closed %t calls %v", stage, closed, f.calls)
			}
		})
	}
}

// A stage whose creation call is in flight closes at once: the installed
// Runtime completes the environment inside its creation call, so from an
// audition that runs after Open only the controller, cookie and navigation
// stages can still be pending.
func TestStagedCloseDuringAnInFlightCreationClosesNow(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserLoopback)
	b.Start()
	f.invoke("EnvironmentCompleted", 0, 100) // the controller is now in flight
	closed := false
	if err := b.AuditionCloseAt("environment", func() {}); err == nil {
		t.Fatal("a completed stage was accepted")
	}
	if err := b.AuditionCloseAt("controller", func() { closed = true; b.Close() }); err != nil || !closed {
		t.Fatalf("in-flight controller stage: %v closed %t", err, closed)
	}
	f.invoke("ControllerCompleted", 0, 200)
	if !f.did("controller-close") || f.did("settings") {
		t.Fatalf("late controller not closed unattached: %v", f.calls)
	}
}
