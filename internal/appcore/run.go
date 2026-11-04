package appcore

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	a.mu.Unlock()
	if runSetup {
		err := invoke("setup", func() error { return setup(a) })
		err = taskError(a.ctx, err)
		a.mu.Lock()
		a.failLocked(err)
		a.mu.Unlock()
	}
	a.mu.Lock()
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
	// No timer/worker can post to the dispatch target after this join.
	close(a.stop)
	<-a.supervised
	err := b.Release()
	a.mu.Lock()
	a.cleanupLocked(err)
	if ctx.Err() != nil {
		a.callerErr = ctx.Err()
	}
	result := errors.Join(append([]error{a.failure, a.callerErr}, a.cleanup...)...)
	a.mu.Unlock()
	return result
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
	for _, old := range a.cleanup {
		if errors.Is(old, err) && errors.Is(err, old) {
			return
		}
	}
	a.cleanup = append(a.cleanup, err)
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
	switch e.Kind {
	case platform.CloseRequested:
		if w != nil {
			a.closeWindowLocked(w)
			a.lastWindowLocked()
		}
	case platform.Destroyed:
		if w != nil {
			w.native = false
			a.closeWindowLocked(w)
			a.lastWindowLocked()
			a.cleanupLocked(e.Err)
		}
	case platform.Completed:
		if op := a.operations[e.Completion]; op != nil && op.window == e.Window && !op.ready {
			op.ready, op.err = true, e.Err
		}
	}
	a.notifyLocked()
}
