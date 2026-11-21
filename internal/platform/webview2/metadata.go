package webview2

import (
	"unicode/utf16"
	"unsafe"
)

// utf8Length is the UTF-8 byte length of a UTF-16 string, exactly as its Go
// conversion (syscall.UTF16ToString, utf16.Decode) will produce it, computed
// in place without allocating: a surrogate pair is 4 bytes, and a lone
// surrogate becomes U+FFFD, 3 bytes.
func utf8Length(units []uint16) int {
	n := 0
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u < 0x80:
			n++
		case u < 0x800:
			n += 2
		case utf16.IsSurrogate(rune(u)) && u < 0xDC00 && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] < 0xE000:
			n += 4
			i++
		default:
			n += 3 // the rest of the BMP, and a lone surrogate's U+FFFD
		}
	}
	return n
}

// wideUTF8 measures a NUL-terminated native UTF-16 string in place.
func wideUTF8(p *uint16, length int) int {
	if p == nil || length == 0 {
		return 0
	}
	return utf8Length(unsafe.Slice(p, length))
}
