# Large capture diagnosis

Why the first stage 2 matrix (`../run/`) failed `large`: the driver's screen capture, not the fixture. `GetDIBits` returned zero lines with `GetLastError` 0 on four of five sizes while the fixture's retained snapshots matched every surface.

`main.go` (its own module, like `desk`) repeats the driver's capture sequence from the screen DC, with no window and no input, in variants:

- `base`: as `desk` captured, into a Go `[]byte`.
- `touch`: every page of the Go buffer written first.
- `valloc`: the destination from one `VirtualAlloc`.
- `chunk`: 64 rows per call into the Go buffer.
- `gc`: `runtime.GC()` before allocating.
- `retry`: a second call after a failure.
- `query`: `VirtualQuery` over the Go buffer, counting distinct allocation bases.

`probe-output.txt` is one run on 2026-11-06 of `base`, `valloc` and `query` on this host (Windows 10 10.0.19045 over Remote Desktop, 32 bpp). Every Go-heap failure spans two `VirtualAlloc` reservations, 4 MiB apart, and every success spans one; `valloc` never fails. Touching pages, chunking and retrying do not help. This is the failure the text spike recorded for `SetDIBitsToDevice` (`docs/future/text.md`), now seen from the reading side, and the reason the backend keeps each window's frame in one `VirtualAlloc` allocation.

The correction gives `desk` a `VirtualAlloc` destination.

`large-rerun-disconnected/` is the `large` case alone with the corrected driver, run by hand through `desk` at 13:57 local, and it is **not** evidence for the correction: Michael's Remote Desktop session had disconnected (`query session` showed session 2 `Disc` at 13:57:47 and a 1920×1200 `WinDisc` display), so every `BitBlt` from the screen failed with an invalid handle, and the 2400×1300 request was clamped to 1924×1181. It is kept for what the fixture did meanwhile: all five sizes presented with `paint_failures` 0, the five retained snapshots were written, and the run drained with `return` nil. The session state during the run is inferred from the later `query session` and the capture failures, not observed directly.

Build and run: `CGO_ENABLED=0 go build -C evidence/synthetic-mixer/windows/large-capture-diagnosis -o dibprobe.exe . && ./dibprobe.exe 3 base valloc query`.
