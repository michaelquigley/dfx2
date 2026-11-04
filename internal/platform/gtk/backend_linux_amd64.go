package gtk

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"syscall"

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
var callbacks struct{ idle, destroyed, close, scale uintptr }

// GLib G_PRIORITY_DEFAULT_IDLE. GTK resize/redraw run at 110/120; a higher
// priority here can starve them when public callbacks keep rearming the source.
const priorityDefaultIdle int32 = 200

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
	})
}

type window struct {
	widget, token                           uintptr
	closeSignal, scaleSignal, destroySignal uint64
	width, height                           int // logical dimensions supplied by the application
	scale                                   int32
	closing                                 bool
	destroyed                               bool
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
		for _, signal := range []uint64{w.destroySignal, w.closeSignal, w.scaleSignal} {
			if signal != 0 {
				n.disconnect(widget, signal)
			}
		}
		n.destroy(widget)
		n.unref(widget)
		routes.Delete(w.token)
		b.mu.Lock()
		delete(b.windows, id)
		b.mu.Unlock()
		return errors.New("GTK could not connect window lifetime signals")
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
	b.dispatches++
	b.mu.Unlock()
	b.sink(platform.Event{Kind: platform.Wake})
}

func (b *Backend) Pump() error {
	n.iteration(b.context, 1) // false is a legitimate interrupted/empty turn
	b.flushRetired()
	return nil
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
		n.unref(w.widget)
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
	s := platform.Counters{Wakes: b.wakes, Dispatches: b.dispatches, Windows: len(b.windows), Retired: len(b.retired)}
	if b.token != 0 {
		s.DispatchTargets = 1
	}
	return s
}

// CloseNextCreation requests a close reentrantly before the next Create returns.
// Only internal fixtures call this, on the UI thread.
func (b *Backend) CloseNextCreation() { b.closeNext = true }

var _ platform.Backend = (*Backend)(nil)
