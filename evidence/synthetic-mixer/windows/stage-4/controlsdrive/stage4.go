//go:build windows

package main

// Stage 4 cases: the ordered command worker and its device fixtures, timing
// observations, closing either window, shutdown with outstanding commands, and
// the separate text-cache auditions. Each mirrors the case of the same name in
// evidence/synthetic-mixer/linux/controls_drive.py.

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (d *driver) fixture(line string) error {
	d.command(line)
	_, err := d.expect("fixture", 15*time.Second, nil)
	return err
}

func (d *driver) commands() []event {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []event
	for _, e := range d.all {
		if e.name() == "command" {
			out = append(out, e)
		}
	}
	return out
}

func (d *driver) commandsSince(mark int) []event { return d.commands()[mark:] }

func (d *driver) note(k string, v any) {
	if d.detail == nil {
		d.detail = map[string]any{}
	}
	d.detail[k] = v
}

func field(m any, k string) any {
	if mm, ok := m.(map[string]any); ok {
		return mm[k]
	}
	return nil
}

func num(m any, k string) float64 { f, _ := field(m, k).(float64); return f }
func truth(m any, k string) bool  { b, _ := field(m, k).(bool); return b }

// channel returns a channel's published state from a status record.
func channel(s event, id string) map[string]any {
	list, _ := s["channels"].([]any)
	for _, c := range list {
		if field(c, "id") == id {
			return c.(map[string]any)
		}
	}
	return nil
}

// shown is the value a strip's fader shows: its pending edit, or the device's.
func shown(s event, strip int) float64 {
	list, _ := s["channels"].([]any)
	if strip >= len(list) {
		return 0.5
	}
	if truth(list[strip], "gain_pending") {
		return num(list[strip], "pending_gain")
	}
	return num(list[strip], "gain")
}

func states(records []event, state string) []event {
	var out []event
	for _, r := range records {
		if r.text("state") == state {
			out = append(out, r)
		}
	}
	return out
}

func (d *driver) focusFader(hwnd uintptr, strip int) error {
	s, err := d.status()
	if err != nil {
		return err
	}
	if err := click(fader(hwnd, strip, shown(s, strip))); err != nil {
		return err
	}
	return d.settle()
}

func shiftTab() error {
	if err := key(vkShift, false); err != nil {
		return err
	}
	if err := tap(vkTab); err != nil {
		return err
	}
	return key(vkShift, true)
}

func space() error {
	if err := key(vkSpace, false); err != nil {
		return err
	}
	return key(vkSpace, true)
}

func (d *driver) snap(name, window string, commands bool) error {
	if err := d.settleWith(commands, 15*time.Second); err != nil {
		return err
	}
	d.command(strings.TrimSpace("snapshot " + name + " " + window))
	_, err := d.expect("snapshot", 15*time.Second, nil)
	return err
}

func (d *driver) delayedCommands(a, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	for _, line := range []string{"gain d 0.5", "mute d off"} {
		if err := d.external(line); err != nil {
			return err
		}
	}
	if err := d.focusFader(a, 3); err != nil {
		return err
	}
	if err := d.fixture("delay 700ms"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	cmark := len(d.commands())
	if err := tap(vkUp, vkUp, vkUp); err != nil {
		return err
	}
	if err := d.settleWith(false, 10*time.Second); err != nil {
		return err
	}
	var values []float64
	for _, e := range d.since(mark) {
		values = append(values, e.number("value"))
	}
	if err := require(slices.Equal(values, []float64{0.51, 0.52, 0.53}), "delayed fader keys: %v", values); err != nil {
		return err
	}
	pending, err := d.status()
	if err != nil {
		return err
	}
	ch := channel(pending, "d")
	if err := require(pending.number("commands_outstanding") >= 2 && truth(ch, "gain_pending") && num(ch, "pending_gain") == 0.53 && num(ch, "gain") < 0.53,
		"pending value not published or commands not held: %v %v", pending["commands"], ch); err != nil {
		return err
	}
	// Responsive while the device lags: a toggle edits at once, and an
	// external change on another channel renders.
	if err := tap(vkTab); err != nil {
		return err
	}
	if err := space(); err != nil {
		return err
	}
	frames := pending.number("frames")
	if err := d.external("gain e 0.7"); err != nil {
		return err
	}
	if err := d.settleWith(false, 10*time.Second); err != nil {
		return err
	}
	during, err := d.status()
	if err != nil {
		return err
	}
	edits := d.since(mark)
	last := edits[len(edits)-1]
	if err := require(last.text("control") == "mute" && last["value"] == true && during.number("commands_outstanding") >= 1,
		"a toggle waited for delayed commands: %v outstanding %v", last, during.number("commands_outstanding")); err != nil {
		return err
	}
	if err := require(during.number("frames") > frames, "an external change did not render while commands waited"); err != nil {
		return err
	}
	if err := d.snap("delayed-pending", "A", false); err != nil {
		return err
	}
	if err := d.screen("A", "screen-delayed-pending"); err != nil {
		return err
	}
	if err := d.settleWith(true, 15*time.Second); err != nil {
		return err
	}
	applied := states(d.commandsSince(cmark), "applied")
	var got []any
	var latencies []float64
	for i, r := range applied {
		got = append(got, r["value"])
		latencies = append(latencies, r.number("latency_ms"))
		if i > 0 && r.number("sequence") <= applied[i-1].number("sequence") {
			return fmt.Errorf("delayed commands out of order: %v", applied)
		}
	}
	if err := require(fmt.Sprint(got) == "[0.51 0.52 0.53 true]", "applied %v", got); err != nil {
		return err
	}
	// Each waited at least the delay at the device; one at a time, so the three
	// gain commands accepted together resolve about 700 ms apart.
	for _, l := range latencies {
		if l < 700 {
			return fmt.Errorf("a command skipped the device delay: %v", latencies)
		}
	}
	if err := require(latencies[2] >= 2000, "commands were not serialized at the device: %v", latencies); err != nil {
		return err
	}
	after, err := d.status()
	if err != nil {
		return err
	}
	ch = channel(after, "d")
	if err := require(num(ch, "gain") == 0.53 && truth(ch, "muted") && !truth(ch, "gain_pending") && !truth(ch, "mute_pending"), "device state %v", ch); err != nil {
		return err
	}
	d.note("latencies_ms", latencies)
	return d.fixture("delay 0s")
}

func (d *driver) rejectedCommands(_, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := shiftTab(); err != nil { // back onto d's fader from its mute
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	for _, line := range []string{"delay 400ms", "reject 1"} {
		if err := d.fixture(line); err != nil {
			return err
		}
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	cmark := len(d.commands())
	if err := tap(vkUp, vkUp); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	var resolved []string
	for _, r := range d.commandsSince(cmark) {
		if r.text("state") != "accepted" {
			resolved = append(resolved, fmt.Sprintf("%s %v", r.text("state"), r["value"]))
		}
	}
	if err := require(fmt.Sprint(resolved) == "[rejected 0.54 applied 0.55]", "earlier rejection: %v", resolved); err != nil {
		return err
	}
	if err := d.fixture("reject 1"); err != nil {
		return err
	}
	if err := tap(vkUp); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	s, err := d.status()
	if err != nil {
		return err
	}
	ch := channel(s, "d")
	if err := require(num(ch, "gain") == 0.55 && !truth(ch, "gain_pending"), "rejection did not reconcile to the device: %v", ch); err != nil {
		return err
	}
	if msg := s.text("last_error"); !strings.Contains(msg, "rejected: device refused") || !strings.Contains(msg, "0.56") {
		return fmt.Errorf("error not shown: %q", msg)
	}
	if err := d.snap("rejected-error", "A", true); err != nil {
		return err
	}
	if err := d.screen("A", "screen-rejected-error"); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond) // a quiet stretch in which reconciliation must not submit
	if err := d.settle(); err != nil {
		return err
	}
	edits, accepted := d.since(mark), states(d.commandsSince(cmark), "accepted")
	if err := require(len(edits) == 3 && len(accepted) == 3, "reconciliation submitted commands: %d edits, %d accepted", len(edits), len(accepted)); err != nil {
		return err
	}
	d.note("last_error", s.text("last_error"))
	return d.fixture("delay 0s")
}

func (d *driver) overloadedCommands(_, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.fixture("delay 60s"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	cmark := len(d.commands())
	for i := range 70 {
		vk := uint16(vkUp)
		if i%2 == 1 {
			vk = vkDown
		}
		if err := tap(vk); err != nil {
			return err
		}
	}
	if err := d.settleWith(false, 15*time.Second); err != nil {
		return err
	}
	edits, records := d.since(mark), d.commandsSince(cmark)
	accepted := states(records, "accepted")
	var refused []event
	for _, r := range states(records, "rejected") {
		if r.text("reason") == "queue full" {
			refused = append(refused, r)
		}
	}
	refusedEdits := 0
	for _, e := range edits {
		if e.text("refused") != "" {
			refusedEdits++
		}
	}
	// 64 queue behind the command the device holds; if the worker had not yet
	// taken the first, 64 in all. Either way nothing is merged or lost.
	if err := require(len(edits) == 70 && len(accepted)+len(refused) == 70 && (len(accepted) == 64 || len(accepted) == 65) && refusedEdits == len(refused),
		"overload: %d edits, %d accepted, %d refused", len(edits), len(accepted), len(refused)); err != nil {
		return err
	}
	for i, r := range refused {
		if r.number("sequence") != edits[len(edits)-len(refused)+i].number("sequence") {
			return errors.New("refusals were not the newest edits")
		}
	}
	s, err := d.status()
	if err != nil {
		return err
	}
	if err := require(s.number("commands_outstanding") == float64(len(accepted)) && strings.Contains(s.text("last_error"), "queue full"), "overload not shown: %v %q", s["commands"], s.text("last_error")); err != nil {
		return err
	}
	if err := d.snap("overload-error", "A", false); err != nil {
		return err
	}
	if err := d.screen("A", "screen-overload-error"); err != nil {
		return err
	}
	if err := d.fixture("delay 0s"); err != nil {
		return err
	}
	if err := d.settleWith(true, 20*time.Second); err != nil {
		return err
	}
	applied := states(d.commandsSince(cmark), "applied")
	if len(applied) != len(accepted) {
		return fmt.Errorf("%d applied of %d accepted", len(applied), len(accepted))
	}
	for i := range applied {
		if applied[i].number("sequence") != accepted[i].number("sequence") {
			return fmt.Errorf("applied out of order at %d", i)
		}
	}
	after, err := d.status()
	if err != nil {
		return err
	}
	ch := channel(after, "d")
	if err := require(num(ch, "gain") == accepted[len(accepted)-1].number("value") && !truth(ch, "gain_pending"), "device after overload: %v", ch); err != nil {
		return err
	}
	d.note("accepted", len(accepted))
	d.note("refused", len(refused))
	return nil
}

func (d *driver) externalWhilePending(_, _ uintptr) error {
	if err := d.activate("A"); err != nil {
		return err
	}
	if err := d.fixture("delay 800ms"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	cmark := len(d.commands())
	if err := tap(vkUp); err != nil {
		return err
	}
	if err := d.settleWith(false, 10*time.Second); err != nil {
		return err
	}
	edit := d.since(mark)
	if len(edit) != 1 {
		return fmt.Errorf("the key did not edit once: %v", edit)
	}
	if err := d.external("gain d 0.2"); err != nil {
		return err
	}
	if err := d.settleWith(false, 10*time.Second); err != nil {
		return err
	}
	s, err := d.status()
	if err != nil {
		return err
	}
	ch := channel(s, "d")
	if err := require(num(ch, "gain") == 0.2 && truth(ch, "gain_pending") && num(ch, "pending_gain") == edit[0].number("value"), "pending under an external change: %v", ch); err != nil {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if s, err = d.status(); err != nil {
		return err
	}
	ch = channel(s, "d")
	accepted := states(d.commandsSince(cmark), "accepted")
	if err := require(num(ch, "gain") == edit[0].number("value") && len(d.since(mark)) == 1 && len(accepted) == 1, "external change echoed or lost: %v", ch); err != nil {
		return err
	}
	return d.fixture("delay 0s")
}

// timing records observations, not promises: frame rate, view evaluations and
// process CPU with the feed running, with text churn, and once settled.
func (d *driver) timing(_, _ uintptr) error {
	measure := func(seconds float64) (map[string]any, event, error) {
		before, err := d.status()
		if err != nil {
			return nil, nil, err
		}
		t0 := time.Now()
		time.Sleep(time.Duration(seconds * float64(time.Second)))
		after, err := d.status()
		if err != nil {
			return nil, nil, err
		}
		dt := time.Since(t0).Seconds()
		rate := func(k string) float64 { return float64(int((after.number(k)-before.number(k))/dt*10)) / 10 }
		return map[string]any{"seconds": dt, "frames_per_s": rate("frames"), "evaluations_per_s": rate("evaluations"),
			"presents_per_s": rate("presents"), "cpu_percent": float64(int(1000*(after.number("cpu_seconds")-before.number("cpu_seconds"))/dt)) / 10,
			"ticks": after.number("tick") - before.number("tick")}, after, nil
	}
	d.command("resume")
	if _, err := d.expect("feed", 15*time.Second, func(e event) bool { return e["running"] == true }); err != nil {
		return err
	}
	time.Sleep(time.Second)
	running, _, err := measure(5)
	if err != nil {
		return err
	}
	if err := d.fixture("churn 36"); err != nil {
		return err
	}
	time.Sleep(time.Second)
	churn, status, err := measure(5)
	if err != nil {
		return err
	}
	if err := d.snap("timing-churn", "A", false); err != nil {
		return err
	}
	if err := d.screen("A", "screen-timing-churn"); err != nil {
		return err
	}
	if err := d.fixture("churn off"); err != nil {
		return err
	}
	d.command("pause")
	if _, err := d.expect("feed", 15*time.Second, func(e event) bool { return e["running"] == false }); err != nil {
		return err
	}
	if running["ticks"].(float64) < 120 {
		return fmt.Errorf("the 30 Hz producer did not run: %v", running)
	}
	// Settled: no animation deadline for 250 ms, then three quiet seconds.
	deadline, since := time.Now().Add(10*time.Second), time.Time{}
	for {
		if time.Now().After(deadline) {
			return errors.New("meters never settled")
		}
		s, err := d.status()
		if err != nil {
			return err
		}
		switch {
		case s.number("animating") != 0:
			since = time.Time{}
		case since.IsZero():
			since = time.Now()
		case time.Since(since) >= 250*time.Millisecond:
			goto settled
		}
		time.Sleep(20 * time.Millisecond)
	}
settled:
	quiet, last, err := measure(3)
	if err != nil {
		return err
	}
	if quiet["frames_per_s"] != 0.0 || quiet["evaluations_per_s"] != 0.0 || quiet["presents_per_s"] != 0.0 {
		return fmt.Errorf("settled mixer did not go idle: %v", quiet)
	}
	time.Sleep(time.Second)
	later, err := d.status()
	if err != nil {
		return err
	}
	if later.number("frame_timers") != last.number("frame_timers") {
		return errors.New("a deadline timer ran while settled")
	}
	d.note("running", running)
	d.note("churn", churn)
	d.note("quiet", quiet)
	d.note("text", status["text"])
	return nil
}

// closeFirstWindow opens C, then closes A, the first window: B and C keep
// working, and so do the application's producer and command worker.
func (d *driver) closeFirstWindow(_, b uintptr) error {
	d.command("open")
	if _, err := d.expect("opened", 15*time.Second, func(e event) bool { return e.text("window") == "C" }); err != nil {
		return err
	}
	c, err := d.find("C")
	if err != nil {
		return err
	}
	d.command("close A")
	if _, err := d.expect("closed", 15*time.Second, func(e event) bool { return e.text("window") == "A" }); err != nil {
		return err
	}
	pSetWindowPos.Call(c, 0, 40, 60, 0, 0, swpNoSize|swpNoZOrder)
	time.Sleep(300 * time.Millisecond)
	s, err := d.status()
	if err != nil {
		return err
	}
	if err := require(s.number("windows") == 2 && s.number("tasks") >= 2, "closing A changed the application: %v windows %v tasks", s.number("windows"), s.number("tasks")); err != nil {
		return err
	}
	for _, w := range []struct {
		name  string
		hwnd  uintptr
		strip int
		value float64
	}{{"B", b, 1, 0.7}, {"C", c, 2, 0.3}} {
		if err := d.activate(w.name); err != nil {
			return err
		}
		mark, err := d.mark()
		if err != nil {
			return err
		}
		cmark := len(d.commands())
		if err := click(fader(w.hwnd, w.strip, w.value)); err != nil {
			return err
		}
		if err := d.settle(); err != nil {
			return err
		}
		if len(d.since(mark)) == 0 || len(states(d.commandsSince(cmark), "applied")) == 0 {
			return fmt.Errorf("window %s stopped working after A closed", w.name)
		}
	}
	return nil
}

// shutdownOutstanding quits with five commands waiting on the device: each is
// canceled, none is reported applied, and Run returns cleanly.
func (d *driver) shutdownOutstanding(_, _ uintptr) error {
	c, err := d.find("C")
	if err != nil {
		return err
	}
	if err := d.activate("C"); err != nil {
		return err
	}
	if err := d.focusFader(c, 4); err != nil {
		return err
	}
	if err := d.fixture("delay 60s"); err != nil {
		return err
	}
	mark, err := d.mark()
	if err != nil {
		return err
	}
	cmark := len(d.commands())
	if err := tap(vkUp, vkUp, vkUp, vkUp, vkUp); err != nil {
		return err
	}
	if err := d.settleWith(false, 10*time.Second); err != nil {
		return err
	}
	s, err := d.status()
	if err != nil {
		return err
	}
	if err := require(len(d.since(mark)) == 5 && s.number("commands_outstanding") == 5, "commands not outstanding: %v", s["commands"]); err != nil {
		return err
	}
	d.command("quit")
	if _, err := d.expect("drained", 30*time.Second, nil); err != nil {
		return err
	}
	records := d.commandsSince(cmark)
	var accepted, canceled []float64
	for _, r := range states(records, "accepted") {
		accepted = append(accepted, r.number("sequence"))
	}
	for _, r := range states(records, "canceled") {
		canceled = append(canceled, r.number("sequence"))
	}
	slices.Sort(canceled)
	if err := require(len(accepted) == 5 && slices.Equal(accepted, canceled) && len(states(records, "applied")) == 0, "shutdown: accepted %v canceled %v", accepted, canceled); err != nil {
		return err
	}
	d.note("canceled", canceled)
	return nil
}

func summaryList(v any) []any { l, _ := v.([]any); return l }

// textAudition runs a separate mixer with a churn label: changing words and
// numbers force eviction; counters must stay within the budget between frames,
// and a window that has evicted must render exactly what a fresh window
// renders. The small glyph budget scales with the square of the display scale,
// as glyph pixels do, and has to sit above the view's steady working set and
// below every glyph the churn alphabet can produce, or nothing is evictable.
func textAudition(fixture, root, budget string) map[string]any {
	result := map[string]any{"budget": budget, "passed": false}
	out := filepath.Join(root, "text-"+budget)
	if err := os.MkdirAll(out, 0o755); err != nil {
		result["failure"] = err.Error()
		return result
	}
	// Windows open on the primary monitor; its DPI is the scale they render at.
	scale, flagValue := 1.0, budget
	if dpi, _, _ := pGetDpiForSystem.Call(); dpi != 0 {
		scale = float64(dpi) / 96
	}
	if budget == "small" {
		flagValue = fmt.Sprintf("%d:%d:48", int(48*1024*scale*scale), 64*1024)
	}
	var d *driver
	fail := func(err error) map[string]any {
		result["failure"] = err.Error()
		if d != nil && d.cmd.ProcessState == nil {
			d.cmd.Process.Kill()
		}
		return result
	}
	result["flag"], result["scale"] = flagValue, scale
	d, err := start(fixture, out, "-text-budget", flagValue, "-churn", "36")
	if err != nil {
		return fail(err)
	}
	if _, err := d.expect("opened", 60*time.Second, func(e event) bool { return e.text("window") == "B" }); err != nil {
		return fail(err)
	}
	a, err := d.find("A")
	if err != nil {
		return fail(err)
	}
	b, err := d.find("B")
	if err != nil {
		return fail(err)
	}
	pSetWindowPos.Call(a, 0, 40, 60, 0, 0, swpNoSize|swpNoZOrder)
	r := windowRect(a)
	pSetWindowPos.Call(b, 0, uintptr(r.right+40), 60, 0, 0, swpNoSize|swpNoZOrder)
	d.command("resume")
	if _, err := d.expect("feed", 15*time.Second, func(e event) bool { return e["running"] == true }); err != nil {
		return fail(err)
	}
	var samples []event
	for range 8 {
		time.Sleep(500 * time.Millisecond)
		s, err := d.status()
		if err != nil {
			return fail(err)
		}
		samples = append(samples, s)
	}
	d.command("pause")
	if _, err := d.expect("feed", 15*time.Second, func(e event) bool { return e["running"] == false }); err != nil {
		return fail(err)
	}
	time.Sleep(2500 * time.Millisecond) // meters fall at 0.5 per second; the audition compares settled images
	if err := d.screen("A", "screen-text-"+budget+"-A"); err != nil {
		return fail(err)
	}
	d.command("open")
	if _, err := d.expect("opened", 15*time.Second, func(e event) bool { return e.text("window") == "C" }); err != nil {
		return fail(err)
	}
	time.Sleep(1500 * time.Millisecond) // C renders the snapshot A last rendered: no publication since the pause
	final, err := d.status()
	if err != nil {
		return fail(err)
	}
	for _, w := range []string{"A", "C"} {
		d.command("snapshot text-" + budget + "-" + w + " " + w)
		if _, err := d.expect("snapshot", 15*time.Second, nil); err != nil {
			return fail(err)
		}
	}
	limit := final["text_budget"]
	var over []string
	for i, s := range append(samples, final) {
		windows, _ := s["text"].(map[string]any)
		for w, st := range windows {
			if num(st, "glyph_bytes") > num(limit, "glyph_bytes") || num(st, "paragraph_bytes") > num(limit, "paragraph_bytes") || num(st, "paragraph_entries") > num(limit, "paragraph_entries") {
				over = append(over, fmt.Sprintf("sample %d window %s: %v", i, w, st))
			}
		}
	}
	same, size, err := samePNG(filepath.Join(out, "text-"+budget+"-A.png"), filepath.Join(out, "text-"+budget+"-C.png"))
	if err != nil {
		return fail(err)
	}
	evictions := field(final["text"], "A")
	result["final"], result["budget_limits"], result["over_budget"] = final["text"], limit, over
	result["a_matches_fresh_c"], result["size"] = same, size
	result["glyph_evictions"], result["paragraph_evictions"] = num(evictions, "glyph_evictions"), num(evictions, "paragraph_evictions")
	d.command("quit")
	if _, err := d.expect("drained", 30*time.Second, nil); err != nil {
		return fail(err)
	}
	waitErr := d.cmd.Wait()
	var returned string
	d.mu.Lock()
	for _, e := range d.all {
		if e.name() == "return" {
			returned = e.text("error")
		}
	}
	d.mu.Unlock()
	result["exit_error"], result["returned"] = fmt.Sprint(waitErr), returned
	evicted := num(evictions, "paragraph_evictions") > 0 && (budget != "small" || num(evictions, "glyph_evictions") > 0)
	result["passed"] = waitErr == nil && returned == "<nil>" && len(over) == 0 && same && evicted
	return result
}

// samePNG compares two retained-image snapshots pixel for pixel.
func samePNG(x, y string) (bool, [2]int, error) {
	read := func(path string) (image.Image, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return png.Decode(f)
	}
	a, err := read(x)
	if err != nil {
		return false, [2]int{}, err
	}
	c, err := read(y)
	if err != nil {
		return false, [2]int{}, err
	}
	size := [2]int{a.Bounds().Dx(), a.Bounds().Dy()}
	if a.Bounds() != c.Bounds() {
		return false, size, nil
	}
	for yy := a.Bounds().Min.Y; yy < a.Bounds().Max.Y; yy++ {
		for xx := a.Bounds().Min.X; xx < a.Bounds().Max.X; xx++ {
			r1, g1, b1, a1 := a.At(xx, yy).RGBA()
			r2, g2, b2, a2 := c.At(xx, yy).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				return false, size, nil
			}
		}
	}
	return true, size, nil
}
