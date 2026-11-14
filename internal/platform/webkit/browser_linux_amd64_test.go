package webkit

import (
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// These tests replace native entry points with recorders; they load no
// library and need no display. Native behavior is the integrated matrix's.

type fakeNative struct {
	t         *testing.T
	calls     []string
	asyncs    []uintptr // pending async tokens, in order
	idles     []uintptr
	copies    map[uintptr]uintptr // GBytes free tokens -> storage
	finished  map[uintptr]string  // native request -> how it finished
	filter    uintptr             // what filter_store_save_finish returns
	filterErr uintptr             // the GError it reports, if any
	cookieErr uintptr             // add_cookie_finish fails with this GError when set
	statuses  []uint32            // response statuses delivered
	gerrors   map[uintptr]int     // GError allocations -> frees
	finishes  map[uintptr]int     // native request -> times finished
	keep      [][]byte            // g_malloc storage, kept alive
	nextObj   uintptr
}

func install(t *testing.T) *fakeNative {
	t.Helper()
	saved := w
	t.Cleanup(func() { w = saved })
	f := &fakeNative{t: t, copies: map[uintptr]uintptr{}, finished: map[uintptr]string{}, filter: 900, nextObj: 1000,
		gerrors: map[uintptr]int{}, finishes: map[uintptr]int{}}
	obj := func() uintptr { f.nextObj++; return f.nextObj }
	method, uri := cstr(t, "GET"), cstr(t, Origin+"/api")
	call := func(name string) { f.calls = append(f.calls, name) }
	w = api{
		ref:        func(p uintptr) uintptr { return p },
		refSink:    func(p uintptr) uintptr { return p },
		unref:      func(p uintptr) { call("unref") },
		connect:    func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64 { return uint64(obj()) },
		disconnect: func(uintptr, uint64) {},
		idleAdd: func(_ int32, _ uintptr, token uintptr, _ uintptr) uint32 {
			f.idles = append(f.idles, token)
			return 1
		},
		malloc: func(n uintptr) uintptr {
			// Native memory, as g_malloc returns: checkptr rejects treating
			// Go heap memory as an address from C.
			b, err := syscall.Mmap(-1, 0, int(max(n, 1)), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
			if err != nil {
				t.Fatal(err)
			}
			f.keep = append(f.keep, b)
			t.Cleanup(func() { syscall.Munmap(b) })
			return uintptr(unsafe.Pointer(unsafe.SliceData(b)))
		},
		free:     func(uintptr) {},
		bytesNew: func(unsafe.Pointer, uintptr) uintptr { return obj() },
		bytesNewWithFree: func(data, size, _ uintptr, token uintptr) uintptr {
			f.copies[token] = data
			return obj()
		},
		bytesData:         func(uintptr, *uintptr) unsafe.Pointer { return nil },
		bytesUnref:        func(uintptr) {},
		memoryStream:      func(uintptr) uintptr { return obj() },
		readBytesAsync:    func(_, _ uintptr, _ int32, _, _, token uintptr) { f.asyncs = append(f.asyncs, token) },
		readBytesFinish:   func(uintptr, uintptr, *uintptr) uintptr { return 0 },
		cancellableNew:    func() uintptr { return obj() },
		cancellableCancel: func(uintptr) { call("cancel") },
		errorNewLiteral: func(uint32, int32, string) uintptr {
			e := obj()
			f.gerrors[e] = 0
			return e
		},
		errorFree: func(e uintptr) {
			if _, ok := f.gerrors[e]; ok {
				f.gerrors[e]++
			}
		},
		quarkFromString: func(string) uint32 { return 1 },
		quarkToString: func(q uint32) string {
			if q == quarkIOError {
				return "g-io-error-quark"
			}
			return "fake"
		},
		containerAdd:        func(uintptr, uintptr) { call("container-add") },
		widgetShow:          func(uintptr) {},
		widgetDestroy:       func(uintptr) { call("view-destroy") },
		headersNew:          func(int32) uintptr { return obj() },
		headersAppend:       func(uintptr, string, string) {},
		headersIterInit:     func(*[3]uintptr, uintptr) {},
		headersIterNext:     func(*[3]uintptr, *uintptr, *uintptr) int32 { return 0 },
		cookieNew:           func(string, string, string, string, int32) uintptr { return obj() },
		cookieSetHTTPOnly:   func(uintptr, int32) {},
		cookieSetSameSite:   func(uintptr, int32) {},
		cookieFree:          func(uintptr) {},
		majorVersion:        func() uint32 { return 2 },
		minorVersion:        func() uint32 { return 52 },
		microVersion:        func() uint32 { return 6 },
		contextNewEphemeral: func() uintptr { return obj() },
		securityManager:     func(uintptr) uintptr { return obj() },
		schemeAsSecure:      func(uintptr, string) {},
		schemeAsCORS:        func(uintptr, string) {},
		registerScheme:      func(uintptr, string, uintptr, uintptr, uintptr) {},
		dataManager:         func(uintptr) uintptr { return obj() },
		cookieManager:       func(uintptr) uintptr { return obj() },
		addCookie:           func(_, _, _, _, token uintptr) { f.asyncs = append(f.asyncs, token) },
		addCookieFinish: func(_, _ uintptr, e *uintptr) int32 {
			if f.cookieErr != 0 {
				*e = f.cookieErr
				return 0
			}
			return 1
		},
		viewNew:            func(uintptr) uintptr { return obj() },
		viewSettings:       func(uintptr) uintptr { return obj() },
		setDeveloperExtras: func(uintptr, int32) {},
		setOpenWindows:     func(uintptr, int32) {},
		loadURI:            func(_ uintptr, uri string) { call("load " + uri) },
		stopLoading:        func(uintptr) { call("stop") },
		loadAlternateHTML:  func(uintptr, string, string, uintptr) { call("error-page") },
		viewContentManager: func(uintptr) uintptr { return obj() },
		addFilter:          func(uintptr, uintptr) { call("add-filter") },
		filterStoreNew:     func(string) uintptr { return obj() },
		filterStoreSave:    func(_ uintptr, _ string, _, _, _, token uintptr) { f.asyncs = append(f.asyncs, token) },
		filterStoreFinish: func(_, _ uintptr, e *uintptr) uintptr {
			if f.filterErr != 0 {
				*e = f.filterErr
				return 0
			}
			return f.filter
		},
		filterUnref:         func(uintptr) { call("filter-unref") },
		schemeFinish:        func(r, _ uintptr) { f.finished[r] = "response"; f.finishes[r]++ },
		schemeFinishError:   func(r, _ uintptr) { f.finished[r] = "error"; f.finishes[r]++ },
		responseNew:         func(uintptr, int64) uintptr { return obj() },
		responseStatus:      func(_ uintptr, status uint32, _ uintptr) { f.statuses = append(f.statuses, status) },
		responseContentType: func(uintptr, string) {},
		responseHeaders:     func(uintptr, uintptr) {},
		schemeMethod:        func(uintptr) uintptr { return method },
		schemeURI:           func(uintptr) uintptr { return uri },
		schemeHeaders:       func(uintptr) uintptr { return 0 },
		schemeBody:          func(uintptr) uintptr { return 0 },
	}
	removeAll = func(string) error { return nil }
	t.Cleanup(func() { removeAll = defaultRemoveAll })
	return f
}

// cstr places a NUL-terminated string in native (non-Go) memory, as WebKit
// owns request strings, and unmaps it when the test ends.
func cstr(t *testing.T, s string) uintptr {
	t.Helper()
	b, err := syscall.Mmap(-1, 0, len(s)+1, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	copy(b, s)
	t.Cleanup(func() { syscall.Munmap(b) })
	return uintptr(unsafe.Pointer(unsafe.SliceData(b)))
}

// quarkIOError is the fake quark for GLib's G_IO_ERROR domain.
const quarkIOError = 2

// complete runs the oldest pending async callback, as GLib would.
func (f *fakeNative) complete() {
	f.t.Helper()
	if len(f.asyncs) == 0 {
		f.t.Fatal("no pending async operation")
	}
	token := f.asyncs[0]
	f.asyncs = f.asyncs[1:]
	t := drop(token)
	t.b.completed(t.async, 1, 2)
}

// idle runs the oldest queued idle callback, as the main loop would.
func (f *fakeNative) idle() {
	f.t.Helper()
	if len(f.idles) == 0 {
		f.t.Fatal("no idle queued")
	}
	token := f.idles[0]
	f.idles = f.idles[1:]
	t := drop(token)
	switch t.kind {
	case kindDestroy:
		t.b.destroy()
	case kindResponseIdle:
		t.b.responseFreed(t.id)
	}
}

// freeCopy releases a response copy, as WebKit dropping its GBytes would,
// then runs the idle hop to the UI thread.
func (f *fakeNative) freeCopy() {
	f.t.Helper()
	for token := range f.copies {
		delete(f.copies, token)
		registry.Lock()
		t := registry.targets[token]
		t.kind = kindResponseIdle
		registry.Unlock()
		f.idles = append(f.idles, token)
		f.idle()
		return
	}
	f.t.Fatal("no response copy held")
}

func (f *fakeNative) did(name string) bool {
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

type fakeHost struct {
	mu        sync.Mutex
	ready     bool
	readies   int
	failed    []error
	released  int
	cleanups  []string
	decisions []platform.RequestDecision
	releasedR []platform.ID
	requests  []platform.BrowserRequest
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
func (h *fakeHost) RequestBody(platform.ID, []byte) platform.RequestDecision {
	return platform.RequestDecision{Action: platform.RequestAbort}
}
func (h *fakeHost) RequestBodyEnd(id platform.ID, err error) platform.RequestDecision {
	return platform.RequestDecision{ID: id, Action: platform.RequestAbort}
}
func (h *fakeHost) RequestReleased(id platform.ID) { h.releasedR = append(h.releasedR, id) }

// newTestBrowser builds an in-process attachment over fakes, skipping the
// process-wide library load and signal reservation.
func newTestBrowser(t *testing.T, host *fakeHost, mode platform.BrowserMode) (*fakeNative, *browser) {
	t.Helper()
	f := install(t)
	loadOnce.Do(func() {}) // never bind real libraries in these tests
	reservation.Lock()
	saved := reservation.signal
	reservation.signal = 60
	reservation.Unlock()
	t.Cleanup(func() { reservation.Lock(); reservation.signal = saved; reservation.Unlock() })
	config := platform.BrowserConfig{Mode: mode, GCSignal: 60, RequestMetadata: 64 << 10}
	if mode == platform.BrowserLoopback {
		config.StartURL, config.Origins = "http://127.0.0.1:4000/", []string{"http://127.0.0.1:4000"}
		config.Credential = &platform.BrowserCookie{Name: "c", Value: "v", Origin: "http://127.0.0.1:4000"}
	}
	if mode == platform.BrowserAttach {
		config.StartURL, config.Origins = "http://127.0.0.1:9/", []string{"http://127.0.0.1:9"}
	}
	attachment, err := newBrowser(platform.BrowserParent{Window: 1, Handle: 77}, config, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	return f, attachment
}

func TestFirstNavigationWaitsForFilterAndCredentialAndReadiness(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserLoopback)
	b.Start()
	if len(f.asyncs) != 2 || f.did("load http://127.0.0.1:4000/") {
		t.Fatalf("navigated before filter and credential: %v", f.calls)
	}
	f.complete() // the filter
	if f.did("load http://127.0.0.1:4000/") {
		t.Fatal("navigated before the credential was installed")
	}
	f.complete() // the cookie
	if !f.did("add-filter") || !f.did("load http://127.0.0.1:4000/") || host.readies != 1 {
		t.Fatalf("calls %v readies %d", f.calls, host.readies)
	}
}

func TestClosingWindowRefusesNavigationAndReleasesLateResults(t *testing.T) {
	host := &fakeHost{ready: false} // the core reports the window closing
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	b.Start()
	f.complete()
	if f.did("load " + Origin + "/") {
		t.Fatal("navigated although the core refused readiness")
	}
	// A close before the filter is saved: the late filter is released,
	// never installed, and destruction waits for that callback.
	host2 := &fakeHost{ready: true}
	f2, b2 := newTestBrowser(t, host2, platform.BrowserInProcess)
	_ = f
	b2.Start()
	b2.Close()
	if len(f2.idles) != 0 {
		t.Fatal("destroy scheduled while a native callback is still owed")
	}
	f2.complete()
	if f2.did("add-filter") || !f2.did("filter-unref") || f2.did("load "+Origin+"/") {
		t.Fatalf("late filter attached or navigated: %v", f2.calls)
	}
	if len(f2.idles) != 1 {
		t.Fatal("destroy not scheduled after the last callback")
	}
	f2.idle()
	if !f2.did("view-destroy") || host2.released != 1 || len(host2.cleanups) != 1 {
		t.Fatalf("destroy %v released %d cleanups %v", f2.calls, host2.released, host2.cleanups)
	}
}

func TestCloseSettlesRequestsAndReleasesOnlyAfterResponseCopiesFree(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{
		{ID: 1, Action: platform.RequestWait}, {ID: 2, Action: platform.RequestWait}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	b.Start()
	f.complete()
	b.receive(501)
	b.receive(502)
	if !b.Respond(1, platform.BrowserResponse{Status: 200, Body: []byte("ok")}) || f.finished[501] != "response" {
		t.Fatal("response not delivered")
	}
	if len(host.releasedR) != 0 {
		t.Fatal("request released while WebKit still holds its response copy")
	}
	b.Close()
	if f.finished[502] != "error" || len(host.releasedR) != 1 || host.releasedR[0] != 2 {
		t.Fatalf("waiting request not settled at close: %v %v", f.finished, host.releasedR)
	}
	if b.Respond(2, platform.BrowserResponse{Status: 200}) {
		t.Fatal("response accepted after close")
	}
	f.idle() // destroy
	if !f.did("view-destroy") || host.released != 0 {
		t.Fatal("released while a response copy is still held")
	}
	f.freeCopy()
	if host.released != 1 || len(host.releasedR) != 2 || host.releasedR[1] != 1 {
		t.Fatalf("released %d requests %v", host.released, host.releasedR)
	}
}

func TestReadingRequestFinishesWhenItsCanceledReadReturns(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 7, Action: platform.RequestRead, ReadChunk: 4}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	w.schemeBody = func(uintptr) uintptr { return 555 }
	b.Start()
	f.complete()
	b.receive(601)
	if len(f.asyncs) != 1 {
		t.Fatal("no asynchronous body read")
	}
	b.Close()
	if _, done := f.finished[601]; done || len(f.idles) != 0 {
		t.Fatal("reading request finished, or destroy scheduled, before its read returned")
	}
	f.complete() // the canceled read
	if f.finished[601] != "error" || len(host.releasedR) != 1 || len(f.idles) != 1 {
		t.Fatalf("finished %v released %v idles %d", f.finished, host.releasedR, len(f.idles))
	}
	f.idle()
	if host.released != 1 {
		t.Fatal("not released")
	}
}

func TestErrorActionsAreSingleUseAndOfferRetryOnlyForGET(t *testing.T) {
	host := &fakeHost{ready: true}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	b.Start()
	f.complete()
	b.nav.record(Origin+"/api/fail", "GET")
	b.loadFailed(Origin+"/api/fail", 0)
	good := b.failure.nonce
	b.errorAction(errorScheme + "retry/forged")
	if f.did("load " + Origin + "/api/fail") {
		t.Fatal("forged action followed")
	}
	b.errorAction(errorScheme + "retry/" + good)
	if !f.did("load " + Origin + "/api/fail") {
		t.Fatal("Retry not followed")
	}
	f.calls = nil
	b.errorAction(errorScheme + "start/" + good)
	if f.did("load " + Origin + "/") {
		t.Fatal("a used error page's action was followed again")
	}
	b.nav.record(Origin+"/api/edit", "") // an in-process form: method unknown
	b.loadFailed(Origin+"/api/edit", 0)
	n := b.failure.nonce
	b.errorAction(errorScheme + "retry/" + n)
	if f.did("load " + Origin + "/api/edit") {
		t.Fatal("Retry repeated a form submission")
	}
	b.errorAction(errorScheme + "start/" + n)
	if !f.did("load " + Origin + "/") {
		t.Fatal("Return to start not followed")
	}
	events := b.AuditionState().Events
	if !strings.Contains(events[len(events)-1].Kind, "error-action") {
		t.Fatalf("%+v", events)
	}
}

func TestNewNavigationInvalidatesSameURIErrorActions(t *testing.T) {
	for _, action := range []string{"retry", "start"} {
		t.Run(action, func(t *testing.T) {
			f, b := newTestBrowser(t, &fakeHost{ready: true}, platform.BrowserInProcess)
			b.Start()
			f.complete()
			uri := Origin + "/api/fail"
			b.nav.record(uri, "GET")
			b.loadFailed(uri, 0)
			old := b.failure.nonce
			if old == "" || !f.did("error-page") {
				t.Fatal("error document did not retain its actions")
			}
			w.navigationAction = func(uintptr) uintptr { return 1 }
			w.actionRequest = func(uintptr) uintptr { return 2 }
			w.requestURI = func(uintptr) string { return uri }
			w.requestMethod = func(uintptr) string { return "GET" }
			w.actionType = func(uintptr) int32 { return 3 } // reload
			w.actionIsRedirect = func(uintptr) int32 { return 0 }
			// This is an ordinary navigation, even though its URI matches
			// the failed load. No further load failure occurs.
			b.policy(1, policyNavigation)
			f.calls = nil
			b.errorAction(errorScheme + action + "/" + old)
			if len(f.calls) != 0 {
				t.Fatalf("stale %s caused native work: %v", action, f.calls)
			}
			if b.failure.nonce != "" {
				t.Fatal("old failure still owns actions after navigation")
			}
		})
	}
}

func TestCleanupResidueIsNamedAfterBoundedAttempts(t *testing.T) {
	install(t)
	attempts := 0
	removeAll = func(string) error { attempts++; return errors.New("busy") }
	err := removeAfterReleaseWith("/tmp/dfx2-webview-filter-x", 3, 0)
	var residue *platform.FileDeletionResidue
	if !errors.As(err, &residue) || residue.Path != "/tmp/dfx2-webview-filter-x" || attempts != 3 {
		t.Fatalf("%v after %d attempts", err, attempts)
	}
}

// Every native failure path finishes its request once and frees the GError
// it allocated once: the ID-zero refusal, an admitted request's failure at
// close, and a request arriving for a closing window.
func TestNativeFailuresFinishOnceAndFreeTheirErrors(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{
		{Action: platform.RequestAbort},        // refused without admission: ID zero
		{ID: 4, Action: platform.RequestWait}}} // admitted, failed at close
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	b.Start()
	f.complete()
	b.receive(1001)
	b.receive(1002)
	b.Close()
	b.receive(1003) // arrives while closing: refused as an orphan
	for _, request := range []uintptr{1001, 1002, 1003} {
		if f.finishes[request] != 1 || f.finished[request] != "error" {
			t.Errorf("request %d finished %d times (%q)", request, f.finishes[request], f.finished[request])
		}
	}
	if len(f.gerrors) != 3 {
		t.Fatalf("%d GErrors allocated for three failures", len(f.gerrors))
	}
	for e, frees := range f.gerrors {
		if frees != 1 {
			t.Errorf("GError %d freed %d times", e, frees)
		}
	}
	if len(host.releasedR) != 1 || host.releasedR[0] != 4 {
		t.Fatalf("released %v", host.releasedR)
	}
}

// Initialization results that arrive after Close: only GLib's own
// cancellation is silent; any other failure is still a browser failure; a
// filter that saved anyway is released, never installed, and nothing
// navigates. Every path settles its callback, so the attachment releases.
func TestInitializationResultsAfterClose(t *testing.T) {
	for _, op := range []string{"filter", "credential"} {
		for _, outcome := range []string{"canceled", "failed", "foreign-19", "succeeded"} {
			t.Run(op+" "+outcome, func(t *testing.T) {
				host := &fakeHost{ready: true}
				f, b := newTestBrowser(t, host, platform.BrowserLoopback) // a filter save, then a credential
				b.Start()
				b.Close()
				canceled := nativeErrorOf(t, quarkIOError, ioErrorCancelled)
				f.filterErr, f.cookieErr = canceled, canceled // the other operation is canceled, as close does
				var result uintptr
				switch outcome {
				case "failed":
					result = nativeErrorOf(t, quarkIOError, 15) // G_IO_ERROR_NOT_SUPPORTED
				case "foreign-19": // the cancellation code, but not GLib's I/O domain
					result = nativeErrorOf(t, 1, ioErrorCancelled)
				case "canceled":
					result = canceled
				}
				if op == "filter" {
					f.filterErr = result
				} else {
					f.cookieErr = result
				}
				f.complete() // the filter save
				f.complete() // the credential
				switch outcome {
				case "failed", "foreign-19":
					if len(host.failed) != 1 || !strings.Contains(host.failed[0].Error(), map[string]string{"failed": " 15", "foreign-19": "fake 19"}[outcome]) {
						t.Fatalf("genuine %s failure after close lost: %v", op, host.failed)
					}
				default:
					if len(host.failed) != 0 {
						t.Fatalf("reported %v", host.failed)
					}
				}
				if f.did("add-filter") || f.did("load http://127.0.0.1:4000/") || host.readies != 0 {
					t.Fatalf("installed or navigated after close: %v", f.calls)
				}
				if op == "filter" && outcome == "succeeded" && !f.did("filter-unref") {
					t.Fatal("a filter saved after close was not released")
				}
				if len(f.idles) != 1 {
					t.Fatalf("destroy not scheduled once every callback settled: %d idles, %d asyncs", len(f.idles), b.asyncs)
				}
				f.idle()
				if host.released != 1 {
					t.Fatal("attachment never released")
				}
			})
		}
	}
}
