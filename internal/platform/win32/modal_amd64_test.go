package win32

import (
	"errors"
	"slices"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// These run the backend's guards against a modeled size/move loop. They cannot
// show that Windows dispatches a message-only window's timer inside its real
// loop, or how that loop ends; the native audition carries those.

func (f *systemModel) enter(w *window) { windowMessage(&f.api, w.hwnd, wmEnterSizeMove, 0, 0) }
func (f *systemModel) exit(w *window)  { windowMessage(&f.api, w.hwnd, wmExitSizeMove, 0, 0) }

// loop dispatches queued messages as a modal loop would, without Pump.
func (f *systemModel) loop() {
	for {
		f.queueMu.Lock()
		empty := len(f.queue) == 0
		var m message
		if !empty {
			m = f.queue[0]
			f.queue = f.queue[1:]
		}
		f.queueMu.Unlock()
		if empty {
			return
		}
		f.api.dispatch(&m)
	}
}

func (f *systemModel) armed(t *testing.T) uintptr {
	t.Helper()
	if len(f.timers) != 1 {
		t.Fatalf("armed timers: %v", f.timers)
	}
	for id := range f.timers {
		return id
	}
	return 0
}

// fire delivers the armed timer's message; killing it is the handler's job.
func (f *systemModel) fire(t *testing.T) {
	t.Helper()
	id := f.armed(t)
	f.api.dispatch(&message{hwnd: f.b.application.hwnd, id: wmTimer, wparam: id})
	if _, still := f.timers[id]; still {
		t.Fatal("one-shot timer survived its handler")
	}
}

// sinkTurns counts Wake deliveries and fails on any that nests.
func sinkTurns(t *testing.T, f *systemModel, body func(turn int)) *int {
	t.Helper()
	turns, depth := new(int), 0
	f.b.sink = func(e platform.Event) {
		f.events = append(f.events, e)
		if e.Kind != platform.Wake {
			return
		}
		if depth++; depth > 1 {
			t.Error("core turn nested inside a core turn")
		}
		*turns++
		if body != nil {
			body(*turns)
		}
		depth--
	}
	return turns
}

func TestIdleSizeMoveArmsNoTimer(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	f.enter(w)
	if c := f.b.Counters(); !c.ModalActive || c.ModalLoops != 1 {
		t.Fatalf("loop not tracked: %+v", c)
	}
	f.loop()
	f.exit(w)
	if c := f.b.Counters(); c.ModalActive || c.ModalTimers != 0 || c.ModalTurns != 0 || len(f.timers) != 0 {
		t.Fatalf("idle loop did work: %+v timers %v", c, f.timers)
	}
}

func TestWakeSchedulesAOneShotAndNeverRunsFromItsOwnHandler(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	var order []string
	turns := sinkTurns(t, f, func(turn int) {
		order = slices.Clone(f.trace)
		if turn == 1 { // work left over: one more wake
			if err := b.Wake(); err != nil {
				t.Error(err)
			}
		}
	})
	f.enter(w)
	f.trace = nil
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	if *turns != 0 {
		t.Fatal("WM_APP ran a core turn inside the window procedure")
	}
	if id := f.armed(t); f.timers[id] != modalDelay {
		t.Fatalf("delay %d", f.timers[id])
	}
	f.fire(t)
	if *turns != 1 || !slices.Equal(order, []string{"arm", "kill"}) {
		t.Fatalf("turns %d, order before turn %v", *turns, order)
	}
	if len(f.timers) != 0 {
		t.Fatal("re-armed before its wake message arrived")
	}
	f.loop() // the leftover wake's message arms the next one-shot
	f.fire(t)
	if *turns != 2 {
		t.Fatalf("turns %d", *turns)
	}
	f.loop()
	if len(f.timers) != 0 {
		t.Fatalf("settled loop kept a timer: %v", f.timers)
	}
	f.exit(w)
	if c := b.Counters(); c.ModalTurns != 2 || c.ModalTimers != 2 || c.ModalRefusals != 0 {
		t.Fatalf("counters %+v", c)
	}
}

func TestWakeLatchedBeforeTheLoopArmsOnEntry(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop() // latched, not yet delivered by a pump
	if len(f.timers) != 0 {
		t.Fatal("timer armed outside a loop")
	}
	f.enter(w)
	f.fire(t)
	if *turns != 1 {
		t.Fatalf("turns %d", *turns)
	}
	f.exit(w)
}

func TestLeavingTheLoopHandsTheLatchedWakeToPump(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.armed(t)
	f.exit(w)
	if len(f.timers) != 0 || *turns != 0 {
		t.Fatalf("exit left timers %v turns %d", f.timers, *turns)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if *turns != 1 {
		t.Fatalf("Pump turns %d", *turns)
	}
	if b.Counters().ModalTurns != 0 {
		t.Fatal("ordinary delivery counted as modal")
	}
}

func TestPumpServicingThatEntersTheLoopDoesNotRepeatASpentWake(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	// Pump's bounded servicing dispatches a nonclient press whose DefWindowProc
	// runs the whole loop, inside which the timer spends the latched wake.
	dispatch := f.api.dispatch
	f.api.dispatch = func(m *message) {
		if m.id == wmNCMouseFirst {
			f.enter(w)
			f.fire(t)
			f.exit(w)
			return
		}
		dispatch(m)
	}
	f.enqueue(message{hwnd: w.hwnd, id: wmNCMouseFirst})
	var woke bool
	f.api.getMessage = func(m *message) (int32, error) {
		woke = true
		*m = message{hwnd: b.application.hwnd, id: wmDestroy}
		return 1, nil
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if *turns != 1 || !woke {
		t.Fatalf("turns %d, pump blocked natively: %t", *turns, woke)
	}
}

func TestLateTimerMessagesAreIgnored(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	stale := f.armed(t)
	f.exit(w)
	late := message{hwnd: b.application.hwnd, id: wmTimer, wparam: stale}
	f.api.dispatch(&late) // KillTimer does not remove a message already queued
	if *turns != 0 || b.Counters().ModalRefusals != 0 {
		t.Fatalf("late timer after exit: turns %d", *turns)
	}
	f.enter(w) // the wake is still latched, so the new loop arms a new timer
	current := f.armed(t)
	if current == stale {
		t.Fatal("timer identity reused")
	}
	f.api.dispatch(&late)
	if *turns != 0 || f.armed(t) != current {
		t.Fatalf("stale message consumed the live timer: turns %d", *turns)
	}
	f.fire(t)
	f.api.dispatch(&message{hwnd: b.application.hwnd, id: wmTimer, wparam: current}) // duplicate
	if *turns != 1 {
		t.Fatalf("turns %d", *turns)
	}
	f.exit(w)
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	f.api.dispatch(&late) // a late timer cannot resurrect anything about a dead window
	if *turns != 1 {
		t.Fatalf("turns %d", *turns)
	}
}

func TestTimerArrivingDuringANativeOperationIsRefusedAndRetained(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	// SetWindowTextW reenters the window procedure; model a timer delivered
	// inside that reentry.
	var insideTurns = -1
	f.api.setTitle = func(uintptr, *uint16) error {
		f.fire(t)
		insideTurns = *turns
		return nil
	}
	if err := b.SetTitle(7, "reentrant"); err != nil {
		t.Fatal(err)
	}
	if insideTurns != 0 {
		t.Fatalf("a turn ran inside a native call: %d", insideTurns)
	}
	if c := b.Counters(); c.ModalRefusals != 1 {
		t.Fatalf("refusal not counted: %+v", c)
	}
	f.fire(t) // re-armed at the safe point after the operation
	if *turns != 1 {
		t.Fatalf("wake lost after refusal: %d", *turns)
	}
	f.exit(w)
}

func TestWakeDuringATurnArmsOnlyWhenTheTurnEnds(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, func(turn int) {
		if turn == 1 {
			if err := b.Wake(); err != nil {
				t.Error(err)
			}
			f.loop()
			if len(f.timers) != 0 {
				t.Error("timer armed inside a running turn")
			}
		}
	})
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.fire(t)
	f.fire(t) // armed at the end of the first turn, from the retained wake
	if *turns != 2 {
		t.Fatalf("turns %d", *turns)
	}
	f.exit(w)
}

func TestCreationFromAModalTurnDefersWakesItsReentryLatches(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	var created bool
	turns := sinkTurns(t, f, func(turn int) {
		if turn != 1 {
			return
		}
		// A posted callback opens a window during the drag. Creation pumps a
		// wake through the window procedure while WM_CREATE is on the stack.
		f.api.getDPI = func(uintptr) (uint32, error) {
			if err := b.Wake(); err != nil {
				t.Error(err)
			}
			f.loop()
			if len(f.timers) != 0 {
				t.Error("timer armed inside native creation")
			}
			return 96, nil
		}
		if err := b.Create(8, platform.WindowOptions{Width: 50, Height: 50, Content: true}); err != nil {
			t.Error(err)
		}
		f.api.getDPI = func(uintptr) (uint32, error) { return 96, nil }
		created = true
	})
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.fire(t)
	if !created || *turns != 1 {
		t.Fatalf("created=%t turns=%d", created, *turns)
	}
	f.fire(t) // scheduled once creation and the turn had both finished
	if *turns != 2 {
		t.Fatalf("deferred wake lost: %d", *turns)
	}
	f.exit(w)
}

func TestLoopInsideNativeCreationDeliversNoTurn(t *testing.T) {
	f := model(t)
	b := f.b
	turns := sinkTurns(t, f, nil)
	f.api.getDPI = func(uintptr) (uint32, error) {
		w := b.windows[7]
		f.enter(w)
		if err := b.Wake(); err != nil {
			t.Error(err)
		}
		f.loop()
		if len(f.timers) != 0 || *turns != 0 {
			t.Errorf("timers %v turns %d inside creation", f.timers, *turns)
		}
		f.exit(w)
		return 96, nil
	}
	openContent(t, f, 7, 100, 100)
	if *turns != 0 {
		t.Fatalf("turns %d", *turns)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if *turns != 1 {
		t.Fatalf("Pump turns %d", *turns)
	}
}

func TestDestroyingTheLoopOwnerFromAModalTurnWaitsForThePump(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	other := openContent(t, f, 8, 100, 100)
	if err := b.Present(7, solid(4, 3, 16, 1)); err != nil {
		t.Fatal(err)
	}
	var acknowledged []platform.ID
	var insideTurn bool
	turns := sinkTurns(t, f, func(turn int) {
		if turn != 1 {
			return
		}
		insideTurn = true
		if err := b.Destroy(7); err != nil {
			t.Error(err)
		}
		if err := b.Wake(); err != nil { // the core still has work for window 8
			t.Error(err)
		}
		f.loop()
		insideTurn = false
	})
	sink := b.sink
	b.sink = func(e platform.Event) {
		if e.Kind == platform.Destroyed {
			if insideTurn {
				t.Error("acknowledged inside the turn that destroyed it")
			}
			acknowledged = append(acknowledged, e.Window)
		}
		sink(e)
	}
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.fire(t)
	// Windows destroys the window without WM_EXITSIZEMOVE while its loop is
	// still unwinding, beneath this timer's stack.
	if c := b.Counters(); c.ModalActive {
		t.Fatalf("loop still active: %+v", c)
	}
	if len(f.timers) != 0 {
		t.Fatalf("timer survived the loop's window: %v", f.timers)
	}
	if len(acknowledged) != 0 {
		t.Fatalf("acknowledged before the owner's stack unwound: %v", acknowledged)
	}
	if _, ok := routes.Load(w.token); !ok {
		t.Fatal("routing dropped before acknowledgment")
	}
	if f.frees != 0 {
		t.Fatal("retained pixels freed before the owner's stack unwound")
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if *turns != 2 || !slices.Equal(acknowledged, []platform.ID{7}) || f.frees != 1 {
		t.Fatalf("turns=%d acknowledged=%v frees=%d", *turns, acknowledged, f.frees)
	}
	if b.modal.owner != nil {
		t.Fatal("dead loop owner retained after the pump regained control")
	}
	// The next window's loop works normally.
	f.enter(other)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.fire(t)
	f.exit(other)
	if *turns != 3 {
		t.Fatalf("turns %d", *turns)
	}
}

func TestDestroyingAnotherWindowFromAModalTurnAcknowledgesAfterTheTurn(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	other := openContent(t, f, 8, 100, 100)
	if err := b.Present(8, solid(4, 3, 16, 1)); err != nil {
		t.Fatal(err)
	}
	var acknowledged []platform.ID
	var inTurn bool
	sinkTurns(t, f, func(turn int) {
		if turn != 1 {
			return
		}
		inTurn = true
		if err := b.Destroy(8); err != nil {
			t.Error(err)
		}
		inTurn = false
	})
	sink := b.sink
	b.sink = func(e platform.Event) {
		if e.Kind == platform.Destroyed {
			if inTurn {
				t.Error("acknowledged inside the destroying turn")
			}
			acknowledged = append(acknowledged, e.Window)
		}
		sink(e)
	}
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.fire(t)
	if !slices.Equal(acknowledged, []platform.ID{8}) || f.frees != 1 {
		t.Fatalf("acknowledged=%v frees=%d while the other window's loop runs", acknowledged, f.frees)
	}
	if !b.Counters().ModalActive || b.modal.owner != w {
		t.Fatal("destroying another window ended the loop")
	}
	if _, ok := routes.Load(other.token); ok {
		t.Fatal("routing retained after acknowledgment")
	}
	f.exit(w)
}

func TestCloseFromAFrameInAModalTurnPresentsNothingAfterTeardown(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	var late error
	sinkTurns(t, f, func(turn int) {
		if turn != 1 {
			return
		}
		if err := b.Present(7, solid(4, 3, 16, 1)); err != nil {
			t.Error(err)
		}
		windowMessage(&f.api, w.hwnd, wmClose, 0, 0) // sealed by the close notification
		if err := b.Destroy(7); err != nil {
			t.Error(err)
		}
		late = b.Present(7, solid(4, 3, 16, 2))
	})
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.fire(t)
	if late == nil || w.image.pixels[0] != 1 {
		t.Fatalf("present after teardown: %v", late)
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if len(f.storage) != 0 {
		t.Fatalf("%d allocations live", len(f.storage))
	}
}

func TestFailedTimerKeepsTheWakeAndReportsThroughPump(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	boom := errors.New("no timers left")
	f.timerError = boom
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.exit(w)
	if *turns != 0 {
		t.Fatalf("turns %d", *turns)
	}
	if err := b.Pump(); !errors.Is(err, boom) {
		t.Fatalf("pump: %v", err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if *turns != 1 {
		t.Fatalf("wake lost with the failed timer: %d", *turns)
	}
}

func TestLatchedFailureHoldsModalDeliveryUntilPumpReportsIt(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	boom := errors.New("client rect failed")
	f.client = func(uintptr, *rect) error { return boom }
	windowMessage(&f.api, w.hwnd, wmSize, 0, 0)
	f.client = nil
	f.fire(t)
	if *turns != 0 {
		t.Fatal("ordinary callbacks started after a latched native failure")
	}
	f.exit(w)
	if err := b.Pump(); !errors.Is(err, boom) {
		t.Fatalf("pump: %v", err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if *turns != 1 {
		t.Fatalf("wake lost behind the failure: %d", *turns)
	}
}

func TestReleaseStopsAnArmedTimerBeforeTheTargetGoes(t *testing.T) {
	f := model(t)
	b := f.b
	// No path leaves a timer armed at terminal release; this is the backstop
	// behind "timers stop before dispatch-target release".
	b.modal.timer, b.modal.armed = 41, true
	f.timers[41] = modalDelay
	destroy := f.api.destroy
	f.api.destroy = func(hwnd uintptr) error {
		if len(f.timers) != 0 {
			t.Error("dispatch target destroyed with a live timer")
		}
		return destroy(hwnd)
	}
	if err := b.Release(); err != nil {
		t.Fatal(err)
	}
	if b.modal.armed {
		t.Fatal("timer still recorded")
	}
}

func TestConcurrentWakesDuringALoopCoalesceIntoOneTimer(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 100, 100)
	turns := sinkTurns(t, f, nil)
	f.enter(w)
	for range 50 {
		if err := b.Wake(); err != nil {
			t.Fatal(err)
		}
	}
	f.loop()
	f.armed(t)
	f.fire(t)
	f.loop()
	if *turns != 1 || len(f.timers) != 0 {
		t.Fatalf("turns %d timers %v", *turns, f.timers)
	}
	f.exit(w)
}
