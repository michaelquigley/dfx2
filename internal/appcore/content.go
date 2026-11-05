package appcore

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// frameInterval paces active animation with one-shot deadlines from monotonic
// time. It is not a claim of display refresh synchronization.
const frameInterval = time.Second / 60

// Content is the private bridge between a window and the UI package. Every
// method runs on the UI thread, never from a native callback stack.
type Content interface {
	// Frame evaluates the view once and renders it. The returned image is only
	// borrowed until Present returns; Animate requests another paced frame.
	Frame(FrameRequest) (FrameResult, error)
	// Release runs once, after native teardown has been acknowledged, so no
	// native use of the window's content remains.
	Release()
}

type FrameRequest struct {
	Now      time.Time // monotonic, sampled at frame start
	Surface  platform.Surface
	Sequence uint64 // this window's frame counter
}

type FrameResult struct {
	Image   platform.Image
	Animate bool
}

// attachment is one window's scheduling state machine. All fields are guarded
// by App.mu; impl is called only outside it.
type attachment struct {
	impl                 Content
	surface              platform.Surface
	generation, consumed uint64    // invalidations requested / rendered
	deadline             time.Time // next animation frame; zero when settled
	frames               uint64
	rendering            bool
}

// OpenContent creates a window whose drawing surface is owned by content.
// It follows NewWindow's thread and failure rules, except that a backend
// without content support refuses before native work and nothing is latched.
func OpenContent(a *App, options WindowOptions, content Content) (*Window, error) {
	if content == nil {
		return nil, errors.New("dfx2 ui: nil content")
	}
	return a.newWindow(options, content)
}

// Invalidate requests a frame from any goroutine. It never uses a public post
// slot: it advances a generation under the producer mutex and shares the
// ordinary wake and wake-failure machinery. Repeated calls coalesce.
func Invalidate(w *Window) error {
	a := w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	if w.attachment == nil {
		return errors.New("dfx2 Invalidate: window has no UI content")
	}
	if w.state >= closing || w.ctx.Err() != nil {
		return ErrClosed
	}
	w.attachment.generation++
	// A hidden, unsized or currently rendering window keeps the mark without
	// waking; visibility or render completion reschedules it.
	if a.frameDueLocked(w, a.clock.Now()) {
		a.notifyLocked()
	}
	return nil
}

// ContentStatus is a synchronized snapshot for internal tests and auditions.
type ContentStatus struct {
	Frames               uint64
	Generation, Consumed uint64
	Animating            bool
	Surface              platform.Surface
}

func InspectContent(w *Window) (ContentStatus, bool) {
	a := w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	at := w.attachment
	if at == nil {
		return ContentStatus{}, false
	}
	return ContentStatus{Frames: at.frames, Generation: at.generation, Consumed: at.consumed,
		Animating: !at.deadline.IsZero(), Surface: at.surface}, true
}

func (a *App) surfaceChangedLocked(w *Window, s platform.Surface) {
	at := w.attachment
	if at == nil {
		return
	}
	old := at.surface
	at.surface = s
	if old.Width != s.Width || old.Height != s.Height || old.Scale != s.Scale {
		at.generation++
	}
	// Becoming visible needs no mark: a suspended deadline is already due and
	// a dirty mark is retained. Exposure of a valid image never reaches here.
}

func eligibleLocked(w *Window) bool {
	at := w.attachment
	return at != nil && w.state == open && at.surface.Visible && at.surface.Width > 0 && at.surface.Height > 0
}

func (a *App) frameDueLocked(w *Window, now time.Time) bool {
	if !eligibleLocked(w) || w.attachment.rendering {
		return false
	}
	at := w.attachment
	return at.generation != at.consumed || (!at.deadline.IsZero() && !at.deadline.After(now))
}

// nextDeadlineLocked returns the earliest deadline of a window that may
// render, due or not. Hidden, unsized and closing windows suspend theirs.
func (a *App) nextDeadlineLocked() time.Time {
	var next time.Time
	for _, w := range a.windows {
		if !eligibleLocked(w) {
			continue
		}
		if d := w.attachment.deadline; !d.IsZero() && (next.IsZero() || d.Before(next)) {
			next = d
		}
	}
	return next
}

// frames services at most one frame per due window in this dispatch turn, in
// rotating window order. A frame requested while rendering waits for a later turn.
func (a *App) frames() {
	a.mu.Lock()
	var ids []platform.ID
	for id, w := range a.windows {
		if w.attachment != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	start, _ := slices.BinarySearch(ids, a.frameCursor+1)
	ids = append(ids[start:], ids[:start]...)
	now := a.clock.Now()
	var due []*Window
	for _, id := range ids {
		if w := a.windows[id]; a.frameDueLocked(w, now) {
			w.attachment.rendering = true
			due = append(due, w)
		}
	}
	if len(due) > 0 {
		a.frameCursor = due[0].id
	}
	a.mu.Unlock()
	for _, w := range due {
		a.frame(w)
	}
}

func (a *App) frame(w *Window) {
	at := w.attachment
	a.mu.Lock()
	if w.state != open {
		// Closing sealed content work after this window was selected.
		at.rendering = false
		a.mu.Unlock()
		return
	}
	generation, surface := at.generation, at.surface
	at.deadline = time.Time{} // widgets renew animation only if still needed
	at.frames++
	sequence := at.frames
	now := a.clock.Now()
	a.mu.Unlock()

	var result FrameResult
	err := invoke("ui frame", func() (err error) {
		result, err = at.impl.Frame(FrameRequest{Now: now, Surface: surface, Sequence: sequence})
		return err
	})
	if err == nil {
		a.mu.Lock()
		present := w.state == open
		a.mu.Unlock()
		// The native window cannot be destroyed concurrently: destruction is
		// initiated only by this UI thread after content work is sealed.
		if present {
			if perr := a.backend.Present(w.id, result.Image); perr != nil {
				err = fmt.Errorf("present: %w", perr)
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	at.rendering = false
	if err != nil {
		a.failLocked(fmt.Errorf("dfx2 ui window %d frame %d: %w", w.id, sequence, err))
		a.notifyLocked()
		return
	}
	// Clear only the generation this frame consumed; later marks survive.
	at.consumed = generation
	if result.Animate && w.state == open {
		at.deadline = now.Add(frameInterval)
	}
	a.signalLocked() // the supervisor re-evaluates the shared deadline timer
}
