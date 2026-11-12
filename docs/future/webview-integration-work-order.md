# Webview integration work order

Production plan paired with [webview-integration.md](webview-integration.md). Mercurius session `s_zlCzh4NrFu3w`, round 1, returned `ready_to_build` with no blocking findings; Michael accepted its one advisory, now included in stage 3's response-correlation test, and the review is closed. This translates the agreed browser behavior into the existing runtime. Review is not authorization to start implementation. The Linux signal mechanism below still requires Michael's decision; proposed API and packaging choices become commitments only when this pair is accepted.

## Result and boundaries

Deliver a public `webview` package alongside `ui`, with one application lifetime and three explicit modes: finite in-process `http.Handler`, window-owned loopback `http.Handler`, and attachment to an independently owned URL. Extend the synthetic mixer so native and browser windows edit the same application-owned model. Ship one application executable, build with `CGO_ENABLED=0`, and use the system browser runtime. Native-only startup must not load browser libraries or create browser storage.

Target Linux/amd64 GTK 3/WebKitGTK and Windows/amd64 Win32/WebView2. Keep browser hosting separate from CPU rendering; neither public native handles nor JavaScript RPC/evaluation are introduced. Browser subprocesses remain engine implementation details. Do not add a helper executable, automatic runtime installation, a new WebKit version floor, persistent profiles, external-client credentials, WebSockets, service-worker support or Sessionmixer hardware integration.

The existing spike implementations are references, not code to copy wholesale. Their standalone loops, instrumentation, unbounded convenience paths and fixture-specific error-page exceptions are not production architecture. Existing foundations remain authoritative in [runtime.md](../current/runtime.md) and [ui.md](../current/ui.md).

## Evidence and decisions before native integration

The [Linux feasibility record](../../experiments/webkitgtk-spike/evidence/linux/integration-feasibility/README.md) establishes the exercised browser paths on installed WebKitGTK 2.52.6, with a VM-free Go host. Its closeout passed 19 scenarios, the combined case and the original 14-case audition; Michael completed the error-page actions. The [Windows return](../../experiments/webview2-spike/evidence/windows/integration-feasibility/results.md) preserves 19/22, including the two intentionally isolated confinement failures and the old POST-count assertion. Michael accepted mandatory native interception plus CSP and a framework-action, not at-most-once-delivery, retry contract. Those decisions do not rewrite the raw result as 22/22.

The adjusted Windows `error-pages` driver has not run natively. Its targeted rerun is the first Windows task, before production host validation. Preserve both runs and report transport redelivery separately. A new failure returns for triage; do not repair it by weakening the action checks.

### Linux signal decision still required

Michael chose to monitor the reproduced signal risks during representative development, without further signal research, handler replacement, a helper, cgo adoption or a new runtime minimum. That decision did not select a production GC-signal configuration mechanism.

Proposed narrow mechanism for decision: the application supplies one explicitly reserved real-time signal through Linux browser options; before the first WebKit initialization, reuse the experiment's reservation/configuration sequence and private `JSConfigureSignalForGC` lookup. Missing symbol, inconsistent subsequent reservations or failed sanity checks refuse initialization visibly. Do not choose a signal automatically, initialize a host VM, patch fault handlers, change SIGXCPU, or add continuous signal auditing. No environment-variable fallback occurs silently. The first reservation is process-wide and survives closing every browser window.

The application must ensure it and its dependencies neither already use nor later subscribe to, ignore, reset or send that signal. Checks cannot discover all Go subscriptions. This moves JSC's handler; it does not make stray delivery safe. The alternative environment setting has inherited-process and warning behavior documented by the cgo spike. Neither mechanism is approved by this draft. Stage 1 can establish the portable contract, but stage 2 cannot implement Linux initialization until Michael chooses. Keep this a compatibility decision, not another open-ended safety spike.

Use the installed runtime for development and retain its version and child-exit observations. Do not claim graphics-teardown safety or a supported fixed-runtime pass from the mixed-package comparison. Missing required runtime functions are an initialization error with an actionable message; this does not authorize a new version floor.

### Windows single-executable packaging proposal

The experiment requires a separately supplied `WebView2Loader.dll`; production must not inherit that distribution requirement. Embed the pinned amd64 loader and its redistribution notices in the Windows browser package, extract it into a fresh application-private temporary directory on first browser use, verify its expected digest, and load only its absolute path. No download at startup, search-path fallback or executable helper. This still depends on the system WebView2 Runtime; it does not bundle that runtime.

Confirm the SDK's redistribution terms before adding the artifact. If they cannot support this proposal, return that specific blocker. Keep the loader loaded until every browser environment and callback has released. Then unload it before deleting its directory, using the same bounded post-release residue policy as profiles. A loader needed by a live browser is owned work, not deletable residue. Test running a copied application executable by itself, without SDK files beside it, and test native-only operation with no browser runtime available.

## Public API proposal

Use `webview.Open(app, options)` returning a small `webview.Window` wrapper and error, analogous to `ui.Open`. The wrapper forwards `Close`, `Context`, `Done`, `SetTitle`, `Post` and `Go`; it does not expose rendering, native handles, readiness, arbitrary navigation or JavaScript calls.

Options contain ordinary `dfx2.WindowOptions`, an explicitly nonzero mode, and mode-specific fields:

| Mode | Required | Rejected |
| --- | --- | --- |
| `InProcess` | Handler; optional finite limits | Attachment URL/origin list; loopback settings |
| `Loopback` | Handler; optional server limits | Attachment URL/origin list; finite response limits |
| `Attach` | Initial HTTP(S) URL and explicit allowed navigation origins containing its origin | Handler and owned-endpoint settings |

Owned modes start at `/`; the handler owns routing beneath it. Attachment URLs cannot contain user information. Canonicalize origins once, including effective ports, reject malformed origin entries, and compare parsed origins rather than string prefixes. Linux signal options apply to all browser modes on Linux and are inert on Windows; their final spelling waits for the decision above. Optional developer tools are off by default and enabled explicitly for auditions.

Validate configuration before creating resources. Match existing opening semantics: nil app is an immediate argument error; on a valid owner, wrong-thread/closed-owner errors retain their existing classification, and invalid creation configuration follows `NewWindow`'s fatal opening-error policy. A platform that cannot host browsers returns the existing unsupported-content category before native work and rechecks last-window shutdown. Missing libraries or failed browser initialization on a supported platform are fatal and retained in `Run`.

An accepted window is live even while its browser initializes. No synchronous wait or nested native loop is allowed. Cookie/filter/session installation gates first navigation internally. Ordinary response status, a failed page load and a rejected command do not mean initialization failed.

## Private boundaries grounded in the runtime

`internal/appcore/window.go` already registers a starting window before `Backend.Create`; `dispatch.go` settles internal completions and retains closing windows until their content release finishes. Extend those paths rather than creating an independent browser lifetime. `WindowOptions.Content` currently means a CPU drawing surface. Replace that boolean privately with explicit plain/drawn/browser kinds and preserve drawn-window behavior.

Keep the root dependency direction: `dfx2` must not import `webview` or the browser implementation. The public browser package chooses its platform-specific private browser factory and passes it through an internal appcore opening bridge, as `ui` passes its content implementation. The platform contract may describe browser hosting; it must not import appcore, HTTP handlers or public content packages.

Add a private browser-parent descriptor supplied by the existing native backend after shell creation. It contains only what the matching browser factory needs to attach to the GTK container or HWND, plus copied bounds; it never escapes through public API. Add copied bounds notifications for browser windows without enrolling them in frame/input scheduling. The factory creates a private browser attachment with start, bounds, close and diagnostics operations. Native browser packages import the platform contract, not appcore. Appcore owns the attachment and routes numeric completion identities. Do not build a general plugin registry or second GTK/Win32 pump.

Use a private portable endpoint package for HTTP adaptation, admission, credential checks and owned-server lifetime. It knows no GTK/COM types. HTTP handlers stay on workers; native callbacks only validate, admit, copy bounded data or settle native work. Application callbacks never run reentrantly inside browser callbacks.

The creation contract needs an explicit split: synchronous shell creation may fail only after releasing everything it acquired; once accepted, asynchronous browser startup owns a completion and reports later failure through the application failure path. Register routing and completion ownership before every native call that may complete synchronously. A late result after close is released, never attached or navigated.

## Ownership and completion

Track the browser attachment, endpoint lifetime, admitted requests, native body reads, response completions and profile/loader cleanup. Expose their counts/stages to the existing `Inspect`/shutdown supervisor, without logging credentials or response bodies. Extend internal diagnostics; do not add a public telemetry API.

The public 256-entry `Post` queue is unsuitable for native completion: close discards it. Add only the private bridge needed to register worker/native work before starting it, publish copied results from any goroutine, and settle on the owning UI thread even during shutdown. Reuse the existing operations map, wake mechanism and completion settlement. Bound per-request records by endpoint admission; lifecycle completions are a fixed number per window. No unbounded completion queue or goroutine waiting for a public queue slot.

Endpoint admission and sealing share a lock. A request rechecks owner cancellation while acquiring admission; the close boundary seals admission before cleanup can claim to have drained it. Track a worker until its actual return, including canceled requests and discarded responses. Never launch an untracked handler and then try to register it. Application-owned services remain under `app.Go`, not a browser window.

Close cancels/seals the endpoint, initiates native browser closure while its parent is valid, and keeps pumping internal completions. Native request cancellation and response release must not wait for a handler that ignores cancellation. Such a handler remains independently counted and keeps `Done`/`Run` pending. Release the parent shell only when browser objects no longer use it; retain the window record until endpoint workers, native callbacks and required storage cleanup have all finished. Unexpected parent loss takes the fatal path and still drains the attachment. Do not equate hidden, controller-closed or empty public queues with drained ownership.

On GTK defer final context/session destruction until a later main-loop turn after the last callback, following the spike's reproduced crash. On Windows account for COM completion handlers released without invocation, as observed during `GetCookies`; callback invocation and final release cannot each settle the same operation. Release controller/events/environment in the validated order, observe the owned environment's browser-process exit, and delete its profile only after all users are gone. Never kill shared engine processes to make a counter reach zero.

For profile and extracted-loader directories, propose 20 deletion attempts spaced 100 ms apart after native release. Run filesystem work off the UI thread and track it. On exhaustion preserve an error naming the path alongside prior failures and finish; never reuse the residue. Live native objects or a missing required exit event have no such timeout escape. Crash leftovers are documented, not swept by a new garbage collector.

Unexpected handler panics retain a stack, cancel the application and drain in both owned modes. Report failure before net/http's outer recovery can hide it; abort a partially written loopback response without exposing the stack. Treat `http.ErrAbortHandler` as a deliberate request abort, not an application failure. Preserve distinct cleanup errors using the existing error-record identity behavior; do not redesign error aggregation in this arc.

## HTTP contract and bounded adaptation

### Finite in-process adapter

Implement a private request/response adapter independent of the native APIs. Copy method, URL path/query, supported headers and body into a Go request with the window endpoint context; set Host from the assigned origin. Report synthetic transport honestly: no invented TCP peer or TLS state. Do not promise HTTP protocol negotiation, trailers, informational responses, upgrades or browser-visible header formatting identical to loopback. Document the supported response headers, duplicate handling and HEAD/no-body statuses; reject unsupported response features before publishing success.

The accepted defaults are 1 MiB request body, 1 MiB response body, 64 KiB metadata per direction, 16 admitted requests and 64 MiB aggregate adapter buffers per endpoint. Zero configuration selects defaults; negative or inconsistent limits fail validation. Metadata charging includes UTF-8 bytes for method/URL/status text and every header name/value occurrence, plus bounded record overhead. Count allocation capacity and concurrent copies, including native request copies, native response streams and queued results; do not charge only final slice lengths. Reserve before allocation and transfer/release the reservation with ownership. Reject declarations larger than remaining capacity before reading; unknown lengths stop at the cap plus a bounded detection byte.

Native reads must be bounded and must not block the UI thread on a stream supplied by a browser. Use WebKit's asynchronous stream reading; establish the correct Windows stream/thread mechanism from the adapter and COM contracts before using it. A worker may not use an apartment-bound interface merely because it has an AddRef. If safe bounded reading needs a native mechanism the spike did not establish, return that concrete implementation blocker rather than making the UI thread wait.

Admission happens before body accumulation or handler start. No waiting queue: excess work receives 503, oversized request bodies 413, oversized metadata 431, oversized response body or metadata 502. A response writer records overflow even if the handler ignores its `Write` error; publish no successful prefix. Supply only the supported buffered `http.ResponseWriter` surface, with no `Flusher`, `Hijacker` or escape through `Unwrap`. Framework-known unsupported response features fail locally; an unexpected panic from a handler's failed interface assertion still follows the panic policy.

Keep failure responses fixed and tiny. Reserve a fixed emergency-response allowance in endpoint accounting, and bound native refusal delivery without admitting new handler work. Exhaustion of that native delivery capacity aborts/rejects at the browser API without an allocated queued response; it must not create unlimited 503 operations. Document that a native resource failure can appear as a fetch/load failure rather than an HTTP status when even bounded error delivery cannot be created.

Reload/fetch abort may leave a handler running. Admission, buffers and completion ownership persist until their respective users release them. Close is the reliable cancellation boundary. Prove pressure recovery with handlers held on barriers and with late results after close, not sleeps or counters sampled before dispatch finishes.

### Owned loopback

Use `127.0.0.1:0` and a per-window server. Proposed defaults: 64 KiB request headers, 5 s header-read timeout, 30 s idle timeout, 32 live connections and 16 concurrent handlers. Admission failures do not wait in an application queue. Close excess connections before serving them; refuse handler overload with 503. Account for connections from acceptance through closure. Preserve streaming: no finite response cap, global write deadline or mandatory buffering. Request bodies remain streamed under handler control and close cancellation.

Expose `Flusher` while preserving failure tracking. Do not expose `Hijacker`, and reject upgrade requests in this slice, so no unowned upgraded connection can escape. Close seals admission, cancels request contexts, closes listener and connections, and independently waits for every handler. `http.Server.Shutdown` or `Close` returning is not proof that handlers returned. A listener failure is fatal; ordinary malformed/disconnected requests remain local.

Generate a cryptographically random per-endpoint secret; install an HttpOnly session cookie in the isolated browser before navigation. Check constant-time credential equality, exact Host including port, and any supplied Origin before dispatching the application handler. Missing Origin is allowed only with valid capability and Host. Keep access checking separate from browser cookie installation so a later external-client policy does not rewrite transport ownership. Do not add that policy now.

## Browser policy and native behavior

Owned windows require navigation checks, native request confinement and framework-enforced CSP together. On Linux use the demonstrated content filter; on Windows intercept every available resource context and refuse off-origin requests, including redirects. Navigation cancellation alone is not credited with stopping network delivery. CSP must be enforced on owned documents in both transports, including handler errors and redirects as applicable; handler headers may tighten it but cannot remove or relax the framework policy.

Use a self-contained page baseline: scripts/styles/assets/connect limited to the assigned origin, no objects, child frames or external form targets. No inline script dependency in the mixer. Deny new windows, downloads and permission requests, and disable or deny service-worker use rather than claiming it covered by ordinary interception. If a native API cannot enforce a required restriction, report it before claiming confinement. Developer tools remain an explicit developer option, never a reason to expose host JS APIs.

For attachments, allow top-level navigation only to the explicit origins; propose refusing popups and subframes consistently. Do not inject an owned endpoint's cookie, CSP or access policy into the remote service. Subresource behavior belongs to that service; its allowed navigation list is not a claim of network confinement. Use an isolated temporary session nonetheless.

Linux sessions are in-memory and per window; Windows environments use fresh private per-window directories. Validate cookies and web storage across two windows, reload and reopen. Distinguish cache isolation evidence from tests where nothing was cached. No host-side JSC result conversion, script messages, JS contexts or evaluation bindings on Linux; page-driven HTTP reports support auditions without violating that boundary.

Implement the built-in error page as a private local document, independent of endpoint availability. Admit only the framework's specific pending document/action state, not arbitrary `data:` or error-page-looking URLs. Bind actions to the current failed top-level navigation and invalidate stale actions on later navigation/close. Escape error text and omit credentials and stack traces.

Retry is offered only for a known failed GET, using the final attempted method across redirects; unknown method gets Return to start only. Return starts the configured initial URL with GET. No automatic retry, reload or POST replay. Correlate native response observations with the current top-level navigation, so an old request or a redirect's HTTP status cannot turn a failed load into a reported success. Actual 404/500 pages display normally. Use the Windows response-observation lesson without copying the fixture's loose URI-only correlation.

Browser-process failure is fatal; load failure is local. Classify the actual native notifications and normal close/child-exit cases explicitly. Keep faults during shutdown as cleanup errors when appropriate. No automatic restart and no production machinery whose only purpose is monitoring the accepted Linux signal risk.

## Implementation stages

Each stage updates built behavior and evidence, runs Terminus against the reviewed candidate, and stops for Michael's review and commit. Resolve findings one at a time under the grimoire rule. A clean review is not native validation or acceptance. Do not begin the next stage while its prerequisite decisions or host evidence are pending.

### Stage 1 — portable lifetime and endpoint contracts

Implement the public wrapper/configuration, private browser-parent/attachment contract, fake browser, core ownership bridge, finite adapter and owned loopback server. Keep real backends explicitly unsupported for browser content until their stages; preserve native/drawn behavior. Add `docs/current/webview.md` describing only what exists and `evidence/webview-integration/stage-1.md`.

Discriminating tests cover starting-window accounting, unsupported refusal after a reentrant last-window close, close during each asynchronous stage, late results, completion release without invocation, cross-window cancellation, handlers held after close, cleanup failure after an earlier failure, bounded request/copy accounting, limit errors and recovery, loopback streaming/authentication/connection drain, and handler panic versus ordinary HTTP failure. Use barriers and fake pump-boundary observations. Demonstrate the assertions fail against the relevant broken behavior; no mutation-count target or large new test framework.

Run the full no-cgo suite/vet, affected concurrency repetitions, Windows cross-build/vet and unsupported-platform build checks. Align the dfx2 canon deliberately before stage acceptance, through its own workflow; do not let the implementer quietly edit sibling canon or treat absent webview rules as a reason to skip review. Native lifecycle regressions run if the changed shared-core/native code affects their binary.

### Stage 2 — Linux browser integration

After the signal decision, implement the private WebKitGTK host in the existing GTK loop: lazy loading, per-window sessions, three modes, cookie/filter setup, bounded request handling, load-error page, native failure classification and asynchronous release. No host VM. Native-only operation remains browser-independent. Fill in `docs/current/webview.md` and `evidence/webview-integration/stage-2.md` with runtime requirements and monitored risks.

Run integrated Linux cases on Wayland and X11/XWayland, including scale 2 where available: both owned transports, attachment-server survival, two-window routing/storage isolation, streaming, rejected external requests with credential collectors, HTTP errors, GET/POST/303/307/unknown-method error actions, close during initialization/read/handler/completion, resident reopen, browser failure and profile/session release. Use page-driven reports. Run affected foundation/UI regressions and record child exits. Test in-process operation without an application listener. Do not reopen the signal investigation as an acceptance ritual; retain the VM-free API restriction and report representative failures.

Obtain Michael's Linux manual browser audition. Stage 2 does not claim Windows support. Stop for review/commit before the Windows stage.

### Stage 3 — Windows browser integration and return

First rerun the adjusted spike `error-pages` case and retain its result. Then implement the private WebView2 host, pinned embedded-loader packaging, per-window environments/profiles, cookies, native interception plus CSP, bounded stream/response ownership, error actions and release accounting. Cross-builds and modeled COM tests may be prepared on Linux; only Windows can close native claims.

Write the Windows handoff before transfer, naming the exact committed candidate, commands, retained outputs and manual steps. Michael commits, transfers to the Windows host, and brings back its committed results. Native tests include the Linux-equivalent browser matrix, loader extraction and executable-only deployment, missing runtime, synchronous/reentrant/late initialization, dropped completion handlers, waiting for process exit before profile deletion, and native/browser windows coexisting during a Win32 size/move loop. Confirm copied data and HTTP completions progress without breaking the bounded dispatch behavior already built.

Include a controlled response-correlation race: hold an earlier fetch or navigation response for a URL until a later top-level navigation to that same URL fails, then release the old response. Verify that it cannot suppress the newer load's error page or change its Retry/Return actions. Order the case with barriers or explicit fixture events, not a timing assumption.

Manual audition covers error actions, two windows, resizing, closing either first and resident reopen. Record runtime/SDK/build, DPI and RDP conditions; untested DPI or lock/disconnect scenarios stay untested rather than inferred. Return failures for triage, including a shutdown pending with owned resources. Rebuild/test on Linux after the return and rerun affected native cases when product code changes. Review the returned tree with Terminus before stage acceptance.

### Stage 4 — mixed mixer and acceptance

Extend `examples/mixer`, retaining its native-only default and Makefile install convention. Add explicit flags for a native window plus a browser in each owned transport, and an attachment audition using a separately owned fixture server. Serve embedded assets and thin HTTP handlers over the existing application-owned synthetic model/command path; no browser or HTTP types enter model state. Polling has one outstanding request, edits are not automatically retried, and snapshots/reconciliation do not create new commands. Do not build generic deduplication or a production session/device server into the fixture.

Both views show channel names, levels, gain, mute and the existing pending/delayed/rejected outcomes. Test edits from either side, channel reorder/removal, close either first, last-window cancellation, resident reopen and independently owned server survival. Shared workers outlive individual requests/windows as already designed. Make pending edit behavior visible without hardening unrelated fixture paths.

Run the integrated browser matrix and affected foundation/UI regressions on both native hosts, with a commit/Windows-return/Linux-return cycle as in stage 3. Michael manually auditions the mixed views on each host. Evidence records exact revisions/hashes, native runtimes, failures and missing coverage; raw logs are not rewritten. Final Terminus review, explicit canon alignment and Michael's acceptance close the arc. Only then retire realized intent documents, moving any live follow-ups to smaller future documents and accepted residual behavior to current docs.

## Handoff and review discipline

Never substitute spike results, cross-builds, a binary hash or a clean review for a new native claim. A matching hash can carry forward unchanged runtime evidence; a changed test driver still needs its changed assertions exercised. Keep the old signal probes and unexplained historical failures as context, not new prerequisites to solve.

The implementer may settle local names and helper placement within these contracts. Return for a decision when a native mechanism cannot satisfy ownership, confinement, optional loading or packaging, or when the Linux signal choice is needed. Do not silently add cgo, another process, a weaker authentication boundary, a new runtime floor or a timeout that declares live work drained. Ordinary layout, counters and fixture wiring do not require a new permission loop.
