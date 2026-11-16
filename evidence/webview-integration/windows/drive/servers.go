//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type record struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Cookie  string `json:"cookie,omitempty"`
	Origin  string `json:"origin,omitempty"`
	Upgrade string `json:"upgrade,omitempty"`
	Dest    string `json:"dest,omitempty"` // Sec-Fetch-Dest
	Remote  string `json:"remote"`         // the client port tells two connections apart
	T       string `json:"t"`
}

// route is what a recorder answers for a path: "drop" closes the connection
// without a response, a redirect carries a status and location, "hold" waits
// for release, and any other string is an HTML body.
type route struct {
	drop     bool
	status   int
	location string
	body     string
	hold     bool
	dropDocs bool // drop document requests, hold or answer the rest
}

// recorder is a collector or attachment server that records every request.
// Every reply closes its connection, so a stopped server is really
// unreachable: a kept-alive connection would otherwise keep answering.
type recorder struct {
	mu      sync.Mutex
	records []record
	reports []event
	routes  map[string]route
	static  string
	port    int
	srv     *http.Server
	ln      net.Listener
	cached  int
	held    chan struct{}
}

func newRecorder(routes map[string]route) *recorder {
	r := &recorder{routes: routes, held: make(chan struct{})}
	if r.routes == nil {
		r.routes = map[string]route{}
	}
	r.start()
	return r
}

func (r *recorder) start() {
	ln, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(r.port))
	if err != nil {
		panic(err)
	}
	r.ln = ln
	r.port = ln.Addr().(*net.TCPAddr).Port
	r.srv = &http.Server{Handler: http.HandlerFunc(r.handle), ReadHeaderTimeout: 10 * time.Second}
	r.srv.SetKeepAlivesEnabled(false)
	go r.srv.Serve(ln)
}

func (r *recorder) stop()          { r.srv.Close() }
func (r *recorder) origin() string { return "http://127.0.0.1:" + strconv.Itoa(r.port) }

func (r *recorder) set(path string, rt route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[path] = rt
}

// release lets held requests answer.
func (r *recorder) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.held:
	default:
		close(r.held)
	}
}

func (r *recorder) handle(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	rec := record{Method: req.Method, Path: req.URL.RequestURI(), Cookie: req.Header.Get("Cookie"), Origin: req.Header.Get("Origin"),
		Upgrade: req.Header.Get("Upgrade"), Dest: req.Header.Get("Sec-Fetch-Dest"), Remote: req.RemoteAddr, T: time.Now().Format("15:04:05.000")}
	r.mu.Lock()
	r.records = append(r.records, rec)
	rt, routed := r.routes[req.URL.Path]
	held := r.held
	r.mu.Unlock()
	w.Header().Set("Connection", "close")
	drop := func() {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				c.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	switch {
	case req.URL.Path == "/api/report" && req.Method == http.MethodPost:
		var e event
		if json.Unmarshal(body, &e) == nil {
			r.mu.Lock()
			r.reports = append(r.reports, e)
			r.mu.Unlock()
		}
		w.WriteHeader(204)
		return
	case routed && rt.drop:
		drop()
		return
	case routed && rt.dropDocs:
		if rec.Dest == "document" {
			drop()
			return
		}
		<-held
		reply(w, 200, "held response released", "text/plain")
		return
	case routed && rt.hold:
		<-held
		reply(w, 200, "held response released", "text/plain")
		return
	case routed && rt.status >= 300 && rt.status < 400:
		w.Header().Set("Location", rt.location)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(rt.status)
		return
	case routed && rt.status >= 400:
		reply(w, rt.status, rt.body, "text/html; charset=utf-8")
		return
	case routed && rt.body != "":
		reply(w, 200, rt.body, "text/html; charset=utf-8")
		return
	case strings.HasPrefix(req.URL.Path, "/post"):
		target := req.URL.Query().Get("target")
		reply(w, 200, `<!doctype html><title>posting</title><form id="f" method="post" action="`+target+`"><input name="x" value="1"></form>`+
			`<script>document.getElementById("f").submit()</script>`, "text/html; charset=utf-8")
		return
	}
	if r.static != "" {
		r.serveStatic(w, req, body)
		return
	}
	reply(w, 200, "<!doctype html><title>collected</title>", "text/html; charset=utf-8")
}

var staticTypes = map[string]string{"index.html": "text/html; charset=utf-8", "probe.js": "text/javascript; charset=utf-8", "probe.css": "text/css; charset=utf-8", "logo.svg": "image/svg+xml"}

// serveStatic serves the audition page and the API routes its probes use,
// so an attachment can run the same page-driven checks as an owned window.
func (r *recorder) serveStatic(w http.ResponseWriter, req *http.Request, body []byte) {
	name := strings.TrimPrefix(req.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	switch {
	case staticTypes[name] != "":
		b, err := os.ReadFile(filepath.Join(r.static, name))
		if err != nil {
			reply(w, 500, err.Error(), "text/plain")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		reply(w, 200, string(b), staticTypes[name])
	case req.URL.Path == "/api/query":
		reply(w, 200, `{"x":`+strconv.Quote(req.URL.Query().Get("x"))+`}`, "application/json")
	case req.URL.Path == "/api/echo":
		reply(w, 200, `{"length":`+strconv.Itoa(len(body))+`}`, "application/json")
	case req.URL.Path == "/api/cached":
		r.mu.Lock()
		r.cached++
		n := strconv.Itoa(r.cached)
		r.mu.Unlock()
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("X-Count", n)
		reply(w, 200, "cached "+n, "text/plain")
	default:
		reply(w, 404, "not found", "text/plain")
	}
}

func reply(w http.ResponseWriter, status int, body, ctype string) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func (r *recorder) hits(prefix string) []record {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []record
	for _, rec := range r.records {
		if strings.HasPrefix(rec.Path, prefix) {
			out = append(out, rec)
		}
	}
	return out
}

func (r *recorder) count(method, path string) int {
	n := 0
	for _, rec := range r.hits("") {
		if (method == "" || rec.Method == method) && strings.SplitN(rec.Path, "?", 2)[0] == path {
			n++
		}
	}
	return n
}

func (r *recorder) reportList() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event(nil), r.reports...)
}

func queryEscape(s string) string { return url.QueryEscape(s) }

// httpGet makes one plain client request and reports its status, -1 when the
// server is unreachable.
func httpGet(addr, path string, headers map[string]string) int {
	req, _ := http.NewRequest("GET", addr+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	tr := &http.Transport{DisableKeepAlives: true}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

// runAttached loads the page as ?run=NAME in an attached window and waits
// for the report the page posts to this service.
func (r *recorder) runAttached(h *host, label, start, name string, query map[string]string) event {
	id := fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
	u := start + "?run=" + name + "&id=" + id
	for k, v := range query {
		u += "&" + k + "=" + queryEscape(v)
	}
	since := len(r.reportList())
	h.send("go " + label + " " + u)
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		for _, rep := range r.reportList()[since:] {
			if rep.str("id") == id {
				require(rep.str("error") == "", "report %s: %s", id, rep.str("error"))
				return rep
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	fail("%s: no report %s reached the service", h.name, id)
	return nil
}
