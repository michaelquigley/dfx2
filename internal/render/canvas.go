// Package render draws opaque window images on the CPU: clipped fills,
// antialiased lines, coverage masks and premultiplied color bitmaps, into one
// reusable premultiplied BGRA frame.
package render

import (
	"fmt"
	"image"
	"math"

	"golang.org/x/image/vector"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// Color is a straight-alpha color.
type Color struct{ R, G, B, A uint8 }

func RGB(r, g, b uint8) Color { return Color{r, g, b, 255} }

// Mix interpolates two colors, t in [0, 1].
func Mix(a, b Color, t float32) Color {
	l := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t + 0.5) }
	return Color{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), l(a.A, b.A)}
}

// Canvas is one window's reusable frame. Every image is opaque: Clear sets
// alpha to 255 and composition never lowers it.
type Canvas struct {
	Width, Height, Stride int
	Pix                   []byte
	// Limit bounds the image in bytes; zero means platform.MaxImageBytes.
	Limit int
	clip  image.Rectangle
	clips []image.Rectangle
	ras   vector.Rasterizer
	mask  *image.Alpha // line scratch, bounded by the canvas
}

// Resize sets the frame size with checked arithmetic and the byte limit.
// Storage is replaced only after the new size is accepted; on failure the
// previous frame remains intact. Capacity is reused unless it is more than
// four times what the new size needs.
func (c *Canvas) Resize(width, height int) error {
	stride, size, err := platform.ImageSize(width, height)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if limit := c.Limit; limit > 0 && size > limit {
		return fmt.Errorf("render: image %dx%d (%d bytes) exceeds the %d-byte limit", width, height, size, limit)
	}
	if cap(c.Pix) >= size && cap(c.Pix) <= 4*size {
		c.Pix = c.Pix[:size]
	} else {
		c.Pix = make([]byte, size)
	}
	c.Width, c.Height, c.Stride = width, height, stride
	c.clip = image.Rect(0, 0, width, height)
	c.clips = c.clips[:0]
	return nil
}

// Image returns the frame for presentation. It borrows Pix.
func (c *Canvas) Image(scale float64) platform.Image {
	return platform.Image{Width: c.Width, Height: c.Height, Stride: c.Stride, Scale: scale, Pix: c.Pix}
}

// Release drops the frame's storage.
func (c *Canvas) Release() { *c = Canvas{Limit: c.Limit} }

// Clip is the current clip rectangle.
func (c *Canvas) Clip() image.Rectangle { return c.clip }

// PushClip intersects the clip with r until the matching PopClip.
func (c *Canvas) PushClip(r image.Rectangle) {
	c.clips = append(c.clips, c.clip)
	c.clip = c.clip.Intersect(r)
}

func (c *Canvas) PopClip() {
	if n := len(c.clips); n > 0 {
		c.clip, c.clips = c.clips[n-1], c.clips[:n-1]
	}
}

// Clear fills the whole frame, ignoring the clip, with an opaque color.
func (c *Canvas) Clear(col Color) {
	for i := 0; i+3 < len(c.Pix); i += 4 {
		c.Pix[i], c.Pix[i+1], c.Pix[i+2], c.Pix[i+3] = col.B, col.G, col.R, 255
	}
}

// Fill composites col over r within the clip.
func (c *Canvas) Fill(r image.Rectangle, col Color) {
	r = r.Intersect(c.clip)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := c.Pix[y*c.Stride+r.Min.X*4 : y*c.Stride+r.Max.X*4]
		for i := 0; i < len(row); i += 4 {
			blend(row[i:i+4], col, uint32(col.A))
		}
	}
}

// Line strokes an antialiased segment of the given width, in physical pixels.
func (c *Canvas) Line(x0, y0, x1, y1, width float32, col Color) {
	dx, dy := x1-x0, y1-y0
	length := float32(math.Hypot(float64(dx), float64(dy)))
	if length == 0 || width <= 0 {
		return
	}
	nx, ny := -dy/length*width/2, dx/length*width/2
	xs := []float32{x0 + nx, x1 + nx, x1 - nx, x0 - nx}
	ys := []float32{y0 + ny, y1 + ny, y1 - ny, y0 - ny}
	minX, minY, maxX, maxY := xs[0], ys[0], xs[0], ys[0]
	for i := range xs {
		minX, maxX = min(minX, xs[i]), max(maxX, xs[i])
		minY, maxY = min(minY, ys[i]), max(maxY, ys[i])
	}
	bounds := image.Rect(int(math.Floor(float64(minX))), int(math.Floor(float64(minY))),
		int(math.Ceil(float64(maxX))), int(math.Ceil(float64(maxY)))).Intersect(c.clip)
	if bounds.Empty() {
		return
	}
	w, h := bounds.Dx(), bounds.Dy()
	ox, oy := float32(bounds.Min.X), float32(bounds.Min.Y)
	c.ras.Reset(w, h)
	c.ras.MoveTo(xs[0]-ox, ys[0]-oy)
	for i := 1; i < 4; i++ {
		c.ras.LineTo(xs[i]-ox, ys[i]-oy)
	}
	c.ras.ClosePath()
	if c.mask == nil || cap(c.mask.Pix) < w*h {
		c.mask = image.NewAlpha(image.Rect(0, 0, w, h))
	} else {
		c.mask.Pix, c.mask.Stride, c.mask.Rect = c.mask.Pix[:w*h], w, image.Rect(0, 0, w, h)
		clear(c.mask.Pix)
	}
	c.ras.Draw(c.mask, c.mask.Bounds(), image.Opaque, image.Point{})
	c.Mask(bounds.Min.X, bounds.Min.Y, w, h, c.mask.Pix, col)
}

// Mask tints an 8-bit coverage bitmap with col at (x, y), within the clip.
func (c *Canvas) Mask(x, y, w, h int, coverage []byte, col Color) {
	r := image.Rect(x, y, x+w, y+h).Intersect(c.clip)
	for py := r.Min.Y; py < r.Max.Y; py++ {
		src := coverage[(py-y)*w:]
		for px := r.Min.X; px < r.Max.X; px++ {
			cov := uint32(src[px-x])
			if cov == 0 {
				continue
			}
			o := py*c.Stride + px*4
			blend(c.Pix[o:o+4], col, cov*uint32(col.A)/255)
		}
	}
}

// Bitmap composites a premultiplied RGBA bitmap at (x, y), within the clip.
func (c *Canvas) Bitmap(x, y, w, h int, rgba []byte) {
	r := image.Rect(x, y, x+w, y+h).Intersect(c.clip)
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			s := rgba[((py-y)*w+(px-x))*4:]
			a := uint32(s[3])
			if a == 0 {
				continue
			}
			inv := 255 - a
			d := c.Pix[py*c.Stride+px*4:]
			d[0] = uint8(min(uint32(s[2])+(uint32(d[0])*inv+127)/255, 255))
			d[1] = uint8(min(uint32(s[1])+(uint32(d[1])*inv+127)/255, 255))
			d[2] = uint8(min(uint32(s[0])+(uint32(d[2])*inv+127)/255, 255))
		}
	}
}

// blend composites a straight color at alpha a (0..255) over an opaque pixel.
func blend(d []byte, col Color, a uint32) {
	if a == 0 {
		return
	}
	if a == 255 {
		d[0], d[1], d[2] = col.B, col.G, col.R
		return
	}
	lerp := func(dst, src uint8) uint8 { return uint8((uint32(dst)*(255-a) + uint32(src)*a + 127) / 255) }
	d[0], d[1], d[2] = lerp(d[0], col.B), lerp(d[1], col.G), lerp(d[2], col.R)
}
