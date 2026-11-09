# Synthetic mixer stage 3: Windows results

Status: **executed on Windows on 2026-11-09 at `35dc8c4`.** The stage 3 controls matrix passed natively, 10 of 10. The stage 2 regression matrix as written did not pass: its repeated-test gate failed on two root-module unit tests that are timing-sensitive on this host, before any native case ran. Every native case of that matrix, and the foundation lifecycle matrix, then passed through scratch copies of the runners with only the failed test gate omitted, recorded below as diagnostic runs. The two test failures are findings for triage, not corrected here; no test, runtime, backend or example code changed. Michael's manual audition found nothing wrong in what was testable; closing a window during a drag could not be exercised, and DPI is untested on this host. His words are in the manual section. Nothing here is acceptance, and stage 4 is not started.

Everything under "native" happened on this desktop, with real `SendInput` mouse and keyboard, real capture and real window messages. The unit tests, including the Win32 input tests, run against modeled systems and say nothing native.

## Host

| Item | Value |
| --- | --- |
| Windows | Microsoft Windows 10 Pro 10.0.19045, 64-bit |
| Go | go1.27.3 windows/amd64, `CGO_ENABLED=0` throughout |
| PowerShell | Windows PowerShell 5.1.19041.6456, 64-bit, not elevated |
| Session | Remote Desktop, the only way onto this host. `query session` before and after: `rdp-tcp#8`, session 2, active. `host.json` records `RDP-Tcp#0` because `$env:SESSIONNAME` in the agent's shell predates client reconnects |
| Display | Microsoft Remote Display Adapter, 3440×1440, 32 bpp, one screen at 96 DPI (`LogPixels` 96; every `first_surface` is `@1`). The listed Basic Display Adapter at 1024×768 is not the session's display |
| Show window contents while dragging | on (`driver-host` in every modal log) |
| Fonts | `sans-serif` resolves to Verdana; Hebrew and Arabic come from Arial, Japanese from MS Gothic, emoji from Segoe UI Emoji (`matrix-native/fonts.jsonl`), as in stage 2 |

## Revision

`35dc8c474cb6a0f26e3040c52eda4ac9cac062a6`, local `main` equal to the transferred commit "synthetic-mixer: stage 3", committed 2026-11-09 11:06 local. The worktree was clean before the first run; every runner recorded it as clean apart from `stage-3/run/` (`revision.txt`, each `host.json`). `ui/layout_test.go` contains `TestHugeEqualFlexWeightsShareEquallyAndFinish` at line 117, so the transfer is the flex-fix candidate the README names. The Linux-side hash `aff24bf6…` is not comparable; Windows builds of this revision hashed `25f86840…` on the clean tree and `3d4542d6…` once `run/` existed (Go stamps `vcs.modified` from untracked files too). The controls run and the manual audition use the `3d4542d6…` binary.

## What ran, in order

| Local time | Command | Outcome |
| --- | --- | --- |
| 11:10:30–11:10:43 | `audition.ps1 -OutDir …/stage-3/run/matrix` | **stopped at the `tests-repeated` gate**: `go test ./internal/appcore ./internal/platform/win32 -count=50` failed (13 and 1 failures, below). `tests` and `tests-win32-verbose` passed. No native case ran |
| 11:10:43–11:11:12 | `controls.ps1` into `run/controls` | **passed**, 10 of 10, exit 0 |
| 11:16:02–11:18:54 | scratch copy of `audition.ps1` without the `tests-repeated` line, into `run/matrix-native` | **17 of 17 mixer cases passed**; the nested foundation matrix stopped at its own single-pass `go test ./...` gate on the same test (`run/matrix-native/foundation/tests.log`), so the runner exited 1 for that reason alone |
| 11:28:39–11:28:52 | scratch copy of `evidence/foundation/windows/audition.ps1` without its `tests` line, into `run/matrix-native/foundation-native` | **9 of 9 passed**, exit 0 |

Between runs, the two failing tests were repeated in isolation (`run/test-repetition/`). Michael was asked before each run and kept his hands off the mouse and keyboard; the RDP client stayed connected.

## Stage 3 controls matrix: `run/controls/` (native)

`controlsdrive` ran for the first time anywhere. It found both windows, placed A at (40, 60) and B to its right, and drove every case through `SendInput` and `SetCursorPos`; model changes went through the fixture's console. `first_surface` was `460x300@1 visible=true`. Every case passed on the first attempt, with no runner change.

| Case | Seconds | What the edit log shows |
| --- | --- | --- |
| `drag_outside_window` | 2.2 | A press at 0.5 on a 0.5 fader edited nothing. Motion gave 0.601, 0.703, 0.797. Moving to 100 px right of and 40 px above the window gave exactly 1; 100 px below gave exactly 0, both under capture. The release outside added no edit, and motion afterwards edited nothing |
| `checkbox` | 1.1 | Press edited nothing; release inside gave one `mute=true`; press, drag 800 px away, release gave nothing |
| `keyboard` | 4.3 | Up, Up, Right, Down, Left, Page Up, Page Down, Home, Home, Down, End from 0.5 gave exactly 0.51, 0.52, 0.53, 0.52, 0.51, 0.61, 0.51, 0, 1 (the second Home added nothing). Tab then Space held: nothing on press; on release `a/mute=true`. Tab, Up: `b/gain=0.51`. Shift+Tab, Space: `a/mute=false`. Three letter keys: nothing |
| `independent_focus` | 1.7 | Up in B, A, B edited `B/c`, `A/a`, `B/c` in that order |
| `external_during_drag` | 1.4 | `gain b 0.9`, `mute b on` with no drag: no edit. Press at 0.9 on the 0.9 fader: one edit at 0.899 (pointer quantization). `gain b 0.1`, `gain b 0.3` during the drag: no edit. Motion to 0.6: one edit at 0.601 |
| `reorder_during_drag` | 1.7 | Press on a at 0.8, `reorder`, motion to the first strip's 0.3, release: every edit on channel a (0.797, 0.297). After `reorder` back, a click on the first strip edited a |
| `remove_during_drag` | 2.1 | Press on a at 0.7 (0.703), `remove a`, motion and release: no further edit. `insert a`, motion over the reinserted strip: nothing. Click on it: `a/gain=0.203` |
| `focus_loss_during_drag` | 1.8 | Press on b (0.797), activate B, motion to 0.1 and release: no edit. Back in A, a click on b edited (0.203) |
| `hover_is_idle` | 4.3 | Five pointer positions and a letter: 0 frames. Three seconds: 0 frames, frame timers, presents and evaluations |
| `close_during_drag` | 1.2 | Press on B's c (edit at 0.5), `close`: B's `closed` arrived; release; a click in A on c edited (0.899) |

Exit 0, `Run` returned `<nil>`, `drained` all zeros (windows, tasks, callbacks, completions, queued input, native windows, retired, dispatch targets, paint failures), stderr empty, 33 edits. The Linux flex-rerun logged 35 edits: its driver makes two calibration presses the Windows driver does not need. The remaining 33 match the Linux sequence one for one in window, channel, control and value, differing only by pointer quantization (148 px of travel here against the compositor's subpixel coordinates there).

`controls-before.png` and `controls-focused.png` (460×300): three strips, crisp Verdana text, square Mute boxes, the focus ring outside the box. The focused image shows A's fader at the top after End, with the ring on A's Mute after Shift+Tab.

### What this establishes about the README's modeled assumptions

1. **`SetCapture`, `ReleaseCapture`, `WM_CAPTURECHANGED`:** a real drag kept tracking 100 px outside the window on three sides, clamping to 1 above and 0 below, and the backend's own release at button-up read as no loss (a later motion edited nothing and the next press worked). Native, every drag case.
2. **Signed coordinates:** the above-and-right position gave exactly 1 and the below position exactly 0, so the captured pointer's coordinates arrived negative above the window and beyond the client area to the right and below, as signed 16-bit values. Native.
3. **Keys:** Tab, the arrows, Page Up/Down, Home, End and Space reached the window with no dialog manager in the way, in the exact sequence; Tab never left the window during the automation; a letter key did nothing and requested no frame. Native. Auto-repeat was not sent by the driver; see the manual audition.
4. **`WM_KILLFOCUS` during a drag:** activating the other window ended the gesture with no final edit, released capture safely, and a fresh press worked. Native.
5. **Coexistence with size/move loops:** not covered by this matrix. The modal cases of the regression matrix below show the loop's own capture working, and the manual audition's step 6 covers pressing a title bar after a fader drag. `ReleaseCapture` during a real size/move loop remains modeled only.
6. **DPI:** untested; see DPI below.

Overflow (257 events between frames) remains unreachable natively, as on Linux.

## Stage 2 regression matrix: the test gate (finding)

`audition.ps1` runs `go test ./internal/appcore ./internal/platform/win32 -count=50` before any native case. On this host that gate fails almost every time, on two tests. Everything else in the gate passed: `go test ./... -count=1` passed in the first run (`run/matrix/tests.log`), the verbose Win32 pass passed, and so did vet and the builds.

**`TestOverflowDiscardsPendingInputReportsOnceAndRequestsAFrame` (`internal/appcore/input_test.go:136`).** 13 of 50 in the gate; 88 of 200 in isolation; 0 of 200 with `GOMAXPROCS=1`; 19 of 50 for the whole package. Every failure is the first assertion: after `queueInput` with 256 events into a hidden window, `InspectContent` showed between 55 and 252 queued, `Frames:0`, `Consumed:0`, `InputOverflows:0`.

A probe applied with `go test -overlay`, the tree untouched (`run/test-repetition/overflow-probe_test.go.txt`, its overlay, and `overflow-probe-100.log`), repeats that first step and, when the count is short, reads how many events the fake backend still holds. In all 43 short runs of 100, the inspected count plus the fake's undelivered count was 256 (254 to 257, the two reads being separate), and once the fake's queue drained the window held all 256 with 0 frames and 0 overflows. **No event is lost; the test's barrier returns before the pump has delivered the batch.** `queueInput` enqueues one event at a time and then calls `turn`, which posts a callback and waits for the `Wake` whose dispatch runs it. The fake's queue is first-in, first-out, so this works only if that wake is queued after the whole batch. While the test goroutine is still enqueueing, the UI thread is already pumping, and the core requests its own wake on arriving input; the `Post` then finds a wake pending and does not queue another. That earlier wake's dispatch runs the barrier callback with the rest of the batch still in the fake's queue. The same class of defect as the one Terminus `ba06f23e6163` flagged in `ui/core_test.go` (one `Enqueue` per event under an uncontrolled schedule), now in the appcore helpers; the journal of 2026-11-08 records the fix pattern used there. On Linux this schedule is rare enough that 100 repetitions passed. This reading of the mechanism is from the code and the probe, not from instrumenting the core.

**`TestAcknowledgmentKeepsTheFailureNotYetReported` (`internal/platform/win32/core_amd64_test.go:214`).** 1 of 50 in the gate, 3 of 200 in isolation, 1 of 50 for the package; always "3 free attempts on one retry, want 2". The test is over the modeled Win32 host and says nothing native. Reading the helpers: `holdPending` starts counting after `Await` returns, and `Await` returns as soon as the UI thread is parked with `Problem` mentioning unreleased allocations, which is true after the first failed free (the replacement through `Present`) and before the teardown's free has been attempted. If the UI thread parks in that gap, the baseline misses the second allocation's first attempt, and the next tick's two retries plus that attempt count as three. That is a hypothesis from the code; it has not been confirmed with a probe. A runtime cause (a retry freeing one allocation twice) is not excluded by this evidence.

These are reported for triage. Neither test was changed, and the gate was not weakened in the tracked runner. The matrix as written therefore has no passing run on this host at this revision.

The historical unexplained `internal/appcore` `FAIL` (2026-11-05, output lost) predates both tests and is not explained by this; it carries forward separately.

## Stage 2 regression matrix: native cases, `run/matrix-native/` (native, diagnostic run)

Run through a copy of `audition.ps1` in the agent's scratch directory, identical to the tracked script except that `$PSScriptRoot` is spelled out and the `tests-repeated` line is replaced by a comment (`run/matrix-native/runner-diff.txt`). It still ran `go test ./... -count=1` (passed this time), the verbose Win32 pass, vet and the three builds, then the seventeen cases. The nested foundation matrix is covered below.

**All 17 cases passed**, every one draining to zeros with empty stderr and zero paint failures; the inspection found 31 system modules, no browser module and no child process.

| Case | Observed |
| --- | --- |
| `fonts` | faces as in Host above |
| `animate` | capture not black (background 0.87); settled, 5 s of zeros |
| `resize` | 760×320, 380×240, 980×420, 560×260, each with a retained snapshot; 5 s of zeros after |
| `minimize` | `visible=false` with frames held at 28 and frame timers at 2; restored to 77 frames; quiet after. New with stage 3: `input_queued` was 1 while minimized (the focus-loss input queued behind the hidden window) and 0 once restored |
| `expose` | 5 invalidations: 5 paints, 0 frames, 0 presents; capture not black |
| `large` | all five sizes reached and captured, below |
| `close-sample` | 791 accepted, 436 refused after close |
| `reopen` | five cycles, zero windows after each |
| `two` | 30 frames after the other window's close |
| `modal-move`, `modal-size` | below |
| `modal-idle` | 18 s hold: inside the live loop with the feed paused, 3 s of 0 frames, modal timers, modal turns, wakes and dispatches with `modal_active` true; a post then ran inside the loop in 15 ms; resuming rendered 29 frames in a second, still inside it; 33 turns for the whole hold |
| `modal-close-self` | the dragged window closed from a worker 3 s in: `Done` in 15 ms, loop gone, driver saw the window dead, B rendered 29 frames in the next second |
| `modal-close-other` | B closed 3 s into A's drag: `Done` in 31 ms with the loop still active, A rendered 29 frames in the next second, the drag ran its full 8 s |
| `modal-quit` | `Quit` 2 s in, inside the loop: `return` nil, drained |
| `signal` | three rising updates per window (A: 1/2, 2/3, 3/7; B: 1/1, 2/2, 3/5), then `context canceled` |
| `inspection` | no WebView2, Edge or Chromium module; no child process |

`large`, captures matching retained snapshots at every step:

| Step | Surface | Bytes | Capture black | Capture background |
| --- | --- | --- | --- | --- |
| 0 | 984×761 | 2,995,296 | 0 | 0.97 |
| 1 | 1240×900 | 4,464,000 | 0 | 0.98 |
| 2 | 1600×1000 | 6,400,000 | 0 | 0.98 |
| 3 | 2400×1300 | 12,480,000 | 0 | 0.99 |
| 4 | 3200×1400 | 17,920,000 | 0.031 | 0.94 |

The 3200×1400 window's black is the strip below the 1440-pixel screen, as in stage 2.

Modal progress, the driver dragging A for 13 s with the pointer moving every 16 ms:

| Case | Whole seconds | Frames per second, A | Frames per second, B | Titles per second, A / B | Turns | Timers | Refusals |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `modal-move` | 12 | 30–31 (363) | 30–31 (363) | 10–11 / 10–11 (121 each) | 394 | 393 | 0 |
| `modal-size` | 12 | 32–38 (408) | 29–34 (373) | 9–11 / 9–11 (120, 121) | 458 | 459 | 0 |

In every sampled second both windows rendered and both title counters advanced; both loops ended on release, and the 5 s settled interval was all zeros. This is the same shape as the stage 2 runs of 2026-11-06, so stage 3's changes to the frame path and the window procedure did not alter the loop's behavior here.

## Foundation lifecycle matrix: `run/matrix-native/foundation-native/` (native, diagnostic run)

Run through a copy of `evidence/foundation/windows/audition.ps1` identical except for the omitted `tests` line (same diff file). All nine cases passed with exit 0 from the runner: `lifecycle-window`, `lifecycle-resident`, `idle-window`, `idle-resident`, `failure` (exit 1 as expected), `cancel`, `create-close`, `signal` (console `CTRL_BREAK_EVENT` observed as cancellation) and `inspection`. The earlier attempt, nested inside the mixer runner, is kept as `run/matrix-native/foundation/tests.log`: it stopped at `go test ./... -count=1` on the overflow test, which was the only failure in that pass.

## Runner changes

None to any tracked runner. `controlsdrive`, `controls.ps1`, `audition.ps1` and `desk` ran as committed. The two scratch copies are described above, with their diffs retained; they exist only because the test gates block the native cases on this host, and their runs are labeled diagnostic for that reason. Whether the gates should pass at `-count=50` on Windows is part of the triage.

## Manual audition (native, Michael's observations)

Michael ran `C:\Users\Michael\AppData\Local\Temp\dfx2-mixer.exe -controls` from his own terminal on 2026-11-09, the binary built here from `35dc8c4` (`3d4542d6…`, the same build the controls run used), over the same Remote Desktop session. His report, verbatim:

> everything that was testable seems fine. cannot close while dragging, and dpi changes are unavailable.

Read against the README's steps: steps 1 to 5, 7 and 8 (appearance, a hand drag past the screen edges, keyboard navigation with auto-repeat and Space on release, two windows with independent focus, Alt+Tab during a drag, the console's external changes during a drag, and clipping under resize) are covered by "everything that was testable seems fine", with nothing reported as wrong. In step 6, the close-during-a-drag part could not be exercised: "cannot close while dragging". That matches stage 2's manual observation that the host swallows Alt+F4 during a drag, so this is unavailable coverage on this host, not a failure; the automated `close_during_drag` case closed the dragged window from the console instead and passed. Step 9 is untested: "dpi changes are unavailable". The agent did not watch the audition; this section is Michael's observation only.

## DPI

Untested. The session is Remote Desktop with one virtual display at 96 DPI; every `first_surface` in every run is `@1`, no scale change is possible over RDP on this host, and no monitor transition happened. The driver's `GetDpiForWindow` mapping and the backend's signed-coordinate scaling therefore ran only at scale 1. Injected tests at other scales are not native coverage.

## Still open after this run

- The two unit-test failures above await triage on Linux; until then the stage 2 matrix as written has no passing run on this host at this revision, and the foundation runner's gate fails about half the time here.
- `ReleaseCapture` during a real size/move loop stays modeled only; native capture loss to a menu or another process's capture was not provoked.
- Overflow natively, GTK capture loss, a true X11 server, fractional Wayland scale and accessibility consumers remain as `stage-3.md` lists them.
- Earlier items carried forward unchanged: stage 2's three timer-fallback limits (`SetTimer` never failed here), the earlier unexplained Linux failures, the historical `internal/appcore` `FAIL` of 2026-11-05, a failed `VirtualFree` being injected-only, and the Linux manual audition, which has not happened.
