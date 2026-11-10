package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
	"github.com/michaelquigley/dfx2/internal/text"
	"github.com/michaelquigley/dfx2/internal/text/gotext"
)

// These fixtures run the example's real model, command worker and views
// through the real core and ui widgets, over the fake backend. Ordering comes
// from barriers, never from sleeps: the device gate holds each command until
// the test releases it, input batches arrive inside one posted callback, and
// idle is decided by the pump hook between dispatch turns. Timeouts only guard
// against hangs.

var presetFonts = sync.OnceValue(func() error {
	engine, err := gotext.NewFaces(goregular.TTF)
	if err != nil {
		return err
	}
	return gotext.Preset(engine) // pinned Go Regular; no host font discovery
})

const hang = 5 * time.Second

// A strip's fader in the controls view, in logical units: the root column's
// 16 of padding, then strips 110 wide with 12 between; the fader is 36 wide
// and 160 tall with a 12-tall thumb.
func faderAt(strip int, value float64) (float64, float64) {
	return 16 + float64(strip)*(stripWidth+stripGap) + 18, 16 + 6 + (1-value)*148
}

type bench struct {
	t       *testing.T
	fake    *fake.Backend
	m       *mixer
	app     *dfx2.App
	done    chan error
	cancel  context.CancelFunc
	log     *bytes.Buffer
	held    chan Command  // the worker is holding this command at the device
	release chan struct{} // lets the held command reach the device
	records chan Record   // resolutions only: applied, rejected by the device, canceled

	mu   sync.Mutex
	all  []Record
	idle chan error
	// shown is the latest value each window's widgets returned per channel.
	shown  map[string]map[string][2]any
	labels map[string][]string // the labels each window recorded in its latest evaluation
}

// runMixer starts the controls mixer, paused, with two windows A and B and a
// device that holds every command until the test releases it.
func runMixer(t *testing.T) *bench {
	t.Helper()
	if err := presetFonts(); err != nil {
		t.Fatal(err)
	}
	b := &bench{t: t, fake: &fake.Backend{}, done: make(chan error, 1), log: &bytes.Buffer{},
		held: make(chan Command, 256), release: make(chan struct{}), records: make(chan Record, 1024),
		shown: map[string]map[string][2]any{}, labels: map[string][]string{}}
	o := &output{encoder: json.NewEncoder(b.log)}
	b.m = newMixer(config{controls: true, paused: true}, o, strings.NewReader(""))
	b.m.cmds.gate = func(ctx context.Context, cmd Command) error {
		b.held <- cmd
		select {
		case <-b.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	program := b.m.cmds.record
	b.m.cmds.record = func(r Record) {
		program(r)
		b.mu.Lock()
		b.all = append(b.all, r)
		b.mu.Unlock()
		if r.State != "accepted" && r.Reason != errQueueFull.Error() && r.Reason != errShuttingDown.Error() {
			b.records <- r
		}
	}
	b.m.trace = trace{
		begin: func(window string) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.labels[window] = nil
		},
		shown: func(window string, c Channel, gain float64, muted bool) {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.shown[window] == nil {
				b.shown[window] = map[string][2]any{}
			}
			b.shown[window][c.ID] = [2]any{gain, muted}
		},
		label: func(window, text string) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.labels[window] = append(b.labels[window], text)
		},
	}
	b.fake.BeforePump = b.quiet
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	t.Cleanup(cancel)
	go func() {
		b.done <- appcore.RunBackend(ctx, dfx2.Options{Logf: func(string, ...any) {}}, func(a *dfx2.App) error {
			defer close(ready)
			b.app = a
			return b.m.setup(a)
		}, b.fake)
	}()
	select {
	case <-ready:
	case <-time.After(hang):
		t.Fatal("setup did not run")
	}
	for _, w := range appcore.ContentWindows(b.app) {
		b.surface(w)
	}
	b.waitIdle()
	return b
}

func (b *bench) surface(w *appcore.Window) {
	b.fake.Enqueue(platform.Event{Kind: platform.SurfaceChanged, Window: appcore.WindowID(w),
		Surface: platform.Surface{Width: 760, Height: 520, Scale: 1, Visible: true}})
}

// window returns the named window's core window. Windows are named in
// creation order.
func (b *bench) window(name string) *appcore.Window {
	b.t.Helper()
	var w named
	b.onUIThread(func() { w = b.m.windows[name] })
	if w.core == nil {
		b.t.Fatalf("no window %s", name)
	}
	return w.core
}

// input delivers one batch to a window from inside a single posted callback,
// so the whole batch is queued before any frame can take part of it.
func (b *bench) input(window string, inputs ...platform.Input) {
	b.t.Helper()
	id := appcore.WindowID(b.window(window))
	b.onUIThread(func() {
		for _, in := range inputs {
			b.fake.Emit(platform.Event{Kind: platform.InputReceived, Window: id, Input: in})
		}
	})
	b.waitIdle()
}

func (b *bench) onUIThread(f func()) {
	b.t.Helper()
	ran := make(chan struct{})
	if err := b.app.Post(func() {
		defer close(ran)
		f()
	}); err != nil {
		b.t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(hang):
		b.t.Fatal("posted callback did not run")
	}
}

// waitIdle returns once no window owes a frame. The decision is the pump
// hook's, between dispatch turns, where no frame can be in flight; arming it
// in a posted callback means the first boundary it can see follows that turn.
// The worker must be held or idle meanwhile, or a publication could follow.
func (b *bench) waitIdle() {
	b.t.Helper()
	idle := make(chan error, 1)
	b.onUIThread(func() {
		b.mu.Lock()
		b.idle = idle
		b.mu.Unlock()
	})
	select {
	case err := <-idle:
		if err != nil {
			b.t.Fatal(err)
		}
	case <-time.After(hang):
		b.t.Fatalf("not idle: %+v", appcore.Inspect(b.app))
	}
}

func (b *bench) quiet() {
	b.mu.Lock()
	idle := b.idle
	b.mu.Unlock()
	if idle == nil {
		return
	}
	s := appcore.Inspect(b.app)
	if s.WakePending || s.Queued != 0 || s.Animating != 0 || b.fake.Queued() != 0 {
		return
	}
	for _, w := range appcore.ContentWindows(b.app) {
		if cs, ok := appcore.InspectContent(w); !ok || cs.InputQueued != 0 || cs.Generation != cs.Consumed {
			return
		}
	}
	b.mu.Lock()
	b.idle = nil
	b.mu.Unlock()
	close(idle)
}

// publish stands in for a meter sample arriving: a new snapshot with the
// device state unchanged, so every window evaluates its view again.
func (b *bench) publish() {
	b.t.Helper()
	b.m.model.publish(func() bool { return true })
	b.waitIdle()
}

// guardedPublish publishes from another goroutine under the hang guard, for
// hooks that would deadlock if publication held the model's mutex.
func (b *bench) guardedPublish() {
	b.t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.m.model.publish(func() bool { return true })
	}()
	select {
	case <-done:
	case <-time.After(hang):
		b.t.Fatal("publication blocked: it must not hold the model's mutex while invalidating")
	}
}

// heldNext waits for the worker to hold the next command at the device.
func (b *bench) heldNext() Command {
	b.t.Helper()
	select {
	case cmd := <-b.held:
		return cmd
	case <-time.After(hang):
		b.t.Fatal("no command reached the device gate")
		return Command{}
	}
}

// ack lets the held command reach the device and waits for its record and
// the frames its publication requested.
func (b *bench) ack(wantSeq uint64) Record {
	b.t.Helper()
	if cmd := b.heldNext(); cmd.Seq != wantSeq {
		b.t.Fatalf("device holds #%d, want #%d", cmd.Seq, wantSeq)
	}
	select {
	case b.release <- struct{}{}:
	case <-time.After(hang):
		b.t.Fatal("worker did not take the release")
	}
	r := b.nextRecord()
	if r.Seq != wantSeq {
		b.t.Fatalf("record %v, want #%d", r, wantSeq)
	}
	b.waitIdle()
	return r
}

func (b *bench) nextRecord() Record {
	b.t.Helper()
	select {
	case r := <-b.records:
		return r
	case <-time.After(hang):
		b.t.Fatal("no command record")
		return Record{}
	}
}

func (b *bench) recordStrings(states ...string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, r := range b.all {
		if len(states) == 0 || slices.Contains(states, r.State) {
			out = append(out, fmt.Sprintf("#%d %s %s=%s", r.Seq, r.State, r.Command.Control, r.Command.ValueText()))
		}
	}
	return out
}

func (b *bench) wantRecords(states []string, want ...string) {
	b.t.Helper()
	if got := b.recordStrings(states...); !slices.Equal(got, want) {
		b.t.Fatalf("records %q\nwant    %q", got, want)
	}
}

// wantShown checks what a window's widgets last returned for a channel.
func (b *bench) wantShown(window, channel string, gain float64, muted bool) {
	b.t.Helper()
	b.mu.Lock()
	got := b.shown[window][channel]
	b.mu.Unlock()
	if g, ok := got[0].(float64); !ok || fmt.Sprintf("%.2f", g) != fmt.Sprintf("%.2f", gain) || got[1] != muted {
		b.t.Fatalf("window %s channel %s shows %v, want gain %.2f muted %t", window, channel, got, gain, muted)
	}
}

func (b *bench) wantLabel(window, substring string) {
	b.t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range b.labels[window] {
		if strings.Contains(l, substring) {
			return
		}
	}
	b.t.Fatalf("window %s rendered no label containing %q: %q", window, substring, b.labels[window])
}

func (b *bench) channel(id string) Channel {
	for _, c := range b.m.model.Snapshot().Channels {
		if c.ID == id {
			return c
		}
	}
	b.t.Fatalf("no channel %s", id)
	return Channel{}
}

// stop closes every window, which ends a window application, and returns
// Run's result after the worker has drained.
func (b *bench) stop() error {
	b.t.Helper()
	b.onUIThread(func() {
		for _, w := range b.m.windows {
			w.ui.Close()
		}
	})
	return b.wait()
}

func (b *bench) wait() error {
	b.t.Helper()
	select {
	case err := <-b.done:
		if v := b.fake.Violations(); len(v) != 0 {
			b.t.Fatal(v)
		}
		return err
	case <-time.After(hang):
		b.t.Fatalf("Run did not return: %+v", appcore.Inspect(b.app))
		return nil
	}
}

func space() []platform.Input {
	return []platform.Input{{Kind: platform.KeyDown, Key: platform.KeySpace}, {Kind: platform.KeyUp, Key: platform.KeySpace}}
}

func keyDown(k platform.Key, mods ...platform.Modifiers) platform.Input {
	in := platform.Input{Kind: platform.KeyDown, Key: k}
	for _, m := range mods {
		in.Modifiers |= m
	}
	return in
}

var (
	accepted = []string{"accepted"}
	resolved = []string{"applied", "rejected", "canceled"}
)

// The work order's combined fixture. From mute=false and gain=0.50, two mute
// toggles and two Up presses, each followed by a meter sample, all before any
// acknowledgment: the device's snapshot stays stale throughout, and each edit
// must start from the example's pending value, not from it.
func TestPendingEditsAcrossDelayedAcknowledgmentsKeepTheirOrderAndBase(t *testing.T) {
	b := runMixer(t)
	b.wantShown("A", "a", 0.5, false)

	b.input("A", keyDown(platform.KeyTab), keyDown(platform.KeyTab)) // fader a, then mute a
	b.input("A", space()...)
	b.publish()
	b.wantShown("A", "a", 0.5, true)
	b.wantShown("B", "a", 0.5, true) // the other window shows the pending value too
	b.input("A", space()...)
	b.publish()
	b.input("A", keyDown(platform.KeyTab, platform.ModShift)) // back to fader a
	b.input("A", keyDown(platform.KeyArrowUp))
	b.publish()
	b.wantShown("A", "a", 0.51, false)
	b.input("A", keyDown(platform.KeyArrowUp))
	b.publish()

	b.wantRecords(accepted, "#1 accepted mute=on", "#2 accepted mute=off", "#3 accepted gain=0.51", "#4 accepted gain=0.52")
	if c := b.channel("a"); c.Gain != 0.5 || c.Muted || !c.GainPending || !c.MutePending {
		t.Fatalf("device state moved before any acknowledgment: %+v", c)
	}
	b.wantShown("A", "a", 0.52, false)
	b.wantShown("B", "a", 0.52, false)
	b.wantLabel("A", "pending 0.52 #4, unmute #2")

	// Acknowledgments arrive one by one as device snapshots. An earlier
	// command's acknowledgment never shows its now superseded value.
	b.ack(1)
	b.wantShown("A", "a", 0.52, false)
	if c := b.channel("a"); !c.Muted || !c.MutePending {
		t.Fatalf("after #1: %+v", c)
	}
	b.ack(2)
	b.ack(3)
	b.wantShown("A", "a", 0.52, false)
	b.ack(4)
	b.wantShown("A", "a", 0.52, false)
	b.wantShown("B", "a", 0.52, false)
	b.wantLabel("A", "in sync")
	b.wantRecords(resolved, "#1 applied mute=on", "#2 applied mute=off", "#3 applied gain=0.51", "#4 applied gain=0.52")
	if c := b.channel("a"); c.Gain != 0.52 || c.Muted || c.GainPending || c.MutePending {
		t.Fatalf("device state after every acknowledgment: %+v", c)
	}

	// The device refuses #5 while #6 is outstanding: #6's later value stays
	// pending, and then applies.
	b.m.model.setRejectNext(1)
	b.waitIdle()
	b.input("A", keyDown(platform.KeyArrowUp))
	b.publish()
	b.input("A", keyDown(platform.KeyArrowUp))
	b.publish()
	b.ack(5)
	b.wantShown("A", "a", 0.54, false)
	b.wantLabel("A", "Last error: #5 a gain 0.53 rejected: device refused")
	b.wantLabel("B", "Last error: #5 a gain 0.53 rejected: device refused")
	b.ack(6)
	b.wantShown("A", "a", 0.54, false)

	// The device refuses the latest edit: the view returns to the device's
	// value, the error is shown, and reconciling submits nothing.
	b.m.model.setRejectNext(1)
	b.waitIdle()
	b.input("A", keyDown(platform.KeyArrowUp))
	b.wantShown("A", "a", 0.55, false)
	b.ack(7)
	b.wantShown("A", "a", 0.54, false)
	b.wantShown("B", "a", 0.54, false)
	b.wantLabel("A", "Last error: #7 a gain 0.55 rejected: device refused")
	evaluations := b.m.evaluations.Load()
	for range 3 {
		b.publish()
	}
	if b.m.evaluations.Load() < evaluations+6 {
		t.Fatal("the reconciled windows did not evaluate their views again")
	}
	b.wantShown("A", "a", 0.54, false)
	b.wantRecords(resolved, "#1 applied mute=on", "#2 applied mute=off", "#3 applied gain=0.51", "#4 applied gain=0.52",
		"#5 rejected gain=0.53", "#6 applied gain=0.54", "#7 rejected gain=0.55")
	if got := b.recordStrings(accepted...); len(got) != 7 {
		t.Fatalf("reconciliation submitted commands: %q", got)
	}
	if c := b.channel("a"); c.Gain != 0.54 || c.GainPending {
		t.Fatalf("device state after rejection: %+v", c)
	}
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// A rejected mute toggle returns to the device's state and leaves the next
// toggle starting from what is shown.
func TestARejectedToggleReturnsToTheDevicesState(t *testing.T) {
	b := runMixer(t)
	b.m.model.setRejectNext(1)
	b.waitIdle()
	b.input("B", keyDown(platform.KeyTab), keyDown(platform.KeyTab))
	b.input("B", space()...)
	b.wantShown("B", "a", 0.5, true)
	b.ack(1)
	b.wantShown("B", "a", 0.5, false)
	b.wantShown("A", "a", 0.5, false)
	b.input("B", space()...)
	b.ack(2)
	b.wantShown("B", "a", 0.5, true)
	b.wantRecords(nil, "#1 accepted mute=on", "#1 rejected mute=on", "#2 accepted mute=on", "#2 applied mute=on")
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// External snapshots move the views but never submit commands, including
// during a drag, where the gesture keeps its value; reordering keeps the
// gesture on its channel, and removing the channel ends it with no edit.
func TestExternalChangesNeverSubmitCommandsEvenDuringADrag(t *testing.T) {
	b := runMixer(t)
	go func() { // acknowledge everything at once; this test is about what is submitted
		for {
			select {
			case <-b.held:
			case <-t.Context().Done():
				return
			}
			select {
			case b.release <- struct{}{}:
			case <-t.Context().Done():
				return
			}
		}
	}()
	for _, line := range []string{"gain a 0.2", "mute a on", "gain b 0.9", "reorder", "order a b c d e f", "remove c", "insert c", "order a b c d e f"} {
		if known, err := b.m.model.external(line); !known || err != nil {
			t.Fatal(line, known, err)
		}
		b.waitIdle()
	}
	if got := b.recordStrings(); len(got) != 0 {
		t.Fatalf("external changes submitted %q", got)
	}
	b.wantShown("A", "b", 0.9, false)
	b.wantShown("B", "a", 0.2, true)

	x, y := faderAt(0, 0.8) // away from the thumb: the press edits
	b.input("A", platform.Input{Kind: platform.PointerDown, Button: platform.ButtonLeft, X: x, Y: y})
	if r := b.nextRecord(); r.State != "applied" || r.Command.ValueText() != "0.80" || r.Command.Channel != "a" {
		t.Fatalf("press edit: %v", r)
	}
	b.waitIdle()
	for _, line := range []string{"gain a 0.1", "mute a off", "reorder"} {
		if _, err := b.m.model.external(line); err != nil {
			t.Fatal(err)
		}
		b.waitIdle()
	}
	b.wantShown("A", "a", 0.8, false) // the gesture owns the displayed value
	b.wantShown("B", "a", 0.1, false) // the other window follows the device
	b.wantRecords(accepted, "#1 accepted gain=0.80")
	// Strip 0 now shows f, but the capture stays on a.
	x, y = faderAt(0, 0.3)
	b.input("A", platform.Input{Kind: platform.PointerMove, X: x, Y: y})
	if r := b.nextRecord(); r.State != "applied" || r.Command.Channel != "a" || r.Command.ValueText() != "0.30" {
		t.Fatalf("drag after reorder: %v", r)
	}
	b.waitIdle()
	if _, err := b.m.model.external("remove a"); err != nil {
		t.Fatal(err)
	}
	b.waitIdle()
	x, y = faderAt(0, 0.9)
	b.input("A", platform.Input{Kind: platform.PointerMove, X: x, Y: y}, platform.Input{Kind: platform.PointerUp, Button: platform.ButtonLeft, X: x, Y: y})
	b.wantRecords(accepted, "#1 accepted gain=0.80", "#2 accepted gain=0.30")
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// The FIFO admits 64 queued commands beyond the one at the device and refuses
// the rest at once, visibly, without waiting or merging; accepted commands
// reach the device in sequence order.
func TestTheCommandQueueIsABoundedFIFOThatRefusesWithoutWaiting(t *testing.T) {
	b := runMixer(t)
	b.input("A", keyDown(platform.KeyTab))
	b.input("A", keyDown(platform.KeyArrowDown)) // #1, 0.49, held at the device
	if cmd := b.heldNext(); cmd.Seq != 1 {
		t.Fatalf("held #%d", cmd.Seq)
	}
	// Each press is a separate edit, so a long burst arrives in one batch and
	// is processed one edit per evaluation. Alternating Up and Down keeps
	// every press an edit: odd sequence numbers are 0.49, even ones 0.50.
	burst := make([]platform.Input, queueLimit+3)
	for i := range burst {
		burst[i] = keyDown([]platform.Key{platform.KeyArrowUp, platform.KeyArrowDown}[i%2])
	}
	value := func(seq int) string { return []string{"0.50", "0.49"}[seq%2] }
	b.input("A", burst...)
	got := b.recordStrings()
	if len(got) != 1+queueLimit+3 {
		t.Fatalf("%d records: %q", len(got), got)
	}
	for i, r := range got[1:] { // #1's acceptance came first
		seq := i + 2
		want := fmt.Sprintf("#%d accepted gain=%s", seq, value(seq))
		if seq > queueLimit+1 {
			want = fmt.Sprintf("#%d rejected gain=%s", seq, value(seq))
		}
		if r != want {
			t.Fatalf("record %d is %q, want %q", i+1, r, want)
		}
	}
	last := queueLimit + 4
	b.wantLabel("A", fmt.Sprintf("Last error: #%d a gain %s refused: queue full", last, value(last)))
	b.wantLabel("B", fmt.Sprintf("Last error: #%d a gain %s refused: queue full", last, value(last)))
	if k := b.m.model.Snapshot().Commands; k.Accepted != queueLimit+1 || k.Refused != 3 || k.Outstanding != queueLimit+1 {
		t.Fatalf("totals %+v", k)
	}
	// A refused edit is not pending, so the views fall back to the latest
	// accepted value, #65's 0.49, not the refused #68's 0.50.
	b.wantShown("A", "a", 0.49, false)
	b.wantShown("B", "a", 0.49, false)
	for seq := uint64(1); seq <= queueLimit+1; seq++ {
		if seq > 1 {
			if cmd := b.heldNext(); cmd.Seq != seq {
				t.Fatalf("device received #%d, want #%d", cmd.Seq, seq)
			}
		}
		select {
		case b.release <- struct{}{}:
		case <-time.After(hang):
			t.Fatal("worker stalled")
		}
	}
	for seq := range queueLimit + 1 {
		if r := b.nextRecord(); r.State != "applied" || r.Seq != uint64(seq+1) {
			t.Fatalf("resolution %v, want #%d applied", r, seq+1)
		}
	}
	b.waitIdle()
	if c := b.channel("a"); fmt.Sprintf("%.2f", c.Gain) != "0.49" || c.GainPending {
		t.Fatalf("device after the burst: %+v", c)
	}
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// Shutdown cancels the held command and everything queued behind it: none of
// them reaches the device, each is recorded as canceled, and a submission
// racing shutdown is refused.
func TestShutdownCancelsOutstandingCommandsWithoutApplyingThem(t *testing.T) {
	b := runMixer(t)
	b.input("A", keyDown(platform.KeyTab))
	for range 4 {
		b.input("A", keyDown(platform.KeyArrowUp))
	}
	b.heldNext() // close both windows with #1 at the device
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
	b.wantRecords(resolved, "#1 canceled gain=0.51", "#2 canceled gain=0.52", "#3 canceled gain=0.53", "#4 canceled gain=0.54")
	if c := b.channel("a"); c.Gain != 0.5 || c.GainPending {
		t.Fatalf("a canceled command reached the device: %+v", c)
	}
	if _, err := b.m.cmds.Submit(Command{Channel: "a", Control: "gain", Gain: 0.9}); !errors.Is(err, errShuttingDown) {
		t.Fatalf("submission after shutdown: %v", err)
	}
	k := b.m.model.Snapshot().Commands
	if k.Accepted != 4 || k.Canceled != 4 || k.Applied != 0 || k.Outstanding != 0 || k.Refused != 1 {
		t.Fatalf("totals %+v", k)
	}
}

// Closing either window leaves the other window, the producer and the worker
// alive; closing the last ends the application.
func TestClosingEitherWindowLeavesTheOtherAndTheApplicationsWorkers(t *testing.T) {
	for _, first := range []string{"A", "B"} {
		t.Run("close "+first, func(t *testing.T) {
			b := runMixer(t)
			other := map[string]string{"A": "B", "B": "A"}[first]
			w := b.window(first)
			w.Close()
			select {
			case <-w.Done():
			case <-time.After(hang):
				t.Fatal("window did not drain")
			}
			b.waitIdle()
			if s := appcore.Inspect(b.app); s.Windows != 1 || s.Tasks < 2 {
				t.Fatalf("after closing %s: %+v", first, s)
			}
			b.input(other, keyDown(platform.KeyTab))
			b.input(other, keyDown(platform.KeyArrowUp))
			b.ack(1)
			b.wantShown(other, "a", 0.51, false)
			if c := b.channel("a"); c.Gain != 0.51 {
				t.Fatalf("worker after closing %s: %+v", first, c)
			}
			if err := b.stop(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// fakeSubscriber counts invalidations and refuses once closed.
type fakeSubscriber struct {
	mu     sync.Mutex
	closed bool
	calls  int
}

func (f *fakeSubscriber) Invalidate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.closed {
		return dfx2.ErrClosed
	}
	return nil
}

// A real window closes after a publication collected it and before it is
// invalidated. Its refusal is counted, the other window still renders, and
// unsubscribing from inside that gap cannot deadlock against the publication.
func TestAPublicationToleratesASubscriberClosingAfterCollection(t *testing.T) {
	b := runMixer(t)
	var victim named
	b.onUIThread(func() { victim = b.m.windows["B"] })
	before, _ := appcore.InspectContent(b.window("A"))
	var once sync.Once
	b.m.model.mu.Lock()
	b.m.model.collected = func() {
		once.Do(func() {
			victim.ui.Close()
			<-victim.ui.Done()
			b.m.model.unsubscribe(victim.ui) // the program's watcher does this too; it must not block here
		})
	}
	b.m.model.mu.Unlock()
	b.guardedPublish()
	b.waitIdle()
	if n := b.m.model.refusedInvalidations(); n != 1 {
		t.Fatalf("%d refused invalidations, want 1", n)
	}
	after, _ := appcore.InspectContent(b.window("A"))
	if after.Frames <= before.Frames {
		t.Fatal("the remaining window did not render the publication")
	}
	// The same race with a subscriber that is mid-close: it is invalidated
	// once, refuses, and is not counted against live windows.
	sub := &fakeSubscriber{}
	b.m.model.subscribe(sub)
	b.m.model.mu.Lock()
	b.m.model.collected = func() {
		sub.mu.Lock()
		sub.closed = true
		sub.mu.Unlock()
		b.m.model.unsubscribe(sub)
	}
	b.m.model.mu.Unlock()
	b.guardedPublish()
	b.waitIdle()
	b.publish()
	if sub.calls != 1 || b.m.model.refusedInvalidations() != 2 {
		t.Fatalf("calls %d refused %d", sub.calls, b.m.model.refusedInvalidations())
	}
	b.m.model.mu.Lock()
	b.m.model.collected = nil
	b.m.model.mu.Unlock()
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// With small budgets and changing text, both caches evict between frames and
// stay within their budgets; a window that has evicted renders the same image
// as a fresh window with empty caches. The budgets sit above the view's steady
// working set: an entry the last frame used is never evicted, so a budget
// below it only serves the excess transiently, every frame, with no eviction.
func TestTextChurnEvictsWithinBudgetAndRendersLikeAFreshWindow(t *testing.T) {
	budget := text.Budget{GlyphBytes: 24 << 10, ParagraphBytes: 48 << 10, ParagraphEntries: 40, Transient: text.DefaultBudget.Transient}
	saved := text.DefaultBudget
	text.DefaultBudget = budget // copied by each window as it opens
	t.Cleanup(func() { text.DefaultBudget = saved })
	b := runMixer(t)
	b.m.model.setChurn(30)
	b.waitIdle()
	stats := func(name string) text.Stats {
		var s text.Stats
		b.onUIThread(func() {
			content, err := appcore.AuditionContent(b.m.windows[name].core)
			if err != nil {
				t.Fatal(err)
			}
			s = content.(interface{ TextStats() text.Stats }).TextStats()
		})
		return s
	}
	for range 20 {
		b.publish() // a new version, so new churn words
		for _, name := range []string{"A", "B"} {
			s := stats(name)
			if s.GlyphBytes > budget.GlyphBytes || s.ParagraphBytes > budget.ParagraphBytes || s.ParagraphEntries > budget.ParagraphEntries {
				t.Fatalf("window %s over budget between frames: %+v", name, s)
			}
		}
	}
	s := stats("A")
	if s.GlyphEvictions == 0 || s.ParaEvictions == 0 {
		t.Fatalf("no eviction: %+v", s)
	}
	b.onUIThread(func() {
		if _, err := b.m.open(); err != nil {
			t.Fatal(err)
		}
	})
	b.surface(b.window("C"))
	b.waitIdle()
	b.publish() // A and C render the same snapshot
	a, _ := b.fake.Window(appcore.WindowID(b.window("A")))
	c, _ := b.fake.Window(appcore.WindowID(b.window("C")))
	if c.Presents == 0 || !bytes.Equal(a.Last.Pix, c.Last.Pix) {
		t.Fatalf("an evicting window renders differently from a fresh one (fresh presents %d)", c.Presents)
	}
	if err := b.stop(); err != nil {
		t.Fatal(err)
	}
}

// The program's device delay holds a command for the configured time, and a
// new setting applies to the command already held.
func TestTheDeviceDelayAppliesToTheHeldCommand(t *testing.T) {
	m := newModel(false)
	q := newCommands(m, func(Record) {})
	waiting := make(chan struct{}, 1)
	q.waiting = func() { waiting <- struct{}{} }
	await := func(what string) {
		select {
		case <-waiting:
		case <-time.After(hang):
			t.Fatal(what)
		}
	}
	m.setDelay(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.delay(ctx, Command{}) }()
	await("the delay never waited")
	m.setDelay(0) // the held command is released at once
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(hang):
		t.Fatal("a zero delay did not release the held command")
	}
	m.setDelay(time.Hour)
	go func() { done <- q.delay(ctx, Command{}) }()
	await("the delay never waited")
	m.setDelay(2 * time.Hour) // still held, waiting again on the new setting
	await("a longer delay did not keep holding")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(hang):
		t.Fatal("cancellation did not release the held command")
	}
}

func TestTextBudgetFlag(t *testing.T) {
	for v, want := range map[string]text.Budget{
		"default":         text.DefaultBudget,
		"small":           smallTextBudget,
		"524288:65536:48": {GlyphBytes: 524288, ParagraphBytes: 65536, ParagraphEntries: 48, Transient: text.DefaultBudget.Transient},
	} {
		if got, err := parseBudget(v); err != nil || got != want {
			t.Errorf("%s: %+v %v", v, got, err)
		}
	}
	for _, bad := range []string{"", "tiny", "1:2", "1:2:3:4", "0:1:1", "1:-1:1", "1:1:1x", "1.5:1:1"} {
		if _, err := parseBudget(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
