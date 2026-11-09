// Command mixer is the synthetic mixer example. It shows text and one animated
// meter fed by a synthetic, application-owned sampler, on Linux and Windows.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/text/gotext"
	"github.com/michaelquigley/dfx2/ui"
)

type config struct {
	resident   bool
	paused     bool
	pauseAfter time.Duration
	script     string
	snapshots  string
	modalWait  time.Duration
	controls   bool
}

func main() {
	var c config
	flag.BoolVar(&c.resident, "resident", false, "keep running with zero windows; console commands open, close, pause, resume, status, quit")
	flag.BoolVar(&c.paused, "paused", false, "start with the synthetic feed paused")
	flag.DurationVar(&c.pauseAfter, "pause-after", 0, "pause the synthetic feed after this long, for idle checks")
	flag.StringVar(&c.script, "script", "", "bounded native audition: animate, resize, minimize, close-sample, reopen, two, expose, large, fonts, signal, session, modal-move, modal-size, modal-idle, modal-close-self, modal-close-other or modal-quit")
	flag.DurationVar(&c.modalWait, "modal-wait", 20*time.Second, "how long modal scripts wait for a size/move loop to start, from the audition driver or a person dragging")
	flag.StringVar(&c.snapshots, "snapshots", "", "directory for retained-image PNG snapshots written by scripts")
	flag.BoolVar(&c.controls, "controls", false, "two windows of channel strips with faders and mute checkboxes over one model; console commands below")
	fonts := flag.String("font", "sans-serif", "font families, comma-separated, in order of preference")
	flag.Parse()
	gotext.Families = strings.Split(*fonts, ",")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := &output{encoder: json.NewEncoder(os.Stdout)}
	mode := dfx2.WindowApplication
	if c.resident || c.script == "reopen" {
		mode = dfx2.ResidentApplication
	}
	m := &mixer{c: c, o: o, src: newSource(!c.paused), desk: newDesk()}
	err := dfx2.Run(ctx, dfx2.Options{Mode: mode}, m.setup)
	m.watchers.Wait()
	o.event("return", "error", fmt.Sprint(err), "invalidations_refused_after_close", m.src.refused())
	if m.a != nil { // Run can fail before setup, when the backend cannot open
		o.event("drained", status(m.a)...)
	}
	// A signal cancels cooperatively and drains; that is a clean exit.
	if err != nil && !(errors.Is(err, context.Canceled) && ctx.Err() != nil) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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

func status(a *dfx2.App) []any {
	s := appcore.Inspect(a)
	return []any{"windows", s.Windows, "tasks", s.Tasks, "queued", s.Queued, "native_completions", s.NativeCompletions,
		"terminal", s.Terminal, "release_pending", s.ReleasePending, "frames", s.Frames, "animating", s.Animating,
		"frame_timers", s.FrameTimers, "input_queued", inputQueued(a), "wakes", s.Native.Wakes, "dispatches", s.Native.Dispatches,
		"presents", s.Native.Presents, "paints", s.Native.Paints, "paint_failures", s.Native.PaintFailures,
		"modal_active", s.Native.ModalActive, "modal_loops", s.Native.ModalLoops, "modal_turns", s.Native.ModalTurns,
		"modal_timers", s.Native.ModalTimers, "modal_refusals", s.Native.ModalRefusals, "native_windows", s.Native.Windows,
		"native_retired", s.Native.Retired, "dispatch_targets", s.Native.DispatchTargets, "native_problem", s.Native.Problem}
}

// source is the application-owned synthetic sampler state. The sampler task
// publishes under the mutex; views read a snapshot. Windows subscribe to
// invalidation and unsubscribe on close; a publication racing a close gets
// dfx2.ErrClosed from Invalidate, which is expected.
type source struct {
	mu          sync.Mutex
	level       float64
	db          float64
	tick        uint64
	running     bool
	subscribers map[*ui.Window]bool
	control     chan bool
	closed      uint64 // invalidations refused by closing windows
}

func newSource(running bool) *source {
	return &source{running: running, db: -60, subscribers: make(map[*ui.Window]bool), control: make(chan bool, 1)}
}

type snapshot struct {
	level, db float64
	tick      uint64
	running   bool
}

func (s *source) snapshot() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return snapshot{s.level, s.db, s.tick, s.running}
}

func (s *source) subscribe(w *ui.Window) {
	s.mu.Lock()
	s.subscribers[w] = true
	s.mu.Unlock()
}

func (s *source) unsubscribe(w *ui.Window) {
	s.mu.Lock()
	delete(s.subscribers, w)
	s.mu.Unlock()
}

func (s *source) refused() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *source) setRunning(on bool) {
	select {
	case s.control <- on:
	default:
		// A pending command is replaced by the newest one.
		select {
		case <-s.control:
		default:
		}
		s.control <- on
	}
}

func (s *source) invalidateAll() {
	s.mu.Lock()
	windows := make([]*ui.Window, 0, len(s.subscribers))
	for w := range s.subscribers {
		windows = append(windows, w)
	}
	s.mu.Unlock()
	for _, w := range windows {
		if err := w.Invalidate(); errors.Is(err, dfx2.ErrClosed) {
			s.mu.Lock()
			s.closed++
			s.mu.Unlock()
		}
	}
}

// run is the 30 Hz sampler, owned by the application rather than a window.
func (s *source) run(ctx context.Context, o *output) error {
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	var n uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case on := <-s.control:
			s.mu.Lock()
			changed := s.running != on
			s.running = on
			s.mu.Unlock()
			if changed {
				o.event("feed", "running", on)
				s.invalidateAll()
			}
		case <-ticker.C:
			s.mu.Lock()
			running := s.running
			if running {
				n++
				s.tick = n
				s.db = levelDB(n)
				s.level = min(max((s.db+60)/60, 0), 1)
			}
			s.mu.Unlock()
			if running {
				s.invalidateAll()
			}
		}
	}
}

// levelDB is a deterministic synthetic level: two slow sines and hashed jitter.
func levelDB(n uint64) float64 {
	t := float64(n) / 30
	v := 0.5 + 0.3*math.Sin(2*math.Pi*0.23*t) + 0.12*math.Sin(2*math.Pi*1.7*t)
	x := n + 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	v += 0.15 * (float64((x^x>>31)>>11)/(1<<53) - 0.5)
	return 20 * math.Log10(min(max(v, 0.001), 1))
}

type mixer struct {
	a        *dfx2.App
	c        config
	o        *output
	src      *source
	watchers sync.WaitGroup
	opened   int
	initial  []*ui.Window
	titles   []*titleCounter
	desk     *desk

	evaluations atomic.Uint64 // -controls view evaluations
}

func (m *mixer) draw(f *ui.Frame) {
	s := m.src.snapshot()
	state := "running"
	if !s.running {
		state = "paused"
	}
	f.Label("dfx2 synthetic mixer — text and an animated meter")
	f.Label(fmt.Sprintf("Level %6.1f dB · tick %d · feed %s", s.db, s.tick, state))
	f.Meter("level", s.level)
	f.Label("Fallback: Ελληνικά, Кириллица, עברית, العربية, 日本語, emoji 🎛️🎚️🎹 — mixed שלום world direction.")
	f.Label("Resize the window: this paragraph rewraps from cached shaping without evaluating the view again, and exposure repaints the retained image.")
}

func (m *mixer) open() (*ui.Window, error) {
	m.opened++
	name := fmt.Sprintf("%c", 'A'+(m.opened-1)%26)
	options, view := dfx2.WindowOptions{Title: "dfx2 mixer " + name, Width: 560, Height: 260}, m.draw
	if m.c.controls {
		options.Width, options.Height, view = 460, 300, m.controlsView(name)
	}
	w, err := ui.Open(m.a, options, view)
	if err != nil {
		return nil, err
	}
	m.src.subscribe(w)
	m.watchers.Add(1)
	go func() {
		defer m.watchers.Done()
		<-w.Done()
		m.src.unsubscribe(w)
		m.o.event("closed", "window", name)
		m.o.event("ownership", status(m.a)...)
	}()
	m.o.event("opened", "window", name)
	return w, nil
}

func (m *mixer) setup(a *dfx2.App) error {
	m.a = a
	if err := a.Go("sampler", func(ctx context.Context) error { return m.src.run(ctx, m.o) }); err != nil {
		return err
	}
	if m.c.pauseAfter > 0 {
		if err := a.Go("pause-after", func(ctx context.Context) error {
			select {
			case <-ctx.Done():
			case <-time.After(m.c.pauseAfter):
				m.src.setRunning(false)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if m.c.resident || m.c.controls {
		if err := a.Go("console", func(ctx context.Context) error { return m.console(ctx, os.Stdin) }); err != nil {
			return err
		}
	}
	switch m.c.script {
	case "":
		if m.c.controls {
			for range 2 {
				if _, err := m.open(); err != nil {
					return err
				}
			}
			return nil
		}
		if !m.c.resident {
			_, err := m.open()
			return err
		}
		return nil
	case "animate", "resize", "minimize", "close-sample", "reopen", "two", "expose", "large", "fonts", "signal", "session",
		"modal-move", "modal-size", "modal-idle", "modal-close-self", "modal-close-other", "modal-quit":
		// Window mode exits when setup leaves no window, so scripts open
		// their first windows here; resident reopen starts with none.
		count := 1
		switch {
		case m.c.script == "reopen":
			count = 0
		case m.c.script == "two", m.c.script == "signal", strings.HasPrefix(m.c.script, "modal-"):
			count = 2
		}
		for range count {
			w, err := m.open()
			if err != nil {
				return err
			}
			m.initial = append(m.initial, w)
		}
		if count == 2 && m.c.script != "two" {
			if err := m.startTitles(); err != nil {
				return err
			}
		}
		return a.Go("script-"+m.c.script, m.runScript)
	}
	return fmt.Errorf("unknown script %q", m.c.script)
}

// console is cancellable: blocking stdin scanning stays in a caller-owned
// goroutine, so it can never hold Run open.
func (m *mixer) console(ctx context.Context, in io.Reader) error {
	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			lines <- strings.TrimSpace(scanner.Text())
		}
		close(lines)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if m.deskCommand(line) {
				continue
			}
			switch line {
			case "open":
				m.post(func() {
					if _, err := m.open(); err != nil {
						m.o.event("open-error", "error", err.Error())
					}
				})
			case "close":
				m.post(func() {
					if ws := appcore.ContentWindows(m.a); len(ws) > 0 {
						ws[len(ws)-1].Close()
					}
				})
			case "pause":
				m.src.setRunning(false)
			case "resume":
				m.src.setRunning(true)
			case "status":
				m.record("status", "evaluations", m.evaluations.Load())
			case "quit":
				m.a.Quit()
				return nil
			case "":
			default:
				m.o.event("unknown-command", "command", line)
			}
		}
	}
}

func (m *mixer) post(f func()) {
	if err := m.a.Post(f); err != nil {
		m.o.event("post-error", "error", err.Error())
	}
}

// call runs f on the UI thread and waits for it, from a script task. Scripts
// are owned tasks, so they may wait; the UI thread never does.
func (m *mixer) call(ctx context.Context, f func() error) error {
	done := make(chan error, 1)
	if err := m.a.Post(func() { done <- f() }); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
