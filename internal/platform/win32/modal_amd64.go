package win32

// modalDelay is USER_TIMER_MINIMUM: the positive delay that lets a size/move
// loop service its own input and paint messages between turns.
const modalDelay = 10

// modalState follows DefWindowProc's size/move loop. A public dispatch can
// only run inside that loop from the dispatch window's own one-shot timer, and
// only when no turn is active and no synchronous backend call that can reenter
// the window procedure is unfinished. Everything here is UI-thread state,
// except the counters and active flag, which are read under Backend.mu.
type modalState struct {
	owner    *window // the loop's window; outlives its destruction until Pump regains control
	depth    int     // unfinished native operations
	inTurn   bool    // a core turn is on this stack, from either delivery path
	armed    bool
	timer    uintptr // the armed timer's ID: unique, so a late message cannot pass for it
	active   bool
	loops    uint64
	turns    uint64
	timers   uint64
	refusals uint64
}

// operation brackets a synchronous native call that can reenter the window
// procedure. A wake a guard held back during the call is scheduled at its end.
func (b *Backend) operation(call func()) {
	b.modal.depth++
	call()
	b.modal.depth--
	b.armModal()
}

func (b *Backend) modalSafe() bool {
	m := &b.modal
	return m.owner != nil && !m.owner.destroyed && m.depth == 0 && !m.inTurn
}

// armModal schedules delivery of a latched wake at a safe point. With nothing
// latched it arms nothing, so an idle size/move loop runs no timer. A failed
// arm keeps the wake latched for Pump and reports the failure there.
func (b *Backend) armModal() {
	if b.modal.armed || !b.modalSafe() {
		return
	}
	b.mu.Lock()
	ready := b.ready && !b.closed && b.backendErr == nil
	b.mu.Unlock()
	if !ready {
		return
	}
	id := b.modal.timer + 1
	if err := b.api.setTimer(b.application.hwnd, id, modalDelay); err != nil {
		b.mu.Lock()
		if b.backendErr == nil {
			b.backendErr = err
		}
		b.mu.Unlock()
		return
	}
	b.modal.timer, b.modal.armed = id, true
	b.mu.Lock()
	b.modal.timers++
	b.mu.Unlock()
}

func (b *Backend) stopModalTimer() {
	if b.modal.armed {
		b.api.killTimer(b.application.hwnd, b.modal.timer)
		b.modal.armed = false
	}
}

func (b *Backend) enterSizeMove(w *window) {
	if w.dispatch {
		return
	}
	b.modal.owner = w
	b.mu.Lock()
	b.modal.active = true
	b.modal.loops++
	b.mu.Unlock()
	b.armModal()
}

// exitSizeMove hands any latched wake to Pump, which delivers it as soon as
// this message's stack returns.
func (b *Backend) exitSizeMove(w *window) {
	if b.modal.owner != w {
		return
	}
	b.modal.owner = nil
	b.setModalInactive()
	b.stopModalTimer()
}

// loopOwnerDestroyed ends modal delivery when the loop's window is destroyed,
// usually by the very turn on this stack. Windows then sends no closing
// notification. The owner stays recorded: its loop is still unwinding, so its
// retirement waits until Pump regains control.
func (b *Backend) loopOwnerDestroyed(w *window) {
	if b.modal.owner == w {
		b.setModalInactive()
		b.stopModalTimer()
	}
}

func (b *Backend) setModalInactive() {
	b.mu.Lock()
	b.modal.active = false
	b.mu.Unlock()
}

// modalTimer kills the one-shot on entry, then delivers at most one bounded
// core turn. A turn that leaves work behind requests another wake, whose
// message arms the next one-shot; nothing here re-arms by itself. A refusal
// keeps the wake latched: the turn or operation in progress re-arms when it
// ends, and without a loop Pump delivers it. Late messages are ignored.
func (b *Backend) modalTimer(id uintptr) {
	m := &b.modal
	if !m.armed || id != m.timer {
		return
	}
	b.stopModalTimer()
	if !b.modalSafe() {
		b.mu.Lock()
		m.refusals++
		b.mu.Unlock()
		return
	}
	b.deliver(true)
	if b.modalSafe() {
		// Only the loop's own window can still have a callback stack beneath us.
		b.flushRetired(m.owner)
	}
}
