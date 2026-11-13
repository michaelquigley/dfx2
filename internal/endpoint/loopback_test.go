package endpoint

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/michaelquigley/dfx2/internal/platform"
)

type loopbackFixture struct {
	l      *Loopback
	host   *testHost
	config platform.BrowserConfig
	cancel context.CancelFunc
	client *http.Client
}

func newLoopback(t *testing.T, handler http.Handler, limits ServerLimits) loopbackFixture {
	t.Helper()
	l, err := NewLoopback(handler, limits)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := ownerContext(t)
	host := newHost()
	config := platform.BrowserConfig{Mode: platform.BrowserLoopback}
	if err := l.Start(ctx, host, &config); err != nil {
		t.Fatal(err)
	}
	x := loopbackFixture{l, host, config, cancel, &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	t.Cleanup(func() {
		x.cancel()
		x.l.mu.Lock()
		closed := x.l.closed
		x.l.mu.Unlock()
		if !closed {
			x.l.Close()
		}
	})
	return x
}

// fire sends a credentialed GET from a background goroutine, ignoring its
// outcome.
func (x loopbackFixture) fire(path string) {
	if resp, err := x.do(nil, "GET", path, nil); err == nil {
		resp.Body.Close()
	}
}

// do sends a request with the credential unless the caller changes it.
func (x loopbackFixture) do(_ *testing.T, method, path string, edit func(*http.Request)) (*http.Response, error) {
	req, err := http.NewRequest(method, x.config.StartURL+strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: x.config.Credential.Name, Value: x.config.Credential.Value})
	if edit != nil {
		edit(req)
	}
	return x.client.Do(req)
}

func (x loopbackFixture) status(t *testing.T, method, path string, edit func(*http.Request)) (int, string, http.Header) {
	t.Helper()
	resp, err := x.do(t, method, path, edit)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func TestLoopbackStartsOnLoopbackWithAnInstalledCredential(t *testing.T) {
	x := newLoopback(t, http.NotFoundHandler(), ServerLimits{})
	c := x.config.Credential
	if !strings.HasPrefix(x.config.StartURL, "http://127.0.0.1:") || x.config.Origins[0]+"/" != x.config.StartURL ||
		c == nil || c.Name != CookieName || c.Origin != x.config.Origins[0] || len(c.Value) < 40 {
		t.Fatalf("config %+v", x.config)
	}
	if strings.Contains(x.l.Status(), c.Value) || strings.Contains(x.config.StartURL, c.Value) {
		t.Fatal("credential leaked into status or URL")
	}
	other := newLoopback(t, http.NotFoundHandler(), ServerLimits{})
	if other.config.Credential.Value == c.Value || other.config.StartURL == x.config.StartURL {
		t.Fatal("endpoints share a credential or origin")
	}
}

func TestLoopbackAccessLayerGuardsTheHandler(t *testing.T) {
	var calls atomic.Int32
	x := newLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Del("Content-Security-Policy")
		w.Header().Set("Content-Security-Policy", "default-src *")
		io.WriteString(w, "app")
	}), ServerLimits{})
	o := x.config.Origins[0]
	code, body, header := x.status(t, "GET", "/", nil)
	if code != 200 || body != "app" || calls.Load() != 1 {
		t.Fatalf("authorized: %d %q", code, body)
	}
	policies := header.Values("Content-Security-Policy")
	if len(policies) != 2 || policies[1] != ContentSecurityPolicy {
		t.Fatalf("framework policy removable by the handler: %q", policies)
	}
	if code, _, _ := x.status(t, "POST", "/", func(r *http.Request) { r.Header.Set("Origin", o) }); code != 200 {
		t.Fatalf("same-origin POST: %d", code)
	}
	for name, edit := range map[string]func(*http.Request){
		"no cookie":    func(r *http.Request) { r.Header.Del("Cookie") },
		"wrong cookie": func(r *http.Request) { r.Header.Set("Cookie", CookieName+"=guess") },
		"other host":   func(r *http.Request) { r.Host = "localhost" + strings.TrimPrefix(o, "http://127.0.0.1") },
		"rebound host": func(r *http.Request) { r.Host = "attacker.test" },
		"cross origin": func(r *http.Request) { r.Header.Set("Origin", "http://evil.test") },
		"null origin":  func(r *http.Request) { r.Header.Set("Origin", "null") },
		"other port":   func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:1") },
		"two origins":  func(r *http.Request) { r.Header["Origin"] = []string{o, o} },
	} {
		before := calls.Load()
		code, body, header := x.status(t, "POST", "/", edit)
		if code != 403 || calls.Load() != before || body != failureBody(403) || header.Get("Content-Security-Policy") != ContentSecurityPolicy {
			t.Errorf("%s: %d %q reached handler=%t", name, code, body, calls.Load() != before)
		}
	}
	before := calls.Load()
	if code, _, _ := x.status(t, "GET", "/", func(r *http.Request) {
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
	}); code != 501 || calls.Load() != before {
		t.Fatalf("upgrade reached the handler or was not refused: %d", code)
	}
	if strings.Contains(x.l.Status(), x.config.Credential.Value) {
		t.Fatal("credential in status")
	}
}

func TestLoopbackImplicitResponseKeepsFrameworkPolicy(t *testing.T) {
	for _, supplied := range []string{"", "default-src *"} {
		t.Run(fmt.Sprintf("policy=%q", supplied), func(t *testing.T) {
			x := newLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Handler", "header-only")
				if supplied != "" {
					w.Header().Set("Content-Security-Policy", supplied)
				}
				// Returning without WriteHeader, Write or Flush lets net/http
				// supply the final 200. Exercise that real server path.
			}), ServerLimits{})
			code, body, header := x.status(t, "GET", "/", nil)
			if code != http.StatusOK || body != "" || header.Get("X-Handler") != "header-only" {
				t.Fatalf("implicit response changed: %d %q %v", code, body, header)
			}
			policies := header.Values("Content-Security-Policy")
			want := 1
			if supplied != "" {
				want++
			}
			if len(policies) != want || policies[len(policies)-1] != ContentSecurityPolicy {
				t.Fatalf("implicit response lacks framework policy: %q", policies)
			}
			if supplied != "" && policies[0] != supplied {
				t.Fatalf("handler policy lost: %q", policies)
			}
		})
	}
}

func TestLoopbackSwitchingProtocolsCommitsFrameworkPolicy(t *testing.T) {
	x := newLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}), ServerLimits{})
	resp, err := x.do(t, "GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	policies := resp.Header.Values("Content-Security-Policy")
	if len(policies) != 2 || policies[0] != "default-src *" || policies[1] != ContentSecurityPolicy {
		t.Fatalf("final 101 response lacks framework policy: %q", policies)
	}
}

func TestLoopbackProtocolRejectionsStayOutsideApplicationResponses(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		status       int
	}{
		{"malformed", "Bad Header: invalid\r\n", http.StatusBadRequest},
		{"oversized", "X-Large: " + strings.Repeat("x", 8192) + "\r\n", http.StatusRequestHeaderFieldsTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			x := newLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
			}), ServerLimits{HeaderBytes: 1024})
			addr := strings.TrimPrefix(x.config.Origins[0], "http://")
			c, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			// Supply a valid credential: it is the HTTP parser, not the access
			// layer, that must reject these requests before the handler runs.
			_, err = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nCookie: %s=%s\r\n%s\r\n",
				addr, x.config.Credential.Name, x.config.Credential.Value, tc.header)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "GET"})
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
			resp.Body.Close()
			// net/http may reset a connection with unread oversized input.
			// Accept that only after receiving the exact fixed error body.
			if err != nil && !errors.Is(err, syscall.ECONNRESET) {
				t.Fatal(err)
			}
			wantBody := fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status))
			if resp.StatusCode != tc.status || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !strings.HasPrefix(string(body), wantBody) || len(body) > 1024 || !resp.Close || (err != nil && string(body) != wantBody) {
				t.Fatalf("unexpected protocol rejection: %d %v %q", resp.StatusCode, resp.Header, body)
			}
			if calls.Load() != 0 || strings.Contains(string(body), x.config.Credential.Value) {
				t.Fatal("protocol rejection reached the handler or exposed the credential")
			}
			// The exception must not spread to ordinary framework responses.
			code, _, header := x.status(t, "GET", "/", nil)
			if code != http.StatusOK || calls.Load() != 1 || header.Get("Content-Security-Policy") != ContentSecurityPolicy {
				t.Fatalf("ordinary response lost policy: %d %v", code, header)
			}
		})
	}
}

func TestLoopbackHandlerAdmissionRefusesAndRecovers(t *testing.T) {
	h := newHeld(t)
	x := newLoopback(t, h, ServerLimits{Handlers: 1})
	first := make(chan int, 1)
	go func() {
		resp, err := x.do(t, "GET", "/a", nil)
		if err != nil {
			first <- -1
			return
		}
		resp.Body.Close()
		first <- resp.StatusCode
	}()
	receive(t, h.entered)
	if code, body, _ := x.status(t, "GET", "/b", nil); code != 503 || body != failureBody(503) {
		t.Fatalf("handler overload: %d", code)
	}
	h.free()
	if receive(t, first) != 200 {
		t.Fatal("held request failed")
	}
	if code, _, _ := x.status(t, "GET", "/c", nil); code != 200 {
		t.Fatalf("recovery: %d", code)
	}
}

func TestLoopbackClosesConnectionsBeyondItsLimit(t *testing.T) {
	x := newLoopback(t, http.NotFoundHandler(), ServerLimits{Connections: 1})
	addr := strings.TrimPrefix(x.config.Origins[0], "http://")
	idle, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	// The first connection is tracked once accepted; wait for that.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(x.l.Status(), "connections=1/1") {
		if time.Now().After(deadline) {
			t.Fatal("first connection never accepted")
		}
		time.Sleep(time.Millisecond)
	}
	excess, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer excess.Close()
	excess.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := excess.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("excess connection served or left open: %v", err)
	}
	if !strings.Contains(x.l.Status(), "refused-connections=1") {
		t.Fatal(x.l.Status())
	}
}

func TestLoopbackStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	interfaces := make(chan string, 1)
	x := newLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hijacker := w.(http.Hijacker)
		_, unwrapper := w.(interface{ Unwrap() http.ResponseWriter })
		interfaces <- fmt.Sprintf("hijacker=%t unwrap=%t", hijacker, unwrapper)
		io.WriteString(w, "first\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			panic(err)
		}
		<-release
		io.WriteString(w, "second\n")
	}), ServerLimits{})
	resp, err := x.do(t, "GET", "/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := receive(t, interfaces); got != "hijacker=false unwrap=false" {
		t.Fatal(got)
	}
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "first\n" {
		t.Fatalf("first chunk before the handler finished: %q %v", line, err)
	}
	if resp.Header.Get("Content-Security-Policy") != ContentSecurityPolicy {
		t.Fatal("streamed response lacks the policy")
	}
	close(release)
	if line, _ := reader.ReadString('\n'); line != "second\n" {
		t.Fatalf("second chunk %q", line)
	}
}

func TestLoopbackCloseCancelsAndDrainsOnlyAfterHandlersReturn(t *testing.T) {
	stubborn := newHeld(t)
	canceled := make(chan error, 1)
	x := newLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/polite" {
			<-r.Context().Done()
			canceled <- r.Context().Err()
			return
		}
		stubborn.ServeHTTP(w, r)
	}), ServerLimits{})
	go x.fire("/polite")
	go x.fire("/stubborn")
	receive(t, stubborn.entered)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(x.l.Status(), "handlers=2/") {
		if time.Now().After(deadline) {
			t.Fatal("polite handler never entered")
		}
		time.Sleep(time.Millisecond)
	}
	x.cancel()
	x.l.Close()
	if receive(t, canceled) == nil {
		t.Fatal("handler context not canceled")
	}
	if _, err := x.do(t, "GET", "/", nil); err == nil {
		t.Fatal("closed endpoint still accepts requests")
	}
	if x.host.isDrained() {
		t.Fatal("drained while a handler ignoring cancellation still runs")
	}
	if !strings.Contains(x.l.Status(), "handlers=1/") {
		t.Fatalf("stuck handler not counted: %s", x.l.Status())
	}
	stubborn.free()
	closed(t, x.host.drained)
	if len(x.host.failed()) != 0 {
		t.Fatalf("ordinary close reported failures: %v", x.host.failed())
	}
}

func TestLoopbackClosingOneEndpointLeavesAnotherRunning(t *testing.T) {
	hb := newHeld(t)
	a := newLoopback(t, http.NotFoundHandler(), ServerLimits{})
	b := newLoopback(t, hb, ServerLimits{})
	result := make(chan int, 1)
	go func() {
		resp, err := b.do(t, "GET", "/", nil)
		if err != nil {
			result <- -1
			return
		}
		resp.Body.Close()
		result <- resp.StatusCode
	}()
	r := receive(t, hb.entered)
	a.cancel()
	a.l.Close()
	closed(t, a.host.drained)
	if r.Context().Err() != nil {
		t.Fatal("closing A canceled B's request")
	}
	hb.free()
	if receive(t, result) != 200 {
		t.Fatal("B's request failed")
	}
}

func TestLoopbackPanicsFailTheApplicationWithoutReachingThePage(t *testing.T) {
	x := newLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "staged=1")
		switch r.URL.Path {
		case "/abort":
			panic(http.ErrAbortHandler)
		case "/late":
			io.WriteString(w, "partial")
			http.NewResponseController(w).Flush()
			panic("exploded after output")
		case "/500":
			w.WriteHeader(500)
		default:
			panic("handler exploded")
		}
	}), ServerLimits{})
	code, body, header := x.status(t, "GET", "/boom", nil)
	if code != 500 || body != failureBody(500) || header.Get("Set-Cookie") != "" {
		t.Fatalf("panic response %d %q %v", code, body, header)
	}
	if f := x.host.failed(); len(f) != 1 || !strings.Contains(f[0].Error(), "goroutine") {
		t.Fatalf("failures %v", f)
	}
	resp, err := x.do(t, "GET", "/late", nil)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a response under way was completed after its handler panicked")
	}
	if f := x.host.failed(); len(f) != 2 || !strings.Contains(f[1].Error(), "exploded after output") {
		t.Fatalf("failures %v", f)
	}
	if _, err := x.do(t, "GET", "/abort", nil); err == nil {
		t.Fatal("ErrAbortHandler did not abort")
	}
	if code, _, _ := x.status(t, "GET", "/500", nil); code != 500 {
		t.Fatal("deliberate 500")
	}
	if len(x.host.failed()) != 2 {
		t.Fatal("abort or deliberate 500 treated as application failure")
	}
}

func TestLoopbackRejectsWrongModeAndNilHandler(t *testing.T) {
	if _, err := NewLoopback(nil, ServerLimits{}); err == nil {
		t.Fatal("nil handler")
	}
	l, _ := NewLoopback(http.NotFoundHandler(), ServerLimits{})
	ctx, _ := ownerContext(t)
	if err := l.Start(ctx, newHost(), &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err == nil {
		t.Fatal("loopback started in in-process mode")
	}
}

// failingListener serves no connection; Accept fails when the test says so.
type failingListener struct {
	net.Listener // the real bound socket, for its address
	fail         chan error
	closed       chan struct{}
	once         sync.Once
}

func (ln *failingListener) Accept() (net.Conn, error) {
	select {
	case err := <-ln.fail:
		return nil, err
	case <-ln.closed:
		return nil, net.ErrClosed
	}
}

func (ln *failingListener) Close() error {
	ln.once.Do(func() { close(ln.closed); ln.Listener.Close() })
	return nil
}

func TestLoopbackListenerFailureIsReportedBeforeDrainInEitherOrder(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel first %t", cancelFirst), func(t *testing.T) {
			l, _ := NewLoopback(http.NotFoundHandler(), ServerLimits{})
			fl := &failingListener{fail: make(chan error, 1), closed: make(chan struct{})}
			l.listen = func() (net.Listener, error) {
				real, err := net.Listen("tcp", "127.0.0.1:0")
				fl.Listener = real
				return fl, err
			}
			ctx, cancel := ownerContext(t)
			host := newHost()
			config := platform.BrowserConfig{Mode: platform.BrowserLoopback}
			if err := l.Start(ctx, host, &config); err != nil {
				t.Fatal(err)
			}
			if cancelFirst {
				// The owner is already closing when the listener fails.
				cancel()
			}
			fl.fail <- errInjected
			deadline := time.Now().Add(5 * time.Second)
			for len(host.failed()) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("listener failure discarded")
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			l.Close()
			closed(t, host.drained)
			if f := host.failed(); len(f) != 1 || !errors.Is(f[0], errInjected) || host.failuresAtDrain != 1 {
				t.Fatalf("failures %v, at drain %d", f, host.failuresAtDrain)
			}
		})
	}
}

func TestLoopbackOrdinaryCloseIsNotAListenerFailure(t *testing.T) {
	x := newLoopback(t, http.NotFoundHandler(), ServerLimits{})
	x.cancel()
	x.l.Close()
	closed(t, x.host.drained)
	if f := x.host.failed(); len(f) != 0 {
		t.Fatalf("close reported %v", f)
	}
}

// closingListener fails Accept only once Close has begun, overlapping a real
// failure with server shutdown.
type closingListener struct {
	net.Listener
	accepting chan struct{}
	closing   chan struct{}
	fail      chan error
	once      sync.Once
}

func (ln *closingListener) Accept() (net.Conn, error) {
	select {
	case ln.accepting <- struct{}{}:
	default:
	}
	return nil, <-ln.fail
}

func (ln *closingListener) Close() error {
	ln.once.Do(func() { close(ln.closing); ln.Listener.Close() })
	return nil
}

func TestLoopbackListenerFailureOverlappingCloseIsKept(t *testing.T) {
	l, _ := NewLoopback(http.NotFoundHandler(), ServerLimits{})
	cl := &closingListener{accepting: make(chan struct{}, 1), closing: make(chan struct{}), fail: make(chan error, 1)}
	l.listen = func() (net.Listener, error) {
		real, err := net.Listen("tcp", "127.0.0.1:0")
		cl.Listener = real
		return cl, err
	}
	ctx, cancel := ownerContext(t)
	host := newHost()
	config := platform.BrowserConfig{Mode: platform.BrowserLoopback}
	if err := l.Start(ctx, host, &config); err != nil {
		t.Fatal(err)
	}
	receive(t, cl.accepting) // Serve is blocked in Accept
	cancel()
	go l.Close()
	<-cl.closing // the server is shutting down
	cl.fail <- errInjected
	closed(t, host.drained)
	if f := host.failed(); len(f) != 1 || !errors.Is(f[0], errInjected) || host.failuresAtDrain != 1 {
		t.Fatalf("failures %v, at drain %d", f, host.failuresAtDrain)
	}
}
