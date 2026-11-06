package win32

import (
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
)

func openContent(t *testing.T, f *systemModel, id platform.ID, width, height int) *window {
	t.Helper()
	if err := f.b.Create(id, platform.WindowOptions{Width: width, Height: height, Content: true}); err != nil {
		t.Fatal(err)
	}
	return f.b.windows[id]
}

func solid(width, height, stride int, b byte) platform.Image {
	pix := make([]byte, stride*height)
	for i := range pix {
		pix[i] = b
	}
	return platform.Image{Width: width, Height: height, Stride: stride, Scale: 1, Pix: pix}
}

func (f *systemModel) wakeEvents() (n int) {
	for _, e := range f.events {
		if e.Kind == platform.Wake {
			n++
		}
	}
	return n
}

func TestPresentCopiesAndExposurePaintsOnlyTheRetainedImage(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 4, 3)
	// A loose source stride proves the row copy; the retained image is tight.
	source := solid(4, 3, 24, 0)
	for y := range 3 {
		copy(source.Pix[y*24:], []byte{1, 2, byte(y), 0xff})
	}
	if err := f.b.Present(7, source); err != nil {
		t.Fatal(err)
	}
	if f.invalidated != 1 {
		t.Fatalf("present requested %d paints", f.invalidated)
	}
	for i := range source.Pix {
		source.Pix[i] = 0xee // native code must not see the lent slice again
	}
	before := len(f.events)
	for range 3 {
		if r := windowMessage(&f.api, w.hwnd, wmPaint, 0, 0); r != 0 {
			t.Fatalf("paint returned %d", r)
		}
	}
	if len(f.events) != before || f.paintBalance != 0 || len(f.blits) != 3 {
		t.Fatalf("exposure produced events %v, balance %d, blits %d", f.events[before:], f.paintBalance, len(f.blits))
	}
	blit := f.blits[0]
	if blit.bits != &w.image.pixels[0] || blit.width != 4 || blit.height != 3 {
		t.Fatalf("blit %+v", blit)
	}
	info := blit.info
	if info.size != 40 || info.width != 4 || info.height != -3 || info.bitCount != 32 || info.planes != 1 || info.compression != biRGB {
		t.Fatalf("not a top-down 32-bit BI_RGB header: %+v", info)
	}
	for y := range 3 {
		if row := w.image.pixels[y*16 : y*16+4]; row[0] != 1 || row[1] != 2 || row[2] != byte(y) || row[3] != 0xff {
			t.Fatalf("retained row %d: %v", y, row)
		}
	}
	if c := f.b.Counters(); c.Presents != 1 || c.Paints != 3 || c.PaintFailures != 0 {
		t.Fatalf("counters %+v", c)
	}
}

func TestAllocationFailureKeepsTheLastValidImage(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 4, 3)
	if err := f.b.Present(7, solid(4, 3, 16, 9)); err != nil {
		t.Fatal(err)
	}
	old := w.image
	boom := errors.New("out of address space")
	f.allocError = boom
	if err := f.b.Present(7, solid(400, 300, 1600, 8)); !errors.Is(err, boom) {
		t.Fatalf("present: %v", err)
	}
	if &w.image.pixels[0] != &old.pixels[0] || w.image.width != 4 || w.image.pixels[0] != 9 || f.frees != 0 {
		t.Fatalf("failed replacement touched the retained image: %+v frees=%d", w.image.width, f.frees)
	}
	windowMessage(&f.api, w.hwnd, wmPaint, 0, 0)
	if len(f.blits) != 1 || f.blits[0].bits != &old.pixels[0] {
		t.Fatal("exposure after a failed replacement lost the last image")
	}
	if c := f.b.Counters(); c.Presents != 1 {
		t.Fatalf("failed present counted: %+v", c)
	}
}

func TestStorageIsFilledBeforeReplacementAndReused(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 4, 3)
	if err := f.b.Present(7, solid(100, 100, 400, 1)); err != nil {
		t.Fatal(err)
	}
	first := &w.image.pixels[0]
	if f.allocations != 1 || len(w.image.pixels) != 40000+10000 {
		t.Fatalf("headroom: %d allocations of %d", f.allocations, len(w.image.pixels))
	}
	for _, size := range []int{100, 110, 60} { // same, inside headroom, smaller within 4×
		if err := f.b.Present(7, solid(size, size, size*4, 2)); err != nil {
			t.Fatal(err)
		}
	}
	if f.allocations != 1 || &w.image.pixels[0] != first || w.image.width != 60 {
		t.Fatalf("needless reallocation: %d", f.allocations)
	}
	f.trace = nil
	free := f.api.free
	f.api.free = func(pixels []byte) error {
		if w.image.width != 200 || w.image.pixels[0] != 3 {
			t.Error("previous image freed before its replacement was filled and installed")
		}
		return free(pixels)
	}
	if err := f.b.Present(7, solid(200, 200, 800, 3)); err != nil {
		t.Fatal(err)
	}
	f.api.free = free
	if len(f.trace) != 2 || f.trace[0] != "allocate" || f.trace[1] != "free" || w.image.pixels[0] != 3 {
		t.Fatalf("growth order: %v", f.trace)
	}
	if err := f.b.Present(7, solid(10, 10, 40, 4)); err != nil { // over four times too large
		t.Fatal(err)
	}
	if f.allocations != 3 || f.frees != 2 || len(f.storage) != 1 {
		t.Fatalf("shrink did not replace: %d/%d live %d", f.allocations, f.frees, len(f.storage))
	}
}

func TestInvalidImagesAreRefusedBeforeAllocation(t *testing.T) {
	f := model(t)
	openContent(t, f, 7, 4, 3)
	for name, image := range map[string]platform.Image{
		"short":    {Width: 4, Height: 3, Stride: 16, Scale: 1, Pix: make([]byte, 40)},
		"stride":   {Width: 4, Height: 3, Stride: 8, Scale: 1, Pix: make([]byte, 64)},
		"too big":  {Width: 1 << 14, Height: 1 << 14, Stride: 1 << 16, Scale: 1},
		"overflow": {Width: 1 << 40, Height: 1 << 40, Stride: 1 << 42, Scale: 1},
	} {
		if err := f.b.Present(7, image); err == nil {
			t.Errorf("%s image accepted", name)
		}
	}
	if f.allocations != 0 {
		t.Fatalf("%d allocations for refused images", f.allocations)
	}
}

func TestRetainedPixelsLastUntilAcknowledgedRetirement(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	var freedAtAck int
	sink := b.sink
	b.sink = func(e platform.Event) {
		if e.Kind == platform.Destroyed {
			freedAtAck = f.frees
		}
		sink(e)
	}
	// Close seals content work, but the native window can still paint.
	windowMessage(&f.api, w.hwnd, wmClose, 0, 0)
	windowMessage(&f.api, w.hwnd, wmPaint, 0, 0)
	if len(f.blits) != 1 || f.frees != 0 {
		t.Fatalf("closing window lost its image: blits=%d frees=%d", len(f.blits), f.frees)
	}
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if f.frees != 0 {
		t.Fatal("pixels freed before the native stack returned")
	}
	if err := b.Present(7, solid(4, 3, 16, 6)); err == nil {
		t.Fatal("present after native destruction accepted")
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if freedAtAck != 1 || len(f.storage) != 0 {
		t.Fatalf("freed %d before acknowledgment, live %d", freedAtAck, len(f.storage))
	}
	if err := b.Present(7, solid(4, 3, 16, 6)); err == nil {
		t.Fatal("present after acknowledgment accepted")
	}
}

// frees wraps the model's free so a test can fail chosen allocations and count
// every native attempt.
type frees struct {
	attempts int
	fail     func(pixels []byte) bool
}

var errFree = errors.New("free failed")

func failFrees(f *systemModel) *frees {
	c := &frees{fail: func([]byte) bool { return true }}
	free := f.api.free
	f.api.free = func(pixels []byte) error {
		c.attempts++
		if c.fail(pixels) {
			return errFree
		}
		return free(pixels)
	}
	return c
}

func (f *systemModel) destroyedEvents() (n int) {
	for _, e := range f.events {
		if e.Kind == platform.Destroyed {
			n++
		}
	}
	return n
}

// pumpTimer delivers the armed timer through Pump's own dispatch path.
func (f *systemModel) pumpTimer(t *testing.T) error {
	t.Helper()
	f.enqueue(message{hwnd: f.b.application.hwnd, id: wmTimer, wparam: f.armed(t)})
	return f.b.Pump()
}

// failedTeardown destroys a presented window whose free fails, through to the
// fatal report.
func failedTeardown(t *testing.T, f *systemModel, id platform.ID) *frees {
	t.Helper()
	c := failFrees(f)
	if err := f.b.Destroy(id); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Wake(); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Pump(); !errors.Is(err, errFree) {
		t.Fatalf("first free failure did not reach Pump: %v", err)
	}
	return c
}

func requirePending(t *testing.T, f *systemModel, attempts int, c *frees) {
	t.Helper()
	counters := f.b.Counters()
	if f.destroyedEvents() != 0 {
		t.Fatal("destruction acknowledged with an allocation still live")
	}
	if counters.Windows != 1 || counters.Retired != 1 {
		t.Fatalf("ownership not visible: %+v", counters)
	}
	if !strings.Contains(counters.Problem, errFree.Error()) || !strings.Contains(counters.Problem, "unreleased allocations: 1:") {
		t.Fatalf("diagnostics: %q", counters.Problem)
	}
	if c.attempts != attempts {
		t.Fatalf("%d free attempts, want %d", c.attempts, attempts)
	}
}

// A persistent failure holds everything open, at a paced retry, for as long as
// it lasts. The bound is this test's; the backend has none.
func TestPersistentFreeFailureRetainsOwnershipAtAPacedRetry(t *testing.T) {
	f := model(t)
	b := f.b
	openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	c := failedTeardown(t, f, 7)
	attempts := 1
	requirePending(t, f, attempts, c)
	wakes := b.Counters().Wakes
	for range 50 {
		id := f.armed(t)
		if delay := f.timers[id]; delay != 10 {
			t.Fatalf("retry delay %d", delay)
		}
		// A retry leaves nothing immediately available to retrieve: a posted
		// wake here would be the busy loop this timer exists to prevent.
		if err := f.pumpTimer(t); err != nil {
			t.Fatalf("a repeated failure was reported again: %v", err)
		}
		attempts++
		requirePending(t, f, attempts, c)
		if n := len(f.queue); n != 0 {
			t.Fatalf("%d messages immediately available after a retry", n)
		}
		if next := f.armed(t); next == id {
			t.Fatal("retry timer identity reused")
		}
	}
	if got := b.Counters().Wakes; got != wakes {
		t.Fatalf("retries posted %d wakes", got-wakes)
	}
	// Unrelated traffic does not speed retries up: only the timer starts one.
	armed := f.armed(t)
	for range 20 {
		if err := b.Wake(); err != nil {
			t.Fatal(err)
		}
		if err := b.Pump(); err != nil {
			t.Fatal(err)
		}
	}
	requirePending(t, f, attempts, c)
	if f.armed(t) != armed {
		t.Fatal("unrelated traffic rearmed the retry")
	}
	// Cleanup recovers, so this test drains.
	c.fail = func([]byte) bool { return false }
	if err := f.pumpTimer(t); err != nil {
		t.Fatal(err)
	}
	counters := b.Counters()
	if f.destroyedEvents() != 1 || counters.Windows != 0 || counters.Retired != 0 || counters.Problem != "" {
		t.Fatalf("destroyed=%d %+v", f.destroyedEvents(), counters)
	}
	if len(f.storage) != 0 || len(f.timers) != 0 {
		t.Fatalf("live storage %d, timers %v", len(f.storage), f.timers)
	}
	// The cleared diagnostics leave this as the failure's only remaining record,
	// and fifty failed retries add nothing to it.
	last := f.events[len(f.events)-1]
	if last.Kind != platform.Destroyed || !errors.Is(last.Err, errFree) || strings.Count(last.Err.Error(), errFree.Error()) != 1 {
		t.Fatalf("acknowledgment %+v", last)
	}
}

func TestFreeFailureRecoversOnItsFirstRetry(t *testing.T) {
	f := model(t)
	openContent(t, f, 7, 4, 3)
	if err := f.b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	c := failedTeardown(t, f, 7)
	c.fail = func([]byte) bool { return false }
	if err := f.pumpTimer(t); err != nil {
		t.Fatal(err)
	}
	if c.attempts != 2 || f.destroyedEvents() != 1 || len(f.storage) != 0 || len(f.timers) != 0 {
		t.Fatalf("attempts=%d destroyed=%d live=%d timers=%v", c.attempts, f.destroyedEvents(), len(f.storage), f.timers)
	}
}

func TestSuccessfulFreesScheduleNoRetry(t *testing.T) {
	f := model(t)
	b := f.b
	openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(100, 100, 400, 1)); err != nil {
		t.Fatal(err)
	}
	if err := b.Present(7, solid(200, 200, 800, 2)); err != nil { // replaces the first
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
	if len(f.timers) != 0 || f.frees != 2 || f.destroyedEvents() != 1 || b.Counters().Problem != "" {
		t.Fatalf("timers=%v frees=%d destroyed=%d %+v", f.timers, f.frees, f.destroyedEvents(), b.Counters())
	}
	if last := f.events[len(f.events)-1]; last.Err != nil {
		t.Fatalf("clean teardown acknowledged with %v", last.Err)
	}
}

// A replaced image's failed free keeps that allocation owned by its window,
// while the window keeps presenting from the new one.
func TestFailedReplacementFreeStaysTrackedAndIsNeverFreedTwice(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	old := &w.image.pixels[0]
	c := failFrees(f)
	if err := b.Present(7, solid(400, 300, 1600, 6)); !errors.Is(err, errFree) {
		t.Fatalf("present: %v", err)
	}
	if w.image.width != 400 || w.image.pixels[0] != 6 {
		t.Fatal("replacement not installed")
	}
	if _, live := f.storage[old]; !live {
		t.Fatal("failed free dropped from the model")
	}
	if p := b.Counters().Problem; !strings.Contains(p, errFree.Error()) {
		t.Fatalf("diagnostics: %q", p)
	}
	if err := f.pumpTimer(t); err != nil { // still failing, and not reported twice
		t.Fatal(err)
	}
	if c.attempts != 2 || f.armed(t) == 0 {
		t.Fatalf("attempts %d", c.attempts)
	}
	c.fail = func([]byte) bool { return false }
	if err := f.pumpTimer(t); err != nil {
		t.Fatal(err)
	}
	if _, live := f.storage[old]; live || len(f.storage) != 1 || len(f.timers) != 0 || b.Counters().Problem != "" {
		t.Fatalf("live=%v storage=%d timers=%v %q", live, len(f.storage), f.timers, b.Counters().Problem)
	}
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	b.flushRetired(nil)
	if c.attempts != 4 || f.destroyedEvents() != 1 || len(f.storage) != 0 { // the model errors on a double free
		t.Fatalf("attempts=%d destroyed=%d storage=%d", c.attempts, f.destroyedEvents(), len(f.storage))
	}
}

func TestAcknowledgmentWaitsForEveryAllocationAndRetriesOnlyWhatRemains(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	old := &w.image.pixels[0]
	c := failFrees(f)
	if err := b.Present(7, solid(400, 300, 1600, 6)); !errors.Is(err, errFree) {
		t.Fatalf("present: %v", err)
	}
	current := &w.image.pixels[0]
	if err := b.Destroy(7); err != nil {
		t.Fatal(err)
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	if err := b.Pump(); !errors.Is(err, errFree) {
		t.Fatalf("teardown free failure: %v", err)
	}
	if !strings.Contains(b.Counters().Problem, "unreleased allocations: 2:") {
		t.Fatalf("diagnostics: %q", b.Counters().Problem)
	}
	c.fail = func(pixels []byte) bool { return &pixels[0] == current }
	attempts := c.attempts
	if err := f.pumpTimer(t); err != nil {
		t.Fatal(err)
	}
	if c.attempts != attempts+2 || f.destroyedEvents() != 0 {
		t.Fatalf("attempts +%d destroyed=%d", c.attempts-attempts, f.destroyedEvents())
	}
	if _, live := f.storage[old]; live {
		t.Fatal("released allocation still live")
	}
	if !strings.Contains(b.Counters().Problem, "unreleased allocations: 1:") {
		t.Fatalf("diagnostics: %q", b.Counters().Problem)
	}
	c.fail = func([]byte) bool { return false }
	attempts = c.attempts
	if err := f.pumpTimer(t); err != nil {
		t.Fatal(err)
	}
	if c.attempts != attempts+1 || f.destroyedEvents() != 1 || len(f.storage) != 0 || len(f.timers) != 0 {
		t.Fatalf("attempts +%d destroyed=%d storage=%d timers=%v", c.attempts-attempts, f.destroyedEvents(), len(f.storage), f.timers)
	}
}

func TestLateRetryTimerMessagesAreIgnored(t *testing.T) {
	f := model(t)
	openContent(t, f, 7, 4, 3)
	if err := f.b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	c := failedTeardown(t, f, 7)
	first := f.armed(t)
	f.fire(t)
	attempts := c.attempts
	windowMessage(&f.api, f.b.application.hwnd, wmTimer, first, 0) // already killed, already delivered
	if c.attempts != attempts || f.armed(t) == first {
		t.Fatalf("stale timer retried: attempts +%d", c.attempts-attempts)
	}
	c.fail = func([]byte) bool { return false }
	f.fire(t)
	f.b.flushRetired(nil)
	attempts = c.attempts
	windowMessage(&f.api, f.b.application.hwnd, wmTimer, first, 0) // nothing left at all
	if c.attempts != attempts || len(f.timers) != 0 {
		t.Fatalf("attempts +%d timers %v", c.attempts-attempts, f.timers)
	}
}

// With no retry timer available nothing would end a blocking wait, so Pump keeps
// the same pace itself and still services messages.
func TestRetryContinuesWithoutATimerAndWithoutBlocking(t *testing.T) {
	f := model(t)
	b := f.b
	openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	pauses := 0
	b.waitRetry = func() { pauses++ }
	timerFailure := errors.New("no timers left")
	f.timerError = timerFailure
	c := failedTeardown(t, f, 7)
	if len(f.timers) != 0 || !strings.Contains(b.Counters().Problem, timerFailure.Error()) {
		t.Fatalf("timers=%v diagnostics %q", f.timers, b.Counters().Problem)
	}
	attempts, before := c.attempts, pauses
	for i := range 5 {
		// The model's GetMessage fails on an empty queue, as a blocked one never returns.
		if err := b.Pump(); err != nil {
			t.Fatal(err)
		}
		if c.attempts != attempts+i+1 || pauses != before+i+1 {
			t.Fatalf("attempts +%d pauses +%d after %d pumps", c.attempts-attempts, pauses-before, i+1)
		}
	}
	requirePending(t, f, c.attempts, c)
	var serviced int
	def := f.api.defWindow
	f.api.defWindow = func(hwnd, msg, wp, lp uintptr) uintptr {
		if msg == wmNCMouseFirst {
			serviced++
		}
		return def(hwnd, msg, wp, lp)
	}
	other := openContent(t, f, 8, 4, 3)
	f.enqueue(message{hwnd: other.hwnd, id: wmNCMouseFirst})
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	f.api.defWindow = def
	if serviced != 1 {
		t.Fatal("messages starved while retrying without a timer")
	}
	// The timer returns, and retries move back to it.
	f.timerError = nil
	if err := b.Pump(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Counters().Problem, timerFailure.Error()) {
		t.Fatalf("stale timer diagnostic: %q", b.Counters().Problem)
	}
	attempts = c.attempts
	before = pauses
	c.fail = func([]byte) bool { return false }
	if err := f.pumpTimer(t); err != nil {
		t.Fatal(err)
	}
	if c.attempts != attempts+1 || pauses != before || f.destroyedEvents() != 1 {
		t.Fatalf("attempts +%d pauses +%d destroyed=%d", c.attempts-attempts, pauses-before, f.destroyedEvents())
	}
}

func TestRetrySucceedingWithoutATimerStillAcknowledges(t *testing.T) {
	f := model(t)
	b := f.b
	openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	b.waitRetry = func() {}
	f.timerError = errors.New("no timers left")
	c := failedTeardown(t, f, 7)
	c.fail = func([]byte) bool { return false }
	if err := b.Pump(); err != nil { // must not block with the acknowledgment outstanding
		t.Fatal(err)
	}
	if f.destroyedEvents() != 1 || len(f.storage) != 0 || b.Counters().Problem != "" {
		t.Fatalf("destroyed=%d storage=%d %q", f.destroyedEvents(), len(f.storage), b.Counters().Problem)
	}
}

// Inside a size/move loop the retry still runs and another window can finish;
// the loop's own window still waits for Pump.
func TestRetriesAdvanceInsideASizeMoveLoop(t *testing.T) {
	f := model(t)
	b := f.b
	owner := openContent(t, f, 7, 4, 3)
	openContent(t, f, 8, 4, 3)
	for _, id := range []platform.ID{7, 8} {
		if err := b.Present(id, solid(4, 3, 16, 5)); err != nil {
			t.Fatal(err)
		}
	}
	sinkTurns(t, f, nil)
	c := failFrees(f)
	f.enter(owner)
	if err := b.Destroy(8); err != nil {
		t.Fatal(err)
	}
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.fire(t) // the modal turn's flush meets the failure
	if f.destroyedEvents() != 0 || c.attempts != 1 {
		t.Fatalf("destroyed=%d attempts=%d", f.destroyedEvents(), c.attempts)
	}
	f.fire(t) // still failing, on the retry timer
	if f.destroyedEvents() != 0 || c.attempts != 2 {
		t.Fatalf("destroyed=%d attempts=%d", f.destroyedEvents(), c.attempts)
	}
	c.fail = func([]byte) bool { return false }
	f.fire(t)
	if f.destroyedEvents() != 1 || len(f.timers) != 0 {
		t.Fatalf("destroyed=%d timers=%v", f.destroyedEvents(), f.timers)
	}
	f.exit(owner)
	if err := b.Pump(); !errors.Is(err, errFree) {
		t.Fatalf("first failure not reported after the loop: %v", err)
	}
}

func TestReleaseStopsAnArmedRetryTimerBeforeTheTargetGoes(t *testing.T) {
	f := model(t)
	b := f.b
	// No path leaves one armed at terminal release; this is the backstop behind
	// "timers stop before dispatch-target release".
	b.cleanup.timer, b.cleanup.armed = cleanupTimerBase+1, true
	f.timers[cleanupTimerBase+1] = cleanupDelay
	if err := b.Release(); err != nil {
		t.Fatal(err)
	}
	if len(f.timers) != 0 {
		t.Fatalf("timers %v", f.timers)
	}
}

// A paint that cannot reach the screen fails the application, however the
// failure arrived. Each case runs through Pump's own dispatch path, so the
// message that latches the failure is the one that returns it.
func TestPaintFailuresFailTheApplicationThroughPump(t *testing.T) {
	boom := errors.New("native paint failure")
	for name, c := range map[string]struct {
		breakPaint func(f *systemModel, defaulted *int)
		wantNative bool
		wantText   string
		wantPaints uint64
	}{
		"blit sets no lines": {
			breakPaint: func(f *systemModel, _ *int) { f.blitLines = 0 },
			wantText:   "set no lines",
		},
		"blit reports an error": {
			breakPaint: func(f *systemModel, _ *int) {
				f.api.setDIBits = func(uintptr, int32, int32, *byte, *bitmapInfo) (int32, error) { return 0, boom }
			},
			wantNative: true,
		},
		"blit returns GDI_ERROR": {
			breakPaint: func(f *systemModel, _ *int) {
				f.api.setDIBits = func(uintptr, int32, int32, *byte, *bitmapInfo) (int32, error) { return -1, boom }
			},
			wantNative: true,
		},
		"BeginPaint fails": {
			breakPaint: func(f *systemModel, defaulted *int) {
				f.api.beginPaint = func(uintptr, *paintStruct) (uintptr, error) { return 0, boom }
				def := f.api.defWindow
				f.api.defWindow = func(hwnd, msg, wp, lp uintptr) uintptr {
					if msg == wmPaint {
						*defaulted++
					}
					return def(hwnd, msg, wp, lp)
				}
			},
			wantNative: true,
		},
		"FillRect fails during erase": {
			breakPaint: func(f *systemModel, _ *int) {
				f.api.fillBlack = func(uintptr, *rect) error { return boom }
				// BeginPaint sends the erase before the blit, as Windows does.
				begin := f.api.beginPaint
				f.api.beginPaint = func(hwnd uintptr, p *paintStruct) (uintptr, error) {
					windowMessage(&f.api, hwnd, wmEraseBkgnd, 900, 0)
					return begin(hwnd, p)
				}
				for hwnd := range f.clients {
					f.clients[hwnd] = rect{right: 50, bottom: 30} // the image no longer covers it
				}
			},
			wantNative: true,
			wantPaints: 1, // the rest of the paint still ran
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := model(t)
			w := openContent(t, f, 7, 4, 3)
			if err := f.b.Present(7, solid(4, 3, 16, 5)); err != nil {
				t.Fatal(err)
			}
			var defaulted int
			c.breakPaint(f, &defaulted)
			windowMessage(&f.api, w.hwnd, wmSize, 0, 0)
			f.enqueue(message{hwnd: w.hwnd, id: wmPaint})
			err := f.b.Pump()
			if err == nil {
				t.Fatal("failed paint returned nil from Pump")
			}
			if c.wantNative && !errors.Is(err, boom) {
				t.Fatalf("native error lost: %v", err)
			}
			if !strings.Contains(err.Error(), c.wantText) {
				t.Fatalf("error %q does not name %q", err, c.wantText)
			}
			if !strings.Contains(err.Error(), "window 7") {
				t.Fatalf("error %q does not name the window", err)
			}
			counters := f.b.Counters()
			if counters.PaintFailures != 1 || counters.Paints != c.wantPaints {
				t.Fatalf("counters %+v", counters)
			}
			// EndPaint balances every BeginPaint that succeeded, and only those.
			if f.paintBalance != 0 {
				t.Fatalf("paint balance %d", f.paintBalance)
			}
			if name == "BeginPaint fails" && defaulted != 1 {
				t.Fatalf("a failed BeginPaint must leave validation to DefWindowProc, so it cannot spin: %d", defaulted)
			}
			// The failure is reported once; the backend still pumps during the drain.
			if err := f.b.Wake(); err != nil {
				t.Fatal(err)
			}
			if err := f.b.Pump(); err != nil {
				t.Fatalf("failure reported twice: %v", err)
			}
		})
	}
}

// A failed paint inside a size/move loop must not let another callback start,
// and it surfaces as soon as Pump regains control.
func TestPaintFailureInsideALoopHoldsDeliveryUntilPump(t *testing.T) {
	f := model(t)
	b := f.b
	w := openContent(t, f, 7, 4, 3)
	if err := b.Present(7, solid(4, 3, 16, 5)); err != nil {
		t.Fatal(err)
	}
	turns := sinkTurns(t, f, nil)
	f.enter(w)
	if err := b.Wake(); err != nil {
		t.Fatal(err)
	}
	f.loop()
	f.blitLines = 0
	windowMessage(&f.api, w.hwnd, wmPaint, 0, 0)
	f.fire(t)
	if *turns != 0 {
		t.Fatal("a turn started after a failed paint")
	}
	if f.paintBalance != 0 {
		t.Fatalf("paint balance %d", f.paintBalance)
	}
	f.exit(w)
	if err := b.Pump(); err == nil || !strings.Contains(err.Error(), "set no lines") {
		t.Fatalf("pump: %v", err)
	}
}

func TestEraseFillsOnlyWhatTheImageWillNotCover(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 40, 30)
	const hdc = 900
	if r := windowMessage(&f.api, w.hwnd, wmEraseBkgnd, hdc, 0); r != 1 || len(f.fills) != 1 || f.fills[0].right != 40 {
		t.Fatalf("no image: returned %d fills %v", r, f.fills)
	}
	windowMessage(&f.api, w.hwnd, wmPaint, 0, 0) // nothing retained yet: validated, not blitted
	if len(f.blits) != 0 || f.paintBalance != 0 {
		t.Fatalf("blits=%d balance=%d", len(f.blits), f.paintBalance)
	}
	if err := f.b.Present(7, solid(40, 30, 160, 5)); err != nil {
		t.Fatal(err)
	}
	f.fills = nil
	windowMessage(&f.api, w.hwnd, wmEraseBkgnd, hdc, 0)
	if len(f.fills) != 0 {
		t.Fatalf("covered client filled: %v", f.fills)
	}
	f.clients[w.hwnd] = rect{right: 50, bottom: 30} // grown before its frame arrives
	windowMessage(&f.api, w.hwnd, wmSize, 0, 0)
	windowMessage(&f.api, w.hwnd, wmEraseBkgnd, hdc, 0)
	if len(f.fills) != 1 || f.fills[0].right != 50 {
		t.Fatalf("uncovered client: %v", f.fills)
	}
}

func (f *systemModel) surfaces() (out []platform.Surface) {
	for _, e := range f.events {
		if e.Kind == platform.SurfaceChanged {
			out = append(out, e.Surface)
		}
	}
	return out
}

func TestSurfaceEventsCarryCopiedStateOnlyOnChange(t *testing.T) {
	f := model(t)
	f.dpi = 144
	w := openContent(t, f, 7, 320, 240)
	sized := platform.Surface{Width: 480, Height: 360, Scale: 1.5}
	shown := sized
	shown.Visible = true
	got := f.surfaces()
	if len(got) != 2 || got[0] != sized || got[1] != shown {
		t.Fatalf("creation surfaces %+v", got)
	}
	// Unchanged notifications, including exposure, stay silent.
	windowMessage(&f.api, w.hwnd, wmSize, 0, 0)
	windowMessage(&f.api, w.hwnd, wmShowWindow, 1, 0)
	windowMessage(&f.api, w.hwnd, wmPaint, 0, 0)
	if len(f.surfaces()) != 2 {
		t.Fatalf("duplicate surface: %+v", f.surfaces())
	}
	// Minimizing hides without reporting Windows' 0×0 client as a resize, so
	// restoring an unchanged window only changes visibility.
	f.clients[w.hwnd] = rect{}
	windowMessage(&f.api, w.hwnd, wmSize, sizeMinimized, 0)
	hidden := sized
	if got := f.surfaces(); got[len(got)-1] != hidden {
		t.Fatalf("minimized %+v", got[len(got)-1])
	}
	f.clients[w.hwnd] = rect{right: 480, bottom: 360}
	windowMessage(&f.api, w.hwnd, wmSize, 0, 0)
	if got := f.surfaces(); got[len(got)-1] != shown || w.pixelWidth != 480 {
		t.Fatalf("restored %+v", got[len(got)-1])
	}
	// A DPI change that the suggested rectangle leaves at the same client size
	// still reaches the core. This is synthetic: no monitor transition happened.
	f.memory[999] = rect{right: 496, bottom: 399}
	windowMessage(&f.api, w.hwnd, wmDPIChanged, 192|192<<16, 999)
	if got := f.surfaces(); got[len(got)-1] != (platform.Surface{Width: 480, Height: 360, Scale: 2, Visible: true}) {
		t.Fatalf("DPI %+v", got[len(got)-1])
	}
	// A close arriving while the DPI change's own native call runs seals it too.
	n := len(f.surfaces())
	setPosition := f.api.setPosition
	f.api.setPosition = func(hwnd uintptr, r rect, flags uint32) error {
		f.b.closeRequested(w)
		return nil
	}
	windowMessage(&f.api, w.hwnd, wmDPIChanged, 96|96<<16, 999)
	f.api.setPosition = setPosition
	f.clients[w.hwnd] = rect{right: 100, bottom: 100}
	windowMessage(&f.api, w.hwnd, wmSize, 0, 0)
	windowMessage(&f.api, w.hwnd, wmShowWindow, 0, 0)
	if len(f.surfaces()) != n {
		t.Fatal("surface reported after close")
	}
}

func TestOrdinaryWindowsKeepTheirBehavior(t *testing.T) {
	f := model(t)
	if err := f.b.Create(7, platform.WindowOptions{Width: 320, Height: 240}); err != nil {
		t.Fatal(err)
	}
	w := f.b.windows[7]
	var defaulted []uintptr
	def := f.api.defWindow
	f.api.defWindow = func(hwnd, msg, wp, lp uintptr) uintptr {
		defaulted = append(defaulted, msg)
		return def(hwnd, msg, wp, lp)
	}
	for _, msg := range []uintptr{wmPaint, wmEraseBkgnd} {
		windowMessage(&f.api, w.hwnd, msg, 0, 0)
	}
	if len(defaulted) != 2 || f.paintBalance != 0 || len(f.fills) != 0 {
		t.Fatalf("ordinary paint handled: %v", defaulted)
	}
	if len(f.surfaces()) != 0 {
		t.Fatalf("ordinary window reported surfaces: %+v", f.surfaces())
	}
	if err := f.b.Present(7, solid(4, 3, 16, 1)); err == nil {
		t.Fatal("ordinary window accepted an image")
	}
	if f.allocations != 0 {
		t.Fatal("ordinary window allocated pixels")
	}
}

func TestContentCreationFailureLeavesNothingBehind(t *testing.T) {
	f := model(t)
	boom := errors.New("adjust failed")
	f.adjustError = boom
	if err := f.b.Create(7, platform.WindowOptions{Width: 320, Height: 240, Content: true}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if len(f.b.windows) != 0 || f.allocations != 0 || len(f.storage) != 0 {
		t.Fatalf("windows=%d allocations=%d", len(f.b.windows), f.allocations)
	}
}

func TestAuditionHooksUseOrdinaryNativePaths(t *testing.T) {
	f := model(t)
	w := openContent(t, f, 7, 320, 240)
	if err := f.b.AuditionSnapshot(7, filepath.Join(t.TempDir(), "none.png")); err == nil {
		t.Fatal("snapshot without an image")
	}
	if err := f.b.AuditionResize(7, 640, 480); err != nil {
		t.Fatal(err)
	}
	if w.pixelWidth != 640 || w.pixelHeight != 480 {
		t.Fatalf("resized %dx%d", w.pixelWidth, w.pixelHeight)
	}
	if err := f.b.AuditionIconify(7, true); err != nil {
		t.Fatal(err)
	}
	if got := f.surfaces(); got[len(got)-1].Visible {
		t.Fatalf("iconified %+v", got[len(got)-1])
	}
	if err := f.b.AuditionIconify(7, false); err != nil {
		t.Fatal(err)
	}
	if got := f.surfaces(); !got[len(got)-1].Visible || got[len(got)-1].Width != 640 {
		t.Fatalf("restored %+v", got[len(got)-1])
	}
	before := f.invalidated
	if err := f.b.AuditionExpose(7); err != nil || f.invalidated != before+1 {
		t.Fatalf("expose: %v", err)
	}
	if err := f.b.Present(7, platform.Image{Width: 1, Height: 1, Stride: 4, Scale: 1, Pix: []byte{0x10, 0x20, 0x30, 0xff}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image.png")
	if err := f.b.AuditionSnapshot(7, path); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoded, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := decoded.At(0, 0).RGBA(); r>>8 != 0x30 || g>>8 != 0x20 || b>>8 != 0x10 {
		t.Fatalf("snapshot channels %x %x %x", r>>8, g>>8, b>>8)
	}
	f.b.closeRequested(w)
	if err := f.b.AuditionExpose(7); err == nil {
		t.Fatal("audition hook accepted a closing window")
	}
}
