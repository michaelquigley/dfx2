package endpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/origin"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// Finite is the in-process adapter: native requests are admitted, copied into
// Go-owned values within reserved limits, served on worker goroutines and
// answered on the UI thread with a complete buffered response.
//
// Every admitted request reserves its worst case before anything is buffered:
// its metadata, its body (declared length, or the limit plus a detection byte
// when unknown) and the native read buffer, and a full response with its
// native copy and metadata. Parts are released as their owners finish with
// them; the admission slot and the remainder are released only when both the
// browser and the handler have finished, so an abandoned handler still holds
// them. There is no queue in front of admission.
type Finite struct {
	handler http.Handler
	limits  Limits

	mu       sync.Mutex
	ctx      context.Context
	host     appcore.EndpointHost
	origin   string
	closed   bool
	drained  bool
	reserved int64 // bytes held by requests; the refusal reserve is separate
	admitted int
	refusals int // fixed refusals in native delivery without admission
	next     platform.ID
	requests map[platform.ID]*request
}

var _ appcore.InProcessEndpoint = (*Finite)(nil)

type request struct {
	id       platform.ID
	method   string
	url      *url.URL
	header   http.Header
	declared int64 // -1 unknown
	body     []byte
	bodyCap  int64
	// Reserved bytes, by owner.
	meta, bodyRes, read, resp int64

	admitted bool // holds an admission slot; otherwise a refusal slot
	native   bool // the browser still uses the request
	reading  bool
	handling bool // a handler is registered and its result not yet settled
	finished bool
}

// NewFinite validates the handler and limits without acquiring resources.
func NewFinite(handler http.Handler, limits Limits) (*Finite, error) {
	if handler == nil {
		return nil, errors.New("in-process mode requires a handler")
	}
	l, err := limits.Normalize()
	if err != nil {
		return nil, err
	}
	return &Finite{handler: handler, limits: l, requests: make(map[platform.ID]*request)}, nil
}

func (f *Finite) Start(ctx context.Context, host appcore.EndpointHost, config *platform.BrowserConfig) error {
	if config.Mode != platform.BrowserInProcess {
		return fmt.Errorf("in-process endpoint in browser mode %d", config.Mode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctx, f.host = ctx, host
	config.RequestMetadata = f.limits.Metadata // the browser measures against it
	return nil
}

func (f *Finite) Bind(o string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.origin = o
}

func (f *Finite) Close() {
	f.mu.Lock()
	f.closed = true
	drained := f.drainLocked()
	f.mu.Unlock()
	if drained {
		f.host.Drained()
	}
}

func (f *Finite) drainLocked() bool {
	if f.drained || !f.closed || f.ctx == nil || f.ctx.Err() == nil || len(f.requests) != 0 {
		return false
	}
	f.drained = true
	return true
}

func (f *Finite) Status() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var native, handling, reading int
	for _, r := range f.requests {
		if r.native {
			native++
		}
		if r.handling {
			handling++
		}
		if r.reading {
			reading++
		}
	}
	return fmt.Sprintf("in-process admitted=%d/%d native=%d handlers=%d reading=%d refusals=%d/%d reserved=%d/%d closed=%t drained=%t",
		f.admitted, f.limits.Requests, native, handling, reading, f.refusals, Refusals, f.reserved, f.budget(), f.closed, f.drained)
}

func (f *Finite) budget() int64 {
	return f.limits.Buffers - f.limits.reserveRefusals() - f.limits.reserveTransfer()
}

// set changes one reservation of a live request.
func (f *Finite) set(r *request, field *int64, value int64) {
	if r.finished {
		return
	}
	f.reserved += value - *field
	*field = value
}

var abortDecision = platform.RequestDecision{Action: platform.RequestAbort}

// refuseLocked answers a request that holds no admission with a fixed failure,
// within the bounded refusal reserve. Without a free slot it aborts, owing
// nothing.
func (f *Finite) refuseLocked(status int) platform.RequestDecision {
	if f.refusals >= Refusals {
		return abortDecision
	}
	f.refusals++
	f.next++
	f.requests[f.next] = &request{id: f.next, native: true}
	return platform.RequestDecision{ID: f.next, Action: platform.RequestRespond, Response: failureResponse(status)}
}

// metadataSize charges UTF-8 bytes of the method, URL and every header
// occurrence, plus fixed record and per-header overhead.
func metadataSize(first, second string, headers []platform.Header) int64 {
	n := int64(recordOverhead + len(first) + len(second))
	for _, h := range headers {
		n += int64(len(h.Name) + len(h.Value) + headerOverhead)
	}
	return n
}

// headerSize measures a response before copying it. Empty value slices still
// retain a map entry and name in the snapshot, even though they emit no field.
func headerSize(status int, h http.Header) int64 {
	n := int64(recordOverhead + len(strconv.Itoa(status)))
	for name, values := range h {
		if len(values) == 0 {
			n += int64(len(name) + headerOverhead)
		}
		for _, value := range values {
			n += int64(len(name) + len(value) + headerOverhead)
		}
	}
	return n
}

func validMethod(m string) bool {
	if m == "" {
		return false
	}
	for _, c := range m {
		if c <= ' ' || c >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", c) {
			return false
		}
	}
	return true
}

func (f *Finite) Request(in platform.BrowserRequest) platform.RequestDecision {
	r, decision := f.admit(in)
	if r == nil {
		return decision
	}
	if r.bodyCap == 0 {
		return f.serve(r)
	}
	return decision
}

func (f *Finite) admit(in platform.BrowserRequest) (*request, platform.RequestDecision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Admission and sealing share this lock: a canceled owner admits nothing.
	if f.closed || f.ctx == nil || f.ctx.Err() != nil || f.origin == "" {
		return nil, abortDecision
	}
	// Measure before parsing: nothing is decoded or copied for oversized
	// metadata.
	// A browser that stopped measuring past the limit supplied no headers;
	// its measurement then decides.
	meta := max(metadataSize(in.Method, in.URL, in.Header), in.MetadataSize)
	if meta > int64(f.limits.Metadata) {
		return nil, f.refuseLocked(http.StatusRequestHeaderFieldsTooLarge)
	}
	if !validMethod(in.Method) || in.BodyLength < -1 {
		return nil, f.refuseLocked(http.StatusBadRequest)
	}
	if in.BodyLength > f.limits.RequestBody {
		return nil, f.refuseLocked(http.StatusRequestEntityTooLarge)
	}
	bodyCap := in.BodyLength
	if bodyCap < 0 {
		bodyCap = f.limits.RequestBody + 1 // detection byte
	}
	read := min(int64(ReadChunk), bodyCap)
	// The public metadata ceiling measures the incoming representation. The
	// aggregate reservation also covers parsing and retained URL variants.
	meta += urlAllowance(int64(len(in.URL)))
	need := meta + bodyCap + read + f.limits.responseAllowance()
	if f.admitted >= f.limits.Requests || f.reserved+need > f.budget() {
		return nil, f.refuseLocked(http.StatusServiceUnavailable)
	}
	// Reserve before cloning or parsing. On refusal roll it back; on success
	// transfer the reservation to the request without charging it twice.
	f.reserved += need
	accepted := false
	defer func() {
		if !accepted {
			f.reserved -= need
		}
	}()
	// Parsed fields can retain the input, so it must be Go-owned. Reuse this
	// parse for origin checking rather than allocating another decoded URL.
	u, err := url.Parse(strings.Clone(in.URL))
	if err != nil {
		return nil, f.refuseLocked(http.StatusBadRequest)
	}
	if o, err := origin.FromURL(u); err != nil || o != f.origin {
		return nil, f.refuseLocked(http.StatusForbidden)
	}
	header := make(http.Header, len(in.Header))
	for _, h := range in.Header {
		header.Add(strings.Clone(h.Name), strings.Clone(h.Value))
	}
	r := &request{method: strings.Clone(in.Method), url: u, header: header, declared: in.BodyLength, bodyCap: bodyCap,
		admitted: true, native: true, reading: bodyCap > 0,
		meta: meta, bodyRes: bodyCap, read: read, resp: f.limits.responseAllowance()}
	accepted = true
	f.admitted++
	f.next++
	r.id = f.next
	f.requests[r.id] = r
	return r, platform.RequestDecision{ID: f.next, Action: platform.RequestRead, ReadChunk: int(read)}
}

func (f *Finite) RequestBody(id platform.ID, data []byte) platform.RequestDecision {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests[id]
	if r == nil || !r.reading {
		return abortDecision
	}
	if f.ctx.Err() != nil {
		// Close cancels native body reads; the browser still releases the request.
		f.stopReadingLocked(r)
		return platform.RequestDecision{ID: id, Action: platform.RequestAbort}
	}
	if int64(len(data)) > r.read {
		// The browser broke its read contract; stop without a response.
		f.stopReadingLocked(r)
		return abortDecision
	}
	total := int64(len(r.body)) + int64(len(data))
	switch {
	case r.declared >= 0 && total > r.declared:
		f.stopReadingLocked(r)
		return f.answerLocked(id, http.StatusBadRequest)
	case total > f.limits.RequestBody:
		f.stopReadingLocked(r)
		return f.answerLocked(id, http.StatusRequestEntityTooLarge)
	}
	if r.body == nil {
		// One allocation of the reserved capacity: growth would hold the old
		// and new buffers at once, beyond the body reservation.
		r.body = make([]byte, 0, r.bodyCap)
	}
	r.body = append(r.body, data...)
	return platform.RequestDecision{ID: id, Action: platform.RequestRead, ReadChunk: int(r.read)}
}

func (f *Finite) stopReadingLocked(r *request) {
	r.reading = false
	r.body = nil
	f.set(r, &r.read, 0)
	f.set(r, &r.bodyRes, 0)
}

// answerLocked ends an admitted request with a fixed failure; the request's
// own response reservation covers its native delivery.
func (f *Finite) answerLocked(id platform.ID, status int) platform.RequestDecision {
	return platform.RequestDecision{ID: id, Action: platform.RequestRespond, Response: failureResponse(status)}
}

func (f *Finite) RequestBodyEnd(id platform.ID, readErr error) platform.RequestDecision {
	f.mu.Lock()
	r := f.requests[id]
	if r == nil || !r.reading {
		f.mu.Unlock()
		return abortDecision
	}
	if readErr != nil || f.ctx.Err() != nil {
		f.stopReadingLocked(r)
		f.mu.Unlock()
		return abortDecision
	}
	if r.declared >= 0 && int64(len(r.body)) != r.declared {
		f.stopReadingLocked(r)
		defer f.mu.Unlock()
		return f.answerLocked(id, http.StatusBadRequest)
	}
	r.reading = false
	f.set(r, &r.read, 0)
	f.set(r, &r.bodyRes, int64(cap(r.body)))
	f.mu.Unlock()
	return f.serve(r)
}

// serve registers the handler's completion before starting it. UI thread,
// without the endpoint lock: Begin takes the core's.
func (f *Finite) serve(r *request) platform.RequestDecision {
	op, err := f.host.Begin()
	f.mu.Lock()
	id := r.id
	if err != nil || f.ctx.Err() != nil || !r.native {
		f.set(r, &r.bodyRes, 0)
		r.body = nil
		f.mu.Unlock()
		if err == nil {
			f.host.Publish(op, func(bool) error { return nil })
		}
		return platform.RequestDecision{ID: id, Action: platform.RequestAbort}
	}
	r.handling = true
	req := f.requestLocked(r)
	f.mu.Unlock()
	go f.work(r, op, req)
	return platform.RequestDecision{ID: id, Action: platform.RequestWait}
}

// requestLocked builds a server-side request from Go-owned copies. The
// transport is synthetic: no TCP peer, TLS state or protocol negotiation.
func (f *Finite) requestLocked(r *request) *http.Request {
	body := io.ReadCloser(http.NoBody)
	if len(r.body) > 0 {
		body = io.NopCloser(bytes.NewReader(r.body))
	}
	u := &url.URL{Path: r.url.Path, RawPath: r.url.RawPath, RawQuery: r.url.RawQuery}
	req := &http.Request{Method: r.method, URL: u, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: r.header,
		Body: body, ContentLength: int64(len(r.body)), Host: origin.Host(f.origin), RequestURI: u.RequestURI()}
	return req.WithContext(f.ctx)
}

func (f *Finite) work(r *request, op platform.ID, req *http.Request) {
	w := &writer{method: req.Method, limit: f.limits.ResponseBody, metadata: int64(f.limits.Metadata), header: make(http.Header)}
	var failure error
	aborted := false
	func() {
		defer func() {
			if p := recover(); p != nil {
				aborted, failure = recoverHandler(p)
			}
		}()
		f.handler.ServeHTTP(w, req)
	}()
	var resp platform.BrowserResponse
	switch {
	case failure != nil:
		// Report before settling: cancel and drain the application. The page
		// sees a fixed failure, never the stack.
		f.host.Fail(failure)
		resp = failureResponse(http.StatusInternalServerError)
	case aborted:
		// A deliberate abort has no connection to sever in-process.
		resp = failureResponse(http.StatusBadGateway)
	default:
		resp = w.finish()
	}
	f.host.Publish(op, func(closing bool) error { f.settle(r, closing, resp); return nil })
}

// settle runs on the UI thread. A closing window or a request the browser
// already released discards the result; otherwise the browser copies it.
func (f *Finite) settle(r *request, closing bool, resp platform.BrowserResponse) {
	f.mu.Lock()
	r.handling = false
	r.body = nil
	f.set(r, &r.bodyRes, 0)
	id := r.id
	if closing || !r.native || f.ctx.Err() != nil {
		f.set(r, &r.resp, 0)
		drained := f.finishLocked(r)
		f.mu.Unlock()
		if drained {
			f.host.Drained()
		}
		return
	}
	// The Go result holds its body buffer's capacity; the browser's copy is
	// exact. Both are within the allowance reserved at admission.
	meta := metadataSize(strconv.Itoa(resp.Status), "", resp.Header)
	native := int64(len(resp.Body)) + meta
	f.set(r, &r.resp, int64(cap(resp.Body))+meta+native)
	f.mu.Unlock()
	delivered := f.host.Respond(id, resp)
	f.mu.Lock()
	if delivered && r.native {
		f.set(r, &r.resp, native) // the browser's copy, until it releases the request
	} else {
		f.set(r, &r.resp, 0)
	}
	drained := f.finishLocked(r)
	f.mu.Unlock()
	if drained {
		f.host.Drained()
	}
}

// finishLocked retires a request once neither the browser nor a handler uses
// it, returning whether the endpoint has just drained.
func (f *Finite) finishLocked(r *request) bool {
	if r.finished || r.native || r.handling {
		return false
	}
	id := r.id
	f.set(r, &r.meta, 0)
	f.set(r, &r.bodyRes, 0)
	f.set(r, &r.read, 0)
	f.set(r, &r.resp, 0)
	r.finished = true
	if r.admitted {
		f.admitted--
	} else {
		f.refusals--
	}
	delete(f.requests, id)
	return f.drainLocked()
}

func (f *Finite) RequestReleased(id platform.ID) {
	f.mu.Lock()
	r := f.requests[id]
	if r == nil || !r.native {
		f.mu.Unlock()
		return
	}
	r.native = false
	if r.reading {
		f.stopReadingLocked(r)
	}
	if !r.handling {
		f.set(r, &r.resp, 0) // a delivered copy or an unused allowance
	}
	drained := f.finishLocked(r)
	f.mu.Unlock()
	if drained {
		f.host.Drained()
	}
}

// writer is the buffered in-process ResponseWriter. It deliberately has no
// Flusher, Hijacker or Unwrap: streaming and upgrades are not offered.
type writer struct {
	method      string
	limit       int64
	metadata    int64 // response metadata limit
	header      http.Header
	written     http.Header // header snapshot at WriteHeader
	status      int
	body        []byte
	overflow    bool
	unsupported bool
}

// ErrResponseTooLarge is returned by Write once a response exceeds its limit.
// The response then fails with 502 even if the handler ignores the error.
var ErrResponseTooLarge = errors.New("dfx2 webview: in-process response exceeds its limit")

func (w *writer) Header() http.Header { return w.header }

func (w *writer) WriteHeader(code int) {
	if code < 100 || code > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", code)) // as net/http
	}
	if w.status != 0 {
		return
	}
	if code < 200 {
		// Informational and switching-protocol responses are not supported.
		w.unsupported = true
		return
	}
	w.status = code
	// The handler's header map is its own; the adapter measures it before
	// taking a snapshot, so oversized metadata is never copied.
	if headerSize(code, w.header) > w.metadata {
		w.unsupported = true
		return
	}
	w.written = w.header.Clone()
}

func bodyAllowed(status int) bool { return status >= 200 && status != 204 && status != 304 }

func (w *writer) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if !bodyAllowed(w.status) {
		return 0, http.ErrBodyNotAllowed
	}
	if w.method == http.MethodHead {
		return len(p), nil // as net/http: discarded
	}
	if w.overflow || int64(len(w.body))+int64(len(p)) > w.limit {
		w.overflow = true
		return 0, ErrResponseTooLarge
	}
	if need := int64(len(w.body)) + int64(len(p)); need > int64(cap(w.body)) {
		// Growth briefly holds both buffers, at most 1.5 times the limit. The
		// response allowance holds twice the limit, and its second half, for
		// the browser's copy, is unused until the handler has returned.
		grown := make([]byte, len(w.body), min(max(2*int64(cap(w.body)), need, 4096), w.limit))
		copy(grown, w.body)
		w.body = grown
	}
	w.body = append(w.body, p...)
	return len(p), nil
}

// finish publishes the response or a fixed 502: never a successful prefix.
func (w *writer) finish() platform.BrowserResponse {
	if w.status == 0 && !w.unsupported {
		w.WriteHeader(http.StatusOK)
	}
	if w.overflow || w.unsupported {
		return failureResponse(http.StatusBadGateway)
	}
	h := w.written
	for name := range h {
		if strings.HasPrefix(name, http.TrailerPrefix) {
			return failureResponse(http.StatusBadGateway) // trailers are not supported
		}
	}
	for _, hop := range []string{"Connection", "Keep-Alive", "Transfer-Encoding", "Trailer", "Upgrade", "Content-Length"} {
		h.Del(hop)
	}
	if bodyAllowed(w.status) {
		if len(w.body) > 0 && !hasKey(h, "Content-Type") {
			h.Set("Content-Type", http.DetectContentType(w.body)) // as net/http
		}
		h.Set("Content-Length", strconv.Itoa(len(w.body)))
	}
	h.Add("Content-Security-Policy", ContentSecurityPolicy)
	if headerSize(w.status, h) > w.metadata {
		return failureResponse(http.StatusBadGateway)
	}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	slices.Sort(names)
	var headers []platform.Header
	for _, name := range names {
		for _, value := range h[name] {
			headers = append(headers, platform.Header{Name: name, Value: value})
		}
	}
	return platform.BrowserResponse{Status: w.status, Header: headers, Body: w.body}
}

// hasKey distinguishes an explicitly empty Content-Type, which suppresses
// sniffing in net/http, from an absent one.
func hasKey(h http.Header, name string) bool {
	_, ok := h[http.CanonicalHeaderKey(name)]
	return ok
}
