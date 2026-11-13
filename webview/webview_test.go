package webview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/endpoint"
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

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

// eventually polls state another goroutine owns; the deadline guards a hang.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func useFactory(t *testing.T, f platform.BrowserFactory) {
	saved := factory
	factory = func() platform.BrowserFactory { return f }
	t.Cleanup(func() { factory = saved })
}

type harness struct {
	b    *fake.Backend
	app  chan *dfx2.App
	done chan error
}

func launch(t *testing.T, mode dfx2.Mode, setup func(*dfx2.App) error) harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := harness{b: &fake.Backend{}, app: make(chan *dfx2.App, 1), done: make(chan error, 1)}
	go func() {
		h.done <- appcore.RunBackend(ctx, dfx2.Options{Mode: mode, Logf: func(string, ...any) {}}, func(a *dfx2.App) error {
			h.app <- a
			return setup(a)
		}, h.b)
	}()
	return h
}

func (h harness) onUI(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	h.b.Call(func() { f(); close(done) })
	receive(t, done)
}

var size = dfx2.WindowOptions{Title: "web", Width: 320, Height: 200}

func TestConfigureRequiresAnExplicitConsistentMode(t *testing.T) {
	h := http.NotFoundHandler()
	for name, o := range map[string]Options{
		"no mode":                {Handler: h},
		"unknown mode":           {Mode: 9, Handler: h},
		"in-process no handler":  {Mode: InProcess},
		"in-process with URL":    {Mode: InProcess, Handler: h, URL: "http://example.com/"},
		"in-process server":      {Mode: InProcess, Handler: h, Server: ServerLimits{Handlers: 2}},
		"in-process bad limits":  {Mode: InProcess, Handler: h, Limits: Limits{Requests: -1}},
		"loopback no handler":    {Mode: Loopback},
		"loopback origins":       {Mode: Loopback, Handler: h, Origins: []string{"http://example.com"}},
		"loopback finite limits": {Mode: Loopback, Handler: h, Limits: Limits{ResponseBody: 10}},
		"attach handler":         {Mode: Attach, Handler: h, URL: "http://example.com/", Origins: []string{"http://example.com"}},
		"attach limits":          {Mode: Attach, URL: "http://example.com/", Origins: []string{"http://example.com"}, Limits: Limits{Requests: 1}},
		"attach no origins":      {Mode: Attach, URL: "http://example.com/"},
		"attach own origin":      {Mode: Attach, URL: "http://example.com/", Origins: []string{"http://other.example"}},
		"attach relative":        {Mode: Attach, URL: "/index.html", Origins: []string{"http://example.com"}},
		"attach scheme":          {Mode: Attach, URL: "file:///etc/passwd", Origins: []string{"file://"}},
		"attach userinfo":        {Mode: Attach, URL: "http://user:pw@example.com/", Origins: []string{"http://example.com"}},
		"attach origin path":     {Mode: Attach, URL: "http://example.com/", Origins: []string{"http://example.com/app"}},
	} {
		if _, _, err := configure(o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	config, e, err := configure(Options{Mode: Attach, URL: "https://Example.com/app?x#y",
		Origins: []string{"https://example.com:443", "https://EXAMPLE.com/", "http://auth.example:8080"}})
	if err != nil || e != nil || config.Mode != platform.BrowserAttach || config.StartURL != "https://Example.com/app?x#y" ||
		strings.Join(config.Origins, ",") != "https://example.com:443,http://auth.example:8080" {
		t.Fatalf("attach config %+v %v %v", config, e, err)
	}
	if _, e, err := configure(Options{Mode: InProcess, Handler: h, DevTools: true}); err != nil || e == nil {
		t.Fatal(err)
	}
}

func TestOpenRefusesWithoutBrowserHostingAndLatchesNothing(t *testing.T) {
	if platformFactory() != nil {
		t.Skip("this platform hosts browsers")
	}
	h := launch(t, dfx2.WindowApplication, func(a *dfx2.App) error {
		w, err := a.NewWindow(size)
		if err != nil {
			return err
		}
		if _, err := Open(a, Options{Window: size, Mode: Loopback, Handler: http.NotFoundHandler()}); !errors.Is(err, ErrUnsupportedContent) {
			t.Errorf("refusal: %v", err)
		}
		w.Close()
		return nil
	})
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(nil, Options{}); err == nil {
		t.Fatal("nil app accepted")
	}
}

func TestInvalidOptionsAreLatchedLikeOpeningFailures(t *testing.T) {
	useFactory(t, &fake.BrowserFactory{})
	h := launch(t, dfx2.ResidentApplication, func(a *dfx2.App) error {
		_, err := Open(a, Options{Window: size, Handler: http.NotFoundHandler()})
		if err == nil {
			t.Error("missing mode accepted")
		}
		return nil
	})
	if err := receive(t, h.done); err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("not latched: %v", err)
	}
}

func TestInProcessServesThroughTheBrowserAndDrainsOnClose(t *testing.T) {
	f := &fake.BrowserFactory{Origin: "fake://w1"}
	useFactory(t, f)
	entered := make(chan *http.Request, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	windows := make(chan *Window, 1)
	h := launch(t, dfx2.WindowApplication, func(a *dfx2.App) error {
		w, err := Open(a, Options{Window: size, Mode: InProcess, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/slow" {
				entered <- r
				<-release
			}
			io.WriteString(w, "snapshot")
		})})
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	var fast, slow platform.RequestDecision
	h.onUI(t, func() {
		if !br.Ready() {
			t.Error("navigation refused")
		}
		fast = br.Request(platform.BrowserRequest{Method: "GET", URL: "fake://w1/api/snapshot"})
		slow = br.Request(platform.BrowserRequest{Method: "GET", URL: "fake://w1/slow"})
	})
	if fast.Action != platform.RequestWait || slow.Action != platform.RequestWait {
		t.Fatalf("decisions %+v %+v", fast, slow)
	}
	eventually(t, "response", func() bool { _, ok := br.Response(fast.ID); return ok })
	if r, _ := br.Response(fast.ID); r.Status != 200 || string(r.Body) != "snapshot" {
		t.Fatalf("response %d %q", r.Status, r.Body)
	}
	r := receive(t, entered)
	w.Close()
	if r.Context().Err() == nil {
		t.Fatal("close did not cancel the handler")
	}
	select {
	case <-w.Done():
		t.Fatal("Done before the handler returned")
	case err := <-h.done:
		t.Fatalf("Run returned before the handler did: %v", err)
	default:
	}
	once.Do(func() { close(release) })
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	receive(t, w.Done())
	if _, ok := br.Response(slow.ID); ok {
		t.Fatal("late result attached to a closed browser")
	}
	if v := br.Violations(); len(v) != 0 {
		t.Fatal(v)
	}
}

func TestOwnedHandlerPanicCancelsAndDrainsTheApplication(t *testing.T) {
	f := &fake.BrowserFactory{Origin: "fake://w1"}
	useFactory(t, f)
	h := launch(t, dfx2.WindowApplication, func(a *dfx2.App) error {
		_, err := Open(a, Options{Window: size, Mode: InProcess, Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("model invariant broken")
		})})
		return err
	})
	br := receive(t, f.Created())
	var d platform.RequestDecision
	h.onUI(t, func() { d = br.Request(platform.BrowserRequest{Method: "POST", URL: "fake://w1/api/edit"}) })
	err := receive(t, h.done)
	if err == nil || !strings.Contains(err.Error(), "model invariant broken") || !strings.Contains(err.Error(), "goroutine") {
		t.Fatalf("Run: %v", err)
	}
	if r, ok := br.Response(d.ID); ok && (r.Status != 500 || strings.Contains(string(r.Body), "invariant")) {
		t.Fatalf("page saw %d %q", r.Status, r.Body)
	}
}

func TestLoopbackWindowOwnsItsServer(t *testing.T) {
	f := &fake.BrowserFactory{}
	useFactory(t, f)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	windows := make(chan *Window, 1)
	h := launch(t, dfx2.WindowApplication, func(a *dfx2.App) error {
		w, err := Open(a, Options{Window: size, Mode: Loopback, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/slow" {
				entered <- struct{}{}
				<-release
			}
			io.WriteString(w, "page")
		})})
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	c := br.Config.Credential
	if br.Config.Mode != platform.BrowserLoopback || c == nil || c.Name != endpoint.CookieName || !strings.HasPrefix(br.Config.StartURL, "http://127.0.0.1:") {
		t.Fatalf("config %+v", br.Config)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	get := func(path string) (*http.Response, error) {
		req, _ := http.NewRequest("GET", br.Config.StartURL+path, nil)
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		return client.Do(req)
	}
	resp, err := get("index.html")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "page" {
		t.Fatalf("page %d %q", resp.StatusCode, body)
	}
	go get("slow")
	receive(t, entered)
	w.Close()
	select {
	case <-w.Done():
		t.Fatal("Done before the loopback handler returned")
	default:
	}
	once.Do(func() { close(release) })
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	if _, err := get("index.html"); err == nil {
		t.Fatal("owned server outlived its window")
	}
}

func TestAttachedServiceOutlivesItsWindow(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "service") }))
	defer service.Close()
	f := &fake.BrowserFactory{}
	useFactory(t, f)
	windows := make(chan *Window, 1)
	h := launch(t, dfx2.WindowApplication, func(a *dfx2.App) error {
		w, err := Open(a, Options{Window: size, Mode: Attach, URL: service.URL + "/", Origins: []string{service.URL}})
		windows <- w
		return err
	})
	w := receive(t, windows)
	br := receive(t, f.Created())
	if br.Config.Mode != platform.BrowserAttach || br.Config.Credential != nil || br.Config.StartURL != service.URL+"/" || len(br.Config.Origins) != 1 {
		t.Fatalf("config %+v", br.Config)
	}
	w.Close()
	if err := receive(t, h.done); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(service.URL)
	if err != nil {
		t.Fatalf("attached service stopped with its window: %v", err)
	}
	resp.Body.Close()
}
