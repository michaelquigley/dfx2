//go:build windows && amd64

package main

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// Win32, ole32 and shlwapi through syscall: no cgo, no headers. This
// experiment carries its own copy rather than sharing the text spike's.
var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	shlwapi  = syscall.NewLazyDLL("shlwapi.dll")

	pRegisterClassExW                    = user32.NewProc("RegisterClassExW")
	pUnregisterClassW                    = user32.NewProc("UnregisterClassW")
	pCreateWindowExW                     = user32.NewProc("CreateWindowExW")
	pDestroyWindow                       = user32.NewProc("DestroyWindow")
	pDefWindowProcW                      = user32.NewProc("DefWindowProcW")
	pGetMessageW                         = user32.NewProc("GetMessageW")
	pTranslateMessage                    = user32.NewProc("TranslateMessage")
	pDispatchMessageW                    = user32.NewProc("DispatchMessageW")
	pPostQuitMessage                     = user32.NewProc("PostQuitMessage")
	pPostMessageW                        = user32.NewProc("PostMessageW")
	pGetClientRect                       = user32.NewProc("GetClientRect")
	pSetWindowPos                        = user32.NewProc("SetWindowPos")
	pShowWindow                          = user32.NewProc("ShowWindow")
	pLoadCursorW                         = user32.NewProc("LoadCursorW")
	pSetWindowLongPtrW                   = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtrW                   = user32.NewProc("GetWindowLongPtrW")
	pSetWindowTextW                      = user32.NewProc("SetWindowTextW")
	pSetTimer                            = user32.NewProc("SetTimer")
	pKillTimer                           = user32.NewProc("KillTimer")
	pSetProcessDpiAwarenessContext       = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetThreadDpiAwarenessContext        = user32.NewProc("GetThreadDpiAwarenessContext")
	pGetAwarenessFromDpiAwarenessContext = user32.NewProc("GetAwarenessFromDpiAwarenessContext")
	pGetDpiForWindow                     = user32.NewProc("GetDpiForWindow")
	pAdjustWindowRectExForDpi            = user32.NewProc("AdjustWindowRectExForDpi")
	pGetModuleHandleW                    = kernel32.NewProc("GetModuleHandleW")
	pRtlMoveMemory                       = kernel32.NewProc("RtlMoveMemory")
	pGetCurrentThreadID                  = kernel32.NewProc("GetCurrentThreadId")
	pCoInitializeEx                      = ole32.NewProc("CoInitializeEx")
	pCoUninitialize                      = ole32.NewProc("CoUninitialize")
	pCoTaskMemAlloc                      = ole32.NewProc("CoTaskMemAlloc")
	pCoTaskMemFree                       = ole32.NewProc("CoTaskMemFree")
	pSHCreateMemStream                   = shlwapi.NewProc("SHCreateMemStream")

	allProcs = []*syscall.LazyProc{
		pRegisterClassExW, pUnregisterClassW, pCreateWindowExW, pDestroyWindow, pDefWindowProcW,
		pGetMessageW, pTranslateMessage, pDispatchMessageW, pPostQuitMessage, pPostMessageW,
		pGetClientRect, pSetWindowPos, pShowWindow, pLoadCursorW, pSetWindowLongPtrW, pGetWindowLongPtrW,
		pSetWindowTextW, pSetTimer, pKillTimer, pSetProcessDpiAwarenessContext, pGetThreadDpiAwarenessContext,
		pGetAwarenessFromDpiAwarenessContext, pGetDpiForWindow, pAdjustWindowRectExForDpi,
		pGetModuleHandleW, pRtlMoveMemory, pGetCurrentThreadID, pCoInitializeEx, pCoUninitialize,
		pCoTaskMemAlloc, pCoTaskMemFree, pSHCreateMemStream,
	}
)

const (
	wmNCCreate   = 0x0081
	wmDestroy    = 0x0002
	wmMove       = 0x0003
	wmSize       = 0x0005
	wmSetFocus   = 0x0007
	wmClose      = 0x0010
	wmTimer      = 0x0113
	wmMoving     = 0x0216
	wmDPIChanged = 0x02E0
	wmApp        = 0x8000
	// wmDispatch says handler results are ready (Dispatcher.Take). Its
	// wParam and lParam are zero: results are taken, never passed.
	wmDispatch = wmApp + 1
	// wmServerFailed says the loopback server stopped on its own.
	wmServerFailed = wmApp + 2

	sizeMinimized = 1

	wsOverlappedWindow = 0x00CF0000
	cwUseDefault       = 0x80000000
	swShow             = 5
	swHide             = 0
	swpNoMove          = 0x0002
	swpNoZOrder        = 0x0004
	swpNoActivate      = 0x0010
	idcArrow           = 32512
	colorWindow        = 5
	dpiAwarenessPerMon = 2
	errorAccessDenied  = syscall.Errno(5)

	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4

	timerEnvWait = 1

	sOK          = 0
	sFalse       = 1
	eNoInterface = 0x80004002
	eFail        = 0x80004005
	ePointer     = 0x80004003
)

var (
	gwlpUserData   = ^uintptr(20) // GWLP_USERDATA, -21
	dpiContextPMv2 = ^uintptr(3)  // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, -4
)

type wndClassEx struct {
	size, style                        uint32
	wndProc                            uintptr
	clsExtra, wndExtra                 int32
	instance, icon, cursor, background uintptr
	menuName, className                *uint16
	iconSm                             uintptr
}

type point struct{ x, y int32 }

type winMsg struct {
	hwnd           uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	pt             point
	private        uint32
}

type rect struct{ left, top, right, bottom int32 }

// hresult is an HRESULT, printed as Windows tools print it.
type hresult uint32

func (h hresult) failed() bool { return int32(h) < 0 }
func (h hresult) String() string {
	switch h {
	case sOK:
		return "S_OK"
	case 0x80004004:
		return "E_ABORT (0x80004004)"
	case 0x80070002:
		return "ERROR_FILE_NOT_FOUND (0x80070002)"
	case 0x8007139F:
		return "ERROR_INVALID_STATE (0x8007139F)"
	}
	return fmt.Sprintf("0x%08X", uint32(h))
}
func (h hresult) err(what string) error {
	if !h.failed() {
		return nil
	}
	return fmt.Errorf("%s: HRESULT %v", what, h)
}

// perMonitorDPI makes the process per-monitor DPI aware (v2) before any
// window exists, as the text spike does. WebView2 then follows the
// monitor's scale itself; the host only keeps the bounds in pixels.
func perMonitorDPI() error {
	ok, _, err := pSetProcessDpiAwarenessContext.Call(dpiContextPMv2)
	if ok != 0 {
		return nil
	}
	if errors.Is(err, errorAccessDenied) {
		ctx, _, _ := pGetThreadDpiAwarenessContext.Call()
		if aw, _, _ := pGetAwarenessFromDpiAwarenessContext.Call(ctx); aw == dpiAwarenessPerMon {
			return nil
		}
	}
	return fmt.Errorf("SetProcessDpiAwarenessContext(per-monitor v2): %w", err)
}

// utf16 converts s for a call. It panics on NUL, which never appears in
// the strings this host builds.
func utf16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

// takeString copies a callee-allocated LPWSTR and frees it with
// CoTaskMemFree, as the WebView2 API conventions require.
func takeString(p *uint16) string {
	if p == nil {
		return ""
	}
	var n int
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	s := syscall.UTF16ToString(unsafe.Slice(p, n))
	pCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}

func currentThread() uint32 {
	id, _, _ := pGetCurrentThreadID.Call()
	return uint32(id)
}
