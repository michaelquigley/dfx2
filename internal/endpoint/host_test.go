package endpoint

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// testHost stands in for the core. The test goroutine plays the UI thread:
// it calls request methods and runs published settlements itself.
type testHost struct {
	mu        sync.Mutex
	next      platform.ID
	pending   map[platform.ID]bool
	published chan publication
	responses map[platform.ID]platform.BrowserResponse
	failures  []error
	drained   chan struct{}
	drains    int
	// failuresAtDrain is the failure count when Drained first arrived.
	failuresAtDrain int
	beginErr        error
	// respond overrides delivery; nil records the response and succeeds.
	respond func(platform.ID, platform.BrowserResponse) bool
}

type publication struct {
	id     platform.ID
	settle func(bool) error
}

func newHost() *testHost {
	return &testHost{pending: make(map[platform.ID]bool), published: make(chan publication, 64),
		responses: make(map[platform.ID]platform.BrowserResponse), drained: make(chan struct{})}
}

func (h *testHost) Begin() (platform.ID, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.beginErr != nil {
		return 0, h.beginErr
	}
	h.next++
	h.pending[h.next] = true
	return h.next, nil
}

func (h *testHost) Publish(id platform.ID, settle func(bool) error) {
	h.mu.Lock()
	ok := h.pending[id]
	delete(h.pending, id)
	h.mu.Unlock()
	if ok {
		h.published <- publication{id, settle}
	}
}

func (h *testHost) Respond(id platform.ID, r platform.BrowserResponse) bool {
	if h.respond != nil {
		return h.respond(id, r)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.responses[id] = r
	return true
}

func (h *testHost) Fail(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures = append(h.failures, err)
}

func (h *testHost) Drained() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drains++
	if h.drains == 1 {
		h.failuresAtDrain = len(h.failures)
		close(h.drained)
	}
}

func (h *testHost) response(id platform.ID) (platform.BrowserResponse, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.responses[id]
	return r, ok
}

func (h *testHost) failed() []error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]error(nil), h.failures...)
}

func (h *testHost) outstanding() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending)
}

// settleNext runs the next published settlement as the UI thread would.
func (h *testHost) settleNext(t *testing.T, closing bool) {
	t.Helper()
	p := receive(t, h.published)
	if err := p.settle(closing); err != nil {
		t.Fatal(err)
	}
}

func (h *testHost) noPublication(t *testing.T) {
	t.Helper()
	select {
	case p := <-h.published:
		t.Fatalf("unexpected publication %d", p.id)
	default:
	}
}

func (h *testHost) isDrained() bool {
	select {
	case <-h.drained:
		return true
	default:
		return false
	}
}

// Deadlines only guard hangs; channels establish every ordering.
func receive[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for test barrier")
		var zero T
		return zero
	}
}

func closed(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for close")
	}
}

var errInjected = errors.New("injected")

func ownerContext(t *testing.T) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, cancel
}
