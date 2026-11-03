# The system webview in dfx2

This page holds findings, not a design. The first are from the [Windows WebView2 spike](windows-webview2-spike.md), `experiments/webview2-spike`. It hosted WebView2 from Go with `CGO_ENABLED=0`, and served one embedded page through one `http.Handler` two ways: a loopback HTTP server, and socket-free interception into the handler. The bindings and the adapter are experimental. The adapter carries finite, buffered HTTP only, and is not a replacement for `net/http` serving.

The evidence is in [`experiments/webview2-spike/evidence/windows/`](../../experiments/webview2-spike/evidence/windows/): the driver's [summary](../../experiments/webview2-spike/evidence/windows/drive-summary.txt), per-run [logs](../../experiments/webview2-spike/evidence/windows/logs/), and three captures. The [README](../../experiments/webview2-spike/README.md) has setup, commands, routes and controls.

## Windows results (2026-11-03)

### Host and conditions

- The host is the same machine as the native spike: Windows 10 Pro 22H2, build 19045.6466, amd64, a QEMU/KVM guest (i440FX) with 8 logical CPUs of an i7-6950X.
- **The session is remote, over RDP**: one Remote Display Adapter display at 96 dpi. Everything visual is RDP presentation.
- Go 1.27.3 windows/amd64; the module declares 1.26. `CGO_ENABLED=0` throughout.
- **WebView2 SDK** `Microsoft.Web.WebView2` **1.0.4129.50**, from NuGet. It was chosen because its build number matches the installed Runtime's. Pins:
  - package SHA-256 `d3934f48…f33f473d2`;
  - x64 `WebView2Loader.dll` SHA-256 `a9a09232…df99ec90`, file version 1.0.4129.50, Authenticode-signed by Microsoft Corporation.

  `setup-loader.ps1` verifies all three, and the host logs the loader's hash against the pin.
- **Runtime:** Evergreen **151.0.4129.93**, as reported by the loader and by `get_BrowserVersionString`. The usual registry key, `EdgeUpdate\Clients\{F3017226-…}` (HKLM, both views, and HKCU), is absent on this host, yet the loader finds the Runtime under `Program Files (x86)\Microsoft\EdgeWebView`. Ask the loader (`GetAvailableCoreWebView2BrowserVersionString`); don't detect the Runtime by that key.

### Commands and tests run

From `experiments/webview2-spike`, with `CGO_ENABLED=0`:

- **Tests:** `go test ./...` passes. It covers request/response fidelity, the limits, origin checks, header blocks, panic→500, the absence of streaming interfaces, and the dispatcher's ordering, wakeup coalescing, failed-wake retry and close/cancellation contract. It also checks that the fixture serves the same status, type and bytes through `httptest` loopback and the adapter.
- **Bindings:** with `WEBVIEW2_IDL` pointed at the SDK's `WebView2.idl`, `TestBindingsMatchIDL` checks every IID and vtable slot the host uses against the IDL, and passes.
- **Vet and build:** `go vet ./...` is clean, and so is `GOOS=linux go vet ./...`. `go build` succeeds. `GOOS=windows GOARCH=arm64` builds the unsupported-platform stub. Linux was only vetted, never run.
- **Driver:** `evidence/windows/drive` ran every scenario against the final binary in both modes. Its findings are below, as "automated". Michael then auditioned both modes by hand, with devtools; that's "observed".

### Audition

| Check | Automated | Observed (Michael) |
| --- | --- | --- |
| Startup and prerequisites | Both modes load the page and assets. Loader hash, Runtime and browser versions are logged. A missing loader (`-loader` into an empty folder, and the exe copied alone) exits 1 before any window, with setup instructions. A missing Runtime was **simulated only** (`-runtime-dir` on an empty folder → `ERROR_FILE_NOT_FOUND`, exit 1, instructions). The real Evergreen Runtime wasn't removed: **untested**. | Both windows loaded normally. |
| HTTP behavior | Page checks pass 14/14 in both modes. GET query (UTF-8, repeated and empty parameters, custom request header) and POST bodies (UTF-8 text, 64 KiB binary, by SHA-256) arrive intact. All 267 binary bytes survive, and so do handler 404/500, the mux's 404, content types and `Cache-Control: no-store`. Page JavaScript sees `x-spike-repeat` as `one, two` in **both** modes (fetch joins repeats). `curl.exe -i` shows two `X-Spike-Repeat` lines from loopback. | **Devtools, loopback:** `x-spike-repeat` as two lines (`one`, `two`); Raw shows the HTTP/1.1 response, including `X-Spike-Unicode: café`. **Devtools, in-process:** `x-spike-repeat` as **one joined value `one, two`**, and there's **no Raw view**. Both modes: `cache-control: no-store` is present, and `x-spike-unicode` reads `cafÃ©`. |
| Responsiveness | Slow, then fast: fast at ~110 ms, slow at ~3004 ms, in both modes. Four `SetWindowPos` resizes and a minimize/restore during a pending slow request; the slow request finished on time. | "Everything seemed to behave fine": typing, Tab/Shift+Tab, scrolling and resizing during Slow, and slow-then-fast. |
| Errors and limits | Handler 404/500 are visible as those statuses. In-process, a 1.5 MiB POST is read to 1 MiB+1 and gets **413** (`X-Spike-Adapter-Error: request-too-large`, handler not run). A 2 MiB response gets **502** (`response-too-large`; the handler's write returned `ErrBodyTooLarge`). The next request succeeds both times. Loopback serves both normally: the limit is the adapter's. | Run all checks: 14/14 in both. |
| Transport | Loopback logs its listener and owns exactly one `Listen 127.0.0.1:<port>` plus its connections. Go `net/http` and `curl.exe` reach it. **In-process: no application listener is created, and `Get-NetTCPConnection -OwningProcess` lists no TCP endpoints at all for the process.** Every request appears as an intercepted `req N`. The browser processes' own sockets weren't inspected, and they aren't application listeners. | n/a |
| Reload and close | See [Lifetime](#lifetime). Close idle, busy, during startup, and by Ctrl+Break: exit 0 in 16–67 ms. 5× repeat in each mode was clean. Server failure (`-break-listener`) exits 1 within ~70 ms. | X with Slow pending in each mode "felt immediate" (logged: 40–41 ms, request canceled/settled). Pressing F5 during Slow in-process reproduced the reload gap. |
| Origin and focus | `window.open` of an outside URL and of the fixture's own origin both return `null`, and the host logs the refusal. `location.href = https://example.com/` is canceled in NavigationStarting, and the page reports it's still on the fixture 1.5 s later. All in both modes. | Typing and Tab "behaved fine". **Outside navigation wasn't tried by hand.** |
| DPI | Per-monitor-v2 is set, and the window opens at 96 dpi. The `WM_DPICHANGED` path has never run. | **Untested:** the RDP display is fixed at 96 dpi, as in the native spike. |

### Lifetime

- **Closing with work pending, in-process.** `WM_CLOSE` runs these steps in order:
  1. Cancel the window's context, and close the dispatcher: no new work, and it never posts again.
  2. On the UI thread, settle every pending deferral with a 503 (`put_Response`, then `Complete`, both `S_OK`), and release the args and the deferral.
  3. Remove the event handlers and the filter, close the controller (~22–28 ms), release what's owned, and destroy the window.

  The canceled handler returns about 1 ms later, and its result is dropped. Every run ended with every worker drained and 0 callback objects still referenced (each object counts its own references). Across all runs, 0 callbacks arrived off the UI thread.
- **Closing with work pending, loopback.** The server's `BaseContext` is the window's context, so closing cancels active handlers (`saw cancellation`). `Shutdown` then returns in about 1 ms.
- **Close during startup.** Closing at 0, 50, 150 or 300 ms after the window appeared always landed in controller creation. `DestroyWindow` then delivers the controller callback with **`E_ABORT`, synchronously, inside `DestroyWindow`**, as WebView2.idl says. The callback sees closing and attaches nothing, and the process exits 9–19 ms later.
- **Close during environment creation never happens on this host.** The environment-completed handler runs **inside** `CreateCoreWebView2EnvironmentWithOptions`, before it returns. A `WM_CLOSE` posted just before the call (`-close-during env`) is dispatched only after it, so nothing pumps our posted messages during the call. The host's other branch (hide, keep pumping for up to 5 s, then destroy) is implemented but **untested**. Don't rely on either ordering: the API is documented as asynchronous.
- **Reload or abort doesn't reach an in-process handler: a real gap.** When the page aborts a fetch (`AbortController`) or reloads during `/api/slow`, the browser side ends at once. In-process, the Go handler runs its full 3 s, and its response is installed into the abandoned request with `S_OK`. This implementation receives no request-abandonment signal through the interfaces it uses; only closing the window cancels in-process handlers. That does not establish that every possible WebView2 integration lacks a cancellation strategy. In loopback, the same abort and reload cancel the handler in 300–520 ms, because the connection closes. Michael reproduced the reload case by hand (F5).
- **A console event bypassed close entirely, and now doesn't.** In the first manual audition, both windows died together with exit `0xC000013A` (`STATUS_CONTROL_C_EXIT`) when the terminal that launched them was closed or interrupted: no request settled and no controller closed. The host is a console program. It now turns Ctrl+C, Ctrl+Break and console close (`os.Interrupt`, `SIGTERM`) into a posted `WM_CLOSE`. **Ctrl+Break was tested** (exit 0 in ~40 ms, the pending request settled). Console close (`CTRL_CLOSE_EVENT`), logoff and shutdown are **untested**.

### Capability

| Behavior | In-process status |
| --- | --- |
| Finite GET/POST, query, request headers, UTF-8 and binary bodies ≤ 1 MiB | **Demonstrated.** |
| Status codes and reason phrases, content types, `Cache-Control` | **Demonstrated.** |
| Repeated response headers | **Observed for `X-Spike-Repeat`:** it reaches JavaScript as `one, two` (the same in loopback), and in-process devtools shows one joined value. This does not establish how `Set-Cookie` is handled; cookies and headers requiring separate values need direct tests before relying on them. Building headers with `ICoreWebView2HttpResponseHeaders::AppendHeader` instead of a raw string is an **untested** alternative. |
| Non-ASCII header values | Arrive like loopback: the UTF-8 bytes are decoded as Latin-1 (`cafÃ©`). |
| Bodies over 1 MiB | Explicit 413/502 by design. Larger finite bodies need a different stream strategy, which is **untested**. |
| Cancellation when the page abandons a request | **Gap:** not delivered to the handler. Closing the window cancels. |
| Streaming responses, SSE | **Untested.** The buffered writer offers no `Flusher`, and an incremental `IStream` wasn't tried. |
| WebSockets / upgrades | **Untested.** Interception has no upgrade path, so it likely needs loopback. |
| Cookies, authentication, redirects | **Untested.** |
| Service workers, iframes, custom schemes | **Untested.** The filter covers top-level-document requests; iframe navigation isn't policed. |

### Ownership and threading lessons

- **One trampoline per signature works for COM callbacks.** Four `syscall.NewCallback`s are made once per process: QueryInterface, AddRef, Release and Invoke. Every WebView2 handler fits Invoke's two-argument shape. Each handler object lives in `CoTaskMemAlloc` memory, holding a vtable pointer to a package-level array, a reference count and a routing id. No Go pointer is handed to native code. Each object is freed when its count reaches zero, and at exit every count had.
- **Hand over the object's reference after registering it.** The host creates each handler object holding one reference, registers it (`add_…` or `Create…`), then releases its own. WebView2 then holds the only reference: `remove_…` or creation completion frees the object, and live-object accounting proves teardown complete.
- **Keep the COM state on the UI thread; send workers only copied data.** The request (URI, method, headers, and the body read to the limit) is copied on the UI thread, and the request's body stream is released there. A worker gets only Go values; the args and the deferral stay in a UI-thread map. Results come back through a coalesced, payload-free `WM_APP`, the same shape as the native spike's meter wakeup. The response is installed and the deferral completed on the UI thread.
- **Make posting impossible after close, then destroy.** The dispatcher calls `PostMessage` under its lock, and `Close` takes that lock, so once `Close` returns no worker can post. Only then is the window destroyed (and its handle free to be reused). The loopback server's failure post goes through the same closable poster.
- **Response streams:** `SHCreateMemStream` copies the bytes, so no Go memory outlives the call. It answers `QueryInterface(IAgileObject)` and `IMarshal` with `S_OK`. WebView2.idl asks for an "agile" response stream, and this one claims to be.
- **Size the RECT ABI per architecture.** `put_Bounds` takes RECT by value: by reference on x64, but in registers on arm64. That's why the host is windows/amd64 only.
- **The GDI/Go-heap pitfall is not specific to the text spike.** The driver's first captures, from a 3.2 MB Go slice passed to `GetDIBits`, were all black. A `VirtualAlloc` buffer fixed them. That's consistent with the native spike's arena finding, though it wasn't isolated here.

### What it implies for dfx2

- **The handler boundary holds.** One unchanged `http.Handler` served both transports, with the same routes and bytes. The platform layer can own the transport and lifetime, and products keep writing ordinary handlers.
- **In-process is viable for finite request/response work.** It needs no listener, the extra latency wasn't noticeable, and its limits fail explicitly. It is not a general `net/http` replacement. Products that stream, use WebSockets, or need abandon-cancellation or exact repeated headers (cookies) need loopback, or further work, before they migrate.
- **The main-thread contract carries over from the native spike.** It's one STA UI thread with a pumping loop, a payload-free coalesced wakeup, and workers that never touch native objects. The platform layer's closing sequence: stop accepting work, cancel, settle on the owning thread, release, then destroy. Close must also catch console events.
- **Expect re-entrancy.** Completion handlers may run before their Create call returns, and `DestroyWindow` can run a creation callback re-entrantly. Set state before calling, and let callbacks observe closing.
- **Untested on Windows, and still needed:** DPI changes and mixed DPI; a removed Runtime; console close/logoff; streaming and WebSockets; multiple windows. The Linux experiment is recorded below.

### Linux return checks

Completed on 2026-11-03 after fast-forwarding local `main` to `050dc6e`. Go 1.27.1 linux/amd64, with `CGO_ENABLED=0`:

- `go test ./...` passes, including the handler/adapter parity tests using a real local HTTP server. The first sandboxed run could not open the test socket; the rerun with loopback access passed.
- `go vet ./...` passes for Linux and for `GOOS=windows GOARCH=amd64`.
- The pinned SDK package was downloaded independently into `/tmp`. Its SHA-256 and the included x64 loader's SHA-256 both match the recorded pins. With its extracted `WebView2.idl`, `TestBindingsMatchIDL` **passed, not skipped**. The full test run also had `WEBVIEW2_IDL` set. This checks declared IIDs and method slots, not runtime ABI behavior or callback lifetimes.
- The Linux executable builds and exits 1 with the documented windows/amd64-only message. The Windows amd64 host and audition driver both cross-build.
- The driver summary, representative lifecycle logs, manual-audition logs, and all three PNGs were inspected. The logs corroborate window-close cancellation and the in-process reload gap. Every tracked file under `experiments/webview2-spike` arrived as mode 100644.

These are portable-code and integration checks, not a second Windows runtime audition. The untested Windows cases above remain open; the existing native/text experiment was unchanged.

## Linux WebKitGTK results (2026-11-03)

The [Linux experiment](../../experiments/webkitgtk-spike/README.md) reuses the Windows spike's portable adapter, dispatcher and fixture through a local Go module replacement. The fixture wrapper changes labels and transport detection only. The host uses purego and builds with `CGO_ENABLED=0`; it introduces no root framework packages or changes to either earlier experiment.

### Host and evidence

WebKitGTK 2.52.6 (4.1/Soup 3 ABI), GTK 3.24.52, Soup 3.6.6; Go 1.27.1 linux/amd64. The desktop session is Wayland, with GTK's default backend and scale factor 1. X11 and scale changes were not tested. Runtime loading needs WebKit 2.40 or later for the request-body API.

The [evidence directory](../../experiments/webkitgtk-spike/evidence/linux/) contains logs, application-listener observations and two WebKit snapshots. The [driver](../../experiments/webkitgtk-spike/audition.py) executes actual native windows and asserts the observations below. The two screenshots were visually inspected; this was an automated audition, **not a manual audition by Michael**. Resize/minimize/restore calls were issued; screenshots establish the resized content, not compositor acceptance of minimization.

`go test ./...` and `go vet ./...` passed in the new module. The shared `adapt` and `fixture` tests passed too, with loopback permission for the fixture's real HTTP server. Terminus review could not start: the canon has no `projects/dfx2/rubric.yaml`. There is no code-review verdict.

### Results

| Check | In-process | Loopback |
| --- | --- | --- |
| Address and application listener | `dfx2://spike/`; no TCP listener owned by the Go host in `ss` | `http://127.0.0.1:<random>/`; one listener owned by the host |
| Page and HTTP checks | **14/14 pass**, using the Windows checks | **14/14 pass** |
| Origin and browser APIs | `location.origin` is `dfx2://spike`; secure context, crypto and session storage work | Secure context, crypto and session storage work |
| Slow then fast | Fast finishes around 100 ms; slow around 3 s | Same |
| Fetch abort / reload | Browser abandons the request; Go handler runs its full 3 s | Go handler sees cancellation, around 300 ms / 500 ms respectively |
| Close with a handler pending | Context canceled, request settled, late result dropped; tracked work drains | Context canceled and server shuts down; tracked work drains |
| Other close paths | Startup, SIGTERM with work pending, and close with native body read pending pass | Startup, SIGTERM with work pending, and listener failure pass; failure exits 1 |
| Navigation | New windows return null; outside navigation refused | Same |
| Headers and limits | Repeated custom header becomes `one, two`; UTF-8 header reads `cafÃ©`; >1 MiB request gets 413 and response gets 502; subsequent requests succeed | Same header observations; larger bodies served normally |

All twelve driver scenarios passed. Every run ended with zero tracked native requests, zero native async operations and zero dispatcher handlers; loopback server shutdown also completed. This is explicit ownership accounting, not a leak-detector result. The two busy-close runs took about 10–27 ms from the closing log to the closed log. No GLib critical was logged.

### What carries into the design

**The common boundary is the Go handler, not a common URL implementation.** Windows intercepts an HTTPS origin. Linux registers a custom scheme as secure and CORS-enabled, without granting universal or local access. Relative URLs let the same fixture work in both. Before making the scheme a product default, test cookies, authentication, redirects, service workers and other origin-sensitive facilities directly; the passing fixture doesn't cover them.

**“In-process” removes the application HTTP listener, not WebKit's child processes.** The Go application directly invokes its handler. WebKit still owns web/network processes; their sockets were not surveyed.

**The ownership model carries over, but the native mechanics differ.** GTK objects remain on one locked UI thread. The request body is read with Gio's async API into bounded Go copies. Handler work happens on workers; one coalesced GLib idle source returns results to the owner. Avoid `g_main_context_invoke` for this wakeup: it can execute synchronously on the calling worker. Response bytes are copied into `GBytes` and a memory stream, so native objects do not retain Go memory. Four permanent callback trampolines route through numeric userdata.

**Native cancellation has to drain before destroying the window.** Close stops new posts and dispatcher work, cancels handler and Gio contexts, settles dispatched requests, hides the window, and continues pumping until the native async callbacks release their objects. The pending-body-read scenario exercised that branch. This experiment has no deadline for native cancellation callbacks; a stuck runtime could hold that part of teardown open.

**The same abandonment gap now has evidence on both platforms.** Neither in-process adapter propagates fetch abort or reload to its Go handler. Window close does cancel. Keep loopback first-class; don't promise `net/http` parity for a finite buffered transport or silently claim a per-request cancellation contract that these integrations don't implement.

**JavaScriptCore signal coexistence is unfinished.** Every native run logs JSC replacing the existing signal-10 handler. dfw already studies an optional private `JSConfigureSignalForGC` entry point and selection of an unused real-time signal. That implementation uses cgo, so it was not pulled into this experiment. A no-cgo solution needs its own validation before production use; neither hard-coding a signal nor hiding the warning resolves ownership.

Still untested: manual focus/typing/Tab/scrolling, inspector presentation, mixed DPI, X11, multiple windows, web-process crash recovery, cookies, redirects/authentication, streaming/SSE, WebSockets and aggregate load limits.
