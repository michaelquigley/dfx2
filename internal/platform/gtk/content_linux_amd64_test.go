package gtk

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// fakeCairo stands in for Cairo-owned image surfaces: Go buffers with a
// padded stride, so copies must honour the destination stride.
type fakeCairo struct {
	next      uintptr
	buffers   map[uintptr][]byte
	strides   map[uintptr]int
	failNext  int32
	destroyed []uintptr
	dirty     int
	scales    []float64
}

func installCairo(t *testing.T) *fakeCairo {
	t.Helper()
	c := &fakeCairo{next: 1000, buffers: map[uintptr][]byte{}, strides: map[uintptr]int{}}
	n.imageSurfaceCreate = func(format, w, h int32) uintptr {
		if format != cairoFormatARGB32 {
			t.Errorf("format %d", format)
		}
		c.next++
		if c.failNext != 0 {
			c.strides[c.next] = -1
			return c.next
		}
		stride := int(w)*4 + 16
		c.buffers[c.next], c.strides[c.next] = make([]byte, stride*int(h)), stride
		return c.next
	}
	n.surfaceStatus = func(s uintptr) int32 {
		if c.strides[s] < 0 {
			status := c.failNext
			c.failNext = 0
			return status
		}
		return 0
	}
	n.imageSurfaceData = func(s uintptr) unsafe.Pointer { return unsafe.Pointer(&c.buffers[s][0]) }
	n.imageSurfaceStride = func(s uintptr) int32 { return int32(c.strides[s]) }
	n.surfaceFlush = func(uintptr) {}
	n.surfaceMarkDirty = func(uintptr) { c.dirty++ }
	n.surfaceSetScale = func(_ uintptr, x, y float64) { c.scales = append(c.scales, x) }
	n.surfaceDestroy = func(s uintptr) { c.destroyed = append(c.destroyed, s); delete(c.buffers, s) }
	return c
}

func image(w, h int, fill byte) platform.Image {
	pix := make([]byte, w*h*4)
	for i := range pix {
		pix[i] = fill + byte(i)
	}
	return platform.Image{Width: w, Height: h, Stride: w * 4, Scale: 2, Pix: pix}
}

func contentBackend() *Backend {
	return &Backend{windows: map[platform.ID]*window{7: {widget: 11, area: 12, token: 13}}}
}

func TestPresentCopiesIntoRetainedNativeStorage(t *testing.T) {
	replaceNative(t)
	c := installCairo(t)
	var queued int
	n.queueDraw = func(area uintptr) {
		if area != 12 {
			t.Errorf("queued draw on %d", area)
		}
		queued++
	}
	b := contentBackend()
	img := image(3, 2, 0)
	if err := b.Present(7, img); err != nil {
		t.Fatal(err)
	}
	w := b.windows[7]
	first := w.surface
	buf := c.buffers[first]
	for y := range 2 {
		if !slices.Equal(buf[y*c.strides[first]:y*c.strides[first]+12], img.Pix[y*12:y*12+12]) {
			t.Fatalf("row %d not copied with the native stride", y)
		}
	}
	// Native code never sees the Go slice: mutating it later changes nothing.
	clear(img.Pix)
	if buf[1] != 1 {
		t.Fatal("retained image aliases the Go frame")
	}
	// Same size reuses the retained surface.
	if err := b.Present(7, image(3, 2, 50)); err != nil || w.surface != first || len(c.destroyed) != 0 {
		t.Fatalf("same-size present: err=%v surface=%d destroyed=%v", err, w.surface, c.destroyed)
	}
	// A failed replacement keeps the last valid image and leaks no error surface.
	c.failNext = 1
	err := b.Present(7, image(5, 4, 0))
	if err == nil || !strings.Contains(err.Error(), "status 1") || w.surface != first || w.surfaceWidth != 3 {
		t.Fatalf("failed replacement: err=%v surface=%d", err, w.surface)
	}
	if !reflect.DeepEqual(c.destroyed, []uintptr{first + 1}) {
		t.Fatalf("destroyed %v, want only the error surface", c.destroyed)
	}
	// A successful resize installs new storage before releasing the old one.
	if err := b.Present(7, image(5, 4, 0)); err != nil {
		t.Fatal(err)
	}
	if w.surface == first || w.surfaceWidth != 5 || c.destroyed[len(c.destroyed)-1] != first {
		t.Fatalf("resize: surface=%d destroyed=%v", w.surface, c.destroyed)
	}
	if queued != 3 || c.dirty != 3 || c.scales[0] != 2 || b.Counters().Presents != 3 {
		t.Fatalf("queued=%d dirty=%d scales=%v presents=%d", queued, c.dirty, c.scales, b.Counters().Presents)
	}
	bad := image(3, 2, 0)
	bad.Pix = bad.Pix[:10]
	if err := b.Present(7, bad); err == nil {
		t.Fatal("short image accepted")
	}
}

func TestPaintOnlyRepaintsTheRetainedImage(t *testing.T) {
	replaceNative(t)
	installCairo(t)
	n.queueDraw = func(uintptr) {}
	var painted []uintptr
	n.setSourceSurface = func(cr, surface uintptr, x, y float64) { painted = append(painted, cr, surface) }
	n.paint = func(cr uintptr) { painted = append(painted, cr) }
	b := contentBackend()
	b.sink = func(e platform.Event) { t.Errorf("exposure reached the core: %+v", e) }
	if got := b.paint(7, 99); got != 0 || len(painted) != 0 {
		t.Fatal("painted without a retained image")
	}
	if err := b.Present(7, image(2, 2, 0)); err != nil {
		t.Fatal(err)
	}
	surface := b.windows[7].surface
	for range 3 {
		if b.paint(7, 99) != 1 {
			t.Fatal("paint did not stop default drawing")
		}
	}
	if len(painted) != 9 || painted[1] != surface || b.Counters().Paints != 3 || b.Counters().Presents != 1 {
		t.Fatalf("painted %v counters %+v", painted, b.Counters())
	}
}

func TestSurfaceChangesAreCopiedAndDeduplicated(t *testing.T) {
	replaceNative(t)
	alloc, scale, mapped, state := [2]int32{200, 100}, int32(2), int32(1), uint32(0)
	n.getScale = func(uintptr) int32 { return scale }
	n.allocatedWidth = func(uintptr) int32 { return alloc[0] }
	n.allocatedHeight = func(uintptr) int32 { return alloc[1] }
	n.mapped = func(uintptr) int32 { return mapped }
	n.widgetWindow = func(uintptr) uintptr { return 44 }
	n.windowState = func(uintptr) uint32 { return state }
	b := contentBackend()
	var got []platform.Surface
	b.sink = func(e platform.Event) {
		if e.Kind != platform.SurfaceChanged || e.Window != 7 {
			t.Errorf("event %+v", e)
		}
		got = append(got, e.Surface)
	}
	b.surfaceChanged(7)
	b.surfaceChanged(7) // unchanged: no event
	state = gdkWindowStateIconified
	b.surfaceChanged(7)
	state, mapped = 0, 0
	b.surfaceChanged(7)
	mapped, scale = 1, 1
	b.surfaceChanged(7)
	want := []platform.Surface{
		{Width: 400, Height: 200, Scale: 2, Visible: true},
		{Width: 400, Height: 200, Scale: 2, Visible: false},
		{Width: 200, Height: 100, Scale: 1, Visible: true},
	}
	// Iconified and unmapped differ only in cause; the copied state is equal.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("surfaces %+v", got)
	}
	b.windows[7].destroyed = true
	b.surfaceChanged(7)
	if len(got) != 3 {
		t.Fatal("destroyed window still reported surface changes")
	}
}

func TestContentCreationFailureLeavesNoNativeObject(t *testing.T) {
	replaceNative(t)
	b := &Backend{windows: make(map[platform.ID]*window)}
	var order []string
	n.windowNew = func(int32) uintptr { return 11 }
	n.drawingAreaNew = func() uintptr { return 12 }
	n.ref = func(w uintptr) uintptr { order = append(order, "ref"); return w }
	n.containerAdd = func(uintptr, uintptr) {}
	connected := 0
	n.connect = func(object uintptr, name string, _, token, _ uintptr, _ int32) uint64 {
		if _, ok := routes.Load(token); !ok {
			t.Errorf("%s connected before routing", name)
		}
		connected++
		if name == "map" {
			return 0 // the native toolkit refuses one content signal
		}
		return uint64(connected)
	}
	disconnected := 0
	n.disconnect = func(uintptr, uint64) { disconnected++ }
	n.destroy = func(uintptr) { order = append(order, "destroy") }
	n.unref = func(o uintptr) { order = append(order, "unref") }
	n.show = func(uintptr) { t.Error("failed window shown") }
	b.sink = func(e platform.Event) { t.Errorf("failed creation delivered %+v", e) }
	err := b.Create(7, platform.WindowOptions{Width: 10, Height: 10, Content: true})
	if err == nil || !strings.Contains(err.Error(), "map") {
		t.Fatalf("create: %v", err)
	}
	// Every connected handler, destroy included, is disconnected first.
	if disconnected != connected-1 || !reflect.DeepEqual(order, []string{"ref", "ref", "destroy", "unref", "unref"}) {
		t.Fatalf("connected=%d disconnected=%d order=%v", connected, disconnected, order)
	}
	if len(b.windows) != 0 {
		t.Fatal("failed window still registered")
	}
	routes.Range(func(k, v any) bool {
		if v.(route).backend == b {
			t.Errorf("route %v survived failed creation", k)
		}
		return true
	})
}

func TestContentTeardownReleasesPixelsAfterFinalNativeUse(t *testing.T) {
	replaceNative(t)
	c := installCairo(t)
	n.queueDraw = func(uintptr) {}
	b := contentBackend()
	w := b.windows[7]
	w.closeSignal, w.scaleSignal = 1, 2
	w.contentSignals = []signal{{12, 3}, {12, 4}, {11, 5}}
	routes.Store(w.token, route{backend: b, window: 7})
	t.Cleanup(func() { routes.Delete(w.token) })
	if err := b.Present(7, image(2, 2, 0)); err != nil {
		t.Fatal(err)
	}
	surface := w.surface
	var order []string
	n.disconnect = func(object uintptr, id uint64) { order = append(order, "disconnect") }
	n.destroy = func(widget uintptr) {
		order = append(order, "destroy")
		b.destroyed(7, widget)
		if err := b.Present(7, image(2, 2, 0)); err == nil {
			t.Error("present accepted during native destruction")
		}
	}
	n.iteration = func(uintptr, int32) int32 { return 0 }
	n.unref = func(o uintptr) {
		if slices.Contains(c.destroyed, surface) {
			t.Error("pixels released before the final widget reference")
		}
		order = append(order, "unref")
	}
	n.surfaceDestroy = func(s uintptr) {
		if s == surface {
			order = append(order, "pixels")
		}
		c.destroyed = append(c.destroyed, s)
	}
	b.sink = func(e platform.Event) {
		if e.Kind == platform.Destroyed {
			order = append(order, "acknowledge")
		}
	}
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	want := []string{"disconnect", "disconnect", "disconnect", "disconnect", "disconnect", "destroy", "unref", "unref", "pixels", "acknowledge"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("teardown order %v", order)
	}
	if err := b.Present(7, image(2, 2, 0)); err == nil {
		t.Fatal("present after teardown accepted")
	}
}
