package gtk

import (
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/michaelquigley/dfx2/internal/platform"
)

var testGLib struct {
	once   sync.Once
	native native
	err    error
}

// Load GLib once for this process. No GTK initialization or display is needed.
// Like the production native libraries, its handle is retained for the process.
func schedulerGLib() (native, error) {
	testGLib.once.Do(func() {
		h, err := purego.Dlopen("libglib-2.0.so.0", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			testGLib.err = err
			return
		}
		for name, target := range map[string]any{
			"g_main_context_default":   &testGLib.native.defaultContext,
			"g_main_context_acquire":   &testGLib.native.acquire,
			"g_main_context_release":   &testGLib.native.release,
			"g_main_context_iteration": &testGLib.native.iteration,
			"g_idle_add_full":          &testGLib.native.idleAdd,
			"g_source_remove":          &testGLib.native.removeSource,
		} {
			address, err := purego.Dlsym(h, name)
			if err != nil {
				testGLib.err = fmt.Errorf("%s: %w", name, err)
				return
			}
			purego.RegisterFunc(target, address)
		}
	})
	return testGLib.native, testGLib.err
}

func TestDispatchYieldsToNativeResizeAndRedraw(t *testing.T) {
	glib, err := schedulerGLib()
	if err != nil {
		t.Skip("optional native GLib scheduler unavailable:", err)
	}
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	replaceNative(t)
	n = glib
	context := n.defaultContext()
	if n.acquire(context) == 0 {
		t.Fatal("could not acquire GLib default context")
	}
	installCallbacks() // shared permanent trampolines, including across -count
	app := &Backend{context: context, token: uintptr(nextToken.Add(1)), windows: make(map[platform.ID]*window)}
	routes.Store(app.token, route{backend: app})
	var probes []*Backend
	t.Cleanup(func() {
		// Remove still-ready probes on the deliberately starving old behavior,
		// as well as the next application wake left ready on successful runs.
		for _, probe := range probes {
			if probe.source != 0 {
				n.removeSource(probe.source)
			}
			routes.Delete(probe.token)
		}
		if err := app.Release(); err != nil {
			t.Error(err)
		}
	})
	var turns, resized, redrawn int
	queueProbe := func(priority int32, ran *int) {
		probe := &Backend{token: uintptr(nextToken.Add(1))}
		probe.sink = func(platform.Event) { *ran = turns }
		routes.Store(probe.token, route{backend: probe})
		probes = append(probes, probe)
		probe.source = n.idleAdd(priority, callbacks.idle, probe.token, 0)
		if probe.source == 0 {
			t.Error("could not queue native priority probe")
		}
	}
	app.sink = func(platform.Event) {
		turns++
		// A continuously nonempty public queue rearms the backend this way
		// between bounded callback batches. Test its real scheduler behavior.
		if err := app.Wake(); err != nil {
			t.Error(err)
		}
		if turns == 1 {
			// GTK_PRIORITY_RESIZE and GDK_PRIORITY_REDRAW from GTK 3 headers.
			// Queue after the first rearm so the scheduler must arbitrate.
			queueProbe(110, &resized)
			queueProbe(120, &redrawn)
		}
	}
	if err := app.Wake(); err != nil {
		t.Fatal(err)
	}
	// Iteration counts bound an always-ready workload; no wall time arranges
	// scheduling. Native sources must run while app dispatch keeps rearming.
	for range 64 {
		n.iteration(context, 0)
		if turns >= 3 && resized != 0 && redrawn != 0 {
			break
		}
	}
	if resized == 0 || redrawn == 0 {
		t.Fatalf("native work starved: app turns=%d resize-at=%d redraw-at=%d", turns, resized, redrawn)
	}
	if turns < 3 {
		t.Fatalf("application dispatch did not continue: %d turns", turns)
	}
}
