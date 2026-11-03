package adapt

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// owner stands in for the UI thread: wake only signals, and results are
// taken where the test chooses.
type owner struct {
	wakes atomic.Int32
	ch    chan struct{}
}

func newOwner() *owner { return &owner{ch: make(chan struct{}, 100)} }

func (o *owner) wake() bool {
	o.wakes.Add(1)
	o.ch <- struct{}{}
	return true
}

func (o *owner) waitResult(t *testing.T, d *Dispatcher) []Result {
	t.Helper()
	select {
	case <-o.ch:
		return d.Take()
	case <-time.After(5 * time.Second):
		t.Fatal("no wakeup")
		return nil
	}
}

func slowFast(release <-chan struct{}, sawCancel *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-release:
			case <-r.Context().Done():
				sawCancel.Store(true)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.Write([]byte(r.URL.Path))
	})
}

func TestFastCompletesBeforeSlow(t *testing.T) {
	release := make(chan struct{})
	var canceled atomic.Bool
	o := newOwner()
	d := NewDispatcher(context.Background(), slowFast(release, &canceled), origin, o.wake, t.Logf)
	slowID, _ := d.Start(Request{Method: "GET", URL: "https://dfx2-spike.invalid/slow"})
	fastID, _ := d.Start(Request{Method: "GET", URL: "https://dfx2-spike.invalid/fast"})
	r := o.waitResult(t, d)
	if len(r) != 1 || r[0].ID != fastID || string(r[0].Response.Body) != "/fast" {
		t.Fatalf("first result %+v, want fast %d", r, fastID)
	}
	close(release)
	r = o.waitResult(t, d)
	if len(r) != 1 || r[0].ID != slowID || r[0].Response.Canceled {
		t.Fatalf("second result %+v", r)
	}
	if !d.Wait(time.Second) || d.Outstanding() != 0 {
		t.Fatal("workers not drained")
	}
}

// One wakeup covers every result that lands before the owner takes them.
func TestWakeCoalesces(t *testing.T) {
	var wakes atomic.Int32
	d := NewDispatcher(context.Background(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), origin,
		func() bool { wakes.Add(1); return true }, t.Logf)
	for i := 0; i < 20; i++ {
		d.Start(Request{URL: "https://dfx2-spike.invalid/"})
	}
	d.Wait(time.Second)
	if n := len(d.Take()); n != 20 {
		t.Fatalf("took %d", n)
	}
	if w := wakes.Load(); w != 1 {
		t.Fatalf("%d wakeups for 20 results not yet taken", w)
	}
}

// Close cancels running handlers, refuses new work, never calls wake again,
// and drops results that arrive after it. The owner never waits on a worker.
func TestCloseCancelsAndStopsPosting(t *testing.T) {
	release := make(chan struct{})
	var canceled atomic.Bool
	var mu sync.Mutex
	closed := false
	var postsAfterClose atomic.Int32
	wake := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			postsAfterClose.Add(1)
		}
		return true
	}
	d := NewDispatcher(context.Background(), slowFast(release, &canceled), origin, wake, t.Logf)
	id, _ := d.Start(Request{URL: "https://dfx2-spike.invalid/slow"})

	start := time.Now()
	running := d.Close()
	mu.Lock()
	closed = true
	mu.Unlock()
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Close waited")
	}
	if len(running) != 1 || running[0] != id {
		t.Fatalf("running at close %v, want [%d]", running, id)
	}
	if _, ok := d.Start(Request{URL: "https://dfx2-spike.invalid/fast"}); ok {
		t.Fatal("Start after Close accepted")
	}
	if !d.Wait(2 * time.Second) {
		t.Fatal("worker did not return after cancellation")
	}
	if !canceled.Load() {
		t.Fatal("handler never saw cancellation")
	}
	if postsAfterClose.Load() != 0 {
		t.Fatal("wake called after Close")
	}
	if r := d.Take(); len(r) != 0 {
		t.Fatalf("result delivered after Close: %+v", r)
	}
	s := d.Stats()
	if s.Started != 1 || s.Canceled != 1 || s.Dropped != 1 || s.Refused != 1 || s.Finished != 0 {
		t.Fatalf("stats %+v", s)
	}
}

// Canceling the parent (the window's lifetime) reaches handlers too.
func TestParentContextCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var canceled atomic.Bool
	o := newOwner()
	d := NewDispatcher(ctx, slowFast(release, &canceled), origin, o.wake, t.Logf)
	d.Start(Request{URL: "https://dfx2-spike.invalid/slow"})
	cancel()
	r := o.waitResult(t, d)
	if len(r) != 1 || !r[0].Response.Canceled || r[0].Response.Status != 503 || !canceled.Load() {
		t.Fatalf("%+v", r)
	}
}

// A failed wakeup leaves the result queued; the next completion retries.
func TestFailedWakeRetries(t *testing.T) {
	var calls atomic.Int32
	d := NewDispatcher(context.Background(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), origin,
		func() bool { return calls.Add(1) > 1 }, t.Logf)
	d.Start(Request{URL: "https://dfx2-spike.invalid/a"})
	d.Wait(time.Second)
	d.Start(Request{URL: "https://dfx2-spike.invalid/b"})
	d.Wait(time.Second)
	if calls.Load() != 2 {
		t.Fatalf("wake calls %d", calls.Load())
	}
	if n := len(d.Take()); n != 2 {
		t.Fatalf("took %d", n)
	}
}
