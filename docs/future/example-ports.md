# Representative dfx example ports

Draft for review. Before integrating Sessionmixer, exercise dfx2 with four small native examples adapted from `../dfx`. Port their useful behavior into the immediate-mode Go API; source compatibility and a complete copy of the old gallery are not goals. The accompanying [work order](example-ports-work-order.md) starts with counter and composition, then moves to faders and meters.

The purpose is to make the framework pleasant to use and reveal ordinary missing capabilities before a device application obscures them. The accepted mixed mixer remains the native/browser integration fixture. These examples need no browser, hardware, command protocol or new lifecycle architecture.

## Survey and selection

The survey covered the 24 example directories in dfx at `92848de`. Its examples mix framework demonstrations with direct ImGui calls; copying every control in a selected example would import much more scope than its purpose requires.

| dfx source | dfx2 example | What it should teach | Required framework work |
| --- | --- | --- | --- |
| `dfx_example_simple` | `dfx2_example_counter` | Application-owned state; one button action; keyboard activation; idle drawing | A keyed button |
| `dfx_example_composition`, `dfx_example_layout`, `dfx_example_custom_component` | `dfx2_example_composition` | Ordinary Go view helpers, rows/columns, stable identity, resizing and repeated controls | Reuse the button and existing layout/scopes |
| `dfx_example_mixer` | `dfx2_example_faders` | Attractive faders, wheel editing, normalized/integer/dB values and a nonlinear mapping | Fader appearance and native wheel input |
| `dfx_example_vumeter` | `dfx2_example_meters` | Mono/stereo/multichannel composition, changing levels, peak decay and pause/resume | Start with the existing meter and scheduler |

The other examples remain references, not a backlog of mandatory ports. Node canvas, docking/workspaces, file trees, editable text, full theme galleries, menus, undo and configuration each introduce separate capabilities. Likewise, the old composition example's text field, color picker and combo box are not prerequisites to demonstrate composition.

## Counter and composition

Proposed public addition:

```go
func (f *Frame) Button(key, label string) bool
```

It reports one activation to the view, not a retained application value. A left press followed by release inside activates; releasing outside or losing capture/focus cancels the gesture. Tab and Shift+Tab traverse it with the existing controls. Focused Space press/release activates once, without repeating on held key-down messages. During continuous window focus, track the Space hold independently of whether a control had focus when it began. Tab or Shift+Tab cancels an armed Space gesture; further Space key-down repeats from that hold cannot arm the newly focused button, including when the hold began with no focused control. Space must be released and freshly pressed before that button can activate. Deliver button activations in queued input order, at most one button activation per window evaluation, including when successive activations target different buttons. Leave subsequent activations in the existing bounded input queue for later evaluations; neither view-call order nor the boolean return may reverse or collapse them. Enter/default-button semantics and application-wide shortcuts are outside this first addition. This deliberately replaces the dfx simple example's global Space action with focused activation.

The counter should be readable in one short main file: open a window, handle the button activation and increment the count, then record the count label, and close normally. This order shows the updated count in the same evaluation, before the window goes idle. No worker, polling loop, extra redraw or periodic invalidation is needed.

The composition example uses a few named counter panels and a sidebar, composed by ordinary functions receiving explicit application state. Each repeated panel uses `Scope` with a stable model ID. Buttons select, reorder, remove and restore panels; labels and checkboxes supply the remaining content. Keep selection and counts in the application even when a panel is absent; interaction state remains the framework's. Resize demonstrates measured widths, flexible columns, wrapping and clipping, not ImGui cursor placement or automatic responsive breakpoints.

## Faders

Use dfx's current slot, fill, handle and position marker as the visual reference, and compare the two applications by eye. Improve the default dfx2 control rather than introducing a theme system for one gallery. Preserve visible focus and the existing drag/keyboard/value-ownership behavior.

Wheel behavior is a proposal for review before stage 2: the hovered fader receives vertical wheel input; scrolling up increases its normalized visual position. One discrete notch moves 0.01, Ctrl multiplies by ten and Alt divides by ten; if both are held their factors cancel. Fractional native deltas contribute proportionally. Clamp to the endpoints, leave focus unchanged, and ignore wheel input during a captured drag. Horizontal scrolling does not edit a fader. The same normalized movement applies before an example's range conversion, matching the old gallery's visual-space interaction. Both native backends must supply deltas and pointer coordinates in the existing input path; wheel input must not be disguised as coalescible pointer motion.

Show a small bank: normalized 0–1, integer 0–32767, dB −60 to +12 with linear travel in dB, and linear amplitude with a documented nonlinear visual mapping. Keep conversions, rounding, formatting and mapping helpers in the example initially. Application values remain in their own units; only the call to `Frame.Fader` uses normalized position. Do not add `FaderI`/`FaderF`, a public taper interface or device-specific gain logic merely to reproduce dfx's API.

Labels show each range, current value and mapping. Exact track-aligned tick scales, reset gestures, configurable stops and a public sizing/style options type are not required for the first gallery. If visual comparison shows one is essential, propose that specific addition before extending the API. This stage addresses the existing [appearance](roadmap/improve-fader-appearance.md) and [wheel](roadmap/support-mouse-wheel-fader-edits.md) cards without changing their priority file.

## Meters

Compose the existing horizontal meter into mono, stereo and an eight-channel bank with labels and synthetic levels. The first port demonstrates the behavior of the dfx gallery without reproducing its vertical orientation, segmented skins or waterfall history. No new meter API is required by this draft.

The example owns a modest, cancelable synthetic feed; publish snapshots safely and invalidate on updates. Use monotonic elapsed time for generated signals. Pause stops feed updates and their wakeups. A retained peak may finish its existing decay, then drawing becomes idle. Resume continues without replaying missed ticks. Include a controllable level drop so peak behavior can be seen, rather than requiring a new production audio-ballistics system. Closing the example stops its feed through normal ownership.

## Scope and completion

Put each program under `examples/` using the names above, so existing `make` installs discoverable `dfx2_example_*` commands into the Go bin directory. Keep the existing `mixer`, `lifetime` and `webview` commands. Each example has a short README naming its dfx reference, intentional differences and hands-on steps.

Finish each stage on Linux and Windows, with Michael's visual/input audition where it matters. Portable tests and cross-builds are not native Windows evidence. Acceptance is a usable example and a coherent small API, supported by proportionate checks; it does not require resolving every previously deferred framework finding.

After the gallery, return to a parallel, read-only Sessionmixer frontend. Reassess concrete API gaps using the examples, then choose the real device data path. Do not broaden this arc into hardware integration or migrate the existing application now.
