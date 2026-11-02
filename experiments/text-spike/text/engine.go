// Package text is a spike of dfx2's text system: an engine finds fonts
// (falling back per rune), shapes and rasterizes single glyphs; everything
// above it (wrapping shaped runs to a width, line assembly, the glyph cache
// and its atlases, compositing) is shared code that would not change if a
// native engine (Pango, DirectWrite) took the go-text engine's place.
//
// The spike lets go-text's shaping.Output cross the seam to stay small; a
// real dfx2 would define its own run and glyph types there.
package text

import (
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/shaping"
)

// Engine is the seam between the text system and the font stack.
type Engine interface {
	// Shape splits text (one paragraph) into runs by bidi level, script and
	// font, resolving a font for each rune the preferred one lacks, and
	// shapes each run at size pixels per em.
	Shape(text []rune, size float32) []shaping.Output
	// Glyph rasterizes glyph gid of face at size pixels per em, with its
	// origin dx pixels (0 ≤ dx < 1) right of a pixel's left edge.
	Glyph(face *font.Face, gid font.GID, size, dx float32) Bitmap
}

// Bitmap is a rasterized glyph.
type Bitmap struct {
	// Left and Top place the bitmap's top-left pixel relative to the pen's
	// whole pixel on the baseline, y down.
	Left, Top int
	W, H      int
	// Pix is coverage, a byte a pixel, or premultiplied RGBA for color
	// glyphs (emoji).
	Pix   []byte
	Color bool
}
