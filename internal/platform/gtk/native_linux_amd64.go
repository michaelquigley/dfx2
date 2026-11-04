// Package gtk implements the Linux/amd64 backend using GTK 3 without cgo.
package gtk

import (
	"fmt"
	"sync"

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
