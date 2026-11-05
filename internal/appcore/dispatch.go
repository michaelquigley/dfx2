package appcore

import (
	"fmt"

	"github.com/michaelquigley/dfx2/internal/platform"
)

func (a *App) dispatch() {
	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return
	}
	// Producers and this consumer use the same lock to transfer wake ownership.
	// A producer arriving during a callback will queue the next dispatch turn.
	a.wakePending = false
	a.mu.Unlock()
	a.drainInternal()
	// A single turn has a global budget of 64 callbacks. Cross-owner order is
	// unspecified; rotate through owners so a busy application cannot starve a
	// window's queue. Queues themselves remain FIFO.
	a.mu.Lock()
	owners := []*owner{&a.owner}
	for _, w := range a.windows {
		owners = append(owners, &w.owner)
	}
	a.mu.Unlock()
	for budget := 64; budget > 0; {
		progress := false
		for _, o := range owners {
			if budget == 0 {
				break
			}
			a.mu.Lock()
			var f func()
			if o.state < closing && o.ctx.Err() == nil && len(o.queue) > 0 {
				f = o.queue[0]
				o.queue[0] = nil
				o.queue = o.queue[1:]
			}
			a.mu.Unlock()
			if f == nil {
				continue
			}
			// The lock's dequeue is the callback's start/close linearization
			// point. No native call or application function runs under that lock.
			err := invoke("posted callback", func() error { f(); return nil })
			a.mu.Lock()
			a.failLocked(err)
			a.mu.Unlock()
			budget--
			progress = true
		}
		if !progress {
			break
		}
	}
	a.frames()
	a.drainInternal()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state >= closing && len(a.windows) == 0 && len(a.tasks) == 0 && len(a.operations) == 0 && a.wakeFailures == 0 {
		a.terminal = true
		a.state = closed
		a.signalLocked()
		return
	}
	if a.pendingLocked() {
		a.notifyLocked()
	}
}

func (a *App) drainInternal() {
	// Initiate native teardown outside the mutex: Destroy may synchronously
	// deliver destruction and completion callbacks back into event.
	for {
		a.mu.Lock()
		var target *Window
		for _, w := range a.windows {
			if w.state == closing && !w.creating && w.native && !w.destroyStarted {
				target, w.destroyStarted = w, true
				break
			}
		}
		a.mu.Unlock()
		if target == nil {
			break
		}
		err := a.backend.Destroy(target.id)
		a.mu.Lock()
		a.cleanupLocked(err)
		a.mu.Unlock()
	}
	for {
		a.mu.Lock()
		var id platform.ID
		var op *completion
		for candidate, pending := range a.operations {
			if pending.ready && !pending.settling {
				id, op = candidate, pending
				op.settling = true
				break
			}
		}
		isClosing := false
		if op != nil {
			isClosing = a.windows[op.window].state >= closing
		}
		a.mu.Unlock()
		if op == nil {
			break
		}
		var err error
		if op.settle != nil {
			err = invoke("native completion", func() error { return op.settle(isClosing) })
		}
		a.mu.Lock()
		a.cleanupLocked(op.err)
		a.cleanupLocked(err)
		delete(a.operations, id)
		a.mu.Unlock()
	}
	a.mu.Lock()
	for id, t := range a.tasks {
		if t.finished {
			delete(a.tasks, id)
		}
	}
	var finished []*Window
	for id, w := range a.windows {
		for taskID, t := range w.tasks {
			if t.finished {
				delete(w.tasks, taskID)
			}
		}
		if w.state != closing || w.creating || w.native || w.releasing || len(w.tasks) != 0 {
			continue
		}
		pending := false
		for _, op := range a.operations {
			if op.window == id {
				pending = true
				break
			}
		}
		if pending {
			continue
		}
		// Stay registered, and visible to diagnostics, until content release
		// returns: a stalled release is unfinished cleanup, not a drained window.
		w.releasing = true
		finished = append(finished, w)
	}
	a.mu.Unlock()
	// Content is released after native teardown and its final callback, on the
	// UI thread and outside the mutex. Only then is the window removed and Done
	// closed.
	for _, w := range finished {
		var err error
		if w.attachment != nil {
			err = invoke("ui release", func() error { w.attachment.impl.Release(); return nil })
		}
		a.mu.Lock()
		// A release failure is a cleanup failure: keep it alongside any
		// earlier application failure rather than behind it.
		a.cleanupLocked(err)
		w.state = closed
		delete(a.windows, w.id)
		a.mu.Unlock()
		close(w.done)
	}
}

func (a *App) pendingLocked() bool {
	if len(a.queue) != 0 {
		return true
	}
	now := a.clock.Now()
	for _, t := range a.tasks {
		if t.finished {
			return true
		}
	}
	for _, op := range a.operations {
		if op.ready {
			return true
		}
	}
	for _, w := range a.windows {
		if len(w.queue) != 0 || (w.state == closing && !w.creating && w.native && !w.destroyStarted) || a.frameDueLocked(w, now) {
			return true
		}
		for _, t := range w.tasks {
			if t.finished {
				return true
			}
		}
	}
	return false
}

// beginCompletion is the private native-operation seam. Admit before starting
// native work, even if that work may complete reentrantly. The callback must
// release a late result when isClosing, and may only attach it while open.
// It is a UI-thread integration point, not a public browser/content API.
func (a *App) beginCompletion(w *Window, settle func(isClosing bool) error) (platform.ID, error) {
	if !a.backend.IsOwner() {
		return 0, ErrWrongThread
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if w.app != a || w.state == closed {
		return 0, fmt.Errorf("dfx2 native completion: %w", ErrClosed)
	}
	id := a.nextLocked()
	a.operations[id] = &completion{window: w.id, settle: settle}
	return id, nil
}
