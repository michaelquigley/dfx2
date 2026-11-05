// Package fake provides a controllable backend without native libraries.
package fake

import (
	"errors"
	"sync"

	"github.com/michaelquigley/dfx2/internal/platform"
)

type Window struct {
	Handle   uintptr
	Options  platform.WindowOptions
	Presents int
	Last     platform.Image // a copy, as native retained storage would hold
}

// Backend's hooks and Owner are configured before Open. Hooks execute on the
// test's UI thread; they may Emit events reentrantly. Enqueue and FailWakes are
// safe to call from test control goroutines. The fake deliberately reuses one
// handle: routing must depend on IDs rather than handles.
type Backend struct {
	Owner func() bool
	// BeforeCreate runs at the start of Create, before any refusal, for races
	// against creation that has been admitted but not yet answered.
	BeforeCreate                            func(platform.ID)
	OnCreate                                func(platform.ID)
	OnDestroy                               func(platform.ID)
	BeforePump                              func()
	BeforeRelease                           func()
	AfterEvent                              func(platform.Event)
	OpenError                               error
	PumpErrors                              []error
	CreateError, DestroyError, ReleaseError error
	DelayDestroy                            bool
	// ContentUnsupported mimics a backend without content surfaces. OnPresent
	// runs before a Present copy and may fail it.
	ContentUnsupported bool
	OnPresent          func(platform.ID, platform.Image) error

	mu           sync.Mutex
	sink         func(platform.Event)
	windows      map[platform.ID]Window
	events       []platform.Event
	changed      chan struct{}
	wakeErrors   []error
	wakes, pumps int
	released     bool
	violations   []string
}

func (b *Backend) Open(sink func(platform.Event)) error {
	if b.OpenError != nil {
		return b.OpenError
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sink = sink
	b.windows = make(map[platform.ID]Window)
	b.changed = make(chan struct{}, 1)
	return nil
}

func (b *Backend) IsOwner() bool { return b.Owner == nil || b.Owner() }

func (b *Backend) Create(id platform.ID, options platform.WindowOptions) error {
	if b.BeforeCreate != nil {
		b.BeforeCreate(id)
	}
	if options.Content && b.ContentUnsupported {
		return platform.ErrUnsupportedContent
	}
	b.mu.Lock()
	b.windows[id] = Window{Handle: 1, Options: options}
	b.mu.Unlock()
	if b.OnCreate != nil {
		b.OnCreate(id)
	}
	if b.CreateError != nil {
		b.mu.Lock()
		delete(b.windows, id)
		b.mu.Unlock()
	}
	return b.CreateError
}

func (b *Backend) Destroy(id platform.ID) error {
	if b.OnDestroy != nil {
		b.OnDestroy(id)
	}
	if !b.DelayDestroy {
		b.Emit(platform.Event{Kind: platform.Destroyed, Window: id})
	}
	return b.DestroyError
}

func (b *Backend) SetTitle(id platform.ID, title string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	w, ok := b.windows[id]
	if !ok {
		return errors.New("fake: title on absent window")
	}
	w.Options.Title = title
	b.windows[id] = w
	return nil
}

// Present is the fake presentation sink. Use after teardown, on a window
// without content, or after Release is recorded as a violation and rejected.
func (b *Backend) Present(id platform.ID, image platform.Image) error {
	if err := image.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	w, ok := b.windows[id]
	switch {
	case b.released:
		b.violations = append(b.violations, "present after release")
	case !ok:
		b.violations = append(b.violations, "present after teardown")
	case !w.Options.Content:
		b.violations = append(b.violations, "present on a window without content")
	}
	b.mu.Unlock()
	if b.released || !ok || !w.Options.Content {
		return errors.New("fake: present rejected")
	}
	if b.OnPresent != nil {
		if err := b.OnPresent(id, image); err != nil {
			return err
		}
	}
	copied := image
	copied.Pix = append([]byte(nil), image.Pix...)
	b.mu.Lock()
	defer b.mu.Unlock()
	if w, ok = b.windows[id]; ok {
		w.Presents++
		w.Last = copied
		b.windows[id] = w
	}
	return nil
}

// Violations lists rejected presentation uses, for assertions.
func (b *Backend) Violations() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.violations...)
}

// Window returns a copy of one live fake window.
func (b *Backend) Window(id platform.ID) (Window, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w, ok := b.windows[id]
	return w, ok
}

// Emit delivers on the calling thread, for reentrancy tests. Use Enqueue from
// controllers/workers so that ordinary native delivery happens inside Pump.
func (b *Backend) Emit(e platform.Event) {
	b.mu.Lock()
	if e.Kind == platform.Destroyed {
		delete(b.windows, e.Window)
	}
	sink := b.sink
	b.mu.Unlock()
	sink(e)
	if b.AfterEvent != nil {
		b.AfterEvent(e)
	}
}

func (b *Backend) Enqueue(e platform.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enqueueLocked(e)
}

func (b *Backend) enqueueLocked(e platform.Event) {
	if b.released {
		panic("fake: event posted after Release")
	}
	b.events = append(b.events, e)
	select {
	case b.changed <- struct{}{}:
	default:
	}
}

func (b *Backend) Wake() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wakes++
	if len(b.wakeErrors) > 0 {
		err := b.wakeErrors[0]
		b.wakeErrors = b.wakeErrors[1:]
		if err != nil {
			return err
		}
	}
	b.enqueueLocked(platform.Event{Kind: platform.Wake})
	return nil
}

func (b *Backend) FailWakes(errs ...error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wakeErrors = append(b.wakeErrors, errs...)
}

func (b *Backend) Pump() error {
	if b.BeforePump != nil {
		b.BeforePump()
	}
	b.mu.Lock()
	if len(b.PumpErrors) > 0 {
		err := b.PumpErrors[0]
		b.PumpErrors = b.PumpErrors[1:]
		b.mu.Unlock()
		if err != nil {
			return err
		}
	} else {
		b.mu.Unlock()
	}
	for {
		b.mu.Lock()
		if len(b.events) > 0 {
			e := b.events[0]
			b.events = b.events[1:]
			b.pumps++
			b.mu.Unlock()
			b.Emit(e)
			return nil
		}
		changed := b.changed
		b.mu.Unlock()
		<-changed
	}
}

func (b *Backend) Release() error {
	if b.BeforeRelease != nil {
		b.BeforeRelease()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.windows) != 0 {
		panic("fake: Release with live native windows")
	}
	b.released = true
	b.events = nil
	return b.ReleaseError
}

func (b *Backend) Snapshot() (map[platform.ID]Window, int, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ws := make(map[platform.ID]Window)
	for id, w := range b.windows {
		ws[id] = w
	}
	return ws, b.wakes, b.pumps, b.released
}
