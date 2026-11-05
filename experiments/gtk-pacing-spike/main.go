//go:build linux && amd64

// A standalone pacing probe. No production runtime code is imported or changed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/ebitengine/purego"
)

func init() { runtime.LockOSThread() }

var n struct {
	mapped                                              func(uintptr) int32
	widgetWindow                                        func(uintptr) uintptr
	windowState                                         func(uintptr) uint32
	init                                                func(uintptr, uintptr) int32
	window                                              func(int32) uintptr
	title                                               func(uintptr, string)
	size                                                func(uintptr, int32, int32)
	resize                                              func(uintptr, int32, int32)
	area                                                func() uintptr
	add                                                 func(uintptr, uintptr)
	show, destroy, iconify, restore, present, queueDraw func(uintptr)
	width, height                                       func(uintptr) int32
	tick                                                func(uintptr, uintptr, uintptr, uintptr) uint32
	untick                                              func(uintptr, uint32)
	connect                                             func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64
	idle                                                func(int32, uintptr, uintptr, uintptr) uint32
	timeout                                             func(int32, uint32, uintptr, uintptr, uintptr) uint32
	remove                                              func(uint32) int32
	iteration                                           func(uintptr, int32) int32
	surface                                             func(int32, int32, int32) uintptr
	status                                              func(uintptr) int32
	freeSurface                                         func(uintptr)
	create                                              func(uintptr) uintptr
	freeCR                                              func(uintptr)
	rgb                                                 func(uintptr, float64, float64, float64)
	rect                                                func(uintptr, float64, float64, float64, float64)
	fill, paint                                         func(uintptr)
	source                                              func(uintptr, uintptr, float64, float64)
}

func bind() {
	groups := []struct {
		name string
		syms map[string]any
	}{
		{"libgtk-3.so.0", map[string]any{
			"gtk_widget_get_mapped": &n.mapped, "gtk_widget_get_window": &n.widgetWindow,
			"gtk_init_check": &n.init, "gtk_window_new": &n.window, "gtk_window_set_title": &n.title,
			"gtk_window_set_default_size": &n.size, "gtk_window_resize": &n.resize, "gtk_drawing_area_new": &n.area,
			"gtk_container_add": &n.add, "gtk_widget_show_all": &n.show, "gtk_widget_destroy": &n.destroy,
			"gtk_window_iconify": &n.iconify, "gtk_window_deiconify": &n.restore, "gtk_window_present": &n.present,
			"gtk_widget_queue_draw": &n.queueDraw, "gtk_widget_get_allocated_width": &n.width,
			"gtk_widget_get_allocated_height": &n.height, "gtk_widget_add_tick_callback": &n.tick,
			"gtk_widget_remove_tick_callback": &n.untick}},
		{"libgdk-3.so.0", map[string]any{"gdk_window_get_state": &n.windowState}},
		{"libgobject-2.0.so.0", map[string]any{"g_signal_connect_data": &n.connect}},
		{"libglib-2.0.so.0", map[string]any{"g_idle_add_full": &n.idle, "g_timeout_add_full": &n.timeout,
			"g_source_remove": &n.remove, "g_main_context_iteration": &n.iteration}},
		{"libcairo.so.2", map[string]any{"cairo_image_surface_create": &n.surface, "cairo_surface_status": &n.status,
			"cairo_surface_destroy": &n.freeSurface, "cairo_create": &n.create, "cairo_destroy": &n.freeCR,
			"cairo_set_source_rgb": &n.rgb, "cairo_rectangle": &n.rect, "cairo_fill": &n.fill,
			"cairo_paint": &n.paint, "cairo_set_source_surface": &n.source}},
	}
	for _, g := range groups {
		lib, err := purego.Dlopen(g.name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		for name, dst := range g.syms {
			purego.RegisterLibFunc(dst, lib, name)
		}
	}
}

// Tokens identify routes, never Go pointers. One permanent callback per signature.
const (
	drawEvent     = 1
	tickEvent     = 2
	deleteEvent   = 3
	requestSource = 4
	timerSource   = 5
	controlSource = 6
)

func token(kind int, id int) uintptr { return uintptr(kind)<<32 | uintptr(id) }

var eventCB, sourceCB, allocationCB, mappingCB uintptr
var windows []*window
var mode string
var started time.Time
var control bool
var phase int
var done bool

type counts struct {
	Frames   uint64 `json:"frames"`
	Paints   uint64 `json:"paints"`
	Ticks    uint64 `json:"ticks"`
	Requests uint64 `json:"requests"`
	Timers   uint64 `json:"timers"`
	Samples  uint64 `json:"samples"`
	Rendered uint64 `json:"rendered_sample"`
}
type window struct {
	visible, dirty                                 bool
	mu                                             sync.Mutex
	id                                             int
	widget, area, surface                          uintptr
	sw, sh                                         int32
	closed, pending, request, ready, animate, feed bool
	idle, timer, tick                              uint32
	count                                          counts
}

func main() {
	flag.StringVar(&mode, "mode", "tick", "timer, tick, paced, or continuous")
	flag.Parse()
	if mode != "timer" && mode != "tick" && mode != "paced" && mode != "continuous" {
		panic("mode must be timer, tick, paced, or continuous")
	}
	bind()
	if n.init(0, 0) == 0 {
		panic("GTK initialization failed")
	}
	started = time.Now()
	sourceCB = purego.NewCallback(func(t uintptr) int32 {
		kind, id := int(t>>32), int(t&0xffffffff)
		if kind == controlSource {
			control = true
			return 1
		}
		w := windows[id-1]
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed {
			return 0
		}
		switch kind {
		case requestSource:
			w.idle = 0
			w.request = true
		case timerSource:
			w.timer = 0
			if mode == "paced" {
				w.request = true
			} else {
				w.ready = true
			}
		}
		return 0
	})
	eventCB = purego.NewCallback(func(widget, arg, t uintptr) int32 {
		kind, id := int(t>>32), int(t&0xffffffff)
		w := windows[id-1]
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed {
			return 1
		}
		switch kind {
		case drawEvent:
			if w.surface != 0 {
				n.source(arg, w.surface, 0, 0)
				n.paint(arg)
				w.count.Paints++
			}
			return 1
		case tickEvent:
			if mode == "continuous" {
				w.count.Ticks++
				w.ready = true
				return 1
			}
			w.tick = 0
			w.count.Ticks++
			w.ready = true
			return 0
		case 7:
			w.refreshVisibilityLocked()
			return 0
		case deleteEvent:
			done = true
			return 1
		}
		return 0
	})
	allocationCB = purego.NewCallback(func(widget, allocation, t uintptr) {
		w := windows[int(t)-1]
		// Size notifications only request work, never render or dispatch application code.
		w.invalidate(false)
	})
	mappingCB = purego.NewCallback(func(widget, t uintptr) {
		w := windows[int(t)-1]
		w.mu.Lock()
		defer w.mu.Unlock()
		if !w.closed {
			w.refreshVisibilityLocked()
		}
	})
	for id := 1; id <= 2; id++ {
		w := &window{id: id, animate: true, feed: true}
		windows = append(windows, w)
		w.widget = n.window(0)
		w.area = n.area()
		n.title(w.widget, fmt.Sprintf("dfx2 GTK pacing — %s — %d", mode, id))
		if id == 1 {
			n.size(w.widget, 480, 260)
		} else {
			n.size(w.widget, 320, 160)
		}
		n.add(w.widget, w.area)
		n.connect(w.area, "draw", eventCB, token(drawEvent, id), 0, 0)
		n.connect(w.area, "size-allocate", allocationCB, uintptr(id), 0, 0)
		n.connect(w.widget, "delete-event", eventCB, token(deleteEvent, id), 0, 0)
		n.connect(w.widget, "window-state-event", eventCB, token(7, id), 0, 0)
		n.connect(w.area, "map", mappingCB, uintptr(id), 0, 0)
		n.connect(w.area, "unmap", mappingCB, uintptr(id), 0, 0)
		n.show(w.widget)
		w.invalidate(false)
	}
	stop := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		ticker := time.NewTicker(time.Second / 30)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for _, w := range windows {
					w.invalidate(true)
				}
			}
		}
	}()
	// Test orchestration only; this is not a rendering/animation timer.
	controlID := n.timeout(0, 50, sourceCB, token(controlSource, 0), 0)
	emit("started")
	for !done {
		n.iteration(0, 1)
		// This boundary corresponds to returning from the backend's native pump.
		if control {
			control = false
			advance()
		}
		for _, w := range windows {
			w.service()
		}
	}
	close(stop)
	<-joined
	n.remove(controlID)
	for _, w := range windows {
		w.close()
	}
	emit("closed")
}

// Producers keep publishing samples while a frame request is outstanding, but
// do not queue another native wake. A single pending request spans idle + tick.
func (w *window) invalidate(sample bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || (sample && !w.feed) {
		return
	}
	if sample {
		w.count.Samples++
	}
	w.dirty = true
	if (mode == "paced" || mode == "continuous") && !w.visible {
		return
	}
	if !w.pending {
		w.pending = true
		w.count.Requests++
		w.idle = n.idle(200, sourceCB, token(requestSource, w.id), 0)
	}
}

func (w *window) refreshVisibilityLocked() {
	visible := n.mapped(w.area) != 0
	if gdk := n.widgetWindow(w.widget); gdk != 0 && n.windowState(gdk)&3 != 0 {
		visible = false
	}
	if visible && !w.visible {
		w.dirty = true
	}
	w.visible = visible
}

func (w *window) service() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if (mode == "paced" || mode == "continuous") && !w.visible {
		idle, timer, tick := w.idle, w.timer, w.tick
		w.idle, w.timer, w.tick = 0, 0, 0
		w.pending, w.request, w.ready = false, false, false
		w.mu.Unlock()
		if idle != 0 {
			n.remove(idle)
		}
		if timer != 0 {
			n.remove(timer)
		}
		if tick != 0 {
			n.untick(w.area, tick)
		}
		return
	}
	if (mode == "paced" || mode == "continuous") && !w.pending && (w.dirty || w.animate) {
		w.pending = true
		w.request = true
	}
	if w.request {
		w.request = false
		if mode != "timer" {
			if w.tick != 0 {
				panic("duplicate tick registration")
			}
			w.tick = n.tick(w.area, eventCB, token(tickEvent, w.id), 0)
			if w.tick == 0 {
				panic("tick registration failed")
			}
		} else {
			w.ready = true
		}
	}
	if !w.ready {
		w.mu.Unlock()
		return
	}
	w.ready = false
	w.pending = mode == "continuous"
	w.dirty = false
	sample := w.count.Samples
	animate := w.animate
	w.mu.Unlock()
	// Simulated view evaluation and CPU image generation happen outside callbacks.
	width, height := n.width(w.area), n.height(w.area)
	if width <= 0 || height <= 0 {
		return
	}
	if w.surface == 0 || width != w.sw || height != w.sh {
		s := n.surface(0, width, height)
		if n.status(s) != 0 {
			panic("Cairo allocation failed")
		}
		if w.surface != 0 {
			n.freeSurface(w.surface)
		}
		w.surface, w.sw, w.sh = s, width, height
	}
	cr := n.create(w.surface)
	n.rgb(cr, 0.07, 0.08, 0.1)
	n.paint(cr)
	level := 0.5 + 0.45*math.Sin(time.Since(started).Seconds()*2)
	n.rgb(cr, 0.2, 0.8, 0.4)
	n.rect(cr, 16, 32, float64(width-32)*level, 32)
	n.fill(cr)
	n.freeCR(cr)
	w.mu.Lock()
	w.count.Frames++
	w.count.Rendered = sample
	// A continuous tick stays installed only while animation or unconsumed
	// invalidation needs it; no per-frame remove/re-add resets the native clock.
	if mode == "continuous" {
		if !animate && !w.dirty {
			n.untick(w.area, w.tick)
			w.tick = 0
			w.pending = false
		}
	} else if animate && !w.pending {
		w.pending = true
		if mode == "tick" {
			w.tick = n.tick(w.area, eventCB, token(tickEvent, w.id), 0)
		} else {
			w.count.Timers++
			w.timer = n.timeout(200, 17, sourceCB, token(timerSource, w.id), 0)
		}
	}
	w.mu.Unlock()
	n.queueDraw(w.area)
}

func (w *window) close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	idle, timer, tick := w.idle, w.timer, w.tick
	w.idle, w.timer, w.tick = 0, 0, 0
	w.pending, w.request, w.ready = false, false, false
	w.mu.Unlock()
	if idle != 0 {
		n.remove(idle)
	}
	if timer != 0 {
		n.remove(timer)
	}
	if tick != 0 {
		n.untick(w.area, tick)
	}
	n.destroy(w.widget)
	if w.surface != 0 {
		n.freeSurface(w.surface)
		w.surface = 0
	}
}

func emit(name string) {
	type state struct {
		counts
		Pending, Closed bool
		Visible         bool
		Tick, Timer     uint32
	}
	states := []state{}
	for _, w := range windows {
		w.mu.Lock()
		states = append(states, state{w.count, w.pending, w.closed, w.visible, w.tick, w.timer})
		w.mu.Unlock()
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"event": name, "mode": mode, "seconds": time.Since(started).Seconds(), "windows": states}); err != nil {
		panic(err)
	}
}

func advance() {
	steps := []struct {
		at   float64
		name string
		act  func()
	}{
		{1, "arranged", func() { n.present(windows[1].widget) }},
		{2, "minimize", func() { n.iconify(windows[0].widget) }},
		{3, "minimized-start", nil},
		{11, "minimized-end", func() { n.restore(windows[0].widget); n.present(windows[0].widget) }},
		{11.3, "restore-arranged", func() { n.present(windows[1].widget) }},
		{12, "restored", nil},
		{13, "pause", func() {
			for _, w := range windows {
				w.mu.Lock()
				w.animate = false
				w.feed = false
				w.mu.Unlock()
				w.invalidate(false)
			}
		}},
		{14, "quiet-start", nil},
		{17, "quiet-end", func() { n.queueDraw(windows[0].area) }},
		{18, "exposed", func() { n.resize(windows[0].widget, 600, 300) }},
		{19, "resized", func() {
			for _, w := range windows {
				w.mu.Lock()
				w.feed = true
				w.animate = true
				w.mu.Unlock()
				w.invalidate(false)
			}
			n.iconify(windows[0].widget)
		}},
		{20, "close-pending-start", nil},
		{22, "close-pending-end", func() { windows[0].close() }},
		{23, "survivor", func() { done = true }},
	}
	if phase < len(steps) && time.Since(started).Seconds() >= steps[phase].at {
		s := steps[phase]
		emit(s.name)
		phase++
		if s.act != nil {
			s.act()
		}
	}
}
