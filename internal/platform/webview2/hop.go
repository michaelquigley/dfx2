package webview2

import (
	"sync"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

const hopRetryDelay = 10 * time.Millisecond

// callbackQueue carries owned cleanup to the UI thread. wake posts a native
// message; after schedules a retry independently of that message loop.
// Neither may invoke its callback synchronously.
type callbackQueue struct {
	mu           sync.Mutex
	posted       []func()
	wake         func() bool
	after        func(time.Duration, func())
	retryPending bool
}

// post retains ownership even when the native wake fails.
func (q *callbackQueue) post(fn func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.posted = append(q.posted, fn)
	if len(q.posted) == 1 && !q.retryPending {
		q.wakeLocked()
	}
}

func (q *callbackQueue) wakeLocked() {
	if !q.wake() {
		q.retryPending = true
		q.after(hopRetryDelay, q.retry)
	}
}

// retry only posts a native message. It never runs UI work on the timer thread.
// A single pending timer also paces new posts; an already drained queue needs
// no wake and does not rearm it.
func (q *callbackQueue) retry() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.retryPending = false
	if len(q.posted) != 0 {
		q.wakeLocked()
	}
}

// dispatch is called only by the native message handler, on the UI thread.
// Work posted by a callback belongs to a subsequent dispatch.
func (q *callbackQueue) dispatch() {
	q.mu.Lock()
	fns := q.posted
	q.posted = nil
	q.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// dispatchWindow is shared by the live attachments on the UI thread. Each
// successful open creates a separate queue. A retry captures that queue and
// its original HWND; after its producers drain, retry finds no work to wake.
type dispatchWindow struct {
	register   func() error
	create     func() (uintptr, error)
	destroy    func(uintptr) error
	unregister func() error
	wake       func(uintptr) bool
	after      func(time.Duration, func())

	hwnd  uintptr
	queue *callbackQueue
}

func (h *dispatchWindow) open(host platform.BrowserHost) error {
	if err := h.register(); err != nil {
		return err
	}
	hwnd, err := h.create()
	if err != nil {
		h.releaseClass(host)
		return err
	}
	h.hwnd = hwnd
	h.queue = &callbackQueue{wake: func() bool { return h.wake(hwnd) }, after: h.after}
	return nil
}

func (h *dispatchWindow) close(host platform.BrowserHost, finished func()) {
	// DestroyWindow must run on the owning thread. On failure keep the
	// attachment pending and post the next attempt through the live window.
	q := h.queue
	reported := false
	var attempt func()
	attempt = func() {
		if err := h.destroy(h.hwnd); err != nil {
			if !reported {
				host.Failed(err)
				reported = true
			}
			h.after(hopRetryDelay, func() { q.post(attempt) })
			return
		}
		h.hwnd, h.queue = 0, nil
		h.releaseClass(host)
		finished()
	}
	attempt()
}

func (h *dispatchWindow) releaseClass(host platform.BrowserHost) {
	if err := h.unregister(); err != nil {
		// The HWND is gone (or was never created). A class belongs to
		// the process. Keep failed unregistration tracked through Run.
		host.Failed(err)
		if err := host.Cleanup("WebView2 dispatch window class", func() error {
			for {
				time.Sleep(hopRetryDelay)
				if h.unregister() == nil {
					return nil
				}
			}
		}); err != nil {
			host.Failed(err)
		}
	}
}
