package appcore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"runtime/debug"
	"sync/atomic"

	"github.com/michaelquigley/dfx2/internal/platform"
)

var ran atomic.Bool

func validate(ctx context.Context, options Options, setup func(*App) error) error {
	if ctx == nil {
		return errors.New("dfx2 Run: nil context")
	}
	if setup == nil {
		return errors.New("dfx2 Run: nil setup")
	}
	if options.Mode != WindowApplication && options.Mode != ResidentApplication {
		return fmt.Errorf("dfx2 Run: invalid mode %d", options.Mode)
	}
	return nil
}

func Run(ctx context.Context, options Options, setup func(*App) error) error {
	if !ran.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	if err := validate(ctx, options, setup); err != nil {
		return err
	}
	b, err := newBackend()
	if err != nil {
		return err
	}
	return run(ctx, options, setup, b, realClock{})
}

// RunBackend is Run on a supplied backend, without the once-per-process guard.
// It exists for tests that join a real backend to this core across the package
// boundary; applications go through Run.
func RunBackend(ctx context.Context, options Options, setup func(*App) error, b platform.Backend) error {
	return run(ctx, options, setup, b, realClock{})
}

// run is the private injection seam. Production Run owns the once-per-process
// guard; portable tests may run many isolated applications with fake backends.
func run(ctx context.Context, options Options, setup func(*App) error, b platform.Backend, c clock) error {
	if err := validate(ctx, options, setup); err != nil {
		return err
	}
	if options.Logf == nil {
		options.Logf = log.Printf
	}
	appctx, cancel := context.WithCancel(ctx)
	a := &App{
		owner:  owner{ctx: appctx, cancel: cancel, state: starting, tasks: make(map[platform.ID]*task)},
		parent: ctx, options: options, backend: b, clock: c,
		windows: make(map[platform.ID]*Window), operations: make(map[platform.ID]*completion),
		changed: make(chan struct{}, 1), stop: make(chan struct{}), supervised: make(chan struct{}),
		wakesStopped: make(chan struct{}),
	}
	defer cancel()
	if err := b.Open(a.event); err != nil {
		return fmt.Errorf("dfx2 backend open: %w", err)
	}
	go a.supervise()
	a.mu.Lock()
	if ctx.Err() != nil {
		a.callerErr = ctx.Err()
		a.shutdownLocked()
	}
	runSetup := a.state < closing
	a.inTurn = runSetup // setup is application code: no turn may nest inside it
	a.mu.Unlock()
	if runSetup {
		err := invoke("setup", func() error { return setup(a) })
		err = taskError(a.ctx, err)
		a.mu.Lock()
		a.failLocked(err)
		a.mu.Unlock()
	}
	a.mu.Lock()
	a.inTurn = false
	a.setupDone = true
	if a.state == starting {
		a.state = open
	}
	a.lastWindowLocked()
	a.notifyLocked()
	a.mu.Unlock()
	for {
		a.mu.Lock()
		terminal := a.terminal
		a.mu.Unlock()
		if terminal {
			break
		}
		if err := b.Pump(); err != nil {
			a.mu.Lock()
			a.failLocked(fmt.Errorf("dfx2 backend pump: %w", err))
			a.notifyLocked()
			a.mu.Unlock()
		}
	}
	// Seal wake retries before target release, retaining independent shutdown
	// diagnostics if native release itself gets stuck.
	<-a.wakesStopped
	err := b.Release()
	a.mu.Lock()
	a.dispatchReleased = true
	a.mu.Unlock()
	close(a.stop)
	<-a.supervised
	a.mu.Lock()
	a.cleanupLocked(err)
	if ctx.Err() != nil {
		a.callerErr = ctx.Err()
	}
	result := a.resultLocked()
	a.mu.Unlock()
	return result
}

// resultLocked joins the failure, the caller's error and each cleanup record
// that has not already appeared. A backend may report one record twice: as the
// fatal error that began shutdown, and again with the window's acknowledgment,
// where it survives if an earlier failure took first place.
func (a *App) resultLocked() error {
	parts := []error{a.failure, a.callerErr}
	seen := a.failure
	for _, err := range a.cleanup {
		if err = unreported(err, seen); err != nil {
			parts = append(parts, err)
			seen = errors.Join(seen, err)
		}
	}
	return errors.Join(parts...)
}

// unreported returns err without the records seen already holds. A multiple
// error loses only those constituents, and stays whole if it holds none.
func unreported(err, seen error) error {
	if !holdsReported(err, seen) {
		return err
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return nil // a reported record itself
	}
	var kept []error
	for _, part := range joined.Unwrap() {
		kept = append(kept, unreported(part, seen))
	}
	return errors.Join(kept...)
}

func holdsReported(err, seen error) bool {
	if holdsRecord(seen, err) {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, part := range joined.Unwrap() {
			if holdsReported(part, seen) {
				return true
			}
		}
	}
	return false
}

// holdsRecord reports whether tree contains this very record. Only a pointer
// has identity: a value such as syscall.Errno is a cause that unrelated
// failures can share. Matching deliberately ignores text and errors.Is.
func holdsRecord(tree, record error) bool {
	if record == nil || reflect.TypeOf(record).Kind() != reflect.Pointer {
		return false
	}
	if tree == record {
		return true
	}
	switch e := tree.(type) {
	case interface{ Unwrap() error }:
		return holdsRecord(e.Unwrap(), record)
	case interface{ Unwrap() []error }:
		for _, part := range e.Unwrap() {
			if holdsRecord(part, record) {
				return true
			}
		}
	}
	return false
}

func invoke(where string, f func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("dfx2 %s panic: %v\n%s", where, p, debug.Stack())
		}
	}()
	return f()
}

func (a *App) nextLocked() platform.ID { a.next++; return a.next }

func (a *App) failLocked(err error) {
	if err == nil {
		return
	}
	if a.failure == nil {
		a.failure = err
	}
	a.shutdownLocked()
}

func (a *App) cleanupLocked(err error) {
	if err == nil {
		return
	}
	a.cleanup = append(a.cleanup, err) // resultLocked removes repeats
	a.shutdownLocked()
}

func (a *App) shutdownLocked() {
	if a.state >= closing {
		return
	}
	a.state = closing
	a.queue = nil
	a.cancel()
	for _, w := range a.windows {
		a.closeWindowLocked(w)
	}
	a.signalLocked()
}

func (a *App) closeWindowLocked(w *Window) {
	if w.state >= closing {
		return
	}
	w.state = closing
	w.queue = nil
	w.cancel()
}

func (a *App) lastWindowLocked() {
	if !a.setupDone || a.options.Mode == ResidentApplication || a.state >= closing {
		return
	}
	for _, w := range a.windows {
		if w.state < closing {
			return
		}
	}
	a.shutdownLocked()
}

// Quit seals admission and cancels owned work immediately, from any goroutine.
// It requests UI-thread cleanup and never waits for it.
func (a *App) Quit() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.shutdownLocked()
	a.notifyLocked()
}

// Context is safe to read from any goroutine.
func (a *App) Context() context.Context { return a.ctx }

func (a *App) event(e platform.Event) {
	if e.Kind == platform.Wake {
		a.dispatch()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminal {
		return
	}
	w := a.windows[e.Window]
	if w != nil && w.browser != nil && e.Kind == platform.SurfaceChanged {
		// Forward copied bounds outside the mutex; browsers are not enrolled
		// in frame or input scheduling.
		impl := a.browserBoundsLocked(w)
		a.mu.Unlock()
		if impl != nil {
			impl.Bounds(e.Surface)
		}
		a.mu.Lock() // for the deferred unlock
		return
	}
	switch e.Kind {
	case platform.CloseRequested:
		if w != nil {
			a.closeWindowLocked(w)
			a.lastWindowLocked()
		}
	case platform.Destroyed:
		if w != nil {
			if w.browser != nil && !w.browser.released && w.native {
				// The shell is destroyed only after the browser released it.
				a.failLocked(fmt.Errorf("dfx2 webview window %d: native parent destroyed while the browser still used it", w.id))
			}
			w.native = false
			a.closeWindowLocked(w)
			a.lastWindowLocked()
			a.cleanupLocked(e.Err)
		}
	case platform.SurfaceChanged:
		if w != nil {
			a.surfaceChangedLocked(w, e.Surface)
		}
	case platform.InputReceived:
		if w != nil {
			a.inputLocked(w, e.Input)
		}
	case platform.Completed:
		if op := a.operations[e.Completion]; op != nil && op.window == e.Window && !op.ready {
			op.ready, op.err = true, e.Err
		}
	}
	a.notifyLocked()
}
