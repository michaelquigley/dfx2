package ui

import (
	"fmt"
	"image"
	"math"

	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/render"
	"github.com/michaelquigley/dfx2/internal/text"
	"github.com/michaelquigley/dfx2/internal/text/gotext"
)

// Logical metrics of the stage 1 root column.
const (
	padding     = 16.0
	gap         = 10.0
	labelSize   = 14.0
	meterHeight = 14.0
	peakWidth   = 2.0
)

var (
	background = render.RGB(0x1e, 0x1f, 0x24)
	foreground = render.RGB(0xe8, 0xe6, 0xe3)
	track      = render.Mix(background, foreground, 0.12)
	meterZones = []struct {
		to    float64
		color render.Color
	}{{0.7, render.RGB(0x4c, 0xaf, 0x50)}, {0.9, render.RGB(0xff, 0xb3, 0x00)}, {1, render.RGB(0xe5, 0x39, 0x35)}}
)

// content is one window's UI state, owned by the UI thread: retained meter
// state by key, the per-window text caches and the reusable CPU frame.
type content struct {
	draw   func(*Frame)
	meters map[string]*meterState
	engine func() (text.Engine, error)
	budget text.Budget
	text   *text.System
	canvas render.Canvas
	last   *scene // most recent frame metadata, without text buffers, for tests
	frames int    // view evaluations
}

func newContent(draw func(*Frame)) *content {
	return &content{draw: draw, meters: make(map[string]*meterState), budget: text.DefaultBudget,
		engine: func() (text.Engine, error) { return gotext.Shared() }}
}

var _ appcore.Content = (*content)(nil)

// scene describes one frame: the semantic record, the measured layout and
// the draw commands rendered from it. Text buffers are borrowed until frame
// end, then cleared; the remaining metadata may be retained for tests.
type scene struct {
	items    []item
	rects    []image.Rectangle // physical pixels, parallel to items
	commands []command
	animate  bool
}

type op uint8

const (
	opClear op = iota
	opFill
	opLine
	opText
	opClip
	opUnclip
)

type command struct {
	op                op
	rect              image.Rectangle
	color             render.Color
	x0, y0, x1, y1, w float32
	lines             []text.Line
}

// Frame evaluates the view once, measures its record, renders the record and
// returns the image. Layout and rendering never call the view again.
func (c *content) Frame(req appcore.FrameRequest) (appcore.FrameResult, error) {
	if c.text == nil {
		engine, err := c.engine()
		if err != nil {
			return appcore.FrameResult{}, fmt.Errorf("ui fonts: %w", err)
		}
		c.text = text.NewSystem(engine, c.budget)
	}
	f := &Frame{c: c, now: req.Now, keys: make(map[string]bool)}
	for _, m := range c.meters {
		m.seen = false
	}
	c.frames++
	func() {
		defer func() { f.done = true }()
		c.draw(f)
	}()
	// Removing a widget discards its state; reintroducing the key starts fresh.
	for key, m := range c.meters {
		if !m.seen {
			delete(c.meters, key)
		}
	}
	c.text.BeginFrame()
	defer c.text.EndFrame()
	s, err := c.layout(f, req.Surface.Width, req.Surface.Height, req.Surface.Scale)
	if err != nil {
		return appcore.FrameResult{}, err
	}
	defer func() {
		// Registered after EndFrame's defer so borrowed text is dropped
		// before scratch accounting resets, including on errors and panics.
		for i := range s.commands {
			s.commands[i].lines = nil
		}
	}()
	c.last = s
	if err := c.canvas.Resize(req.Surface.Width, req.Surface.Height); err != nil {
		return appcore.FrameResult{}, err
	}
	if err := c.render(s); err != nil {
		return appcore.FrameResult{}, err
	}
	return appcore.FrameResult{Image: c.canvas.Image(req.Surface.Scale), Animate: f.animate}, nil
}

// Release drops per-window caches, retained state and pixels after native
// teardown. The shared font engine is process-owned and is not released here.
func (c *content) Release() {
	if c.text != nil {
		c.text.Release()
		c.text = nil
	}
	c.canvas.Release()
	c.meters, c.last = nil, nil
}

// layout measures the record in a single root column. Text wraps to the
// column's width from cached shaping; nothing re-enters the view.
func (c *content) layout(f *Frame, width, height int, scale float64) (*scene, error) {
	px := func(v float64) int { return int(math.Round(v * scale)) }
	s := &scene{items: f.items, animate: f.animate}
	s.commands = append(s.commands, command{op: opClear, color: background})
	left, right, y := px(padding), width-px(padding), px(padding)
	inner := max(right-left, 1)
	for _, it := range f.items {
		var r image.Rectangle
		cmds := []command{}
		switch it.role {
		case roleLabel:
			p, err := c.text.Paragraph(it.name, text.FixedOf(float32(labelSize*scale)))
			if err != nil {
				return nil, err
			}
			lines, err := c.text.Lines(p, text.Fixed(inner*64))
			if err != nil {
				return nil, err
			}
			r = image.Rect(left, y, right, y+int(math.Ceil(float64(text.Height(lines).Float()))))
			cmds = append(cmds, command{op: opText, rect: r, color: foreground, lines: lines})
		case roleMeter:
			r = image.Rect(left, y, right, y+px(meterHeight))
			cmds = append(cmds, command{op: opFill, rect: r, color: track})
			at := func(v float64) int { return r.Min.X + int(math.Round(v*float64(r.Dx()))) }
			from := 0.0
			for _, z := range meterZones {
				if from >= it.value {
					break
				}
				cmds = append(cmds, command{op: opFill, rect: image.Rect(at(from), r.Min.Y, at(min(z.to, it.value)), r.Max.Y), color: z.color})
				from = z.to
			}
			if it.peak > 0 {
				x := float32(r.Min.X) + float32(it.peak)*float32(r.Dx())
				cmds = append(cmds, command{op: opLine, x0: x, y0: float32(r.Min.Y), x1: x, y1: float32(r.Max.Y),
					w: float32(peakWidth * scale), color: foreground})
			}
		}
		s.rects = append(s.rects, r)
		// Every widget clips to its own rectangle; overflow never paints
		// over a neighbour.
		s.commands = append(s.commands, command{op: opClip, rect: r})
		s.commands = append(s.commands, cmds...)
		s.commands = append(s.commands, command{op: opUnclip})
		y = r.Max.Y + px(gap)
		if y >= height {
			// Remaining items are recorded but fall outside the surface.
			y = height
		}
	}
	return s, nil
}

func (c *content) render(s *scene) error {
	cv := &c.canvas
	for _, cmd := range s.commands {
		switch cmd.op {
		case opClear:
			cv.Clear(cmd.color)
		case opFill:
			cv.Fill(cmd.rect, cmd.color)
		case opLine:
			cv.Line(cmd.x0, cmd.y0, cmd.x1, cmd.y1, cmd.w, cmd.color)
		case opClip:
			cv.PushClip(cmd.rect)
		case opUnclip:
			cv.PopClip()
		case opText:
			if err := c.drawText(cmd.rect.Min, cmd.lines, cmd.color); err != nil {
				return err
			}
		}
	}
	return nil
}

// drawText places glyphs with fractional horizontal pens (quarter pixels) on
// whole-pixel baselines, compositing masks tinted and color glyphs as they are.
func (c *content) drawText(at image.Point, lines []text.Line, color render.Color) error {
	top := text.Fixed(at.Y * 64)
	for _, l := range lines {
		baseline := int(math.Round(float64((top + l.Ascent).Float())))
		for _, g := range l.Glyphs {
			x := float64(at.X) + float64(g.X.Float())
			ix := int(math.Floor(x))
			sub := int(math.Round((x - float64(ix)) * text.Subpixels))
			if sub == text.Subpixels {
				ix, sub = ix+1, 0
			}
			img, err := c.text.Glyph(text.GlyphKey{Font: g.Font, ID: g.ID, Size: g.Size, Subpixel: uint8(sub)})
			if err != nil {
				return err
			}
			if img.Width == 0 || img.Height == 0 {
				continue
			}
			gx, gy := ix+img.Left, baseline-int(math.Round(float64(g.Y.Float())))+img.Top
			if img.Color {
				c.canvas.Bitmap(gx, gy, img.Width, img.Height, img.Pix)
			} else {
				c.canvas.Mask(gx, gy, img.Width, img.Height, img.Pix, color)
			}
		}
		top += l.Ascent + l.Descent + l.Gap
	}
	return nil
}
