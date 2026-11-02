//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// A window's messages are handled on the thread that created it: keep the
// main goroutine on the process's first thread, where the loop runs.
func init() { runtime.LockOSThread() }

// Win32, called through syscall: no cgo, no headers.
var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

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
	pBeginPaint                          = user32.NewProc("BeginPaint")
	pEndPaint                            = user32.NewProc("EndPaint")
	pInvalidateRect                      = user32.NewProc("InvalidateRect")
	pGetClientRect                       = user32.NewProc("GetClientRect")
	pSetWindowPos                        = user32.NewProc("SetWindowPos")
	pShowWindow                          = user32.NewProc("ShowWindow")
	pLoadCursorW                         = user32.NewProc("LoadCursorW")
	pSetWindowLongPtrW                   = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtrW                   = user32.NewProc("GetWindowLongPtrW")
	pSetWindowTextW                      = user32.NewProc("SetWindowTextW")
	pSetProcessDpiAwarenessContext       = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetThreadDpiAwarenessContext        = user32.NewProc("GetThreadDpiAwarenessContext")
	pGetAwarenessFromDpiAwarenessContext = user32.NewProc("GetAwarenessFromDpiAwarenessContext")
	pGetDpiForWindow                     = user32.NewProc("GetDpiForWindow")
	pAdjustWindowRectExForDpi            = user32.NewProc("AdjustWindowRectExForDpi")
	pSetDIBitsToDevice                   = gdi32.NewProc("SetDIBitsToDevice")
	pGetModuleHandleW                    = kernel32.NewProc("GetModuleHandleW")
	pRtlMoveMemory                       = kernel32.NewProc("RtlMoveMemory")
	pVirtualAlloc                        = kernel32.NewProc("VirtualAlloc")
	pVirtualFree                         = kernel32.NewProc("VirtualFree")

	allProcs = []*syscall.LazyProc{
		pRegisterClassExW, pUnregisterClassW, pCreateWindowExW, pDestroyWindow, pDefWindowProcW,
		pGetMessageW, pTranslateMessage, pDispatchMessageW, pPostQuitMessage, pPostMessageW,
		pBeginPaint, pEndPaint, pInvalidateRect, pGetClientRect, pSetWindowPos, pShowWindow,
		pLoadCursorW, pSetWindowLongPtrW, pGetWindowLongPtrW, pSetWindowTextW,
		pSetProcessDpiAwarenessContext, pGetThreadDpiAwarenessContext, pGetAwarenessFromDpiAwarenessContext,
		pGetDpiForWindow, pAdjustWindowRectExForDpi, pSetDIBitsToDevice, pGetModuleHandleW, pRtlMoveMemory, pVirtualAlloc, pVirtualFree,
	}
)

const (
	wmNCCreate    = 0x0081
	wmDestroy     = 0x0002
	wmPaint       = 0x000F
	wmClose       = 0x0010
	wmEraseBkgnd  = 0x0014
	wmChar        = 0x0102
	wmLButtonDown = 0x0201
	wmDPIChanged  = 0x02E0
	wmApp         = 0x8000
	// wmMeter is the meter worker's wakeup. Its wParam and lParam are zero:
	// the state it announces is read from the feed, never passed.
	wmMeter = wmApp + 1

	csVRedraw          = 0x0001
	csHRedraw          = 0x0002
	wsOverlappedWindow = 0x00CF0000
	cwUseDefault       = 0x80000000
	swShow             = 5
	swpNoMove          = 0x0002
	swpNoZOrder        = 0x0004
	swpNoActivate      = 0x0010
	idcArrow           = 32512
	dibRGBColors       = 0
	biRGB              = 0
	dpiAwarenessPerMon = 2 // DPI_AWARENESS_PER_MONITOR_AWARE (v1 or v2)
	errorAccessDenied  = syscall.Errno(5)
	memCommit          = 0x1000
	memReserve         = 0x2000
	memRelease         = 0x8000
	pageReadWrite      = 0x04
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

type paintStruct struct {
	hdc             uintptr
	erase           int32
	paint           rect
	restore, incUpd int32
	reserved        [32]byte
}

// bitmapInfo is a BITMAPINFO with a BITMAPINFOHEADER and the one (unused)
// color-table entry its declaration carries.
type bitmapInfo struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPPM, yPPM             int32
	clrUsed, clrImportant  uint32
	colors                 [1]uint32
}

// window is one native window's state, owned by the UI thread. Windows find
// theirs through an id kept in GWLP_USERDATA, so the one window procedure
// serves every window and no Go pointer is handed to Windows to keep.
type window struct {
	id, hwnd     uintptr
	a            *app
	feed         *meterFeed // the -meter worker, or nil
	title        string
	base         float32 // a.size at launch, for 0
	dpi          uint32
	scale        float32 // the scale a.frame was rendered at
	dirty        bool    // the picture changed since a.frame was rendered
	destroyed    bool
	presentFails int     // SetDIBitsToDevice calls that reported no lines set
	native       uintptr // the frame's pixels, from VirtualAlloc (see pixels)
	nativeCap    int
}

var (
	windows   = map[uintptr]*window{} // by id; UI thread only
	nextID    uintptr
	wndProcCB uintptr // the window procedure, made once: callbacks are never freed
)

func runWindow(a *app, title string, wDIP, hDIP int) error {
	for _, p := range allProcs {
		if err := p.Find(); err != nil {
			return fmt.Errorf("win32: %w", err)
		}
	}
	if err := perMonitorDPI(); err != nil {
		return err
	}
	inst, _, err := pGetModuleHandleW.Call(0)
	if inst == 0 {
		return fmt.Errorf("GetModuleHandleW: %w", err)
	}
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	className, _ := syscall.UTF16PtrFromString("dfx2TextSpike")
	if wndProcCB == 0 {
		wndProcCB = syscall.NewCallback(wndProc)
	}
	wc := wndClassEx{
		style:     csHRedraw | csVRedraw,
		wndProc:   wndProcCB,
		instance:  inst,
		cursor:    cursor,
		className: className,
	}
	wc.size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassExW: %w", err)
	}
	defer pUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), inst)

	nextID++
	w := &window{id: nextID, a: a, title: title, base: a.size, dirty: true}
	windows[w.id] = w
	defer delete(windows, w.id)

	titlePtr, _ := syscall.UTF16PtrFromString(title)
	hwnd, _, err := pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(titlePtr)), wsOverlappedWindow,
		cwUseDefault, cwUseDefault, cwUseDefault, cwUseDefault,
		0, 0, inst, w.id)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %w", err)
	}
	// From here, wndProc knows the window (WM_NCCREATE set w.hwnd).

	// Size the client area in DIPs at the DPI of the monitor it opened on.
	d, _, _ := pGetDpiForWindow.Call(hwnd)
	w.dpi = uint32(d)
	r := rect{right: int32(muldiv(wDIP, w.dpi)), bottom: int32(muldiv(hDIP, w.dpi))}
	if ok, _, err := pAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), wsOverlappedWindow, 0, 0, uintptr(w.dpi)); ok == 0 {
		pDestroyWindow.Call(hwnd)
		return fmt.Errorf("AdjustWindowRectExForDpi: %w", err)
	}
	pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpNoMove|swpNoZOrder|swpNoActivate)
	w.setTitle()
	log.Printf("window: %d dpi (scale %g), client %dx%d DIPs", w.dpi, float32(w.dpi)/96, wDIP, hDIP)

	a.perf.mark = time.Now()
	if a.meter != nil {
		w.feed = newMeterFeed(meterPeriod, func() bool {
			ok, _, _ := pPostMessageW.Call(hwnd, wmMeter, 0, 0)
			return ok != 0
		})
		w.feed.start(true)
	}
	pShowWindow.Call(hwnd, swShow)

	var loopErr error
	var m winMsg
	for {
		r, _, err := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			loopErr = fmt.Errorf("GetMessageW: %w", err)
			break
		}
		if r == 0 { // WM_QUIT
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	if !w.destroyed {
		w.close()
	}
	w.stopFeed()
	a.perf.summary("exit", a, w.feed)
	if w.presentFails > 0 {
		log.Printf("SetDIBitsToDevice reported no lines set %d times", w.presentFails)
	}
	w.freePixels()
	return loopErr
}

// perMonitorDPI makes the process per-monitor DPI aware (v2), before any
// window exists, so Windows never bitmap-stretches what it draws.
func perMonitorDPI() error {
	ok, _, err := pSetProcessDpiAwarenessContext.Call(dpiContextPMv2)
	if ok != 0 {
		return nil
	}
	if errors.Is(err, errorAccessDenied) {
		// Already set, by a manifest or an earlier call: fine if per-monitor.
		ctx, _, _ := pGetThreadDpiAwarenessContext.Call()
		if aw, _, _ := pGetAwarenessFromDpiAwarenessContext.Call(ctx); aw == dpiAwarenessPerMon {
			return nil
		}
	}
	return fmt.Errorf("SetProcessDpiAwarenessContext(per-monitor v2): %w", err)
}

func muldiv(dip int, dpi uint32) int { return int(math.Round(float64(dip) * float64(dpi) / 96)) }

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	var w *window
	if msg == wmNCCreate {
		// CREATESTRUCTW starts with lpCreateParams: the id CreateWindowExW got.
		var id uintptr
		pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&id)), lp, unsafe.Sizeof(id))
		pSetWindowLongPtrW.Call(hwnd, gwlpUserData, id)
		if w = windows[id]; w != nil {
			w.hwnd = hwnd
		}
	} else {
		id, _, _ := pGetWindowLongPtrW.Call(hwnd, gwlpUserData)
		w = windows[id]
	}
	if w != nil {
		if r, ok := w.handle(msg, wp, lp); ok {
			return r
		}
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (w *window) handle(msg, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case wmEraseBkgnd:
		return 1, true // the paint covers every pixel: no flash of background
	case wmPaint:
		w.paint()
		return 0, true
	case wmDPIChanged:
		w.dpi = uint32(wp & 0xffff)
		var r rect
		pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&r)), lp, unsafe.Sizeof(r))
		pSetWindowPos.Call(w.hwnd, 0, uintptr(r.left), uintptr(r.top), uintptr(r.right-r.left), uintptr(r.bottom-r.top), swpNoZOrder|swpNoActivate)
		log.Printf("window: DPI changed to %d (scale %g), suggested %dx%d at (%d,%d)",
			w.dpi, float32(w.dpi)/96, r.right-r.left, r.bottom-r.top, r.left, r.top)
		w.setTitle()
		w.redraw()
		return 0, true
	case wmChar:
		w.key(rune(wp))
		return 0, true
	case wmLButtonDown:
		x, y := int(int16(lp&0xffff)), int(int16(lp>>16&0xffff))
		if m := w.a.meter; m != nil && x >= m.button.Min.X && x < m.button.Max.X && y >= m.button.Min.Y && y < m.button.Max.Y {
			w.togglePause()
		}
		return 0, true
	case wmMeter:
		if w.feed != nil {
			m := w.a.meter
			m.level, m.tick, _ = w.feed.take()
			w.a.perf.wakes++
			w.redraw()
		}
		return 0, true
	case wmClose:
		w.close()
		return 0, true
	case wmDestroy:
		w.stopFeed() // already stopped by close; this covers any other path
		w.destroyed = true
		pPostQuitMessage.Call(0)
		return 0, true
	}
	return 0, false
}

func (w *window) key(c rune) {
	a := w.a
	switch c {
	case '+', '=':
		a.size = min(a.size*1.15, 400)
	case '-':
		a.size = max(a.size/1.15, 4)
	case '0':
		a.size = w.base
	case 'l', 'L':
		a.setLight(a.bg != light)
	case ' ', 'p', 'P':
		if a.meter == nil {
			return
		}
		w.togglePause()
		return
	case 's', 'S':
		a.perf.summary("s", a, w.feed)
		return
	case 0x1b, 'q', 'Q':
		w.close()
		return
	default:
		return
	}
	w.redraw()
}

func (w *window) togglePause() {
	m := w.a.meter
	m.paused = !m.paused
	w.feed.setRunning(!m.paused)
	log.Printf("meter: paused %v at tick %d", m.paused, m.tick)
	w.redraw()
}

// redraw marks the picture changed and asks for a paint. Windows merges
// invalidations, so many before the next WM_PAINT cost one paint.
func (w *window) redraw() {
	w.dirty = true
	pInvalidateRect.Call(w.hwnd, 0, 0)
}

// close stops the worker, so nothing is posted to the window once it is
// gone, then destroys the window, which ends the loop.
func (w *window) close() {
	w.stopFeed()
	if !w.destroyed {
		pDestroyWindow.Call(w.hwnd)
	}
}

func (w *window) stopFeed() {
	if w.feed != nil {
		w.feed.stop()
	}
}

func (w *window) setTitle() {
	t := fmt.Sprintf("%s — %d dpi (%d%%)", w.title, w.dpi, muldiv(100, w.dpi))
	p, _ := syscall.UTF16PtrFromString(t)
	pSetWindowTextW.Call(w.hwnd, uintptr(unsafe.Pointer(p)))
}

// pixels makes sure the frame's pixels can hold n bytes in memory from one
// VirtualAlloc, which the frame's Resize then reuses.
//
// The frame must not live in the Go heap. On Windows, Go reserves its heap
// in 4 MB arenas, each its own VirtualAlloc, and SetDIBitsToDevice sets no
// lines (and reports no error) for a frame of a megabyte or two that
// straddles two of them: observed on this host as a black or stale window at
// exactly the sizes whose buffer crossed a 4 MB boundary, every time, and
// never otherwise.
func (w *window) pixels(n int) error {
	if n <= w.nativeCap {
		return nil
	}
	w.freePixels()
	size := (n + n/4 + 0xffff) &^ 0xffff // headroom, so growing by dragging rarely reallocates
	p, _, err := pVirtualAlloc.Call(0, uintptr(size), memCommit|memReserve, pageReadWrite)
	if p == 0 {
		return fmt.Errorf("VirtualAlloc(%d): %w", size, err)
	}
	w.native, w.nativeCap = p, size
	// Memory outside the Go heap, freed only by freePixels.
	w.a.frame.Pix = unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), p)), size)[:0]
	w.a.frame.W, w.a.frame.H = 0, 0 // its contents are gone: render again
	return nil
}

// freePixels releases the frame's native memory. The frame must not be
// drawn or presented after it until pixels runs again.
func (w *window) freePixels() {
	if w.native == 0 {
		return
	}
	w.a.frame.Pix, w.a.frame.W, w.a.frame.H = nil, 0, 0
	pVirtualFree.Call(w.native, 0, memRelease)
	w.native, w.nativeCap = 0, 0
}

// paint renders the frame if the picture or the client's size or scale
// changed, and presents it. Exposure alone re-presents the last frame.
func (w *window) paint() {
	a := w.a
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer pEndPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))
	a.perf.paints++

	var rc rect
	pGetClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&rc)))
	cw, ch := int(rc.right-rc.left), int(rc.bottom-rc.top)
	if cw <= 0 || ch <= 0 {
		return // minimized, or sized to nothing: nothing to draw into
	}
	if err := w.pixels(cw * ch * 4); err != nil {
		log.Print(err)
		return
	}
	scale := float32(w.dpi) / 96
	if w.dirty || cw != a.frame.W || ch != a.frame.H || scale != w.scale {
		t := time.Now()
		a.render(cw, ch, scale)
		a.perf.rendered(time.Since(t))
		w.dirty, w.scale = false, scale
	}

	bmi := bitmapInfo{width: int32(cw), height: -int32(ch), planes: 1, bitCount: 32, compression: biRGB}
	bmi.size = uint32(unsafe.Offsetof(bmi.colors))
	n, _, err := pSetDIBitsToDevice.Call(hdc, 0, 0, uintptr(cw), uintptr(ch), 0, 0, 0, uintptr(ch),
		uintptr(unsafe.Pointer(&a.frame.Pix[0])), uintptr(unsafe.Pointer(&bmi)), dibRGBColors)
	runtime.KeepAlive(a.frame.Pix)
	if n == 0 {
		w.presentFails++
		if w.presentFails <= 5 {
			log.Printf("SetDIBitsToDevice set no lines (%v): client %dx%d, frame at %#x", err, cw, ch, uintptr(unsafe.Pointer(&a.frame.Pix[0])))
		}
	}

	// With the meter, summarize every 5 s of painting. This adds no wakeups:
	// a window at rest prints nothing until something paints it.
	if a.meter != nil && time.Since(a.perf.mark) >= 5*time.Second {
		a.perf.summary("5s", a, w.feed)
	}
}
