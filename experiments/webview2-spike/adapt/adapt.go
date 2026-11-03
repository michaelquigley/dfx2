// Package adapt turns an intercepted webview request, copied into Go-owned
// bytes, into an ordinary http.Request, runs a Go http.Handler on it, and
// buffers what the handler writes into a finite response.
//
// It knows nothing about WebView2 or Windows, so its contracts are tested
// on any platform. It is an experiment's adapter for finite request and
// response bodies, not a replacement for net/http serving: no streaming,
// no upgrades, no trailers, no connection state.
package adapt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Limit bounds request and response bodies in this experiment's adapter:
// 1 MiB each way. Larger bodies fail visibly (413 in, 502 out); nothing is
// truncated. It is an experiment limit, not a proposed framework default.
const Limit = 1 << 20

// Request is an intercepted request, already copied out of the webview's
// COM objects on the thread that owns them. Nothing in it refers to native
// memory, so a worker may hold it.
type Request struct {
	Method string
	URL    string      // the absolute URI the webview reported
	Header [][2]string // in the order reported; repeated names stay repeated
	Body   []byte
	// BodyTooLarge reports that the body stream held more than Limit
	// bytes. Body then holds only what was read, and must not be used.
	BodyTooLarge bool
}

// Response is the buffered result of one handler call.
type Response struct {
	Status int
	Reason string
	Header http.Header
	Body   []byte

	// Diagnostics, never sent to the webview.
	Ran      bool          // the handler was invoked (false for rejected input)
	Canceled bool          // the handler's context was done when it returned
	Elapsed  time.Duration // handler time
	Note     string        // why the adapter, not the handler, chose this response
}

// Errors the adapter reports to a handler that writes too much.
var ErrBodyTooLarge = errors.New("adapt: response body exceeds the adapter's 1 MiB limit")

// CheckOrigin parses raw and accepts it only if its scheme and host (with
// any port) are exactly origin's, it carries no user info, and its path is
// absolute. A string prefix is not enough: https://dfx2-spike.invalid.evil/
// and https://user@dfx2-spike.invalid/ both share one.
func CheckOrigin(raw string, origin *url.URL) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch {
	case !strings.EqualFold(u.Scheme, origin.Scheme):
		return nil, fmt.Errorf("scheme %q is not %q", u.Scheme, origin.Scheme)
	case !strings.EqualFold(u.Host, origin.Host):
		return nil, fmt.Errorf("host %q is not %q", u.Host, origin.Host)
	case u.User != nil:
		return nil, errors.New("user info in the URL")
	case u.Opaque != "":
		return nil, errors.New("opaque URL")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

// NewHTTPRequest builds the request a handler sees, shaped as a server
// request is: URL holds only the path and query, Host and RequestURI are
// set, and the body is a reader over the copied bytes.
func NewHTTPRequest(ctx context.Context, origin *url.URL, in *Request) (*http.Request, error) {
	u, err := CheckOrigin(in.URL, origin)
	if err != nil {
		return nil, err
	}
	method := in.Method
	if method == "" {
		method = http.MethodGet
	}
	reqURI := u.RequestURI()
	r, err := http.NewRequestWithContext(ctx, method, reqURI, bytes.NewReader(in.Body))
	if err != nil {
		return nil, err
	}
	r.URL, err = url.ParseRequestURI(reqURI)
	if err != nil {
		return nil, err
	}
	r.RequestURI = reqURI
	r.Host = u.Host
	r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/1.1", 1, 1
	r.RemoteAddr = "webview2-intercept"
	r.ContentLength = int64(len(in.Body))
	if len(in.Body) == 0 {
		r.Body = http.NoBody
	}
	for _, kv := range in.Header {
		k := http.CanonicalHeaderKey(kv[0])
		if k == "Host" {
			continue // net/http moves Host into r.Host, never r.Header
		}
		r.Header.Add(k, kv[1])
	}
	return r, nil
}

// Serve runs h on one intercepted request and returns its buffered
// response. It never panics: a handler panic becomes a 500. Oversized input
// is refused before h runs; oversized output is replaced with a 502.
func Serve(ctx context.Context, h http.Handler, origin *url.URL, in *Request) Response {
	if in.BodyTooLarge || len(in.Body) > Limit {
		return adapterError(http.StatusRequestEntityTooLarge, "request-too-large",
			fmt.Sprintf("request body exceeds the adapter's %d-byte limit", Limit))
	}
	r, err := NewHTTPRequest(ctx, origin, in)
	if err != nil {
		return adapterError(http.StatusBadRequest, "bad-request", err.Error())
	}
	w := newBuffer(Limit)
	start := time.Now()
	panicked := run(h, w, r)
	resp := w.finish()
	resp.Ran = true
	resp.Elapsed = time.Since(start)
	resp.Canceled = ctx.Err() != nil
	switch {
	case panicked != nil:
		// net/http would log and drop the connection; a finite adapter
		// gives the page a visible 500 instead.
		e := adapterError(http.StatusInternalServerError, "handler-panic", fmt.Sprintf("handler panicked: %v", panicked))
		e.Ran, e.Elapsed, e.Canceled = true, resp.Elapsed, resp.Canceled
		return e
	case w.overflow:
		e := adapterError(http.StatusBadGateway, "response-too-large",
			fmt.Sprintf("response body exceeds the adapter's %d-byte limit (status %d from the handler was discarded)", Limit, resp.Status))
		e.Ran, e.Elapsed, e.Canceled = true, resp.Elapsed, resp.Canceled
		return e
	}
	return resp
}

func run(h http.Handler, w http.ResponseWriter, r *http.Request) (panicked any) {
	defer func() {
		if p := recover(); p != nil {
			panicked = p
		}
	}()
	h.ServeHTTP(w, r)
	return nil
}

// adapterError is a response the adapter makes itself. The header names the
// reason so the page and the log can tell it from a handler's response.
func adapterError(status int, code, msg string) Response {
	h := http.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Spike-Adapter-Error", code)
	return Response{Status: status, Reason: http.StatusText(status), Header: h, Body: []byte(msg + "\n"), Note: code}
}

// HeaderBlock renders the headers as WebView2's CreateWebResourceResponse
// wants them: "Name: value" lines joined by CRLF. A repeated header becomes
// repeated lines, in the handler's order. Names are sorted so the block is
// deterministic. A value containing CR, LF or NUL is dropped, as net/http
// would refuse to write it.
func HeaderBlock(h http.Header) (block string, dropped []string) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if !validName(k) {
			dropped = append(dropped, k)
			continue
		}
		for _, v := range h[k] {
			if strings.ContainsAny(v, "\r\n\x00") {
				dropped = append(dropped, k)
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\r\n")
			}
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
		}
	}
	return b.String(), dropped
}

func validName(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte("()<>@,;:\\\"/[]?={}", c) >= 0 {
			return false
		}
	}
	return true
}
