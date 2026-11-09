// Package platform defines the private, policy-free native backend boundary.
// Implementations must not depend on appcore, the public API, or content packages.
package platform

import (
	"errors"
	"fmt"
	"math"
)

type ID uint64

// ErrUnsupportedContent reports that a backend cannot host framework-drawn
// content yet. Backends return it before starting any native work, so no
// native object exists and the condition is not an application failure.
var ErrUnsupportedContent = errors.New("dfx2: UI content is not supported by this platform backend yet")

// MaxImageBytes bounds one rendered or retained window image. Exceeding it is a
// reported rendering failure rather than an unbounded allocation.
const MaxImageBytes = 256 << 20

// Counters supports internal native auditions without exposing native handles
// or instrumentation on the public App/Window API.
type Counters struct {
	Wakes, Dispatches uint64
	Windows, Retired  int
	DispatchTargets   int
	// Presents counts images copied into retained native storage; Paints counts
	// native exposure callbacks that painted a retained image. PaintFailures
	// counts failed native paint steps; each one also fails the application.
	Presents, Paints, PaintFailures uint64
	// ModalActive reports an established native size/move loop. ModalLoops
	// counts those entered, ModalTurns the dispatch turns delivered from inside
	// them, ModalTimers the one-shot timers armed for that delivery, and
	// ModalRefusals the timer deliveries a guard refused. Zero outside Win32.
	ModalActive                                        bool
	ModalLoops, ModalTurns, ModalTimers, ModalRefusals uint64
	Problem                                            string
}

type WindowOptions struct {
	Title         string
	Width, Height int
	// Content requests a framework drawing surface inside the window. A backend
	// that cannot provide one returns ErrUnsupportedContent before native work.
	Content bool
}

type Kind uint8

const (
	Wake Kind = iota
	CloseRequested
	Destroyed
	Completed
	// SurfaceChanged carries a content window's copied size, scale and
	// visibility. Backends deliver it only when one of those values changes.
	SurfaceChanged
	// InputReceived carries one copied native input edge or motion for a
	// content window.
	InputReceived
)

// Surface is copied from native configure, scale and visibility notifications.
type Surface struct {
	Width, Height int     // physical pixels
	Scale         float64 // physical pixels per logical unit
	// Visible means mapped and not minimized, as far as the native toolkit
	// reports. Compositor occlusion of a mapped window is not detected.
	Visible bool
}

// InputKind names a normalized input event. Pointer and key values are edges,
// delivered in native order. Motion is delivered only while the left button is
// held, because no widget uses hover.
type InputKind uint8

const (
	PointerMove InputKind = iota + 1
	PointerDown
	PointerUp
	KeyDown
	KeyUp
	FocusGained
	FocusLost
	// CaptureLost reports that native code took the pointer away during a press.
	// A backend's own release after the button-up edge is not a loss.
	CaptureLost
)

// Button is a pointer button.
type Button uint8

const (
	ButtonLeft Button = iota + 1
	ButtonMiddle
	ButtonRight
)

// Key is a key a widget can use. Backends drop every other key, so typing does
// not request frames and the native toolkit keeps its own shortcuts.
type Key uint8

const (
	KeyTab Key = iota + 1
	KeySpace
	KeyArrowUp
	KeyArrowDown
	KeyArrowLeft
	KeyArrowRight
	KeyPageUp
	KeyPageDown
	KeyHome
	KeyEnd
)

// Modifiers holds the modifier keys down when an edge happened.
type Modifiers uint8

const (
	ModShift Modifiers = 1 << iota
	ModControl
	ModAlt
)

// Input is one copied native event. X and Y are logical client coordinates:
// physical pixels over the surface scale. They may lie outside the window while
// the pointer is captured.
type Input struct {
	Kind      InputKind
	X, Y      float64
	Button    Button
	Key       Key
	Modifiers Modifiers
}

// Event contains numeric routing IDs and Go-owned values, never borrowed native
// memory. Completion IDs settle operations admitted by the core before starting
// native work. Duplicate or stale notifications must be harmless.
type Event struct {
	Kind       Kind
	Window     ID
	Completion ID
	Err        error
	Surface    Surface
	Input      Input
}

// Image is a complete opaque frame: premultiplied BGRA, rows top-down, Stride
// bytes apart. Scale is physical pixels per logical unit.
type Image struct {
	Width, Height, Stride int
	Scale                 float64
	Pix                   []byte
}

// ImageSize returns the tight stride and byte size for a width×height BGRA
// image, with checked arithmetic and the MaxImageBytes limit.
func ImageSize(width, height int) (stride, size int, err error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("image dimensions must be positive (got %dx%d)", width, height)
	}
	if width > math.MaxInt32/4 || height > math.MaxInt32 {
		return 0, 0, fmt.Errorf("image dimensions %dx%d overflow", width, height)
	}
	stride = width * 4
	if height > MaxImageBytes/stride {
		return 0, 0, fmt.Errorf("image %dx%d exceeds the %d MiB limit", width, height, MaxImageBytes>>20)
	}
	return stride, stride * height, nil
}

// Validate checks dimensions, stride and pixel length before any copy.
func (i Image) Validate() error {
	tight, _, err := ImageSize(i.Width, i.Height)
	if err != nil {
		return err
	}
	if i.Stride < tight || i.Stride > math.MaxInt32 {
		return fmt.Errorf("image stride %d invalid for width %d", i.Stride, i.Width)
	}
	if i.Height > MaxImageBytes/i.Stride {
		return fmt.Errorf("image stride %d × height %d exceeds the %d MiB limit", i.Stride, i.Height, MaxImageBytes>>20)
	}
	if len(i.Pix) < i.Stride*(i.Height-1)+tight {
		return fmt.Errorf("image pixels too short: %d bytes for %dx%d stride %d", len(i.Pix), i.Width, i.Height, i.Stride)
	}
	if !(i.Scale > 0) || math.IsInf(i.Scale, 0) {
		return fmt.Errorf("image scale %v invalid", i.Scale)
	}
	return nil
}

// Backend owns one dispatch target, independent of its content windows.
// Except Wake, all methods and event delivery run on the owner thread.
// Native callbacks use permanent trampolines and numeric routing IDs.
type Backend interface {
	// Open establishes the owner thread and dispatch target. On error it must
	// release partial initialization itself. Libraries load here, not in init.
	Open(func(Event)) error
	IsOwner() bool
	// Create may deliver events reentrantly. On error no native window remains.
	// On success exactly one Destroyed event is owed, possibly during Create.
	Create(ID, WindowOptions) error
	// Destroy disconnects interactive callbacks and starts teardown. It may
	// deliver events reentrantly. Even on error, Destroyed is still owed;
	// an error is not permission to forget a live native object.
	Destroy(ID) error
	SetTitle(ID, string) error
	// Present validates a content window's complete image and copies it
	// synchronously into backend-owned retained storage, then schedules native
	// exposure. Native code never retains the Pix slice. Replacement storage is
	// installed only after it was allocated successfully; retained storage is
	// freed after the window's final native use. It never delivers events.
	Present(ID, Image) error
	// ReleaseCapture ends the window's native pointer capture, if it holds one,
	// because the gesture ended without a button-up edge. It reports no
	// CaptureLost for this release. A toolkit whose implicit grab cannot be
	// revoked may do nothing: the core ignores pointer events that have no
	// gesture, and the grab ends at the button-up edge.
	ReleaseCapture(ID) error
	// Wake is nonblocking, safe from any goroutine and never delivers inline.
	// It queues a Wake event or returns an error without queuing one. A Wake
	// event normally arrives from Pump. A backend whose native toolkit runs its
	// own loop may also deliver it from there, but never while a native creation
	// or destruction it started is unfinished. The core refuses a Wake that
	// arrives during another turn and re-requests any remaining work afterward.
	Wake() error
	// Pump processes one native dispatch turn, blocking when idle. On failure
	// the target and routing remain valid: the core cancels and keeps pumping.
	Pump() error
	// Release stops queued sources before dropping routing. Called only after
	// all owners, completions and background wake retries have drained.
	Release() error
}
