package ui

import (
	"image"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/appcore"
)

func rectOf(t *testing.T, c *content, key string) image.Rectangle {
	t.Helper()
	for i, it := range c.last.items {
		if it.key == key || (it.key == "" && it.name == key) {
			return c.last.rects[i]
		}
	}
	t.Fatalf("no item %q in %+v", key, c.last.items)
	return image.Rectangle{}
}

func TestRootColumnKeepsThePlacementViewsHadBeforeContainers(t *testing.T) {
	c := testContent(t, func(f *Frame) { f.Meter("a", 0); f.Meter("b", 0) })
	frame(t, c, request(0, 300, 200, 1))
	if a := rectOf(t, c, "a"); a != image.Rect(16, 16, 284, 30) {
		t.Fatalf("first meter %v", a)
	}
	if b := rectOf(t, c, "b"); b != image.Rect(16, 40, 284, 54) {
		t.Fatalf("second meter %v", b)
	}
}

func TestFlexSharesRemainingSpaceAfterNonflexChildren(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Height: 40, Gap: 10, Padding: 5}, func(f *Frame) {
			f.Column(Layout{Width: 50}, func(f *Frame) { f.Meter("fixed", 0) })
			f.Column(Layout{Flex: 1}, func(f *Frame) { f.Meter("one", 0) })
			f.Column(Layout{Flex: 2}, func(f *Frame) { f.Meter("two", 0) })
		})
	})
	frame(t, c, request(0, 348, 200, 1)) // row width 316, inner 306, gaps 20, flex 236
	if r := rectOf(t, c, "fixed"); r.Min != image.Pt(21, 21) || r.Dx() != 50 {
		t.Fatalf("fixed %v", r)
	}
	one, two := rectOf(t, c, "one"), rectOf(t, c, "two")
	if one.Min.X != 81 || one.Dx()+two.Dx() != 236 || two.Min.X != one.Max.X+10 {
		t.Fatalf("flex lost pixels: %v %v", one, two)
	}
	if d := two.Dx() - 2*one.Dx(); d < -2 || d > 2 {
		t.Fatalf("weights not respected: %v %v", one, two)
	}
}

func TestFlexRespectsBoundsAndRedistributes(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Height: 30}, func(f *Frame) {
			f.Column(Layout{Flex: 1, MaxWidth: 40}, func(f *Frame) { f.Meter("capped", 0) })
			f.Column(Layout{Flex: 1}, func(f *Frame) { f.Meter("open", 0) })
			f.Column(Layout{Flex: 1, MinWidth: 200}, func(f *Frame) { f.Meter("floored", 0) })
		})
	})
	frame(t, c, request(0, 332, 100, 1)) // 300 to share
	if capped, open, floored := rectOf(t, c, "capped").Dx(), rectOf(t, c, "open").Dx(), rectOf(t, c, "floored").Dx(); capped != 40 || floored != 200 || open != 60 {
		t.Fatalf("capped=%d open=%d floored=%d", capped, open, floored)
	}
}

// boundedFrame renders like frame, but turns a layout that never finishes into
// a failure. A hung frame's goroutine is abandoned; the test still ends.
func boundedFrame(t *testing.T, c *content, req appcore.FrameRequest) {
	t.Helper()
	type result struct {
		r   appcore.FrameResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := c.Frame(req)
		done <- result{r, err}
	}()
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatal(out.err)
		}
		if err := out.r.Image.Validate(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("layout did not finish within 5 s")
	}
}

func flexRow(weights ...Layout) func(*Frame) {
	return func(f *Frame) {
		f.Row(Layout{Height: 30}, func(f *Frame) {
			for i, l := range weights {
				f.Column(l, func(f *Frame) { f.Meter(string(rune('a'+i)), 0) })
			}
		})
	}
}

func widths(t *testing.T, c *content, n int) []int {
	t.Helper()
	out := make([]int, n)
	for i := range out {
		out[i] = rectOf(t, c, string(rune('a'+i))).Dx()
	}
	return out
}

// Summing two weights of 1e308 overflows; unscaled, the NaN shares never froze
// and the distribution never ended.
func TestHugeEqualFlexWeightsShareEquallyAndFinish(t *testing.T) {
	c := testContent(t, flexRow(Layout{Flex: 1e308}, Layout{Flex: 1e308}))
	boundedFrame(t, c, request(0, 332, 100, 1)) // 300 to share
	if w := widths(t, c, 2); w[0] != 150 || w[1] != 150 {
		t.Fatalf("widths %v", w)
	}
}

// Unscaled, a lone huge weight's share overflowed to infinity and froze at the
// unbounded length instead of taking the remaining space.
func TestALoneHugeFlexWeightLaysOutLikeFlexOne(t *testing.T) {
	var got [2][]image.Rectangle
	for i, weight := range []float64{1, 1e308} {
		c := testContent(t, func(f *Frame) {
			f.Row(Layout{Height: 30}, func(f *Frame) {
				f.Column(Layout{Width: 50}, func(f *Frame) { f.Meter("fixed", 0) })
				f.Column(Layout{Flex: weight}, func(f *Frame) { f.Meter("flex", 0) })
			})
		})
		boundedFrame(t, c, request(0, 332, 100, 1))
		got[i] = []image.Rectangle{rectOf(t, c, "fixed"), rectOf(t, c, "flex")}
	}
	if got[0][1].Dx() != 250 || got[1][0] != got[0][0] || got[1][1] != got[0][1] {
		t.Fatalf("Flex 1 %v, Flex 1e308 %v", got[0], got[1])
	}
}

func TestAHugeFlexWeightBesideFlexOneTakesNearlyAll(t *testing.T) {
	c := testContent(t, flexRow(Layout{Flex: 1e308}, Layout{Flex: 1}))
	boundedFrame(t, c, request(0, 332, 100, 1))
	if w := widths(t, c, 2); w[0]+w[1] != 300 || w[0] < 299 {
		t.Fatalf("widths %v", w)
	}
}

// Against 1e308 the small weights scale to zero; once the huge child freezes at
// its bound, scaling over the children still sharing restores their 1:2 ratio.
func TestHugeWeightAtItsBoundLeavesTheRestInProportion(t *testing.T) {
	c := testContent(t, flexRow(Layout{Flex: 1e308, MaxWidth: 40}, Layout{Flex: 1e-20}, Layout{Flex: 2e-20}))
	boundedFrame(t, c, request(0, 332, 100, 1))
	if w := widths(t, c, 3); w[0] != 40 || w[1] != 87 || w[2] != 173 {
		t.Fatalf("widths %v", w)
	}
}

// Validation rejects weights that are not finite, so no layout reaches the
// guards this way; called directly, the distribution fails instead of looping.
func TestNonFiniteFlexArithmeticFailsInsteadOfLooping(t *testing.T) {
	s := &solver{scale: 1}
	for _, weight := range []float64{math.NaN(), math.Inf(1)} {
		children := []*node{{item: -1, layout: Layout{Flex: weight}}, {item: -1, layout: Layout{Flex: 1}}}
		done := make(chan error, 1)
		go func() {
			_, err := s.distribute(children, axisX, 300)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "dfx2 ui: flex distribution") {
				t.Fatalf("weight %v: %v", weight, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("weight %v: distribution did not finish", weight)
		}
	}
}

func TestColumnWithAFlexChildFillsAFlexRowsHeight(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Flex: 1}, func(f *Frame) {
			f.Column(Layout{Width: 80}, func(f *Frame) {
				f.Meter("top", 0)
				f.Column(Layout{Flex: 1}, func(f *Frame) {})
				f.Meter("bottom", 0)
			})
		})
	})
	frame(t, c, request(0, 200, 300, 1))
	if top, bottom := rectOf(t, c, "top"), rectOf(t, c, "bottom"); top.Min.Y != 16 || bottom.Max.Y != 284 {
		t.Fatalf("top %v bottom %v", top, bottom)
	}
}

func TestCrossAxisKeepsMeasuredSizeWithoutStretching(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Height: 200}, func(f *Frame) {
			f.Column(Layout{}, func(f *Frame) { f.Meter("short", 0) })
			f.Fader("tall", 0)
		})
		f.Column(Layout{Width: 100}, func(f *Frame) { f.Label("wide column"); f.Fader("narrow", 0) })
	})
	frame(t, c, request(0, 400, 500, 1))
	if r := rectOf(t, c, "short"); r.Dy() != 14 || r.Dx() != 120 {
		t.Fatalf("meter stretched: %v", r)
	}
	if r := rectOf(t, c, "narrow"); r.Dx() != 36 || r.Dy() != 160 {
		t.Fatalf("fader stretched: %v", r)
	}
}

func TestMinimaHoldAndOverflowIsClippedAndUnreachable(t *testing.T) {
	var value float64
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Height: 100}, func(f *Frame) {
			f.Column(Layout{Flex: 1, MinWidth: 150}, func(f *Frame) { f.Meter("first", 1) })
			f.Column(Layout{Flex: 1, MinWidth: 150}, func(f *Frame) {
				var changed bool
				if value, changed = f.Fader("second", value); changed {
					t.Fatal("hidden fader changed")
				}
			})
		})
	})
	frame(t, c, request(0, 200, 150, 1)) // 168 inner; minima need 300
	if first, second := rectOf(t, c, "first"), rectOf(t, c, "second"); first.Dx() != 150 || second.Min.X != 166 {
		t.Fatalf("minima did not hold: %v %v", first, second)
	}
	// The second column runs past the surface. What the surface clips cannot be
	// pressed, although the fader's own rectangle lies inside the column.
	if second := rectOf(t, c, "second"); second.Max.X != 202 {
		t.Fatalf("fader %v does not reach past the 200-wide surface", second)
	}
	if h := c.hitAt(200.5, 50); h != nil {
		t.Fatalf("press beyond the window hit %+v", h)
	}
	if h := c.hitAt(170, 50); h == nil || h.path != "second" {
		t.Fatalf("visible part not reachable: %+v", h)
	}
}

func TestIntrinsicContainersKeepTheirBounds(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Column(Layout{MinWidth: 200, MinHeight: 50, Padding: 2}, func(f *Frame) {
			f.Row(Layout{MaxWidth: 40, MaxHeight: 8}, func(f *Frame) { f.Meter("capped", 0) })
		})
		f.Meter("below", 0)
	})
	frame(t, c, request(0, 400, 400, 1))
	below := rectOf(t, c, "below")
	if below.Min.Y != 16+50+10 {
		t.Fatalf("minimum height did not hold: %v", below)
	}
	capped := rectOf(t, c, "capped")
	if capped.Dx() != 120 {
		t.Fatalf("the row's child keeps its own measure: %v", capped)
	}
}

func TestContainerClipsChildrenAndHidesThemFromInput(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Column(Layout{Width: 100, Height: 50}, func(f *Frame) { f.Fader("clipped", 0.5) })
		f.Meter("below", 1)
	})
	r := frame(t, c, request(0, 300, 300, 1))
	fader := rectOf(t, c, "clipped")
	if fader.Dy() != 160 {
		t.Fatalf("fader %v", fader)
	}
	box := image.Rect(16, 16, 116, 66)
	for y := 66; y < rectOf(t, c, "below").Min.Y; y++ {
		for x := range r.Image.Width {
			p := r.Image.Pix[y*r.Image.Stride+x*4:]
			if p[0] != background.B || p[1] != background.G || p[2] != background.R {
				t.Fatalf("overflow painted at %d,%d outside %v", x, y, box)
			}
		}
	}
	if h := c.hitAt(20, 100); h != nil {
		t.Fatalf("clipped part of a fader took a press: %+v", h)
	}
	if h := c.hitAt(20, 40); h == nil {
		t.Fatal("visible part of a fader took no press")
	}
}

func TestLabelWrapsToItsAssignedWidthWithoutReshapingOrEvaluating(t *testing.T) {
	const words = "several words that have to wrap inside a narrow flexible column"
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Flex: 1}, func(f *Frame) {
			f.Column(Layout{Flex: 1}, func(f *Frame) { f.Label(words) })
			f.Column(Layout{Width: 100}, func(f *Frame) { f.Meter("m", 0) })
		})
	})
	frame(t, c, request(0, 600, 400, 1))
	wide := rectOf(t, c, words)
	shapes, frames := c.text.Stats().Shapes, c.frames
	frame(t, c, request(0, 260, 400, 1))
	narrow := rectOf(t, c, words)
	if narrow.Dx() > 260-32-100 || narrow.Dy() <= wide.Dy() {
		t.Fatalf("label did not rewrap: wide %v narrow %v", wide, narrow)
	}
	if got := c.text.Stats().Shapes; got != shapes {
		t.Fatalf("rewrapping reshaped: %d -> %d", shapes, got)
	}
	if c.frames != frames+1 {
		t.Fatalf("view ran %d times for one frame", c.frames-frames)
	}
}

func TestLabelInARowTakesItsNaturalWidth(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Gap: 10}, func(f *Frame) {
			f.Label("one two")
			f.Meter("after", 0)
		})
	})
	frame(t, c, request(0, 500, 200, 1))
	label, meter := rectOf(t, c, "one two"), rectOf(t, c, "after")
	if label.Dx() < 20 || label.Dy() > 30 || meter.Min.X != label.Max.X+10 || meter.Dx() != 120 {
		t.Fatalf("label %v meter %v", label, meter)
	}
}

func TestLayoutScalesToPhysicalPixels(t *testing.T) {
	draw := func(f *Frame) {
		f.Row(Layout{Gap: 10, Padding: 4}, func(f *Frame) {
			f.Column(Layout{Width: 60}, func(f *Frame) { f.Meter("m", 0) })
			f.Fader("g", 0.5)
		})
	}
	one, two := testContent(t, draw), testContent(t, draw)
	frame(t, one, request(0, 300, 300, 1))
	frame(t, two, request(0, 600, 600, 2))
	for _, key := range []string{"m", "g"} {
		a, b := rectOf(t, one, key), rectOf(t, two, key)
		if b.Min.X != 2*a.Min.X || b.Min.Y != 2*a.Min.Y || b.Dx() != 2*a.Dx() || b.Dy() != 2*a.Dy() {
			t.Fatalf("%s: %v at scale 1, %v at scale 2", key, a, b)
		}
	}
}

func TestHugeRequestedSizesStayInRange(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Row(Layout{Width: 1e300, Height: 1e300}, func(f *Frame) { f.Meter("m", 0) })
	})
	frame(t, c, request(0, 200, 100, 2))
	if r := rectOf(t, c, "m"); r.Min != image.Pt(32, 32) || r.Dx() <= 0 {
		t.Fatalf("meter %v", r)
	}
}

func TestInvalidLayoutsPanicWithContext(t *testing.T) {
	nothing := func(*Frame) {}
	for name, draw := range map[string]func(*Frame){
		"negative":  func(f *Frame) { f.Row(Layout{Width: -1}, nothing) },
		"nan":       func(f *Frame) { f.Column(Layout{Gap: nan()}, nothing) },
		"flex":      func(f *Frame) { f.Column(Layout{Flex: -1}, nothing) },
		"bounds":    func(f *Frame) { f.Row(Layout{MinWidth: 20, MaxWidth: 10}, nothing) },
		"no body":   func(f *Frame) { f.Row(Layout{}, nil) },
		"no scope":  func(f *Frame) { f.Scope("x", nil) },
		"empty key": func(f *Frame) { f.Scope("", nothing) },
	} {
		t.Run(name, func(t *testing.T) {
			c := testContent(t, draw)
			defer func() {
				if p := recover(); p == nil || !strings.Contains(p.(string), "dfx2 ui") {
					t.Fatalf("recovered %v", p)
				}
			}()
			c.Frame(request(0, 100, 100, 1))
		})
	}
}

func TestDuplicateSiblingKeysFailBeforeAFrameIsPublished(t *testing.T) {
	nothing := func(*Frame) {}
	for name, draw := range map[string]func(*Frame){
		"faders":         func(f *Frame) { f.Fader("k", 0); f.Fader("k", 0) },
		"across kinds":   func(f *Frame) { f.Meter("k", 0); f.Checkbox("k", "x", false) },
		"scope and leaf": func(f *Frame) { f.Scope("k", nothing); f.Fader("k", 0) },
		"scopes":         func(f *Frame) { f.Scope("k", nothing); f.Scope("k", nothing) },
		"through rows": func(f *Frame) {
			f.Row(Layout{}, func(f *Frame) { f.Fader("k", 0) })
			f.Column(Layout{}, func(f *Frame) { f.Fader("k", 0) })
		},
		"in one scope": func(f *Frame) { f.Scope("s", func(f *Frame) { f.Fader("k", 0); f.Fader("k", 0) }) },
	} {
		t.Run(name, func(t *testing.T) {
			c := testContent(t, draw)
			defer func() {
				p := recover()
				if p == nil || !strings.Contains(p.(string), "duplicate key") {
					t.Fatalf("recovered %v", p)
				}
				if c.last != nil {
					t.Fatal("a frame was published")
				}
			}()
			c.Frame(request(0, 100, 100, 1))
		})
	}
	// The same key under different scopes is two identities, and a key cannot
	// pass for a nested path.
	c := testContent(t, func(f *Frame) {
		f.Scope("a", func(f *Frame) { f.Fader("k", 0) })
		f.Scope("b", func(f *Frame) { f.Fader("k", 0) })
		f.Fader("a/k", 0)
		f.Fader(`a\/k`, 0)
	})
	frame(t, c, request(0, 400, 800, 1))
	if len(c.controls) != 4 {
		t.Fatalf("controls %v", c.controls)
	}
}
