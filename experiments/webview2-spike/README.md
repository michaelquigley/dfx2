# webview2-spike

Hosts one embedded page in WebView2 from Go with `CGO_ENABLED=0`, and serves it through one `http.Handler` two ways:

| `-mode` | How the page reaches the handler |
| --- | --- |
| `loopback` | An `http.Server` on `127.0.0.1:0`; the webview navigates to the assigned address. |
| `inprocess` | The webview navigates to `https://dfx2-spike.invalid/`. Requests for that exact origin are intercepted (`WebResourceRequested`) and dispatched to the handler directly. No listener, and no fallback to loopback. |

It's an experiment for [the WebView2 spike brief](../../docs/future/windows-webview2-spike.md). Findings are in [docs/future/webview.md](../../docs/future/webview.md). The bindings and the in-process adapter are experimental and not a dfx2 API. The adapter handles finite, buffered HTTP only.

## Prerequisites (Windows, amd64)

- **Go** 1.26 or later. No C compiler.
- **WebView2 Runtime** (Evergreen). Windows 11 includes it. On Windows 10, install it from <https://developer.microsoft.com/microsoft-edge/webview2/>. The host logs the version it finds and refuses to start, with instructions, if there is none.
- **`WebView2Loader.dll`**, x64, from the WebView2 SDK. It's not part of Windows. `setup-loader.ps1` downloads the pinned SDK (NuGet `Microsoft.Web.WebView2` **1.0.4129.50**) and checks the package's SHA-256, the loader's SHA-256 and the loader's Authenticode signature. It then copies the loader beside the executable. The host loads the loader only from that absolute path (or `-loader`), never from the search path, and logs its hash against the pin.

| File | SHA-256 |
| --- | --- |
| `microsoft.web.webview2.1.0.4129.50.nupkg` | `d3934f482d484b89fb4825df720c710664e1143a1e90f7b3a60794ef33f473d2` |
| `build/native/x64/WebView2Loader.dll` (file version 1.0.4129.50, signed by Microsoft Corporation) | `a9a09232c25805323d4cfb3fc8f545a190a9c8a99c93262ea99d0b88df99ec90` |

None of the following is tracked by git: `.sdk/` (the unpacked package, including `WebView2.idl`), the DLL, executables, and the `webview2-userdata/` profile.

## Build, test, run

From this folder, in PowerShell:

```powershell
.\setup-loader.ps1
$env:CGO_ENABLED = '0'
go test ./...
go vet ./...
go build -o dfx2-webview2-spike.exe .
.\dfx2-webview2-spike.exe -mode loopback
.\dfx2-webview2-spike.exe -mode inprocess
```

Run the built executable. `go run` puts it in a temporary folder, where the loader isn't. The log goes to stderr.

Check the bindings against the SDK's own IDL. The test skips unless pointed at the IDL, and it runs on Linux too, given the unpacked package:

```powershell
$env:WEBVIEW2_IDL = "$PWD\.sdk\1.0.4129.50\WebView2.idl"; go test -run IDL -v .
```

On other platforms, `go test ./...` and `go vet ./...` cover the handler, the adapter and the IDL check. The executable builds there and exits with "the WebView2 host runs only on windows/amd64".

### Flags

| Flag | Meaning |
| --- | --- |
| `-mode loopback\|inprocess` | Required. |
| `-devtools` | Enable devtools and open them after the first navigation. Without it, devtools are disabled (F12 does nothing). |
| `-autorun checks\|slow\|reload` | Run a page script on load, for the driver: all checks, one slow request, or reload during a slow request. |
| `-user-data DIR` | WebView2 profile folder. Default: `webview2-userdata\<mode>` beside the executable, never a browser profile of yours. |
| `-loader PATH` | `WebView2Loader.dll` to load. Default: beside the executable. |
| `-runtime-dir DIR` | Fixed-version Runtime folder. An empty folder simulates a missing Runtime. |
| `-width`, `-height` | Client size in DIPs (1000×760). |
| `-break-listener D` | Loopback only: close the listener under the server after `D`, to show that a server failure ends the app (exit 1). |
| `-close-during env\|controller` | Test hook: post `WM_CLOSE` just before that creation call. |

## The page

The same page, assets and bytes in both modes. It uses only relative URLs, and every response carries `Cache-Control: no-store`.

| Route | Purpose |
| --- | --- |
| `GET /`, `/app.css`, `/app.js` | The page and its assets (explicit content types). |
| `GET /api/query?…` | JSON echo of method, path, raw and parsed query, `Host`, `X-Spike-Request`. |
| `POST /api/echo` | JSON: body length, SHA-256, content type, and the text if it's small valid UTF-8. |
| `GET /api/binary` | 267 bytes: 0x00–0xFF, then NUL, CR LF, invalid UTF-8 and a BOM. |
| `GET /api/status/404`, `/api/status/500` | Handler-made error responses (JSON). Any other path gets the mux's own 404. |
| `GET /api/headers` | `X-Spike-Repeat: one` and `X-Spike-Repeat: two` as two header lines, plus a raw UTF-8 header value. |
| `GET /api/slow?tag=` | Answers after 3 s, or returns at once when its context is canceled, logging `saw cancellation`. |
| `GET /api/big` | 2 MiB: over the in-process adapter's limit. |
| `POST /api/report` | Logs a line from the page into the host log (the page's evidence). |

Controls: **Run all checks** (PASS/FAIL/INFO per check, also reported to the host log), one button per check, slow/fast/race/abort/reload-during-slow, the two oversized requests, outside `window.open` and navigation (buttons and links), and three text fields for focus and Tab.

Closing: the window's X, `WM_CLOSE`, Ctrl+C, Ctrl+Break and closing the console all take the same close path. The console events arrive as `os.Interrupt` or `SIGTERM` and are turned into a posted `WM_CLOSE`. Run from a terminal that shares a console with others, the host otherwise dies with `0xC000013A` and skips its close path.

## Reading the log

Both modes use one vocabulary: `req N start`, `req N handler done|canceled`, and in-process also `req N response installed|settled on close`, with outstanding counts. In-process ids come from the dispatcher and loopback ids from the server middleware: separate sequences. Close prints `close:` lines (state, pending work, controller close, worker drain) and two `exit:` lines. Those count callbacks, any off the UI thread, late creation callbacks, and callback objects still referenced, which should be none.

## Audition driver

`evidence/windows/drive` launches the real executable and drives its real window. It closes the window, resizes, minimizes and restores, captures the screen, lists the process's TCP sockets, makes ordinary HTTP client requests in loopback mode, and times exits. It writes per-run logs, captures and `summary.txt`. It can't type, tab or scroll inside the webview; those need a person.

```powershell
go build -o drive.exe ./evidence/windows/drive
.\drive.exe -exe .\dfx2-webview2-spike.exe -out $env:TEMP\wv2-drive            # everything, ~2.5 min
.\drive.exe -exe .\dfx2-webview2-spike.exe -out $env:TEMP\wv2-drive -scenarios checks,close-busy -modes inprocess
```

Scenarios: `checks`, `resize`, `close-idle`, `close-busy`, `reload`, `close-init` (closes 0–600 ms after the window appears, plus the `-close-during` hooks), `console-break` (Ctrl+Break with a slow request pending), `repeat`, `server-failure` (loopback), `prereq` (missing loader, loader not beside the exe, simulated missing Runtime). The driver starts each host in its own process group, so a Ctrl+C in the driver's console doesn't reach it. Captures need an unminimized interactive session; a minimized RDP client captures black.

## Limitations (by design here)

- Request and response bodies are buffered. The limit is 1 MiB each way, an experiment limit: larger requests get 413, larger responses 502, each marked `X-Spike-Adapter-Error`. Nothing is truncated. The buffered writer has no `Flusher`/`Hijacker`, so streaming, SSE and WebSockets aren't offered in-process.
- In-process, a fetch the page aborts or abandons (by reload) does not cancel the Go handler. The handler runs to completion, and the response is installed into a request nobody waits for. Only closing the window cancels in-process handlers. Loopback cancels on connection close. See webview.md.
- In-process, repeated response headers reach the page joined (`one, two`), and devtools shows them as one joined value. The adapter passes WebView2 separate lines; WebView2 merges them. Loopback keeps two lines on the wire.
- One window, top-level documents only (no iframe navigation checks), no cookies, redirects, service workers or custom schemes.
- windows/amd64 only. On arm64, `put_Bounds` passes its 16-byte RECT differently.

## Integration feasibility

The shared package [`feasibility`](feasibility/) holds the portable pieces of the [webview feasibility spike](../../docs/future/webview-feasibility-spike.md): the owned endpoint's access guard, the probe page and handler, and bounded deletion after native release. The Linux host uses it now. The Windows probes have not been written or run; the [Windows handoff](evidence/windows/integration-feasibility/README.md) lists what landed, what remains and how to run it.

