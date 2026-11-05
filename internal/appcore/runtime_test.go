package appcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

// Deadlines only guard hangs. Ordering is established by channels, fake events,
// and individual manual timers, never elapsed wall time.
func receive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test barrier")
		var zero T
		return zero
	}
}

func pending(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
		t.Error("completed before owned work drained")
	default:
	}
}

func resultPending(t *testing.T, c <-chan error) {
	t.Helper()
	select {
	case err := <-c:
		t.Fatalf("Run returned before drain: %v", err)
	default:
	}
}

type harness struct {
	app   *App
	done  <-chan error
	ready <-chan struct{}
}

func launch(t *testing.T, options Options, b *fake.Backend, clock clock, setup func(*App) error) harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	apps := make(chan *App, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, options, func(a *App) error {
			apps <- a
			defer close(ready)
			return setup(a)
		}, b, clock)
	}()
	return harness{receive(t, apps), done, ready}
}

func window(a *App, title string) (*Window, error) {
	return a.NewWindow(WindowOptions{Title: title, Width: 320, Height: 200})
}
func quietOptions(mode Mode) Options { return Options{Mode: mode, Logf: func(string, ...any) {}} }

type manualTimer struct {
	d       time.Duration
	c       chan time.Time
	stopped atomic.Bool
}

func (t *manualTimer) C() <-chan time.Time { return t.c }
func (t *manualTimer) Stop()               { t.stopped.Store(true) }
func (t *manualTimer) fire() {
	if !t.stopped.Load() {
		t.c <- time.Time{}
	}
}

type manualClock struct {
	created chan *manualTimer
	waiting []*manualTimer // only the test controller accesses this slice
	mu      sync.Mutex
	now     time.Time
}

// Now is monotonic test time: it only moves when a test advances it.
func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *manualClock {
	return &manualClock{created: make(chan *manualTimer, 100), now: time.Unix(1_000_000, 0)}
}
func (c *manualClock) NewTimer(d time.Duration) timer {
	t := &manualTimer{d: d, c: make(chan time.Time, 1)}
	c.created <- t
	return t
}
func (c *manualClock) next(t *testing.T, d time.Duration) *manualTimer {
	t.Helper()
	for i, tm := range c.waiting {
		if tm.d == d {
			c.waiting = append(c.waiting[:i], c.waiting[i+1:]...)
			return tm
		}
	}
	for {
		tm := receive(t, c.created)
		if tm.d == d {
			return tm
		}
		c.waiting = append(c.waiting, tm)
	}
}

func TestIndependentWindows(t *testing.T) {
	var first, second *Window
	b := &fake.Backend{}
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) (err error) {
		first, err = window(a, "A")
		if err != nil {
			return err
		}
		second, err = window(a, "B")
		return err
	})
	receive(t, h.ready)
	first.Close()
	receive(t, first.Done())
	if second.Context().Err() != nil || h.app.Context().Err() != nil {
		t.Fatal("closing A canceled B or the app")
	}
	updated := make(chan struct{})
	if err := second.Post(func() {
		if err := second.SetTitle("B updated"); err != nil {
			t.Error(err)
		}
		close(updated)
	}); err != nil {
		t.Fatal(err)
	}
	receive(t, updated)
	ws, _, _, _ := b.Snapshot()
	if len(ws) != 1 || ws[second.id].Options.Title != "B updated" {
		t.Fatalf("remaining windows: %v", ws)
	}
	second.Close()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, second.Done())
}

func TestLastCloseCancelsAppBeforeWindowTaskFinishes(t *testing.T) {
	var w *Window
	appCanceled := make(chan struct{})
	windowExited := make(chan struct{})
	h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) (err error) {
		w, err = window(a, "A")
		if err != nil {
			return err
		}
		if err = a.Go("shared", func(ctx context.Context) error { <-ctx.Done(); close(appCanceled); return ctx.Err() }); err != nil {
			return err
		}
		return w.Go("depends-on-app", func(ctx context.Context) error {
			<-ctx.Done()
			<-appCanceled
			close(windowExited)
			return ctx.Err()
		})
	})
	receive(t, h.ready)
	w.Close()
	receive(t, windowExited)
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestSetupZeroWindowsAndResidentReopen(t *testing.T) {
	t.Run("window", func(t *testing.T) {
		h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(*App) error { return nil })
		if err := receive(t, h.done); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("resident", func(t *testing.T) {
		h := launch(t, quietOptions(ResidentApplication), &fake.Backend{}, newClock(), func(*App) error { return nil })
		receive(t, h.ready)
		for i := range 2 {
			opened := make(chan *Window, 1)
			if err := h.app.Post(func() {
				w, err := window(h.app, fmt.Sprintf("reopen-%d", i))
				if err != nil {
					t.Error(err)
				}
				opened <- w
			}); err != nil {
				t.Fatal(err)
			}
			w := receive(t, opened)
			w.Close()
			receive(t, w.Done())
			if h.app.Context().Err() != nil {
				t.Fatal("resident app canceled with zero windows")
			}
		}
		h.app.Quit()
		if err := receive(t, h.done); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSetupCompletesBeforeLastWindowRule(t *testing.T) {
	h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) error {
		w, err := window(a, "first")
		if err != nil {
			return err
		}
		w.Close()
		w, err = window(a, "second")
		if err != nil {
			return err
		}
		w.Close()
		return nil
	})
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestStartupCloseAndLateCompletionNeverAttach(t *testing.T) {
	var a *App
	var op platform.ID
	settled := make(chan bool, 1)
	created := make(chan *Window, 1)
	b := &fake.Backend{DelayDestroy: true}
	nativeDestroyed := make(chan struct{}, 1)
	b.AfterEvent = func(e platform.Event) {
		if e.Kind == platform.Destroyed {
			nativeDestroyed <- struct{}{}
		}
	}
	b.OnCreate = func(id platform.ID) {
		a.mu.Lock()
		w := a.windows[id]
		a.mu.Unlock()
		var err error
		op, err = a.beginCompletion(w, func(closing bool) error { settled <- closing; return nil })
		if err != nil {
			t.Error(err)
		}
		b.Emit(platform.Event{Kind: platform.CloseRequested, Window: id})
		created <- w
	}
	destroying := make(chan platform.ID, 1)
	b.OnDestroy = func(id platform.ID) { destroying <- id }
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(app *App) error {
		a = app
		_, err := window(a, "startup")
		if !errors.Is(err, ErrClosed) {
			return fmt.Errorf("startup close: %v", err)
		}
		return nil
	})
	w := receive(t, created)
	id := receive(t, destroying)
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: id})
	receive(t, nativeDestroyed)
	// Native death alone cannot finish Done; unknown completions cannot help.
	b.Enqueue(platform.Event{Kind: platform.Completed, Window: id, Completion: 999999})
	// Inspect retained record under the producer lock; no scheduling assumptions.
	h.app.mu.Lock()
	if h.app.windows[id] == nil {
		t.Error("lost routing with pending native completion")
	}
	h.app.mu.Unlock()
	pending(t, w.Done())
	resultPending(t, h.done)
	b.Enqueue(platform.Event{Kind: platform.Completed, Window: id, Completion: op})
	if !receive(t, settled) {
		t.Fatal("late result attached to closing window")
	}
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, w.Done())
}

func TestReentrantDestructionCompletionAndHandleReuse(t *testing.T) {
	var a *App
	var op platform.ID
	var first *Window
	settled := make(chan bool, 1)
	staleDelivered := make(chan struct{}, 1)
	b := &fake.Backend{}
	b.AfterEvent = func(e platform.Event) {
		// The final injected event pairs an old completion with a different
		// owner. Earlier reentrant completions use the original owner's ID.
		if e.Kind == platform.Completed && e.Completion == op && e.Window != first.id {
			staleDelivered <- struct{}{}
		}
	}
	b.OnDestroy = func(id platform.ID) { b.Emit(platform.Event{Kind: platform.Completed, Window: id, Completion: op}) }
	h := launch(t, quietOptions(ResidentApplication), b, newClock(), func(app *App) (err error) {
		a = app
		first, err = window(a, "first")
		if err != nil {
			return err
		}
		op, err = a.beginCompletion(first, func(closing bool) error { settled <- closing; return nil })
		return err
	})
	receive(t, h.ready)
	first.Close()
	receive(t, first.Done())
	if !receive(t, settled) {
		t.Fatal("destruction callback attached a result")
	}
	opened := make(chan *Window, 1)
	if err := a.Post(func() {
		w, err := window(a, "replacement")
		if err != nil {
			t.Error(err)
		}
		opened <- w
	}); err != nil {
		t.Fatal(err)
	}
	second := receive(t, opened)
	if first.id == second.id {
		t.Fatal("owner ID reused")
	}
	b.Enqueue(platform.Event{Kind: platform.CloseRequested, Window: first.id})
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: first.id})
	b.Enqueue(platform.Event{Kind: platform.Completed, Window: second.id, Completion: op})
	// A wake queued during replacement creation can precede these events.
	// Wait for native delivery before admitting the assertion as a public post.
	receive(t, staleDelivered)
	checked := make(chan struct{})
	if err := second.Post(func() {
		if second.Context().Err() != nil {
			t.Error("stale event closed replacement")
		}
		close(checked)
	}); err != nil {
		t.Fatal(err)
	}
	receive(t, checked)
	a.Quit()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
}

func TestTaskRegistrationCloseRaceAccountsEveryAdmission(t *testing.T) {
	for _, closeFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(closeFirst), func(t *testing.T) {
			var admitted, completed atomic.Int32
			var w *Window
			closed := make(chan struct{})
			h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) (err error) {
				w, err = window(a, "race")
				if err != nil {
					return err
				}
				startClose := make(chan struct{})
				go func() { <-startClose; w.Close(); close(closed) }()
				if closeFirst {
					close(startClose)
					<-closed
				}
				for i := range 128 {
					err := w.Go("duplicate-name", func(ctx context.Context) error { <-ctx.Done(); completed.Add(1); return ctx.Err() })
					if err == nil {
						admitted.Add(1)
					} else if !errors.Is(err, ErrClosed) {
						return err
					}
					if i == 0 && !closeFirst {
						close(startClose)
					}
				}
				return nil
			})
			if err := receive(t, h.done); err != nil {
				t.Fatal(err)
			}
			receive(t, closed)
			receive(t, w.Done())
			if admitted.Load() != completed.Load() {
				t.Fatalf("admitted %d completed %d", admitted.Load(), completed.Load())
			}
			if closeFirst && admitted.Load() != 0 {
				t.Fatal("started work after close")
			}
			if !closeFirst && admitted.Load() == 0 {
				t.Fatal("did not exercise admitted work")
			}
		})
	}
}

func TestPostCloseSaturationAndPrivateCompletion(t *testing.T) {
	var w *Window
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var ran atomic.Int32
	h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) (err error) {
		w, err = window(a, "queue")
		if err != nil {
			return err
		}
		if err := w.Go("completion bypasses full queue", func(ctx context.Context) error { <-ctx.Done(); return nil }); err != nil {
			return err
		}
		return w.Post(func() { close(entered); <-release; ran.Add(1) })
	})
	receive(t, h.ready)
	receive(t, entered)
	for range 256 {
		if err := w.Post(func() { ran.Add(1000) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Post(func() {}); !errors.Is(err, ErrBusy) {
		t.Fatalf("full queue: %v", err)
	}
	w.Close()
	if w.Context().Err() == nil {
		t.Fatal("close did not cancel immediately")
	}
	if err := w.Post(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("post after close: %v", err)
	}
	pending(t, w.Done())
	resultPending(t, h.done)
	once.Do(func() { close(release) })
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 1 {
		t.Fatalf("unstarted callbacks ran: %d", ran.Load())
	}
}

func TestPostFIFOAlwaysQueuedAndYields(t *testing.T) {
	b := &fake.Backend{}
	var count, previousPumps int
	h := launch(t, quietOptions(ResidentApplication), b, newClock(), func(a *App) error {
		for i := range 130 {
			if err := a.Post(func() {
				if count != i {
					t.Errorf("FIFO got %d want %d", i, count)
				}
				_, _, pumps, _ := b.Snapshot()
				if i == 64 || i == 128 {
					if pumps <= previousPumps {
						t.Error("did not yield to native dispatch")
					}
				}
				count++
				previousPumps = pumps
				if i == 129 {
					a.Quit()
				}
			}); err != nil {
				return err
			}
		}
		if count != 0 {
			return errors.New("Post ran inline")
		}
		return nil
	})
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if count != 130 {
		t.Fatalf("ran %d callbacks", count)
	}
}

func TestFinalFailedWakeRetriesWithoutProducer(t *testing.T) {
	c := newClock()
	b := &fake.Backend{}
	b.FailWakes(errors.New("transient wake failure"))
	ran := make(chan struct{})
	h := launch(t, quietOptions(ResidentApplication), b, c, func(a *App) error {
		return a.Post(func() { close(ran); a.Quit() })
	})
	receive(t, h.ready)
	c.next(t, retryDelay).fire()
	receive(t, ran)
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	_, wakes, _, released := b.Snapshot()
	if wakes < 2 || !released {
		t.Fatalf("wakes %d released %t", wakes, released)
	}
}

func TestPersistentWakeFailureCancelsButRetainsOwnership(t *testing.T) {
	c := newClock()
	b := &fake.Backend{}
	wakeErr := errors.New("broken delivery")
	b.FailWakes(wakeErr, wakeErr, wakeErr)
	var w *Window
	var ran atomic.Bool
	h := launch(t, quietOptions(WindowApplication), b, c, func(a *App) (err error) {
		w, err = window(a, "stranded")
		if err != nil {
			return err
		}
		return w.Post(func() { ran.Store(true) })
	})
	receive(t, h.ready)
	c.next(t, retryDelay).fire()
	receive(t, h.app.Context().Done())
	c.next(t, retryDelay).fire()
	finalRetry := c.next(t, retryDelay) // still scheduled despite app cancellation
	pending(t, w.Done())
	resultPending(t, h.done)
	_, _, _, released := b.Snapshot()
	if released {
		t.Fatal("released stranded native owner")
	}
	// This is the final retry, with no producer to rescue it.
	finalRetry.fire()
	if err := receive(t, h.done); !errors.Is(err, wakeErr) {
		t.Fatalf("persistent wake error lost: %v", err)
	}
	receive(t, w.Done())
	if ran.Load() {
		t.Fatal("public callback executed after persistent failure sealed owner")
	}
}

func TestDiagnosticsWhileUIStalledAndTaskIgnoresCancellation(t *testing.T) {
	c := newClock()
	logs := make(chan string, 10)
	options := Options{Logf: func(f string, args ...any) { logs <- fmt.Sprintf(f, args...) }}
	b := &fake.Backend{}
	entered := make(chan struct{})
	releaseUI, releaseTask := make(chan struct{}), make(chan struct{})
	var uiOnce, taskOnce sync.Once
	t.Cleanup(func() { uiOnce.Do(func() { close(releaseUI) }); taskOnce.Do(func() { close(releaseTask) }) })
	var w *Window
	h := launch(t, options, b, c, func(a *App) (err error) {
		w, err = window(a, "stalled")
		if err != nil {
			return err
		}
		if err := w.Go("non-cooperative", func(context.Context) error { <-releaseTask; return nil }); err != nil {
			return err
		}
		return w.Post(func() { close(entered); <-releaseUI })
	})
	receive(t, entered)
	w.Close()
	c.next(t, diagnosticDelay).fire()
	message := receive(t, logs)
	for _, want := range []string{"shutdown outstanding", fmt.Sprintf("window=%d state=closing", w.id), "non-cooperative", "native-completions=0", "wake-pending=true"} {
		if !strings.Contains(message, want) {
			t.Errorf("diagnostic lacks %q: %s", want, message)
		}
	}
	pending(t, w.Done())
	resultPending(t, h.done)
	uiOnce.Do(func() { close(releaseUI) })
	// Native teardown can finish, but the uncooperative task still owns Done.
	c.next(t, diagnosticDelay).fire()
	if message = receive(t, logs); !strings.Contains(message, "non-cooperative") {
		t.Fatal(message)
	}
	pending(t, w.Done())
	resultPending(t, h.done)
	taskOnce.Do(func() { close(releaseTask) })
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, w.Done())
	_, _, _, released := b.Snapshot()
	if !released {
		t.Fatal("target not released after explicit task completion")
	}
}

func TestTaskFailureArrivingAfterShutdownIsPreserved(t *testing.T) {
	lateFailure := errors.New("late task failure")
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var w *Window
	h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) (err error) {
		w, err = window(a, "late failure")
		if err != nil {
			return err
		}
		return w.Go("late", func(context.Context) error { <-release; return lateFailure })
	})
	receive(t, h.ready)
	w.Close()
	receive(t, h.app.Context().Done())
	resultPending(t, h.done)
	pending(t, w.Done())
	once.Do(func() { close(release) })
	if err := receive(t, h.done); !errors.Is(err, lateFailure) {
		t.Fatalf("lost late failure: %v", err)
	}
}

func TestApplicationFailureBoundariesDrainCooperativeWork(t *testing.T) {
	boom := errors.New("task failure")
	for _, boundary := range []string{"setup", "post", "task", "task-error"} {
		t.Run(boundary, func(t *testing.T) {
			trigger := make(chan struct{})
			exited := make(chan struct{})
			h := launch(t, quietOptions(WindowApplication), &fake.Backend{}, newClock(), func(a *App) error {
				w, err := window(a, "failure")
				if err != nil {
					return err
				}
				if err := w.Go("cooperative peer", func(ctx context.Context) error { <-ctx.Done(); close(exited); return ctx.Err() }); err != nil {
					return err
				}
				switch boundary {
				case "setup":
					panic("setup exploded")
				case "post":
					return a.Post(func() { panic("post exploded") })
				default:
					return a.Go("failed", func(context.Context) error {
						<-trigger
						if boundary == "task-error" {
							return boom
						}
						panic("task exploded")
					})
				}
			})
			receive(t, h.ready)
			close(trigger)
			err := receive(t, h.done)
			if err == nil {
				t.Fatal("application failure lost")
			}
			if boundary == "task-error" {
				if !errors.Is(err, boom) {
					t.Fatal(err)
				}
			} else if !strings.Contains(err.Error(), "panic:") || !strings.Contains(err.Error(), "runtime/debug.Stack") {
				t.Fatalf("missing panic stack: %v", err)
			}
			receive(t, exited)
		})
	}
}

func TestFailurePolicy(t *testing.T) {
	t.Run("ignored creation error", func(t *testing.T) {
		boom := errors.New("creation failed")
		h := launch(t, quietOptions(ResidentApplication), &fake.Backend{CreateError: boom}, newClock(), func(a *App) error { _, _ = window(a, "ignored"); return nil })
		if err := receive(t, h.done); !errors.Is(err, boom) {
			t.Fatalf("ignored failure not latched: %v", err)
		}
	})
	t.Run("cleanup errors joined and deduplicated", func(t *testing.T) {
		primary, cleanup, release := errors.New("setup failed"), errors.New("destroy failed"), errors.New("release failed")
		h := launch(t, quietOptions(WindowApplication), &fake.Backend{DestroyError: cleanup, ReleaseError: release}, newClock(), func(a *App) error {
			if _, err := window(a, "A"); err != nil {
				return err
			}
			if _, err := window(a, "B"); err != nil {
				return err
			}
			return primary
		})
		err := receive(t, h.done)
		for _, want := range []error{primary, cleanup, release} {
			if !errors.Is(err, want) {
				t.Errorf("lost %v: %v", want, err)
			}
		}
		if strings.Count(err.Error(), cleanup.Error()) != 1 {
			t.Fatalf("duplicate cleanup error: %v", err)
		}
	})
	t.Run("unrelated deadline is a failure", func(t *testing.T) {
		h := launch(t, quietOptions(ResidentApplication), &fake.Backend{}, newClock(), func(a *App) error {
			return a.Go("deadline", func(context.Context) error { return context.DeadlineExceeded })
		})
		if err := receive(t, h.done); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("caller cancellation waits for native release", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b := &fake.Backend{DelayDestroy: true}
		destroyed := make(chan platform.ID, 1)
		b.OnDestroy = func(id platform.ID) { destroyed <- id }
		done := make(chan error, 1)
		ready := make(chan struct{})
		go func() {
			done <- run(ctx, quietOptions(WindowApplication), func(a *App) error { _, err := window(a, "cancel"); close(ready); return err }, b, newClock())
		}()
		receive(t, ready)
		cancel()
		id := receive(t, destroyed)
		resultPending(t, done)
		b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: id})
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestValidationAndWrongThreadAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		options Options
		setup   func(*App) error
	}{
		{"context", nil, Options{}, func(*App) error { return nil }},
		{"setup", context.Background(), Options{}, nil},
		{"mode", context.Background(), Options{Mode: 42}, func(*App) error { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.ctx, tc.options, tc.setup, &fake.Backend{}, newClock()); err == nil {
				t.Fatal("accepted invalid Run")
			}
		})
	}
	var wrong atomic.Bool
	b := &fake.Backend{Owner: func() bool { return !wrong.Load() }}
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		w, err := window(a, "checks")
		if err != nil {
			return err
		}
		wrong.Store(true)
		if _, err := window(a, "wrong"); !errors.Is(err, ErrWrongThread) {
			t.Error(err)
		}
		if err := a.Go("wrong", func(context.Context) error { t.Error("started wrong-thread task"); return nil }); !errors.Is(err, ErrWrongThread) {
			t.Error(err)
		}
		if err := w.Go("wrong", func(context.Context) error { return nil }); !errors.Is(err, ErrWrongThread) {
			t.Error(err)
		}
		if err := w.SetTitle("wrong"); !errors.Is(err, ErrWrongThread) {
			t.Error(err)
		}
		wrong.Store(false)
		if err := a.Go("nil", nil); err == nil {
			t.Error("nil work accepted")
		}
		if err := w.Post(nil); err == nil {
			t.Error("nil post accepted")
		}
		w.Close()
		if err := w.Go("closed", func(context.Context) error { t.Error("closed task started"); return nil }); !errors.Is(err, ErrClosed) {
			t.Error(err)
		}
		return nil
	})
	if err := receive(t, h.done); err != nil {
		t.Fatalf("operation rejection became app failure: %v", err)
	}
	t.Run("bad dimensions are latched before native create", func(t *testing.T) {
		var creates atomic.Int32
		b := &fake.Backend{OnCreate: func(platform.ID) { creates.Add(1) }}
		h := launch(t, quietOptions(ResidentApplication), b, newClock(), func(a *App) error { _, _ = a.NewWindow(WindowOptions{Width: 0, Height: 100}); return nil })
		if err := receive(t, h.done); err == nil || !strings.Contains(err.Error(), "dimensions") {
			t.Fatal(err)
		}
		if creates.Load() != 0 {
			t.Fatal("created invalid native window")
		}
	})
}
