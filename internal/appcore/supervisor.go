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
	var retry, diagnostic timer
	defer func() {
		if retry != nil {
			retry.Stop()
		}
		if diagnostic != nil {
			diagnostic.Stop()
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
			wakesSealed = true
			close(a.wakesStopped)
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
		var retryC, diagnosticC <-chan time.Time
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
		parts = append(parts, fmt.Sprintf("window=%d state=%s native=%t creating=%t", id, w.state, w.native, w.creating))
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
