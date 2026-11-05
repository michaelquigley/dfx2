// Package gotext is dfx2's pure-Go text engine: fontscan finds faces with
// per-rune fallback, go-text's HarfBuzz port shapes, x/image/vector
// rasterizes outlines with slight vertical hinting, and PNG color bitmaps are
// scaled. Every go-text type stays inside this package: shaping results are
// converted into owned text.Shaped data before they leave it.
package gotext

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"unicode"

	"github.com/go-text/typesetting/bidi"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/fontscan"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	"github.com/go-text/typesetting/shaping"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/text"
)

// Engine implements text.Engine. It is not safe for concurrent use: dfx2
// calls it only from the UI thread. Faces it loads stay loaded for the
// engine's lifetime; FontIDs are never reused.
type Engine struct {
	fonts  shaping.Fontmap
	family func(*font.Face) string
	seg    shaping.Segmenter
	shaper shaping.HarfbuzzShaper
	levels bidi.Paragraph
	lines  segmenter.Segmenter
	lang   language.Language
	ras    vector.Rasterizer
	faces  []*font.Face // index is FontID; 0 is unused
	ids    map[*font.Face]text.FontID
	zones  map[*font.Face]zones

	// Hinting enables slight hinting: vertical only, so advances don't change.
	Hinting bool
}

func newEngine(fonts shaping.Fontmap) *Engine {
	return &Engine{fonts: fonts, lang: language.DefaultLanguage(), faces: []*font.Face{nil},
		ids: make(map[*font.Face]text.FontID), zones: make(map[*font.Face]zones), Hinting: true}
}

// NewSystemFonts indexes the system's fonts (the index is cached under
// cacheDir) and prefers families in order, then the generic emoji family, as
// browsers do for emoji the preferred families lack.
func NewSystemFonts(families []string, cacheDir string) (*Engine, error) {
	fm := fontscan.NewFontMap(discard{})
	if err := fm.UseSystemFonts(cacheDir); err != nil {
		return nil, fmt.Errorf("gotext: system fonts: %w", err)
	}
	fm.SetQuery(fontscan.Query{Families: append(append([]string(nil), families...), fontscan.Emoji)})
	e := newEngine(fm)
	e.family = func(f *font.Face) string {
		family, _ := fm.FontMetadata(f.Font)
		return family
	}
	return e, nil
}

// DefaultCacheDir is the per-user location of the font index cache.
func DefaultCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dfx2", "fontscan"), nil
}

// NewFaces builds an engine over explicit font files in fallback order, for
// deterministic tests and pinned fixtures: no host font ordering applies.
func NewFaces(files ...[]byte) (*Engine, error) {
	var faces ordered
	for i, data := range files {
		face, err := font.ParseTTF(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("gotext: font %d: %w", i, err)
		}
		faces = append(faces, face)
	}
	if len(faces) == 0 {
		return nil, errors.New("gotext: no faces")
	}
	return newEngine(faces), nil
}

// ordered resolves each rune to the first face covering it, else the first.
type ordered []*font.Face

func (o ordered) ResolveFace(r rune) *font.Face {
	for _, f := range o {
		if _, ok := f.NominalGlyph(r); ok {
			return f
		}
	}
	return o[0]
}

type discard struct{}

func (discard) Printf(string, ...any) {}

func (e *Engine) id(f *font.Face) text.FontID {
	if id, ok := e.ids[f]; ok {
		return id
	}
	id := text.FontID(len(e.faces))
	e.faces = append(e.faces, f)
	e.ids[f] = id
	return id
}

// Family names the family of a face the engine has loaded, when known.
func (e *Engine) Family(id text.FontID) string {
	if int(id) <= 0 || int(id) >= len(e.faces) || e.family == nil {
		return ""
	}
	return e.family(e.faces[id])
}

func (e *Engine) Shape(runes []rune, size text.Fixed) (text.Shaped, error) {
	if size <= 0 {
		return text.Shaped{}, fmt.Errorf("gotext: size %v must be positive", size.Float())
	}
	primary := e.fonts.ResolveFace(' ')
	if primary == nil {
		return text.Shaped{}, errors.New("gotext: no font available")
	}
	sh := text.Shaped{Breaks: e.breaks(runes)}
	sh.Ascent, sh.Descent, sh.Gap = metrics(primary, size)
	if len(runes) == 0 {
		return sh, nil
	}
	levels := e.bidiLevels(runes)
	in := shaping.Input{Text: runes, RunStart: 0, RunEnd: len(runes), Direction: di.DirectionLTR,
		Size: fixed.Int26_6(size), Language: e.lang}
	for _, part := range spacesToPrimary(e.seg.Split(in, e.fonts), primary) {
		out := e.shaper.Shape(part)
		run := text.Run{Font: e.id(out.Face), Size: size, Level: levels[part.RunStart],
			Start: part.RunStart, End: part.RunEnd, Glyphs: make([]text.Glyph, len(out.Glyphs)),
			Ascent: text.Fixed(out.LineBounds.Ascent), Descent: text.Fixed(-out.LineBounds.Descent), Gap: text.Fixed(out.LineBounds.Gap)}
		// Copy into owned neutral glyphs: the shaper reuses its buffers and
		// go-text's wrapper is known to mutate glyph advances in place.
		for i, g := range out.Glyphs {
			run.Glyphs[i] = text.Glyph{ID: text.GlyphID(g.GlyphID), Cluster: g.TextIndex(), Runes: g.RunesCount(),
				Advance: text.Fixed(g.Advance), XOffset: text.Fixed(g.XOffset), YOffset: text.Fixed(g.YOffset)}
		}
		sh.Runs = append(sh.Runs, run)
	}
	return sh, nil
}

func metrics(f *font.Face, size text.Fixed) (ascent, descent, gap text.Fixed) {
	ext, ok := f.FontHExtents()
	if !ok {
		return text.FixedOf(size.Float() * 0.9), text.FixedOf(size.Float() * 0.25), 0
	}
	s := size.Float() / float32(f.Upem())
	return text.FixedOf(ext.Ascender * s), text.FixedOf(-ext.Descender * s), text.FixedOf(ext.LineGap * s)
}

// bidiLevels resolves each rune's embedding level in a left-to-right paragraph.
func (e *Engine) bidiLevels(runes []rune) []uint8 {
	levels := make([]uint8, len(runes)+1)
	runs := e.levels.Segment(runes, bidi.LeftToRight)
	for i := range runs.NumRuns() {
		r := runs.Run(i)
		for j := r.Start; j < r.End && j < len(runes); j++ {
			levels[j] = uint8(r.Level)
		}
	}
	return levels
}

// breaks classifies every rune boundary with UAX #14 line breaking.
func (e *Engine) breaks(runes []rune) []text.Break {
	out := make([]text.Break, len(runes)+1)
	if len(runes) == 0 {
		return out
	}
	e.lines.Init(runes)
	it := e.lines.LineIterator()
	for it.Next() {
		l := it.Line()
		end := l.Offset + len(l.Text)
		if l.IsMandatoryBreak {
			out[end] = text.MustBreak
		} else {
			out[end] = text.CanBreak
		}
	}
	return out
}

// spacesToPrimary moves runs of spaces out of runs in fallback fonts into the
// preferred font. go-text, like Pango, never changes fonts for a space, so a
// space after an emoji takes the emoji font's, which in Noto Color Emoji is
// wider than an em.
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

func (e *Engine) Rasterize(id text.FontID, gid text.GlyphID, size text.Fixed, subpixel int) (text.Bitmap, error) {
	if int(id) <= 0 || int(id) >= len(e.faces) {
		return text.Bitmap{}, fmt.Errorf("gotext: unknown font %d", id)
	}
	if subpixel < 0 || subpixel >= text.Subpixels {
		return text.Bitmap{}, fmt.Errorf("gotext: subpixel %d out of range", subpixel)
	}
	face := e.faces[id]
	s := size.Float() / float32(face.Upem())
	ys := vmap{scale: s, plain: true}
	if e.Hinting {
		z, ok := e.zones[face]
		if !ok {
			z = zonesOf(face)
			e.zones[face] = z
		}
		ys = newVmap(z, s)
	}
	dx := float32(subpixel) / text.Subpixels
	return e.raster(face, font.GID(gid), face.GlyphData(font.GID(gid)), s, ys, dx)
}

// raster dispatches on the glyph's data format. Formats this engine cannot
// draw (COLR paint, non-PNG bitmaps, SVG without a fallback outline) become
// a visible placeholder box instead of silently blank output.
func (e *Engine) raster(face *font.Face, gid font.GID, data font.GlyphData, s float32, ys vmap, dx float32) (text.Bitmap, error) {
	switch d := data.(type) {
	case font.GlyphOutline:
		return e.outline(d.Segments, s, ys, dx)
	case font.GlyphSVG:
		if len(d.Outline.Segments) != 0 {
			return e.outline(d.Outline.Segments, s, ys, dx)
		}
	case font.GlyphBitmap:
		if d.Format == font.PNG {
			if b, err := pngBitmap(face, gid, d, s); err == nil {
				return b, nil
			}
		}
	}
	return placeholder(face, gid, s)
}

func checkSize(w, h, bpp int) error {
	if w <= 0 || h <= 0 || w > platform.MaxImageBytes/bpp/h {
		return fmt.Errorf("gotext: glyph bitmap %dx%d exceeds the image limit", w, h)
	}
	return nil
}

// outline fills a glyph's contours, in font units with y up, at s pixels a
// unit across and as ys maps them up, its origin dx right of a pixel's edge.
func (e *Engine) outline(segs []font.Segment, s float32, ys vmap, dx float32) (text.Bitmap, error) {
	if len(segs) == 0 {
		return text.Bitmap{}, nil
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
		return text.Bitmap{}, nil
	}
	if err := checkSize(w, h, 1); err != nil {
		return text.Bitmap{}, err
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
			ex, ey := at(a[2])
			e.ras.CubeTo(bx, by, cx, cy, ex, ey)
		}
	}
	if open {
		e.ras.ClosePath()
	}
	dst := image.NewAlpha(image.Rect(0, 0, w, h))
	e.ras.Draw(dst, dst.Bounds(), image.Opaque, image.Point{})
	return text.Bitmap{Left: x0, Top: y0, Width: w, Height: h, Pix: dst.Pix}, nil
}

// pngBitmap scales a color glyph's PNG (Noto Color Emoji's CBDT strikes) to
// the glyph's extents. image.RGBA is premultiplied, as text.Bitmap requires.
func pngBitmap(face *font.Face, gid font.GID, b font.GlyphBitmap, s float32) (text.Bitmap, error) {
	ext, ok := face.GlyphExtents(gid)
	if !ok {
		return text.Bitmap{}, errors.New("no extents")
	}
	left, top := round(ext.XBearing*s), round(-ext.YBearing*s)
	w, h := round(ext.Width*s), round(-ext.Height*s)
	if err := checkSize(w, h, 4); err != nil {
		return text.Bitmap{}, err
	}
	src, err := png.Decode(bytes.NewReader(b.Data))
	if err != nil {
		return text.Bitmap{}, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return text.Bitmap{Left: left, Top: top, Width: w, Height: h, Pix: dst.Pix, Color: true}, nil
}

// placeholder draws a hollow box the glyph's advance wide and its x-height to
// cap-height tall, so unsupported data is visible rather than blank.
func placeholder(face *font.Face, gid font.GID, s float32) (text.Bitmap, error) {
	advance := face.HorizontalAdvance(gid) * s
	height := face.LineMetric(font.CapHeight) * s
	if height <= 0 {
		height = float32(face.Upem()) * 0.7 * s
	}
	w, h := max(round(advance*0.8), 3), max(round(height), 3)
	if err := checkSize(w, h, 1); err != nil {
		return text.Bitmap{}, err
	}
	stroke := max(1, round(float32(h)/12))
	pix := make([]byte, w*h)
	for y := range h {
		for x := range w {
			if x < stroke || y < stroke || x >= w-stroke || y >= h-stroke {
				pix[y*w+x] = 0xff
			}
		}
	}
	return text.Bitmap{Left: max(round(advance*0.1), 0), Top: -h, Width: w, Height: h, Pix: pix, Fallback: true}, nil
}

func round(v float32) int { return int(math.Round(float64(v))) }

// GlyphKind names a glyph's data format, for audition diagnostics.
func (e *Engine) GlyphKind(id text.FontID, gid text.GlyphID) string {
	if int(id) <= 0 || int(id) >= len(e.faces) {
		return "unknown font"
	}
	switch d := e.faces[id].GlyphData(font.GID(gid)).(type) {
	case font.GlyphOutline:
		if len(d.Segments) == 0 {
			return "empty outline"
		}
		return "outline"
	case font.GlyphSVG:
		return "svg"
	case font.GlyphBitmap:
		if d.Format == font.PNG {
			return "png bitmap"
		}
		return "other bitmap (placeholder)"
	case font.GlyphColor:
		return "colr paint (placeholder)"
	case nil:
		return "no data"
	}
	return "other"
}
