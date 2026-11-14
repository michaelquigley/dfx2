package webkit

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/origin"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// Factory creates WebKitGTK attachments in GTK browser shells.
type Factory struct{}

var _ platform.BrowserFactory = Factory{}

// WebKit enum and error values used here.
const (
	policyNavigation  = 0
	policyNewWindow   = 1
	policyResponse    = 2
	headersResponse   = 1 // SOUP_MESSAGE_HEADERS_RESPONSE
	sameSiteStrict    = 2 // SOUP_SAME_SITE_POLICY_STRICT
	networkCancelled  = 302
	ioErrorCancelled  = 19 // G_IO_ERROR_CANCELLED, in the g-io-error-quark domain
	processTerminated = 2  // WEBKIT_WEB_PROCESS_TERMINATED_BY_API
)

type connection struct {
	object uintptr
	id     uint64
}

type nativeRequest struct {
	native, stream uintptr
	reading        bool
}

// browser is one attachment. Every field except stats is UI-thread state.
type browser struct {
	host    platform.BrowserHost
	devtool bool
	parent  uintptr
	mode    platform.BrowserMode
	start   string
	allowed []string
	owned   string // the origin the content filter confines to; "" for attachments
	// metadata is the in-process endpoint's request metadata limit.
	metadata int64
	cookie   *platform.BrowserCookie

	context, view, cancellable, store uintptr
	filterDir                         string
	connections                       []connection
	tokens                            []uintptr // signal routes, dropped at release

	nav      navigations
	failure  struct{ nonce, uri, method string }
	requests map[platform.ID]*nativeRequest
	asyncs   int // native operations whose callback is owed
	copies   int // response copies native code still holds

	filterReady, cookieReady, navigated bool
	closing, destroying, destroyed      bool
	released                            bool

	mu    sync.Mutex
	stats Audit
}

const eventMemory = 64

func (b *browser) event(e Event, count func(*Audit)) {
	b.mu.Lock()
	defer b.mu.Unlock()
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
	reads := 0
	for _, r := range b.requests {
		if r.reading {
			reads++
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.Navigated, b.stats.Closing, b.stats.Destroyed, b.stats.Released = b.navigated, b.closing, b.destroyed, b.released
	b.stats.Requests, b.stats.Reads, b.stats.Copies, b.stats.Asyncs = len(b.requests), reads, b.copies, b.asyncs
}

func (f Factory) New(parent platform.BrowserParent, config platform.BrowserConfig, host platform.BrowserHost) (platform.Browser, error) {
	if err := load(); err != nil {
		return nil, err
	}
	installCallbacks()
	// Before the first WebKit type: that is where JavaScriptCore initializes.
	if err := reserve(config.GCSignal); err != nil {
		return nil, err
	}
	return newBrowser(parent, config, host)
}

// newBrowser creates the attachment once libraries are bound and the GC
// signal is configured.
func newBrowser(parent platform.BrowserParent, config platform.BrowserConfig, host platform.BrowserHost) (*browser, error) {
	b := &browser{host: host, devtool: config.DevTools, parent: parent.Handle, mode: config.Mode, requests: make(map[platform.ID]*nativeRequest),
		metadata: int64(config.RequestMetadata)}
	b.stats.Runtime, b.stats.Signal = version(), reservedSignal()
	switch config.Mode {
	case platform.BrowserInProcess:
		b.start, b.allowed, b.owned = Origin+"/", []string{Origin}, Origin
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
		return nil, errors.New("browser parent has no native container")
	}
	b.stats.Start = b.start
	// An ephemeral context per window: its own cookies, storage, cache and
	// network process, gone when the context is released.
	b.context = w.contextNewEphemeral()
	if b.context == 0 {
		return nil, errors.New("webkit_web_context_new_ephemeral failed")
	}
	if b.mode == platform.BrowserInProcess {
		security := w.securityManager(b.context)
		w.schemeAsSecure(security, scheme)
		w.schemeAsCORS(security, scheme)
		w.registerScheme(b.context, scheme, callbacks.two, b.route(kindScheme), 0)
	}
	b.connect(b.context, "download-started", callbacks.three, kindDownload)
	b.view = w.viewNew(b.context)
	if b.view == 0 {
		b.abandon()
		return nil, errors.New("webkit_web_view_new_with_context failed")
	}
	w.refSink(b.view)
	settings := w.viewSettings(b.view)
	w.setDeveloperExtras(settings, boolean(b.devtool))
	w.setOpenWindows(settings, 0)
	for _, s := range []struct {
		name     string
		callback uintptr
		kind     int
	}{
		{"decide-policy", callbacks.four, kindPolicy},
		{"load-failed", callbacks.five, kindLoadFailed},
		{"create", callbacks.three, kindCreate},
		{"permission-request", callbacks.three, kindPermission},
		{"web-process-terminated", callbacks.three, kindTerminated},
	} {
		if !b.connect(b.view, s.name, s.callback, s.kind) {
			b.abandon()
			return nil, fmt.Errorf("WebKitGTK could not connect %s", s.name)
		}
	}
	b.cancellable = w.cancellableNew()
	w.containerAdd(b.parent, b.view)
	w.widgetShow(b.view)
	b.sync()
	return b, nil
}

func boolean(v bool) int32 {
	if v {
		return 1
	}
	return 0
}

// route registers a signal token, released with the attachment.
func (b *browser) route(kind int) uintptr {
	token := register(&target{b: b, kind: kind})
	b.tokens = append(b.tokens, token)
	return token
}

func (b *browser) connect(object uintptr, name string, callback uintptr, kind int) bool {
	id := w.connect(object, name, callback, b.route(kind), 0, 0)
	if id == 0 {
		return false
	}
	b.connections = append(b.connections, connection{object, id})
	return true
}

// abandon releases a partial attachment synchronously. No asynchronous work
// has started, so nothing is owed.
func (b *browser) abandon() {
	for _, c := range b.connections {
		w.disconnect(c.object, c.id)
	}
	if b.view != 0 { // already sunk
		w.widgetDestroy(b.view)
		w.unref(b.view)
	}
	w.unref(b.context)
	for _, t := range b.tokens {
		drop(t)
	}
}

func (b *browser) Origin() string {
	if b.mode == platform.BrowserInProcess {
		return Origin
	}
	return ""
}

// async starts tracking a native operation whose callback is owed.
func (b *browser) async(f func(source, result uintptr)) uintptr {
	b.asyncs++
	return register(&target{b: b, kind: kindAsync, async: f})
}

func (b *browser) completed(f func(source, result uintptr), source, result uintptr) {
	b.asyncs--
	f(source, result)
	b.sync()
	b.maybeDestroy()
}

// Start installs the content filter and, for loopback, the credential; the
// first navigation waits for both.
func (b *browser) Start() {
	dir, err := os.MkdirTemp("", "dfx2-webview-filter-")
	if err != nil {
		b.host.Failed(fmt.Errorf("content filter storage: %w", err))
		return
	}
	b.filterDir = dir
	b.store = w.filterStoreNew(dir)
	rules := []byte(filterRules(b.owned))
	bytes := w.bytesNew(unsafe.Pointer(&rules[0]), uintptr(len(rules))) // copied
	token := b.async(func(store, result uintptr) {
		var e uintptr
		filter := w.filterStoreFinish(store, result, &e)
		domain, code, message := gerror(e)
		switch {
		case filter != 0 && b.closing:
			w.filterUnref(filter) // saved anyway: released, never installed
		case filter == 0 && b.closing && canceledByClose(domain, code):
		case filter == 0:
			b.host.Failed(fmt.Errorf("content filter: %s %d: %s", domain, code, message))
		default:
			w.addFilter(w.viewContentManager(b.view), filter)
			w.filterUnref(filter)
			b.filterReady = true
			b.maybeNavigate()
		}
	})
	w.filterStoreSave(b.store, "window", bytes, b.cancellable, callbacks.three, token)
	w.bytesUnref(bytes)
	if b.cookie == nil {
		b.cookieReady = true
		return
	}
	host := origin.Host(b.cookie.Origin)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	cookie := w.cookieNew(b.cookie.Name, b.cookie.Value, host, "/", -1) // a session cookie
	w.cookieSetHTTPOnly(cookie, 1)
	w.cookieSetSameSite(cookie, sameSiteStrict)
	manager := w.cookieManager(w.dataManager(b.context))
	token = b.async(func(source, result uintptr) {
		var e uintptr
		ok := w.addCookieFinish(source, result, &e) != 0
		domain, code, message := gerror(e)
		switch {
		case ok && b.closing:
		case !ok && b.closing && canceledByClose(domain, code):
		case !ok:
			b.host.Failed(fmt.Errorf("endpoint credential: %s %d: %s", domain, code, message))
		default:
			b.cookieReady = true
			b.maybeNavigate()
		}
	})
	w.addCookie(manager, cookie, b.cancellable, callbacks.three, token)
	w.cookieFree(cookie) // add_cookie copies it
}

// canceledByClose is GLib's own cancellation, which Close causes by
// canceling the window's operations. Only that is silent while closing; any
// other failure that arrives then is still an initialization failure.
func canceledByClose(domain string, code int32) bool {
	return domain == "g-io-error-quark" && code == ioErrorCancelled
}

func (b *browser) maybeNavigate() {
	if b.closing || b.navigated || !b.filterReady || !b.cookieReady {
		return
	}
	// False means the window is closing; Close follows.
	if !b.host.Ready() {
		return
	}
	b.navigated = true
	w.loadURI(b.view, b.start)
}

// Bounds needs nothing: the view fills its GTK container.
func (b *browser) Bounds(platform.Surface) {}

func (b *browser) Diagnostics() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	return fmt.Sprintf("webkitgtk %s signal=%d navigated=%t closing=%t destroyed=%t released=%t native-requests=%d reads=%d response-copies=%d async=%d",
		s.Runtime, s.Signal, s.Navigated, s.Closing, s.Destroyed, s.Released, s.Requests, s.Reads, s.Copies, s.Asyncs)
}

// Close cancels native operations and requests, then destroys the view on a
// later main-loop turn once every owed callback has run: a GTask releases
// its source object only after its callback returns, and destroying the
// context inside one crashed WebKit 2.52.6 later.
func (b *browser) Close() {
	if b.closing {
		return
	}
	b.closing = true
	b.failure.nonce = ""
	w.cancellableCancel(b.cancellable)
	w.stopLoading(b.view)
	for id, r := range b.requests {
		if !r.reading { // a reading request finishes when its canceled read returns
			b.finishError(id, r, "browser window closed")
		}
	}
	b.sync()
	b.maybeDestroy()
}

func (b *browser) maybeDestroy() {
	if !b.closing || b.asyncs != 0 || b.destroying {
		return
	}
	b.destroying = true
	w.idleAdd(priorityDefaultIdle, callbacks.one, register(&target{b: b, kind: kindDestroy}), 0)
}

func (b *browser) destroy() {
	for _, c := range b.connections {
		w.disconnect(c.object, c.id)
	}
	b.connections = nil
	w.widgetDestroy(b.view) // leaves the parent container
	w.unref(b.view)
	if b.store != 0 {
		w.unref(b.store)
	}
	w.unref(b.context)
	w.unref(b.cancellable)
	b.destroyed = true
	if dir := b.filterDir; dir != "" {
		// Off the UI thread, after native release, with bounded attempts.
		if err := b.host.Cleanup("content filter "+dir, func() error { return removeAfterRelease(dir) }); err != nil {
			b.host.Failed(err)
		}
	}
	b.sync()
	b.maybeRelease()
}

// maybeRelease reports Released once the view is destroyed and native code
// holds no request or response copy.
func (b *browser) maybeRelease() {
	if !b.destroyed || b.released || len(b.requests) != 0 || b.copies != 0 {
		return
	}
	b.released = true
	for _, t := range b.tokens {
		drop(t)
	}
	b.tokens = nil
	b.sync()
	b.host.Released()
}

// receive admits one in-process request, on the UI thread. Its strings are
// views of WebKit's storage, which the request object keeps alive until the
// synchronous host.Request returns; the endpoint copies what it retains.
// The metadata is measured first, natively and without allocating, stopping
// once it exceeds the endpoint's limit; only a request within the limit gets
// a header slice, so the slice is bounded by the limit and covered by the
// endpoint's transfer reserve while it exists.
func (b *browser) receive(request uintptr) {
	if b.closing {
		refuseOrphan(request)
		return
	}
	in := platform.BrowserRequest{Method: view(w.schemeMethod(request)), URL: view(w.schemeURI(request))}
	if in.Method == "" {
		in.Method = "GET"
	}
	size := int64(platform.RequestRecordOverhead + len(in.Method) + len(in.URL))
	headers := w.schemeHeaders(request)
	var iter [3]uintptr
	var name, value uintptr
	count := 0
	if headers != 0 {
		w.headersIterInit(&iter, headers)
		for size <= b.metadata && w.headersIterNext(&iter, &name, &value) != 0 {
			size += int64(len(view(name)) + len(view(value)) + platform.RequestHeaderOverhead)
			count++
		}
	}
	in.MetadataSize = size
	length := int64(-1)
	if headers != 0 && size <= b.metadata {
		in.Header = make([]platform.Header, 0, count)
		w.headersIterInit(&iter, headers)
		for len(in.Header) < count && w.headersIterNext(&iter, &name, &value) != 0 {
			h := platform.Header{Name: view(name), Value: view(value)}
			if strings.EqualFold(h.Name, "Content-Length") {
				if n, err := strconv.ParseInt(strings.TrimSpace(h.Value), 10, 64); err == nil && n >= 0 {
					length = n
				}
			}
			in.Header = append(in.Header, h)
		}
	}
	stream := w.schemeBody(request) // transfer full; may be NULL
	switch {
	case stream == 0:
		in.BodyLength = 0
	case length == 0:
		in.BodyLength = 0
	default:
		in.BodyLength = length
	}
	r := &nativeRequest{native: w.ref(request), stream: stream}
	d := b.host.Request(in)
	b.act(d, r)
}

// act carries out the endpoint's decision for a native request.
func (b *browser) act(d platform.RequestDecision, r *nativeRequest) {
	switch d.Action {
	case platform.RequestRead:
		if r.stream == 0 {
			b.requests[d.ID] = r
			b.act(b.host.RequestBodyEnd(d.ID, nil), r)
			return
		}
		r.reading = true
		b.requests[d.ID] = r
		b.read(d.ID, r, d.ReadChunk)
	case platform.RequestWait:
		r.reading = false
		b.closeStream(r)
		b.requests[d.ID] = r
	case platform.RequestRespond:
		r.reading = false
		b.closeStream(r)
		b.requests[d.ID] = r
		b.deliver(d.ID, r, d.Response)
	default:
		r.reading = false
		if d.ID == 0 {
			b.closeStream(r)
			failNative(r.native, "request refused")
			w.unref(r.native)
		} else {
			b.requests[d.ID] = r
			b.finishError(d.ID, r, "request refused")
		}
	}
	b.sync()
}

func (b *browser) closeStream(r *nativeRequest) {
	if r.stream != 0 {
		w.unref(r.stream)
		r.stream = 0
	}
}

// finishError fails a native request without a response and releases it.
func (b *browser) finishError(id platform.ID, r *nativeRequest, message string) {
	b.closeStream(r)
	failNative(r.native, message)
	w.unref(r.native)
	delete(b.requests, id)
	b.host.RequestReleased(id)
	b.maybeRelease()
}

// read requests the next body chunk asynchronously; the UI thread never
// waits on a stream the browser supplies.
func (b *browser) read(id platform.ID, r *nativeRequest, chunk int) {
	token := b.async(func(stream, result uintptr) {
		var e uintptr
		bytes := w.readBytesFinish(stream, result, &e)
		_, _, message := gerror(e)
		var data []byte
		if bytes != 0 {
			var size uintptr
			if p := w.bytesData(bytes, &size); p != nil && size != 0 {
				data = unsafe.Slice((*byte)(p), int(size)) // borrowed for the host call
			}
		}
		var d platform.RequestDecision
		switch {
		case b.closing:
			if bytes != 0 {
				w.bytesUnref(bytes)
			}
			b.finishError(id, r, "browser window closed")
			return
		case e != 0:
			d = b.host.RequestBodyEnd(id, errors.New(message))
		case len(data) == 0:
			d = b.host.RequestBodyEnd(id, nil)
		default:
			d = b.host.RequestBody(id, data)
		}
		if bytes != 0 {
			w.bytesUnref(bytes)
		}
		if d.Action == platform.RequestRead {
			b.read(id, r, d.ReadChunk)
			return
		}
		// The endpoint's refusals carry no ID; this request was admitted under
		// id, which still owes exactly one release.
		if d.ID == 0 {
			d.ID = id
		}
		delete(b.requests, id)
		b.act(d, r)
	})
	w.readBytesAsync(r.stream, uintptr(chunk), 0, b.cancellable, callbacks.three, token)
}

// Respond finishes a waiting request with a complete response.
func (b *browser) Respond(id platform.ID, resp platform.BrowserResponse) bool {
	r := b.requests[id]
	if r == nil || r.reading || b.closing {
		return false
	}
	b.deliver(id, r, resp)
	b.sync()
	return true
}

// deliver copies the response into g_malloc storage owned by a GBytes whose
// release, wherever WebKit drops it, reports the request released.
func (b *browser) deliver(id platform.ID, r *nativeRequest, resp platform.BrowserResponse) {
	size := len(resp.Body)
	data := w.malloc(uintptr(max(size, 1)))
	if size > 0 {
		copy(unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), data)), size), resp.Body)
	}
	token := register(&target{b: b, kind: kindBytesFreed, id: id, data: data})
	bytes := w.bytesNewWithFree(data, uintptr(size), callbacks.one, token)
	stream := w.memoryStream(bytes)
	w.bytesUnref(bytes)
	response := w.responseNew(stream, int64(size))
	w.unref(stream)
	w.responseStatus(response, uint32(resp.Status), 0)
	headers := w.headersNew(headersResponse)
	contentType := ""
	for _, h := range resp.Header {
		w.headersAppend(headers, h.Name, h.Value)
		if strings.EqualFold(h.Name, "Content-Type") {
			contentType = h.Value
		}
	}
	w.responseHeaders(response, headers) // transfer full
	if contentType != "" {
		w.responseContentType(response, contentType)
	}
	b.copies++
	delete(b.requests, id)
	w.schemeFinish(r.native, response)
	w.unref(response)
	w.unref(r.native)
	b.mu.Lock()
	b.stats.Delivered++
	b.mu.Unlock()
}

// responseFreed runs on the UI thread after WebKit dropped a response copy.
func (b *browser) responseFreed(id platform.ID) {
	b.copies--
	b.host.RequestReleased(id)
	b.sync()
	b.maybeRelease()
}

// policy decides navigations, new windows and responses.
func (b *browser) policy(decision uintptr, kind int32) uintptr {
	switch kind {
	case policyNewWindow:
		w.policyIgnore(decision)
		b.event(Event{Kind: "popup-refused"}, func(a *Audit) { a.Popups++ })
		return 1
	case policyResponse:
		// A response the view cannot display would become a download.
		if w.mimeSupported(decision) == 0 {
			w.policyIgnore(decision)
			b.event(Event{Kind: "response-refused"}, func(a *Audit) { a.Downloads++ })
			return 1
		}
		return 0
	case policyNavigation:
	default:
		return 0
	}
	action := w.navigationAction(decision)
	request := w.actionRequest(action)
	uri := w.requestURI(request)
	if strings.HasPrefix(uri, errorScheme) {
		w.policyIgnore(decision)
		b.errorAction(uri)
		return 1
	}
	method := navigationMethod(w.requestMethod(request), w.actionType(action))
	if b.closing || !allowed(uri, b.allowed) {
		w.policyIgnore(decision)
		b.event(Event{Kind: "navigation-refused", URI: uri, Method: method}, func(a *Audit) { a.Refused++ })
		return 1
	}
	b.nav.record(uri, method)
	// Alternate HTML for an unreachable URL bypasses navigation policy in
	// WebKit. Every allowed navigation here retires the previous error page,
	// even when it requests the same URI again.
	b.failure.nonce = ""
	b.event(Event{Kind: "navigation-allowed", URI: uri, Method: method, Detail: map[bool]string{true: "redirect"}[w.actionIsRedirect(action) != 0]},
		func(a *Audit) { a.Allowed++ })
	return 0
}

// loadFailed replaces a failed top-level load with the framework's error
// page. Refused navigations and cancellations are not load failures.
func (b *browser) loadFailed(uri string, gerr uintptr) uintptr {
	domain, code, message := borrowError(gerr)
	if b.closing || domain == "WebKitPolicyError" || (domain == "WebKitNetworkError" && code == networkCancelled) {
		b.event(Event{Kind: "load-stopped", URI: uri, Detail: fmt.Sprintf("%s %d", domain, code)}, nil)
		return 0
	}
	method := b.nav.method(uri)
	b.failure.nonce, b.failure.uri, b.failure.method = nonce(), uri, method
	w.loadAlternateHTML(b.view, errorPage(uri, method, b.failure.nonce), uri, 0)
	b.event(Event{Kind: "error-page", URI: uri, Method: method, Retry: retryable(method), Detail: fmt.Sprintf("%s %d: %s", domain, code, message)},
		func(a *Audit) { a.ErrorPages++ })
	return 1
}

// errorAction carries out Retry or Return to start for the current failure
// only. Retry repeats a known GET; Return loads the start page with GET. Both
// go through navigation policy; neither resubmits a form.
func (b *browser) errorAction(uri string) {
	action, n, ok := errorAction(uri)
	if !ok || b.failure.nonce == "" || n != b.failure.nonce || b.closing {
		b.event(Event{Kind: "error-action-refused", Detail: "stale or unknown action"}, nil)
		return
	}
	target := b.start
	if action == "retry" {
		if !retryable(b.failure.method) {
			b.event(Event{Kind: "error-action-refused", Detail: "retry withheld: method " + b.failure.method}, nil)
			return
		}
		target = b.failure.uri
	}
	b.failure.nonce = "" // one action per failure
	b.event(Event{Kind: "error-action", URI: target, Detail: action}, func(a *Audit) { a.Actions++ })
	w.loadURI(b.view, target)
}

func (b *browser) popup() { b.event(Event{Kind: "popup-refused"}, func(a *Audit) { a.Popups++ }) }

func (b *browser) permission(request uintptr) {
	w.permissionDeny(request)
	b.event(Event{Kind: "permission-denied"}, func(a *Audit) { a.Permissions++ })
}

func (b *browser) download(download uintptr) {
	w.downloadCancel(download)
	b.event(Event{Kind: "download-canceled"}, func(a *Audit) { a.Downloads++ })
}

// terminated is always a browser-process failure, closing or not. The host
// never terminates a web process itself, and destroy disconnects this signal
// before native destruction, so ordinary teardown cannot raise it.
func (b *browser) terminated(reason int32) {
	b.event(Event{Kind: "web-process-terminated", Detail: strconv.Itoa(int(reason))}, nil)
	b.host.Failed(fmt.Errorf("WebKit web process terminated (reason %d)", reason))
}

// AuditionLoad navigates as a user would, through navigation policy. UI
// thread; internal auditions only.
func (b *browser) AuditionLoad(uri string) error {
	if b.closing || !b.navigated {
		return errors.New("browser closing or still initializing")
	}
	w.loadURI(b.view, uri)
	return nil
}

// AuditionErrorAction follows the current error page's action link, through
// navigation policy, as a click would. UI thread; internal auditions only.
func (b *browser) AuditionErrorAction(action string) error {
	if b.closing || b.failure.nonce == "" {
		return errors.New("no error page is showing")
	}
	w.loadURI(b.view, errorScheme+action+"/"+b.failure.nonce)
	return nil
}

// AuditionURI reports the view's current URI and title. UI thread.
func (b *browser) AuditionURI() (uri, title string) {
	if b.destroyed {
		return "", ""
	}
	return w.viewURI(b.view), w.viewTitle(b.view)
}

// AuditionState is safe from any goroutine.
func (b *browser) AuditionState() Audit {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	s.Events = append([]Event(nil), s.Events...)
	return s
}
