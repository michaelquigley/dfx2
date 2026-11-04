package appcore

import (
	"errors"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// Status is a synchronized ownership snapshot for internal examples/auditions.
type Status struct {
	Windows, Tasks, NativeCompletions, Queued int
	WakePending, Closing, Terminal            bool
	ReleasePending                            bool
	Native                                    platform.Counters
}

// Inspect is safe from any goroutine. It is deliberately a package function in
// an internal package, so App's public method set stays free of audition hooks.
// Core and backend values are sampled separately; this is not an atomic native
// transaction. Quiescent-state scripts verify their ownership preconditions.
func Inspect(a *App) Status {
	a.mu.Lock()
	s := Status{Windows: len(a.windows), Tasks: len(a.tasks), NativeCompletions: len(a.operations), Queued: len(a.queue), WakePending: a.wakePending, Closing: a.state >= closing, Terminal: a.terminal}
	s.ReleasePending = a.terminal && !a.dispatchReleased
	for _, w := range a.windows {
		s.Tasks += len(w.tasks)
		s.Queued += len(w.queue)
	}
	a.mu.Unlock()
	if b, ok := a.backend.(interface{ Counters() platform.Counters }); ok {
		s.Native = b.Counters()
	}
	return s
}

// CloseNextCreation enables the internal native creation-close audition. The
// backend must deliver its normal close event reentrantly from Create.
func CloseNextCreation(a *App) error {
	if !a.backend.IsOwner() {
		return ErrWrongThread
	}
	if b, ok := a.backend.(interface{ CloseNextCreation() }); ok {
		b.CloseNextCreation()
		return nil
	}
	return errors.New("backend has no creation-close audition hook")
}
