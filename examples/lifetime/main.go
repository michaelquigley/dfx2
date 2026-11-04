// Command lifetime exercises independent native windows and owned tasks.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
)

type config struct {
	resident   bool
	pauseAfter time.Duration
	script     string
}

type output struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (o *output) event(event string, values ...any) {
	line := map[string]any{"event": event, "time": time.Now().Format(time.RFC3339Nano)}
	for i := 0; i+1 < len(values); i += 2 {
		line[values[i].(string)] = values[i+1]
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = o.encoder.Encode(line)
}
func (o *output) status(event string, a *dfx2.App) {
	s := appcore.Inspect(a)
	o.event(event, "windows", s.Windows, "tasks", s.Tasks, "native_completions", s.NativeCompletions,
		"queued", s.Queued, "terminal", s.Terminal, "native_windows", s.Native.Windows,
		"native_retired", s.Native.Retired, "dispatch_targets", s.Native.DispatchTargets,
		"release_pending", s.ReleasePending, "native_problem", s.Native.Problem,
		"wakes", s.Native.Wakes, "dispatches", s.Native.Dispatches)
}

type demoWindow struct {
	w       *dfx2.Window
	name    string
	applied atomic.Int64
	updated chan struct{}
	paused  chan struct{}
}

type demo struct {
	a          *dfx2.App
	c          config
	o          *output
	watchers   sync.WaitGroup
	nextWindow int
}

func (d *demo) watch(w *demoWindow) {
	d.watchers.Add(1)
	go func() {
		defer d.watchers.Done()
		<-w.w.Done()
		d.o.event("closed", "window", w.name, "applied", w.applied.Load())
		d.o.status("ownership", d.a)
	}()
}

func (d *demo) open(name string) (*demoWindow, error) {
	w, err := d.a.NewWindow(dfx2.WindowOptions{Title: "dfx2 lifetime " + name + " — 0", Width: 420, Height: 240})
	if err != nil {
		return nil, err
	}
	result := &demoWindow{w: w, name: name, updated: make(chan struct{}, 1), paused: make(chan struct{})}
	d.watch(result)
	if err := w.Go("counter-"+name, func(ctx context.Context) error { return d.counter(ctx, result) }); err != nil {
		return nil, err
	}
	d.o.event("opened", "window", name)
	d.o.status("ownership", d.a)
	return result, nil
}

func (d *demo) counter(ctx context.Context, w *demoWindow) error {
	defer d.o.event("task-ended", "task", "counter-"+w.name)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var pause *time.Timer
	var pauseC <-chan time.Time
	if d.c.pauseAfter > 0 {
		pause = time.NewTimer(d.c.pauseAfter)
		pauseC = pause.C
		defer pause.Stop()
	}
	var counter int64
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pauseC:
			close(w.paused)
			d.o.event("paused", "window", w.name, "applied", w.applied.Load())
			return nil
		case <-ticker.C:
			counter++
			value := counter
			err := w.w.Post(func() {
				if err := w.w.SetTitle(fmt.Sprintf("dfx2 lifetime %s — %d", w.name, value)); err != nil {
					d.o.event("title-error", "error", err.Error())
					return
				}
				w.applied.Store(value)
				select {
				case w.updated <- struct{}{}:
				default:
				}
				d.o.event("update", "window", w.name, "count", value)
			})
			if errors.Is(err, dfx2.ErrClosed) {
				return nil
			}
			if errors.Is(err, dfx2.ErrBusy) {
				d.o.event("post-busy", "window", w.name)
				continue
			}
			if err != nil {
				return err
			}
		}
	}
}

func waitUpdate(ctx context.Context, w *demoWindow, target int64) error {
	for w.applied.Load() < target {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.w.Done():
			return fmt.Errorf("window %s closed before update %d", w.name, target)
		case <-w.paused:
			return fmt.Errorf("window %s paused before update %d", w.name, target)
		case <-w.updated:
		}
	}
	return nil
}

func waitDone(ctx context.Context, w *demoWindow) error {
	select {
	case <-w.w.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *demo) postedOpen(ctx context.Context, name string) (*demoWindow, error) {
	type result struct {
		w   *demoWindow
		err error
	}
	ready := make(chan result, 1)
	if err := d.a.Post(func() { w, err := d.open(name); ready <- result{w, err} }); err != nil {
		return nil, err
	}
	// Closing may discard the public post; cancellation must end this wait.
	select {
	case r := <-ready:
		return r.w, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var controlledFailure = errors.New("controlled lifetime task failure")

func (d *demo) script(ctx context.Context, a, b *demoWindow, cancel context.CancelFunc) error {
	if d.c.script == "idle" {
		return d.idle(ctx, a, b)
	}
	if err := waitUpdate(ctx, a, 3); err != nil {
		return err
	}
	if err := waitUpdate(ctx, b, 3); err != nil {
		return err
	}
	switch d.c.script {
	case "failure":
		d.o.event("inject-failure")
		return controlledFailure
	case "cancel":
		d.o.event("inject-cancel")
		cancel()
		return nil
	}
	a.w.Close()
	if err := waitDone(ctx, a); err != nil {
		return err
	}
	baseline := b.applied.Load()
	if err := waitUpdate(ctx, b, baseline+3); err != nil {
		return err
	}
	d.o.event("verified-independent", "window", "B", "before", baseline, "after", b.applied.Load())
	b.w.Close()
	if !d.c.resident {
		return nil
	}
	if err := waitDone(ctx, b); err != nil {
		return err
	}
	d.o.status("resident-zero-windows", d.a)
	c, err := d.postedOpen(ctx, "C")
	if err != nil {
		return err
	}
	if err := waitUpdate(ctx, c, 3); err != nil {
		return err
	}
	c.w.Close()
	if err := waitDone(ctx, c); err != nil {
		return err
	}
	d.o.event("verified-reopen", "window", "C")
	d.a.Quit()
	return nil
}

func (d *demo) idle(ctx context.Context, a, b *demoWindow) error {
	for _, w := range []*demoWindow{a, b} {
		select {
		case <-w.paused:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Wait until the window-task completions and their wakes drain. Only this
	// app-owned script task remains. This settling probe ends before measurement.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s := appcore.Inspect(d.a)
		if s.Tasks == 1 && s.Queued == 0 && !s.WakePending {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	ticker.Stop()
	before := appcore.Inspect(d.a)
	d.o.status("idle-begin", d.a)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	after := appcore.Inspect(d.a)
	wakes, dispatches := after.Native.Wakes-before.Native.Wakes, after.Native.Dispatches-before.Native.Dispatches
	d.o.event("idle-measurement", "wakes", wakes, "dispatches", dispatches)
	if wakes != 0 || dispatches != 0 {
		return fmt.Errorf("paused application woke: wakes=%d dispatches=%d", wakes, dispatches)
	}
	d.a.Quit()
	return nil
}

func (d *demo) console(ctx context.Context, messages <-chan string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case message, ok := <-messages:
			if !ok {
				d.o.event("console-eof")
				return nil
			}
			switch message {
			case "open":
				d.nextWindow++
				if _, err := d.postedOpen(ctx, fmt.Sprintf("console-%d", d.nextWindow)); err != nil {
					if errors.Is(err, dfx2.ErrClosed) || ctx.Err() != nil {
						return nil
					}
					return err
				}
			case "quit":
				d.a.Quit()
				return nil
			case "status":
				d.o.status("status", d.a)
			default:
				d.o.event("console-command", "message", "commands: open, status, quit")
			}
		}
	}
}

// A blocking terminal Scan must never be a registered task. This caller-owned
// goroutine may remain blocked until process exit; the cancellable registered
// console consumer cannot hold Run open waiting for input.
func readConsole(ctx context.Context, input io.Reader) <-chan string {
	messages := make(chan string)
	go func() {
		defer close(messages)
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			select {
			case messages <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return messages
}

func execute(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := config{}
	flags := flag.NewFlagSet("lifetime", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&c.resident, "resident", false, "keep the app alive at zero windows; console commands: open, status, quit")
	flags.DurationVar(&c.pauseAfter, "pause-after", 0, "stop counter tasks after this interval, leaving windows open")
	flags.StringVar(&c.script, "script", "", "bounded audition: lifecycle, idle, failure, cancel, create-close")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if c.pauseAfter < 0 || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid pause interval or unexpected arguments")
		return 2
	}
	switch c.script {
	case "", "lifecycle", "idle", "failure", "cancel", "create-close":
	default:
		fmt.Fprintln(stderr, "unknown script:", c.script)
		return 2
	}
	if c.script == "idle" && c.pauseAfter == 0 {
		c.pauseAfter = 500 * time.Millisecond
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if c.script != "" {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, 15*time.Second)
		defer timeoutCancel()
	}
	o := &output{encoder: json.NewEncoder(stdout)}
	d := &demo{c: c, o: o}
	o.event("start", "resident", c.resident, "script", c.script, "pause_after", c.pauseAfter.String())
	mode := dfx2.WindowApplication
	if c.resident {
		mode = dfx2.ResidentApplication
	}
	err := dfx2.Run(ctx, dfx2.Options{Mode: mode}, func(a *dfx2.App) error {
		d.a = a
		if c.script == "create-close" {
			if err := appcore.CloseNextCreation(a); err != nil {
				return err
			}
			w, err := a.NewWindow(dfx2.WindowOptions{Title: "dfx2 lifetime starting", Width: 420, Height: 240})
			if !errors.Is(err, dfx2.ErrClosed) || w == nil || w.Context().Err() == nil {
				return fmt.Errorf("creation-close did not close starting owner: %v", err)
			}
			d.watch(&demoWindow{w: w, name: "starting"})
			o.event("verified-create-close")
			if c.resident {
				a.Quit()
			}
			return nil
		}
		first, err := d.open("A")
		if err != nil {
			return err
		}
		second, err := d.open("B")
		if err != nil {
			return err
		}
		if c.script != "" {
			return a.Go("script-"+c.script, func(ctx context.Context) error { return d.script(ctx, first, second, cancel) })
		}
		if c.resident {
			messages := readConsole(ctx, stdin)
			return a.Go("console", func(ctx context.Context) error { return d.console(ctx, messages) })
		}
		return nil
	})
	d.watchers.Wait()
	if d.a != nil {
		o.status("drained", d.a)
	}
	if err != nil {
		o.event("return", "error", err.Error())
		if cancellationOnly(err) {
			return 0
		}
		return 1
	}
	o.event("return", "error", "")
	return 0
}

func cancellationOnly(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if child != nil && !cancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return cancellationOnly(wrapped.Unwrap())
	}
	return err == context.Canceled
}

func main() { os.Exit(execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
