package webview2

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

type hopClock struct {
	t        *testing.T
	q        *callbackQueue
	succeeds bool
	attempts int
	timers   []func()
}

func newHopClock(t *testing.T) *hopClock {
	c := &hopClock{t: t}
	c.q = &callbackQueue{
		wake: func() bool { c.attempts++; return c.succeeds },
		after: func(d time.Duration, fn func()) {
			if d != 10*time.Millisecond {
				t.Fatalf("retry delay = %v", d)
			}
			c.timers = append(c.timers, fn)
		},
	}
	return c
}
func (c *hopClock) tick() {
	c.t.Helper()
	if len(c.timers) != 1 {
		c.t.Fatalf("retry timers = %d, want 1", len(c.timers))
	}
	fn := c.timers[0]
	c.timers = nil
	fn()
}

func TestHopRetainsWorkAndRetriesWithoutAnotherEvent(t *testing.T) {
	c := newHopClock(t)
	calls := 0
	c.q.post(func() { calls++ })
	if calls != 0 {
		t.Fatal("callback ran inline")
	}
	c.succeeds = true
	c.tick()
	if calls != 0 {
		t.Fatal("timer executed UI work")
	}
	c.q.dispatch()
	c.q.dispatch()
	if calls != 1 || c.attempts != 2 || len(c.timers) != 0 {
		t.Fatalf("calls=%d wakes=%d timers=%d", calls, c.attempts, len(c.timers))
	}
}

func TestHopPersistentFailureIsPaced(t *testing.T) {
	c := newHopClock(t)
	calls, posted := 0, 0
	post := func() { posted++; c.q.post(func() { calls++ }) }
	post()
	for i := 0; i < 20; i++ {
		for j := 0; j < 10; j++ {
			post()
		}
		if c.attempts != i+1 {
			t.Fatalf("new work accelerated retries: %d", c.attempts)
		}
		c.tick()
		if calls != 0 {
			t.Fatal("failed wake executed work")
		}
	}
	c.succeeds = true
	c.tick()
	c.q.dispatch()
	c.q.dispatch()
	if calls != posted || len(c.timers) != 0 {
		t.Fatalf("calls=%d want=%d timers=%d", calls, posted, len(c.timers))
	}
}

func TestHopStaleRetryStopsAfterDrain(t *testing.T) {
	c := newHopClock(t)
	calls := 0
	c.q.post(func() { calls++ })
	c.q.dispatch() // An already queued native message can drain the work.
	c.tick()
	if calls != 1 || c.attempts != 1 || len(c.timers) != 0 {
		t.Fatalf("calls=%d wakes=%d timers=%d", calls, c.attempts, len(c.timers))
	}
}

func TestHopCallbacksPostForNextDispatch(t *testing.T) {
	c := newHopClock(t)
	c.succeeds = true
	calls := 0
	c.q.post(func() { calls++; c.q.post(func() { calls++ }) })
	c.q.dispatch()
	if calls != 1 {
		t.Fatalf("first dispatch calls=%d", calls)
	}
	c.q.dispatch()
	if calls != 2 || len(c.timers) != 0 {
		t.Fatalf("calls=%d timers=%d", calls, len(c.timers))
	}
}

func TestHopConcurrentPostsRunOnce(t *testing.T) {
	c := newHopClock(t)
	c.succeeds = true
	var wg sync.WaitGroup
	calls := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.q.post(func() { calls++ }) }()
	}
	wg.Wait()
	c.q.dispatch()
	c.q.dispatch()
	if calls != 100 || c.attempts != 1 {
		t.Fatalf("calls=%d wakes=%d", calls, c.attempts)
	}
}

func TestHopRetainsDroppedCompletion(t *testing.T) {
	installFake(t)
	c := newHopClock(t)
	n.post = c.q.post
	h := &fakeHost{}
	b := &browser{host: h, closing: true, envPending: true}
	settled := 0
	b.dropped("environment", func() { b.envPending = false; settled++ })
	if !b.envPending || h.released != 0 {
		t.Fatal("completion settled inline")
	}
	c.succeeds = true
	c.tick()
	c.q.dispatch()
	if settled != 1 || b.envPending || h.released != 0 {
		t.Fatalf("settled=%d pending=%v released=%d", settled, b.envPending, h.released)
	}
	c.q.dispatch()
	c.q.dispatch()
	if settled != 1 || h.released != 1 {
		t.Fatalf("settled=%d released=%d", settled, h.released)
	}
}

func TestHopRetainsResponseStreamRelease(t *testing.T) {
	f := installFake(t)
	c := newHopClock(t)
	n.post = c.q.post
	h := &fakeHost{}
	b := &browser{host: h, env: 100}
	b.deliver(42, 1, 0, platform.BrowserResponse{Status: 200, Body: []byte("body")})
	f.freeStream()
	if b.streams != 1 || len(h.releasedR) != 0 {
		t.Fatal("release ran inline")
	}
	c.succeeds = true
	c.tick()
	c.q.dispatch()
	c.q.dispatch()
	if b.streams != 0 || len(h.releasedR) != 1 || h.releasedR[0] != 42 {
		t.Fatalf("streams=%d released=%v", b.streams, h.releasedR)
	}
}

func TestHopTeardownStaysDeferredAfterFailedWake(t *testing.T) {
	installFake(t)
	c := newHopClock(t)
	n.post = c.q.post
	h := &fakeHost{}
	b := &browser{host: h, closing: true}
	b.maybeRelease()
	if h.released != 0 {
		t.Fatal("teardown ran inline after failed wake")
	}
	c.succeeds = true
	c.tick()
	c.q.dispatch()
	c.q.dispatch()
	if h.released != 1 {
		t.Fatalf("released=%d", h.released)
	}
}

func TestDispatchWindowSharedUntilLastReleaseAndRecreated(t *testing.T) {
	installFake(t)
	var calls []string
	var retry func()
	wakeOK := true
	const handle = 73 // Windows can reuse the same value after destruction.
	h := dispatchWindow{
		register: func() error { calls = append(calls, "register"); return nil },
		create:   func() (uintptr, error) { calls = append(calls, "create"); return handle, nil },
		destroy: func(hwnd uintptr) error {
			if hwnd != handle {
				t.Fatalf("destroy HWND %d", hwnd)
			}
			calls = append(calls, "destroy")
			return nil
		},
		unregister: func() error { calls = append(calls, "unregister"); return nil },
		wake: func(hwnd uintptr) bool {
			if hwnd != handle {
				t.Fatalf("wake HWND %d", hwnd)
			}
			calls = append(calls, "wake")
			return wakeOK
		},
		after: func(_ time.Duration, fn func()) { retry = fn },
	}
	n.openHop, n.closeHop = h.open, h.close
	n.post = func(fn func()) { h.queue.post(fn) }
	open := func(host *fakeHost) *browser {
		t.Helper()
		b, err := newBrowser(platform.BrowserParent{Handle: 1}, platform.BrowserConfig{Mode: platform.BrowserInProcess}, host)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first, second := &fakeHost{}, &fakeHost{}
	a, b := open(first), open(second)
	a.Close()
	h.queue.dispatch()
	if first.released != 1 || !slices.Equal(calls, []string{"register", "create", "wake"}) {
		t.Fatalf("first close: releases=%d native calls=%v", first.released, calls)
	}

	// The other attachment can still dispatch its final callback. Let an
	// already queued native message beat the retry for its failed wake.
	wakeOK = false
	b.Close()
	if retry == nil {
		t.Fatal("failed wake did not schedule a retry")
	}
	h.queue.dispatch()
	if second.released != 1 || h.hwnd != 0 || h.queue != nil ||
		!slices.Equal(calls, []string{"register", "create", "wake", "wake", "destroy", "unregister"}) {
		t.Fatalf("last close: releases=%d HWND=%d native calls=%v", second.released, h.hwnd, calls)
	}

	wakeOK = true
	calls = nil
	third := &fakeHost{}
	open(third).Close()
	retry() // old queue, with work pending on the reused HWND's new queue
	h.queue.dispatch()
	if third.released != 1 || !slices.Equal(calls, []string{"register", "create", "wake", "destroy", "unregister"}) {
		t.Fatalf("resident reopen: releases=%d native calls=%v", third.released, calls)
	}
}

func TestDispatchWindowCreateFailureUnregistersClass(t *testing.T) {
	f := installFake(t)
	createErr := errors.New("window creation failed")
	var calls []string
	h := dispatchWindow{
		register:   func() error { calls = append(calls, "register"); return nil },
		create:     func() (uintptr, error) { calls = append(calls, "create"); return 0, createErr },
		unregister: func() error { calls = append(calls, "unregister"); return nil },
	}
	n.openHop = h.open
	host := &fakeHost{}
	b, err := newBrowser(platform.BrowserParent{Handle: 1}, platform.BrowserConfig{Mode: platform.BrowserInProcess}, host)
	if b != nil || !errors.Is(err, createErr) || !slices.Equal(calls, []string{"register", "create", "unregister"}) {
		t.Fatalf("browser=%v error=%v native calls=%v", b, err, calls)
	}
	if loader.users != 0 || f.freed != 1 || len(host.cleanups) != 2 || h.queue != nil {
		t.Fatalf("users=%d unloaded=%d cleanups=%v queue=%p", loader.users, f.freed, host.cleanups, h.queue)
	}
}

func TestDispatchWindowCleanupFailuresRetainOwnership(t *testing.T) {
	var retry func()
	host := &fakeHost{}
	destroys, unregisters := 0, 0
	destroyErr, unregisterErr := errors.New("destroy"), errors.New("unregister")
	h := dispatchWindow{
		hwnd: 7, queue: &callbackQueue{wake: func() bool { return true }},
		destroy: func(uintptr) error {
			destroys++
			if destroys == 1 {
				return destroyErr
			}
			return nil
		},
		unregister: func() error {
			unregisters++
			if unregisters == 1 {
				return unregisterErr
			}
			return nil
		},
		after: func(d time.Duration, fn func()) {
			if d != hopRetryDelay {
				t.Fatalf("delay %v", d)
			}
			retry = fn
		},
	}
	h.close(host, host.Released)
	if h.hwnd != 7 || retry == nil || unregisters != 0 || host.released != 0 || !slices.Equal(host.failed, []error{destroyErr}) {
		t.Fatalf("failed destroy: HWND=%d unregisters=%d released=%d errors=%v", h.hwnd, unregisters, host.released, host.failed)
	}
	retry()
	if destroys != 1 {
		t.Fatal("retry ran native cleanup outside UI dispatch")
	}
	h.queue.dispatch()
	if destroys != 2 || unregisters != 2 || host.released != 1 || len(host.cleanups) != 1 ||
		!slices.Equal(host.failed, []error{destroyErr, unregisterErr}) {
		t.Fatalf("retry: destroys=%d unregisters=%d released=%d cleanups=%v errors=%v", destroys, unregisters, host.released, host.cleanups, host.failed)
	}
}
