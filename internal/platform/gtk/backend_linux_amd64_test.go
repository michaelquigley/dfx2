package gtk

import (
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// These tests replace native entry points, never loading GTK or a display.
// Actual native execution and header checks are separate acceptance evidence.
func replaceNative(t *testing.T) {
	t.Helper()
	old := n
	t.Cleanup(func() { n = old })
}

func TestWakeDuringDispatchRearmsAndReleaseSealsTarget(t *testing.T) {
	replaceNative(t)
	var created, removed uint32
	n.idleAdd = func(_ int32, callback, token, notify uintptr) uint32 { created++; return created }
	n.removeSource = func(id uint32) int32 { removed = id; return 1 }
	n.release = func(uintptr) {}
	b := &Backend{windows: make(map[platform.ID]*window)}
	var deliveries int
	b.sink = func(e platform.Event) {
		if e.Kind != platform.Wake {
			t.Error(e)
		}
		deliveries++
		if err := b.Wake(); err != nil {
			t.Error(err)
		}
	}
	var producers sync.WaitGroup
	for range 32 {
		producers.Add(1)
		go func() {
			defer producers.Done()
			if err := b.Wake(); err != nil {
				t.Error(err)
			}
		}()
	}
	producers.Wait()
	if created != 1 {
		t.Fatalf("coalesced wake created %d sources", created)
	}
	b.dispatch()
	if deliveries != 1 || created != 2 {
		t.Fatalf("dispatch did not rearm: deliveries=%d sources=%d", deliveries, created)
	}
	if err := b.Release(); err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed source %d, want pending source 2", removed)
	}
	if err := b.Wake(); err == nil || created != 2 {
		t.Fatal("post-release wake touched native target")
	}
}

func TestDestroyAcknowledgmentWaitsForNativeStackAndFinalReference(t *testing.T) {
	replaceNative(t)
	b := &Backend{windows: map[platform.ID]*window{7: {widget: 123, token: 456, closeSignal: 1, scaleSignal: 2}}}
	routes.Store(uintptr(456), route{backend: b, window: 7})
	t.Cleanup(func() { routes.Delete(uintptr(456)) })
	var order []string
	n.disconnect = func(uintptr, uint64) { order = append(order, "disconnect") }
	n.destroy = func(widget uintptr) {
		order = append(order, "destroy-enter")
		b.destroyed(7, widget)
		order = append(order, "destroy-return")
	}
	n.iteration = func(uintptr, int32) int32 { return 0 }
	n.unref = func(widget uintptr) {
		if _, ok := routes.Load(uintptr(456)); !ok {
			t.Error("routing dropped before final native release")
		}
		order = append(order, "unref")
	}
	b.sink = func(e platform.Event) {
		if e.Kind != platform.Destroyed || e.Window != 7 {
			t.Error(e)
		}
		order = append(order, "acknowledge")
	}
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"disconnect", "disconnect", "destroy-enter", "destroy-return"}) {
		t.Fatalf("early native acknowledgment: %v", order)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"disconnect", "disconnect", "destroy-enter", "destroy-return", "unref", "acknowledge"}) {
		t.Fatal(order)
	}
	if len(b.windows) != 0 {
		t.Fatal("acknowledged native window still registered")
	}
}

func TestReentrantDestructionSealsCreationBeforeFinalAcknowledgment(t *testing.T) {
	replaceNative(t)
	b := &Backend{windows: make(map[platform.ID]*window)}
	var order []string
	n.windowNew = func(int32) uintptr { return 123 }
	n.ref = func(w uintptr) uintptr { return w }
	n.connect = func(uintptr, string, uintptr, uintptr, uintptr, int32) uint64 { return 1 }
	n.disconnect = func(uintptr, uint64) {}
	n.setTitle = func(uintptr, string) {}
	n.setDefaultSize = func(uintptr, int32, int32) {}
	n.getScale = func(uintptr) int32 { return 1 }
	n.show = func(widget uintptr) { b.destroyed(7, widget) }
	n.destroy = func(uintptr) { t.Error("already-destroyed widget destroyed twice") }
	n.iteration = func(uintptr, int32) int32 { return 0 }
	n.unref = func(uintptr) { order = append(order, "unref") }
	b.sink = func(e platform.Event) {
		switch e.Kind {
		case platform.CloseRequested:
			order = append(order, "close")
		case platform.Destroyed:
			order = append(order, "acknowledge")
		default:
			t.Error(e)
		}
	}
	if err := b.Create(7, platform.WindowOptions{Width: 100, Height: 100}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"close"}) {
		t.Fatalf("creation was not sealed before returning: %v", order)
	}
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"close", "unref", "acknowledge"}) {
		t.Fatal(order)
	}
}

func TestOwnerCheckRejectsAnotherOSThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	b := &Backend{owner: currentThread()}
	if !b.IsOwner() {
		t.Fatal("owner rejected")
	}
	other := make(chan bool, 1)
	go func() { runtime.LockOSThread(); defer runtime.UnlockOSThread(); other <- b.IsOwner() }()
	if <-other {
		t.Fatal("different OS thread accepted")
	}
}
