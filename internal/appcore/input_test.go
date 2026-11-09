package appcore

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

func inputOf(w *Window, in platform.Input) platform.Event {
	return platform.Event{Kind: platform.InputReceived, Window: w.id, Input: in}
}

func move(y float64) platform.Input { return platform.Input{Kind: platform.PointerMove, Y: y} }

var (
	leftDown = platform.Input{Kind: platform.PointerDown, Button: platform.ButtonLeft}
	leftUp   = platform.Input{Kind: platform.PointerUp, Button: platform.ButtonLeft}
	spaceKey = platform.Input{Kind: platform.KeyDown, Key: platform.KeySpace}
)

func queueInput(t *testing.T, b *fake.Backend, h harness, r *barrier, w *Window, inputs ...platform.Input) {
	t.Helper()
	for _, in := range inputs {
		b.Enqueue(inputOf(w, in))
	}
	r.turn(t, h.app)
}

func TestAdjacentMotionCoalescesButNeverAcrossEdges(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	h, ws, r := openContent(t, b, clock, c)
	// Hidden keeps the queue undrained until every event has arrived.
	r.show(t, false, ws[0])
	queueInput(t, b, h, r, ws[0], leftDown, move(1), move(2), leftUp, move(3), move(4), spaceKey, move(5))
	noFrame(t, c)
	r.show(t, true, ws[0])
	req := receive(t, c.frames)
	want := []platform.Input{leftDown, move(2), leftUp, move(4), spaceKey, move(5)}
	if !slices.Equal(req.Input, want) {
		t.Fatalf("queued input %+v, want %+v", req.Input, want)
	}
	closeAll(t, h, ws...)
}

func TestMotionNeverRewritesAnEventLentToAFrame(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, false, ws[0])
	queueInput(t, b, h, r, ws[0], move(1))
	first := true
	c.onFrame = func(FrameRequest) error {
		if first {
			first = false
			b.Emit(inputOf(ws[0], move(2))) // a native message pumped during the frame
		}
		return nil
	}
	r.show(t, true, ws[0])
	if got := receive(t, c.frames); !slices.Equal(got.Input, []platform.Input{move(1)}) {
		t.Fatalf("first frame read %+v", got.Input)
	}
	if got := receive(t, c.frames); !slices.Equal(got.Input, []platform.Input{move(2)}) {
		t.Fatalf("second frame read %+v: motion after the lent event was lost", got.Input)
	}
	closeAll(t, h, ws...)
}

func TestUnconsumedInputWaitsForALaterFrame(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	c.consume = func(FrameRequest) int { return 1 }
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, false, ws[0])
	queueInput(t, b, h, r, ws[0], leftDown, leftUp, spaceKey)
	r.show(t, true, ws[0])
	for _, want := range [][]platform.Input{{leftDown, leftUp, spaceKey}, {leftUp, spaceKey}, {spaceKey}} {
		if got := receive(t, c.frames); !slices.Equal(got.Input, want) {
			t.Fatalf("frame read %+v, want %+v", got.Input, want)
		}
	}
	r.turn(t, h.app)
	noFrame(t, c)
	if s, _ := InspectContent(ws[0]); s.InputQueued != 0 {
		t.Fatalf("%d events still queued", s.InputQueued)
	}
	closeAll(t, h, ws...)
}

// A frame reporting no progress would be rendered again at once, forever.
func TestContentThatConsumesNoInputFails(t *testing.T) {
	for _, consumed := range []int{0, -1, 2} {
		t.Run(fmt.Sprint(consumed), func(t *testing.T) {
			b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
			c.consume = func(FrameRequest) int { return consumed }
			h, ws, r := openContent(t, b, clock, c)
			r.show(t, false, ws[0])
			queueInput(t, b, h, r, ws[0], spaceKey)
			r.show(t, true, ws[0])
			err := receive(t, h.done)
			if err == nil || !strings.Contains(err.Error(), "input events") || !strings.Contains(err.Error(), fmt.Sprintf("window %d frame", ws[0].id)) {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}

func TestOverflowDiscardsPendingInputReportsOnceAndRequestsAFrame(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	r := watch(b)
	var mu sync.Mutex
	var logged []string
	options := quietOptions(WindowApplication)
	options.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	var w *Window
	h := launch(t, options, b, clock, func(a *App) (err error) {
		w, err = OpenContent(a, WindowOptions{Width: 4, Height: 3}, c)
		return err
	})
	receive(t, h.ready)
	r.show(t, false, w)
	var inputs []platform.Input
	for range inputLimit {
		inputs = append(inputs, spaceKey)
	}
	queueInput(t, b, h, r, w, inputs...)
	if s, _ := InspectContent(w); s.InputQueued != inputLimit || s.InputOverflows != 0 {
		t.Fatalf("at the limit: %+v", s)
	}
	queueInput(t, b, h, r, w, leftDown) // the 257th
	if s, _ := InspectContent(w); s.InputQueued != 0 || s.InputOverflows != 1 {
		t.Fatalf("after overflow: %+v", s)
	}
	queueInput(t, b, h, r, w, leftUp) // events after the overflow survive
	eventually(t, "the overflow diagnostic", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(logged) > 0
	})
	r.show(t, true, w)
	req := receive(t, c.frames)
	if !req.InputLost || !slices.Equal(req.Input, []platform.Input{leftUp}) {
		t.Fatalf("frame after overflow: lost=%t input=%+v", req.InputLost, req.Input)
	}
	r.turn(t, h.app)
	noFrame(t, c)
	mu.Lock()
	if len(logged) != 1 || !strings.Contains(logged[0], fmt.Sprintf("window %d", w.id)) || !strings.Contains(logged[0], "overflowed") {
		t.Fatalf("diagnostics %q", logged)
	}
	mu.Unlock()
	// A later frame does not report the old loss again.
	if err := Invalidate(w); err != nil {
		t.Fatal(err)
	}
	if req := receive(t, c.frames); req.InputLost {
		t.Fatal("overflow reported twice")
	}
	closeAll(t, h, w)
}

func TestOverflowAloneStillRequestsAFrame(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	receive(t, c.frames)
	id := ws[0].id
	if err := ws[0].Post(func() {
		for range inputLimit + 1 {
			b.Emit(inputOf(ws[0], spaceKey))
		}
	}); err != nil {
		t.Fatal(err)
	}
	req := receive(t, c.frames)
	if !req.InputLost || len(req.Input) != 0 {
		t.Fatalf("window %d: overflow with nothing after it: %+v", id, req)
	}
	closeAll(t, h, ws...)
}

func TestEachOverflowIsReportedForItsOwnWindowOnce(t *testing.T) {
	b, clock := &fake.Backend{}, newFrameClock(t)
	r := watch(b)
	var mu sync.Mutex
	var logged []string
	options := quietOptions(WindowApplication)
	options.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	var first, second *Window
	h := launch(t, options, b, clock, func(a *App) (err error) {
		if first, err = OpenContent(a, WindowOptions{Width: 4, Height: 3}, newFakeContent()); err != nil {
			return err
		}
		second, err = OpenContent(a, WindowOptions{Width: 4, Height: 3}, newFakeContent())
		return err
	})
	receive(t, h.ready)
	overflow := func(w *Window, want int) {
		t.Helper()
		var inputs []platform.Input
		for range inputLimit + 1 {
			inputs = append(inputs, spaceKey)
		}
		queueInput(t, b, h, r, w, inputs...)
		eventually(t, "the diagnostic", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(logged) == want
		})
	}
	overflow(first, 1)
	overflow(second, 2)
	r.turn(t, h.app)
	mu.Lock()
	if len(logged) != 2 || !strings.Contains(logged[0], fmt.Sprintf("window %d", first.id)) || !strings.Contains(logged[1], fmt.Sprintf("window %d", second.id)) {
		t.Fatalf("diagnostics %q", logged)
	}
	mu.Unlock()
	closeAll(t, h, first, second)
}

func TestOverflowDuringAFrameCannotConsumeWhatArrivedAfterIt(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, false, ws[0])
	queueInput(t, b, h, r, ws[0], leftDown)
	first := true
	c.onFrame = func(FrameRequest) error {
		if first {
			first = false
			for range inputLimit - 1 { // with the lent event, the limit
				b.Emit(inputOf(ws[0], spaceKey))
			}
			b.Emit(inputOf(ws[0], leftUp)) // overflows
			b.Emit(inputOf(ws[0], move(7)))
		}
		return nil
	}
	r.show(t, true, ws[0])
	if got := receive(t, c.frames); got.InputLost || !slices.Equal(got.Input, []platform.Input{leftDown}) {
		t.Fatalf("frame in progress: %+v", got)
	}
	// The frame "consumed" one event, which is no longer in the queue. Removing
	// it blindly would eat the motion that arrived after the overflow.
	got := receive(t, c.frames)
	if !got.InputLost || !slices.Equal(got.Input, []platform.Input{move(7)}) {
		t.Fatalf("next frame: %+v", got)
	}
	closeAll(t, h, ws...)
}

func TestClosedWindowDropsInput(t *testing.T) {
	b, clock, c, other := &fake.Backend{DelayDestroy: true}, newFrameClock(t), newFakeContent(), newFakeContent()
	h, ws, r := openContent(t, b, clock, c, other) // the other window keeps the application open
	r.show(t, true, ws[0])
	receive(t, c.frames)
	ws[0].Close()
	queueInput(t, b, h, r, ws[0], spaceKey)
	noFrame(t, c)
	if s, _ := InspectContent(ws[0]); s.InputQueued != 0 {
		t.Fatalf("closing window queued input: %+v", s)
	}
	ws[1].Close()
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: ws[0].id})
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: ws[1].id})
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseCaptureFollowsAFrameUnlessAPressIsQueued(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	c.consume = func(FrameRequest) int { return 1 }
	c.capture = func(req FrameRequest) bool { return req.Input[0] == spaceKey }
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, false, ws[0])
	queueInput(t, b, h, r, ws[0], spaceKey, leftDown)
	r.show(t, true, ws[0])
	receive(t, c.frames)
	receive(t, c.frames)
	r.turn(t, h.app)
	if got := b.CaptureReleases(ws[0].id); got != 0 {
		t.Fatalf("released %d times, breaking the queued press's capture", got)
	}
	queueInput(t, b, h, r, ws[0], spaceKey)
	receive(t, c.frames)
	r.turn(t, h.app)
	if got := b.CaptureReleases(ws[0].id); got != 1 {
		t.Fatalf("released %d times, want 1", got)
	}
	closeAll(t, h, ws...)
}

func TestReleaseCaptureFailureIsFatalWithContext(t *testing.T) {
	fault := errors.New("capture fault")
	b, clock, c := &fake.Backend{ReleaseCaptureError: fault}, newFrameClock(t), newFakeContent()
	c.capture = func(FrameRequest) bool { return true }
	h, ws, r := openContent(t, b, clock, c)
	r.show(t, true, ws[0])
	err := receive(t, h.done)
	if !errors.Is(err, fault) || !strings.Contains(err.Error(), "release capture") || !strings.Contains(err.Error(), "frame 1") {
		t.Fatalf("Run: %v", err)
	}
}

// Closing during a frame seals it: the native window is on its way out, and
// destruction ends any capture it held.
func TestClosingFrameDoesNotReleaseCapture(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	c.capture = func(FrameRequest) bool { return true }
	h, ws, r := openContent(t, b, clock, c)
	c.onFrame = func(FrameRequest) error { ws[0].Close(); return nil }
	r.show(t, true, ws[0])
	receive(t, c.frames)
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if got := b.CaptureReleases(ws[0].id); got != 0 {
		t.Fatalf("released %d times on a closing window", got)
	}
}
