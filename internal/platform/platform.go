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
	// native exposure callbacks that painted a retained image.
	Presents, Paints uint64
	Problem          string
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
)

// Surface is copied from native configure, scale and visibility notifications.
type Surface struct {
	Width, Height int     // physical pixels
	Scale         float64 // physical pixels per logical unit
	// Visible means mapped and not minimized, as far as the native toolkit
	// reports. Compositor occlusion of a mapped window is not detected.
	Visible bool
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
	// Wake is nonblocking, safe from any goroutine and never delivers inline.
	// It queues a Wake event or returns an error without queuing one.
	Wake() error
	// Pump processes one native dispatch turn, blocking when idle. On failure
	// the target and routing remain valid: the core cancels and keeps pumping.
	Pump() error
	// Release stops queued sources before dropping routing. Called only after
	// all owners, completions and background wake retries have drained.
	Release() error
}
