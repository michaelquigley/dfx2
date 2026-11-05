package gotext

import (
	"bytes"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/go-text/typesetting/font"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/michaelquigley/dfx2/internal/text"
)

// fixtures loads pinned faces in a fixed fallback order: Go Regular, then
// Noto Sans Hebrew. See ../testdata/README.md for provenance and licenses.
func fixtures(t *testing.T, extra ...[]byte) *Engine {
	t.Helper()
	hebrew, err := os.ReadFile("../testdata/NotoSansHebrew-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewFaces(append([][]byte{goregular.TTF, hebrew}, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

const size = text.Fixed(20 * 64)

func must(lines []text.Line, err error) []text.Line {
	if err != nil {
		panic(err)
	}
	return lines
}

func lineAdvances(lines []text.Line) []text.Fixed {
	var out []text.Fixed
	for _, l := range lines {
		out = append(out, l.Width)
	}
	return out
}

// Rewrapping at another width must lay out as a paragraph wrapped there
// first: spaces trimmed at the ends of lines keep their advance elsewhere,
// and changing width never reshapes.
func TestRewrapKeepsSpacesWithoutReshaping(t *testing.T) {
	s := text.NewSystem(fixtures(t), text.DefaultBudget)
	const txt = "the quick brown fox jumps over the lazy dog again and again"
	s.BeginFrame()
	defer s.EndFrame()
	p, err := s.Paragraph(txt, size)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []int{120, 250, 80, 10000} {
		if lines := must(s.Lines(p, text.Fixed(w*64))); w < 10000 && len(lines) < 2 {
			t.Fatalf("width %d did not wrap", w)
		}
	}
	fresh := text.NewSystem(fixtures(t), text.DefaultBudget)
	fresh.BeginFrame()
	defer fresh.EndFrame()
	q, _ := fresh.Paragraph(txt, size)
	got, want := lineAdvances(must(s.Lines(p, 10000*64))), lineAdvances(must(fresh.Lines(q, 10000*64)))
	if len(got) != 1 || !slices.Equal(got, want) {
		t.Fatalf("after rewrapping, lines advance %v; wrapped once, %v", got, want)
	}
	if st := s.Stats(); st.Shapes != 1 || st.Wraps != 4 {
		t.Fatalf("shapes=%d wraps=%d", st.Shapes, st.Wraps)
	}
}

func TestFallbackAndSpacesReturnToThePrimaryFace(t *testing.T) {
	e := fixtures(t)
	sh, err := e.Shape([]rune("ab שלום cd"), size)
	if err != nil {
		t.Fatal(err)
	}
	primary := sh.Runs[0].Font
	fonts := map[int]text.FontID{}
	for _, r := range sh.Runs {
		for _, g := range r.Glyphs {
			fonts[g.Cluster] = r.Font
		}
	}
	for i := 3; i < 7; i++ { // the Hebrew letters
		if fonts[i] == primary {
			t.Fatalf("rune %d not resolved to the fallback face", i)
		}
	}
	// The space after the fallback run would otherwise take the fallback
	// face's space; it returns to the preferred face.
	if fonts[7] != primary {
		t.Fatalf("space after a fallback run in font %d, want primary %d", fonts[7], primary)
	}
}

// Every rune belongs to exactly one cluster, clusters survive line splits,
// and right-to-left runs are reversed on the line.
func TestMixedDirectionPreservesClustersAndVisualOrder(t *testing.T) {
	s := text.NewSystem(fixtures(t), text.DefaultBudget)
	s.BeginFrame()
	defer s.EndFrame()
	const txt = "abc שלום עולם def"
	p, err := s.Paragraph(txt, size)
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{10000, 90} {
		covered := map[int]int{}
		for _, l := range must(s.Lines(p, text.Fixed(width*64))) {
			var clusters []int
			for _, g := range l.Glyphs {
				if len(clusters) == 0 || clusters[len(clusters)-1] != g.Cluster {
					clusters = append(clusters, g.Cluster)
				}
			}
			for _, c := range clusters {
				covered[c]++
			}
			if width == 10000 {
				// Visual order: abc, the Hebrew words right to left, def.
				hebrew := clusters[4 : len(clusters)-4]
				if !slices.IsSortedFunc(hebrew, func(a, b int) int { return b - a }) || hebrew[0] < hebrew[len(hebrew)-1] {
					t.Fatalf("Hebrew clusters not reversed: %v", clusters)
				}
				if clusters[0] != 0 || clusters[len(clusters)-1] != len([]rune(txt))-1 {
					t.Fatalf("LTR ends misplaced: %v", clusters)
				}
			}
		}
		for i := range len([]rune(txt)) {
			if covered[i] != 1 {
				t.Fatalf("width %d: rune %d covered %d times (%v)", width, i, covered[i], covered)
			}
		}
	}
}

func TestBreakOpportunitiesFollowUAX14(t *testing.T) {
	sh, err := fixtures(t).Shape([]rune("one two-three"), size)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]text.Break{4: text.CanBreak, 8: text.CanBreak, 13: text.MustBreak}
	for i, b := range sh.Breaks {
		if b != want[i] {
			t.Fatalf("break before %d = %d, want %d (%v)", i, b, want[i], sh.Breaks)
		}
	}
}

// The spike's slight hinting: one vertical scale per size that lands the top
// of the x-height's overshoot on a whole pixel, and zone edges on whole pixels.
func TestSlightHintingSnapsZonesToWholePixels(t *testing.T) {
	face, err := font.ParseTTF(bytes.NewReader(goregular.TTF))
	if err != nil {
		t.Fatal(err)
	}
	z := zonesOf(face)
	if z.x.ref <= 0 || len(z.all) < 3 {
		t.Fatalf("zones %+v", z)
	}
	for _, px := range []float32{11, 13, 14.67, 18, 24} {
		s := px / float32(face.Upem())
		m := newVmap(z, s)
		if m.plain {
			t.Fatalf("%v px: hinting disabled", px)
		}
		top := m.y(z.x.ref + z.x.over)
		if math.Abs(float64(top)-math.Round(float64(top))) > 1e-3 {
			t.Fatalf("%v px: x-height overshoot top at %v", px, top)
		}
		for _, zn := range z.all {
			if v := m.y(zn.ref); math.Abs(float64(v)-math.Round(float64(v))) > 1e-3 {
				t.Fatalf("%v px: zone %v at %v", px, zn.ref, v)
			}
		}
		for i := 1; i < len(m.pts); i++ {
			if m.pts[i].px < m.pts[i-1].px {
				t.Fatalf("%v px: vertical map folds: %+v", px, m.pts)
			}
		}
	}
	// Hinting moves no advances: rasters differ only vertically.
	e := fixtures(t)
	sh, _ := e.Shape([]rune("x"), size)
	g := sh.Runs[0].Glyphs[0]
	hinted, _ := e.Rasterize(sh.Runs[0].Font, g.ID, size, 0)
	e.Hinting = false
	plain, _ := e.Rasterize(sh.Runs[0].Font, g.ID, size, 0)
	if hinted.Width != plain.Width || hinted.Left != plain.Left {
		t.Fatalf("hinting changed horizontal extents: %+v vs %+v", hinted, plain)
	}
}

func TestSubpixelPositionsAreDistinctRasters(t *testing.T) {
	e := fixtures(t)
	sh, _ := e.Shape([]rune("l"), size)
	g := sh.Runs[0].Glyphs[0]
	var rasters [][]byte
	for sub := range text.Subpixels {
		b, err := e.Rasterize(sh.Runs[0].Font, g.ID, size, sub)
		if err != nil || b.Width == 0 || b.Color {
			t.Fatalf("subpixel %d: %+v %v", sub, b, err)
		}
		rasters = append(rasters, b.Pix)
	}
	if slices.Equal(rasters[0], rasters[2]) {
		t.Fatal("quarter-pixel positions rasterized identically")
	}
	if _, err := e.Rasterize(sh.Runs[0].Font, g.ID, size, text.Subpixels); err == nil {
		t.Fatal("out-of-range subpixel accepted")
	}
}

// Unsupported color formats draw a visible placeholder, never blank output.
func TestUnsupportedGlyphDataIsVisible(t *testing.T) {
	e := fixtures(t)
	face := e.fonts.ResolveFace('a')
	gid, _ := face.NominalGlyph('a')
	for name, data := range map[string]font.GlyphData{
		"colr":                font.GlyphColor{},
		"jpg bitmap":          font.GlyphBitmap{Format: font.JPG},
		"bad png":             font.GlyphBitmap{Format: font.PNG, Data: []byte("not a png")},
		"svg without outline": font.GlyphSVG{Source: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0H100V100H0Z"/></svg>`)},
	} {
		b, err := e.raster(face, gid, data, 20/float32(face.Upem()), vmap{scale: 1, plain: true}, 0)
		if err != nil || !b.Fallback || b.Width == 0 || b.Height == 0 || !slices.Contains(b.Pix, 0xff) {
			t.Fatalf("%s: %+v %v", name, b, err)
		}
	}
}

func TestSVGUsesAvailableOutlineAndSpaceStaysBlank(t *testing.T) {
	e := fixtures(t)
	face := e.fonts.ResolveFace('a')
	gid, _ := face.NominalGlyph('a')
	outline, ok := face.GlyphData(gid).(font.GlyphOutline)
	if !ok || len(outline.Segments) == 0 {
		t.Fatal("fixture has no outline for a")
	}
	s := float32(20) / float32(face.Upem())
	ys := vmap{scale: s, plain: true}
	want, err := e.raster(face, gid, outline, s, ys, 0)
	if err != nil || len(want.Pix) == 0 {
		t.Fatalf("outline: %+v %v", want, err)
	}
	got, err := e.raster(face, gid, font.GlyphSVG{Outline: outline}, s, ys, 0)
	if err != nil || got.Fallback || got.Width != want.Width || got.Height != want.Height ||
		got.Left != want.Left || got.Top != want.Top || !bytes.Equal(got.Pix, want.Pix) {
		t.Fatalf("SVG fallback outline differs: %+v %v", got, err)
	}
	space, _ := face.NominalGlyph(' ')
	b, err := e.raster(face, space, face.GlyphData(space), s, ys, 0)
	if err != nil || b.Fallback || b.Width != 0 || b.Height != 0 || len(b.Pix) != 0 {
		t.Fatalf("space became visible: %+v %v", b, err)
	}
}

// Host evidence, not a pinned fixture: Noto Color Emoji is not vendored.
func TestSpaceAfterEmojiUsesPrimaryFaceOnHost(t *testing.T) {
	emoji, err := os.ReadFile("/usr/share/fonts/truetype/noto/NotoColorEmoji.ttf")
	if err != nil {
		t.Skip("host lacks Noto Color Emoji:", err)
	}
	e := fixtures(t, emoji)
	sh, err := e.Shape([]rune("a 🎛 b"), size)
	if err != nil {
		t.Fatal(err)
	}
	primary := sh.Runs[0].Font
	var emojiFont, spaceFont text.FontID
	var emojiGlyph text.GlyphID
	for _, r := range sh.Runs {
		for _, g := range r.Glyphs {
			switch g.Cluster {
			case 2:
				emojiFont, emojiGlyph = r.Font, g.ID
			case 3:
				spaceFont = r.Font
			}
		}
	}
	if emojiFont == primary || spaceFont != primary {
		t.Fatalf("emoji font %d, following space font %d, primary %d", emojiFont, spaceFont, primary)
	}
	b, err := e.Rasterize(emojiFont, emojiGlyph, size, 0)
	if err != nil || !b.Color || b.Fallback || b.Width == 0 {
		t.Fatalf("color emoji raster %dx%d color=%t fallback=%t: %v", b.Width, b.Height, b.Color, b.Fallback, err)
	}
}
