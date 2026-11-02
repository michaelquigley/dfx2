---
title: windows spike - a no-cgo window with the text spike
created: 2026-11-01
tags: [feature, spike]
state: researching
---

prove dfx2's no-cgo approach on Windows by giving `experiments/text-spike` a Windows window (`window_windows.go`, and narrowing `window_other.go` to neither Linux nor Windows). use plain Win32 through `syscall`, without cgo and without new dependencies:
- register a window class with a window procedure made by `syscall.NewCallback`. it's made once and never freed, like purego's.
- run the message loop on the locked main thread.
- present the spike's CPU frame in `WM_PAINT` with `SetDIBitsToDevice`. the frame is already premultiplied BGRA, which is what a top-down 32-bit DIB takes.
- be per-monitor DPI aware (v2): scale from `GetDpiForWindow`, and resize and redraw on `WM_DPICHANGED`.
- keep the zoom keys and the per-frame log.

build it from Linux with `GOOS=windows CGO_ENABLED=0 go build`. Michael runs it on Windows. record in `docs/future/text.md` how the go-text engine and its slight hinting look there next to Windows' own text, which is ClearType with DirectWrite's grid fitting, not FreeType's light hinting. say whether the Linux-tuned hinting reads right there.

## why

everything verified so far ran on Linux: purego, GTK and the text measurements. dfx and dfw both ship on Windows, where native calls go through `syscall` and COM instead, so this is the riskiest assumption still untested on the second platform, before the design depends on it.

## background

once a window draws, the natural next steps are separate cards: WebView2 through COM in the same process (its loader, environment and controller, hosted in this window), and a DirectWrite engine behind the text seam, which needs the engine-neutral seam card first. mygo's `internal/windows` and `internal/webview2`, and the "Windows" section of its architecture guide, show the mechanisms (ideas only; see `docs/future/mygo-study.md`).
