package webview2

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/michaelquigley/dfx2/internal/platform"
)

// The in-place measure agrees with the conversion it predicts, surrogates and
// invalid sequences included.
func TestUTF8LengthMatchesConversion(t *testing.T) {
	cases := [][]uint16{nil, {'a'}, {0xE9}, {0x20AC}, {0xD83D, 0xDE00}, {0xD83D}, {0xDE00}, {0xDE00, 0xD83D}, {0xD83D, 'x'}}
	r := rand.New(rand.NewSource(1))
	for range 2000 {
		u := make([]uint16, r.Intn(12))
		for i := range u {
			u[i] = []uint16{'a', 0xE9, 0x20AC, 0xD83D, 0xDE00, 0xFFFF}[r.Intn(6)]
		}
		cases = append(cases, u)
	}
	for _, u := range cases {
		if got, want := utf8Length(u), len(string(utf16.Decode(u))); got != want {
			t.Fatalf("%x: measured %d, conversion %d", u, got, want)
		}
	}
}

func countTakes(t *testing.T) *int {
	taken := 0
	take := n.take
	n.take = func(p *uint16) string { taken++; return take(p) }
	return &taken
}

// A URI past the limit is refused by the endpoint's 431 without converting
// the URI, the method or any header; every native string is freed once.
func TestOversizedURIIsRefusedBeforeConversion(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 0, Action: platform.RequestAbort}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	b.metadata = 200
	f.request.uri, f.request.method = startURL+"?q="+strings.Repeat("é", 100), "POST"
	f.request.headers = [][2]string{{"X", "1"}}
	taken := countTakes(t)
	b.receive(560)
	in := host.requests[0]
	if in.URL != "" || in.Method != "" || in.Header != nil || in.MetadataSize <= 200 || *taken != 0 {
		t.Fatalf("oversized URI converted: %+v (taken %d)", in, *taken)
	}
	if bad := f.unfreed(); len(bad) != 0 {
		t.Fatalf("native strings freed %v times", bad)
	}
}

// Headers are limited by their exact UTF-8 length, not their UTF-16 units:
// a header whose units fit but whose bytes do not is never converted.
func TestHeadersAreLimitedByUTF8BeforeConversion(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 0, Action: platform.RequestAbort}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	f.request.uri, f.request.method = startURL+"api", "GET"
	base := platform.RequestRecordOverhead + len("GET") + len(f.request.uri)
	k := 50
	b.metadata = int64(base + platform.RequestHeaderOverhead + 1 + k + k/2) // units fit; UTF-8 (2 bytes each) does not
	f.request.headers = [][2]string{{"X", strings.Repeat("é", k)}}
	taken := countTakes(t)
	b.receive(561)
	in := host.requests[0]
	if in.Header != nil || in.MetadataSize != int64(base+platform.RequestHeaderOverhead+1+2*k) || *taken != 2 {
		t.Fatalf("header past the limit in bytes was converted: %+v (taken %d)", in, *taken)
	}
	if bad := f.unfreed(); len(bad) != 0 {
		t.Fatalf("native strings freed %v times", bad)
	}
}

// Accepted values are Go copies of exactly the measured size, intact after
// their native strings are freed, in a slice of exactly the header count.
func TestAcceptedMetadataIsOwnedAndExactlySized(t *testing.T) {
	host := &fakeHost{ready: true, decisions: []platform.RequestDecision{{ID: 1, Action: platform.RequestWait}}}
	f, b := newTestBrowser(t, host, platform.BrowserInProcess)
	started(t, f, b)
	f.request.uri, f.request.method = startURL+"api/é", "PUT"
	f.request.headers = [][2]string{{"X-A", "1"}, {"X-B", "😀é"}, {"Accept", "text/plain"}}
	b.receive(562)
	in := host.requests[0]
	if in.Method != "PUT" || in.URL != startURL+"api/é" || len(in.Header) != 3 || cap(in.Header) != 3 ||
		in.Header[1] != (platform.Header{Name: "X-B", Value: "😀é"}) || in.Header[2].Value != "text/plain" {
		t.Fatalf("accepted metadata %+v (cap %d)", in, cap(in.Header))
	}
	want := platform.RequestRecordOverhead + len("PUT") + len(in.URL)
	for _, h := range in.Header {
		want += len(h.Name) + len(h.Value) + platform.RequestHeaderOverhead
	}
	if in.MetadataSize != int64(want) {
		t.Fatalf("measured %d, converted %d", in.MetadataSize, want)
	}
	if bad := f.unfreed(); len(bad) != 0 {
		t.Fatalf("native strings freed %v times", bad)
	}
}
