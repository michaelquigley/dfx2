package ui

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Frame records one evaluation of the view. It is valid only during the draw
// call that received it: the framework measures and renders the record after
// draw returns and never calls draw again for that frame.
//
// The view's root is a column with 16 logical units of padding and 10 between
// children, filling the window. Programmer mistakes (a duplicate or empty key,
// an invalid value or layout, use after draw returned) panic; the framework
// turns the panic into an application failure with its stack, so it never
// unwinds through a native callback.
type Frame struct {
	c       *content
	now     time.Time
	items   []item
	keys    map[string]bool
	animate bool
	done    bool
	root    *node
	current *node
	scope   string
}

type role uint8

const (
	roleLabel role = iota + 1
	roleMeter
	roleFader
	roleCheckbox
)

func (r role) String() string {
	switch r {
	case roleLabel:
		return "label"
	case roleMeter:
		return "meter"
	case roleFader:
		return "fader"
	case roleCheckbox:
		return "checkbox"
	}
	return "unknown"
}

// item is one recorded widget with the semantic metadata accessibility will
// need later: its role, name, value and identity.
type item struct {
	role    role
	key     string // full scope path; empty for stateless labels
	name    string
	value   float64 // meter level, fader value, or 1 for a checked checkbox
	peak    float64 // displayed meter peak at this frame's time
	focused bool
	pressed bool
}

func (f *Frame) check(what string) {
	if f.done {
		panic(fmt.Sprintf("dfx2 ui: Frame.%s after the draw call returned", what))
	}
}

var keyEscaper = strings.NewReplacer(`\`, `\\`, `/`, `\/`)

// claim returns key's full path under the current scope, rejecting an empty
// key or one a sibling already used. Paths are unambiguous: a key containing
// "/" cannot pass for a nested scope.
func (f *Frame) claim(what, key string) string {
	if key == "" {
		panic(fmt.Sprintf("dfx2 ui: %s requires a non-empty key", what))
	}
	path := keyEscaper.Replace(key)
	if f.scope != "" {
		path = f.scope + "/" + path
	}
	if f.keys[path] {
		panic(fmt.Sprintf("dfx2 ui: duplicate key %q", path))
	}
	f.keys[path] = true
	return path
}

func (f *Frame) leaf(it item) {
	f.items = append(f.items, it)
	f.current.children = append(f.current.children, &node{item: len(f.items) - 1})
}

func checkNormalized(what, key string, value float64) {
	if math.IsNaN(value) || value < 0 || value > 1 {
		panic(fmt.Sprintf("dfx2 ui: %s %q value %v outside [0, 1]", what, key, value))
	}
}

// Row places body's widgets left to right.
func (f *Frame) Row(layout Layout, body func(*Frame)) { f.container("Row", axisX, layout, body) }

// Column places body's widgets top to bottom.
func (f *Frame) Column(layout Layout, body func(*Frame)) { f.container("Column", axisY, layout, body) }

func (f *Frame) container(what string, main axis, layout Layout, body func(*Frame)) {
	f.check(what)
	if body == nil {
		panic(fmt.Sprintf("dfx2 ui: %s requires a body", what))
	}
	layout.validate(what)
	n := &node{item: -1, main: main, layout: layout}
	parent := f.current
	parent.children = append(parent.children, n)
	f.current = n
	defer func() { f.current = parent }()
	body(f)
}

// Scope names body's widgets: their identity is the path of enclosing scope
// keys plus their own, so they keep state and focus when the view reorders
// them. A scope adds no layout. Sibling scopes and widgets share one key space.
func (f *Frame) Scope(key string, body func(*Frame)) {
	f.check("Scope")
	if body == nil {
		panic("dfx2 ui: Scope requires a body")
	}
	path := f.claim("Scope", key)
	parent := f.scope
	f.scope = path
	defer func() { f.scope = parent }()
	body(f)
}

// Label records text. It wraps to the width its container assigns and takes
// the size of the result; it has no key and no state.
func (f *Frame) Label(text string) {
	f.check("Label")
	f.leaf(item{role: roleLabel, name: text})
}

// Meter records a horizontal level meter as wide as its container offers, or
// 120 logical units inside a row. key identifies its retained display state;
// level is normalized to [0, 1]. The displayed peak attacks immediately and
// falls linearly at 0.5 per second of monotonic time, and the meter requests
// animation frames only while that peak is above the level. A meter is display
// only: it reports no changes.
func (f *Frame) Meter(key string, level float64) {
	f.check("Meter")
	path := f.claim("Meter", key)
	checkNormalized("Meter", key, level)
	m := f.c.meters[path]
	if m == nil {
		m = &meterState{peak: level, at: f.now}
		f.c.meters[path] = m
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
	f.leaf(item{role: roleMeter, key: path, name: key, value: level, peak: m.peak})
}

// Fader records a vertical fader showing value, normalized to [0, 1], high at
// the top. It returns the value to use from now on and whether the user changed
// it: changed is never true for a new value the application supplied. During a
// drag the gesture owns the displayed value, whatever value says; after it ends,
// frames follow value again. Drag with the left button, or use the arrow keys
// (0.01), Page Up and Page Down (0.1), and Home and End (0 and 1).
func (f *Frame) Fader(key string, value float64) (float64, bool) {
	f.check("Fader")
	path := f.claim("Fader", key)
	checkNormalized("Fader", key, value)
	ctl := f.c.control(path, roleFader, value)
	out, changed := ctl.resolve(value, f.c.capture == path)
	f.leaf(item{role: roleFader, key: path, name: key, value: out, focused: f.c.focus == path, pressed: f.c.capture == path})
	return out, changed
}

// Checkbox records a labeled checkbox showing value. It returns the value to
// use from now on and whether the user changed it: changed is never true for a
// new value the application supplied. Press and release the left button inside
// it, or press and release Space while it has focus.
func (f *Frame) Checkbox(key, label string, value bool) (bool, bool) {
	f.check("Checkbox")
	path := f.claim("Checkbox", key)
	supplied := 0.0
	if value {
		supplied = 1
	}
	ctl := f.c.control(path, roleCheckbox, supplied)
	out, changed := ctl.resolve(supplied, false)
	f.leaf(item{role: roleCheckbox, key: path, name: label, value: out, focused: f.c.focus == path,
		pressed: f.c.capture == path || f.c.spaceHeld == path})
	return out == 1, changed
}

// meterDecay is the peak's fall, in normalized units per second.
const meterDecay = 0.5

type meterState struct {
	peak float64
	at   time.Time
	seen bool
}
