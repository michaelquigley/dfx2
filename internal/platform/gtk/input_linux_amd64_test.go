package gtk

import (
	"reflect"
	"slices"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// nativeEvent stands in for a GdkEvent read through GDK's accessors. Event
// addresses are handed out by index, so nothing depends on struct layout.
type nativeEvent struct {
	kind   int32
	x, y   float64
	button uint32
	keyval uint32
	state  uint32
}

func installEvents(t *testing.T, events ...nativeEvent) []uintptr {
	t.Helper()
	at := func(event uintptr) nativeEvent { return events[event-1] }
	n.eventType = func(e uintptr) int32 { return at(e).kind }
	n.eventCoords = func(e uintptr, x, y *float64) int32 { *x, *y = at(e).x, at(e).y; return 1 }
	n.eventButton = func(e uintptr, button *uint32) int32 { *button = at(e).button; return 1 }
	n.eventKeyval = func(e uintptr, keyval *uint32) int32 { *keyval = at(e).keyval; return 1 }
	n.eventState = func(e uintptr, state *uint32) int32 { *state = at(e).state; return 1 }
	n.grabFocus = func(uintptr) {}
	addresses := make([]uintptr, len(events))
	for i := range events {
		addresses[i] = uintptr(i + 1)
	}
	return addresses
}

func inputBackend(t *testing.T) (*Backend, *[]platform.Input) {
	t.Helper()
	b := contentBackend()
	var got []platform.Input
	b.sink = func(e platform.Event) {
		if e.Kind != platform.InputReceived || e.Window != 7 {
			t.Errorf("event %+v", e)
		}
		got = append(got, e.Input)
	}
	return b, &got
}

func TestNativeEventsBecomeCopiedLogicalInput(t *testing.T) {
	replaceNative(t)
	events := installEvents(t,
		nativeEvent{kind: gdkButtonPress, x: 10.5, y: -3, button: 1, state: gdkShiftMask},
		nativeEvent{kind: gdkMotionNotify, x: -40, y: 900, state: gdkControlMask | gdkMod1Mask},
		nativeEvent{kind: gdkButtonRelease, x: -40, y: 900, button: 1},
		nativeEvent{kind: gdkButtonPress, x: 1, y: 2, button: 3},
		nativeEvent{kind: gdkKeyPress, keyval: 0xfe20, state: gdkShiftMask}, // ISO_Left_Tab
		nativeEvent{kind: gdkKeyRelease, keyval: 0x20},
		nativeEvent{kind: gdkKeyPress, keyval: 0xff9a}, // KP_Prior
	)
	b, got := inputBackend(t)
	for _, event := range events {
		if handled := b.input(7, event); handled != 1 {
			t.Errorf("event %d not stopped", event)
		}
	}
	want := []platform.Input{
		{Kind: platform.PointerDown, Button: platform.ButtonLeft, X: 10.5, Y: -3, Modifiers: platform.ModShift},
		{Kind: platform.PointerMove, X: -40, Y: 900, Modifiers: platform.ModControl | platform.ModAlt},
		{Kind: platform.PointerUp, Button: platform.ButtonLeft, X: -40, Y: 900},
		{Kind: platform.PointerDown, Button: platform.ButtonRight, X: 1, Y: 2},
		{Kind: platform.KeyDown, Key: platform.KeyTab, Modifiers: platform.ModShift},
		{Kind: platform.KeyUp, Key: platform.KeySpace},
		{Kind: platform.KeyDown, Key: platform.KeyPageUp},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("input %+v\nwant %+v", *got, want)
	}
}

func TestEveryKeyAWidgetUsesIsMappedToItsOwnKey(t *testing.T) {
	want := map[platform.Key]int{platform.KeyTab: 2, platform.KeySpace: 2, platform.KeyArrowUp: 2, platform.KeyArrowDown: 2,
		platform.KeyArrowLeft: 2, platform.KeyArrowRight: 2, platform.KeyPageUp: 2, platform.KeyPageDown: 2, platform.KeyHome: 2, platform.KeyEnd: 2}
	got := map[platform.Key]int{}
	for _, key := range gdkKeys {
		got[key]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("key values per key %v", got)
	}
}

func TestOtherEventsKeepTheirNativeMeaningAndRequestNoFrame(t *testing.T) {
	replaceNative(t)
	events := installEvents(t,
		nativeEvent{kind: gdkKeyPress, keyval: 0x61},     // a letter
		nativeEvent{kind: gdkKeyRelease, keyval: 0xffbe}, // F1
		nativeEvent{kind: gdkButtonPress, button: 8},     // a side button
		nativeEvent{kind: 5, button: 1},                  // GDK_2BUTTON_PRESS, an extra after two plain presses
		nativeEvent{kind: 6, button: 1},                  // GDK_3BUTTON_PRESS
	)
	b, got := inputBackend(t)
	for _, event := range events {
		if handled := b.input(7, event); handled != 0 {
			t.Errorf("event %d was stopped", event)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("input %+v", *got)
	}
}

func TestPressGrabsKeyboardFocusForTheDrawingArea(t *testing.T) {
	replaceNative(t)
	events := installEvents(t, nativeEvent{kind: gdkButtonPress, button: 1})
	var grabbed []uintptr
	n.grabFocus = func(widget uintptr) { grabbed = append(grabbed, widget) }
	b, _ := inputBackend(t)
	b.input(7, events[0])
	if !slices.Equal(grabbed, []uintptr{12}) {
		t.Fatalf("focus grabbed on %v", grabbed)
	}
}

func TestFocusAndGrabEdgesAreDelivered(t *testing.T) {
	replaceNative(t)
	b, got := inputBackend(t)
	b.inputEdge(7, platform.FocusGained)
	b.inputEdge(7, platform.CaptureLost)
	b.inputEdge(7, platform.FocusLost)
	want := []platform.Input{{Kind: platform.FocusGained}, {Kind: platform.CaptureLost}, {Kind: platform.FocusLost}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("input %+v", *got)
	}
}

func TestClosingAndDestroyedWindowsSealInput(t *testing.T) {
	replaceNative(t)
	events := installEvents(t, nativeEvent{kind: gdkButtonPress, button: 1})
	for name, seal := range map[string]func(*window){
		"closing":   func(w *window) { w.closing = true },
		"destroyed": func(w *window) { w.destroyed = true },
	} {
		t.Run(name, func(t *testing.T) {
			b, got := inputBackend(t)
			seal(b.windows[7])
			b.input(7, events[0])
			b.inputEdge(7, platform.FocusLost)
			if len(*got) != 0 {
				t.Fatalf("sealed window delivered %+v", *got)
			}
		})
	}
	b, got := inputBackend(t)
	delete(b.windows, 7)
	b.input(7, events[0])
	b.inputEdge(7, platform.FocusLost)
	if len(*got) != 0 {
		t.Fatalf("absent window delivered %+v", *got)
	}
}

func TestContentCreationPreparesInputBeforeShowing(t *testing.T) {
	replaceNative(t)
	var order []string
	var signals []string
	var mask int32
	b := &Backend{windows: make(map[platform.ID]*window)}
	n.windowNew = func(int32) uintptr { return 11 }
	n.drawingAreaNew = func() uintptr { return 12 }
	n.ref = func(o uintptr) uintptr { return o }
	n.containerAdd = func(uintptr, uintptr) { order = append(order, "add") }
	n.addEvents = func(area uintptr, m int32) { mask = m; order = append(order, "mask") }
	n.setCanFocus = func(area uintptr, on int32) {
		if on != 1 {
			t.Error("area not focusable")
		}
		order = append(order, "focusable")
	}
	n.grabFocus = func(uintptr) { order = append(order, "focus") }
	n.connect = func(object uintptr, name string, _, _, _ uintptr, _ int32) uint64 {
		if object == 12 {
			signals = append(signals, name)
		}
		return 1
	}
	n.setTitle = func(uintptr, string) {}
	n.setDefaultSize = func(uintptr, int32, int32) {}
	n.getScale = func(uintptr) int32 { return 1 }
	n.show = func(uintptr) { order = append(order, "show") }
	if err := b.Create(7, platform.WindowOptions{Width: 4, Height: 3, Content: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { routes.Delete(b.windows[7].token) })
	if want := []string{"add", "mask", "focusable", "focus", "show"}; !slices.Equal(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}
	if mask != 1<<5|1<<8|1<<9|1<<10|1<<11|1<<14 {
		t.Fatalf("event mask %#x", mask)
	}
	for _, name := range []string{"button-press-event", "button-release-event", "motion-notify-event", "key-press-event",
		"key-release-event", "focus-in-event", "focus-out-event", "grab-broken-event", "grab-notify"} {
		if !slices.Contains(signals, name) {
			t.Errorf("%s not connected", name)
		}
	}
}

func TestReleaseCaptureDoesNothingToTheImplicitGrab(t *testing.T) {
	if err := contentBackend().ReleaseCapture(7); err != nil {
		t.Fatal(err)
	}
}
