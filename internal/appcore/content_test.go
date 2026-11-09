package appcore

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

// frameClock is monotonic test time plus one-shot timers. While idle is set,
// arming any frame-deadline timer is a test failure: a settled window must
// leave no periodic or pending deadline work.
type frameClock struct {
	mu       sync.Mutex
	now      time.Time
	timers   chan *manualTimer
	idle     atomic.Bool
	unwanted atomic.Int64
}

// The unwanted-timer check runs at cleanup rather than from the supervisor
// goroutine, which may still be running after a test has already failed.
func newFrameClock(t *testing.T) *frameClock {
	c := &frameClock{now: time.Unix(2_000_000, 0), timers: make(chan *manualTimer, 1000)}
	t.Cleanup(func() {
		if n := c.unwanted.Load(); n != 0 {
			t.Errorf("%d frame deadline timers armed while the window should have been idle", n)
		}
	})
	return c
}

func (c *frameClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *frameClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *frameClock) NewTimer(d time.Duration) timer {
	tm := &manualTimer{d: d, c: make(chan time.Time, 1)}
	if d <= frameInterval { // diagnostics (5 s) are shutdown work, not frame deadlines
		if c.idle.Load() {
			c.unwanted.Add(1)
		}
		c.timers <- tm
	}
	return tm
}

// fakeContent records each frame on the UI thread and hands back an image of
// the surface's size. onFrame may block (a barrier), invalidate, or panic.
type fakeContent struct {
	frames    chan FrameRequest
	onFrame   func(FrameRequest) error
	onRelease func()
	consume   func(FrameRequest) int // input events consumed; default all
	capture   func(FrameRequest) bool
	animate   atomic.Bool
	released  atomic.Int32
	count     atomic.Int32
}

func newFakeContent() *fakeContent { return &fakeContent{frames: make(chan FrameRequest, 1000)} }

func (c *fakeContent) Frame(req FrameRequest) (FrameResult, error) {
	c.count.Add(1)
	if c.onFrame != nil {
		if err := c.onFrame(req); err != nil {
			return FrameResult{}, err
		}
	}
	c.frames <- req
	// At least 1×1, so a wrongly scheduled zero-size frame is observed as a
	// frame rather than first failing presentation.
	w, h := max(req.Surface.Width, 1), max(req.Surface.Height, 1)
	consumed := len(req.Input)
	if c.consume != nil {
		consumed = c.consume(req)
	}
	return FrameResult{Image: platform.Image{Width: w, Height: h, Stride: w * 4, Scale: req.Surface.Scale,
		Pix: make([]byte, w*h*4)}, Animate: c.animate.Load(), Consumed: consumed,
		ReleaseCapture: c.capture != nil && c.capture(req)}, nil
}

func (c *fakeContent) Release() {
	if c.onRelease != nil {
		c.onRelease()
	}
	c.released.Add(1)
}

// barrier observes the fake backend's deliveries on the UI thread. AfterEvent
// runs only after the core has returned from handling an event, so it marks
// both a processed surface notification and a completed dispatch turn,
// including that turn's frame step. Install it before launch.
type barrier struct {
	b         *fake.Backend
	mu        sync.Mutex
	surfaces  chan platform.Event
	waiting   []chan struct{} // released after the next completed wake dispatch
	requested []chan struct{} // armed by a posted callback during a dispatch
}

func watch(b *fake.Backend) *barrier {
	r := &barrier{b: b, surfaces: make(chan platform.Event, 100)}
	prev := b.AfterEvent
	b.AfterEvent = func(e platform.Event) {
		if prev != nil {
			prev(e)
		}
		switch e.Kind {
		case platform.SurfaceChanged:
			r.surfaces <- e
		case platform.Wake:
			r.mu.Lock()
			done := r.requested
			r.requested = nil
			r.mu.Unlock()
			for _, c := range done {
				close(c)
			}
		}
	}
	return r
}

// surface delivers surface notifications together and waits until the core
// has processed each. It does not wait for any frame they cause.
func (r *barrier) surface(t *testing.T, events ...platform.Event) {
	t.Helper()
	for _, e := range events {
		r.b.Enqueue(e)
	}
	for _, e := range events {
		for {
			if got := receive(t, r.surfaces); got.Window == e.Window && got.Surface == e.Surface {
				break
			}
		}
	}
}

func surfaceOf(w *Window, s platform.Surface) platform.Event {
	return platform.Event{Kind: platform.SurfaceChanged, Window: w.id, Surface: s}
}

// show makes windows visible at 4×3 in one delivery, so none gets a head start.
func (r *barrier) show(t *testing.T, visible bool, ws ...*Window) {
	t.Helper()
	var events []platform.Event
	for _, w := range ws {
		events = append(events, surfaceOf(w, platform.Surface{Width: 4, Height: 3, Scale: 1, Visible: visible}))
	}
	r.surface(t, events...)
}

// turn waits until a wake dispatch that started after this call has returned
// completely: its posted callbacks, its frame step and its internal drain.
// Negative frame assertions follow it, after any relevant surface barrier.
func (r *barrier) turn(t *testing.T, a *App) {
	t.Helper()
	r.dispatch(t, a, nil)
}

// dispatch runs f on the UI thread and waits for that complete dispatch,
// including frames. Arming in the same callback ties completion to f.
func (r *barrier) dispatch(t *testing.T, a *App, f func()) {
	t.Helper()
	done := make(chan struct{})
	if err := a.Post(func() {
		if f != nil {
			f()
		}
		r.mu.Lock()
		r.requested = append(r.requested, done)
		r.mu.Unlock()
	}); err != nil {
		t.Fatalf("turn barrier refused: %v", err)
	}
	select {
	case <-done:
	case <-a.Context().Done():
		select {
		case <-done: // completed too; select chose between ready cases at random
		default:
			t.Fatal("application shut down before the barrier's dispatch completed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a completed dispatch")
	}
}

// eventually polls a condition owned by the supervisor goroutine, which has no
// UI-thread barrier. The deadline only guards a hang; it arranges no ordering.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func noFrame(t *testing.T, c *fakeContent) {
	t.Helper()
	select {
	case req := <-c.frames:
		t.Fatalf("unexpected frame %d", req.Sequence)
	default:
	}
}

func openContent(t *testing.T, b *fake.Backend, clock clock, contents ...*fakeContent) (harness, []*Window, *barrier) {
	t.Helper()
	r := watch(b)
	windows := make([]*Window, len(contents))
	h := launch(t, quietOptions(WindowApplication), b, clock, func(a *App) (err error) {
		for i, c := range contents {
			if windows[i], err = OpenContent(a, WindowOptions{Width: 4, Height: 3}, c); err != nil {
				return err
			}
		}
		return nil
	})
	receive(t, h.ready)
	return h, windows, r
}

func closeAll(t *testing.T, h harness, windows ...*Window) {
	t.Helper()
	for _, w := range windows {
		w.Close()
	}
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestFirstFrameWaitsForVisibleSizedSurface(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	h, ws, r := openContent(t, b, clock, c)
	r.turn(t, h.app)
	noFrame(t, c) // created but not yet configured
	r.surface(t, surfaceOf(ws[0], platform.Surface{Scale: 1, Visible: true}))
	r.turn(t, h.app)
	noFrame(t, c) // visible but zero-sized: no frame and no spinning
	r.show(t, true, ws[0])
	req := receive(t, c.frames)
	if req.Sequence != 1 || req.Surface.Width != 4 || !req.Now.Equal(clock.Now()) {
		t.Fatalf("first frame %+v", req)
	}
	r.turn(t, h.app)
	if w, _ := b.Window(ws[0].id); w.Presents != 1 || w.Last.Width != 4 {
		t.Fatalf("presented %+v", w)
	}
	// Identical surface notifications (exposure) present nothing and never
	// re-evaluate the view.
	r.show(t, true, ws[0])
	r.turn(t, h.app)
	noFrame(t, c)
	closeAll(t, h, ws...)
	if c.released.Load() != 1 || len(b.Violations()) != 0 {
		t.Fatalf("released=%d violations=%v", c.released.Load(), b.Violations())
	}
}

func TestInvalidationRacingAFrameSurvivesAndClearsOnlyItsGeneration(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	c.onFrame = func(FrameRequest) error {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	receive(t, entered)
	// The UI thread is inside the first frame, which consumes generation 1.
	if err := Invalidate(ws[0]); err != nil {
		t.Fatal(err)
	}
	during, _ := InspectContent(ws[0])
	if during.Consumed != 0 || during.Generation < 2 {
		t.Fatalf("during frame %+v", during)
	}
	close(release)
	receive(t, c.frames)
	second := receive(t, c.frames)
	r.turn(t, h.app)
	// The first frame cleared only the generation it sampled; the mark added
	// while it rendered caused the second frame, which consumed it.
	if s, _ := InspectContent(ws[0]); second.Sequence != 2 || s.Consumed != during.Generation || s.Frames != 2 {
		t.Fatalf("after race: frame %d status %+v", second.Sequence, s)
	}
	noFrame(t, c)
	closeAll(t, h, ws...)
}

func TestRepeatedInvalidationNeverUsesPostSlots(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	c.onFrame = func(FrameRequest) error {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	receive(t, entered)
	var ran atomic.Int32
	for range 255 {
		if err := ws[0].Post(func() { ran.Add(1) }); err != nil {
			t.Fatal(err)
		}
	}
	// The window's own FIFO orders this barrier after its 255 callbacks.
	drained := make(chan struct{})
	if err := ws[0].Post(func() { ran.Add(1); close(drained) }); err != nil {
		t.Fatal(err)
	}
	if err := ws[0].Post(func() {}); !errors.Is(err, ErrBusy) {
		t.Fatalf("257th post: %v", err)
	}
	for range 10_000 {
		if err := Invalidate(ws[0]); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	receive(t, c.frames)
	receive(t, c.frames) // the coalesced invalidations make exactly one more frame
	receive(t, drained)
	r.turn(t, h.app)
	noFrame(t, c)
	if ran.Load() != 256 {
		t.Fatalf("ran %d posted callbacks", ran.Load())
	}
	closeAll(t, h, ws...)
	if err := Invalidate(ws[0]); !errors.Is(err, ErrClosed) {
		t.Fatalf("invalidate after close: %v", err)
	}
}

func TestAnimationUsesOneShotDeadlinesFromMonotonicTime(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	c.animate.Store(true)
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	start := receive(t, c.frames).Now
	tm := receive(t, clock.timers)
	if tm.d != frameInterval {
		t.Fatalf("deadline timer %v", tm.d)
	}
	r.turn(t, h.app)
	noFrame(t, c) // nothing renders before the deadline
	// Time passes far beyond several intervals: one frame, no catch-up burst.
	clock.advance(250 * time.Millisecond)
	tm.fire()
	req := receive(t, c.frames)
	if req.Now.Sub(start) != 250*time.Millisecond || req.Sequence != 2 {
		t.Fatalf("frame %d at %v", req.Sequence, req.Now.Sub(start))
	}
	next := receive(t, clock.timers)
	r.turn(t, h.app)
	noFrame(t, c)
	// Settling: the next frame requests no animation and no timer remains.
	c.animate.Store(false)
	clock.advance(frameInterval)
	next.fire()
	receive(t, c.frames)
	r.turn(t, h.app)
	clock.idle.Store(true)
	if err := Invalidate(ws[0]); err != nil {
		t.Fatal(err)
	}
	receive(t, c.frames)
	r.turn(t, h.app)
	if s := Inspect(h.app); s.Animating != 0 {
		t.Fatalf("settled window still animating: %+v", s)
	}
	closeAll(t, h, ws...)
}

func TestCloseCancelsDeadlineAndSealsContent(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	c.animate.Store(true)
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	receive(t, c.frames)
	tm := receive(t, clock.timers)
	clock.idle.Store(true)
	ws[0].Close()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if !tm.stopped.Load() {
		t.Fatal("deadline timer survived close")
	}
	if c.released.Load() != 1 || len(b.Violations()) != 0 {
		t.Fatalf("released=%d violations=%v", c.released.Load(), b.Violations())
	}
}

func TestCloseDuringFrameSkipsPresentationAndReleasesAfterTeardown(t *testing.T) {
	// The only frame is blocked until after Close, so no presentation is
	// legitimate. The native window still exists when that frame returns, so
	// the fake's lifetime checks cannot catch one; count every attempt.
	var presented atomic.Int32
	b := &fake.Backend{OnPresent: func(platform.ID, platform.Image) error { presented.Add(1); return nil }}
	clock, c := newFrameClock(t), newFakeContent()
	entered, release := make(chan struct{}), make(chan struct{})
	c.onFrame = func(FrameRequest) error { close(entered); <-release; return nil }
	var w *Window
	b.OnDestroy = func(platform.ID) {
		if c.released.Load() != 0 {
			t.Error("content released before native teardown")
		}
	}
	h, ws, r := openContent(t, b, clock, c)
	w = ws[0]
	r.show(t, true, w)
	receive(t, entered)
	w.Close()
	if err := Invalidate(w); !errors.Is(err, ErrClosed) {
		t.Fatalf("invalidate while closing: %v", err)
	}
	close(release)
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, w.Done())
	if n := presented.Load(); n != 0 {
		t.Fatalf("closed window's frame presented %d times", n)
	}
	if c.released.Load() != 1 || len(b.Violations()) != 0 {
		t.Fatalf("released=%d violations=%v", c.released.Load(), b.Violations())
	}
}

func TestHiddenWindowSuspendsDeadlinesAndRestoresOnce(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	c.animate.Store(true)
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	receive(t, c.frames)
	tm := receive(t, clock.timers)
	r.show(t, false, ws[0]) // minimized/unmapped
	r.turn(t, h.app)
	clock.idle.Store(true)
	// The animation deadline is long past and the window is dirty, yet a
	// hidden window renders nothing.
	clock.advance(3 * time.Second)
	if err := Invalidate(ws[0]); err != nil {
		t.Fatal(err)
	}
	r.turn(t, h.app)
	noFrame(t, c)
	if s, _ := InspectContent(ws[0]); s.Generation == s.Consumed {
		t.Fatalf("dirty mark lost while hidden: %+v", s)
	}
	eventually(t, "hidden window's deadline timer to stop", tm.stopped.Load)
	clock.idle.Store(false)
	c.animate.Store(false)
	r.show(t, true, ws[0])
	req := receive(t, c.frames)
	if !req.Now.Equal(clock.Now()) {
		t.Fatal("restoration did not render at current monotonic time")
	}
	r.turn(t, h.app)
	r.turn(t, h.app)
	noFrame(t, c) // exactly one frame for elapsed animation and the dirty mark
	closeAll(t, h, ws...)
}

func TestFramesAreFairAndBoundedPerDispatchTurn(t *testing.T) {
	b, clock := &fake.Backend{}, newFrameClock(t)
	var turns atomic.Int64
	b.AfterEvent = func(e platform.Event) {
		if e.Kind == platform.Wake {
			turns.Add(1)
		}
	}
	type record struct {
		window int
		turn   int64
	}
	var mu sync.Mutex
	var order []record
	contents := []*fakeContent{newFakeContent(), newFakeContent()}
	var ws []*Window
	for i, c := range contents {
		var n atomic.Int32
		c.onFrame = func(FrameRequest) error {
			mu.Lock()
			order = append(order, record{i, turns.Load()})
			mu.Unlock()
			// Requested while rendering: must wait for a later turn.
			if n.Add(1) < 6 {
				return Invalidate(ws[i])
			}
			return nil
		}
	}
	var ran atomic.Int32
	r := watch(b)
	h := launch(t, quietOptions(WindowApplication), b, clock, func(a *App) error {
		for _, c := range contents {
			w, err := OpenContent(a, WindowOptions{Width: 4, Height: 3}, c)
			if err != nil {
				return err
			}
			ws = append(ws, w)
		}
		for range 20 {
			if err := a.Post(func() { ran.Add(1) }); err != nil {
				return err
			}
		}
		return nil
	})
	receive(t, h.ready)
	r.show(t, true, ws...)
	for range 6 {
		receive(t, contents[0].frames)
		receive(t, contents[1].frames)
	}
	r.turn(t, h.app)
	mu.Lock()
	snapshot := slices.Clone(order)
	mu.Unlock()
	seen := map[record]bool{}
	for _, r := range snapshot {
		if seen[r] {
			t.Fatalf("window %d rendered twice in turn %d: %v", r.window, r.turn, snapshot)
		}
		seen[r] = true
	}
	counts := [2]int{}
	for _, r := range snapshot {
		counts[r.window]++
	}
	if counts != [2]int{6, 6} || ran.Load() != 20 {
		t.Fatalf("frames %v, posted callbacks %d", counts, ran.Load())
	}
	// Rotation: when both render in one turn, the first alternates.
	var firsts []int
	for i := 1; i < len(snapshot); i++ {
		if snapshot[i].turn == snapshot[i-1].turn {
			firsts = append(firsts, snapshot[i-1].window)
		}
	}
	if len(firsts) < 2 || !slices.ContainsFunc(firsts, func(w int) bool { return w == 1 }) || !slices.Contains(firsts, 0) {
		t.Fatalf("no rotation across turns: %v", snapshot)
	}
	closeAll(t, h, ws...)
}

func TestContentFailuresUseCancelAndDrain(t *testing.T) {
	for name, tc := range map[string]struct {
		frame   func(FrameRequest) error
		present func(platform.ID, platform.Image) error
		want    []string
	}{
		"panic":      {frame: func(FrameRequest) error { panic("duplicate key \"gain\"") }, want: []string{"dfx2 ui window", "frame 1", "ui frame panic", "duplicate key", "goroutine"}},
		"render":     {frame: func(FrameRequest) error { return errors.New("render: image exceeds limit") }, want: []string{"frame 1", "exceeds limit"}},
		"allocation": {present: func(platform.ID, platform.Image) error { return errors.New("cairo status 1") }, want: []string{"present", "cairo status 1"}},
	} {
		t.Run(name, func(t *testing.T) {
			b, clock, c := &fake.Backend{OnPresent: tc.present}, newFrameClock(t), newFakeContent()
			c.onFrame = tc.frame
			var other *fakeContent = newFakeContent()
			h, ws, r := openContent(t, b, clock, c, other)
			r.show(t, true, ws[0])
			err := receive(t, h.done)
			for _, want := range tc.want {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error %v lacks %q", err, want)
				}
			}
			for _, content := range []*fakeContent{c, other} {
				if content.released.Load() != 1 {
					t.Fatalf("release count %d", content.released.Load())
				}
			}
			if v := b.Violations(); len(v) != 0 {
				t.Fatal(v)
			}
			if _, _, _, released := b.Snapshot(); !released {
				t.Fatal("dispatch target not released")
			}
		})
	}
}

func TestUnsupportedContentIsExplicitAndNotLatched(t *testing.T) {
	b, c := &fake.Backend{ContentUnsupported: true}, newFakeContent()
	r := watch(b)
	var plain *Window
	h := launch(t, quietOptions(WindowApplication), b, newFrameClock(t), func(a *App) (err error) {
		w, err := OpenContent(a, WindowOptions{Width: 4, Height: 3}, c)
		if w != nil || !errors.Is(err, platform.ErrUnsupportedContent) {
			t.Errorf("OpenContent = %v, %v", w, err)
		}
		plain, err = a.NewWindow(WindowOptions{Width: 4, Height: 3})
		return err
	})
	receive(t, h.ready)
	r.turn(t, h.app)
	if err := plain.SetTitle("ordinary windows keep working"); err != nil {
		t.Fatal(err)
	}
	if c.released.Load() != 1 {
		t.Fatalf("refused content released %d times", c.released.Load())
	}
	closeAll(t, h, plain)
}

// The supervisor may evaluate late, after a deadline passed with no dispatch
// turn pending. An armed timer must keep its (possibly unread) fire, and a
// deadline that was never armed must wake the UI thread at once. Dropping it
// in either case leaves nothing to deliver the frame.
func TestLateDeadlineEvaluationNeverDropsTheFrame(t *testing.T) {
	setup := func(t *testing.T) (*App, *fake.Backend, *frameClock) {
		clock := newFrameClock(t)
		b := &fake.Backend{}
		if err := b.Open(func(platform.Event) {}); err != nil {
			t.Fatal(err)
		}
		a := &App{backend: b, clock: clock, windows: make(map[platform.ID]*Window), changed: make(chan struct{}, 1)}
		w := &Window{app: a, owner: owner{id: 1, state: open}}
		w.attachment = &attachment{surface: platform.Surface{Width: 4, Height: 3, Scale: 1, Visible: true},
			generation: 1, consumed: 1, deadline: clock.Now().Add(frameInterval)}
		a.windows[1] = w
		return a, b, clock
	}
	t.Run("armed", func(t *testing.T) {
		a, b, clock := setup(t)
		var frame timer
		var at time.Time
		a.mu.Lock()
		a.scheduleFrameLocked(&frame, &at)
		a.mu.Unlock()
		armed := receive(t, clock.timers)
		clock.advance(time.Second) // descheduled past the deadline
		a.mu.Lock()
		a.scheduleFrameLocked(&frame, &at)
		a.mu.Unlock()
		if armed.stopped.Load() || frame == nil {
			t.Fatal("late evaluation discarded the armed timer and its pending fire")
		}
		if _, wakes, _, _ := b.Snapshot(); wakes != 0 {
			t.Fatalf("woke %d times while the timer still owns delivery", wakes)
		}
	})
	t.Run("unarmed", func(t *testing.T) {
		a, b, clock := setup(t)
		var frame timer
		var at time.Time
		clock.advance(time.Second) // first evaluation already after the deadline
		a.mu.Lock()
		a.scheduleFrameLocked(&frame, &at)
		a.scheduleFrameLocked(&frame, &at) // re-evaluation neither wakes again nor arms
		a.mu.Unlock()
		if _, wakes, _, _ := b.Snapshot(); wakes != 1 || frame != nil {
			t.Fatalf("wakes=%d timer=%v, want one wake and no timer", wakes, frame)
		}
		select {
		case tm := <-clock.timers:
			t.Fatalf("armed %v for a due deadline", tm.d)
		default:
		}
	})
}

// A stalled content release is unfinished cleanup: the window stays counted,
// the independent diagnostics name it, and Done and Run wait for it.
func TestStalledContentReleaseStaysVisibleToDiagnostics(t *testing.T) {
	b, clock, c := &fake.Backend{}, newClock(), newFakeContent()
	entered, release := make(chan struct{}), make(chan struct{})
	c.onRelease = func() { close(entered); <-release }
	logs := make(chan string, 10)
	options := Options{Mode: WindowApplication, Logf: func(format string, args ...any) { logs <- fmt.Sprintf(format, args...) }}
	var w *Window
	h := launch(t, options, b, clock, func(a *App) (err error) {
		w, err = OpenContent(a, WindowOptions{Width: 4, Height: 3}, c)
		return err
	})
	receive(t, h.ready)
	w.Close()
	receive(t, entered) // native teardown is acknowledged; the UI thread is inside Release
	if _, _, _, released := b.Snapshot(); released {
		t.Fatal("dispatch target released during content release")
	}
	if s := Inspect(h.app); s.Windows != 1 || s.Terminal {
		t.Fatalf("stalled release reported drained: %+v", s)
	}
	pending(t, w.Done())
	resultPending(t, h.done)
	clock.next(t, diagnosticDelay).fire()
	if msg := receive(t, logs); !strings.Contains(msg, "windows=1") || !strings.Contains(msg, "content-release-pending=true") {
		t.Fatalf("diagnostic omitted the stalled release: %s", msg)
	}
	close(release)
	receive(t, w.Done())
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if s := Inspect(h.app); s.Windows != 0 || c.released.Load() != 1 {
		t.Fatalf("after release: %+v released=%d", s, c.released.Load())
	}
}

// A release panic during teardown after an earlier failure is a distinct
// cleanup failure: Run keeps the original failure and the release panic with
// its stack.
func TestReleasePanicAfterFailureIsRetained(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	c.onFrame = func(FrameRequest) error { panic("frame boom") }
	c.onRelease = func() { panic("release boom") }
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	err := receive(t, h.done)
	for _, want := range []string{"ui frame panic: frame boom", "ui release panic: release boom", "(*fakeContent).Release"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Run error lacks %q:\n%v", want, err)
		}
	}
}

// A refused content window was a live starting owner while its creation was
// pending, so it postponed last-window shutdown. Its refusal must reevaluate
// that rule: window mode shuts down, resident mode stays alive.
//
// The trigger is the ordinary window's destruction acknowledgment delivered
// reentrantly during the refused Create, which the backend contract permits.
// Merely closing the ordinary window then does not discriminate: its later
// acknowledgment reevaluates the rule itself and masks the defect.
func TestUnsupportedRefusalParticipatesInLastWindowShutdown(t *testing.T) {
	for _, mode := range []Mode{WindowApplication, ResidentApplication} {
		t.Run([]string{"window", "resident"}[mode], func(t *testing.T) {
			b, c := &fake.Backend{ContentUnsupported: true}, newFakeContent()
			r := watch(b)
			var plain *Window
			b.BeforeCreate = func(id platform.ID) {
				if plain == nil || id == plain.id {
					return
				}
				b.Emit(platform.Event{Kind: platform.Destroyed, Window: plain.id})
				if plain.app.ctx.Err() != nil {
					t.Error("application shut down while a starting window was live")
				}
			}
			h := launch(t, quietOptions(mode), b, newFrameClock(t), func(a *App) (err error) {
				plain, err = a.NewWindow(WindowOptions{Width: 4, Height: 3})
				return err
			})
			receive(t, h.ready)
			refused := make(chan error, 1)
			if err := h.app.Post(func() {
				_, err := OpenContent(h.app, WindowOptions{Width: 4, Height: 3}, c)
				refused <- err
			}); err != nil {
				t.Fatal(err)
			}
			if err := receive(t, refused); !errors.Is(err, platform.ErrUnsupportedContent) {
				t.Fatalf("OpenContent: %v", err)
			}
			if mode == WindowApplication {
				if err := receive(t, h.done); err != nil {
					t.Fatalf("Run: %v", err)
				}
				if h.app.Context().Err() == nil || c.released.Load() != 1 {
					t.Fatalf("app context %v, released %d", h.app.Context().Err(), c.released.Load())
				}
				return
			}
			receive(t, plain.Done())
			r.turn(t, h.app)
			if s := Inspect(h.app); s.Windows != 0 || s.Closing || h.app.Context().Err() != nil || c.released.Load() != 1 {
				t.Fatalf("resident app with zero windows: %+v ctx=%v released=%d", s, h.app.Context().Err(), c.released.Load())
			}
			resultPending(t, h.done)
			h.app.Quit()
			if err := receive(t, h.done); err != nil {
				t.Fatalf("Run after Quit: %v", err)
			}
		})
	}
}
