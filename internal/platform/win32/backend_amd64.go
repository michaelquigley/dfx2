package win32

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/platform"
)

var routes sync.Map // numeric creation token -> *window; no native Go pointers
var nextToken atomic.Uint64

type window struct {
	backend                                               *Backend
	id                                                    platform.ID
	token, hwnd                                           uintptr
	dispatch, creating, closing, destroyed, closeOnCreate bool
	createErr                                             error
	dpi                                                   uint32
	logicalWidth, logicalHeight                           float64
	pixelWidth, pixelHeight                               int32
}

// Backend owns the message-only dispatch window until appcore authorizes
// release. All native callbacks latch state; public dispatch waits until the
// native call stack has returned to Pump.
type Backend struct {
	mu                                sync.Mutex
	api                               *native
	owner                             uint32
	sink                              func(platform.Event)
	instance                          uintptr
	className                         []uint16
	application                       *window
	windows                           map[platform.ID]*window
	retired                           []uintptr
	pending, ready, closed, closeNext bool
	backendErr                        error
	problem                           string
	messageFailed                     bool
	waitRetry                         func() // injected failure-path barrier
	wakes, dispatches                 uint64
}

func text(s string) ([]uint16, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return nil, errors.New("Win32 text contains NUL")
	}
	return append(utf16.Encode([]rune(s)), 0), nil
}

func (b *Backend) record(id platform.ID, dispatch bool) *window {
	w := &window{backend: b, id: id, token: uintptr(nextToken.Add(1)), dispatch: dispatch, creating: true}
	routes.Store(w.token, w)
	return w
}

func (b *Backend) Open(sink func(platform.Event)) error {
	if b.api == nil {
		api, err := systemAPI()
		if err != nil {
			return err
		}
		b.api = api
	}
	b.owner, b.sink = b.api.threadID(), sink
	if err := b.api.perMonitor(); err != nil {
		return err
	}
	instance, err := b.api.module()
	if err != nil {
		return err
	}
	cursor, err := b.api.cursor()
	if err != nil {
		return err
	}
	b.instance = instance
	b.className, _ = text(windowClass)
	class := classEx{size: uint32(unsafe.Sizeof(classEx{})), procedure: b.api.procedure,
		instance: instance, cursor: cursor, background: 6, className: &b.className[0]} // COLOR_WINDOW + 1
	if err := b.api.register(&class); err != nil {
		return err
	}
	b.windows = make(map[platform.ID]*window)
	w := b.record(0, true)
	b.application = w
	empty := []uint16{0}
	hwnd, createErr := b.api.create(&b.className[0], &empty[0], 0, hwndMessage, instance, w.token)
	w.creating = false
	if hwnd == 0 || w.destroyed {
		routes.Delete(w.token)
		if w.createErr != nil {
			createErr = w.createErr
		}
		if createErr == nil {
			createErr = errors.New("Win32 dispatch window creation failed")
		}
		return errors.Join(createErr, b.api.unregister(&b.className[0], instance))
	}
	return nil
}

func (b *Backend) IsOwner() bool { return b.api.threadID() == b.owner }

func (b *Backend) Create(id platform.ID, options platform.WindowOptions) error {
	if options.Content {
		// Stage 2 of the synthetic mixer adds Win32 presentation. Until then,
		// refuse before any native work so ordinary windows keep working.
		return platform.ErrUnsupportedContent
	}
	if options.Width <= 0 || options.Height <= 0 || options.Width > math.MaxInt32 || options.Height > math.MaxInt32 {
		return errors.New("Win32 client dimensions must fit positive int values")
	}
	title, err := text(options.Title)
	if err != nil {
		return err
	}
	w := b.record(id, false)
	w.logicalWidth, w.logicalHeight = float64(options.Width), float64(options.Height)
	w.closeOnCreate, b.closeNext = b.closeNext, false
	b.mu.Lock()
	b.windows[id] = w
	b.mu.Unlock()
	hwnd, createErr := b.api.create(&b.className[0], &title[0], wsOverlappedWindow, 0, b.instance, w.token)
	w.creating = false
	if hwnd == 0 {
		// WM_CREATE returning -1 makes Windows destroy the partial window
		// before CreateWindowExW returns NULL. No native object is forgotten.
		routes.Delete(w.token)
		b.mu.Lock()
		delete(b.windows, id)
		b.mu.Unlock()
		if w.createErr != nil {
			return w.createErr
		}
		if createErr == nil {
			createErr = errors.New("CreateWindowExW failed without an error code")
		}
		return createErr
	}
	if !w.closing && !w.destroyed {
		b.api.show(hwnd)
	}
	return nil
}

func pixels(logical float64, dpi uint32) (int32, error) {
	value := math.Round(logical * float64(dpi) / 96)
	if dpi == 0 || value <= 0 || value > math.MaxInt32 {
		return 0, errors.New("Win32 scaled client size is outside int range")
	}
	return int32(value), nil
}

func (b *Backend) initialize(w *window) error {
	dpi, err := b.api.getDPI(w.hwnd)
	if err != nil {
		return err
	}
	w.dpi = dpi
	width, err := pixels(w.logicalWidth, dpi)
	if err != nil {
		return err
	}
	height, err := pixels(w.logicalHeight, dpi)
	if err != nil {
		return err
	}
	r := rect{right: width, bottom: height}
	if err := b.api.adjustRect(&r, dpi); err != nil {
		return err
	}
	if err := b.api.setPosition(w.hwnd, r, swpNoMove|swpNoZOrder|swpNoActivate); err != nil {
		return err
	}
	if w.createErr != nil {
		return w.createErr
	}
	return b.updateClient(w)
}

func (b *Backend) updateClient(w *window) error {
	var r rect
	if err := b.api.clientRect(w.hwnd, &r); err != nil {
		return err
	}
	w.pixelWidth, w.pixelHeight = r.right-r.left, r.bottom-r.top
	if w.dpi != 0 && w.pixelWidth > 0 && w.pixelHeight > 0 {
		w.logicalWidth, w.logicalHeight = float64(w.pixelWidth)*96/float64(w.dpi), float64(w.pixelHeight)*96/float64(w.dpi)
	}
	return nil
}

// windowMessage is called by the one permanent Windows callback, or by
// injected-call tests. Creation data is copied before numeric token lookup.
func windowMessage(api *native, hwnd, msg, wp, lp uintptr) uintptr {
	var w *window
	if msg == wmNCCreate {
		var creation createStruct
		api.copyMemory(unsafe.Pointer(&creation), lp, unsafe.Sizeof(creation))
		if value, ok := routes.Load(creation.params); ok {
			w = value.(*window)
		}
		if w == nil {
			return 0
		}
		if err := api.setUserData(hwnd, w.token); err != nil {
			w.createErr = err
			return 0
		}
		w.hwnd = hwnd
	} else if value, ok := routes.Load(api.getUserData(hwnd)); ok {
		w = value.(*window)
	}
	if w == nil {
		return api.defWindow(hwnd, msg, wp, lp)
	}
	b := w.backend
	switch msg {
	case wmCreate:
		if !w.dispatch && !w.closing {
			if err := b.initialize(w); err != nil {
				w.createErr = err
				return ^uintptr(0)
			}
			if w.closeOnCreate {
				b.closeRequested(w)
			}
		}
	case wmClose:
		if !w.dispatch {
			b.closeRequested(w)
		}
		return 0
	case wmDestroy:
		return 0 // ordinary window destruction never posts WM_QUIT
	case wmNCDestroy:
		_ = api.setUserData(hwnd, 0)
		if w.dispatch {
			b.mu.Lock()
			w.destroyed, w.hwnd = true, 0
			if !b.closed {
				b.backendErr = errors.New("Win32 application dispatch window destroyed before terminal cleanup")
			}
			b.mu.Unlock()
		} else if !w.destroyed {
			w.destroyed = true
			b.closeRequested(w)
			b.mu.Lock()
			b.retired = append(b.retired, w.token)
			b.mu.Unlock()
		}
	case wmWake:
		if w.dispatch {
			b.mu.Lock()
			if !b.closed {
				b.ready = true
			}
			b.mu.Unlock()
		}
		return 0
	case wmSize:
		if !w.dispatch && !w.closing {
			b.nativeFailure(w, b.updateClient(w))
		}
	case wmDPIChanged:
		if !w.dispatch && !w.closing {
			w.dpi = uint32(wp & 0xffff)
			if w.dpi == 0 {
				b.nativeFailure(w, errors.New("WM_DPICHANGED supplied zero DPI"))
				return 0
			}
			var suggested rect
			api.copyMemory(unsafe.Pointer(&suggested), lp, unsafe.Sizeof(suggested))
			b.nativeFailure(w, api.setPosition(hwnd, suggested, swpNoZOrder|swpNoActivate))
		}
		return 0
	}
	return api.defWindow(hwnd, msg, wp, lp)
}

func (b *Backend) nativeFailure(w *window, err error) {
	if err == nil {
		return
	}
	if w.creating {
		w.createErr = err
		return
	}
	b.mu.Lock()
	if b.backendErr == nil {
		b.backendErr = err
	}
	b.mu.Unlock()
}

func (b *Backend) closeRequested(w *window) {
	if w.closing {
		return
	}
	w.closing = true
	b.sink(platform.Event{Kind: platform.CloseRequested, Window: w.id})
}

// Present is unreachable until Win32 content windows exist: Create refuses them.
func (b *Backend) Present(platform.ID, platform.Image) error { return platform.ErrUnsupportedContent }

func (b *Backend) SetTitle(id platform.ID, title string) error {
	value, err := text(title)
	if err != nil {
		return err
	}
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.destroyed {
		return errors.New("Win32 title target no longer exists")
	}
	return b.api.setTitle(w.hwnd, &value[0])
}

func (b *Backend) Destroy(id platform.ID) error {
	b.mu.Lock()
	w := b.windows[id]
	b.mu.Unlock()
	if w == nil || w.destroyed {
		return nil
	}
	w.closing = true
	return b.api.destroy(w.hwnd)
}

func (b *Backend) Wake() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.application == nil || b.application.hwnd == 0 {
		return errors.New("Win32 dispatch target is closed")
	}
	if !b.pending {
		if err := b.api.postWake(b.application.hwnd); err != nil {
			return err
		}
		b.pending = true
		b.wakes++
	}
	return nil
}

func (b *Backend) takeError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	err := b.backendErr
	b.backendErr = nil
	return err
}

func (b *Backend) deliverReady() bool {
	b.mu.Lock()
	ready := b.ready
	b.mu.Unlock()
	if !ready {
		return false
	}
	// Posted messages outrank paint/timer work in GetMessage. Service bounded
	// native categories before a self-rearming public callback batch.
	for _, filter := range [][2]uint32{{wmKeyFirst, wmKeyLast}, {wmMouseFirst, wmMouseLast}, {wmNCMouseFirst, wmNCMouseLast}, {wmPaint, wmPaint}, {wmTimer, wmTimer}} {
		var m message
		if b.api.peekMessage(&m, 0, filter[0], filter[1]) {
			if m.id == wmQuit {
				b.mu.Lock()
				b.backendErr = errors.New("unexpected WM_QUIT during native servicing")
				b.mu.Unlock()
				break
			}
			b.api.dispatch(&m)
		}
	}
	// Native callbacks may have latched a failure while servicing messages.
	// Let appcore cancel before any further ordinary callback can start.
	b.mu.Lock()
	if b.backendErr != nil {
		b.mu.Unlock()
		return false
	}
	b.ready, b.pending = false, false
	b.dispatches++
	b.mu.Unlock()
	b.sink(platform.Event{Kind: platform.Wake})
	return true
}

func (b *Backend) Pump() error {
	if err := b.takeError(); err != nil {
		return err
	}
	if !b.deliverReady() {
		if err := b.takeError(); err != nil {
			return err
		}
		// Only a broken GetMessage path gets a retry delay; healthy idle
		// blocks natively. Preserve ownership and let diagnostics keep running.
		if b.messageFailed {
			b.pauseRetry()
		}
		var m message
		result, err := b.api.getMessage(&m)
		if result == -1 {
			b.messageFailed = true
			if err == nil {
				err = errors.New("native API supplied no error code")
			}
			err = fmt.Errorf("GetMessageW failed: %w", err)
			b.mu.Lock()
			b.problem = err.Error()
			b.mu.Unlock()
			return err
		}
		b.messageFailed = false
		b.mu.Lock()
		b.problem = ""
		b.mu.Unlock()
		if result == 0 {
			return fmt.Errorf("unexpected WM_QUIT (code %d) before terminal cleanup", m.wparam)
		}
		b.api.dispatch(&m)
		if err := b.takeError(); err != nil {
			return err
		}
		b.deliverReady()
	}
	b.flushRetired()
	return b.takeError()
}

func (b *Backend) flushRetired() {
	b.mu.Lock()
	retired := b.retired
	b.retired = nil
	b.mu.Unlock()
	for _, token := range retired {
		value, ok := routes.Load(token)
		if !ok {
			continue
		} // failed creation already rolled itself back
		w := value.(*window)
		if w.creating {
			b.mu.Lock()
			b.retired = append(b.retired, token)
			b.mu.Unlock()
			continue
		}
		routes.Delete(token)
		b.mu.Lock()
		delete(b.windows, w.id)
		b.mu.Unlock()
		b.sink(platform.Event{Kind: platform.Destroyed, Window: w.id})
	}
}

func (b *Backend) Release() error {
	b.mu.Lock()
	if len(b.windows) != 0 || len(b.retired) != 0 {
		b.mu.Unlock()
		return errors.New("Win32 release with outstanding native windows")
	}
	b.closed = true
	target := b.application.hwnd
	b.mu.Unlock()
	var m message
	for b.api.peekMessage(&m, target, wmWake, wmWake) {
	}
	var releaseErr error
	for target != 0 {
		err := b.api.destroy(target)
		if err == nil {
			b.mu.Lock()
			b.application.hwnd = 0
			b.mu.Unlock()
			break
		}
		if releaseErr == nil {
			releaseErr = err
		}
		b.mu.Lock()
		b.problem = err.Error()
		target = b.application.hwnd
		b.mu.Unlock()
		if target != 0 {
			b.pauseRetry()
		}
	}
	routes.Delete(b.application.token)
	b.mu.Lock()
	b.problem = ""
	b.mu.Unlock()
	return errors.Join(releaseErr, b.api.unregister(&b.className[0], b.instance))
}

func (b *Backend) pauseRetry() {
	if b.waitRetry != nil {
		b.waitRetry()
	} else {
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *Backend) Counters() platform.Counters {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := platform.Counters{Wakes: b.wakes, Dispatches: b.dispatches, Windows: len(b.windows), Retired: len(b.retired), Problem: b.problem}
	if b.application != nil && b.application.hwnd != 0 {
		s.DispatchTargets = 1
	}
	return s
}

func (b *Backend) CloseNextCreation() { b.closeNext = true }

var _ platform.Backend = (*Backend)(nil)
