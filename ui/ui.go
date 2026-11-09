// Package ui draws immediate-mode content in dfx2 windows.
//
// Open creates a window whose content is the application's draw function.
// The framework calls draw once per frame on the UI thread; a frame arises
// from input, Invalidate, resize, scale or visibility changes, or active
// animation. Exposing an unchanged window repaints its retained image without
// calling draw. Ordinary Go functions compose the view: there is no component
// base type and no framework-owned application state.
//
// A view nests rows, columns and scopes around labels, meters, faders and
// checkboxes, rendered on the CPU with pure-Go text. The application owns its
// values. Controls keep only interaction state, under the path of their keys, and
// report user edits, never a value the application supplied. Linux/amd64 presents
// through GTK 3 and Windows/amd64 through GDI.
package ui

import (
	"context"
	"errors"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// Window is a dfx2 window with UI content. It forwards the ordinary window
// operations, with their thread rules, and adds Invalidate. It exposes no
// native resources.
type Window struct {
	w *dfx2.Window
}

// Open creates a content window on the UI thread. Creation follows
// App.NewWindow's rules: failures are returned and latch application failure.
// A backend without content support returns an error matching
// errors.Is(err, ErrUnsupportedContent) before any native work, and nothing
// is latched. The draw function runs only on the UI thread, once per frame.
func Open(app *dfx2.App, options dfx2.WindowOptions, draw func(*Frame)) (*Window, error) {
	if app == nil {
		return nil, errors.New("dfx2 ui.Open: nil app")
	}
	if draw == nil {
		return nil, errors.New("dfx2 ui.Open: nil draw function")
	}
	w, err := appcore.OpenContent(app, options, newContent(draw))
	if w == nil {
		return nil, err
	}
	return &Window{w: w}, err
}

// ErrUnsupportedContent reports a platform backend that cannot present UI
// content yet.
var ErrUnsupportedContent = platform.ErrUnsupportedContent

// Invalidate requests a frame. It is safe from any goroutine, never waits,
// coalesces with other pending requests and does not consume a Post slot.
// An invalidation that races with a frame in progress causes a later frame.
// It returns dfx2.ErrClosed once the window is closing.
func (w *Window) Invalidate() error { return appcore.Invalidate(w.w) }

// Close seals admission and requests native cleanup without waiting; any goroutine.
func (w *Window) Close() { w.w.Close() }

// Context is canceled when the window starts closing; any goroutine.
func (w *Window) Context() context.Context { return w.w.Context() }

// Done closes after native teardown, content release and owned tasks have
// drained. Never wait for it on the UI thread; any goroutine.
func (w *Window) Done() <-chan struct{} { return w.w.Done() }

// SetTitle is a UI-thread operation.
func (w *Window) SetTitle(title string) error { return w.w.SetTitle(title) }

// Post queues f for the UI thread without waiting; any goroutine.
func (w *Window) Post(f func()) error { return w.w.Post(f) }

// Go registers a task owned by this window; UI thread.
func (w *Window) Go(name string, work func(context.Context) error) error { return w.w.Go(name, work) }
