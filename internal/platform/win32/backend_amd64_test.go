package win32

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// This model delivers Win32 creation/destruction callbacks synchronously and
// models the documented posted-message precedence. It calls no Windows APIs.
// The Windows host must still execute the portable tests and native auditions.
type systemModel struct {
	b                                                    *Backend
	api                                                  native
	data                                                 map[uintptr]uintptr
	clients                                              map[uintptr]rect
	memory                                               map[uintptr]rect
	live                                                 map[uintptr]bool
	queueMu                                              sync.Mutex
	queue                                                []message
	events                                               []platform.Event
	trace                                                []string
	next                                                 uintptr
	dpi                                                  uint32
	postError, getError, adjustError, targetDestroyError error
	shown                                                int
	insideCreate, insideDestroy                          bool

	// Native presentation and timers. Allocations are Go slices here; only the
	// Windows bridge returns VirtualAlloc memory.
	storage                   map[*byte]int
	allocError, timerError    error
	allocations, frees        int
	timers                    map[uintptr]uint32
	invalidated, paintBalance int
	blits                     []blit
	blitLines                 int32
	fills                     []rect
	client                    func(uintptr, *rect) error
}

type blit struct {
	bits          *byte
	info          bitmapInfo
	width, height int32
}

func model(t *testing.T) *systemModel {
	t.Helper()
	f := unopenedModel(t)
	if err := f.b.Open(func(e platform.Event) { f.events = append(f.events, e) }); err != nil {
		t.Fatal(err)
	}
	return f
}

// unopenedModel leaves Backend.Open to its caller, as the core does.
func unopenedModel(t *testing.T) *systemModel {
	t.Helper()
	f := &systemModel{data: make(map[uintptr]uintptr), clients: make(map[uintptr]rect), memory: make(map[uintptr]rect), live: make(map[uintptr]bool), next: 100, dpi: 96,
		storage: make(map[*byte]int), timers: make(map[uintptr]uint32), blitLines: -1}
	f.api.threadID = func() uint32 { return 7 }
	f.api.perMonitor = func() error { f.trace = append(f.trace, "dpi-awareness"); return nil }
	f.api.module = func() (uintptr, error) { return 1, nil }
	f.api.cursor = func() (uintptr, error) { return 2, nil }
	f.api.register = func(*classEx) error { f.trace = append(f.trace, "register"); return nil }
	f.api.unregister = func(*uint16, uintptr) error {
		if len(f.live) != 0 {
			t.Error("class unregistered with live windows")
		}
		f.trace = append(f.trace, "unregister")
		return nil
	}
	f.api.getUserData = func(hwnd uintptr) uintptr { return f.data[hwnd] }
	f.api.setUserData = func(hwnd, token uintptr) error { f.data[hwnd] = token; return nil }
	f.api.defWindow = func(hwnd, msg, wp, lp uintptr) uintptr {
		if msg == wmNCCreate {
			return 1
		}
		return 0
	}
	f.api.copyMemory = func(dst unsafe.Pointer, source, size uintptr) {
		switch size {
		case unsafe.Sizeof(createStruct{}):
			*(*createStruct)(dst) = createStruct{params: source}
		case unsafe.Sizeof(rect{}):
			*(*rect)(dst) = f.memory[source]
		default:
			t.Errorf("unexpected native copy size: %d", size)
		}
	}
	f.api.create = func(class, title *uint16, style uint32, parent, instance, token uintptr) (uintptr, error) {
		f.next++
		hwnd := f.next
		f.live[hwnd] = true
		f.insideCreate = true
		defer func() { f.insideCreate = false }()
		if parent == hwndMessage {
			f.trace = append(f.trace, "dispatch-create")
		} else {
			f.trace = append(f.trace, "content-create")
		}
		if windowMessage(&f.api, hwnd, wmNCCreate, 0, token) == 0 {
			delete(f.live, hwnd)
			return 0, errors.New("model WM_NCCREATE refused")
		}
		if windowMessage(&f.api, hwnd, wmCreate, 0, token) == ^uintptr(0) {
			_ = f.api.destroy(hwnd)
			return 0, errors.New("model WM_CREATE refused")
		}
		return hwnd, nil
	}
	f.api.getDPI = func(uintptr) (uint32, error) { return f.dpi, nil }
	f.api.adjustRect = func(r *rect, dpi uint32) error {
		if f.adjustError != nil {
			return f.adjustError
		}
		r.left -= 8
		r.top -= 31
		r.right += 8
		r.bottom += 8
		return nil
	}
	f.api.setPosition = func(hwnd uintptr, r rect, flags uint32) error {
		client := rect{right: r.right - r.left - 16, bottom: r.bottom - r.top - 39}
		changed := f.clients[hwnd] != client
		f.clients[hwnd] = client
		if changed { // Windows sends no WM_SIZE for an unchanged size
			windowMessage(&f.api, hwnd, wmSize, 0, 0)
		}
		return nil
	}
	f.api.clientRect = func(hwnd uintptr, r *rect) error {
		if f.client != nil {
			return f.client(hwnd, r)
		}
		*r = f.clients[hwnd]
		return nil
	}
	f.api.show = func(hwnd uintptr, command int32) {
		f.shown++
		switch command {
		case swShow:
			windowMessage(&f.api, hwnd, wmShowWindow, 1, 0)
		case swMinimize:
			windowMessage(&f.api, hwnd, wmSize, sizeMinimized, 0)
		case swRestore:
			windowMessage(&f.api, hwnd, wmSize, 0, 0)
		}
	}
	f.api.allocate = func(size int) ([]byte, error) {
		if f.allocError != nil {
			return nil, f.allocError
		}
		f.allocations++
		pixels := make([]byte, size)
		f.storage[&pixels[0]] = size
		f.trace = append(f.trace, "allocate")
		return pixels, nil
	}
	f.api.free = func(pixels []byte) error {
		if _, ok := f.storage[&pixels[0]]; !ok {
			t.Error("freed storage that is not a live allocation")
		}
		delete(f.storage, &pixels[0])
		f.frees++
		f.trace = append(f.trace, "free")
		return nil
	}
	f.api.invalidate = func(hwnd uintptr) error {
		if !f.live[hwnd] {
			return errors.New("invalidate on dead window")
		}
		f.invalidated++
		return nil
	}
	f.api.beginPaint = func(hwnd uintptr, p *paintStruct) (uintptr, error) { f.paintBalance++; return 900, nil }
	f.api.endPaint = func(hwnd uintptr, p *paintStruct) { f.paintBalance-- }
	f.api.setDIBits = func(hdc uintptr, width, height int32, bits *byte, info *bitmapInfo) (int32, error) {
		if _, ok := f.storage[bits]; !ok {
			t.Error("blit source is not live backend-owned storage")
		}
		f.blits = append(f.blits, blit{bits, *info, width, height})
		if f.blitLines >= 0 {
			return f.blitLines, nil
		}
		return height, nil
	}
	f.api.fillBlack = func(hdc uintptr, r *rect) error { f.fills = append(f.fills, *r); return nil }
	f.api.setTimer = func(hwnd, id uintptr, milliseconds uint32) error {
		if hwnd != f.b.application.hwnd {
			t.Error("modal timer not owned by the dispatch window")
		}
		if milliseconds == 0 {
			t.Error("modal timer without a positive delay")
		}
		if f.timerError != nil {
			return f.timerError
		}
		f.timers[id] = milliseconds
		f.trace = append(f.trace, "arm")
		return nil
	}
	f.api.killTimer = func(hwnd, id uintptr) {
		delete(f.timers, id)
		f.trace = append(f.trace, "kill")
	}
	f.api.setTitle = func(hwnd uintptr, title *uint16) error {
		if !f.live[hwnd] {
			return errors.New("title on dead window")
		}
		return nil
	}
	f.api.destroy = func(hwnd uintptr) error {
		if f.b.application != nil && hwnd == f.b.application.hwnd && f.targetDestroyError != nil {
			err := f.targetDestroyError
			f.targetDestroyError = nil
			return err
		}
		f.insideDestroy = true
		defer func() { f.insideDestroy = false }()
		windowMessage(&f.api, hwnd, wmDestroy, 0, 0)
		windowMessage(&f.api, hwnd, wmNCDestroy, 0, 0)
		delete(f.live, hwnd)
		return nil
	}
	f.api.postWake = func(hwnd uintptr) error {
		if hwnd != f.b.application.hwnd {
			t.Error("worker posted to a content window")
		}
		if f.postError != nil {
			err := f.postError
			f.postError = nil
			return err
		}
		f.enqueue(message{hwnd: hwnd, id: wmWake})
		return nil
	}
	f.api.getMessage = func(m *message) (int32, error) {
		if f.getError != nil {
			err := f.getError
			f.getError = nil
			return -1, err
		}
		f.queueMu.Lock()
		defer f.queueMu.Unlock()
		if len(f.queue) == 0 {
			return -1, errors.New("model has no ready messages")
		}
		// GetMessage gives posted application messages precedence over
		// paint/timer work, regardless of insertion order.
		index := 0
		for i, candidate := range f.queue {
			switch candidate.id {
			case wmPaint, wmTimer, wmKeyFirst, wmMouseFirst, wmNCMouseFirst:
				continue
			default:
				index = i
			}
			break
		}
		*m = f.queue[index]
		f.queue = append(f.queue[:index], f.queue[index+1:]...)
		if m.id == wmQuit {
			return 0, nil
		}
		return 1, nil
	}
	f.api.peekMessage = func(m *message, hwnd uintptr, min, max uint32) bool {
		f.queueMu.Lock()
		defer f.queueMu.Unlock()
		for i, candidate := range f.queue {
			if candidate.id == wmQuit || ((hwnd == 0 || candidate.hwnd == hwnd) && ((min == 0 && max == 0) || (candidate.id >= min && candidate.id <= max))) {
				*m = candidate
				f.queue = append(f.queue[:i], f.queue[i+1:]...)
				return true
			}
		}
		return false
	}
	f.api.dispatch = func(m *message) {
		switch m.id {
		case wmPaint:
			f.trace = append(f.trace, "paint")
		case wmKeyFirst:
			f.trace = append(f.trace, "input")
		case wmNCMouseFirst:
			f.trace = append(f.trace, "nonclient")
		}
		windowMessage(&f.api, m.hwnd, uintptr(m.id), m.wparam, m.lparam)
	}
	f.b = &Backend{api: &f.api}
	t.Cleanup(func() {
		if f.b.application == nil {
			return
		}
		// Every deliberate native ownership in this model has a release path.
		for id := range f.b.windows {
			_ = f.b.Destroy(id)
		}
		f.b.flushRetired(nil)
		if f.b.application.hwnd != 0 {
			f.b.closed = true
			_ = f.api.destroy(f.b.application.hwnd)
		}
		routes.Delete(f.b.application.token)
	})
	return f
}

func (f *systemModel) enqueue(m message) {
	f.queueMu.Lock()
	f.queue = append(f.queue, m)
	f.queueMu.Unlock()
}

func TestCreationCloseReentrancyAndNativeHandleReuse(t *testing.T) {
	f := model(t)
	b := f.b
	b.sink = func(e platform.Event) {
		if e.Kind == platform.CloseRequested && !f.insideCreate {
			t.Error("creation close was delayed")
		}
		if e.Kind == platform.Destroyed && (f.insideCreate || f.insideDestroy) {
			t.Error("native acknowledgment preceded stack return")
		}
		f.events = append(f.events, e)
	}
	b.CloseNextCreation()
	if err := b.Create(7, platform.WindowOptions{Width: 320, Height: 240}); err != nil {
		t.Fatal(err)
	}
	first := b.windows[7]
	if len(f.events) != 1 || f.events[0].Kind != platform.CloseRequested || f.shown != 0 {
		t.Fatalf("starting close: %v shown=%d", f.events, f.shown)
	}
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if _, ok := routes.Load(first.token); !ok {
		t.Fatal("route released before final native stack returned")
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if _, ok := routes.Load(first.token); ok {
		t.Fatal("route retained after acknowledgment")
	}
	f.next = first.hwnd - 1 // Windows may reuse the handle; IDs must not alias.
	if err := b.Create(8, platform.WindowOptions{Width: 320, Height: 240}); err != nil {
		t.Fatal(err)
	}
	second := b.windows[8]
	if first.hwnd != second.hwnd || first.token == second.token {
		t.Fatal("did not exercise handle reuse with distinct tokens")
	}
	if r := windowMessage(&f.api, second.hwnd, wmNCCreate, 0, first.token); r != 0 {
		t.Fatal("stale creation token accepted")
	}
	if f.data[second.hwnd] != second.token {
		t.Fatal("stale token overwrote new window routing")
	}
}

func TestFailedSizingRollsBackBeforeCreateReturns(t *testing.T) {
	f := model(t)
	boom := errors.New("adjust failed")
	f.adjustError = boom
	if err := f.b.Create(7, platform.WindowOptions{Width: 320, Height: 240}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if len(f.b.windows) != 0 || len(f.live) != 1 {
		t.Fatal("failed creation retained a content window")
	}
	f.b.flushRetired(nil)
	for _, event := range f.events {
		if event.Kind == platform.Destroyed {
			t.Fatal("failed creation emitted an owed destruction acknowledgment")
		}
	}
}

func TestRearmingWakeYieldsToInputAndPaint(t *testing.T) {
	f := model(t)
	b := f.b
	var turns int
	b.sink = func(e platform.Event) {
		if e.Kind != platform.Wake {
			return
		}
		f.trace = append(f.trace, "app")
		turns++
		f.enqueue(message{hwnd: b.application.hwnd, id: wmKeyFirst})
		f.enqueue(message{hwnd: b.application.hwnd, id: wmNCMouseFirst})
		f.enqueue(message{hwnd: b.application.hwnd, id: wmPaint})
		if err := b.Wake(); err != nil {
			t.Error(err)
		}
	}
	f.trace = nil
	f.enqueue(message{hwnd: b.application.hwnd, id: wmKeyFirst})
	f.enqueue(message{hwnd: b.application.hwnd, id: wmNCMouseFirst})
	f.enqueue(message{hwnd: b.application.hwnd, id: wmPaint})
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := b.Pump(); err != nil {
			t.Fatal(err)
		}
	}
	if turns != 3 || !reflect.DeepEqual(f.trace, []string{"input", "nonclient", "paint", "app", "input", "nonclient", "paint", "app", "input", "nonclient", "paint", "app"}) {
		t.Fatalf("native servicing starved: %v", f.trace)
	}
}

func TestWakeInsideNativeCreationWaitsForPump(t *testing.T) {
	f := model(t)
	b := f.b
	var delivered int
	b.sink = func(e platform.Event) {
		if e.Kind == platform.Wake {
			delivered++
			if f.insideCreate {
				t.Error("public dispatch reentered native creation")
			}
		}
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.api.getDPI = func(uintptr) (uint32, error) {
		// Model an internal native message loop consuming the posted wake
		// while WM_CREATE is still on the stack.
		var m message
		if result, err := f.api.getMessage(&m); result != 1 || err != nil {
			t.Fatalf("missing posted wake: %d %v", result, err)
		}
		f.api.dispatch(&m)
		if delivered != 0 {
			t.Error("native creation delivered a public callback")
		}
		return 96, nil
	}
	if err := b.Create(7, platform.WindowOptions{Width: 100, Height: 100}); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("deferred wake lost: %d", delivered)
	}
}

func TestUnexpectedQuitCannotReportCleanExit(t *testing.T) {
	f := model(t)
	f.enqueue(message{id: wmQuit})
	if err := f.b.Pump(); err == nil || !strings.Contains(err.Error(), "WM_QUIT") {
		t.Fatal(err)
	}
	if f.b.Counters().DispatchTargets != 1 {
		t.Fatal("unexpected quit released native target")
	}
}

func TestNativeCloseLeavesOtherWindowsAndDispatchAlive(t *testing.T) {
	f := model(t)
	b := f.b
	for _, id := range []platform.ID{7, 8} {
		if err := b.Create(id, platform.WindowOptions{Width: 100, Height: 100}); err != nil {
			t.Fatal(err)
		}
	}
	f.enqueue(message{hwnd: b.windows[7].hwnd, id: wmClose})
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 1 || f.events[0].Kind != platform.CloseRequested || f.events[0].Window != 7 {
		t.Fatal(f.events)
	}
	if err := b.SetTitle(8, "other window still open"); err != nil {
		t.Fatal(err)
	}
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if s := b.Counters(); s.Windows != 1 || s.DispatchTargets != 1 {
		t.Fatal(s)
	}
	for _, queued := range f.queue {
		if queued.id == wmQuit {
			t.Fatal("ordinary close posted application quit")
		}
	}
}

func TestFinalWakeFailureAndConcurrentCoalescing(t *testing.T) {
	f := model(t)
	b := f.b
	boom := errors.New("post failed")
	f.postError = boom
	if err := b.Wake(); !errors.Is(err, boom) || b.pending {
		t.Fatalf("failed wake retained ownership: %v pending=%t", err, b.pending)
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
	if len(f.queue) != 1 || f.queue[0].wparam != 0 || f.queue[0].lparam != 0 {
		t.Fatalf("coalesced payloads: %v", f.queue)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
}

func TestGetMessageFailureKeepsOwnershipAndRecovers(t *testing.T) {
	f := model(t)
	b := f.b
	if err := b.Create(7, platform.WindowOptions{Width: 100, Height: 100}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("get message failed")
	f.getError = boom
	if err := b.Pump(); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if s := b.Counters(); s.Windows != 1 || s.DispatchTargets != 1 || !strings.Contains(s.Problem, boom.Error()) {
		t.Fatalf("failed pump lost ownership: %+v", s)
	}
	var retries int
	b.waitRetry = func() { retries++ }
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if retries != 1 || b.Counters().Windows != 0 {
		t.Fatalf("recovery retry=%d counters=%+v", retries, b.Counters())
	}
}

func TestDPIChangesKeepClientAndLogicalUnitsSeparate(t *testing.T) {
	f := model(t)
	f.dpi = 144
	if err := f.b.Create(7, platform.WindowOptions{Width: 320, Height: 240}); err != nil {
		t.Fatal(err)
	}
	w := f.b.windows[7]
	if w.dpi != 144 || w.pixelWidth != 480 || w.pixelHeight != 360 || w.logicalWidth != 320 || w.logicalHeight != 240 {
		t.Fatalf("initial DPI: %+v", w)
	}
	f.memory[999] = rect{left: -800, top: 10, right: -144, bottom: 529} // client 640x480 at 192 dpi
	windowMessage(&f.api, w.hwnd, wmDPIChanged, 192|(192<<16), 999)
	if w.dpi != 192 || w.pixelWidth != 640 || w.pixelHeight != 480 || w.logicalWidth != 320 || w.logicalHeight != 240 {
		t.Fatalf("changed DPI: %+v", w)
	}
}

func TestDispatchReleaseFailureRetainsRoutingAndSealsPosts(t *testing.T) {
	f := model(t)
	b := f.b
	boom := errors.New("dispatch destroy failed")
	f.targetDestroyError = boom
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	b.waitRetry = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { done <- b.Release() }()
	<-entered
	if s := b.Counters(); s.DispatchTargets != 1 || !strings.Contains(s.Problem, boom.Error()) {
		t.Fatalf("release hid live target: %+v", s)
	}
	if _, ok := routes.Load(b.application.token); !ok {
		t.Fatal("release dropped live routing")
	}
	if err := b.Wake(); err == nil {
		t.Fatal("release admitted a new post")
	}
	select {
	case err := <-done:
		t.Fatalf("returned with live target: %v", err)
	default:
	}
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if b.Counters().DispatchTargets != 0 {
		t.Fatal("dispatch target still alive after release")
	}
}
