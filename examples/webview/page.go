package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

//go:embed static
var static embed.FS

var types = map[string]string{"index.html": "text/html; charset=utf-8", "probe.js": "text/javascript; charset=utf-8",
	"probe.css": "text/css; charset=utf-8", "logo.svg": "image/svg+xml"}

// page is the audition application: an ordinary handler that knows nothing
// of how it is reached or protected. Each window gets its own, so its counts
// are that window's requests. Reports the page posts reach onReport.
type page struct {
	mu        sync.Mutex
	counts    map[string]int
	cached    int
	streamAck chan struct{}
	release   chan struct{} // closed to release /api/hold
	onReport  func(json.RawMessage)
}

func newPage(onReport func(json.RawMessage)) *page {
	return &page{counts: map[string]int{}, streamAck: make(chan struct{}, 1), release: make(chan struct{}), onReport: onReport}
}

func (p *page) Counts() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int, len(p.counts))
	for k, v := range p.counts {
		out[k] = v
	}
	return out
}

// Release lets every /api/hold request return.
func (p *page) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.release:
	default:
		close(p.release)
	}
}

func (p *page) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.counts[r.Method+" "+r.URL.Path]++
	p.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/", "/index.html", "/probe.js", "/probe.css", "/logo.svg":
		name := r.URL.Path[1:]
		if name == "" {
			name = "index.html"
		}
		b, _ := static.ReadFile("static/" + name)
		w.Header().Set("Content-Type", types[name])
		w.Write(b)
	case "/api/query":
		writeJSON(w, http.StatusOK, map[string]any{"x": r.URL.Query().Get("x")})
	case "/api/echo":
		b, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(b)
		writeJSON(w, http.StatusOK, map[string]any{"method": r.Method, "length": len(b), "sha256": hex.EncodeToString(sum[:])})
	case "/api/stream":
		// The second chunk waits for the page to acknowledge the first, so
		// receiving both proves the first arrived before the handler finished.
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming needs the loopback transport", http.StatusNotImplemented)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "chunk-1\n")
		f.Flush()
		select {
		case <-p.streamAck:
			io.WriteString(w, "chunk-2 after ack\n")
		case <-time.After(5 * time.Second):
			io.WriteString(w, "chunk-2 without ack\n")
		case <-r.Context().Done():
			return
		}
	case "/api/stream-ack":
		select {
		case p.streamAck <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	case "/api/cached":
		// Cacheable: a session's cache may answer it, another session cannot.
		p.mu.Lock()
		p.cached++
		n := p.cached
		p.mu.Unlock()
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("X-Count", strconv.Itoa(n))
		io.WriteString(w, "cached "+strconv.Itoa(n))
	case "/api/redirect-out":
		// Enforcement must still apply after a redirect, wherever it points.
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	case "/api/fail":
		// No response at all: a top-level load failure over loopback. The
		// in-process adapter answers a deliberate abort with a fixed 502.
		panic(http.ErrAbortHandler)
	case "/api/see-other":
		http.Redirect(w, r, "/api/fail", http.StatusSeeOther) // POST, then a failing GET
	case "/api/temporary":
		http.Redirect(w, r, "/api/fail", http.StatusTemporaryRedirect) // the POST itself is repeated, and fails
	case "/api/status":
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		if code < 400 || code > 599 {
			code = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		io.WriteString(w, "<!doctype html><title>status "+strconv.Itoa(code)+"</title><p>application status "+strconv.Itoa(code))
	case "/api/hold":
		// Pending work for close cases: returns when released, never on
		// cancellation alone.
		<-p.release
		io.WriteString(w, "released")
	case "/api/slow":
		select {
		case <-time.After(3 * time.Second):
			io.WriteString(w, "slow finished")
		case <-r.Context().Done():
		}
	case "/api/report":
		b, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		if r.Method != http.MethodPost || !json.Valid(b) {
			http.Error(w, "POST JSON only", http.StatusBadRequest)
			return
		}
		if p.onReport != nil {
			p.onReport(json.RawMessage(b))
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
