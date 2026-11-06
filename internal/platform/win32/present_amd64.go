package win32

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// retained is a window's last complete image: top-down BGRA in one native
// allocation. The spike saw GDI set no lines for frames in Go heap memory that
// straddled a 4 MiB boundary. The allocation-boundary cause is an inference
// from that host, so keep native storage without treating the cause as settled.
type retained struct {
	pixels        []byte // whole allocation
	width, height int
}

// cleanupDelay paces retries of a failed free, in milliseconds. It is a timer,
// not a reposted wake: a wake is immediately available to GetMessage, so
// reposting one would spin.
const cleanupDelay = 10

// cleanupTimerBase keeps retry timer IDs apart from modal delivery's, which
// count up from 1.
const cleanupTimerBase uintptr = 1 << 62

// cleanupState tracks allocations whose free failed. The allocations themselves
// stay on their windows, whose retirement waits for them. Only the armed one-shot
// starts a retry, so unrelated messages cannot speed retries up. UI-thread state,
// except the fields behind Counters.Problem, which are written under Backend.mu.
type cleanupState struct {
	armed   bool
	timer   uintptr // unique, so a late message cannot pass for the armed timer
	pending int
	freeErr error
	armErr  error
}

// freePixels releases one allocation. A failure leaves it owned by the window
// and schedules a retry. The caller reports the returned error, which begins
// shutdown. The window keeps the same error for its acknowledgment, because
// that report loses to any earlier failure and a later successful retry clears
// the diagnostics.
func (b *Backend) freePixels(w *window, pixels []byte) error {
	err := b.api.free(pixels)
	if err == nil {
		return nil
	}
	w.unfreed = append(w.unfreed, pixels)
	failure := fmt.Errorf("Win32 image cleanup: window %d: %w", w.id, err)
	w.cleanupErrs = append(w.cleanupErrs, failure)
	b.mu.Lock()
	b.cleanup.pending++
	b.cleanup.freeErr = err
	b.mu.Unlock()
	b.armCleanup()
	return failure
}

// armCleanup schedules one retry while allocations remain pending. If the timer
// itself fails, nextMessage carries the retries at the same pace.
func (b *Backend) armCleanup() {
	c := &b.cleanup
	if c.armed || c.pending == 0 {
		return
	}
	id := max(c.timer, cleanupTimerBase) + 1
	err := b.api.setTimer(b.application.hwnd, id, cleanupDelay)
	b.mu.Lock()
	c.armErr = err
	b.mu.Unlock()
	if err == nil {
		c.timer, c.armed = id, true
	}
}

func (b *Backend) stopCleanupTimer() {
	if b.cleanup.armed {
		b.api.killTimer(b.application.hwnd, b.cleanup.timer)
		b.cleanup.armed = false
	}
}

// retryFrees tries every pending allocation once. These are not the window's
// retained pixels, so no native use remains and any stack may run this. Only an
// allocation's first failure is fatal; repeats stay in the diagnostics.
func (b *Backend) retryFrees() {
	b.mu.Lock()
	windows := make([]*window, 0, len(b.windows))
	for _, w := range b.windows {
		windows = append(windows, w)
	}
	b.mu.Unlock()
	pending := 0
	var failure error
	for _, w := range windows {
		kept := w.unfreed[:0]
		for _, pixels := range w.unfreed {
			if err := b.api.free(pixels); err != nil {
				kept = append(kept, pixels)
				failure = err
			}
		}
		clear(w.unfreed[len(kept):])
		w.unfreed = kept
		pending += len(kept)
	}
	b.mu.Lock()
	b.cleanup.pending = pending
	if failure != nil {
		b.cleanup.freeErr = failure
	}
	if pending == 0 {
		b.cleanup.freeErr, b.cleanup.armErr = nil, nil
	}
	b.mu.Unlock()
	b.armCleanup()
}

// cleanupTimerFired runs one paced retry from the one-shot. Outside a size/move
// loop, Pump acknowledges whatever finished as soon as this message returns.
func (b *Backend) cleanupTimerFired(id uintptr) {
	if !b.cleanup.armed || id != b.cleanup.timer {
		return
	}
	b.stopCleanupTimer()
	b.retryFrees()
	if b.modalSafe() {
		b.flushRetired(b.modal.owner)
	}
}

// nextMessage retrieves the message Pump will dispatch. If allocations are
// pending and SetTimer has failed, nothing is coming to end a blocking wait, so
// it keeps the retries going at the same pace without blocking. A zero message
// is WM_NULL: dispatching it does nothing, and Pump goes on to acknowledge.
func (b *Backend) nextMessage(m *message) (int32, error) {
	if b.cleanup.armed || b.cleanup.pending == 0 {
		return b.api.getMessage(m)
	}
	b.pauseRetry()
	b.retryFrees()
	if b.api.peekMessage(m, 0, 0, 0) && m.id == wmQuit {
		return 0, nil
	}
	return 1, nil
}

// cleanupProblem describes pending allocations for the shutdown diagnostics.
// The caller holds Backend.mu.
func (b *Backend) cleanupProblem() string {
	c := &b.cleanup
	if c.pending == 0 {
		return ""
	}
	problem := fmt.Sprintf("Win32 image cleanup: unreleased allocations: %d: %v", c.pending, c.freeErr)
	if c.armErr != nil {
		problem += fmt.Sprintf("; retry timer unavailable: %v", c.armErr)
	}
	return problem
}

// reportSurface copies the current size, scale and visibility to the core when
// one of them changed. Closing has already sealed content work.
func (b *Backend) reportSurface(w *window) {
	if !w.content || w.closing || w.dpi == 0 {
		return
	}
	s := platform.Surface{Width: int(w.pixelWidth), Height: int(w.pixelHeight), Scale: float64(w.dpi) / 96, Visible: w.shown && !w.minimized}
	if s != w.reported {
		w.reported = s
		b.sink(platform.Event{Kind: platform.SurfaceChanged, Window: w.id, Surface: s})
	}
}

// Present copies a complete image into native storage and asks for a paint.
// A larger image fills a new allocation before the old one is freed, so a
// failed allocation keeps the last valid image. InvalidateRect only marks the
// window, so this delivers no events.
func (b *Backend) Present(id platform.ID, img platform.Image) error {
	if err := img.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || !w.content {
		return errors.New("Win32 present target has no content surface")
	}
	if w.destroyed {
		return errors.New("Win32 present after native destruction")
	}
	row, size, err := platform.ImageSize(img.Width, img.Height)
	if err != nil {
		return err
	}
	old := w.image.pixels
	storage := old
	// Same policy as the Go frame: reuse unless more than four times too large.
	if len(old) < size || len(old)/4 > size {
		// Headroom, so growing by dragging rarely reallocates.
		if storage, err = b.api.allocate(min(size+size/4, platform.MaxImageBytes)); err != nil {
			return fmt.Errorf("Win32 image %dx%d: %w", img.Width, img.Height, err)
		}
	}
	for y := range img.Height {
		copy(storage[y*row:(y+1)*row], img.Pix[y*img.Stride:y*img.Stride+row])
	}
	w.image = retained{pixels: storage, width: img.Width, height: img.Height}
	var freeErr error
	if len(old) != 0 && len(storage) != 0 && &old[0] != &storage[0] {
		freeErr = b.freePixels(w, old)
	}
	b.mu.Lock()
	b.presents++
	b.mu.Unlock()
	return errors.Join(freeErr, b.api.invalidate(w.hwnd))
}

// releaseImage frees retained pixels after the window's final native use. Its
// failure takes the normal fatal path, and retirement waits on w.unfreed.
func (b *Backend) releaseImage(w *window) {
	pixels := w.image.pixels
	w.image = retained{}
	if len(pixels) != 0 {
		b.nativeFailure(w, b.freePixels(w, pixels))
	}
}

// paintFailure counts a paint step that failed and latches it for Pump, which
// fails the application. A window that silently stopped showing its frames is
// the failure the spike saw, so no native result here is treated as transient
// without evidence from a real host.
func (b *Backend) paintFailure(w *window, err error) {
	b.mu.Lock()
	b.paintFailures++
	b.mu.Unlock()
	b.nativeFailure(w, fmt.Errorf("Win32 paint: window %d, %dx%d image in %dx%d client: %w",
		w.id, w.image.width, w.image.height, w.pixelWidth, w.pixelHeight, err))
}

// erase fills only what the retained image will not cover, in black rather
// than the class background, until the frame for the new size arrives.
func (b *Backend) erase(w *window, hdc uintptr) {
	if w.image.width >= int(w.pixelWidth) && w.image.height >= int(w.pixelHeight) {
		return
	}
	if err := b.api.fillBlack(hdc, &rect{right: w.pixelWidth, bottom: w.pixelHeight}); err != nil {
		b.paintFailure(w, err)
	}
}

// paint draws the retained image. It never asks the core for a frame: exposure
// of a valid image does not evaluate the view. False leaves the message to
// DefWindowProc, which validates the region so a failed BeginPaint cannot spin.
func (b *Backend) paint(w *window) bool {
	var ps paintStruct
	hdc, err := b.api.beginPaint(w.hwnd, &ps)
	if hdc == 0 {
		b.paintFailure(w, err)
		return false
	}
	defer b.api.endPaint(w.hwnd, &ps)
	if w.image.pixels == nil {
		return true
	}
	width, height := int32(w.image.width), int32(w.image.height)
	info := bitmapInfo{width: width, height: -height, planes: 1, bitCount: 32, compression: biRGB}
	info.size = uint32(unsafe.Offsetof(info.colors))
	lines, err := b.api.setDIBits(hdc, width, height, &w.image.pixels[0], &info)
	if lines <= 0 { // zero, or GDI_ERROR
		if err == nil {
			err = errors.New("SetDIBitsToDevice set no lines")
		}
		b.paintFailure(w, err)
		return true
	}
	b.mu.Lock()
	b.paints++
	b.mu.Unlock()
	return true
}

func (b *Backend) auditionTarget(id platform.ID) (*window, error) {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.destroyed || w.closing || !w.content {
		return nil, errors.New("Win32 audition target is not open")
	}
	return w, nil
}

// AuditionResize sets the client size in logical units, as a window manager
// would. Internal fixtures only, on the UI thread.
func (b *Backend) AuditionResize(id platform.ID, width, height int) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	if width <= 0 || height <= 0 || width > math.MaxInt32 || height > math.MaxInt32 {
		return errors.New("Win32 audition resize needs positive int dimensions")
	}
	cx, err := pixels(float64(width), w.dpi)
	if err != nil {
		return err
	}
	cy, err := pixels(float64(height), w.dpi)
	if err != nil {
		return err
	}
	r := rect{right: cx, bottom: cy}
	if err := b.api.adjustRect(&r, w.dpi); err != nil {
		return err
	}
	b.operation(func() { err = b.api.setPosition(w.hwnd, r, swpNoMove|swpNoZOrder|swpNoActivate) })
	return err
}

// AuditionIconify minimizes or restores the window through the native call.
func (b *Backend) AuditionIconify(id platform.ID, iconify bool) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	command := int32(swRestore)
	if iconify {
		command = swMinimize
	}
	b.operation(func() { b.api.show(w.hwnd, command) })
	return nil
}

// AuditionExpose invalidates the client area, as an exposure would. Desktop
// composition retains window contents, so occluding a window sends no WM_PAINT.
func (b *Backend) AuditionExpose(id platform.ID) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	return b.api.invalidate(w.hwnd)
}

// AuditionSnapshot writes the retained image, exactly as GDI would receive it,
// to a PNG file. It is not a screen capture.
func (b *Backend) AuditionSnapshot(id platform.ID, path string) error {
	w, err := b.auditionTarget(id)
	if err != nil {
		return err
	}
	if w.image.pixels == nil {
		return errors.New("Win32 audition snapshot: no retained image yet")
	}
	out := image.NewNRGBA(image.Rect(0, 0, w.image.width, w.image.height))
	for i := 0; i < w.image.width*w.image.height*4; i += 4 {
		s := w.image.pixels[i : i+4 : i+4]
		copy(out.Pix[i:i+4:i+4], []byte{s[2], s[1], s[0], 0xff})
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, out); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
