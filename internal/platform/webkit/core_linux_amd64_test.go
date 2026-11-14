package webkit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

// coreFactory builds the real attachment, over the fake native calls, for a
// real core on the fake backend.
type coreFactory struct{ made chan *browser }

func (c coreFactory) New(parent platform.BrowserParent, config platform.BrowserConfig, host platform.BrowserHost) (platform.Browser, error) {
	b, err := newBrowser(parent, config, host)
	if err == nil {
		c.made <- b
	}
	return b, err
}

// A web process that terminates while its window is closing is still a
// browser-process failure, and it reaches Run's result, beside an earlier
// application failure too. Teardown itself cannot raise it: the signal is
// disconnected before native destruction.
func TestTerminationWhileClosingReachesRun(t *testing.T) {
	earlier := errors.New("application task failed first")
	for _, withEarlier := range []bool{false, true} {
		t.Run(map[bool]string{false: "window close", true: "after an application failure"}[withEarlier], func(t *testing.T) {
			native, _ := newTestBrowser(t, &fakeHost{}, platform.BrowserAttach) // fakes, reservation, TMPDIR
			backend := &fake.Backend{}
			factory := coreFactory{made: make(chan *browser, 1)}
			windows := make(chan *appcore.Window, 1)
			fail := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- appcore.RunBackend(context.Background(), appcore.Options{Mode: appcore.WindowApplication, Logf: func(string, ...any) {}},
					func(a *appcore.App) error {
						w, err := appcore.OpenBrowser(a, appcore.WindowOptions{Title: "t", Width: 100, Height: 100}, appcore.BrowserSpec{
							Factory: factory, Config: platform.BrowserConfig{Mode: platform.BrowserAttach, StartURL: "http://127.0.0.1:9/",
								Origins: []string{"http://127.0.0.1:9"}, GCSignal: 60}})
						windows <- w
						if err != nil {
							return err
						}
						return a.Go("worker", func(ctx context.Context) error {
							select {
							case <-fail:
								return earlier
							case <-ctx.Done():
								return nil
							}
						})
					}, backend)
			}()
			ui := func(f func()) { // on the core's UI thread, outside dispatch turns
				finished := make(chan struct{})
				backend.Call(func() { f(); close(finished) })
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("UI call timed out")
				}
			}
			b := <-factory.made
			w := <-windows
			if withEarlier {
				close(fail) // the application fails first; shutdown closes the window
			} else {
				w.Close()
			}
			// The task's failure reaches the core asynchronously; poll the
			// attachment's state on the UI thread until close has begun.
			closing := false
			for deadline := time.Now().Add(5 * time.Second); !closing && time.Now().Before(deadline); {
				ui(func() { closing = b.closing })
				if !closing {
					time.Sleep(time.Millisecond)
				}
			}
			ui(func() {
				if !b.closing || b.destroying {
					t.Error("not closing with its filter callback still owed")
				}
				b.terminated(0) // the web process crashes before destruction
			})
			ui(native.complete) // the canceled filter save returns; destruction is queued
			ui(native.idle)     // destruction, then release
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return")
			}
			if err == nil || !strings.Contains(err.Error(), "web process terminated (reason 0)") {
				t.Fatalf("termination lost from Run: %v", err)
			}
			if withEarlier && !errors.Is(err, earlier) {
				t.Fatalf("earlier failure lost: %v", err)
			}
		})
	}
}
