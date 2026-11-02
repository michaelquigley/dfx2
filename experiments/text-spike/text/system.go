package text

import (
	"image"
	"math"
	"slices"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
)

// subpixels is how many horizontal positions within a pixel a glyph is
// rasterized at: its pen's fraction of a pixel is rounded to the nearest
// quarter, so glyphs keep their shaped spacing instead of snapping to whole
// pixels, at the cost of up to four bitmaps a glyph and size.
const subpixels = 4

// RGB is an opaque color.
type RGB struct{ R, G, B uint8 }

// Frame is a frame's pixels: premultiplied BGRA, 4 bytes a pixel, as
// cairo's ARGB32 is on little-endian machines and as a GPU would take it.
type Frame struct {
	W, H int
	Pix  []byte
}

// Resize makes the frame w×h, reusing its memory when it can.
func (f *Frame) Resize(w, h int) {
	f.W, f.H = w, h
	if n := w * h * 4; cap(f.Pix) >= n {
		f.Pix = f.Pix[:n]
	} else {
		f.Pix = make([]byte, n)
	}
}

// Clear fills the frame with c.
func (f *Frame) Clear(c RGB) {
	for i := 0; i < len(f.Pix); i += 4 {
		f.Pix[i], f.Pix[i+1], f.Pix[i+2], f.Pix[i+3] = c.B, c.G, c.R, 255
	}
}

// Atlas packs bitmaps into shelves of one texture-shaped image: coverage
// (1 byte a pixel) or premultiplied RGBA (4). It grows taller when full and
// tracks the rectangle that changed since the last upload, which is all a
// GPU renderer would copy.
type Atlas struct {
	W, H, BPP     int
	Pix           []byte
	Dirty         image.Rectangle
	x, y, rowH, n int
}

func newAtlas(bpp int) *Atlas {
	a := &Atlas{W: 1024, H: 128, BPP: bpp}
	a.Pix = make([]byte, a.W*a.H*bpp)
	return a
}

// Glyphs is how many bitmaps the atlas holds.
func (a *Atlas) Glyphs() int { return a.n }

// Used is the fraction of the atlas's rows taken.
func (a *Atlas) Used() float64 { return float64(a.y+a.rowH) / float64(a.H) }

func (a *Atlas) put(w, h int, src []byte) (x, y int, ok bool) {
	const pad = 1 // keeps bilinear sampling on a GPU from bleeding neighbors in
	if w+pad > a.W {
		return 0, 0, false
	}
	if a.x+w+pad > a.W {
		a.x, a.y, a.rowH = 0, a.y+a.rowH+pad, 0
	}
	for a.y+h+pad > a.H {
		a.H *= 2
		a.Pix = append(a.Pix, make([]byte, len(a.Pix))...)
	}
	x, y = a.x, a.y
	row := w * a.BPP
	for r := range h {
		copy(a.Pix[((y+r)*a.W+x)*a.BPP:], src[r*row:(r+1)*row])
	}
	a.x += w + pad
	a.rowH = max(a.rowH, h)
	a.n++
	a.Dirty = a.Dirty.Union(image.Rect(x, y, x+w, y+h))
	return x, y, true
}

type glyphKey struct {
	face *font.Face
	gid  font.GID
	size fixed.Int26_6
	sub  uint8
}

type glyphEntry struct {
	atlas           *Atlas
	x, y, w, h      int
	left, top       int
	color, rastered bool
}

// Stats counts the system's work, for watching what a frame costs.
type Stats struct {
	Shapes, Wraps   int // paragraphs shaped and wrapped, ever
	Rasterized      int // glyph bitmaps made, ever
	FrameRasterized int // ... during the last frame
	FrameGlyphs     int // glyphs drawn during the last frame
}

// System is the shared text code above an Engine.
type System struct {
	eng         Engine
	Mask, Color *Atlas
	glyphs      map[glyphKey]glyphEntry
	Stats       Stats
}

func NewSystem(e Engine) *System {
	return &System{eng: e, Mask: newAtlas(1), Color: newAtlas(4), glyphs: map[glyphKey]glyphEntry{}}
}

// BeginFrame resets the per-frame counters.
func (s *System) BeginFrame() { s.Stats.FrameRasterized, s.Stats.FrameGlyphs = 0, 0 }

// glyph returns a glyph's place in an atlas, rasterizing it the first time
// it is asked for at this size and subpixel position.
func (s *System) glyph(k glyphKey) glyphEntry {
	if e, ok := s.glyphs[k]; ok {
		return e
	}
	b := s.eng.Glyph(k.face, k.gid, float32(k.size)/64, float32(k.sub)/subpixels)
	s.Stats.Rasterized++
	s.Stats.FrameRasterized++
	e := glyphEntry{w: b.W, h: b.H, left: b.Left, top: b.Top, color: b.Color, rastered: true}
	if b.W > 0 && b.H > 0 {
		e.atlas = s.Mask
		if b.Color {
			e.atlas = s.Color
		}
		var ok bool
		if e.x, e.y, ok = e.atlas.put(b.W, b.H, b.Pix); !ok {
			e.atlas = nil
		}
	}
	s.glyphs[k] = e
	return e
}

// Paragraph is text without newlines. It is shaped once for a size and
// wrapped again only when its width changes, from the same shaped runs:
// resizing a window rewraps without reshaping.
type Paragraph struct {
	text    []rune
	size    float32
	runs    []shaping.Output
	width   int
	lines   []shaping.Line
	wrapper shaping.LineWrapper
}

func NewParagraph(text string) *Paragraph { return &Paragraph{text: []rune(text), width: -1} }

func (s *System) layout(p *Paragraph, size float32, width int) {
	if p.runs == nil || p.size != size {
		p.runs, p.size, p.width = s.eng.Shape(p.text, size), size, -1
		s.Stats.Shapes++
	}
	if p.width != width {
		// The wrapper zeroes the advance of each line's trailing space in
		// place, in glyphs it shares with the runs it is given: wrap a copy,
		// or a space trimmed at one width stays gone at the next.
		runs := make([]shaping.Output, len(p.runs))
		for i, r := range p.runs {
			r.Glyphs = slices.Clone(r.Glyphs)
			runs[i] = r
		}
		p.lines, _ = p.wrapper.WrapParagraphF(shaping.WrapConfig{}, fixed.I(width), p.text, shaping.NewSliceIterator(runs))
		p.width = width
		s.Stats.Wraps++
	}
}

// Draw lays p out at size pixels per em, wrapped to width pixels, with its
// first line's top at (x, y), and returns the height it took.
func (s *System) Draw(f *Frame, p *Paragraph, size float32, x, y, width int, c RGB) int {
	s.layout(p, size, width)
	if len(p.lines) == 0 {
		return int(math.Ceil(float64(size) * 1.25))
	}
	top := float32(y)
	for _, line := range p.lines {
		var asc, desc, gap fixed.Int26_6
		for _, r := range line {
			asc = max(asc, r.LineBounds.Ascent)
			desc = min(desc, r.LineBounds.Descent)
			gap = max(gap, r.LineBounds.Gap)
		}
		// Baselines snap to whole pixels; pens along them don't.
		baseline := float32(math.Round(float64(top + fx(asc))))
		runs := slices.Clone(line)
		slices.SortFunc(runs, func(a, b shaping.Output) int { return int(a.VisualIndex - b.VisualIndex) })
		pen := float32(x)
		for _, r := range runs {
			for _, g := range r.Glyphs {
				s.drawGlyph(f, r.Face, g.GlyphID, r.Size, pen+fx(g.XOffset), baseline-fx(g.YOffset), c)
				pen += fx(g.Advance)
			}
		}
		top += fx(asc - desc + gap)
	}
	return int(math.Ceil(float64(top))) - y
}

func (s *System) drawGlyph(f *Frame, face *font.Face, gid font.GID, size fixed.Int26_6, x, y float32, c RGB) {
	ix := int(math.Floor(float64(x)))
	sub := int(math.Round(float64(x-float32(ix)) * subpixels))
	if sub == subpixels {
		ix, sub = ix+1, 0
	}
	e := s.glyph(glyphKey{face: face, gid: gid, size: size, sub: uint8(sub)})
	s.Stats.FrameGlyphs++
	if e.atlas == nil {
		return
	}
	blit(f, e, ix+e.left, int(y)+e.top, c)
}

// blit composites a glyph from its atlas onto the frame at (dx, dy): a
// coverage mask tinted c, or a color glyph over what is there. A GPU
// renderer would draw the same thing as one textured quad.
func blit(f *Frame, e glyphEntry, dx, dy int, c RGB) {
	a := e.atlas
	for r := range e.h {
		fy := dy + r
		if fy < 0 || fy >= f.H {
			continue
		}
		for col := range e.w {
			fxp := dx + col
			if fxp < 0 || fxp >= f.W {
				continue
			}
			d := f.Pix[(fy*f.W+fxp)*4:]
			src := a.Pix[((e.y+r)*a.W+e.x+col)*a.BPP:]
			if !e.color {
				cov := uint32(src[0])
				if cov == 0 {
					continue
				}
				d[0] = lerp(d[0], c.B, cov)
				d[1] = lerp(d[1], c.G, cov)
				d[2] = lerp(d[2], c.R, cov)
				continue
			}
			sa := uint32(src[3])
			if sa == 0 {
				continue
			}
			inv := 255 - sa
			d[0] = uint8(uint32(src[2]) + uint32(d[0])*inv/255)
			d[1] = uint8(uint32(src[1]) + uint32(d[1])*inv/255)
			d[2] = uint8(uint32(src[0]) + uint32(d[2])*inv/255)
		}
	}
}

func lerp(d, s uint8, a uint32) uint8 {
	return uint8((uint32(d)*(255-a) + uint32(s)*a + 127) / 255)
}

func fx(v fixed.Int26_6) float32 { return float32(v) / 64 }
