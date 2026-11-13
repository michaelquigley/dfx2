# Webview content

Stage 1 of the webview integration builds the public `webview` package and its portable contracts: explicit hosting modes, the private browser attachment boundary, shared ownership in the core, the finite in-process HTTP adapter and the owned loopback server. No platform hosts a browser yet. On Linux and Windows, as on every other target, `webview.Open` refuses with `webview.ErrUnsupportedContent` before any native work, and nothing is latched. Everything below is exercised through the fake backend and a fake browser; none of it is native evidence. The [stage 1 record](../../evidence/webview-integration/stage-1.md) lists the checks and what remains for the native stages.

The [runtime contract](runtime.md) still governs lifetime, threads, posting, failure and shutdown. This document covers only what browser windows add.

## Programming model

```go
w, err := webview.Open(app, webview.Options{
	Window:  dfx2.WindowOptions{Title: "Mixer", Width: 760, Height: 520},
	Mode:    webview.InProcess,
	Handler: mux, // an ordinary http.Handler, serving from "/"
})
```

`webview.Window` forwards `Close`, `Context`, `Done`, `SetTitle`, `Post` and `Go` with their documented thread rules. It exposes no native handles, readiness, navigation or JavaScript calls.

Every window chooses one mode, and nothing falls back between them:

| Mode | Required | Rejected | Owns |
| --- | --- | --- | --- |
| `InProcess` | `Handler`; optional `Limits` | `URL`, `Origins`, `Server` | The browser and every request it admitted |
| `Loopback` | `Handler`; optional `Server` | `URL`, `Origins`, `Limits` | The browser, its server, connections and handlers |
| `Attach` | `URL` (absolute http or https, no user information) and `Origins` including the URL's own | `Handler`, `Limits`, `Server` | The browser only; never the service |

The zero `Mode` is invalid. Origin entries are origins alone (scheme, host, optional port); a path, query or fragment is rejected. Origins are parsed and canonicalized with explicit effective ports, then compared as parsed origins, never by string prefix. `DevTools` enables the engine's inspector, for auditions; it is off by default.

## Opening, readiness and closing

A nil app is an immediate argument error. On a valid app, the wrong thread returns `dfx2.ErrWrongThread` and a closing app `dfx2.ErrClosed`, as operation errors. Invalid options and invalid dimensions are checked next, before any resource exists, and are returned and latched as application failures, as `NewWindow` does with its own. A platform without browser hosting refuses after that with `ErrUnsupportedContent`, before native work, and latches nothing; a starting window it refused is rechecked for last-window shutdown.

Otherwise the native shell is created and the window is live at once. In order, the core then obtains the shell's private parent descriptor, starts the endpoint (binding the loopback listener), creates the browser attachment and starts it. Any failure along that path is a fatal opening error, returned and latched; whatever was already acquired drains through the ordinary close path. `Open` returns once the attachment has started; the browser initializes asynchronously and asks the core before it navigates. The core refuses that navigation once the window is closing, so a late initialization result is released rather than navigated. A close that arrives during shell or browser creation skips the remaining steps: the browser is never created or never started.

Close cancels the window context at once. The endpoint's admission and sealing share its lock and its context is the window's, so no request is admitted after close. On the UI thread, the core then closes the endpoint and the browser while the shell is still valid. The shell is destroyed only after the browser reports that no native object or callback still uses it. The window record, and `Done`, wait until the browser has released, the endpoint reports that everything it admitted has returned, every published completion has settled and every cleanup task has finished. A handler that ignores cancellation therefore keeps `Done` and `Run` pending, and visible.

Browser windows count as live for last-window shutdown from the moment the shell is registered. They are not enrolled in frame or input scheduling: copied size and scale notifications are forwarded to the attachment as bounds.

## Failure tiers

An HTTP error status and a rejected application command are page results. Unexpected browser failures, reported by the attachment, are fatal: the application cancels and drains, and the error reaches `Run`. A shell destroyed while its browser still used it is the same kind of failure, and the browser still drains. An unexpected panic in an owned handler, in either owned mode, is an application failure with its stack; the page never sees the stack. `http.ErrAbortHandler` is a deliberate request abort and a deliberately returned 500 is an ordinary response; neither fails the application.

Post-release cleanup, such as removing a temporary profile directory, runs off the UI thread as owned work registered by the attachment before it releases. Unexpected cleanup errors and recovered panics cancel the application and remain in `Run`'s result alongside earlier failures; panics retain their stacks. The sole nonfatal exception is an explicitly identified file-deletion residue after native users have released the files and bounded deletion attempts have finished. Its path and cause remain in `Run`, but other windows keep running. The window stays registered until cleanup returns. The private attachment must return `platform.FileDeletionResidue` directly with a nonempty path and cause; wrapping or joining it does not exempt other failures, and panicking with it is still fatal.

## In-process adapter

The browser routes native requests by window to the core, which forwards them to the window's adapter on the UI thread. The adapter admits or refuses, copies the request into Go-owned values and starts the handler on a worker; it never calls application code on the UI thread. The worker's result is published through a completion registered before the worker started, and settled on the UI thread even while closing. A result for a closing window, or for a request the browser already released, is discarded rather than delivered.

Limits per endpoint; zero selects the default:

| Limit | Default | Failure |
| --- | --- | --- |
| Request body | 1 MiB | 413 |
| Response body | 1 MiB | 502 |
| Request or response metadata | 64 KiB (minimum 1 KiB) | 431 / 502 |
| Admitted requests | 16 | 503 |
| Aggregate adapter-owned buffers, native copies included | 64 MiB | 503 |

Metadata is measured before the URL is parsed, and charged as the UTF-8 bytes of the method and URL (or status) and every header name and value occurrence, plus 64 bytes per record and 64 per header occurrence for the Go structures that hold it. A response is measured on the handler's own header map before the adapter copies anything, so oversized metadata is never copied. Entries with nil or empty value slices still charge their names and entry overhead because the snapshot retains them, even though they emit no header field. The aggregate reservation additionally includes eight URL-input lengths plus 1 KiB for decoded/escaped URL forms, origin canonicalization, RequestURI construction and fixed URL/request structure overhead. This conservative allowance is reserved before cloning or parsing, retained until final request release, and rolled back on refusal. The origin check reuses the parsed URL. The public metadata ceiling still measures the incoming representation; neither it nor the aggregate budget is raised. Admission reserves a request's worst case before anything is buffered: its metadata and URL allowance, its body (the declared length, or the limit plus a detection byte when unknown), one native read buffer of at most 64 KiB, and a full response with its metadata, twice: as the Go result and as the browser's copy. Parts are released as their owners finish: the read buffer when the body ends, the body when the handler returns, the Go response (charged at its buffer's capacity) once the browser has copied it, the browser's copy when the browser releases the request. The admission slot and remaining bytes are released only when the browser and the handler have both finished, so an abandoned handler still holds them. With the defaults, sixteen worst-case requests fit the budget. Limits that are negative, that exceed the buffer limit individually, a buffer limit above 2^50 bytes, or limits that leave the budget unable to hold one worst-case request, are invalid options; the bounds keep every reservation sum exact in `int64`.

There is no queue: excess requests are refused at once. Refusals of requests that hold no admission (403 for a URL outside the endpoint's origin, 400 for a malformed request, 413, 431, 503) use one of 16 bounded slots reserved out of the buffer budget. With every slot in native delivery, the browser is told to abort the request instead, so even refusals cannot grow without bound. Failure responses are fixed and tiny; they never echo the request or a stack.

The body buffer is allocated once, at its reserved capacity, on the first read, so reading never holds two buffers at once; a body of unknown length therefore holds the full limit plus one byte until its handler returns. Body reads follow the declared length exactly: a body longer or shorter than declared gets 400, a read beyond the granted chunk or a failed native read aborts the request, and close cancels reads in progress. The handler sees `Method`, the path and query (`URL`, `RequestURI`), the request's headers, the finite body and `ContentLength`, `Host` from the browser-assigned origin, and the window context. The transport is synthetic: `RemoteAddr` is empty, `TLS` is nil, and there is no protocol negotiation, trailer or upgrade.

The `ResponseWriter` is buffered and offers neither `Flusher`, `Hijacker` nor `Unwrap`; `http.NewResponseController(w).Flush` reports `http.ErrNotSupported`. A body beyond the limit fails the whole response with 502, even if the handler ignores `Write`'s `ErrResponseTooLarge`; no successful prefix is ever published. Informational (1xx) responses, trailers and oversized response metadata also give 502. Headers are taken as they stood at `WriteHeader`; `Connection`, `Keep-Alive`, `Transfer-Encoding`, `Trailer`, `Upgrade` and the handler's `Content-Length` are dropped, `Content-Length` is set from the body, and an absent `Content-Type` is sniffed as `net/http` does. `HEAD` and bodiless statuses carry no body. An unexpected handler panic answers with a fixed 500 and fails the application; `http.ErrAbortHandler` answers with a fixed 502 and does not.

Reload and fetch abort do not cancel handlers: the browser releases its request and the handler keeps its admission until it returns. Close is the reliable cancellation boundary.

## Owned loopback server

Each window owns one server on `127.0.0.1` with an assigned port, serving ordinary HTTP: request bodies stream under the handler's control, and responses stream with no finite limit, write deadline or mandatory buffering. The `ResponseWriter` offers `Flusher` (and `FlushError`), never `Hijacker` or `Unwrap`, so no handler can take an unowned connection. Upgrade requests are refused with 501. The server's error log is discarded.

| Limit | Default | Failure |
| --- | --- | --- |
| Request header bytes | 64 KiB | `net/http`'s 431 |
| Header read timeout | 5 s | connection closed |
| Idle timeout | 30 s | connection closed |
| Live connections | 32 | closed before being served |
| Concurrent handlers | 16 | 503 |

Each endpoint generates a random 256-bit secret, which the browser is to install as an HttpOnly session cookie named `dfx2_endpoint`, scoped to the endpoint's origin, before its first navigation. Before any handler runs, the access layer requires that cookie (compared in constant time), the exact `Host` including the port, and, when an `Origin` header is present, exactly the endpoint's origin. A missing `Origin` is accepted only with both of the others. A request failing any check gets 403 without reaching the handler. The secret never appears in URLs, page script or `Status`. Access checking is separate from how the browser receives its cookie; no other access policy exists yet.

Close stops the listener, closes every connection the endpoint accepted and cancels request contexts. It does not wait: connections are counted from acceptance until closure and handlers from entry until return, independently of the server's shutdown bookkeeping, and the endpoint reports drained only when the serve loop, every connection and every handler have finished. Any accept failure other than the listener's own closing (`net.ErrClosed`) or a temporary error, which `net/http` retries, is fatal, even when it overlaps the window's close. It is captured at `Accept`, because `net/http` reports `ErrServerClosed` once shutdown has begun, and it is reported before the endpoint can report drained. A handler panic answers with a fixed 500, its staged headers discarded, or aborts a response already under way, and fails the application. Closing one window's endpoint never touches another's.

## Owned-response security policy

Every response produced through either owned endpoint adapter, including its fixed failures, carries this framework `Content-Security-Policy`:

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; media-src 'self'; object-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; manifest-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'
```

It is added after the handler's own headers, as the response is committed, so a handler can add its own policies, which browsers enforce as well, but cannot remove or relax this one. For a loopback handler that returns without writing, the wrapper adds the policy before net/http sends its implicit 200 response. It permits no inline script or style, no objects, frames or workers (service workers included), and no external form targets. Attached services receive no framework policy, credential or access layer.

Standard-library protocol errors sent before the loopback endpoint handler runs are a narrow exception: Go may send plain-text or bodyless rejections, such as malformed-request 400s and oversized-header 431s, without the framework CSP. They never reach the application handler. dfx2 does not replace the HTTP parser or rewrite raw connection output to add a header to those errors. The exception does not apply to application responses with the same status codes, implicit successful responses or the adapters' fixed failures, and does not relax native browser confinement.

## Private boundaries

`internal/platform` describes browser hosting with plain data and interfaces: content kinds (plain, drawn, browser), the browser-parent descriptor a backend supplies for a live browser shell, the browser configuration (mode, start URL, allowed origins, loopback credential, inspector), the factory and attachment, the host the core provides to it, and in-process request and response values. It imports no core, HTTP or content package. Browser factories are selected only by the `webview` package, so native-only applications never reach them. GTK and Win32 refuse browser shells with `ErrUnsupportedContent`. `internal/endpoint` holds both adapters and imports no native types; `internal/origin` canonicalizes origins for the adapters and, later, for browser navigation policy. The root package reaches none of `webview`, `internal/endpoint`, `internal/origin` or `net/http`; the dependency test enforces it.

Every attachment method runs on the UI thread except `Diagnostics`, which the shutdown supervisor calls while the UI thread may be stalled. Each request ID a browser receives from admission owes exactly one release, whatever the decision. Neither the core nor the adapters hold their locks while calling the other or the attachment, so reentrant notifications are safe. Shutdown diagnostics add, for each browser window, its attachment stage, endpoint status, cleanup names and the attachment's own summary; `Inspect` counts browser windows and running cleanups.
