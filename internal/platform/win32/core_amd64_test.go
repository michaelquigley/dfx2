package win32_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/win32"
)

// These join the real Win32 backend to the real core, over a modeled system.
// They call no Windows API.

const heldRetries = 3

var cleanupRecord = regexp.MustCompile(`Win32 image cleanup: window (\d+)`)

// widening renders each frame ten times wider than the last, which forces a
// replacement allocation on every present.
type widening struct{ width int }

func (c *widening) Frame(appcore.FrameRequest) (appcore.FrameResult, error) {
	c.width = max(c.width*10, 4)
	img := platform.Image{Width: c.width, Height: 3, Stride: c.width * 4, Scale: 1, Pix: make([]byte, c.width*12)}
	return appcore.FrameResult{Image: img}, nil
}

func (*widening) Release() {}

type application struct {
	host *win32.CoreHost
	done chan error
	app  chan *appcore.App
}

func launch(t *testing.T, setup func(*appcore.App) error) *application {
	t.Helper()
	a := &application{host: win32.NewCoreHost(t), done: make(chan error, 1), app: make(chan *appcore.App, 1)}
	options := appcore.Options{Mode: appcore.WindowApplication, Logf: func(string, ...any) {}}
	go func() {
		a.done <- appcore.RunBackend(context.Background(), options, func(app *appcore.App) error {
			a.app <- app
			return setup(app)
		}, a.host.Backend())
	}()
	return a
}

// presented waits until n images have reached native storage.
func (a *application) presented(t *testing.T, n uint64) {
	t.Helper()
	a.host.Await("presents", func() bool { return a.host.Counters().Presents == n })
}

// holdPending shows Run and every window waiting while frees keep failing, with
// each retry firing from its own tick.
func (a *application) holdPending(t *testing.T, allocations int, windows ...*appcore.Window) {
	t.Helper()
	a.host.Await("all failed frees and retired windows", func() bool {
		return a.cleanupReady(allocations, len(windows))
	})
	for range heldRetries {
		attempts := a.host.FreeAttempts()
		a.host.Tick()
		a.host.Settle()
		if got := a.host.FreeAttempts() - attempts; got != allocations {
			t.Fatalf("%d free attempts on one retry, want %d", got, allocations)
		}
		select {
		case err := <-a.done:
			t.Fatalf("Run returned with an allocation unreleased: %v", err)
		default:
		}
		for _, w := range windows {
			select {
			case <-w.Done():
				t.Fatal("window finished with an allocation unreleased")
			default:
			}
		}
		if c := a.host.Counters(); c.Windows != len(windows) || c.Retired != len(windows) || !strings.Contains(c.Problem, "unreleased allocations") {
			t.Fatalf("ownership not visible: %+v", c)
		}
	}
}

// A replacement failure can appear before teardown has freed the current
// image. Start measuring retries only after every initial failure is recorded
// and retirement is complete. No retry ticks have been sent at this point.
func (a *application) cleanupReady(allocations, windows int) bool {
	c := a.host.Counters()
	return a.host.PendingFrees() == allocations && c.Windows == windows && c.Retired == windows &&
		strings.Contains(c.Problem, "unreleased allocations")
}

// release lets the retry succeed and returns Run's result.
func (a *application) release(t *testing.T) error {
	t.Helper()
	a.host.FailFrees(false)
	a.host.Tick()
	select {
	case err := <-a.done:
		if p := a.host.Counters().Problem; p != "" {
			t.Fatalf("diagnostics after release: %q", p)
		}
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return once the free succeeded")
		return nil
	}
}

func cleanupWindows(err error) (windows []string) {
	for _, match := range cleanupRecord.FindAllStringSubmatch(err.Error(), -1) {
		windows = append(windows, match[1])
	}
	return windows
}

func opened(t *testing.T, app *appcore.App) *appcore.Window {
	t.Helper()
	w, err := appcore.OpenContent(app, appcore.WindowOptions{Width: 40, Height: 30}, &widening{})
	if err != nil {
		t.Error(err)
	}
	return w
}

// The earlier failure occupies first-failure selection, so only the
// acknowledgment can carry this free's error to Run.
func TestCleanupFailureAfterAnEarlierFailureReachesRun(t *testing.T) {
	errTask := errors.New("task failed")
	fail := make(chan struct{})
	windows := make(chan *appcore.Window, 1)
	a := launch(t, func(app *appcore.App) error {
		windows <- opened(t, app)
		return app.Go("failing", func(context.Context) error { <-fail; return errTask })
	})
	w := <-windows
	a.presented(t, 1)
	a.host.FailFrees(true)
	close(fail)
	a.holdPending(t, 1, w)
	err := a.release(t)
	if !errors.Is(err, errTask) || !errors.Is(err, win32.ErrHostFree) {
		t.Fatalf("Run lost a failure: %v", err)
	}
	if got := cleanupWindows(err); len(got) != 1 {
		t.Fatalf("cleanup failure reported %d times: %v", len(got), err)
	}
	select {
	case <-w.Done():
	default:
		t.Fatal("window not finished after Run returned")
	}
}

// Both frees fail in one flush. The backend's error latch holds only the first,
// and they share a cause, so neither that latch nor the cause may merge them.
func TestEveryFailedFreeInOneFlushReachesRun(t *testing.T) {
	windows := make(chan *appcore.Window, 2)
	quit := make(chan struct{})
	a := launch(t, func(app *appcore.App) error {
		windows <- opened(t, app)
		windows <- opened(t, app)
		return app.Go("quit", func(context.Context) error { <-quit; app.Quit(); return nil })
	})
	first, second := <-windows, <-windows
	a.presented(t, 2)
	a.host.FailFrees(true)
	close(quit)
	a.holdPending(t, 2, first, second)
	err := a.release(t)
	got := cleanupWindows(err)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("want one record per window, got %v: %v", got, err)
	}
}

// With no other failure, the fatal report and the acknowledgment carry the same
// recorded error, and Run shows it once.
func TestAnOnlyCleanupFailureAppearsOnce(t *testing.T) {
	windows := make(chan *appcore.Window, 1)
	quit := make(chan struct{})
	a := launch(t, func(app *appcore.App) error {
		windows <- opened(t, app)
		return app.Go("quit", func(context.Context) error { <-quit; app.Quit(); return nil })
	})
	w := <-windows
	a.presented(t, 1)
	a.host.FailFrees(true)
	close(quit)
	a.holdPending(t, 1, w)
	err := a.release(t)
	if !errors.Is(err, win32.ErrHostFree) {
		t.Fatalf("Run lost the failure: %v", err)
	}
	if got := cleanupWindows(err); len(got) != 1 {
		t.Fatalf("reported %d times: %v", len(got), err)
	}
}

// One window, two failed frees: the replacement's was already fatal through
// Present and the teardown's is new. Their text is identical, so only their
// identity tells them apart.
func TestAcknowledgmentKeepsTheFailureNotYetReported(t *testing.T) {
	windows := make(chan *appcore.Window, 1)
	redraw := make(chan struct{})
	a := launch(t, func(app *appcore.App) error {
		w := opened(t, app)
		windows <- w
		return app.Go("redraw", func(context.Context) error { <-redraw; return appcore.Invalidate(w) })
	})
	w := <-windows
	a.presented(t, 1)
	a.host.FailFrees(true)
	close(redraw)
	a.holdPending(t, 2, w)
	err := a.release(t)
	if got := cleanupWindows(err); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("want both records for one window, got %v: %v", got, err)
	}
	if text := err.Error(); strings.Count(text, "present: ") != 1 {
		t.Fatalf("want one record through Present: %v", err)
	}
}

func TestRetryBaselineWaitsForTeardownFreeToBeRecorded(t *testing.T) {
	windows := make(chan *appcore.Window, 1)
	redraw := make(chan struct{})
	a := launch(t, func(app *appcore.App) error {
		w := opened(t, app)
		windows <- w
		return app.Go("redraw", func(context.Context) error { <-redraw; return appcore.Invalidate(w) })
	})
	w := <-windows
	a.presented(t, 1)
	entered, release := a.host.HoldFree(2)
	a.host.FailFrees(true)
	close(redraw)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("teardown's first free never started")
	}
	// The replacement's failure is visible, but the current image's first
	// free is still on the stack. A parked notification cannot make this a
	// valid baseline, even though the old readiness predicate accepts it.
	if c := a.host.Counters(); !strings.Contains(c.Problem, "unreleased allocations") || a.host.PendingFrees() != 1 {
		t.Errorf("did not isolate the replacement failure: %+v", c)
	}
	if a.cleanupReady(2, 1) {
		t.Error("retry baseline accepted before teardown's free was recorded")
	}
	release()
	a.holdPending(t, 2, w)
	err := a.release(t)
	if got := cleanupWindows(err); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("want both records for one window, got %v: %v", got, err)
	}
}
