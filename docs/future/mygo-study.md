# What mygo suggests for dfx2

mygo (`github.com/egoist/mygo`, MIT) is a young Go desktop framework. It was
studied at commit `da6e0fbf`, version 0.7.4, about five weeks and 950
commits old. It has two modes behind one platform layer:

- **Web pages in the system webview:** WKWebView, WebKitGTK, WebView2.
- **Native UI it draws itself:** package `ui`, rendered with Metal, Direct3D
  11, OpenGL or the CPU.

dfx2 doesn't adopt or copy it. This file records the mechanisms worth
rebuilding, so they can be weighed without re-reading mygo. Section names
refer to mygo's `docs/architecture.md`. A local clone lives at
`~/Sandbox/mygo`.

## How mygo's two modes compare with dfw and dfx

| | dfw | mygo webview mode | dfx | mygo native mode |
|---|---|---|---|---|
| Boundary | the product's HTTP server; no JS bridge | typed `postMessage` IPC, TypeScript client generated from Go types | direct Go calls | direct Go calls |
| Rendering | system webview | system webview | Dear ImGui, GLFW + OpenGL | its own scene, drawn by Metal, D3D11, OpenGL/WebGL or the CPU |
| Native interop | cgo (`webview_go`, C files) | purego and `syscall` | cgo (cimgui-go) | purego and `syscall` |
| Scope | deliberately narrow: no menus, dialogs, single instance or packaging | Electron-like: menus, dialogs, tray, updater, packaging CLI | components, actions, audio-tool widgets | a toolkit: layout, accessibility, IME, RTL, motion, rich text |

## Ideas, roughly in build order

**Text behind a narrow engine interface** (Native UI › "Text";
`internal/text/engine.go`).
- The engine does what is platform-specific: picks fonts with fallback,
  shapes, runs bidi, breaks lines and rasterizes glyphs.
- Shared Go code does everything else: line assembly, trimming, ellipsis,
  alignment, carets, selection and justification, so behavior matches
  everywhere.
- mygo's engines are DirectWrite, Core Text, Pango and a browser canvas.

Explored further in [text.md](text.md).

**Glyphs rasterized on demand.**
- Cached by font, glyph, scale and a subpixel x offset quantized to a few
  positions a pixel.
- Baselines snap to whole pixels.
- No pre-baked sizes, glyph ranges or merged icon fonts. That makes DPI
  changes and zoom free, and the node canvas gets crisp text at every zoom.

**A two-zone atlas** (Native UI › "Atlas lifetime"). There are coverage and
color atlases.
- **Zones:** a mask drawn for the first time lands in a transient zone that
  the next frame frees, and moves to a lasting zone when a second frame
  uses it, so animation never fills the atlas.
- **Overflow:** when the atlas overflows mid-frame, it repacks what the
  frame used, grows, and redraws the frame. No frame ever shows missing
  glyphs.
- **Uploads:** only dirty rectangles go to the GPU.
- **Icons:** SVG icons and paths are coverage masks in the same atlas,
  tinted like glyphs. Paths are keyed by shape snapped to quarter pixels.
  This could replace icon fonts.

**Text layout caching suited to immediate mode.**
- A cache keyed by a comparable parameters value, with styled spans encoded
  as a string so the struct stays a valid map key.
- Each element holds its last layout, so unchanged text costs nothing.
- `LayoutFrom` re-lays text out at a new width without reshaping when no
  line wraps.

**An immediate-mode API on a retained engine** (Native UI › "Frames";
`ui/runtime.go`).
- An element's identity is a hash of its parent's and its position or an
  explicit `Key`. Focus, scroll, editor and animation state hang off it.
  Generation-checked handles catch stale use in development builds.
- Widgets answer input within the same build (`Clicked()`).
- If a handler changes state while the view builds, the view rebuilds (up
  to 3 times) before painting, so there's no one-frame lag.

**Frames only on demand.**
- Frames come only from input, invalidation, timers, or a running
  animation.
- A settled window asks for no frame and arms no timer, and idles at about
  0% of a core.
- dfx sets no frame pacing of its own.

**Layout without dirty flags** (`ui/layoutcache.go`).
- The view rebuilds the whole tree every frame.
- Before layout, each element is compared with what its state kept from the
  last frame: its layout style, flags, text styles, tracks and children. An
  element that is the same and gets the same size reuses its boxes and
  measurements.
- Layout is flexbox (`layout.go`) and CSS grid (`grid.go`).
- dfx's 2026-10-20 journal shows the cost of having no layout: panels
  clipped under a new style, fixed widths replaced with measured remainders.

**A scene display list and interchangeable renderers** (Native UI › "Scenes",
"Renderers").
- A scene is a flat list of operations in device pixels: rounded rectangles
  with per-side borders, shadows, glyph runs, images, clips, effects.
- One shader draws nearly all of it as instanced quads, using signed
  distance fields for corners, borders and blurred box shadows.
- Operations hidden behind opaque fills are skipped. Large borders and
  shadows draw as scissored strips around where their output would be zero.
- A CPU rasterizer draws the same pixels with the same formulas. It's the
  fallback and the engine for pixel tests.

**Native interop without cgo** ("Native interop without cgo").
- purego on macOS and Linux, `syscall` and COM on Windows.
- purego callbacks are a scarce, never-freed resource: one per signature,
  routed by user data.
- Float arguments need purego, not `syscall`, on ARM64.

**A small platform contract** ("Layers", "Threading model").
- The core holds all policy. Backends only translate a synchronous
  `Backend`/`Window`/`Surface` contract, and a fake backend runs most tests
  without a GUI.
- The main goroutine is locked to the main thread. `postMain`/`onMain` make
  public methods safe from any goroutine.
- `await` pumps native events when called on the main thread, so a dialog
  can block inside an event handler without deadlocking.

**Tests that never depend on wall-clock time** ("Testing" › "Waiting in
tests").
- A headless `Tester` with a clock tests advance by hand.
- Tests wait for conditions or events, never sleep, and measure work by
  counts.

**Apps pay only for what they use** ("Binary size"; mygo's `AGENTS.md`).
- Opt-in features are reached through registration, never from paths every
  app runs.
- Never call `reflect.Type.Method` or `MethodByName` on those paths: it makes
  the linker keep every exported method (about 2.5 MB).
- Two reference apps are measured on every push.

**One framework for both modes** (Native UI › "Web views").
- A window's content is either a webview or a drawing surface.
- A webview can also sit inside a native window, under a transparent
  drawing surface, with the renderer cutting a rounded hole (`OpHole`)
  where the page shows. Native UI can paint over it.
- Separately: generated TypeScript types from Go API types (`internal/tsgen`),
  custom schemes serving an `http.Handler` instead of loopback TCP, and a
  per-page token so a reply can't resolve a promise of a page that has
  since navigated.
- dfw's HTTP boundary and its daemon + window topology are independent of
  all this.

**Also there, not yet weighed:** accessibility trees mapped to AT-SPI and UI
Automation, IME at the caret, right-to-left layout, an inspector,
virtualized lists, rich text, a code editor, shaders written in Go syntax,
and running native UI in browsers (wasm + WebGL 2).
