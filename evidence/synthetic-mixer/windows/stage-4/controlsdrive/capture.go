//go:build windows

package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

// Screen capture of a window's client area: what reached the display, unlike a
// retained-image snapshot, which is what the framework handed to GDI. Adapted
// from ../../desk, including its native destination buffer.

var (
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pGetDpiForSystem        = user32.NewProc("GetDpiForSystem")
	pGetClientRect          = user32.NewProc("GetClientRect")
	pGetDC                  = user32.NewProc("GetDC")
	pReleaseDC              = user32.NewProc("ReleaseDC")
	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pGetDIBits              = gdi32.NewProc("GetDIBits")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pVirtualAlloc           = kernel32.NewProc("VirtualAlloc")
	pVirtualFree            = kernel32.NewProc("VirtualFree")
)

const (
	srcCopy          = 0x00cc0020
	memCommitReserve = 0x3000
	memRelease       = 0x8000
	pageReadWrite    = 0x04
	hwndTopmost      = ^uintptr(0) // HWND_TOPMOST, (HWND)-1
	hwndNotopmost    = ^uintptr(1) // HWND_NOTOPMOST, (HWND)-2
	swpNoMove        = 0x0002
	swpNoActivate    = 0x0010
)

type bitmapInfo struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPPM, yPPM             int32
	clrUsed, clrImportant  uint32
	colors                 [1]uint32
}

// screen captures a window's client area into OUT/name.png.
func (d *driver) screen(window, name string) error {
	hwnd, err := d.find(window)
	if err != nil {
		return err
	}
	pSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	pSetWindowPos.Call(hwnd, hwndNotopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
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
		return fmt.Errorf("BitBlt: %w (a disconnected session cannot capture)", err)
	}
	info := bitmapInfo{width: width, height: -height, planes: 1, bitCount: 32}
	info.size = uint32(unsafe.Offsetof(info.colors))
	// Native storage: GetDIBits into a Go heap buffer spanning two of Go's
	// Windows heap arenas returned zero lines on the stage 2 host.
	size := int(width) * int(height) * 4
	base, _, err := pVirtualAlloc.Call(0, uintptr(size), memCommitReserve, pageReadWrite)
	if base == 0 {
		return fmt.Errorf("VirtualAlloc: %w", err)
	}
	defer pVirtualFree.Call(base, 0, memRelease)
	pixels := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(nil), base)), size)
	if lines, _, err := pGetDIBits.Call(memory, bitmap, 0, uintptr(height), base, uintptr(unsafe.Pointer(&info)), 0); lines == 0 {
		return fmt.Errorf("GetDIBits: %w", err)
	}
	out := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	for i := 0; i+3 < len(pixels); i += 4 {
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = pixels[i+2], pixels[i+1], pixels[i], 0xff
	}
	f, err := os.Create(filepath.Join(d.out, name+".png"))
	if err != nil {
		return err
	}
	if err := png.Encode(f, out); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
