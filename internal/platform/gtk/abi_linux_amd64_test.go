package gtk

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

func currentThread() int { return syscall.Gettid() }

// Compare every binding with declarations from the installed development
// headers. Optional on machines lacking headers; the audition host must record
// PASS rather than SKIP. This compiles no C and calls no native functions.
func TestBindingsMatchHeaders(t *testing.T) {
	paths := []string{
		"/usr/include/gtk-3.0/gtk/gtkmain.h", "/usr/include/gtk-3.0/gtk/gtkwindow.h", "/usr/include/gtk-3.0/gtk/gtkwidget.h",
		"/usr/include/glib-2.0/glib/gmain.h", "/usr/include/glib-2.0/gobject/gsignal.h", "/usr/include/glib-2.0/gobject/gobject.h",
	}
	var corpus strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			t.Skip("native development headers absent:", path)
		}
		if err != nil {
			t.Fatal(err)
		}
		corpus.Write(data)
	}
	bindings := map[string]any{
		"gtk_init_check": n.initCheck, "gtk_window_new": n.windowNew, "gtk_window_set_title": n.setTitle,
		"gtk_window_set_default_size": n.setDefaultSize,
		"gtk_widget_show_all":         n.show, "gtk_widget_destroy": n.destroy, "gtk_widget_get_scale_factor": n.getScale,
		"g_signal_connect_data": n.connect, "g_signal_handler_disconnect": n.disconnect,
		"g_object_ref": n.ref, "g_object_unref": n.unref, "g_main_context_default": n.defaultContext,
		"g_main_context_acquire": n.acquire, "g_main_context_release": n.release, "g_main_context_iteration": n.iteration,
		"g_idle_add_full": n.idleAdd, "g_source_remove": n.removeSource,
	}
	stripName := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\s*$`)
	for name, binding := range bindings {
		pattern := regexp.MustCompile(`(?s)([A-Za-z_][A-Za-z0-9_]*\s*\*?)\s*` + name + `\s*\(([^();]*)\)\s*;`)
		match := pattern.FindStringSubmatch(corpus.String())
		if match == nil {
			t.Errorf("declaration not found: %s", name)
			continue
		}
		typ := reflect.TypeOf(binding)
		if strings.TrimSpace(match[1]) == "void" {
			if typ.NumOut() != 0 {
				t.Errorf("%s: void return bound as %v", name, typ)
			}
		} else if typ.NumOut() != 1 || !matchesC(match[1], typ.Out(0).Kind()) {
			t.Errorf("%s: return %s bound as %v", name, match[1], typ)
		}
		parameters := strings.Split(match[2], ",")
		if strings.TrimSpace(match[2]) == "void" {
			parameters = nil
		}
		if typ.NumIn() != len(parameters) {
			t.Errorf("%s: parameter count: header %d Go %d", name, len(parameters), typ.NumIn())
			continue
		}
		for i, parameter := range parameters {
			ctype := strings.TrimSpace(stripName.ReplaceAllString(parameter, ""))
			if !matchesC(ctype, typ.In(i).Kind()) {
				t.Errorf("%s parameter %d: %s bound as %v", name, i, ctype, typ.In(i))
			}
		}
	}
}

func matchesC(ctype string, kind reflect.Kind) bool {
	ctype = strings.Join(strings.Fields(ctype), " ")
	if strings.Contains(ctype, "*") {
		return kind == reflect.Uintptr || (kind == reflect.String && (strings.Contains(ctype, "char") || strings.Contains(ctype, "gchar")))
	}
	switch ctype {
	case "int", "gint", "gboolean", "GtkWindowType", "GConnectFlags":
		return kind == reflect.Int32
	case "guint":
		return kind == reflect.Uint32
	case "gulong":
		return kind == reflect.Uint64
	case "gpointer", "GCallback", "GSourceFunc", "GDestroyNotify", "GClosureNotify":
		return kind == reflect.Uintptr
	}
	return false
}
