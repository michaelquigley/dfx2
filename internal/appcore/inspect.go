package appcore

import (
	"errors"
	"slices"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// Status is a synchronized ownership snapshot for internal examples/auditions.
type Status struct {
	Windows, Tasks, NativeCompletions, Queued int
	WakePending, Closing, Terminal            bool
	ReleasePending                            bool
	// Frames counts frames started by live content windows; Animating counts
	// those with a pending animation deadline (a frame clears its deadline as
	// it starts, so a frame in flight reads as zero); FrameTimers counts
	// deadline timers ever armed by the application.
	Frames      uint64
	Animating   int
	FrameTimers uint64
	Native      platform.Counters
}

// Inspect is safe from any goroutine. It is deliberately a package function in
// an internal package, so App's public method set stays free of audition hooks.
// Core and backend values are sampled separately; this is not an atomic native
// transaction. Quiescent-state scripts verify their ownership preconditions.
func Inspect(a *App) Status {
	a.mu.Lock()
	s := Status{Windows: len(a.windows), Tasks: len(a.tasks), NativeCompletions: len(a.operations), Queued: len(a.queue), WakePending: a.wakePending, Closing: a.state >= closing, Terminal: a.terminal}
	s.ReleasePending = a.terminal && !a.dispatchReleased
	s.FrameTimers = a.frameTimers
	for _, w := range a.windows {
		s.Tasks += len(w.tasks)
		s.Queued += len(w.queue)
		if w.attachment != nil {
			s.Frames += w.attachment.frames
			if !w.attachment.deadline.IsZero() {
				s.Animating++
			}
		}
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

// ContentWindows lists live UI content windows in creation order, for
// internal examples. Safe from any goroutine.
func ContentWindows(a *App) []*Window {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*Window
	for _, w := range a.windows {
		if w.attachment != nil && w.state < closing {
			out = append(out, w)
		}
	}
	slices.SortFunc(out, func(x, y *Window) int { return int(x.id) - int(y.id) })
	return out
}

// WindowID is a window's numeric identity, for internal fixtures that deliver
// native events to it.
func WindowID(w *Window) platform.ID { return w.id }

type auditionBackend interface {
	AuditionResize(platform.ID, int, int) error
	AuditionIconify(platform.ID, bool) error
	AuditionSnapshot(platform.ID, string) error
	AuditionExpose(platform.ID) error
}

func audition(w *Window) (auditionBackend, error) {
	if !w.app.backend.IsOwner() {
		return nil, ErrWrongThread
	}
	b, ok := w.app.backend.(auditionBackend)
	if !ok {
		return nil, errors.New("backend has no content audition hooks")
	}
	return b, nil
}

// AuditionResize, AuditionIconify, AuditionSnapshot and AuditionExpose drive native content
// windows for internal auditions, on the UI thread. They add nothing to the
// public API and deliver results through the ordinary native notifications.
func AuditionResize(w *Window, width, height int) error {
	b, err := audition(w)
	if err != nil {
		return err
	}
	return b.AuditionResize(w.id, width, height)
}

func AuditionIconify(w *Window, iconify bool) error {
	b, err := audition(w)
	if err != nil {
		return err
	}
	return b.AuditionIconify(w.id, iconify)
}

func AuditionSnapshot(w *Window, path string) error {
	b, err := audition(w)
	if err != nil {
		return err
	}
	return b.AuditionSnapshot(w.id, path)
}

// AuditionContent returns a live window's attached content, on the UI thread,
// so an internal example can read state its content type exposes there, such
// as text cache counters. It refuses closing windows, whose content may
// already be released, and adds nothing to the public API.
func AuditionContent(w *Window) (Content, error) {
	if !w.app.backend.IsOwner() {
		return nil, ErrWrongThread
	}
	w.app.mu.Lock()
	defer w.app.mu.Unlock()
	if w.attachment == nil || w.state >= closing {
		return nil, ErrClosed
	}
	return w.attachment.impl, nil
}

// AuditionExpose asks the native toolkit to repaint the window, as an exposure
// would. It requests no frame: only the retained image is painted.
func AuditionExpose(w *Window) error {
	b, err := audition(w)
	if err != nil {
		return err
	}
	return b.AuditionExpose(w.id)
}
