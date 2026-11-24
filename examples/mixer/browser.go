package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"regexp"
	"syscall"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/webview"
)

// Browser views of the same model. The page polls one snapshot at a time and
// submits edits through the ordered command path; everything HTTP stays in
// this file, and the model and the command queue know nothing of it.

//go:embed web
var webFiles embed.FS

// The browser modes -browser selects.
const (
	browserInProcess = "inprocess"
	browserLoopback  = "loopback"
	browserAttach    = "attach"
)

func validBrowserMode(mode string) bool {
	return mode == "" || mode == browserInProcess || mode == browserLoopback || mode == browserAttach
}

// viewName is what a page may call itself: the attached page names its view
// in the query string, because one fixture server serves every attached window.
var viewName = regexp.MustCompile(`^[A-Za-z0-9-]{1,16}$`)

// pageHandler serves the page and its two endpoints over the model. name is the
// view's window, or empty for the shared fixture server, whose pages name
// themselves.
type pageHandler struct {
	m      *mixer
	name   string
	assets http.Handler
}

func (m *mixer) handler(name string) http.Handler {
	sub, _ := fs.Sub(webFiles, "web")
	return &pageHandler{m: m, name: name, assets: http.FileServerFS(sub)}
}

func (h *pageHandler) view(r *http.Request) string {
	if h.name != "" {
		return h.name
	}
	if v := r.URL.Query().Get("view"); viewName.MatchString(v) {
		return v
	}
	return "attach"
}

func (h *pageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/api/state":
		h.state(w, r)
	case "/api/edit":
		h.edit(w, r)
	case "/api/report":
		h.report(w, r)
	default:
		h.assets.ServeHTTP(w, r)
	}
}

// state answers one poll with the latest snapshot. It never submits anything.
func (h *pageHandler) state(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, stateOf(h.m.model.Snapshot(), h.view(r), h.m.c.audition))
}

type editRequest struct {
	Channel string          `json:"channel"`
	Control string          `json:"control"`
	Value   json.RawMessage `json:"value"`
}

// edit submits one user edit, exactly as a native view does, and reports the
// submission's outcome; the device's answer arrives in later snapshots. The
// page never retries.
func (h *pageHandler) edit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in editRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil || len(in.Channel) == 0 || len(in.Channel) > 8 {
		http.Error(w, "malformed edit", http.StatusBadRequest)
		return
	}
	cmd := Command{Window: h.view(r), Channel: in.Channel, Control: in.Control}
	switch in.Control {
	case "gain":
		var g float64
		if json.Unmarshal(in.Value, &g) != nil || math.IsNaN(g) || g < 0 || g > 1 {
			http.Error(w, "gain must be in [0, 1]", http.StatusBadRequest)
			return
		}
		cmd.Gain = g
	case "mute":
		if json.Unmarshal(in.Value, &cmd.Mute) != nil {
			http.Error(w, "mute must be true or false", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "control must be gain or mute", http.StatusBadRequest)
		return
	}
	seq, err := h.m.submit(cmd)
	out := map[string]any{"sequence": seq, "accepted": err == nil}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// report records what an audition page shows, for the automated audition. It
// exists only with -audition.
func (h *pageHandler) report(w http.ResponseWriter, r *http.Request) {
	if !h.m.c.audition || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var page map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&page); err != nil {
		http.Error(w, "malformed report", http.StatusBadRequest)
		return
	}
	h.m.o.event("page", "window", h.view(r), "page", page)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// The page's view of a snapshot: a plain copy, so the model's types never
// carry JSON concerns.
type stateJSON struct {
	View     string        `json:"view"`
	Audition bool          `json:"audition,omitempty"`
	Version  uint64        `json:"version"`
	Tick     uint64        `json:"tick"`
	Running  bool          `json:"running"`
	Channels []channelJSON `json:"channels"`
	Commands commandsJSON  `json:"commands"`
	Error    string        `json:"error"`
}

type channelJSON struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Level       float64 `json:"level"`
	DB          float64 `json:"db"`
	Gain        float64 `json:"gain"`
	Muted       bool    `json:"muted"`
	GainPending bool    `json:"gainPending"`
	PendingGain float64 `json:"pendingGain"`
	GainSeq     uint64  `json:"gainSeq"`
	MutePending bool    `json:"mutePending"`
	PendingMute bool    `json:"pendingMute"`
	MuteSeq     uint64  `json:"muteSeq"`
	ShownGain   float64 `json:"shownGain"`
	ShownMute   bool    `json:"shownMute"`
}

type commandsJSON struct {
	Accepted    uint64 `json:"accepted"`
	Applied     uint64 `json:"applied"`
	Refused     uint64 `json:"refused"`
	Rejected    uint64 `json:"rejected"`
	Canceled    uint64 `json:"canceled"`
	Outstanding int    `json:"outstanding"`
	DelayMS     int64  `json:"delayMs"`
	RejectNext  int    `json:"rejectNext"`
}

func stateOf(s *Snapshot, view string, audition bool) stateJSON {
	out := stateJSON{View: view, Audition: audition, Version: s.Version, Tick: s.Tick, Running: s.Running, Error: s.Error,
		Channels: make([]channelJSON, len(s.Channels)),
		Commands: commandsJSON{Accepted: s.Commands.Accepted, Applied: s.Commands.Applied, Refused: s.Commands.Refused,
			Rejected: s.Commands.Rejected, Canceled: s.Commands.Canceled, Outstanding: s.Commands.Outstanding,
			DelayMS: s.Commands.Delay.Milliseconds(), RejectNext: s.Commands.RejectNext}}
	for i, c := range s.Channels {
		out.Channels[i] = channelJSON{ID: c.ID, Name: c.Name, Level: c.Level, DB: c.DB, Gain: c.Gain, Muted: c.Muted,
			GainPending: c.GainPending, PendingGain: c.PendingGain, GainSeq: c.GainSeq,
			MutePending: c.MutePending, PendingMute: c.PendingMute, MuteSeq: c.MuteSeq,
			ShownGain: c.ShownGain(), ShownMute: c.ShownMute()}
	}
	return out
}

// startFixture starts the attachment audition's server: an application task,
// independent of every window, so closing an attached window leaves it
// serving. It listens on 127.0.0.1 without authentication; it is a fixture.
func (m *mixer) startFixture(a *dfx2.App) error {
	l, err := listenFixture("tcp", m.c.attachAddr)
	if err != nil {
		return fmt.Errorf("attach fixture server: %w", err)
	}
	m.fixture = "http://" + l.Addr().String()
	srv := &http.Server{Handler: m.handler(""), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: fixtureReadTimeout}
	m.o.event("fixture-server", "url", m.fixture)
	err = a.Go("fixture-server", func(ctx context.Context) error {
		done := make(chan error, 1)
		go func() { done <- srv.Serve(l) }()
		serving := func(err error) error { // Serve's own result: ErrServerClosed is the expected end
			if err == nil || errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("attach fixture server: %w", err)
		}
		// Shutdown has no deadline, on either path: the task ends only when
		// every handler has returned and every connection has closed.
		// The read timeouts bound a client that never finishes its request,
		// headers or body: its handler's read fails and the handler returns.
		shutdown := func() error {
			if err := srv.Shutdown(context.Background()); err != nil {
				return fmt.Errorf("attach fixture server shutdown: %w", err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			stopped := shutdown()
			return errors.Join(serving(<-done), stopped)
		case err := <-done: // Serve failed on its own: drain what it admitted
			return errors.Join(serving(err), shutdown())
		}
	})
	if err != nil {
		l.Close() // no task owns the listener
	}
	return err
}

// listenFixture opens the fixture server's listener; a test replaces it to
// fail the listener underneath a running server.
var listenFixture = net.Listen

// fixtureReadTimeout bounds reading one request, headers and body, so a
// stalled client cannot hold a handler, and with it shutdown, open forever.
// It is not a shutdown deadline: shutdown still waits for every handler.
var fixtureReadTimeout = 10 * time.Second

// openBrowser opens a browser view in the -browser mode. UI thread.
func (m *mixer) openBrowser() (*webview.Window, error) {
	m.opened++
	name := fmt.Sprintf("%c", 'A'+(m.opened-1)%26)
	o := webview.Options{Window: dfx2.WindowOptions{Title: "dfx2 mixer " + name + " (" + m.c.browser + ")", Width: 900, Height: 600},
		LinuxGCSignal: syscall.Signal(m.c.gcSignal), DevTools: m.c.devtools}
	switch m.c.browser {
	case browserInProcess:
		o.Mode, o.Handler = webview.InProcess, m.handler(name)
	case browserLoopback:
		o.Mode, o.Handler = webview.Loopback, m.handler(name)
	case browserAttach:
		o.Mode, o.URL, o.Origins = webview.Attach, m.fixture+"/?view="+name, []string{m.fixture}
	default:
		return nil, fmt.Errorf("no browser mode selected")
	}
	w, err := webview.Open(m.a, o)
	if err != nil {
		return nil, err
	}
	m.browsers[name] = w
	m.watchers.Add(1)
	go func() {
		defer m.watchers.Done()
		<-w.Done()
		_ = m.a.Post(func() {
			if m.browsers[name] == w {
				delete(m.browsers, name)
			}
		})
		m.o.event("closed", "window", name, "kind", "browser")
		m.o.event("ownership", status(m.a)...)
	}()
	m.o.event("opened", "window", name, "kind", "browser", "mode", m.c.browser)
	return w, nil
}
