package win32

import (
	"bytes"
	"errors"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// ErrHostFree is the cause of every free the host fails, as one native error
// code would be.
var ErrHostFree = errors.New("free failed")

// CoreHost runs the modeled system for a real core, from the external test
// package: appcore imports this package on Windows, so tests in this package
// cannot import it back. GetMessage blocks here as it does natively, instead of
// failing on an empty queue, and the armed retry timer fires only on Tick.
type CoreHost struct {
	t                *testing.T
	f                *systemModel
	failing          atomic.Bool
	attempts         atomic.Int64
	posted           chan struct{}
	parked, fireTick chan struct{}
}

func NewCoreHost(t *testing.T) *CoreHost {
	t.Helper()
	h := &CoreHost{t: t, f: unopenedModel(t), posted: make(chan struct{}, 1), parked: make(chan struct{}), fireTick: make(chan struct{})}
	f := h.f
	// Unlike the model's other tests, the core calls from several goroutines
	// and asks which one owns the UI.
	f.api.threadID = goroutineID
	free := f.api.free
	f.api.free = func(pixels []byte) error {
		h.attempts.Add(1)
		if h.failing.Load() {
			return ErrHostFree
		}
		return free(pixels)
	}
	post := f.api.postWake
	f.api.postWake = func(hwnd uintptr) error {
		err := post(hwnd)
		select {
		case h.posted <- struct{}{}:
		default:
		}
		return err
	}
	retrieve := f.api.getMessage
	f.api.getMessage = func(m *message) (int32, error) {
		for {
			f.queueMu.Lock()
			ready := len(f.queue) != 0
			f.queueMu.Unlock()
			if ready {
				return retrieve(m)
			}
			var tick chan struct{}
			var timer uintptr
			for id := range f.timers {
				tick, timer = h.fireTick, id
			}
			select {
			case <-h.posted:
			case h.parked <- struct{}{}:
			case <-tick:
				*m = message{hwnd: f.b.application.hwnd, id: wmTimer, wparam: timer}
				return 1, nil
			}
		}
	}
	return h
}

func goroutineID() uint32 {
	var buf [32]byte
	fields := bytes.Fields(buf[:runtime.Stack(buf[:], false)])
	id, _ := strconv.ParseUint(string(fields[1]), 10, 32)
	return uint32(id)
}

func (h *CoreHost) Backend() platform.Backend { return h.f.b }

func (h *CoreHost) Counters() platform.Counters { return h.f.b.Counters() }

// FailFrees makes every free fail, or succeed again.
func (h *CoreHost) FailFrees(fail bool) { h.failing.Store(fail) }

func (h *CoreHost) FreeAttempts() int { return int(h.attempts.Load()) }

// Await returns once the UI thread is parked in GetMessage, with an empty queue,
// and ready holds.
func (h *CoreHost) Await(what string, ready func() bool) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-h.parked:
			if ready() {
				return
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// Tick fires the armed retry timer and waits for its message to be handled. It
// times out if no timer is armed.
func (h *CoreHost) Tick() {
	h.t.Helper()
	select {
	case h.fireTick <- struct{}{}:
	case <-time.After(5 * time.Second):
		h.t.Fatal("no retry timer armed")
	}
}

// Settle waits until the UI thread has gone back to GetMessage.
func (h *CoreHost) Settle() {
	h.t.Helper()
	select {
	case <-h.parked:
	case <-time.After(5 * time.Second):
		h.t.Fatal("UI thread did not return to GetMessage")
	}
}
