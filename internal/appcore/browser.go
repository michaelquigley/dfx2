package appcore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// BrowserSpec is the private opening bridge for the webview package: the
// platform's browser factory, the copied browser configuration and, for owned
// modes, the window's endpoint. The core never imports HTTP or browser code.
type BrowserSpec struct {
	// Err is a configuration error found before any resource was created. It
	// follows NewWindow's opening policy: returned and latched, after the
	// thread and owner checks.
	Err error
	// Factory nil means this platform cannot host browsers: opening refuses
	// with platform.ErrUnsupportedContent before native work.
	Factory  platform.BrowserFactory
	Config   platform.BrowserConfig
	Endpoint Endpoint // nil for attachment
}

// Endpoint is a window-owned HTTP endpoint. Its lock is never held while it
// calls the host, and the core never holds its own lock while calling it.
type Endpoint interface {
	// Start runs on the UI thread after the shell exists and before the
	// browser is created. It acquires the endpoint's resources and may amend
	// the browser configuration. ctx is the window's context: its cancellation
	// seals admission. On error nothing acquired remains and nothing is owed;
	// on success Drained is owed once ctx is canceled, Close has been called and
	// everything the endpoint admitted has returned.
	Start(ctx context.Context, host EndpointHost, config *platform.BrowserConfig) error
	// Close releases resources after cancellation, on the UI thread, once. It
	// never waits for handlers.
	Close()
	// Status summarizes admitted work for diagnostics, from any goroutine,
	// without blocking on the UI thread or calling the host.
	Status() string
}

// InProcessEndpoint also serves the browser's native requests. Its request
// methods run on the UI thread and never call application code.
type InProcessEndpoint interface {
	Endpoint
	// Bind receives the origin the browser assigned, before Start.
	Bind(origin string)
	Request(platform.BrowserRequest) platform.RequestDecision
	RequestBody(platform.ID, []byte) platform.RequestDecision
	RequestBodyEnd(platform.ID, error) platform.RequestDecision
	RequestReleased(platform.ID)
}

// EndpointHost is the core's side of one endpoint.
type EndpointHost interface {
	// Begin registers a completion owned by the window before the work that
	// publishes it starts. UI thread.
	Begin() (platform.ID, error)
	// Publish completes a registered completion from any goroutine. settle
	// runs on the UI thread, during shutdown too; closing means the window is
	// closing and the result must be released rather than attached.
	Publish(platform.ID, func(closing bool) error)
	// Respond forwards a finite response to the browser. UI thread, from a
	// settle. False means it was not delivered.
	Respond(platform.ID, platform.BrowserResponse) bool
	// Fail reports an application failure, such as a handler panic, from any
	// goroutine.
	Fail(error)
	// Drained reports that the endpoint owns nothing more, from any goroutine.
	Drained()
}

// browserState is one browser window's ownership. Fields are guarded by
// App.mu; impl and spec.Endpoint are called only outside it.
type browserState struct {
	spec            BrowserSpec
	impl            platform.Browser
	attaching       bool // OpenBrowser is still building the attachment
	endpointStarted bool
	endpointDrained bool
	closeStarted    bool
	ready           bool // navigation allowed
	released        bool
	cleanups        map[platform.ID]string
}

// OpenBrowser creates a browser window. It follows NewWindow's thread and
// failure rules; a platform without browser hosting refuses before native
// work and nothing is latched. The returned window is live while its browser
// initializes asynchronously.
func OpenBrowser(a *App, options WindowOptions, spec BrowserSpec) (*Window, error) {
	if !a.backend.IsOwner() {
		return nil, ErrWrongThread
	}
	a.mu.Lock()
	if a.state >= closing || a.ctx.Err() != nil {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	invalid := spec.Err
	if invalid == nil && (options.Width <= 0 || options.Height <= 0) {
		invalid = fmt.Errorf("client dimensions must be positive (got %dx%d)", options.Width, options.Height)
	}
	// Configuration errors precede the platform check, as in NewWindow.
	if invalid != nil {
		err := fmt.Errorf("dfx2 webview.Open: %w", invalid)
		a.failLocked(err)
		a.notifyLocked()
		a.mu.Unlock()
		return nil, err
	}
	a.mu.Unlock()
	if spec.Factory == nil {
		return nil, fmt.Errorf("dfx2 webview: %w", platform.ErrUnsupportedContent)
	}
	return a.newWindow(options, nil, &browserState{spec: spec, attaching: true, cleanups: make(map[platform.ID]string)})
}

// attachBrowser runs on the UI thread after the shell was created and the
// window opened. Every failure is fatal; the partial attachment drains
// through the ordinary close path.
func (a *App) attachBrowser(w *Window) error {
	br := w.browser
	defer func() {
		a.mu.Lock()
		br.attaching = false
		a.notifyLocked()
		a.mu.Unlock()
	}()
	if a.closingWindow(w) {
		return nil
	}
	parent, err := a.backend.BrowserParent(w.id)
	if err != nil {
		return fmt.Errorf("browser parent: %w", err)
	}
	config := br.spec.Config
	config.Origins = slices.Clone(config.Origins)
	if br.spec.Endpoint != nil {
		if err := br.spec.Endpoint.Start(w.ctx, endpointHost{w}, &config); err != nil {
			return fmt.Errorf("endpoint: %w", err)
		}
		a.mu.Lock()
		br.endpointStarted = true
		a.mu.Unlock()
	}
	if a.closingWindow(w) {
		return nil
	}
	impl, err := br.spec.Factory.New(parent, config, browserHost{w})
	if err != nil {
		return fmt.Errorf("browser: %w", err)
	}
	a.mu.Lock()
	br.impl = impl
	a.mu.Unlock()
	if in, ok := br.spec.Endpoint.(InProcessEndpoint); ok {
		in.Bind(impl.Origin())
	}
	// A close that arrived during creation must not start navigation; the
	// drain path closes the attachment instead.
	if !a.closingWindow(w) {
		impl.Start()
	}
	return nil
}

func (a *App) closingWindow(w *Window) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return w.state >= closing
}

// browserDrainedLocked reports whether a window's browser ownership is over.
func browserDrainedLocked(w *Window) bool {
	br := w.browser
	return br == nil || br.released && (br.endpointDrained || !br.endpointStarted) && len(br.cleanups) == 0
}

// closeBrowsers initiates browser teardown for closing windows, before their
// shells are destroyed. UI thread, outside the mutex: Close may deliver
// notifications reentrantly.
func (a *App) closeBrowsers() {
	for {
		a.mu.Lock()
		var target *Window
		for _, w := range a.windows {
			if br := w.browser; br != nil && w.state == closing && !w.creating && !br.attaching && !br.closeStarted {
				target = w
				br.closeStarted = true
				break
			}
		}
		var impl platform.Browser
		var endpoint Endpoint
		if target != nil {
			br := target.browser
			impl = br.impl
			if br.endpointStarted {
				endpoint = br.spec.Endpoint
			}
			if impl == nil {
				br.released = true // nothing native was created
			}
		}
		a.mu.Unlock()
		if target == nil {
			return
		}
		if endpoint != nil {
			if err := invoke("endpoint close", func() error { endpoint.Close(); return nil }); err != nil {
				a.mu.Lock()
				a.cleanupLocked(err)
				a.mu.Unlock()
			}
		}
		if impl != nil {
			if err := invoke("browser close", func() error { impl.Close(); return nil }); err != nil {
				a.mu.Lock()
				a.cleanupLocked(err)
				a.mu.Unlock()
			}
		}
	}
}

// browserBoundsLocked records a browser shell's bounds and returns the
// attachment to forward them to, outside the mutex.
func (a *App) browserBoundsLocked(w *Window) platform.Browser {
	br := w.browser
	if br == nil || br.closeStarted || w.state >= closing {
		return nil
	}
	return br.impl
}

// residueLocked keeps a post-release cleanup failure in the result without
// canceling the application: the residue is not live owned work.
func (a *App) residueLocked(err error) {
	if err != nil {
		a.cleanup = append(a.cleanup, err)
	}
}

type browserHost struct{ w *Window }

func (h browserHost) Ready() bool {
	w := h.w
	a := w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	br := w.browser
	if w.state != open || br.closeStarted || br.released {
		return false
	}
	br.ready = true
	return true
}

func (h browserHost) Failed(err error) {
	w := h.w
	a := w.app
	if err == nil {
		err = errors.New("unspecified browser failure")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failLocked(fmt.Errorf("dfx2 webview window %d: %w", w.id, err))
	a.notifyLocked()
}

func (h browserHost) Released() {
	w := h.w
	a := w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	br := w.browser
	if br.released {
		return
	}
	br.released = true
	if !br.closeStarted {
		a.failLocked(fmt.Errorf("dfx2 webview window %d: browser released before close", w.id))
	}
	a.notifyLocked()
}

func (h browserHost) Cleanup(name string, work func() error) error {
	w := h.w
	a := w.app
	if !a.backend.IsOwner() {
		return ErrWrongThread
	}
	if work == nil {
		return errors.New("dfx2 browser cleanup: nil work")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	br := w.browser
	if br.released || w.state == closed {
		return fmt.Errorf("dfx2 browser cleanup %q: browser already released", name)
	}
	id := a.nextLocked()
	br.cleanups[id] = name
	go func() {
		err := invoke("browser cleanup "+name, work)
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(br.cleanups, id)
		if err != nil {
			record := fmt.Errorf("dfx2 webview window %d cleanup %s: %w", w.id, name, err)
			// Only a directly returned, explicitly identified deletion residue
			// is nonfatal. In particular, a join containing another failure or
			// a recovered panic must not acquire this exemption through As.
			if residue, ok := err.(*platform.FileDeletionResidue); ok && residue != nil && residue.Path != "" && residue.Err != nil {
				a.residueLocked(record)
			} else {
				a.cleanupLocked(record)
			}
		}
		a.notifyLocked()
	}()
	return nil
}

func (h browserHost) inProcess() InProcessEndpoint {
	in, _ := h.w.browser.spec.Endpoint.(InProcessEndpoint)
	return in
}

var abort = platform.RequestDecision{Action: platform.RequestAbort}

func (h browserHost) Request(r platform.BrowserRequest) platform.RequestDecision {
	if in := h.inProcess(); in != nil {
		return in.Request(r)
	}
	return abort
}

func (h browserHost) RequestBody(id platform.ID, data []byte) platform.RequestDecision {
	if in := h.inProcess(); in != nil {
		return in.RequestBody(id, data)
	}
	return abort
}

func (h browserHost) RequestBodyEnd(id platform.ID, err error) platform.RequestDecision {
	if in := h.inProcess(); in != nil {
		return in.RequestBodyEnd(id, err)
	}
	return abort
}

func (h browserHost) RequestReleased(id platform.ID) {
	if in := h.inProcess(); in != nil {
		in.RequestReleased(id)
	}
}

type endpointHost struct{ w *Window }

func (h endpointHost) Begin() (platform.ID, error) { return h.w.app.beginCompletion(h.w, nil) }

func (h endpointHost) Publish(id platform.ID, settle func(closing bool) error) {
	a := h.w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	if op := a.operations[id]; op != nil && op.window == h.w.id && !op.ready {
		op.settle, op.ready = settle, true
		a.notifyLocked()
	}
}

func (h endpointHost) Respond(id platform.ID, r platform.BrowserResponse) bool {
	w := h.w
	a := w.app
	a.mu.Lock()
	br := w.browser
	impl := br.impl
	if w.state != open || br.closeStarted || impl == nil {
		impl = nil
	}
	a.mu.Unlock()
	return impl != nil && impl.Respond(id, r)
}

func (h endpointHost) Fail(err error) {
	w := h.w
	a := w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failLocked(fmt.Errorf("dfx2 webview window %d endpoint: %w", w.id, err))
	a.notifyLocked()
}

func (h endpointHost) Drained() {
	w := h.w
	a := w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	w.browser.endpointDrained = true
	a.notifyLocked()
}

// BrowserStatus is a synchronized snapshot for internal tests and auditions.
type BrowserStatus struct {
	Attached, Ready, CloseStarted, Released bool
	EndpointStarted, EndpointDrained        bool
	Cleanups                                []string
	Endpoint, Native                        string
}

// InspectBrowser is safe from any goroutine.
func InspectBrowser(w *Window) (BrowserStatus, bool) {
	a := w.app
	a.mu.Lock()
	br := w.browser
	if br == nil {
		a.mu.Unlock()
		return BrowserStatus{}, false
	}
	s := BrowserStatus{Attached: br.impl != nil, Ready: br.ready, CloseStarted: br.closeStarted, Released: br.released,
		EndpointStarted: br.endpointStarted, EndpointDrained: br.endpointDrained}
	for _, name := range br.cleanups {
		s.Cleanups = append(s.Cleanups, name)
	}
	impl, endpoint := br.impl, br.spec.Endpoint
	started := br.endpointStarted
	a.mu.Unlock()
	slices.Sort(s.Cleanups)
	s.Endpoint, s.Native = browserDiagnostics(impl, endpoint, started)
	return s, true
}

func browserDiagnostics(impl platform.Browser, endpoint Endpoint, started bool) (endpointStatus, native string) {
	if endpoint != nil && started {
		if err := invoke("endpoint status", func() error { endpointStatus = endpoint.Status(); return nil }); err != nil {
			endpointStatus = "status unavailable: " + firstLine(err.Error())
		}
	}
	if impl != nil {
		if err := invoke("browser diagnostics", func() error { native = impl.Diagnostics(); return nil }); err != nil {
			native = "diagnostics unavailable: " + firstLine(err.Error())
		}
	}
	return endpointStatus, native
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// browserDiagnosticLocked describes a browser window's core state and returns
// a function, run outside the mutex, that adds the endpoint and native
// summaries.
func browserDiagnosticLocked(id platform.ID, w *Window) func() string {
	br := w.browser
	var names []string
	for _, name := range br.cleanups {
		names = append(names, name)
	}
	slices.Sort(names)
	core := fmt.Sprintf("window=%d browser attached=%t attaching=%t ready=%t close-started=%t released=%t endpoint-started=%t endpoint-drained=%t cleanups=%q",
		id, br.impl != nil, br.attaching, br.ready, br.closeStarted, br.released, br.endpointStarted, br.endpointDrained, names)
	impl, endpoint, started := br.impl, br.spec.Endpoint, br.endpointStarted
	return func() string {
		endpointStatus, native := browserDiagnostics(impl, endpoint, started)
		return fmt.Sprintf("%s endpoint=[%s] native=[%s]", core, endpointStatus, native)
	}
}
