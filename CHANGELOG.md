# Changelog

## Unreleased

- FEATURE: The `webview` package opens browser windows that share the application lifetime, with an explicit `InProcess`, `Loopback` or `Attach` mode and no fallback between them. Its portable contracts are built and tested against fake native seams: the finite in-process `http.Handler` adapter with reserved request, response, metadata, admission and aggregate limits; the window-owned loopback server with a per-window HttpOnly credential, Host/Origin checks, connection and handler limits and streaming; a framework content security policy on every owned response; and browser, endpoint and cleanup ownership that keeps `Done` and `Run` pending until everything admitted has returned. No platform hosts a browser yet: `Open` refuses with `ErrUnsupportedContent` before native work. See [webview.md](docs/current/webview.md).

- FIX: Paragraph caches own their UTF-8 keys and charge them against the existing budget, including through rewraps and eviction; substring keys no longer retain larger caller allocations.

- FIX: GTK painting checks Cairo context errors and propagates them through the application cancellation and drain path to `Run`, preserving a deferred dispatch wake so shutdown can finish.

- FEATURE: Windows/amd64 implements UI content presentation through `VirtualAlloc`-backed GDI and delivers bounded dispatch turns during native size and move loops, so frames and posted callbacks keep advancing for every window, with no timer running while idle. A core guard refuses any nested dispatch turn. Native [Windows validation](evidence/synthetic-mixer/windows/results.md) passed presentation, modal progress, idle, lifecycle and lock/RDP transition checks; real DPI transitions remain untested.
- FEATURE: `ui.Open` creates windows whose content is an immediate-mode view of labels and keyed animated meters, with any-goroutine `Invalidate`, per-window frame scheduling, monotonic one-shot animation pacing and no idle work once settled. Linux/amd64 renders on the CPU with pure-Go text (fallback, bidi, slight hinting, color emoji) under bounded per-window caches and presents through GTK and Cairo. See [ui.md](docs/current/ui.md), the [mixer example](examples/mixer/README.md) and the [stage 1 record](evidence/synthetic-mixer/stage-1.md).
- Added the no-cgo application/window runtime for Linux/amd64 (GTK 3) and Windows/amd64 (Win32), with independent windows and explicit window/resident application modes.
- Added bounded UI posting, owned cooperative tasks, failure propagation and shutdown diagnostics that remain observable while UI dispatch or final native release is stalled.
- Added the two-window lifetime example, controllable fake backend, lifecycle/concurrency and native ABI checks, and retained native validation on both hosts. See the [runtime contract](docs/current/runtime.md) and [final validation](evidence/foundation/stage-4.md) for behavior and platform limits.
