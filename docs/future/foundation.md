# dfx2 application and UI foundation

Draft proposal for discussion. The next implementation should establish one application lifetime for native drawing and webview windows, then exercise it with a small synthetic mixer. The public API below is a sketch, not implemented Go. Accepted decisions are recorded in [direction.md](direction.md); other choices here remain proposals.

The [foundation runtime work order](foundation-work-order.md) specifies the first executable slice only: application lifetime and native windows. Mixer controls and webview hosting below remain subsequent work. Mercurius reviewed the spec and work order together for that bounded slice and returned `ready_to_build` with no blocking findings on 2026-11-03. Michael requested its implementation handoff; broader design proposals remain subsequent work.

The [native/text findings](text.md) and [webview findings](webview.md) support designing this foundation. They establish working no-cgo paths on Linux and Windows, including worker wakeups and request ownership. They do not establish a complete widget toolkit, multi-window behavior, or production readiness. Sessionmixer supplies the first native use case; source compatibility with dfx and dfw is deliberately absent.

## Application and window ownership

Accepted: native immediate-mode windows and webview windows can coexist in one application, sharing its event loop, lifecycle machinery and services. Closing one leaves the others running. Each window initially has one content type; composition of native controls and webviews inside a single window is deferred.

An application owns the locked main thread, event loop, and its windows. Each window has a context, a closable work queue, and a lifetime that moves from starting to open to closing to closed. A window may go directly from starting to closing. Native drawing and webviews use the same ownership rules, with different content implementations.

`Run` is called from `main`, once per process. Its setup callback executes on the UI thread; returning from setup starts normal event dispatch. Returning an error starts cleanup. Creating a webview remains asynchronous: acquiring a window object does not mean its browser is ready. Creation callbacks may be reentrant and must see the state established before the native call.

```go
err := dfx2.Run(ctx, dfx2.Options{}, func(app *dfx2.App) error {
    mixer := newSyntheticMixer()
    w, err := ui.Open(app, dfx2.WindowOptions{
        Title: "Mixer study", Width: 480, Height: 420,
    }, mixer.Draw)
    if err != nil {
        return err
    }
    return w.Go("mixer", func(ctx context.Context) error {
        return mixer.Run(ctx, w.Invalidate)
    })
})
```

Accepted lifetime model: a window application begins shutdown when the last window closes. A resident application remains alive with no windows until explicitly quit, so shared services can continue and new windows can open. Window close cancels window-owned work; application-owned services live until application shutdown. Resident mode is explicit, not inferred from goroutine activity. The proposed default is window-application mode; exact configuration spelling remains open. Tray integration is separate from resident lifetime.

The sketch's `w.Go` registers cooperative background work before starting it. Closing cancels that work and tracks its completion. A service shared by several windows instead belongs to `app.Go`, or to the caller outside `Run`. A window never closes a service it merely uses.

For review, the initial failure policy treats unexpected registered-task, setup, window-initialization and backend failures as application failures: cancel and drain, then return the error from `Run`. Panics in framework-invoked application code become fatal errors with stack information, never unwinding through native callbacks. Expected service errors can be handled inside the owning task. Ordinary window close remains isolated, and queue-full, closed-owner and wrong-thread errors remain operation errors. This keeps the first runtime's failure behavior explicit without introducing recovery callbacks. It is a planning recommendation, not implied by acceptance of ordinary close behavior; the work order supplies error classification and startup ordering.

### Threading and posting

Creation, content callbacks, layout, native object access and destruction run on the UI thread. `Window.Post`, `Invalidate`, and `Close` are safe from other goroutines. `Post(func()) error` always queues, including when called on the UI thread; it never runs the function inline. Queues preserve accepted order and have a finite capacity, returning `ErrBusy` or `ErrClosed` without blocking the caller.

A successful post means accepted, not completed. Close discards ordinary queued callbacks that have not started. Posted functions therefore cannot be the sole owner of native cleanup or a completion notification another goroutine must receive. Native completions have a separate internal route that stays live while closing. The UI thread never blocks waiting for work that needs it to finish, and public calls do not introduce nested event loops.

`Invalidate` coalesces requests to draw. The worker publishes a synchronized snapshot before invalidating; the UI reads the latest snapshot when it draws. If another update arrives during drawing, the dirty state must survive for a subsequent frame. The wakeup carries no Go pointer. A failed native wake must not strand pending work; backend failure becomes an observable application error if it cannot be recovered.

### Closing

Accepted close stops new ordinary posts and invalidations, cancels the window context, and stops new content work. It settles intercepted requests and cancels outstanding native operations on their owning thread. The window can hide immediately, but the loop continues pumping until native callbacks release their objects. Late creation callbacks release their results instead of attaching them. Workers may finish after native teardown if they hold only Go data; their results are discarded. The window is closed only after its tracked work drains.

Accepted shutdown contract: application shutdown cancels its owned work and keeps the loop available for native cleanup callbacks. `Run` returns after that work has drained. Cancellation is cooperative: this does not promise a bounded return from an arbitrary stuck handler or native runtime. Stuck work must be reported as outstanding rather than mistaken for completed cleanup. The proposed diagnostic mechanism is a watchdog; it must not free objects still reachable by native callbacks. Hard process termination is a separate operator action.

The title-bar control and `Close` use this ordinary path. Close confirmation is deferred, with a future decision point before entering closing. Fatal startup/runtime errors and cancellation of the `Run` context initiate shutdown without a consent round. The executable owns OS-signal policy and can translate termination into context cancellation.

## The immediate-mode mixer

Sessionmixer already [samples meters independently of drawing](../../../sessionmixer/level_sampler.go) at roughly 30 Hz and [receives hardware changes on a background goroutine](../../../sessionmixer/session_monitor.go). Its [current fader](../../../sessionmixer/device_fader_ui.go) combines these values with an immediate-mode control. The new framework should make that pattern straightforward without importing ALSA or Scarlett policy into UI packages.

Accepted: the application owns meaningful values, while widgets retain interaction state such as focus, capture and drag position using stable keys. Widgets receive values and report user changes; external updates redraw without emitting user commands. Ordinary Go functions compose the interface, without a required `Component` base type or framework-owned application-state container.

Proposed identity details: each interactive widget has an explicit key within its parent's scope; use a stable channel ID, not its slice position, so reordering strips cannot transfer a drag to another channel. Duplicate keys are development errors. Removing a widget releases capture and focus; its transient state expires, so reintroducing it does not resume an old gesture.

```go
func (m *mixerUI) Draw(f *ui.Frame) {
    sample := m.source.Snapshot() // copied, synchronized data
    f.Row(ui.Layout{Gap: 12}, func(f *ui.Frame) {
        for _, channel := range sample.Channels {
            f.Scope(channel.ID, func(f *ui.Frame) {
                f.Column(ui.Layout{Width: 120, Gap: 8}, func(f *ui.Frame) {
                    f.Label(channel.Name)
                    f.Meter(channel.Level)
                    value, changed := f.Fader("gain", channel.Gain,
                        ui.Range{Min: 0, Max: 1})
                    if changed {
                        if err := m.commands.SetGain(channel.ID, value); err != nil {
                            m.status = err.Error()
                        }
                    }
                    muted, changed := f.Checkbox("mute", "Mute", channel.Muted)
                    if changed {
                        if err := m.commands.SetMute(channel.ID, muted); err != nil {
                            m.status = err.Error()
                        }
                    }
                })
            })
        }
    })
    f.Label(m.status)
}
```

`source` and `commands` are application-owned Go types, not framework services. These example commands enqueue without blocking, preserve order, and report a full queue visibly. The fixture has one command worker, whose failures are reflected in its published state. Framework wakeup coalescing must not silently collapse commands. Meter snapshots can replace older snapshots; user actions cannot be dropped by the same rule. The normalized fader is a fixture control; dB taper, ganging and hardware acknowledgements remain product policy.

`changed` means user input changed this control during this frame. Receiving a different value from hardware does not emit a user command. During an active drag, the widget retains its gesture value; external values can update the model without moving the thumb underneath the pointer. On release it emits the final user change, ends capture, then resumes following the supplied model value on subsequent frames. Whether the product retains an optimistic value until hardware acknowledgement is an application decision, not an implicit framework writeback.

Accepted execution model: the view runs once per frame. Layout and rendering retries reuse recorded output rather than executing application code again, and consumed input is not replayed on subsequent frames.

Proposed input details: input is routed using the last completed layout, with capture taking precedence; new controls become interactive after their first layout. Widgets record their updated visual value in the current scene. If user code changes state that earlier widgets already read, invalidate for another frame.

Accepted layout model: start with measured rows and columns, expressed through scoped callbacks and ordinary helper functions. Support gaps, padding, minimum/maximum sizes and flexible space, using logical units. Grid and absolute positioning wait for a concrete interface that needs them; API spelling remains open.

Proposed rendering details: the platform supplies pixel size and scale, rendering converts once, and input arrives in the same logical coordinate system. Containers clip their contents; exposure may re-present the existing image.

### Frames and animation

Accepted: input, invalidation, resize and scale changes request frames. Pending invalidations coalesce around the latest published state. Active animations request subsequent frames through one scheduler per window, paced to the display where supported. Once activity settles, the window has no permanent redraw timer.

Controls use a framework animation clock and helpers that retain presentation progress under stable keys. Progress uses monotonic elapsed time, not frame counts: a delayed frame advances to the appropriate position without replaying missed frames. Hidden or minimized windows can suspend drawing and catch up when shown. Animation changes visual presentation, not the application value or the widget's user-change result. Direct dragging follows the pointer immediately.

A meter source can publish at roughly 30 Hz while its display animates at the window's frame cadence. Attack, decay and peak hold use elapsed time. A fader can animate toward an externally supplied value without sending hardware commands. A spinner keeps requesting frames while busy and stops when the operation ends. Pausing the synthetic source leaves the window idle after any remaining visual transitions settle. The particular animation-helper API, interpolation policies and pacing fallback remain to be specified.

Use the pure-Go text path and CPU rendering for this first slice. Preserve an engine-neutral text seam and shared line assembly, without building a second engine merely to prove interchangeability. The Windows presentation allocation finding remains a backend constraint. This choice leaves the final text-engine and GPU-renderer decisions open.

## Webview transport

Accepted: keep the product boundary at `http.Handler`, with an explicit choice of in-process dispatch, an owned loopback server, or attachment to an existing server. There is no automatic fallback that changes origin or cancellation behavior. Owned endpoints cancel their work on window close; the initial in-process contract does not promise cancellation on fetch abort or page reload. API spelling and detailed limits remain proposals; an omitted transport selection would be a configuration error.

The spikes implement in-process dispatch through the browser's resource-loading hooks. Windows receives WebView2 `WebResourceRequested` callbacks for an intercepted HTTPS origin; Linux registers a WebKitGTK custom URI scheme. Each host copies the request into Go-owned data, runs `http.Handler.ServeHTTP` on a worker with a buffered response writer, then completes the native response on the UI thread. The page uses ordinary relative fetches and resource URLs. There is no application HTTP listener or application-defined IPC protocol on this path. Browser subprocesses still communicate through the engine's own internal IPC; “in-process” describes the Go endpoint dispatch, not the browser's process topology.

```go
_, err := webview.Open(app, webview.Options{
    Window: dfx2.WindowOptions{Title: "Handler study", Width: 900, Height: 650},
    Handler: handler,
    Transport: webview.InProcess,
    Limits: webview.Limits{
        RequestBytes: 1 << 20, ResponseBytes: 1 << 20,
        ConcurrentRequests: 16,
    },
})
```

This starts a window-owned endpoint. Selecting `Loopback` instead starts an owned HTTP listener. A separate attach operation takes an existing URL and owns only the browser window, preserving dfw's daemon/window topology. Attaching must not acquire ownership of that daemon or promise that window close cancels its handlers. Shared handlers must be concurrency-safe; closing one endpoint cancels its own requests, not another window's.

| Contract | In-process | Owned loopback |
| --- | --- | --- |
| Application HTTP listener | None; browser subprocesses still exist | Random port on loopback |
| Request and response | Finite, buffered; explicit size and concurrency limits | Ordinary `net/http` serving, configured by the application |
| Fetch abort or reload | No per-request cancellation promise in the initial implementation | Normal HTTP request-context behavior |
| Window close | Cancel handlers and settle native requests | Cancel endpoint context and stop its server |
| Streaming and upgrades | Reject unsupported use; no `Flusher` or `Hijacker` | Transport supports ordinary HTTP facilities; browser integration still needs tests |
| Origin | Assigned by backend; use relative URLs | Assigned local HTTP origin |

The example's 1 MiB limits reuse the spike's values, not a universal recommendation. Sixteen concurrent in-process requests is a proposed starting ceiling to validate under load. Admission must happen before buffering bodies; overflow receives an explicit failure. Configure a total byte budget as well, accounting for native copies, so concurrency limits actually bound buffering. A handler that ignores cancellation retains its slot until it returns; navigation must not create unbounded abandoned workers.

The endpoint remains alive across page reloads. Do not cancel all requests on every navigation as a substitute for missing per-request abandonment information. Close supplies the reliable lifetime boundary demonstrated by both spikes.

Before calling either host production-ready, specify navigation and subframe policy, browser-profile/storage lifetime, and loopback access control. A random local port is not authorization. A window-owned endpoint needs checks against unwanted origins/hosts and an application capability; cookie/header fidelity and redirects must be tested before choosing the capability's carriage. This draft does not turn the fixture's origin checks into a complete security contract. The first foundation webview fixture remains local and synthetic.

## Package boundaries and seam census

Accepted public surfaces are `dfx2` for application/window lifetime and UI-thread posting, `ui` for native immediate-mode controls/layout/animation, and `webview` for handler/URL hosting. Platform bindings, callback registries and native dispatch live under `internal/`. The root package must not pull in either content implementation. Native-only programs must not load WebKit or WebView2. Platform packages never import UI packages; an import test enforces the boundary. Narrow native-handle access waits for a concrete integration and explicit thread/lifetime rules.

Accepted boundaries are marked below; other recommendations remain for Michael's review:

| Boundary | Proposed call and reason | Revisit when |
| --- | --- | --- |
| Model / rendering | Accepted: separate application values from widget interaction state; ordinary Go functions compose the view | A concrete product needs a richer presentation model |
| Model / transport | Keep handlers at the edge; no browser-origin or HTTP types in mixer state | No anticipated reason to couple them |
| Platform / content | Accepted: private platform implementation; public `dfx2`, `ui` and `webview` surfaces; shared lifetime with separate content implementations | A concrete consumer needs a public platform API |
| Contract circumvention | UI-thread ownership and closable posting are mandatory; defer raw-handle escape hatches | A real native integration requires one with a scoped lifetime |
| Error by tier | Startup/owned-task failure returns through `Run`; command rejection is visible in the UI; HTTP failures remain responses | A product needs partial-window or service recovery |

## Proposed implementation sequence

The [runtime work order](foundation-work-order.md) expands the first item below into numbered implementation stages and concrete acceptance checks. The remaining items need later work orders; this outline does not authorize their implementation.

1. **Application lifetime.** Establish the root module, private platform contract, fake backend and native windows on Linux and Windows. Verify independent windows, resident operation and reopening, posting, cancellation and shutdown with a two-window fixture.
2. **Synthetic mixer.** Add CPU presentation, text, elementary layout and the four controls above. Drive meters and external fader changes independently. Audition drag, keyboard focus, reorder/removal during capture, resize, animation, pause and close on both hosts. Verify each input produces one command and external changes produce none. A fake clock checks scheduling; real desktop auditions check input and presentation.
3. **Webview on the same lifetime.** Port only the needed mechanisms from the experiments into optional hosts. Exercise a native window and a webview together, both transports, close-one/keep-one behavior, body reads and handlers pending at close, admission limits and child exits. Retain the original experiments as comparison evidence.
4. **Sessionmixer integration plan.** Use the synthetic result to scope a real strip and its controller, including command failure and hardware acknowledgement. Keep the existing application's hardware dependencies separate from dfx2's no-cgo guarantee. Expand toward the full product only after this slice feels right.

## Deferred and why

GPU rendering, the full layout/widget library, native/webview overlays, generated TypeScript, tray/discovery conveniences, persistent window state and macOS are outside the first slice: none is needed to test the common ownership model or mixer API. Begin with the demonstrated Linux/Windows amd64 paths; portability beyond them needs its own evidence.

IME, accessibility, mixed DPI and text editing remain product-readiness work. The initial non-editable mixer controls must retain semantic roles, labels and stable identity so later accessibility is not forced to reconstruct intent from pixels. Editable text needs composition, selection and caret contracts before it enters the widget set.

Monitor the WebKit child-exit issue as agreed; it does not block this design work. Supported-runtime validation and the separate no-cgo JSC signal-coexistence work remain Linux webview prerequisites. Neither the temporary runtime comparison nor a successful repeat audition counts as a production fix.
