package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/ui"
)

// Strip geometry. A strip's fader and checkbox come first, so their logical
// positions do not depend on font metrics: the input auditions press them at
// computed coordinates (STRIP_STEP and friends in the drivers).
const (
	stripWidth = 110
	stripGap   = 12
)

// trace observes what a view rendered, for tests. Nil in the program.
type trace struct {
	begin func(window string)                                      // a view evaluation starts
	shown func(window string, c Channel, gain float64, muted bool) // values the widgets returned
	label func(window, text string)
}

// controlsView is one window's view of the shared model: six strips, then the
// command status. Its only side effect is submitting user edits.
func (m *mixer) controlsView(window string) func(*ui.Frame) {
	return func(f *ui.Frame) {
		m.evaluations.Add(1)
		if m.trace.begin != nil {
			m.trace.begin(window)
		}
		s := m.model.Snapshot()
		label := func(text string) {
			f.Label(text)
			if m.trace.label != nil {
				m.trace.label(window, text)
			}
		}
		f.Row(ui.Layout{Gap: stripGap}, func(f *ui.Frame) {
			for _, c := range s.Channels {
				f.Scope(c.ID, func(f *ui.Frame) {
					f.Column(ui.Layout{Width: stripWidth, Gap: 8}, func(f *ui.Frame) {
						gain, changed := f.Fader("gain", c.ShownGain())
						if changed {
							m.submit(Command{Window: window, Channel: c.ID, Control: "gain", Gain: gain})
						}
						muted, changed := f.Checkbox("mute", "Mute", c.ShownMute())
						if changed {
							m.submit(Command{Window: window, Channel: c.ID, Control: "mute", Mute: muted})
						}
						if m.trace.shown != nil {
							m.trace.shown(window, c, gain, muted)
						}
						f.Meter("level", c.Level)
						label(c.Name)
						label(fmt.Sprintf("%5.1f dB", c.DB))
						label(deviceText(c))
						label(pendingText(c))
					})
				})
			}
		})
		for _, line := range statusLines(s) {
			label(line)
		}
		for _, line := range churnLines(s.Version, s.Churn) {
			label(line)
		}
	}
}

// deviceText is the device's own state, which a pending edit may differ from.
func deviceText(c Channel) string {
	state := "live"
	if c.Muted {
		state = "muted"
	}
	return fmt.Sprintf("%.2f %s", c.Gain, state)
}

func pendingText(c Channel) string {
	var parts []string
	if c.GainPending {
		parts = append(parts, fmt.Sprintf("%.2f #%d", c.PendingGain, c.GainSeq))
	}
	if c.MutePending {
		mute := "unmute"
		if c.PendingMute {
			mute = "mute"
		}
		parts = append(parts, fmt.Sprintf("%s #%d", mute, c.MuteSeq))
	}
	if len(parts) == 0 {
		return "in sync"
	}
	return "pending " + strings.Join(parts, ", ")
}

// statusLines are the visible command totals, fixture settings and the latest
// command error.
func statusLines(s *Snapshot) []string {
	feed := "running"
	if !s.Running {
		feed = "paused"
	}
	k := s.Commands
	totals := fmt.Sprintf("Commands: %d outstanding · %d accepted · %d applied · %d rejected by device · %d refused · %d canceled",
		k.Outstanding, k.Accepted, k.Applied, k.Rejected, k.Refused, k.Canceled)
	fixture := fmt.Sprintf("Device delay %v · reject next %d · feed %s · tick %d", k.Delay, k.RejectNext, feed, s.Tick)
	last := "No command errors."
	if s.Error != "" {
		last = "Last error: " + s.Error
	}
	return []string{totals, fixture, last}
}

// churnAlphabet mixes Latin, Greek and Cyrillic letters, which the tested
// hosts' default faces cover, so changing words keep rasterizing new glyphs.
var churnAlphabet = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZαβγδεζηθικλμνξοπρστυφχψωΑΒΓΔΘΛΞΠΣΦΨΩабвгдежзийклмнопрстуфхцчшщыэюяБГДЖЗИЛПФЦЧШЭЮЯ")

// churnLines are n deterministic words, twelve to a line, that differ for
// every publication version. Each line is its own small paragraph, so the
// caches admit them and later frames leave them unused, which is what makes
// eviction possible: an entry the current frame uses is never evicted.
func churnLines(version uint64, n int) []string {
	var lines []string
	var b strings.Builder
	for i := range n {
		x := version*0x9e3779b97f4a7c15 + uint64(i)*0xbf58476d1ce4e5b9
		x ^= x >> 29
		x *= 0x94d049bb133111eb
		x ^= x >> 32
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		for range 3 + x%5 {
			b.WriteRune(churnAlphabet[x%uint64(len(churnAlphabet))])
			x /= uint64(len(churnAlphabet))
			x += 0x5851f42d
		}
		if i%12 == 11 || i == n-1 {
			lines = append(lines, b.String())
			b.Reset()
		}
	}
	return lines
}

// submit hands one user edit to the command queue and logs it. The other
// window learns of it through the published pending value.
func (m *mixer) submit(cmd Command) {
	seq, err := m.cmds.Submit(cmd)
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	m.o.event("edit", "window", cmd.Window, "channel", cmd.Channel, "control", cmd.Control, "value", cmd.Value(), "sequence", seq, "refused", reason)
}

// snapshotWindow writes a window's retained image, the first open window's
// when window is empty, into the -snapshots directory, for the auditions'
// visual record and image comparisons.
func (m *mixer) snapshotWindow(name, window string) {
	m.post(func() {
		var core *appcore.Window
		if window == "" {
			if ws := appcore.ContentWindows(m.a); len(ws) > 0 {
				core = ws[0]
			}
		} else if w, ok := m.windows[window]; ok {
			core = w.core
		}
		if core == nil || m.c.snapshots == "" {
			m.o.event("snapshot-error", "error", "no such window or no -snapshots directory")
			return
		}
		path := filepath.Join(m.c.snapshots, filepath.Base(name)+".png")
		if err := appcore.AuditionSnapshot(core, path); err != nil {
			m.o.event("snapshot-error", "error", err.Error())
			return
		}
		m.o.event("snapshot", "path", path, "window", window)
	})
}

// inputQueued totals queued input across a's content windows.
func inputQueued(a *dfx2.App) int {
	n := 0
	for _, w := range appcore.ContentWindows(a) {
		if s, ok := appcore.InspectContent(w); ok {
			n += s.InputQueued
		}
	}
	return n
}
