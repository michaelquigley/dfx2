# Windows WebView2 spike

Prove that dfx2 can host WebView2 without cgo and serve the same page through either loopback HTTP or socket-free dispatch to a Go `http.Handler`. Implement and audition this on Michael's Windows development host, then bring the code and findings back to Linux. The experiment should establish the thread, request, and lifetime boundaries needed by the future shared platform layer.

Read `AGENTS.md`, recent `docs/journal/` entries, [direction.md](direction.md), and the Windows results in [text.md](text.md). The native-window spike is already implemented in `experiments/text-spike`; use its Win32 experience, including its unverified DPI status, without making this experiment depend on the text engine. mygo remains a source of ideas only, never code or a dependency.

## Scope and meaning of the two modes

Create `experiments/webview2-spike` as its own Go module, with a README and a tiny embedded HTML/CSS/JavaScript fixture. Use the standard library, `syscall`, and COM with `CGO_ENABLED=0`. Build only the bindings the experiment needs from the official SDK definitions. Do not introduce a web framework, frontend build tool, JavaScript-to-Go RPC bridge, or production dfx2 packages. This spike requires no source compatibility with dfw.

Both modes run one Go application process containing the window and handler. WebView2 has its own browser processes; “in-process” describes handler dispatch, not a promise of one OS process.

| Mode | Path to the same handler |
| --- | --- |
| `-mode loopback` | Bind `127.0.0.1:0`, run an ordinary `http.Server`, and navigate the webview to its assigned address. |
| `-mode inprocess` | Navigate to `https://dfx2-spike.invalid/`; intercept requests for that exact origin and invoke the handler directly. No application HTTP listener and no fallback to loopback. |

The page uses relative URLs in both modes. Select the mode at launch, not through two different pages. A reserved HTTPS origin keeps this experiment focused on interception; a custom URL scheme is not required. Microsoft documents replacing HTTPS resources through [WebResourceRequested](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/working-with-local-content). Do not combine this route with folder mapping, which bypasses that event for mapped resources.

## Stage 1: host WebView2 and prove loopback

Create a Win32 window with a locked UI thread initialized as a COM STA. Run its normal message pump; create the environment and controller asynchronously, fit the webview to the client area on resize, and give it usable keyboard/mouse focus. Reuse the earlier spike's per-monitor DPI approach, but record actual tests separately from having implemented the path.

Use the installed WebView2 Runtime and the matching-architecture `WebView2Loader.dll` from an official release SDK. Load the DLL from a known location beside the executable. Pin and document the SDK acquisition/version and loader checksum, and record the Runtime version actually used. The loader and installed Runtime are separate runtime dependencies; no C compiler or native header compilation should be required. Microsoft's [distribution documentation](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution) describes the loader requirement. Missing loader or Runtime must produce an actionable error, not a blank window. Keep downloaded SDK files, DLLs, executables, and the webview's user-data directory out of git; give the fixture its own user-data directory rather than sharing a browser profile.

Start the loopback server before navigating. The handler owns routes and content; the host owns server/window lifetime. A server failure must reach the application rather than leave an unexplained live window. Include a title identifying the selected mode and optional devtools for inspecting the fixture.

Use one process-lifetime callback trampoline per required signature, routing through object identity. COM objects still need correct `QueryInterface`, `AddRef`, and `Release` behavior: permanent callback code does not imply permanent per-request objects. Keep callback object/vtable storage and Go state alive for the full native reference lifetime. Do not hand COM an unrooted pointer into Go memory.

## Stage 2: dispatch intercepted requests to the handler

Register interception before navigation, covering document, script, style, and fetch requests under the fixture origin. Validate the parsed origin, not just a string prefix. Unknown local routes return the handler's 404. Prevent the fixture from navigating or opening new windows outside its origin; all assets are embedded and no external network resources are needed.

Adapt method, URL/path/query, headers, and body into a normal `http.Request`. Dispatch the same handler used by loopback and translate its status, repeated response headers, content type, and body into a WebView2 response. Preserve UTF-8 and binary bytes. For this spike, buffer finite request and response bodies with a documented 1 MiB limit in the adapter; oversized input/output should give a visible error, never silent truncation. This is an experiment limit, not a proposed framework default. Do not advertise streaming interfaces on a buffered response writer.

Handler work runs off the UI thread. Take a WebView2 deferral, copy the small request into Go-owned data, invoke the handler on a worker, and marshal response installation and deferral completion back to the UI thread. Keep apartment-bound COM interfaces out of workers. Use an appropriate COM stream with explicit ownership for the response body, rather than exposing a temporary Go slice. Verify the selected stream's lifetime/threading contract against the SDK. Microsoft requires a pumping STA and warns against blocking or nested callback message loops in its [threading model](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model); [request-body ownership](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2webresourcerequest) also needs explicit handling.

Instrument request start, handler completion/cancellation, response completion, and outstanding work. Give requests identifiers so close/reload races can be read from a short log. Demonstrate that a slow request doesn't prevent another request or window interaction from completing. Do not add application IPC as a shortcut around HTTP adaptation.

## Stage 3: lifetime and failure behavior

Give handler work a context owned by the window. The slow fixture handler must honor cancellation. In loopback mode, connect this lifetime to server requests too; `Server.Shutdown` alone is not cancellation of an active handler.

Define a small closing state: stop accepting new work, cancel outstanding handlers, settle or dispose pending request state on the owning thread, close the controller, release owned COM references, and finish the loop. Pending deferrals and body streams must have an explicit end on both success and failure. Do not wait on the UI thread for a worker that needs that same thread to finish. Late environment/controller creation callbacks must observe closing and release their results instead of attaching to a destroyed window. Keep the dispatch target alive until no worker can post to it. Microsoft's [controller contract](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2controller) explains explicit `Close` and event-handler cleanup.

Exercise close during startup, normal browsing, and a slow request, plus reload while a request is pending. Distinguish browser-side fetch cancellation from cancellation that actually reaches the Go handler; record any gap instead of claiming equivalence. No blanket termination of browser processes: clean up the resources this fixture owns.

## The fixture and acceptance checks

Make the page self-contained and easy to audition: named buttons, a small result log, and an editable input to demonstrate focus. Routes should include the page and static assets, JSON GET with a query, JSON POST echo, a tiny binary response, explicit 404/500 responses, a repeated-header response, and a three-second context-aware slow response. Send `Cache-Control: no-store` for these checks so caching cannot masquerade as handler dispatch. Use the same routes and bytes in both modes.

Run the following checks in each mode and record automated evidence separately from Michael's observations:

| Check | Required evidence |
| --- | --- |
| Startup and prerequisites | Page/assets load; loader and Runtime versions are logged. Missing-loader error is exercised. If missing Runtime cannot be tested safely on this host, mark it untested. |
| HTTP behavior | GET query and POST body arrive intact; binary bytes, status codes, content types, and repeated headers survive the adapter. Identify which headers were inspected through devtools/logs versus page JavaScript. |
| Responsiveness | Start the slow request, then a fast one; the fast one completes first. Resize, type, and scroll while work is pending. |
| Errors and limits | A handler 404/500 is visible as that response. Oversized bodies in in-process mode fail explicitly, and the next normal request succeeds. |
| Transport | Log the listener address in loopback mode and demonstrate access with an ordinary HTTP client. In-process mode has no listener creation and shows intercepted handler requests; use host socket inspection to corroborate. Browser-internal sockets are not application listeners. |
| Reload and close | Reload during a slow request; close idle, busy, and during asynchronous initialization. Record exit timing, cancellation, pending counts, and any late callbacks. Repeat launch/close several times. |
| Origin and focus | Relative assets/fetches work; typing and tab navigation work; an attempted outside navigation/new window is refused. |
| DPI | Resize/minimize/restore on the available host. Scaling changes and mixed-DPI monitors are exercised only where possible, otherwise explicitly untested. |

Success means ordinary finite HTTP requests work in both modes, slow work leaves the host responsive, and close has an observed, explainable lifetime. A platform limitation is a valid result; report it without silently substituting loopback for in-process dispatch.

## Build, test, and return

Keep the handler and byte-level adaptation independently testable without Windows. Use Windows build tags for bindings and a clear unsupported-platform entry point so package-wide tests can run on Linux. Add focused tests for request/response fidelity, size limits, and cancellation/completion ordering. Use Go's own tests for those contracts and the real WebView2 for browser behavior.

From `experiments/webview2-spike` in PowerShell, after the documented loader setup:

```powershell
$env:CGO_ENABLED = '0'
go test ./...
go vet ./...
go build -o dfx2-webview2-spike.exe .
.\dfx2-webview2-spike.exe -mode loopback
.\dfx2-webview2-spike.exe -mode inprocess
```

Write the experiment README with exact setup, commands, routes, controls, and limitations. Create `docs/future/webview.md` for findings: host/architecture/session type, Go/SDK/Runtime versions, test and audition results, ownership/threading lessons, and what the experiment implies for dfx2. Include a capability table distinguishing demonstrated finite HTTP behavior from untested streaming, WebSockets, cookies/authentication, redirects, service workers, and custom schemes. Do not describe the adapter as a general replacement for `net/http` serving.

Put a few representative screenshots, concise logs, and any reusable audition driver under `experiments/webview2-spike/evidence/windows/`. Preserve runnable test procedures, not only assertions that tests passed. Record hard-won rationale and unfinished handoff state in the day's journal without duplicating the results. Keep ordinary source/docs/assets non-executable when transferring from Windows.

Use branch `spike/windows-webview`. Michael commits this brief here and makes it available on Windows; the implementing session works there, leaves changes for his review, and Michael commits/publishes the implementation and evidence. Back on Linux, run this module's portable tests/vet, cross-build for the tested Windows architecture, and inspect the returned evidence. Linux checks do not stand in for Windows execution. Keep the earlier native spike unchanged; if a shared-file change becomes necessary, explain it and repeat the affected checks. Do not commit or push unless explicitly instructed for that action.

## Deferred (and Why)

- **Streaming/SSE and WebSockets:** finite buffered HTTP does not establish incremental delivery or protocol upgrades. These need a separate investigation before migrating products that use them; loopback remains available.
- **Cookies/authentication, redirects, service workers, and custom schemes:** they extend the transport/origin contract beyond this fixture. Report dependencies discovered, but do not grow a browser networking layer here.
- **Native drawing mixed with web content, multiple windows, tray/daemon topology, and product migration:** establish the standalone webview lifecycle first.
- **Packaging/installers, automatic Runtime installation, and other platforms:** document this host's prerequisites and evidence; distribution and WebKitGTK get their own work.
- **Permanent public API and reusable COM/platform packages:** extract only after comparing this experiment with the native-window results. The successful Go handler boundary is the design input; the experimental bindings are not automatically the framework.
