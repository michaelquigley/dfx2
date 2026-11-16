package webview2

import (
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
