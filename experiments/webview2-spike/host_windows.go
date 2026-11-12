//go:build windows && amd64

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"dfx2webview2spike/adapt"
)

// The UI thread: the main goroutine stays on the process's first thread,
// which is the COM STA that owns the window and every WebView2 object.
func init() { runtime.LockOSThread() }

// loaderSHA256 pins the x64 WebView2Loader.dll from Microsoft.Web.WebView2
// 1.0.4129.50 (build\native\x64). A different file is reported, not refused.
const loaderSHA256 = "a9a09232c25805323d4cfb3fc8f545a190a9c8a99c93262ea99d0b88df99ec90"

type hostState int

const (
	starting hostState = iota // environment or controller creation pending
	running
	closing // stop accepting work; waiting only for a creation callback
	closed  // DestroyWindow called
)

func (s hostState) String() string {
	return [...]string{"starting", "running", "closing", "closed"}[s]
}

// pending is one intercepted request whose response isn't installed yet.
// Its COM references are touched only on the UI thread.
type pending struct {
	args, deferral *com
	what           string
	start          time.Time
}

// poster posts wakeups to the window from any goroutine until closed.
// After close returns no post can reach the window, so it may be destroyed
// (and its handle reused) safely.
type poster struct {
	mu     sync.Mutex
	hwnd   uintptr
	closed bool
	failed int
}

func (p *poster) post(msg uint32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.hwnd == 0 {
		return false
	}
	ok, _, _ := pPostMessageW.Call(p.hwnd, uintptr(msg), 0, 0)
	if ok == 0 {
		p.failed++
	}
	return ok != 0
}

func (p *poster) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

type host struct {
	cfg     config
	handler http.Handler
	hwnd    uintptr
	thread  uint32
	state   hostState
	err     error // the reason the app exits non-zero

	loader     *syscall.DLL
	envPending bool
	ctlPending bool
	env, ctl   *com
	wv         *com
	tokens     []eventToken

	origin *url.URL
	start  string // the first navigation
	ctx    context.Context
	cancel context.CancelFunc
	poster poster

	// in-process
	disp    *adapt.Dispatcher
	pending map[uint64]*pending
	// loopback
	ln        net.Listener
	srv       *http.Server
	serverErr error
	serverMu  sync.Mutex

	launched     time.Time
	closeAsked   time.Time
	destroyed    bool
	navigations  int
	devtoolsOpen bool

	loaderHash, runtimeVersion string // what the prerequisite checks found, for the feasibility host's record
	counts                     struct{ intercepted, installed, settledOnClose, refusedOrigin, late, invoked, offUI int }
}

// eventToken is a registration to remove at teardown.
type eventToken struct {
	name   string
	remove int
	value  int64
}

// callback makes a handler object whose Invoke also checks the threading
// contract: WebView2 calls back on the thread that created the object (the
// UI thread), so any other thread is a finding.
func (h *host) callback(name string, iid guid, fn func(a, b uintptr) hresult) *callback {
	return newCallback(name, iid, func(a, b uintptr) hresult {
		h.counts.invoked++
		if t := currentThread(); t != h.thread {
			h.counts.offUI++
			log.Printf("com: %s invoked on thread %d, not the UI thread %d", name, t, h.thread)
		}
		return fn(a, b)
	})
}

var hosts = map[uintptr]*host{} // by hwnd's user data; UI thread only

func runHost(c config, h http.Handler) error {
	hs := &host{cfg: c, handler: h, launched: time.Now(), pending: map[uint64]*pending{}}
	hs.ctx, hs.cancel = context.WithCancel(context.Background())
	defer hs.cancel()
	return hs.run()
}

func (h *host) run() error {
	for _, p := range allProcs {
		if err := p.Find(); err != nil {
			return fmt.Errorf("win32: %w", err)
		}
	}
	h.thread = currentThread()
	if hr := hresult(callProc(pCoInitializeEx, 0, coinitApartmentThreaded|coinitDisableOLE1DDE)); hr.failed() {
		return hr.err("CoInitializeEx(STA)")
	}
	defer pCoUninitialize.Call()
	if err := perMonitorDPI(); err != nil {
		return err
	}

	// Prerequisites first, so a missing one is an error on the console and
	// never a blank window.
	if err := h.loadLoader(); err != nil {
		return err
	}
	if err := h.checkRuntime(); err != nil {
		return err
	}
	ud, err := h.userDataDir()
	if err != nil {
		return err
	}

	if err := h.createWindow(); err != nil {
		return err
	}
	if err := h.startTransport(); err != nil {
		pDestroyWindow.Call(h.hwnd)
		return err
	}
	if err := h.createEnvironment(ud); err != nil {
		h.poster.close()
		pDestroyWindow.Call(h.hwnd)
		h.stopTransport()
		return err
	}

	loopErr := h.loop()
	h.afterLoop()
	if loopErr != nil {
		return loopErr
	}
	return h.err
}

func (h *host) exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func (h *host) loadLoader() error {
	path := h.cfg.loader
	if path == "" {
		path = filepath.Join(h.exeDir(), "WebView2Loader.dll")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("WebView2Loader.dll not found at %s\n"+
			"  It comes from the WebView2 SDK, not from Windows. Run .\\setup-loader.ps1 in experiments\\webview2-spike\n"+
			"  (it downloads Microsoft.Web.WebView2 1.0.4129.50 and copies the x64 loader beside the executable),\n"+
			"  or pass -loader <path>.", abs)
	}
	sum, err := fileSHA256(abs)
	if err != nil {
		return err
	}
	// An absolute path: LoadLibrary takes exactly this file, never one
	// found on the search path.
	dll, err := syscall.LoadDLL(abs)
	if err != nil {
		return fmt.Errorf("loading %s: %w", abs, err)
	}
	h.loader = dll
	h.loaderHash = sum
	match := "matches the pinned SDK 1.0.4129.50 x64 loader"
	if sum != loaderSHA256 {
		match = "WARNING: does not match the pinned loader " + loaderSHA256
	}
	log.Printf("loader: %s sha256 %s (%s)", abs, sum, match)
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := sha256.New()
	if _, err := io.Copy(s, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(s.Sum(nil)), nil
}

func (h *host) runtimeDirPtr() uintptr {
	if h.cfg.runtimeDir == "" {
		return 0
	}
	return uintptr(unsafe.Pointer(utf16(h.cfg.runtimeDir)))
}

func (h *host) checkRuntime() error {
	p, err := h.loader.FindProc("GetAvailableCoreWebView2BrowserVersionString")
	if err != nil {
		return err
	}
	var v *uint16
	hr := hresult(callProc(p, h.runtimeDirPtr(), uintptr(unsafe.Pointer(&v))))
	ver := takeString(v)
	where := "installed Evergreen Runtime"
	if h.cfg.runtimeDir != "" {
		where = "fixed-version Runtime in " + h.cfg.runtimeDir
	}
	if hr.failed() || ver == "" {
		return fmt.Errorf("WebView2 Runtime not found (%s, HRESULT %v)\n"+
			"  Install the Evergreen WebView2 Runtime from https://developer.microsoft.com/microsoft-edge/webview2/\n"+
			"  (Windows 11 includes it), or pass -runtime-dir with a fixed-version Runtime folder.", where, hr)
	}
	h.runtimeVersion = ver
	log.Printf("runtime: %s %s", where, ver)
	return nil
}

func (h *host) userDataDir() (string, error) {
	ud := h.cfg.userData
	if ud == "" {
		ud = filepath.Join(h.exeDir(), "webview2-userdata", h.cfg.mode)
	}
	ud, err := filepath.Abs(ud)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(ud, 0o755); err != nil {
		return "", err
	}
	log.Printf("user data: %s", ud)
	return ud, nil
}

var wndProcCB uintptr // the window procedure, made once: callbacks are never freed

func (h *host) createWindow() error {
	inst, _, err := pGetModuleHandleW.Call(0)
	if inst == 0 {
		return fmt.Errorf("GetModuleHandleW: %w", err)
	}
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	className := utf16("dfx2WebView2Spike")
	if wndProcCB == 0 {
		wndProcCB = syscall.NewCallback(wndProc)
	}
	wc := wndClassEx{wndProc: wndProcCB, instance: inst, cursor: cursor, background: colorWindow + 1, className: className}
	wc.size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassExW: %w", err)
	}

	const id = 1
	hosts[id] = h
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16(h.title()))),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, cwUseDefault, cwUseDefault, 0, 0, inst, id)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %w", err)
	}
	h.hwnd = hwnd
	h.poster.hwnd = hwnd
	h.watchConsole()

	d, _, _ := pGetDpiForWindow.Call(hwnd)
	dpi := uint32(d)
	r := rect{right: int32(muldiv(h.cfg.width, dpi)), bottom: int32(muldiv(h.cfg.height, dpi))}
	pAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), wsOverlappedWindow, 0, 0, uintptr(dpi))
	pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpNoMove|swpNoZOrder|swpNoActivate)
	log.Printf("window: hwnd %#x on UI thread %d, %d dpi (scale %g), client %dx%d DIPs", hwnd, h.thread, dpi, float32(dpi)/96, h.cfg.width, h.cfg.height)
	pShowWindow.Call(hwnd, swShow)
	return nil
}

func muldiv(dip int, dpi uint32) int { return (dip*int(dpi) + 48) / 96 }

func (h *host) title() string {
	t := "dfx2 WebView2 spike — " + h.cfg.mode
	if h.origin != nil {
		t += " — " + h.origin.String()
	}
	if h.state != running {
		t += " (" + h.state.String() + ")"
	}
	return t
}

func (h *host) setTitle() {
	if h.hwnd != 0 && !h.destroyed {
		pSetWindowTextW.Call(h.hwnd, uintptr(unsafe.Pointer(utf16(h.title()))))
	}
}

// startTransport makes the handler reachable before anything navigates.
func (h *host) startTransport() error {
	switch h.cfg.mode {
	case "loopback":
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("loopback listener: %w", err)
		}
		h.ln = ln
		h.origin = &url.URL{Scheme: "http", Host: ln.Addr().String()}
		h.srv = &http.Server{
			Handler: adapt.Instrument(h.handler, log.Printf),
			// Handlers' contexts derive from the window's, so closing the
			// window cancels them; Shutdown alone would only wait.
			BaseContext:       func(net.Listener) context.Context { return h.ctx },
			ReadHeaderTimeout: 10 * time.Second,
		}
		log.Printf("transport: loopback HTTP server listening on %s (pid %d)", h.origin, os.Getpid())
		go func() {
			err := h.srv.Serve(ln)
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			h.serverMu.Lock()
			h.serverErr = err
			h.serverMu.Unlock()
			if !h.poster.post(wmServerFailed) {
				log.Printf("transport: server stopped (%v) after the window closed", err)
			}
		}()
		if d := h.cfg.breakListener; d > 0 {
			time.AfterFunc(d, func() {
				log.Printf("transport: -break-listener: closing the listener under the server")
				ln.Close()
			})
		}
	case "inprocess":
		h.origin, _ = url.Parse(inprocessOrigin)
		h.disp = adapt.NewDispatcher(h.ctx, h.handler, h.origin, func() bool { return h.poster.post(wmDispatch) }, log.Printf)
		log.Printf("transport: in-process dispatch for %s; no application listener (pid %d)", h.origin, os.Getpid())
	}
	h.start = h.origin.String() + "/"
	if h.cfg.autorun != "" {
		h.start += "?autorun=" + url.QueryEscape(h.cfg.autorun)
	}
	h.setTitle()
	return nil
}

func (h *host) stopTransport() {
	if h.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		t := time.Now()
		err := h.srv.Shutdown(ctx)
		log.Printf("transport: server shutdown in %v (%v)", time.Since(t).Round(time.Millisecond), errOK(err))
	}
}

func errOK(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// callProc calls a DLL export. Like com.call it relies on uintptrescapes.
//
//go:uintptrescapes
func callProc(p interface {
	Call(...uintptr) (uintptr, uintptr, error)
}, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

func (h *host) createEnvironment(userData string) error {
	create, err := h.loader.FindProc("CreateCoreWebView2EnvironmentWithOptions")
	if err != nil {
		return err
	}
	cb := h.callback("EnvironmentCompleted", iidEnvironmentCompleted, func(a, b uintptr) hresult {
		h.onEnvironment(hresult(uint32(a)), asCOM(b))
		return sOK
	})
	h.envPending = true
	if h.cfg.closeDuring == "env" {
		log.Printf("test: posting WM_CLOSE before environment creation")
		pPostMessageW.Call(h.hwnd, wmClose, 0, 0)
	}
	t := time.Now()
	hr := hresult(callProc(create, h.runtimeDirPtr(), uintptr(unsafe.Pointer(utf16(userData))), 0, cb.ptr()))
	cb.release() // WebView2 holds its own reference until it calls back
	if hr.failed() {
		h.envPending = false
		return hr.err("CreateCoreWebView2EnvironmentWithOptions")
	}
	log.Printf("webview: environment creation started (returned in %v)", time.Since(t).Round(time.Microsecond))
	return nil
}

func (h *host) onEnvironment(hr hresult, env *com) {
	h.envPending = false
	since := time.Since(h.launched).Round(time.Millisecond)
	if h.state >= closing {
		// Late: the window is closing. Not AddRef'd, so returning releases
		// it; nothing attaches to the window.
		h.counts.late++
		log.Printf("webview: late environment callback during close (%v, %v after launch): not attached, released", hr, since)
		h.finishClose()
		return
	}
	if hr.failed() || env == nil {
		h.fail(fmt.Errorf("environment creation failed: HRESULT %v", hr))
		return
	}
	env.addRef()
	h.env = env
	var v *uint16
	env.call(envGetBrowserVersionString, uintptr(unsafe.Pointer(&v)))
	log.Printf("webview: environment ready in %v; browser version %s", since, takeString(v))

	cb := h.callback("ControllerCompleted", iidControllerCompleted, func(a, b uintptr) hresult {
		h.onController(hresult(uint32(a)), asCOM(b))
		return sOK
	})
	h.ctlPending = true
	if h.cfg.closeDuring == "controller" {
		log.Printf("test: posting WM_CLOSE before controller creation")
		pPostMessageW.Call(h.hwnd, wmClose, 0, 0)
	}
	hr = env.call(envCreateController, h.hwnd, cb.ptr())
	cb.release()
	if hr.failed() {
		h.ctlPending = false
		h.fail(hr.err("CreateCoreWebView2Controller"))
	}
}

func (h *host) onController(hr hresult, ctl *com) {
	h.ctlPending = false
	since := time.Since(h.launched).Round(time.Millisecond)
	if h.state >= closing {
		// Late: either the window is being destroyed (WebView2 then calls
		// back with E_ABORT inside DestroyWindow) or close began while
		// creation ran. Close what we were given; never attach it.
		h.counts.late++
		if ctl != nil {
			ctl.call(ctrlClose)
		}
		log.Printf("webview: late controller callback during close (%v, %v after launch): controller %v, closed and not attached", hr, since, ctl != nil)
		return
	}
	if hr.failed() || ctl == nil {
		h.fail(fmt.Errorf("controller creation failed: HRESULT %v", hr))
		return
	}
	ctl.addRef()
	h.ctl = ctl
	var wv *com
	if hr := ctl.call(ctrlGetCoreWebView2, uintptr(unsafe.Pointer(&wv))); hr.failed() {
		h.fail(hr.err("get_CoreWebView2"))
		return
	}
	h.wv = wv
	var pid uint32
	wv.call(wvGetBrowserProcessID, uintptr(unsafe.Pointer(&pid)))
	log.Printf("webview: controller ready in %v; browser process %d", since, pid)

	var settings *com
	if hr := wv.call(wvGetSettings, uintptr(unsafe.Pointer(&settings))); hr.failed() {
		h.fail(hr.err("get_Settings"))
		return
	}
	settings.call(setPutAreDevToolsEnabled, boolArg(h.cfg.devtools))
	settings.release()

	if err := h.addHandlers(); err != nil {
		h.fail(err)
		return
	}
	h.state = running
	h.setTitle()
	h.fitBounds()
	ctl.call(ctrlPutIsVisible, 1)
	ctl.call(ctrlMoveFocus, 0) // COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC
	log.Printf("webview: navigating to %s", h.start)
	if hr := wv.call(wvNavigate, uintptr(unsafe.Pointer(utf16(h.start)))); hr.failed() {
		h.fail(hr.err("Navigate"))
	}
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// addHandlers registers every event handler, and in-process interception,
// before the first navigation.
func (h *host) addHandlers() error {
	type reg struct {
		name        string
		iid         guid
		add, remove int
		fn          func(a, b uintptr) hresult
	}
	regs := []reg{
		{"NavigationStarting", iidNavigationStarting, wvAddNavigationStarting, wvRemoveNavigationStarting, h.onNavigationStarting},
		{"NavigationCompleted", iidNavigationCompleted, wvAddNavigationCompleted, wvRemoveNavigationCompleted, h.onNavigationCompleted},
		{"NewWindowRequested", iidNewWindowRequested, wvAddNewWindowRequested, wvRemoveNewWindowRequested, h.onNewWindowRequested},
		{"ProcessFailed", iidProcessFailed, wvAddProcessFailed, wvRemoveProcessFailed, h.onProcessFailed},
	}
	if h.disp != nil {
		regs = append(regs, reg{"WebResourceRequested", iidWebResourceRequested, wvAddWebResourceRequested, wvRemoveWebResourceRequested, h.onWebResourceRequested})
	}
	for _, r := range regs {
		cb := h.callback(r.name, r.iid, r.fn)
		var tok int64
		hr := h.wv.call(r.add, cb.ptr(), uintptr(unsafe.Pointer(&tok)))
		cb.release() // WebView2 holds its own until remove_ (or its teardown)
		if hr.failed() {
			return hr.err("add_" + r.name)
		}
		h.tokens = append(h.tokens, eventToken{r.name, r.remove, tok})
	}
	if h.disp != nil {
		filter := h.origin.String() + "/*"
		// COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL: documents, scripts,
		// stylesheets, fetch/XHR and the rest.
		if hr := h.wv.call(wvAddWebResourceRequestedFilter, uintptr(unsafe.Pointer(utf16(filter))), 0); hr.failed() {
			return hr.err("AddWebResourceRequestedFilter")
		}
		log.Printf("webview: intercepting %s (all resource contexts)", filter)
	}
	return nil
}

func (h *host) allowed(uri string) bool {
	_, err := adapt.CheckOrigin(uri, h.origin)
	return err == nil
}

func (h *host) onNavigationStarting(sender, args uintptr) hresult {
	a := asCOM(args)
	var u *uint16
	var id uint64
	a.call(navStartGetURI, uintptr(unsafe.Pointer(&u)))
	a.call(navStartGetNavigationID, uintptr(unsafe.Pointer(&id)))
	uri := takeString(u)
	if !h.allowed(uri) {
		a.call(navStartPutCancel, 1)
		log.Printf("origin: navigation %d to %s refused (outside %s)", id, uri, h.origin)
		return sOK
	}
	h.navigations++
	log.Printf("webview: navigation %d starting: %s", id, uri)
	return sOK
}

func (h *host) onNavigationCompleted(sender, args uintptr) hresult {
	a := asCOM(args)
	var ok int32
	var status int32
	var id uint64
	a.call(navDoneGetIsSuccess, uintptr(unsafe.Pointer(&ok)))
	a.call(navDoneGetWebErrorStatus, uintptr(unsafe.Pointer(&status)))
	a.call(navDoneGetNavigationID, uintptr(unsafe.Pointer(&id)))
	log.Printf("webview: navigation %d completed: success %v, web error status %d (%v after launch)", id, ok != 0, status, time.Since(h.launched).Round(time.Millisecond))
	if h.cfg.devtools && !h.devtoolsOpen && h.wv != nil {
		h.devtoolsOpen = true
		h.wv.call(wvOpenDevToolsWindow)
	}
	return sOK
}

func (h *host) onNewWindowRequested(sender, args uintptr) hresult {
	a := asCOM(args)
	var u *uint16
	a.call(newWinGetURI, uintptr(unsafe.Pointer(&u)))
	// Handled with no NewWindow set: no window opens, and window.open
	// returns null in the page.
	a.call(newWinPutHandled, 1)
	log.Printf("origin: new window for %s refused", takeString(u))
	return sOK
}

func (h *host) onProcessFailed(sender, args uintptr) hresult {
	var kind int32
	asCOM(args).call(pfGetProcessFailedKind, uintptr(unsafe.Pointer(&kind)))
	log.Printf("webview: process failed, COREWEBVIEW2_PROCESS_FAILED_KIND %d", kind)
	return sOK
}

// onWebResourceRequested copies one intercepted request into Go-owned data
// on the UI thread, takes a deferral, and starts the handler on a worker.
// The args and deferral stay here, on the UI thread, until deliver or close
// settles them.
func (h *host) onWebResourceRequested(sender, argsPtr uintptr) hresult {
	args := asCOM(argsPtr)
	h.counts.intercepted++
	var req *com
	if hr := args.call(wrrGetRequest, uintptr(unsafe.Pointer(&req))); hr.failed() {
		log.Printf("intercept: get_Request: %v", hr)
		return sOK
	}
	defer req.release()
	var u, m *uint16
	req.call(reqGetURI, uintptr(unsafe.Pointer(&u)))
	req.call(reqGetMethod, uintptr(unsafe.Pointer(&m)))
	in := adapt.Request{URL: takeString(u), Method: takeString(m)}

	if !h.allowed(in.URL) {
		// The filter matched but the parsed origin isn't exactly ours.
		h.counts.refusedOrigin++
		log.Printf("intercept: %s %s refused: not the fixture origin", in.Method, in.URL)
		h.respond(args, synthetic(http.StatusMisdirectedRequest, "not the fixture origin"))
		return sOK
	}
	if h.state != running {
		h.respond(args, synthetic(http.StatusServiceUnavailable, "window closing"))
		return sOK
	}

	var hdrs, it *com
	if req.call(reqGetHeaders, uintptr(unsafe.Pointer(&hdrs))) == sOK && hdrs != nil {
		if hdrs.call(hdrsGetIterator, uintptr(unsafe.Pointer(&it))) == sOK && it != nil {
			for {
				var has int32
				it.call(iterGetHasCurrentHeader, uintptr(unsafe.Pointer(&has)))
				if has == 0 {
					break
				}
				var n, v *uint16
				it.call(iterGetCurrentHeader, uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&v)))
				in.Header = append(in.Header, [2]string{takeString(n), takeString(v)})
				it.call(iterMoveNext, uintptr(unsafe.Pointer(&has)))
			}
			it.release()
		}
		hdrs.release()
	}

	// The body stream belongs to the request; read what the adapter allows
	// (plus one byte, to see "too large") and release it here, on its own
	// thread. Workers never see it.
	var body *com
	if req.call(reqGetContent, uintptr(unsafe.Pointer(&body))) == sOK && body != nil {
		b, tooLarge, err := readStream(body, adapt.Limit)
		body.release()
		if err != nil {
			log.Printf("intercept: %s %s: reading body: %v", in.Method, in.URL, err)
			h.respond(args, synthetic(http.StatusBadRequest, "request body unreadable: "+err.Error()))
			return sOK
		}
		in.Body, in.BodyTooLarge = b, tooLarge
	}

	var def *com
	if hr := args.call(wrrGetDeferral, uintptr(unsafe.Pointer(&def))); hr.failed() {
		log.Printf("intercept: GetDeferral: %v", hr)
		return sOK
	}
	args.addRef() // kept until the response is installed
	id, ok := h.disp.Start(in)
	p := &pending{args: args, deferral: def, what: in.Method + " " + in.URL, start: time.Now()}
	if !ok {
		h.settle(0, p, synthetic(http.StatusServiceUnavailable, "window closing"), "refused")
		return sOK
	}
	h.pending[id] = p
	return sOK
}

func synthetic(status int, msg string) adapt.Response {
	hd := http.Header{}
	hd.Set("Content-Type", "text/plain; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Spike-Host", msg)
	return adapt.Response{Status: status, Reason: http.StatusText(status), Header: hd, Body: []byte(msg + "\n")}
}

// deliver installs the responses whose handlers finished. UI thread only.
func (h *host) deliver() {
	for _, r := range h.disp.Take() {
		p := h.pending[r.ID]
		if p == nil {
			log.Printf("req %d: no pending request for result", r.ID)
			continue
		}
		delete(h.pending, r.ID)
		h.settle(r.ID, p, r.Response, "installed")
	}
}

// settle installs resp for one deferred request, completes its deferral and
// releases both references: every pending request ends here, on success,
// failure or close.
func (h *host) settle(id uint64, p *pending, resp adapt.Response, how string) {
	hrResp := h.respond(p.args, resp)
	hrDone := p.deferral.call(deferralComplete)
	p.deferral.release()
	p.args.release()
	h.counts.installed++
	log.Printf("req %d response %s: %d, %d B, %v after start (put_Response %v, Complete %v; pending %d)",
		id, how, resp.Status, len(resp.Body), time.Since(p.start).Round(time.Millisecond), hrResp, hrDone, len(h.pending))
}

// respond builds a WebView2 response and sets it on args. The body goes in
// a memory stream that owns its own copy.
func (h *host) respond(args *com, resp adapt.Response) hresult {
	block, dropped := adapt.HeaderBlock(resp.Header)
	if len(dropped) > 0 {
		log.Printf("intercept: dropped invalid response headers %v", dropped)
	}
	var stream *com
	if len(resp.Body) > 0 {
		s, err := memStream(resp.Body)
		if err != nil {
			log.Print(err)
			return eFail
		}
		stream = s
		h.checkStreamOnce(s)
	}
	var r *com
	var sp uintptr
	if stream != nil {
		sp = stream.ptr()
	}
	hr := h.env.call(envCreateWebResourceResponse, sp, uintptr(resp.Status),
		uintptr(unsafe.Pointer(utf16(resp.Reason))), uintptr(unsafe.Pointer(utf16(block))), uintptr(unsafe.Pointer(&r)))
	stream.release() // the response holds its own reference
	if hr.failed() {
		log.Printf("intercept: CreateWebResourceResponse: %v", hr)
		return hr
	}
	hr = args.call(wrrPutResponse, r.ptr())
	r.release()
	return hr
}

var streamChecked bool

// checkStreamOnce records what the response stream claims about threading.
// WebView2.idl asks for a response stream that is "agile or created from a
// background thread", because WebView2 may read it off the UI thread.
func (h *host) checkStreamOnce(s *com) {
	if streamChecked {
		return
	}
	streamChecked = true
	agile, hrA := s.queryInterface(&iidIAgileObject)
	agile.release()
	marshal, hrM := s.queryInterface(&iidIMarshal)
	marshal.release()
	log.Printf("intercept: SHCreateMemStream stream: IAgileObject %v, IMarshal %v", hrA, hrM)
}

func (h *host) fitBounds() {
	if h.ctl == nil {
		return
	}
	var r rect
	pGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&r)))
	// RECT is 16 bytes: the x64 ABI passes it by reference to a copy.
	h.ctl.call(ctrlPutBounds, uintptr(unsafe.Pointer(&r)))
}

func (h *host) fail(err error) {
	log.Printf("error: %v", err)
	if h.err == nil {
		h.err = err
	}
	h.beginClose("failure")
}

// beginClose is the closing state machine's entry. In order: stop new
// work and cancel handlers; settle every pending request on this thread;
// remove handlers, close the controller and release what we own; then
// destroy the window, unless an environment callback is still due, in
// which case the window hides and waits for it (bounded by a timer). It
// never waits for a worker: workers never need this thread after Close.
func (h *host) beginClose(why string) {
	if h.state >= closing {
		return
	}
	prev := h.state
	h.state = closing
	h.closeAsked = time.Now()
	outstanding := 0
	if h.disp != nil {
		outstanding = h.disp.Outstanding()
	}
	log.Printf("close: %s in state %s, %v after launch (handlers running %d, deferrals pending %d, env pending %v, controller pending %v)",
		why, prev, time.Since(h.launched).Round(time.Millisecond), outstanding, len(h.pending), h.envPending, h.ctlPending)

	h.cancel() // the window's context: in-process and loopback handlers
	if h.disp != nil {
		if running := h.disp.Close(); len(running) > 0 {
			log.Printf("close: canceled handlers for %v; their results will be dropped", sortIDs(running))
		}
	}
	ids := make([]uint64, 0, len(h.pending))
	for id := range h.pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		p := h.pending[id]
		delete(h.pending, id)
		h.counts.settledOnClose++
		h.settle(id, p, synthetic(http.StatusServiceUnavailable, "window closing"), "settled on close")
	}
	h.teardownWebView()
	h.setTitle()

	if h.envPending {
		pShowWindow.Call(h.hwnd, swHide)
		pSetTimer.Call(h.hwnd, timerEnvWait, 5000, 0)
		log.Printf("close: environment creation still pending; window hidden, waiting up to 5 s for its callback")
		return
	}
	h.finishClose()
}

func sortIDs(ids []uint64) []uint64 {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (h *host) teardownWebView() {
	if h.wv != nil {
		for i := len(h.tokens) - 1; i >= 0; i-- {
			t := h.tokens[i]
			// EventRegistrationToken is 8 bytes: passed by value.
			if hr := h.wv.call(t.remove, uintptr(t.value)); hr.failed() {
				log.Printf("close: remove_%s: %v", t.name, hr)
			}
		}
		h.tokens = nil
		if h.disp != nil {
			h.wv.call(wvRemoveWebResourceRequestedFilter, uintptr(unsafe.Pointer(utf16(h.origin.String()+"/*"))), 0)
		}
		h.wv.release()
		h.wv = nil
	}
	if h.ctl != nil {
		t := time.Now()
		hr := h.ctl.call(ctrlClose)
		h.ctl.release()
		h.ctl = nil
		log.Printf("close: controller closed (%v) in %v", hr, time.Since(t).Round(time.Microsecond))
	}
	if h.env != nil {
		h.env.release()
		h.env = nil
	}
}

// finishClose ends posting to the window, then destroys it. A controller
// creation still running completes with E_ABORT inside DestroyWindow (per
// WebView2.idl), and onController sees closing.
func (h *host) finishClose() {
	if h.state == closed {
		return
	}
	h.state = closed
	pKillTimer.Call(h.hwnd, timerEnvWait)
	h.poster.close()
	if !h.destroyed {
		pDestroyWindow.Call(h.hwnd)
	}
}

func (h *host) loop() error {
	var m winMsg
	for {
		r, _, err := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			return fmt.Errorf("GetMessageW: %w", err)
		}
		if r == 0 {
			return nil
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// afterLoop runs on the UI thread once its loop has ended. Nothing here
// needs that loop: workers stopped posting at Close.
func (h *host) afterLoop() {
	closeDur := time.Duration(0)
	if !h.closeAsked.IsZero() {
		closeDur = time.Since(h.closeAsked)
	}
	log.Printf("close: message loop ended %v after close began", closeDur.Round(time.Millisecond))
	h.stopTransport()
	if h.disp != nil {
		t := time.Now()
		drained := h.disp.Wait(5 * time.Second)
		s := h.disp.Stats()
		log.Printf("close: workers drained %v in %v; started %d, finished %d, canceled %d, dropped after close %d, refused %d",
			drained, time.Since(t).Round(time.Millisecond), s.Started, s.Finished, s.Canceled, s.Dropped, s.Refused)
		log.Printf("close: intercepted %d, responses installed %d (settled on close %d), origin refusals %d",
			h.counts.intercepted, h.counts.installed, h.counts.settledOnClose, h.counts.refusedOrigin)
	}
	if h.poster.failed > 0 {
		log.Printf("close: %d wakeup posts failed", h.poster.failed)
	}
	live := liveCallbacks()
	log.Printf("exit: %d WebView2 callbacks, %d off the UI thread", h.counts.invoked, h.counts.offUI)
	log.Printf("exit: late creation callbacks %d; callback objects still referenced %d %v; %v after close began, %v after launch",
		h.counts.late, len(live), live, closeDur.Round(time.Millisecond), time.Since(h.launched).Round(time.Millisecond))
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	var h *host
	if msg == wmNCCreate {
		var id uintptr
		pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&id)), lp, unsafe.Sizeof(id))
		pSetWindowLongPtrW.Call(hwnd, gwlpUserData, id)
		h = hosts[id]
	} else {
		id, _, _ := pGetWindowLongPtrW.Call(hwnd, gwlpUserData)
		h = hosts[id]
	}
	if h != nil {
		if r, ok := h.handle(msg, wp, lp); ok {
			return r
		}
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (h *host) handle(msg, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case wmSize:
		if h.ctl != nil {
			if wp == sizeMinimized {
				h.ctl.call(ctrlPutIsVisible, 0)
			} else {
				h.ctl.call(ctrlPutIsVisible, 1)
				h.fitBounds()
			}
		}
		return 0, true
	case wmMove, wmMoving:
		if h.ctl != nil {
			h.ctl.call(ctrlNotifyParentWindowPositionChanged)
		}
		return 0, false
	case wmSetFocus:
		if h.ctl != nil {
			h.ctl.call(ctrlMoveFocus, 0)
		}
		return 0, true
	case wmDPIChanged:
		var r rect
		pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&r)), lp, unsafe.Sizeof(r))
		pSetWindowPos.Call(h.hwnd, 0, uintptr(r.left), uintptr(r.top), uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpNoZOrder|swpNoActivate)
		log.Printf("window: DPI changed to %d (scale %g), suggested %dx%d", wp&0xffff, float32(wp&0xffff)/96, r.right-r.left, r.bottom-r.top)
		return 0, true
	case wmDispatch:
		if h.disp != nil && h.state == running {
			h.deliver()
		}
		return 0, true
	case wmServerFailed:
		h.serverMu.Lock()
		err := h.serverErr
		h.serverMu.Unlock()
		h.fail(fmt.Errorf("loopback server stopped: %w", err))
		return 0, true
	case wmTimer:
		if wp == timerEnvWait && h.state == closing {
			log.Printf("close: no environment callback within 5 s; destroying the window (its callback object stays referenced)")
			h.finishClose()
		}
		return 0, true
	case wmClose:
		h.beginClose("WM_CLOSE")
		return 0, true
	case wmDestroy:
		h.destroyed = true
		if h.state < closing {
			h.beginClose("WM_DESTROY")
		}
		pPostQuitMessage.Call(0)
		return 0, true
	}
	return 0, false
}

// watchConsole turns console control events into an ordinary close. The
// host is a console program, so Ctrl+C, Ctrl+Break or closing its console
// otherwise ends the process at once (exit 0xC000013A), with no close
// path: no pending request settled, no controller closed. That happened
// in the first manual audition. Go reports Ctrl+C/Break as os.Interrupt
// and console close/logoff/shutdown as SIGTERM.
func (h *host) watchConsole() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		for s := range sig {
			if h.poster.post(wmClose) {
				log.Printf("close: console signal %v: posted WM_CLOSE", s)
			} else {
				log.Printf("close: console signal %v after the window closed: ignored", s)
			}
		}
	}()
}
