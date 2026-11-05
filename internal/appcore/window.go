package appcore

import (
	"context"
	"errors"
	"fmt"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// NewWindow creates a native window synchronously on the UI thread. Creation
// failures are returned and latched as application failures even if ignored.
func (a *App) NewWindow(options WindowOptions) (*Window, error) { return a.newWindow(options, nil) }

func (a *App) newWindow(options WindowOptions, content Content) (*Window, error) {
	if !a.backend.IsOwner() {
		return nil, ErrWrongThread
	}
	a.mu.Lock()
	if a.state >= closing || a.ctx.Err() != nil {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	if options.Width <= 0 || options.Height <= 0 {
		err := fmt.Errorf("dfx2 NewWindow: client dimensions must be positive (got %dx%d)", options.Width, options.Height)
		a.failLocked(err)
		a.notifyLocked()
		a.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	w := &Window{app: a, owner: owner{id: a.nextLocked(), ctx: ctx, cancel: cancel,
		state: starting, tasks: make(map[platform.ID]*task)}, creating: true, native: true, done: make(chan struct{})}
	if content != nil {
		// Generation 1 is unconsumed: the first visible, sized surface renders.
		w.attachment = &attachment{impl: content, generation: 1}
	}
	// Register before Create: it can synchronously request close or destruction,
	// and content surface notifications route through this record.
	a.windows[w.id] = w
	a.mu.Unlock()
	err := a.backend.Create(w.id, platform.WindowOptions{Title: options.Title, Width: options.Width, Height: options.Height, Content: content != nil})
	a.mu.Lock()
	defer a.mu.Unlock()
	w.creating = false
	if content != nil && errors.Is(err, platform.ErrUnsupportedContent) {
		// Refused before native work: drain the record (releasing content)
		// without latching an application failure.
		w.native = false
		a.closeWindowLocked(w)
		// The refused record was a live starting window and may have
		// postponed last-window shutdown; nonfatal, but it still counts.
		a.lastWindowLocked()
		a.notifyLocked()
		return nil, err
	}
	if err != nil {
		w.native = false // Create's failure contract releases partial objects.
		err = fmt.Errorf("dfx2 NewWindow: %w", err)
		a.failLocked(err)
	} else if w.state == starting {
		w.state = open
	}
	a.notifyLocked()
	if err != nil {
		return nil, err
	}
	if w.state >= closing {
		return w, ErrClosed
	}
	return w, nil
}

// Close immediately seals ordinary admission and cancels this window's tasks.
// It is idempotent, safe from any goroutine, and never waits for native cleanup.
func (w *Window) Close() {
	a := w.app
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closeWindowLocked(w)
	a.lastWindowLocked()
	a.notifyLocked()
}

// Context is safe to read from any goroutine.
func (w *Window) Context() context.Context { return w.ctx }

// Done closes after native teardown, private completions and registered tasks
// have drained. Never wait for it in a UI callback. Safe from any goroutine.
func (w *Window) Done() <-chan struct{} { return w.done }

// SetTitle is a UI-thread operation.
func (w *Window) SetTitle(title string) error {
	a := w.app
	if !a.backend.IsOwner() {
		return ErrWrongThread
	}
	a.mu.Lock()
	closing := w.state >= closing || w.ctx.Err() != nil
	a.mu.Unlock()
	if closing {
		return ErrClosed
	}
	if err := a.backend.SetTitle(w.id, title); err != nil {
		a.mu.Lock()
		a.failLocked(fmt.Errorf("dfx2 SetTitle: %w", err))
		a.notifyLocked()
		a.mu.Unlock()
		return err
	}
	return nil
}

// Post always queues, including on the UI thread. Nil means accepted, not run;
// unstarted callbacks are discarded on close. Each owner's queue holds 256.
func (a *App) Post(f func()) error    { return a.post(&a.owner, f) }
func (w *Window) Post(f func()) error { return w.app.post(&w.owner, f) }

func (a *App) post(o *owner, f func()) error {
	if f == nil {
		return errors.New("dfx2 Post: nil callback")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if o.state >= closing || o.ctx.Err() != nil {
		return ErrClosed
	}
	if len(o.queue) == 256 {
		return ErrBusy
	}
	o.queue = append(o.queue, f)
	a.notifyLocked()
	return nil
}

// Go registers a named cooperative task on the UI thread before starting its
// goroutine. The task must release owned I/O resources before returning.
func (a *App) Go(name string, work func(context.Context) error) error {
	return a.start(&a.owner, name, work)
}
func (w *Window) Go(name string, work func(context.Context) error) error {
	return w.app.start(&w.owner, name, work)
}

func (a *App) start(o *owner, name string, work func(context.Context) error) error {
	if !a.backend.IsOwner() {
		return ErrWrongThread
	}
	if work == nil {
		return errors.New("dfx2 Go: nil work")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if o.state >= closing || o.ctx.Err() != nil {
		return ErrClosed
	}
	id := a.nextLocked()
	t := &task{name: name}
	o.tasks[id] = t
	go func() {
		err := invoke("task "+name, func() error { return work(o.ctx) })
		// Sample at return, before mutex contention can delay reconciliation
		// until after the owner has been canceled for an unrelated reason.
		err = taskError(o.ctx, err)
		a.mu.Lock()
		defer a.mu.Unlock()
		t.finished, t.err = true, err
		a.failLocked(err)
		a.notifyLocked()
	}()
	return nil
}

// Ignore the owner's cancellation only when it accounts for the entire error.
// A joined real failure must survive even when it includes cancellation too.
func taskError(ctx context.Context, err error) error {
	ownerErr := ctx.Err()
	if err == nil || ownerErr == nil {
		return err
	}
	var onlyCancellation func(error) bool
	onlyCancellation = func(err error) bool {
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				if child != nil && !onlyCancellation(child) {
					return false
				}
			}
			return true
		}
		if wrapped, ok := err.(interface{ Unwrap() error }); ok && wrapped.Unwrap() != nil {
			return onlyCancellation(wrapped.Unwrap())
		}
		// A canceled owner does not account for an independent deadline.
		return errors.Is(err, ownerErr)
	}
	if onlyCancellation(err) {
		return nil
	}
	return err
}
