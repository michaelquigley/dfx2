---
title: windows spike - a no-cgo window with the text spike
created: 2026-11-01
tags: [feature, spike]
state: researching
---

execute [the Windows native-spike brief](../windows-native-spike.md) on Michael's Windows development host. extend `experiments/text-spike` with a no-cgo Win32 window, per-monitor DPI handling, and a synthetic background-driven meter that exercises sessionmixer's update pattern. audition the window and text on Windows, record the evidence and limitations in `docs/future/text.md`, and bring the implementation back for Linux integration checks. the brief carries the scope, stages, acceptance checks, and cross-host handoff.

## why

everything verified so far ran on Linux: purego, GTK and the text measurements. dfx and dfw both ship on Windows, where native calls go through `syscall` and COM instead, so this is the riskiest assumption still untested on the second platform, before the design depends on it.

## background

once a window draws, the natural next steps are separate cards: WebView2 through COM in the same process (its loader, environment and controller, hosted in this window), and a DirectWrite engine behind the text seam, which needs the engine-neutral seam card first. mygo's `internal/windows` and `internal/webview2`, and the "Windows" section of its architecture guide, show the mechanisms (ideas only; see `docs/future/mygo-study.md`).
