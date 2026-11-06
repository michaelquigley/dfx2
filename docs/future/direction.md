# dfx2 direction

The foundation runtime is built: application/window lifetime on Linux/amd64 and Windows/amd64 is documented in [runtime.md](../current/runtime.md). The first native UI stages also provide text and animated meters on both platforms; [ui.md](../current/ui.md) describes built behavior. Measured layout, interactive controls and webview integration remain future work. This document holds the broader design decisions, their rationale and open questions.

## What dfx2 is

dfx2 is the successor to two Go UI stacks, with one framework covering both:

- **dfx** (`../dfx`): immediate-mode UI on Dear ImGui through cimgui-go (cgo), GLFW and OpenGL, with a `Component`/`State`/action-registry API and widgets for audio tools (faders, VU meters, the node canvas, the file tree, the log viewer). Used by patchbay, baab, sessionmixer and research/motorist.
- **dfw** (`../dfw`): a webview window (WebKitGTK, WebView2, through `webview_go` and cgo) around a product's own HTTP server, plus a tray daemon and window processes that attach to it. Used by gloss, screeningroom, archive and ranger.

dfx2 drops Dear ImGui and cgo. It draws native UI itself and shows web UI in the system webview, behind one platform layer. `df` (`../df`: dd, dl, da) stays the low-level foundation, and dfx2 is the application and user-interface layer above it. The API people write against keeps dfx's immediate-mode feel.

**Programming model and first application.** Source compatibility with dfx or dfw is not a goal. Preserve the immediate-mode programming model, with a smoother, more idiomatic Go API. Sessionmixer is the first real application that will guide the native UI work.

The next design discussion is the [application and UI foundation proposal](foundation.md): shared application/window lifetime, a synthetic mixer API, explicit webview transports, and an implementation sequence. Choices not recorded under Decisions below remain proposals.

## Decisions

**Native and webview windows coexist in one application.** An application can open immediate-mode native windows and webview windows together, sharing the event loop, lifecycle machinery and application services. Closing one window leaves the others running. Initially each window has one content type; embedding webviews alongside native controls inside the same window is deferred.

**Application lifetime has two explicit modes.** A window application begins shutdown when its last window closes. A resident application keeps running with no windows until explicitly quit, supporting background services and future tray integration. Closing a window cancels only its owned work; application-owned shared services live until application shutdown. Shutdown cancels owned work, continues pumping native cleanup callbacks, and returns from `Run` after cleanup completes. Cancellation is cooperative: stuck work must remain visible as outstanding, not be reported as cleaned up.

**Application values and widget interaction state have separate owners.** Applications own values such as gain, mute and meter readings. Widgets retain focus, capture and drag state, associated with stable keys. A widget receives the current value and returns changes caused by user input; an external value update requests a redraw without becoming another user command. Ordinary Go functions compose the interface, without a required `Component` base type or framework-owned application-state container. Exact API spelling remains open.

**Layout starts with measured rows and columns.** Scoped callbacks express nesting, and ordinary helper functions compose their contents. The framework measures and positions controls using gaps, padding, minimum/maximum sizes and flexible space, in logical units. Grid and absolute positioning wait for a concrete interface that needs them. Exact API spelling remains open.

**Frames run on demand, including animation.** Input, resize, scale changes and explicit invalidation request frames; pending invalidations coalesce around the latest published state. Active animations request subsequent frames through a shared per-window scheduler, paced to the display where supported. Progress uses monotonic elapsed time rather than frame counts. Once activity settles, the window has no permanent redraw timer. Animation retains presentation state under stable control keys without changing application values or generating user commands; direct dragging follows the pointer immediately. Hidden or minimized windows can suspend drawing and catch up when shown. The view runs once per frame; layout or rendering retries reuse recorded output, and consumed input is not replayed.

**Webview transport is explicit.** Keep ordinary Go `http.Handler` as the endpoint boundary. Choose in-process dispatch for finite buffered requests/responses with defined limits, an owned loopback server for normal HTTP facilities, or attachment to an existing server whose lifetime remains independent. Never fall back automatically between transports. Closing a window cancels work owned by its endpoint; the initial in-process contract does not promise handler cancellation on fetch abort or page reload. Detailed limits, origin policy and API spelling remain open.

**A repository of its own; dfx stays maintained.** dfx2 is built here, while dfx (and dfw) keep being maintained until every product has migrated. There's no in-place rewrite of dfx.

**No cgo.** Native code is called through purego on Linux and through `syscall` and COM on Windows, never `import "C"`. Both current stacks need a cgo toolchain and native headers to build, and can't cross-compile. mygo (see [mygo-study.md](mygo-study.md)) shows the approach carries a whole framework: GTK, WebKitGTK, Pango, Win32, WebView2, DirectWrite. The text spike opened a GTK 3 window and drew into it through purego with `CGO_ENABLED=0` on Linux. purego callbacks are never freed, so create one per signature at startup and route by user data.

**The platform layer lives in dfx2.** The platform layer means the raw bindings, the backends, the contract between them and the core, and the main-thread loop. It stays in dfx2 until a second consumer, such as a tray-only daemon wanting native dialogs, gives a reason to split it out.

- **Why not `df`:** `df` is platform-agnostic infrastructure that programs with no GUI use too.
- **Why not a separate repository now:**
  - `internal/` packages can't be imported across modules, so a separate repository would force every binding and backend to be public API.
  - Every feature that crosses the line would take two releases.

Keep the boundary as if the layer were separate: platform packages never import UI packages. Enforce that with an import test, so splitting it out later is mechanical.

**The platform implementation is private.** Keep GTK, Win32, COM, native callbacks, browser bindings and dispatch machinery under `internal/`. The public surfaces are `dfx2` for application/window lifetime and UI-thread posting, `ui` for immediate-mode controls/layout/animation, and `webview` for handler-backed or existing-URL windows. Native-only applications must not load WebKit or WebView2. Add a narrow native-handle escape hatch only when a real integration needs it, with explicit thread and lifetime rules; a public platform package would need a concrete consumer.

**Text has two workable paths.** A pure-Go engine (go-text plus our own slight hinting) looks nearly identical to GTK's text at UI sizes on the target desktop. A Pango engine can sit behind the same seam. See [text.md](text.md).

**mygo is a source of ideas, not a dependency.** dfx2 doesn't adopt mygo and doesn't copy its code. Its ideas are rebuilt here where they earn a place. [mygo-study.md](mygo-study.md) records them.

## Open questions

- **Which ideas earn a place, and in what order.** [mygo-study.md](mygo-study.md) lists them roughly in build order. The text stack is the furthest along.
- **The default text engine:** pure Go or Pango, and whether the engine is pluggable. Also whether LCD (subpixel) antialiasing matters. The target desktop uses it, but the hinted pure-Go engine without it already looked nearly identical.
- **Where line breaking lives.** The recommendation is shared code, over shaped runs, with break opportunities from go-text's `segmenter`. That keeps "shape once, rewrap at any width" for every engine.
- **How dfw maps in.**
  - Support a single-process approach with an in-process HTTP endpoint, while retaining the loopback model. The [Windows WebView2 spike](windows-webview2-spike.md) explores socket-free request interception into a Go `http.Handler` alongside loopback; the permanent transport contract remains to be specified.
  - Its HTTP-server boundary and its daemon + window topology are independent of rendering. What lifecycle behavior carries over?
  - Should dfx2 generate TypeScript types from Go API types, and serve pages from a custom scheme where server and window share a process?
- **Platforms.** dfx and dfw target Linux and Windows. dfw's macOS support is deferred. Decide dfx2's scope.
- **Migration.** What does sessionmixer need from the new immediate-mode API, and how do products move? Source compatibility is not required.
- **License.** The repository has none yet. dfx is Apache 2.0.
