package text

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/math/fixed"
)

func lineAdvances(p *Paragraph) []fixed.Int26_6 {
	var out []fixed.Int26_6
	for _, l := range p.lines {
		var a fixed.Int26_6
		for _, r := range l {
			for _, g := range r.Glyphs {
				a += g.Advance
			}
		}
		out = append(out, a)
	}
	return out
}

// Rewrapping at another width must lay out as a paragraph wrapped there
// first: spaces trimmed at the ends of lines keep their advance elsewhere.
func TestRewrapKeepsSpaces(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewGoText([]string{"sans-serif"}, filepath.Join(cache, "dfx2-text-spike"))
	if err != nil {
		t.Fatal(err)
	}
	const txt = "the quick brown fox jumps over the lazy dog again and again"
	s := NewSystem(eng)

	p := NewParagraph(txt)
	for _, w := range []int{120, 250, 80, 10000} {
		s.layout(p, 20, w)
	}
	fresh := NewParagraph(txt)
	s.layout(fresh, 20, 10000)

	got, want := lineAdvances(p), lineAdvances(fresh)
	if len(got) != 1 || len(want) != 1 || got[0] != want[0] {
		t.Fatalf("after rewrapping, lines advance %v; wrapped once, %v", got, want)
	}
}
