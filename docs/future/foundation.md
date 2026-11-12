# dfx2 foundation: remaining integration work

The application/window runtime and synthetic-mixer arc are accepted. Their built contracts live in [runtime.md](../current/runtime.md) and [ui.md](../current/ui.md); the [mixer acceptance record](../../evidence/synthetic-mixer/stage-4.md) preserves validation and limitations. The fulfilled mixer spec and work order are retired. This document retains the broader proposal's unfinished webview and Sessionmixer work; API examples below remain sketches, not implemented Go.

The next design slice puts a native UI window and a system webview in one application, sharing lifecycle and services while keeping their content implementations separate. The [webview findings](webview.md) establish mechanisms and gaps from independent experiments, not an integrated host. Accepted decisions remain in [direction.md](direction.md).

The focused [webview integration draft](webview-integration.md) refines this outline with the agreed mixed-mixer target, readiness behavior, explicit transports, navigation defaults and temporary per-window storage. It identifies the remaining mechanism and policy decisions before a work order. Use that draft for this arc; the sketches below remain background where it is more specific. The next executable task is its [native feasibility spike](webview-feasibility-spike.md), before the production work order.

## Browser lifetime proposal

Browser creation is asynchronous: receiving a window object does not establish browser readiness. Native completion callbacks may run before their creation call returns, or reentrantly during destruction. Establish state before calling native code; a late creation callback must release its result when the window is closing. The focused draft keeps readiness internal for ordinary opening and permits Close throughout startup.

Use the existing cancel-and-drain lifetime. Closing an owned endpoint stops admission, cancels handlers and native body reads, settles intercepted requests on the UI thread and continues pumping until native callbacks release their objects. The window may hide while draining. Workers that hold only copied Go data can finish after native teardown, with late results discarded, but remain tracked until they finish. Public Post must not carry cleanup that closing can discard. Stuck handlers or native operations stay visible as outstanding; no forced freeing or bounded shutdown promise. Closing an attached browser must not stop an independently owned server. Close confirmation remains deferred.

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

The first two items are accepted. The remaining items need focused design and work orders; this outline does not authorize their implementation.

1. **Application lifetime (built).** The root module, private platform contract, fake backend and native windows on Linux and Windows implement independent windows, resident operation and reopening, posting, cancellation and shutdown. The two-window fixture and retained host auditions validate this bounded slice.
2. **Synthetic mixer (built).** Native presentation, pure-Go text, measured layout, interactive controls and the application-owned synthetic model are accepted. [ui.md](../current/ui.md) and the [stage 4 record](../../evidence/synthetic-mixer/stage-4.md) carry the result, including the limited native coverage of post-return corrections.
3. **Webview on the same lifetime.** Port only the needed mechanisms from the experiments into optional hosts. Exercise a native window and a webview together, both transports, close-one/keep-one behavior, body reads and handlers pending at close, admission limits and child exits. Retain the original experiments as comparison evidence.
4. **Sessionmixer integration plan.** Use the synthetic result to scope a real strip and its controller, including command failure and hardware acknowledgement. Keep the existing application's hardware dependencies separate from dfx2's no-cgo guarantee. Expand toward the full product only after this slice feels right.

## Deferred and why

GPU rendering, grids and scrolling, menus/dialogs/docking, the full widget library, native/webview overlays, generated TypeScript, tray/discovery conveniences, persistent window state and macOS are outside the first slice: none is needed to test the common ownership model or mixer API. Begin with the demonstrated Linux/Windows amd64 paths; portability beyond them needs its own evidence.

IME, accessibility, mixed DPI and text editing remain product-readiness work. The built non-editable controls retain semantic roles, labels and stable identity; this does not constitute accessibility integration. Editable text needs composition, selection and caret contracts before it enters the widget set.

Monitor the WebKit child-exit issue as agreed; it does not block this design work. Supported-runtime validation and the separate no-cgo JSC signal-coexistence work remain Linux webview prerequisites. Neither the temporary runtime comparison nor a successful repeat audition counts as a production fix.

Fader appearance and mouse-wheel editing remain separate [visual](roadmap/improve-fader-appearance.md) and [input](roadmap/support-mouse-wheel-fader-edits.md) follow-ups from the manual audition. Device-specific units, taper, ganging, production meter ballistics and hardware acknowledgment belong to Sessionmixer. Native text engines, full color-font coverage and general interpolation helpers wait for a demonstrated need; the current implementation and cache-pressure costs are documented in [ui.md](../current/ui.md).
