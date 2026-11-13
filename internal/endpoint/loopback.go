package endpoint

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/origin"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// CookieName names the owned loopback credential. Its value is a random
// per-endpoint secret the browser holds as an HttpOnly session cookie; it is
// never placed in page URLs, application JavaScript or logs.
const CookieName = "dfx2_endpoint"

// Loopback is a window-owned HTTP server on 127.0.0.1 with an assigned port.
// Ordinary HTTP serving applies, streaming included; there is no finite
// response limit, global write deadline or mandatory buffering. The access
// layer checks the credential, the exact Host and any supplied Origin before
// the application handler runs. Connections and handlers are counted from
// acceptance and entry until closure and return, independently of the
// server's own shutdown bookkeeping.
type Loopback struct {
	handler http.Handler
	limits  ServerLimits
	listen  func() (net.Listener, error) // a test seam; binds 127.0.0.1:0

	mu           sync.Mutex
	ctx          context.Context
	host         appcore.EndpointHost
	server       *http.Server
	origin       string // canonical http://127.0.0.1:port
	hostport     string
	secret       []byte
	serving      bool
	closed       bool
	drained      bool
	conns        map[*conn]struct{}
	handlers     int
	acceptErr    error // the first real listener failure, captured at Accept
	refusedConns uint64
	busy         uint64 // requests refused for handler admission
	unauthorized uint64
	upgrades     uint64
}

var _ appcore.Endpoint = (*Loopback)(nil)

// NewLoopback validates the handler and limits without acquiring resources.
func NewLoopback(handler http.Handler, limits ServerLimits) (*Loopback, error) {
	if handler == nil {
		return nil, errors.New("loopback mode requires a handler")
	}
	l, err := limits.Normalize()
	if err != nil {
		return nil, err
	}
	return &Loopback{handler: handler, limits: l, conns: make(map[*conn]struct{}),
		listen: func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }}, nil
}

// Start binds the listener, generates the credential and fills in the
// browser's start URL, origin and cookie. Nothing remains on error.
func (l *Loopback) Start(ctx context.Context, host appcore.EndpointHost, config *platform.BrowserConfig) error {
	if config.Mode != platform.BrowserLoopback {
		return fmt.Errorf("loopback endpoint in browser mode %d", config.Mode)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("loopback credential: %w", err)
	}
	ln, err := l.listen()
	if err != nil {
		return fmt.Errorf("loopback listen: %w", err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		ln.Close()
		return fmt.Errorf("loopback listener bound %v, not 127.0.0.1", ln.Addr())
	}
	port := addr.Port
	o, err := origin.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		ln.Close()
		return err
	}
	value := base64.RawURLEncoding.EncodeToString(secret)
	server := &http.Server{
		Handler:           http.HandlerFunc(l.serveHTTP),
		ReadHeaderTimeout: l.limits.ReadHeaderTimeout,
		IdleTimeout:       l.limits.IdleTimeout,
		MaxHeaderBytes:    l.limits.HeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		// Malformed and disconnected requests stay local; nothing about them,
		// or the credential, reaches the application's log.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	l.mu.Lock()
	l.ctx, l.host, l.server, l.origin, l.hostport = ctx, host, server, o, origin.Host(o)
	l.secret = []byte(value)
	l.serving = true
	l.mu.Unlock()
	config.StartURL = o + "/"
	config.Origins = []string{o}
	config.Credential = &platform.BrowserCookie{Name: CookieName, Value: value, Origin: o}
	go l.serve(&listener{Listener: ln, l: l})
	return nil
}

func (l *Loopback) serve(ln net.Listener) {
	// Serve returns ErrServerClosed whenever shutdown has begun, even when an
	// unrelated Accept failure ended it, so failures are captured at Accept
	// instead. Either is published before serving ends, so the endpoint
	// cannot report drained ahead of its own failure, whatever the owner's
	// state.
	err := l.server.Serve(ln)
	l.mu.Lock()
	failure := l.acceptErr
	l.mu.Unlock()
	if failure == nil && !errors.Is(err, http.ErrServerClosed) {
		failure = err
	}
	if failure != nil {
		l.host.Fail(fmt.Errorf("loopback listener: %w", failure))
	}
	l.mu.Lock()
	l.serving = false
	drained := l.drainLocked()
	l.mu.Unlock()
	if drained {
		l.host.Drained()
	}
}

// Close stops the listener and closes every connection. It does not wait:
// handlers that ignore cancellation stay counted until they return.
func (l *Loopback) Close() {
	l.mu.Lock()
	l.closed = true
	conns := make([]*conn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	server := l.server
	l.mu.Unlock()
	server.Close()
	for _, c := range conns {
		c.Close()
	}
	l.mu.Lock()
	drained := l.drainLocked()
	l.mu.Unlock()
	if drained {
		l.host.Drained()
	}
}

func (l *Loopback) drainLocked() bool {
	if l.drained || !l.closed || l.ctx.Err() == nil || l.serving || l.handlers != 0 || len(l.conns) != 0 {
		return false
	}
	l.drained = true
	return true
}

func (l *Loopback) Status() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fmt.Sprintf("loopback origin=%s connections=%d/%d handlers=%d/%d serving=%t closed=%t drained=%t refused-connections=%d busy=%d unauthorized=%d upgrades=%d",
		l.origin, len(l.conns), l.limits.Connections, l.handlers, l.limits.Handlers, l.serving, l.closed, l.drained,
		l.refusedConns, l.busy, l.unauthorized, l.upgrades)
}

// authorized checks the exact Host, the credential in constant time, and any
// supplied Origin. A missing Origin is accepted only with both of the others.
func (l *Loopback) authorized(r *http.Request) bool {
	if r.Host != l.hostport {
		return false
	}
	c, err := r.Cookie(CookieName)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), l.secret) != 1 {
		return false
	}
	if values, ok := r.Header["Origin"]; ok {
		if len(values) != 1 {
			return false
		}
		o, err := origin.Parse(values[0])
		if err != nil || o != l.origin {
			return false
		}
	}
	return true
}

func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, v := range r.Header.Values("Connection") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func (l *Loopback) serveHTTP(w http.ResponseWriter, r *http.Request) {
	// Nothing reaches the handler, or reveals admission state, without the
	// capability.
	if !l.authorized(r) {
		l.mu.Lock()
		l.unauthorized++
		l.mu.Unlock()
		writeFailure(w, http.StatusForbidden)
		return
	}
	if isUpgrade(r) {
		// No upgraded connection may escape the endpoint's ownership.
		l.mu.Lock()
		l.upgrades++
		l.mu.Unlock()
		writeFailure(w, http.StatusNotImplemented)
		return
	}
	l.mu.Lock()
	if l.closed || l.ctx.Err() != nil || l.handlers >= l.limits.Handlers {
		l.busy++
		l.mu.Unlock()
		writeFailure(w, http.StatusServiceUnavailable)
		return
	}
	l.handlers++
	l.mu.Unlock()
	defer l.handlerReturned()
	rw := &responseWriter{w: w}
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		abort, err := recoverHandler(p)
		if abort {
			panic(http.ErrAbortHandler)
		}
		// Report before net/http's own recovery could hide it, then answer
		// with a fixed failure, or abort a response already under way.
		l.host.Fail(err)
		if rw.wrote {
			panic(http.ErrAbortHandler)
		}
		clear(w.Header()) // nothing the handler staged, cookies included
		writeFailure(w, http.StatusInternalServerError)
	}()
	l.handler.ServeHTTP(rw, r)
	// A header-only handler leaves net/http to write its implicit 200 after
	// this method returns. Install the policy before that bypasses our writer.
	rw.commit()
}

func (l *Loopback) handlerReturned() {
	l.mu.Lock()
	l.handlers--
	drained := l.drainLocked()
	l.mu.Unlock()
	if drained {
		l.host.Drained()
	}
}

// responseWriter offers Flusher but neither Hijacker nor Unwrap, so a
// handler cannot take an unowned connection.
type responseWriter struct {
	w     http.ResponseWriter
	wrote bool
}

func (rw *responseWriter) Header() http.Header { return rw.w.Header() }

// commit adds the framework policy as the final header is sent, after the
// handler's own header changes, so a handler cannot remove it.
func (rw *responseWriter) commit() {
	if !rw.wrote {
		rw.wrote = true
		policy(rw.w)
	}
}

func (rw *responseWriter) WriteHeader(code int) {
	if code == http.StatusSwitchingProtocols || code >= 200 {
		rw.commit()
	}
	rw.w.WriteHeader(code)
}

func (rw *responseWriter) Write(p []byte) (int, error) {
	rw.commit()
	return rw.w.Write(p)
}

func (rw *responseWriter) Flush() { _ = rw.FlushError() }

func (rw *responseWriter) FlushError() error {
	rw.commit()
	if f, ok := rw.w.(interface{ FlushError() error }); ok {
		return f.FlushError()
	}
	if f, ok := rw.w.(http.Flusher); ok {
		f.Flush()
		return nil
	}
	return http.ErrNotSupported
}

// listener refuses connections beyond the limit, or after sealing, by
// closing them before they are served.
type listener struct {
	net.Listener
	l *Loopback
}

func (ln *listener) Accept() (net.Conn, error) {
	l := ln.l
	for {
		c, err := ln.Listener.Accept()
		if err != nil {
			// Closing the listener yields net.ErrClosed, and net/http retries
			// temporary errors itself; anything else is a real failure.
			if temporary, ok := err.(interface{ Temporary() bool }); !errors.Is(err, net.ErrClosed) && !(ok && temporary.Temporary()) {
				l.mu.Lock()
				if l.acceptErr == nil {
					l.acceptErr = err
				}
				l.mu.Unlock()
			}
			return nil, err
		}
		l.mu.Lock()
		if l.closed || l.ctx.Err() != nil || len(l.conns) >= l.limits.Connections {
			l.refusedConns++
			l.mu.Unlock()
			c.Close()
			continue
		}
		tracked := &conn{Conn: c, l: l}
		l.conns[tracked] = struct{}{}
		l.mu.Unlock()
		return tracked, nil
	}
}

// conn is counted from acceptance until its first Close.
type conn struct {
	net.Conn
	l    *Loopback
	once sync.Once
}

func (c *conn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		l := c.l
		l.mu.Lock()
		delete(l.conns, c)
		drained := l.drainLocked()
		l.mu.Unlock()
		if drained {
			l.host.Drained()
		}
	})
	return err
}
