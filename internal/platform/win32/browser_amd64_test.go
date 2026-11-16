package win32

import (
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// A browser shell is a plain window the backend draws nothing in: it reports
// its surface like a drawn window, re-reports it after a move, forwards focus
// alone, refuses presentation, and describes itself as a browser parent until
// it is destroyed.
func TestBrowserShellsReportSurfaceMovesFocusAndParent(t *testing.T) {
	f := model(t)
	if err := f.b.Create(5, platform.WindowOptions{Title: "b", Width: 100, Height: 50, Content: platform.BrowserContent}); err != nil {
		t.Fatal(err)
	}
	parent, err := f.b.BrowserParent(5)
	if err != nil {
		t.Fatal(err)
	}
	w := f.b.windows[5]
	if parent.Window != 5 || parent.Handle != w.hwnd || parent.Handle == 0 || !w.browser || w.content {
		t.Fatalf("parent %+v for %+v", parent, w)
	}
	surfaces := func() (out []platform.Surface) {
		for _, e := range f.events {
			if e.Kind == platform.SurfaceChanged && e.Window == 5 {
				out = append(out, e.Surface)
			}
		}
		return out
	}
	shown := surfaces()
	if len(shown) == 0 || !shown[len(shown)-1].Visible || shown[len(shown)-1].Width != 100 || parent.Surface != shown[len(shown)-1] {
		t.Fatalf("surface reports %+v, parent %+v", shown, parent.Surface)
	}
	// A move re-delivers the unchanged surface; a drawn window's would be
	// deduplicated.
	windowMessage(&f.api, w.hwnd, wmMove, 0, 0)
	if moved := surfaces(); len(moved) != len(shown)+1 || moved[len(moved)-1] != shown[len(shown)-1] {
		t.Fatalf("move not re-reported: %+v", moved)
	}
	// Focus is forwarded; other input is not, and nothing paints.
	before := len(f.events)
	windowMessage(&f.api, w.hwnd, wmSetFocus, 0, 0)
	windowMessage(&f.api, w.hwnd, wmLButtonDown, 0, 0)
	windowMessage(&f.api, w.hwnd, wmKeyDown, 9, 0)
	windowMessage(&f.api, w.hwnd, wmPaint, 0, 0)
	if len(f.events) != before+1 || f.events[before].Kind != platform.InputReceived || f.events[before].Input.Kind != platform.FocusGained || f.events[before].Window != 5 {
		t.Fatalf("browser shell input events: %+v", f.events[before:])
	}
	if f.paintBalance != 0 || len(f.blits) != 0 {
		t.Fatal("browser shell painted")
	}
	if err := f.b.Present(5, platform.Image{Width: 1, Height: 1, Stride: 4, Scale: 1, Pix: make([]byte, 4)}); err == nil {
		t.Fatal("presentation accepted by a browser shell")
	}
	// A drawn window is no browser parent; a destroyed shell is none either.
	if err := f.b.Create(6, platform.WindowOptions{Title: "d", Width: 10, Height: 10, Content: platform.DrawnContent}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.BrowserParent(6); err == nil {
		t.Fatal("drawn window described as a browser parent")
	}
	if _, err := f.b.BrowserParent(99); err == nil {
		t.Fatal("unknown window described as a browser parent")
	}
	if err := f.b.Destroy(5); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.BrowserParent(5); err == nil {
		t.Fatal("destroyed shell described as a browser parent")
	}
	f.b.flushRetired(nil)
	destroyed := false
	for _, e := range f.events {
		destroyed = destroyed || e.Kind == platform.Destroyed && e.Window == 5
	}
	if !destroyed {
		t.Fatal("browser shell destruction not acknowledged")
	}
}
