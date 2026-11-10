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

// Logical metrics of the root column, which keeps the placement views had
// before containers existed.
const (
	padding   = 16.0
	gap       = 10.0
	peakWidth = 2.0
)

var (
	background = render.RGB(0x1e, 0x1f, 0x24)
	foreground = render.RGB(0xe8, 0xe6, 0xe3)
	track      = render.Mix(background, foreground, 0.12)
	accent     = render.RGB(0x4f, 0xa3, 0xf7)
	idleFocus  = render.Mix(background, accent, 0.45) // focus in a window without native focus
	meterZones = []struct {
		to    float64
		color render.Color
	}{{0.7, render.RGB(0x4c, 0xaf, 0x50)}, {0.9, render.RGB(0xff, 0xb3, 0x00)}, {1, render.RGB(0xe5, 0x39, 0x35)}}
)

// content is one window's UI state, owned by the UI thread: retained meter and
// control state by scope path, the gesture and focus in progress, the last
// completed hit layout, the per-window text caches and the reusable CPU frame.
type content struct {
	draw     func(*Frame)
	meters   map[string]*meterState
	controls map[string]*control
	engine   func() (text.Engine, error)
	budget   text.Budget
	text     *text.System
	canvas   render.Canvas
	last     *scene // most recent frame metadata, without text buffers, for tests
	frames   int    // view evaluations

	capture, focus, spaceHeld string // control paths
	deferredPress             *deferredFaderPress
	active                    bool  // the window has native focus
	releaseCapture            bool  // this frame ended a gesture without a button-up edge
	hits                      []hit // the last completed layout's controls, in recorded order
	hitScale                  float64
}

func newContent(draw func(*Frame)) *content {
	return &content{draw: draw, meters: make(map[string]*meterState), controls: make(map[string]*control), budget: text.DefaultBudget,
		engine: func() (text.Engine, error) { return gotext.Shared() }}
}

var _ appcore.Content = (*content)(nil)

// scene describes one frame: the semantic record, the measured layout and
// the draw commands rendered from it. Text buffers are borrowed until frame
// end, then cleared; the remaining metadata may be retained for tests.
type scene struct {
	items    []item
	rects    []image.Rectangle // physical pixels, parallel to items
	hits     []hit
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

// Frame advances pending interaction, evaluates the view once, measures its
// record, renders the record and returns the image. Layout and rendering never
// call the view again, and a consumed input event is never replayed.
func (c *content) Frame(req appcore.FrameRequest) (appcore.FrameResult, error) {
	if c.text == nil {
		engine, err := c.engine()
		if err != nil {
			return appcore.FrameResult{}, fmt.Errorf("ui fonts: %w", err)
		}
		c.text = text.NewSystem(engine, c.budget)
	}
	c.releaseCapture = false
	consumed := c.advance(req.Input, req.InputLost)
	root := &node{item: -1, main: axisY, layout: Layout{Padding: padding, Gap: gap}}
	f := &Frame{c: c, now: req.Now, keys: make(map[string]bool), root: root, current: root}
	for _, m := range c.meters {
		m.seen = false
	}
	for _, ctl := range c.controls {
		ctl.seen = false
	}
	c.frames++
	func() {
		defer func() { f.done = true }()
		c.draw(f)
	}()
	c.prune()
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
	// Input from here on targets this layout. A new control becomes interactive now.
	c.hits, c.hitScale = s.hits, req.Surface.Scale
	return appcore.FrameResult{Image: c.canvas.Image(req.Surface.Scale), Animate: f.animate, Consumed: consumed, ReleaseCapture: c.releaseCapture}, nil
}

// prune discards the state of every key this view left out; reintroducing the
// key starts fresh. A removed gesture's target yields no edit, and any pending
// one goes with it.
func (c *content) prune() {
	for key, m := range c.meters {
		if !m.seen {
			delete(c.meters, key)
		}
	}
	for path, ctl := range c.controls {
		if !ctl.seen {
			delete(c.controls, path)
			c.forget(path)
		}
	}
}

// Release drops per-window caches, retained state and pixels after native
// teardown. The shared font engine is process-owned and is not released here.
// TextStats reports this window's text cache counters, for internal auditions
// through appcore.AuditionContent. UI thread only; zero before the first frame.
func (c *content) TextStats() text.Stats {
	if c.text == nil {
		return text.Stats{}
	}
	return c.text.Stats()
}

func (c *content) Release() {
	if c.text != nil {
		c.text.Release()
		c.text = nil
	}
	c.canvas.Release()
	c.meters, c.controls, c.last, c.hits = nil, nil, nil, nil
	c.deferredPress = nil
}

// layout solves the recorded tree against the surface and emits draw commands
// and hit rectangles. Nothing re-enters the view.
func (c *content) layout(f *Frame, width, height int, scale float64) (*scene, error) {
	sv := &solver{c: c, f: f, scale: scale}
	err := sv.solve(f.root, offer{room: [2]int{width, height}, exact: [2]bool{true, true}})
	if err != nil {
		return nil, err
	}
	s := &scene{items: f.items, animate: f.animate, rects: make([]image.Rectangle, len(f.items))}
	s.commands = append(s.commands, command{op: opClear, color: background})
	surface := image.Rect(0, 0, width, height)
	for _, child := range f.root.children {
		c.emit(s, sv, child, image.Point{}, surface)
	}
	return s, nil
}

// emit places a node under its parent and records its commands. Every container
// and widget clips to its own rectangle, so overflow never paints over a
// neighbour; what a clip hides cannot be pressed either.
func (c *content) emit(s *scene, sv *solver, n *node, origin image.Point, clip image.Rectangle) {
	at := origin.Add(image.Pt(n.pos[axisX], n.pos[axisY]))
	r := image.Rectangle{Min: at, Max: at.Add(image.Pt(n.size[axisX], n.size[axisY]))}
	visible := r.Intersect(clip)
	s.commands = append(s.commands, command{op: opClip, rect: r})
	if n.item < 0 {
		for _, child := range n.children {
			c.emit(s, sv, child, at, visible)
		}
	} else {
		it := &s.items[n.item]
		s.rects[n.item] = r
		s.commands = append(s.commands, sv.widget(it, n, r)...)
		if it.role == roleFader || it.role == roleCheckbox {
			s.hits = append(s.hits, hit{path: it.key, role: it.role, rect: r, visible: visible})
		}
	}
	s.commands = append(s.commands, command{op: opUnclip})
}

// widget records one widget's drawing, in physical pixels.
func (s *solver) widget(it *item, n *node, r image.Rectangle) []command {
	var cmds []command
	switch it.role {
	case roleLabel:
		cmds = append(cmds, command{op: opText, rect: r, color: foreground, lines: n.lines})
	case roleMeter:
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
				w: float32(peakWidth * s.scale), color: foreground})
		}
	case roleFader:
		thumb := thumbHeight(s.scale)
		top := r.Min.Y + int(math.Round((1-it.value)*float64(max(r.Dy()-thumb, 0))))
		slot := s.px(faderSlot)
		center := r.Min.X + r.Dx()/2
		cmds = append(cmds,
			command{op: opFill, rect: image.Rect(center-slot/2, r.Min.Y, center-slot/2+slot, r.Max.Y), color: track},
			command{op: opFill, rect: image.Rect(center-slot/2, top, center-slot/2+slot, r.Max.Y), color: accent})
		color := foreground
		if it.pressed {
			color = accent
		}
		cmds = append(cmds, command{op: opFill, rect: image.Rect(r.Min.X, top, r.Max.X, top+thumb), color: color})
		cmds = append(cmds, s.focusCommands(it, r)...)
	case roleCheckbox:
		size, inset := s.px(checkboxSize), s.px(checkboxInset)
		corner := image.Pt(r.Min.X+inset, r.Min.Y+(r.Dy()-size)/2)
		box := image.Rectangle{Min: corner, Max: corner.Add(image.Pt(size, size))}
		if it.pressed {
			cmds = append(cmds, command{op: opFill, rect: box, color: track})
		}
		if it.value == 1 {
			cmds = append(cmds, command{op: opFill, rect: box, color: accent})
			line := float32(checkboxStroke * 1.4 * s.scale)
			x, y, u := float32(box.Min.X), float32(box.Min.Y), float32(size)
			cmds = append(cmds,
				command{op: opLine, x0: x + 0.22*u, y0: y + 0.52*u, x1: x + 0.42*u, y1: y + 0.72*u, w: line, color: foreground},
				command{op: opLine, x0: x + 0.42*u, y0: y + 0.72*u, x1: x + 0.78*u, y1: y + 0.28*u, w: line, color: foreground})
		}
		cmds = append(cmds, stroke(box, max(s.px(checkboxStroke), 1), foreground)...)
		label := r
		label.Min.X += inset + size + s.px(checkboxGap)
		label.Min.Y += (r.Dy() - n.textHeight) / 2
		cmds = append(cmds, command{op: opText, rect: label, color: foreground, lines: n.lines})
		cmds = append(cmds, s.focusCommands(it, r)...)
	}
	return cmds
}

// focusCommands draws the visible focus ring, inside the widget's own clip.
// Each window keeps its own focused control; it dims without native focus.
func (s *solver) focusCommands(it *item, r image.Rectangle) []command {
	if !it.focused {
		return nil
	}
	color := idleFocus
	if s.c.active {
		color = accent
	}
	return stroke(r, max(s.px(focusRing), 1), color)
}

// stroke outlines the inside of r with four fills.
func stroke(r image.Rectangle, width int, color render.Color) []command {
	return []command{
		{op: opFill, rect: image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+width), color: color},
		{op: opFill, rect: image.Rect(r.Min.X, r.Max.Y-width, r.Max.X, r.Max.Y), color: color},
		{op: opFill, rect: image.Rect(r.Min.X, r.Min.Y, r.Min.X+width, r.Max.Y), color: color},
		{op: opFill, rect: image.Rect(r.Max.X-width, r.Min.Y, r.Max.X, r.Max.Y), color: color},
	}
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
			if cmd.rect.Intersect(cv.Clip()).Empty() {
				continue // scrolled out by a clip: no rasterizing for invisible text
			}
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
