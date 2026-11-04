package appcore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

func TestSaturatedOwnersCannotBlockNativeCleanup(t *testing.T) {
	var w *Window
	var op platform.ID
	settled := make(chan bool, 1)
	permitPump := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(permitPump) }) })
	b := &fake.Backend{BeforePump: func() { <-permitPump }}
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) (err error) {
		w, err = window(a, "saturated cleanup")
		if err != nil {
			return err
		}
		op, err = a.beginCompletion(w, func(closing bool) error { settled <- closing; return nil })
		if err != nil {
			return err
		}
		for range 256 {
			if err := a.Post(func() { t.Error("closed app callback ran") }); err != nil {
				return err
			}
			if err := w.Post(func() { t.Error("closed window callback ran") }); err != nil {
				return err
			}
		}
		if !errors.Is(a.Post(func() {}), ErrBusy) || !errors.Is(w.Post(func() {}), ErrBusy) {
			return errors.New("did not saturate both owners")
		}
		return nil
	})
	receive(t, h.ready)
	w.Close()
	b.Enqueue(platform.Event{Kind: platform.Completed, Window: w.id, Completion: op})
	once.Do(func() { close(permitPump) })
	if !receive(t, settled) {
		t.Fatal("native completion did not observe closing")
	}
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	// Late control requests must never touch a released dispatch target.
	w.Close()
	h.app.Quit()
	if err := h.app.Post(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestConcurrentPostsAndClose(t *testing.T) {
	var w *Window
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) (err error) {
		w, err = window(a, "post race")
		if err != nil {
			return err
		}
		return w.Post(func() { close(entered); <-release })
	})
	receive(t, entered)
	gate := make(chan struct{})
	var workers sync.WaitGroup
	var rejected, accepted atomic.Int32
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-gate
			for range 32 {
				err := w.Post(func() { t.Error("queued callback survived close") })
				switch {
				case err == nil:
					accepted.Add(1)
				case errors.Is(err, ErrClosed), errors.Is(err, ErrBusy):
					rejected.Add(1)
				default:
					t.Errorf("Post: %v", err)
				}
			}
		}()
	}
	workers.Add(1)
	go func() { defer workers.Done(); <-gate; w.Close() }()
	close(gate)
	workers.Wait()
	if accepted.Load()+rejected.Load() != 512 {
		t.Fatal("lost a concurrent post result")
	}
	if err := w.Post(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationDuringDispatchIsNotLost(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h := launch(t, quietOptions(ResidentApplication), &fake.Backend{}, newClock(), func(a *App) error {
		// Exhaust the turn so the final producer must be rearmed for a new one.
		for range 63 {
			if err := a.Post(func() {}); err != nil {
				return err
			}
		}
		return a.Post(func() { close(entered); <-release })
	})
	receive(t, entered)
	if err := h.app.Post(h.app.Quit); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestBackendFailuresAreNotCleanExit(t *testing.T) {
	boom := errors.New("backend broke")
	t.Run("open", func(t *testing.T) {
		b := &fake.Backend{OpenError: boom}
		err := run(context.Background(), Options{}, func(*App) error { t.Error("setup after failed Open"); return nil }, b, newClock())
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
	})
	t.Run("pump cancels and continues cleanup", func(t *testing.T) {
		b := &fake.Backend{PumpErrors: []error{boom}}
		h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error { _, err := window(a, "pump error"); return err })
		if err := receive(t, h.done); !errors.Is(err, boom) {
			t.Fatal(err)
		}
		ws, _, _, released := b.Snapshot()
		if len(ws) != 0 || !released {
			t.Fatal("pump failure skipped cleanup")
		}
	})
}

func TestJoinedCancellationDoesNotEraseRealFailure(t *testing.T) {
	boom := errors.New("real failure alongside cancellation")
	gate := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(gate) }) })
	h := launch(t, quietOptions(ResidentApplication), &fake.Backend{}, newClock(), func(a *App) error {
		return a.Go("joined", func(ctx context.Context) error {
			<-gate
			return fmt.Errorf("task result: %w", errors.Join(ctx.Err(), boom))
		})
	})
	receive(t, h.ready)
	h.app.Quit()
	once.Do(func() { close(gate) })
	if err := receive(t, h.done); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

func TestTaskDeadlineAfterOwnerCloseIsPreserved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result func(context.Context) error
		want   error
	}{
		{"owner cancellation", func(ctx context.Context) error { return fmt.Errorf("task: %w", ctx.Err()) }, nil},
		{"independent deadline", func(context.Context) error { return context.DeadlineExceeded }, context.DeadlineExceeded},
		{"wrapped independent deadline", func(context.Context) error { return fmt.Errorf("operation: %w", context.DeadlineExceeded) }, context.DeadlineExceeded},
		{"joined cancellation and deadline", func(ctx context.Context) error { return errors.Join(ctx.Err(), context.DeadlineExceeded) }, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(gate) }) })
			var w *Window
			h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) (err error) {
				w, err = window(a, "deadline during shutdown")
				if err != nil {
					return err
				}
				return w.Go("delayed result", func(ctx context.Context) error {
					<-gate
					if ctx.Err() != context.Canceled {
						t.Errorf("owner context: %v", ctx.Err())
					}
					return tc.result(ctx)
				})
			})
			receive(t, h.ready)
			w.Close()
			receive(t, h.app.Context().Done())
			pending(t, w.Done())
			resultPending(t, h.done)
			once.Do(func() { close(gate) })
			err := receive(t, h.done)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run error = %v, want %v", err, tc.want)
			}
			receive(t, w.Done())
		})
	}
}

// Observe the owner's error sample without adding a production test hook.
// Arming happens just before application work returns; setup/registration
// checks made earlier do not count as the completion's sample.
type sampledContext struct {
	context.Context
	armed   atomic.Bool
	once    sync.Once
	sampled chan error
}

func (c *sampledContext) Err() error {
	err := c.Context.Err()
	if c.armed.Load() {
		c.once.Do(func() { c.sampled <- err })
	}
	return err
}

func TestFailureClassificationPrecedesCompletionLock(t *testing.T) {
	for _, boundary := range []string{"task", "setup"} {
		t.Run(boundary, func(t *testing.T) {
			gate, prepared := make(chan struct{}), make(chan struct{})
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(gate) }) })
			sample := &sampledContext{sampled: make(chan error, 1)}
			var w *Window
			h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) (err error) {
				w, err = window(a, "completion blocked during cancellation")
				if err != nil {
					return err
				}
				work := func(context.Context) error {
					<-gate
					sample.armed.Store(true)
					return context.Canceled // An independently canceled operation.
				}
				if boundary == "setup" {
					sample.Context = a.ctx
					a.ctx = sample
					close(prepared)
					return work(a.ctx)
				}
				sample.Context = w.ctx
				w.ctx = sample
				err = w.Go("independent cancellation", work)
				close(prepared)
				return err
			})
			receive(t, prepared)
			func() {
				// Hold reconciliation until after shutdown. The owner-error
				// sample must remain reachable while this mutex is held.
				h.app.mu.Lock()
				defer h.app.mu.Unlock()
				release.Do(func() { close(gate) })
				if err := receive(t, sample.sampled); err != nil {
					t.Fatalf("owner was already canceled at task/setup return: %v", err)
				}
				h.app.shutdownLocked()
				h.app.notifyLocked()
				pending(t, w.Done())
				resultPending(t, h.done)
			}()
			if err := receive(t, h.done); !errors.Is(err, context.Canceled) {
				t.Fatalf("independent cancellation lost during reconciliation: %v", err)
			}
			receive(t, w.Done())
		})
	}
}

func TestDestroyedReentrantlyDuringCreate(t *testing.T) {
	var w *Window
	b := &fake.Backend{}
	b.OnCreate = func(id platform.ID) { b.Emit(platform.Event{Kind: platform.Destroyed, Window: id}) }
	b.OnDestroy = func(platform.ID) { t.Error("destroyed the same native window twice") }
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		var err error
		w, err = window(a, "destroyed during create")
		if !errors.Is(err, ErrClosed) {
			return fmt.Errorf("create after destruction: %v", err)
		}
		return nil
	})
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, w.Done())
}
