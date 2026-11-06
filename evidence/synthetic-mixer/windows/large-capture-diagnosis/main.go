//go:build windows

// dibprobe repeats desk's screen-capture sequence (CreateCompatibleBitmap,
// BitBlt from the screen DC, GetDIBits) at the large case's sizes and reports
// each attempt's result, to separate a size-dependent failure from an
// intermittent one. It opens no window and touches no input.
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pGetDC                  = user32.NewProc("GetDC")
	pReleaseDC              = user32.NewProc("ReleaseDC")
	pGetDeviceCaps          = gdi32.NewProc("GetDeviceCaps")
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

type bitmapInfo struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPPM, yPPM             int32
	clrUsed, clrImportant  uint32
	colors                 [1]uint32
}

// variant: "base" (as desk), "touch" (write one byte per 4 KiB page first),
// "valloc" (VirtualAlloc'd destination), "chunk" (64 rows per call into the Go
// buffer), "gc" (runtime.GC before allocating), "retry" (second call on failure).
func attempt(width, height int32, variant string) string {
	screen, _, err := pGetDC.Call(0)
	if screen == 0 {
		return fmt.Sprintf("GetDC: %v", err)
	}
	defer pReleaseDC.Call(0, screen)
	memory, _, err := pCreateCompatibleDC.Call(screen)
	if memory == 0 {
		return fmt.Sprintf("CreateCompatibleDC: %v", err)
	}
	defer pDeleteDC.Call(memory)
	bitmap, _, err := pCreateCompatibleBitmap.Call(screen, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return fmt.Sprintf("CreateCompatibleBitmap: %v", err)
	}
	defer pDeleteObject.Call(bitmap)
	previous, _, _ := pSelectObject.Call(memory, bitmap)
	ok, _, err := pBitBlt.Call(memory, 0, 0, uintptr(width), uintptr(height), screen, 0, 0, 0x00cc0020)
	pSelectObject.Call(memory, previous)
	if ok == 0 {
		return fmt.Sprintf("BitBlt: %v", err)
	}
	info := bitmapInfo{width: width, height: -height, planes: 1, bitCount: 32}
	info.size = uint32(unsafe.Offsetof(info.colors))
	n := int(width) * int(height) * 4
	if variant == "gc" {
		runtime.GC()
	}
	var base uintptr
	var pixels []byte
	if variant == "valloc" {
		base, _, err = pVirtualAlloc.Call(0, uintptr(n), 0x3000, 0x04) // MEM_COMMIT|MEM_RESERVE, PAGE_READWRITE
		if base == 0 {
			return fmt.Sprintf("VirtualAlloc: %v", err)
		}
		defer pVirtualFree.Call(base, 0, 0x8000)
	} else {
		pixels = make([]byte, n)
		base = uintptr(unsafe.Pointer(&pixels[0]))
		if variant == "touch" {
			for i := 0; i < n; i += 4096 {
				pixels[i] = 0
			}
		}
	}
	if variant == "chunk" {
		stride := int(width) * 4
		for start := int32(0); start < height; start += 64 {
			rows := min(int32(64), height-start)
			// Top-down: row `start` of the image is at offset start*stride; for a
			// top-down DIB, GetDIBits' start scan line counts from the bottom of
			// the bitmap, so request the matching bottom-up index.
			lines, _, err := pGetDIBits.Call(memory, bitmap, uintptr(height-start-rows), uintptr(rows), base+uintptr(int(start)*stride), uintptr(unsafe.Pointer(&info)), 0)
			if lines == 0 {
				return fmt.Sprintf("GetDIBits chunk at %d: 0 lines (%v)", start, err)
			}
		}
		runtime.KeepAlive(pixels)
		return "ok chunked"
	}
	regions := ""
	if variant == "query" {
		regions = describe(base, uintptr(n))
	}
	lines, _, err := pGetDIBits.Call(memory, bitmap, 0, uintptr(height), base, uintptr(unsafe.Pointer(&info)), 0)
	if variant == "query" {
		runtime.KeepAlive(pixels)
		if lines == 0 {
			return fmt.Sprintf("GetDIBits: 0 lines (%v); %s", err, regions)
		}
		return fmt.Sprintf("ok %d lines; %s", lines, regions)
	}
	if lines == 0 && variant == "retry" {
		lines, _, err = pGetDIBits.Call(memory, bitmap, 0, uintptr(height), base, uintptr(unsafe.Pointer(&info)), 0)
		if lines != 0 {
			runtime.KeepAlive(pixels)
			return fmt.Sprintf("ok on retry %d lines", lines)
		}
	}
	runtime.KeepAlive(pixels)
	if lines == 0 {
		return fmt.Sprintf("GetDIBits: 0 lines (%v)", err)
	}
	return fmt.Sprintf("ok %d lines", lines)
}

type memoryBasicInformation struct {
	baseAddress, allocationBase uintptr
	allocationProtect, _        uint32
	regionSize                  uintptr
	state, protect, kind        uint32
	_                           uint32
}

var pVirtualQuery = kernel32.NewProc("VirtualQuery")

// describe walks the buffer with VirtualQuery and reports every distinct
// allocation base (one per VirtualAlloc reservation) and region it spans.
func describe(base, n uintptr) string {
	var out []string
	var lastAlloc uintptr
	allocations := 0
	for p := base; p < base+n; {
		var m memoryBasicInformation
		r, _, _ := pVirtualQuery.Call(p, uintptr(unsafe.Pointer(&m)), unsafe.Sizeof(m))
		if r == 0 {
			out = append(out, fmt.Sprintf("VirtualQuery failed at +%d", p-base))
			break
		}
		if m.allocationBase != lastAlloc {
			allocations++
			lastAlloc = m.allocationBase
		}
		out = append(out, fmt.Sprintf("+%d:alloc=%#x,size=%d,state=%#x,protect=%#x", p-base, m.allocationBase, m.regionSize, m.state, m.protect))
		p = m.baseAddress + m.regionSize
	}
	return fmt.Sprintf("allocations=%d [%s]", allocations, joinStrings(out))
}

func joinStrings(s []string) string {
	r := ""
	for i, x := range s {
		if i > 0 {
			r += " "
		}
		r += x
	}
	return r
}

func main() {
	rounds := 3
	if len(os.Args) > 1 {
		rounds, _ = strconv.Atoi(os.Args[1])
	}
	variants := []string{"base", "touch", "valloc", "chunk", "gc", "retry"}
	if len(os.Args) > 2 {
		variants = os.Args[2:]
	}
	screen, _, _ := pGetDC.Call(0)
	bits, _, _ := pGetDeviceCaps.Call(screen, 12)   // BITSPIXEL
	planes, _, _ := pGetDeviceCaps.Call(screen, 14) // PLANES
	pReleaseDC.Call(0, screen)
	fmt.Printf("screen bitspixel=%d planes=%d\n", bits, planes)
	sizes := [][2]int32{{560, 260}, {984, 761}, {1240, 900}, {1600, 1000}, {2400, 1300}, {3200, 1400}, {984, 760}, {985, 761}}
	for _, variant := range variants {
		failed := 0
		for r := 0; r < rounds; r++ {
			for _, s := range sizes {
				result := attempt(s[0], s[1], variant)
				if len(result) < 2 || result[:2] != "ok" {
					failed++
				}
				fmt.Printf("%s round=%d %dx%d: %s\n", variant, r, s[0], s[1], result)
			}
		}
		fmt.Printf("== %s: %d of %d failed\n", variant, failed, rounds*len(sizes))
	}
}
