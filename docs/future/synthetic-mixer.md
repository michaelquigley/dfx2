# Synthetic mixer

The next implementation arc after the committed application/window runtime. This narrows the native-UI portion of [foundation.md](foundation.md); its [work order](synthetic-mixer-work-order.md) supplies implementation stages and validation. Nothing here describes built UI behavior. Mercurius returned `ready_to_build` with no blocking findings on 2026-11-04. Both testing advisories were accepted and incorporated; the review closed on 2026-11-05. Implementation remains staged and subject to operator acceptance.

The result is a small, usable immediate-mode mixer with synthetic data on Linux and Windows: named channel strips, animated meters, gain faders, and mute checkboxes. It establishes the programming model Sessionmixer will use without requiring audio hardware or migrating Sessionmixer yet. The first visible result is a window containing text and an animated meter.

## Scope and boundaries

The accepted direction remains one framework, no cgo, no dfx/dfw source compatibility, ordinary Go composition, and shared application lifetime for native and eventual webview windows. This arc builds native content only. The existing runtime contract remains authoritative except for the explicit extension to scheduling during Windows size/move loops below.

CPU rendering and the pure-Go text path proven in the text spike are sufficient for this exercise. GPU rendering, native text engines, editable text and IME, accessibility integration, grids, scrolling, menus, dialogs, docking, webviews, and actual audio devices are subsequent work. Preserve widget roles, names, values, and identities so accessibility can be added without inventing them later. This is not a claim that the resulting toolkit is accessible or ready to replace all of Sessionmixer.

The following seam choices carry the accepted direction into this slice; new mechanisms in the work order remain proposals until review and acceptance.

| Boundary | Choice and reason | Revisit when |
| --- | --- | --- |
| Application model / rendering | Separate. Application owns gain, mute and samples; UI owns focus, capture, gesture and animation state. External changes must not become user commands. | A concrete application needs another interaction policy. |
| Model / device transport | Separate. The example uses snapshots and an ordered command worker; the framework knows no audio protocol. | Sessionmixer integration establishes device acknowledgment policy. |
| UI / native platform | Separate, private integration. UI supplies pixels and consumes copied events; platform owns native handles and presentation resources. | Another content type needs an additional concrete operation. |
| Layout / rendering | Record once, measure and draw the record. Application code and commands must not replay during layout. | A demonstrated layout need exceeds rows and columns. |
| Text shaping / shared text system | Separate through private engine-neutral data. One engine now; wrapping, caches and composition stay shared. | A second engine is justified by measured quality or cost. |
| Public API / internal escape hatches | No public raw handles, arbitrary renderer plugins or native callback injection. Internal fixture access is permitted. | A real consumer cannot express its operation through the typed API. |
| Errors by tier | Expected command failures belong to the example; native/rendering failures use runtime cancel-and-drain. Programmer mistakes fail visibly. | A recoverable content failure needs isolation. |

## Programming model

`ui.Open(app, options, draw)` creates one UI content window and returns a UI window wrapper plus an error. It uses the existing window options and lifetime. The wrapper exposes the ordinary window operations and an any-goroutine `Invalidate() error`; it does not expose native resources. Creating UI content and registering owned work retain the runtime's UI-thread rules.

The application supplies `func(*ui.Frame)`. A frame is valid only during that call. Ordinary methods and functions compose the view; there is no component base class or separate application state container. Illustrative API:

```go
func (m *Mixer) Draw(f *ui.Frame) {
    sample := m.source.Snapshot()
    f.Row(ui.Layout{Gap: 12}, func(f *ui.Frame) {
        for _, ch := range sample.Channels {
            f.Scope(ch.ID, func(f *ui.Frame) {
                f.Column(ui.Layout{Width: 120, Gap: 8}, func(f *ui.Frame) {
                    f.Label(ch.Name)
                    f.Meter("level", ch.Level)
                    gain, changed := f.Fader("gain", ch.Gain)
                    if changed {
                        m.SubmitGain(ch.ID, gain)
                    }
                    muted, changed := f.Checkbox("mute", "Mute", ch.Muted)
                    if changed {
                        m.SubmitMute(ch.ID, muted)
                    }
                })
            })
        }
    })
}
```

Faders and meters use normalized values. Device-specific units, taper, gain ganging, and optimistic acknowledgment belong to the application. Submission must return promptly; the example displays busy and device errors.

Every stateful widget has an explicit key beneath explicit scopes. Channel IDs survive reordering; list positions are not identities. Duplicate sibling keys are programmer errors. Removing a widget discards its interaction state and capture. Reintroducing its key later creates a fresh interaction.

Controls return a new value and `changed=true` only for user input. A snapshot update can move a fader or change mute without emitting a command. During a fader drag, the gesture owns its displayed value despite external updates. Release reports any remaining change, ends that ownership, and subsequent frames follow the supplied model. Focus loss, removal, or capture loss cancels the gesture without manufacturing a final command; previously returned edits remain application edits.

Keyboard users can traverse controls in recorded order, operate the fader and toggle mute. Focus is visible. Dragging continues outside the control and window while capture holds. Losing native focus/capture clears pressed state.

## A frame is one application evaluation

Input targets the last completed layout, with capture taking precedence. New controls become interactive after their first completed layout. Events cannot be retargeted to a different key merely because the model reordered. Disappeared targets are discarded and their interaction canceled.

For each frame, the framework advances pending interaction, calls the view once, measures the recorded rows/columns, and renders that record. Rewrapping text, solving flex sizes, and presenting an exposed window never call the view again. Widget output uses the returned edited value immediately. If application state changes after an earlier label was recorded, invalidate for a later frame instead of replaying the current view.

Rows and columns use logical units, padding, gaps, fixed or intrinsic size, minimum/maximum bounds, and flex distribution. They clip overflow predictably. There is no absolute-position escape hatch in this slice. Presentation scales logical geometry into physical pixels; input coordinates use the same scale.

## Scheduling and animation

Input, model invalidation, resize and scale changes request frames. Invalidations coalesce independently of the bounded public post queue. An update racing with rendering survives for a subsequent frame. Close seals invalidation immediately.

Each window has one frame scheduler. Widgets request another frame only while animation is active; they do not own tickers. Animation uses monotonic elapsed time, so missed frames do not slow time or cause a catch-up rendering burst. Meter decay is UI animation; sampling remains a separate application worker.

An unchanged, nonanimating window schedules no periodic work. Windows the backend reports as hidden suspend rendering and animation wakeups while retaining a dirty mark. Accepted GTK/Wayland exception: when GTK does not report minimization, the window may continue rendering while minimized. The [pacing experiment](../../experiments/gtk-pacing-spike/README.md) found that stronger suppression adds native scheduling state and cadence/restoration tradeoffs; avoiding that complexity takes precedence over eliminating this background work for now. Restoration renders the latest state and elapsed animation once. Exposing a valid existing image only presents it. There is no promise to detect compositor occlusion of an otherwise mapped window.

On Windows, moving or resizing one window must continue bounded public dispatch and frame progress for every open window. This intentionally replaces the foundation's documented modal-loop pause. It must not introduce recursive view or application callbacks, callbacks during partially completed native creation or destruction, or an always-running timer. The Windows stage proves this before controls depend on it.

## Rendering and text

Render an opaque window image on the CPU with clipping, filled rectangles, lines, and shaped text. Native backends retain the last complete image for exposure. They own any storage passed to native presentation beyond a synchronous call. Resizing and allocation failures must not leave a stale Go slice referring to freed native memory.

Promote the useful text-spike mechanisms: shaping and font fallback, shared wrapping, slight vertical hinting, fractional horizontal glyph positioning, and separate mask/color glyph storage. Engine-neutral records own their data; mutable third-party shaping buffers cannot leak into cached paragraphs. Changing wrap width reuses shaping. Cache identity includes all inputs that affect shaping or rasterization, including physical scale.

Bound caches and release window resources on close. The spike's ever-growing atlases are not an acceptable production default. Eviction must preserve glyphs referenced by the current frame. A visible missing-glyph fallback is preferable to silently blank unsupported color glyphs; full Windows COLR support is not a requirement of this slice. Evidence must state the tested fonts and limitations.

## Synthetic application and acceptance

The example uses a synchronized snapshot source and a separately owned command worker. It exercises several stable channel IDs, reordered and removed channels, external gain/mute changes, changing levels, and two simultaneous windows over the same model. One window's focus and drag state never leaks to the other. Closing either window leaves the other and the application-owned source alive.

Accepted commands preserve order. Queue-full rejects visibly; it never silently drops or coalesces gain/mute edits. Meter snapshots may coalesce. Injected device delay and rejection demonstrate that drawing remains responsive and external state never echoes back as a command. Closing cancels cooperative source/command workers through the existing runtime and reports abandoned example commands without pretending they reached a device.

Completion requires deterministic tests of the frame/interaction contracts and actual Linux and Windows auditions of the resulting implementation. Evidence distinguishes cross-builds from execution and native scale changes from synthetic scale tests. The result must show text, working mouse and keyboard controls, responsive resize, independent windows, finite cache use, and idle scheduling that becomes quiet when sampling and animations stop.
