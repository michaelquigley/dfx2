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
- **Reload or abort doesn't reach an in-process handler: a real gap.** When the page aborts a fetch (`AbortController`) or reloads during `/api/slow`, the browser side ends at once. In-process, the Go handler runs its full 3 s, and its response is installed into the abandoned request with `S_OK`. WebView2 gives no signal for an abandoned request, and only closing the window cancels in-process handlers. In loopback, the same abort and reload cancel the handler in 300–520 ms, because the connection closes. Michael reproduced the reload case by hand (F5).
- **A console event bypassed close entirely, and now doesn't.** In the first manual audition, both windows died together with exit `0xC000013A` (`STATUS_CONTROL_C_EXIT`) when the terminal that launched them was closed or interrupted: no request settled and no controller closed. The host is a console program. It now turns Ctrl+C, Ctrl+Break and console close (`os.Interrupt`, `SIGTERM`) into a posted `WM_CLOSE`. **Ctrl+Break was tested** (exit 0 in ~40 ms, the pending request settled). Console close (`CTRL_CLOSE_EVENT`), logoff and shutdown are **untested**.

### Capability

| Behavior | In-process status |
| --- | --- |
| Finite GET/POST, query, request headers, UTF-8 and binary bodies ≤ 1 MiB | **Demonstrated.** |
| Status codes and reason phrases, content types, `Cache-Control` | **Demonstrated.** |
| Repeated response headers | **Merged:** they reach JavaScript as `one, two` (the same in loopback), but devtools shows one joined value. Unsuitable for headers that can't be comma-joined (`Set-Cookie`). Building headers with `ICoreWebView2HttpResponseHeaders::AppendHeader` instead of a raw string is an **untested** alternative. |
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
- **Untested here, and still needed:** DPI changes and mixed DPI; the WebKitGTK side; a removed Runtime; console close/logoff; streaming and WebSockets; multiple windows.

### Linux return checks

Pending: the Linux session hasn't run these yet. See the handoff in [the 2026-11-03 journal](../journal/2026-11-03.md).
