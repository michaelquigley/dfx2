package fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"dfx2webview2spike/adapt"
)

// get runs a request through both transports the spike uses: a real
// loopback server and the in-process adapter, and checks they agree.
func both(t *testing.T, method, path string, body []byte) (loop *http.Response, loopBody []byte, in adapt.Response) {
	t.Helper()
	h := Handler(t.Logf)
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	loop, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	loopBody, _ = io.ReadAll(loop.Body)
	loop.Body.Close()

	origin := &url.URL{Scheme: "https", Host: "dfx2-spike.invalid"}
	in = adapt.Serve(context.Background(), h, origin, &adapt.Request{Method: method, URL: "https://dfx2-spike.invalid" + path, Body: body})
	return
}

func TestModesAgree(t *testing.T) {
	cases := []struct {
		method, path string
		body         []byte
		status       int
		typ          string
	}{
		{"GET", "/", nil, 200, "text/html; charset=utf-8"},
		{"GET", "/app.js", nil, 200, "text/javascript; charset=utf-8"},
		{"GET", "/app.css", nil, 200, "text/css; charset=utf-8"},
		{"GET", "/api/query?text=h%C3%A9llo&n=1&n=2", nil, 200, "application/json; charset=utf-8"},
		{"POST", "/api/echo", []byte("héllo\x00\xff"), 200, "application/json; charset=utf-8"},
		{"GET", "/api/binary", nil, 200, "application/octet-stream"},
		{"GET", "/api/status/404", nil, 404, "application/json; charset=utf-8"},
		{"GET", "/api/status/500", nil, 500, "application/json; charset=utf-8"},
		{"GET", "/no/such/route", nil, 404, "text/plain; charset=utf-8"},
		{"GET", "/api/headers", nil, 200, "application/json; charset=utf-8"},
	}
	for _, c := range cases {
		loop, lb, in := both(t, c.method, c.path, c.body)
		if loop.StatusCode != c.status || in.Status != c.status {
			t.Errorf("%s %s: loopback %d, in-process %d, want %d", c.method, c.path, loop.StatusCode, in.Status, c.status)
		}
		if lt, it := loop.Header.Get("Content-Type"), in.Header.Get("Content-Type"); lt != c.typ || it != c.typ {
			t.Errorf("%s %s: types %q / %q, want %q", c.method, c.path, lt, it, c.typ)
		}
		if loop.Header.Get("Cache-Control") != "no-store" || in.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: Cache-Control missing", c.method, c.path)
		}
		if c.path != "/api/query?text=h%C3%A9llo&n=1&n=2" && c.path != "/api/echo" && !bytes.Equal(lb, in.Body) {
			t.Errorf("%s %s: bodies differ between modes", c.method, c.path)
		}
	}
}

func TestBinaryAndRepeatedHeaders(t *testing.T) {
	_, lb, in := both(t, "GET", "/api/binary", nil)
	if !bytes.Equal(lb, Binary) || !bytes.Equal(in.Body, Binary) {
		t.Error("binary changed")
	}
	loop, _, in := both(t, "GET", "/api/headers", nil)
	if v := loop.Header.Values("X-Spike-Repeat"); strings.Join(v, ",") != "one,two" {
		t.Errorf("loopback repeat %v", v)
	}
	block, _ := adapt.HeaderBlock(in.Header)
	if !strings.Contains(block, "X-Spike-Repeat: one\r\nX-Spike-Repeat: two") {
		t.Errorf("in-process block %q", block)
	}
}

func TestEchoAndQuery(t *testing.T) {
	_, lb, in := both(t, "POST", "/api/echo", []byte("héllo — 日本語"))
	for _, b := range [][]byte{lb, in.Body} {
		var j map[string]any
		json.Unmarshal(b, &j)
		if j["text"] != "héllo — 日本語" || j["length"].(float64) != float64(len("héllo — 日本語")) {
			t.Errorf("echo %s", b)
		}
	}
	_, lb, in = both(t, "GET", "/api/query?text=h%C3%A9llo&n=1&n=2", nil)
	for _, b := range [][]byte{lb, in.Body} {
		var j struct{ Query map[string][]string }
		json.Unmarshal(b, &j)
		if j.Query["text"][0] != "héllo" || len(j.Query["n"]) != 2 {
			t.Errorf("query %s", b)
		}
	}
}

func TestBigOverAdapterLimit(t *testing.T) {
	loop, lb, in := both(t, "GET", "/api/big", nil)
	if loop.StatusCode != 200 || len(lb) != BigSize {
		t.Errorf("loopback %d %d B", loop.StatusCode, len(lb))
	}
	if in.Status != 502 || in.Header.Get("X-Spike-Adapter-Error") != "response-too-large" {
		t.Errorf("in-process %d %v", in.Status, in.Header)
	}
}

func TestSlowHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	origin := &url.URL{Scheme: "https", Host: "dfx2-spike.invalid"}
	done := make(chan adapt.Response)
	go func() {
		done <- adapt.Serve(ctx, Handler(t.Logf), origin, &adapt.Request{Method: "GET", URL: "https://dfx2-spike.invalid/api/slow?tag=t"})
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	cancel()
	r := <-done
	if time.Since(start) > 500*time.Millisecond || !r.Canceled || r.Status != 503 {
		t.Fatalf("after %v: canceled %v status %d", time.Since(start), r.Canceled, r.Status)
	}
}
