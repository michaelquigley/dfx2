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
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/text"
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
	delay      time.Duration
	textBudget string
	churn      int
}

func main() {
	var c config
	flag.BoolVar(&c.resident, "resident", false, "keep running with zero windows; console commands open, close, pause, resume, status, quit")
	flag.BoolVar(&c.paused, "paused", false, "start with the synthetic feed paused")
	flag.DurationVar(&c.pauseAfter, "pause-after", 0, "pause the synthetic feed after this long, for idle checks")
	flag.StringVar(&c.script, "script", "", "bounded native audition: animate, resize, minimize, close-sample, reopen, two, expose, large, fonts, signal, session, modal-move, modal-size, modal-idle, modal-close-self, modal-close-other or modal-quit")
	flag.DurationVar(&c.modalWait, "modal-wait", 20*time.Second, "how long modal scripts wait for a size/move loop to start, from the audition driver or a person dragging")
	flag.StringVar(&c.snapshots, "snapshots", "", "directory for retained-image PNG snapshots written by scripts")
	flag.BoolVar(&c.controls, "controls", false, "the synthetic mixer: two windows of six channel strips over one model, with an ordered command worker; console commands in the README")
	flag.DurationVar(&c.delay, "delay", 0, "initial synthetic device delay before each command reaches the device (-controls)")
	flag.StringVar(&c.textBudget, "text-budget", "default", "per-window text cache budget for auditions: default, small, or GLYPH_BYTES:PARAGRAPH_BYTES:PARAGRAPH_ENTRIES")
	flag.IntVar(&c.churn, "churn", 0, "words of changing text in a churn label, to exercise the text caches (-controls)")
	fonts := flag.String("font", "sans-serif", "font families, comma-separated, in order of preference")
	flag.Parse()
	gotext.Families = strings.Split(*fonts, ",")
	budget, err := parseBudget(c.textBudget)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	text.DefaultBudget = budget // each window copies it as it opens

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := &output{encoder: json.NewEncoder(os.Stdout)}
	mode := dfx2.WindowApplication
	if c.resident || c.script == "reopen" {
		mode = dfx2.ResidentApplication
	}
	m := newMixer(c, o, os.Stdin)
	err = dfx2.Run(ctx, dfx2.Options{Mode: mode}, m.setup)
	m.watchers.Wait()
	k := m.model.Snapshot().Commands
	o.event("return", "error", fmt.Sprint(err), "invalidations_refused_after_close", m.model.refusedInvalidations(),
		"commands", commandValues(k), "commands_conserved", k.Accepted == k.Applied+k.Rejected+k.Canceled && k.Outstanding == 0)
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

// smallTextBudget makes the text caches evict under the churn label and the
// mixer's changing numbers at scale 1, for auditions; the default budget is
// ui's own. Eviction needs headroom above the view's steady working set,
// because an entry the last frame used is never evicted: below it, the excess
// is served transiently every frame and nothing evicts. Glyph pixels grow with
// the square of the scale, so auditions at other scales pass explicit sizes.
var smallTextBudget = text.Budget{GlyphBytes: 48 << 10, ParagraphBytes: 64 << 10, ParagraphEntries: 48, Transient: text.DefaultBudget.Transient}

// parseBudget reads -text-budget.
func parseBudget(v string) (text.Budget, error) {
	switch v {
	case "default":
		return text.DefaultBudget, nil
	case "small":
		return smallTextBudget, nil
	}
	b := text.Budget{Transient: text.DefaultBudget.Transient}
	if _, err := fmt.Sscanf(v, "%d:%d:%d", &b.GlyphBytes, &b.ParagraphBytes, &b.ParagraphEntries); err != nil ||
		b.GlyphBytes <= 0 || b.ParagraphBytes <= 0 || b.ParagraphEntries <= 0 || fmt.Sprintf("%d:%d:%d", b.GlyphBytes, b.ParagraphBytes, b.ParagraphEntries) != v {
		return text.Budget{}, fmt.Errorf("-text-budget %q: want default, small, or three positive integers GLYPH_BYTES:PARAGRAPH_BYTES:PARAGRAPH_ENTRIES", v)
	}
	return b, nil
}

// named is an open window: its ui wrapper and, for internal inspection, the
// core window behind it.
type named struct {
	ui   *ui.Window
	core *appcore.Window
}

type mixer struct {
	a        *dfx2.App
	c        config
	o        *output
	in       io.Reader
	model    *model
	cmds     *commands
	watchers sync.WaitGroup
	opened   int
	windows  map[string]named // windows by name; UI thread
	initial  []*ui.Window
	titles   []*titleCounter
	trace    trace

	evaluations atomic.Uint64 // -controls view evaluations
}

func newMixer(c config, o *output, in io.Reader) *mixer {
	m := &mixer{c: c, o: o, in: in, model: newModel(!c.paused), windows: make(map[string]named)}
	m.model.stats.Delay, m.model.churn = c.delay, c.churn
	m.model.current = m.model.snapshotLocked()
	m.cmds = newCommands(m.model, func(r Record) {
		values := []any{"state", r.State, "sequence", r.Seq, "window", r.Command.Window, "channel", r.Command.Channel,
			"control", r.Command.Control, "value", r.Command.Value()}
		if r.Reason != "" {
			values = append(values, "reason", r.Reason)
		}
		if r.Latency > 0 {
			values = append(values, "latency_ms", float64(r.Latency.Microseconds())/1000)
		}
		o.event("command", values...)
	})
	return m
}

func commandValues(k CommandStats) map[string]any {
	return map[string]any{"accepted": k.Accepted, "applied": k.Applied, "rejected": k.Rejected, "refused": k.Refused,
		"canceled": k.Canceled, "outstanding": k.Outstanding, "delay_ms": k.Delay.Milliseconds(), "reject_next": k.RejectNext}
}

func (m *mixer) draw(f *ui.Frame) {
	s := m.model.Snapshot()
	state := "running"
	if !s.Running {
		state = "paused"
	}
	f.Label("dfx2 synthetic mixer — text and an animated meter")
	f.Label(fmt.Sprintf("Level %6.1f dB · tick %d · feed %s", s.DB, s.Tick, state))
	f.Meter("level", s.Level)
	f.Label("Fallback: Ελληνικά, Кириллица, עברית, العربية, 日本語, emoji 🎛️🎚️🎹 — mixed שלום world direction.")
	f.Label("Resize the window: this paragraph rewraps from cached shaping without evaluating the view again, and exposure repaints the retained image.")
}

func (m *mixer) open() (*ui.Window, error) {
	m.opened++
	name := fmt.Sprintf("%c", 'A'+(m.opened-1)%26)
	options, view := dfx2.WindowOptions{Title: "dfx2 mixer " + name, Width: 560, Height: 260}, m.draw
	if m.c.controls {
		options.Width, options.Height, view = 760, 520, m.controlsView(name)
	}
	w, err := ui.Open(m.a, options, view)
	if err != nil {
		return nil, err
	}
	m.model.subscribe(w)
	cores := appcore.ContentWindows(m.a) // creation order, so the newest is w's
	m.windows[name] = named{w, cores[len(cores)-1]}
	m.watchers.Add(1)
	go func() {
		defer m.watchers.Done()
		<-w.Done()
		m.model.unsubscribe(w)
		// Forget the name on the UI thread, unless a later window reused it.
		_ = m.a.Post(func() {
			if m.windows[name].ui == w {
				delete(m.windows, name)
			}
		})
		m.o.event("closed", "window", name)
		m.o.event("ownership", status(m.a)...)
	}()
	m.o.event("opened", "window", name)
	return w, nil
}

func (m *mixer) setup(a *dfx2.App) error {
	m.a = a
	// The producer and the command worker belong to the application, not to
	// either window: closing one window leaves them and the other window alive.
	if err := a.Go("sampler", func(ctx context.Context) error { return m.model.run(ctx, m.o) }); err != nil {
		return err
	}
	if err := a.Go("commands", m.cmds.run); err != nil {
		return err
	}
	if m.c.pauseAfter > 0 {
		if err := a.Go("pause-after", func(ctx context.Context) error {
			select {
			case <-ctx.Done():
			case <-time.After(m.c.pauseAfter):
				m.model.setRunning(false)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if m.c.resident || m.c.controls {
		if err := a.Go("console", func(ctx context.Context) error { return m.console(ctx, m.in) }); err != nil {
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
			m.command(ctx, line)
		}
	}
}

// command runs one console line. Model commands are external changes: they
// publish a snapshot and never submit a command.
func (m *mixer) command(ctx context.Context, line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	if m.c.controls {
		known, err := m.model.external(line)
		switch {
		case known && err != nil:
			m.o.event("external-error", "command", line, "error", err.Error())
			return
		case known:
			m.o.event("external", "command", line)
			return
		}
	}
	bad := func(err error) { m.o.event("command-error", "command", line, "error", err.Error()) }
	switch {
	case fields[0] == "snapshot" && (len(fields) == 2 || len(fields) == 3) && m.c.controls:
		window := ""
		if len(fields) == 3 {
			window = fields[2]
		}
		m.snapshotWindow(fields[1], window)
	case fields[0] == "delay" && len(fields) == 2:
		d, err := time.ParseDuration(fields[1])
		if err != nil || d < 0 {
			bad(fmt.Errorf("delay takes a non-negative duration such as 250ms"))
			return
		}
		m.model.setDelay(d)
		m.o.event("fixture", "delay_ms", d.Milliseconds())
	case fields[0] == "reject" && len(fields) == 2:
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 0 {
			bad(fmt.Errorf("reject takes a count of commands for the device to refuse"))
			return
		}
		m.model.setRejectNext(n)
		m.o.event("fixture", "reject_next", n)
	case fields[0] == "churn" && len(fields) == 2:
		n, err := strconv.Atoi(fields[1])
		if fields[1] == "off" {
			n, err = 0, nil
		}
		if err != nil || n < 0 || n > 48 {
			bad(fmt.Errorf("churn takes off or a word count up to 48"))
			return
		}
		m.model.setChurn(n)
		m.o.event("fixture", "churn", n)
	case line == "open":
		m.post(func() {
			if _, err := m.open(); err != nil {
				m.o.event("open-error", "error", err.Error())
			}
		})
	case fields[0] == "close" && len(fields) <= 2:
		m.post(func() {
			if len(fields) == 2 {
				if w, ok := m.windows[fields[1]]; ok {
					w.ui.Close()
					return
				}
				m.o.event("command-error", "command", line, "error", "no such window")
				return
			}
			if ws := appcore.ContentWindows(m.a); len(ws) > 0 {
				ws[len(ws)-1].Close()
			}
		})
	case line == "pause":
		m.model.setRunning(false)
	case line == "resume":
		m.model.setRunning(true)
	case line == "status":
		m.record("status", m.statusExtra(ctx)...)
	case line == "quit":
		m.a.Quit()
	default:
		m.o.event("unknown-command", "command", line)
	}
}

// statusExtra adds the mixer's own counters to a status record: view
// evaluations, commands, process CPU time and, gathered on the UI thread,
// each window's text cache counters.
func (m *mixer) statusExtra(ctx context.Context) []any {
	s := m.model.Snapshot()
	k := s.Commands
	channels := make([]map[string]any, len(s.Channels))
	for i, c := range s.Channels {
		channels[i] = map[string]any{"id": c.ID, "gain": c.Gain, "muted": c.Muted, "gain_pending": c.GainPending, "pending_gain": c.PendingGain,
			"mute_pending": c.MutePending, "pending_mute": c.PendingMute}
	}
	values := []any{"evaluations", m.evaluations.Load(), "commands", commandValues(k), "commands_outstanding", k.Outstanding,
		"cpu_seconds", cpuTime().Seconds(), "channels", channels, "tick", s.Tick, "last_error", s.Error}
	caches := map[string]any{}
	if err := m.call(ctx, func() error {
		for name, w := range m.windows {
			content, err := appcore.AuditionContent(w.core)
			if err != nil {
				continue // closing or closed
			}
			if t, ok := content.(interface{ TextStats() text.Stats }); ok {
				caches[name] = textValues(t.TextStats())
			}
		}
		return nil
	}); err != nil {
		values = append(values, "text_error", err.Error())
	}
	return append(values, "text", caches, "text_budget", textValues(text.Stats{
		GlyphBytes: text.DefaultBudget.GlyphBytes, ParagraphBytes: text.DefaultBudget.ParagraphBytes, ParagraphEntries: text.DefaultBudget.ParagraphEntries}))
}

func textValues(s text.Stats) map[string]any {
	return map[string]any{"glyph_entries": s.GlyphEntries, "glyph_bytes": s.GlyphBytes, "paragraph_entries": s.ParagraphEntries,
		"paragraph_bytes": s.ParagraphBytes, "glyph_evictions": s.GlyphEvictions, "paragraph_evictions": s.ParaEvictions,
		"shapes": s.Shapes, "rasterized": s.Rasterized, "transient_glyphs": s.TransientGlyphs, "transient_paragraphs": s.TransientParas}
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
