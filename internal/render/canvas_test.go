package render

import (
	"image"
	"slices"
	"strings"
	"testing"
)

func pixel(c *Canvas, x, y int) [4]byte {
	o := y*c.Stride + x*4
	return [4]byte(c.Pix[o : o+4])
}

func bgra(col Color) [4]byte { return [4]byte{col.B, col.G, col.R, 255} }

var (
	black = RGB(0, 0, 0)
	red   = RGB(255, 0, 0)
	blue  = RGB(0, 0, 255)
)

func TestFillRespectsNestedClips(t *testing.T) {
	var c Canvas
	if err := c.Resize(8, 6); err != nil {
		t.Fatal(err)
	}
	c.Clear(black)
	c.PushClip(image.Rect(2, 1, 6, 5))
	c.PushClip(image.Rect(4, 0, 10, 10)) // intersects to (4,1)-(6,5)
	c.Fill(image.Rect(-5, -5, 50, 50), red)
	c.PopClip()
	c.Fill(image.Rect(0, 4, 3, 6), blue) // clipped to (2,4)-(3,5)
	c.PopClip()
	c.PopClip() // unbalanced pops are harmless
	var rows []string
	for y := range 6 {
		var b strings.Builder
		for x := range 8 {
			switch pixel(&c, x, y) {
			case bgra(red):
				b.WriteByte('R')
			case bgra(blue):
				b.WriteByte('B')
			case bgra(black):
				b.WriteByte('.')
			default:
				b.WriteByte('?')
			}
		}
		rows = append(rows, b.String())
	}
	want := []string{"........", "....RR..", "....RR..", "....RR..", "..B.RR..", "........"}
	if !slices.Equal(rows, want) {
		t.Fatalf("clipped fill\n%s", strings.Join(rows, "\n"))
	}
	for i := 3; i < len(c.Pix); i += 4 {
		if c.Pix[i] != 255 {
			t.Fatal("frame not opaque")
		}
	}
}

func TestResizeFailureKeepsThePreviousFrame(t *testing.T) {
	c := Canvas{Limit: 64 * 64 * 4}
	if err := c.Resize(64, 64); err != nil {
		t.Fatal(err)
	}
	c.Clear(red)
	before := c.Pix
	for _, size := range [][2]int{{65, 64}, {0, 10}, {10, -1}, {1 << 30, 1 << 30}} {
		if err := c.Resize(size[0], size[1]); err == nil {
			t.Fatalf("%v accepted", size)
		}
		if c.Width != 64 || c.Height != 64 || &c.Pix[0] != &before[0] || pixel(&c, 63, 63) != bgra(red) {
			t.Fatalf("%v: failed resize disturbed the frame", size)
		}
	}
	// The global image limit applies without an injected one.
	var unlimited Canvas
	if err := unlimited.Resize(20000, 20000); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("4 GB image: %v", err)
	}
	// Shrinking reuses storage; a much smaller frame does not pin a huge one.
	if err := c.Resize(32, 64); err != nil || &c.Pix[0] != &before[0] {
		t.Fatal("moderate shrink reallocated")
	}
	if err := c.Resize(4, 4); err != nil || cap(c.Pix) != 64 {
		t.Fatalf("tiny frame kept %d bytes", cap(c.Pix))
	}
	if img := c.Image(2); img.Width != 4 || img.Stride != 16 || img.Scale != 2 || img.Validate() != nil {
		t.Fatalf("image %+v", img)
	}
}

func TestCompositingIsDeterministic(t *testing.T) {
	var c Canvas
	if err := c.Resize(4, 1); err != nil {
		t.Fatal(err)
	}
	c.Clear(black)
	c.Mask(0, 0, 4, 1, []byte{0, 128, 255, 64}, Color{255, 255, 255, 255})
	got := []byte{pixel(&c, 0, 0)[0], pixel(&c, 1, 0)[0], pixel(&c, 2, 0)[0], pixel(&c, 3, 0)[0]}
	if !slices.Equal(got, []byte{0, 128, 255, 64}) {
		t.Fatalf("mask coverage %v", got)
	}
	c.Clear(black)
	// Premultiplied RGBA: half-transparent red over black stays half red.
	c.Bitmap(1, 0, 2, 1, []byte{128, 0, 0, 128, 0, 0, 255, 255})
	if pixel(&c, 1, 0) != [4]byte{0, 0, 128, 255} || pixel(&c, 2, 0) != [4]byte{255, 0, 0, 255} || pixel(&c, 0, 0) != bgra(black) {
		t.Fatalf("bitmap %v", c.Pix)
	}
	c.Clear(black)
	c.Fill(image.Rect(0, 0, 4, 1), Color{255, 255, 255, 128})
	if pixel(&c, 0, 0)[0] != 128 {
		t.Fatalf("translucent fill %v", pixel(&c, 0, 0))
	}
}

func TestLineIsAntialiasedAndClipped(t *testing.T) {
	var c Canvas
	if err := c.Resize(10, 10); err != nil {
		t.Fatal(err)
	}
	c.Clear(black)
	// A 2 px vertical line centred on x=5 covers columns 4 and 5 fully.
	c.Line(5, 0, 5, 10, 2, red)
	for y := range 10 {
		if pixel(&c, 4, y) != bgra(red) || pixel(&c, 5, y) != bgra(red) || pixel(&c, 3, y) != bgra(black) || pixel(&c, 6, y) != bgra(black) {
			t.Fatalf("row %d: %v", y, c.Pix[y*c.Stride:(y+1)*c.Stride])
		}
	}
	// A 1 px line centred on a pixel edge splits coverage between two columns.
	c.Clear(black)
	c.Line(3, 0, 3, 10, 1, red)
	if r := pixel(&c, 2, 5)[2]; r < 120 || r > 135 || pixel(&c, 2, 5) != pixel(&c, 3, 5) {
		t.Fatalf("half coverage %v %v", pixel(&c, 2, 5), pixel(&c, 3, 5))
	}
	c.Clear(black)
	c.PushClip(image.Rect(0, 0, 10, 5))
	c.Line(0, 0, 10, 10, 3, red)
	c.PopClip()
	for y := 5; y < 10; y++ {
		for x := range 10 {
			if pixel(&c, x, y) != bgra(black) {
				t.Fatalf("line escaped its clip at %d,%d", x, y)
			}
		}
	}
}
