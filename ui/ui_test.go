package ui

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/text"
	"github.com/michaelquigley/dfx2/internal/text/gotext"
)

// These tests call the content bridge directly with deterministic requests;
// scheduling and native presentation are covered in appcore and the backends.
func testContent(t *testing.T, draw func(*Frame)) *content {
	t.Helper()
	c := newContent(draw)
	c.engine = testEngine(t)
	return c
}

func testEngine(t *testing.T) func() (text.Engine, error) {
	t.Helper()
	engine, err := gotext.NewFaces(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	return func() (text.Engine, error) { return engine, nil }
}

var epoch = time.Unix(3_000_000, 0)

func request(at time.Duration, w, h int, scale float64) appcore.FrameRequest {
	return appcore.FrameRequest{Now: epoch.Add(at), Surface: platform.Surface{Width: w, Height: h, Scale: scale, Visible: true}}
}

func frame(t *testing.T, c *content, req appcore.FrameRequest) appcore.FrameResult {
	t.Helper()
	r, err := c.Frame(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Image.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOneEvaluationRecordsSemanticsLayoutAndCommands(t *testing.T) {
	calls := 0
	c := testContent(t, func(f *Frame) {
		calls++
		f.Label("Level")
		f.Meter("level", 0.25)
	})
	r := frame(t, c, request(0, 300, 120, 1))
	if calls != 1 || c.frames != 1 {
		t.Fatalf("view evaluated %d times for one frame", calls)
	}
	s := c.last
	if len(s.items) != 2 || s.items[0].role != roleLabel || s.items[0].name != "Level" ||
		s.items[1].role != roleMeter || s.items[1].key != "level" || s.items[1].value != 0.25 {
		t.Fatalf("record %+v", s.items)
	}
	label, meter := s.rects[0], s.rects[1]
	if label.Min != image.Pt(16, 16) || meter.Min.Y <= label.Max.Y || meter.Dy() != 14 || meter.Dx() != 300-32 {
		t.Fatalf("layout %v %v", label, meter)
	}
	var clips, texts int
	for _, cmd := range s.commands {
		switch cmd.op {
		case opClip:
			clips++
		case opText:
			texts++
		}
	}
	if s.commands[0].op != opClear || clips != 2 || texts != 1 || r.Animate {
		t.Fatalf("commands %d clips=%d texts=%d animate=%t", len(s.commands), clips, texts, r.Animate)
	}
	// Label pixels are drawn inside the label and nothing outside widgets.
	img := r.Image
	inked := false
	for y := range img.Height {
		for x := range img.Width {
			p := img.Pix[y*img.Stride+x*4:]
			if p[0] == background.B && p[1] == background.G && p[2] == background.R {
				continue
			}
			at := image.Pt(x, y)
			if !at.In(label) && !at.In(meter) {
				t.Fatalf("ink outside widgets at %v", at)
			}
			if at.In(label) {
				inked = true
			}
		}
	}
	if !inked {
		t.Fatal("label drew no glyphs")
	}
}

func TestMeterDecayUsesElapsedTimeAndAnimatesOnlyWhileAboveLevel(t *testing.T) {
	level := 1.0
	c := testContent(t, func(f *Frame) { f.Meter("m", level) })
	if frame(t, c, request(0, 200, 60, 1)).Animate {
		t.Fatal("animating at rest")
	}
	level = 0
	// Missed frames neither slow the decay nor need catching up: one frame a
	// second later shows the same peak as four frames over that second.
	sparse := frame(t, c, request(time.Second, 200, 60, 1))
	if got := c.last.items[0].peak; got != 0.5 || !sparse.Animate {
		t.Fatalf("peak %v animate %t after 1s", got, sparse.Animate)
	}
	level = 1
	frame(t, c, request(2*time.Second, 200, 60, 1)) // immediate attack
	if c.last.items[0].peak != 1 {
		t.Fatal("attack not immediate")
	}
	level = 0
	for _, at := range []time.Duration{2250, 2500, 2750, 3000} {
		frame(t, c, request(at*time.Millisecond, 200, 60, 1))
	}
	if got := c.last.items[0].peak; got != 0.5 {
		t.Fatalf("dense peak %v", got)
	}
	if frame(t, c, request(4*time.Second, 200, 60, 1)).Animate || c.last.items[0].peak != 0 {
		t.Fatal("still animating after the peak reached the level")
	}
	// Removing a keyed widget discards its state; reintroducing starts fresh.
	show := false
	c.draw = func(f *Frame) {
		if show {
			f.Meter("m", 0.2)
		}
	}
	frame(t, c, request(5*time.Second, 200, 60, 1))
	if len(c.meters) != 0 {
		t.Fatal("removed meter kept state")
	}
	show = true
	if frame(t, c, request(6*time.Second, 200, 60, 1)).Animate || c.last.items[0].peak != 0.2 {
		t.Fatal("reintroduced meter inherited old state")
	}
}

func TestScaleMultipliesLogicalGeometry(t *testing.T) {
	draw := func(f *Frame) { f.Label("Scale"); f.Meter("m", 0.5) }
	one, two := testContent(t, draw), testContent(t, draw)
	frame(t, one, request(0, 300, 120, 1))
	frame(t, two, request(0, 600, 240, 2))
	a, b := one.last.rects, two.last.rects
	if b[1].Min.X != 2*a[1].Min.X || b[1].Dy() != 2*a[1].Dy() || b[1].Dx() != 2*a[1].Dx() {
		t.Fatalf("meter %v at scale 1, %v at scale 2", a[1], b[1])
	}
	if h1, h2 := a[0].Dy(), b[0].Dy(); h2 < 2*h1-2 || h2 > 2*h1+2 {
		t.Fatalf("label height %d at scale 1, %d at scale 2", h1, h2)
	}
	if got := frame(t, two, request(0, 600, 240, 2)).Image; got.Scale != 2 || got.Width != 600 {
		t.Fatalf("image %dx%d@%v", got.Width, got.Height, got.Scale)
	}
}

func TestOverflowIsClippedToTheWidget(t *testing.T) {
	c := testContent(t, func(f *Frame) {
		f.Label("averyveryverylongwordthatcannotwrapinsideanarrowwindow")
		f.Meter("m", 1)
	})
	r := frame(t, c, request(0, 80, 200, 1))
	label := c.last.rects[0]
	if label.Dx() != 80-32 {
		t.Fatalf("an unbreakable word widened its label past the column: %v", label)
	}
	for y := range r.Image.Height {
		for x := label.Max.X; x < r.Image.Width; x++ {
			p := r.Image.Pix[y*r.Image.Stride+x*4:]
			if p[0] != background.B || p[1] != background.G || p[2] != background.R {
				t.Fatalf("overflowing text painted at %d,%d outside %v", x, y, label)
			}
		}
	}
}

func TestProgrammerErrorsPanicWithContext(t *testing.T) {
	var kept *Frame
	for name, draw := range map[string]func(*Frame){
		"duplicate key": func(f *Frame) { f.Meter("gain", 0); f.Meter("gain", 0) },
		"NaN":           func(f *Frame) { f.Meter("gain", nan()) },
		"range":         func(f *Frame) { f.Meter("gain", 1.5) },
		"empty key":     func(f *Frame) { f.Meter("", 0) },
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
	c := testContent(t, func(f *Frame) { kept = f })
	frame(t, c, request(0, 100, 100, 1))
	defer func() {
		if p := recover(); p == nil || !strings.Contains(p.(string), "after the draw call returned") {
			t.Fatalf("use after return: %v", p)
		}
	}()
	kept.Label("late")
}

func nan() float64 { var z float64; return z / z }

func TestOversizedSurfaceFailsAndKeepsThePreviousFrame(t *testing.T) {
	c := testContent(t, func(f *Frame) { f.Label("x") })
	c.canvas.Limit = 200 * 100 * 4
	first := frame(t, c, request(0, 200, 100, 1))
	before := append([]byte(nil), first.Image.Pix...)
	if _, err := c.Frame(request(0, 400, 400, 1)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized frame: %v", err)
	}
	if c.canvas.Width != 200 || string(c.canvas.Pix) != string(before) {
		t.Fatal("failed resize replaced the retained Go frame")
	}
}

func TestReleaseDropsWindowResources(t *testing.T) {
	c := testContent(t, func(f *Frame) { f.Label("x"); f.Meter("m", 0.5) })
	frame(t, c, request(0, 200, 100, 1))
	if c.text.Stats().GlyphEntries == 0 {
		t.Fatal("no cached glyphs")
	}
	c.Release()
	if c.text != nil || c.canvas.Pix != nil || c.meters != nil || c.last != nil {
		t.Fatal("release kept window resources")
	}
}

type observingEngine struct {
	text.Engine
	beforeRaster func()
}

func (e observingEngine) Rasterize(font text.FontID, glyph text.GlyphID, size text.Fixed, subpixel int) (text.Bitmap, error) {
	e.beforeRaster()
	return e.Engine.Rasterize(font, glyph, size, subpixel)
}

func TestFrameDropsTransientSceneLines(t *testing.T) {
	for _, mode := range []string{"success", "resize error", "raster error", "raster panic"} {
		t.Run(mode, func(t *testing.T) {
			c := testContent(t, func(f *Frame) { f.Label("transient label") })
			defer c.Release()
			c.budget.ParagraphBytes = 1 // Every paragraph and wrap must use scratch.
			base, err := c.engine()
			if err != nil {
				t.Fatal(err)
			}
			fault := errors.New("injected raster failure")
			rasterCalls := 0
			probe := observingEngine{Engine: base, beforeRaster: func() {
				rasterCalls++
				if c.text.Stats().TransientBytes == 0 {
					t.Fatal("scratch was released before rendering")
				}
				found := false
				for _, cmd := range c.last.commands {
					if cmd.op == opText && len(cmd.lines) > 0 {
						found = true
					}
				}
				if !found {
					t.Fatal("text lines were dropped before rendering")
				}
				if mode == "raster panic" {
					panic(fault)
				}
			}}
			if mode == "raster error" {
				probe.Engine = failingRasterEngine{Engine: base, err: fault}
			}
			c.engine = func() (text.Engine, error) { return probe, nil }
			if mode == "resize error" {
				c.canvas.Limit = 1
			}
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				_, err = c.Frame(request(0, 200, 100, 1))
			}()
			switch mode {
			case "success":
				if err != nil || recovered != nil {
					t.Fatalf("frame: err=%v panic=%v", err, recovered)
				}
			case "resize error":
				if err == nil || !strings.Contains(err.Error(), "limit") || recovered != nil || rasterCalls != 0 {
					t.Fatalf("resize: err=%v panic=%v raster calls=%d", err, recovered, rasterCalls)
				}
			case "raster error":
				if !errors.Is(err, fault) || recovered != nil {
					t.Fatalf("raster: err=%v panic=%v", err, recovered)
				}
			case "raster panic":
				if recovered != fault {
					t.Fatalf("panic=%v, want injected fault", recovered)
				}
			}
			if mode != "resize error" && rasterCalls == 0 {
				t.Fatal("rendering was not exercised")
			}
			if st := c.text.Stats(); st.TransientParas == 0 || st.ParagraphEntries != 0 || st.TransientBytes != 0 {
				t.Fatalf("expected transient paragraph and released frame accounting: %+v", st)
			}
			if c.last == nil || len(c.last.rects) != 1 {
				t.Fatal("layout metadata was not retained")
			}
			texts := 0
			for _, cmd := range c.last.commands {
				if cmd.op == opText {
					texts++
				}
				if cmd.lines != nil {
					t.Fatal("retained scene keeps text lines after scratch accounting ended")
				}
			}
			if texts != 1 {
				t.Fatalf("text command metadata count=%d, want 1", texts)
			}
		})
	}
}

type failingRasterEngine struct {
	text.Engine
	err error
}

func (e failingRasterEngine) Rasterize(text.FontID, text.GlyphID, text.Fixed, int) (text.Bitmap, error) {
	return text.Bitmap{}, e.err
}
