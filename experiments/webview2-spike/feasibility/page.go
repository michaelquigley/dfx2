package feasibility

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

//go:embed static
var static embed.FS

var types = map[string]string{"index.html": "text/html; charset=utf-8", "probe.js": "text/javascript; charset=utf-8",
	"probe.css": "text/css; charset=utf-8", "logo.svg": "image/svg+xml"}

// Page is the probe application: an ordinary handler that knows nothing
// about how it is reached or protected. Its counts are per path.
type Page struct {
	mu        sync.Mutex
	counts    map[string]int
	cached    int
	streamAck chan struct{}
	// OnReport, when set, receives each page-driven result the page posts
	// to /api/report. It runs on the handler's goroutine.
	OnReport func(json.RawMessage)
}

func NewPage() *Page { return &Page{counts: map[string]int{}, streamAck: make(chan struct{}, 1)} }

// Counts returns the handler's per-path invocation counts.
func (p *Page) Counts() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int, len(p.counts))
	for k, v := range p.counts {
		out[k] = v
	}
	return out
}

// StreamWait bounds how long /api/stream waits for the page's
// acknowledgment of its first chunk.
var StreamWait = 5 * time.Second

func (p *Page) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		sum := sha256.Sum256(b)
		writeJSON(w, http.StatusOK, map[string]any{"length": len(b), "sha256": hex.EncodeToString(sum[:])})
	case "/api/form":
		// An edit POST: counted, then 303 to a GET, as a page form would be.
		http.Redirect(w, r, "/?posted="+strconv.Itoa(p.Counts()["POST /api/form"]), http.StatusSeeOther)
	case "/api/stream":
		// Chunked: the second chunk waits for the page to acknowledge the
		// first, so receiving both proves the first was visible early.
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming needs a flushing transport", http.StatusNotImplemented)
			return
		}
		select {
		case <-p.streamAck:
		default:
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "chunk-1\n")
		f.Flush()
		select {
		case <-p.streamAck:
			io.WriteString(w, "chunk-2 after ack\n")
		case <-time.After(StreamWait):
			io.WriteString(w, "chunk-2 without ack\n")
		case <-r.Context().Done():
			return
		}
		f.Flush()
		io.WriteString(w, "done\n")
	case "/api/stream-ack":
		select {
		case p.streamAck <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	case "/api/cached":
		// Cacheable, so a second request from the same session is answered by
		// that session's cache, while another session must ask the handler.
		p.mu.Lock()
		p.cached++
		n := p.cached
		p.mu.Unlock()
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("X-Count", strconv.Itoa(n))
		io.WriteString(w, "cached "+strconv.Itoa(n))
	case "/api/redirect":
		http.Redirect(w, r, "/api/query?x=redirected", http.StatusFound)
	case "/api/redirect-out":
		// An allowed URL that redirects to wherever it is told, including
		// another port: enforcement must apply after the redirect.
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	case "/api/abort":
		// A transport-level failure for the error page: no response at all.
		panic(http.ErrAbortHandler)
	case "/api/slow":
		// Pending work for close-with-a-request cases, in either transport.
		select {
		case <-time.After(3 * time.Second):
			io.WriteString(w, "slow finished\n")
		case <-r.Context().Done():
		}
	case "/api/count":
		writeJSON(w, http.StatusOK, p.Counts())
	case "/api/report":
		// A page-driven result: ordinary same-origin application traffic,
		// for hosts that evaluate no JavaScript natively.
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		if !json.Valid(b) {
			http.Error(w, "JSON only", http.StatusBadRequest)
			return
		}
		if p.OnReport != nil {
			p.OnReport(json.RawMessage(b))
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// Paths lists counted paths in order, for logs.
func (p *Page) Paths() []string {
	c := p.Counts()
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
