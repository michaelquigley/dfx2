# dfx2 direction

dfx2 is in planning. Nothing is built yet: this document holds what has been
decided, why, and what is still open. It is intent, not description. When
code exists, what it does goes into `docs/current/`.

## What dfx2 is

dfx2 is the successor to two Go UI stacks, with one framework covering both:

- **dfx** (`../dfx`): immediate-mode UI on Dear ImGui through cimgui-go
  (cgo), GLFW and OpenGL, with a `Component`/`State`/action-registry API and
  widgets for audio tools (faders, VU meters, the node canvas, the file
  tree, the log viewer). Used by patchbay, baab, sessionmixer and
  research/motorist.
- **dfw** (`../dfw`): a webview window (WebKitGTK, WebView2, through
  `webview_go` and cgo) around a product's own HTTP server, plus a tray
  daemon and window processes that attach to it. Used by gloss,
  screeningroom, archive and ranger.

dfx2 drops Dear ImGui and cgo. It draws native UI itself and shows web UI in
the system webview, behind one platform layer. `df` (`../df`: dd, dl, da)
stays the low-level foundation, and dfx2 is the application and
user-interface layer above it. The API people write against keeps dfx's
immediate-mode feel.

## Decisions

**A repository of its own; dfx stays maintained.** dfx2 is built here, while
dfx (and dfw) keep being maintained until every product has migrated. There's
no in-place rewrite of dfx.

**No cgo.** Native code is called through purego on Linux and through
`syscall` and COM on Windows, never `import "C"`. Both current stacks need a
cgo toolchain and native headers to build, and can't cross-compile. mygo (see
[mygo-study.md](mygo-study.md)) shows the approach carries a whole framework:
GTK, WebKitGTK, Pango, Win32, WebView2, DirectWrite. The text spike opened a
GTK 3 window and drew into it through purego with `CGO_ENABLED=0` on Linux.
purego callbacks are never freed, so create one per signature at startup and
route by user data.

**The platform layer lives in dfx2.** The platform layer means the raw
bindings, the backends, the contract between them and the core, and the
main-thread loop. It stays in dfx2 until a second consumer, such as a
tray-only daemon wanting native dialogs, gives a reason to split it out.

- **Why not `df`:** `df` is platform-agnostic infrastructure that programs
  with no GUI use too.
- **Why not a separate repository now:**
  - `internal/` packages can't be imported across modules, so a separate
    repository would force every binding and backend to be public API.
  - Every feature that crosses the line would take two releases.

Keep the boundary as if the layer were separate: platform packages never
import UI packages. Enforce that with an import test, so splitting it out
later is mechanical.

**Text has two workable paths.** A pure-Go engine (go-text plus our own
slight hinting) looks nearly identical to GTK's text at UI sizes on the
target desktop. A Pango engine can sit behind the same seam. See
[text.md](text.md).

**mygo is a source of ideas, not a dependency.** dfx2 doesn't adopt mygo and
doesn't copy its code. Its ideas are rebuilt here where they earn a place.
[mygo-study.md](mygo-study.md) records them.

## Open questions

- **`internal/` or public platform packages.** The recommendation is
  `internal/`, with narrow public escape hatches: a window's native handle
  (documented as main thread only, not to be destroyed) and a public
  `OnMain(func())`. Promote a package to public only when a real consumer
  asks. Moving from `internal/` to public breaks no one; the reverse breaks
  everyone. If they're public from the start, keep the callback registry and
  the main-thread queue private, and have development builds check that
  bindings run on the main thread.
- **Which ideas earn a place, and in what order.**
  [mygo-study.md](mygo-study.md) lists them roughly in build order. The text
  stack is the furthest along.
- **The default text engine:** pure Go or Pango, and whether the engine is
  pluggable. Also whether LCD (subpixel) antialiasing matters. The target
  desktop uses it, but the hinted pure-Go engine without it already looked
  nearly identical.
- **Where line breaking lives.** The recommendation is shared code, over
  shaped runs, with break opportunities from go-text's `segmenter`. That
  keeps "shape once, rewrap at any width" for every engine.
- **How dfw maps in.**
  - Its HTTP-server boundary and its daemon + window topology are
    independent of rendering. Do they carry over as they are?
  - Should dfx2 generate TypeScript types from Go API types, and serve
    pages from a custom scheme where server and window share a process?
- **Platforms.** dfx and dfw target Linux and Windows. dfw's macOS support
  is deferred. Decide dfx2's scope.
- **Migration.** How much of dfx's API (`Component`, `State`, the action
  registry, the widgets) carries over unchanged, and how products move.
- **License.** The repository has none yet. dfx is Apache 2.0.
