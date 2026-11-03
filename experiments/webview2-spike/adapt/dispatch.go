package adapt

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Logf is where the adapter reports request lifetimes.
type Logf func(format string, args ...any)

// Result is one finished handler call, waiting for its owner thread.
type Result struct {
	ID       uint64
	Response Response
}

// Dispatcher runs a handler for intercepted requests on worker goroutines
// and hands the results back to the one thread that owns the webview.
//
// The contract with that owner thread:
//
//   - Start is called on the owner thread. It copies nothing native; the
//     owner keeps any COM state for the id itself.
//   - When results are ready the dispatcher calls wake, at most once until
//     the owner calls Take. wake must not block (PostMessage, not
//     SendMessage). It is called with the dispatcher's lock held, so once
//     Close returns, wake is never called again: the owner may then destroy
//     whatever wake posts to.
//   - Close stops new work, cancels every running handler's context, and
//     makes results that arrive later be dropped (and logged). It never
//     waits for a worker, so it is safe on the owner thread even though
//     workers would otherwise need that thread.
//   - Wait, off the owner thread or after its loop has ended, waits for the
//     workers to return.
type Dispatcher struct {
	h      http.Handler
	origin *url.URL
	wake   func() bool
	logf   Logf
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu          sync.Mutex
	closed      bool
	wakePending bool
	nextID      uint64
	running     map[uint64]string // id → "METHOD path", for the close log
	done        []Result
	stats       Stats
}

// Stats counts the dispatcher's requests.
type Stats struct {
	Started, Finished, Canceled, Dropped, Refused int
}

// NewDispatcher dispatches to h. Handler contexts derive from parent, so
// canceling parent (the window's lifetime) cancels them too.
func NewDispatcher(parent context.Context, h http.Handler, origin *url.URL, wake func() bool, logf Logf) *Dispatcher {
	ctx, cancel := context.WithCancel(parent)
	return &Dispatcher{h: h, origin: origin, wake: wake, logf: logf, ctx: ctx, cancel: cancel, running: map[uint64]string{}}
}

// Start begins one request and returns its id. ok is false once the
// dispatcher is closed; the request was not started and the caller settles
// it itself.
func (d *Dispatcher) Start(in Request) (id uint64, ok bool) {
	d.mu.Lock()
	if d.closed {
		d.stats.Refused++
		d.mu.Unlock()
		return 0, false
	}
	d.nextID++
	id = d.nextID
	what := in.Method + " " + pathOf(in.URL)
	d.running[id] = what
	d.stats.Started++
	n := len(d.running)
	d.wg.Add(1)
	d.mu.Unlock()

	d.logf("req %d start %s (%d B body, outstanding %d)", id, what, len(in.Body), n)
	go d.work(id, in)
	return id, true
}

func (d *Dispatcher) work(id uint64, in Request) {
	defer d.wg.Done()
	resp := Serve(d.ctx, d.h, d.origin, &in)

	d.mu.Lock()
	delete(d.running, id)
	n := len(d.running)
	if resp.Canceled {
		d.stats.Canceled++
	}
	if d.closed {
		d.stats.Dropped++
		d.mu.Unlock()
		d.logf("req %d handler %s after close: result dropped (outstanding %d)", id, outcome(resp), n)
		return
	}
	d.stats.Finished++
	d.done = append(d.done, Result{ID: id, Response: resp})
	// Logged before the wakeup, so the log never shows a response
	// installed before its handler finished.
	d.logf("req %d handler %s (outstanding %d)", id, outcome(resp), n)
	postFailed := false
	if !d.wakePending {
		// Under the lock: Close can't slip between the check and the post.
		d.wakePending = d.wake()
		postFailed = !d.wakePending
	}
	d.mu.Unlock()
	if postFailed {
		d.logf("req %d: wakeup post failed; result waits for the next one", id)
	}
}

// Take returns the results ready for the owner thread.
func (d *Dispatcher) Take() []Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.done
	d.done = nil
	d.wakePending = false
	return r
}

// Close stops new work and cancels running handlers. It returns the ids
// still running, which the owner settles. It doesn't wait for them.
func (d *Dispatcher) Close() (running []uint64) {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		d.done = nil // never delivered: the owner settles these ids itself
		for id := range d.running {
			running = append(running, id)
		}
	}
	d.mu.Unlock()
	d.cancel()
	return running
}

// Wait waits up to timeout for every worker to return.
func (d *Dispatcher) Wait(timeout time.Duration) bool {
	c := make(chan struct{})
	go func() { d.wg.Wait(); close(c) }()
	select {
	case <-c:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Outstanding counts handlers still running.
func (d *Dispatcher) Outstanding() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.running)
}

// Stats returns the counts so far.
func (d *Dispatcher) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stats
}

func outcome(r Response) string {
	s := "done " + strconv.Itoa(r.Status)
	if r.Note != "" {
		s += " [" + r.Note + "]"
	}
	if r.Canceled {
		s = "canceled (context done), returned " + strconv.Itoa(r.Status)
	}
	if r.Ran {
		s += " in " + r.Elapsed.Round(time.Millisecond).String()
	} else {
		s += " (handler not run)"
	}
	return s
}

func pathOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.RequestURI()
	}
	return raw
}
