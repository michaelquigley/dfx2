package webview2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/michaelquigley/dfx2/internal/origin"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// Factory creates WebView2 attachments in Win32 browser shells.
type Factory struct{}

var _ platform.BrowserFactory = Factory{}

// eventRegistration is an event handler to remove at teardown.
type eventRegistration struct {
	wv     uintptr
	remove int
	token  int64
}

// nativeRequest is an intercepted in-process request waiting for the
// endpoint's response: its event arguments and deferral, held on the UI
// thread until Respond or Close settles them.
type nativeRequest struct {
	args, deferral uintptr
}

// browser is one attachment. Every field except stats is UI-thread state.
type browser struct {
	host    platform.BrowserHost
	devtool bool
	parent  uintptr // the shell's HWND
	surface platform.Surface
	mode    platform.BrowserMode
	start   string
	allowed []string
	owned   string // the origin interception confines to; "" for attachments
	// metadata is the in-process endpoint's request metadata limit.
	metadata int64
	cookie   *platform.BrowserCookie
	profile  string // this window's user data folder

	env, env5, ctl, wv, wv2, wv4 uintptr
	events                       []eventRegistration
	exitToken                    int64
	envPending, ctlPending       bool
	owed                         int // completions whose handler the engine still holds
	browserPID                   uint32
	browserStarted               bool // a controller was delivered: a browser process serves the profile
	browserExited, browserFailed bool
	exitKind                     int32
	runtime                      string

	nav          navigation
	failure      struct{ nonce, uri, method string }
	errorPending bool   // the error document's own navigation is in flight
	errorNavID   uint64 // and its NavigationId, once known
	requests     map[platform.ID]*nativeRequest
	streams      int // response bodies the engine still holds

	cookieReady, navigated       bool
	closing, controllerClosed    bool
	releasing, released, cleaned bool
	loaderHeld                   bool

	closeAt string // audition: close when this stage is reached
	closeFn func()
	closed  time.Time

	mu    sync.Mutex
	seq   uint64
	stats platform.BrowserAudit
}

const eventMemory = 64

func (b *browser) event(e platform.BrowserEvent, count func(*platform.BrowserAudit)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e.Seq = b.seq
	if count != nil {
		count(&b.stats)
	}
	if len(b.stats.Events) == eventMemory {
		b.stats.Events = b.stats.Events[1:]
	}
	b.stats.Events = append(b.stats.Events, e)
}

// sync copies UI-thread counts into the locked snapshot.
func (b *browser) sync() {
	asyncs := b.owed
	if b.envPending {
		asyncs++
	}
	if b.ctlPending {
		asyncs++
	}
	if b.browserStarted && !b.browserExited {
		asyncs++ // the browser process's exit is still owed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.Navigated, b.stats.Closing, b.stats.Destroyed, b.stats.Released = b.navigated, b.closing, b.controllerClosed, b.released
	b.stats.Requests, b.stats.Copies, b.stats.Asyncs, b.stats.Runtime = len(b.requests), b.streams, asyncs, b.runtime
}

// New extracts and loads the pinned loader on the first attachment, checks
// the runtime, and creates the window's fresh profile directory. No native
// browser object exists yet: Start creates them asynchronously.
func (f Factory) New(parent platform.BrowserParent, config platform.BrowserConfig, host platform.BrowserHost) (platform.Browser, error) {
	if err := install(); err != nil {
		return nil, err
	}
	return newBrowser(parent, config, host)
}

func newBrowser(parent platform.BrowserParent, config platform.BrowserConfig, host platform.BrowserHost) (*browser, error) {
	b := &browser{host: host, devtool: config.DevTools, parent: parent.Handle, surface: parent.Surface, mode: config.Mode,
		requests: make(map[platform.ID]*nativeRequest), metadata: int64(config.RequestMetadata)}
	switch config.Mode {
	case platform.BrowserInProcess:
		b.start, b.allowed, b.owned = startURL, []string{Origin}, Origin
	case platform.BrowserLoopback:
		if len(config.Origins) != 1 || config.Credential == nil {
			return nil, errors.New("loopback browser needs its endpoint origin and credential")
		}
		c := *config.Credential
		b.start, b.allowed, b.owned, b.cookie = config.StartURL, []string{config.Origins[0]}, config.Origins[0], &c
	case platform.BrowserAttach:
		if len(config.Origins) == 0 {
			return nil, errors.New("attached browser needs allowed origins")
		}
		b.start, b.allowed = config.StartURL, append([]string(nil), config.Origins...)
	default:
		return nil, fmt.Errorf("browser mode %d", config.Mode)
	}
	if b.parent == 0 {
		return nil, errors.New("browser parent has no window handle")
	}
	b.stats.Start = b.start
	if err := n.initCOM(); err != nil {
		return nil, err
	}
	version, err := loader.acquire()
	if err != nil {
		return nil, err
	}
	b.loaderHeld, b.runtime = true, version
	// A fresh, private profile per window: its own cookies, storage, cache and
	// browser process, deleted after that process has exited.
	dir, err := os.MkdirTemp("", "dfx2-webview2-profile-")
	if err != nil {
		b.dropLoader()
		return nil, fmt.Errorf("browser profile directory: %w", err)
	}
	b.profile = dir
	b.sync()
	return b, nil
}

// dropLoader releases this attachment's use of the loader. The last user
// unloads it and removes its directory through the host's cleanup path.
func (b *browser) dropLoader() {
	if !b.loaderHeld {
		return
	}
	b.loaderHeld = false
	loader.release(func(name string, work func() error) error {
		if b.host == nil {
			return work()
		}
		return b.host.Cleanup(name, work)
	})
}

func (b *browser) Origin() string {
	if b.mode == platform.BrowserInProcess {
		return Origin
	}
	return ""
}

// stage runs the audition close hook when its stage is reached.
func (b *browser) stage(name string) {
	if b.closeAt == name && b.closeFn != nil {
		fn := b.closeFn
		b.closeAt, b.closeFn = "", nil
		fn()
	}
}

// Start creates the environment over the profile; the controller, settings,
// handlers, the credential and the first navigation follow in its callbacks.
func (b *browser) Start() {
	cb := n.newHandler("EnvironmentCompleted", iidEnvironmentCompleted, func(a, env uintptr) hresult {
		b.onEnvironment(hresult(uint32(a)), env)
		return sOK
	}, func() { b.dropped("EnvironmentCompleted", func() { b.envPending = false }) })
	b.envPending = true
	hr := n.createEnvironment(loader.lib, b.profile, cb)
	cb.release()
	if hr.failed() {
		b.envPending = false
		b.host.Failed(hr.err("CreateCoreWebView2EnvironmentWithOptions"))
		return
	}
	b.stage("environment")
	b.sync()
}

// dropped settles a completion whose handler the engine released without
// invoking it, as Close does to a pending GetCookies. It runs on any thread,
// so the settlement hops to the UI thread; nothing native can complete
// through the handler any more.
func (b *browser) dropped(name string, settle func()) {
	n.post(func() {
		settle()
		b.event(platform.BrowserEvent{Kind: "completion-dropped", Detail: name}, nil)
		b.sync()
		b.maybeRelease()
	})
}

func (b *browser) onEnvironment(hr hresult, env uintptr) {
	b.envPending = false
	failed := hr.failed() || env == 0
	if failed {
		// Close does not cancel environment creation. Keep a delivered failure
		// even when it races with shutdown.
		b.host.Failed(fmt.Errorf("WebView2 environment creation failed: HRESULT %v (is the WebView2 Runtime installed?)", hr))
	}
	if b.closing {
		// Late: not AddRef'd, so returning releases it; nothing attaches.
		b.event(platform.BrowserEvent{Kind: "late-callback", Detail: "environment " + hr.String()}, nil)
		b.sync()
		b.maybeRelease()
		return
	}
	if failed {
		return
	}
	n.addRef(env)
	b.env = env
	if v := n.envVersion(env); v != "" {
		b.runtime = v
	}
	// The browser process's exit is the release evidence for the profile.
	env5, hr5 := n.queryInterface(env, &iidIEnvironment5)
	if hr5.failed() || env5 == 0 {
		b.host.Failed(fmt.Errorf("ICoreWebView2Environment5 unavailable: HRESULT %v; a WebView2 Runtime with BrowserProcessExited is required", hr5))
		return
	}
	b.env5 = env5
	exited := n.newHandler("BrowserProcessExited", iidBrowserProcessExited, func(_, args uintptr) hresult {
		b.onBrowserProcessExited(args)
		return sOK
	}, nil)
	token, hr := n.env5AddExited(env5, exited)
	exited.release()
	if hr.failed() {
		b.host.Failed(hr.err("add_BrowserProcessExited"))
		return
	}
	b.exitToken = token
	b.stage("controller")
	if b.closing {
		b.sync()
		b.maybeRelease()
		return
	}
	cb := n.newHandler("ControllerCompleted", iidControllerCompleted, func(a, ctl uintptr) hresult {
		b.onController(hresult(uint32(a)), ctl)
		return sOK
	}, func() { b.dropped("ControllerCompleted", func() { b.ctlPending = false }) })
	b.ctlPending = true
	hr = n.envCreateCtrl(env, b.parent, cb)
	cb.release()
	if hr.failed() {
		b.ctlPending = false
		b.host.Failed(hr.err("CreateCoreWebView2Controller"))
	}
	b.sync()
}

func (b *browser) onController(hr hresult, ctl uintptr) {
	b.ctlPending = false
	failed := hr.failed() || ctl == 0
	if failed {
		// The parent stays alive until attachment release, so even E_ABORT
		// (creation canceled by parent destruction) is unexpected here.
		b.host.Failed(fmt.Errorf("WebView2 controller creation failed: HRESULT %v", hr))
	}
	if b.closing {
		// Late: close what was delivered; never attach it. Its browser process
		// then exits, and release waits for that.
		if ctl != 0 {
			b.browserStarted = true
			n.ctrlClose(ctl)
			b.controllerClosed = true
		}
		b.event(platform.BrowserEvent{Kind: "late-callback", Detail: "controller " + hr.String()}, nil)
		b.sync()
		b.maybeRelease()
		return
	}
	if failed {
		return
	}
	n.addRef(ctl)
	b.ctl = ctl
	b.browserStarted = true
	wv, hr := n.ctrlWebView(ctl)
	if hr.failed() || wv == 0 {
		b.host.Failed(hr.err("get_CoreWebView2"))
		return
	}
	b.wv = wv
	b.browserPID = n.wvBrowserPID(wv)
	if hr := n.wvSettings(wv, b.devtool); hr.failed() {
		b.host.Failed(hr.err("ICoreWebView2Settings"))
		return
	}
	// The runtime must be using this window's private profile, not one an
	// environment variable or registry override pointed it at.
	if path, ok := n.wvProfilePath(wv); !ok || !within(path, b.profile) {
		b.host.Failed(fmt.Errorf("WebView2 profile isolation: the runtime reports profile %q outside the window's directory %q", path, b.profile))
		return
	}
	if err := b.addHandlers(); err != nil {
		b.host.Failed(err)
		return
	}
	b.Bounds(b.surface)
	b.event(platform.BrowserEvent{Kind: "controller", Detail: "browser process " + strconv.Itoa(int(b.browserPID))}, nil)
	if b.cookie != nil {
		b.installCookie()
	} else {
		b.cookieReady = true
	}
	b.sync()
	b.maybeNavigate()
}

// within reports whether path is inside dir, comparing cleaned paths
// case-insensitively, as Windows does.
func within(path, dir string) bool {
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(dir)), strings.ToLower(filepath.Clean(path)))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// addHandlers registers every event handler and the request interception
// before the first navigation. Owned windows intercept every request in every
// context and answer anything off their origin themselves; attachments
// observe document requests only, for the failed navigation's method.
func (b *browser) addHandlers() error {
	type reg struct {
		name        string
		iid         guid
		add, remove int
		fn          func(a, args uintptr) hresult
	}
	regs := []reg{
		{"NavigationStarting", iidNavigationStarting, wvAddNavigationStarting, wvRemoveNavigationStarting, func(_, args uintptr) hresult { b.onNavigationStarting(args); return sOK }},
		{"NavigationCompleted", iidNavigationCompleted, wvAddNavigationCompleted, wvRemoveNavigationCompleted, func(_, args uintptr) hresult { b.onNavigationCompleted(args); return sOK }},
		{"FrameNavigationStarting", iidFrameNavigationStarting, wvAddFrameNavigationStarting, wvRemoveFrameNavigationStarting, func(_, args uintptr) hresult { b.onFrameNavigationStarting(args); return sOK }},
		{"NewWindowRequested", iidNewWindowRequested, wvAddNewWindowRequested, wvRemoveNewWindowRequested, func(_, args uintptr) hresult { b.onNewWindowRequested(args); return sOK }},
		{"PermissionRequested", iidPermissionRequested, wvAddPermissionRequested, wvRemovePermissionRequested, func(_, args uintptr) hresult { b.onPermissionRequested(args); return sOK }},
		{"ProcessFailed", iidProcessFailed, wvAddProcessFailed, wvRemoveProcessFailed, func(_, args uintptr) hresult { b.onProcessFailed(args); return sOK }},
		{"WebResourceRequested", iidWebResourceRequested, wvAddWebResourceRequested, wvRemoveWebResourceRequested, func(_, args uintptr) hresult { b.receive(args); return sOK }},
	}
	for _, r := range regs {
		cb := n.newHandler(r.name, r.iid, r.fn, nil)
		token, hr := n.wvAddEvent(b.wv, r.add, cb)
		cb.release() // the engine holds its own until remove_
		if hr.failed() {
			return hr.err("add_" + r.name)
		}
		b.events = append(b.events, eventRegistration{b.wv, r.remove, token})
	}
	wv2, hr := n.queryInterface(b.wv, &iidICoreWebView2_2)
	if hr.failed() || wv2 == 0 {
		return hr.err("QueryInterface(ICoreWebView2_2)")
	}
	b.wv2 = wv2
	cb := n.newHandler("WebResourceResponseReceived", iidResponseReceived, func(_, args uintptr) hresult { b.onResponseReceived(args); return sOK }, nil)
	token, hr := n.wvAddEvent(wv2, wv2AddWebResourceResponseReceived, cb)
	cb.release()
	if hr.failed() {
		return hr.err("add_WebResourceResponseReceived")
	}
	b.events = append(b.events, eventRegistration{wv2, wv2RemoveWebResourceResponseReceived, token})
	wv4, hr := n.queryInterface(b.wv, &iidICoreWebView2_4)
	if hr.failed() || wv4 == 0 {
		return hr.err("QueryInterface(ICoreWebView2_4)")
	}
	b.wv4 = wv4
	cb = n.newHandler("DownloadStarting", iidDownloadStarting, func(_, args uintptr) hresult { b.onDownloadStarting(args); return sOK }, nil)
	token, hr = n.wvAddEvent(wv4, wv4AddDownloadStarting, cb)
	cb.release()
	if hr.failed() {
		return hr.err("add_DownloadStarting")
	}
	b.events = append(b.events, eventRegistration{wv4, wv4RemoveDownloadStarting, token})
	context := int32(resourceContextAll)
	if b.owned == "" {
		context = resourceContextDocument
	}
	if hr := n.wvAddFilter(b.wv, "*", context); hr.failed() {
		return hr.err("AddWebResourceRequestedFilter")
	}
	return nil
}

// installCookie adds the credential through the native cookie manager.
// AddOrUpdateCookie is synchronous and has no completion; GetCookies
// afterwards confirms the manager holds it before anything navigates.
func (b *browser) installCookie() {
	cm, hr := n.wvCookieManager(b.wv2)
	if hr.failed() || cm == 0 {
		b.host.Failed(hr.err("get_CookieManager"))
		return
	}
	defer n.release(cm)
	host := origin.Host(b.cookie.Origin)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	cookie, hr := n.cookieCreate(cm, b.cookie.Name, b.cookie.Value, host, "/")
	if hr.failed() || cookie == 0 {
		b.host.Failed(hr.err("CreateCookie"))
		return
	}
	n.cookieSecure(cookie)
	hr = n.cookieAdd(cm, cookie)
	n.release(cookie)
	if hr.failed() {
		b.host.Failed(hr.err("AddOrUpdateCookie"))
		return
	}
	b.stage("cookie")
	name := b.cookie.Name
	cb := n.newHandler("GetCookiesCompleted", iidGetCookiesCompleted, func(a, list uintptr) hresult {
		b.owed--
		b.onCookies(hresult(uint32(a)), list, name)
		return sOK
	}, func() { b.dropped("GetCookiesCompleted", func() { b.owed-- }) })
	b.owed++
	hr = n.cookieList(cm, b.cookie.Origin+"/", cb)
	cb.release()
	if hr.failed() {
		b.owed--
		b.host.Failed(hr.err("GetCookies"))
	}
}

func (b *browser) onCookies(hr hresult, list uintptr, name string) {
	found, httpOnly, sameSite := false, false, int32(-1)
	if !hr.failed() && list != 0 {
		found, httpOnly, sameSite = n.cookieFound(list, name)
	}
	failed := hr.failed() || !found || !httpOnly
	if failed {
		// A handler dropped by Close settles through dropped(), not this callback.
		b.host.Failed(fmt.Errorf("endpoint credential not installed: GetCookies %v, found %t, HttpOnly %t", hr, found, httpOnly))
	}
	switch {
	case b.closing:
		b.event(platform.BrowserEvent{Kind: "late-callback", Detail: "cookies " + hr.String()}, nil)
		b.sync()
		b.maybeRelease()
	case failed:
		return
	default:
		b.cookieReady = true
		b.event(platform.BrowserEvent{Kind: "cookie-installed", Detail: "SameSite kind " + strconv.Itoa(int(sameSite))}, nil)
		b.maybeNavigate()
	}
}

func (b *browser) maybeNavigate() {
	if b.closing || b.navigated || !b.cookieReady || b.wv == 0 {
		return
	}
	b.stage("navigation")
	// False means the window is closing; Close follows.
	if b.closing || !b.host.Ready() {
		return
	}
	b.navigated = true
	if hr := n.wvNavigate(b.wv, b.start); hr.failed() {
		b.host.Failed(hr.err("Navigate"))
	}
	b.sync()
}

// Bounds fits the controller to the shell's client area and follows its
// visibility; the surface is kept for a controller created later.
func (b *browser) Bounds(s platform.Surface) {
	b.surface = s
	if b.ctl == 0 || b.closing {
		return
	}
	n.ctrlBounds(b.ctl, int32(s.Width), int32(s.Height))
	n.ctrlVisible(b.ctl, s.Visible)
	n.ctrlNotifyMoved(b.ctl)
}

// Focus moves keyboard focus into the web view when the shell gains it.
func (b *browser) Focus() {
	if b.ctl != 0 && !b.closing {
		n.ctrlFocus(b.ctl)
	}
}

func (b *browser) Diagnostics() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	return fmt.Sprintf("webview2 %s navigated=%t closing=%t controller-closed=%t released=%t native-requests=%d response-streams=%d async=%d",
		s.Runtime, s.Navigated, s.Closing, s.Destroyed, s.Released, s.Requests, s.Copies, s.Asyncs)
}

func (b *browser) onNavigationStarting(args uintptr) {
	uri, id, redirected := n.navStarting(args)
	switch {
	case strings.HasPrefix(uri, errorOrigin+"/"):
		n.navCancel(args)
		b.errorAction(uri)
		return
	case b.errorPending && errorPageURI(uri):
		// The host's own error document, with its opaque origin.
		b.errorNavID = id
		return
	case b.closing || !allowed(uri, b.allowed):
		n.navCancel(args)
		b.event(platform.BrowserEvent{Kind: "navigation-refused", URI: uri, Detail: map[bool]string{true: "redirect"}[redirected]},
			func(a *platform.BrowserAudit) { a.Refused++ })
		return
	}
	b.nav.start(id, uri, redirected)
	// Every allowed navigation retires the previous error page, even one that
	// requests the same URI again.
	b.failure.nonce = ""
	b.event(platform.BrowserEvent{Kind: "navigation-allowed", URI: uri, Detail: map[bool]string{true: "redirect"}[redirected]},
		func(a *platform.BrowserAudit) { a.Allowed++ })
}

func (b *browser) onFrameNavigationStarting(args uintptr) {
	uri, _, _ := n.navStarting(args)
	n.navCancel(args)
	b.event(platform.BrowserEvent{Kind: "subframe-refused", URI: uri}, func(a *platform.BrowserAudit) { a.Refused++ })
}

func (b *browser) onNewWindowRequested(args uintptr) {
	// Handled with no NewWindow set: nothing opens, and window.open returns null.
	n.newWindowHandled(args)
	b.event(platform.BrowserEvent{Kind: "popup-refused"}, func(a *platform.BrowserAudit) { a.Popups++ })
}

func (b *browser) onPermissionRequested(args uintptr) {
	n.permissionDeny(args)
	b.event(platform.BrowserEvent{Kind: "permission-denied"}, func(a *platform.BrowserAudit) { a.Permissions++ })
}

func (b *browser) onDownloadStarting(args uintptr) {
	n.downloadCancel(args)
	b.event(platform.BrowserEvent{Kind: "download-canceled"}, func(a *platform.BrowserAudit) { a.Downloads++ })
}

func (b *browser) onResponseReceived(args uintptr) {
	uri, status := n.responseReceived(args)
	if b.errorPending {
		return
	}
	if b.nav.response(uri) {
		b.event(platform.BrowserEvent{Kind: "document-response", URI: uri, Detail: strconv.Itoa(int(status))}, nil)
	}
}

func (b *browser) onNavigationCompleted(args uintptr) {
	success, status, id, httpStatus := n.navCompleted(args)
	if b.errorPending && id == b.errorNavID {
		b.errorPending, b.errorNavID = false, 0
		return
	}
	if id != b.nav.id {
		return // superseded, or the completion of a refused navigation
	}
	detail := fmt.Sprintf("status %d http %d", status, httpStatus)
	switch b.nav.completed(success, status, b.closing) {
	case loaded:
		b.event(platform.BrowserEvent{Kind: "load", URI: b.nav.uri, Method: b.nav.method, Detail: detail}, nil)
	case loadStopped:
		b.event(platform.BrowserEvent{Kind: "load-stopped", URI: b.nav.uri, Detail: detail}, nil)
	case loadFailed:
		b.showError(detail)
	}
}

// showError replaces the failed document with the framework's local page. A
// method never seen from a document request counts as unknown: no Retry.
func (b *browser) showError(detail string) {
	b.failure.nonce, b.failure.uri, b.failure.method = nonce(), b.nav.uri, b.nav.method
	b.errorPending, b.errorNavID = true, 0
	hr := n.wvNavigateHTML(b.wv, errorPage(b.failure.uri, b.failure.method, b.failure.nonce))
	b.event(platform.BrowserEvent{Kind: "error-page", URI: b.failure.uri, Method: b.failure.method, Retry: retryable(b.failure.method), Detail: detail},
		func(a *platform.BrowserAudit) { a.ErrorPages++ })
	if hr.failed() {
		b.errorPending = false
		b.host.Failed(hr.err("NavigateToString"))
	}
}

// errorAction carries out Retry or Return to start for the current failure
// only. Retry repeats a known GET; Return loads the start page with GET. Both
// go through navigation policy; neither resubmits a form.
func (b *browser) errorAction(uri string) {
	action, nonce, ok := errorAction(uri)
	if !ok || b.failure.nonce == "" || nonce != b.failure.nonce || b.closing {
		b.event(platform.BrowserEvent{Kind: "error-action-refused", Detail: "stale or unknown action"}, nil)
		return
	}
	target := b.start
	if action == "retry" {
		if !retryable(b.failure.method) {
			b.event(platform.BrowserEvent{Kind: "error-action-refused", Detail: "retry withheld: method " + b.failure.method}, nil)
			return
		}
		target = b.failure.uri
	}
	b.failure.nonce = "" // one action per failure
	b.event(platform.BrowserEvent{Kind: "error-action", URI: target, Detail: action}, func(a *platform.BrowserAudit) { a.Actions++ })
	if hr := n.wvNavigate(b.wv, target); hr.failed() {
		b.host.Failed(hr.err("Navigate"))
	}
}

// Process failures: the browser process, the main frame's render process and
// a frame's render process ending are browser failures, like a WebKit web
// process terminating on Linux. An unresponsive renderer and the auxiliary
// processes the engine restarts by itself are recorded, not fatal.
func (b *browser) onProcessFailed(args uintptr) {
	kind, reason, exitCode, description := n.processFailed(args)
	name := pick(processFailedKindNames, int(kind), "process failure kind "+strconv.Itoa(int(kind)))
	detail := fmt.Sprintf("%s (reason %s, exit code %d, %s)", name, pick(processFailedReasonNames, int(reason), strconv.Itoa(int(reason))), exitCode, description)
	b.event(platform.BrowserEvent{Kind: "process-failed", Detail: detail}, nil)
	switch kind {
	case failedBrowserProcess, failedRenderProcess, failedFrameRenderProcess:
		if kind == failedBrowserProcess {
			b.browserFailed = true
		}
		b.host.Failed(errors.New("WebView2 " + detail))
	}
}

func pick(names string, i int, fallback string) string {
	parts := strings.Split(names, ",")
	if i >= 0 && i < len(parts) {
		return parts[i]
	}
	return fallback
}

func (b *browser) onBrowserProcessExited(args uintptr) {
	kind, pid := n.exitedArgs(args)
	b.browserExited, b.exitKind = true, kind
	since := ""
	if !b.closed.IsZero() {
		since = " " + time.Since(b.closed).Round(time.Millisecond).String() + " after close"
	}
	b.event(platform.BrowserEvent{Kind: "browser-process-exited", Detail: fmt.Sprintf("pid %d kind %d closing %t%s", pid, kind, b.closing, since)}, nil)
	if !b.closing && !b.browserFailed {
		b.browserFailed = true
		b.host.Failed(fmt.Errorf("WebView2 browser process %d exited (kind %d)", pid, kind))
	}
	b.sync()
	if b.closing {
		b.maybeRelease()
	}
}

// receive handles one intercepted request on the UI thread. Requests outside
// an owned window's origin are answered here and never leave the browser.
// In-process requests on the origin are measured, admitted and read within
// the endpoint's limits; the request strings are Go copies.
func (b *browser) receive(args uintptr) {
	req, uri, method, context := n.requestInfo(args)
	if req == 0 {
		return
	}
	defer n.release(req)
	if context == resourceContextDocument {
		b.nav.document(uri, method)
		b.event(platform.BrowserEvent{Kind: "document-request", URI: uri, Method: method}, nil)
	}
	if b.owned == "" {
		return // an attachment: observed only
	}
	if !allowed(uri, []string{b.owned}) {
		b.refuse(args, 403, "forbidden\n")
		b.event(platform.BrowserEvent{Kind: "blocked", URI: uri, Method: method, Detail: strconv.Itoa(int(context))}, nil)
		return
	}
	if b.mode != platform.BrowserInProcess {
		return // loopback: on the origin, to the network
	}
	if b.closing {
		b.refuse(args, 503, "endpoint busy or closing\n")
		return
	}
	in := platform.BrowserRequest{Method: method, URL: uri}
	if in.Method == "" {
		in.Method = "GET"
	}
	size := int64(platform.RequestRecordOverhead + len(in.Method) + len(in.URL))
	length := int64(-1)
	if it, hr := n.requestHeaders(req); !hr.failed() && it != 0 {
		// Measure first, converting nothing past the limit: a UTF-16 unit is at
		// least one UTF-8 byte, so an over-limit count needs no conversion.
		for size <= b.metadata && n.iterHas(it) {
			name, value, ok := n.iterCurrent(it)
			if !ok {
				break
			}
			units := int64(n.wlen(name) + n.wlen(value) + platform.RequestHeaderOverhead)
			if size+units > b.metadata {
				n.free(name)
				n.free(value)
				size += units
				break
			}
			h := platform.Header{Name: n.take(name), Value: n.take(value)}
			size += int64(len(h.Name) + len(h.Value) + platform.RequestHeaderOverhead)
			if strings.EqualFold(h.Name, "Content-Length") {
				if v, err := strconv.ParseInt(strings.TrimSpace(h.Value), 10, 64); err == nil && v >= 0 {
					length = v
				}
			}
			in.Header = append(in.Header, h)
			n.iterNext(it)
		}
		n.release(it)
	}
	in.MetadataSize = size
	if size > b.metadata {
		in.Header = nil
	}
	content := n.requestContent(req)
	if content != 0 {
		defer n.release(content)
	}
	switch {
	case content == 0, length == 0:
		in.BodyLength = 0
	default:
		in.BodyLength = length
	}
	d := b.host.Request(in)
	b.act(args, content, d)
	b.sync()
}

// act carries out the endpoint's decision for an intercepted request.
func (b *browser) act(args, content uintptr, d platform.RequestDecision) {
	id := d.ID // body-read decisions may abort without repeating the admitted ID
	for d.Action == platform.RequestRead {
		// Bounded synchronous reads of the engine's buffered request body, in
		// the chunks the endpoint grants, until it ends or the endpoint decides.
		if content == 0 {
			d = b.host.RequestBodyEnd(d.ID, nil)
			continue
		}
		buf := make([]byte, max(d.ReadChunk, 1))
		read, end, hr := n.streamRead(content, buf)
		switch {
		case hr.failed():
			d = b.host.RequestBodyEnd(d.ID, hr.err("IStream::Read"))
		case read > 0:
			id := d.ID
			d = b.host.RequestBody(id, buf[:read])
			if d.Action == platform.RequestRead && end {
				d = b.host.RequestBodyEnd(id, nil)
			}
		default:
			d = b.host.RequestBodyEnd(d.ID, nil)
		}
	}
	switch d.Action {
	case platform.RequestWait:
		def, hr := n.deferral(args)
		if hr.failed() || def == 0 {
			b.host.RequestReleased(d.ID) // the engine proceeds without a response
			return
		}
		n.addRef(args)
		b.requests[d.ID] = &nativeRequest{args: args, deferral: def}
	case platform.RequestRespond:
		b.deliver(d.ID, args, 0, d.Response)
	default:
		// Abort: no response is installed, so the engine's request proceeds to
		// its unresolvable host and fails there. The original admission still
		// needs releasing even when the abort decision omits its ID.
		if d.ID == 0 {
			d.ID = id
		}
		if d.ID != 0 {
			b.host.RequestReleased(d.ID)
		}
	}
}

// refuse answers a request natively with a fixed failure, holding nothing.
func (b *browser) refuse(args uintptr, status int, body string) {
	resp, hr := n.envCreateResponse(b.env, 0, status, statusText(status), "Content-Type: text/plain; charset=utf-8\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\n"+
		"Content-Security-Policy: default-src 'none'\r\n")
	if hr.failed() || resp == 0 {
		return
	}
	n.putResponse(args, resp)
	n.release(resp)
}

func statusText(status int) string {
	switch status {
	case 403:
		return "Forbidden"
	case 503:
		return "Service Unavailable"
	}
	return "Error"
}

// Respond finishes a waiting request with a complete response.
func (b *browser) Respond(id platform.ID, resp platform.BrowserResponse) bool {
	r := b.requests[id]
	if r == nil || b.closing {
		return false
	}
	delete(b.requests, id)
	b.deliver(id, r.args, r.deferral, resp)
	b.sync()
	return true
}

// deliver installs a response. Its body is copied into an IStream the host
// implements, whose final release by the engine, wherever it happens, reports
// the request released; a bodiless response releases it at once.
func (b *browser) deliver(id platform.ID, args, deferral uintptr, resp platform.BrowserResponse) {
	var sb strings.Builder
	for _, h := range resp.Header {
		sb.WriteString(h.Name)
		sb.WriteString(": ")
		sb.WriteString(h.Value)
		sb.WriteString("\r\n")
	}
	var stream uintptr
	if len(resp.Body) > 0 {
		body := append([]byte(nil), resp.Body...) // borrowed for the call
		stream = n.newBodyStream(body, func() {
			n.post(func() { b.streamReleased(id) })
		})
	}
	native, hr := n.envCreateResponse(b.env, stream, resp.Status, reasonPhrase(resp.Status), sb.String())
	if stream != 0 {
		n.release(stream) // the response holds its own reference, or none on failure
	}
	installed := false
	if !hr.failed() && native != 0 {
		installed = !n.putResponse(args, native).failed()
		n.release(native)
	}
	if deferral != 0 {
		n.deferralDone(deferral)
		n.release(deferral)
		n.release(args)
	}
	b.mu.Lock()
	b.stats.Delivered++
	b.mu.Unlock()
	if stream == 0 || !installed && hr.failed() {
		// Nothing of ours is held: released now. (A stream the engine never
		// took is released by our own release above, which also reports.)
		if stream == 0 {
			b.host.RequestReleased(id)
		}
		return
	}
	b.streams++
}

func reasonPhrase(status int) string {
	switch status / 100 {
	case 2:
		return "OK"
	case 3:
		return "Redirect"
	case 4:
		return "Client Error"
	case 5:
		return "Server Error"
	}
	return "Response"
}

// streamReleased runs on the UI thread after the engine dropped a response
// body.
func (b *browser) streamReleased(id platform.ID) {
	b.streams--
	b.host.RequestReleased(id)
	b.sync()
}

// Close stops the page, settles every waiting request without a response,
// and closes the controller while the shell is still valid. Release follows
// once every owed callback has settled and the browser process has exited.
func (b *browser) Close() {
	if b.closing {
		return
	}
	b.closing, b.closed = true, time.Now()
	b.failure.nonce = ""
	for id, r := range b.requests {
		delete(b.requests, id)
		n.deferralDone(r.deferral)
		n.release(r.deferral)
		n.release(r.args)
		b.host.RequestReleased(id)
	}
	if b.wv != 0 {
		n.wvStop(b.wv)
	}
	for i := len(b.events) - 1; i >= 0; i-- {
		e := b.events[i]
		n.wvRemoveEvent(e.wv, e.remove, e.token)
	}
	b.events = nil
	for _, obj := range []*uintptr{&b.wv4, &b.wv2, &b.wv} {
		if *obj != 0 {
			n.release(*obj)
			*obj = 0
		}
	}
	if b.ctl != 0 {
		n.ctrlClose(b.ctl)
		n.release(b.ctl)
		b.ctl = 0
		b.controllerClosed = true
	}
	b.sync()
	b.maybeRelease()
}

// maybeRelease moves a closing window toward release once nothing native is
// outstanding: no creation callback due, no completion owed and, if a browser
// process ever served this profile, its exit observed. There is no timeout:
// an exit that never arrives is visible in diagnostics, never assumed.
func (b *browser) maybeRelease() {
	if !b.closing || b.releasing || b.envPending || b.ctlPending || b.owed > 0 || b.browserStarted && !b.browserExited {
		return
	}
	b.releasing = true
	// One loop iteration later: never inside the exit callback itself.
	n.post(b.afterRelease)
}

// afterRelease runs after every native user of the profile has let go:
// remove the exit handler, release the environment, drop the loader, then
// remove the profile off the UI thread with bounded attempts, and report
// Released.
func (b *browser) afterRelease() {
	if b.env5 != 0 {
		n.env5RemoveExited(b.env5, b.exitToken)
		n.release(b.env5)
		b.env5 = 0
	}
	if b.env != 0 {
		n.release(b.env)
		b.env = 0
	}
	b.dropLoader()
	if dir := b.profile; dir != "" && !b.cleaned {
		b.cleaned = true
		if err := b.host.Cleanup("browser profile "+dir, func() error { return removeAfterRelease(dir) }); err != nil {
			b.host.Failed(err)
		}
	}
	b.released = true
	b.sync()
	b.host.Released()
}

// AuditionLoad navigates as a user would, through navigation policy. UI
// thread; internal auditions only.
func (b *browser) AuditionLoad(uri string) error {
	if b.closing || !b.navigated {
		return errors.New("browser closing or still initializing")
	}
	return n.wvNavigate(b.wv, uri).err("Navigate")
}

// AuditionErrorAction follows the current error page's action link, through
// navigation policy, as a click would. UI thread; internal auditions only.
func (b *browser) AuditionErrorAction(action string) error {
	if b.closing || b.failure.nonce == "" {
		return errors.New("no error page is showing")
	}
	return n.wvNavigate(b.wv, errorOrigin+"/"+action+"/"+b.failure.nonce).err("Navigate")
}

// AuditionURI reports the view's current URI and title. UI thread.
func (b *browser) AuditionURI() (uri, title string) {
	if b.wv == 0 || b.closing {
		return "", ""
	}
	return n.wvSource(b.wv), n.wvTitle(b.wv)
}

// AuditionState is safe from any goroutine.
func (b *browser) AuditionState() platform.BrowserAudit {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	s.Events = append([]platform.BrowserEvent(nil), s.Events...)
	return s
}

// AuditionCloseAt arranges for close to run when initialization reaches a
// stage: "environment" (creation requested), "controller" (environment ready,
// controller not yet requested), "cookie" (credential added, its confirmation
// pending) or "navigation" (about to ask the core). A stage already passed is
// an error. UI thread; internal auditions only.
func (b *browser) AuditionCloseAt(stage string, close func()) error {
	passed := map[string]bool{"environment": b.env != 0, "controller": b.ctl != 0,
		"cookie": b.cookie == nil || b.cookieReady || b.owed > 0, "navigation": b.navigated}
	inFlight := map[string]bool{"environment": b.envPending, "controller": b.ctlPending}
	done, known := passed[stage]
	if !known {
		return fmt.Errorf("unknown stage %q", stage)
	}
	if done || b.closing {
		return fmt.Errorf("stage %q already passed", stage)
	}
	if inFlight[stage] {
		close() // that creation is in flight now: its callback will be late
		return nil
	}
	b.closeAt, b.closeFn = stage, close
	return nil
}

// AuditionOpenDevTools opens the engine's inspector window. UI thread.
func (b *browser) AuditionOpenDevTools() error {
	if b.wv == 0 || b.closing || !b.devtool {
		return errors.New("developer tools are not enabled on this window")
	}
	n.wvOpenDevTools(b.wv)
	return nil
}

// AuditionProfile reports the window's profile directory and browser process.
func (b *browser) AuditionProfile() (dir string, pid uint32) { return b.profile, b.browserPID }
