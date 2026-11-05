package text

import (
	"slices"
	"unicode"
)

// Line is one wrapped line, positioned left to right from its left edge.
type Line struct {
	Start, End           int   // logical rune range
	Width                Fixed // pen advance, trailing whitespace excluded
	Ascent, Descent, Gap Fixed
	Glyphs               []Placed
}

// Placed is a glyph positioned on a line: X is its origin from the line's
// left edge with its shaping offset applied, Y its vertical offset, y up.
type Placed struct {
	Font    FontID
	ID      GlyphID
	Size    Fixed
	X, Y    Fixed
	Cluster int
}

func isSpace(r rune) bool { return unicode.Is(unicode.White_Space, r) }

// wrap breaks shaped text into lines no wider than width where it can,
// without reshaping. It reads sh and never writes it: wrapping again at
// another width starts from the same untouched shaped data. A word wider than
// width overflows its line (and is clipped by the caller) rather than being
// broken mid-word.
func wrap(text []rune, sh *Shaped, width Fixed) []Line {
	n := len(text)
	if n == 0 {
		return []Line{{Ascent: sh.Ascent, Descent: sh.Descent, Gap: sh.Gap}}
	}
	// Advance per cluster, attributed to the cluster's first rune.
	advance := make([]Fixed, n)
	for _, r := range sh.Runs {
		for _, g := range r.Glyphs {
			if g.Cluster >= 0 && g.Cluster < n {
				advance[g.Cluster] += g.Advance
			}
		}
	}
	breakAt := func(i int) Break {
		if i < len(sh.Breaks) {
			return sh.Breaks[i]
		}
		return NoBreak
	}
	var lines []Line
	for start := 0; start < n; {
		end, pen, opportunity := n, Fixed(0), -1
		for i := start; i < n; i++ {
			if i > start && breakAt(i) == MustBreak {
				end = i
				break
			}
			if i > start && breakAt(i) != NoBreak {
				opportunity = i
			}
			pen += advance[i]
			// Only visible content overflows: trailing spaces hang.
			if !isSpace(text[i]) && pen > width && opportunity > start {
				end = opportunity
				break
			}
		}
		lines = append(lines, line(text, sh, start, end))
		start = end
	}
	return lines
}

// line assembles runes [start, end) in visual order. Runs split at the line's
// edges keep their glyphs' order; runs are reordered by the UAX #9 L2 rule
// from their embedding levels. Trailing whitespace keeps its glyphs but takes
// no advance, so it neither widens the line nor shifts right-to-left text.
func line(text []rune, sh *Shaped, start, end int) Line {
	trailing := end
	for trailing > start && isSpace(text[trailing-1]) {
		trailing--
	}
	type part struct {
		run    *Run
		glyphs []Glyph
	}
	var parts []part
	for i := range sh.Runs {
		r := &sh.Runs[i]
		if r.End <= start || r.Start >= end {
			continue
		}
		var glyphs []Glyph
		for _, g := range r.Glyphs {
			if g.Cluster >= start && g.Cluster < end {
				glyphs = append(glyphs, g)
			}
		}
		if len(glyphs) > 0 {
			parts = append(parts, part{r, glyphs})
		}
	}
	// L2: from the highest level down to the lowest odd level, reverse every
	// maximal sequence of parts at that level or higher.
	var highest, lowestOdd uint8 = 0, 255
	for _, p := range parts {
		highest = max(highest, p.run.Level)
		if p.run.Level%2 == 1 {
			lowestOdd = min(lowestOdd, p.run.Level)
		}
	}
	for level := highest; level >= lowestOdd && level > 0; level-- {
		for i := 0; i < len(parts); {
			if parts[i].run.Level < level {
				i++
				continue
			}
			j := i
			for j < len(parts) && parts[j].run.Level >= level {
				j++
			}
			slices.Reverse(parts[i:j])
			i = j
		}
	}
	l := Line{Start: start, End: end}
	if len(parts) == 0 {
		l.Ascent, l.Descent, l.Gap = sh.Ascent, sh.Descent, sh.Gap
	}
	var pen Fixed
	for _, p := range parts {
		l.Ascent, l.Descent, l.Gap = max(l.Ascent, p.run.Ascent), max(l.Descent, p.run.Descent), max(l.Gap, p.run.Gap)
		for _, g := range p.glyphs {
			l.Glyphs = append(l.Glyphs, Placed{Font: p.run.Font, ID: g.ID, Size: p.run.Size,
				X: pen + g.XOffset, Y: g.YOffset, Cluster: g.Cluster})
			if g.Cluster < trailing {
				pen += g.Advance
			}
		}
	}
	l.Width = pen
	return l
}
