package webview2

import "unsafe"

// ptrAdd steps through a UTF-16 string the fake allocated on the Go heap.
func ptrAdd(p *uint16, i int) unsafe.Pointer { return unsafe.Add(unsafe.Pointer(p), 2*i) }
