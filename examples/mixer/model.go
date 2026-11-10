package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/michaelquigley/dfx2"
)

// The application-owned model behind every window. It stands for three things
// a real mixer keeps apart: the synthetic device's authoritative channel state,
// the producer's sampled levels, and this example's pending values for commands
// the device has not acknowledged yet. Every change goes through publish, which
// builds a new owned Snapshot under the mutex and then invalidates the
// subscribed windows outside it. Views only read snapshots.

// channelNames gives the six stable channel IDs, a to f, their display names.
var channelNames = []string{"Kick", "Snare", "Bass", "Keys", "Vocal", "Room"}

// Snapshot is one publication. It is never mutated after publication, so a
// view may hold it for the whole frame without locking.
type Snapshot struct {
	Version   uint64
	Tick      uint64
	Running   bool
	Level, DB float64 // the default view's single meter
	Channels  []Channel
	Commands  CommandStats
	Error     string // the latest command failure, shown in every window
	Churn     int    // words of changing text in the churn label, 0 for none
}

// Channel is one strip's published state. Gain and Muted are the device's;
// a pending value is this example's edit whose command is still outstanding.
type Channel struct {
	ID, Name    string
	Level, DB   float64
	Gain        float64
	Muted       bool
	GainPending bool
	PendingGain float64
	GainSeq     uint64
	MutePending bool
	PendingMute bool
	MuteSeq     uint64
}

// ShownGain is what the view supplies to the fader: the pending edit while its
// command is outstanding, otherwise the device's value.
func (c Channel) ShownGain() float64 {
	if c.GainPending {
		return c.PendingGain
	}
	return c.Gain
}

func (c Channel) ShownMute() bool {
	if c.MutePending {
		return c.PendingMute
	}
	return c.Muted
}

// CommandStats are the command totals and device fixture settings.
// Refused commands were never accepted (queue full or shutting down);
// Rejected ones were accepted and then refused by the device.
type CommandStats struct {
	Accepted, Applied, Refused, Rejected, Canceled uint64
	Outstanding                                    int // accepted, not yet resolved
	Delay                                          time.Duration
	RejectNext                                     int
}

// invalidator is a subscribed window; *ui.Window in the program.
type invalidator interface{ Invalidate() error }

type model struct {
	mu          sync.Mutex
	current     *Snapshot
	version     uint64
	channels    []*Channel // display order
	removed     []*Channel
	tick        uint64
	running     bool
	level, db   float64
	stats       CommandStats
	lastError   string
	churn       int
	delaySet    chan struct{} // closed and replaced when the delay changes
	subscribers map[invalidator]struct{}
	refused     uint64 // invalidations refused by closing windows
	control     chan bool

	// collected runs after a publication has collected its subscribers and
	// before it invalidates them; tests close a window there.
	collected func()
}

func newModel(running bool) *model {
	m := &model{running: running, db: -60, subscribers: make(map[invalidator]struct{}), control: make(chan bool, 1), delaySet: make(chan struct{})}
	for i, name := range channelNames {
		m.channels = append(m.channels, &Channel{ID: string(rune('a' + i)), Name: name, Gain: 0.5, DB: -60})
	}
	m.current = m.snapshotLocked()
	return m
}

// Snapshot returns the latest publication. Any goroutine.
func (m *model) Snapshot() *Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

func (m *model) snapshotLocked() *Snapshot {
	m.version++
	s := &Snapshot{Version: m.version, Tick: m.tick, Running: m.running, Level: m.level, DB: m.db,
		Channels: make([]Channel, len(m.channels)), Commands: m.stats, Error: m.lastError, Churn: m.churn}
	for i, c := range m.channels {
		s.Channels[i] = *c
	}
	return s
}

func (m *model) subscribe(w invalidator) {
	m.mu.Lock()
	m.subscribers[w] = struct{}{}
	m.mu.Unlock()
}

func (m *model) unsubscribe(w invalidator) {
	m.mu.Lock()
	delete(m.subscribers, w)
	m.mu.Unlock()
}

func (m *model) refusedInvalidations() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refused
}

// publish applies change under the mutex, publishes a new snapshot, and then
// invalidates the windows subscribed at that moment, outside the mutex. A
// window that started closing after it was collected refuses with
// dfx2.ErrClosed; that race is expected and only counted. A change returning
// false publishes nothing.
func (m *model) publish(change func() bool) bool {
	m.mu.Lock()
	if change != nil && !change() {
		m.mu.Unlock()
		return false
	}
	m.current = m.snapshotLocked()
	windows := make([]invalidator, 0, len(m.subscribers))
	for w := range m.subscribers {
		windows = append(windows, w)
	}
	collected := m.collected
	m.mu.Unlock()
	if collected != nil {
		collected()
	}
	for _, w := range windows {
		if err := w.Invalidate(); errors.Is(err, dfx2.ErrClosed) {
			m.mu.Lock()
			m.refused++
			m.mu.Unlock()
		}
	}
	return true
}

func (m *model) findLocked(id string) *Channel {
	for _, list := range [][]*Channel{m.channels, m.removed} {
		for _, c := range list {
			if c.ID == id {
				return c
			}
		}
	}
	return nil
}

func (m *model) setRunning(on bool) {
	select {
	case m.control <- on:
	default:
		// A pending request is replaced by the newest one.
		select {
		case <-m.control:
		default:
		}
		m.control <- on
	}
}

// sample advances the synthetic levels to tick n and publishes them.
func (m *model) sample(n uint64) {
	m.publish(func() bool {
		m.tick = n
		m.db = levelDB(n)
		m.level = normalized(m.db)
		for _, list := range [][]*Channel{m.channels, m.removed} {
			for _, c := range list {
				c.DB = levelDB(n + uint64(c.ID[0]-'a')*997)
				c.Level = normalized(c.DB)
			}
		}
		return true
	})
}

func normalized(db float64) float64 { return min(max((db+60)/60, 0), 1) }

// run is the 30 Hz producer, owned by the application rather than a window.
func (m *model) run(ctx context.Context, o *output) error {
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	var n uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case on := <-m.control:
			if m.publish(func() bool {
				changed := m.running != on
				m.running = on
				return changed
			}) {
				o.event("feed", "running", on)
			}
		case <-ticker.C:
			m.mu.Lock()
			running := m.running
			m.mu.Unlock()
			if running {
				n++
				m.sample(n)
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

// external applies one console fixture change: the device or its operator
// changing state underneath the application. It publishes a snapshot and never
// submits a command. It reports whether line was a model command.
func (m *model) external(line string) (bool, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false, nil
	}
	var err error
	known := true
	m.publish(func() bool {
		switch {
		case fields[0] == "gain" && len(fields) == 3:
			c := m.findLocked(fields[1])
			v, perr := strconv.ParseFloat(fields[2], 64)
			switch {
			case c == nil:
				err = fmt.Errorf("no channel %q", fields[1])
			case perr != nil || !(v >= 0 && v <= 1):
				err = fmt.Errorf("gain %q is not in [0, 1]", fields[2])
			default:
				c.Gain = v
			}
		case fields[0] == "mute" && len(fields) == 3:
			c := m.findLocked(fields[1])
			switch {
			case c == nil:
				err = fmt.Errorf("no channel %q", fields[1])
			case fields[2] != "on" && fields[2] != "off":
				err = fmt.Errorf("mute takes on or off, not %q", fields[2])
			default:
				c.Muted = fields[2] == "on"
			}
		case fields[0] == "reorder" && len(fields) == 1:
			slices.Reverse(m.channels)
		case fields[0] == "order" && len(fields) > 1:
			ids := fields[1:]
			if len(ids) != len(m.channels) || slices.ContainsFunc(m.channels, func(c *Channel) bool { return !slices.Contains(ids, c.ID) }) {
				err = fmt.Errorf("order must name each shown channel once: %v", ids)
				break
			}
			slices.SortStableFunc(m.channels, func(x, y *Channel) int { return slices.Index(ids, x.ID) - slices.Index(ids, y.ID) })
		case fields[0] == "remove" && len(fields) == 2:
			at := slices.IndexFunc(m.channels, func(c *Channel) bool { return c.ID == fields[1] })
			if at < 0 {
				err = fmt.Errorf("no shown channel %q", fields[1])
				break
			}
			m.removed = append(m.removed, m.channels[at])
			m.channels = slices.Delete(m.channels, at, at+1)
		case fields[0] == "insert" && len(fields) == 2:
			at := slices.IndexFunc(m.removed, func(c *Channel) bool { return c.ID == fields[1] })
			if at < 0 {
				err = fmt.Errorf("no removed channel %q", fields[1])
				break
			}
			m.channels = append(m.channels, m.removed[at])
			m.removed = slices.Delete(m.removed, at, at+1)
		default:
			known = false
		}
		return known && err == nil
	})
	return known, err
}

// setFixture changes the device delay, pending rejections or churn words, for
// display and for the worker; it is not a model edit.
func (m *model) setDelay(d time.Duration) {
	m.publish(func() bool {
		m.stats.Delay = d
		close(m.delaySet)
		m.delaySet = make(chan struct{})
		return true
	})
}
func (m *model) setRejectNext(n int) { m.publish(func() bool { m.stats.RejectNext = n; return true }) }
func (m *model) setChurn(n int)      { m.publish(func() bool { m.churn = n; return true }) }

// delay is the device delay and a channel closed when it next changes.
func (m *model) delay() (time.Duration, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats.Delay, m.delaySet
}

// The pending-value policy. An accepted command makes its value pending on
// its channel, keyed by sequence number, so every window shows it and the next
// edit starts from it. A resolution clears the pending value only if it is
// still that command's: a later accepted edit on the same control supersedes
// it, and stays pending until its own resolution. Applying sets the device's
// value. A device rejection or a cancellation leaves the device's value, so
// the view returns to authoritative state, and the reason is shown. None of
// this submits anything: a value the view supplies never reports a change.

// accept records cmd as pending. The caller holds the queue lock, so pending
// values are set before the worker can see the command.
func (m *model) accept(cmd Command) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.findLocked(cmd.Channel); c != nil {
		if cmd.Control == "gain" {
			c.GainPending, c.PendingGain, c.GainSeq = true, cmd.Gain, cmd.Seq
		} else {
			c.MutePending, c.PendingMute, c.MuteSeq = true, cmd.Mute, cmd.Seq
		}
	}
	m.stats.Accepted++
	m.stats.Outstanding++
}

// refuse publishes a submission that was never accepted.
func (m *model) refuse(cmd Command, reason error) {
	m.publish(func() bool {
		m.stats.Refused++
		m.lastError = fmt.Sprintf("#%d %s %s %s refused: %v", cmd.Seq, cmd.Channel, cmd.Control, cmd.ValueText(), reason)
		return true
	})
}

// deliver hands cmd to the synthetic device, which applies it or rejects it,
// and publishes the result.
func (m *model) deliver(cmd Command) (state, reason string) {
	m.publish(func() bool {
		c := m.findLocked(cmd.Channel)
		switch {
		case m.stats.RejectNext > 0:
			m.stats.RejectNext--
			state, reason = "rejected", "device refused"
		case c == nil || slices.Contains(m.removed, c):
			state, reason = "rejected", "channel removed"
		default:
			state = "applied"
			if cmd.Control == "gain" {
				c.Gain = cmd.Gain
			} else {
				c.Muted = cmd.Mute
			}
		}
		m.resolveLocked(cmd, state, reason)
		return true
	})
	return state, reason
}

// cancel publishes commands that never reached the device.
func (m *model) cancel(cmds []Command) {
	if len(cmds) == 0 {
		return
	}
	m.publish(func() bool {
		for _, cmd := range cmds {
			m.resolveLocked(cmd, "canceled", "")
		}
		return true
	})
}

func (m *model) resolveLocked(cmd Command, state, reason string) {
	if c := m.findLocked(cmd.Channel); c != nil {
		if cmd.Control == "gain" && c.GainPending && c.GainSeq == cmd.Seq {
			c.GainPending = false
		}
		if cmd.Control == "mute" && c.MutePending && c.MuteSeq == cmd.Seq {
			c.MutePending = false
		}
	}
	m.stats.Outstanding--
	switch state {
	case "applied":
		m.stats.Applied++
	case "rejected":
		m.stats.Rejected++
		m.lastError = fmt.Sprintf("#%d %s %s %s rejected: %s", cmd.Seq, cmd.Channel, cmd.Control, cmd.ValueText(), reason)
	case "canceled":
		m.stats.Canceled++
	}
}
