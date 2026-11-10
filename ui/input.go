package ui

import (
	"image"
	"math"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// control is one fader's or checkbox's retained interaction state. value is
// what the last evaluation returned, and is the base of the next edit, so an
// edit never builds on a snapshot the user no longer sees. A checkbox holds 1
// when checked.
type control struct {
	role          role
	value         float64
	pending       float64
	edited        bool // pending awaits the view, which can report one edit an evaluation
	seen          bool
	releaseInside bool // a checkbox release already validated before edit deferral
}

// The queue still owns the press event. Keep its resolved meaning until that
// event is retried; a nil control marks removal, so it cannot hit a new target.
type deferredFaderPress struct {
	path  string
	ctl   *control
	value float64
}

// resolve returns what the view should use. An edit wins. Otherwise a gesture
// that owns the value keeps it, and the supplied value is displayed.
func (ctl *control) resolve(supplied float64, owned bool) (float64, bool) {
	out, changed := supplied, false
	switch {
	case ctl.edited:
		out, changed, ctl.edited = ctl.pending, true, false
	case owned:
		out = ctl.value
	}
	ctl.value, ctl.seen = out, true
	return out, changed
}

func (ctl *control) current() float64 {
	if ctl.edited {
		return ctl.pending
	}
	return ctl.value
}

// set records an edit. It reports false, changing nothing, if an earlier edit
// still awaits the view: the event waits for the next frame.
func (ctl *control) set(value float64) bool {
	if value == ctl.current() {
		return true
	}
	if ctl.edited {
		return false
	}
	ctl.pending, ctl.edited = value, true
	return true
}

// control returns the state under path, starting fresh if the key is new or
// now names a different kind of control.
func (c *content) control(path string, r role, supplied float64) *control {
	ctl := c.controls[path]
	if ctl == nil || ctl.role != r {
		if ctl != nil {
			c.forget(path)
		}
		ctl = &control{role: r, value: supplied}
		c.controls[path] = ctl
	}
	return ctl
}

// forget ends everything that refers to a control that no longer exists.
// A deferred press can already own native capture before UI capture is set.
func (c *content) forget(path string) {
	if p := c.deferredPress; p != nil && p.path == path {
		p.ctl = nil
		c.releaseCapture = true
	}
	if c.capture == path {
		c.capture = ""
		c.releaseCapture = true
	}
	if c.focus == path {
		c.focus = ""
	}
	if c.spaceHeld == path {
		c.spaceHeld = ""
	}
}

// cancelGesture clears pressed state without manufacturing an edit. Edits
// already returned remain the application's. Control focus survives.
func (c *content) cancelGesture() {
	c.deferredPress = nil
	if ctl := c.controls[c.capture]; ctl != nil {
		ctl.releaseInside = false
	}
	if c.capture != "" {
		c.capture = ""
		c.releaseCapture = true
	}
	c.spaceHeld = ""
}

// advance applies queued input to the last completed layout and returns how
// many events it consumed. It stops before an event that would make a second
// edit to one control, because the view can report only one per evaluation; that
// event and the rest wait for a later frame. The first event can never stop:
// no edit is pending yet, so queued input always makes progress. Overflow
// discarded events, so whatever gesture they belonged to can no longer finish.
func (c *content) advance(events []platform.Input, lost bool) int {
	if lost {
		c.cancelGesture()
	}
	for i, in := range events {
		if !c.apply(in) {
			return i
		}
	}
	return len(events)
}

func (c *content) apply(in platform.Input) bool {
	switch in.Kind {
	case platform.PointerDown:
		if in.Button == platform.ButtonLeft {
			return c.press(in)
		}
	case platform.PointerMove:
		if h := c.captured(); h != nil && h.role == roleFader {
			return c.drag(h, in)
		}
	case platform.PointerUp:
		if in.Button == platform.ButtonLeft {
			return c.release(in)
		}
	case platform.KeyDown:
		return c.keyDown(in)
	case platform.KeyUp:
		return c.keyUp(in)
	case platform.FocusGained:
		c.active = true
	case platform.FocusLost:
		c.active = false
		c.cancelGesture()
	case platform.CaptureLost:
		c.cancelGesture()
	}
	return true
}

func (c *content) press(in platform.Input) bool {
	if p := c.deferredPress; p != nil {
		if p.ctl == nil || c.controls[p.path] != p.ctl {
			c.cancelGesture()
			c.releaseCapture = true
			return true // consume the canceled press, without a new hit test
		}
		if !p.ctl.set(p.value) {
			return false
		}
		c.cancelGesture()
		c.releaseCapture = false
		c.focus, c.capture = p.path, p.path
		return true
	}
	h := c.hitAt(in.X, in.Y)
	if h == nil {
		c.cancelGesture()
		c.releaseCapture = false // this press owns any native capture now
		return true
	}
	if h.role == roleFader {
		ctl := c.controls[h.path]
		value := c.faderValue(h, ctl, in.Y)
		if !ctl.set(value) {
			c.deferredPress = &deferredFaderPress{path: h.path, ctl: ctl, value: value}
			return false
		}
	}
	c.cancelGesture()
	c.releaseCapture = false
	c.focus, c.capture = h.path, h.path
	return true
}

func (c *content) drag(h *hit, in platform.Input) bool {
	ctl := c.controls[h.path]
	return ctl.set(c.faderValue(h, ctl, in.Y))
}

func (c *content) release(in platform.Input) bool {
	h := c.captured()
	if h == nil {
		c.capture = "" // the target is gone; a press found nothing to end
		return true
	}
	ctl := c.controls[h.path]
	switch h.role {
	case roleFader:
		if !ctl.set(c.faderValue(h, ctl, in.Y)) {
			return false
		}
	case roleCheckbox:
		// Retrying this release must not hit-test against a layout that the
		// preceding evaluation may have moved. The capture keeps its identity.
		if (ctl.releaseInside || c.pointIn(in.X, in.Y, h.visible)) && !ctl.set(1-ctl.current()) {
			ctl.releaseInside = true
			return false
		}
		ctl.releaseInside = false
	}
	c.capture = ""
	return true
}

func (c *content) keyDown(in platform.Input) bool {
	if in.Key == platform.KeyTab {
		c.cancelGesture()
		c.traverse(in.Modifiers&platform.ModShift != 0)
		return true
	}
	ctl := c.controls[c.focus]
	if ctl == nil || c.capture != "" {
		return true
	}
	switch ctl.role {
	case roleFader:
		value := ctl.current()
		switch in.Key {
		case platform.KeyArrowUp, platform.KeyArrowRight:
			value += 0.01
		case platform.KeyArrowDown, platform.KeyArrowLeft:
			value -= 0.01
		case platform.KeyPageUp:
			value += 0.1
		case platform.KeyPageDown:
			value -= 0.1
		case platform.KeyHome:
			value = 0
		case platform.KeyEnd:
			value = 1
		default:
			return true
		}
		// Stepping stays on the decimal grid rather than accumulating binary error.
		return ctl.set(math.Round(min(max(value, 0), 1)*1e6) / 1e6)
	case roleCheckbox:
		if in.Key == platform.KeySpace {
			c.spaceHeld = c.focus
		}
	}
	return true
}

func (c *content) keyUp(in platform.Input) bool {
	if in.Key != platform.KeySpace || c.spaceHeld == "" {
		return true
	}
	if ctl := c.controls[c.spaceHeld]; ctl != nil && !ctl.set(1-ctl.current()) {
		return false
	}
	c.spaceHeld = ""
	return true
}

// traverse moves focus through the controls in recorded order, wrapping. With
// no focused control, Tab starts at the first and Shift+Tab at the last.
func (c *content) traverse(backward bool) {
	var order []string
	at := -1
	for i := range c.hits {
		if h := &c.hits[i]; h.role == roleFader || h.role == roleCheckbox {
			if h.path == c.focus {
				at = len(order)
			}
			order = append(order, h.path)
		}
	}
	if len(order) == 0 {
		return
	}
	switch {
	case at < 0 && backward:
		at = len(order) - 1
	case at < 0:
		at = 0
	case backward:
		at = (at + len(order) - 1) % len(order)
	default:
		at = (at + 1) % len(order)
	}
	c.focus = order[at]
}

// captured finds the gesture's target by identity in the last completed layout,
// wherever the view has moved it since the press.
func (c *content) captured() *hit {
	if c.capture == "" {
		return nil
	}
	for i := range c.hits {
		if c.hits[i].path == c.capture {
			return &c.hits[i]
		}
	}
	return nil
}

func (c *content) hitAt(x, y float64) *hit {
	for i := range c.hits {
		if h := &c.hits[i]; (h.role == roleFader || h.role == roleCheckbox) && c.pointIn(x, y, h.visible) {
			return h
		}
	}
	return nil
}

// pointIn compares a logical input coordinate with physical layout, at the
// scale that layout was solved for.
func (c *content) pointIn(x, y float64, r image.Rectangle) bool {
	return image.Pt(int(math.Floor(x*c.hitScale)), int(math.Floor(y*c.hitScale))).In(r)
}

// faderValue maps a logical y to a value, high at the top, clamped to [0, 1].
// The thumb's center follows the pointer. A fader too short to travel keeps its value.
func (c *content) faderValue(h *hit, ctl *control, y float64) float64 {
	thumb := float64(thumbHeight(c.hitScale))
	travel := float64(h.rect.Dy()) - thumb
	if travel <= 0 {
		return ctl.current()
	}
	return min(max(1-(y*c.hitScale-float64(h.rect.Min.Y)-thumb/2)/travel, 0), 1)
}

func thumbHeight(scale float64) int { return int(math.Round(faderThumb * scale)) }
