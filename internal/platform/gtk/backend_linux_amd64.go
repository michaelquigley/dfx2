package gtk

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/michaelquigley/dfx2/internal/platform"
)

type route struct {
	backend *Backend
	window  platform.ID
}

var routes sync.Map // numeric token -> route; never a native-retained Go pointer
var nextToken atomic.Uint64
var callbackOnce sync.Once
var callbacks struct {
	idle, destroyed, close, scale, draw, surface, mapping, state uintptr
	input, focusIn, focusOut, grabBroken, grabNotify             uintptr
}

// GLib G_PRIORITY_DEFAULT_IDLE. GTK resize/redraw run at 110/120; a higher
// priority here can starve them when public callbacks keep rearming the source.
const priorityDefaultIdle int32 = 200

const (
	cairoFormatARGB32       int32  = 0
	gdkWindowStateWithdrawn uint32 = 1 << 0
	gdkWindowStateIconified uint32 = 1 << 1

	// Only left-button motion: no widget uses hover, so idle pointer movement
	// must not request frames.
	gdkInputMask = 1<<5 | 1<<8 | 1<<9 | 1<<10 | 1<<11 | 1<<14 // BUTTON1_MOTION, BUTTON_PRESS/RELEASE, KEY_PRESS/RELEASE, FOCUS_CHANGE

	gdkMotionNotify  int32 = 3
	gdkButtonPress   int32 = 4 // GDK sends this for every press; 2BUTTON/3BUTTON_PRESS are extras
	gdkButtonRelease int32 = 7
	gdkKeyPress      int32 = 8
	gdkKeyRelease    int32 = 9

	gdkShiftMask   uint32 = 1 << 0
	gdkControlMask uint32 = 1 << 2
	gdkMod1Mask    uint32 = 1 << 3
)

func installCallbacks() {
	callbackOnce.Do(func() {
		callbacks.idle = purego.NewCallback(func(token uintptr) int32 {
			if r, ok := routes.Load(token); ok {
				r.(route).backend.dispatch()
			}
			return 0 // G_SOURCE_REMOVE
		})
		callbacks.destroyed = purego.NewCallback(func(widget, token uintptr) {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.destroyed(v.window, widget)
			}
		})
		callbacks.close = purego.NewCallback(func(widget, event, token uintptr) int32 {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.closeRequested(v.window)
			}
			return 1 // core initiates native teardown, never GTK's default close
		})
		callbacks.scale = purego.NewCallback(func(widget, pspec, token uintptr) {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.scaleChanged(v.window, widget)
			}
		})
		// Content surfaces. Draw paints only the retained image; the others
		// copy primitive size/scale/visibility into a core event.
		callbacks.draw = purego.NewCallback(func(widget, cr, token uintptr) int32 {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				return v.backend.paint(v.window, cr)
			}
			return 0
		})
		callbacks.surface = purego.NewCallback(func(widget, argument, token uintptr) { // size-allocate, notify::scale-factor
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.surfaceChanged(v.window)
			}
		})
		callbacks.mapping = purego.NewCallback(func(widget, token uintptr) { // map, unmap
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.surfaceChanged(v.window)
			}
		})
		callbacks.state = purego.NewCallback(func(widget, event, token uintptr) int32 { // window-state-event
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.surfaceChanged(v.window)
			}
			return 0 // propagate
		})
		// Input handlers copy primitive values into a core event and never run
		// the view.
		callbacks.input = purego.NewCallback(func(widget, event, token uintptr) int32 {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				return v.backend.input(v.window, event)
			}
			return 0
		})
		callbacks.focusIn = purego.NewCallback(func(widget, event, token uintptr) int32 {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.inputEdge(v.window, platform.FocusGained)
			}
			return 0
		})
		callbacks.focusOut = purego.NewCallback(func(widget, event, token uintptr) int32 {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.inputEdge(v.window, platform.FocusLost)
			}
			return 0
		})
		callbacks.grabBroken = purego.NewCallback(func(widget, event, token uintptr) int32 {
			if r, ok := routes.Load(token); ok {
				v := r.(route)
				v.backend.inputEdge(v.window, platform.CaptureLost)
			}
			return 0
		})
		callbacks.grabNotify = purego.NewCallback(func(widget uintptr, wasGrabbed int32, token uintptr) { // shadowed by another GTK grab
			if r, ok := routes.Load(token); ok && wasGrabbed == 0 {
				v := r.(route)
				v.backend.inputEdge(v.window, platform.CaptureLost)
			}
		})
	})
}

// signal is one connected handler and the object that owns it.
type signal struct {
	object uintptr
	id     uint64
}

type window struct {
	widget, token                           uintptr
	closeSignal, scaleSignal, destroySignal uint64
	width, height                           int // logical dimensions supplied by the application
	scale                                   int32
	closing                                 bool
	destroyed                               bool

	// Content windows: a drawing area referenced independently of its
	// container, its interactive signals, and the retained Cairo-owned image.
	area                        uintptr
	contentSignals              []signal
	surface                     uintptr
	surfaceWidth, surfaceHeight int
	reported                    platform.Surface
}

// Backend keeps its source alive independently of all content windows. The
// default GLib context is acquired for this lifetime by the startup OS thread.
type Backend struct {
	mu                sync.Mutex
	sink              func(platform.Event)
	owner             int
	context, token    uintptr
	windows           map[platform.ID]*window
	retired           []platform.ID // backend refs released after native callbacks return
	source            uint32
	closed            bool
	closeNext         bool // internal native audition hook, UI-thread only
	wakes, dispatches uint64
	presents, paints  uint64
	paintFailures     uint64
	paintErr          error // first callback failure awaiting delivery through Pump
	deferredWake      bool  // native idle fired while a paint error awaited delivery
}

func (b *Backend) Open(sink func(platform.Event)) error {
	if syscall.Gettid() != syscall.Getpid() {
		return errors.New("GTK Run must be called from main on the startup OS thread")
	}
	if err := load(); err != nil {
		return err
	}
	if n.initCheck(0, 0) == 0 {
		return errors.New("GTK 3 could not open the display; check DISPLAY/WAYLAND_DISPLAY and the desktop session")
	}
	context := n.defaultContext()
	if n.acquire(context) == 0 {
		return errors.New("GTK default main context is owned by another thread")
	}
	installCallbacks()
	b.owner = syscall.Gettid()
	b.context, b.sink = context, sink
	b.windows = make(map[platform.ID]*window)
	b.token = uintptr(nextToken.Add(1))
	routes.Store(b.token, route{backend: b})
	return nil
}

func (b *Backend) IsOwner() bool { return syscall.Gettid() == b.owner }

func (b *Backend) Create(id platform.ID, options platform.WindowOptions) error {
	if options.Content == platform.BrowserContent {
		return platform.ErrUnsupportedContent // browser hosting arrives with its own stage
	}
	if options.Width <= 0 || options.Height <= 0 || options.Width > math.MaxInt32 || options.Height > math.MaxInt32 {
		return errors.New("GTK client dimensions must fit positive gint values")
	}
	w := &window{token: uintptr(nextToken.Add(1)), width: options.Width, height: options.Height}
	b.mu.Lock()
	b.windows[id] = w
	b.mu.Unlock()
	routes.Store(w.token, route{backend: b, window: id})
	// Publish the route before any potentially reentrant native operation.
	widget := n.windowNew(0) // GTK_WINDOW_TOPLEVEL
	if widget == 0 {
		routes.Delete(w.token)
		b.mu.Lock()
		delete(b.windows, id)
		b.mu.Unlock()
		return errors.New("gtk_window_new returned nil")
	}
	n.ref(widget) // independent of GTK's own top-level ownership
	b.mu.Lock()
	w.widget = widget
	b.mu.Unlock()
	w.destroySignal = n.connect(widget, "destroy", callbacks.destroyed, w.token, 0, 0)
	w.closeSignal = n.connect(widget, "delete-event", callbacks.close, w.token, 0, 0)
	w.scaleSignal = n.connect(widget, "notify::scale-factor", callbacks.scale, w.token, 0, 0)
	if w.destroySignal == 0 || w.closeSignal == 0 || w.scaleSignal == 0 {
		b.abandon(id, w)
		return errors.New("GTK could not connect window lifetime signals")
	}
	if options.Content == platform.DrawnContent {
		if err := b.attachContent(w); err != nil {
			b.abandon(id, w)
			return err
		}
	}
	n.setTitle(widget, options.Title)
	n.setDefaultSize(widget, int32(options.Width), int32(options.Height))
	b.scaleChanged(id, widget)
	if b.closeNext {
		b.closeNext = false
		// Deliver the same close notification as delete-event while the
		// native widget exists and the core owner is still starting.
		b.closeRequested(id)
	}
	b.mu.Lock()
	show := !w.closing
	b.mu.Unlock()
	if show {
		n.show(widget)
	}
	return nil
}

// attachContent adds the drawing area before the window is shown. Routing is
// already installed; signal handlers only copy state or paint retained pixels.
func (b *Backend) attachContent(w *window) error {
	area := n.drawingAreaNew()
	if area == 0 {
		return errors.New("gtk_drawing_area_new returned nil")
	}
	n.ref(area) // released with the window's final reference, after destroy
	b.mu.Lock()
	w.area = area
	b.mu.Unlock()
	n.containerAdd(w.widget, area)
	// One focusable area owns all of the window's keys and its implicit pointer
	// grab, which keeps a drag routed here outside the window.
	n.addEvents(area, gdkInputMask)
	n.setCanFocus(area, 1)
	n.grabFocus(area)
	for _, c := range []struct {
		object   uintptr
		name     string
		callback uintptr
	}{
		{area, "draw", callbacks.draw},
		{area, "size-allocate", callbacks.surface},
		{area, "notify::scale-factor", callbacks.surface},
		{area, "map", callbacks.mapping},
		{area, "unmap", callbacks.mapping},
		{w.widget, "window-state-event", callbacks.state},
		{area, "button-press-event", callbacks.input},
		{area, "button-release-event", callbacks.input},
		{area, "motion-notify-event", callbacks.input},
		{area, "key-press-event", callbacks.input},
		{area, "key-release-event", callbacks.input},
		{area, "focus-in-event", callbacks.focusIn},
		{area, "focus-out-event", callbacks.focusOut},
		{area, "grab-broken-event", callbacks.grabBroken},
		{area, "grab-notify", callbacks.grabNotify},
	} {
		id := n.connect(c.object, c.name, c.callback, w.token, 0, 0)
		if id == 0 {
			return fmt.Errorf("GTK could not connect content signal %s", c.name)
		}
		w.contentSignals = append(w.contentSignals, signal{c.object, id})
	}
	return nil
}

// abandon releases a window whose creation failed before it was shown. Every
// handler, including destroy, is disconnected first: a failed Create owes no
// Destroyed event and leaves no native object.
func (b *Backend) abandon(id platform.ID, w *window) {
	for _, s := range []uint64{w.destroySignal, w.closeSignal, w.scaleSignal} {
		if s != 0 {
			n.disconnect(w.widget, s)
		}
	}
	w.destroySignal, w.closeSignal, w.scaleSignal = 0, 0, 0
	for _, s := range w.contentSignals {
		n.disconnect(s.object, s.id)
	}
	w.contentSignals = nil
	n.destroy(w.widget)
	b.releaseNative(w)
	routes.Delete(w.token)
	b.mu.Lock()
	delete(b.windows, id)
	b.mu.Unlock()
}

// releaseNative drops the backend's final references after the last native
// use: the toplevel, the drawing area, and the retained Cairo image.
func (b *Backend) releaseNative(w *window) {
	n.unref(w.widget)
	if w.area != 0 {
		n.unref(w.area)
	}
	if w.surface != 0 {
		n.surfaceDestroy(w.surface)
		w.surface = 0
	}
}

func (b *Backend) SetTitle(id platform.ID, title string) error {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil {
		return errors.New("GTK title target no longer exists")
	}
	n.setTitle(w.widget, title)
	return nil
}

func (b *Backend) closeRequested(id platform.ID) {
	b.mu.Lock()
	w := b.windows[id]
	if w == nil || w.closing {
		b.mu.Unlock()
		return
	}
	w.closing = true
	b.mu.Unlock()
	b.sink(platform.Event{Kind: platform.CloseRequested, Window: id})
}

func (b *Backend) scaleChanged(id platform.ID, widget uintptr) {
	scale := n.getScale(widget)
	b.mu.Lock()
	if w := b.windows[id]; w != nil {
		w.scale = scale
	}
	b.mu.Unlock()
}

// surfaceChanged copies the drawing area's allocation, integer scale and
// visibility, delivering an event only when one of them changed. Minimizing
// is observed through the toplevel's GdkWindowState; whether a compositor
// reports it is toolkit/session dependent.
func (b *Backend) surfaceChanged(id platform.ID) {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.area == 0 || w.destroyed {
		return
	}
	scale := int(n.getScale(w.area))
	width, height := int(n.allocatedWidth(w.area)), int(n.allocatedHeight(w.area))
	visible := n.mapped(w.area) != 0
	if gdk := n.widgetWindow(w.widget); gdk != 0 && n.windowState(gdk)&(gdkWindowStateWithdrawn|gdkWindowStateIconified) != 0 {
		visible = false
	}
	if scale < 1 || width < 0 || height < 0 {
		scale, width, height = 1, 0, 0
	}
	s := platform.Surface{Width: width * scale, Height: height * scale, Scale: float64(scale), Visible: visible}
	if s == w.reported {
		return
	}
	w.reported = s
	b.sink(platform.Event{Kind: platform.SurfaceChanged, Window: id, Surface: s})
}

// gdkKeys maps the key values widgets use, including their keypad forms.
// ISO_Left_Tab is what most layouts send for Shift+Tab.
var gdkKeys = map[uint32]platform.Key{
	0xff09: platform.KeyTab, 0xfe20: platform.KeyTab, 0x0020: platform.KeySpace, 0xff80: platform.KeySpace,
	0xff52: platform.KeyArrowUp, 0xff97: platform.KeyArrowUp, 0xff54: platform.KeyArrowDown, 0xff99: platform.KeyArrowDown,
	0xff51: platform.KeyArrowLeft, 0xff96: platform.KeyArrowLeft, 0xff53: platform.KeyArrowRight, 0xff98: platform.KeyArrowRight,
	0xff55: platform.KeyPageUp, 0xff9a: platform.KeyPageUp, 0xff56: platform.KeyPageDown, 0xff9b: platform.KeyPageDown,
	0xff50: platform.KeyHome, 0xff95: platform.KeyHome, 0xff57: platform.KeyEnd, 0xff9c: platform.KeyEnd,
}

// input copies a pointer or key event into the core and returns whether GTK
// should stop handling it. Keys widgets use stop here, so Tab reaches the
// framework instead of GTK's focus chain; every other key keeps its native
// meaning and requests no frame.
func (b *Backend) input(id platform.ID, event uintptr) int32 {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.closing || w.destroyed {
		return 0
	}
	var state uint32
	n.eventState(event, &state)
	in := platform.Input{Modifiers: gdkModifiers(state)}
	switch kind := n.eventType(event); kind {
	case gdkMotionNotify:
		in.Kind = platform.PointerMove
		n.eventCoords(event, &in.X, &in.Y)
	case gdkButtonPress, gdkButtonRelease:
		var button uint32
		n.eventButton(event, &button)
		if in.Button = gdkButton(button); in.Button == 0 {
			return 0
		}
		in.Kind = platform.PointerUp
		if kind == gdkButtonPress {
			in.Kind = platform.PointerDown
			n.grabFocus(w.area)
		}
		n.eventCoords(event, &in.X, &in.Y)
	case gdkKeyPress, gdkKeyRelease:
		var keyval uint32
		n.eventKeyval(event, &keyval)
		key, ok := gdkKeys[keyval]
		if !ok {
			return 0
		}
		in.Key, in.Kind = key, platform.KeyUp
		if kind == gdkKeyPress {
			in.Kind = platform.KeyDown
		}
	default:
		return 0 // a double-click's extra press; its plain press already came
	}
	b.sink(platform.Event{Kind: platform.InputReceived, Window: id, Input: in})
	return 1
}

func gdkModifiers(state uint32) platform.Modifiers {
	var m platform.Modifiers
	if state&gdkShiftMask != 0 {
		m |= platform.ModShift
	}
	if state&gdkControlMask != 0 {
		m |= platform.ModControl
	}
	if state&gdkMod1Mask != 0 {
		m |= platform.ModAlt
	}
	return m
}

func gdkButton(button uint32) platform.Button {
	switch button {
	case 1:
		return platform.ButtonLeft
	case 2:
		return platform.ButtonMiddle
	case 3:
		return platform.ButtonRight
	}
	return 0
}

// inputEdge reports a focus change or a lost grab.
func (b *Backend) inputEdge(id platform.ID, kind platform.InputKind) {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.closing || w.destroyed {
		return
	}
	b.sink(platform.Event{Kind: platform.InputReceived, Window: id, Input: platform.Input{Kind: kind}})
}

// ReleaseCapture does nothing: GTK's implicit grab has no revocation call, and
// it ends at the button-up edge. The core ignores pointer events without a gesture.
func (b *Backend) ReleaseCapture(platform.ID) error { return nil }

// BrowserParent refuses: this backend creates no BrowserContent shells yet.
func (b *Backend) BrowserParent(platform.ID) (platform.BrowserParent, error) {
	return platform.BrowserParent{}, platform.ErrUnsupportedContent
}

// paint is the draw handler: it paints the last complete image and never asks
// the core for a frame. Without an image GTK draws the default background.
func (b *Backend) paint(id platform.ID, cr uintptr) int32 {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.surface == 0 {
		return 0
	}
	n.setSourceSurface(cr, w.surface, 0, 0)
	n.paint(cr)
	status := n.contextStatus(cr)
	b.mu.Lock()
	if status != 0 {
		b.paintFailures++
		if b.paintErr == nil {
			b.paintErr = fmt.Errorf("GTK paint: window %d, %dx%d image: cairo status %d", id, w.surfaceWidth, w.surfaceHeight, status)
		}
	} else {
		b.paints++
	}
	b.mu.Unlock()
	return 1
}

// Present copies a complete image into Cairo-owned storage. A size change
// allocates and fills replacement storage before releasing the previous image,
// so a failure keeps the last valid image retained.
func (b *Backend) Present(id platform.ID, image platform.Image) error {
	if err := image.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.area == 0 {
		return errors.New("GTK present target has no content surface")
	}
	if w.destroyed {
		return errors.New("GTK present after native destruction")
	}
	surface, replacement := w.surface, false
	if surface == 0 || w.surfaceWidth != image.Width || w.surfaceHeight != image.Height {
		surface = n.imageSurfaceCreate(cairoFormatARGB32, int32(image.Width), int32(image.Height))
		if status := n.surfaceStatus(surface); surface == 0 || status != 0 {
			if surface != 0 {
				n.surfaceDestroy(surface) // error surfaces are still references
			}
			return fmt.Errorf("cairo_image_surface_create %dx%d: status %d", image.Width, image.Height, status)
		}
		replacement = true
	}
	if err := copyImage(surface, image); err != nil {
		if replacement {
			n.surfaceDestroy(surface)
		}
		return err
	}
	n.surfaceSetScale(surface, image.Scale, image.Scale)
	if replacement {
		if w.surface != 0 {
			n.surfaceDestroy(w.surface)
		}
		w.surface, w.surfaceWidth, w.surfaceHeight = surface, image.Width, image.Height
	}
	n.queueDraw(w.area)
	b.mu.Lock()
	b.presents++
	b.mu.Unlock()
	return nil
}

// copyImage writes Go pixels into Cairo's own buffer synchronously. Cairo never
// sees the Go slice; the destination is valid while the surface is referenced.
func copyImage(surface uintptr, image platform.Image) error {
	n.surfaceFlush(surface)
	data, stride := n.imageSurfaceData(surface), int(n.imageSurfaceStride(surface))
	row := image.Width * 4
	if data == nil || stride < row {
		return fmt.Errorf("cairo image surface data unavailable (stride %d for width %d)", stride, image.Width)
	}
	dst := unsafe.Slice((*byte)(data), stride*image.Height)
	for y := range image.Height {
		copy(dst[y*stride:y*stride+row], image.Pix[y*image.Stride:y*image.Stride+row])
	}
	n.surfaceMarkDirty(surface)
	return nil
}

func (b *Backend) Destroy(id platform.ID) error {
	b.mu.Lock()
	w := b.windows[id]
	if w != nil {
		w.closing = true
	}
	b.mu.Unlock()
	if w == nil || w.destroyed {
		return nil
	}
	// Interactive callbacks are gone before destruction starts. Keep destroy
	// connected until acknowledgment, and keep the route during reentrancy.
	b.disconnectInteractive(w)
	n.destroy(w.widget)
	return nil
}

func (b *Backend) disconnectInteractive(w *window) {
	for _, signal := range []*uint64{&w.closeSignal, &w.scaleSignal} {
		if *signal != 0 {
			n.disconnect(w.widget, *signal)
			*signal = 0
		}
	}
	// Draw and surface handlers stop before teardown; the area reference keeps
	// its handler table valid even while its container is being destroyed.
	for _, s := range w.contentSignals {
		n.disconnect(s.object, s.id)
	}
	w.contentSignals = nil
}

func (b *Backend) destroyed(id platform.ID, widget uintptr) {
	b.mu.Lock()
	w := b.windows[id]
	if w == nil || w.destroyed {
		b.mu.Unlock()
		return
	}
	w.destroyed = true
	notifyClose := !w.closing
	w.closing = true
	b.retired = append(b.retired, id)
	b.mu.Unlock()
	b.disconnectInteractive(w)
	// Unexpected/reentrant destruction must seal the core owner immediately,
	// even though final-reference release and its acknowledgment come later.
	if notifyClose {
		b.sink(platform.Event{Kind: platform.CloseRequested, Window: id})
	}
}

func (b *Backend) Wake() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("GTK dispatch target released")
	}
	if b.source == 0 {
		// Always queues on the default context; never use context_invoke,
		// which is allowed to execute on the worker that called it.
		b.source = n.idleAdd(priorityDefaultIdle, callbacks.idle, b.token, 0)
		if b.source == 0 {
			return errors.New("g_idle_add_full failed")
		}
		b.wakes++
	}
	return nil
}

func (b *Backend) dispatch() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.source = 0 // a producer during draining may queue the next source
	if b.paintErr != nil {
		// The core still owns this wake and may coalesce shutdown into it.
		// Deliver it from the next Pump, after the core receives the error.
		b.deferredWake = true
		b.mu.Unlock()
		return
	}
	b.dispatches++
	b.mu.Unlock()
	b.sink(platform.Event{Kind: platform.Wake})
}

func (b *Backend) Pump() error {
	// A native call outside iteration can also deliver a draw callback. Do
	// not block waiting for another event when that call has already failed.
	if err := b.takePaintError(); err != nil {
		return err
	}
	b.mu.Lock()
	deferred := b.deferredWake
	b.deferredWake = false
	if deferred {
		b.dispatches++
	}
	b.mu.Unlock()
	if deferred {
		// Do not consume source: a producer may already have queued another
		// native idle, which Release must still be able to remove.
		b.sink(platform.Event{Kind: platform.Wake})
		b.flushRetired()
		return b.takePaintError()
	}
	n.iteration(b.context, 1) // false is a legitimate interrupted/empty turn
	b.flushRetired()
	return b.takePaintError()
}

func (b *Backend) takePaintError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	err := b.paintErr
	b.paintErr = nil
	return err
}

func (b *Backend) flushRetired() {
	b.mu.Lock()
	retired := b.retired
	b.retired = nil
	b.mu.Unlock()
	for _, id := range retired {
		b.mu.Lock()
		w := b.windows[id]
		b.mu.Unlock()
		b.releaseNative(w)
		routes.Delete(w.token)
		b.mu.Lock()
		delete(b.windows, id)
		b.mu.Unlock()
		// Acknowledge only after the signal stack returned and our final
		// object reference is released, so Done cannot precede native cleanup.
		b.sink(platform.Event{Kind: platform.Destroyed, Window: id})
	}
}

func (b *Backend) Release() error {
	b.mu.Lock()
	if len(b.windows) != 0 {
		b.mu.Unlock()
		return errors.New("GTK release with live windows")
	}
	b.closed = true
	source := b.source
	b.source = 0
	b.mu.Unlock()
	if source != 0 {
		n.removeSource(source)
	}
	b.flushRetired()
	routes.Delete(b.token)
	n.release(b.context)
	b.mu.Lock()
	b.token = 0
	b.mu.Unlock()
	return nil
}

// Counters is an internal audition surface, not part of the public window API.
func (b *Backend) Counters() platform.Counters {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := platform.Counters{Wakes: b.wakes, Dispatches: b.dispatches, Windows: len(b.windows), Retired: len(b.retired),
		Presents: b.presents, Paints: b.paints, PaintFailures: b.paintFailures}
	if b.paintErr != nil {
		s.Problem = b.paintErr.Error()
	}
	if b.token != 0 {
		s.DispatchTargets = 1
	}
	return s
}

// CloseNextCreation requests a close reentrantly before the next Create returns.
// Only internal fixtures call this, on the UI thread.
func (b *Backend) CloseNextCreation() { b.closeNext = true }

// AuditionResize requests a native toplevel resize in logical units, as a
// window manager would. Internal fixtures only, on the UI thread.
func (b *Backend) AuditionResize(id platform.ID, width, height int) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	if width <= 0 || height <= 0 || width > math.MaxInt32 || height > math.MaxInt32 {
		return errors.New("GTK audition resize needs positive gint dimensions")
	}
	n.windowResize(w.widget, int32(width), int32(height))
	return nil
}

// AuditionIconify asks the window manager to minimize or restore the window.
// Whether the session reports the resulting state back is outside GTK's control.
func (b *Backend) AuditionIconify(id platform.ID, iconify bool) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	if iconify {
		n.windowIconify(w.widget)
	} else {
		// Deiconify alone may be ignored by focus-stealing prevention;
		// present also deiconifies and asks to raise the window.
		n.windowDeiconify(w.widget)
		n.windowPresent(w.widget)
	}
	return nil
}

// AuditionSnapshot writes the retained image, exactly as handed to GTK, to a
// PNG file. It is not a screen capture of compositor output.
func (b *Backend) AuditionSnapshot(id platform.ID, path string) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	if w.surface == 0 {
		return errors.New("GTK audition snapshot: no retained image yet")
	}
	if status := n.surfaceWritePNG(w.surface, path); status != 0 {
		return fmt.Errorf("cairo_surface_write_to_png: status %d", status)
	}
	return nil
}

// AuditionExpose queues a native redraw of the drawing area, as an exposure
// would. The draw handler paints only the retained image.
func (b *Backend) AuditionExpose(id platform.ID) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	n.queueDraw(w.area)
	return nil
}

func (b *Backend) auditionTarget(id platform.ID) (*window, error) {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.destroyed || w.closing {
		return nil, errors.New("GTK audition target is not open")
	}
	return w, nil
}

var _ platform.Backend = (*Backend)(nil)
