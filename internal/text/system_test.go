package text

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unsafe"
)

// gridEngine is a deterministic engine: one glyph per rune, 10 px advances,
// breaks after spaces, and 8×10 coverage rasters whose bytes identify the
// glyph and subpixel, so a wrong or recycled raster is detectable.
type gridEngine struct{ shapes, rasters int }

func (e *gridEngine) Shape(runes []rune, size Fixed) (Shaped, error) {
	e.shapes++
	sh := Shaped{Breaks: make([]Break, len(runes)+1), Ascent: 8 * 64, Descent: 2 * 64}
	run := Run{Font: 1, Size: size, End: len(runes), Ascent: 8 * 64, Descent: 2 * 64}
	for i, r := range runes {
		run.Glyphs = append(run.Glyphs, Glyph{ID: GlyphID(r), Cluster: i, Runes: 1, Advance: 10 * 64})
		if i > 0 && unicode.IsSpace(runes[i-1]) && !unicode.IsSpace(r) {
			sh.Breaks[i] = CanBreak
		}
	}
	sh.Breaks[len(runes)] = MustBreak
	if len(runes) > 0 {
		sh.Runs = []Run{run}
	}
	return sh, nil
}

func pattern(id GlyphID, sub int) byte { return byte(id)*4 + byte(sub) + 1 }

func (e *gridEngine) Rasterize(font FontID, id GlyphID, size Fixed, sub int) (Bitmap, error) {
	e.rasters++
	pix := make([]byte, 80)
	for i := range pix {
		pix[i] = pattern(id, sub)
	}
	return Bitmap{Width: 8, Height: 10, Top: -8, Pix: pix}, nil
}

var glyphEntry = 80 + int(unsafe.Sizeof(GlyphImage{}))

func check(t *testing.T, s *System, budget Budget) {
	t.Helper()
	st := s.Stats()
	if st.GlyphBytes > budget.GlyphBytes || st.ParagraphBytes > budget.ParagraphBytes ||
		st.ParagraphEntries > budget.ParagraphEntries || st.TransientBytes > budget.Transient {
		t.Fatalf("budget exceeded: %+v vs %+v", st, budget)
	}
}

type held struct {
	img *GlyphImage
	key GlyphKey
	pix []byte
}

func render(t *testing.T, s *System, budget Budget, texts []string, width Fixed) (refs []held, widths [][]Fixed) {
	t.Helper()
	for _, txt := range texts {
		p, err := s.Paragraph(txt, 640)
		if err != nil {
			t.Fatal(err)
		}
		check(t, s, budget)
		var ws []Fixed
		lines, err := s.Lines(p, width)
		if err != nil {
			t.Fatal(err)
		}
		check(t, s, budget)
		for _, l := range lines {
			ws = append(ws, l.Width)
			for _, g := range l.Glyphs {
				key := GlyphKey{Font: g.Font, ID: g.ID, Size: g.Size, Subpixel: uint8(g.X/16) % Subpixels}
				img, err := s.Glyph(key)
				if err != nil {
					t.Fatal(err)
				}
				check(t, s, budget)
				refs = append(refs, held{img, key, slices.Clone(img.Pix)})
			}
		}
		widths = append(widths, ws)
	}
	// Every reference handed out this frame is still the raster it was.
	for _, r := range refs {
		if r.img.Pix[0] != pattern(r.key.ID, int(r.key.Subpixel)) || !slices.Equal(r.img.Pix, r.pix) {
			t.Fatalf("current-frame reference for %+v changed during the frame", r.key)
		}
	}
	return refs, widths
}

func TestCachePressureKeepsBudgetsAndFrameReferences(t *testing.T) {
	texts := []string{"abc def ghi", "jkl mno pqr", "stu vwx yz"}
	probe := NewSystem(&gridEngine{}, DefaultBudget)
	probe.BeginFrame()
	p, _ := probe.Paragraph(texts[0], 640)
	if _, err := probe.Lines(p, 1000*64); err != nil {
		t.Fatal(err)
	}
	one := p.bytes
	probe.EndFrame()
	// Each paragraph and glyph fits alone; the frame's working set does not.
	budget := Budget{GlyphBytes: 6 * glyphEntry, ParagraphBytes: 2*one + one/2, ParagraphEntries: 2, Transient: 64 * glyphEntry}
	engine := &gridEngine{}
	s := NewSystem(engine, budget)

	s.BeginFrame()
	refs, _ := render(t, s, budget, texts, 1000*64)
	st := s.Stats()
	if st.TransientGlyphs == 0 || st.TransientParas == 0 || st.TransientBytes == 0 {
		t.Fatalf("working set never exceeded the cache: %+v", st)
	}
	s.EndFrame()
	check(t, s, budget)
	if s.Stats().TransientBytes != 0 {
		t.Fatal("transient scratch outlived its frame")
	}

	// Force eviction between frames. Evicted storage is dropped, never reused.
	s.evict(true)
	if st := s.Stats(); st.GlyphEntries != 0 || st.ParagraphEntries != 0 || st.GlyphBytes != 0 || st.ParagraphBytes != 0 {
		t.Fatalf("forced eviction left %+v", st)
	}
	s.BeginFrame()
	again, _ := render(t, s, budget, texts[:1], 1000*64)
	s.EndFrame()
	for _, old := range refs {
		if !slices.Equal(old.img.Pix, old.pix) {
			t.Fatal("evicted raster storage was recycled")
		}
		for _, r := range again {
			if r.img == old.img {
				t.Fatal("a raster evicted earlier was handed out again")
			}
		}
	}

	// The paragraph is retained now: a different width rewraps it without
	// reshaping, with correct glyphs and spacing.
	shapes := engine.shapes
	s.BeginFrame()
	_, narrow := render(t, s, budget, texts[:1], 45*64)
	s.EndFrame()
	if engine.shapes != shapes {
		t.Fatal("width change reshaped a retained paragraph")
	}
	fresh := NewSystem(&gridEngine{}, DefaultBudget)
	fresh.BeginFrame()
	_, want := render(t, fresh, DefaultBudget, texts[:1], 45*64)
	fresh.EndFrame()
	if !slices.Equal(narrow[0], want[0]) || len(narrow[0]) != 3 || narrow[0][0] != 30*64 {
		t.Fatalf("rewrapped widths %v, fresh %v", narrow, want)
	}
}

func TestOversizedEntriesRenderTransientlyWithinTheScratchLimit(t *testing.T) {
	budget := Budget{GlyphBytes: glyphEntry - 1, ParagraphBytes: 1, ParagraphEntries: 1, Transient: 1 << 20}
	s := NewSystem(&gridEngine{}, budget)
	s.BeginFrame()
	p, err := s.Paragraph("ab", 640)
	if err != nil || p.admitted {
		t.Fatalf("oversized paragraph admitted=%t err=%v", p.admitted, err)
	}
	// Room for this paragraph's scratch and exactly two glyphs.
	budget.Transient = s.Stats().TransientBytes + 2*glyphEntry
	s.budget = budget
	for _, id := range []GlyphID{'a', 'b'} {
		g, err := s.Glyph(GlyphKey{Font: 1, ID: id, Size: 640})
		if err != nil || g.Pix[0] != pattern(id, 0) {
			t.Fatalf("transient glyph %c: %v", id, err)
		}
		check(t, s, budget)
	}
	if st := s.Stats(); st.GlyphEntries != 0 || st.ParagraphEntries != 0 {
		t.Fatalf("oversized entries admitted: %+v", st)
	}
	// Scratch is bounded: exceeding it is a reported rendering failure.
	_, err = s.Glyph(GlyphKey{Font: 1, ID: 'c', Size: 640})
	if !errors.Is(err, errTransientLimit) {
		t.Fatalf("scratch overflow: %v", err)
	}
	s.EndFrame()
	if s.Stats().TransientBytes != 0 {
		t.Fatal("scratch not released after the frame")
	}
}

func TestCacheHitsAndUseOutsideFrames(t *testing.T) {
	engine := &gridEngine{}
	s := NewSystem(engine, DefaultBudget)
	if _, err := s.Paragraph("x", 640); err == nil {
		t.Fatal("paragraph outside a frame")
	}
	for range 3 {
		s.BeginFrame()
		p, _ := s.Paragraph("hello world", 640)
		lines, err := s.Lines(p, 1000*64)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			for _, g := range l.Glyphs {
				if _, err := s.Glyph(GlyphKey{Font: g.Font, ID: g.ID, Size: g.Size}); err != nil {
					t.Fatal(err)
				}
			}
		}
		s.EndFrame()
	}
	if engine.shapes != 1 || engine.rasters != 8 { // "helo wrd" distinct runes
		t.Fatalf("shapes=%d rasters=%d", engine.shapes, engine.rasters)
	}
	// A different size is a different shaping and raster identity.
	s.BeginFrame()
	s.Paragraph("hello world", 1280)
	s.Glyph(GlyphKey{Font: 1, ID: 'h', Size: 1280})
	s.EndFrame()
	if engine.shapes != 2 || engine.rasters != 9 {
		t.Fatalf("size change: shapes=%d rasters=%d", engine.shapes, engine.rasters)
	}
	s.Release()
	if st := s.Stats(); st.GlyphEntries != 0 || st.ParagraphEntries != 0 {
		t.Fatalf("release kept %+v", st)
	}
}

// wrapInput grows sharply when wrapped narrowly: one line per word.
var wrapInput = strings.Repeat("a ", 100)

// payloads measures the shaped-only, wide-wrap and narrow-wrap payloads of a
// paragraph with an unconstrained system.
func payloads(t *testing.T, txt string) (shapedBytes, wideBytes, narrowBytes int) {
	t.Helper()
	measure := func(width Fixed) int {
		s := NewSystem(&gridEngine{}, DefaultBudget)
		s.BeginFrame()
		defer s.EndFrame()
		p, err := s.Paragraph(txt, 640)
		if err != nil {
			t.Fatal(err)
		}
		if width == 0 {
			return p.bytes
		}
		if _, err := s.Lines(p, width); err != nil {
			t.Fatal(err)
		}
		return p.bytes
	}
	return measure(0), measure(wide), measure(narrow)
}

const (
	wide   = Fixed(10000 * 64)
	narrow = Fixed(10 * 64)
)

func widths(lines []Line) []Fixed {
	var out []Fixed
	for _, l := range lines {
		out = append(out, l.Width)
	}
	return out
}

func TestAdmittedWrapGrowthStaysWithinBudget(t *testing.T) {
	shaped, wideBytes, narrowBytes := payloads(t, wrapInput)
	if !(shaped < wideBytes && wideBytes <= 2*shaped && 2*shaped < narrowBytes) {
		t.Fatalf("fixture sizes shaped=%d wide=%d narrow=%d", shaped, wideBytes, narrowBytes)
	}
	t.Run("narrower rewrap in the same frame", func(t *testing.T) {
		budget := Budget{GlyphBytes: 1 << 20, ParagraphBytes: 2 * shaped, ParagraphEntries: 4, Transient: 1 << 20}
		engine := &gridEngine{}
		s := NewSystem(engine, budget)
		s.BeginFrame()
		p, err := s.Paragraph(wrapInput, 640)
		if err != nil || !p.admitted {
			t.Fatalf("admitted=%t err=%v", p.admitted, err)
		}
		wideLines, err := s.Lines(p, wide)
		check(t, s, budget)
		if err != nil || s.Stats().ParagraphBytes != wideBytes {
			t.Fatalf("wide wrap: %v, paragraph bytes %d want %d", err, s.Stats().ParagraphBytes, wideBytes)
		}
		kept := slices.Clone(wideLines[0].Glyphs)
		narrowLines, err := s.Lines(p, narrow)
		check(t, s, budget)
		if err != nil {
			t.Fatal(err)
		}
		st := s.Stats()
		if st.ParagraphBytes != wideBytes || st.TransientWraps != 1 || st.TransientBytes != linesBytes(narrowLines) {
			t.Fatalf("narrow growth not lent as scratch: %+v", st)
		}
		// The cache entry keeps its stored wrap; earlier references are intact.
		if p.width != wide || !slices.Equal(wideLines[0].Glyphs, kept) || len(narrowLines) != 100 {
			t.Fatalf("stored width %v, lines %d", p.width.Float(), len(narrowLines))
		}
		s.EndFrame()
		check(t, s, budget)
		if s.Stats().TransientBytes != 0 {
			t.Fatal("lent lines outlived the frame")
		}
		// Next frame: a cache hit, still no reshape, same narrow layout.
		s.BeginFrame()
		q, _ := s.Paragraph(wrapInput, 640)
		again, err := s.Lines(q, narrow)
		check(t, s, budget)
		if err != nil || q != p || engine.shapes != 1 || !slices.Equal(widths(again), widths(narrowLines)) {
			t.Fatalf("next frame: err=%v same=%t shapes=%d", err, q == p, engine.shapes)
		}
		s.EndFrame()
	})
	t.Run("eviction between frames makes room for the growth", func(t *testing.T) {
		other := "zz"
		otherShaped, otherWide, _ := payloads(t, other)
		budget := Budget{GlyphBytes: 1 << 20, ParagraphBytes: narrowBytes + otherWide - 1, ParagraphEntries: 4, Transient: 1 << 20}
		if shaped+2*otherShaped > budget.ParagraphBytes {
			t.Fatal("fixture cannot admit both paragraphs")
		}
		engine := &gridEngine{}
		s := NewSystem(engine, budget)
		s.BeginFrame()
		p, _ := s.Paragraph(wrapInput, 640)
		q, _ := s.Paragraph(other, 640)
		if _, err := s.Lines(q, wide); err != nil || !p.admitted || !q.admitted {
			t.Fatalf("setup: %v admitted=%t,%t", err, p.admitted, q.admitted)
		}
		s.EndFrame()
		s.BeginFrame()
		p, _ = s.Paragraph(wrapInput, 640)
		if _, err := s.Lines(p, narrow); err != nil || s.Stats().TransientWraps != 1 {
			t.Fatalf("growth should not fit beside the other entry: %v %+v", err, s.Stats())
		}
		check(t, s, budget)
		s.EndFrame() // the unused entry is evicted for the recorded shortfall
		check(t, s, budget)
		if q.admitted || !p.admitted {
			t.Fatalf("eviction chose wrongly: other=%t grown=%t", q.admitted, p.admitted)
		}
		s.BeginFrame()
		p, _ = s.Paragraph(wrapInput, 640)
		if _, err := s.Lines(p, narrow); err != nil || s.Stats().TransientWraps != 1 || p.width != narrow {
			t.Fatalf("growth not stored after eviction: %v %+v", err, s.Stats())
		}
		check(t, s, budget)
		if st := s.Stats(); st.ParagraphBytes != narrowBytes || engine.shapes != 2 {
			t.Fatalf("bytes %d want %d, shapes %d", st.ParagraphBytes, narrowBytes, engine.shapes)
		}
		s.EndFrame()
	})
}

func TestTransientWrapGrowthIsChargedAndRefused(t *testing.T) {
	shaped, wideBytes, narrowBytes := payloads(t, wrapInput)
	frame := func(limit int) (*System, *Paragraph) {
		s := NewSystem(&gridEngine{}, Budget{GlyphBytes: 1 << 20, ParagraphBytes: 1, ParagraphEntries: 1, Transient: limit})
		s.BeginFrame()
		p, err := s.Paragraph(wrapInput, 640)
		if err != nil || p.admitted || s.Stats().TransientBytes != shaped {
			t.Fatalf("setup: admitted=%t err=%v %+v", p.admitted, err, s.Stats())
		}
		return s, p
	}
	t.Run("first wrap beyond the limit", func(t *testing.T) {
		s, p := frame(shaped)
		if _, err := s.Lines(p, narrow); !errors.Is(err, errTransientLimit) {
			t.Fatalf("narrow wrap within a %d-byte limit: %v", shaped, err)
		}
		if st := s.Stats(); st.TransientBytes != shaped || p.wrapped || p.bytes != shaped {
			t.Fatalf("refused wrap was kept: %+v wrapped=%t bytes=%d", st, p.wrapped, p.bytes)
		}
		s.EndFrame()
	})
	t.Run("narrower rewrap beyond the limit", func(t *testing.T) {
		s, p := frame(wideBytes)
		wideLines, err := s.Lines(p, wide)
		if err != nil || s.Stats().TransientBytes != wideBytes {
			t.Fatalf("wide wrap: %v %+v", err, s.Stats())
		}
		if _, err := s.Lines(p, narrow); !errors.Is(err, errTransientLimit) {
			t.Fatalf("narrow growth past %d bytes: %v", wideBytes, err)
		}
		if st := s.Stats(); st.TransientBytes != wideBytes || p.width != wide || len(p.lines) != len(wideLines) {
			t.Fatalf("refused growth changed state: %+v width=%v", st, p.width.Float())
		}
		s.EndFrame()
	})
	t.Run("growth within the limit is charged", func(t *testing.T) {
		// Both wraps can remain live in this frame, so scratch covers the
		// shaped paragraph and both line buffers, not only the final wrap.
		wantScratch := narrowBytes + wideBytes - shaped
		s, p := frame(wantScratch)
		if _, err := s.Lines(p, wide); err != nil {
			t.Fatal(err)
		}
		lines, err := s.Lines(p, narrow)
		if err != nil || len(lines) != 100 || s.Stats().TransientBytes != wantScratch || p.bytes != narrowBytes {
			t.Fatalf("narrow wrap: %v transient=%d want %d payload=%d want %d", err, s.Stats().TransientBytes, wantScratch, p.bytes, narrowBytes)
		}
		s.EndFrame()
		if s.Stats().TransientBytes != 0 {
			t.Fatal("scratch not released")
		}
		s.BeginFrame()
		if _, err := s.Lines(p, wide); err == nil {
			t.Fatal("an unadmitted paragraph was wrapped after its frame ended")
		}
		s.EndFrame()
	})
}

// Every successful result remains live until EndFrame, even after a later
// call replaces the paragraph's stored wrap. The limit must cover their sum.
func TestRepeatedRewrapsAccountForEveryLiveResult(t *testing.T) {
	shaped, widePayload, narrowPayload := payloads(t, wrapInput)
	wideWrap, narrowWrap := widePayload-shaped, narrowPayload-shaped
	for _, admitted := range []bool{true, false} {
		name := "cached"
		if !admitted {
			name = "transient"
		}
		t.Run(name, func(t *testing.T) {
			budget := Budget{GlyphBytes: 1024, ParagraphBytes: 2 * narrowPayload, ParagraphEntries: 1,
				Transient: 2*wideWrap + narrowWrap}
			if !admitted {
				budget.ParagraphBytes = 1
				budget.Transient = shaped + 2*wideWrap + 2*narrowWrap
			}
			engine := &gridEngine{}
			s := NewSystem(engine, budget)
			s.BeginFrame()
			p, err := s.Paragraph(wrapInput, 640)
			if err != nil || p.admitted != admitted {
				t.Fatalf("setup: admitted=%t err=%v", p.admitted, err)
			}
			var results, snapshots [][]Line
			wantScratch := 0
			if !admitted {
				wantScratch = shaped
			}
			for i, width := range []Fixed{wide, narrow, wide, narrow} {
				lines, err := s.Lines(p, width)
				if err != nil {
					t.Fatalf("wrap %d: %v", i, err)
				}
				if admitted {
					if i > 0 {
						wantScratch += linesBytes(results[i-1])
					}
				} else {
					wantScratch += linesBytes(lines)
				}
				results = append(results, lines)
				snapshot := slices.Clone(lines)
				for j := range snapshot {
					snapshot[j].Glyphs = slices.Clone(snapshot[j].Glyphs)
				}
				snapshots = append(snapshots, snapshot)
				if got := s.Stats().TransientBytes; got != wantScratch {
					t.Fatalf("wrap %d: scratch=%d want all live results=%d", i, got, wantScratch)
				}
				check(t, s, budget)
				before := s.Stats()
				again, err := s.Lines(p, width)
				if err != nil || &again[0] != &lines[0] || s.Stats() != before {
					t.Fatalf("same-width cache hit changed storage or accounting: %v", err)
				}
			}
			before, oldBytes, oldWidth := s.Stats(), p.bytes, p.width
			oldLines := p.lines
			if _, err := s.Lines(p, wide); !errors.Is(err, errTransientLimit) {
				t.Fatalf("fifth live wrap must exhaust aggregate scratch: %v", err)
			}
			if s.Stats().TransientBytes != before.TransientBytes || s.Stats().ParagraphBytes != before.ParagraphBytes ||
				p.bytes != oldBytes || p.width != oldWidth || &p.lines[0] != &oldLines[0] {
				t.Fatal("refused replacement changed retained state or accounting")
			}
			for i, lines := range results {
				if !slices.Equal(widths(lines), widths(snapshots[i])) {
					t.Fatalf("earlier wrap %d changed widths", i)
				}
				for j := range lines {
					if !slices.Equal(lines[j].Glyphs, snapshots[i][j].Glyphs) {
						t.Fatalf("earlier wrap %d line %d changed glyphs", i, j)
					}
				}
			}
			if engine.shapes != 1 {
				t.Fatalf("rewrapping reshaped %d times", engine.shapes)
			}
			s.EndFrame()
			if s.Stats().TransientBytes != 0 || len(s.transient) != 0 {
				t.Fatal("displaced-wrap scratch survived EndFrame")
			}
		})
	}
}

func TestCachedWrapDisplacementOnlyChargesCurrentFrameReferences(t *testing.T) {
	_, _, narrowPayload := payloads(t, wrapInput)
	for _, readOldWrap := range []bool{false, true} {
		name := "unused old wrap"
		if readOldWrap {
			name = "old wrap returned by cache hit"
		}
		t.Run(name, func(t *testing.T) {
			s := NewSystem(&gridEngine{}, Budget{GlyphBytes: 1024, ParagraphBytes: 2 * narrowPayload, ParagraphEntries: 1})
			s.BeginFrame()
			p, err := s.Paragraph(wrapInput, 640)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Lines(p, wide); err != nil {
				t.Fatal(err)
			}
			s.EndFrame()
			s.BeginFrame()
			p, err = s.Paragraph(wrapInput, 640)
			if err != nil {
				t.Fatal(err)
			}
			if readOldWrap {
				if _, err := s.Lines(p, wide); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.Lines(p, narrow)
			if readOldWrap {
				if !errors.Is(err, errTransientLimit) || p.width != wide {
					t.Fatalf("live old wrap must stay accounted; err=%v width=%v", err, p.width)
				}
			} else if err != nil || p.width != narrow {
				t.Fatalf("an unused prior-frame wrap needs no scratch: %v", err)
			}
			s.EndFrame()
		})
	}
}
