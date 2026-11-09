package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/ui"
)

// desk is the application-owned model behind -controls: channels with stable
// IDs, whose order and membership the console can change. It is a small
// stand-in for stage 4's model and device worker: an edit applies at once and
// is logged, and external changes are never reported as edits.
type desk struct {
	mu       sync.Mutex
	channels []*channel
	removed  []*channel
	edits    uint64
}

type channel struct {
	id, name string
	gain     float64
	muted    bool
}

func newDesk() *desk {
	return &desk{channels: []*channel{
		{id: "a", name: "Channel A", gain: 0.5},
		{id: "b", name: "Channel B", gain: 0.5},
		{id: "c", name: "Channel C", gain: 0.5},
	}}
}

func (d *desk) snapshot() []channel {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]channel, len(d.channels))
	for i, c := range d.channels {
		out[i] = *c
	}
	return out
}

func (d *desk) find(id string) *channel {
	for _, c := range d.channels {
		if c.id == id {
			return c
		}
	}
	return nil
}

// controlsView is one window's view of the shared model. A strip's fader and
// checkbox come first, so their logical positions do not depend on font metrics:
// the input auditions press them at computed coordinates.
func (m *mixer) controlsView(window string) func(*ui.Frame) {
	return func(f *ui.Frame) {
		m.evaluations.Add(1)
		level := m.src.snapshot().level
		f.Row(ui.Layout{Gap: 12}, func(f *ui.Frame) {
			for _, c := range m.desk.snapshot() {
				f.Scope(c.id, func(f *ui.Frame) {
					f.Column(ui.Layout{Width: 130, Gap: 8}, func(f *ui.Frame) {
						if gain, changed := f.Fader("gain", c.gain); changed {
							m.edit(window, c.id, "gain", gain, func(c *channel) { c.gain = gain })
						}
						if muted, changed := f.Checkbox("mute", "Mute", c.muted); changed {
							m.edit(window, c.id, "mute", muted, func(c *channel) { c.muted = muted })
						}
						f.Meter("level", level)
						f.Label(c.name)
					})
				})
			}
		})
	}
}

// edit applies one user edit to the model and logs it. The other window learns
// of it through invalidation, as of any other model change.
func (m *mixer) edit(window, channel, control string, value any, apply func(*channel)) {
	m.desk.mu.Lock()
	m.desk.edits++
	sequence := m.desk.edits
	if c := m.desk.find(channel); c != nil {
		apply(c)
	}
	m.desk.mu.Unlock()
	m.o.event("edit", "window", window, "channel", channel, "control", control, "value", value, "sequence", sequence)
	m.src.invalidateAll()
}

// snapshotFirst writes the first window's retained image into the -snapshots
// directory, for the auditions' visual record.
func (m *mixer) snapshotFirst(name string) {
	m.post(func() {
		ws := appcore.ContentWindows(m.a)
		if len(ws) == 0 || m.c.snapshots == "" {
			m.o.event("snapshot-error", "error", "no window or no -snapshots directory")
			return
		}
		path := filepath.Join(m.c.snapshots, filepath.Base(name)+".png")
		if err := appcore.AuditionSnapshot(ws[0], path); err != nil {
			m.o.event("snapshot-error", "error", err.Error())
			return
		}
		m.o.event("snapshot", "path", path)
	})
}

// deskCommand handles the console's model commands. These are external changes:
// they invalidate and never produce an edit.
func (m *mixer) deskCommand(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 || !m.c.controls {
		return false
	}
	if fields[0] == "snapshot" && len(fields) == 2 {
		m.snapshotFirst(fields[1])
		return true
	}
	d := m.desk
	d.mu.Lock()
	done := true
	switch {
	case fields[0] == "gain" && len(fields) == 3:
		if c := d.find(fields[1]); c != nil {
			if v, err := strconv.ParseFloat(fields[2], 64); err == nil && v >= 0 && v <= 1 {
				c.gain = v
			}
		}
	case fields[0] == "mute" && len(fields) == 3:
		if c := d.find(fields[1]); c != nil {
			c.muted = fields[2] == "on"
		}
	case fields[0] == "reorder" && len(fields) == 1:
		slices.Reverse(d.channels)
	case fields[0] == "order" && len(fields) == len(d.channels)+1:
		slices.SortStableFunc(d.channels, func(x, y *channel) int {
			return slices.Index(fields[1:], x.id) - slices.Index(fields[1:], y.id)
		})
	case fields[0] == "remove" && len(fields) == 2:
		if c := d.find(fields[1]); c != nil {
			d.channels = slices.DeleteFunc(d.channels, func(x *channel) bool { return x == c })
			d.removed = append(d.removed, c)
		}
	case fields[0] == "insert" && len(fields) == 2:
		at := slices.IndexFunc(d.removed, func(x *channel) bool { return x.id == fields[1] })
		if at >= 0 {
			d.channels = append(d.channels, d.removed[at])
			d.removed = slices.Delete(d.removed, at, at+1)
		}
	default:
		done = false
	}
	d.mu.Unlock()
	if done {
		m.o.event("external", "command", line)
		m.src.invalidateAll()
	}
	return done
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
