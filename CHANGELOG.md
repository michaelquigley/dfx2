# Changelog

## Unreleased

- FEATURE: Windows/amd64 implements UI content presentation through `VirtualAlloc`-backed GDI and delivers bounded dispatch turns during native size and move loops, so frames and posted callbacks keep advancing for every window, with no timer running while idle. A core guard refuses any nested dispatch turn. Native [Windows validation](evidence/synthetic-mixer/windows/results.md) passed presentation, modal progress, idle, lifecycle and lock/RDP transition checks; real DPI transitions remain untested.
- FEATURE: `ui.Open` creates windows whose content is an immediate-mode view of labels and keyed animated meters, with any-goroutine `Invalidate`, per-window frame scheduling, monotonic one-shot animation pacing and no idle work once settled. Linux/amd64 renders on the CPU with pure-Go text (fallback, bidi, slight hinting, color emoji) under bounded per-window caches and presents through GTK and Cairo. See [ui.md](docs/current/ui.md), the [mixer example](examples/mixer/README.md) and the [stage 1 record](evidence/synthetic-mixer/stage-1.md).
- Added the no-cgo application/window runtime for Linux/amd64 (GTK 3) and Windows/amd64 (Win32), with independent windows and explicit window/resident application modes.
- Added bounded UI posting, owned cooperative tasks, failure propagation and shutdown diagnostics that remain observable while UI dispatch or final native release is stalled.
- Added the two-window lifetime example, controllable fake backend, lifecycle/concurrency and native ABI checks, and retained native validation on both hosts. See the [runtime contract](docs/current/runtime.md) and [final validation](evidence/foundation/stage-4.md) for behavior and platform limits.
