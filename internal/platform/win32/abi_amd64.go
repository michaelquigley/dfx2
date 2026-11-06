// Package win32 implements the private Windows/amd64 native backend.
// Its dispatch mechanics and fixed Windows ABI types can be tested with
// injected calls on another amd64 host; that is not Windows execution.
package win32

import "unsafe"

const (
	wmCreate                      = 0x0001
	wmDestroy                     = 0x0002
	wmSize                        = 0x0005
	wmPaint                       = 0x000f
	wmClose                       = 0x0010
	wmQuit                        = 0x0012
	wmEraseBkgnd                  = 0x0014
	wmShowWindow                  = 0x0018
	wmNCCreate                    = 0x0081
	wmNCDestroy                   = 0x0082
	wmTimer                       = 0x0113
	wmEnterSizeMove               = 0x0231
	wmExitSizeMove                = 0x0232
	wmDPIChanged                  = 0x02e0
	wmWake                        = 0x8001
	wmKeyFirst, wmKeyLast         = 0x0100, 0x0109
	wmMouseFirst, wmMouseLast     = 0x0200, 0x020e
	wmNCMouseFirst, wmNCMouseLast = 0x00a0, 0x00ad
	wsOverlappedWindow            = 0x00cf0000
	swpNoMove                     = 0x0002
	swpNoZOrder                   = 0x0004
	swpNoActivate                 = 0x0010
	sizeMinimized                 = 1
	swShow                        = 5
	swMinimize                    = 6
	swRestore                     = 9
	biRGB                         = 0
	windowClass                   = "dfx2FoundationRuntime"
)

const hwndMessage = ^uintptr(2)    // HWND_MESSAGE, -3
const userDataIndex = ^uintptr(20) // GWLP_USERDATA, -21

// Windows x64 uses LLP64: LONG/DWORD/BOOL remain 32 bits; handles and pointers
// are 64 bits. These declarations follow the WinUser.h SDK definitions.
type classEx struct {
	size, style                        uint32
	procedure                          uintptr
	classExtra, windowExtra            int32
	instance, icon, cursor, background uintptr
	menuName, className                *uint16
	smallIcon                          uintptr
}
type point struct{ x, y int32 }
type rect struct{ left, top, right, bottom int32 }
type message struct {
	hwnd           uintptr
	id             uint32
	wparam, lparam uintptr
	time           uint32
	position       point
	private        uint32
}
type createStruct struct {
	params, instance, menu, parent uintptr
	height, width, y, x, style     int32
	name, class                    uintptr
	extendedStyle                  uint32
}
type paintStruct struct {
	hdc             uintptr
	erase           int32
	paint           rect
	restore, incUpd int32
	reserved        [32]byte
}

// bitmapInfo is a BITMAPINFO: a BITMAPINFOHEADER and the one color-table entry
// its declaration carries, unused for BI_RGB at 32 bits per pixel.
type bitmapInfo struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPPM, yPPM             int32
	clrUsed, clrImportant  uint32
	colors                 [1]uint32
}

// Pointer-bearing calls stay typed through this seam. The Windows bridge
// converts to uintptr directly in syscall.Proc.Call, which marks escapes;
// no unannotated variadic uintptr helper can hide a movable Go stack pointer.
type native struct {
	procedure   uintptr
	threadID    func() uint32
	perMonitor  func() error
	module      func() (uintptr, error)
	cursor      func() (uintptr, error)
	register    func(*classEx) error
	unregister  func(*uint16, uintptr) error
	create      func(class, title *uint16, style uint32, parent, instance, token uintptr) (uintptr, error)
	destroy     func(uintptr) error
	defWindow   func(hwnd, msg, wp, lp uintptr) uintptr
	getUserData func(uintptr) uintptr
	setUserData func(uintptr, uintptr) error
	copyMemory  func(unsafe.Pointer, uintptr, uintptr)
	getMessage  func(*message) (int32, error)
	peekMessage func(*message, uintptr, uint32, uint32) bool
	dispatch    func(*message)
	postWake    func(uintptr) error
	setTitle    func(uintptr, *uint16) error
	getDPI      func(uintptr) (uint32, error)
	adjustRect  func(*rect, uint32) error
	setPosition func(uintptr, rect, uint32) error
	clientRect  func(uintptr, *rect) error
	show        func(hwnd uintptr, command int32)
	// Native presentation. allocate returns storage from one native allocation,
	// valid until free; it is never Go heap memory on the real bridge.
	allocate   func(size int) ([]byte, error)
	free       func([]byte) error
	invalidate func(hwnd uintptr) error
	beginPaint func(uintptr, *paintStruct) (uintptr, error)
	endPaint   func(uintptr, *paintStruct)
	setDIBits  func(hdc uintptr, width, height int32, bits *byte, info *bitmapInfo) (int32, error)
	fillBlack  func(hdc uintptr, r *rect) error
	setTimer   func(hwnd, id uintptr, milliseconds uint32) error
	killTimer  func(hwnd, id uintptr)
}
