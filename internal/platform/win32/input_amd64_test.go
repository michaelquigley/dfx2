package win32

import (
	"errors"
	"reflect"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

func (f *systemModel) inputs() (out []platform.Input) {
	for _, e := range f.events {
		if e.Kind == platform.InputReceived {
			out = append(out, e.Input)
		}
	}
	return out
}

// lparam packs client coordinates as WM_MOUSEMOVE does, in two signed shorts.
func lparam(x, y int16) uintptr { return uintptr(uint16(x)) | uintptr(uint16(y))<<16 }

func TestPointerEventsCopyLogicalCoordinatesAndTakeCapture(t *testing.T) {
	f := model(t)
	f.dpi = 192
	w := openContent(t, f, 7, 100, 100)
	f.keysDown = map[int32]bool{vkShift: true}
	windowMessage(&f.api, w.hwnd, wmMouseMove, 0, lparam(4, 4)) // hover: no widget uses it
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, lparam(20, 40))
	if f.capture != w.hwnd || !w.captured {
		t.Fatalf("capture %d captured=%t", f.capture, w.captured)
	}
	f.keysDown = map[int32]bool{vkControl: true, vkMenu: true}
	windowMessage(&f.api, w.hwnd, wmMouseMove, 0, lparam(-30, 3000)) // left of the window, under capture
	f.keysDown = nil
	windowMessage(&f.api, w.hwnd, wmLButtonUp, 0, lparam(-30, 3000))
	if f.capture != 0 || w.captured {
		t.Fatalf("release left capture %d captured=%t", f.capture, w.captured)
	}
	windowMessage(&f.api, w.hwnd, wmMouseMove, 0, lparam(8, 8)) // hover again
	want := []platform.Input{
		{Kind: platform.PointerDown, Button: platform.ButtonLeft, X: 10, Y: 20, Modifiers: platform.ModShift},
		{Kind: platform.PointerMove, X: -15, Y: 1500, Modifiers: platform.ModControl | platform.ModAlt},
		{Kind: platform.PointerUp, Button: platform.ButtonLeft, X: -15, Y: 1500},
	}
	if got := f.inputs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("input %+v\nwant %+v", got, want)
	}
}

func TestOwnReleaseIsNotReportedAsACaptureLoss(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmLButtonUp, 0, 0)
	for _, in := range f.inputs() {
		if in.Kind == platform.CaptureLost {
			t.Fatal("the backend's own release read as a loss")
		}
	}
}

func TestNativeCodeTakingCaptureReportsALossOnce(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	f.api.setCapture(999) // a size/move loop, a menu, another window
	windowMessage(&f.api, w.hwnd, wmCaptureChanged, 0, 999)
	lost := 0
	for _, in := range f.inputs() {
		if in.Kind == platform.CaptureLost {
			lost++
		}
	}
	if lost != 1 || w.captured {
		t.Fatalf("losses %d captured=%t", lost, w.captured)
	}
	// With capture gone, motion stops and this window cannot release the new owner's.
	before := len(f.inputs())
	windowMessage(&f.api, w.hwnd, wmMouseMove, 0, lparam(1, 1))
	if err := f.b.ReleaseCapture(7); err != nil {
		t.Fatal(err)
	}
	if f.capture != 999 || len(f.inputs()) != before {
		t.Fatalf("capture %d inputs %+v", f.capture, f.inputs())
	}
}

func TestSecondWindowsPressTakesCaptureFromTheFirst(t *testing.T) {
	f := model(t)
	a := openContent(t, f, 7, 100, 100)
	b := openContent(t, f, 8, 100, 100)
	windowMessage(&f.api, a.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, b.hwnd, wmLButtonDown, 0, 0)
	if a.captured || !b.captured || f.capture != b.hwnd {
		t.Fatalf("a=%t b=%t capture=%d", a.captured, b.captured, f.capture)
	}
	var lostOn []platform.ID
	for _, e := range f.events {
		if e.Kind == platform.InputReceived && e.Input.Kind == platform.CaptureLost {
			lostOn = append(lostOn, e.Window)
		}
	}
	if !reflect.DeepEqual(lostOn, []platform.ID{7}) {
		t.Fatalf("losses on %v", lostOn)
	}
}

func TestFocusLossReleasesCaptureAtOnce(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmSetFocus, 0, 0)
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmKillFocus, 0, 0)
	if f.capture != 0 || w.captured {
		t.Fatalf("capture %d captured=%t", f.capture, w.captured)
	}
	got := f.inputs()
	if got[0].Kind != platform.FocusGained || got[len(got)-1].Kind != platform.FocusLost {
		t.Fatalf("input %+v", got)
	}
	for _, in := range got {
		if in.Kind == platform.CaptureLost {
			t.Fatal("focus loss read as a capture loss too")
		}
	}
}

func TestReleaseCaptureEndsANativeCaptureAtGestureCancel(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	if err := f.b.ReleaseCapture(7); err != nil {
		t.Fatal(err)
	}
	if f.capture != 0 || w.captured {
		t.Fatalf("capture %d captured=%t", f.capture, w.captured)
	}
	if err := f.b.ReleaseCapture(7); err != nil { // nothing held: nothing to do
		t.Fatal(err)
	}
	if err := f.b.ReleaseCapture(99); err != nil { // a window already gone
		t.Fatal(err)
	}
}

func TestFailedReleaseCaptureReachesPump(t *testing.T) {
	f := model(t)
	fault := errors.New("capture fault")
	f.captureError = fault
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmLButtonUp, 0, 0)
	if err := f.b.takeError(); !errors.Is(err, fault) {
		t.Fatalf("err %v", err)
	}
}

func TestKeysWidgetsUseAreDeliveredAndOthersAreLeftAlone(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	defaults := 0
	base := f.api.defWindow
	f.api.defWindow = func(hwnd, msg, wp, lp uintptr) uintptr {
		if msg == wmKeyDown || msg == wmKeyUp {
			defaults++
		}
		return base(hwnd, msg, wp, lp)
	}
	f.keysDown = map[int32]bool{vkShift: true}
	windowMessage(&f.api, w.hwnd, wmKeyDown, vkTab, 0)
	f.keysDown = nil
	for virtual, key := range map[uintptr]platform.Key{vkSpace: platform.KeySpace, vkUp: platform.KeyArrowUp, vkDown: platform.KeyArrowDown,
		vkLeft: platform.KeyArrowLeft, vkRight: platform.KeyArrowRight, vkPrior: platform.KeyPageUp, vkNext: platform.KeyPageDown, vkHome: platform.KeyHome, vkEnd: platform.KeyEnd} {
		f.events = nil
		windowMessage(&f.api, w.hwnd, wmKeyDown, virtual, 0)
		windowMessage(&f.api, w.hwnd, wmKeyUp, virtual, 0)
		want := []platform.Input{{Kind: platform.KeyDown, Key: key}, {Kind: platform.KeyUp, Key: key}}
		if got := f.inputs(); !reflect.DeepEqual(got, want) {
			t.Fatalf("key %#x: %+v", virtual, got)
		}
	}
	if defaults != 0 {
		t.Fatalf("%d handled keys reached DefWindowProc", defaults)
	}
	f.events = nil
	windowMessage(&f.api, w.hwnd, wmKeyDown, 'A', 0)
	windowMessage(&f.api, w.hwnd, wmKeyUp, 'A', 0)
	if got := f.inputs(); len(got) != 0 || defaults != 2 {
		t.Fatalf("a letter: inputs %+v defaults %d", got, defaults)
	}
}

func TestMiddleAndRightButtonsAreDeliveredWithoutCapture(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmRButtonDown, 0, lparam(1, 2))
	windowMessage(&f.api, w.hwnd, wmRButtonUp, 0, lparam(1, 2))
	windowMessage(&f.api, w.hwnd, wmMButtonDown, 0, lparam(3, 4))
	windowMessage(&f.api, w.hwnd, wmMButtonUp, 0, lparam(3, 4))
	if f.capture != 0 || w.captured {
		t.Fatal("only the left button captures")
	}
	want := []platform.Input{
		{Kind: platform.PointerDown, Button: platform.ButtonRight, X: 1, Y: 2}, {Kind: platform.PointerUp, Button: platform.ButtonRight, X: 1, Y: 2},
		{Kind: platform.PointerDown, Button: platform.ButtonMiddle, X: 3, Y: 4}, {Kind: platform.PointerUp, Button: platform.ButtonMiddle, X: 3, Y: 4},
	}
	if got := f.inputs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("input %+v", got)
	}
}

func TestOrdinaryAndClosingWindowsDeliverNoInput(t *testing.T) {
	f := model(t)
	if err := f.b.Create(5, platform.WindowOptions{Width: 10, Height: 10}); err != nil {
		t.Fatal(err)
	}
	ordinary := f.b.windows[5]
	windowMessage(&f.api, ordinary.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, ordinary.hwnd, wmKeyDown, vkSpace, 0)
	windowMessage(&f.api, f.b.application.hwnd, wmLButtonDown, 0, 0)
	if f.capture != 0 || len(f.inputs()) != 0 {
		t.Fatalf("capture %d input %+v", f.capture, f.inputs())
	}
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmClose, 0, 0) // closing, not yet destroyed
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmKeyDown, vkSpace, 0)
	if f.capture != 0 || len(f.inputs()) != 0 {
		t.Fatalf("closing: capture %d input %+v", f.capture, f.inputs())
	}
	if err := f.b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmKeyDown, vkSpace, 0)
	if f.capture != 0 || len(f.inputs()) != 0 {
		t.Fatalf("after close: capture %d input %+v", f.capture, f.inputs())
	}
}

// The real size/move loop takes capture inside DefWindowProc; a gesture
// cancellation in a modal turn must not break it.
func TestReleaseCaptureCannotBreakASizeMoveLoopsCapture(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmEnterSizeMove, 0, 0)
	f.capture = 0
	f.api.setCapture(w.hwnd) // DefWindowProc's loop owns capture now, on the same window
	windowMessage(&f.api, w.hwnd, wmCaptureChanged, 0, w.hwnd)
	if w.captured {
		t.Fatal("the loop's capture read as ours")
	}
	if err := f.b.ReleaseCapture(7); err != nil {
		t.Fatal(err)
	}
	if f.capture != w.hwnd {
		t.Fatal("releasing a canceled gesture broke the loop's capture")
	}
}

func TestSetAndReleaseCaptureCountAsNativeOperations(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 100, 100)
	other := openContent(t, f, 8, 100, 100)
	windowMessage(&f.api, other.hwnd, wmEnterSizeMove, 0, 0)
	var depths []int
	setCapture, releaseCapture := f.api.setCapture, f.api.releaseCapture
	f.api.setCapture = func(hwnd uintptr) { depths = append(depths, f.b.modal.depth); setCapture(hwnd) }
	f.api.releaseCapture = func() error { depths = append(depths, f.b.modal.depth); return releaseCapture() }
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmLButtonUp, 0, 0)
	if !reflect.DeepEqual(depths, []int{1, 1}) {
		t.Fatalf("depths %v", depths)
	}
	windowMessage(&f.api, other.hwnd, wmExitSizeMove, 0, 0)
}
