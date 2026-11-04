package appcore

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

func TestDiagnosticsSurviveDispatchTargetRelease(t *testing.T) {
	clock := newClock()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	logs := make(chan string, 1)
	b := &fake.Backend{BeforeRelease: func() { close(entered); <-release }}
	h := launch(t, Options{Logf: func(f string, args ...any) { logs <- fmt.Sprintf(f, args...) }}, b, clock, func(*App) error { return nil })
	receive(t, entered)
	clock.next(t, diagnosticDelay).fire()
	if message := receive(t, logs); !strings.Contains(message, "release-pending=true") {
		t.Fatal(message)
	}
	resultPending(t, h.done)
	// The wake path has been sealed, while independent diagnostics are live.
	h.app.mu.Lock()
	select {
	case <-h.app.wakesStopped:
	default:
		t.Error("native release began before wake retries stopped")
	}
	h.app.mu.Unlock()
	once.Do(func() { close(release) })
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if Inspect(h.app).ReleasePending {
		t.Fatal("release still reported pending after Run returned")
	}
}
