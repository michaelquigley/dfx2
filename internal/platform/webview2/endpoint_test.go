package webview2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/michaelquigley/dfx2/internal/endpoint"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// readEndpointHost forwards request ownership to the real endpoint. These
// aborted reads must never start a handler or publish a response.
type readEndpointHost struct {
	fakeHost
	t       *testing.T
	f       *endpoint.Finite
	drained int
}

func (h *readEndpointHost) Request(r platform.BrowserRequest) platform.RequestDecision {
	return h.f.Request(r)
}
func (h *readEndpointHost) RequestBody(id platform.ID, b []byte) platform.RequestDecision {
	return h.f.RequestBody(id, b)
}
func (h *readEndpointHost) RequestBodyEnd(id platform.ID, err error) platform.RequestDecision {
	return h.f.RequestBodyEnd(id, err)
}
func (h *readEndpointHost) RequestReleased(id platform.ID) {
	h.fakeHost.RequestReleased(id)
	h.f.RequestReleased(id)
}
func (h *readEndpointHost) Begin() (platform.ID, error) {
	h.t.Fatal("aborted read started a handler")
	return 0, nil
}
func (h *readEndpointHost) Publish(platform.ID, func(bool) error) {
	h.t.Error("aborted read published a completion")
}
func (h *readEndpointHost) Respond(platform.ID, platform.BrowserResponse) bool {
	h.t.Error("aborted read published a response")
	return false
}
func (h *readEndpointHost) Fail(err error) { h.t.Errorf("endpoint failed: %v", err) }
func (h *readEndpointHost) Drained()       { h.drained++ }

func newReadEndpoint(t *testing.T) (*readEndpointHost, context.CancelFunc) {
	t.Helper()
	f, err := endpoint.NewFinite(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("aborted read reached handler") }), endpoint.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	h := &readEndpointHost{t: t, f: f}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := f.Start(ctx, h, &platform.BrowserConfig{Mode: platform.BrowserInProcess}); err != nil {
		t.Fatal(err)
	}
	f.Bind("https://dfx2.invalid:443")
	return h, cancel
}

func TestBodyReadAbortReleasesRealEndpoint(t *testing.T) {
	for _, name := range []string{"read failure", "cancel at EOF", "cancel with final bytes"} {
		t.Run(name, func(t *testing.T) {
			native := installFake(t)
			h, cancel := newReadEndpoint(t)
			b := &browser{host: h}
			d := h.Request(platform.BrowserRequest{Method: "POST", URL: "https://dfx2.invalid/body", BodyLength: -1})
			if d.Action != platform.RequestRead || d.ID == 0 {
				t.Fatalf("not admitted for reading: %+v", d)
			}
			calls := 0
			n.streamRead = func(_ uintptr, buf []byte) (int, bool, hresult) {
				calls++
				if calls != 1 {
					t.Fatal("read continued after abort")
				}
				if name == "read failure" {
					return 0, false, eFail
				}
				cancel()
				h.f.Close()
				if h.drained != 0 {
					t.Fatal("endpoint drained before native release")
				}
				if name == "cancel with final bytes" {
					buf[0] = 'x'
					return 1, true, sOK
				}
				return 0, true, sOK
			}
			b.act(701, 555, d)
			if calls != 1 || len(h.releasedR) != 1 || h.releasedR[0] != d.ID {
				t.Fatalf("read calls=%d released=%v, want [%d]", calls, h.releasedR, d.ID)
			}
			for _, field := range []string{"admitted=0/", "native=0 ", "reserved=0/"} {
				if !strings.Contains(h.f.Status(), field) {
					t.Fatalf("ownership remains: %s", h.f.Status())
				}
			}
			cancel()
			h.f.Close()
			b.Close()
			native.flush()
			b.Close()
			if h.drained != 1 || len(h.releasedR) != 1 {
				t.Fatalf("drains=%d releases=%v", h.drained, h.releasedR)
			}
			if native.count("put-response") != 0 || len(native.deferrals) != 0 {
				t.Fatalf("abort installed native work: %v", native.calls)
			}
		})
	}
}

func TestInitialAbortWithoutIDReleasesNothing(t *testing.T) {
	native := installFake(t)
	h, cancel := newReadEndpoint(t)
	cancel()
	h.f.Close()
	d := h.Request(platform.BrowserRequest{Method: "POST", URL: "https://dfx2.invalid/body", BodyLength: -1})
	if d.Action != platform.RequestAbort || d.ID != 0 {
		t.Fatalf("not an ID-less refusal: %+v", d)
	}
	b := &browser{host: h}
	b.act(701, 0, d)
	b.Close()
	native.flush()
	if len(h.releasedR) != 0 || h.drained != 1 {
		t.Fatalf("released=%v drained=%d", h.releasedR, h.drained)
	}
}
