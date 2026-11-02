//go:build windows

package main

import (
	"testing"
	"unsafe"
)

// The structs handed to Win32 must match its 64-bit layouts.
func TestWin32Layouts(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("layouts checked for 64-bit Windows only")
	}
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"WNDCLASSEXW", unsafe.Sizeof(wndClassEx{}), 80},
		{"MSG", unsafe.Sizeof(winMsg{}), 48},
		{"PAINTSTRUCT", unsafe.Sizeof(paintStruct{}), 72},
		{"RECT", unsafe.Sizeof(rect{}), 16},
		{"BITMAPINFOHEADER", unsafe.Offsetof(bitmapInfo{}.colors), 40},
	} {
		if c.got != c.want {
			t.Errorf("%s is %d bytes, want %d", c.name, c.got, c.want)
		}
	}
}
