# Linux WebKitGTK spike

An experiment in hosting the system webview from Go with **CGO_ENABLED=0**, through purego, with two transports into the same `http.Handler`:

- `loopback`: `net/http` on a random `127.0.0.1` port.
- `inprocess`: WebKit's registered `dfx2://spike/` URI scheme, with no application listener. This means one Go application process; WebKit still uses its own web/network processes.

This is a single-window experiment, not the dfx2 API. The sibling `../webview2-spike` module supplies the portable adapter, dispatcher, and fixture unchanged, through a local module replacement. The Linux wrapper changes only the fixture's labels and mode detection. Keep both experiments in the checkout. Neither the Windows host nor the native/text spike changes.

## Build and run

Requires Linux, a graphical session, GTK 3, WebKitGTK **4.1** (the Soup 3 variant), and WebKit **2.40 or newer** for request-body access. The tested host has WebKitGTK 2.52.6, GTK 3.24.52 and Soup 3.6.6. Development headers are not needed to build or run. Libraries are loaded at runtime; missing libraries or symbols produce an error. Only linux/amd64 was executed.

From this directory:

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-webkitgtk-spike .
/tmp/dfx2-webkitgtk-spike -mode inprocess -devtools
/tmp/dfx2-webkitgtk-spike -mode loopback -devtools
```

The page has the Windows experiment's controls: run all checks, slow/fast, abort, reload during slow, body limits, new windows, outside navigation, and editable fields. Close the window, Ctrl+C, or SIGTERM for normal teardown. Profiles are ephemeral. New windows and navigation outside the selected origin are refused; this is not a general sandbox for untrusted pages or external subresources.

The runtime prints JavaScriptCore's warning about replacing Go's SIGUSR1 handler on this host. This experiment leaves that collision visible. dfw's optional `JSConfigureSignalForGC` integration is a lead for subsequent no-cgo work, not something validated or ported here. Do not fix it by hard-coding a real-time signal or suppressing stderr.

## Checks and evidence

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test dfx2webview2spike/adapt dfx2webview2spike/fixture
python3 audition.py /tmp/dfx2-webkitgtk-spike /tmp/webkit-audition
```

The shared fixture tests need local sockets. The audition needs the desktop, Python 3, `ss`, `pkg-config`, and Go. It opens real windows sequentially, writes logs and WebKit-rendered PNGs, and fails on missing observations, wrong exit codes, GLib criticals or browser-check failures. The PNGs show web content, not window-manager decorations or proof of minimize behavior.

Recorded evidence lives in [`evidence/linux/`](evidence/linux/). Both modes pass **14/14** checks. The driver covers reload, close with a handler pending, SIGTERM with a handler pending, close during startup, and listener failure. The in-process-only `-close-reading` case closes with a real Gio body read pending, to exercise asynchronous cancellation and reference cleanup. `-resize-after` requests a resize, minimize and restore; a compositor may ignore those requests. Final screenshots were visually inspected.

The logs distinguish the lifetime contracts: abort/reload cancels loopback handlers; the in-process handler keeps running. Closing cancels both. Every recorded case drains tracked native requests, async operations, and handler workers. These counters are ownership checks, not a leak-detector result.

The loopback listener is visible in `ss`; the in-process host owns no TCP listener. Browser-child sockets were not surveyed. Keyboard focus, Tab, scrolling, manual window-manager interaction, inspector presentation, mixed DPI, X11, multiple windows, and browser-process failure remain unauditioned.

## Transport and lifetime

The scheme is registered as secure and CORS-enabled, without granting local or universal access. On this runtime it has origin `dfx2://spike`, secure context APIs, crypto, relative fetch, and session storage. That observation does not establish cookie, service-worker, redirect or authentication parity with HTTPS. Streaming, SSE and WebSockets are outside this buffered adapter.

The shared adapter caps request and response bodies at 1 MiB. Native request reads are asynchronous 64 KiB chunks; an oversized request is stopped at most one chunk beyond the limit, then receives 413 without running the handler. An oversized response becomes 502. There is no aggregate concurrency or memory limit: this is a trusted fixture, not production resource policy.

One locked GTK thread owns native objects. Four permanent purego trampolines route by signature and numeric userdata; none are allocated per request. Workers receive Go copies only. `g_idle_add_full` queues completions onto the main context; `g_main_context_invoke` could instead run on the worker. Native body reads and diagnostic JavaScript/snapshots use async tokens.

Close cancels the window context, closes the dispatcher and poster, cancels Gio operations, settles dispatched requests, and hides the window. The main loop keeps pumping until native async callbacks have finished and released their objects, then destroys the window. Worker/server draining follows. There is no timeout on native cancellation callbacks; a stuck runtime could hold shutdown open. All recorded cancellations completed promptly.

Request-body streams are owned references. Response headers transfer full ownership to `WebKitURISchemeResponse`. `g_bytes_new` copies Go response bytes; native streams never retain Go memory. The declarations were checked against the installed WebKit2-4.1 GIR and GTK/Gio/Soup headers. See WebKit's [URI-scheme registration documentation](https://webkitgtk.org/reference/webkit2gtk/stable/method.WebContext.register_uri_scheme.html) for the asynchronous request retention contract.

The experiment follows the Windows fixture's standard-library logging, so the evidence format remains comparable. This is not a production logging choice for dfx2.
