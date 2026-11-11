# Synthetic mixer stage 3: layout and interactive controls

Stage 3 of the [work order](https://github.com/michaelquigley/dfx2/blob/d6b94f933c9b549c759ee3ae30e4644bb24945e8/docs/future/synthetic-mixer-work-order.md) is built. The corrected candidate was committed at `c8f880a`, and its Windows return landed at `f2cb303`. Built behavior is in [ui.md](../../docs/current/ui.md). The [second Windows return](windows/stage-3/return-results.md) closes the test-gate gap: appcore and Win32 passed 300 repetitions on Windows, and the original audition passed all 17 mixer and 9 foundation native cases with every gate enabled. The earlier 10/10 controls run and Windows manual audition remain applicable to the unchanged Windows runtime. Michael has also confirmed that the Linux controls manual audition looked fine. Cairo and PNG-validation fixes passed full review `aeda6d4e169a`; final return review `ce97d67355f5` found a paragraph-cache key accounting gap. Michael accepted its correction, now implemented in shared text code; this new candidate passed Terminus `4b18b3c1df9f` with one documentation advisory (since corrected), and its [Windows validation](windows/stage-3/paragraph-key-results.md) has returned at `a2a2109`. Final return review `7f15050aeece` and review `8d23bf53f115` against the aligned canon are clean, with no findings. The earlier Windows and manual results describe their tested revisions. Stage 3 awaits Michael's acceptance, and stage 4 has not started.

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

Before transfer, all 13 exited 0 at the tested binary hash, `aff24bf6…` (see the native reruns below). `TestBindingsMatchHeaders` reports PASS, not SKIP, including the new GDK event accessors. The race detector needs cgo and was not run. Repetition does not make a fixture deterministic, and does not show that one is; see the core-path fixtures below. The Windows and Darwin commands in this list are compilation and static analysis only; subsequent native Windows execution is recorded separately below.

The Win32 unit tests use an injected model on both operating systems. Native Windows controls now exercise capture, signed coordinates, keyboard delivery and focus loss; capture release inside a real size/move loop remains modeled only.

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

The Linux regression driver and its tests require Python 3 with Pillow (`python3-pil` on Debian/Ubuntu). PNG acceptance checks chunk integrity, decodes all pixels and requires the complete end chunk; dimension metadata alone is insufficient.

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

## Windows return and Linux follow-up

The Windows return at `9b700d2` changes evidence and documentation only. Its controls matrix passed 10/10. The 17 mixer and 9 foundation native regression cases passed through scratch runners with their failing unit-test gates omitted; the original runner failures and logs remain unchanged. Michael's Windows manual report was "everything that was testable seems fine", with manual close-during-drag and DPI unavailable. See the [complete Windows record](windows/stage-3/results.md).

Michael accepted two test fixes on Linux:

- **Input delivery:** `queueInput` now emits its whole batch on the UI thread in the same posted callback that arms its completed-dispatch barrier. A regression parks the pump, places a wake ahead of any individually queued input, and holds the pump after the barrier reports completion. Restoring the old helper through an overlay failed the intended assertion 100/100 times: all three inputs were still undelivered. The fixed helper and overflow tests passed 100 repetitions.
- **Cleanup retry baseline:** `holdPending` waits for all expected failed allocations to be recorded and all expected windows to be retired. A replacement failure alone is insufficient. `CoreHost.Await` observes parking but does not hold the UI thread there. The regression holds teardown's first free, checks that the shared readiness predicate rejects that incomplete state, then releases it and runs the exact retry and error-retention assertions. Restoring the old readiness predicate through an overlay failed that assertion 100/100 times, without timeouts. The acknowledgment and new regression tests passed 100 repetitions.

On the resulting tree, all with `CGO_ENABLED=0`:

- `go test ./... -count=1 -timeout=180s` and Linux `go vet ./...` pass.
- `go test ./internal/appcore ./internal/platform/win32 -count=300 -timeout=180s` passes.
- Windows `go vet ./...` passes. The appcore and Win32 test binaries compile for Windows/amd64 and Darwin/amd64; this is not execution on those systems.
- The separate Windows controls driver builds and vets; the Linux driver regression suite passes 21/21.
- Inspection of the 27 returned native JSONL logs finds exactly one drained record per run, zero outstanding ownership and paint failures, and empty stderr files.

At this point only `_test.go` files and documentation had changed after the Windows return, so the retained native evidence still applied. The subsequent Cairo fix changes the Linux backend and requires fresh Linux runs, recorded below; Windows runtime sources remain unchanged. At that point the Windows runners, Linux manual audition and final return review were pending; the second Windows return below records their later status. Canon alignment remains separate. The historical failures listed below are not explained or closed by these test fixes.

## Cairo paint-error fix after return review

Terminus `de2919d40d6a` found that GTK draw callbacks ignored Cairo context errors. Michael accepted checking `cairo_status` after drawing and reporting the error through `Pump`. Failed paints now count separately from successful paints; the pending diagnostic names the window, retained image size and status. `Pump` checks before blocking as well as after native iteration, and dispatch defers application callbacks until the core receives the failure. The deferred dispatch retains the wake already owned by the core, which can coalesce shutdown work into it. The next `Pump` delivers that wake after the error without clearing any newer queued native source. Cleanup still releases the retained image and native references through the ordinary path.

Validation on Linux, all Go commands using `CGO_ENABLED=0`:

- The four backend failure cases (source or paint error, inside or outside native iteration) and two backend/core failure cases fail on assertions with the old backend overlaid. Both core cases previously returned nil after a later ordinary close. The successful-paint control remains green.
- With the fix, the full root suite and vet pass, and the GTK package passes 100 repetitions. `TestBindingsMatchHeaders` passes, including `cairo_status`, without skipping. Windows/amd64 and Darwin/amd64 root cross-builds pass.
- Six targeted mutations are caught on assertions: ignoring context status, dropping the pre-iteration error check, dropping the post-iteration error check, dispatching application callbacks with an error pending, losing the deferred wake, and forgetting a newer native source while delivering that wake. These are additional checks; the earlier scratch-only 98-mutation suite was not rerun. The overlay failure logs are retained under `linux/stage-3/cairo-rerun-final/validation/`; the original-backend run predates the extra deferred-wake assertions, and the six targeted mutation logs use the final tests.
- The backend/core tests use injected native calls and bypass native startup and widget creation. They exercise the real core and GTK presentation, draw, pump, destruction and release paths, checking the returned error, owned task cancellation, content release, window completion and zero native ownership. They do not provoke an actual Cairo failure on a desktop.

The first Cairo build, SHA-256 `e710770c7f5a4017bb0995d84647ae97d3d6f6501c6e730b0bb15ee8230e1ab6`, passed all 60 cases in [`linux/stage-3/cairo-rerun/`](linux/stage-3/cairo-rerun/). Inspection during that validation found the deferred wake needed explicit preservation; those runs therefore do not validate the final code. The corrected Linux mixer SHA-256 is `4b83ab0ac90d9d85c607f6fa769d3ea4ddfae93f9da00934ef44efb5c65ee5d5`. Its controls and regression runs passed **60/60** in [`linux/stage-3/cairo-rerun-final/`](linux/stage-3/cairo-rerun-final/): 10 controls and 10 regression cases each on Wayland, XWayland and XWayland at `GDK_SCALE=2`. All 33 mixer processes drained to zero ownership with empty application stderr and no paint failures. Each controls run reports 35 edits; the scale-2 retained image was inspected and remains consistent with the previous audition. Wayland minimization retains the accepted 62 frames / 31 timers, and settled quiet intervals remain idle. The manifest records commands and exit codes; the earlier runs are untouched. These use the same isolated headless GNOME session and virtual monitor as the previous matrices. They do not themselves establish a manual pass; Michael subsequently confirmed that separately, below. Terminus `bbb7390ad464` reviewed this fix with the new tests and evidence included and returned clean; the separate PNG-validation finding remained unresolved at that point and was corrected in the subsequent section below.

## Unavailable coverage

Stated plainly, because a green run does not cover these:

- **Windows host limits:** actual DPI transitions and manual closing during a drag remain untested on this host. The corrected tests and original regression gates now pass on Windows; the automated close-during-drag case remains native evidence.
- **Manual coverage detail:** Michael confirmed the Linux controls audition "looked fine". This is his overall observation, not an instrumented per-step record, and it establishes no unreported scale or capture-loss scenario. The automated Linux matrices remain nested headless sessions on a virtual monitor.
- **Native capture loss on GTK:** `grab-broken-event` and `grab-notify` are connected and tested with injected calls. Nothing in these runs provokes one natively; `focus_loss_during_drag` exercises focus-out, through shell window activation.
- **A true X11 server** and **fractional or native Wayland scale:** X11 here is XWayland, and scale 2 is `GDK_SCALE`.
- **Overflow natively:** 257 events between frames is not reachable through this driver. The queue, UI and core handling are tested deterministically, through real core and content with a fake backend.
- **`ReleaseCapture` on Win32 during a real size/move loop:** modeled only.
- **Accessibility:** roles, names and values are in the internal record; nothing consumes them, by design.

Earlier open items remain open: the historical appcore `FAIL`, the earlier unexplained Linux failures, stage 2's timer-fallback limits and native Windows DPI.

## Canon drift to flag

The following drift was flagged during implementation and resolved by Michael’s approved canon alignment on 2026-11-10. The canon alignment is committed in `terminus-canon` at `be99161`; its rubric loads successfully in the coverage audit. Full source review `8d23bf53f115` against that canon is clean, with no findings. Original drift:

- `terminus-canon/projects/dfx2/rubric.yaml`: "measured layout and interactive controls are later mixer stages".
- `immediate-mode-state-and-frames.md`, boundary: "Labels and meters in a single root column ... do not require the later measured-layout or interactive-control APIs".
- New invariants that no quality states yet: input bounded per window with overflow cancelling rather than blocking; at most one reported edit per widget per evaluation, with an unconsumed suffix waiting for a later frame; hover and unused keys requesting no frames. The existing qualities cover state ownership, identity and no replay.

## Review status

- **Terminus `ba06f23e6163`** (full rubric, 195 files, none uncovered): not clean. Its one blocking finding was that the main core integration test relied on uncontrolled scheduling. Michael accepted the fix: batched delivery and a pump-boundary idle check.
- **Terminus `9ecf6644dbdd`** (full rubric, 196 files, none uncovered): not clean. Its one blocking finding was that the held-frame test released the frame after a timed sleep. Michael accepted the fix: the observer is armed with the batch, checked while the frame is held, and the frame is released in cleanup on failure. That fix touched only tests and the fake, and left the mixer hash at `8da27f9e…`.
- **Terminus `5fc97cd2e8ac`** (full rubric, 196 files, none uncovered): not clean. Its one blocking finding was that huge finite flex weights hang layout. Michael accepted the fix: per-pass weight scaling and a guard that fails the frame visibly. It changed the binary to `aff24bf6…`.
- After the flex fix, all 13 portable checks exit 0, fault injection catches 96 of 98 (the two survivors are explained above), and both native Linux matrices pass in all three configurations.
- **Terminus `2e1844d373da`** (full rubric, 346 files, none uncovered): **clean**, superseding the three rounds above for the Linux-side tree. That is not acceptance: the Windows execution, the Linux return checks, the Linux manual audition and the final review remain.

- Linux return: both Windows-discovered test defects are fixed and the checks above pass. Full-repository Terminus `de2919d40d6a` is **not clean**: one blocker, GTK paint callbacks do not check Cairo context status, so painting errors cannot reach `Run`. Michael accepted the fix; it is implemented and has not had a new Terminus review. Coverage: 1,347 covered, 212 excluded, and the two existing Hebrew font fixture/license paths uncovered. The review raised no finding on the two test fixes; that is not proof of exhaustive examination.

- **Return review scope correction:** `a3ff5842a6dd` used full scope before the new GTK integration tests and Cairo native evidence were staged; full scope selects tracked paths, so those files were absent from its manifest. It returned not clean with one blocker in the unchanged Linux driver: `png_size` accepts a 24-byte PNG header without decoding an image.
- **Terminus `bbb7390ad464`: clean**, no blocking or advisory findings, after staging the new files (no commit). Its manifest explicitly includes both GTK integration-test files and the final native manifest. Coverage is 1,678 covered, 212 excluded, and the same two Hebrew font fixture/license paths uncovered.
- The earlier PNG finding remains valid despite the later clean result. A temporary five-step probe using only 24-byte PNG headers returned no failures from `large_failures`; the existing passing test fixtures write those same incomplete headers. Michael accepted the driver fix, recorded below. Stage 3 is not accepted.

## PNG validation fix

The Linux driver now uses Pillow to verify PNG chunk integrity, then reopens the same stream and fully decodes its pixels before accepting dimensions. Pillow's verification stops at the IEND header without checking its CRC, so the driver also requires the complete fixed zero-length end chunk. Truncated-image loading is not enabled. Passing fixtures now write real RGB PNGs. Runtime code and the Windows runner are unchanged.

- All **23 driver tests pass** with Pillow 12.1.1. Seven corrupt-file cases cover a header alone, truncated pixel data, missing IEND, incomplete IEND CRC, a bad IDAT CRC, invalid compressed pixels with a valid CRC, and insufficient decoded pixels with a valid CRC. A separate test proves the large audition itself rejects five header-only snapshots.
- Against the old driver, all eight new rejection assertions fail. Three targeted mutations are caught independently: skipping chunk verification accepts the bad IDAT CRC; skipping pixel decoding accepts the two correctly checksummed but undecodable payloads; skipping the end check accepts an incomplete IEND CRC.
- Revalidation of the retained large-run events and images passes **115 snapshots across 23 runs**, including the final Cairo runs and the returned Windows snapshots. The two original stage 2 preflight runs still fail for their missing snapshots (and Wayland's missing growth). The [revalidation record](linux/stage-3/png-validation.json) preserves every result, the driver hash and the relocation method for retained paths. Original native logs and summaries are untouched; this checks retained image data, not a new native execution or a pass of the previously blocked Windows test gates.

Only the Linux validation tooling, its fixtures and documentation changed in this fix. The tested mixer binary remains `4b83ab0a…`, so no native matrix or Go test rerun was needed. Full-repository Terminus `aeda6d4e169a` is **clean**, with no blocking or advisory findings. Its manifest includes the new GTK integration tests and PNG revalidation record: 1,679 files covered, 212 excluded, and the two existing Hebrew font fixture/license paths uncovered. It reviews both runtime and validation fixes, superseding the outstanding findings from this return review arc. Only this review-status note and the journal changed after the verdict. At that review, Windows test-gate reruns and the Linux manual audition were still pending; their later completion is recorded below. Canon alignment remains separate, and stage 3 awaits acceptance.

## Second Windows return and Linux review

The evidence-only return at `f2cb303` validates candidate `c8f880a`; no runner, test, runtime, backend or example source changed between them. The raw records confirm:

- Windows `go test ./internal/appcore ./internal/platform/win32 -count=300 -timeout=300s`: both packages pass, exit 0. These are modeled tests, not native observations.
- Original Windows `audition.ps1`: exit 0 with its full suite, verbose Win32 tests, 50-repetition gate, vet and builds enabled. All **17 mixer cases** pass. The original nested foundation runner passes its own test gate and all **9 native cases**; its intentional failure case exits 1 as specified.
- All **26 native process logs** contain exactly one final drained record with no outstanding ownership or paint failures, and all application stderr files are empty. The five new large PNG snapshots fully decode and match their observed surface sizes through the corrected Linux validator. Original logs and summaries are untouched.
- The earlier native controls matrix and Windows manual audition carry forward because their runtime and example sources are unchanged. DPI and manual close-during-drag remain unavailable as documented on that host.

On Linux, `CGO_ENABLED=0 go test ./... -count=1 -timeout=180s` and `go vet ./...` pass after return. No code changed, so the final 60/60 Linux native matrix remains applicable and was not rerun. The returned Markdown was processed with `unfurl` here.

Michael subsequently clarified the Linux manual result: "actually, i did test that. it looked fine." This closes the pending manual-audition result as an operator-reported pass, without claiming individual scenarios or hardware scale transitions that he did not report.

Final full review `ce97d67355f5` is **not clean**, with one blocker: admitted paragraph map keys retain the original UTF-8 text without charging its bytes, and a substring can retain a larger backing string. Inspection confirms `paragraphBytes` accounts runes, shaping and wrapping but omits that key payload. Michael accepted the fix described below. Coverage: 1,799 covered, 212 excluded, and the same two Hebrew font fixture/license paths uncovered. The Windows return and Linux manual pass remain valid observations of the tested candidate. Canon alignment and acceptance remain separate; stage 4 is untouched. Historical unexplained failures and accepted platform limits remain recorded.


## Paragraph-key ownership and accounting fix

Michael accepted `ce97d67355f5`. Paragraph admission now reserves and owns a copy of the UTF-8 key, so a substring cannot pin the caller's larger backing string. The shaped payload keeps its existing wrap headroom; the key is charged once. Its charge survives wrap recomputation and is removed with eviction or release. A refused paragraph stores no key and charges only its shaped/wrapped payload to scratch. No cache or scratch limit was raised.

Four new regressions cover substring backing ownership and cache hits, UTF-8 byte accounting across wide/narrow rewraps and eviction, exact admission boundaries with key-free transient accounting, and between-frame eviction when the key causes a shortfall. All four fail on assertions against the old product code, and pass on the fix for 100 repetitions. The transient fixture computes its expected payload independently, so accidentally charging a key on both paths cannot cancel out in a comparison.

Six targeted mutations are caught: borrowing the key, omitting its admission reservation, counting runes instead of UTF-8 bytes, losing the charge during rewrap, retaining it after eviction, and charging an unowned transient key. These supplement the earlier mutation campaigns; they do not relabel those campaigns as rerun. Assertion logs, including the before-fix run, are retained under [`paragraph-key-rerun/validation/`](linux/stage-3/paragraph-key-rerun/validation/).

The full root suite passes with `CGO_ENABLED=0`; text and UI tests pass 100 repetitions. Vet passes for Linux, Windows and Darwin, and the Windows and Darwin root cross-builds pass. These are portable and compile checks, not Windows execution. The Linux mixer is SHA-256 `5c7a4efc9611c938c551fec6327af529cab2d6cfc8e86b3d7da15f0faf978076`.

This shared text change needs Windows validation on Michael's next committed candidate; the [handoff](windows/stage-3/README.md) calls for new output directories and the original gates. Earlier Windows results and both operator manual observations remain evidence for their tested revisions. Full review `4b18b3c1df9f` reviewed this fix on 2026-11-10: **clean**, zero blocking findings and one documentation advisory. Coverage is 1,969 covered, 212 excluded, and the same two Hebrew font fixture/license paths uncovered. The manifest includes the new key tests and native rerun evidence. Michael accepted the documentation advisory: `runtime.md` now includes the built interactive widgets, and `ui.md` distinguishes native Windows input coverage from the remaining modeled-only capture-release scenario, unprovoked capture-loss cases and untested DPI transitions. These documentation corrections postdate the verdict and have not had another Terminus review; the paragraph-key fix received no finding. Canon alignment and acceptance remain separate; stage 4 is untouched.


Native Linux reruns on that binary pass **60 of 60**: controls 10/10 and regression 10/10 on Wayland, XWayland, and XWayland with forced `GDK_SCALE=2`, each in the isolated GNOME shell. The [manifest](linux/stage-3/paragraph-key-rerun/manifest.json) records commands and exit codes; the [audit](linux/stage-3/paragraph-key-rerun/audit.json) confirms all 33 process logs drain once with no outstanding ownership or paint failures and empty application stderr. Shell warnings are separate. The scale-2 controls image was visually checked. Forced scale is not a real monitor transition, and these runs do not replace the operator's desktop observation. Earlier native evidence remains untouched.


## Paragraph-key Windows return

Return `a2a2109` contains only evidence and a journal entry for candidate `c1ad2bb`; no runtime, test, runner or example source changed. Linux inspection confirms the retained commands and raw results:

- Text packages pass 100 repetitions on Windows. The original regression runner passes with every gate enabled, including the full suite, repeated appcore/Win32 tests, vet, builds and the nested foundation runner.
- All 17 mixer, 9 foundation and 10 controls cases pass. The foundation's intentional failure exits 1 as expected. All 27 native process logs drain exactly once with zero outstanding ownership and no paint failures; application stderr is empty.
- The five large snapshots pass the corrected Linux validator: PNG chunks verified, pixels decoded, dimensions matched to the recorded surfaces and required growth observed. Original evidence is untouched.
- Linux return checks pass: `CGO_ENABLED=0 go test ./... -count=1 -timeout=180s` and `go vet ./...`, using `GOCACHE=/tmp/dfx2-pacing-go-cache`. The initial sandbox attempt could not write the host's Go module cache; the rerun with cache access passed. This was setup failure, not a test assertion failure.

The code matches the candidate that passed the retained 60/60 Linux native cases in `paragraph-key-rerun/`, so no additional native Linux matrix was run. The prior Linux and Windows operator observations remain recorded; no manual audition was repeated for this cache-only correction. DPI and the other stated platform limits remain untested. Returned Markdown was processed with `unfurl` on Linux. Final full review `7f15050aeece` is **clean**, with zero blocking and zero advisory findings. Coverage: 2,098 covered, 212 excluded, and the same two Hebrew font fixture/license paths uncovered. The returned Windows evidence and corrected current docs are in scope. Only this verdict note and the journal changed after review. Canon alignment and acceptance remain separate, and stage 4 is untouched.


## Review against the aligned canon

Canon commit `be99161` recognizes built layout and controls and makes bounded input, overflow cancellation, ordered edit deferral and idle-input behavior explicit in the existing immediate-mode quality. Full dfx2 review `8d23bf53f115` includes those revised rules and is **clean**, with zero blocking and zero advisory findings. Coverage remains 2,098 covered, 212 excluded, and the two previously recorded Hebrew font fixture/license paths uncovered.

No runtime or validation code changed in this alignment or review; both hosts' candidate evidence remains applicable. Only the evidence and journal verdict notes changed after this review. Stage 3 is ready for Michael's acceptance. Existing platform limits and historical unresolved observations remain recorded; stage 4 has not started.
