package webview2

import "github.com/michaelquigley/dfx2/internal/platform"

// api is the native surface the attachment uses: COM calls into WebView2 and
// the few Win32 calls around them. The Windows files bind it on the first
// attachment; tests replace it with recorders. Every function runs on the UI
// thread unless it says otherwise. Handles are COM interface pointers (or
// test tokens); strings returned are Go-owned copies.
type api struct {
	// Process-wide.
	initCOM        func() error
	loaderBlob     func() []byte
	loadLibrary    func(path string) (uintptr, error)
	freeLibrary    func(lib uintptr) error
	runtimeVersion func(lib uintptr) (string, error)
	// post retains fn until a later UI-thread dispatch, retrying failed wakes.
	// It is available only after installation; it never runs fn inline.
	post func(fn func())

	// Handler objects the host implements. invoke runs on whichever thread the
	// engine calls on; dropped runs when the engine releases the object without
	// ever invoking it, on any thread. Either settles a completion once.
	newHandler func(name string, iid guid, invoke func(a, b uintptr) hresult, dropped func()) handler

	// IUnknown.
	queryInterface func(obj uintptr, iid *guid) (uintptr, hresult)
	addRef         func(obj uintptr)
	release        func(obj uintptr)

	// Environment creation and the environment.
	createEnvironment func(lib uintptr, userData string, cb handler) hresult
	envVersion        func(env uintptr) string
	envCreateCtrl     func(env uintptr, parent uintptr, cb handler) hresult
	envCreateResponse func(env uintptr, body uintptr, status int, reason, headers string) (uintptr, hresult)
	env5AddExited     func(env5 uintptr, cb handler) (int64, hresult)
	env5RemoveExited  func(env5 uintptr, token int64)
	exitedArgs        func(args uintptr) (kind int32, pid uint32)

	// Controller and web view.
	ctrlWebView     func(ctl uintptr) (uintptr, hresult)
	ctrlVisible     func(ctl uintptr, visible bool)
	ctrlBounds      func(ctl uintptr, width, height int32)
	ctrlNotifyMoved func(ctl uintptr)
	ctrlFocus       func(ctl uintptr)
	ctrlClose       func(ctl uintptr) hresult
	wvBrowserPID    func(wv uintptr) uint32
	wvSettings      func(wv uintptr, devtools bool) hresult
	wvProfilePath   func(wv uintptr) (string, bool)
	wvAddEvent      func(wv uintptr, add int, cb handler) (int64, hresult)
	wvRemoveEvent   func(wv uintptr, remove int, token int64)
	wvAddFilter     func(wv uintptr, pattern string, context int32) hresult
	wvNavigate      func(wv uintptr, uri string) hresult
	wvNavigateHTML  func(wv uintptr, html string) hresult
	wvStop          func(wv uintptr)
	wvSource        func(wv uintptr) string
	wvTitle         func(wv uintptr) string
	wvOpenDevTools  func(wv uintptr)
	wvCookieManager func(wv2 uintptr) (uintptr, hresult)

	// Event arguments.
	navStarting      func(args uintptr) (uri string, id uint64, redirected bool)
	navCancel        func(args uintptr)
	navCompleted     func(args uintptr) (success bool, status int32, id uint64, httpStatus int32)
	newWindowHandled func(args uintptr)
	processFailed    func(args uintptr) (kind, reason, exitCode int32, description string)
	permissionDeny   func(args uintptr)
	downloadCancel   func(args uintptr)
	responseReceived func(args uintptr) (uri string, status int32)

	// Intercepted requests. requestInfo's req is a reference the caller
	// releases. Header iteration converts nothing until asked: wlen measures a
	// UTF-16 string in place, take converts and frees it, free only frees.
	requestInfo    func(args uintptr) (req uintptr, uri, method string, context int32)
	requestHeaders func(req uintptr) (it uintptr, hr hresult)
	iterHas        func(it uintptr) bool
	iterCurrent    func(it uintptr) (name, value *uint16, ok bool)
	iterNext       func(it uintptr)
	wlen           func(p *uint16) int
	take           func(p *uint16) string
	free           func(p *uint16)
	requestContent func(req uintptr) uintptr
	streamRead     func(stream uintptr, buf []byte) (n int, end bool, hr hresult)
	deferral       func(args uintptr) (uintptr, hresult)
	deferralDone   func(def uintptr) hresult
	putResponse    func(args uintptr, resp uintptr) hresult
	// newBodyStream is a response body the host implements as an IStream over
	// a copy of body; released runs, on any thread, when the engine drops its
	// last reference.
	newBodyStream func(body []byte, released func()) uintptr

	// Cookies.
	cookieCreate func(cm uintptr, name, value, domain, path string) (uintptr, hresult)
	cookieSecure func(cookie uintptr) // HttpOnly, SameSite=Strict
	cookieAdd    func(cm uintptr, cookie uintptr) hresult
	cookieList   func(cm uintptr, uri string, cb handler) hresult
	cookieFound  func(list uintptr, name string) (found, httpOnly bool, sameSite int32)
}

// handler is a COM object the host implements, held by the engine.
type handler interface {
	// ptr is the interface pointer passed to the engine.
	ptr() uintptr
	// release drops the host's own reference, after the engine took its own.
	release()
}

// n is the bound native surface.
var n api

var _ = platform.ID(0)
