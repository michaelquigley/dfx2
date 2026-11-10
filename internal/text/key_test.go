package text

import (
	"strings"
	"testing"
	"unsafe"
)

// A refused paragraph owns runes and shaped data, but no retained UTF-8 key.
// Use that independently exercised path as the payload baseline.
func transientParagraph(t *testing.T, input string) (*System, *Paragraph) {
	t.Helper()
	budget := DefaultBudget
	budget.ParagraphEntries = 0
	s := NewSystem(&gridEngine{}, budget)
	s.BeginFrame()
	t.Cleanup(s.EndFrame)
	p, err := s.Paragraph(input, 640)
	if err != nil || p.admitted || s.Stats().ParagraphEntries != 0 {
		t.Fatalf("transient paragraph: admitted=%t err=%v stats=%+v", p.admitted, err, s.Stats())
	}
	// gridEngine emits one run and glyph per rune, plus n+1 break bytes.
	// Compute that known fixture's payload without paragraphBytes, so an
	// erroneous UTF-8 charge on both paths cannot cancel out in comparisons.
	runes := len([]rune(input))
	want := int(unsafe.Sizeof(Paragraph{})) + 4*runes + runes + 1
	if runes != 0 {
		want += int(unsafe.Sizeof(Run{})) + runes*int(unsafe.Sizeof(Glyph{}))
	}
	if p.bytes != want || s.Stats().TransientBytes != want {
		t.Fatalf("transient payload=%d scratch=%d want owned shaped payload=%d", p.bytes, s.Stats().TransientBytes, want)
	}
	return s, p
}

func TestParagraphKeyOwnsOnlyItsSubstringAndSurvivesCacheHits(t *testing.T) {
	backing := strings.Repeat("x", 1<<20) + "é🙂 text" + strings.Repeat("y", 1<<20)
	input := backing[1<<20 : (1<<20)+len("é🙂 text")]
	s := NewSystem(&gridEngine{}, DefaultBudget)
	s.BeginFrame()
	p, err := s.Paragraph(input, 640)
	if err != nil || !p.admitted {
		t.Fatalf("admission: %v", err)
	}
	var stored string
	for key := range s.paragraphs {
		stored = key.text
	}
	if stored != input || unsafe.StringData(stored) == unsafe.StringData(input) {
		t.Fatal("cached key retains the caller's substring backing storage")
	}
	s.EndFrame()
	s.BeginFrame()
	other := strings.Clone(input)
	q, err := s.Paragraph(other, 640)
	if err != nil || p != q || s.Stats().Shapes != 1 {
		t.Fatalf("cache hit: %v stats=%+v", err, s.Stats())
	}
	for key := range s.paragraphs {
		if unsafe.StringData(key.text) != unsafe.StringData(stored) {
			t.Fatal("cache hit replaced the owned key")
		}
	}
	s.EndFrame()
}

func TestParagraphKeyChargePersistsThroughRewrapAndEviction(t *testing.T) {
	input := strings.Repeat("é🙂 a ", 8)
	transient, scratch := transientParagraph(t, input)
	s := NewSystem(&gridEngine{}, DefaultBudget)
	s.BeginFrame()
	p, err := s.Paragraph(input, 640)
	if err != nil || !p.admitted {
		t.Fatalf("admission: %v", err)
	}
	assertCharge := func() {
		t.Helper()
		want := scratch.bytes + len(input) // UTF-8 bytes, not rune count
		if p.bytes != want || s.Stats().ParagraphBytes != want {
			t.Fatalf("paragraph payload=%d cache=%d want shaped/wrapped payload %d + key %d", p.bytes, s.Stats().ParagraphBytes, scratch.bytes, len(input))
		}
	}
	assertCharge()
	for _, width := range []Fixed{wide, narrow, wide} {
		if _, err := transient.Lines(scratch, width); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Lines(p, width); err != nil {
			t.Fatal(err)
		}
		assertCharge()
		before := s.Stats()
		if _, err := s.Lines(p, width); err != nil || s.Stats() != before {
			t.Fatal("same-width hit changed accounting", err)
		}
	}
	s.EndFrame()
	s.evict(true)
	if st := s.Stats(); st.ParagraphBytes != 0 || st.ParagraphEntries != 0 {
		t.Fatalf("evicted key still charged: %+v", st)
	}
}

func TestParagraphAdmissionReservesKeyBytesWithoutChargingTransientKey(t *testing.T) {
	input := strings.Repeat("é ", 32)
	_, scratch := transientParagraph(t, input)
	base := scratch.bytes
	for _, shortage := range []int{1, 0} {
		budget := DefaultBudget
		budget.ParagraphBytes = 2*base + len(input) - shortage
		budget.Transient = base // no string is retained on the transient path
		s := NewSystem(&gridEngine{}, budget)
		s.BeginFrame()
		p, err := s.Paragraph(input, 640)
		if err != nil {
			t.Fatal(err)
		}
		if shortage == 1 {
			if p.admitted || s.Stats().ParagraphBytes != 0 || s.Stats().TransientBytes != base || p.bytes != base {
				t.Fatalf("key did not fit but was admitted, or scratch charged an unowned key: %+v", s.Stats())
			}
		} else if !p.admitted || s.Stats().ParagraphBytes != base+len(input) || s.Stats().TransientBytes != 0 {
			t.Fatalf("exact admission budget: %+v", s.Stats())
		}
		s.EndFrame()
	}
}

func TestParagraphKeyShortfallEvictsAnUnusedEntryBetweenFrames(t *testing.T) {
	input := strings.Repeat("é ", 32)
	_, scratch := transientParagraph(t, input)
	base := scratch.bytes
	budget := DefaultBudget
	budget.ParagraphBytes = base + len(input) + 2*base + len(input) - 1
	s := NewSystem(&gridEngine{}, budget)
	s.BeginFrame()
	first, err := s.Paragraph(input, 640)
	if err != nil || !first.admitted {
		t.Fatal("first entry not admitted", err)
	}
	s.EndFrame()
	s.BeginFrame()
	second, err := s.Paragraph(input, 641) // same payload size; a different cache key
	if err != nil || second.admitted {
		t.Fatal("second entry ignored the one-byte key shortfall", err)
	}
	if !first.admitted {
		t.Fatal("entry evicted mid-frame")
	}
	s.EndFrame()
	if first.admitted || s.Stats().ParagraphBytes != 0 {
		t.Fatal("unused key's charge survived eviction")
	}
	s.BeginFrame()
	second, err = s.Paragraph(input, 641)
	if err != nil || !second.admitted || s.Stats().ParagraphBytes != base+len(input) {
		t.Fatal("entry not admitted after eviction", err)
	}
	s.EndFrame()
	s.Release()
	if s.Stats().ParagraphBytes != 0 {
		t.Fatal("release kept the key charge")
	}
}
