package text

import (
	"errors"
	"fmt"
	"slices"
	"unsafe"
)

// Budget bounds one window's caches. Glyph pixels and paragraph payload are
// tracked in bytes; paragraphs are also bounded by entry count. Transient
// limits the scratch held for unadmitted entries and displaced wraps during
// one frame.
type Budget struct {
	GlyphBytes       int
	ParagraphBytes   int
	ParagraphEntries int
	Transient        int
}

// DefaultBudget is the stage 1 per-window default. Transient matches the
// surface image limit.
var DefaultBudget = Budget{GlyphBytes: 16 << 20, ParagraphBytes: 1 << 20, ParagraphEntries: 256, Transient: 256 << 20}

// Stats counts a System's work and occupancy, for tests and auditions.
type Stats struct {
	Shapes, Wraps, Rasterized       uint64
	GlyphEntries, ParagraphEntries  int
	GlyphBytes, ParagraphBytes      int
	GlyphEvictions, ParaEvictions   uint64
	TransientGlyphs, TransientParas uint64
	TransientBytes                  int // held by the current frame
	TransientWraps                  uint64
	FallbackGlyphs                  uint64
}

// System holds one window's shaped paragraphs and rasterized glyphs above a
// shared Engine. Use it from the UI thread. Entries are evicted only in
// EndFrame, so every reference handed out during a frame stays valid until
// the frame ends; evicted storage is dropped, never recycled.
type System struct {
	engine     Engine
	budget     Budget
	frame      uint64
	inFrame    bool
	paragraphs map[paragraphKey]*Paragraph
	glyphs     map[GlyphKey]*GlyphImage
	transient  []any // unadmitted entries and displaced wraps held until EndFrame
	deficit    struct{ glyphBytes, paraBytes, paraEntries int }
	stats      Stats
}

func NewSystem(engine Engine, budget Budget) *System {
	return &System{engine: engine, budget: budget, paragraphs: make(map[paragraphKey]*Paragraph), glyphs: make(map[GlyphKey]*GlyphImage)}
}

func (s *System) Stats() Stats {
	st := s.stats
	st.GlyphEntries, st.ParagraphEntries = len(s.glyphs), len(s.paragraphs)
	return st
}

type paragraphKey struct {
	text string
	size Fixed
}

// Paragraph is a shaped paragraph plus its most recent wrap. Shaping depends
// on text and size only; a new width rewraps the same shaped data.
type Paragraph struct {
	text       []rune
	shaped     Shaped
	width      Fixed
	wrapped    bool
	lines      []Line
	bytes      int    // shaped payload plus the stored wrap, as accounted
	lastUsed   uint64 // frame of last use
	admitted   bool
	held       uint64 // frame whose transient scratch holds an unadmitted paragraph
	linesFrame uint64 // frame in which the stored wrap was last handed out
}

// Lines returns the paragraph wrapped at width, rewrapping the same shaped
// runs (never reshaping) when the width changed. The result is valid until
// the frame ends; storage handed out earlier in the frame is never mutated.
//
// A new wrap is charged before it is kept. An admitted paragraph stores it
// only if the paragraph budget still holds; otherwise the lines are returned
// as transient scratch for this frame, the entry keeps its previous wrap, and
// the shortfall is left for eviction between frames. An unadmitted paragraph
// charges each new wrap to transient scratch. Displaced wraps already handed
// out in this frame remain held and accounted until EndFrame. Exceeding the
// scratch limit is an error, and nothing is stored.
func (s *System) Lines(p *Paragraph, width Fixed) ([]Line, error) {
	if !s.inFrame {
		return nil, errors.New("text: Lines outside a frame")
	}
	if !p.admitted && p.held != s.frame {
		return nil, errors.New("text: unadmitted paragraph used outside the frame that shaped it")
	}
	if p.wrapped && p.width == width {
		p.linesFrame = s.frame
		return p.lines, nil
	}
	lines := wrap(p.text, &p.shaped, width)
	s.stats.Wraps++
	bytes := paragraphBytes(p) - linesBytes(p.lines) + linesBytes(lines)
	growth := bytes - p.bytes
	switch {
	case p.admitted && s.stats.ParagraphBytes+growth <= s.budget.ParagraphBytes:
		// The new wrap replaces the cached one, but current-frame callers
		// may still reference that old buffer. Reserve its scratch lifetime
		// before changing either the paragraph or its cache accounting.
		if p.linesFrame == s.frame {
			if err := s.holdTransient(p.lines, linesBytes(p.lines)); err != nil {
				return nil, err
			}
			s.stats.TransientWraps++
		}
		s.stats.ParagraphBytes += growth
	case p.admitted:
		// No room in the cache this frame, and nothing may be evicted
		// before the frame ends: lend the lines as scratch instead.
		if err := s.holdTransient(lines, linesBytes(lines)); err != nil {
			return nil, err
		}
		s.deficit.paraBytes += growth
		s.stats.TransientWraps++
		return lines, nil
	default:
		// Its existing lines are already charged to scratch and remain
		// valid for this frame. Charge the whole new buffer, not just growth.
		if err := s.chargeTransient(linesBytes(lines)); err != nil {
			return nil, err
		}
		if p.wrapped {
			s.transient = append(s.transient, p.lines)
			s.stats.TransientWraps++
		}
	}
	p.lines, p.width, p.wrapped, p.bytes = lines, width, true, bytes
	p.linesFrame = s.frame
	return lines, nil
}

// Height sums the line heights of a wrap.
func Height(lines []Line) Fixed {
	var h Fixed
	for _, l := range lines {
		h += l.Ascent + l.Descent + l.Gap
	}
	return h
}

// GlyphKey identifies a raster: face, glyph, physical size (scale included)
// and quarter-pixel horizontal position.
type GlyphKey struct {
	Font     FontID
	ID       GlyphID
	Size     Fixed
	Subpixel uint8
}

// GlyphImage is an owned raster. Mask and color glyphs are kept apart by
// Bitmap.Color; both count against the glyph byte budget.
type GlyphImage struct {
	Bitmap
	bytes    int
	lastUsed uint64
}

// BeginFrame starts a frame: references handed out until EndFrame stay valid.
func (s *System) BeginFrame() {
	s.frame++
	s.inFrame = true
	s.deficit = struct{ glyphBytes, paraBytes, paraEntries int }{}
}

// EndFrame releases transient scratch and evicts between frames: first to
// restore the budget, then to make room for entries this frame could not
// admit, preferring entries the frame did not use (least recently used first).
func (s *System) EndFrame() {
	s.inFrame = false
	clear(s.transient)
	s.transient = s.transient[:0]
	s.stats.TransientBytes = 0
	s.evict(false)
}

// Release drops every cached entry; the engine's fonts are not owned here.
func (s *System) Release() {
	s.paragraphs, s.glyphs, s.transient = nil, nil, nil
	s.stats.GlyphBytes, s.stats.ParagraphBytes, s.stats.TransientBytes = 0, 0, 0
}

func (s *System) chargeTransient(bytes int) error {
	if s.stats.TransientBytes+bytes > s.budget.Transient {
		return fmt.Errorf("%w: %d + %d bytes > %d", errTransientLimit, s.stats.TransientBytes, bytes, s.budget.Transient)
	}
	s.stats.TransientBytes += bytes
	return nil
}

func (s *System) holdTransient(entry any, bytes int) error {
	if err := s.chargeTransient(bytes); err != nil {
		return err
	}
	s.transient = append(s.transient, entry)
	return nil
}

// Paragraph returns text shaped at size, shaping only on a cache miss.
func (s *System) Paragraph(text string, size Fixed) (*Paragraph, error) {
	if !s.inFrame {
		return nil, errors.New("text: Paragraph outside a frame")
	}
	key := paragraphKey{text, size}
	if p := s.paragraphs[key]; p != nil {
		p.lastUsed = s.frame
		return p, nil
	}
	runes := []rune(text)
	shaped, err := s.engine.Shape(runes, size)
	if err != nil {
		return nil, fmt.Errorf("text: shape %q: %w", text, err)
	}
	s.stats.Shapes++
	p := &Paragraph{text: runes, shaped: shaped, lastUsed: s.frame, held: s.frame}
	p.bytes = paragraphBytes(p)
	// Leave room for the first wrap, which usually doubles glyph payload.
	need := 2 * p.bytes
	switch {
	case need > s.budget.ParagraphBytes:
		s.stats.TransientParas++
		return p, s.holdTransient(p, p.bytes)
	case s.stats.ParagraphBytes+need > s.budget.ParagraphBytes || len(s.paragraphs)+1 > s.budget.ParagraphEntries:
		s.deficit.paraBytes += need
		s.deficit.paraEntries++
		s.stats.TransientParas++
		return p, s.holdTransient(p, p.bytes)
	}
	p.admitted = true
	s.paragraphs[key] = p
	s.stats.ParagraphBytes += p.bytes
	return p, nil
}

// Glyph returns a raster, rasterizing it on a cache miss.
func (s *System) Glyph(key GlyphKey) (*GlyphImage, error) {
	if !s.inFrame {
		return nil, errors.New("text: Glyph outside a frame")
	}
	if g := s.glyphs[key]; g != nil {
		g.lastUsed = s.frame
		return g, nil
	}
	b, err := s.engine.Rasterize(key.Font, key.ID, key.Size, int(key.Subpixel))
	if err != nil {
		return nil, fmt.Errorf("text: rasterize font %d glyph %d: %w", key.Font, key.ID, err)
	}
	s.stats.Rasterized++
	if b.Fallback {
		s.stats.FallbackGlyphs++
	}
	g := &GlyphImage{Bitmap: b, bytes: len(b.Pix) + int(unsafe.Sizeof(GlyphImage{})), lastUsed: s.frame}
	switch {
	case g.bytes > s.budget.GlyphBytes:
		s.stats.TransientGlyphs++
		return g, s.holdTransient(g, g.bytes)
	case s.stats.GlyphBytes+g.bytes > s.budget.GlyphBytes:
		s.deficit.glyphBytes += g.bytes
		s.stats.TransientGlyphs++
		return g, s.holdTransient(g, g.bytes)
	}
	s.glyphs[key] = g
	s.stats.GlyphBytes += g.bytes
	return g, nil
}

// evict runs between frames. all drops every entry (used by tests to force
// eviction); otherwise it restores the budgets, then frees room for this
// frame's refused admissions from entries the frame did not use.
func (s *System) evict(all bool) {
	type candidate struct {
		lastUsed uint64
		drop     func()
	}
	var glyphs, paras []candidate
	for k, g := range s.glyphs {
		glyphs = append(glyphs, candidate{g.lastUsed, func() {
			delete(s.glyphs, k)
			s.stats.GlyphBytes -= g.bytes
			s.stats.GlyphEvictions++
		}})
	}
	for k, p := range s.paragraphs {
		paras = append(paras, candidate{p.lastUsed, func() {
			delete(s.paragraphs, k)
			p.admitted = false
			s.stats.ParagraphBytes -= p.bytes
			s.stats.ParaEvictions++
		}})
	}
	byAge := func(a, b candidate) int { return int(int64(a.lastUsed) - int64(b.lastUsed)) }
	slices.SortStableFunc(glyphs, byAge)
	slices.SortStableFunc(paras, byAge)
	for _, c := range glyphs {
		over := s.stats.GlyphBytes > s.budget.GlyphBytes
		room := s.deficit.glyphBytes > 0 && c.lastUsed < s.frame && s.budget.GlyphBytes-s.stats.GlyphBytes < s.deficit.glyphBytes
		if !all && !over && !room {
			break
		}
		c.drop()
	}
	for _, c := range paras {
		over := s.stats.ParagraphBytes > s.budget.ParagraphBytes || len(s.paragraphs) > s.budget.ParagraphEntries
		// Refused admissions and refused wrap growth both leave a shortfall.
		room := c.lastUsed < s.frame &&
			(s.deficit.paraBytes > 0 && s.budget.ParagraphBytes-s.stats.ParagraphBytes < s.deficit.paraBytes ||
				s.deficit.paraEntries > 0 && s.budget.ParagraphEntries-len(s.paragraphs) < s.deficit.paraEntries)
		if !all && !over && !room {
			break
		}
		c.drop()
	}
}

// paragraphBytes estimates a paragraph's owned payload: runes, shaped glyphs,
// breaks and the current wrap.
func paragraphBytes(p *Paragraph) int {
	n := int(unsafe.Sizeof(Paragraph{})) + len(p.text)*4 + len(p.shaped.Breaks)
	for _, r := range p.shaped.Runs {
		n += int(unsafe.Sizeof(r)) + len(r.Glyphs)*int(unsafe.Sizeof(Glyph{}))
	}
	return n + linesBytes(p.lines)
}

func linesBytes(lines []Line) int {
	n := 0
	for _, l := range lines {
		n += int(unsafe.Sizeof(l)) + len(l.Glyphs)*int(unsafe.Sizeof(Placed{}))
	}
	return n
}
