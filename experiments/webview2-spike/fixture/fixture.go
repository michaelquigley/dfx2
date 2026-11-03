// Package fixture is the spike's one application: an embedded page and a
// Go http.Handler that both transport modes serve unchanged. The handler
// owns its routes and content; it knows nothing about how it is reached.
package fixture

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

//go:embed static
var static embed.FS

// assets are served with explicit types: mime.TypeByExtension reads the
// registry on Windows, which could make the bytes differ by host.
var assets = map[string]string{
	"index.html": "text/html; charset=utf-8",
	"app.css":    "text/css; charset=utf-8",
	"app.js":     "text/javascript; charset=utf-8",
}

// SlowDelay is how long /api/slow takes when nothing cancels it.
const SlowDelay = 3 * time.Second

// Binary is /api/binary's body: every byte value once, then sequences that
// a careless text path would mangle (NUL, CR LF, invalid UTF-8, a BOM).
var Binary = func() []byte {
	b := make([]byte, 0, 256+16)
	for i := 0; i < 256; i++ {
		b = append(b, byte(i))
	}
	return append(b, 0x00, '\r', '\n', 0xff, 0xfe, 0xc3, 0x28, 0xef, 0xbb, 0xbf, 0x00)
}()

// BigSize is /api/big's body length: 2 MiB, over the in-process adapter's
// limit and nothing special to loopback.
const BigSize = 2 << 20

// Handler returns the fixture. logf receives page reports (/api/report)
// and slow-handler outcomes; it may be nil.
func Handler(logf func(string, ...any)) http.Handler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", asset("index.html"))
	for name := range assets {
		mux.HandleFunc("GET /"+name, asset(name))
	}
	mux.HandleFunc("GET /api/query", query)
	mux.HandleFunc("POST /api/echo", echo)
	mux.HandleFunc("GET /api/binary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(Binary)))
		w.Write(Binary)
	})
	mux.HandleFunc("GET /api/status/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.PathValue("code"))
		if err != nil || (code != 404 && code != 500) {
			http.Error(w, "only 404 and 500 are offered", http.StatusBadRequest)
			return
		}
		writeJSON(w, code, map[string]any{"status": code, "from": "handler"})
	})
	mux.HandleFunc("GET /api/headers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Spike-Repeat", "one")
		w.Header().Add("X-Spike-Repeat", "two")
		w.Header().Set("X-Spike-Unicode", "café") // raw UTF-8 in a header value
		writeJSON(w, http.StatusOK, map[string]any{"sent": map[string][]string{
			"X-Spike-Repeat": {"one", "two"}, "X-Spike-Unicode": {"café"},
		}})
	})
	mux.HandleFunc("GET /api/slow", func(w http.ResponseWriter, r *http.Request) {
		tag := r.URL.Query().Get("tag")
		t := time.NewTimer(SlowDelay)
		defer t.Stop()
		select {
		case <-t.C:
			writeJSON(w, http.StatusOK, map[string]any{"slow": "finished", "tag": tag, "ms": SlowDelay.Milliseconds()})
		case <-r.Context().Done():
			logf("fixture: /api/slow tag=%q saw cancellation: %v", tag, r.Context().Err())
			// Nobody is listening, but return something well-formed.
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"slow": "canceled", "tag": tag})
		}
	})
	mux.HandleFunc("GET /api/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(BigSize))
		chunk := make([]byte, 64<<10)
		for i := range chunk {
			chunk[i] = byte(i * 7)
		}
		for n := 0; n < BigSize; n += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				logf("fixture: /api/big stopped after %d bytes: %v", n, err)
				return
			}
		}
	})
	mux.HandleFunc("POST /api/report", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		logf("report: %s", b)
		w.WriteHeader(http.StatusNoContent)
	})
	return noStore(mux)
}

// noStore marks every response, including the mux's own 404s, so a cache
// can't answer in place of the handler.
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func asset(name string) http.HandlerFunc {
	b, err := static.ReadFile("static/" + name)
	if err != nil {
		panic(err)
	}
	typ := assets[name]
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", typ)
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	}
}

func query(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"method":   r.Method,
		"path":     r.URL.Path,
		"rawQuery": r.URL.RawQuery,
		"query":    r.URL.Query(),
		"host":     r.Host,
		"accept":   r.Header.Get("Accept"),
		"custom":   r.Header.Values("X-Spike-Request"),
	})
}

func echo(w http.ResponseWriter, r *http.Request) {
	// The handler's own cap is generous: limits on the in-process path are
	// the adapter's, and must be visible as the adapter's.
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(b)
	out := map[string]any{
		"method":        r.Method,
		"contentType":   r.Header.Get("Content-Type"),
		"contentLength": r.ContentLength,
		"length":        len(b),
		"sha256":        hex.EncodeToString(sum[:]),
		"custom":        r.Header.Values("X-Spike-Request"),
	}
	if utf8.Valid(b) && len(b) <= 4096 {
		out["text"] = string(b)
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, fmt.Sprint(err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(b)
}
