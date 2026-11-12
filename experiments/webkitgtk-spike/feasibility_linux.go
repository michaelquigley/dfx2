//go:build linux

package main

// Passes B-D of the integration feasibility spike: a multi-window host whose
// windows each have their own ephemeral WebKit session. It is driven by
// console commands from feasibility.py and reports JSON events on stdout.
// The ordinary single-window host is untouched; nothing here runs unless
// -feasibility is given.
//
// Owned loopback windows get a random endpoint credential installed through
// the native cookie manager before their first navigation, and their
// endpoint admits only requests carrying it. Same-origin enforcement is a
// native content filter, with the endpoint's CSP header as a second,
// independently switchable mechanism. A failed top-level load shows a
// host-local error page whose actions are intercepted natively.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"dfx2webview2spike/adapt"
	"dfx2webview2spike/feasibility"
	"github.com/ebitengine/purego"
)

type feasConfig struct {
	filter, csp, cookie, cookieFail bool
	root                            string
	vmfree                          bool // no native JavaScript: see vmfree_linux.go
	reserve                         int  // with vmfree: the application's reserved GC signal
	xcpu                            bool // with vmfree: a Go SIGXCPU subscriber and scheduling records (xcpu_linux.go)
}

// Kinds routed through callback userdata: window id << 8 | kind.
const (
	kDelete = iota + 1
	kLoad
	kLoadFailed
	kProcess
	kCreate
	kPolicy
	kScheme
)

var fn struct {
	dataManager           func(uintptr) uintptr
	cookieManager         func(uintptr) uintptr
	addCookie             func(uintptr, uintptr, uintptr, uintptr, uintptr)
	addCookieFinish       func(uintptr, uintptr, *uintptr) int32
	soupCookieNew         func(string, string, string, string, int32) uintptr
	soupCookieHTTPOnly    func(uintptr, int32)
	soupCookieSameSite    func(uintptr, int32)
	soupCookieFree        func(uintptr)
	filterStoreNew        func(string) uintptr
	filterStoreSave       func(uintptr, string, uintptr, uintptr, uintptr, uintptr)
	filterStoreSaveFinish func(uintptr, uintptr, *uintptr) uintptr
	filterUnref           func(uintptr)
	viewUCM               func(uintptr) uintptr
	ucmAddFilter          func(uintptr, uintptr)
	loadAlternateHTML     func(uintptr, string, string, uintptr)
	actionIsRedirect      func(uintptr) int32
	actionNavigationType  func(uintptr) int32
	requestMethod         func(uintptr) string
	schemeFinishError     func(uintptr, uintptr)
	errorNewLiteral       func(uint32, int32, string) uintptr
	quarkFromString       func(string) uint32
	quarkToString         func(uint32) string
	callAsyncJS           func(uintptr, string, int64, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr)
	callAsyncJSFinish     func(uintptr, uintptr, *uintptr) uintptr
	jscValueToJSON        func(uintptr, uint32) uintptr
}

func loadFeasibility(vmfree bool) error {
	if err := loadNative(); err != nil {
		return err
	}
	libs := map[string]map[string]any{
		"libwebkit2gtk-4.1.so.0": {
			"webkit_web_context_get_website_data_manager":    &fn.dataManager,
			"webkit_website_data_manager_get_cookie_manager": &fn.cookieManager,
			"webkit_cookie_manager_add_cookie":               &fn.addCookie,
			"webkit_cookie_manager_add_cookie_finish":        &fn.addCookieFinish,
			"webkit_user_content_filter_store_new":           &fn.filterStoreNew,
			"webkit_user_content_filter_store_save":          &fn.filterStoreSave,
			"webkit_user_content_filter_store_save_finish":   &fn.filterStoreSaveFinish,
			"webkit_user_content_filter_unref":               &fn.filterUnref,
			"webkit_web_view_get_user_content_manager":       &fn.viewUCM,
			"webkit_user_content_manager_add_filter":         &fn.ucmAddFilter,
			"webkit_web_view_load_alternate_html":            &fn.loadAlternateHTML,
			"webkit_navigation_action_is_redirect":           &fn.actionIsRedirect,
			"webkit_navigation_action_get_navigation_type":   &fn.actionNavigationType,
			"webkit_uri_request_get_http_method":             &fn.requestMethod,
			"webkit_uri_scheme_request_finish_error":         &fn.schemeFinishError,
		},
		"libsoup-3.0.so.0": {"soup_cookie_new": &fn.soupCookieNew, "soup_cookie_set_http_only": &fn.soupCookieHTTPOnly,
			"soup_cookie_set_same_site_policy": &fn.soupCookieSameSite, "soup_cookie_free": &fn.soupCookieFree},
		"libglib-2.0.so.0": {"g_error_new_literal": &fn.errorNewLiteral, "g_quark_from_string": &fn.quarkFromString, "g_quark_to_string": &fn.quarkToString},
	}
	if !vmfree {
		// Native evaluation and result conversion: the VM-free host binds
		// neither.
		libs["libwebkit2gtk-4.1.so.0"]["webkit_web_view_call_async_javascript_function"] = &fn.callAsyncJS
		libs["libwebkit2gtk-4.1.so.0"]["webkit_web_view_call_async_javascript_function_finish"] = &fn.callAsyncJSFinish
		libs["libjavascriptcoregtk-4.1.so.0"] = map[string]any{"jsc_value_to_json": &fn.jscValueToJSON}
	}
	for lib, bindings := range libs {
		h, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return err
		}
		for name, dest := range bindings {
			addr, err := purego.Dlsym(h, name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			purego.RegisterFunc(dest, addr)
		}
	}
	return nil
}

type fwin struct {
	id                                int
	label, mode                       string
	allowed                           []*url.URL
	initial                           string
	window, view, webctx, cancellable uintptr
	token                             string
	host                              string // exact Host for the guard
	page                              *feasibility.Page
	counters                          *feasibility.Counters
	listener                          net.Listener
	server                            *http.Server
	served                            chan error
	dispatcher                        *adapt.Dispatcher
	ctx                               context.Context
	cancel                            context.CancelFunc
	requests                          map[uintptr]*request
	pending                           map[uint64]*request
	asyncs                            int // native async operations owned by this window
	closing, destroyed, navigated     bool
	destroying                        bool
	filterReady, cookieReady          bool
	navMethod, navURI                 string // the last allowed main-frame navigation
	navFromPost                       bool
	errNonce, errURI, errMethod       string
	errFromPost                       bool
}

type feas struct {
	c       feasConfig
	windows map[int]*fwin
	byLabel map[string]*fwin
	nextID  int
	post    poster
	async   map[uintptr]func(uintptr, uintptr)
	nextTok uintptr
	cb      struct{ one, two, three, four, five uintptr }
	out     sync.Mutex
	store   uintptr
	quit    bool
	sigs    *probe // disposition reader, for the VM-free host's records
	xcpu    chan os.Signal
}

var feasActive *feas

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

func token(id, kind int) uintptr { return uintptr(id<<8 | kind) }

func (f *feas) win(data uintptr) (*fwin, int) {
	return f.windows[int(data>>8)], int(data & 0xff)
}

func runFeasibility(c feasConfig) error {
	if err := loadFeasibility(c.vmfree); err != nil {
		return err
	}
	f := &feas{c: c, windows: map[int]*fwin{}, byLabel: map[string]*fwin{}, async: map[uintptr]func(uintptr, uintptr){}, nextTok: 1 << 32}
	feasActive = f
	var reservation map[string]any
	if c.vmfree {
		if err := loadVMFree(); err != nil {
			return err
		}
		urg, _ := sigaction(int(syscall.SIGURG))
		f.sigs = &probe{goHandler: urg.handler, notified: []syscall.Signal{syscall.SIGINT, syscall.SIGTERM}}
		if c.xcpu {
			// The application's own SIGXCPU subscriber, before WebKit exists.
			f.xcpu = make(chan os.Signal, 16)
			signal.Notify(f.xcpu, syscall.SIGXCPU)
			f.sigs.notified = append(f.sigs.notified, syscall.SIGXCPU)
		}
		f.event("signals", "stage", "start", "dispositions", signalView(f.sigs, c.reserve), "record", xcpuRecord())
		if c.reserve != 0 {
			// Before any WebKit type is initialized: WebKit initializes JSC
			// at its first class initialization.
			var err error
			if reservation, err = reserveGCSignal(f.sigs, c.reserve); err != nil {
				f.event("reservation-refused", "reservation", reservation, "error", err.Error())
				return err
			}
		}
	} else if c.reserve != 0 || c.xcpu {
		return fmt.Errorf("-signal-reserve and -feas-xcpu with -feasibility need -feas-vmfree")
	}
	if n.gtkInitCheck(0, 0) == 0 {
		return fmt.Errorf("GTK could not open the display")
	}
	f.cb.one = purego.NewCallback(func(data uintptr) uintptr { feasActive.drain(); return 0 })
	f.post.callback = f.cb.one
	f.cb.two = purego.NewCallback(func(object, data uintptr) uintptr {
		if w, kind := feasActive.win(data); w != nil && kind == kScheme {
			feasActive.receive(w, object)
		}
		return 0
	})
	f.cb.three = purego.NewCallback(func(object, arg, data uintptr) uintptr { return feasActive.three(object, arg, data) })
	f.cb.four = purego.NewCallback(func(view, decision, kind, data uintptr) uintptr { return feasActive.policy(view, decision, kind, data) })
	f.cb.five = purego.NewCallback(func(view, event, uri, gerr, data uintptr) uintptr {
		return feasActive.loadFailed(view, event, uri, gerr, data)
	})
	if c.filter {
		dir := filepath.Join(c.root, fmt.Sprintf("filters-%d", os.Getpid()))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		f.store = fn.filterStoreNew(dir) // transfer full: released after the main loop
		f.event("filter-store", "path", dir)
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	go func() { <-interrupts; f.post.post(func() { f.command("quit") }) }()
	go func() {
		s := bufio.NewScanner(os.Stdin)
		s.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for s.Scan() {
			line := s.Text()
			f.post.post(func() { f.command(line) })
		}
	}()
	f.event("ready", "pid", os.Getpid(), "filter", c.filter, "csp", c.csp, "cookie", c.cookie, "cookie_fail_injected", c.cookieFail,
		"vmfree", c.vmfree, "reservation", reservation)
	if c.vmfree {
		f.event("signals", "stage", "ready", "dispositions", signalView(f.sigs, c.reserve), "record", xcpuRecord())
	}
	n.gtkMain()
	if f.store != 0 {
		n.gObjectUnref(f.store)
	}
	removal, err := feasibility.RemoveAfterRelease(filepath.Join(c.root, fmt.Sprintf("filters-%d", os.Getpid())), true, 5, 50*time.Millisecond, nil)
	if !c.filter {
		removal, err = feasibility.Removal{}, nil
	}
	if c.vmfree {
		f.event("signals", "stage", "exit", "dispositions", signalView(f.sigs, c.reserve), "record", xcpuRecord())
	}
	f.event("exit", "filter_store_removal", removal, "error", fmt.Sprint(err))
	return nil
}

func (f *feas) drain() {
	for _, fun := range f.post.take() {
		fun()
	}
	for _, w := range f.windows {
		if w.dispatcher == nil || w.closing {
			continue
		}
		for _, r := range w.dispatcher.Take() {
			if req := w.pending[r.ID]; req != nil {
				delete(w.pending, r.ID)
				f.finish(w, req, r.Response)
			}
		}
	}
}

func (f *feas) asyncToken(w *fwin, fun func(uintptr, uintptr)) uintptr {
	f.nextTok++
	if w != nil {
		w.asyncs++
	}
	f.async[f.nextTok] = func(a, b uintptr) {
		if w != nil {
			w.asyncs--
		}
		fun(a, b)
		if w != nil {
			f.maybeDestroy(w)
		}
	}
	return f.nextTok
}

func (f *feas) three(object, arg, data uintptr) uintptr {
	if fun := f.async[data]; fun != nil {
		delete(f.async, data)
		fun(object, arg)
		return 0
	}
	w, kind := f.win(data)
	if w == nil {
		return 0
	}
	switch kind {
	case kDelete:
		f.close(w, "window close")
		return 1
	case kLoad:
		f.event("load", "window", w.label, "load_event", arg, "uri", n.webkitWebViewGetURI(object))
	case kProcess:
		f.event("web-process-terminated", "window", w.label, "reason", arg)
	case kCreate:
		f.event("popup-refused", "window", w.label)
	}
	return 0
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
	case "js": // js LABEL BODY
		if f.c.vmfree {
			f.event("error", "command", "js", "error", "refused: the VM-free host evaluates no JavaScript")
		} else if w != nil {
			f.js(w, arg(2))
		}
	case "xcpu-native", "xcpu-wait": // VM-free host with -feas-xcpu
		f.xcpuProbe(map[string]string{"xcpu-native": "native", "xcpu-wait": "external"}[arg(0)])
	case "reload":
		if w != nil && f.c.vmfree {
			gdk.reload(w.view)
		}
	case "click": // click LABEL X Y, VM-free host only
		var x, y float64
		if _, err := fmt.Sscan(arg(2), &x, &y); w != nil && f.c.vmfree && err == nil {
			f.click(w, x, y)
		}
	case "load":
		if w != nil {
			n.webkitWebViewLoadURI(w.view, arg(2))
		}
	case "close":
		if w != nil {
			f.close(w, "command")
		}
	case "snapshot":
		if w != nil {
			f.snapshot(w, arg(2))
		}
	case "inspect": // open and dock the inspector
		if w != nil {
			n.webkitSettingsSetEnableDeveloperExtras(n.webkitWebViewGetSettings(w.view), 1)
			inspector := n.webkitWebViewGetInspector(w.view)
			n.webkitWebInspectorShow(inspector)
			go func() {
				time.Sleep(1500 * time.Millisecond)
				f.post.post(func() {
					if !w.closing {
						n.webkitWebInspectorAttach(inspector)
						f.event("inspector", "window", w.label, "attached", n.webkitWebInspectorIsAttached(inspector) != 0)
					}
				})
			}()
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
		requests: map[uintptr]*request{}, pending: map[uint64]*request{}}
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
		if f.c.cookie {
			tok, err := feasibility.NewToken()
			if err != nil {
				f.event("error", "window", label, "error", err.Error())
				return
			}
			w.token = tok
			csp := ""
			if f.c.csp {
				csp = feasibility.OwnedCSP
			}
			handler = feasibility.Guard(handler, tok, w.host, origin.String(), csp, w.counters)
		}
		w.server = &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return w.ctx }, ReadHeaderTimeout: 5 * time.Second}
		w.served = make(chan error, 1)
		go func() { w.served <- w.server.Serve(ln) }()
	case "inprocess":
		origin, _ = url.Parse(inprocessOrigin)
		w.dispatcher = adapt.NewDispatcher(w.ctx, feasibility.Count(w.page, w.counters), origin, func() bool { return f.post.post(nil) }, func(string, ...any) {})
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
	f.windows[w.id], f.byLabel[label] = w, w
	w.page.OnReport = func(r json.RawMessage) {
		f.post.post(func() { f.event("report", "window", label, "report", r) })
	}
	w.cancellable = n.gCancellableNew()
	w.webctx = n.webkitWebContextNewEphemeral()
	if mode == "inprocess" {
		security := n.webkitWebContextGetSecurityManager(w.webctx)
		n.webkitSecurityManagerRegisterURISchemeAsSecure(security, "dfx2")
		n.webkitSecurityManagerRegisterURISchemeAsCorsEnabled(security, "dfx2")
		n.webkitWebContextRegisterURIScheme(w.webctx, "dfx2", f.cb.two, token(w.id, kScheme), 0)
	}
	w.window = n.gtkWindowNew(0)
	n.gtkWindowSetTitle(w.window, "dfx2 feasibility "+label+" — "+mode)
	n.gtkWindowSetDefaultSize(w.window, 760, 560)
	w.view = n.webkitWebViewNewWithContext(w.webctx)
	n.webkitSettingsSetJavascriptCanOpenWindowsAutomatically(n.webkitWebViewGetSettings(w.view), 1)
	n.gtkContainerAdd(w.window, w.view)
	n.gSignalConnectData(w.window, "delete-event", f.cb.three, token(w.id, kDelete), 0, 0)
	n.gSignalConnectData(w.view, "load-changed", f.cb.three, token(w.id, kLoad), 0, 0)
	n.gSignalConnectData(w.view, "load-failed", f.cb.five, token(w.id, kLoadFailed), 0, 0)
	n.gSignalConnectData(w.view, "web-process-terminated", f.cb.three, token(w.id, kProcess), 0, 0)
	n.gSignalConnectData(w.view, "create", f.cb.three, token(w.id, kCreate), 0, 0)
	n.gSignalConnectData(w.view, "decide-policy", f.cb.four, token(w.id, kPolicy), 0, 0)
	n.gtkWidgetShowAll(w.window)
	f.event("opened", "window", label, "mode", mode, "origin", origin.String(), "ephemeral_session", true,
		"credential", w.token != "")
	// Navigation waits for the content filter and the credential cookie.
	w.filterReady, w.cookieReady = !f.c.filter, w.token == ""
	if f.c.filter {
		f.installFilter(w)
	}
	if w.token != "" {
		f.installCookie(w)
	}
	f.maybeNavigate(w)
}

// filterRules blocks every load except those to the window's allowed
// origins, and every subframe document, before any request leaves the web
// process. Rules are WebKit content-blocker JSON.
func filterRules(allowed []*url.URL) string {
	rules := []map[string]any{
		{"trigger": map[string]any{"url-filter": ".*"}, "action": map[string]any{"type": "block"}},
	}
	for _, o := range allowed {
		rules = append(rules, map[string]any{"trigger": map[string]any{"url-filter": "^" + regexp.QuoteMeta(o.String()) + "/"},
			"action": map[string]any{"type": "ignore-previous-rules"}})
	}
	rules = append(rules, map[string]any{"trigger": map[string]any{"url-filter": ".*", "resource-type": []string{"document"}, "load-context": []string{"child-frame"}},
		"action": map[string]any{"type": "block"}})
	b, _ := json.Marshal(rules)
	return string(b)
}

func (f *feas) installFilter(w *fwin) {
	src := filterRules(w.allowed)
	b := []byte(src)
	bytes := n.gBytesNew(unsafe.Pointer(&b[0]), uintptr(len(b)))
	tok := f.asyncToken(w, func(store, result uintptr) {
		var e uintptr
		filter := fn.filterStoreSaveFinish(store, result, &e)
		err := nativeError(e)
		switch {
		case w.closing:
			if filter != 0 {
				fn.filterUnref(filter)
			}
			f.event("filter-late", "window", w.label)
		case err != nil || filter == 0:
			f.event("filter-failed", "window", w.label, "error", fmt.Sprint(err))
			f.close(w, "content filter failed")
		default:
			fn.ucmAddFilter(fn.viewUCM(w.view), filter)
			fn.filterUnref(filter)
			w.filterReady = true
			f.event("filter-installed", "window", w.label, "rules", src)
			f.maybeNavigate(w)
		}
	})
	fn.filterStoreSave(f.store, fmt.Sprintf("window-%d", w.id), bytes, w.cancellable, f.cb.three, tok)
	n.gBytesUnref(bytes)
}

func (f *feas) installCookie(w *fwin) {
	cookie := fn.soupCookieNew(feasibility.CookieName, w.token, "127.0.0.1", "/", -1) // -1: a session cookie
	fn.soupCookieHTTPOnly(cookie, 1)
	fn.soupCookieSameSite(cookie, 2) // SOUP_SAME_SITE_POLICY_STRICT
	cm := fn.cookieManager(fn.dataManager(w.webctx))
	tok := f.asyncToken(w, func(source, result uintptr) {
		var e uintptr
		ok := fn.addCookieFinish(source, result, &e) != 0
		err := nativeError(e)
		if f.c.cookieFail {
			ok, err = false, fmt.Errorf("injected: cookie installation failure")
		}
		switch {
		case w.closing:
			f.event("cookie-late", "window", w.label, "ok", ok, "navigated", w.navigated)
		case !ok:
			f.event("cookie-failed", "window", w.label, "error", fmt.Sprint(err))
			f.close(w, "cookie installation failed")
		default:
			w.cookieReady = true
			f.event("cookie-installed", "window", w.label, "http_only", true, "same_site", "strict")
			f.maybeNavigate(w)
		}
	})
	fn.addCookie(cm, cookie, w.cancellable, f.cb.three, tok)
	fn.soupCookieFree(cookie) // add_cookie copies it
}

func (f *feas) maybeNavigate(w *fwin) {
	if w.closing || w.navigated || !w.filterReady || !w.cookieReady {
		return
	}
	w.navigated = true
	f.event("navigate", "window", w.label, "uri", w.initial)
	n.webkitWebViewLoadURI(w.view, w.initial)
}

func (f *feas) allowed(w *fwin, raw string) bool {
	for _, o := range w.allowed {
		if _, err := adapt.CheckOrigin(raw, o); err == nil {
			return true
		}
	}
	return false
}

func (f *feas) policy(view, decision, kind, data uintptr) uintptr {
	w, _ := f.win(data)
	if w == nil || kind > 1 {
		return 0
	}
	action := n.webkitNavigationPolicyDecisionGetNavigationAction(decision)
	req := n.webkitNavigationActionGetRequest(action)
	uri := n.webkitURIRequestGetURI(req)
	method := fn.requestMethod(req)
	redirect := fn.actionIsRedirect(action) != 0
	if method == "" {
		// Custom schemes report no HTTP method. A form submission (1) or
		// resubmission (4) is then treated as a POST: Retry is withheld from
		// in-process form navigations, including GET forms, rather than risk
		// replaying an edit.
		method = "GET"
		if t := fn.actionNavigationType(action); t == 1 || t == 4 {
			method = "FORM"
		}
	}
	if kind == 1 {
		f.event("navigation-refused", "window", w.label, "kind", "new-window", "uri", uri)
		n.webkitPolicyDecisionIgnore(decision)
		return 1
	}
	if strings.HasPrefix(uri, "dfx2-error:") {
		n.webkitPolicyDecisionIgnore(decision)
		f.errorAction(w, uri)
		return 1
	}
	if !f.allowed(w, uri) {
		f.event("navigation-refused", "window", w.label, "kind", "navigation", "uri", uri, "method", method, "redirect", redirect)
		n.webkitPolicyDecisionIgnore(decision)
		return 1
	}
	// Subframe navigations reach this decision too; the content filter and
	// CSP refuse their documents. Record only what could be the main frame.
	if !redirect {
		w.navFromPost = method == "POST" || method == "FORM"
	} else if method == "POST" || method == "FORM" {
		w.navFromPost = true
	}
	w.navMethod, w.navURI = method, uri
	f.event("navigation-allowed", "window", w.label, "uri", uri, "method", method, "redirect", redirect)
	return 0
}

func (f *feas) loadFailed(view, event, uri, gerr, data uintptr) uintptr {
	w, _ := f.win(data)
	if w == nil {
		return 0
	}
	g := (*struct {
		domain  uint32
		code    int32
		message uintptr
	})(unsafe.Add(unsafe.Pointer(nil), gerr))
	domain, failing := fn.quarkToString(g.domain), cstring(uri)
	ev := []any{"window", w.label, "uri", failing, "domain", domain, "code", g.code, "message", cstring(g.message), "method", w.navMethod, "from_post", w.navFromPost}
	// Policy refusals and cancellations are not load failures.
	if domain == "WebKitPolicyError" || (domain == "WebKitNetworkError" && g.code == 302) || w.closing {
		f.event("load-failed", append(ev, "error_page", false)...)
		return 0
	}
	f.showError(w, failing)
	f.event("load-failed", append(ev, "error_page", true, "retry_offered", w.errMethod == "GET")...)
	return 1
}

// showError replaces the failed document with host-generated markup. Its two
// actions are links to dfx2-error: URIs carrying a per-failure nonce; the
// host intercepts them in decide-policy and acts on what it recorded, so the
// markup supplies no destination. Nothing on it runs script.
func (f *feas) showError(w *fwin, failing string) {
	b := make([]byte, 16)
	rand.Read(b)
	w.errNonce, w.errURI, w.errMethod, w.errFromPost = hex.EncodeToString(b), failing, w.navMethod, w.navFromPost
	if w.navURI != failing && w.errMethod == "" {
		w.errMethod = "GET"
	}
	retry := ""
	if w.errMethod == "GET" {
		retry = `<a class="b" id="retry" style="left:40px" href="dfx2-error:retry/` + w.errNonce + `">Retry</a> `
	}
	page := `<!doctype html><meta charset="utf-8"><title>Page did not load</title>` +
		`<style>body{font:15px sans-serif;margin:3em;background:#1e1f24;color:#e8e6e3}.b{position:absolute;top:240px;box-sizing:border-box;width:180px;height:44px;line-height:44px;text-align:center;background:#4fa3f7;color:#000;text-decoration:none;border-radius:4px}</style>` +
		`<h1>This page did not load</h1><p><code>` + html.EscapeString(failing) + `</code></p>` +
		`<p>` + map[bool]string{true: "The request that failed was a form submission; it was not repeated.", false: "Nothing was submitted."}[w.errMethod != "GET"] + `</p>` +
		`<p>` + retry + `<a class="b" id="start" style="left:260px" href="dfx2-error:start/` + w.errNonce + `">Return to start</a></p>`
	fn.loadAlternateHTML(w.view, page, failing, 0)
	// Where pointer input finds the actions (CSS pixels from the view's
	// top left), and that the markup carries no script.
	f.event("error-page", "window", w.label, "uri", failing, "retry", retry != "", "script", strings.Contains(page, "<script"),
		"retry_at", []int{130, 262}, "start_at", []int{350, 262})
}

func (f *feas) errorAction(w *fwin, uri string) {
	parts := strings.SplitN(strings.TrimPrefix(uri, "dfx2-error:"), "/", 2)
	if len(parts) != 2 || w.errNonce == "" || parts[1] != w.errNonce {
		f.event("error-action-refused", "window", w.label, "uri", uri)
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
		f.event("error-action-refused", "window", w.label, "uri", uri)
		return
	}
	w.errNonce = "" // one action per failure
	f.event("error-action", "window", w.label, "action", parts[0], "uri", target)
	n.webkitWebViewLoadURI(w.view, target) // a GET, through the same navigation policy
}

func (f *feas) receive(w *fwin, p uintptr) {
	req := &request{native: n.gObjectRef(p), input: adapt.Request{URL: n.webkitURISchemeRequestGetURI(p), Method: n.webkitURISchemeRequestGetHTTPMethod(p)}}
	w.requests[p] = req
	if w.closing {
		f.finish(w, req, closedResponse())
		return
	}
	if strings.Contains(req.input.URL, "/api/abort") {
		// A native load failure for the in-process transport.
		e := fn.errorNewLiteral(fn.quarkFromString("dfx2-spike"), 1, "injected transport failure: no response")
		fn.schemeFinishError(req.native, e)
		n.gErrorFree(e)
		n.gObjectUnref(req.native)
		delete(w.requests, p)
		return
	}
	var iter [3]uintptr
	if headers := n.webkitURISchemeRequestGetHTTPHeaders(p); headers != 0 {
		n.soupMessageHeadersIterInit(&iter, headers)
		var name, value uintptr
		for n.soupMessageHeadersIterNext(&iter, &name, &value) != 0 {
			req.input.Header = append(req.input.Header, [2]string{cstring(name), cstring(value)})
		}
	}
	req.stream = n.webkitURISchemeRequestGetHTTPBody(p)
	if req.stream != 0 {
		f.read(w, req)
		return
	}
	f.dispatch(w, req)
}

func (f *feas) read(w *fwin, req *request) {
	tok := f.asyncToken(w, func(stream, result uintptr) {
		var e uintptr
		b := n.gInputStreamReadBytesFinish(stream, result, &e)
		err := nativeError(e)
		var size uintptr
		if b != 0 {
			p := n.gBytesGetData(b, &size)
			if size != 0 {
				req.input.Body = append(req.input.Body, unsafe.Slice((*byte)(p), int(size))...)
			}
			n.gBytesUnref(b)
		}
		if w.closing || err != nil || size == 0 || len(req.input.Body) > adapt.Limit {
			n.gObjectUnref(req.stream)
			req.stream = 0
			if w.closing || err != nil {
				f.finish(w, req, closedResponse())
				return
			}
			req.input.BodyTooLarge = len(req.input.Body) > adapt.Limit
			f.dispatch(w, req)
			return
		}
		f.read(w, req)
	})
	n.gInputStreamReadBytesAsync(req.stream, 64*1024, 0, w.cancellable, f.cb.three, tok)
}

func (f *feas) dispatch(w *fwin, req *request) {
	if id, ok := w.dispatcher.Start(req.input); ok {
		w.pending[id] = req
	} else {
		f.finish(w, req, closedResponse())
	}
}

func (f *feas) finish(w *fwin, req *request, r adapt.Response) {
	var p unsafe.Pointer
	if len(r.Body) > 0 {
		p = unsafe.Pointer(&r.Body[0])
	}
	bytes := n.gBytesNew(p, uintptr(len(r.Body)))
	stream := n.gMemoryInputStreamNewFromBytes(bytes)
	n.gBytesUnref(bytes)
	response := n.webkitURISchemeResponseNew(stream, int64(len(r.Body)))
	n.gObjectUnref(stream)
	reason := r.Reason
	if reason == "" {
		reason = http.StatusText(r.Status)
	}
	n.webkitURISchemeResponseSetStatus(response, uint32(r.Status), reason)
	headers := n.soupMessageHeadersNew(1)
	for name, values := range r.Header {
		for _, value := range values {
			n.soupMessageHeadersAppend(headers, name, value)
		}
	}
	if f.c.csp {
		n.soupMessageHeadersAppend(headers, "Content-Security-Policy", feasibility.OwnedCSP)
	}
	n.webkitURISchemeResponseSetHTTPHeaders(response, headers)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		n.webkitURISchemeResponseSetContentType(response, ct)
	}
	n.webkitURISchemeRequestFinishWithResponse(req.native, response)
	n.gObjectUnref(response)
	n.gObjectUnref(req.native)
	delete(w.requests, req.native)
}

func (f *feas) js(w *fwin, body string) {
	tok := f.asyncToken(w, func(view, result uintptr) {
		var e uintptr
		v := fn.callAsyncJSFinish(view, result, &e)
		err := nativeError(e)
		var out json.RawMessage
		if v != 0 {
			if s := fn.jscValueToJSON(v, 0); s != 0 {
				out = json.RawMessage(cstring(s))
				n.gFree(s)
			}
			n.gObjectUnref(v)
		}
		if len(out) == 0 {
			out = json.RawMessage("null")
		}
		f.event("js", "window", w.label, "body", body, "result", out, "error", errString(err))
	})
	fn.callAsyncJS(w.view, body, -1, 0, 0, 0, w.cancellable, f.cb.three, tok)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (f *feas) snapshot(w *fwin, path string) {
	tok := f.asyncToken(w, func(view, result uintptr) {
		var e uintptr
		surface := n.webkitWebViewGetSnapshotFinish(view, result, &e)
		err := nativeError(e)
		if surface != 0 {
			if status := n.cairoSurfaceWriteToPNG(surface, path); status != 0 {
				err = fmt.Errorf("PNG status %d", status)
			}
			n.cairoSurfaceDestroy(surface)
		}
		f.event("snapshot", "window", w.label, "path", path, "error", errString(err))
	})
	n.webkitWebViewGetSnapshot(w.view, 0, 0, w.cancellable, f.cb.three, tok)
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
		f.post.post(func() { f.event("selftest", "window", w.label, "statuses", statuses) })
	}()
}

func (f *feas) status() {
	windows := map[string]any{}
	for _, w := range f.windows {
		windows[w.label] = map[string]any{"mode": w.mode, "closing": w.closing, "destroyed": w.destroyed,
			"endpoint": w.counters.Snapshot(), "handler": w.page.Counts(), "native_requests": len(w.requests), "native_async": w.asyncs,
			"port": portOf(w.host)}
	}
	for _, w := range f.windows {
		if !w.destroyed {
			windows[w.label].(map[string]any)["uri"] = n.webkitWebViewGetURI(w.view)
			if f.c.vmfree {
				windows[w.label].(map[string]any)["title"] = gdk.title(w.view)
			}
		}
	}
	ev := []any{"windows", windows, "async", len(f.async)}
	if f.c.vmfree {
		ev = append(ev, "signals", signalView(f.sigs, f.c.reserve), "record", xcpuRecord())
	}
	f.event("status", ev...)
}

func portOf(host string) int {
	_, p, _ := net.SplitHostPort(host)
	n, _ := strconv.Atoi(p)
	return n
}

func (f *feas) close(w *fwin, why string) {
	if w.closing {
		return
	}
	w.closing = true
	f.event("closing", "window", w.label, "why", why, "native_requests", len(w.requests), "native_async", w.asyncs, "navigated", w.navigated)
	w.cancel()
	if w.dispatcher != nil {
		w.dispatcher.Close()
	}
	n.gCancellableCancel(w.cancellable)
	n.webkitWebViewStopLoading(w.view)
	n.gtkWidgetHide(w.window)
	for id, req := range w.pending {
		f.finish(w, req, closedResponse())
		delete(w.pending, id)
	}
	f.maybeDestroy(w)
}

// maybeDestroy tears a closing window down once its native operations have
// completed, on a later main-loop iteration. A GTask is released only after
// its completion callback returns, and releasing it drops its source object,
// such as the cookie manager; destroying the context from inside that
// callback left the manager to be finalized after its context (a native
// crash on 2.52.6). Deferring by one iteration lets every task let go first.
func (f *feas) maybeDestroy(w *fwin) {
	if !w.closing || w.asyncs > 0 || w.destroyed || w.destroying {
		return
	}
	w.destroying = true
	f.post.post(func() { f.destroy(w) })
}

func (f *feas) destroy(w *fwin) {
	w.destroyed = true
	n.gtkWidgetDestroy(w.window)
	n.gObjectUnref(w.cancellable)
	n.gObjectUnref(w.webctx)
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
	f.event("closed", "window", w.label, "native_requests", len(w.requests), "native_async", w.asyncs, "handlers_drained", drained,
		"endpoint", w.counters.Snapshot(), "handler", w.page.Counts())
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
	n.gtkMainQuit()
}
