package adapt

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var origin = &url.URL{Scheme: "https", Host: "dfx2-spike.invalid"}

func TestCheckOrigin(t *testing.T) {
	good := []string{
		"https://dfx2-spike.invalid/",
		"https://dfx2-spike.invalid",
		"HTTPS://DFX2-SPIKE.INVALID/app.js",
		"https://dfx2-spike.invalid/api/query?x=1#frag",
	}
	for _, s := range good {
		if _, err := CheckOrigin(s, origin); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := []string{
		"http://dfx2-spike.invalid/",           // scheme
		"https://dfx2-spike.invalid.evil.com/", // prefix-alike host
		"https://dfx2-spike.invalid:8443/",     // port
		"https://user@dfx2-spike.invalid/",     // user info
		"https://evil.com/https://dfx2-spike.invalid/",
		"https:dfx2-spike.invalid", // opaque
		"//dfx2-spike.invalid/",    // no scheme
		"%zz",
	}
	for _, s := range bad {
		if _, err := CheckOrigin(s, origin); err == nil {
			t.Errorf("%s: accepted", s)
		}
	}
}

func TestRequestFidelity(t *testing.T) {
	body := []byte("héllo\r\n\x00\xff binary")
	in := &Request{
		Method: "POST",
		URL:    "https://dfx2-spike.invalid/api/echo?text=h%C3%A9llo&n=1&n=2&sp=a+b",
		Header: [][2]string{{"content-type", "application/octet-stream"}, {"X-Multi", "a"}, {"x-multi", "b"}, {"Host", "dfx2-spike.invalid"}},
		Body:   body,
	}
	var got *http.Request
	var gotBody []byte
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		gotBody, _ = io.ReadAll(r.Body)
	})
	resp := Serve(context.Background(), h, origin, in)
	if resp.Status != 200 || !resp.Ran {
		t.Fatalf("status %d ran %v", resp.Status, resp.Ran)
	}
	if got.Method != "POST" || got.URL.Path != "/api/echo" || got.RequestURI != "/api/echo?text=h%C3%A9llo&n=1&n=2&sp=a+b" || got.Host != "dfx2-spike.invalid" {
		t.Errorf("request line: %s %s %q host %s", got.Method, got.URL.Path, got.RequestURI, got.Host)
	}
	if q := got.URL.Query(); q.Get("text") != "héllo" || strings.Join(q["n"], ",") != "1,2" || q.Get("sp") != "a b" {
		t.Errorf("query %v", q)
	}
	if v := got.Header.Values("X-Multi"); strings.Join(v, ",") != "a,b" {
		t.Errorf("repeated request header: %v", v)
	}
	if got.Header.Get("Host") != "" {
		t.Error("Host left in Header")
	}
	if got.ContentLength != int64(len(body)) || !bytes.Equal(gotBody, body) {
		t.Errorf("body %q (Content-Length %d)", gotBody, got.ContentLength)
	}
	if got.URL.Scheme != "" || got.URL.Host != "" {
		t.Errorf("server requests carry no scheme/host in URL: %v", got.URL)
	}
}

func TestResponseFidelity(t *testing.T) {
	bin := make([]byte, 256)
	for i := range bin {
		bin[i] = byte(i)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Repeat", "one")
		w.Header().Add("X-Repeat", "two")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusTeapot)
		w.Header().Set("X-Late", "ignored") // after WriteHeader: not sent, as in net/http
		w.Write(bin[:100])
		w.Write(bin[100:])
	})
	resp := Serve(context.Background(), h, origin, &Request{Method: "GET", URL: "https://dfx2-spike.invalid/"})
	if resp.Status != 418 || resp.Reason != "I'm a teapot" {
		t.Errorf("status %d %q", resp.Status, resp.Reason)
	}
	if !bytes.Equal(resp.Body, bin) {
		t.Error("binary body changed")
	}
	block, dropped := HeaderBlock(resp.Header)
	if block != "Content-Type: application/octet-stream\r\nX-Repeat: one\r\nX-Repeat: two" || dropped != nil {
		t.Errorf("header block %q dropped %v", block, dropped)
	}
}

func TestSniffAndDefaults(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>hi") })
	resp := Serve(context.Background(), h, origin, &Request{URL: "https://dfx2-spike.invalid/"})
	if resp.Status != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("%d %q", resp.Status, resp.Header.Get("Content-Type"))
	}
	empty := Serve(context.Background(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), origin, &Request{URL: "https://dfx2-spike.invalid/"})
	if empty.Status != 200 || len(empty.Body) != 0 {
		t.Errorf("empty handler: %d %d B", empty.Status, len(empty.Body))
	}
}

func TestHeaderBlockDropsInjection(t *testing.T) {
	h := http.Header{"X-Bad": {"a\r\nSet-Cookie: x=1"}, "X-Good": {"ok"}, "Bad Name": {"v"}}
	block, dropped := HeaderBlock(h)
	if block != "X-Good: ok" || len(dropped) != 2 {
		t.Errorf("%q %v", block, dropped)
	}
}

func TestRequestLimit(t *testing.T) {
	ran := false
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true })
	at := Serve(context.Background(), h, origin, &Request{Method: "POST", URL: "https://dfx2-spike.invalid/", Body: make([]byte, Limit)})
	if at.Status != 200 || !ran {
		t.Fatalf("exactly Limit: %d ran %v", at.Status, ran)
	}
	ran = false
	over := Serve(context.Background(), h, origin, &Request{Method: "POST", URL: "https://dfx2-spike.invalid/", Body: make([]byte, Limit+1)})
	if over.Status != 413 || ran || over.Header.Get("X-Spike-Adapter-Error") != "request-too-large" {
		t.Fatalf("over Limit: %d ran %v %v", over.Status, ran, over.Header)
	}
	flagged := Serve(context.Background(), h, origin, &Request{Method: "POST", URL: "https://dfx2-spike.invalid/", Body: []byte("x"), BodyTooLarge: true})
	if flagged.Status != 413 || ran {
		t.Fatalf("BodyTooLarge: %d ran %v", flagged.Status, ran)
	}
}

func TestResponseLimit(t *testing.T) {
	var werr error
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64<<10)
		for i := 0; i < 32 && werr == nil; i++ {
			_, werr = w.Write(chunk)
		}
	})
	resp := Serve(context.Background(), h, origin, &Request{URL: "https://dfx2-spike.invalid/big"})
	if resp.Status != 502 || resp.Header.Get("X-Spike-Adapter-Error") != "response-too-large" || len(resp.Body) > 1024 {
		t.Fatalf("%d %v %d B", resp.Status, resp.Header, len(resp.Body))
	}
	if werr != ErrBodyTooLarge {
		t.Fatalf("handler saw %v", werr)
	}
	exact := Serve(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, Limit))
	}), origin, &Request{URL: "https://dfx2-spike.invalid/"})
	if exact.Status != 200 || len(exact.Body) != Limit {
		t.Fatalf("exactly Limit: %d %d", exact.Status, len(exact.Body))
	}
}

func TestNoStreamingInterfaces(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			t.Error("buffer advertises http.Flusher")
		}
		if _, ok := w.(http.Hijacker); ok {
			t.Error("buffer advertises http.Hijacker")
		}
		if _, ok := w.(io.ReaderFrom); ok {
			t.Error("buffer advertises io.ReaderFrom")
		}
		if err := http.NewResponseController(w).Flush(); err == nil {
			t.Error("ResponseController.Flush succeeded on a buffer")
		}
	})
	Serve(context.Background(), h, origin, &Request{URL: "https://dfx2-spike.invalid/"})
}

func TestPanicBecomes500(t *testing.T) {
	resp := Serve(context.Background(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), origin,
		&Request{URL: "https://dfx2-spike.invalid/"})
	if resp.Status != 500 || resp.Header.Get("X-Spike-Adapter-Error") != "handler-panic" {
		t.Fatalf("%d %v", resp.Status, resp.Header)
	}
}

func TestBadOriginRefused(t *testing.T) {
	ran := false
	resp := Serve(context.Background(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }), origin,
		&Request{URL: "https://dfx2-spike.invalid.evil.com/"})
	if resp.Status != 400 || ran {
		t.Fatalf("%d ran %v", resp.Status, ran)
	}
}
