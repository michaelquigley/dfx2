package appcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

// fakeEndpoint records its lifecycle; the test decides when it drains.
type fakeEndpoint struct {
	startErr error
	amend    func(*platform.BrowserConfig)
	started  chan EndpointHost

	mu     sync.Mutex
	ctx    context.Context
	host   EndpointHost
	closes int
	bound  string
	reqs   []platform.BrowserRequest
}

func newEndpoint() *fakeEndpoint { return &fakeEndpoint{started: make(chan EndpointHost, 1)} }

func (e *fakeEndpoint) Start(ctx context.Context, host EndpointHost, config *platform.BrowserConfig) error {
	if e.startErr != nil {
		return e.startErr
	}
	e.mu.Lock()
	e.ctx, e.host = ctx, host
	e.mu.Unlock()
	if e.amend != nil {
		e.amend(config)
	}
	e.started <- host
	return nil
}

func (e *fakeEndpoint) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closes++
}

func (e *fakeEndpoint) Status() string { return "fake endpoint status" }

func (e *fakeEndpoint) state() (context.Context, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ctx, e.closes
}

// fakeInProcess also receives forwarded native requests.
type fakeInProcess struct{ *fakeEndpoint }

func (e fakeInProcess) Bind(o string) { e.mu.Lock(); e.bound = o; e.mu.Unlock() }
func (e fakeInProcess) Request(r platform.BrowserRequest) platform.RequestDecision {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reqs = append(e.reqs, r)
	return platform.RequestDecision{ID: 7, Action: platform.RequestWait}
}
func (e fakeInProcess) RequestBody(platform.ID, []byte) platform.RequestDecision { return abort }
func (e fakeInProcess) RequestBodyEnd(platform.ID, error) platform.RequestDecision {
	return abort
}
func (e fakeInProcess) RequestReleased(platform.ID) {}

func attachSpec(f *fake.BrowserFactory, e Endpoint) BrowserSpec {
	mode := platform.BrowserAttach
	if e != nil {
		mode = platform.BrowserLoopback
	}
	return BrowserSpec{Factory: f, Endpoint: e, Config: platform.BrowserConfig{Mode: mode, StartURL: "http://127.0.0.1:9/", Origins: []string{"http://127.0.0.1:9"}}}
}

func openBrowserWindow(a *App, title string, spec BrowserSpec) (*Window, error) {
	return OpenBrowser(a, WindowOptions{Title: title, Width: 320, Height: 200}, spec)
}

// onUI runs f as a native callback on the UI thread and waits for it. The fake
// pumps in FIFO order, so it also follows any dispatch already queued.
func onUI(t *testing.T, b *fake.Backend, f func()) {
	t.Helper()
	done := make(chan struct{})
	b.Call(func() { f(); close(done) })
	receive(t, done)
}

func noViolations(t *testing.T, b *fake.Backend, browsers ...*fake.Browser) {
	t.Helper()
	if v := b.Violations(); len(v) != 0 {
		t.Errorf("backend violations: %v", v)
	}
	for _, br := range browsers {
		if v := br.Violations(); len(v) != 0 {
			t.Errorf("browser violations: %v", v)
		}
	}
}

func TestBrowserWindowIsLiveWhileInitializingAndNavigatesOnlyWhenReady(t *testing.T) {
	f := &fake.BrowserFactory{}
	var created atomic.Pointer[fake.Browser]
	f.OnNew = func(br *fake.Browser) { created.Store(br) }
	destroyedAfterRelease := make(chan bool, 1)
	b := &fake.Backend{}
	b.OnDestroy = func(platform.ID) {
		_, _, closed, released, _ := created.Load().State()
		destroyedAfterRelease <- closed && released
	}
	windows := make(chan *Window, 1)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := openBrowserWindow(a, "browser", attachSpec(f, nil))
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	receive(t, h.ready)
	started, navigated, _, _, _ := br.State()
	if !started || navigated {
		t.Fatalf("started %t navigated %t", started, navigated)
	}
	if s := Inspect(h.app); s.Windows != 1 || s.Browsers != 1 || s.Closing {
		t.Fatalf("starting browser window not live: %+v", s)
	}
	if br.Parent.Window != w.id || br.Parent.Handle == 0 || br.Config.Mode != platform.BrowserAttach || br.Config.StartURL != "http://127.0.0.1:9/" {
		t.Fatalf("attachment %+v %+v", br.Parent, br.Config)
	}
	onUI(t, b, func() {
		if !br.Ready() {
			t.Error("open window refused navigation")
		}
	})
	surface := platform.Surface{Width: 640, Height: 400, Scale: 2, Visible: true}
	b.Enqueue(platform.Event{Kind: platform.SurfaceChanged, Window: w.id, Surface: surface})
	onUI(t, b, func() {})
	if got := br.Bounded(); len(got) != 1 || got[0] != surface {
		t.Fatalf("bounds %v", got)
	}
	if st, _ := InspectContent(w); st.Frames != 0 {
		t.Fatal("browser window enrolled in frame scheduling")
	}
	w.Close()
	if !receive(t, destroyedAfterRelease) {
		t.Fatal("shell destroyed before its browser released it")
	}
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, w.Done())
	noViolations(t, b, br)
}

func TestCloseDuringShellCreationNeverCreatesTheBrowser(t *testing.T) {
	f := &fake.BrowserFactory{}
	e := newEndpoint()
	b := &fake.Backend{}
	b.OnCreate = func(id platform.ID) { b.Emit(platform.Event{Kind: platform.CloseRequested, Window: id}) }
	windows := make(chan *Window, 1)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := openBrowserWindow(a, "closed", attachSpec(f, e))
		if !errors.Is(err, ErrClosed) || w == nil {
			t.Errorf("open: %v %v", w, err)
		}
		windows <- w
		return nil
	})
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, receive(t, windows).Done())
	select {
	case <-f.Created():
		t.Fatal("browser created for a window closed during shell creation")
	case <-e.started:
		t.Fatal("endpoint started for a window closed during shell creation")
	default:
	}
	noViolations(t, b)
}

func TestCloseDuringBrowserCreationNeverStartsNavigation(t *testing.T) {
	e := newEndpoint()
	f := &fake.BrowserFactory{DelayRelease: true}
	b := &fake.Backend{}
	f.OnNew = func(br *fake.Browser) {
		b.Emit(platform.Event{Kind: platform.CloseRequested, Window: br.Parent.Window})
	}
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		_, err := openBrowserWindow(a, "racing", attachSpec(f, e))
		if !errors.Is(err, ErrClosed) {
			t.Errorf("open: %v", err)
		}
		return nil
	})
	br := receive(t, f.Created())
	receive(t, h.ready)
	onUI(t, b, func() {
		// A late initialization result must be released, not navigated.
		if br.Ready() {
			t.Error("closing window allowed navigation")
		}
	})
	started, navigated, closed, released, _ := br.State()
	if started || navigated || !closed || released {
		t.Fatalf("started %t navigated %t closed %t released %t", started, navigated, closed, released)
	}
	if _, closes := e.state(); closes != 1 {
		t.Fatalf("endpoint closes %d", closes)
	}
	resultPending(t, h.done)
	onUI(t, b, br.Release)
	host := receive(t, e.started)
	resultPending(t, h.done)
	host.Drained()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	noViolations(t, b, br)
}

func TestShellOutlivesItsBrowserAndEndpointDrain(t *testing.T) {
	e := newEndpoint()
	f := &fake.BrowserFactory{DelayRelease: true}
	destroys := make(chan platform.ID, 1)
	b := &fake.Backend{OnDestroy: func(id platform.ID) { destroys <- id }}
	windows := make(chan *Window, 1)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := openBrowserWindow(a, "owned", attachSpec(f, e))
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	host := receive(t, e.started)
	if br.Config.Credential != nil {
		t.Fatal("unexpected credential")
	}
	w.Close()
	ctx, _ := e.state()
	if ctx.Err() == nil {
		t.Fatal("endpoint context not canceled at close")
	}
	onUI(t, b, func() {}) // after the dispatch the close queued
	if _, _, closed, _, _ := br.State(); !closed {
		t.Fatal("browser close not initiated")
	}
	select {
	case <-destroys:
		t.Fatal("shell destroyed while the browser still uses it")
	default:
	}
	h.app.mu.Lock()
	busy := h.app.pendingLocked()
	diagnostic, browsers := h.app.diagnosticLocked()
	h.app.mu.Unlock()
	if busy {
		t.Fatal("waiting for a browser release keeps dispatch busy")
	}
	for _, describe := range browsers {
		diagnostic += "; " + describe()
	}
	for _, want := range []string{"close-started=true released=false", "fake endpoint status", "fake browser closed=true"} {
		if !strings.Contains(diagnostic, want) {
			t.Errorf("diagnostics lack %q: %s", want, diagnostic)
		}
	}
	if st, ok := InspectBrowser(w); !ok || !st.Attached || !st.CloseStarted || st.Released || !st.EndpointStarted || st.EndpointDrained ||
		st.Endpoint != "fake endpoint status" || !strings.Contains(st.Native, "closed=true") {
		t.Fatalf("inspect %+v", st)
	}
	onUI(t, b, br.Release)
	receive(t, destroys)
	onUI(t, b, func() {})
	pending(t, w.Done())
	resultPending(t, h.done)
	host.Drained()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, w.Done())
	noViolations(t, b, br)
}

func TestUnexpectedParentLossIsFatalButStillDrainsTheBrowser(t *testing.T) {
	f := &fake.BrowserFactory{DelayRelease: true}
	b := &fake.Backend{}
	windows := make(chan *Window, 1)
	h := launch(t, quietOptions(ResidentApplication), b, newClock(), func(a *App) error {
		w, err := openBrowserWindow(a, "lost", attachSpec(f, nil))
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: w.id})
	onUI(t, b, func() {}) // the loss has been handled and its dispatch queued
	onUI(t, b, func() {}) // that dispatch has run
	if _, _, closed, _, _ := br.State(); !closed {
		t.Fatal("browser not closed after its parent was lost")
	}
	resultPending(t, h.done)
	pending(t, w.Done())
	onUI(t, b, br.Release)
	if err := receive(t, h.done); err == nil || !strings.Contains(err.Error(), "native parent destroyed") {
		t.Fatalf("parent loss: %v", err)
	}
}

func TestBrowserFailureCancelsAndDrainsEveryWindow(t *testing.T) {
	f := &fake.BrowserFactory{}
	b := &fake.Backend{}
	plain := make(chan *Window, 1)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := window(a, "native")
		plain <- w
		if err != nil {
			return err
		}
		_, err = openBrowserWindow(a, "browser", attachSpec(f, nil))
		return err
	})
	br := receive(t, f.Created())
	receive(t, h.ready)
	boom := errors.New("browser process exited")
	onUI(t, b, func() { br.Host.Failed(boom) })
	err := receive(t, h.done)
	if !errors.Is(err, boom) {
		t.Fatalf("result %v", err)
	}
	receive(t, receive(t, plain).Done())
	noViolations(t, b, br)
}

func TestAttachmentFailuresAreFatalAndReleasePartialOwnership(t *testing.T) {
	boom := errors.New("attachment failed")
	for _, tc := range []struct {
		name           string
		backend        *fake.Backend
		factory        *fake.BrowserFactory
		endpoint       *fakeEndpoint
		created        bool
		endpointCloses int
	}{
		{"parent", &fake.Backend{ParentError: boom}, &fake.BrowserFactory{}, newEndpoint(), false, 0},
		{"endpoint", &fake.Backend{}, &fake.BrowserFactory{}, &fakeEndpoint{startErr: boom, started: make(chan EndpointHost, 1)}, false, 0},
		{"browser", &fake.Backend{}, &fake.BrowserFactory{NewError: boom}, newEndpoint(), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := launch(t, quietOptions(ResidentApplication), tc.backend, newClock(), func(a *App) error {
				w, err := openBrowserWindow(a, tc.name, attachSpec(tc.factory, tc.endpoint))
				if w != nil || !errors.Is(err, boom) {
					t.Errorf("open: %v %v", w, err)
				}
				return nil
			})
			if tc.name == "browser" {
				// The started endpoint is closed and must still drain.
				host := receive(t, tc.endpoint.started)
				resultPending(t, h.done)
				eventually(t, "endpoint close", func() bool { _, closes := tc.endpoint.state(); return closes == 1 })
				host.Drained()
			}
			if err := receive(t, h.done); !errors.Is(err, boom) {
				t.Fatalf("result %v", err)
			}
			if _, closes := tc.endpoint.state(); closes != tc.endpointCloses {
				t.Fatalf("endpoint closes %d", closes)
			}
			select {
			case <-tc.factory.Created():
				t.Fatal("browser created")
			default:
			}
			noViolations(t, tc.backend)
		})
	}
}

func TestUnsupportedBrowserHostingLatchesNothing(t *testing.T) {
	t.Run("no factory refuses before native work", func(t *testing.T) {
		var creates atomic.Int32
		b := &fake.Backend{BeforeCreate: func(platform.ID) { creates.Add(1) }}
		h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
			w, err := window(a, "native")
			if err != nil {
				return err
			}
			if _, err := openBrowserWindow(a, "unsupported", BrowserSpec{}); !errors.Is(err, platform.ErrUnsupportedContent) {
				t.Errorf("refusal: %v", err)
			}
			w.Close()
			return nil
		})
		if err := receive(t, h.done); err != nil {
			t.Fatal(err)
		}
		if creates.Load() != 1 {
			t.Fatalf("native creates %d", creates.Load())
		}
	})
	t.Run("backend refusal rechecks a reentrant last-window close", func(t *testing.T) {
		var other *Window
		b := &fake.Backend{BrowserUnsupported: true}
		b.BeforeCreate = func(platform.ID) {
			if other != nil {
				// The other window closes and its native destruction completes
				// while the browser shell is starting, which postpones
				// last-window exit; only the refusal's recheck can end the app.
				other.Close()
				b.Emit(platform.Event{Kind: platform.Destroyed, Window: other.id})
			}
		}
		f := &fake.BrowserFactory{}
		h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
			w, err := window(a, "native")
			if err != nil {
				return err
			}
			other = w
			// Run from a posted callback, after setup, so the last-window rule
			// applies when the refusal returns.
			return a.Post(func() {
				if _, err := openBrowserWindow(a, "refused", attachSpec(f, nil)); !errors.Is(err, platform.ErrUnsupportedContent) {
					t.Errorf("refusal: %v", err)
				}
			})
		})
		if err := receive(t, h.done); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEndpointCompletionsSettleOnTheUIThreadAndHoldTheWindow(t *testing.T) {
	e := newEndpoint()
	f := &fake.BrowserFactory{}
	b := &fake.Backend{}
	windows := make(chan *Window, 1)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := openBrowserWindow(a, "owned", attachSpec(f, e))
		windows <- w
		return err
	})
	w := receive(t, windows)
	host := receive(t, e.started)
	type settled struct{ closing, inTurn bool }
	results := make(chan settled, 2)
	settle := func(closing bool) error {
		h.app.mu.Lock()
		inTurn := h.app.inTurn
		h.app.mu.Unlock()
		results <- settled{closing, inTurn}
		return nil
	}
	var first, second platform.ID
	onUI(t, b, func() {
		var err error
		if first, err = host.Begin(); err != nil {
			t.Error(err)
		}
		if second, err = host.Begin(); err != nil {
			t.Error(err)
		}
	})
	go host.Publish(first, settle)
	if got := receive(t, results); got.closing || !got.inTurn {
		t.Fatalf("open settlement %+v", got)
	}
	w.Close()
	onUI(t, b, func() {}) // after the dispatch the close queued
	pending(t, w.Done())
	resultPending(t, h.done)
	go host.Publish(second, settle)
	if got := receive(t, results); !got.closing {
		t.Fatal("late result settled as if the window were open")
	}
	host.Publish(second, settle) // duplicate: harmless
	resultPending(t, h.done)
	host.Drained()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Begin(); !errors.Is(err, ErrClosed) {
		t.Fatalf("begin after drain: %v", err)
	}
	if len(results) != 0 {
		t.Fatal("a duplicate publication settled twice")
	}
}

func TestRespondReachesOnlyAnOpenAttachedBrowser(t *testing.T) {
	e := fakeInProcess{newEndpoint()}
	f := &fake.BrowserFactory{Origin: "fake://w"}
	b := &fake.Backend{}
	windows := make(chan *Window, 1)
	spec := attachSpec(f, e)
	spec.Config = platform.BrowserConfig{Mode: platform.BrowserInProcess}
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := openBrowserWindow(a, "in-process", spec)
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	host := receive(t, e.started)
	if e.bound != "fake://w" {
		t.Fatalf("bound %q", e.bound)
	}
	onUI(t, b, func() {
		d := br.Request(platform.BrowserRequest{Method: "GET", URL: "fake://w/"})
		if d.ID != 7 || len(e.reqs) != 1 {
			t.Errorf("request not forwarded: %+v", d)
		}
		if !host.Respond(7, platform.BrowserResponse{Status: 200}) {
			t.Error("open browser refused a response")
		}
	})
	if r, ok := br.Response(7); !ok || r.Status != 200 {
		t.Fatal("response not delivered")
	}
	w.Close()
	onUI(t, b, func() {
		if host.Respond(7, platform.BrowserResponse{Status: 201}) {
			t.Error("response delivered to a closing window")
		}
	})
	host.Drained()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	noViolations(t, b, br)
}

func TestNonInProcessBrowsersRefuseNativeRequests(t *testing.T) {
	f := &fake.BrowserFactory{}
	b := &fake.Backend{}
	h := launch(t, quietOptions(ResidentApplication), b, newClock(), func(a *App) error {
		_, err := openBrowserWindow(a, "attach", attachSpec(f, nil))
		return err
	})
	br := receive(t, f.Created())
	onUI(t, b, func() {
		if d := br.Request(platform.BrowserRequest{Method: "GET", URL: "http://127.0.0.1:9/"}); d.Action != platform.RequestAbort || d.ID != 0 {
			t.Errorf("attachment served a native request: %+v", d)
		}
	})
	h.app.Quit()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestClosingOneBrowserWindowLeavesAnotherEndpointRunning(t *testing.T) {
	ea, eb := newEndpoint(), newEndpoint()
	f := &fake.BrowserFactory{}
	b := &fake.Backend{}
	windows := make(chan *Window, 2)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		for _, e := range []*fakeEndpoint{ea, eb} {
			w, err := openBrowserWindow(a, "w", attachSpec(f, e))
			if err != nil {
				return err
			}
			windows <- w
		}
		return nil
	})
	wa, wb := receive(t, windows), receive(t, windows)
	hostA, hostB := receive(t, ea.started), receive(t, eb.started)
	wa.Close()
	hostA.Drained()
	receive(t, wa.Done())
	if ctx, closes := eb.state(); ctx.Err() != nil || closes != 0 {
		t.Fatal("closing A canceled or closed B's endpoint")
	}
	if h.app.Context().Err() != nil {
		t.Fatal("application canceled with a live browser window")
	}
	wb.Close()
	hostB.Drained()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserCleanupResidueIsRetainedWithoutCanceling(t *testing.T) {
	residue := errors.New("profile directory busy")
	f := &fake.BrowserFactory{}
	f.OnClose = func(br *fake.Browser) {
		if err := br.Host.Cleanup("profile", func() error {
			return &platform.FileDeletionResidue{Path: "/tmp/dfx2-profile-1", Err: residue}
		}); err != nil {
			t.Error(err)
		}
	}
	b := &fake.Backend{}
	windows := make(chan *Window, 1)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		if _, err := window(a, "native"); err != nil {
			return err
		}
		w, err := openBrowserWindow(a, "browser", attachSpec(f, nil))
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	w.Close()
	receive(t, w.Done())
	if h.app.Context().Err() != nil {
		t.Fatal("cleanup residue canceled the application")
	}
	onUI(t, b, func() {
		if err := br.Host.Cleanup("late", func() error { return nil }); err == nil {
			t.Error("cleanup accepted after release")
		}
	})
	h.app.Quit()
	err := receive(t, h.done)
	if !errors.Is(err, residue) || !strings.Contains(err.Error(), "/tmp/dfx2-profile-1") {
		t.Fatalf("residue not retained: %v", err)
	}
}

func TestUnexpectedBrowserCleanupFailureCancelsOtherWindows(t *testing.T) {
	failure := errors.New("unexpected cleanup failure")
	residue := &platform.FileDeletionResidue{Path: "/tmp/dfx2-profile-1", Err: errors.New("directory busy")}
	for _, tc := range []struct {
		name  string
		work  func() error
		panic bool
	}{
		{"error", func() error { return failure }, false},
		{"panic", func() error { panic("cleanup exploded") }, true},
		{"joined residue and error", func() error { return errors.Join(residue, failure) }, false},
		{"panic with residue", func() error { panic(residue) }, true},
	} {
		for _, mode := range []Mode{WindowApplication, ResidentApplication} {
			t.Run(fmt.Sprintf("%s/mode%d", tc.name, mode), func(t *testing.T) {
				f := &fake.BrowserFactory{}
				f.OnClose = func(br *fake.Browser) {
					if err := br.Host.Cleanup("profile", tc.work); err != nil {
						t.Error(err)
					}
				}
				windows := make(chan *Window, 2)
				h := launch(t, quietOptions(mode), &fake.Backend{}, newClock(), func(a *App) error {
					native, err := window(a, "survivor")
					if err != nil {
						return err
					}
					windows <- native
					browser, err := openBrowserWindow(a, "browser", attachSpec(f, nil))
					windows <- browser
					return err
				})
				native, browser := receive(t, windows), receive(t, windows)
				browser.Close()
				receive(t, browser.Done())
				if h.app.Context().Err() == nil || native.Context().Err() == nil {
					t.Fatal("unexpected cleanup failure did not cancel the app and other window")
				}
				err := receive(t, h.done)
				if err == nil || !strings.Contains(err.Error(), "cleanup profile") {
					t.Fatalf("cleanup failure lost: %v", err)
				}
				if tc.panic {
					if !strings.Contains(err.Error(), "panic:") || !strings.Contains(err.Error(), "runtime/debug.Stack") {
						t.Fatalf("cleanup panic or stack lost: %v", err)
					}
				} else if !errors.Is(err, failure) {
					t.Fatalf("cleanup cause lost: %v", err)
				}
				receive(t, native.Done())
			})
		}
	}
}

func TestBrowserCleanupFailureFollowingAnEarlierFailureKeepsBoth(t *testing.T) {
	fatal, residue := errors.New("browser process exited"), errors.New("profile residue")
	f := &fake.BrowserFactory{}
	cleanupStarted := make(chan struct{})
	finish := make(chan struct{})
	f.OnClose = func(br *fake.Browser) {
		br.Host.Cleanup("profile", func() error {
			close(cleanupStarted)
			<-finish
			return &platform.FileDeletionResidue{Path: "/tmp/dfx2-profile-1", Err: residue}
		})
	}
	b := &fake.Backend{}
	windows := make(chan *Window, 1)
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := openBrowserWindow(a, "browser", attachSpec(f, nil))
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	onUI(t, b, func() { br.Host.Failed(fatal) })
	receive(t, cleanupStarted)
	if s := Inspect(h.app); s.BrowserCleanups != 1 {
		t.Fatalf("cleanup not visible: %+v", s)
	}
	pending(t, w.Done())
	resultPending(t, h.done)
	close(finish)
	err := receive(t, h.done)
	if !errors.Is(err, fatal) || !errors.Is(err, residue) {
		t.Fatalf("result %v", err)
	}
}

func TestOpenBrowserFollowsOpeningErrorPolicy(t *testing.T) {
	f := &fake.BrowserFactory{}
	var wrong atomic.Bool
	b := &fake.Backend{Owner: func() bool { return !wrong.Load() }}
	h := launch(t, quietOptions(ResidentApplication), b, newClock(), func(a *App) error {
		wrong.Store(true)
		if _, err := openBrowserWindow(a, "wrong", attachSpec(f, nil)); !errors.Is(err, ErrWrongThread) {
			t.Error(err)
		}
		wrong.Store(false)
		return nil
	})
	receive(t, h.ready)
	if h.app.Context().Err() != nil {
		t.Fatal("wrong-thread open latched a failure")
	}
	h.app.Quit()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	invalid := errors.New("invalid browser options")
	for name, spec := range map[string]BrowserSpec{"options": {Err: invalid, Factory: f}, "dimensions": attachSpec(f, nil)} {
		t.Run(name, func(t *testing.T) {
			var creates atomic.Int32
			b := &fake.Backend{BeforeCreate: func(platform.ID) { creates.Add(1) }}
			h := launch(t, quietOptions(ResidentApplication), b, newClock(), func(a *App) error {
				_, _ = OpenBrowser(a, WindowOptions{Width: map[string]int{"options": 10}[name], Height: 10}, spec)
				return nil
			})
			err := receive(t, h.done)
			if err == nil || (name == "options" && !errors.Is(err, invalid)) || (name == "dimensions" && !strings.Contains(err.Error(), "dimensions")) {
				t.Fatalf("not latched: %v", err)
			}
			if creates.Load() != 0 {
				t.Fatal("invalid browser window reached native creation")
			}
		})
	}
	t.Run("closed application", func(t *testing.T) {
		h := launch(t, quietOptions(ResidentApplication), &fake.Backend{}, newClock(), func(a *App) error {
			a.Quit()
			if _, err := openBrowserWindow(a, "late", attachSpec(f, nil)); !errors.Is(err, ErrClosed) {
				t.Error(err)
			}
			return nil
		})
		if err := receive(t, h.done); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBrowserFailureAfterAnEarlierFailureIsKept(t *testing.T) {
	first, crash := errors.New("application failed first"), errors.New("web process crashed")
	f := &fake.BrowserFactory{DelayRelease: true}
	b := &fake.Backend{}
	fail := make(chan struct{})
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		if _, err := openBrowserWindow(a, "browser", attachSpec(f, nil)); err != nil {
			return err
		}
		return a.Go("worker", func(ctx context.Context) error {
			select {
			case <-fail:
				return first
			case <-ctx.Done():
				return nil
			}
		})
	})
	br := receive(t, f.Created())
	close(fail)
	eventually(t, "close after the first failure", func() bool { _, _, closed, _, _ := br.State(); return closed })
	onUI(t, b, func() { br.Host.Failed(crash) })
	onUI(t, b, br.Release)
	err := receive(t, h.done)
	if !errors.Is(err, first) || !errors.Is(err, crash) {
		t.Fatalf("Run lost a failure: %v", err)
	}
}
