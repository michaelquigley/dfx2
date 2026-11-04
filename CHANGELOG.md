# Changelog

## Unreleased

- Added the no-cgo application/window runtime for Linux/amd64 (GTK 3) and Windows/amd64 (Win32), with independent windows and explicit window/resident application modes.
- Added bounded UI posting, owned cooperative tasks, failure propagation and shutdown diagnostics that remain observable while UI dispatch or final native release is stalled.
- Added the two-window lifetime example, controllable fake backend, lifecycle/concurrency and native ABI checks, and retained native validation on both hosts. See the [runtime contract](docs/current/runtime.md) and [final validation](evidence/foundation/stage-4.md) for behavior and platform limits.
