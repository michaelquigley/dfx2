package webkit

import (
	"sync"

	"github.com/ebitengine/purego"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// Five permanent trampolines, one per native signature, created once per
// process. Native userdata is a numeric token; Go state lives in a registry
// keyed by it, never in native ownership.
var callbacks struct {
	once                        sync.Once
	one, two, three, four, five uintptr
}

// Token kinds.
const (
	kindScheme       = iota + 1 // URI-scheme request: (request, data)
	kindPolicy                  // decide-policy: (view, decision, type, data) gboolean
	kindLoadFailed              // load-failed: (view, event, uri, error, data) gboolean
	kindCreate                  // create: (view, action, data) GtkWidget*
	kindPermission              // permission-request: (view, request, data) gboolean
	kindTerminated              // web-process-terminated: (view, reason, data)
	kindDownload                // download-started: (context, download, data)
	kindAsync                   // GAsyncReadyCallback: (source, result, data)
	kindDestroy                 // idle: deferred native destruction
	kindBytesFreed              // GDestroyNotify, any thread: a response copy was freed
	kindResponseIdle            // idle: that release, on the UI thread
)

// priorityDefaultIdle matches the GTK backend's dispatch source.
const priorityDefaultIdle int32 = 200

type target struct {
	b     *browser
	kind  int
	async func(source, result uintptr)
	id    platform.ID // a response copy's request
	data  uintptr     // a response copy's g_malloc storage
}

// registry is locked because GDestroyNotify may run on any thread.
var registry struct {
	sync.Mutex
	next    uintptr
	targets map[uintptr]*target
}

func register(t *target) uintptr {
	registry.Lock()
	defer registry.Unlock()
	if registry.targets == nil {
		registry.targets = make(map[uintptr]*target)
	}
	registry.next++
	registry.targets[registry.next] = t
	return registry.next
}

func lookup(token uintptr) *target {
	registry.Lock()
	defer registry.Unlock()
	return registry.targets[token]
}

func drop(token uintptr) *target {
	registry.Lock()
	defer registry.Unlock()
	t := registry.targets[token]
	delete(registry.targets, token)
	return t
}

func installCallbacks() {
	callbacks.once.Do(func() {
		callbacks.one = purego.NewCallback(func(token uintptr) uintptr {
			t := lookup(token)
			if t == nil {
				return 0
			}
			switch t.kind {
			case kindBytesFreed:
				// Possibly off the UI thread: free the copy, then hop.
				w.free(t.data)
				registry.Lock()
				t.kind, t.data = kindResponseIdle, 0
				registry.Unlock()
				w.idleAdd(priorityDefaultIdle, callbacks.one, token, 0)
			case kindResponseIdle:
				drop(token)
				t.b.responseFreed(t.id)
			case kindDestroy:
				drop(token)
				t.b.destroy()
			}
			return 0 // G_SOURCE_REMOVE
		})
		callbacks.two = purego.NewCallback(func(request, token uintptr) uintptr {
			if t := lookup(token); t != nil && t.kind == kindScheme {
				t.b.receive(request)
			} else {
				refuseOrphan(request)
			}
			return 0
		})
		callbacks.three = purego.NewCallback(func(a, b, token uintptr) uintptr {
			t := lookup(token)
			if t == nil {
				return 0
			}
			switch t.kind {
			case kindAsync:
				drop(token)
				t.b.completed(t.async, a, b)
			case kindCreate:
				t.b.popup()
				return 0 // no new web view
			case kindPermission:
				t.b.permission(b)
				return 1
			case kindTerminated:
				t.b.terminated(int32(b))
			case kindDownload:
				t.b.download(b)
			}
			return 0
		})
		callbacks.four = purego.NewCallback(func(view, decision, kind, token uintptr) uintptr {
			if t := lookup(token); t != nil && t.kind == kindPolicy {
				return t.b.policy(decision, int32(kind))
			}
			return 0
		})
		callbacks.five = purego.NewCallback(func(view, event, uri, gerr, token uintptr) uintptr {
			if t := lookup(token); t != nil && t.kind == kindLoadFailed {
				return t.b.loadFailed(cstring(uri), gerr)
			}
			return 0
		})
	})
}

// refuseOrphan finishes a request whose window no longer exists, so WebKit
// never waits on it.
func refuseOrphan(request uintptr) { failNative(request, "browser window closed") }

// failNative finishes a native request without a response. WebKit does not
// take the GError, so it is freed here: every failure path goes through this.
func failNative(request uintptr, message string) {
	e := w.errorNewLiteral(w.quarkFromString("dfx2-webview"), 1, message)
	w.schemeFinishError(request, e)
	w.errorFree(e)
}
