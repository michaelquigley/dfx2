# Synthetic mixer work order

Implement [synthetic-mixer.md](synthetic-mixer.md) in the stages below. Design review is closed with a `ready_to_build` verdict and both accepted testing advisories incorporated. Begin with stage 1 only; this work order does not authorize implementing every stage at once. The operator accepts and commits each stage before the next begins.

## Starting point

Foundation stages 1–4 are committed through `02b51a5`. Read [runtime.md](../current/runtime.md), then `app.go`, `window.go`, `internal/appcore`, and `internal/platform`. Preserve their lifetime, queue, failure, wake-retry, and shutdown contracts. The public types alias internal appcore types; root must not acquire UI/font or browser imports.

The GTK backend uses the locked startup thread and priority-200 idle dispatch. The Win32 backend delivers latched wakeups from `Pump`, outside its WndProc; `DefWindowProc`'s modal size/move loop currently prevents that delivery. Native retirement acknowledgments occur after native callback stacks return. Keep that ownership distinction when extending dispatch.

Grounding sources:

- [Text findings](text.md), `experiments/text-spike/text`, and its two `window_*.go` files: algorithms and native presentation, not production API. The spike uses purego v0.11.1, go-text/typesetting v0.3.5 and x/image v0.23.0. Start from these measured versions; do not upgrade as incidental work.
- `../sessionmixer/level_sampler.go` and `device_fader_ui.go`: real separation of sampled levels, device state and controls. Do not import Sessionmixer or its audio dependencies into the example.
- [Windows modal-loop follow-up](roadmap/dispatch-public-callbacks-during-win32-modal-size-and-move-loops.md).
- mygo remains ideas only; do not copy it or add it as a dependency.

## Implementation shape

Add public `ui` and private UI/layout/render/text packages as needed. Extend `internal/platform` with typed content events and surface operations, and expose only the narrow internal appcore attachment bridge needed by `ui.Open`. Platform packages must never import UI packages. Avoid a generic plugin system.

The bridge owns a content attachment until native teardown and its final callback have completed. Install routing before any native call that can callback. On attachment failure, close/drain the partially created window; do not leave a blank orphan or report success. Closing seals content work before canceling timers/capture and releasing pixels. Content panics and asynchronous rendering failures enter the existing fatal cancel-and-drain path, with context in the error. Invalid widget values/duplicate keys fail through that same programmer error path; they do not unwind through native callbacks.

Use `ui.Window` wrapping the ordinary window, forwarding its documented operations, and adding `Invalidate() error`. Invalidation returns `ErrClosed` after sealing and otherwise succeeds without using a public post slot. It marks a generation under the runtime's producer synchronization and shares the existing wake/failure machinery. Completing a frame clears only the generation it consumed. No goroutine is created per invalidation.

Record an immutable frame description on the UI thread. Internal tests can inspect layout, semantic metadata and draw commands without exporting them. Surface presentation accepts a complete premultiplied BGRA image and copies it synchronously into backend-owned retained storage. The source slice is never retained by native code. This extra copy keeps ownership simple for the first renderer; measure before replacing it with a public or borrowed buffer API.

Use one reusable Go CPU frame and one native retained image per UI window. Apply checked dimension/stride arithmetic and a 256 MiB limit to each image; exceeding the limit is a reported rendering failure. This accommodates ordinary desktop/4K windows while bounding accidental allocation. A resize replaces storage only after success; teardown frees it after the last native use.

## Stage 1 — Linux text and animated meter

Build the portable frame machinery, CPU renderer, text system and GTK presentation. Add `examples/mixer` initially showing a label and one animated meter, with a command-line option to pause its synthetic feed for idle checks. The UI package must compile on Windows and the existing unsupported target; until stage 2, Windows UI attachment returns an explicit unsupported-content error while ordinary root windows continue working.

### Scheduling and rendering

Give each window dirty state, visibility, next animation deadline and a frame counter. A single app-owned timer may service the earliest per-window deadline; there is still one scheduling state machine per window. Use one-shot pacing at 60 Hz for animation, based on monotonic time, not a claim of display refresh synchronization. Clear a window's animation request at frame start; widgets renew it only if another frame is needed. No live deadlines means no armed timer. Timer delivery uses the private wake path, carries no native Go pointer, and cannot render off the UI thread. Inject a clock for tests.

Service at most one frame per dirty window per dispatch turn, in rotating window order, alongside the existing bounded public dispatch. A frame requested while rendering waits for a later turn. Suspend deadlines while the backend reports the window hidden; on restoration render once using current monotonic time. The accepted GTK/Wayland exception in the spec permits continued rendering when minimization is not reported; do not add frame-clock gating or an inactivity heuristic to eliminate that work in this arc. Do not spin on zero-sized surfaces. Input/layout facilities added in stage 3 share this path.

Implement the meter with a keyed value/peak state, immediate attack and a linear peak decay of 0.5 normalized units per second. Request animation only while displayed peak exceeds the latest level. This simple observable behavior tests the scheduler; production audio ballistics are application work.

GTK adds a drawing area inside the existing native window. Configure/scale and map/unmap events copy primitive data into content state. Native draw callbacks only paint the retained image; they do not evaluate the application. Use a Cairo-owned image surface, copy Go pixels into its storage, mark it dirty, and queue drawing. Check Cairo status and destroy the surface after its final use. Retain the spike's device-scale handling. Create permanent purego callbacks per signature and route through numeric IDs; do not copy its per-window callback allocation pattern.

### Text

Move the tested pure-Go algorithms into private production packages. Define engine-neutral font/glyph IDs, owned shaped runs with cluster/direction/advance information, font metrics and glyph bitmaps. Keep go-text/fontscan types inside the engine adapter; if shared wrapping uses go-text's wrapper, provide an explicit conversion at that boundary rather than exposing its output as the neutral API. There is one engine, not a public engine-registration mechanism.

Keep shape-on-text/size change and wrap-on-width change separate. Deep-copy mutable glyph data before passing it to the wrapper. Preserve tests for spaces after emoji, rewrapping without lost spaces, fallback, and the spike's slight vertical hinting. Test mixed direction and cluster preservation. Raster cache keys include font identity, size/scale and quarter-pixel horizontal position; baselines stay on physical whole pixels. Never produce blank output solely because an available color glyph format is unsupported: use a visible fallback.

Use per-window caches initially: at most 16 MiB of glyph pixels and 1 MiB of shaped/paragraph cache payload with at most 256 paragraph entries. Track payload bytes as well as entry counts. Prefer a simple bounded page/entry eviction policy; a two-zone atlas is not required. Evict between frames, never invalidate current-frame references. An oversized cache entry is rendered transiently and not admitted; transient scratch is bounded by the surface budget and released after the frame. Font discovery/loaded-face ownership is separate from these cache budgets: document its lifetime and release per-window references on close.

### Stage 1 evidence

Test dirty-generation races, repeated invalidation without filling Post, deadline cancellation/close, minimize/restore, exposure without view replay, elapsed-time decay, multi-window fairness, panic/failure cleanup and allocation failure. Use a fake presentation sink that rejects use after teardown and a fake clock that fails on unwanted idle deadlines. Test renderer clipping and scale math with deterministic images; use pinned font fixtures with recorded licenses for text tests instead of relying on host font ordering.

Add a deterministic cache-pressure fixture with small injected budgets: each glyph or paragraph entry fits individually, but the current frame's combined working set exceeds the cache budget. Assert bounded cache admission, transient rendering within the scratch limit, and valid current-frame references throughout rendering. Force eviction between frames, then render again and rewrap a retained paragraph at a different width; verify correct glyphs and spacing, without reshaping solely because the width changed or reusing evicted storage.

Run the native Linux example: text/fallback, animation, pause-to-idle, exposure, resize/scale, close while a sample arrives, and repeated open/close. Capture frame/wake/paint counts over a five-second quiet interval after animations settle; no framework frame/deadline growth is expected, while OS exposure paints may occur. Record Wayland/X11 and actual scale coverage separately. Write `docs/current/ui.md` for what is built and `evidence/synthetic-mixer/stage-1.md`.

## Stage 2 — Windows presentation and modal scheduling

Implement the same content contract on Win32. Retain top-down BGRA pixels in `VirtualAlloc` storage and present with the proven GDI path. The text spike observed black large frames with Go-heap storage and successful native storage; preserve the workaround without claiming its suspected cause is established. Use `BeginPaint`/`EndPaint` for exposure, physical client dimensions, and the runtime's DPI handling. Presentation must not call the view from `WM_PAINT`.

Add an explicit core dispatch-turn guard shared by ordinary and modal delivery. The Win32 backend tracks entry/exit of the native size/move loop and native operation depth around creation, destruction and other synchronous backend calls that can reenter WndProc. During an established size/move loop, a timer owned by the application dispatch window may deliver a bounded core turn only when no turn is active and no such native operation is unfinished. A latched `WM_APP` wake schedules this delivery; it does not run user callbacks unconditionally inside WndProc.

Use a one-shot modal timer (kill on entry to its handler) when work is ready or an animation deadline is due. Re-arm only for remaining work/deadlines, with a positive delay to yield to native events. Never install a permanent idle timer. If a guard prevents delivery, retain the wake and re-arm at the next safe point; do not consume the only wake. Leaving the modal loop transfers pending work to normal Pump delivery. Late timer messages check live routing/generation and cannot resurrect closed content. Stop timers before dispatch-target release.

The permitted reentry is native timer delivery into an otherwise idle core, not a nested application callback. Native retirement acknowledgments still wait until the retiring object's callback stack has unwound. Test destruction initiated from a modal turn and reentrant messages during creation explicitly. Update runtime.md's modal-loop allowance only after the native test succeeds.

Microsoft documents [timer message delivery](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-timers); the guarded size/move scheduling above is our proposed integration and requires host validation, not an assertion that a timer alone solves reentrancy.

### Stage 2 evidence and host handoff

Extend the injected Win32 mechanism tests for nested wake/timer events, creation/destruction reentry, close from a frame, stale timers, lost-wake prevention, allocation failure, and retained-frame exposure. Repeat foundation lifecycle tests; preserve their bounded dispatch and cleanup properties.

Prepare `evidence/synthetic-mixer/windows/README.md` with exact build/run commands and an audition runner. Follow the established operator-mediated commit, Windows checkout, execution, commit and Linux return workflow. An unexecuted cross-build is preparation, not stage acceptance. The Windows host need not have the grimoire or Terminus; the handoff must be self-contained.

Run two animated windows; resize/move either for at least ten seconds while both frames and posted title counters advance. Pause feeds, settle meters, and verify idle counters; repeat with minimize/restore, exposure, close during resize and a large image beyond the spike's failure size. Record fonts, OS, RDP/local console, and actual DPI coverage. Keep synthetic DPI tests distinct from native monitor transitions. If modal dispatch cannot meet the guard and progress contracts, report the evidence for design revision; do not silently retain the old pause. Linux return checks and Terminus follow returned changes.

## Stage 3 — Layout and interactive controls

Implement scoped `Row`, `Column`, `Scope`, `Label`, keyed `Meter`, `Fader`, and `Checkbox` through the recorded frame. The first stage's simple vertical placement becomes the default root column. Retain normal Go callback composition.

`Layout` uses logical-unit width/height (zero means intrinsic), padding, gap, min/max sizes (zero max means unbounded), and a positive flex weight. Measure bottom-up; distribute remaining main-axis space among flex children, respecting their bounds, then arrange top-down. Nonflex children retain requested size. If minimum sizes exceed available space, keep the minima and clip overflow. Cross-axis items default to their measured size; no implicit stretching. Text wraps to its assigned width without reshaping. Resolve any measurement iteration from the record; never rerun the view.

Persist only keyed interaction/animation state and the last completed hit/focus layout. Use full scope paths for identity; validate sibling duplicates before publishing a frame. Prune unseen state at frame completion, cancel native capture for removed controls, and clear removed focus. The next Tab starts at the first available control. Carry roles/names/values in the internal record.

Normalize native events into copied logical coordinates, button/key/modifier values and ordered edges. Keep a bounded private input queue of 256 events per window; coalesce adjacent pointer motion for the same capture, never across button/key edges. Overflow cancels the active gesture and clears pending input, requests a fresh frame and emits a diagnostic; it must not leave a stuck button or pretend all edits were processed.

The value/changed API can represent one edit per widget per view evaluation. Process an input prefix stopping before a second value-changing edit to the same widget; preserve the suffix for the next scheduled frame. Route by stable identity from the completed hit layout, not a later list index. Do not collapse two mute activations into a false no-change or replay a handled edge. A removed target yields no edit. Render the edited value even when the caller supplies an older snapshot. Release emits a final value only if different from the last reported edit, avoiding duplicate identical commands.

Fader: left-button drag, native capture, vertical high-at-top mapping, clamp to [0,1]; arrows change by 0.01, Page Up/Down by 0.1, Home/End select endpoints. Checkbox: left-button activation on release inside, Space activation on key release. Tab/Shift-Tab traverse enabled controls. Focus loss, capture loss and window close clear pressed/capture state without synthesized activation.

GTK's [implicit pointer grab](https://docs.gtk.org/gtk3/input-handling.html) routes a button sequence back to the pressed widget. Use one focusable drawing area, subscribe to the needed masks and grab/focus-loss notifications, and test dragging outside it. On Windows use native capture and capture-change/focus-loss messages. Native callbacks copy events, never run the view. Keep callback allocation and native ownership rules identical to stage 1.

Test nested layout and clipping, text rewrap, stable identity across reorder, remove/reinsert, duplicate keys, focus traversal, capture loss, two activations before a frame, queued drag/release, queue overflow, and concurrent external updates during a drag. Count application evaluations and example command calls; make duplicate dispatch fail. Demonstrate keyboard/mouse controls on both native hosts, including outside-window release and independent focus in two windows. Update built docs and stage evidence, with Windows handoff/return as in stage 2.

## Stage 4 — Synthetic mixer and complete audition

Finish `examples/mixer` with six stable channel IDs, two windows over the same model, 30 Hz synthetic sampling, and controls to pause, reorder, remove/reinsert, inject external gain/mute changes, delay commands and reject commands. Use command-line options or console commands for fixture operations; do not add unneeded UI widgets just to operate the test harness.

Publish owned snapshots under synchronization, then invalidate subscribed windows. Removing a subscription on close must tolerate a racing publication. Register the shared producer and ordered command worker with `app.Go`, not either window. Use a bounded FIFO of 64 example commands with nonblocking submission and visible rejection. External snapshots never submit commands. Log accepted/applied/rejected/canceled sequence numbers so overload, delays and shutdown can be distinguished. Shutdown cancels outstanding synthetic work; the example does not promise to flush an audio device.

Run the end-to-end cases from the spec on both hosts, including concurrent external changes while dragging, reorder/removal during capture, keyboard-only operation, delayed/rejected/overloaded commands, closing either window, and idle after all sampling/animation settles. Exercise changing numeric labels and enough distinct text to force cache eviction; show bounded cache counters and correct subsequent rendering. Record CPU/frame timing as observations, not unmeasured performance promises. Include actual font/scale screenshots for human review and distinguish known glyph limitations from failures.

Add a deterministic combined fixture with delayed device snapshots and application-owned pending edit values: starting at mute=false and gain=0.50, toggle mute twice and press fader Up twice before acknowledgments arrive. Assert ordered commands true/false and 0.51/0.52, without reusing the stale snapshot as the base for each edit. Then inject command rejection and assert the example's documented reconciliation of pending values with authoritative state, visible error reporting, and no extra commands caused by that reconciliation. Keep this pending-value policy in the example, not in the framework widgets.

Publish a small idiomatic example usage section in current docs. Run the foundation native lifetime matrix again because content scheduling and modal dispatch alter its execution context. Reuse evidence only where the relevant runtime/runner sources are unchanged, identifying the tested revision.

## Validation and acceptance for every stage

Use `CGO_ENABLED=0` for tests, vet and builds. Run root tests/vet, repeated affected concurrency tests, Linux/amd64 and Windows/amd64 builds, and the existing Darwin/amd64 unsupported-stub build. The race detector requires cgo and is not a substitute for the deterministic concurrency fixtures here. Verify root-only dependency closure does not include UI/text packages. Native tests and auditions supplement the fake backend; they are not inferred from it.

Each stage records exact commands, revisions, platform and unavailable coverage under `evidence/synthetic-mixer/`, and updates current docs only for built behavior. Run `unfurl -i` on authored Markdown. Inspect the dfx2 canon rubric before implementation: flag new UI invariants or expired foundation exemptions for deliberate canon work instead of silently changing the sibling repo.

Run Terminus over the stage's implementation and bring findings to Michael under the grimoire's one-finding-at-a-time triage rule. A clean review and Michael's acceptance are required before advancing. Agents do not commit or push. Stage 2 and later Windows execution is part of acceptance, not a task left indefinitely behind the implementation.

At final acceptance reconcile built docs and the relevant roadmap cards; never touch `order.yaml`. Review canon drift against the built system, then retire these fulfilled intent documents through the normal convention. Keep the broader foundation/webview proposal where it still describes future work.
