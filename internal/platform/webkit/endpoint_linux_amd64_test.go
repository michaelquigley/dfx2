package webkit

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/michaelquigley/dfx2/internal/endpoint"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// endpointHost forwards an attachment's request traffic to a real in-process
// endpoint, as the core does, and plays the core for the endpoint.
type endpointHost struct {
	fakeHost
	f       *endpoint.Finite
	drained chan struct{}
	ops     platform.ID
}

func (h *endpointHost) Request(r platform.BrowserRequest) platform.RequestDecision {
	return h.f.Request(r)
}
func (h *endpointHost) RequestBody(id platform.ID, b []byte) platform.RequestDecision {
	return h.f.RequestBody(id, b)
}
func (h *endpointHost) RequestBodyEnd(id platform.ID, err error) platform.RequestDecision {
	return h.f.RequestBodyEnd(id, err)
}
func (h *endpointHost) RequestReleased(id platform.ID) {
	h.fakeHost.RequestReleased(id)
	h.f.RequestReleased(id)
}

// appcore.EndpointHost
func (h *endpointHost) Begin() (platform.ID, error)                        { h.ops++; return h.ops, nil }
func (h *endpointHost) Publish(platform.ID, func(bool) error)              {}
func (h *endpointHost) Respond(platform.ID, platform.BrowserResponse) bool { return false }
func (h *endpointHost) Fail(error)                                         {}
func (h *endpointHost) Drained()                                           { close(h.drained) }

// nativeError builds a GError-shaped record in native memory, as
// g_input_stream_read_bytes_finish reports one.
func nativeError(t *testing.T) uintptr { return nativeErrorOf(t, 1, 7) }

// nativeErrorOf builds a GError record with the given domain quark and code;
// the fake g_quark_to_string names quarkIOError "g-io-error-quark".
func nativeErrorOf(t *testing.T, domain uint32, code int32) uintptr {
	t.Helper()
	b, err := syscall.Mmap(-1, 0, 16, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Munmap(b) })
	g := (*struct {
		domain  uint32
		code    int32
		message uintptr
	})(unsafe.Pointer(unsafe.SliceData(b)))
	g.domain, g.code = domain, code
	return uintptr(unsafe.Pointer(unsafe.SliceData(b)))
}

// A body read that ends without a body decision ID must still release the
// admitted request, or the endpoint never drains.
func TestBodyReadAbortsReleaseTheAdmittedRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish func(t *testing.T, cancel context.CancelFunc) // shapes the read's completion
	}{
		{"failed read", func(t *testing.T, _ context.CancelFunc) {
			e := nativeError(t)
			w.readBytesFinish = func(_, _ uintptr, out *uintptr) uintptr { *out = e; return 0 }
		}},
		{"canceled during read", func(t *testing.T, cancel context.CancelFunc) {
			// The window's endpoint context ends while the read is in flight;
			// the read then completes at end of stream.
			cancel()
			w.readBytesFinish = func(uintptr, uintptr, *uintptr) uintptr { return 1 }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := endpoint.NewFinite(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("handler reached by an aborted body")
			}), endpoint.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			host := &endpointHost{fakeHost: fakeHost{ready: true}, f: f, drained: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := f.Start(ctx, host, &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err != nil {
				t.Fatal(err)
			}
			f.Bind(Origin)
			native, b := newTestBrowser(t, &host.fakeHost, platform.BrowserInProcess)
			b.host = host
			post := cstr(t, "POST")
			w.schemeMethod = func(uintptr) uintptr { return post }
			w.schemeBody = func(uintptr) uintptr { return 555 }
			b.Start()
			native.complete() // the content filter
			b.receive(701)
			if !strings.Contains(f.Status(), "admitted=1/") || len(native.asyncs) != 1 {
				t.Fatalf("not reading an admitted request: %s", f.Status())
			}
			tc.finish(t, cancel)
			native.complete() // the read
			if native.finished[701] != "error" {
				t.Fatalf("native request not failed: %v", native.finished)
			}
			if len(host.releasedR) != 1 || host.releasedR[0] == 0 {
				t.Fatalf("admitted request released %v", host.releasedR)
			}
			if !strings.Contains(f.Status(), "admitted=0/") || !strings.Contains(f.Status(), "reserved=0/") {
				t.Fatalf("endpoint accounting not drained: %s", f.Status())
			}
			cancel()
			f.Close()
			select {
			case <-host.drained:
			default:
				t.Fatalf("endpoint never drained: %s", f.Status())
			}
		})
	}
}

// nativeHeaders serves header pairs from native memory through the
// SoupMessageHeaders iterator bindings, counting how far it was walked.
type nativeHeaders struct {
	names, values []uintptr
	steps         int
}

func (n *nativeHeaders) install(t *testing.T, pairs ...string) {
	for i := 0; i+1 < len(pairs); i += 2 {
		n.names = append(n.names, cstr(t, pairs[i]))
		n.values = append(n.values, cstr(t, pairs[i+1]))
	}
	w.schemeHeaders = func(uintptr) uintptr { return 1 }
	w.headersIterInit = func(it *[3]uintptr, _ uintptr) { it[0] = 0 }
	w.headersIterNext = func(it *[3]uintptr, name, value *uintptr) int32 {
		n.steps++
		if int(it[0]) >= len(n.names) {
			return 0
		}
		*name, *value = n.names[it[0]], n.values[it[0]]
		it[0]++
		return 1
	}
}

// overwrite replaces native storage, as WebKit reusing it after the request
// object is released would.
func overwrite(p uintptr) {
	for b := (*byte)(unsafe.Add(unsafe.Pointer(nil), p)); *b != 0; b = (*byte)(unsafe.Add(unsafe.Pointer(b), 1)) {
		*b = 'X'
	}
}

type recordingHost struct {
	*endpointHost
	seen []platform.BrowserRequest
}

func (h *recordingHost) Request(r platform.BrowserRequest) platform.RequestDecision {
	h.seen = append(h.seen, platform.BrowserRequest{MetadataSize: r.MetadataSize, Header: r.Header})
	return h.endpointHost.Request(r)
}

func newEndpointBrowser(t *testing.T, handler http.HandlerFunc) (*fakeNative, *browser, *recordingHost, *endpoint.Finite) {
	t.Helper()
	f, err := endpoint.NewFinite(handler, endpoint.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	host := &recordingHost{endpointHost: &endpointHost{fakeHost: fakeHost{ready: true}, f: f, drained: make(chan struct{})}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	config := platform.BrowserConfig{Mode: platform.BrowserInProcess}
	if err := f.Start(ctx, host.endpointHost, &config); err != nil {
		t.Fatal(err)
	}
	f.Bind(Origin)
	native, b := newTestBrowser(t, &host.fakeHost, platform.BrowserInProcess)
	b.host, b.metadata = host, int64(config.RequestMetadata)
	b.Start()
	native.complete()
	return native, b, host, f
}

func TestOversizedNativeMetadataIsRefusedBeforeAnyHeaderSlice(t *testing.T) {
	native, b, host, f := newEndpointBrowser(t, func(http.ResponseWriter, *http.Request) {
		t.Error("handler reached by oversized metadata")
	})
	var headers nativeHeaders
	var pairs []string
	for i := range 5000 { // far beyond the 64 KiB limit at 64 bytes or more each
		pairs = append(pairs, fmt.Sprintf("X-Header-%d", i), "value")
	}
	headers.install(t, pairs...)
	b.receive(801)
	seen := host.seen[0]
	if seen.Header != nil || seen.MetadataSize <= endpoint.DefaultMetadata {
		t.Fatalf("header slice built or size understated: %d headers, size %d", len(seen.Header), seen.MetadataSize)
	}
	if headers.steps >= len(headers.names)/2 {
		t.Fatalf("measurement walked %d of %d headers instead of stopping past the limit", headers.steps, len(headers.names))
	}
	if native.finished[801] != "response" || len(native.statuses) != 1 || native.statuses[0] != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("not answered 431: %v %v", native.finished, native.statuses)
	}
	if !strings.Contains(f.Status(), "admitted=0/") {
		t.Fatal(f.Status())
	}
}

func TestOrdinaryHeadersSurviveTheirNativeStorage(t *testing.T) {
	type observed struct {
		method, uri, test, accept string
		length                    int64
	}
	read := make(chan struct{})
	got := make(chan observed, 1)
	native, b, host, _ := newEndpointBrowser(t, func(_ http.ResponseWriter, r *http.Request) {
		<-read // only after the native storage was overwritten
		got <- observed{r.Method, r.URL.RequestURI(), r.Header.Get("X-Test"), r.Header.Get("Accept"), r.ContentLength}
	})
	var headers nativeHeaders
	headers.install(t, "X-Test", "one", "Accept", "text/plain", "Content-Length", "0")
	method, uri := cstr(t, "PUT"), cstr(t, Origin+"/api/edit?x=1")
	w.schemeMethod = func(uintptr) uintptr { return method }
	w.schemeURI = func(uintptr) uintptr { return uri }
	b.receive(901)
	seen := host.seen[0]
	if len(seen.Header) != 3 || cap(seen.Header) != 3 || seen.Header[0].Name != "X-Test" {
		t.Fatalf("header slice %v (cap %d)", seen.Header, cap(seen.Header))
	}
	// WebKit may reuse its storage once the synchronous call returns.
	for _, p := range append(append([]uintptr{method, uri}, headers.names...), headers.values...) {
		overwrite(p)
	}
	close(read)
	o := <-got
	if o != (observed{"PUT", "/api/edit?x=1", "one", "text/plain", 0}) {
		t.Fatalf("retained request data changed with native storage: %+v", o)
	}
	_ = native
}
