// Package webview opens dfx2 windows that show web content in the system
// browser, sharing the application lifetime with native windows.
//
// Every window selects one hosting mode explicitly, and there is no fallback
// between them:
//
//   - InProcess serves an http.Handler through the browser's native request
//     interception, with finite buffered requests and responses and no
//     application listener.
//   - Loopback serves an http.Handler from a window-owned server on
//     127.0.0.1, authenticated by a per-window credential cookie; ordinary
//     HTTP facilities, streaming included, apply.
//   - Attach opens an independently owned URL. The window owns its browser
//     only; closing it never stops the service.
//
// Open returns once the window is accepted; the browser initializes
// asynchronously and loads its first page when ready. Close works throughout.
// Closing a window cancels the work its endpoint owns, and Done waits for
// every admitted handler to return. Browser storage is temporary and isolated
// per window.
//
// Linux/amd64 hosts browsers in WebKitGTK 4.1, loaded on the first Open, and
// requires Options.LinuxGCSignal. Other platforms refuse with
// ErrUnsupportedContent before any native work; the Windows host arrives in
// its own stage.
package webview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/michaelquigley/dfx2"
	"github.com/michaelquigley/dfx2/internal/appcore"
	"github.com/michaelquigley/dfx2/internal/endpoint"
	"github.com/michaelquigley/dfx2/internal/origin"
	"github.com/michaelquigley/dfx2/internal/platform"
)

// Mode selects how a window's content is served. The zero value is invalid:
// a mode must be chosen.
type Mode uint8

const (
	InProcess Mode = iota + 1
	Loopback
	Attach
)

func (m Mode) String() string {
	switch m {
	case InProcess:
		return "in-process"
	case Loopback:
		return "loopback"
	case Attach:
		return "attach"
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

// Options configure one browser window.
type Options struct {
	Window dfx2.WindowOptions
	Mode   Mode
	// Handler serves InProcess and Loopback windows from "/". A handler shared
	// across windows must be safe for concurrent use.
	Handler http.Handler
	// Limits bound an InProcess endpoint; zero fields select defaults.
	Limits Limits
	// Server bounds a Loopback endpoint; zero fields select defaults.
	Server ServerLimits
	// URL is an Attach window's initial and Return-to-start page: absolute
	// http or https, without user information.
	URL string
	// Origins lists the origins an Attach window may navigate to, including
	// URL's own. Entries are origins only: scheme, host and optional port.
	Origins []string
	// DevTools enables the browser's inspector, for development.
	DevTools bool
	// LinuxGCSignal is a real-time signal (SIGRTMIN to SIGRTMAX, from 35 on
	// glibc) that the application reserves for WebKit's JavaScriptCore garbage
	// collector. It is required on Linux and inert elsewhere; it is never
	// chosen automatically. The first browser window hands it to JavaScriptCore
	// for the life of the process, and every later window must name the same
	// signal. The application promises that neither it nor its dependencies
	// use, subscribe to, ignore, reset or send that signal: any delivery of it
	// can kill the process. The framework's checks cannot see Go signal.Notify
	// subscriptions.
	LinuxGCSignal syscall.Signal
}

// Limits bound one in-process endpoint. They cover adapter-owned storage,
// native copies included, not the browser's or the handler's own memory.
type Limits struct {
	RequestBody  int64 // default 1 MiB; larger requests get 413
	ResponseBody int64 // default 1 MiB; larger responses fail with 502
	Metadata     int   // default 64 KiB per request or response; request 431, response 502
	Requests     int   // admitted requests, default 16; excess get 503
	Buffers      int64 // aggregate adapter buffers, default 64 MiB; exhaustion gives 503
}

// ServerLimits bound one owned loopback server.
type ServerLimits struct {
	HeaderBytes       int           // default 64 KiB
	ReadHeaderTimeout time.Duration // default 5 s
	IdleTimeout       time.Duration // default 30 s
	Connections       int           // live connections, default 32; excess are closed unserved
	Handlers          int           // concurrent handlers, default 16; excess get 503
}

// ErrUnsupportedContent reports a platform that cannot host browsers yet. It
// is returned before any native work and is not an application failure.
var ErrUnsupportedContent = platform.ErrUnsupportedContent

// factory selects this platform's browser implementation. Nil means browser
// hosting is unsupported. Only this package selects it, so applications that
// never open a webview never load a browser library.
var factory = platformFactory

// Window is a dfx2 window showing web content. It forwards the ordinary window
// operations, with their thread rules. It exposes no native resources,
// readiness, navigation or JavaScript calls.
type Window struct {
	w *dfx2.Window
}

// Open creates a browser window on the UI thread. A nil app is an argument
// error. Otherwise Open follows App.NewWindow: wrong-thread and closed-owner
// errors are operation errors, while invalid options and creation failures
// are returned and latched as application failures. A platform without
// browser hosting returns an error matching ErrUnsupportedContent and latches
// nothing.
func Open(app *dfx2.App, options Options) (*Window, error) {
	if app == nil {
		return nil, errors.New("dfx2 webview.Open: nil app")
	}
	spec := appcore.BrowserSpec{Factory: factory()}
	spec.Config, spec.Endpoint, spec.Err = configure(options)
	w, err := appcore.OpenBrowser(app, options.Window, spec)
	if w == nil {
		return nil, err
	}
	return &Window{w: w}, err
}

// configure validates options without acquiring resources.
func configure(o Options) (platform.BrowserConfig, appcore.Endpoint, error) {
	config := platform.BrowserConfig{DevTools: o.DevTools, GCSignal: int(o.LinuxGCSignal)}
	if o.LinuxGCSignal < 0 || (needsGCSignal && o.LinuxGCSignal == 0) {
		return config, nil, errors.New("Linux browser windows need Options.LinuxGCSignal: a real-time signal the application reserves for JavaScriptCore")
	}
	attachSet := o.URL != "" || len(o.Origins) != 0
	switch o.Mode {
	case InProcess:
		if attachSet || o.Server != (ServerLimits{}) {
			return config, nil, errors.New("in-process mode accepts no URL, origins or loopback limits")
		}
		e, err := endpoint.NewFinite(o.Handler, endpoint.Limits(o.Limits))
		if err != nil {
			return config, nil, err
		}
		config.Mode = platform.BrowserInProcess
		return config, e, nil
	case Loopback:
		if attachSet || o.Limits != (Limits{}) {
			return config, nil, errors.New("loopback mode accepts no URL, origins or in-process limits")
		}
		e, err := endpoint.NewLoopback(o.Handler, endpoint.ServerLimits(o.Server))
		if err != nil {
			return config, nil, err
		}
		config.Mode = platform.BrowserLoopback
		return config, e, nil
	case Attach:
		if o.Handler != nil || o.Limits != (Limits{}) || o.Server != (ServerLimits{}) {
			return config, nil, errors.New("attach mode accepts no handler or endpoint limits")
		}
		start, origins, err := attachment(o.URL, o.Origins)
		if err != nil {
			return config, nil, err
		}
		config.Mode, config.StartURL, config.Origins = platform.BrowserAttach, start, origins
		return config, nil, nil
	}
	return config, nil, fmt.Errorf("invalid mode %d: choose InProcess, Loopback or Attach", o.Mode)
}

func attachment(raw string, entries []string) (string, []string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", nil, fmt.Errorf("attach URL: %w", err)
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return "", nil, fmt.Errorf("attach URL %q must be absolute http or https", u.Redacted())
	}
	start, err := origin.Of(raw)
	if err != nil {
		return "", nil, fmt.Errorf("attach URL: %w", err)
	}
	if len(entries) == 0 {
		return "", nil, errors.New("attach mode requires explicit allowed origins")
	}
	var origins []string
	for _, entry := range entries {
		o, err := origin.Parse(entry)
		if err != nil {
			return "", nil, fmt.Errorf("allowed origin %q: %w", entry, err)
		}
		if !slices.Contains(origins, o) {
			origins = append(origins, o)
		}
	}
	if !slices.Contains(origins, start) {
		return "", nil, fmt.Errorf("allowed origins must include the URL's origin %s", start)
	}
	return u.String(), origins, nil
}

// Close seals admission, cancels the endpoint's work and requests native
// cleanup without waiting; any goroutine.
func (w *Window) Close() { w.w.Close() }

// Context is canceled when the window starts closing; any goroutine.
func (w *Window) Context() context.Context { return w.w.Context() }

// Done closes after the browser has released its native objects, every
// handler the endpoint admitted has returned, temporary storage cleanup has
// finished and owned tasks have drained. Never wait for it on the UI thread.
func (w *Window) Done() <-chan struct{} { return w.w.Done() }

// SetTitle is a UI-thread operation.
func (w *Window) SetTitle(title string) error { return w.w.SetTitle(title) }

// Post queues f for the UI thread without waiting; any goroutine.
func (w *Window) Post(f func()) error { return w.w.Post(f) }

// Go registers a task owned by this window; UI thread.
func (w *Window) Go(name string, work func(context.Context) error) error { return w.w.Go(name, work) }
