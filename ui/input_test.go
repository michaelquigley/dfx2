package ui

import (
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// bench is a content window with the core's queue contract around it: unconsumed
// events wait for a later frame. Every edit a view reports lands in commands, so
// a duplicate or missing dispatch fails the comparison exactly.
type bench struct {
	t        *testing.T
	c        *content
	queue    []platform.Input
	width    int
	height   int
	scale    float64
	lost     bool
	commands []string
	released int
}

func newBench(t *testing.T, draw func(b *bench, f *Frame)) *bench {
	t.Helper()
	b := &bench{t: t, width: 400, height: 400, scale: 1}
	b.c = testContent(t, func(f *Frame) { draw(b, f) })
	b.frame() // a first completed layout, as every window has before input can target it
	return b
}

func (b *bench) frame() {
	b.t.Helper()
	req := request(0, b.width, b.height, b.scale)
	req.Input, req.InputLost = slices.Clone(b.queue), b.lost
	b.lost = false
	r := frame(b.t, b.c, req)
	if r.Consumed > len(b.queue) || (r.Consumed == 0 && len(b.queue) > 0) {
		b.t.Fatalf("consumed %d of %d", r.Consumed, len(b.queue))
	}
	b.queue = b.queue[r.Consumed:]
	if r.ReleaseCapture {
		b.released++
	}
}

// settle runs frames until no input remains.
func (b *bench) settle() {
	b.t.Helper()
	for len(b.queue) > 0 {
		b.frame()
	}
}

func (b *bench) send(inputs ...platform.Input) { b.queue = append(b.queue, inputs...) }

func (b *bench) want(commands ...string) {
	b.t.Helper()
	if !slices.Equal(b.commands, commands) {
		b.t.Fatalf("commands %q, want %q", b.commands, commands)
	}
}

func (b *bench) center(key string) (x, y float64) {
	b.t.Helper()
	r := rectOf(b.t, b.c, key)
	return float64(r.Min.X+r.Max.X) / 2 / b.scale, float64(r.Min.Y+r.Max.Y) / 2 / b.scale
}

func down(x, y float64) platform.Input {
	return platform.Input{Kind: platform.PointerDown, Button: platform.ButtonLeft, X: x, Y: y}
}

func up(x, y float64) platform.Input {
	return platform.Input{Kind: platform.PointerUp, Button: platform.ButtonLeft, X: x, Y: y}
}

func motion(x, y float64) platform.Input {
	return platform.Input{Kind: platform.PointerMove, X: x, Y: y}
}

func key(k platform.Key) platform.Input   { return platform.Input{Kind: platform.KeyDown, Key: k} }
func keyUp(k platform.Key) platform.Input { return platform.Input{Kind: platform.KeyUp, Key: k} }

func tab() platform.Input { return key(platform.KeyTab) }

func shiftTab() platform.Input {
	return platform.Input{Kind: platform.KeyDown, Key: platform.KeyTab, Modifiers: platform.ModShift}
}

// fader records one fader over a model value and reports its edits. Applying an
// edit to the model is the application's business, so tests choose.
func (b *bench) fader(f *Frame, key string, value *float64) {
	out, changed := f.Fader(key, *value)
	if changed {
		b.commands = append(b.commands, fmt.Sprintf("%s=%g", key, out))
		*value = out
	}
}

func (b *bench) checkbox(f *Frame, key string, value *bool) {
	out, changed := f.Checkbox(key, key, *value)
	if changed {
		b.commands = append(b.commands, fmt.Sprintf("%s=%t", key, out))
		*value = out
	}
}

func itemOf(t *testing.T, c *content, key string) item {
	t.Helper()
	for _, it := range c.last.items {
		if it.key == key {
			return it
		}
	}
	t.Fatalf("no item %q", key)
	return item{}
}

// The default fader is 160 tall with a 12 thumb, so at the root its travel maps
// y=22 to 1 and y=170 to 0.
const faderTop, faderBottom = 22.0, 170.0

func TestFaderDragFollowsThePointerHighAtTopAndClamps(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	x := 34.0
	b.send(down(x, faderTop))
	b.frame()
	b.want("g=1")
	if !itemOf(t, b.c, "g").pressed || b.c.focus != "g" {
		t.Fatalf("press did not focus and capture: %+v focus=%q", itemOf(t, b.c, "g"), b.c.focus)
	}
	b.send(motion(x, (faderTop+faderBottom)/2))
	b.frame()
	// Far outside the window still tracks under capture, clamped.
	b.send(motion(-500, 5000))
	b.frame()
	b.send(motion(900, -300))
	b.frame()
	b.send(up(900, -300))
	b.settle()
	b.want("g=1", "g=0.5", "g=0", "g=1") // the release at the last value adds no duplicate
	if b.c.capture != "" || itemOf(t, b.c, "g").pressed {
		t.Fatal("release left the gesture active")
	}
	b.frame()
	if b.released != 0 {
		t.Fatalf("a normal release asked for %d capture releases", b.released)
	}
}

func TestReleaseReportsOnlyARemainingChange(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	x, y := b.center("g")
	b.send(down(x, y)) // the center of a 0.5 thumb's travel: no edit at all
	b.frame()
	b.want()
	b.send(up(x, faderBottom))
	b.settle()
	b.want("g=0")
}

func TestQueuedDragAndReleaseEachBecomeOneEditPerEvaluation(t *testing.T) {
	gain, evaluations := 0.5, 0
	b := newBench(t, func(b *bench, f *Frame) { evaluations++; b.fader(f, "g", &gain) })
	evaluations = 0
	x := 34.0
	b.send(down(x, faderTop), motion(x, faderBottom), motion(x, faderTop), up(x, faderBottom))
	b.settle()
	b.want("g=1", "g=0", "g=1", "g=0")
	if evaluations != 4 {
		t.Fatalf("%d evaluations for four edits", evaluations)
	}
	b.frame()
	b.want("g=1", "g=0", "g=1", "g=0") // nothing replays
}

func TestTwoActivationsBeforeAFrameAreTwoEdits(t *testing.T) {
	muted, evaluations := false, 0
	b := newBench(t, func(b *bench, f *Frame) { evaluations++; b.checkbox(f, "m", &muted) })
	evaluations = 0
	x, y := b.center("m")
	b.send(down(x, y), up(x, y), down(x, y), up(x, y))
	b.frame()
	b.want("m=true")
	b.settle()
	b.want("m=true", "m=false")
	if evaluations != 2 {
		t.Fatalf("%d evaluations", evaluations)
	}
	b.frame()
	b.want("m=true", "m=false")
	// The same holds for Space, without leaning on the pointer.
	b.commands = nil
	b.send(key(platform.KeySpace), keyUp(platform.KeySpace), key(platform.KeySpace), keyUp(platform.KeySpace))
	b.settle()
	b.want("m=true", "m=false")
}

func TestDeferredFaderPressKeepsItsTarget(t *testing.T) {
	for _, change := range []string{"reorder", "remove", "overflow"} {
		t.Run(change, func(t *testing.T) {
			order := []string{"a", "b"}
			values := map[string]float64{"a": 0.5, "b": 0.5}
			b := newBench(t, func(b *bench, f *Frame) {
				f.Row(Layout{Gap: 8}, func(f *Frame) {
					for _, id := range order {
						value := values[id]
						b.fader(f, id, &value)
						values[id] = value
					}
				})
			})
			x, _ := b.center("a")
			b.send(down(x, faderTop), up(x, faderTop), down(x, faderBottom), up(x, faderBottom))
			order = []string{"b", "a"}
			if change == "remove" {
				order = []string{"b"}
			}
			b.frame()
			if len(b.queue) != 2 {
				t.Fatalf("remaining events %d, want deferred press and release", len(b.queue))
			}
			switch change {
			case "reorder":
				b.settle()
				b.frame()
				b.want("a=1", "a=0")
			case "remove":
				b.settle()
				b.frame()
				b.want() // A's edits disappear with it; B inherits neither.
			case "overflow":
				b.queue, b.lost = nil, true
				b.frame()
				x, _ = b.center("b")
				b.send(down(x, faderTop), up(x, faderTop))
				b.settle()
				b.frame()
				b.want("a=1", "b=1")
			}
		})
	}
}

func TestDeferredFaderPressKeepsItsResolvedValue(t *testing.T) {
	moved := false
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) {
		if moved {
			f.Label("move the fader down")
		}
		b.fader(f, "a", &gain)
	})
	x, _ := b.center("a")
	b.send(down(x, faderTop), up(x, faderTop), down(x, faderBottom))
	moved = true
	b.frame()
	b.want("a=1")
	b.settle()
	b.want("a=1", "a=0")
	// A later, newly delivered release uses the current geometry.
	r := rectOf(t, b.c, "a")
	b.send(up(x, float64(r.Max.Y)-float64(thumbHeight(b.scale))/2))
	b.settle()
	b.frame()
	b.want("a=1", "a=0")
}

// A release validated against the layout the user clicked must survive the
// one-edit-per-evaluation boundary, even if that evaluation moves its target.
func TestDeferredCheckboxReleaseKeepsValidatedTarget(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow=%t", overflow), func(t *testing.T) {
			order := []string{"a", "b"}
			values := map[string]bool{"a": false, "b": false}
			b := newBench(t, func(b *bench, f *Frame) {
				f.Row(Layout{Gap: 8}, func(f *Frame) {
					for _, id := range order {
						value := values[id]
						b.checkbox(f, id, &value)
						values[id] = value
					}
				})
			})
			x, y := b.center("a")
			b.send(down(x, y), up(x, y), down(x, y), up(x, y))
			order = []string{"b", "a"}
			b.frame()
			b.want("a=true")
			if len(b.queue) != 1 {
				t.Fatalf("remaining events %d, want one deferred release", len(b.queue))
			}
			if overflow {
				// Overflow discards that release and cancels its gesture. A
				// fresh press followed by a release outside must not activate.
				b.queue, b.lost = nil, true
				b.frame()
				x, y = b.center("a")
				b.send(down(x, y), up(-10, -10))
				b.settle()
				b.frame()
				b.want("a=true")
			} else {
				b.settle()
				b.want("a=true", "a=false")
				b.frame()
				b.want("a=true", "a=false") // no replay
			}
		})
	}
}

func TestActivationsOnStaleSnapshotsStillToggleFromWhatWasShown(t *testing.T) {
	// The application has not acknowledged anything: it keeps supplying false.
	b := newBench(t, func(b *bench, f *Frame) {
		if out, changed := f.Checkbox("m", "Mute", false); changed {
			b.commands = append(b.commands, fmt.Sprint(out))
		}
	})
	x, y := b.center("m")
	b.send(down(x, y), up(x, y), down(x, y), up(x, y))
	b.settle()
	b.want("true", "false")
	if itemOf(t, b.c, "m").value != 0 {
		t.Fatal("not showing the last edit")
	}
}

func TestCheckboxActivatesOnReleaseInsideOnly(t *testing.T) {
	muted := false
	b := newBench(t, func(b *bench, f *Frame) { b.checkbox(f, "m", &muted) })
	x, y := b.center("m")
	b.send(down(x, y))
	b.frame()
	b.want()
	if !itemOf(t, b.c, "m").pressed {
		t.Fatal("not shown pressed")
	}
	b.send(motion(900, 900), up(900, 900))
	b.settle()
	b.frame()
	b.want()
	if itemOf(t, b.c, "m").pressed {
		t.Fatal("stuck pressed")
	}
	b.send(platform.Input{Kind: platform.PointerDown, Button: platform.ButtonRight, X: x, Y: y},
		platform.Input{Kind: platform.PointerUp, Button: platform.ButtonRight, X: x, Y: y})
	b.settle()
	b.want() // only the left button operates controls
	b.send(down(x, y), up(x, y))
	b.settle()
	b.want("m=true")
}

func TestSpaceActivatesOnReleaseAndOnlyIfStillHeld(t *testing.T) {
	muted := false
	b := newBench(t, func(b *bench, f *Frame) { b.checkbox(f, "m", &muted) })
	b.send(tab(), key(platform.KeySpace))
	b.settle()
	b.want()
	if !itemOf(t, b.c, "m").pressed {
		t.Fatal("not shown pressed")
	}
	b.send(key(platform.KeySpace), key(platform.KeySpace)) // auto-repeat
	b.settle()
	b.want()
	b.send(keyUp(platform.KeySpace))
	b.settle()
	b.want("m=true")
	// A stray release, pressed elsewhere, activates nothing.
	b.send(keyUp(platform.KeySpace))
	b.settle()
	b.want("m=true")
	b.send(key(platform.KeySpace), platform.Input{Kind: platform.FocusLost}, keyUp(platform.KeySpace))
	b.settle()
	b.want("m=true")
}

func TestFaderKeyboardStepsFromWhatIsShown(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	b.send(tab(), key(platform.KeyArrowUp), key(platform.KeyArrowUp), key(platform.KeyArrowRight),
		key(platform.KeyArrowDown), key(platform.KeyArrowLeft), key(platform.KeyPageUp), key(platform.KeyPageDown),
		key(platform.KeyEnd), key(platform.KeyEnd), key(platform.KeyArrowUp), key(platform.KeyHome), key(platform.KeyArrowDown))
	b.settle()
	b.want("g=0.51", "g=0.52", "g=0.53", "g=0.52", "g=0.51", "g=0.61", "g=0.51", "g=1", "g=0")
}

func TestKeyboardEditsDoNotReuseAStaleSnapshotAsTheirBase(t *testing.T) {
	// A delayed device: the view keeps supplying 0.5 while edits are outstanding.
	b := newBench(t, func(b *bench, f *Frame) {
		if out, changed := f.Fader("g", 0.5); changed {
			b.commands = append(b.commands, fmt.Sprint(out))
		}
	})
	b.send(tab(), key(platform.KeyArrowUp), key(platform.KeyArrowUp))
	b.frame()
	b.want("0.51")
	if got := itemOf(t, b.c, "g").value; got != 0.51 {
		t.Fatalf("edited value not rendered over the stale snapshot: %v", got)
	}
	b.settle()
	b.want("0.51", "0.52")
	b.frame() // no edit left: the supplied model shows again, without a command
	b.want("0.51", "0.52")
	if got := itemOf(t, b.c, "g").value; got != 0.5 {
		t.Fatalf("frame without an edit shows %v, not the model", got)
	}
}

func TestExternalUpdatesDuringADragNeverEcho(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	x := 34.0
	b.send(down(x, faderTop))
	b.frame()
	b.want("g=1")
	for _, external := range []float64{0.1, 0.9, 0.3} {
		gain = external // a hardware update racing the gesture
		b.frame()
		if got := itemOf(t, b.c, "g").value; got != 1 {
			t.Fatalf("the gesture lost its displayed value to %v: shows %v", external, got)
		}
	}
	b.want("g=1")
	gain = 0.3
	b.send(up(x, faderTop))
	b.settle()
	b.want("g=1") // the release position already reported
	b.frame()
	if got := itemOf(t, b.c, "g").value; got != 0.3 {
		t.Fatalf("after release shows %v, not the model", got)
	}
	gain = 0.7 // and a later external update is displayed, never reported
	b.frame()
	b.want("g=1")
	if got := itemOf(t, b.c, "g").value; got != 0.7 {
		t.Fatalf("shows %v", got)
	}
}

func TestCaptureAndFocusLossCancelWithoutManufacturingAnEdit(t *testing.T) {
	for _, loss := range []platform.Input{{Kind: platform.CaptureLost}, {Kind: platform.FocusLost}} {
		t.Run(fmt.Sprint(loss.Kind), func(t *testing.T) {
			gain, muted := 0.5, false
			b := newBench(t, func(b *bench, f *Frame) {
				b.fader(f, "g", &gain)
				b.checkbox(f, "m", &muted)
			})
			x := 34.0
			b.send(down(x, faderTop))
			b.frame()
			b.want("g=1")
			gain = 0.25 // the application has moved on
			b.send(motion(x, faderBottom), loss, motion(x, faderTop), up(x, faderTop))
			b.settle()
			b.want("g=1", "g=0") // motion before the loss is an edit; nothing after it
			if b.c.capture != "" {
				t.Fatal("gesture survived")
			}
			// Pressed checkbox: losing the press yields no activation.
			b.commands = nil
			cx, cy := b.center("m")
			b.send(down(cx, cy), loss, up(cx, cy))
			b.settle()
			b.want()
			b.frame()
			if itemOf(t, b.c, "m").pressed {
				t.Fatal("stuck pressed")
			}
		})
	}
}

func TestFocusLossKeepsControlFocusForTheNextActivation(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	b.send(platform.Input{Kind: platform.FocusGained}, tab())
	b.settle()
	if !b.c.active || b.c.focus != "g" {
		t.Fatalf("active=%t focus=%q", b.c.active, b.c.focus)
	}
	b.send(platform.Input{Kind: platform.FocusLost})
	b.settle()
	if b.c.active || b.c.focus != "g" {
		t.Fatalf("active=%t focus=%q", b.c.active, b.c.focus)
	}
}

func TestFocusTraversalFollowsRecordedOrderAndWraps(t *testing.T) {
	var gain float64
	var muted bool
	b := newBench(t, func(b *bench, f *Frame) {
		f.Row(Layout{Gap: 4}, func(f *Frame) {
			f.Scope("a", func(f *Frame) { b.fader(f, "gain", &gain); b.checkbox(f, "mute", &muted) })
			f.Meter("meter", 0.5) // a meter takes no focus
			f.Scope("b", func(f *Frame) { b.fader(f, "gain", &gain) })
		})
		f.Label("nor does a label")
	})
	var seen []string
	step := func(in platform.Input) {
		b.send(in)
		b.settle()
		seen = append(seen, b.c.focus)
	}
	for range 4 {
		step(tab())
	}
	want := []string{"a/gain", "a/mute", "b/gain", "a/gain"}
	if !slices.Equal(seen, want) {
		t.Fatalf("Tab order %q, want %q", seen, want)
	}
	seen = nil
	for range 2 {
		step(shiftTab())
	}
	if want := []string{"b/gain", "a/mute"}; !slices.Equal(seen, want) {
		t.Fatalf("Shift+Tab order %q, want %q", seen, want)
	}
	b.c.focus = ""
	step(shiftTab())
	if b.c.focus != "b/gain" {
		t.Fatalf("Shift+Tab with no focus reached %q", b.c.focus)
	}
	b.frame()
	if !itemOf(t, b.c, "b/gain").focused || itemOf(t, b.c, "a/gain").focused {
		t.Fatal("focus not recorded on its item")
	}
}

func TestTabDuringADragCancelsItAndFocusMoves(t *testing.T) {
	gain, other := 0.5, 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain); b.fader(f, "o", &other) })
	x := 34.0
	b.send(down(x, faderTop))
	b.frame()
	b.want("g=1")
	b.send(tab(), motion(x, faderBottom))
	b.settle()
	b.want("g=1")
	if b.c.capture != "" || b.c.focus != "o" || b.released != 1 {
		t.Fatalf("capture=%q focus=%q released=%d", b.c.capture, b.c.focus, b.released)
	}
}

func TestStableIdentityRoutesInputAcrossReorder(t *testing.T) {
	order := []string{"a", "b"}
	gains := map[string]float64{"a": 0.5, "b": 0.5}
	b := newBench(t, func(b *bench, f *Frame) {
		f.Row(Layout{Gap: 8}, func(f *Frame) {
			for _, id := range order {
				f.Scope(id, func(f *Frame) {
					out, changed := f.Fader("gain", gains[id])
					if changed {
						gains[id] = out
						b.commands = append(b.commands, fmt.Sprintf("%s=%g", id, out))
					}
				})
			}
		})
	})
	first := rectOf(t, b.c, "a/gain")
	x := float64(first.Min.X+first.Max.X) / 2
	// Focus and capture on a, then the model reorders the strips.
	b.send(down(x, faderTop))
	b.frame()
	b.want("a=1")
	order = []string{"b", "a"}
	b.frame()
	if moved := rectOf(t, b.c, "a/gain"); moved.Min.X == first.Min.X {
		t.Fatal("reorder did not move the fader")
	}
	b.send(motion(x, faderBottom), up(x, faderBottom))
	b.settle()
	b.want("a=1", "a=0") // not b, which now sits under the pointer
	if b.c.focus != "a/gain" {
		t.Fatalf("focus %q did not follow identity", b.c.focus)
	}
	b.send(key(platform.KeyArrowUp))
	b.settle()
	b.want("a=1", "a=0", "a=0.01")
	// A fresh press targets whatever is there now.
	b.commands = nil
	b.send(down(x, faderTop), up(x, faderTop))
	b.settle()
	b.want("b=1")
}

func TestEventsRouteToTheLastCompletedLayoutNotALaterIndex(t *testing.T) {
	order := []string{"a", "b"}
	b := newBench(t, func(b *bench, f *Frame) {
		f.Row(Layout{}, func(f *Frame) {
			for _, id := range order {
				muted, changed := f.Checkbox(id, id, false)
				if changed {
					b.commands = append(b.commands, fmt.Sprintf("%s=%t", id, muted))
				}
			}
		})
	})
	x, y := b.center("a")
	// The press was aimed at what the user saw. This frame's view reorders, but
	// input is processed against the layout that was on screen.
	order = []string{"b", "a"}
	b.send(down(x, y), up(x, y))
	b.settle()
	b.want("a=true")
}

func TestRemovalCancelsCaptureClearsFocusAndYieldsNoEdit(t *testing.T) {
	present := true
	gain := 0.5
	other := 0.25
	b := newBench(t, func(b *bench, f *Frame) {
		f.Scope("gone", func(f *Frame) {
			if present {
				b.fader(f, "g", &gain)
			}
		})
		b.fader(f, "other", &other)
	})
	x, y := b.center("gone/g")
	b.send(down(x, y))
	b.frame()
	if b.c.capture != "gone/g" || b.c.focus != "gone/g" {
		t.Fatalf("capture=%q focus=%q", b.c.capture, b.c.focus)
	}
	present = false
	b.frame()
	if b.c.capture != "" || b.c.focus != "" || len(b.c.hits) != 1 {
		t.Fatalf("removal left capture=%q focus=%q hits=%d", b.c.capture, b.c.focus, len(b.c.hits))
	}
	if b.released != 1 {
		t.Fatalf("native capture released %d times", b.released)
	}
	// Remaining events of that gesture target nothing.
	b.send(motion(x, faderTop), up(x, faderTop))
	b.settle()
	b.want()
	// The next Tab starts at the first available control.
	b.send(tab())
	b.settle()
	if b.c.focus != "other" {
		t.Fatalf("Tab reached %q", b.c.focus)
	}
	// Reintroducing the key starts a fresh interaction.
	present = true
	gain = 0.75
	b.frame()
	if it := itemOf(t, b.c, "gone/g"); it.pressed || it.focused || it.value != 0.75 {
		t.Fatalf("reinserted fader inherited state: %+v", it)
	}
	b.want()
}

func TestAnEditWhoseTargetIsRemovedThatFrameIsNeverDelivered(t *testing.T) {
	present := true
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) {
		if present {
			b.fader(f, "g", &gain)
		}
	})
	x, _ := b.center("g")
	present = false // the frame that processes this press no longer records the fader
	b.send(down(x, faderTop), motion(x, faderBottom))
	b.settle()
	present = true
	gain = 0.3
	b.frame()
	b.frame()
	b.want()
	if it := itemOf(t, b.c, "g"); it.value != 0.3 || it.pressed {
		t.Fatalf("removed target came back with old state: %+v", it)
	}
}

func TestChangingAKeysKindStartsFresh(t *testing.T) {
	fader := true
	gain := 0.5
	muted := false
	b := newBench(t, func(b *bench, f *Frame) {
		if fader {
			b.fader(f, "k", &gain)
		} else {
			b.checkbox(f, "k", &muted)
		}
	})
	x, _ := b.center("k")
	b.send(down(x, faderTop))
	b.frame()
	fader = false
	b.frame()
	if b.c.capture != "" || b.c.focus != "" || b.released != 1 {
		t.Fatalf("capture=%q focus=%q released=%d", b.c.capture, b.c.focus, b.released)
	}
}

func TestNewControlIsInteractiveOnlyAfterItsFirstCompletedLayout(t *testing.T) {
	shown := false
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) {
		if shown {
			b.fader(f, "g", &gain)
		}
	})
	shown = true
	b.send(down(34, faderTop), up(34, faderTop), tab())
	b.settle()
	b.want()
	if b.c.focus != "" {
		t.Fatalf("focus reached %q before a completed layout", b.c.focus)
	}
	b.send(down(34, faderTop), up(34, faderTop))
	b.settle()
	b.want("g=1")
}

func TestInputLostCancelsTheGestureAndReleasesCapture(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	x := 34.0
	b.send(down(x, faderTop))
	b.frame()
	b.want("g=1")
	gain = 0.25
	b.lost = true
	b.frame()
	if b.c.capture != "" || b.released != 1 {
		t.Fatalf("overflow left capture=%q released=%d", b.c.capture, b.released)
	}
	if got := itemOf(t, b.c, "g").value; got != 0.25 {
		t.Fatalf("shows %v, not the model", got)
	}
	// The discarded gesture's remaining events do nothing: no stuck button.
	b.send(motion(x, faderBottom), up(x, faderBottom))
	b.settle()
	b.want("g=1")
}

func TestANewPressKeepsTheCaptureAnEarlierCancelWouldRelease(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	x := 34.0
	b.send(down(x, faderTop))
	b.frame()
	b.send(platform.Input{Kind: platform.CaptureLost}, down(x, faderTop))
	b.settle()
	if b.c.capture != "g" || b.released != 0 {
		t.Fatalf("capture=%q released=%d", b.c.capture, b.released)
	}
}

func TestAnEmptyPressAlsoKeepsItsCapture(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	b.send(down(34, faderTop))
	b.frame()
	b.send(platform.Input{Kind: platform.CaptureLost}, down(300, 300)) // a press on empty space
	b.settle()
	if b.released != 0 {
		t.Fatalf("released %d times, breaking the capture of the newer press", b.released)
	}
}

func TestKeysDoNotEditDuringADrag(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	b.send(down(34, faderTop))
	b.frame()
	b.want("g=1")
	b.send(key(platform.KeyArrowDown), key(platform.KeyHome))
	b.settle()
	b.want("g=1")
}

func TestOnlyTheLeftButtonStartsOrEndsAGesture(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	right := func(kind platform.InputKind, y float64) platform.Input {
		return platform.Input{Kind: kind, Button: platform.ButtonRight, X: 34, Y: y}
	}
	b.send(right(platform.PointerDown, faderTop), motion(34, faderBottom), right(platform.PointerUp, faderBottom))
	b.settle()
	b.want()
	if b.c.capture != "" || b.c.focus != "" {
		t.Fatalf("right button took capture=%q focus=%q", b.c.capture, b.c.focus)
	}
	b.send(down(34, faderTop))
	b.frame()
	b.send(right(platform.PointerUp, faderBottom))
	b.settle()
	if b.c.capture != "g" {
		t.Fatal("a right-button release ended a left-button drag")
	}
	b.want("g=1")
}

func TestKeyboardStepsDoNotAccumulateBinaryError(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	b.send(tab())
	for range 30 {
		b.send(key(platform.KeyArrowUp))
	}
	b.settle()
	if last := b.commands[len(b.commands)-1]; last != "g=0.8" {
		t.Fatalf("30 steps from 0.5 reached %s", last)
	}
}

func TestPressingACheckboxDoesNotFreezeItsModelValue(t *testing.T) {
	muted := false
	b := newBench(t, func(b *bench, f *Frame) { b.checkbox(f, "m", &muted) })
	x, y := b.center("m")
	b.send(down(x, y))
	b.frame()
	muted = true // only a fader's drag owns its displayed value
	b.frame()
	if itemOf(t, b.c, "m").value != 1 {
		t.Fatal("a press hid an external update")
	}
	b.want()
}

func TestLogicalInputMapsThroughTheLayoutsScale(t *testing.T) {
	for _, scale := range []float64{1, 1.5, 2} {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			gain := 0.5
			b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
			b.width, b.height, b.scale = int(400*scale), int(400*scale), scale
			b.frame()
			if r := rectOf(t, b.c, "g"); r.Min != image.Pt(int(16*scale), int(16*scale)) {
				t.Fatalf("rect %v", r)
			}
			b.send(down(34, (faderTop+faderBottom)/2))
			b.frame()
			b.want()
			if h := b.c.hitAt(15, 100); h != nil {
				t.Fatalf("logical 15 is left of the fader at scale %v: %+v", scale, h)
			}
			if h := b.c.hitAt(17, 100); h == nil {
				t.Fatalf("logical 17 is on the fader at scale %v", scale)
			}
		})
	}
}

func TestTheLayoutsOwnScaleGovernsUntilTheNextOneCompletes(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	// The surface changes scale; this frame's input targets the old layout.
	b.width, b.height, b.scale = 800, 800, 2
	b.send(down(34, faderTop))
	b.frame()
	b.want("g=1")
}

func TestFrameEvaluatesTheViewOncePerFrameWithInput(t *testing.T) {
	gain, evaluations := 0.5, 0
	b := newBench(t, func(b *bench, f *Frame) { evaluations++; b.fader(f, "g", &gain) })
	evaluations = 0
	b.send(tab(), key(platform.KeyArrowUp))
	b.frame() // both fit: the view can report the one edit
	if evaluations != 1 {
		t.Fatalf("%d evaluations", evaluations)
	}
	b.want("g=0.51")
}

func TestControlsDrawVisibleStateWithinTheirRectangles(t *testing.T) {
	gain, muted := 1.0, true
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain); b.checkbox(f, "m", &muted) })
	img := b.c.canvas.Image(1)
	for y := range img.Height {
		for x := range img.Width {
			p := img.Pix[y*img.Stride+x*4:]
			if p[0] == background.B && p[1] == background.G && p[2] == background.R {
				continue
			}
			if at := image.Pt(x, y); !at.In(rectOf(t, b.c, "g")) && !at.In(rectOf(t, b.c, "m")) {
				t.Fatalf("ink outside controls at %v", at)
			}
		}
	}
	thumb := rectOf(t, b.c, "g").Min.Add(image.Pt(1, 1))
	if p := img.Pix[thumb.Y*img.Stride+thumb.X*4:]; p[0] != foreground.B {
		t.Fatalf("value 1 thumb not at the top: %v", p[:3])
	}
	focusBefore := append([]byte(nil), img.Pix...)
	b.send(platform.Input{Kind: platform.FocusGained}, tab())
	b.settle()
	img = b.c.canvas.Image(1)
	if p := img.Pix[thumb.Y*img.Stride+thumb.X*4:]; p[0] != accent.B {
		t.Fatalf("focus ring not drawn: %v", p[:3])
	}
	if string(focusBefore) == string(img.Pix) {
		t.Fatal("focus changed no pixels")
	}
}

func TestReleaseDropsInteractionState(t *testing.T) {
	gain := 0.5
	b := newBench(t, func(b *bench, f *Frame) { b.fader(f, "g", &gain) })
	b.c.Release()
	if b.c.controls != nil || b.c.hits != nil {
		t.Fatal("release kept interaction state")
	}
}
