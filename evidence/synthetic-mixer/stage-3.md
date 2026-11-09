# Synthetic mixer stage 3: layout and interactive controls

Stage 3 of the [work order](../../docs/future/synthetic-mixer-work-order.md) is implemented in the working tree on top of `631d339`; nothing is committed. Built behavior is in [ui.md](../../docs/current/ui.md). **Windows execution happened on 2026-11-09** at `35dc8c4`; the [handoff](windows/stage-3/README.md) ran, and its results, including two timing-sensitive unit tests found on that host, are in [windows/stage-3/results.md](windows/stage-3/results.md) and await the Linux return checks and review. Review status is recorded at the end. Stage 4 has not started.

Host: Linux 7.0.0-42-generic amd64, Go 1.27.1 (module language 1.26), GTK 3.24.52, Cairo 1.18.4. Every build, test and vet uses `CGO_ENABLED=0`.

## What was added

- **`ui`:** `Row`, `Column`, `Scope`, `Fader`, `Checkbox` and `Layout`. Each node is solved once per frame. Interaction state lives in `ui/input.go`.
- **Core:** a bounded per-window input queue under the runtime's mutex, with `FrameRequest.Input`, `InputLost`, `FrameResult.Consumed` and `ReleaseCapture`, and one overflow diagnostic per occurrence from the supervisor.
- **Platform:** `InputReceived` events with normalized values, and `Backend.ReleaseCapture`.
- **GTK:** one focusable drawing area, input masks, permanent callbacks and GDK event accessors.
- **Win32:** `SetCapture`, `ReleaseCapture`, `WM_CAPTURECHANGED`, focus messages and signed logical coordinates.
- **Example:** `-controls`, with two windows of channel strips over one small model, and console commands for external changes. This stands in for stage 4's model and worker: edits apply at once.

## Decisions the work order left open

Each is deliberate and visible in `ui.md`:

- **Bases for edits.** A keyboard step or toggle starts from the value last returned, not the snapshot supplied. This is what makes stale snapshots safe. The pending-value policy across delayed acknowledgments stays in the application.
- **Flex basis.** A flex child's base is its minimum, so equal weights give equal strips. A parent with no bound on that axis gives it the intrinsic size instead. Flex beats the child's own main-axis size.
- **Meter width.** A meter measures as wide as the room offered, and 120 logical units with no bound. This keeps stage 1's root-column placement without implicit stretching: the width is the widget's own measure, and a row, which offers no bound, gets the fixed intrinsic width.
- **Fader press.** A press maps absolutely, so pressing off-center is an edit. A grab offset would need another rule, and the order specifies only the vertical mapping.
- **Home and End.** Home selects 0 and End selects 1, matching the ARIA slider pattern. "Endpoints" left this open.
- **Tab during a drag.** It cancels the gesture and moves focus. Keys otherwise do nothing during a drag.
- **Native release after cancellation.** The core skips `ReleaseCapture` if a left press is still queued: that press took its own capture after the gesture being canceled, and releasing would break it. The UI clears the request itself when it consumes a press in the same frame.
- **GTK's `ReleaseCapture` is a no-op.** GTK's implicit grab has no revocation call. A canceled gesture ignores the remaining events until the button-up edge. So "cancel native capture for removed controls" is real on Win32, and on GTK is the core ignoring those events.

## Portable checks

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=180s
CGO_ENABLED=0 go test ./internal/appcore ./ui ./internal/platform/win32 ./internal/platform/gtk -count=100 -timeout=500s
CGO_ENABLED=0 go test ./internal/text/... -count=20
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -v ./internal/platform/gtk -run Headers -count=1
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./examples/mixer
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
CGO_ENABLED=0 go test . -run TestDependencyBoundaries -count=1
GOOS=windows GOARCH=amd64 go vet .        # in windows/stage-3/controlsdrive
GOOS=windows GOARCH=amd64 go build -o /dev/null .
```

All 13 exit 0 at the current binary hash, `aff24bf6…` (see the native reruns below). `TestBindingsMatchHeaders` reports PASS, not SKIP, including the new GDK event accessors. The race detector needs cgo and was not run. Repetition does not make a fixture deterministic, and does not show that one is; see the core-path fixtures below. Windows and Darwin results are compilation and static analysis only.

The Win32 tests still run an injected model on Linux. That model encodes assumptions about `SetCapture` and `WM_CAPTURECHANGED` that the Windows run has to confirm.

| Contract | Tests |
| --- | --- |
| Root column keeps stage 1's placement; flex shares remaining space by weight without losing pixels; bounds freeze and redistribute; a container with a flex child fills its offer; no cross-axis stretching; minima hold; containers clip; clipped parts take no press; intrinsic bounds; huge sizes stay in range; scale doubles geometry | `ui/layout_test.go` |
| A label wraps to its assigned width without reshaping or another evaluation, and takes its natural width in a row | `TestLabelWrapsToItsAssignedWidthWithoutReshapingOrEvaluating`, `TestLabelInARowTakesItsNaturalWidth` |
| Sibling duplicates fail before a frame is published, across kinds and through rows and columns; the same key in two scopes is two identities, and a key cannot pass for a nested path | `TestDuplicateSiblingKeysFailBeforeAFrameIsPublished` |
| Stable identity across reorder for capture, focus and later keys; events route to the layout the user saw | `TestStableIdentityRoutesInputAcrossReorder`, `TestEventsRouteToTheLastCompletedLayoutNotALaterIndex` |
| Removal cancels capture, clears focus and yields no edit; the next Tab starts at the first control; reinsertion is fresh; a kind change at a key is fresh | `TestRemovalCancelsCaptureClearsFocusAndYieldsNoEdit`, `TestAnEditWhoseTargetIsRemovedThatFrameIsNeverDelivered`, `TestChangingAKeysKindStartsFresh` |
| Two activations before a frame are two evaluations and two edits, by mouse and by Space, also over a stale snapshot; a queued drag and release dispatch each edit once | `TestTwoActivationsBeforeAFrameAreTwoEdits`, `TestActivationsOnStaleSnapshotsStillToggleFromWhatWasShown`, `TestQueuedDragAndReleaseEachBecomeOneEditPerEvaluation`, `TestInputThroughTheCoreDispatchesEachEditExactlyOnce`; the same events with a frame between each give one evaluation per event and an intermediate drag edit, `TestInputSplitAcrossTurnsEditsPerTurn` |
| External updates during and after a drag never echo; the gesture owns its display, and frames follow the model after release; the edited value renders over a stale snapshot | `TestExternalUpdatesDuringADragNeverEcho`, `TestKeyboardEditsDoNotReuseAStaleSnapshotAsTheirBase` |
| Capture loss, focus loss, Tab and overflow cancel with no final edit and no stuck state; a newer press keeps its capture | `TestCaptureAndFocusLossCancelWithoutManufacturingAnEdit`, `TestTabDuringADragCancelsItAndFocusMoves`, `TestInputLostCancelsTheGestureAndReleasesCapture`, `TestANewPressKeepsTheCaptureAnEarlierCancelWouldRelease`, `TestAnEmptyPressAlsoKeepsItsCapture` |
| Fader mapping, clamping, steps, grid; checkbox release inside, Space on release; left button only; keys ignored during a drag; focus traversal and wrap; scale mapping | remaining `ui/input_test.go` |
| Input is queued, coalesced only between adjacent motions, never into an event lent to a frame; the suffix waits for later frames; a frame consuming nothing fails; overflow clears the queue, requests a frame, reports once per window; overflow during a frame cannot eat later events; closed windows drop input; capture release timing and failure | `internal/appcore/input_test.go`, `ui/core_test.go` |
| GTK: copied logical events, unused keys and double-click extras left alone, sealed on close, focus grab on press, input prepared before showing, grab and focus edges | `internal/platform/gtk/input_linux_amd64_test.go` |
| Win32, injected: logical signed coordinates, capture on left press, own release is no loss, native takeover reports once, another window's press, focus loss releases at once, failed release reaches `Pump`, keys, buttons, ordinary and closing windows silent, a loop's capture is safe from `ReleaseCapture`, capture calls count as native operations | `internal/platform/win32/input_amd64_test.go` |
| Header ABI of the new GTK bindings, SDK arity of the new Win32 calls | `TestBindingsMatchHeaders`, `TestSyscallArgumentCounts` |

**Core-path fixtures.** `ui/core_test.go` joins real content to the real core through the fake backend. Its exact edit and evaluation assertions hold only because the scheduling is controlled. Each input batch arrives through `fake.Emit` inside one posted callback, so the whole batch is queued before that turn's frames run. Idle is decided by an observer that is armed on the UI thread and checked by the fake's pump hook between dispatch turns. There, no frame can be in flight, and any window still owed a frame, including one for the unconsumed suffix, has already requested another wake. No pending wake, fake event, posted callback or animation therefore means no further frame. Seeing the barrier callback run would not be enough. `waitIdle` arms the observer in its own callback.

`TestWaitIdleWaitsForTheFrameThatConsumesTheRemainder` controls the order with channels alone:
1. It arms the observer in the same callback as the batch, before the batch's first frame.
2. It holds the suffix's frame mid-draw. While that frame is held, the UI thread cannot reach a pump boundary, so the observer must not have reported.
3. Only then does it release the frame. The frame must have finished once the observer reports.

A cleanup releases the held frame, so a failed assertion cannot leave the UI thread blocked. Timeouts are hang guards only.

The first version of the main integration test, which Terminus `ba06f23e6163` flagged, queued each event with a separate `Enqueue` while the pump ran. A frame could run between any two events, and correct behavior would then have failed the test. It passed 100 repetitions only because that schedule was rare. `TestInputSplitAcrossTurnsEditsPerTurn` forces that schedule and records what correct code does under it. The first version of the held-frame test, which Terminus `9ecf6644dbdd` flagged, released the frame after a 20 ms sleep, so whether it caught a premature return depended on timing.

Helper mutations, 200 runs each of the four core-path tests. These are observations, not guarantees, except where the ordering above guarantees the outcome.

| Mutation | Held-frame test | Other tests |
| --- | --- | --- |
| Observer reports when armed: the callback-only check, through the path `waitIdle` uses | Failed 200 of 200 | Main integration test failed 13 times, overflow test once |
| Pump hook ignores pending work | Failed 200 of 200 | Main integration test failed once |
| Old one-event-at-a-time `Enqueue` delivery | Never failed | Main integration test never failed; overflow test failed 10 times, because frames drained the burst before it could overflow |

For the two observer mutations, the controlled ordering guarantees that the observer has reported before the held frame begins, so the held-frame test cannot pass. Their failures in the other tests depend on timing.

In the first batch of the callback-only mutation, one of the 200 held-frame failures was the 5 s guard: the suffix's frame never started. In 2,800 further runs of that test it did not recur, and every failure was the intended one. The cause is not identified. It occurred only with a broken helper, and it ended as a failure, not a hang.

With the helpers intact, the full validation above passes, including 100 repetitions.

**Fault injection.** 98 faults applied through `go test -overlay`, with the tree untouched, cover the UI state machine, layout, core queue, GTK handlers and Win32 handlers. The first pass found 11 surviving faults in real gaps, so I strengthened the tests: a press on empty space clearing the release request, a scale-free hit test, keys during a drag, non-left buttons, keyboard grid drift, a checkbox owning its value, intrinsic minima, a label wider than its column, overflow with an otherwise empty queue, repeated overflow reports, and closing windows delivering input. All 93 of the original faults are now caught, and still were after each core-path fixture fix. The flex fix below added five; 96 of the 98 are caught, and the two survivors are explained there. The fault script is a scratch tool and is not part of the tree; this count is a report, not a re-runnable artifact.

**Flex arithmetic.** Terminus `5fc97cd2e8ac` found that two siblings with `Flex: 1e308` passed validation and then hung the shared UI thread: the weight total overflowed, every share became NaN, and NaN never froze or settled. A lone `Flex: 1e308` child did not hang, but its infinite share froze at the unbounded length instead of the remaining space. `distribute` now scales the weights still sharing by their largest on each pass, so any finite weights give finite, proportional shares. That scaling is recomputed after a heavy child freezes, so the others keep their ratio. If a pass still yields a non-finite violation, or freezes nothing while the violation is non-zero, the frame fails with an error. The regressions run each frame under a 5 s hang guard:

- two `1e308` siblings split 150/150 and finish;
- a lone `1e308` lays out exactly like `Flex: 1`;
- `1e308` beside `1` takes at least 299 of 300;
- `1e308` capped at 40 leaves `1e-20` and `2e-20` at 87 and 173.

Without the per-pass recomputation, those small weights scale to zero against `1e308`.

**The guards are defensive.** No validated layout reaches them, and fault injection shows exactly that:

- Removing the scaling, or computing it once instead of per pass, is caught by the regressions.
- Removing the non-finite check alone survives, because the progress check then fails the same arithmetic.
- Removing the progress check alone survives. With a finite violation, a positive violation means some share is below its minimum and a negative one means some share is above its maximum, so some child always freezes and the branch cannot be reached.
- Removing both is caught only by `TestNonFiniteFlexArithmeticFailsInsteadOfLooping`, which calls `distribute` directly with NaN and infinite weights that validation rejects, through its 5 s hang guard.

That test checks the backstop. It does not show that the public API can reach either check.

A separate bug appeared during the first native X11 attempt: `examples/mixer` panicked printing its final status when `Run` failed before setup, with no display. That is fixed in the example; it predates this stage.

## Native Linux auditions

Both drivers ran inside an **isolated headless GNOME Shell 50.1**, with its own home, runtime directory, session bus and a 2560×1400 virtual monitor. Nothing reached the operator's desktop. Input is real to the toolkit: Clutter virtual devices inject into the compositor, which delivers it over Wayland or through XWayland. `controls_drive.py` needs unsafe mode only to read window geometry and create those devices.

```sh
CGO_ENABLED=0 go build -o mixer ./examples/mixer
python3 evidence/synthetic-mixer/linux/controls_drive.py mixer --out OUT --base SHORT_DIR --gdk-backend wayland
python3 evidence/synthetic-mixer/linux/controls_drive.py mixer --out OUT --base SHORT_DIR --gdk-backend x11
python3 evidence/synthetic-mixer/linux/controls_drive.py mixer --out OUT --base SHORT_DIR --gdk-backend x11 --gdk-scale 2
python3 evidence/synthetic-mixer/linux/controls_drive.py mixer --out NEST --base SHORT_DIR --exec python3 "$PWD/evidence/synthetic-mixer/linux/drive.py" mixer --out OUT --gdk-backend wayland
```

The last runs stage 1's matrix in the same session, for `wayland`, `x11` and `x11` at scale 2. Results are in [`linux/stage-3/`](linux/stage-3/), with `mixer.sha256` holding the tested binary's hash `8da27f9ede822a6a058eececc8d863c74bb5899e0b1db8d635ebee910e6fda5a`, on 2026-11-06.

| Case | Wayland | XWayland | XWayland, `GDK_SCALE=2` |
| --- | --- | --- | --- |
| `drag_outside_window`: tracking above, beyond and below the window, release outside, no duplicate | pass | pass | pass |
| `checkbox`: release inside toggles, release outside does not | pass | pass | pass |
| `keyboard`: exact fader sequence, Tab, Shift+Tab, Space on release, unused key | pass | pass | pass |
| `independent_focus`: keys alternate B/c, A/a, B/c | pass | pass | pass |
| `external_during_drag`: no echo, gesture keeps value | pass | pass | pass |
| `reorder_during_drag`: edits stay on a | pass | pass | pass |
| `remove_during_drag`: no final edit, reinsertion fresh | pass | pass | pass |
| `focus_loss_during_drag`: gesture ends, fresh press works | pass | pass | pass |
| `hover_is_idle`: 0 frames from hover and a letter; 3 s of 0 frames, timers, presents and evaluations | pass | pass | pass |
| `close_during_drag`: the other window keeps responding | pass | pass | pass |
| Exit, `Run` result, final ownership | 0, nil, all zero | 0, nil, all zero | 0, nil, all zero |
| First surface | 460×300@1 | 460×300@1 | 920×600@2 |

Each run's `controls-focused.png` is the retained image after the keyboard case. I looked at the Wayland and scale-2 images: three strips, crisp text, focus ring clear of the box.

**Rerun after the flex fix.** The fix changed the binary, so both matrices ran again on 2026-11-08 into [`linux/stage-3/flex-rerun/`](linux/stage-3/flex-rerun/), with `mixer.sha256` holding `aff24bf6b17bf5e26ed178b40ef301044561ee0a2b8c095a1803465ad92ce819`. The runs above are kept as they were.

- **Controls:** 10/10 in each configuration. Each run exited 0, `Run` returned nil, there were no ownership problems, and there were 35 edits.
- **Stage 1 regression:** 10/10 in each configuration. Wayland minimize shows the same accepted exception, 62 frames and 31 timers. XWayland reports the window hidden, with zero.
- **Image:** I looked at the scale-2 `controls-focused.png`, and it matches the earlier runs.

The stage 1 matrix passed with no failures in all three configurations: 10 of 10 cases each, with 5 s quiet intervals of 0 frames, timers, wakes, dispatches, presents and paints. The `minimize` case still shows the accepted GTK/Wayland exception: 62 frames and 31 timers while minimized, against 0 and 0 under XWayland.

## Unavailable coverage

Stated plainly, because a green run does not cover these:

- **Windows:** nothing here has executed. `controlsdrive`, `controls.ps1` and the input path are cross-built only.
- **The operator's live desktop:** these runs are in a nested headless shell on a virtual monitor. No person has dragged a fader or used the keyboard, and there is no hardware input device in the path. A hand audition on Michael's Linux desktop has not happened; steps 1 to 8 of the [Windows manual audition](windows/stage-3/README.md#manual-audition) apply equally there.
- **Native capture loss on GTK:** `grab-broken-event` and `grab-notify` are connected and tested with injected calls. Nothing in these runs provokes one natively; `focus_loss_during_drag` exercises focus-out, through shell window activation.
- **A true X11 server** and **fractional or native Wayland scale:** X11 here is XWayland, and scale 2 is `GDK_SCALE`.
- **Overflow natively:** 257 events between frames is not reachable through this driver. The queue, UI and core handling are tested deterministically, through real core and content with a fake backend.
- **`ReleaseCapture` on Win32 during a real size/move loop:** modeled only.
- **Accessibility:** roles, names and values are in the internal record; nothing consumes them, by design.

Earlier open items remain open: the historical appcore `FAIL`, the earlier unexplained Linux failures, stage 2's timer-fallback limits and native Windows DPI.

## Canon drift to flag

Not edited, per the work order. Stage 3 makes these stale for deliberate canon work:

- `terminus-canon/projects/dfx2/rubric.yaml`: "measured layout and interactive controls are later mixer stages".
- `immediate-mode-state-and-frames.md`, boundary: "Labels and meters in a single root column ... do not require the later measured-layout or interactive-control APIs".
- New invariants that no quality states yet: input bounded per window with overflow cancelling rather than blocking; at most one reported edit per widget per evaluation, with an unconsumed suffix waiting for a later frame; hover and unused keys requesting no frames. The existing qualities cover state ownership, identity and no replay.

## Review status

- **Terminus `ba06f23e6163`** (full rubric, 195 files, none uncovered): not clean. Its one blocking finding was that the main core integration test relied on uncontrolled scheduling. Michael accepted the fix: batched delivery and a pump-boundary idle check.
- **Terminus `9ecf6644dbdd`** (full rubric, 196 files, none uncovered): not clean. Its one blocking finding was that the held-frame test released the frame after a timed sleep. Michael accepted the fix: the observer is armed with the batch, checked while the frame is held, and the frame is released in cleanup on failure. That fix touched only tests and the fake, and left the mixer hash at `8da27f9e…`.
- **Terminus `5fc97cd2e8ac`** (full rubric, 196 files, none uncovered): not clean. Its one blocking finding was that huge finite flex weights hang layout. Michael accepted the fix: per-pass weight scaling and a guard that fails the frame visibly. It changed the binary to `aff24bf6…`.
- After the flex fix, all 13 portable checks exit 0, fault injection catches 96 of 98 (the two survivors are explained above), and both native Linux matrices pass in all three configurations.
- **Terminus `2e1844d373da`** (full rubric, 346 files, none uncovered): **clean**, superseding the three rounds above for the Linux-side tree. That is not acceptance: the Windows execution, the Linux return checks, the Linux manual audition and the final review remain.
