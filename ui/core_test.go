package ui

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

// coreBench joins real content, the real core and its queue, through a fake
// backend that delivers events on the UI thread. Native delivery and capture
// are the backends' tests; this covers everything from the copied event on.
type coreBench struct {
	t        *testing.T
	b        *fake.Backend
	app      *dfx2.App
	window   *Window
	mu       sync.Mutex
	commands []string
	evals    int
	done     chan error
	idle     chan error // armed by waitIdle on the UI thread, closed by quiet
}

func runCore(t *testing.T, draw func(*coreBench, *Frame)) *coreBench {
	t.Helper()
	c := &coreBench{t: t, b: &fake.Backend{}, done: make(chan error, 1)}
	c.b.BeforePump = c.quiet
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		c.done <- appcore.RunBackend(ctx, dfx2.Options{Logf: func(string, ...any) {}}, func(app *dfx2.App) error {
			defer close(ready)
			c.app = app
			content := newContent(func(f *Frame) {
				c.mu.Lock()
				c.evals++
				c.mu.Unlock()
				draw(c, f)
			})
			content.engine = testEngine(t)
			w, err := appcore.OpenContent(app, dfx2.WindowOptions{Width: 400, Height: 400}, content)
			if err != nil {
				return err
			}
			c.window = &Window{w: w}
			return nil
		}, c.b)
	}()
	<-ready
	c.send(platform.Event{Kind: platform.SurfaceChanged, Window: c.id(), Surface: platform.Surface{Width: 400, Height: 400, Scale: 1, Visible: true}})
	c.waitIdle()
	return c
}

func (c *coreBench) id() platform.ID {
	ws := appcore.ContentWindows(c.app)
	if len(ws) != 1 {
		c.t.Fatalf("%d content windows", len(ws))
	}
	return appcore.WindowID(ws[0])
}

func (c *coreBench) send(events ...platform.Event) {
	for _, e := range events {
		c.b.Enqueue(e)
	}
}

// input delivers one batch from inside a single posted callback, on the UI
// thread, so the whole batch is in the core's queue before any frame of that
// turn can run. Separate Enqueue calls would race the pump: a frame could run
// between any two events, and the edits and evaluation counts would depend on
// scheduling.
func (c *coreBench) input(inputs ...platform.Input) {
	c.t.Helper()
	c.deliver(inputs, nil)
}

// inputArmed delivers a batch as input does and arms the idle observer in the
// same callback, before the batch's first frame can run. Await the result.
func (c *coreBench) inputArmed(inputs ...platform.Input) chan error {
	c.t.Helper()
	idle := make(chan error, 1)
	c.deliver(inputs, idle)
	return idle
}

func (c *coreBench) deliver(inputs []platform.Input, idle chan error) {
	c.t.Helper()
	id := c.id()
	c.onUIThread(func() {
		for _, in := range inputs {
			c.b.Emit(platform.Event{Kind: platform.InputReceived, Window: id, Input: in})
		}
		if idle != nil {
			c.arm(idle)
		}
	})
}

// onUIThread runs f in one posted callback and waits for it.
func (c *coreBench) onUIThread(f func()) {
	c.t.Helper()
	ran := make(chan struct{})
	if err := c.app.Post(func() {
		defer close(ran)
		f()
	}); err != nil {
		c.t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		c.t.Fatal("posted callback did not run")
	}
}

// waitIdle returns once the core is quiescent. Seeing a callback run is not
// enough: frames run after a turn's callbacks, and a frame that consumes only
// a prefix leaves the rest for a later turn. So the check is made by the pump
// hook, between turns on the UI thread, where no frame can be in flight. Any
// window still owing a frame makes the finished turn request another wake, so
// no pending wake, no fake event, no posted callback and no animation means
// that no further frame will run until new input arrives.
func (c *coreBench) waitIdle() {
	c.t.Helper()
	idle := make(chan error, 1)
	c.onUIThread(func() { c.arm(idle) })
	c.await(idle)
}

// arm hands idle to the pump hook. It runs on the UI thread, inside a posted
// callback, so the hook first looks at the boundary after that turn's frames.
func (c *coreBench) arm(idle chan error) {
	c.mu.Lock()
	c.idle = idle
	c.mu.Unlock()
}

// await blocks until the armed observer reports. The deadline only guards
// against a hang.
func (c *coreBench) await(idle chan error) {
	c.t.Helper()
	select {
	case err := <-idle:
		if err != nil {
			c.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		c.t.Fatalf("not idle: %+v", appcore.Inspect(c.app))
	}
}

// quiet runs before every pump, between dispatch turns.
func (c *coreBench) quiet() {
	c.mu.Lock()
	idle := c.idle
	c.mu.Unlock()
	if idle == nil {
		return
	}
	s := appcore.Inspect(c.app)
	if s.WakePending || s.Queued != 0 || s.Animating != 0 || c.b.Queued() != 0 {
		return
	}
	c.mu.Lock()
	c.idle = nil
	c.mu.Unlock()
	// Quiescent with work left would be a stuck queue, not a slow one.
	ws := appcore.ContentWindows(c.app)
	cs, _ := appcore.InspectContent(ws[0])
	if cs.InputQueued != 0 || cs.Generation != cs.Consumed || cs.Frames == 0 {
		idle <- fmt.Errorf("quiescent with work outstanding: %+v", cs)
	}
	close(idle)
}

func (c *coreBench) evaluations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evals
}

func (c *coreBench) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commands = nil
}

func (c *coreBench) record(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commands = append(c.commands, fmt.Sprintf(format, args...))
}

func (c *coreBench) want(commands ...string) {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !slices.Equal(c.commands, commands) {
		c.t.Fatalf("commands %q, want %q", c.commands, commands)
	}
}

func (c *coreBench) stop() {
	c.t.Helper()
	c.window.Close()
	select {
	case err := <-c.done:
		if err != nil {
			c.t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		c.t.Fatal("Run did not return")
	}
	if v := c.b.Violations(); len(v) != 0 {
		c.t.Fatal(v)
	}
}

func mixerStrip() func(*coreBench, *Frame) {
	gain, muted := 0.5, false
	return func(c *coreBench, f *Frame) {
		if out, changed := f.Fader("gain", gain); changed {
			gain = out
			c.record("gain=%g", out)
		}
		if out, changed := f.Checkbox("mute", "Mute", muted); changed {
			muted = out
			c.record("mute=%t", out)
		}
	}
}

func TestInputThroughTheCoreDispatchesEachEditExactlyOnce(t *testing.T) {
	c := runCore(t, mixerStrip())
	x := 34.0
	// One batch: the press edits, the two motions coalesce into one drag edit,
	// and the release, in a second frame, edits nothing.
	c.input(down(x, faderTop), motion(x, 96), motion(x, faderBottom), up(x, faderBottom))
	c.waitIdle()
	c.want("gain=1", "gain=0")
	c.reset()
	evals := c.evaluations()
	// One batch: focus moves without an edit, so the first toggle shares Tab's
	// frame, and the second toggle waits for exactly one more.
	c.input(tab(), key(platform.KeySpace), keyUp(platform.KeySpace), key(platform.KeySpace), keyUp(platform.KeySpace))
	c.waitIdle()
	c.want("mute=true", "mute=false")
	if n := c.evaluations() - evals; n != 2 {
		t.Errorf("%d evaluations for two edits", n)
	}
	c.stop()
}

// The same events with a frame allowed between each: a schedule the old
// unbatched delivery permitted whenever the pump outran the test goroutine.
// Correct code then produces an intermediate drag edit and one evaluation per
// event, so exact assertions are only sound with batched delivery.
func TestInputSplitAcrossTurnsEditsPerTurn(t *testing.T) {
	c := runCore(t, mixerStrip())
	x := 34.0
	for _, in := range []platform.Input{down(x, faderTop), motion(x, 96), motion(x, faderBottom), up(x, faderBottom)} {
		c.input(in)
		c.waitIdle()
	}
	c.want("gain=1", "gain=0.5", "gain=0")
	c.reset()
	evals := c.evaluations()
	inputs := []platform.Input{tab(), key(platform.KeySpace), keyUp(platform.KeySpace), key(platform.KeySpace), keyUp(platform.KeySpace)}
	for _, in := range inputs {
		c.input(in)
		c.waitIdle()
	}
	c.want("mute=true", "mute=false")
	if n := c.evaluations() - evals; n != len(inputs) {
		t.Errorf("%d evaluations for %d separately delivered events", n, len(inputs))
	}
	c.stop()
}

// The idle observer must not report while the frame owed the batch's
// remainder is still running. The observer is armed with the batch, before its
// first frame; that remainder frame is held mid-draw until the test has checked
// the observer, and must have finished once the observer reports. Ordering is
// by channels alone; the timeouts only guard against a hang.
func TestWaitIdleWaitsForTheFrameThatConsumesTheRemainder(t *testing.T) {
	var muted, hold, finished bool
	entered, gate := make(chan struct{}), make(chan struct{})
	c := runCore(t, func(c *coreBench, f *Frame) {
		if out, changed := f.Checkbox("mute", "Mute", muted); changed {
			muted = out
			c.record("mute=%t", out)
			if hold && !out {
				close(entered)
				<-gate
				c.mu.Lock()
				finished = true
				c.mu.Unlock()
			}
		}
		hold = true
	})
	// Registered after runCore, so it runs first: a failed assertion below
	// still frees the UI thread before the application is canceled.
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	evals := c.evaluations()
	idle := c.inputArmed(tab(), key(platform.KeySpace), keyUp(platform.KeySpace), key(platform.KeySpace), keyUp(platform.KeySpace))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the remainder's frame never ran")
	}
	// The UI thread is inside that frame, so no pump boundary can occur.
	select {
	case err := <-idle:
		t.Fatalf("idle reported while the remainder's frame was held (%v)", err)
	default:
	}
	release()
	c.await(idle)
	c.mu.Lock()
	done := finished
	c.mu.Unlock()
	if !done {
		t.Fatal("idle reported before the remainder's frame finished")
	}
	c.want("mute=true", "mute=false")
	if n := c.evaluations() - evals; n != 2 {
		t.Errorf("%d evaluations for two edits", n)
	}
	c.stop()
}

func TestOverflowThroughTheCoreCancelsWithoutAStuckButtonOrFalseEdits(t *testing.T) {
	var gain float64
	c := runCore(t, func(c *coreBench, f *Frame) {
		if out, changed := f.Fader("gain", gain); changed {
			gain = out
			c.record("gain=%g", out)
		}
	})
	x := 34.0
	c.input(down(x, faderTop))
	c.waitIdle()
	c.want("gain=1")
	// Enough edges to overflow before the next dispatch can drain them, in one
	// batch so that no frame can run between them.
	burst := make([]platform.Input, 0, 301)
	for range 300 {
		burst = append(burst, key(platform.KeyArrowUp))
	}
	// The gesture's own release was among what overflow discards.
	burst = append(burst, motion(x, faderBottom))
	c.input(burst...)
	c.waitIdle()
	id := c.id()
	s, _ := appcore.InspectContent(appcore.ContentWindows(c.app)[0])
	if s.InputOverflows == 0 {
		t.Fatalf("no overflow: %+v", s)
	}
	if got := c.b.CaptureReleases(id); got != 1 {
		t.Fatalf("native capture released %d times", got)
	}
	c.mu.Lock()
	commands := slices.Clone(c.commands)
	c.mu.Unlock()
	// 43 surviving arrow presses could edit only after the gesture ended, and
	// the motion behind them targets nothing.
	for _, command := range commands[1:] {
		if command == "gain=0" {
			t.Fatalf("motion of a canceled gesture edited: %q", commands)
		}
	}
	c.stop()
}
