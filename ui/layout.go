package ui

import (
	"fmt"
	"image"
	"math"

	"github.com/michaelquigley/dfx2/internal/text"
)

// Layout sizes a Row or Column, in logical units.
//
// A container's children keep the size they measure at, along both axes: nothing
// stretches implicitly. Width and Height request a size; zero means intrinsic.
// Flex is a positive weight: children with one share the main-axis space left
// after their siblings and gaps, within their own bounds, and it takes
// precedence over the child's main-axis Width or Height. A parent with no size
// of its own to share gives a flex child its intrinsic size. A container with a
// flex child fills the space its parent offers. If minimum sizes exceed the
// space available, the minima hold and the parent clips the overflow.
type Layout struct {
	Width, Height                            float64
	Padding, Gap                             float64
	MinWidth, MinHeight, MaxWidth, MaxHeight float64 // zero max means unbounded
	Flex                                     float64
}

func (l Layout) validate(what string) {
	values := map[string]float64{"Width": l.Width, "Height": l.Height, "Padding": l.Padding, "Gap": l.Gap,
		"MinWidth": l.MinWidth, "MinHeight": l.MinHeight, "MaxWidth": l.MaxWidth, "MaxHeight": l.MaxHeight, "Flex": l.Flex}
	for name, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			panic(fmt.Sprintf("dfx2 ui: %s layout %s %v must be finite and not negative", what, name, v))
		}
	}
	if l.MaxWidth != 0 && l.MinWidth > l.MaxWidth {
		panic(fmt.Sprintf("dfx2 ui: %s layout MinWidth %v exceeds MaxWidth %v", what, l.MinWidth, l.MaxWidth))
	}
	if l.MaxHeight != 0 && l.MinHeight > l.MaxHeight {
		panic(fmt.Sprintf("dfx2 ui: %s layout MinHeight %v exceeds MaxHeight %v", what, l.MinHeight, l.MaxHeight))
	}
}

// Intrinsic widget metrics, logical units.
const (
	meterWidth     = 120.0 // a meter with no width offered, inside a row
	meterHeight    = 14.0
	faderWidth     = 36.0
	faderHeight    = 160.0
	faderSlot      = 6.0
	faderThumb     = 12.0
	checkboxSize   = 16.0
	checkboxGap    = 6.0
	checkboxInset  = 3.0 // room for the focus ring, so it never covers the box
	labelSize      = 14.0
	focusRing      = 2.0
	checkboxStroke = 1.5
)

type axis uint8

const (
	axisX axis = iota
	axisY
)

// unbounded offers a child all the room it wants. Sizes stay far below it, so
// sums cannot overflow.
const unbounded = 1 << 40

// maxLength caps one requested length in physical pixels.
const maxLength = 1 << 30

// node is one recorded container or leaf. Solving fills pos, relative to its
// parent, and size, in physical pixels.
type node struct {
	item       int // index into Frame.items; -1 for a container
	main       axis
	layout     Layout
	children   []*node
	pos        [2]int
	size       [2]int
	lines      []text.Line
	textHeight int
}

// offer is what a parent gives a child: the room available along each axis and
// whether the child must take exactly that much.
type offer struct {
	room  [2]int
	exact [2]bool
}

// solver measures a recorded tree. Each node is solved exactly once per frame,
// bottom-up with offers passed down: flex shares come from what the node's
// nonflex children measured, so nothing replays the view or measures twice.
type solver struct {
	c     *content
	f     *Frame
	scale float64
}

func (s *solver) px(v float64) int {
	return int(min(math.Round(v*s.scale), maxLength)) // converted only once in range
}

func (s *solver) solve(n *node, o offer) error {
	if n.item >= 0 {
		return s.leaf(n, o)
	}
	l := n.layout
	requested := [2]int{s.px(l.Width), s.px(l.Height)}
	low := [2]int{s.px(l.MinWidth), s.px(l.MinHeight)}
	high := [2]int{unbounded, unbounded}
	if l.MaxWidth > 0 {
		high[axisX] = s.px(l.MaxWidth)
	}
	if l.MaxHeight > 0 {
		high[axisY] = s.px(l.MaxHeight)
	}
	padding, gap := s.px(l.Padding), s.px(l.Gap)

	// A definite length is fixed before the children are measured. Anything
	// else is the room to wrap and share within, and the result comes from them.
	var outer, inner [2]int
	var definite [2]bool
	for a := range 2 {
		switch {
		case o.exact[a]:
			outer[a], definite[a] = o.room[a], true
		case requested[a] > 0:
			outer[a], definite[a] = min(max(requested[a], low[a]), high[a]), true
		default:
			outer[a] = min(o.room[a], high[a])
		}
		inner[a] = outer[a]
		if outer[a] < unbounded {
			inner[a] = max(outer[a]-2*padding, 0)
		}
	}
	main, cross := n.main, 1-n.main
	var flexible []*node
	for _, child := range n.children {
		if child.item < 0 && child.layout.Flex > 0 {
			flexible = append(flexible, child)
		}
	}
	shares := definite[main] || (len(flexible) > 0 && outer[main] < unbounded)

	used := 0
	if len(n.children) > 1 {
		used = gap * (len(n.children) - 1)
	}
	// Children offered no definite main length measure at their intrinsic size.
	natural := offer{}
	natural.room[main], natural.room[cross] = unbounded, inner[cross]
	for _, child := range n.children {
		if shares && child.item < 0 && child.layout.Flex > 0 {
			continue
		}
		if err := s.solve(child, natural); err != nil {
			return err
		}
		used += child.size[main]
	}
	if shares && len(flexible) > 0 {
		lengths, err := s.distribute(flexible, main, inner[main]-used)
		if err != nil {
			return err
		}
		for i, length := range lengths {
			exact := natural
			exact.room[main], exact.exact[main] = length, true
			if err := s.solve(flexible[i], exact); err != nil {
				return err
			}
			used += flexible[i].size[main]
		}
	}

	breadth, position := 0, padding
	for _, child := range n.children {
		breadth = max(breadth, child.size[cross])
		child.pos[main], child.pos[cross] = position, padding
		position += child.size[main] + gap
	}
	measured := [2]int{}
	measured[main], measured[cross] = used+2*padding, breadth+2*padding
	for a := range 2 {
		switch {
		case definite[a]:
			n.size[a] = outer[a]
		case axis(a) == main && shares:
			n.size[a] = min(max(outer[a], low[a]), high[a])
		default:
			n.size[a] = min(max(measured[a], low[a]), high[a])
		}
	}
	return nil
}

// distribute shares space by weight among flex children, within their bounds.
// A share outside a child's bounds freezes that child at the bound, and the rest
// is shared again, as CSS resolves flexible lengths. Unfrozen shares round
// cumulatively, so together they lose no pixel.
//
// Each pass scales the unfrozen weights by their largest, so any finite weights
// give finite shares in proportion: summing 1e308 twice would overflow, and the
// NaN shares that follow never freeze, never settle and never end the loop.
// Scaling over the unfrozen children only keeps the proportions among those
// still sharing once a heavy child has frozen at its bound.
func (s *solver) distribute(children []*node, main axis, space int) ([]int, error) {
	type flexChild struct {
		weight    float64
		low, high int
		frozen    bool
		share     float64
	}
	items := make([]flexChild, len(children))
	for i, child := range children {
		l := child.layout
		low, high := l.MinWidth, l.MaxWidth
		if main == axisY {
			low, high = l.MinHeight, l.MaxHeight
		}
		items[i] = flexChild{weight: l.Flex, low: s.px(low), high: unbounded}
		if high > 0 {
			items[i].high = s.px(high)
		}
	}
	// Unreachable from validated layouts; a hang or a bogus length would be
	// worse than failing the frame.
	fail := func(what string) error {
		w := make([]float64, len(items))
		for i, it := range items {
			w[i] = it.weight
		}
		return fmt.Errorf("dfx2 ui: flex distribution of %d over weights %v %s", space, w, what)
	}
	remaining := float64(max(space, 0))
	for {
		largest := 0.0
		for _, it := range items {
			if !it.frozen {
				largest = max(largest, it.weight)
			}
		}
		if largest == 0 {
			break // every child froze at a bound
		}
		total := 0.0
		for _, it := range items {
			if !it.frozen {
				total += it.weight / largest
			}
		}
		violation := 0.0
		for i := range items {
			it := &items[i]
			if it.frozen {
				continue
			}
			it.share = remaining * (it.weight / largest) / total
			violation += min(max(it.share, float64(it.low)), float64(it.high)) - it.share
		}
		if math.IsNaN(violation) || math.IsInf(violation, 0) {
			return nil, fail("is not finite")
		}
		if violation == 0 {
			break
		}
		froze := false
		for i := range items {
			it := &items[i]
			if it.frozen {
				continue
			}
			if violation > 0 && it.share < float64(it.low) {
				it.share, it.frozen = float64(it.low), true
			} else if violation < 0 && it.share > float64(it.high) {
				it.share, it.frozen = float64(it.high), true
			}
			if it.frozen {
				remaining -= it.share
				froze = true
			}
		}
		if !froze {
			return nil, fail("made no progress")
		}
	}
	lengths := make([]int, len(items))
	sum, rounded := 0.0, 0
	for i, it := range items {
		if it.frozen {
			lengths[i] = int(it.share)
			continue
		}
		sum += it.share
		next := int(math.Round(sum))
		lengths[i] = min(max(next-rounded, it.low), it.high)
		rounded = next
	}
	return lengths, nil
}

// leaf measures a widget from its intrinsic size and the width on offer. Text
// wraps to that width from cached shaping; nothing re-enters the view.
func (s *solver) leaf(n *node, o offer) error {
	it := &s.f.items[n.item]
	room := o.room[axisX]
	switch it.role {
	case roleLabel:
		lines, width, height, err := s.text(it.name, room)
		if err != nil {
			return err
		}
		n.lines, n.size = lines, [2]int{min(width, room), height}
	case roleMeter:
		width := room
		if room >= unbounded {
			width = s.px(meterWidth)
		}
		n.size = [2]int{width, s.px(meterHeight)}
	case roleFader:
		n.size = [2]int{s.px(faderWidth), s.px(faderHeight)}
	case roleCheckbox:
		inset := s.px(checkboxInset)
		lead := inset + s.px(checkboxSize) + s.px(checkboxGap)
		textRoom := room
		if room < unbounded {
			textRoom = max(room-lead, 1)
		}
		lines, width, height, err := s.text(it.name, textRoom)
		if err != nil {
			return err
		}
		n.lines, n.textHeight = lines, height
		n.size = [2]int{min(lead+width+inset, room), max(s.px(checkboxSize)+2*inset, height)}
	}
	return nil
}

func (s *solver) text(value string, room int) ([]text.Line, int, int, error) {
	p, err := s.c.text.Paragraph(value, text.FixedOf(float32(labelSize*s.scale)))
	if err != nil {
		return nil, 0, 0, err
	}
	lines, err := s.c.text.Lines(p, text.Fixed(min(max(room, 1), maxLength>>6)*64))
	if err != nil {
		return nil, 0, 0, err
	}
	var widest text.Fixed
	for _, line := range lines {
		widest = max(widest, line.Width)
	}
	return lines, int(math.Ceil(float64(widest.Float()))), int(math.Ceil(float64(text.Height(lines).Float()))), nil
}

// hit is one control in the last completed layout: its stable identity, its
// rectangle and the part of it the enclosing clips leave reachable.
type hit struct {
	path          string
	role          role
	rect, visible image.Rectangle
}
