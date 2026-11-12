//go:build windows && amd64

package main

// Passes B-D of the integration feasibility spike on Windows: a multi-window
// host whose windows each have their own WebView2 environment over their own
// temporary user-data folder. It is driven by console commands from the
// driver in evidence/windows/integration-feasibility/drive and reports JSON
// events on stdout. The ordinary single-window host is untouched; nothing
// here runs unless -feasibility is given.
//
// Owned loopback windows get a random endpoint credential installed through
// the native cookie manager before their first navigation, and their endpoint
// admits only requests carrying it. Same-origin enforcement is native
// interception of every request (a WebResourceRequested filter on "*"):
// requests outside the window's origin are answered by the host and never
// reach the network. The endpoint's CSP header is a second, independently
// switchable mechanism. A failed top-level load shows a host-local error page
// whose actions are intercepted natively.
//
// The host evaluates no JavaScript: automation is page-driven (?run=...) and
// results arrive through each window's own handler, as on Linux.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"dfx2webview2spike/adapt"
	"dfx2webview2spike/feasibility"
)

const (
	feasClass = "dfx2Feasibility"
	// errorOrigin is where the error page's actions point. The host cancels
	// every navigation to it in NavigationStarting and acts on what it
	// recorded; .invalid can never resolve if one ever escaped.
	errorOrigin = "https://dfx2-error.invalid"

	wmFeasPosted  = wmApp + 3 // run the posted functions (commands, results)
	timerRelease  = 2         // a closing window still waiting for its browser process
	timerNoBrowse = 3         // a closing window that never got a controller

	webErrorOperationCanceled = 14 // COREWEBVIEW2_WEB_ERROR_STATUS_OPERATION_CANCELED
	resourceContextDocument   = 1  // COREWEBVIEW2_WEB_RESOURCE_CONTEXT_DOCUMENT
	cookieSameSiteStrict      = 2  // COREWEBVIEW2_COOKIE_SAME_SITE_KIND_STRICT
	capturePNG                = 0  // COREWEBVIEW2_CAPTURE_PREVIEW_IMAGE_FORMAT_PNG
	hwndMessage               = ^uintptr(2)

	removeAttempts = 20
	removeInterval = 100 * time.Millisecond
)

var (
	pClientToScreen      = user32.NewProc("ClientToScreen")
	pSetCursorPos        = user32.NewProc("SetCursorPos")
	pSendInput           = user32.NewProc("SendInput")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

// mouseInput is an INPUT holding a MOUSEINPUT on Windows x64: 40 bytes.
type mouseInput struct {
	kind, _                uint32
	dx, dy                 int32
	data, flags, timestamp uint32
	extra                  uintptr
}

type fwin struct {
	id          int
	label, mode string
	allowed     []*url.URL
	initial     string
	hwnd        uintptr
	profile     string // this window's user-data folder, under the experiment root

	env, env5, ctl, wv *com
	tokens             []eventToken
	exitToken          int64
	envPending         bool
	ctlPending         bool
	browserPID         uint32
	browserStarted     bool // a controller was delivered, so a browser process serves this profile
	browserExited      bool
	exitKind           int32
	profilePath        string             // what the Runtime reports for its profile
	owed               map[uintptr]string // completions still owed to this window, by handler object (GetCookies, CapturePreview)
	dropped            []string           // handlers WebView2 released without invoking them
	releasing          bool               // teardown after release has been posted

	token, host string
	page        *feasibility.Page
	counters    *feasibility.Counters
	listener    net.Listener
	server      *http.Server
	served      chan error
	dispatcher  *adapt.Dispatcher
	ctx         context.Context
	cancel      context.CancelFunc
	pending     map[uint64]*pending

	closing, destroyed, navigated bool
	cookieReady                   bool
	closeStage                    string
	opened, closeAsked            time.Time
	controllerClosed              time.Time

	navigations                  int
	navMethod, navURI            string // the current top-level navigation
	navFromPost                  bool
	navResponded                 bool // a response arrived for the current top-level document request
	navHTTP                      int32
	wv2                          *com // ICoreWebView2_2, for the response-received registration
	respToken                    int64
	docRequests                  int
	errorPending                 bool // the error page's own navigation is in flight
	errNonce, errURI, errMethod  string
	errFromPost                  bool
	blocked                      map[string]int // requests answered natively, by resource context
	refused                      struct{ navigation, subframe, popup int }
	cookieRefusedAtFirstDocument int
}

type feas struct {
	c       config
	windows map[int]*fwin
	byLabel map[string]*fwin
	nextID  int
	ctrl    uintptr // the message-only controller window
	thread  uint32
	loader  *syscall.DLL
	runtime string
	quit    bool

	mu     sync.Mutex
	posted []func()
	closed bool
	out    sync.Mutex

	counts struct{ invoked, offUI, late int }
}

var (
	feasActive  *feas
	feasWndProc uintptr
)

func (f *feas) event(name string, kv ...any) {
	m := map[string]any{"event": name, "t": time.Now().Format("15:04:05.000")}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	f.out.Lock()
	fmt.Println(string(b))
	f.out.Unlock()
}

// post runs fn on the UI thread, from any goroutine. After the loop has
// ended nothing is posted.
func (f *feas) post(fn func()) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.posted = append(f.posted, fn)
	if len(f.posted) == 1 {
		ok, _, _ := pPostMessageW.Call(f.ctrl, wmFeasPosted, 0, 0)
		if ok == 0 {
			f.posted = f.posted[:0]
			return false
		}
	}
	return true
}

func (f *feas) drain() {
	f.mu.Lock()
	fns := f.posted
	f.posted = nil
	f.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
	for _, w := range f.windows {
		if w.dispatcher == nil || w.closing {
			continue
		}
		for _, r := range w.dispatcher.Take() {
			if p := w.pending[r.ID]; p != nil {
				delete(w.pending, r.ID)
				f.settle(w, r.ID, p, r.Response, "installed")
			}
		}
	}
}

func (f *feas) callback(name string, iid guid, fn func(a, b uintptr) hresult) *callback {
	return newCallback(name, iid, func(a, b uintptr) hresult {
		f.counts.invoked++
		if t := currentThread(); t != f.thread {
			f.counts.offUI++
			f.event("off-ui-thread", "callback", name, "thread", t)
		}
		return fn(a, b)
	})
}

func runFeasibility(c config) error {
	for _, p := range allProcs {
		if err := p.Find(); err != nil {
			return fmt.Errorf("win32: %w", err)
		}
	}
	for _, p := range []*syscall.LazyProc{pClientToScreen, pSetCursorPos, pSendInput, pSetForegroundWindow} {
		if err := p.Find(); err != nil {
			return fmt.Errorf("win32: %w", err)
		}
	}
	f := &feas{c: c, windows: map[int]*fwin{}, byLabel: map[string]*fwin{}}
	feasActive = f
	f.thread = currentThread()
	if hr := hresult(callProc(pCoInitializeEx, 0, coinitApartmentThreaded|coinitDisableOLE1DDE)); hr.failed() {
		return hr.err("CoInitializeEx(STA)")
	}
	defer pCoUninitialize.Call()
	if err := perMonitorDPI(); err != nil {
		return err
	}
	// Prerequisites, as the single-window host checks them.
	h := &host{cfg: c}
	if err := h.loadLoader(); err != nil {
		return err
	}
	f.loader = h.loader
	if err := h.checkRuntime(); err != nil {
		return err
	}
	f.runtime = h.runtimeVersion
	if c.feas.root == "" {
		c.feas.root = filepath.Join(os.TempDir(), "dfx2-feasibility")
		f.c.feas.root = c.feas.root
	}
	root, err := filepath.Abs(c.feas.root)
	if err != nil {
		return err
	}
	f.c.feas.root = root
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := f.createController(); err != nil {
		return err
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	go func() { <-interrupts; f.post(func() { f.command("quit") }) }()
	go func() {
		s := bufio.NewScanner(os.Stdin)
		s.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for s.Scan() {
			line := s.Text()
			f.post(func() { f.command(line) })
		}
		// Console gone: quit, so a dead driver never leaves windows behind.
		f.post(func() { f.command("quit") })
	}()
	f.event("ready", "pid", os.Getpid(), "runtime", f.runtime, "loader_sha256", h.loaderHash, "loader_pinned", h.loaderHash == loaderSHA256,
		"root", root, "filter", c.feas.filter, "csp", c.feas.csp, "cookie", c.feas.cookie, "cookie_fail_injected", c.feas.cookieFail,
		"remove_fail_injected", c.feas.removeFail, "close_during", c.feas.closeDuring, "ui_thread", f.thread)

	var m winMsg
	for {
		r, _, err := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			return fmt.Errorf("GetMessageW: %w", err)
		}
		if r == 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	pDestroyWindow.Call(f.ctrl)
	live := liveCallbacks()
	f.event("exit", "callbacks", f.counts.invoked, "off_ui_thread", f.counts.offUI, "late_creation_callbacks", f.counts.late,
		"callback_objects_live", len(live), "live", live)
	return nil
}

func (f *feas) createController() error {
	inst, _, err := pGetModuleHandleW.Call(0)
	if inst == 0 {
		return fmt.Errorf("GetModuleHandleW: %w", err)
	}
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	className := utf16(feasClass)
	if feasWndProc == 0 {
		feasWndProc = syscall.NewCallback(feasProc)
	}
	wc := wndClassEx{wndProc: feasWndProc, instance: inst, cursor: cursor, background: colorWindow + 1, className: className}
	wc.size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassExW: %w", err)
	}
	// id 0 is the controller: a message-only window that owns the posted
	// queue, so commands and results have a target before any window exists.
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16("dfx2 feasibility controller"))),
		0, 0, 0, 0, 0, hwndMessage, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW(controller): %w", err)
	}
	f.ctrl = hwnd
	return nil
}

func feasProc(hwnd, msg, wp, lp uintptr) uintptr {
	f := feasActive
	var id uintptr
	if msg == wmNCCreate {
		pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&id)), lp, unsafe.Sizeof(id))
		pSetWindowLongPtrW.Call(hwnd, gwlpUserData, id)
	} else {
		id, _, _ = pGetWindowLongPtrW.Call(hwnd, gwlpUserData)
	}
	if f != nil {
		if id == 0 && hwnd == f.ctrl {
			if msg == wmFeasPosted {
				f.drain()
				return 0
			}
		} else if w := f.windows[int(id)]; w != nil {
			if r, ok := f.handle(w, msg, wp, lp); ok {
				return r
			}
		}
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (f *feas) handle(w *fwin, msg, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case wmSize:
		if w.ctl != nil {
			if wp == sizeMinimized {
				w.ctl.call(ctrlPutIsVisible, 0)
			} else {
				w.ctl.call(ctrlPutIsVisible, 1)
				f.fitBounds(w)
			}
		}
		return 0, true
	case wmMove, wmMoving:
		if w.ctl != nil {
			w.ctl.call(ctrlNotifyParentWindowPositionChanged)
		}
		return 0, false
	case wmSetFocus:
		if w.ctl != nil {
			w.ctl.call(ctrlMoveFocus, 0)
		}
		return 0, true
	case wmTimer:
		switch wp {
		case timerRelease:
			if w.closing && !w.destroyed {
				f.maybeRelease(w)
				f.event("release-pending", "window", w.label, "browser_pid", w.browserPID, "waited", time.Since(w.closeAsked).Round(time.Millisecond).String(),
					"native_async", f.outstanding(w), "env_pending", w.envPending, "controller_pending", w.ctlPending)
			}
		case timerNoBrowse:
			pKillTimer.Call(w.hwnd, timerNoBrowse)
			if w.closing && !w.destroyed && !w.browserStarted && !w.releasing {
				f.event("release-assumed", "window", w.label, "reason", "no controller was ever created; no browser process exit to wait for")
				w.releasing = true
				f.post(func() { f.afterRelease(w) })
			}
		}
		return 0, true
	case wmClose:
		f.close(w, "window close")
		return 0, true
	case wmDestroy:
		w.destroyed = true
		return 0, true
	}
	return 0, false
}

func (f *feas) command(line string) {
	fields := strings.SplitN(strings.TrimSpace(line), " ", 3)
	arg := func(i int) string {
		if i < len(fields) {
			return fields[i]
		}
		return ""
	}
	w := f.byLabel[arg(1)]
	switch arg(0) {
	case "open": // open LABEL MODE [URL]
		rest := strings.Fields(arg(2))
		if len(rest) == 0 {
			f.event("error", "command", line, "error", "open LABEL MODE [URL]")
			return
		}
		u := ""
		if len(rest) > 1 {
			u = rest[1]
		}
		f.open(arg(1), rest[0], u)
	case "load":
		if w != nil && w.wv != nil {
			w.wv.call(wvNavigate, uintptr(unsafe.Pointer(utf16(arg(2)))))
		}
	case "reload":
		if w != nil && w.wv != nil {
			w.wv.call(wvReload)
		}
	case "click": // click LABEL X Y, CSS pixels from the webview's top left
		var x, y float64
		if _, err := fmt.Sscan(arg(2), &x, &y); w != nil && err == nil {
			f.click(w, x, y)
		}
	case "close":
		if w != nil {
			f.close(w, "command")
		}
	case "snapshot":
		if w != nil {
			f.snapshot(w, arg(2))
		}
	case "inspect": // open devtools (a separate window)
		if w != nil && w.wv != nil {
			var settings *com
			if w.wv.call(wvGetSettings, uintptr(unsafe.Pointer(&settings))) == sOK && settings != nil {
				settings.call(setPutAreDevToolsEnabled, 1)
				settings.release()
			}
			hr := w.wv.call(wvOpenDevToolsWindow)
			f.event("inspector", "window", w.label, "opened", !hr.failed(), "hresult", hr.String())
		}
	case "selftest":
		if w != nil {
			f.selftest(w)
		}
	case "status":
		f.status()
	case "quit":
		f.quit = true
		for _, w := range f.windows {
			f.close(w, "quit")
		}
		f.maybeQuit()
	case "":
	default:
		f.event("error", "command", line, "error", "unknown command")
	}
}

func (f *feas) open(label, mode, attachURL string) {
	if f.byLabel[label] != nil && !f.byLabel[label].destroyed {
		f.event("error", "command", "open", "error", "label in use")
		return
	}
	f.nextID++
	w := &fwin{id: f.nextID, label: label, mode: mode, page: feasibility.NewPage(), counters: &feasibility.Counters{},
		pending: map[uint64]*pending{}, blocked: map[string]int{}, owed: map[uintptr]string{}, opened: time.Now()}
	w.ctx, w.cancel = context.WithCancel(context.Background())
	var origin *url.URL
	switch mode {
	case "loopback":
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			f.event("error", "window", label, "error", err.Error())
			return
		}
		w.listener, w.host = ln, ln.Addr().String()
		origin, _ = url.Parse("http://" + w.host)
		var handler http.Handler = feasibility.Count(w.page, w.counters)
		if f.c.feas.cookie {
			tok, err := feasibility.NewToken()
			if err != nil {
				f.event("error", "window", label, "error", err.Error())
				return
			}
			w.token = tok
			csp := ""
			if f.c.feas.csp {
				csp = feasibility.OwnedCSP
			}
			handler = feasibility.Guard(handler, tok, w.host, origin.String(), csp, w.counters)
		}
		w.server = &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return w.ctx }, ReadHeaderTimeout: 5 * time.Second}
		w.served = make(chan error, 1)
		go func() { w.served <- w.server.Serve(ln) }()
	case "inprocess":
		origin, _ = url.Parse(inprocessOrigin)
		w.dispatcher = adapt.NewDispatcher(w.ctx, feasibility.Count(w.page, w.counters), origin, func() bool { return f.post(func() {}) }, func(string, ...any) {})
	case "attach":
		u, err := url.Parse(attachURL)
		if err != nil || u.Host == "" {
			f.event("error", "window", label, "error", "attach needs an absolute URL")
			return
		}
		origin = &url.URL{Scheme: u.Scheme, Host: u.Host}
		w.initial = attachURL
	default:
		f.event("error", "window", label, "error", "mode is loopback, inprocess or attach")
		return
	}
	w.allowed = []*url.URL{origin}
	if w.initial == "" {
		w.initial = origin.String() + "/"
	}
	w.page.OnReport = func(r json.RawMessage) {
		f.post(func() { f.event("report", "window", label, "report", r) })
	}
	w.profile = filepath.Join(f.c.feas.root, fmt.Sprintf("profile-%d-%d", os.Getpid(), w.id))
	if err := os.MkdirAll(w.profile, 0o700); err != nil {
		f.event("error", "window", label, "error", err.Error())
		return
	}
	f.windows[w.id], f.byLabel[label] = w, w
	if err := f.createWindow(w); err != nil {
		f.event("error", "window", label, "error", err.Error())
		return
	}
	w.cookieReady = w.token == ""
	f.event("opened", "window", label, "mode", mode, "origin", origin.String(), "profile", w.profile, "credential", w.token != "",
		"hwnd", fmt.Sprintf("%#x", w.hwnd))
	if err := f.createEnvironment(w); err != nil {
		f.event("error", "window", label, "error", err.Error())
		f.close(w, "environment creation failed")
	}
}

func (f *feas) createWindow(w *fwin) error {
	inst, _, _ := pGetModuleHandleW.Call(0)
	x, y := 40+((w.id-1)%3)*800, 60+((w.id-1)/3%2)*620
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16(feasClass))), uintptr(unsafe.Pointer(utf16(f.title(w)))),
		wsOverlappedWindow, uintptr(x), uintptr(y), 760, 560, 0, 0, inst, uintptr(w.id))
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %w", err)
	}
	w.hwnd = hwnd
	d, _, _ := pGetDpiForWindow.Call(hwnd)
	dpi := uint32(d)
	r := rect{right: int32(muldiv(760, dpi)), bottom: int32(muldiv(560, dpi))}
	pAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), wsOverlappedWindow, 0, 0, uintptr(dpi))
	pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpNoMove|swpNoZOrder|swpNoActivate)
	pShowWindow.Call(hwnd, swShow)
	return nil
}

func (f *feas) title(w *fwin) string { return "dfx2 feasibility " + w.label + " — " + w.mode }

func (f *feas) createEnvironment(w *fwin) error {
	create, err := f.loader.FindProc("CreateCoreWebView2EnvironmentWithOptions")
	if err != nil {
		return err
	}
	cb := f.callback("EnvironmentCompleted", iidEnvironmentCompleted, func(a, b uintptr) hresult {
		f.onEnvironment(w, hresult(uint32(a)), asCOM(b))
		return sOK
	})
	w.envPending = true
	if f.c.feas.closeDuring == "env" {
		f.c.feas.closeDuring = ""
		f.event("test", "window", w.label, "hook", "close posted before environment creation")
		pPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	}
	var rt uintptr
	if f.c.runtimeDir != "" {
		rt = uintptr(unsafe.Pointer(utf16(f.c.runtimeDir)))
	}
	hr := hresult(callProc(create, rt, uintptr(unsafe.Pointer(utf16(w.profile))), 0, cb.ptr()))
	cb.release()
	if hr.failed() {
		w.envPending = false
		return hr.err("CreateCoreWebView2EnvironmentWithOptions")
	}
	return nil
}

func (f *feas) onEnvironment(w *fwin, hr hresult, env *com) {
	w.envPending = false
	if w.closing {
		f.counts.late++
		f.event("late-callback", "window", w.label, "callback", "environment", "hresult", hr.String(), "attached", false)
		f.maybeRelease(w)
		return
	}
	if hr.failed() || env == nil {
		f.event("error", "window", w.label, "error", "environment creation failed: "+hr.String())
		f.close(w, "environment creation failed")
		return
	}
	env.addRef()
	w.env = env
	// Browser-process exit is the release evidence for the profile folder.
	if env5, hr := env.queryInterface(&iidIEnvironment5); !hr.failed() && env5 != nil {
		w.env5 = env5
		cb := f.callback("BrowserProcessExited", iidBrowserProcessExited, func(a, b uintptr) hresult {
			f.onBrowserProcessExited(w, asCOM(b))
			return sOK
		})
		if hr := env5.call(env5AddBrowserProcessExited, cb.ptr(), uintptr(unsafe.Pointer(&w.exitToken))); hr.failed() {
			f.event("error", "window", w.label, "error", "add_BrowserProcessExited: "+hr.String())
		}
		cb.release()
	} else {
		f.event("error", "window", w.label, "error", "ICoreWebView2Environment5 unavailable: "+hr.String())
	}
	cb := f.callback("ControllerCompleted", iidControllerCompleted, func(a, b uintptr) hresult {
		f.onController(w, hresult(uint32(a)), asCOM(b))
		return sOK
	})
	w.ctlPending = true
	if f.c.feas.closeDuring == "controller" {
		f.c.feas.closeDuring = ""
		f.event("test", "window", w.label, "hook", "close posted before controller creation")
		pPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	}
	hr = env.call(envCreateController, w.hwnd, cb.ptr())
	cb.release()
	if hr.failed() {
		w.ctlPending = false
		f.event("error", "window", w.label, "error", "CreateCoreWebView2Controller: "+hr.String())
		f.close(w, "controller creation failed")
	}
}

func (f *feas) onController(w *fwin, hr hresult, ctl *com) {
	w.ctlPending = false
	if w.closing {
		f.counts.late++
		if ctl != nil {
			w.browserStarted = true
			ctl.call(ctrlClose)
		}
		f.event("late-callback", "window", w.label, "callback", "controller", "hresult", hr.String(), "controller", ctl != nil, "attached", false)
		f.maybeRelease(w)
		return
	}
	if hr.failed() || ctl == nil {
		f.event("error", "window", w.label, "error", "controller creation failed: "+hr.String())
		f.close(w, "controller creation failed")
		return
	}
	ctl.addRef()
	w.ctl = ctl
	w.browserStarted = true
	var wv *com
	if hr := ctl.call(ctrlGetCoreWebView2, uintptr(unsafe.Pointer(&wv))); hr.failed() {
		f.event("error", "window", w.label, "error", "get_CoreWebView2: "+hr.String())
		f.close(w, "get_CoreWebView2 failed")
		return
	}
	w.wv = wv
	wv.call(wvGetBrowserProcessID, uintptr(unsafe.Pointer(&w.browserPID)))
	var settings *com
	if hr := wv.call(wvGetSettings, uintptr(unsafe.Pointer(&settings))); !hr.failed() && settings != nil {
		settings.call(setPutAreDevToolsEnabled, 0)
		settings.call(setPutIsWebMessageEnabled, 0)
		settings.call(setPutAreHostObjectsAllowed, 0)
		settings.call(setPutIsBuiltInErrorPageEnabled, 0) // the host shows its own
		settings.release()
	}
	if wv13, hr := wv.queryInterface(&iidICoreWebView2_13); !hr.failed() && wv13 != nil {
		var profile *com
		if wv13.call(wv13GetProfile, uintptr(unsafe.Pointer(&profile))) == sOK && profile != nil {
			var p *uint16
			profile.call(profileGetProfilePath, uintptr(unsafe.Pointer(&p)))
			w.profilePath = takeString(p)
			profile.release()
		}
		wv13.release()
	}
	if err := f.addHandlers(w); err != nil {
		f.event("error", "window", w.label, "error", err.Error())
		f.close(w, "handler registration failed")
		return
	}
	f.fitBounds(w)
	ctl.call(ctrlPutIsVisible, 1)
	f.event("controller", "window", w.label, "browser_pid", w.browserPID, "profile_path", w.profilePath,
		"since_open", time.Since(w.opened).Round(time.Millisecond).String())
	if w.token != "" {
		f.installCookie(w)
	}
	f.maybeNavigate(w)
}

func (f *feas) addHandlers(w *fwin) error {
	type reg struct {
		name        string
		iid         guid
		add, remove int
		fn          func(a, b uintptr) hresult
	}
	regs := []reg{
		{"NavigationStarting", iidNavigationStarting, wvAddNavigationStarting, wvRemoveNavigationStarting, func(a, b uintptr) hresult { return f.onNavigationStarting(w, asCOM(b)) }},
		{"NavigationCompleted", iidNavigationCompleted, wvAddNavigationCompleted, wvRemoveNavigationCompleted, func(a, b uintptr) hresult { return f.onNavigationCompleted(w, asCOM(b)) }},
		{"FrameNavigationStarting", iidFrameNavigationStarting, wvAddFrameNavigationStarting, wvRemoveFrameNavigationStarting, func(a, b uintptr) hresult { return f.onFrameNavigationStarting(w, asCOM(b)) }},
		{"NewWindowRequested", iidNewWindowRequested, wvAddNewWindowRequested, wvRemoveNewWindowRequested, func(a, b uintptr) hresult { return f.onNewWindowRequested(w, asCOM(b)) }},
		{"ProcessFailed", iidProcessFailed, wvAddProcessFailed, wvRemoveProcessFailed, func(a, b uintptr) hresult {
			var kind int32
			asCOM(b).call(pfGetProcessFailedKind, uintptr(unsafe.Pointer(&kind)))
			f.event("process-failed", "window", w.label, "kind", kind)
			return sOK
		}},
	}
	filter := ""
	switch {
	case f.c.feas.filter:
		filter = "*" // every request, every context: the host decides
	case w.mode == "inprocess":
		filter = w.allowed[0].String() + "/*" // the transport itself, no enforcement
	}
	if filter != "" {
		regs = append(regs, reg{"WebResourceRequested", iidWebResourceRequested, wvAddWebResourceRequested, wvRemoveWebResourceRequested,
			func(a, b uintptr) hresult { return f.onWebResourceRequested(w, asCOM(b)) }})
	}
	for _, r := range regs {
		cb := f.callback(r.name, r.iid, r.fn)
		var tok int64
		hr := w.wv.call(r.add, cb.ptr(), uintptr(unsafe.Pointer(&tok)))
		cb.release()
		if hr.failed() {
			return hr.err("add_" + r.name)
		}
		w.tokens = append(w.tokens, eventToken{r.name, r.remove, tok})
	}
	// Responses, to tell a server that answered (an ordinary page, whatever
	// its status) from a request that failed.
	wv2, hr := w.wv.queryInterface(&iidICoreWebView2_2)
	if hr.failed() || wv2 == nil {
		return hr.err("QueryInterface(ICoreWebView2_2)")
	}
	w.wv2 = wv2
	cb := f.callback("WebResourceResponseReceived", iidResponseReceived, func(a, b uintptr) hresult { return f.onResponseReceived(w, asCOM(b)) })
	hr = wv2.call(wv2AddWebResourceResponseReceived, cb.ptr(), uintptr(unsafe.Pointer(&w.respToken)))
	cb.release()
	if hr.failed() {
		return hr.err("add_WebResourceResponseReceived")
	}
	if filter != "" {
		if hr := w.wv.call(wvAddWebResourceRequestedFilter, uintptr(unsafe.Pointer(utf16(filter))), 0); hr.failed() {
			return hr.err("AddWebResourceRequestedFilter")
		}
		f.event("filter-installed", "window", w.label, "filter", filter, "contexts", "all", "enforces", f.c.feas.filter)
	}
	return nil
}

// installCookie creates the credential cookie and adds it through the
// native cookie manager. AddOrUpdateCookie is synchronous and has no
// completion; GetCookies afterwards shows what the manager holds before the
// first navigation. Whether the first request carries it is then the
// endpoint's own evidence: its no-credential refusals.
func (f *feas) installCookie(w *fwin) {
	wv2, hr := w.wv.queryInterface(&iidICoreWebView2_2)
	if hr.failed() || wv2 == nil {
		f.cookieFailed(w, "ICoreWebView2_2 unavailable: "+hr.String())
		return
	}
	defer wv2.release()
	var cm *com
	if hr := wv2.call(wv2GetCookieManager, uintptr(unsafe.Pointer(&cm))); hr.failed() || cm == nil {
		f.cookieFailed(w, "get_CookieManager: "+hr.String())
		return
	}
	defer cm.release()
	var cookie *com
	hr = cm.call(cmCreateCookie, uintptr(unsafe.Pointer(utf16(feasibility.CookieName))), uintptr(unsafe.Pointer(utf16(w.token))),
		uintptr(unsafe.Pointer(utf16("127.0.0.1"))), uintptr(unsafe.Pointer(utf16("/"))), uintptr(unsafe.Pointer(&cookie)))
	if hr.failed() || cookie == nil {
		f.cookieFailed(w, "CreateCookie: "+hr.String())
		return
	}
	cookie.call(cookiePutIsHttpOnly, 1)
	cookie.call(cookiePutSameSite, cookieSameSiteStrict)
	var session int32
	cookie.call(cookieGetIsSession, uintptr(unsafe.Pointer(&session)))
	hr = cm.call(cmAddOrUpdateCookie, cookie.ptr())
	cookie.release()
	if f.c.feas.cookieFail {
		hr = eFail
	}
	if hr.failed() {
		if f.c.feas.cookieFail {
			f.cookieFailed(w, "injected: cookie installation failure (AddOrUpdateCookie result replaced)")
		} else {
			f.cookieFailed(w, "AddOrUpdateCookie: "+hr.String())
		}
		return
	}
	f.event("cookie-added", "window", w.label, "http_only", true, "same_site", "strict", "session", session != 0, "synchronous", true)
	if f.c.feas.closeDuring == "cookie" {
		f.c.feas.closeDuring = ""
		f.event("test", "window", w.label, "hook", "close posted after AddOrUpdateCookie, before GetCookies completes")
		pPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	}
	var cb *callback
	cb = f.callback("GetCookiesCompleted", iidGetCookiesCompleted, func(a, b uintptr) hresult {
		delete(w.owed, cb.id)
		f.onCookies(w, hresult(uint32(a)), asCOM(b))
		return sOK
	})
	w.owed[cb.id] = cb.name
	hr = cm.call(cmGetCookies, uintptr(unsafe.Pointer(utf16(w.allowed[0].String()+"/"))), cb.ptr())
	cb.release()
	if hr.failed() {
		delete(w.owed, cb.id)
		f.cookieFailed(w, "GetCookies: "+hr.String())
	}
}

func (f *feas) onCookies(w *fwin, hr hresult, list *com) {
	found, total := 0, uint32(0)
	sameSite, httpOnly := int32(-1), int32(-1)
	if !hr.failed() && list != nil {
		list.call(cookieListGetCount, uintptr(unsafe.Pointer(&total)))
		for i := uint32(0); i < total; i++ {
			var c *com
			if list.call(cookieListGetValueAtIndex, uintptr(i), uintptr(unsafe.Pointer(&c))) == sOK && c != nil {
				var n *uint16
				c.call(cookieGetName, uintptr(unsafe.Pointer(&n)))
				if takeString(n) == feasibility.CookieName {
					found++
					var v int32
					c.call(cookieGetSameSite, uintptr(unsafe.Pointer(&sameSite)))
					c.call(cookieGetIsHttpOnly, uintptr(unsafe.Pointer(&v)))
					httpOnly = v
				}
				c.release()
			}
		}
	}
	switch {
	case w.closing:
		f.event("cookie-late", "window", w.label, "hresult", hr.String(), "found", found, "navigated", w.navigated)
		f.maybeRelease(w)
	case hr.failed() || found == 0:
		f.cookieFailed(w, fmt.Sprintf("GetCookies after AddOrUpdateCookie: %v, credential cookies found %d of %d", hr, found, total))
	default:
		w.cookieReady = true
		f.event("cookie-installed", "window", w.label, "http_only", httpOnly == 1, "same_site_kind", sameSite, "visible_to_manager", true,
			"since_open", time.Since(w.opened).Round(time.Millisecond).String())
		f.maybeNavigate(w)
	}
}

func (f *feas) cookieFailed(w *fwin, why string) {
	f.event("cookie-failed", "window", w.label, "error", why)
	f.close(w, "cookie installation failed")
}

func (f *feas) maybeNavigate(w *fwin) {
	if w.closing || w.navigated || !w.cookieReady || w.wv == nil {
		return
	}
	w.navigated = true
	f.event("navigate", "window", w.label, "uri", w.initial)
	if hr := w.wv.call(wvNavigate, uintptr(unsafe.Pointer(utf16(w.initial)))); hr.failed() {
		f.event("error", "window", w.label, "error", "Navigate: "+hr.String())
	}
}

func (f *feas) allowed(w *fwin, raw string) bool {
	for _, o := range w.allowed {
		if _, err := adapt.CheckOrigin(raw, o); err == nil {
			return true
		}
	}
	return false
}

func (f *feas) onNavigationStarting(w *fwin, a *com) hresult {
	var u *uint16
	var id uint64
	var redirected int32
	a.call(navStartGetURI, uintptr(unsafe.Pointer(&u)))
	a.call(navStartGetNavigationID, uintptr(unsafe.Pointer(&id)))
	a.call(navStartGetIsRedirected, uintptr(unsafe.Pointer(&redirected)))
	uri := takeString(u)
	switch {
	case strings.HasPrefix(uri, errorOrigin+"/"):
		a.call(navStartPutCancel, 1)
		f.errorAction(w, uri)
		return sOK
	case w.errorPending && (strings.HasPrefix(uri, "data:text/html") || strings.HasPrefix(uri, "about:blank")):
		// The host's own error page: NavigateToString navigates to a data:
		// URI of the markup, so the page has an opaque origin.
		f.event("navigation-allowed", "window", w.label, "id", id, "uri", short(uri), "kind", "error-page")
		return sOK
	case !f.allowed(w, uri):
		a.call(navStartPutCancel, 1)
		w.refused.navigation++
		f.event("navigation-refused", "window", w.label, "kind", "navigation", "id", id, "uri", short(uri), "redirect", redirected != 0)
		return sOK
	}
	if redirected == 0 {
		w.navFromPost, w.navMethod = false, ""
	}
	w.navURI, w.navResponded, w.navHTTP = uri, false, 0
	w.navigations++
	f.event("navigation-allowed", "window", w.label, "id", id, "uri", uri, "redirect", redirected != 0, "method", w.navMethod)
	return sOK
}

func (f *feas) onResponseReceived(w *fwin, a *com) hresult {
	var req, resp *com
	a.call(rrGetRequest, uintptr(unsafe.Pointer(&req)))
	a.call(rrGetResponse, uintptr(unsafe.Pointer(&resp)))
	uri := ""
	var status int32
	if req != nil {
		var u *uint16
		req.call(reqGetURI, uintptr(unsafe.Pointer(&u)))
		uri = takeString(u)
		req.release()
	}
	if resp != nil {
		resp.call(rvGetStatusCode, uintptr(unsafe.Pointer(&status)))
		resp.release()
	}
	if uri == w.navURI && !w.errorPending {
		w.navResponded, w.navHTTP = true, status
		f.event("document-response", "window", w.label, "uri", uri, "http_status", status)
	}
	return sOK
}

func (f *feas) onFrameNavigationStarting(w *fwin, a *com) hresult {
	var u *uint16
	a.call(navStartGetURI, uintptr(unsafe.Pointer(&u)))
	a.call(navStartPutCancel, 1)
	w.refused.subframe++
	f.event("navigation-refused", "window", w.label, "kind", "subframe", "uri", takeString(u))
	return sOK
}

func (f *feas) onNewWindowRequested(w *fwin, a *com) hresult {
	var u *uint16
	a.call(newWinGetURI, uintptr(unsafe.Pointer(&u)))
	a.call(newWinPutHandled, 1)
	w.refused.popup++
	f.event("popup-refused", "window", w.label, "uri", takeString(u))
	return sOK
}

func (f *feas) onNavigationCompleted(w *fwin, a *com) hresult {
	var ok, status int32
	var id uint64
	var httpStatus int32
	a.call(navDoneGetIsSuccess, uintptr(unsafe.Pointer(&ok)))
	a.call(navDoneGetWebErrorStatus, uintptr(unsafe.Pointer(&status)))
	a.call(navDoneGetNavigationID, uintptr(unsafe.Pointer(&id)))
	if a2, hr := a.queryInterface(&iidINavDoneArgs2); !hr.failed() && a2 != nil {
		a2.call(navDone2GetHTTPStatusCode, uintptr(unsafe.Pointer(&httpStatus)))
		a2.release()
	}
	var s *uint16
	w.wv.call(wvGetSource, uintptr(unsafe.Pointer(&s)))
	source := takeString(s)
	if w.errorPending {
		w.errorPending = false
		f.event("load", "window", w.label, "id", id, "uri", w.errURI, "success", ok != 0, "status", status, "http_status", httpStatus, "error_page", true, "source", source)
		return sOK
	}
	ev := []any{"window", w.label, "id", id, "uri", w.navURI, "source", source, "success", ok != 0, "status", status, "http_status", httpStatus,
		"document_responded", w.navResponded, "document_http_status", w.navHTTP, "method", w.navMethod, "from_post", w.navFromPost}
	// A response to the document request, whatever its status, is an
	// ordinary page. HttpStatusCode alone cannot say so: after a redirect it
	// reports the redirect's status even when the final request failed. A
	// cancellation is a policy refusal or a navigation superseded by another,
	// never a load failure.
	if ok != 0 || w.navResponded {
		f.event("load", ev...)
		return sOK
	}
	if status == webErrorOperationCanceled || w.closing {
		f.event("load-failed", append(ev, "error_page", false)...)
		return sOK
	}
	f.showError(w)
	f.event("load-failed", append(ev, "error_page", true, "retry_offered", w.errMethod == "GET")...)
	return sOK
}

// showError replaces the failed document with host-generated markup. Its two
// actions are links into errorOrigin carrying a per-failure nonce; the host
// cancels those navigations in NavigationStarting and acts on what it
// recorded, so the markup supplies no destination. Nothing on it runs
// script. A method the host never saw counts as POST.
func (f *feas) showError(w *fwin) {
	b := make([]byte, 16)
	rand.Read(b)
	w.errNonce, w.errURI, w.errMethod, w.errFromPost = hex.EncodeToString(b), w.navURI, w.navMethod, w.navFromPost
	if w.errMethod == "" {
		w.errMethod = "UNKNOWN"
	}
	retry := ""
	if w.errMethod == "GET" {
		retry = `<a class="b" id="retry" style="left:40px" href="` + errorOrigin + `/retry/` + w.errNonce + `">Retry</a> `
	}
	page := `<!doctype html><meta charset="utf-8"><title>Page did not load</title>` +
		`<style>body{font:15px sans-serif;margin:3em;background:#1e1f24;color:#e8e6e3}.b{position:absolute;top:240px;box-sizing:border-box;width:180px;height:44px;line-height:44px;text-align:center;background:#4fa3f7;color:#000;text-decoration:none;border-radius:4px}</style>` +
		`<h1>This page did not load</h1><p><code>` + html.EscapeString(w.errURI) + `</code></p>` +
		`<p>` + map[bool]string{true: "The request that failed was a form submission; it was not repeated.", false: "Nothing was submitted."}[w.errMethod != "GET"] + `</p>` +
		`<p>` + retry + `<a class="b" id="start" style="left:260px" href="` + errorOrigin + `/start/` + w.errNonce + `">Return to start</a></p>`
	w.errorPending = true
	hr := w.wv.call(wvNavigateToString, uintptr(unsafe.Pointer(utf16(page))))
	f.event("error-page", "window", w.label, "uri", w.errURI, "method", w.errMethod, "retry", retry != "", "script", strings.Contains(page, "<script"),
		"retry_at", []int{130, 262}, "start_at", []int{350, 262}, "hresult", hr.String())
}

func (f *feas) errorAction(w *fwin, uri string) {
	parts := strings.SplitN(strings.TrimPrefix(uri, errorOrigin+"/"), "/", 2)
	if len(parts) != 2 || w.errNonce == "" || parts[1] != w.errNonce {
		f.event("error-action-refused", "window", w.label, "uri", uri, "reason", "nonce")
		return
	}
	target := w.initial
	switch parts[0] {
	case "retry":
		if w.errMethod != "GET" {
			f.event("error-action-refused", "window", w.label, "action", "retry", "reason", "failed request was "+w.errMethod)
			return
		}
		target = w.errURI
	case "start":
	default:
		f.event("error-action-refused", "window", w.label, "uri", uri, "reason", "action")
		return
	}
	w.errNonce = "" // one action per failure
	f.event("error-action", "window", w.label, "action", parts[0], "uri", target)
	w.wv.call(wvNavigate, uintptr(unsafe.Pointer(utf16(target)))) // a GET, through the same navigation policy
}

// onWebResourceRequested sees every request of a window with the "*" filter.
// Requests outside the window's origin are answered here and never leave the
// browser; allowed ones proceed, except in-process ones, which are
// dispatched to the handler.
func (f *feas) onWebResourceRequested(w *fwin, args *com) hresult {
	var req *com
	if hr := args.call(wrrGetRequest, uintptr(unsafe.Pointer(&req))); hr.failed() || req == nil {
		return sOK
	}
	defer req.release()
	var u, m *uint16
	var ctxKind int32
	req.call(reqGetURI, uintptr(unsafe.Pointer(&u)))
	req.call(reqGetMethod, uintptr(unsafe.Pointer(&m)))
	args.call(wrrGetResourceContext, uintptr(unsafe.Pointer(&ctxKind)))
	in := adapt.Request{URL: takeString(u), Method: takeString(m)}
	if !f.allowed(w, in.URL) {
		// Outside the origin: answered natively, no network request.
		key := strconv.Itoa(int(ctxKind))
		w.blocked[key]++
		f.respond(w, args, synthetic(http.StatusForbidden, "blocked by the host: outside the window's origin"))
		f.event("blocked", "window", w.label, "method", in.Method, "uri", in.URL, "context", ctxKind)
		return sOK
	}
	if ctxKind == resourceContextDocument {
		w.docRequests++
		w.navMethod = in.Method
		if in.Method == http.MethodPost {
			w.navFromPost = true
		}
		f.event("document-request", "window", w.label, "method", in.Method, "uri", in.URL)
	}
	if w.dispatcher == nil {
		return sOK // loopback or attach: proceeds to the network unchanged
	}
	if strings.Contains(in.URL, "/api/abort") {
		// A native load failure for the in-process transport: no response is
		// installed, so the request leaves interception and fails to resolve.
		f.event("inprocess-abort", "window", w.label, "uri", in.URL, "how", "no response installed; the .invalid host cannot resolve")
		return sOK
	}
	if w.closing {
		f.respond(w, args, synthetic(http.StatusServiceUnavailable, "window closing"))
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
	var body *com
	if req.call(reqGetContent, uintptr(unsafe.Pointer(&body))) == sOK && body != nil {
		b, tooLarge, err := readStream(body, adapt.Limit)
		body.release()
		if err != nil {
			f.respond(w, args, synthetic(http.StatusBadRequest, "request body unreadable: "+err.Error()))
			return sOK
		}
		in.Body, in.BodyTooLarge = b, tooLarge
	}
	var def *com
	if hr := args.call(wrrGetDeferral, uintptr(unsafe.Pointer(&def))); hr.failed() {
		return sOK
	}
	args.addRef()
	id, ok := w.dispatcher.Start(in)
	p := &pending{args: args, deferral: def, what: in.Method + " " + in.URL, start: time.Now()}
	if !ok {
		f.settle(w, 0, p, synthetic(http.StatusServiceUnavailable, "window closing"), "refused")
		return sOK
	}
	w.pending[id] = p
	return sOK
}

func (f *feas) settle(w *fwin, id uint64, p *pending, resp adapt.Response, how string) {
	f.respond(w, p.args, resp)
	p.deferral.call(deferralComplete)
	p.deferral.release()
	p.args.release()
}

func (f *feas) respond(w *fwin, args *com, resp adapt.Response) hresult {
	if w.env == nil {
		return eFail
	}
	if f.c.feas.csp && w.mode == "inprocess" {
		resp.Header = resp.Header.Clone()
		resp.Header.Add("Content-Security-Policy", feasibility.OwnedCSP)
	}
	block, _ := adapt.HeaderBlock(resp.Header)
	var stream *com
	if len(resp.Body) > 0 {
		s, err := memStream(resp.Body)
		if err != nil {
			return eFail
		}
		stream = s
	}
	var r *com
	var sp uintptr
	if stream != nil {
		sp = stream.ptr()
	}
	hr := w.env.call(envCreateWebResourceResponse, sp, uintptr(resp.Status),
		uintptr(unsafe.Pointer(utf16(resp.Reason))), uintptr(unsafe.Pointer(utf16(block))), uintptr(unsafe.Pointer(&r)))
	stream.release()
	if hr.failed() {
		return hr
	}
	hr = args.call(wrrPutResponse, r.ptr())
	r.release()
	return hr
}

func (f *feas) fitBounds(w *fwin) {
	if w.ctl == nil {
		return
	}
	var r rect
	pGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r)))
	w.ctl.call(ctrlPutBounds, uintptr(unsafe.Pointer(&r)))
}

// click sends real pointer input at a point given in CSS pixels from the
// webview's top left, as the Linux host does with synthesized GTK input.
func (f *feas) click(w *fwin, x, y float64) {
	d, _, _ := pGetDpiForWindow.Call(w.hwnd)
	scale := float64(d) / 96
	var p point
	pClientToScreen.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
	sx, sy := p.x+int32(x*scale+0.5), p.y+int32(y*scale+0.5)
	pSetForegroundWindow.Call(w.hwnd)
	time.Sleep(150 * time.Millisecond)
	errs := []string{}
	if ok, _, err := pSetCursorPos.Call(uintptr(sx), uintptr(sy)); ok == 0 {
		errs = append(errs, "SetCursorPos: "+err.Error())
	}
	time.Sleep(30 * time.Millisecond)
	for _, flags := range []uint32{0x0002, 0x0004} { // MOUSEEVENTF_LEFTDOWN, MOUSEEVENTF_LEFTUP
		in := mouseInput{flags: flags}
		if n, _, err := pSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in)); n != 1 {
			errs = append(errs, "SendInput: "+err.Error()+" (a locked or disconnected session cannot inject input)")
		}
		time.Sleep(30 * time.Millisecond)
	}
	f.event("click", "window", w.label, "css", []float64{x, y}, "screen", []int32{sx, sy}, "scale", scale, "errors", errs)
}

// snapshot writes the webview's rendered document as PNG through
// CapturePreview: what WebView2 handed the compositor, not the screen.
func (f *feas) snapshot(w *fwin, path string) {
	if w.wv == nil {
		f.event("snapshot", "window", w.label, "path", path, "error", "no webview")
		return
	}
	stream, err := memStream(nil)
	if err != nil {
		f.event("snapshot", "window", w.label, "path", path, "error", err.Error())
		return
	}
	var cb *callback
	cb = f.callback("CapturePreviewCompleted", iidCapturePreviewCompleted, func(a, b uintptr) hresult {
		delete(w.owed, cb.id)
		hr := hresult(uint32(a))
		var errText string
		if hr.failed() {
			errText = "CapturePreview: " + hr.String()
		} else {
			stream.call(5, 0, 0, 0) // IStream::Seek(0, STREAM_SEEK_SET, NULL)
			b, _, rerr := readStream(stream, 64<<20)
			if rerr != nil {
				errText = rerr.Error()
			} else if werr := os.WriteFile(path, b, 0o644); werr != nil {
				errText = werr.Error()
			}
		}
		stream.release()
		f.event("snapshot", "window", w.label, "path", path, "error", errText)
		if w.closing {
			f.maybeRelease(w)
		}
		return sOK
	})
	w.owed[cb.id] = cb.name
	hr := w.wv.call(wvCapturePreview, capturePNG, stream.ptr(), cb.ptr())
	cb.release()
	if hr.failed() {
		delete(w.owed, cb.id)
		stream.release()
		f.event("snapshot", "window", w.label, "path", path, "error", "CapturePreview: "+hr.String())
	}
}

// selftest is the deliberate internal positive check: a Go client inside the
// host presents the right credential, so the endpoint check can be shown to
// admit it. It is not an external-client provisioning mechanism.
func (f *feas) selftest(w *fwin) {
	if w.mode != "loopback" || w.token == "" {
		return
	}
	host, tok := w.host, w.token
	go func() {
		statuses := map[string]int{}
		for _, c := range []struct{ name, host, origin string }{
			{"right credential", host, ""}, {"right credential, wrong Host", "localhost:" + strconv.Itoa(portOf(host)), ""},
			{"right credential, wrong Origin", host, "http://127.0.0.1:1"}, {"right credential, exact Origin", host, "http://" + host},
		} {
			req, _ := http.NewRequest("GET", "http://"+host+"/api/query?x=selftest", nil)
			req.Host = c.host
			if c.origin != "" {
				req.Header.Set("Origin", c.origin)
			}
			req.AddCookie(&http.Cookie{Name: feasibility.CookieName, Value: tok})
			if resp, err := http.DefaultTransport.RoundTrip(req); err == nil {
				statuses[c.name] = resp.StatusCode
				resp.Body.Close()
			}
		}
		f.post(func() { f.event("selftest", "window", w.label, "statuses", statuses) })
	}()
}

func portOf(host string) int {
	_, p, _ := net.SplitHostPort(host)
	n, _ := strconv.Atoi(p)
	return n
}

func (f *feas) status() {
	windows := map[string]any{}
	for _, w := range f.windows {
		m := map[string]any{"mode": w.mode, "closing": w.closing, "destroyed": w.destroyed,
			"endpoint": w.counters.Snapshot(), "handler": w.page.Counts(), "native_async": f.outstanding(w), "pending_requests": len(w.pending),
			"port": portOf(w.host), "profile": w.profile, "browser_pid": w.browserPID, "browser_exited": w.browserExited,
			"blocked": w.blocked, "refused": map[string]int{"navigation": w.refused.navigation, "subframe": w.refused.subframe, "popup": w.refused.popup},
			"navigations": w.navigations, "document_requests": w.docRequests}
		if w.wv != nil && !w.closing {
			var s, t *uint16
			w.wv.call(wvGetSource, uintptr(unsafe.Pointer(&s)))
			w.wv.call(wvGetDocumentTitle, uintptr(unsafe.Pointer(&t)))
			m["uri"], m["title"] = takeString(s), takeString(t)
		}
		windows[w.label] = m
	}
	f.event("status", "windows", windows, "callbacks_live", len(liveCallbacks()))
}

func (f *feas) close(w *fwin, why string) {
	if w.closing {
		return
	}
	w.closing = true
	w.closeAsked = time.Now()
	switch {
	case w.envPending:
		w.closeStage = "environment creation"
	case w.ctlPending:
		w.closeStage = "controller creation"
	case w.token != "" && !w.cookieReady:
		w.closeStage = "cookie installation"
	case !w.navigated:
		w.closeStage = "before navigation"
	default:
		w.closeStage = "running"
	}
	f.event("closing", "window", w.label, "why", why, "stage", w.closeStage, "native_async", f.outstanding(w), "pending_requests", len(w.pending),
		"navigated", w.navigated, "browser_pid", w.browserPID)
	w.cancel()
	if w.dispatcher != nil {
		w.dispatcher.Close()
	}
	ids := make([]uint64, 0, len(w.pending))
	for id := range w.pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		p := w.pending[id]
		delete(w.pending, id)
		f.settle(w, id, p, synthetic(http.StatusServiceUnavailable, "window closing"), "settled on close")
	}
	if w.wv2 != nil {
		w.wv2.call(wv2RemoveWebResourceResponseReceived, uintptr(w.respToken))
		w.wv2.release()
		w.wv2 = nil
	}
	if w.wv != nil {
		w.wv.call(wvStop)
		for i := len(w.tokens) - 1; i >= 0; i-- {
			t := w.tokens[i]
			w.wv.call(t.remove, uintptr(t.value))
		}
		w.tokens = nil
		w.wv.release()
		w.wv = nil
	}
	if w.ctl != nil {
		t := time.Now()
		hr := w.ctl.call(ctrlClose)
		w.ctl.release()
		w.ctl = nil
		w.controllerClosed = time.Now()
		f.event("controller-closed", "window", w.label, "hresult", hr.String(), "took", time.Since(t).Round(time.Microsecond).String())
	}
	pShowWindow.Call(w.hwnd, swHide)
	f.maybeRelease(w)
}

// maybeRelease moves a closing window toward teardown once nothing native is
// outstanding: no creation callback due, no completion owed, and, if a
// browser process ever served this profile, its exit observed. Until then
// the window waits and reports; filesystem deletion never runs early.
func (f *feas) maybeRelease(w *fwin) {
	if !w.closing || w.destroyed || w.releasing || w.envPending || w.ctlPending || f.outstanding(w) > 0 {
		return
	}
	if w.browserStarted && !w.browserExited {
		pSetTimer.Call(w.hwnd, timerRelease, 2000, 0)
		return
	}
	if !w.browserStarted && w.env != nil && w.env5 != nil {
		// An environment without a controller: whether a browser process ran
		// is not known here. Give an exit event a moment, then proceed.
		pSetTimer.Call(w.hwnd, timerNoBrowse, 1500, 0)
		return
	}
	w.releasing = true
	f.post(func() { f.afterRelease(w) }) // one loop iteration after the last native completion
}

func (f *feas) onBrowserProcessExited(w *fwin, args *com) {
	var pid uint32
	if args != nil {
		args.call(bpeGetExitKind, uintptr(unsafe.Pointer(&w.exitKind)))
		args.call(bpeGetProcessID, uintptr(unsafe.Pointer(&pid)))
	}
	w.browserExited = true
	since := ""
	if !w.controllerClosed.IsZero() {
		since = time.Since(w.controllerClosed).Round(time.Millisecond).String()
	}
	f.event("browser-process-exited", "window", w.label, "pid", pid, "kind", w.exitKind, "after_controller_close", since, "closing", w.closing)
	if w.closing {
		pKillTimer.Call(w.hwnd, timerRelease)
		pKillTimer.Call(w.hwnd, timerNoBrowse)
		f.maybeRelease(w) // not inside the exit callback: teardown is posted one iteration later
	}
}

// afterRelease runs after every native user of the profile has let go:
// remove the exit handler, release the environment, then delete the folder
// with bounded retries, and destroy the window.
func (f *feas) afterRelease(w *fwin) {
	pKillTimer.Call(w.hwnd, timerRelease)
	if w.env5 != nil {
		w.env5.call(env5RemoveBrowserProcessExited, uintptr(w.exitToken))
		w.env5.release()
		w.env5 = nil
	}
	if w.env != nil {
		w.env.release()
		w.env = nil
	}
	remove := os.RemoveAll
	injected := false
	if f.c.feas.removeFail {
		injected = true
		remove = func(string) error { return errors.New("injected: deletion failure after release") }
	}
	released := w.browserExited || !w.browserStarted
	t := time.Now()
	removal, err := feasibility.RemoveAfterRelease(w.profile, released, removeAttempts, removeInterval, remove)
	removal.Injected = injected
	f.event("profile-removal", "window", w.label, "removal", removal, "error", errString(err), "released", released,
		"browser_exit_observed", w.browserExited, "since_close", time.Since(w.closeAsked).Round(time.Millisecond).String(), "took", time.Since(t).Round(time.Millisecond).String())
	f.destroy(w, removal, err)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (f *feas) destroy(w *fwin, removal feasibility.Removal, removeErr error) {
	w.destroyed = true
	pDestroyWindow.Call(w.hwnd)
	drained := true
	if w.dispatcher != nil {
		drained = w.dispatcher.Wait(2 * time.Second)
	}
	if w.server != nil {
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		if err := w.server.Shutdown(ctx); err != nil {
			w.server.Close()
			drained = false
		}
		stop()
		<-w.served
	}
	f.event("closed", "window", w.label, "stage", w.closeStage, "native_async", f.outstanding(w), "pending_requests", len(w.pending), "handlers_drained", drained,
		"browser_pid", w.browserPID, "browser_started", w.browserStarted, "browser_exit_observed", w.browserExited, "exit_kind", w.exitKind,
		"profile_removed", removal.Removed, "removal_attempts", removal.Attempts, "removal_error", errString(removeErr), "completions_dropped", w.dropped,
		"endpoint", w.counters.Snapshot(), "handler", w.page.Counts(), "since_close", time.Since(w.closeAsked).Round(time.Millisecond).String())
	f.maybeQuit()
}

func (f *feas) maybeQuit() {
	if !f.quit {
		return
	}
	for _, w := range f.windows {
		if !w.destroyed {
			return
		}
	}
	pPostQuitMessage.Call(0)
}

// outstanding counts the completions still owed to a window whose handler
// objects WebView2 still holds. A handler it released without invoking, as
// Close does to a pending GetCookies, is recorded as dropped and no longer
// owed: nothing native can complete through it.
func (f *feas) outstanding(w *fwin) int {
	for id, name := range w.owed {
		if !callbackLive(id) {
			delete(w.owed, id)
			w.dropped = append(w.dropped, name)
			f.event("completion-dropped", "window", w.label, "callback", name, "closing", w.closing)
		}
	}
	return len(w.owed)
}

// short keeps data: URIs out of the log: the error page's markup is already
// recorded by error-page.
func short(uri string) string {
	if len(uri) > 160 {
		return uri[:160] + "…"
	}
	return uri
}
