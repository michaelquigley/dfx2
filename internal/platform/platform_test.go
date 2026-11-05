package platform

import (
	"math"
	"testing"
)

func TestImageArithmeticIsCheckedAndBounded(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		ok   bool
	}{
		{1, 1, true}, {3840, 2160, true}, {8192, 8192, true}, // 256 MiB exactly
		{8192, 8193, false}, {0, 1, false}, {1, -1, false},
		{math.MaxInt32, 2, false}, {2, math.MaxInt, false}, {math.MaxInt / 2, math.MaxInt / 2, false},
	} {
		stride, size, err := ImageSize(tc.w, tc.h)
		if (err == nil) != tc.ok {
			t.Fatalf("%dx%d: %v", tc.w, tc.h, err)
		}
		if tc.ok && (stride != tc.w*4 || size != stride*tc.h || size > MaxImageBytes) {
			t.Fatalf("%dx%d: stride %d size %d", tc.w, tc.h, stride, size)
		}
	}
	good := Image{Width: 3, Height: 2, Stride: 16, Scale: 1, Pix: make([]byte, 16+12)}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, img := range map[string]Image{
		"short":       {Width: 3, Height: 2, Stride: 16, Scale: 1, Pix: make([]byte, 27)},
		"stride":      {Width: 3, Height: 2, Stride: 8, Scale: 1, Pix: make([]byte, 64)},
		"scale":       {Width: 3, Height: 2, Stride: 12, Scale: 0, Pix: make([]byte, 24)},
		"nan scale":   {Width: 3, Height: 2, Stride: 12, Scale: math.NaN(), Pix: make([]byte, 24)},
		"huge stride": {Width: 1, Height: 2, Stride: MaxImageBytes, Scale: 1, Pix: make([]byte, 8)},
	} {
		if img.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
