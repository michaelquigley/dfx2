package appcore

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

const retryDelay = 10 * time.Millisecond
const diagnosticDelay = 5 * time.Second

func (a *App) signalLocked() {
	select {
	case a.changed <- struct{}{}:
	default:
	}
}

// Wake must be called under mu: Release cannot race a producer with an old
// target. Backend.Wake never calls back inline or waits for the UI thread.
func (a *App) notifyLocked() {
	if a.terminal {
		return
	}
	if !a.wakePending && a.wakeFailures == 0 {
		a.wakeLocked()
	}
	a.signalLocked()
}

func (a *App) wakeLocked() {
	if err := a.backend.Wake(); err != nil {
		a.wakePending = false
		a.wakeFailures++
		a.wakeErr, a.wakeLog = err, true
		// One transient failure gets an autonomous retry. A second consecutive
		// failure is persistent: cancel, report, and keep retrying cleanup.
		if a.wakeFailures >= 2 {
			a.failLocked(fmt.Errorf("dfx2 dispatch wake: %w", err))
		}
	} else {
		a.wakePending = true
		a.wakeFailures = 0
	}
}

func (a *App) supervise() {
	defer close(a.supervised)
	var retry, diagnostic, frame timer
	// frameAt is the deadline the frame timer was armed for. It survives the
	// timer firing so an unconsumed deadline is never re-armed in a loop.
	var frameAt time.Time
	defer func() {
		if retry != nil {
			retry.Stop()
		}
		if diagnostic != nil {
			diagnostic.Stop()
		}
		if frame != nil {
			frame.Stop()
		}
	}()
	parentDone := a.parent.Done()
	wakesSealed := false
	for {
		a.mu.Lock()
		if a.terminal && !wakesSealed {
			if retry != nil {
				retry.Stop()
				retry = nil
			}
			if frame != nil {
				frame.Stop()
				frame = nil
			}
			wakesSealed = true
			close(a.wakesStopped)
		}
		if !a.terminal {
			a.scheduleFrameLocked(&frame, &frameAt)
		}
		if !a.terminal && a.wakeFailures > 0 && retry == nil {
			retry = a.clock.NewTimer(retryDelay)
		}
		if a.state >= closing && diagnostic == nil {
			diagnostic = a.clock.NewTimer(diagnosticDelay)
		}
		var wakeMessage string
		if a.wakeLog {
			wakeMessage = fmt.Sprintf("dfx2 wake failure (attempt %d): %v; retry pending", a.wakeFailures, a.wakeErr)
			a.wakeLog = false
		}
		a.mu.Unlock()
		if wakeMessage != "" {
			a.log("%s", wakeMessage)
		}
		var retryC, diagnosticC, frameC <-chan time.Time
		if frame != nil {
			frameC = frame.C()
		}
		if retry != nil {
			retryC = retry.C()
		}
		if diagnostic != nil {
			diagnosticC = diagnostic.C()
		}
		select {
		case <-a.stop:
			return
		case <-parentDone:
			parentDone = nil
			a.mu.Lock()
			a.callerErr = a.parent.Err()
			a.shutdownLocked()
			a.notifyLocked()
			a.mu.Unlock()
		case <-a.changed:
		case <-retryC:
			retry = nil
			a.mu.Lock()
			if !a.terminal && a.wakeFailures > 0 {
				a.wakeLocked()
			}
			a.mu.Unlock()
		case <-frameC:
			// Deliver through the private wake path; rendering stays on the UI
			// thread and only the dispatch turn decides what is due.
			frame = nil
			a.mu.Lock()
			if !a.terminal {
				a.notifyLocked()
			}
			a.mu.Unlock()
		case <-diagnosticC:
			diagnostic = nil
			a.mu.Lock()
			message := a.diagnosticLocked()
			a.mu.Unlock()
			if b, ok := a.backend.(interface{ Counters() platform.Counters }); ok {
				c := b.Counters()
				message += fmt.Sprintf("; dispatch-targets=%d native-problem=%q", c.DispatchTargets, c.Problem)
			}
			a.log("%s", message)
		}
	}
}

// scheduleFrameLocked keeps one app-owned one-shot timer for the earliest live
// deadline; no live deadline means no armed timer. at is the deadline last
// armed or delivered, kept after firing so an unconsumed deadline is never
// re-armed in a loop. The supervisor can evaluate late: an armed timer keeps
// owning its deadline even once it is past, so a pending fire is never
// discarded, and a deadline first seen already due is delivered through the
// wake path at once. Either way it cannot be lost while no dispatch is pending.
func (a *App) scheduleFrameLocked(frame *timer, at *time.Time) {
	now := a.clock.Now()
	want := a.nextDeadlineLocked()
	stop := func() {
		if *frame != nil {
			(*frame).Stop()
			*frame = nil
		}
	}
	switch {
	case want.IsZero():
		stop()
		*at = time.Time{}
	case want.Equal(*at):
		// Armed, or already delivered and awaiting its frame.
	case !want.After(now):
		stop()
		*at = want
		a.notifyLocked()
	default:
		stop()
		*frame, *at = a.clock.NewTimer(want.Sub(now)), want
		a.frameTimers++
	}
}

func (a *App) log(format string, args ...any) {
	err := invoke("Logf", func() error { a.options.Logf(format, args...); return nil })
	if err != nil {
		a.mu.Lock()
		a.failLocked(err)
		a.notifyLocked()
		a.mu.Unlock()
	}
}

// Copy diagnostics under the same mutex as producers, then log outside it.
// Neither a stalled native pump nor a stuck UI callback prevents this snapshot.
func (a *App) diagnosticLocked() string {
	var parts []string
	for id, w := range a.windows {
		parts = append(parts, fmt.Sprintf("window=%d state=%s native=%t creating=%t content-release-pending=%t", id, w.state, w.native, w.creating, w.releasing))
		for taskID, t := range w.tasks {
			parts = append(parts, fmt.Sprintf("window=%d task=%d %q completion-pending=%t", id, taskID, t.name, t.finished))
		}
	}
	for id, t := range a.tasks {
		parts = append(parts, fmt.Sprintf("app task=%d %q completion-pending=%t", id, t.name, t.finished))
	}
	sort.Strings(parts)
	return fmt.Sprintf("dfx2 shutdown outstanding: windows=%d app-tasks=%d native-completions=%d wake-pending=%t wake-failures=%d release-pending=%t failure=%v; %s",
		len(a.windows), len(a.tasks), len(a.operations), a.wakePending, a.wakeFailures, a.terminal && !a.dispatchReleased, a.failure, strings.Join(parts, "; "))
}
