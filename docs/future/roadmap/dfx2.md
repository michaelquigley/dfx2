---
title: dfx2
state: building
created: 2026-11-01
tags: [epic, spike]
milestone: v0.2.x
source: study of github.com/egoist/mygo (docs/architecture.md), 2026-11-01
---

shape `dfx2`: a Go-native successor to dfx that drops Dear ImGui and cgo, and absorbs dfw, so one framework covers both native UI that dfx2 draws itself and web UI in the system webview (WebKitGTK, WebView2). dfx and dfw stay maintained until every product has migrated. `df` stays the low-level foundation (da/dd/dl); dfx2 holds the application and user-interface layer. the first output is a design, not code: settle the open questions in `docs/future/direction.md` (the platform implementation stays private in dfx2), decide which ideas in `docs/future/mygo-study.md` earn a place and the order to build them in, and carry the text findings in `docs/future/text.md` into it. preserve immediate-mode programming with idiomatic Go function composition and application-owned values, without source compatibility or required `Component`/`State` types; use sessionmixer as the first application.

## why

dfx and dfw are two stacks with two native dependencies (cimgui-go with GLFW, and webview_go with dfw's own C files), both needing cgo, and neither shares window, lifecycle or platform code with the other. ImGui also limits what dfx can grow into: font atlases are pre-baked at fixed sizes with glyph ranges and merged icon fonts (dfx's `fonts.go`), there is no layout system, so panels clip under a new style (dfx's `docs/journal/2026-10-20.md`), and input methods are limited, with no accessibility, text shaping or automatic font fallback.

## background

this card was captured in dfx on 2026-11-01 and moved here when the repository was created. the decisions so far and their reasons are in `docs/future/direction.md`; the mechanisms worth rebuilding from mygo are in `docs/future/mygo-study.md`; the text spike, its measurements against Pango, and the problems it hit are in `docs/future/text.md` and `experiments/text-spike`.
