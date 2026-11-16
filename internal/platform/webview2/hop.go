package webview2

import (
	"sync"
	"time"
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

// post retains ownership even when the native wake fails. Installation happens
// before any browser can post, and the queue lives for the process.
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
