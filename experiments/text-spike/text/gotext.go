package text

import (
	"bytes"
	"image"
	"image/png"
	"log"
	"math"
	"os"
	"unicode"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/fontscan"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// GoText is an Engine in pure Go: fontscan indexes the system's fonts
// (cached on disk after the first run) and picks fallbacks, go-text's
// HarfBuzz port shapes, and x/image/vector rasterizes outlines.
type GoText struct {
	fonts  *fontscan.FontMap
	seg    shaping.Segmenter
	shaper shaping.HarfbuzzShaper
	lang   language.Language
	ras    vector.Rasterizer

	// Hinting turns on slight hinting: vertical only (see vmap).
	Hinting bool
	zones   map[*font.Face]zones
}

// NewGoText indexes the system's fonts, caching the index in cacheDir, and
// prefers families in order, then the system's emoji font, as browsers
// do for emoji the preferred families lack.
func NewGoText(families []string, cacheDir string) (*GoText, error) {
	fm := fontscan.NewFontMap(log.New(os.Stderr, "fontscan: ", 0))
	if err := fm.UseSystemFonts(cacheDir); err != nil {
		return nil, err
	}
	fm.SetQuery(fontscan.Query{Families: append(families, fontscan.Emoji)})
	return &GoText{fonts: fm, lang: language.DefaultLanguage(), zones: map[*font.Face]zones{}}, nil
}

func (e *GoText) Shape(text []rune, size float32) []shaping.Output {
	in := shaping.Input{
		Text:      text,
		RunEnd:    len(text),
		Direction: di.DirectionLTR,
		Size:      fixed.Int26_6(size * 64),
		Language:  e.lang,
	}
	parts := spacesToPrimary(e.seg.Split(in, e.fonts), e.fonts.ResolveFace(' '))
	out := make([]shaping.Output, len(parts))
	for i, p := range parts {
		out[i] = e.shaper.Shape(p)
	}
	return out
}

// spacesToPrimary moves runs of spaces out of runs in fallback fonts into
// the preferred font. go-text, like Pango, never changes fonts for a space,
// so a space after an emoji takes the emoji font's, which in Noto Color
// Emoji is wider than an em.
func spacesToPrimary(parts []shaping.Input, primary *font.Face) []shaping.Input {
	var out []shaping.Input
	for _, p := range parts {
		if p.Face == primary {
			out = append(out, p)
			continue
		}
		start := p.RunStart
		for i := p.RunStart; i <= p.RunEnd; i++ {
			space := i < p.RunEnd && unicode.Is(unicode.Zs, p.Text[i])
			startSpace := start < p.RunEnd && unicode.Is(unicode.Zs, p.Text[start])
			if i < p.RunEnd && space == startSpace {
				continue
			}
			if i > start {
				q := p
				q.RunStart, q.RunEnd = start, i
				if startSpace {
					q.Face = primary
				}
				out = append(out, q)
			}
			start = i
		}
	}
	return out
}

func (e *GoText) Glyph(face *font.Face, gid font.GID, size, dx float32) Bitmap {
	s := size / float32(face.Upem())
	ys := vmap{scale: s, plain: true}
	if e.Hinting {
		z, ok := e.zones[face]
		if !ok {
			z = zonesOf(face)
			e.zones[face] = z
		}
		ys = newVmap(z, s)
	}
	switch d := face.GlyphData(gid).(type) {
	case font.GlyphOutline:
		return e.outline(d.Segments, s, ys, dx)
	case font.GlyphSVG:
		return e.outline(d.Outline.Segments, s, ys, dx)
	case font.GlyphBitmap:
		return bitmap(face, gid, d, s)
	}
	return Bitmap{}
}

// outline fills a glyph's contours, in font units with y up, at s pixels a
// unit across and as ys maps them up, its origin dx right of a pixel's edge.
func (e *GoText) outline(segs []font.Segment, s float32, ys vmap, dx float32) Bitmap {
	if len(segs) == 0 {
		return Bitmap{}
	}
	minX, minY := float32(math.Inf(1)), float32(math.Inf(1))
	maxX, maxY := float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, sg := range segs {
		for _, p := range sg.ArgsSlice() {
			x, y := p.X*s+dx, -ys.y(p.Y)
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
		}
	}
	x0, y0 := int(math.Floor(float64(minX))), int(math.Floor(float64(minY)))
	x1, y1 := int(math.Ceil(float64(maxX))), int(math.Ceil(float64(maxY)))
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return Bitmap{}
	}
	at := func(p font.SegmentPoint) (float32, float32) {
		return p.X*s + dx - float32(x0), -ys.y(p.Y) - float32(y0)
	}
	e.ras.Reset(w, h)
	open := false
	for _, sg := range segs {
		a := sg.Args
		switch sg.Op {
		case ot.SegmentOpMoveTo:
			if open {
				e.ras.ClosePath()
			}
			e.ras.MoveTo(at(a[0]))
			open = true
		case ot.SegmentOpLineTo:
			e.ras.LineTo(at(a[0]))
		case ot.SegmentOpQuadTo:
			bx, by := at(a[0])
			cx, cy := at(a[1])
			e.ras.QuadTo(bx, by, cx, cy)
		case ot.SegmentOpCubeTo:
			bx, by := at(a[0])
			cx, cy := at(a[1])
			dx, dy := at(a[2])
			e.ras.CubeTo(bx, by, cx, cy, dx, dy)
		}
	}
	if open {
		e.ras.ClosePath()
	}
	dst := image.NewAlpha(image.Rect(0, 0, w, h))
	e.ras.Draw(dst, dst.Bounds(), image.Opaque, image.Point{})
	return Bitmap{Left: x0, Top: y0, W: w, H: h, Pix: dst.Pix}
}

// bitmap scales a color glyph's PNG (Noto Color Emoji's CBDT strikes) to
// the glyph's extents at s pixels a unit.
func bitmap(face *font.Face, gid font.GID, b font.GlyphBitmap, s float32) Bitmap {
	if b.Format != font.PNG {
		return Bitmap{}
	}
	ext, ok := face.GlyphExtents(gid)
	if !ok {
		return Bitmap{}
	}
	src, err := png.Decode(bytes.NewReader(b.Data))
	if err != nil {
		return Bitmap{}
	}
	left := round(ext.XBearing * s)
	top := round(-ext.YBearing * s)
	w, h := round(ext.Width*s), round(-ext.Height*s)
	if w <= 0 || h <= 0 {
		return Bitmap{}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return Bitmap{Left: left, Top: top, W: w, H: h, Pix: dst.Pix, Color: true}
}

func round(v float32) int { return int(math.Round(float64(v))) }

// Family names the font the engine resolves Latin text to.
func (e *GoText) Family() string {
	fam, _ := e.fonts.FontMetadata(e.fonts.ResolveFace('a').Font)
	return fam
}
