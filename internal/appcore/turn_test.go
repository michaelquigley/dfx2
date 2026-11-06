package appcore

import (
	"sync/atomic"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

// A native toolkit's own loop can deliver a Wake while a turn is on the UI
// thread's stack. These emit that Wake reentrantly from each kind of
// application code a turn or setup runs.
var wakeEvent = platform.Event{Kind: platform.Wake}

func TestNestedWakeInsidePostedCallbackIsRefusedAndNotLost(t *testing.T) {
	b, clock := &fake.Backend{}, newFrameClock(t)
	r := watch(b)
	var active, nested atomic.Int32
	inner := make(chan struct{})
	outer := make(chan struct{})
	h := launch(t, quietOptions(ResidentApplication), b, clock, func(*App) error { return nil })
	receive(t, h.ready)
	if err := h.app.Post(func() {
		active.Add(1)
		defer active.Add(-1)
		if err := h.app.Post(func() {
			if active.Load() != 0 {
				nested.Add(1)
			}
			close(inner)
		}); err != nil {
			t.Error(err)
		}
		// The wake that would run the callback above arrives here, during this
		// callback, and is consumed: it is the only one that callback gets.
		if !b.DeliverNext() {
			t.Error("the post queued no wake")
		}
		select {
		case <-inner:
			t.Error("a queued callback ran nested inside a running one")
		default:
		}
		close(outer)
	}); err != nil {
		t.Fatal(err)
	}
	receive(t, outer)
	receive(t, inner) // the closing check re-requested the refused work
	r.turn(t, h.app)
	if nested.Load() != 0 {
		t.Fatal("application callback nested")
	}
	h.app.Quit()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestNestedWakeInsideFrameRendersNoNestedFrame(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	var depth, deepest atomic.Int32
	var w *Window
	c.onFrame = func(req FrameRequest) error {
		d := depth.Add(1)
		defer depth.Add(-1)
		if d > deepest.Load() {
			deepest.Store(d)
		}
		if req.Sequence == 1 {
			if err := Invalidate(w); err != nil {
				return err
			}
			// A frame in progress only marks the invalidation, so a post
			// supplies the wake a native loop then consumes here.
			if err := w.Post(func() {}); err != nil {
				return err
			}
			if !b.DeliverNext() {
				t.Error("the post queued no wake")
			}
		}
		return nil
	}
	h, ws, r := openContent(t, b, clock, c)
	w = ws[0]
	r.show(t, true, ws[0])
	receive(t, c.frames)
	// Only the refused nested delivery's closing check can cause this frame.
	if second := receive(t, c.frames); second.Sequence != 2 {
		t.Fatalf("second frame %+v", second)
	}
	r.turn(t, h.app)
	if deepest.Load() != 1 {
		t.Fatalf("frames nested to depth %d", deepest.Load())
	}
	closeAll(t, h, ws...)
}

func TestNestedWakeInsideSetupRunsNothingUntilItReturns(t *testing.T) {
	b, clock := &fake.Backend{}, newFrameClock(t)
	ran := make(chan struct{})
	var returned atomic.Bool
	h := launch(t, quietOptions(ResidentApplication), b, clock, func(a *App) error {
		if err := a.Post(func() {
			if !returned.Load() {
				t.Error("posted callback ran inside setup")
			}
			close(ran)
		}); err != nil {
			return err
		}
		b.Emit(wakeEvent)
		returned.Store(true)
		return nil
	})
	receive(t, h.ready)
	receive(t, ran)
	h.app.Quit()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

// A view closing its own window during a turn: nothing is presented, teardown
// runs in that same turn's drain, and release follows acknowledgment.
func TestCloseFromAFrameTearsDownInTheSameTurn(t *testing.T) {
	b, clock, c := &fake.Backend{}, newFrameClock(t), newFakeContent()
	var w *Window
	var presents atomic.Int32
	b.OnPresent = func(platform.ID, platform.Image) error {
		presents.Add(1)
		return nil
	}
	destroyed := make(chan platform.ID, 1)
	b.OnDestroy = func(id platform.ID) { destroyed <- id }
	c.onFrame = func(FrameRequest) error {
		w.Close()
		return nil
	}
	h, ws, r := openContent(t, b, clock, c)
	w = ws[0]
	r.show(t, true, w)
	receive(t, c.frames)
	if id := receive(t, destroyed); id != w.id {
		t.Fatalf("destroyed %d", id)
	}
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if presents.Load() != 0 || c.released.Load() != 1 || len(b.Violations()) != 0 {
		t.Fatalf("presents=%d released=%d violations=%v", presents.Load(), c.released.Load(), b.Violations())
	}
}
