//go:build windows

// Command desk runs the mixer fixture and acts as its desktop: it answers the
// fixture's drag-request events with real synthesized mouse input, so Windows
// runs its own DefWindowProc size/move loop, and its capture-request events
// with screen captures of the client area. It relays the fixture's stdout,
// adds driver-* events and exits with the fixture's exit code, or 3 when the
// driver itself failed.
//
// A separate module, like the experiments, so the root module never builds
// it. It needs an interactive desktop: a locked session, a disconnected or
// minimized Remote Desktop client, or a target window behind an elevated one
// reports a failure here rather than a result.
//
// desk -release sends one left-button-up, for a run killed mid-drag.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pEnumWindows                   = user32.NewProc("EnumWindows")
	pGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")
	pGetWindowTextW                = user32.NewProc("GetWindowTextW")
	pIsWindow                      = user32.NewProc("IsWindow")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pGetWindowRect                 = user32.NewProc("GetWindowRect")
	pGetClientRect                 = user32.NewProc("GetClientRect")
	pClientToScreen                = user32.NewProc("ClientToScreen")
	pSendMessageTimeoutW           = user32.NewProc("SendMessageTimeoutW")
	pWindowFromPoint               = user32.NewProc("WindowFromPoint")
	pGetAncestor                   = user32.NewProc("GetAncestor")
	pGetCursorPos                  = user32.NewProc("GetCursorPos")
	pSetCursorPos                  = user32.NewProc("SetCursorPos")
	pSendInput                     = user32.NewProc("SendInput")
	pSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	pGetDC                         = user32.NewProc("GetDC")
	pReleaseDC                     = user32.NewProc("ReleaseDC")
	pCreateCompatibleDC            = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap        = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject                  = gdi32.NewProc("SelectObject")
	pBitBlt                        = gdi32.NewProc("BitBlt")
	pGetDIBits                     = gdi32.NewProc("GetDIBits")
	pDeleteObject                  = gdi32.NewProc("DeleteObject")
	pDeleteDC                      = gdi32.NewProc("DeleteDC")
)

const (
	hwndTopmost, hwndNotopmost = ^uintptr(0), ^uintptr(1) // -1, -2
	swpNoSize                  = 0x0001
	swpNoMove                  = 0x0002
	swpNoActivate              = 0x0010
	wmNCHitTest                = 0x0084
	htCaption, htBottomRight   = 2, 17
	smtoAbortIfHung            = 0x0002
	gaRoot                     = 2
	inputMouse                 = 0
	mouseLeftDown              = 0x0002
	mouseLeftUp                = 0x0004
	spiGetDragFullWindows      = 0x0026
	srcCopy                    = 0x00cc0020
)

type rect struct{ left, top, right, bottom int32 }
type point struct{ x, y int32 }

// mouseInput is an INPUT holding a MOUSEINPUT on Windows x64: 40 bytes.
type mouseInput struct {
	kind, _                uint32
	dx, dy                 int32
	data, flags, timestamp uint32
	extra                  uintptr
}

type bitmapInfo struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPPM, yPPM             int32
	clrUsed, clrImportant  uint32
	colors                 [1]uint32
}

type driver struct {
	pid    uint32
	outDir string
	mu     sync.Mutex
	out    *bufio.Writer
	failed bool
}

func (d *driver) emit(event string, fields map[string]any) {
	line := map[string]any{"event": event, "time": time.Now().Format(time.RFC3339Nano)}
	for k, v := range fields {
		line[k] = v
	}
	b, _ := json.Marshal(line)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.out.Write(b)
	d.out.WriteByte('\n')
	d.out.Flush()
}

func (d *driver) fail(what string, err error) {
	d.mu.Lock()
	d.failed = true
	d.mu.Unlock()
	d.emit("driver-failed", map[string]any{"step": what, "error": err.Error()})
}

func main() { os.Exit(run()) }

func run() int {
	fixture := flag.String("fixture", "", "path to the mixer fixture executable")
	outDir := flag.String("out", ".", "directory for screen captures")
	release := flag.Bool("release", false, "send one left-button-up and exit")
	flag.Parse()
	if *release {
		button(mouseLeftUp)
		return 0
	}
	if *fixture == "" {
		fmt.Fprintln(os.Stderr, "desk: -fixture is required")
		return 2
	}
	// Physical coordinates, matching the per-monitor-aware fixture.
	pSetProcessDpiAwarenessContext.Call(^uintptr(3))

	cmd := exec.Command(*fixture, flag.Args()...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "desk:", err)
		return 2
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "desk:", err)
		return 2
	}
	d := &driver{pid: uint32(cmd.Process.Pid), outDir: *outDir, out: bufio.NewWriter(os.Stdout)}
	var full int32
	pSystemParametersInfoW.Call(spiGetDragFullWindows, 0, uintptr(unsafe.Pointer(&full)), 0)
	d.emit("driver-host", map[string]any{"show_contents_while_dragging": full != 0})

	var work sync.WaitGroup
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		d.mu.Lock()
		d.out.Write(line)
		d.out.WriteByte('\n')
		d.out.Flush()
		d.mu.Unlock()
		var ev struct {
			Event, Window, Mode, Name string
			Seconds                   int
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Event {
		case "drag-request":
			work.Add(1)
			go func() {
				defer work.Done()
				if err := d.drag(ev.Window, ev.Mode, time.Duration(ev.Seconds)*time.Second); err != nil {
					d.fail("drag "+ev.Mode, err)
				}
			}()
		case "capture-request":
			work.Add(1)
			go func() {
				defer work.Done()
				if err := d.capture(ev.Window, ev.Name); err != nil {
					d.fail("capture "+ev.Name, err)
				}
			}()
		}
	}
	work.Wait()
	err = cmd.Wait()
	d.mu.Lock()
	failed := d.failed
	d.mu.Unlock()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return exit.ExitCode()
	case err != nil:
		fmt.Fprintln(os.Stderr, "desk:", err)
		return 2
	case failed:
		return 3
	}
	return 0
}

// find locates the fixture's top-level window by its name prefix. Its title
// keeps changing after that prefix.
func (d *driver) find(name string) (uintptr, error) {
	prefix := "dfx2 mixer " + name
	var found uintptr
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		pGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid != d.pid {
			return 1
		}
		buffer := make([]uint16, 256)
		n, _, _ := pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), 256)
		if strings.HasPrefix(syscall.UTF16ToString(buffer[:n]), prefix) {
			found = hwnd
			return 0
		}
		return 1
	})
	pEnumWindows.Call(callback, 0)
	if found == 0 {
		return 0, fmt.Errorf("no window titled %q in process %d", prefix, d.pid)
	}
	return found, nil
}

// raise lifts the window over others without activating it, so a console or
// its sibling cannot sit over the point the driver is about to use.
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

// grabPoint searches for a point of the window's own caption or corner and
// confirms the window is what is actually under it.
func grabPoint(hwnd uintptr, mode string) (point, int, error) {
	var r rect
	if ok, _, err := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return point{}, 0, fmt.Errorf("GetWindowRect: %w", err)
	}
	want := htCaption
	candidates := []point{}
	if mode == "size" {
		want = htBottomRight
		for k := int32(0); k < 24; k++ {
			candidates = append(candidates, point{r.right - 1 - k, r.bottom - 1 - k})
		}
	} else {
		for k := int32(1); k < 80; k++ {
			candidates = append(candidates, point{(r.left + r.right) / 2, r.top + k})
		}
	}
	for _, p := range candidates {
		if hitTest(hwnd, p.x, p.y) != want {
			continue
		}
		under, _, _ := pWindowFromPoint.Call(uintptr(uint32(p.x)) | uintptr(uint32(p.y))<<32)
		root, _, _ := pGetAncestor.Call(under, gaRoot)
		if under != hwnd && root != hwnd {
			return point{}, 0, fmt.Errorf("another window covers (%d,%d)", p.x, p.y)
		}
		return p, want, nil
	}
	return point{}, 0, fmt.Errorf("no hit-test %d point found in %+v", want, r)
}

func button(flags uint32) error {
	input := mouseInput{kind: inputMouse, flags: flags}
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

// drag presses at the grab point and keeps the button down for the duration.
// Windows then runs its size/move loop on the fixture's UI thread. "move" and
// "size" keep the pointer moving; "hold" nudges once and then stays still, so
// the loop stays established with no native events to service. It stops early
// when the window is gone and always releases the button.
func (d *driver) drag(name, mode string, duration time.Duration) error {
	hwnd, err := d.find(name)
	if err != nil {
		return err
	}
	raise(hwnd)
	time.Sleep(200 * time.Millisecond)
	start, hit, err := grabPoint(hwnd, mode)
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
	d.emit("driver-drag-start", map[string]any{"window": name, "mode": mode, "x": start.x, "y": start.y, "hit_test": hit})
	began := time.Now()
	moves := 0
	alive := true
	for elapsed := time.Duration(0); elapsed < duration; elapsed = time.Since(began) {
		if ok, _, _ := pIsWindow.Call(hwnd); ok == 0 {
			alive = false
			break
		}
		t := elapsed.Seconds()
		var dx, dy float64
		switch mode {
		case "size":
			dx, dy = 110+100*math.Sin(2*math.Pi*t/4), 70+60*math.Sin(2*math.Pi*t/3)
		case "move":
			dx, dy = 120*math.Sin(2*math.Pi*t/4), 60*math.Sin(2*math.Pi*t/3)
		default: // hold: enough to establish the drag, then no more input
			dx, dy = 10, 10
		}
		if mode != "hold" || moves == 0 {
			if err := moveTo(start.x+int32(dx), start.y+int32(dy)); err != nil {
				return err
			}
			moves++
		}
		time.Sleep(16 * time.Millisecond)
	}
	d.emit("driver-drag-done", map[string]any{"window": name, "mode": mode, "moves": moves,
		"elapsed_ms": time.Since(began).Milliseconds(), "window_alive": alive})
	return nil
}

// capture copies the client area from the screen, which is what the operator
// sees, unlike the fixture's retained-image snapshot, writes it as PNG and
// reports how much of it is black. A blit that set nothing leaves black or
// stale pixels; the runner judges the fraction.
func (d *driver) capture(name, file string) error {
	hwnd, err := d.find(name)
	if err != nil {
		return err
	}
	raise(hwnd)
	time.Sleep(150 * time.Millisecond)
	var client rect
	if ok, _, err := pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client))); ok == 0 {
		return fmt.Errorf("GetClientRect: %w", err)
	}
	var origin point
	if ok, _, err := pClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&origin))); ok == 0 {
		return fmt.Errorf("ClientToScreen: %w", err)
	}
	width, height := client.right, client.bottom
	if width <= 0 || height <= 0 {
		return fmt.Errorf("client area is %dx%d", width, height)
	}
	screen, _, err := pGetDC.Call(0)
	if screen == 0 {
		return fmt.Errorf("GetDC: %w", err)
	}
	defer pReleaseDC.Call(0, screen)
	memory, _, err := pCreateCompatibleDC.Call(screen)
	if memory == 0 {
		return fmt.Errorf("CreateCompatibleDC: %w", err)
	}
	defer pDeleteDC.Call(memory)
	bitmap, _, err := pCreateCompatibleBitmap.Call(screen, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return fmt.Errorf("CreateCompatibleBitmap: %w", err)
	}
	defer pDeleteObject.Call(bitmap)
	previous, _, _ := pSelectObject.Call(memory, bitmap)
	ok, _, err := pBitBlt.Call(memory, 0, 0, uintptr(width), uintptr(height), screen, uintptr(origin.x), uintptr(origin.y), srcCopy)
	pSelectObject.Call(memory, previous)
	if ok == 0 {
		return fmt.Errorf("BitBlt: %w", err)
	}
	info := bitmapInfo{width: width, height: -height, planes: 1, bitCount: 32}
	info.size = uint32(unsafe.Offsetof(info.colors))
	pixels := make([]byte, int(width)*int(height)*4)
	lines, _, err := pGetDIBits.Call(memory, bitmap, 0, uintptr(height), uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&info)), 0)
	if lines == 0 {
		return fmt.Errorf("GetDIBits: %w", err)
	}
	out := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	var black, background int
	for i := 0; i+3 < len(pixels); i += 4 {
		b, g, r := pixels[i], pixels[i+1], pixels[i+2]
		copy(out.Pix[i:i+4], []byte{r, g, b, 0xff})
		if r < 8 && g < 8 && b < 8 {
			black++
		}
		if near(r, 0x1e) && near(g, 0x1f) && near(b, 0x24) {
			background++
		}
	}
	path := filepath.Join(d.outDir, file+".png")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, out); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	count := float64(width) * float64(height)
	d.emit("driver-capture", map[string]any{"window": name, "name": file, "width": width, "height": height,
		"black_fraction": float64(black) / count, "background_fraction": float64(background) / count, "path": path})
	return nil
}

func near(v, want byte) bool { return int(v)-int(want) < 5 && int(want)-int(v) < 5 }
