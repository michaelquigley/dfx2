package win32

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
	"unsafe"
)

var nativeOnce sync.Once
var nativeAPI *native
var nativeError error

func callError(name string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s failed without an error code", name)
	}
	return fmt.Errorf("%s: %w", name, err)
}

func systemAPI() (*native, error) {
	nativeOnce.Do(func() {
		// System DLLs are resolved only inside Open/Run. No COM, WebView2,
		// renderer or third-party loader is initialized by this backend.
		user := syscall.NewLazyDLL("user32.dll")
		kernel := syscall.NewLazyDLL("kernel32.dll")
		proc := make(map[string]*syscall.LazyProc)
		for _, group := range []struct {
			dll   *syscall.LazyDLL
			names []string
		}{
			{user, []string{"RegisterClassExW", "UnregisterClassW", "CreateWindowExW", "DestroyWindow", "DefWindowProcW",
				"GetWindowLongPtrW", "SetWindowLongPtrW", "GetMessageW", "PeekMessageW", "TranslateMessage", "DispatchMessageW",
				"PostMessageW", "SetWindowTextW", "ShowWindow", "LoadCursorW", "GetClientRect", "SetWindowPos",
				"GetDpiForWindow", "AdjustWindowRectExForDpi", "SetProcessDpiAwarenessContext",
				"GetThreadDpiAwarenessContext", "GetAwarenessFromDpiAwarenessContext"}},
			{kernel, []string{"GetCurrentThreadId", "GetModuleHandleW", "SetLastError", "RtlMoveMemory"}},
		} {
			for _, name := range group.names {
				p := group.dll.NewProc(name)
				if err := p.Find(); err != nil {
					nativeError = fmt.Errorf("Win32 requires %s: %w", name, err)
					return
				}
				proc[name] = p
			}
		}
		a := &native{}
		a.threadID = func() uint32 { r, _, _ := proc["GetCurrentThreadId"].Call(); return uint32(r) }
		a.perMonitor = func() error {
			r, _, err := proc["SetProcessDpiAwarenessContext"].Call(^uintptr(3)) // PER_MONITOR_AWARE_V2, -4
			if int32(r) != 0 {
				return nil
			}
			if errors.Is(err, syscall.Errno(5)) { // ERROR_ACCESS_DENIED: manifest/previous initialization
				context, _, _ := proc["GetThreadDpiAwarenessContext"].Call()
				awareness, _, _ := proc["GetAwarenessFromDpiAwarenessContext"].Call(context)
				if int32(awareness) == 2 {
					return nil
				} // per-monitor v1 or v2
			}
			return callError("SetProcessDpiAwarenessContext(per-monitor v2)", err)
		}
		a.module = func() (uintptr, error) {
			r, _, err := proc["GetModuleHandleW"].Call(0)
			if r == 0 {
				return 0, callError("GetModuleHandleW", err)
			}
			return r, nil
		}
		a.cursor = func() (uintptr, error) {
			r, _, err := proc["LoadCursorW"].Call(0, 32512) // IDC_ARROW
			if r == 0 {
				return 0, callError("LoadCursorW", err)
			}
			return r, nil
		}
		a.register = func(c *classEx) error {
			r, _, err := proc["RegisterClassExW"].Call(uintptr(unsafe.Pointer(c)))
			if uint16(r) == 0 {
				return callError("RegisterClassExW", err)
			}
			return nil
		}
		a.unregister = func(name *uint16, instance uintptr) error {
			r, _, err := proc["UnregisterClassW"].Call(uintptr(unsafe.Pointer(name)), instance)
			if int32(r) == 0 {
				return callError("UnregisterClassW", err)
			}
			return nil
		}
		a.create = func(class, title *uint16, style uint32, parent, instance, token uintptr) (uintptr, error) {
			var coordinate uintptr
			if parent != hwndMessage {
				coordinate = 0x80000000
			} // CW_USEDEFAULT
			r, _, err := proc["CreateWindowExW"].Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), uintptr(style),
				coordinate, coordinate, coordinate, coordinate, parent, 0, instance, token)
			if r == 0 {
				return 0, callError("CreateWindowExW", err)
			}
			return r, nil
		}
		a.destroy = func(hwnd uintptr) error {
			r, _, err := proc["DestroyWindow"].Call(hwnd)
			if int32(r) == 0 {
				return callError("DestroyWindow", err)
			}
			return nil
		}
		a.defWindow = func(hwnd, msg, wp, lp uintptr) uintptr {
			r, _, _ := proc["DefWindowProcW"].Call(hwnd, msg, wp, lp)
			return r
		}
		a.getUserData = func(hwnd uintptr) uintptr { r, _, _ := proc["GetWindowLongPtrW"].Call(hwnd, userDataIndex); return r }
		a.setUserData = func(hwnd, token uintptr) error {
			proc["SetLastError"].Call(0) // zero return can mean successful replacement of zero
			r, _, err := proc["SetWindowLongPtrW"].Call(hwnd, userDataIndex, token)
			if r == 0 && !errors.Is(err, syscall.Errno(0)) {
				return callError("SetWindowLongPtrW", err)
			}
			return nil
		}
		a.copyMemory = func(dst unsafe.Pointer, source, size uintptr) { proc["RtlMoveMemory"].Call(uintptr(dst), source, size) }
		a.getMessage = func(m *message) (int32, error) {
			r, _, err := proc["GetMessageW"].Call(uintptr(unsafe.Pointer(m)), 0, 0, 0)
			if int32(r) == -1 {
				return -1, err
			}
			return int32(r), nil
		}
		a.peekMessage = func(m *message, hwnd uintptr, min, max uint32) bool {
			r, _, _ := proc["PeekMessageW"].Call(uintptr(unsafe.Pointer(m)), hwnd, uintptr(min), uintptr(max), 1) // PM_REMOVE
			return int32(r) != 0
		}
		a.dispatch = func(m *message) {
			proc["TranslateMessage"].Call(uintptr(unsafe.Pointer(m)))
			proc["DispatchMessageW"].Call(uintptr(unsafe.Pointer(m)))
		}
		a.postWake = func(hwnd uintptr) error {
			r, _, err := proc["PostMessageW"].Call(hwnd, wmWake, 0, 0)
			if int32(r) == 0 {
				return callError("PostMessageW", err)
			}
			return nil
		}
		a.setTitle = func(hwnd uintptr, title *uint16) error {
			r, _, err := proc["SetWindowTextW"].Call(hwnd, uintptr(unsafe.Pointer(title)))
			if int32(r) == 0 {
				return callError("SetWindowTextW", err)
			}
			return nil
		}
		a.getDPI = func(hwnd uintptr) (uint32, error) {
			r, _, err := proc["GetDpiForWindow"].Call(hwnd)
			if uint32(r) == 0 {
				return 0, callError("GetDpiForWindow", err)
			}
			return uint32(r), nil
		}
		a.adjustRect = func(r *rect, dpi uint32) error {
			ok, _, err := proc["AdjustWindowRectExForDpi"].Call(uintptr(unsafe.Pointer(r)), wsOverlappedWindow, 0, 0, uintptr(dpi))
			if int32(ok) == 0 {
				return callError("AdjustWindowRectExForDpi", err)
			}
			return nil
		}
		a.setPosition = func(hwnd uintptr, r rect, flags uint32) error {
			width, height := int64(r.right)-int64(r.left), int64(r.bottom)-int64(r.top)
			if width <= 0 || height <= 0 || width > math.MaxInt32 || height > math.MaxInt32 {
				return errors.New("Win32 outer size is outside int range")
			}
			ok, _, err := proc["SetWindowPos"].Call(hwnd, 0, uintptr(int64(r.left)), uintptr(int64(r.top)), uintptr(width), uintptr(height), uintptr(flags))
			if int32(ok) == 0 {
				return callError("SetWindowPos", err)
			}
			return nil
		}
		a.clientRect = func(hwnd uintptr, r *rect) error {
			ok, _, err := proc["GetClientRect"].Call(hwnd, uintptr(unsafe.Pointer(r)))
			if int32(ok) == 0 {
				return callError("GetClientRect", err)
			}
			return nil
		}
		a.show = func(hwnd uintptr) { proc["ShowWindow"].Call(hwnd, 5) } // SW_SHOW; return is previous visibility, not failure
		// One process-lifetime callback for every window and every creation.
		a.procedure = syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
			return windowMessage(a, hwnd, uintptr(msg), wp, lp)
		})
		nativeAPI = a
	})
	return nativeAPI, nativeError
}
