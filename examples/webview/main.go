// Command webview auditions browser windows: in-process, owned loopback and
// attached-URL windows beside native ones, sharing one application lifetime.
//
// Interactively, -open names windows to open at startup. Driven, it reads
// commands on stdin and reports JSON events on stdout:
//
//	open LABEL inprocess|loopback      a window serving the audition page
//	open LABEL attach URL ORIGIN...    a window onto an independent service
//	native LABEL                       a plain native window
//	go LABEL URI                       navigate, through the window's policy
//	action LABEL retry|start           follow the error page's action
//	release LABEL                      let the page's /api/hold requests return
//	close LABEL | status | quit
//
// Page-driven probes post their results to the window's own handler, which
// reports them as "report" events; the host evaluates no JavaScript.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/platform/webkit"
	"github.com/michaelquigley/dfx2/webview"
)

type output struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (o *output) event(event string, values ...any) {
	line := map[string]any{"event": event, "time": time.Now().Format(time.RFC3339Nano)}
	for i := 0; i+1 < len(values); i += 2 {
		line[values[i].(string)] = values[i+1]
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = o.encoder.Encode(line)
}

type window struct {
	label, mode string
	browser     *webview.Window
	native      *dfx2.Window
	core        *appcore.Window
	page        *page
}

type host struct {
	o        *output
	app      *dfx2.App
	signal   syscall.Signal
	devtools bool
	windows  map[string]*window
	watchers sync.WaitGroup
}

func main() {
	gcSignal := flag.Int("gc-signal", 60, "real-time signal reserved for JavaScriptCore's garbage collector")
	resident := flag.Bool("resident", false, "keep running with no windows until quit")
	devtools := flag.Bool("devtools", false, "enable the browser inspector")
	open := flag.String("open", "", "comma-separated windows to open at startup, labelled by initial: inprocess (I), loopback (L), native (N)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := &host{o: &output{encoder: json.NewEncoder(os.Stdout)}, signal: syscall.Signal(*gcSignal), devtools: *devtools, windows: map[string]*window{}}
	mode := dfx2.WindowApplication
	if *resident {
		mode = dfx2.ResidentApplication
	}
	err := dfx2.Run(ctx, dfx2.Options{Mode: mode}, func(app *dfx2.App) error {
		h.app = app
		h.o.event("ready", "pid", os.Getpid(), "gc_signal", *gcSignal)
		for _, kind := range strings.Split(*open, ",") {
			switch label := strings.ToUpper(kind[:min(1, len(kind))]); kind {
			case "":
			case "native":
				h.command("native " + label)
			default:
				h.command("open " + label + " " + kind)
			}
		}
		return app.Go("commands", func(ctx context.Context) error { return h.read(ctx) })
	})
	h.watchers.Wait()
	h.o.event("return", "error", fmt.Sprint(err))
	if err != nil && !(errors.Is(err, context.Canceled) && ctx.Err() != nil) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// read forwards stdin commands to the UI thread. Blocking stdin is the
// caller's: closing it, or the app, ends the task.
func (h *host) read(ctx context.Context) error {
	lines := make(chan string)
	go func() {
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			lines <- s.Text()
		}
		close(lines)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if err := h.app.Post(func() { h.command(line) }); err != nil {
				return nil
			}
		}
	}
}

func (h *host) command(line string) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	fail := func(err error) { h.o.event("error", "command", line, "error", err.Error()) }
	switch {
	case f[0] == "open" && len(f) >= 3:
		if err := h.open(f[1], f[2], f[3:]); err != nil {
			fail(err)
		}
	case f[0] == "native" && len(f) == 2:
		w, err := h.app.NewWindow(dfx2.WindowOptions{Title: "dfx2 native " + f[1], Width: 480, Height: 240})
		if err != nil {
			fail(err)
			return
		}
		h.track(&window{label: f[1], mode: "native", native: w}, w.Done())
	case f[0] == "go" && len(f) == 3:
		h.drive(f[1], fail, func(b auditionBrowser) error { return b.AuditionLoad(f[2]) })
	case f[0] == "action" && len(f) == 3:
		h.drive(f[1], fail, func(b auditionBrowser) error { return b.AuditionErrorAction(f[2]) })
	case f[0] == "release" && len(f) == 2:
		if w := h.windows[f[1]]; w != nil && w.page != nil {
			w.page.Release()
		}
	case f[0] == "close" && len(f) == 2:
		if w := h.windows[f[1]]; w != nil {
			if w.browser != nil {
				w.browser.Close()
			} else {
				w.native.Close()
			}
		}
	case f[0] == "status":
		h.status()
	case f[0] == "quit":
		h.app.Quit()
	default:
		fail(errors.New("unknown command"))
	}
}

func (h *host) open(label, mode string, rest []string) error {
	if w := h.windows[label]; w != nil {
		return fmt.Errorf("label %s is open", label)
	}
	o := webview.Options{Window: dfx2.WindowOptions{Title: "dfx2 webview " + label + " (" + mode + ")", Width: 760, Height: 560},
		LinuxGCSignal: h.signal, DevTools: h.devtools}
	w := &window{label: label, mode: mode}
	switch mode {
	case "inprocess", "loopback":
		w.page = newPage(func(r json.RawMessage) { h.o.event("report", "window", label, "report", r) })
		o.Handler, o.Mode = w.page, webview.InProcess
		if mode == "loopback" {
			o.Mode = webview.Loopback
		}
	case "attach":
		if len(rest) < 2 {
			return errors.New("attach needs a URL and its allowed origins")
		}
		o.Mode, o.URL, o.Origins = webview.Attach, rest[0], rest[1:]
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	b, err := webview.Open(h.app, o)
	if b == nil {
		return err
	}
	browsers := appcore.BrowserWindows(h.app)
	if len(browsers) > 0 {
		w.core = browsers[len(browsers)-1] // the window Open just created
	}
	w.browser = b
	start := ""
	if a := h.audition(w); a != nil {
		start = a.AuditionState().Start
	}
	h.o.event("opened", "window", label, "mode", mode, "start", start, "error", fmt.Sprint(err))
	h.track(w, b.Done())
	return nil
}

func (h *host) track(w *window, done <-chan struct{}) {
	h.windows[w.label] = w
	h.watchers.Add(1)
	go func() {
		defer h.watchers.Done()
		<-done
		h.o.event("closed", "window", w.label, "mode", w.mode)
		_ = h.app.Post(func() {
			if h.windows[w.label] == w {
				delete(h.windows, w.label)
			}
		})
	}()
}

// auditionBrowser is the WebKitGTK attachment's internal audition surface.
type auditionBrowser interface {
	AuditionLoad(string) error
	AuditionErrorAction(string) error
	AuditionURI() (string, string)
	AuditionState() webkit.Audit
}

func (h *host) audition(w *window) auditionBrowser {
	if w == nil || w.core == nil {
		return nil
	}
	impl, err := appcore.AuditionBrowser(w.core)
	if err != nil {
		return nil
	}
	b, _ := impl.(auditionBrowser)
	return b
}

func (h *host) drive(label string, fail func(error), f func(auditionBrowser) error) {
	b := h.audition(h.windows[label])
	if b == nil {
		fail(errors.New("no live browser window"))
		return
	}
	if err := f(b); err != nil {
		fail(err)
	}
}

func (h *host) status() {
	windows := map[string]any{}
	for label, w := range h.windows {
		s := map[string]any{"mode": w.mode}
		if w.page != nil {
			s["handler"] = w.page.Counts()
		}
		if b := h.audition(w); b != nil {
			s["uri"], s["title"] = b.AuditionURI()
			s["browser"] = b.AuditionState()
		}
		if w.core != nil {
			if st, ok := appcore.InspectBrowser(w.core); ok {
				s["endpoint"], s["native"], s["released"] = st.Endpoint, st.Native, st.Released
			}
		}
		windows[label] = s
	}
	a := appcore.Inspect(h.app)
	h.o.event("status", "windows", windows, "core", map[string]any{"windows": a.Windows, "tasks": a.Tasks,
		"native_completions": a.NativeCompletions, "browsers": a.Browsers, "cleanups": a.BrowserCleanups, "closing": a.Closing})
}
