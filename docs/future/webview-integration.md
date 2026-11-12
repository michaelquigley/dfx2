# Webview integration

Draft for joint review with the [production work order](webview-integration-work-order.md). This narrows the unfinished browser portion of [foundation.md](foundation.md) into the next implementation arc after the accepted synthetic mixer. Michael agreed the mixed-window target, readiness behavior, explicit transports, finite-adapter limits, navigation defaults and temporary per-window storage in the design discussion. This document records those decisions and proposes the remaining details. Neither draft authorizes implementation. Public API spelling and mechanisms explicitly identified as proposals await acceptance.

Built contracts remain in [runtime.md](../current/runtime.md) and [ui.md](../current/ui.md). The [webview findings](webview.md) and independent experiments supply comparison evidence, not an integrated host or a complete endpoint access policy.

Distribution priority (2026-11-12): applications should ship as one executable, using system browser libraries. The isolated [Linux cgo comparison](webview-cgo-spike.md) found no signal-coexistence benefit. Michael chose to monitor the observed risks during representative development, without adopting cgo, a helper, new handler manipulation or a new runtime version floor. Production planning retains the no-cgo host and browser subprocesses. The production GC-signal mechanism still needs an explicit decision; the experiment's reservation is not production approval. The work order also proposes embedding/extracting the Windows loader to remove the spike's separate distribution file.

## The target

One application opens a native mixer window and a webview onto the same application-owned synthetic model. Both show channel names, levels, gain and mute. Editing either submits commands through the same application path; both show the resulting values. Device delay and rejection remain visible without turning snapshots or reconciliation into new commands. Closing either window leaves the other and the shared model running. Closing the last window in window-application mode cancels the shared workers and drains the application.

The web page uses ordinary relative fetches: poll an owned snapshot and POST an edit. The same assets and Go `http.Handler` run with explicitly selected in-process or owned loopback transport. Polling is a fixture choice, not a framework update protocol. Keep at most one snapshot poll outstanding per page; no catch-up backlog is needed. A native-only invocation continues to work without loading a browser runtime.

An additional attachment case opens the same page from a separately owned test server. Closing that browser leaves the server usable. This establishes the daemon/window ownership needed by dfw-style applications without building a daemon or tray framework.

This is an integration exercise, not a Sessionmixer migration. Keep the existing synthetic command policy, expose it through a thin HTTP adapter, and avoid unrelated fixture hardening or new UI controls.

## Scope and boundaries

Target Linux/amd64 with GTK 3 and WebKitGTK, and Windows/amd64 with Win32 and WebView2. Everything builds with `CGO_ENABLED=0`. Each window has one content type; mixing native drawing and a browser within one window is outside this arc.

Public surfaces remain `dfx2` for application lifetime, `ui` for native views and `webview` for browser hosting. Browser libraries and raw platform types remain private and optional. The root package must not acquire a dependency on the UI or browser implementation. Native callbacks are permanent trampolines routed by numeric identity, not callbacks allocated per request or window. No Go pointer survives in native ownership.

The existing platform content flag describes framework drawing, not arbitrary browser hosting. The work order must extend that private boundary deliberately; a webview must not masquerade as a CPU image surface or obtain a public native handle to bypass lifetime tracking.

## Opening, readiness and closing

Agreed: `webview.Open` returns a window once creation is accepted. Browser initialization continues asynchronously, and the framework loads the initial page when the browser is ready. Successful opening does not promise that the browser exists yet or that the page has loaded. An ordinary handler-backed application needs no public readiness callback. Do not block the UI thread or introduce a nested event loop to make browser creation appear synchronous.

The returned window participates in the existing lifetime immediately. `Close` works before browser creation finishes, while navigation is pending and after the page is running. A starting browser window counts as live for last-window shutdown. Initialize routing and ownership before native creation calls: callbacks can be synchronous or reentrant. A late result after close is released rather than attached, and never starts navigation.

Owned endpoints stop admitting work on close, cancel their handler contexts, cancel native body reads and settle outstanding native requests on their owner thread. Native callbacks keep their routing until they release their objects. Workers receive only copied Go data; late worker results are discarded safely, but the workers remain owned until they return. Cleanup does not depend on the public post queue, whose ordinary callbacks may be discarded during close.

`Done` and `Run` retain their existing meaning: all owned work has drained. A handler that ignores cancellation or a native operation that never completes keeps shutdown pending with useful diagnostics. A hidden window is not a drained window. Shared application services belong to `app.Go`, not either view. In resident mode, closing every window leaves those services alive until `Quit` or application cancellation.

Agreed: unexpected browser initialization failure follows the existing fatal cancel-and-drain policy and reaches `Run`. Wrong-thread and closed-owner calls retain the existing operation-error behavior. Configuration validation must occur before starting resources; the work order must specify its error classification consistently with the current opening APIs rather than inventing an implicit recovery path.

## Three explicit hosting modes

| Mode | Application boundary | Ownership | Intended facilities |
| --- | --- | --- | --- |
| In-process | `http.Handler` | Window owns endpoint requests and browser | Finite buffered request/response work |
| Owned loopback | `http.Handler` | Window owns HTTP server, connections/work it admits, and browser | Ordinary HTTP, including streaming |
| Existing URL | Initial URL and explicit allowed navigation origins | Window owns browser only | An independently owned local or remote service |

Selecting a mode is mandatory. There is no fallback that opens a listener, changes origins or silently changes cancellation semantics. A missing browser runtime is a visible initialization failure, not a reason to select another transport.

### In-process

The page uses relative URLs. A native resource callback admits a request, obtains bounded Go-owned request data, and dispatches `ServeHTTP` off the UI thread. A buffered response is completed on the owning UI thread. No application HTTP listener or JavaScript-to-Go RPC API is involved. Browser subprocesses and their own IPC still exist.

Windows interception and Linux scheme handling need not produce identical URL strings. The endpoint origin is assigned by the backend; applications must not encode one platform's scheme into their page. Document the request fields visible to the handler and preserve method, path, query, supported headers, status and finite body bytes consistently. Origin-sensitive browser features require their own evidence.

Admission precedes body accumulation and worker creation. Limits cover request/response bodies, request metadata and headers, concurrent admitted work, and aggregate buffered bytes, including framework-owned native copies. A native body read, queued completion or abandoned-but-running handler still holds its relevant admission/accounting until ownership ends. Oversize and exhausted admission produce explicit request failures without poisoning later requests. No unlimited queue sits in front of the limits.

Agreed configurable defaults per in-process endpoint:

| Limit | Default |
| --- | --- |
| Request body | 1 MiB |
| Response body | 1 MiB |
| Metadata and headers | 64 KiB per request/response |
| Admitted requests | 16 |
| Aggregate adapter-owned buffers, including native copies | 64 MiB |

These are ceilings, not a promise that all 16 requests fit simultaneously. The work order must define metadata accounting and reserve space for simultaneous copies before allocating them. This budget bounds adapter-owned storage; it cannot promise a bound on browser memory or arbitrary allocations inside application handlers. Loopback retains ordinary HTTP streaming and does not inherit these finite-response limits.

Agreed request-local failures: oversized request body returns 413; oversized request metadata returns 431; exhausted admission returns 503; oversized handler response returns 502. Never publish a successful truncated response. Response metadata is also bounded; the work order must specify its failure path consistently with the response limit and account for bounded failure responses without depending on unbounded emergency allocation.

No `Flusher` or `Hijacker` is supplied. Streaming, upgrades and unbounded bodies are outside this mode. A response exceeding its limit fails explicitly rather than publishing a successful truncated response. Failure-response construction and unsupported writer-interface behavior must be specified and tested before the contract is finalized. Unexpected handler panics follow the shared policy below.

Reload and fetch abort do not promise handler cancellation in this first implementation. Reload preserves the endpoint, and canceled browser fetches do not release admission while their handlers still run. Window close is the reliable cancellation boundary. The page must tolerate that distinction; a POST failure does not prove that a command was never accepted, so the fixture must not automatically retry edits.

### Owned loopback

Bind only to loopback on an assigned port. Use normal HTTP serving rather than the finite buffered adapter; the server has explicit request-header and connection/admission settings. Streaming must not inherit a finite-response buffering limit. Native windows remain responsive while HTTP work is pending.

Window close stops admission, cancels the endpoint context and closes or drains its owned serving resources while handler completion stays tracked. Do not infer that a server's shutdown call alone proves handlers or upgraded connections have finished. Any supported upgrade path needs explicit ownership and close behavior; WebSocket support is not claimed until exercised. Streaming is part of this mode's target and needs a native test.

A handler shared across windows must be concurrency-safe. Each endpoint has its own cancellation and access boundary: closing A does not cancel B's requests. There is no shared-server lifetime hidden behind the per-window API in this slice.

### Existing URL

An attachment declares its initial URL and allowed navigation origins, including the initial origin. Closing cancels browser work but does not stop, drain or assume ownership of the service. Remote handlers may continue. The attached page/server owns its authentication and HTTP behavior; this mode does not receive an owned endpoint's capability.

Agreed: a URL being reachable is distinct from browser creation succeeding. HTTP errors and rejected application commands remain page/application results. Initial and later page-load failures stay local to the browser window, visibly reported and retryable; they do not cancel the other windows or shared application services. Unexpected initialization, native-host or browser-process failures cancel and drain the application. The load-failure behavior below applies to both owned-handler and existing-URL windows.

## Page-load failure and retry

Agreed: a failed top-level load displays a small error page in that window, offering Retry and Return to start. Retry repeats only a failed GET navigation and remains subject to the window's origin policy. A failed POST navigation is never resubmitted by the framework; offer Return to start, which loads the configured initial page with GET. Do not reinterpret a failed POST as a GET retry of that same target.

This is a promise about framework actions, not at-most-once HTTP delivery. The Windows probe observed a browser transport resend a POST after a connection closed without a response, before any error-page action. Applications whose commands must not execute twice own duplicate handling, for example through command identities or idempotent operations; the framework adds no generic deduplication layer. Validation records transport redelivery separately and checks that Retry and Return to start introduce no POST replay. Answering a POST before closing the connection is not an at-most-once guarantee.

There is no automatic retry loop. Fetch and asset failures remain the page's responsibility and do not replace the whole page. Actual HTTP responses, including 404 and 500, display normally. The other windows and shared application services continue running.

The error-page mechanism must work when the endpoint is unreachable, preserve endpoint isolation and avoid opening a navigation or credential bypass. Both feasibility hosts exercised local error pages and redirects; production integration still needs its own validation. The adjusted Windows action assertions have a targeted native rerun pending. No generic reload operation may replay an edit POST. This is load recovery, not browser-process restart.

## Navigation and endpoint access

Agreed: an owned-handler window stays within its assigned origin. Refuse outside top-level navigation, popups and subframes, and do not automatically launch an external browser. Apply policy to redirects and subsequent navigations, not merely the initial URL. Existing-URL windows use their declared allowed navigation origins. Proposed first-slice consistency: refuse popups and subframes for attachment too; applications needing them require a separately scoped policy.

Agreed for owned-handler windows: resource loads and requests stay on the assigned origin, including its port. The mixer is self-contained, with no third-party resources. Enforce this for subresources and requests as well as document navigation, including redirects. Navigation checks alone do not establish this boundary; the work order must identify and validate the enforcement mechanisms. Existing-URL attachment retains a separate policy and receives no owned-endpoint credential.

Agreed after the Windows feasibility return: owned Windows webviews require both native request interception and a framework-enforced Content Security Policy. CSP is required protection, not an optional second layer: the WebSocket handshake escaped native interception with the endpoint credential in the probe, while CSP blocked it. The production work order must preserve both mechanisms and their combined confinement checks. Keep the individual-mechanism probes as evidence of their limits; they do not define supported configurations. This decision adds no new mechanism to the spike, whose default already enables both.

Agreed for Windows: navigation cancellation controls which navigation the window accepts; it does not guarantee that the request was never sent. Cancelled off-origin GET navigations in the probe still reached their destinations with the credential. Native request interception supplies the network boundary for those requests. Keep navigation cancellation, native interception and CSP together; neither cancellation nor CSP alone replaces interception. The default spike already does this, so the resolution is a design clarification rather than another mechanism.

Agreed: an owned loopback endpoint requires a per-endpoint capability as well as host/origin checks. A random port is not authorization. Protection must cover the initial document, assets and fetches without requiring every application's JavaScript to invent its own bootstrap protocol. A request lacking the capability must not invoke the handler. Origin checks must accommodate legitimate document/resource requests that carry no Origin header without making that absence sufficient authorization.

Agreed for the first slice: generate a random endpoint secret and install it as an HttpOnly session cookie through the native browser API in the window's isolated storage. Initial navigation waits for successful installation. The loopback access layer checks that credential, Host and applicable Origin before invoking the application handler. No credential goes in application JavaScript or page URLs, and ordinary page, asset and fetch requests authenticate automatically. A native proof on both hosts must cover installation failure/close races, reload, relative assets, fetch, redirects and streaming. Credentials must not escape to a disallowed destination or appear in ordinary logs. Cookie scope does not isolate ports, so same-origin resource restrictions and per-window storage isolation are required parts of this choice, not optional hardening. The threat boundary is unwanted web-origin access to an owned local service, not protection from a privileged attacker inspecting the host process.

Endpoint access policy is separate from browser credential delivery. The first implementation supplies only the private browser-cookie policy; it must not make cookie presence an intrinsic assumption of the transport or application handler. Future explicit policies may admit independently provisioned bearer tokens for cookie-less clients, additional revocable cookie sessions, or unauthenticated loopback access. Cookie-less need not mean unauthenticated. Provisioning/revocation APIs and unauthenticated-access protections are deferred, not required extension machinery for this arc. None changes ownership: a window-owned endpoint still stops on close. A service intended to outlive the window is independently owned and the browser attaches to it.

In-process endpoints also need routing isolation: requests from another window or an unapproved document must not reach the endpoint merely by naming its origin. Prove this using two live browser windows and the actual native request context. If the platform cannot establish the intended boundary, report the gap rather than weakening it silently.

## Browser storage

Agreed: storage is temporary and isolated per window. Reload keeps that window's session. Closing ends it; reopening starts a fresh session, and one window cannot read another's cookies or web storage. This default applies to attachments as well as owned-handler windows. It intentionally excludes persistent login sessions in this arc.

Feasibility demonstrated per-window in-memory Linux sessions and fresh Windows profile directories, including release and deletion. Production must validate its own implementation and retain the records' coverage limits, particularly weak cache evidence where no response was cached. Verify separation of cookies, local/session storage and cache on both platforms. Browser engine processes may be shared internally; that must not become shared application storage or permission to kill another window's browser resources on close.

Temporary on-disk storage, if required, is framework-owned. Agreed: distinguish live browser ownership from leftover files. While callbacks or browser resources still need the profile, shutdown keeps waiting and reporting them. After those resources release it, attempt directory deletion with bounded retries. If deletion still fails, shutdown may finish with a cleanup error naming the directory, preserved alongside any earlier failure; never report successful deletion or reuse that profile. Reopening always gets fresh storage. The work order must establish the native release evidence and retry bounds; deletion failure alone cannot establish that native use has ended.

Abnormal process exit may leave profile directories behind. Identify framework-owned directories clearly, but defer automatic scavenging in this slice rather than risk deleting another running instance's storage. Temporary storage is not a secure-erasure promise. A backend that cannot meet the isolation/fresh-session contract must fail visibly rather than reuse a persistent profile. Deliberate canon alignment must distinguish a reported post-release filesystem residue from live owned work; this does not relax the latter's drain requirement.

## The mixed-mixer application

Keep model, command ordering and pending-value reconciliation in application code. Both frontends use stable channel IDs and the same submission path. The HTTP adapter translates requests and results; it does not inject HTTP or browser types into the model. Invalid edits and queue/device rejection are application responses, not native framework failures.

A snapshot includes enough application-owned information for the page to distinguish authoritative values, pending edits and visible errors. Two mute toggles and two gain steps before acknowledgment must preserve their order and build on the displayed value. Delayed polling must not roll the page back to an older snapshot or manufacture commands. The exact DTOs and polling interval belong to the example and work order, not the framework API.

Retain the synthetic source and command worker at application lifetime. Closing either view removes its subscription and cancels its endpoint work without canceling the model. A pending accepted command belongs to the model, not the HTTP response that submitted it; closing or aborting the request must not imply that the command was undone. Normal application shutdown retains the example's cancellation policy instead of promising delivery to an audio device.

## Owned-handler panic policy

Agreed: unexpected panics in framework-invoked HTTP handlers follow the existing application-panic policy in both in-process and owned loopback modes. Recover the panic with its stack, initiate application cancellation, settle or abort the affected request and drain all owned work. Preserve the failure in Run's result without exposing the stack to the page. A partial loopback response cannot be retroactively replaced with a promised HTTP error response; native settlement and handler accounting must still finish.

A deliberately returned HTTP 500 is an ordinary application response, not a panic or application shutdown. Existing-URL attachment leaves server panic policy to that independent server. The work order must distinguish intentional HTTP request-abort control flow from unexpected panics and make recovery behavior consistent between the two owned transports.

## Native prerequisites and failure visibility

Decision (2026-11-12): carry the established Linux signal conflicts as a monitored compatibility risk while developing and using dfx2 and dfw. Michael reports no manifestation in regular dfw use; this informs the effort budget but does not invalidate the reproduced hazards. Stop expanding the signal investigation. Preserve one-executable distribution and existing evidence; no new handler manipulation, helper process or runtime minimum is adopted to address these findings. Revisit on a representative application failure or new evidence materially changing exposure. Monitoring means retaining runtime versions and crash/hang diagnostics in ordinary auditions, not building a new monitoring subsystem. This is permission to continue development with a known risk, not a production-readiness verdict or a choice among the pending GC-signal mechanisms.

The cgo comparison found that changing bindings does not fix signal coexistence. Do not hard-code a signal, hide the warning or treat successful page rendering as proof that signal ownership is safe. Any eventual private runtime dependency still needs its support and failure behavior stated.

Agreed after the reservation probe: the proposed Linux JSC integration requires an explicit application-reserved signal before browser initialization; the framework must never infer availability from a Go-owned kernel disposition. The application promises that it and its dependencies do not already use that signal and will not subscribe to, ignore, reset or repurpose it afterward. Native range, disposition and mask checks are sanity checks, not proof that no Go subscriber exists. No public API spelling or fixed signal number is accepted. This contract does not establish suspend-handler safety, approve the private configuration symbol or approve modifying JSC's fault handlers. Linux production readiness remains pending.

The handler-safety follow-up ruled out the proposed host-VM workaround: restricting JSC calls to the UI thread does not prevent later suspension while that thread executes Go. The VM-free investigation supports continuing with no JSC VM in the Go host, including none created by native JavaScript-result APIs, while preserving JavaScript in browser children and callback-based in-process HTTP dispatch. The exercised required paths on 2.52.6 stayed VM-free under external instrumentation validated by positive controls. This is bounded evidence for those paths and that version, not general signal safety or evidence for untested APIs. The fault-handler patch is excluded and signal-mask toggling is not pursued. The subsequent SIGXCPU and cgo comparisons remain evidence for the monitored risk above; see the [spike brief](webview-feasibility-spike.md).

Use the installed runtime for development, record its version and retain child-exit observations. No new runtime minimum was adopted by the monitoring decision. The earlier comparison could not reproduce the original graphics-teardown crash on either version and did not prove a remedy; the mixed-package comparison is not supported-deployment validation. Preserve that uncertainty. Distinguish host ownership drain from engine-child exit and record child statuses that cannot be observed.

Windows integration must cover synchronous creation completion, close during controller/environment creation, and reentrant teardown without attaching late objects. Native browser calls join the existing dispatch/reentrancy discipline. The established size/move behavior for native windows must remain intact while a browser window is open.

Agreed: unexpected initialization, native-host or browser-process failures are fatal application failures, with cancel-and-drain and no automatic browser restart. Exact notifications, normal child exit distinctions and behavior during shutdown remain to validate. HTTP errors and rejected commands remain page/application results; page-load failures are visible and retryable within their window. An HTTP error, failed load or deliberately refused navigation is not a browser crash. Keep unexpected failures and distinct cleanup errors in the final result even if later retries succeed.

## Seam census

| Boundary | Call and reason | Revisit when |
| --- | --- | --- |
| Application model / rendering | Separate: native and browser views consume the same application-owned values; widgets/page interaction do not own the shared model | A concrete application requires another reconciliation policy |
| Model / transport | Separate: HTTP translation stays in the example adapter; no browser or HTTP types in mixer state | No current reason to couple them |
| Endpoint access / browser credential | Separate: access is checked at the endpoint boundary; native cookie installation supplies the first credential policy | External clients need tokens, separate sessions or explicit unauthenticated access |
| Endpoint / server lifetime | Explicit: owned handler modes drain their work; URL attachment never owns the external server | A concrete application needs a shared owned server across windows |
| Platform / content | Separate and private: webview and UI share application lifetime, not renderer types or public native handles | A consumer requires a scoped native integration |
| Public work / native completion | Preserve the runtime contract: cleanup uses tracked internal completions, never a public queue slot | No relaxation proposed |
| Error by tier | HTTP/command errors stay application results; load failures stay visible and retryable in the window; unexpected initialization/native/browser-process failures cancel and drain | A concrete application needs browser-process recovery |
| Storage / window | Separate sessions per window, temporary by default; internal process sharing cannot weaken isolation | Persistent profiles become a real product requirement |

## Acceptance scenarios

These establish integrated behavior on both hosts; existing spike results cannot substitute for them.

- Run native and browser views together in each owned transport. Edit either, observe both, delay/reject commands, reorder/remove channels, and close either first. Confirm surviving views and application services remain usable.
- Close before readiness, during body reading, during handler execution and while a response awaits native completion. Include reentrant and late creation callbacks. No native object attaches to a closed owner, no callback uses freed storage, and owned work remains visible until drained.
- Reload and abort slow requests repeatedly. In-process abandoned handlers stay bounded and retain admission until return; loopback cancellation has its own native evidence. No failed edit request is automatically replayed.
- Exercise finite bodies, metadata limits, aggregate pressure, overload and subsequent recovery. Verify exact supported status/body behavior and the documented response-header subset. Demonstrate streaming through loopback without freezing either window.
- Attempt unapproved navigation, redirects, popups, subframes, cross-window endpoint access and unauthorized loopback requests. Exercise page/asset/fetch bootstrap with the chosen capability. Distinguish refused requests from untested browser features.
- Verify storage isolation between windows, preservation across reload, freshness on reopening and cleanup of owned temporary storage, including bounded post-release deletion failure returning a named cleanup error without profile reuse. Keep attached-server survival separate from browser storage lifetime.
- Panic an owned handler in each transport, including after loopback output starts. Assert cancellation/drain and a retained stack, with no stack in the page response; verify a deliberate HTTP 500 leaves the application running.
- Fail initial and subsequent page loads, verify visible failure and explicit retry while the native window continues, and ensure retry does not replay an edit POST. Validate missing runtime, native failure and browser-child failure disposition. Exercise two windows so one failure cannot silently strand another's cleanup. Track requests, handlers, native operations and storage cleanup in shutdown diagnostics.
- Repeat relevant foundation/native UI regressions, including close-one/keep-one, resident reopening and Windows size/move progress. Prove native-only startup does not load browser libraries. Retain child-exit observations and manual auditions on Linux and Windows.

Portable tests use controllable native seams and barriers. Host evidence records exact revisions, runtime versions, desktop/RDP conditions and unavailable coverage. Cross-builds are not execution. CPU/frame measurements remain observations; no performance target is inferred from the earlier unusable Windows timing. Test fixtures should discriminate contract failures without becoming a separate production device system.

## Deferred and why

Embedded native/browser composition inside one window, arbitrary JavaScript evaluation/RPC, readiness callbacks without a consumer, persistent/shared profiles, browser recovery, authentication UI, downloads, permission-granting APIs, broad external-site compatibility, service workers and iframe hosting are outside this slice. Set explicit supported behavior before claiming any of them; do not silently grant capabilities merely because a browser default permits them.

In-process streaming and precise fetch-abandonment cancellation remain outside the finite contract. Loopback is the explicit alternative. WebSocket integration needs a separate ownership proof before it is advertised. Generated TypeScript, tray/discovery facilities, macOS, fader polish and mouse-wheel editing remain follow-ups. Sessionmixer hardware integration follows this shared-lifetime exercise.

## Production planning status

The bounded [webview feasibility spike](webview-feasibility-spike.md) has returned from both hosts. Linux closeout and both manual error-page auditions are recorded. The Windows raw 19/22 result remains unchanged: the confinement attribution failures established the required combined mechanism, and the POST finding clarified transport redelivery versus framework actions. The revised Windows `error-pages` assertions still need their targeted native run.

The paired work order grounds the platform extension, asynchronous ownership, bounded HTTP adapter, packaging and implementation stages in the existing runtime. It carries the Linux signal-mechanism decision explicitly and requires integrated validation of the native mechanisms. Feasibility is evidence for the plan, not acceptance of a production backend.

Submit this pair to Mercurius before implementation. Canon alignment is deliberate work once new guarantees are settled; these drafts do not edit the canon or claim the current qualities cover every new browser policy.
