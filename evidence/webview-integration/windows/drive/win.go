//go:build windows

package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Process, module, socket and screen helpers over Win32, through syscall.

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	psapi    = syscall.NewLazyDLL("psapi.dll")

	pOpenProcess          = kernel32.NewProc("OpenProcess")
	pGetExitCodeProcess   = kernel32.NewProc("GetExitCodeProcess")
	pTerminateProcess     = kernel32.NewProc("TerminateProcess")
	pCloseHandle          = kernel32.NewProc("CloseHandle")
	pEnumProcessModulesEx = psapi.NewProc("EnumProcessModulesEx")
	pGetModuleFileNameExW = psapi.NewProc("GetModuleFileNameExW")
	pFindWindowW          = user32.NewProc("FindWindowW")
	pGetWindowRect        = user32.NewProc("GetWindowRect")
	pSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	pSetWindowPos         = user32.NewProc("SetWindowPos")
	pSendMessageTimeoutW  = user32.NewProc("SendMessageTimeoutW")
	pWindowFromPoint      = user32.NewProc("WindowFromPoint")
	pGetAncestor          = user32.NewProc("GetAncestor")
	pGetCursorPos         = user32.NewProc("GetCursorPos")
	pSetCursorPos         = user32.NewProc("SetCursorPos")
	pSendInput            = user32.NewProc("SendInput")
	pIsWindow             = user32.NewProc("IsWindow")
	pGetDpiForSystem      = user32.NewProc("GetDpiForSystem")
	pGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	pSetProcessDpiContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDC                = user32.NewProc("GetDC")
	pReleaseDC            = user32.NewProc("ReleaseDC")
	pCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBmp  = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject         = gdi32.NewProc("SelectObject")
	pBitBlt               = gdi32.NewProc("BitBlt")
	pGetDIBits            = gdi32.NewProc("GetDIBits")
	pDeleteObject         = gdi32.NewProc("DeleteObject")
	pDeleteDC             = gdi32.NewProc("DeleteDC")
)

const (
	processQueryLimited = 0x1000
	processTerminate    = 0x0001
	stillActive         = 259
	listModulesAll      = 0x03
	hwndTopmost         = ^uintptr(0) // -1
	hwndNotopmost       = ^uintptr(1) // -2
	swpNoSize           = 0x0001
	swpNoMove           = 0x0002
	swpNoActivate       = 0x0010
	wmNCHitTest         = 0x0084
	htCaption           = 2
	smtoAbortIfHung     = 0x0002
	gaRoot              = 2
	mouseLeftDown       = 0x0002
	mouseLeftUp         = 0x0004
)

type point struct{ x, y int32 }

// mouseInput is an INPUT holding a MOUSEINPUT on Windows x64: 40 bytes.
type mouseInput struct {
	kind, _                uint32
	dx, dy                 int32
	data, flags, timestamp uint32
	extra                  uintptr
}

func processAlive(pid uint32) bool {
	h, _, _ := pOpenProcess.Call(processQueryLimited, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer pCloseHandle.Call(h)
	var code uint32
	pGetExitCodeProcess.Call(h, uintptr(unsafe.Pointer(&code)))
	return code == stillActive
}

func killProcess(pid uint32) error {
	h, _, err := pOpenProcess.Call(processTerminate, 0, uintptr(pid))
	if h == 0 {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer pCloseHandle.Call(h)
	if ok, _, err := pTerminateProcess.Call(h, 1); ok == 0 {
		return fmt.Errorf("TerminateProcess(%d): %w", pid, err)
	}
	return nil
}

// modules lists the base names of the modules mapped in a process.
func modules(pid int) []string {
	h, _, _ := pOpenProcess.Call(processQueryLimited|0x0010, 0, uintptr(pid)) // PROCESS_VM_READ too
	if h == 0 {
		return nil
	}
	defer pCloseHandle.Call(h)
	mods := make([]uintptr, 1024)
	var needed uint32
	if ok, _, _ := pEnumProcessModulesEx.Call(h, uintptr(unsafe.Pointer(&mods[0])), uintptr(len(mods)*8), uintptr(unsafe.Pointer(&needed)), listModulesAll); ok == 0 {
		return nil
	}
	var out []string
	for _, m := range mods[:min(int(needed)/8, len(mods))] {
		buf := make([]uint16, 520)
		n, _, _ := pGetModuleFileNameExW.Call(h, m, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 {
			out = append(out, strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:n]))))
		}
	}
	sort.Strings(out)
	return out
}

func mapped(pid int, needle string) bool {
	for _, m := range modules(pid) {
		if strings.Contains(m, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// listeners lists the TCP listening addresses owned by pid, from netstat.
func listeners(pid int) []string {
	out, _ := exec.Command("netstat", "-ano", "-p", "TCP").Output()
	var addrs []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[0] == "TCP" && f[3] == "LISTENING" && f[4] == strconv.Itoa(pid) {
			addrs = append(addrs, f[1])
		}
	}
	return addrs
}

// tempEntries lists the framework-owned directories under dir.
func tempEntries(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "dfx2-webview2-") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func findWindow(title string) uintptr {
	t, _ := syscall.UTF16PtrFromString(title)
	h, _, _ := pFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	return h
}

// raise lifts the window over others without activating it, so a console
// cannot sit over the point the driver is about to press.
func raise(hwnd uintptr) {
	pSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	pSetWindowPos.Call(hwnd, hwndNotopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
}

func hitTest(hwnd uintptr, x, y int32) int {
	var result uintptr
	lp := uintptr(uint32(uint16(int16(x))) | uint32(uint16(int16(y)))<<16)
	r, _, _ := pSendMessageTimeoutW.Call(hwnd, wmNCHitTest, 0, lp, smtoAbortIfHung, 1000, uintptr(unsafe.Pointer(&result)))
	if r == 0 {
		return -1
	}
	return int(int32(result))
}

// captionPoint finds a point of the window's own caption with nothing else
// over it.
func captionPoint(hwnd uintptr) (point, error) {
	var r rect
	if ok, _, err := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return point{}, fmt.Errorf("GetWindowRect: %w", err)
	}
	for k := int32(1); k < 80; k++ {
		p := point{(r.left + r.right) / 2, r.top + k}
		if hitTest(hwnd, p.x, p.y) != htCaption {
			continue
		}
		under, _, _ := pWindowFromPoint.Call(uintptr(uint32(p.x)) | uintptr(uint32(p.y))<<32)
		root, _, _ := pGetAncestor.Call(under, gaRoot)
		if under != hwnd && root != hwnd {
			return point{}, fmt.Errorf("another window covers (%d,%d)", p.x, p.y)
		}
		return p, nil
	}
	return point{}, fmt.Errorf("no caption point found in %+v", r)
}

func button(flags uint32) error {
	input := mouseInput{flags: flags}
	n, _, err := pSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if n != 1 {
		return fmt.Errorf("SendInput: %w (a locked or disconnected session cannot inject input)", err)
	}
	return nil
}

func moveTo(x, y int32) error {
	if ok, _, err := pSetCursorPos.Call(uintptr(x), uintptr(y)); ok == 0 {
		return fmt.Errorf("SetCursorPos: %w", err)
	}
	return nil
}

// captionDrag presses the window's caption with the injected pointer, drags
// it right by about dx over the duration and releases, so Windows runs its
// size/move loop on the host's UI thread, as a title-bar drag would. SC_MOVE
// by message starts no loop unless the window's thread owns the foreground
// queue, which a driver started from a console cannot grant it; a pointer
// press on the caption needs only a connected, unlocked session. Whether the
// loop was established is read from the host's own counters, never assumed.
// The button is always released.
func captionDrag(hwnd uintptr, dx int32, duration time.Duration) error {
	raise(hwnd)
	time.Sleep(200 * time.Millisecond)
	start, err := captionPoint(hwnd)
	if err != nil {
		return err
	}
	var home point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&home)))
	defer moveTo(home.x, home.y)
	if err := moveTo(start.x, start.y); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	if err := button(mouseLeftDown); err != nil {
		return err
	}
	defer button(mouseLeftUp)
	began := time.Now()
	for {
		elapsed := time.Since(began)
		if elapsed >= duration {
			break
		}
		if ok, _, _ := pIsWindow.Call(hwnd); ok == 0 {
			break
		}
		f := float64(elapsed) / float64(duration)
		if err := moveTo(start.x+int32(f*float64(dx)), start.y+int32(20*f)); err != nil {
			return err
		}
		time.Sleep(16 * time.Millisecond)
	}
	return moveTo(start.x+dx, start.y+20)
}

type rect struct{ left, top, right, bottom int32 }

// capture copies the window's screen area: what reached the display. A
// disconnected or locked session captures black or fails; the caller records
// the session state beside the file. A copy that fails after reaching the
// display is retried twice; the last error is reported.
func capture(hwnd uintptr, path string) error {
	if hwnd == 0 {
		return fmt.Errorf("no window")
	}
	pSetForegroundWindow.Call(hwnd)
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		if err = captureOnce(hwnd, path); err == nil {
			return nil
		}
	}
	return err
}

func captureOnce(hwnd uintptr, path string) error {
	time.Sleep(400 * time.Millisecond)
	var wr rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	w, h := int(wr.right-wr.left), int(wr.bottom-wr.top)
	if w <= 0 || h <= 0 {
		return fmt.Errorf("empty window rectangle")
	}
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bmp, _, _ := pCreateCompatibleBmp.Call(screen, uintptr(w), uintptr(h))
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(mem, bmp)
	if ok, _, err := pBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), screen, uintptr(wr.left), uintptr(wr.top), 0x00CC0020|0x40000000); ok == 0 {
		return fmt.Errorf("BitBlt: %w", err)
	}
	pSelectObject.Call(mem, old)
	type bmi struct {
		size                   uint32
		width, height          int32
		planes, bitCount       uint16
		compression, sizeImage uint32
		x, y                   int32
		used, important        uint32
		colors                 [4]uint32
	}
	info := bmi{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	info.size = 40
	px := make([]byte, w*h*4)
	if n, _, err := pGetDIBits.Call(mem, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&px[0])), uintptr(unsafe.Pointer(&info)), 0); n == 0 {
		return fmt.Errorf("GetDIBits set no lines (%dx%d at %d,%d): %w", w, h, wr.left, wr.top, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	black := true
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = px[i*4+2], px[i*4+1], px[i*4+0], 255
		black = black && px[i*4] == 0 && px[i*4+1] == 0 && px[i*4+2] == 0
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	png.Encode(f, img)
	f.Close()
	if black {
		return fmt.Errorf("capture is entirely black (session disconnected or locked?)")
	}
	return nil
}

// sessionState reports this session's state as query session prints it.
func sessionState() string {
	out, _ := exec.Command("query", "session").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, ">") {
			f := strings.Fields(line)
			if len(f) >= 3 {
				return f[len(f)-1]
			}
		}
	}
	return "unknown"
}

func output(name string, args ...string) string {
	b, _ := exec.Command(name, args...).CombinedOutput()
	return string(b)
}

// modulePaths lists the full paths of the modules mapped in a process.
func modulePaths(pid int) []string {
	h, _, _ := pOpenProcess.Call(processQueryLimited|0x0010, 0, uintptr(pid))
	if h == 0 {
		return nil
	}
	defer pCloseHandle.Call(h)
	mods := make([]uintptr, 1024)
	var needed uint32
	if ok, _, _ := pEnumProcessModulesEx.Call(h, uintptr(unsafe.Pointer(&mods[0])), uintptr(len(mods)*8), uintptr(unsafe.Pointer(&needed)), listModulesAll); ok == 0 {
		return nil
	}
	var out []string
	for _, m := range mods[:min(int(needed)/8, len(mods))] {
		buf := make([]uint16, 520)
		if n, _, _ := pGetModuleFileNameExW.Call(h, m, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); n > 0 {
			out = append(out, syscall.UTF16ToString(buf[:n]))
		}
	}
	return out
}

func uintptrOf(r *rect) uintptr { return uintptr(unsafe.Pointer(r)) }
