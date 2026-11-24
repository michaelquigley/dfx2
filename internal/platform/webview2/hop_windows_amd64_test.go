package webview2

import (
	"runtime"
	"testing"
	"unsafe"
)

// The host runs Cleanup outside the UI thread. Save it for after the
// dispatch returns so a native failure inside the window proc is observable.
type nativeHopHost struct {
	fakeHost
	cleanup func() error
}

func (h *nativeHopHost) Cleanup(_ string, work func() error) error {
	h.cleanup = work
	return nil
}

// Exercise DestroyWindow and UnregisterClassW from the final posted callback,
// just as the last attachment releases in production. No WebView2 Runtime is
// needed for this HWND-only test.
func TestNativeDispatchWindowClosesInsideDispatchAndReopens(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := install(); err != nil {
		t.Fatal(err)
	}
	peek := user32.NewProc("PeekMessageW")
	dispatch := user32.NewProc("DispatchMessageW")
	isWindow := user32.NewProc("IsWindow")
	for cycle := 1; cycle <= 2; cycle++ {
		host := &nativeHopHost{}
		if err := n.openHop(host); err != nil {
			t.Fatalf("open %d: %v", cycle, err)
		}
		hwnd := hop.hwnd
		n.post(func() { n.closeHop(host, host.Released) })
		var msg [6]uintptr // aligned storage for a 48-byte Win64 MSG
		if ok, _, _ := peek.Call(uintptr(unsafe.Pointer(&msg[0])), hwnd, wmHop, wmHop, 1); ok == 0 {
			t.Fatalf("cycle %d: no WM_APP callback message", cycle)
		}
		dispatch.Call(uintptr(unsafe.Pointer(&msg[0])))
		live, _, _ := isWindow.Call(hwnd)
		if live != 0 || host.released != 1 || len(host.failed) != 0 || host.cleanup != nil {
			t.Fatalf("close %d: live=%d released=%d failures=%v class cleanup pending=%t", cycle, live, host.released, host.failed, host.cleanup != nil)
		}
	}
}
