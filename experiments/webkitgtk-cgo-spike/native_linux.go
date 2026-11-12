//go:build linux && cgo

package main

// The native side: public GTK 3 and WebKitGTK 4.1 C interfaces only, plus
// two labelled probes (a host JSC context for the positive control, and the
// private GC-signal configuration call for one comparison). No JavaScript
// evaluation, result conversion or script messages in the ordinary host.

/*
#cgo pkg-config: webkit2gtk-4.1 gtk+-3.0 javascriptcoregtk-4.1
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <signal.h>
#include <stdbool.h>
#include <stdlib.h>
#include <sys/syscall.h>
#include <unistd.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
#include <jsc/jsc.h>

extern void goLoadChanged(int id, int event);
extern gboolean goDelete(int id);
extern void goIdle(void);

static void on_load(WebKitWebView *view, WebKitLoadEvent event, gpointer data) {
	goLoadChanged(GPOINTER_TO_INT(data), (int)event);
}

static gboolean on_delete(GtkWidget *widget, GdkEvent *event, gpointer data) {
	return goDelete(GPOINTER_TO_INT(data));
}

static gboolean on_idle(gpointer data) {
	goIdle();
	return G_SOURCE_REMOVE;
}

typedef struct {
	GtkWidget *window;
	WebKitWebContext *context;
	WebKitWebView *view;
} host_window;

static host_window *host_open(int id, const char *uri, const char *title) {
	host_window *w = g_new0(host_window, 1);
	w->context = webkit_web_context_new_ephemeral();
	w->view = WEBKIT_WEB_VIEW(webkit_web_view_new_with_context(w->context));
	w->window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
	gtk_window_set_default_size(GTK_WINDOW(w->window), 760, 560);
	gtk_window_set_title(GTK_WINDOW(w->window), title);
	gtk_container_add(GTK_CONTAINER(w->window), GTK_WIDGET(w->view));
	g_signal_connect(w->view, "load-changed", G_CALLBACK(on_load), GINT_TO_POINTER(id));
	g_signal_connect(w->window, "delete-event", G_CALLBACK(on_delete), GINT_TO_POINTER(id));
	gtk_widget_show_all(w->window);
	webkit_web_view_load_uri(w->view, uri);
	return w;
}

static void host_close(host_window *w) {
	gtk_widget_destroy(w->window);
	g_object_unref(w->context);
	g_free(w);
}

static void host_post(void) { g_idle_add(on_idle, NULL); }

// Positive control only: a JSC context in the host process.
static int host_vm_control(void) {
	JSCContext *context = jsc_context_new();
	JSCValue *value = jsc_context_evaluate(context, "6 * 7", -1);
	int result = jsc_value_to_int32(value);
	g_object_unref(value);
	g_object_unref(context);
	return result;
}

// Comparison only: the private configuration call, looked up at run time.
static int host_private_gc_signal(int signal) {
	bool (*configure)(int) = (bool (*)(int))dlsym(RTLD_DEFAULT, "JSConfigureSignalForGC");
	if (!configure) return -1;
	return configure(signal) ? 1 : 0;
}

static void host_altstack(int *flags, long *size) {
	stack_t ss;
	if (sigaltstack(NULL, &ss)) {
		*flags = -1;
		*size = 0;
		return;
	}
	*flags = ss.ss_flags;
	*size = (long)ss.ss_size;
}

static int host_gettid(void) { return (int)syscall(SYS_gettid); }

static const char *host_versions(void) {
	static char buf[128];
	snprintf(buf, sizeof buf, "webkit %u.%u.%u gtk %u.%u.%u", webkit_get_major_version(), webkit_get_minor_version(),
		webkit_get_micro_version(), gtk_get_major_version(), gtk_get_minor_version(), gtk_get_micro_version());
	return buf;
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

var native struct {
	sync.Mutex
	queue   []func()
	onLoad  func(id, event int)
	windows map[int]*C.host_window
}

// post runs f on the GTK main thread, inside goIdle.
func post(f func()) {
	native.Lock()
	native.queue = append(native.queue, f)
	native.Unlock()
	C.host_post()
}

//export goIdle
func goIdle() {
	native.Lock()
	q := native.queue
	native.queue = nil
	native.Unlock()
	for _, f := range q {
		f()
	}
}

//export goLoadChanged
func goLoadChanged(id, event C.int) {
	if native.onLoad != nil {
		native.onLoad(int(id), int(event))
	}
}

//export goDelete
func goDelete(id C.int) C.gboolean {
	return 1 // the probe closes windows itself
}

func gtkInit() bool { return C.gtk_init_check(nil, nil) != 0 }
func gtkMain()      { C.gtk_main() }
func gtkQuit()      { C.gtk_main_quit() }

func openWindow(id int, uri, title string) {
	cu, ct := C.CString(uri), C.CString(title)
	defer C.free(unsafe.Pointer(cu))
	defer C.free(unsafe.Pointer(ct))
	if native.windows == nil {
		native.windows = map[int]*C.host_window{}
	}
	native.windows[id] = C.host_open(C.int(id), cu, ct)
}

func closeWindow(id int) {
	if w := native.windows[id]; w != nil {
		C.host_close(w)
		delete(native.windows, id)
	}
}

func vmControl() int              { return int(C.host_vm_control()) }
func privateGCSignal(sig int) int { return int(C.host_private_gc_signal(C.int(sig))) }
func gettid() int                 { return int(C.host_gettid()) }
func versions() string            { return C.GoString(C.host_versions()) }

// altstack reports the calling thread's alternate signal stack.
func altstack() map[string]any {
	var flags C.int
	var size C.long
	C.host_altstack(&flags, &size)
	state := map[int]string{0: "enabled", 1: "on stack", 2: "disabled", -1: "error"}[int(flags)]
	return map[string]any{"tid": gettid(), "state": state, "size": int(size)}
}
