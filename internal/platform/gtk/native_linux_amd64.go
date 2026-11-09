// Package gtk implements the Linux/amd64 backend using GTK 3 without cgo.
package gtk

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Signatures match GTK 3/GLib headers; no native structs or Go pointers cross
// this boundary. gboolean/gint are int32, guint is uint32, and gulong is uint64
// on this backend's Linux/amd64 ABI.
type native struct {
	initCheck      func(uintptr, uintptr) int32
	windowNew      func(int32) uintptr
	setTitle       func(uintptr, string)
	setDefaultSize func(uintptr, int32, int32)
	show, destroy  func(uintptr)
	getScale       func(uintptr) int32
	connect        func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64
	disconnect     func(uintptr, uint64)
	ref            func(uintptr) uintptr
	unref          func(uintptr)
	defaultContext func() uintptr
	acquire        func(uintptr) int32
	release        func(uintptr)
	iteration      func(uintptr, int32) int32
	idleAdd        func(int32, uintptr, uintptr, uintptr) uint32
	removeSource   func(uint32) int32

	// Content surfaces. GdkWindowState is a flags enum returned in a 32-bit
	// register; cairo_format_t and cairo_status_t are int-sized enums.
	drawingAreaNew  func() uintptr
	containerAdd    func(uintptr, uintptr)
	queueDraw       func(uintptr)
	allocatedWidth  func(uintptr) int32
	allocatedHeight func(uintptr) int32
	mapped          func(uintptr) int32
	widgetWindow    func(uintptr) uintptr
	windowState     func(uintptr) uint32

	// Input. Event fields are read through GDK's accessors rather than struct
	// offsets. The out pointers are only used during each synchronous call.
	addEvents   func(uintptr, int32)
	setCanFocus func(uintptr, int32)
	grabFocus   func(uintptr)
	eventType   func(uintptr) int32
	eventCoords func(uintptr, *float64, *float64) int32
	eventButton func(uintptr, *uint32) int32
	eventKeyval func(uintptr, *uint32) int32
	eventState  func(uintptr, *uint32) int32

	imageSurfaceCreate func(int32, int32, int32) uintptr
	imageSurfaceData   func(uintptr) unsafe.Pointer
	imageSurfaceStride func(uintptr) int32
	surfaceStatus      func(uintptr) int32
	surfaceFlush       func(uintptr)
	surfaceMarkDirty   func(uintptr)
	surfaceSetScale    func(uintptr, float64, float64)
	surfaceDestroy     func(uintptr)
	setSourceSurface   func(uintptr, uintptr, float64, float64)
	paint              func(uintptr)
	contextStatus      func(uintptr) int32

	// Internal audition hooks only.
	windowResize    func(uintptr, int32, int32)
	windowIconify   func(uintptr)
	windowDeiconify func(uintptr)
	windowPresent   func(uintptr)
	surfaceWritePNG func(uintptr, string) int32
}

var n native
var loadOnce sync.Once
var loadError error

func load() error {
	loadOnce.Do(func() {
		groups := []struct {
			name    string
			symbols map[string]any
		}{
			{"libglib-2.0.so.0", map[string]any{
				"g_main_context_default": &n.defaultContext, "g_main_context_acquire": &n.acquire,
				"g_main_context_release": &n.release, "g_main_context_iteration": &n.iteration,
				"g_idle_add_full": &n.idleAdd, "g_source_remove": &n.removeSource,
			}},
			{"libgobject-2.0.so.0", map[string]any{
				"g_signal_connect_data": &n.connect, "g_signal_handler_disconnect": &n.disconnect,
				"g_object_ref": &n.ref, "g_object_unref": &n.unref,
			}},
			{"libgtk-3.so.0", map[string]any{
				"gtk_init_check": &n.initCheck, "gtk_window_new": &n.windowNew,
				"gtk_window_set_title": &n.setTitle, "gtk_window_set_default_size": &n.setDefaultSize,
				"gtk_widget_show_all": &n.show,
				"gtk_widget_destroy":  &n.destroy, "gtk_widget_get_scale_factor": &n.getScale,
				"gtk_drawing_area_new": &n.drawingAreaNew, "gtk_container_add": &n.containerAdd,
				"gtk_widget_queue_draw": &n.queueDraw, "gtk_widget_get_allocated_width": &n.allocatedWidth,
				"gtk_widget_get_allocated_height": &n.allocatedHeight, "gtk_widget_get_mapped": &n.mapped,
				"gtk_widget_get_window": &n.widgetWindow, "gtk_window_resize": &n.windowResize,
				"gtk_window_iconify": &n.windowIconify, "gtk_window_deiconify": &n.windowDeiconify,
				"gtk_window_present":    &n.windowPresent,
				"gtk_widget_add_events": &n.addEvents, "gtk_widget_set_can_focus": &n.setCanFocus,
				"gtk_widget_grab_focus": &n.grabFocus,
			}},
			{"libgdk-3.so.0", map[string]any{"gdk_window_get_state": &n.windowState,
				"gdk_event_get_event_type": &n.eventType, "gdk_event_get_coords": &n.eventCoords,
				"gdk_event_get_button": &n.eventButton, "gdk_event_get_keyval": &n.eventKeyval,
				"gdk_event_get_state": &n.eventState,
			}},
			{"libcairo.so.2", map[string]any{
				"cairo_image_surface_create": &n.imageSurfaceCreate, "cairo_image_surface_get_data": &n.imageSurfaceData,
				"cairo_image_surface_get_stride": &n.imageSurfaceStride, "cairo_surface_status": &n.surfaceStatus,
				"cairo_surface_flush": &n.surfaceFlush, "cairo_surface_mark_dirty": &n.surfaceMarkDirty,
				"cairo_surface_set_device_scale": &n.surfaceSetScale, "cairo_surface_destroy": &n.surfaceDestroy,
				"cairo_set_source_surface": &n.setSourceSurface, "cairo_paint": &n.paint,
				"cairo_status":               &n.contextStatus,
				"cairo_surface_write_to_png": &n.surfaceWritePNG,
			}},
		}
		for _, group := range groups {
			h, err := purego.Dlopen(group.name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				loadError = fmt.Errorf("GTK 3 load %s: %w", group.name, err)
				return
			}
			// Process-lifetime libraries: GTK registers global types and can
			// retain library code after the application target has been released.
			for name, target := range group.symbols {
				address, err := purego.Dlsym(h, name)
				if err != nil {
					loadError = fmt.Errorf("GTK 3 symbol %s: %w", name, err)
					return
				}
				purego.RegisterFunc(target, address)
			}
		}
	})
	return loadError
}
