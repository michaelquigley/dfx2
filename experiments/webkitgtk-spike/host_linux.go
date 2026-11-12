//go:build linux

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"dfx2webview2spike/adapt"
	"github.com/ebitengine/purego"
)

func init() { runtime.LockOSThread() }

// Permanent callbacks, shared by signature. All callbacks except idle scheduling
// run on the GTK owner thread. Numeric tokens never point into the Go heap.
var callbacks struct{ one, two, three, four uintptr }
var active *host

const (
	tokenScheme  = 1
	tokenDelete  = 2
	tokenLoad    = 3
	tokenProcess = 4
	tokenCreate  = 5
)

func installCallbacks() {
	callbacks.one = purego.NewCallback(func(data uintptr) uintptr { active.drain(); return 0 })
	callbacks.two = purego.NewCallback(func(object, data uintptr) uintptr {
		if data == tokenScheme {
			active.receive(object)
		}
		return 0
	})
	callbacks.three = purego.NewCallback(func(object, arg, data uintptr) uintptr {
		h := active
		switch data {
		case tokenDelete:
			h.close("window close")
			return 1
		case tokenLoad:
			log.Printf("load event=%d uri=%s", arg, n.webkitWebViewGetURI(object))
			if arg == 3 && !h.closing {
				if h.c.exerciseExit && !h.exercising {
					h.exercising = true
					h.exerciseExit()
				}
				h.evaluate(`JSON.stringify({origin:location.origin,secure:isSecureContext,crypto:!!crypto.subtle,storage:(()=>{try{sessionStorage.setItem('probe','yes');return sessionStorage.getItem('probe')}catch(e){return String(e)}})()})`)
			}
		case tokenProcess:
			h.err = fmt.Errorf("web process terminated, reason %d", arg)
			h.close("web process terminated")
		case tokenCreate:
			log.Print("new window refused (create)")
			return 0
		default:
			if f := h.async[data]; f != nil {
				delete(h.async, data)
				f(object, arg)
				h.maybeQuit()
			}
		}
		return 0
	})
	callbacks.four = purego.NewCallback(func(view, decision, kind, data uintptr) uintptr {
		if kind > 1 {
			return 0
		}
		action := n.webkitNavigationPolicyDecisionGetNavigationAction(decision)
		uri := n.webkitURIRequestGetURI(n.webkitNavigationActionGetRequest(action))
		_, originErr := adapt.CheckOrigin(uri, active.origin)
		if kind == 1 || originErr != nil {
			log.Printf("navigation refused kind=%d uri=%s", kind, uri)
			n.webkitPolicyDecisionIgnore(decision)
			return 1
		}
		return 0
	})
}

// g_idle_add_full always queues onto the main context. g_main_context_invoke
// is unsuitable: it may execute synchronously on the calling worker.
type poster struct {
	mu       sync.Mutex
	closed   bool
	source   uint32
	queue    []func()
	callback uintptr // the idle trampoline; callbacks.one when zero
}

func (p *poster) post(f func()) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	if f != nil {
		p.queue = append(p.queue, f)
	}
	if p.source == 0 {
		cb := p.callback
		if cb == 0 {
			cb = callbacks.one
		}
		p.source = n.gIdleAddFull(0, cb, 0, 0)
	}
	return p.source != 0
}
func (p *poster) take() []func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.source = 0
	q := p.queue
	p.queue = nil
	return q
}
func (p *poster) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.queue = nil
	if p.source != 0 {
		n.gSourceRemove(p.source)
		p.source = 0
	}
}

type request struct {
	native, stream uintptr
	input          adapt.Request
}
type host struct {
	exercising                        bool
	c                                 config
	ctx                               context.Context
	cancel                            context.CancelFunc
	window, view, webctx, cancellable uintptr
	origin                            *url.URL
	dispatcher                        *adapt.Dispatcher
	listener                          net.Listener
	server                            *http.Server
	served                            chan error
	post                              poster
	requests                          map[uintptr]*request
	pending                           map[uint64]*request
	async                             map[uintptr]func(uintptr, uintptr)
	nextAsync                         uintptr
	closing, destroyed                bool
	err                               error
	busyOnce                          sync.Once
}

func runHost(c config) error {
	if err := loadNative(); err != nil {
		return err
	}
	if n.gtkInitCheck(0, 0) == 0 {
		return fmt.Errorf("GTK could not open the display")
	}
	installCallbacks()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &host{c: c, ctx: ctx, cancel: cancel, requests: map[uintptr]*request{}, pending: map[uint64]*request{}, async: map[uintptr]func(uintptr, uintptr){}, nextAsync: 1000}
	active = h
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	go func() {
		select {
		case <-ctx.Done():
		case sig := <-interrupts:
			h.post.post(func() { h.close("signal " + sig.String()) })
		}
	}()
	base := fixtureHandler(log.Printf)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/slow" && c.closeBusy > 0 {
			h.busyOnce.Do(func() { h.after(c.closeBusy, func() { h.close("close while busy") }) })
		}
		base.ServeHTTP(w, r)
	})
	rawOrigin := inprocessOrigin
	if c.mode == "loopback" {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return err
		}
		h.listener = ln
		rawOrigin = "http://" + ln.Addr().String()
		h.server = &http.Server{Handler: adapt.Instrument(handler, log.Printf), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second}
		h.served = make(chan error, 1)
	}
	h.origin, _ = url.Parse(rawOrigin)
	h.dispatcher = adapt.NewDispatcher(ctx, handler, h.origin, func() bool { return h.post.post(nil) }, log.Printf)
	h.cancellable = n.gCancellableNew()
	h.webctx = n.webkitWebContextNewEphemeral()
	if c.mode == "inprocess" {
		security := n.webkitWebContextGetSecurityManager(h.webctx)
		n.webkitSecurityManagerRegisterURISchemeAsSecure(security, "dfx2")
		n.webkitSecurityManagerRegisterURISchemeAsCorsEnabled(security, "dfx2")
		n.webkitWebContextRegisterURIScheme(h.webctx, "dfx2", callbacks.two, tokenScheme, 0)
	}
	h.window = n.gtkWindowNew(0)
	n.gtkWindowSetTitle(h.window, fmt.Sprintf("dfx2 WebKitGTK %d.%d.%d — %s", n.webkitGetMajorVersion(), n.webkitGetMinorVersion(), n.webkitGetMicroVersion(), c.mode))
	n.gtkWindowSetDefaultSize(h.window, int32(c.width), int32(c.height))
	h.view = n.webkitWebViewNewWithContext(h.webctx)
	settings := n.webkitWebViewGetSettings(h.view)
	n.webkitSettingsSetJavascriptCanOpenWindowsAutomatically(settings, 1)
	if c.devtools {
		n.webkitSettingsSetEnableDeveloperExtras(settings, 1)
	}
	n.gtkContainerAdd(h.window, h.view)
	n.gSignalConnectData(h.window, "delete-event", callbacks.three, tokenDelete, 0, 0)
	n.gSignalConnectData(h.view, "load-changed", callbacks.three, tokenLoad, 0, 0)
	n.gSignalConnectData(h.view, "web-process-terminated", callbacks.three, tokenProcess, 0, 0)
	n.gSignalConnectData(h.view, "create", callbacks.three, tokenCreate, 0, 0)
	n.gSignalConnectData(h.view, "decide-policy", callbacks.four, 0, 0, 0)
	n.gtkWidgetShowAll(h.window)
	n.gtkWidgetGrabFocus(h.view)
	log.Printf("WebKitGTK %d.%d.%d mode=%s origin=%s scale=%d", n.webkitGetMajorVersion(), n.webkitGetMinorVersion(), n.webkitGetMicroVersion(), c.mode, rawOrigin, n.gtkWidgetGetScaleFactor(h.window))
	if h.server != nil {
		go func() {
			err := h.server.Serve(h.listener)
			h.served <- err
			if err != http.ErrServerClosed {
				h.post.post(func() { h.err = fmt.Errorf("loopback server: %w", err); h.close("server failure") })
			}
		}()
	}
	uri := rawOrigin + "/"
	if c.autorun != "" {
		uri += "?autorun=" + url.QueryEscape(c.autorun)
	}
	n.webkitWebViewLoadURI(h.view, uri)
	if c.devtools {
		inspector := n.webkitWebViewGetInspector(h.view)
		n.webkitWebInspectorShow(inspector)
		if c.dockDevtools {
			h.after(1500*time.Millisecond, func() {
				n.webkitWebInspectorAttach(inspector)
				log.Printf("inspector attached=%t", n.webkitWebInspectorIsAttached(inspector) != 0)
			})
		}
	}
	h.after(c.quitAfter, func() {
		if c.snapshot != "" {
			h.snapshot()
		} else {
			if c.windowClose {
				n.gtkWindowClose(h.window)
			} else {
				h.close("timed exit")
			}
		}
	})
	if c.exerciseExit && c.quitAfter > time.Second {
		h.after(c.quitAfter-time.Second, func() { h.evaluate(`slow('exit'); 'slow before exit'`) })
	}
	h.after(c.breakListener, func() { log.Print("audition: breaking loopback listener"); h.listener.Close() })
	h.after(c.resizeAfter, func() {
		log.Print("audition: resize")
		n.gtkWindowResize(h.window, 850, 640)
		h.after(500*time.Millisecond, func() {
			log.Print("audition: minimize")
			n.gtkWindowIconify(h.window)
			h.after(500*time.Millisecond, func() { log.Print("audition: restore"); n.gtkWindowDeiconify(h.window) })
		})
	})
	n.gtkMain()
	if !h.dispatcher.Wait(2 * time.Second) {
		h.err = fmt.Errorf("handlers did not drain within 2 seconds")
	}
	if h.server != nil {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		if err := h.server.Shutdown(shutdownCtx); err != nil {
			h.server.Close()
			h.err = err
		}
		stop()
		<-h.served
	}
	n.gObjectUnref(h.cancellable)
	n.gObjectUnref(h.webctx)
	log.Printf("closed: requests=%d native-async=%d handlers=%d stats=%+v", len(h.requests), len(h.async), h.dispatcher.Outstanding(), h.dispatcher.Stats())
	active = nil
	return h.err
}

func (h *host) after(d time.Duration, f func()) {
	if d <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-h.ctx.Done():
		case <-timer.C:
			h.post.post(f)
		}
	}()
}
func (h *host) asyncToken(f func(uintptr, uintptr)) uintptr {
	h.nextAsync++
	h.async[h.nextAsync] = f
	return h.nextAsync
}
func (h *host) drain() {
	for _, f := range h.post.take() {
		if !h.closing {
			f()
		}
	}
	if h.closing {
		return
	}
	for _, r := range h.dispatcher.Take() {
		if req := h.pending[r.ID]; req != nil {
			delete(h.pending, r.ID)
			h.finish(req, r.Response)
		}
	}
}
func (h *host) receive(p uintptr) {
	req := &request{native: n.gObjectRef(p), input: adapt.Request{URL: n.webkitURISchemeRequestGetURI(p), Method: n.webkitURISchemeRequestGetHTTPMethod(p)}}
	h.requests[p] = req
	if h.closing {
		h.finish(req, closedResponse())
		return
	}
	var iter [3]uintptr
	headers := n.webkitURISchemeRequestGetHTTPHeaders(p)
	if headers != 0 {
		n.soupMessageHeadersIterInit(&iter, headers)
		var name, value uintptr
		for n.soupMessageHeadersIterNext(&iter, &name, &value) != 0 {
			req.input.Header = append(req.input.Header, [2]string{cstring(name), cstring(value)})
		}
	}
	req.stream = n.webkitURISchemeRequestGetHTTPBody(p) // transfer full
	if req.stream != 0 {
		h.read(req)
		if h.c.closeReading {
			h.close("close during native body read")
		}
	} else {
		h.dispatch(req)
	}
}
func (h *host) read(req *request) {
	token := h.asyncToken(func(stream, result uintptr) {
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
		if h.closing || err != nil || size == 0 || len(req.input.Body) > adapt.Limit {
			n.gObjectUnref(req.stream)
			req.stream = 0
			if h.closing {
				h.finish(req, closedResponse())
				return
			}
			if err != nil {
				log.Printf("body read: %v", err)
				h.finish(req, adapt.Response{Status: 400, Body: []byte("body read failed\n")})
				return
			}
			req.input.BodyTooLarge = len(req.input.Body) > adapt.Limit
			h.dispatch(req)
			return
		}
		h.read(req)
	})
	n.gInputStreamReadBytesAsync(req.stream, 64*1024, 0, h.cancellable, callbacks.three, token)
}
func (h *host) dispatch(req *request) {
	if id, ok := h.dispatcher.Start(req.input); ok {
		h.pending[id] = req
	} else {
		h.finish(req, closedResponse())
	}
}
func closedResponse() adapt.Response {
	return adapt.Response{Status: 503, Body: []byte("window closing\n")}
}
func (h *host) finish(req *request, r adapt.Response) {
	var p unsafe.Pointer
	if len(r.Body) > 0 {
		p = unsafe.Pointer(&r.Body[0])
	}
	bytes := n.gBytesNew(p, uintptr(len(r.Body))) // copies; native never retains Go memory
	stream := n.gMemoryInputStreamNewFromBytes(bytes)
	n.gBytesUnref(bytes)
	response := n.webkitURISchemeResponseNew(stream, int64(len(r.Body)))
	n.gObjectUnref(stream)
	reason := r.Reason
	if reason == "" {
		reason = http.StatusText(r.Status)
	}
	n.webkitURISchemeResponseSetStatus(response, uint32(r.Status), reason)
	headers := n.soupMessageHeadersNew(1) // SOUP_MESSAGE_HEADERS_RESPONSE
	for name, values := range r.Header {
		for _, value := range values {
			n.soupMessageHeadersAppend(headers, name, value)
		}
	}
	n.webkitURISchemeResponseSetHTTPHeaders(response, headers) // transfer full
	if ct := r.Header.Get("Content-Type"); ct != "" {
		n.webkitURISchemeResponseSetContentType(response, ct)
	}
	n.webkitURISchemeRequestFinishWithResponse(req.native, response)
	n.gObjectUnref(response)
	n.gObjectUnref(req.native)
	delete(h.requests, req.native)
}
func (h *host) close(why string) {
	if h.closing {
		return
	}
	h.closing = true
	log.Printf("closing: %s; native requests=%d async=%d handlers=%d", why, len(h.requests), len(h.async), h.dispatcher.Outstanding())
	h.cancel()
	h.dispatcher.Close()
	h.post.close()
	n.gCancellableCancel(h.cancellable)
	n.webkitWebViewStopLoading(h.view)
	if h.c.closeInspector {
		log.Print("audition: explicitly closing inspector")
		n.webkitWebInspectorClose(n.webkitWebViewGetInspector(h.view))
	}
	n.gtkWidgetHide(h.window)
	for id, req := range h.pending {
		h.finish(req, closedResponse())
		delete(h.pending, id)
	}
	h.maybeQuit()
}
func (h *host) maybeQuit() {
	if h.closing && len(h.async) == 0 && !h.destroyed {
		h.destroyed = true
		n.gtkWidgetDestroy(h.window)
		n.gtkMainQuit()
	}
}
func (h *host) evaluate(script string) {
	token := h.asyncToken(func(view, result uintptr) {
		var e uintptr
		v := n.webkitWebViewEvaluateJavascriptFinish(view, result, &e)
		if err := nativeError(e); err != nil {
			log.Printf("javascript: %v", err)
		}
		if v != 0 {
			s := n.jscValueToString(v)
			log.Printf("javascript: %s", cstring(s))
			n.gFree(s)
			n.gObjectUnref(v)
		}
	})
	n.webkitWebViewEvaluateJavascript(h.view, script, -1, 0, 0, h.cancellable, callbacks.three, token)
}
func (h *host) snapshot() {
	token := h.asyncToken(func(view, result uintptr) {
		var e uintptr
		surface := n.webkitWebViewGetSnapshotFinish(view, result, &e)
		if err := nativeError(e); err != nil {
			h.err = err
		}
		if surface != 0 {
			status := n.cairoSurfaceWriteToPNG(surface, h.c.snapshot)
			n.cairoSurfaceDestroy(surface)
			if status != 0 {
				h.err = fmt.Errorf("snapshot PNG status %d", status)
			} else {
				log.Printf("snapshot: %s", h.c.snapshot)
			}
		}
		h.close("snapshot and timed exit")
	})
	n.webkitWebViewGetSnapshot(h.view, 0, 0, h.cancellable, callbacks.three, token)
}

// Diagnostic workload only. Timers and scripts stop with the window lifetime.
func (h *host) exerciseExit() {
	h.evaluate(`(async()=>{
 for(let i=0;i<12;i++) {
  await runAll();
  const inputs=document.querySelectorAll('input,textarea');
  for(const input of inputs){input.focus();input.value='exit investigation '+i;}
  window.scrollTo(0,document.body.scrollHeight);await sleep(250);
  window.scrollTo(0,0);await sleep(250);
 }
 report('exit exercise completed');
 })(); 'exit exercise started'`)
	var resize func(int)
	resize = func(i int) {
		if i >= 24 {
			return
		}
		h.after(time.Second, func() {
			n.gtkWindowResize(h.window, int32(850+(i%3)*100), int32(640+(i%2)*150))
			resize(i + 1)
		})
	}
	resize(0)
}
