// Package text is dfx2's shared text system. An Engine finds fonts, shapes a
// paragraph and rasterizes single glyphs; everything above it (line breaking,
// bidi line ordering, glyph positioning and the bounded per-window caches) is
// shared code that does not change with the engine.
//
// The data crossing the Engine seam is engine-neutral and owned: no font
// library types, and no buffers a third-party shaper may later mutate. There
// is one engine; this is not a registration mechanism.
package text

import (
	"errors"
	"math"
)

// Fixed is a 26.6 fixed-point length in physical pixels.
type Fixed int32

func (f Fixed) Float() float32 { return float32(f) / 64 }

// FixedOf rounds a pixel length to 26.6 fixed point.
func FixedOf(v float32) Fixed { return Fixed(math.Round(float64(v) * 64)) }

// FontID names a face loaded by an engine. IDs are never reused by an engine,
// so a cached raster can never be attributed to a different face. Zero is none.
type FontID uint32

// GlyphID is a glyph index within a face.
type GlyphID uint32

// Glyph is one shaped glyph.
type Glyph struct {
	ID GlyphID
	// Cluster is the first rune of the glyph's cluster; Runes counts the runes
	// shaped into it. Every glyph of one cluster carries the same values.
	Cluster, Runes   int
	Advance          Fixed
	XOffset, YOffset Fixed // y up, as fonts measure
}

// Run is a maximal span shaped with one face, direction and bidi level.
type Run struct {
	Font  FontID
	Size  Fixed // pixels per em
	Level uint8 // bidi embedding level; odd levels run right to left
	// Start and End delimit the run's runes, [Start, End), in logical order.
	Start, End int
	// Glyphs are in visual (left-to-right drawing) order.
	Glyphs []Glyph
	// Line metrics: Ascent above and Descent below the baseline, both positive.
	Ascent, Descent, Gap Fixed
}

// Break classifies the boundary before a rune.
type Break uint8

const (
	NoBreak Break = iota
	CanBreak
	MustBreak
)

// Shaped is one paragraph shaped at one size: runs in logical order, break
// opportunities, and the primary face's metrics for an empty line.
type Shaped struct {
	Runs []Run
	// Breaks has len(text)+1 entries; Breaks[i] applies before rune i.
	Breaks               []Break
	Ascent, Descent, Gap Fixed
}

// Bitmap is a rasterized glyph. Left and Top place its top-left pixel relative
// to the pen's whole pixel on the baseline, y down. Pix is coverage, a byte a
// pixel, or premultiplied RGBA when Color. Fallback marks a visible placeholder
// drawn for a glyph whose data format the engine cannot render.
type Bitmap struct {
	Left, Top, Width, Height int
	Pix                      []byte
	Color, Fallback          bool
}

// Engine is the seam between the shared text system and a font stack. An
// engine is used from one goroutine at a time (the UI thread).
type Engine interface {
	// Shape itemizes text (one paragraph, no newlines) by bidi level, script
	// and font, with per-rune fallback, and shapes it at size pixels per em.
	// The result is owned by the caller.
	Shape(text []rune, size Fixed) (Shaped, error)
	// Rasterize draws one glyph with its origin subpixel/Subpixels pixels right
	// of a whole pixel. The result is owned by the caller.
	Rasterize(font FontID, glyph GlyphID, size Fixed, subpixel int) (Bitmap, error)
}

// Subpixels is the number of horizontal glyph positions within a pixel. Pens
// round to the nearest quarter; baselines stay on whole physical pixels.
const Subpixels = 4

var errTransientLimit = errors.New("text: transient scratch exceeded its limit")
