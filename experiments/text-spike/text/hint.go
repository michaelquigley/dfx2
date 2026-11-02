package text

import (
	"math"
	"slices"

	"github.com/go-text/typesetting/font"
)

// zone is an alignment zone in font units, y up: a flat edge many glyphs
// share (the baseline, the x-height, ...), and how far round glyphs
// overshoot it, up for tops and down for bottoms.
type zone struct{ ref, over float32 }

// zones are a font's alignment zones, and the x-height's, which sets the
// vertical scale.
type zones struct {
	x   zone
	all []zone
}

func zonesOf(f *font.Face) zones {
	ext := func(r rune) (top, bottom float32, ok bool) {
		gid, ok := f.NominalGlyph(r)
		if !ok {
			return 0, 0, false
		}
		e, ok := f.GlyphExtents(gid)
		return e.YBearing, e.YBearing + e.Height, ok
	}
	var z zones
	xh := f.LineMetric(font.XHeight)
	if t, _, ok := ext('x'); xh <= 0 && ok {
		xh = t
	}
	if xh <= 0 {
		return z
	}
	z.x = zone{ref: xh}
	base := zone{}
	if t, b, ok := ext('o'); ok {
		z.x.over = max(t-xh, 0)
		base.over = min(b, 0)
	}
	z.all = append(z.all, base, z.x)
	cap := f.LineMetric(font.CapHeight)
	if t, _, ok := ext('H'); cap <= 0 && ok {
		cap = t
	}
	if cap > xh {
		c := zone{ref: cap}
		if t, _, ok := ext('O'); ok {
			c.over = max(t-cap, 0)
		}
		z.all = append(z.all, c)
		if t, _, ok := ext('d'); ok && t > cap*1.02 {
			z.all = append(z.all, zone{ref: t}) // ascenders, where taller than capitals
		}
	}
	if _, b, ok := ext('p'); ok && b < 0 {
		z.all = append(z.all, zone{ref: b}) // descenders
	}
	return z
}

// vmap maps a glyph's y coordinates, in font units with y up, to pixels
// with y up, as slight hinting does: vertically only, so advances and line
// widths don't change. After FreeType's autohinter in light mode:
//
//   - one vertical scale for the whole font at a size, chosen so the top
//     of the x-height's overshoot lands on a whole pixel, rounding up from
//     0.375 of a pixel (lowercase reads larger);
//   - each zone's edge rounded to a whole pixel, and its overshoot to 0
//     below half a pixel, to half a pixel below three quarters, else to 1;
//   - everything between interpolated linearly, and beyond, scaled.
//
// It does not find and snap a glyph's own horizontal stems (the bar of an
// e, a dash), which the autohinter does too.
type vmap struct {
	scale float32  // pixels a font unit, hinted
	pts   []vmapPt // in increasing order of both
	plain bool     // no hinting: y·scale
}

type vmapPt struct{ unit, px float32 }

func newVmap(z zones, s float32) vmap {
	if z.x.ref <= 0 {
		return vmap{scale: s, plain: true}
	}
	scaled := (z.x.ref + z.x.over) * s
	fitted := float32(math.Floor(float64(scaled) + 40.0/64))
	if fitted < 1 {
		return vmap{scale: s, plain: true}
	}
	m := vmap{scale: s * fitted / scaled}
	for _, zn := range z.all {
		ref := float32(math.Round(float64(zn.ref * m.scale)))
		m.pts = append(m.pts, vmapPt{zn.ref, ref})
		if zn.over == 0 {
			continue
		}
		d := float32(math.Abs(float64(zn.over * m.scale)))
		switch {
		case d < 0.5:
			d = 0
		case d < 0.75:
			d = 0.5
		default:
			d = 1
		}
		if zn.over < 0 {
			d = -d
		}
		m.pts = append(m.pts, vmapPt{zn.ref + zn.over, ref + d})
	}
	slices.SortFunc(m.pts, func(a, b vmapPt) int {
		if a.unit < b.unit {
			return -1
		}
		if a.unit > b.unit {
			return 1
		}
		return 0
	})
	// Keep the map monotonic: drop points that would fold it.
	kept := m.pts[:0]
	for _, p := range m.pts {
		if n := len(kept); n > 0 && (p.unit <= kept[n-1].unit || p.px < kept[n-1].px) {
			continue
		}
		kept = append(kept, p)
	}
	m.pts = kept
	return m
}

func (m vmap) y(y float32) float32 {
	if m.plain || len(m.pts) == 0 {
		return y * m.scale
	}
	first, last := m.pts[0], m.pts[len(m.pts)-1]
	if y <= first.unit {
		return first.px + (y-first.unit)*m.scale
	}
	if y >= last.unit {
		return last.px + (y-last.unit)*m.scale
	}
	i, _ := slices.BinarySearchFunc(m.pts, y, func(p vmapPt, y float32) int {
		if p.unit < y {
			return -1
		}
		if p.unit > y {
			return 1
		}
		return 0
	})
	a, b := m.pts[i-1], m.pts[i]
	if b.unit == y {
		return b.px
	}
	return a.px + (y-a.unit)*(b.px-a.px)/(b.unit-a.unit)
}
