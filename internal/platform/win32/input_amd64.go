package win32

import "github.com/michaelquigley/dfx2/internal/platform"

// Virtual-key codes for the keys widgets use, and for reading modifiers.
const (
	vkTab, vkSpace             = 0x09, 0x20
	vkPrior, vkNext            = 0x21, 0x22
	vkEnd, vkHome              = 0x23, 0x24
	vkLeft, vkUp               = 0x25, 0x26
	vkRight, vkDown            = 0x27, 0x28
	vkShift, vkControl, vkMenu = 0x10, 0x11, 0x12
)

var virtualKeys = map[uintptr]platform.Key{
	vkTab: platform.KeyTab, vkSpace: platform.KeySpace, vkPrior: platform.KeyPageUp, vkNext: platform.KeyPageDown,
	vkEnd: platform.KeyEnd, vkHome: platform.KeyHome, vkLeft: platform.KeyArrowLeft, vkUp: platform.KeyArrowUp,
	vkRight: platform.KeyArrowRight, vkDown: platform.KeyArrowDown,
}

// input copies one native message into a core event and returns whether it was
// handled. It never runs the view: the core only queues the copy. Windows has
// no implicit grab, so a left press takes native capture to keep a drag routed
// here outside the window, the same contract GTK provides by itself. Motion is
// reported only under that capture, because no widget uses hover and idle
// pointer movement must not request frames.
func (b *Backend) input(w *window, msg, wp, lp uintptr) bool {
	switch msg {
	case wmMouseMove:
		if w.captured {
			b.pointerEvent(w, platform.PointerMove, 0, lp)
		}
		return true
	case wmLButtonDown:
		b.pointerEvent(w, platform.PointerDown, platform.ButtonLeft, lp)
		w.captured = true
		b.operation(func() { b.api.setCapture(w.hwnd) })
		return true
	case wmLButtonUp:
		b.pointerEvent(w, platform.PointerUp, platform.ButtonLeft, lp)
		b.releaseCapture(w)
		return true
	case wmRButtonDown:
		b.pointerEvent(w, platform.PointerDown, platform.ButtonRight, lp)
	case wmRButtonUp:
		b.pointerEvent(w, platform.PointerUp, platform.ButtonRight, lp)
	case wmMButtonDown:
		b.pointerEvent(w, platform.PointerDown, platform.ButtonMiddle, lp)
	case wmMButtonUp:
		b.pointerEvent(w, platform.PointerUp, platform.ButtonMiddle, lp)
	case wmKeyDown, wmKeyUp:
		key, ok := virtualKeys[wp]
		if !ok {
			return false // every other key keeps its native meaning and requests no frame
		}
		kind := platform.KeyDown
		if msg == wmKeyUp {
			kind = platform.KeyUp
		}
		b.send(w, platform.Input{Kind: kind, Key: key, Modifiers: b.modifiers()})
		return true
	case wmSetFocus:
		b.send(w, platform.Input{Kind: platform.FocusGained})
	case wmKillFocus:
		// The core may not run a frame until the window is visible again, so
		// the capture does not wait for the gesture's cancellation.
		b.releaseCapture(w)
		b.send(w, platform.Input{Kind: platform.FocusLost})
	case wmCaptureChanged:
		// Our own releases clear captured first, so this is native code taking
		// the pointer: a size/move loop, a menu, or another window.
		if w.captured {
			w.captured = false
			b.send(w, platform.Input{Kind: platform.CaptureLost})
		}
	}
	return false
}

func (b *Backend) send(w *window, in platform.Input) {
	b.sink(platform.Event{Kind: platform.InputReceived, Window: w.id, Input: in})
}

// pointerEvent converts physical client coordinates to logical. They are signed
// 16-bit values: a captured pointer left of or above the window is negative.
func (b *Backend) pointerEvent(w *window, kind platform.InputKind, button platform.Button, lp uintptr) {
	scale := 1.0
	if w.dpi != 0 {
		scale = float64(w.dpi) / 96
	}
	x, y := int16(lp&0xffff), int16(lp>>16&0xffff)
	b.send(w, platform.Input{Kind: kind, Button: button, X: float64(x) / scale, Y: float64(y) / scale, Modifiers: b.modifiers()})
}

func (b *Backend) modifiers() platform.Modifiers {
	var m platform.Modifiers
	if b.api.keyState(vkShift) < 0 {
		m |= platform.ModShift
	}
	if b.api.keyState(vkControl) < 0 {
		m |= platform.ModControl
	}
	if b.api.keyState(vkMenu) < 0 {
		m |= platform.ModAlt
	}
	return m
}

// releaseCapture ends this window's capture, if it holds one. captured is
// cleared first, so the WM_CAPTURECHANGED this causes is not reported as a loss.
func (b *Backend) releaseCapture(w *window) {
	if !w.captured {
		return
	}
	w.captured = false
	var err error
	b.operation(func() { err = b.api.releaseCapture() })
	b.nativeFailure(w, err)
}

// ReleaseCapture ends the window's capture after a gesture ended without a
// button-up edge. A size/move loop's capture is never touched: taking it
// cleared captured through WM_CAPTURECHANGED.
func (b *Backend) ReleaseCapture(id platform.ID) error {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.destroyed {
		return nil
	}
	b.releaseCapture(w)
	return nil
}
