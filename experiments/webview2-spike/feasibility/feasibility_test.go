package feasibility

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func guarded(t *testing.T) (*httptest.Server, *Counters, string) {
	t.Helper()
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	c := &Counters{}
	page := NewPage()
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	host := srv.Listener.Addr().String()
	srv.Config.Handler = Guard(Count(page, c), token, host, "http://"+host, OwnedCSP, c)
	t.Cleanup(srv.Close)
	return srv, c, token
}

func get(t *testing.T, srv *httptest.Server, path string, edit func(*http.Request)) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	if edit != nil {
		edit(req)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The handler never runs for a request without the exact credential, Host
// and, when present, Origin; an absent Origin is allowed only with the
// credential.
func TestGuardRefusesBeforeTheHandler(t *testing.T) {
	srv, c, token := guarded(t)
	cookie := func(v string) func(*http.Request) {
		return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: v}) }
	}
	cases := []struct {
		name   string
		edit   func(*http.Request)
		status int
	}{
		{"no credential", nil, 403},
		{"wrong credential", cookie("x" + token), 403},
		{"wrong host", func(r *http.Request) { cookie(token)(r); r.Host = "localhost" }, 403},
		{"wrong origin", func(r *http.Request) { cookie(token)(r); r.Header.Set("Origin", "http://127.0.0.1:1") }, 403},
		{"no origin with credential", cookie(token), 200},
		{"exact origin", func(r *http.Request) { cookie(token)(r); r.Header.Set("Origin", srv.URL) }, 200},
	}
	for _, tc := range cases {
		if got := get(t, srv, "/api/query?x=1", tc.edit).StatusCode; got != tc.status {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.status)
		}
	}
	s := c.Snapshot()
	if s.Handled != 2 || s.Admitted != 2 || s.Refused["no-credential"] != 1 || s.Refused["wrong-credential"] != 1 || s.Refused["host"] != 1 || s.Refused["origin"] != 1 {
		t.Fatalf("counters %+v", s)
	}
}

// The application sees its own cookies, never the credential, and admitted
// responses carry the owned policy beside any of the handler's.
func TestGuardStripsOnlyTheCredentialAndAddsPolicy(t *testing.T) {
	token := "t"
	var seen string
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Cookie")
		w.Header().Add("Content-Security-Policy", "img-src *")
	}), token, "h", "http://h", OwnedCSP, &Counters{})
	r := httptest.NewRequest("GET", "http://h/", nil)
	r.Host = "h"
	r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	r.AddCookie(&http.Cookie{Name: "vis", Value: "A"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if seen != "vis=A" {
		t.Fatalf("handler saw cookies %q", seen)
	}
	if got := w.Header().Values("Content-Security-Policy"); len(got) != 2 || got[0] != OwnedCSP {
		t.Fatalf("policies %q", got)
	}
}

// The stream's second chunk waits for the acknowledgment of the first.
func TestStreamSendsTheFirstChunkBeforeItsAcknowledgment(t *testing.T) {
	srv, _, token := guarded(t)
	resp := get(t, srv, "/api/stream", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: token}) })
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "chunk-1\n" {
		t.Fatalf("first chunk %q %v", line, err)
	}
	get(t, srv, "/api/stream-ack", func(r *http.Request) { r.Method = "POST"; r.AddCookie(&http.Cookie{Name: CookieName, Value: token}) })
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "chunk-2 after ack") {
		t.Fatalf("rest %q", rest)
	}
}

func TestRemovalWaitsForReleaseAndNamesResidue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	os.MkdirAll(filepath.Join(dir, "data"), 0o700)
	if _, err := RemoveAfterRelease(dir, false, 3, time.Millisecond, nil); !errors.Is(err, ErrNotReleased) {
		t.Fatalf("unreleased: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("an unreleased directory was touched")
	}
	// Injected: deletion keeps failing after release.
	calls := 0
	r, err := RemoveAfterRelease(dir, true, 3, time.Millisecond, func(string) error { calls++; return errors.New("injected: sharing violation") })
	if err == nil || r.Removed || r.Attempts != 3 || calls != 3 || !strings.Contains(err.Error(), dir) {
		t.Fatalf("persistent failure: %+v %v", r, err)
	}
	// Injected: fails twice, then the real removal succeeds.
	calls = 0
	r, err = RemoveAfterRelease(dir, true, 5, time.Millisecond, func(p string) error {
		calls++
		if calls < 3 {
			return errors.New("injected: busy")
		}
		return os.RemoveAll(p)
	})
	if err != nil || !r.Removed || r.Attempts != 3 {
		t.Fatalf("eventual success: %+v %v", r, err)
	}
}

func TestReportReachesTheHookOnlyAsValidJSON(t *testing.T) {
	page := NewPage()
	var got []string
	page.OnReport = func(b json.RawMessage) { got = append(got, string(b)) }
	for _, c := range []struct {
		method, body string
		status       int
	}{{"POST", `{"id":"a","result":1}`, 204}, {"POST", "not json", 400}, {"GET", "", 405}} {
		rec := httptest.NewRecorder()
		page.ServeHTTP(rec, httptest.NewRequest(c.method, "/api/report", strings.NewReader(c.body)))
		if rec.Code != c.status {
			t.Errorf("%s %q: status %d, want %d", c.method, c.body, rec.Code, c.status)
		}
	}
	if len(got) != 1 || got[0] != `{"id":"a","result":1}` {
		t.Errorf("hook got %q", got)
	}
}
