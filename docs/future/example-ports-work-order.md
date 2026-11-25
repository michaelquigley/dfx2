# Representative example ports: work order

Draft for review with [the design](example-ports.md). No implementation has started. The stages below are sequential; finish and review one before starting the next. Stage 2's wheel policy is proposed, not an already accepted public contract.

## Orientation

Read `AGENTS.md`, recent journal entries and [current UI behavior](../current/ui.md). The root framework stays cgo-free. The accepted foundation, synthetic-mixer and webview arcs remain accepted with their recorded limits. This work does not reopen those reviews or change their raw evidence.

Read the selected dfx example source for behavior and presentation, not for architecture. Do not introduce a `Component`/`State` hierarchy, an action registry, an ImGui dependency or a framework-owned application model. Keep changes in the UI and input paths that the examples actually require. Platform packages must continue to know nothing about UI widgets.

## Stage 1: counter and composition

Add `Frame.Button(key, label string) bool` and the two examples. Use the existing control machinery rather than a second event queue:

- `ui/frame.go`: button role, keyed recording and activation result.
- `ui/input.go`: hit testing, capture, focused Space activation, traversal and ordinary bounded deferral. Button activation is an event, not equality between two boolean application values. Admit at most one button activation per window evaluation, across all buttons, and defer later activations in the existing input queue so the application observes their input order.
- `ui/layout.go` and `ui/content.go`: label-based measurement, rendering, focus and pressed appearance. Reuse the existing text and canvas operations; add a drawing primitive only if the button actually needs it.
- `examples/dfx2_example_counter` and `examples/dfx2_example_composition`: public API consumers only. The counter has no shared example harness; composition helpers are ordinary local Go functions.

Build the counter first to check the API in use, then the composition example. In the counter draw function, handle the button and update the count before recording its label; do not add a redraw to compensate for recording stale text. Repeated panels have stable IDs independent of their order or label; scope the keys inside each helper. Include a reorder and a hide/restore action so the intended ownership is observable. Preserve counts in application state, without promising focus restoration after a removed control returns.

Add focused UI tests for pointer activation/cancellation, focused keyboard activation, two activations queued before evaluation, and scoped identity during reorder/removal. Include holding Space on one button, traversing with Tab or Shift+Tab, receiving a repeated Space key-down and releasing Space: neither button activates. Run the same sequence starting with no focused control: the initial hold and its repeats still cannot activate the button reached by traversal. Track the Space hold independently of the armed control during continuous window focus. In both starting conditions, a fresh Space press/release on the new focus activates once. Include two distinct buttons called by the view in the reverse of their queued activation order: Select B followed by Remove Selected must select and then remove B across evaluations. Use mixed input in one existing two-activation case: a pointer click followed by a focused Space press/release, queued before evaluation, must produce one activation in each of two successive evaluations, in input order. This checks that pointer and keyboard activation share the same per-window limit. Combine queued activation and scoped reorder in one case: establish the original A/B layout and queue two clicks at A’s button, then set the fixture’s model order to B/A before the first evaluation. Input is processed against the original layout, while that evaluation records the reversed layout. Before retrying the deferred second release, assert that the completed layout puts B at the original click location. Then verify that the second activation reaches A exactly once and never B. Follow the existing deferred-release fixture; do not rely on the first button’s return value to reorder widgets already recorded in that evaluation. Preserve the already resolved target of the deferred release rather than hit-testing it against the new layout. Reuse existing test barriers. Do not build a new automation framework for these examples. Check measurement and clipping with a long label and a narrow window using the existing facilities.

Update `docs/current/ui.md` and package documentation for the built button, add example READMEs, and extend `make clean` for the new installed commands. `make` already uses `go install ./...`; no new default build machinery is needed.

Stop for review of the programming feel and appearance. This stage does not include wheel input, range abstractions, Enter activation or application shortcuts.

## Stage 2: fader gallery and wheel input

Before coding, confirm the wheel policy in the design with Michael; record any adjustment there. Use the existing roadmap cards as context, not as authority for a larger widget rewrite.

Implement vertical wheel delivery through `internal/platform/platform.go`, the GTK input bindings and Win32 input bindings, then route it in `ui/input.go`. Follow the existing logical-coordinate and modifier conventions. Verify native discrete/fractional delta conversion in the backend tests. Preserve event order through the core's bounded input path; inspect its coalescing rules rather than assuming a new event automatically fits them. Do not change global queue policy to solve unrelated findings.

Improve the fader in the existing measurement/rendering code and add `examples/dfx2_example_faders`. Use small local conversion helpers for ranges and nonlinear position; focused tests should cover their ordinary endpoints, round trips and integer rounding. The renderer need not know any device units. Keep the existing mixer usable with the new default appearance.

Check wheel targeting, direction, modifiers, clamping and drag interaction, plus the existing drag and keyboard tests. On both native hosts, audition wheel input on adjacent faders and compare appearance with dfx at ordinary and scaled display settings. Record fractional-device behavior only where actually exercised. A mouse wheel pass is not evidence for every touchpad.

Stop for the visual/input decision before adding optional styling, ticks or other new public API.

## Stage 3: meter gallery

Add `examples/dfx2_example_meters` with mono, stereo and eight-channel groups using existing `Frame.Meter`, rows, columns, labels, buttons and checkboxes. Provide pause/resume and a level-drop action. Keep synthetic feed state and cancellation in the application; no framework timer or animation API is needed.

Use an owned task or the existing application posting pattern with a safely published snapshot. A paused feed must wait for resume/cancellation rather than wake on an unused ticker. The framework's existing peak decay may still request frames until settled. Resume computes from current elapsed time rather than emitting a backlog. Avoid new meter physics, orientation/style options and a general signal-generator package.

A focused feed test covers pause/resume and close. Existing UI meter tests cover retained peak state; add one only if the example exposes a real missing behavior. Hands-on checks show distinct channels, peak decay after a drop, a settled paused display and clean close. Document that these are synthetic levels and current normalized peak behavior, not Sessionmixer's production meter ballistics.

## Validation and handoff for each stage

Run the no-cgo suite and vet, and cross-build the Windows examples. Use targeted race checks when changing shared feed state. Run the affected examples natively on Linux, then hand the committed candidate to the Windows implementer for native validation. Keep a compact record in `evidence/example-ports/stage-N.md` separating modeled checks, native behavior, visual feedback and untested cases. Do not manufacture a large matrix of equivalent cases.

The focused audition covers input, resize/wrapping, focus, idle after activity settles and close. Stage 2 also needs a mixer smoke check because its default fader changes. Existing unchanged lifecycle/browser evidence remains applicable to its recorded candidate; these examples do not justify rerunning every accepted matrix. If a necessary change reaches shared lifecycle or presentation code, identify the affected behavior and choose the relevant regression then.

Use Terminus for the stage review with the project's calibrated canon. Present material findings for disposition. Keep accepted deferrals deferred unless new evidence changes their relevance; do not chase a clean verdict through unrelated hardening. Focused tests of ordinary behavior are the default, not large repetition or mutation campaigns. Fixes that alter the auditioned behavior need an affected check, not automatic repetition of every native run.

Michael commits and accepts stages. Leave code and documentation uncommitted for review; do not push. Update current docs only for built behavior and put only non-obvious decisions or live handoff state in the journal. Align canon deliberately if the new controls add a contract its qualities need to express; this plan does not authorize canon edits. Once the arc is accepted, retire these fulfilled intent documents after preserving any genuinely remaining work.
