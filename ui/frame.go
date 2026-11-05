package ui

import (
	"fmt"
	"math"
	"time"
)

// Frame records one evaluation of the view. It is valid only during the draw
// call that received it: the framework measures and renders the record after
// draw returns and never calls draw again for that frame.
//
// Widgets are placed top to bottom in the order they are recorded. Programmer
// mistakes (a duplicate key, an invalid value, use after draw returned) panic;
// the framework turns the panic into an application failure with its stack,
// so it never unwinds through a native callback.
type Frame struct {
	c       *content
	now     time.Time
	items   []item
	keys    map[string]bool
	animate bool
	done    bool
}

type role uint8

const (
	roleLabel role = iota + 1
	roleMeter
)

func (r role) String() string {
	switch r {
	case roleLabel:
		return "label"
	case roleMeter:
		return "meter"
	}
	return "unknown"
}

// item is one recorded widget with the semantic metadata accessibility will
// need later: its role, name, value and identity.
type item struct {
	role  role
	key   string // stable identity; empty for stateless labels
	name  string
	value float64 // meter level, normalized
	peak  float64 // displayed meter peak at this frame's time
}

func (f *Frame) check(what string) {
	if f.done {
		panic(fmt.Sprintf("dfx2 ui: Frame.%s after the draw call returned", what))
	}
}

// Label records a line of text. It wraps to the window's width and clips
// below the window; it has no key and no state.
func (f *Frame) Label(text string) {
	f.check("Label")
	f.items = append(f.items, item{role: roleLabel, name: text})
}

// Meter records a horizontal level meter. key identifies its retained display
// state; level is normalized to [0, 1]. The displayed peak attacks
// immediately and falls linearly at 0.5 per second of monotonic time, and the
// meter requests animation frames only while that peak is above the level.
// A meter is display only: it reports no changes.
func (f *Frame) Meter(key string, level float64) {
	f.check("Meter")
	if key == "" {
		panic("dfx2 ui: Meter requires a non-empty key")
	}
	if math.IsNaN(level) || level < 0 || level > 1 {
		panic(fmt.Sprintf("dfx2 ui: Meter %q level %v outside [0, 1]", key, level))
	}
	if f.keys[key] {
		panic(fmt.Sprintf("dfx2 ui: duplicate key %q", key))
	}
	f.keys[key] = true
	m := f.c.meters[key]
	if m == nil {
		m = &meterState{peak: level, at: f.now}
		f.c.meters[key] = m
	} else {
		// Elapsed time, not frame count: missed frames neither slow the
		// decay nor cause catch-up frames.
		decayed := m.peak - meterDecay*f.now.Sub(m.at).Seconds()
		m.peak, m.at = max(level, decayed), f.now
	}
	m.seen = true
	if m.peak > level {
		f.animate = true
	}
	f.items = append(f.items, item{role: roleMeter, key: key, name: key, value: level, peak: m.peak})
}

// meterDecay is the peak's fall, in normalized units per second.
const meterDecay = 0.5

type meterState struct {
	peak float64
	at   time.Time
	seen bool
}
