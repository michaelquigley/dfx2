# Synthetic mixer stage 2: Windows presentation and modal scheduling

Stage 2 of the [work order](../../docs/future/synthetic-mixer-work-order.md) is implemented in the working tree on top of `0d1efec`; nothing is committed. **Nothing from this stage has executed on Windows.** It is at the Windows-host handoff, not accepted: the [Windows README](windows/README.md) has the audition, and acceptance requires its native results, Linux return checks, review and Michael's decision. `docs/current/runtime.md`'s modal-loop paragraph stays as it is until a native run establishes the replacement. Stage 3 has not started.

Host: Linux 7.0.0-42-generic amd64, Go 1.27.1 (module language 1.26), GNOME on Wayland with XWayland. Every build, test and vet uses `CGO_ENABLED=0`. No dependency changed. PowerShell is unavailable here, so `audition.ps1` has only had a coarse structural check.

## Portable checks

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=180s
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./internal/appcore ./internal/platform/win32 -count=300 -timeout=400s
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mixer.exe ./examples/mixer
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -o win32.test.exe ./internal/platform/win32
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -o appcore.test.exe ./internal/appcore
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet -C evidence/synthetic-mixer/windows/desk .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -C evidence/synthetic-mixer/windows/desk -o desk.exe .
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
```

All pass. Windows and Darwin results are compilation and static analysis. The Win32 tests executed on Linux against a modeled system with injected calls; they exercise the backend's own logic and call no Windows API. The dependency-boundary test still keeps platform packages free of UI and root free of UI, text and renderer packages.

## What the injected tests establish

Tests live in `internal/appcore/turn_test.go` and `internal/platform/win32/{modal,present,backend}_amd64_test.go`.

| Contract | Tests |
| --- | --- |
| A wake arriving during a posted callback, a frame or setup runs nothing nested and is not lost, including when it was the only wake | `TestNestedWakeInsidePostedCallbackIsRefusedAndNotLost`, `TestNestedWakeInsideFrameRendersNoNestedFrame`, `TestNestedWakeInsideSetupRunsNothingUntilItReturns` |
| A view closing its window presents nothing; teardown runs in that turn and release follows | `TestCloseFromAFrameTearsDownInTheSameTurn` |
| An idle loop arms no timer; a settled loop stops arming | `TestIdleSizeMoveArmsNoTimer`, `TestWakeSchedulesAOneShotAndNeverRunsFromItsOwnHandler` |
| `WM_APP` schedules and never runs callbacks; the one-shot is killed before its turn; positive delay; re-arming comes only from a remaining wake | `TestWakeSchedulesAOneShotAndNeverRunsFromItsOwnHandler`, `TestConcurrentWakesDuringALoopCoalesceIntoOneTimer` |
| A wake latched before entry is served; leaving the loop transfers it to `Pump`; a spent wake is not repeated when `Pump`'s own servicing enters the loop | `TestWakeLatchedBeforeTheLoopArmsOnEntry`, `TestLeavingTheLoopHandsTheLatchedWakeToPump`, `TestPumpServicingThatEntersTheLoopDoesNotRepeatASpentWake` |
| Nested timer and wake during a native operation or a turn: refused, retained, re-armed at the safe point | `TestTimerArrivingDuringANativeOperationIsRefusedAndRetained`, `TestWakeDuringATurnArmsOnlyWhenTheTurnEnds` |
| Creation reentry from a modal turn, and a loop inside native creation | `TestCreationFromAModalTurnDefersWakesItsReentryLatches`, `TestLoopInsideNativeCreationDeliversNoTurn` |
| Destroying the loop's own window from a modal turn: acknowledgment and pixel release wait for `Pump`; a later loop works | `TestDestroyingTheLoopOwnerFromAModalTurnWaitsForThePump` |
| Destroying another window from a modal turn: acknowledged after that turn, loop undisturbed | `TestDestroyingAnotherWindowFromAModalTurnAcknowledgesAfterTheTurn` |
| Close from a frame in a modal turn; nothing presents after teardown | `TestCloseFromAFrameInAModalTurnPresentsNothingAfterTeardown` |
| Stale, duplicate and post-destruction timer messages do nothing and cannot consume the live timer | `TestLateTimerMessagesAreIgnored` |
| Failed `SetTimer` and latched native failures keep the wake and report through `Pump`; timers stop before target release | `TestFailedTimerKeepsTheWakeAndReportsThroughPump`, `TestLatchedFailureHoldsModalDeliveryUntilPumpReportsIt`, `TestReleaseStopsAnArmedTimerBeforeTheTargetGoes` |
| Present copies rows into backend storage, the source is never reread, exposure paints the retained image top-down without events | `TestPresentCopiesAndExposurePaintsOnlyTheRetainedImage` |
| Allocation failure keeps the last image; replacement fills before freeing; reuse policy; refusal before allocation | `TestAllocationFailureKeepsTheLastValidImage`, `TestStorageIsFilledBeforeReplacementAndReused`, `TestInvalidImagesAreRefusedBeforeAllocation` |
| Pixels outlive close and destruction until acknowledgment | `TestRetainedPixelsLastUntilAcknowledgedRetirement` |
| A failed free stays owned, withholds `Destroyed`, appears in diagnostics and is fatal once; retries run only from a delayed one-shot while allocations remain, unaffected by other traffic, and also inside a size/move loop or without `SetTimer`; a bounded persistent failure stays pending, then drains | `TestPersistentFreeFailureRetainsOwnershipAtAPacedRetry`, `TestFreeFailureRecoversOnItsFirstRetry`, `TestSuccessfulFreesScheduleNoRetry`, `TestFailedReplacementFreeStaysTrackedAndIsNeverFreedTwice`, `TestAcknowledgmentWaitsForEveryAllocationAndRetriesOnlyWhatRemains`, `TestLateRetryTimerMessagesAreIgnored`, `TestRetryContinuesWithoutATimerAndWithoutBlocking`, `TestRetrySucceedingWithoutATimerStillAcknowledges`, `TestRetriesAdvanceInsideASizeMoveLoop`, `TestReleaseStopsAnArmedRetryTimerBeforeTheTargetGoes` |
| A failed free's error reaches `Run` even after an earlier failure and a successful retry; `Run` and `Done` stay pending while the free is blocked; every failure in a flush survives; one record appears once; a window's new failure survives beside one already fatal | `TestCleanupFailureAfterAnEarlierFailureReachesRun`, `TestEveryFailedFreeInOneFlushReachesRun`, `TestAnOnlyCleanupFailureAppearsOnce`, `TestAcknowledgmentKeepsTheFailureNotYetReported`: the real backend under the real core, over the model |
| `Run` removes a repeated record by identity only: not by text, a shared cause, or an `Is` method, and only the reported part of a join | `TestRunShowsEachRecordedFailureOnce`, `TestCleanupRecordsAreDeduplicatedAgainstEachOther` |
| A failed `BeginPaint`, fill or blit (zero lines, `GDI_ERROR`) is counted and fails through `Pump`, with the native error and sizes; `EndPaint` stays balanced; a failed `BeginPaint` cannot spin; a failed paint inside a loop holds delivery; erasing fills only what the image will not cover | `TestPaintFailuresFailTheApplicationThroughPump`, `TestPaintFailureInsideALoopHoldsDeliveryUntilPump`, `TestEraseFillsOnlyWhatTheImageWillNotCover` |
| Surface events: only on change, minimize keeps the size, **synthetic** DPI change, closing seals them | `TestSurfaceEventsCarryCopiedStateOnlyOnChange` |
| Ordinary windows keep default paint and report no surfaces; failed content creation leaves nothing | `TestOrdinaryWindowsKeepTheirBehavior`, `TestContentCreationFailureLeavesNothingBehind` |
| x64 layouts of `PAINTSTRUCT` and `BITMAPINFOHEADER`; every new syscall's argument count against its SDK declaration | `TestWindowsX64Layouts`, `TestSyscallArgumentCounts` (Go layout and source check; native ABI is proved only by execution) |

Discrimination was checked by fault injection through `go test -overlay`, which never modified the tree: 36 mutations of the guards and presentation paths (nested turns allowed, each guard condition dropped, no re-arm at each safe point, stale timers accepted, timer not killed on entry, refused wake consumed, idle timer armed, owner acknowledged during its loop, pixels freed early or leaked, frame aliasing the source, bottom-up blit, a paint evaluating the core, minimize reporting 0×0, and others). The first pass exposed four weak or broken tests. One bug in a test made every result "killed", which was caught by reading the output, and three real gaps were closed: a nested wake that was not actually consumed, a model sending `WM_SIZE` for an unchanged size, and a replacement-order check that could not see the order. That first set later grew with the review corrections; the [validation summary](#validation-summary) has the current counts.

## Validation summary

Everything in this table ran on 2026-11-06 against the working tree on `0d1efec`, after the cleanup-error fix, with Go 1.27.1 on Linux/amd64. No Go source, `drive.py` or `test_drive.py` changed after it. The only later edits are to this record, `docs/current/` and the journal.

| Check | Result |
| --- | --- |
| `go test ./...`, no cgo | pass, all 10 packages with tests |
| `go vet ./...` | pass |
| `internal/appcore` and `internal/platform/win32`, `-count=300` | pass |
| Windows/amd64: `go build ./...`, `go vet ./...` | pass; compilation and static analysis only |
| Windows/amd64: `mixer.exe`, `win32.test.exe`, `appcore.test.exe`, `mixer.test.exe`, `desk.exe` build; `desk` vet | pass; none of them has executed |
| Darwin/amd64: `go build ./...`, `go vet ./...` | pass; compilation and static analysis only |
| `test_drive.py` | 21 tests pass |
| Go fault injection through `go test -overlay` | 90 of 90 killed, none stale |
| `drive.py` fault injection | 16 of 16 killed, none stale |
| `go test -race` | **not run**: `-race requires cgo`, and this project builds without cgo. No claim either way |
| Rebuilt Linux `mixer` | SHA-256 `393d09837de4dda3dc7e4906710985a1d1e3ac186b16c7475a81f70a9f6b07e2`, the binary the [latest matrix](#matrix-after-the-cleanup-error-fix) ran |

**Linux matrix:** 20 of 20 on an active desktop, rerun after the cleanup-error fix because the binary changed. The earlier Linux failures remain on record and **unresolved**.

**`Run` staying pending on a failed free is now exercised across the backend and the core, over a modeled system.** The four backend-to-core tests run the real `win32.Backend` under the real core, through `appcore.RunBackend`, and hold `Run` and `Done` pending across three failed retries before letting the free succeed. They call no Windows API: `GetMessage`, the timer and `VirtualFree` are all modeled, and the retry fires when the test says so. They live in the external package `win32_test`, because `appcore` imports `win32` on Windows.

**The timer-failure fallback has only injected coverage, and the cleanup-error fix did not change it.** When `SetTimer` fails, `Pump` keeps retrying at the same 10 ms pace without blocking. `TestRetryContinuesWithoutATimerAndWithoutBlocking` and `TestRetrySucceedingWithoutATimerStillAcknowledges` cover pacing, ownership, message servicing and the return to the timer. Three limits follow from the code and have no test of their own:

- Each message taken during the fallback follows a 10 ms pause, so servicing is limited to about 100 messages per second while it lasts. A waiting message is delayed, not lost.
- Inside a size/move loop `Pump` is not running, so without a timer retries stop until the loop ends. The allocation stays owned and in the diagnostics meanwhile.
- A `Pump` call that delivers a wake skips the retry for that call, so a continuous stream of wakes would postpone it.

These limits remain open for assessment.

The fallback also dispatches a zero `WM_NULL` message when the queue is empty. No Windows host has run that call.

**Windows:** nothing from this stage has executed there. Native validation and the [host items below](#what-only-a-windows-host-can-establish) remain pending, and acceptance requires them.

## What only a Windows host can establish

- That the real size/move loop dispatches `WM_APP` and `WM_TIMER` to a **message-only** window. The whole approach rests on this.
- What the loop does when its own window is destroyed from a timer inside it. The modeled behavior, no `WM_EXITSIZEMOVE` and the loop returning, is an assumption.
- Real frame rates during a drag. Timer granularity may be about 15.6 ms, and modal pacing adds that to the 1/60 s deadline, so expect visibly fewer than 60 frames per second. No target is claimed; the contract is continued progress.
- Whether `VirtualAlloc` storage presents correctly at and beyond the spike's failure sizes under this GDI path, and every native paint, DPI and input interaction.
- Whether a press on a title bar without movement stalls before `WM_ENTERSIZEMOVE`.
- That `audition.ps1` and `desk` themselves work. Neither has run.

## Linux native regression

`internal/appcore` dispatch changed, and `AuditionExpose` is new for GTK, so the Linux matrix was rerun with the driver's new `expose`, `large` and `fonts` cases:

```sh
CGO_ENABLED=0 go build -o mixer ./examples/mixer
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-preflight/wayland --gdk-backend wayland
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-preflight/x11 --gdk-backend x11
```

**This is not a clean result.** Retained in [linux/stage-2-preflight/](linux/stage-2-preflight/):

| Case | Wayland | X11 |
| --- | --- | --- |
| animate, close-sample, reopen, two, fonts, signal | pass | pass |
| large | **retracted**: recorded as a pass, but the surface stayed 984×761 at every step; missing coverage under the corrected check | surfaces grew to 3200×1400; no snapshots taken, so it does not meet the corrected check |
| minimize | pass (Wayland reports nothing by accepted exception) | **fail**: iconify ignored, stayed `visible=true` |
| resize | **fail**: surface stayed 560×260 | **fail** on the final run; passed on an earlier run |
| expose | **fail**: zero paints | pass on the final run; zero paints on an earlier run |

These appear to come from the session rather than this change, but the cause is **not established**. The committed stage 1 binary built from `0d1efec` failed X11 minimize and Wayland resize identically in the same session, minutes apart; those cases had passed 21 of 21 at 16:03 the same day. During these runs the desktop had been idle for over 16 minutes, with no lock reported, and Wayland windows painted about once, as if the compositor had stopped delivering frames. `mutter` is not installed, so a headless compositor could not separate the two. These failures stay on record. The rerun below passed, but it does not explain them.

### Active-desktop matrix

The full matrix was rerun on 2026-11-05 from 22:25 to 22:27 local into [linux/stage-2-active-matrix/](linux/stage-2-active-matrix/), against the working tree on `0d1efec` with the three review corrections. `idle.log` samples Mutter's idle time every 5 seconds: at most 22 seconds across the run, with the screensaver never active. The sampler's digits were corrected after the run; the file's first line says how.

```sh
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-active-matrix/wayland --gdk-backend wayland
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-active-matrix/x11 --gdk-backend x11
```

All ten cases pass on both backends, 20 of 20, with empty stderr. For the cases that failed earlier:

| Case | Backend | Earlier run | This run |
| --- | --- | --- | --- |
| resize | Wayland, X11 | Surface stayed 560×260 | Every surface matches its request |
| expose | Wayland | Zero paints | 5 paints, zero frames and presents |
| minimize | X11 | Stayed `visible=true`; frames kept rising | `visible=false`; frames held at 32 until restore |
| large | Wayland | Stayed 984×761 | Grew to 3200×1400, five matching snapshots |

Wayland minimize still reports `visible=true` and keeps rendering, as the accepted exception allows. X11 scale 2 was not part of this matrix; its only stage 2 result is the `large` rerun below.

**This is a new result, not an explanation.** The tree differs from the failed runs, and so did the desktop's state, and the committed `0d1efec` binary failed the same way in that earlier session. An idle-session cause fits that evidence and has not been isolated or established. Nothing here reproduced the failure on demand, so the same stall could return on an idle desktop.

### Matrix after the cleanup-error fix

The cleanup-error fix changed `internal/appcore`, so the Linux binary's hash changed, from `8e51f380…` to `393d0983…`. The full matrix was rerun on 2026-11-06 from 10:19 to 10:22 local into [linux/stage-2-cleanup-matrix/](linux/stage-2-cleanup-matrix/), which also keeps the binary's hash and the tree's status. The active-desktop logs above are preserved. `idle.log` shows at most 26 seconds idle, with the screensaver never active.

```sh
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-cleanup-matrix/wayland --gdk-backend wayland
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-cleanup-matrix/x11 --gdk-backend x11
```

All ten cases pass on both backends, 20 of 20, with empty stderr. Wayland minimize again reports `visible=true` and keeps rendering, as the accepted exception allows. X11 scale 2 was not part of this matrix either. **This does not explain the earlier failures**, which were not reproduced on demand.

### Large image correction

An earlier version of this record called Wayland `large` a pass. That was wrong: the driver only counted steps, and the retained run's surface never left 984×761 (2,995,296 bytes), inside the spike's failure sizes. Those logs and their `summary.json` are preserved unchanged, and the summary still shows the old check's empty failure list.

`large` now requires five steps, a surface that never shrinks, a last surface larger than the first, a largest surface beyond 3,000,000 bytes, and a retained snapshot written during the run for each step, whose dimensions match that step's observed physical surface. Requests may be clamped. `test_drive.py` beside the driver covers these without a display, including the retained Wayland log.

Rerun on 2026-11-05 at 21:59 local into [linux/stage-2-large-rerun/](linux/stage-2-large-rerun/), with the desktop in use (Mutter reported about 12 seconds idle at the start):

```sh
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-large-rerun/wayland --gdk-backend wayland --cases large
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-large-rerun/x11 --gdk-backend x11 --cases large
python3 evidence/synthetic-mixer/linux/drive.py mixer --out evidence/synthetic-mixer/linux/stage-2-large-rerun/x11-scale2 --gdk-backend x11 --gdk-scale 2 --cases large
```

| Run | Surfaces | Largest | Result |
| --- | --- | --- | --- |
| Wayland | 984×761, 1240×900, 1600×1000, 2400×1300, 3200×1400 | 17,920,000 bytes | pass; all five snapshots match |
| X11 | the same five | 17,920,000 bytes | pass; all five snapshots match |
| X11, scale 2 | 1968×1522, 2480×1530, 3200×1530, 3840×1530, 3840×1530 | 23,500,800 bytes | pass; host clamped the later requests, and all five snapshots match the physical surfaces |

Wayland growing here does not explain the earlier failure. The earlier run came after over 16 minutes of desktop idle and this one did not, which fits an idle-session cause without establishing it. **The cause remains unresolved**, and only `large` was rerun: the resize, minimize and expose failures above still stand. The Windows runner has equivalent checks in `Test-LargeImages`, **pending Windows execution**.

### Signal progress

The `signal` script's progress reporting has its own process checks, retained in [linux/stage-2-preflight/signal/](linux/stage-2-preflight/signal/) and run on Wayland on 2026-11-05 at 21:47 local:

```sh
timeout --foreground -s TERM 30 mixer -script signal -paused
timeout --foreground -s TERM 5 mixer -script signal
```

| Run | `update` records as (window, applied, frames) | Result |
| --- | --- | --- |
| Paused, before the fix (`paused-before-fix`) | Six, with no counts behind them; 2 presents in the whole run | Waited for the signal as if ready |
| Paused | `(A, 1, 1)`, `(B, 1, 1)` | Exit 1: `window A: no frame progress within 10s (applied 101, frames 1)`; drained |
| Active | `(A, 1, 2)`, `(B, 1, 2)`, `(A, 2, 5)`, `(B, 2, 6)`, `(A, 3, 9)`, `(B, 3, 10)` | SIGTERM gives `context canceled`; drained; 124 is `timeout`'s own exit code |

These runs show the script, not the console event. `drive.py`'s Linux `signal` case runs `-script animate`, so the Linux matrix never runs this script. The runner's `Test-SignalProgress` check and the `ctrlbreak` delivery are **pending Windows execution**.

Font coverage in that session, from the new `fonts` case: Noto Sans for Latin, Greek and Cyrillic; DejaVu Sans for Hebrew and Arabic; Noto Sans CJK JP; Noto Color Emoji as PNG bitmaps. It matches stage 1's manual finding.

## Decisions made while implementing, for review

- **Every failed paint step is fatal.** Michael's decision on Terminus `5d2769bf2217` F1, replacing an earlier counted-only choice. The spike's black frames returned zero lines with no error, and the possibility that a session without a display does the same is unverified, so it does not justify silently accepting failed presentation. The handoff's manual audition locks the screen and disconnects and reconnects Remote Desktop around a live window. If that shows a legitimate transient case, the evidence returns for a narrowly defined exception.
- **A failed free keeps `Run` pending.** Michael's decision on Terminus `9170a5007d5b`, with no canon exception. `VirtualFree` of our own base address should only fail on a bug, which a retry cannot fix, so a real occurrence would probably show as a shutdown that never finishes, with diagnostics every five seconds. Retries use a timer rather than a reposted wake, because a wake is immediately available and would spin. Free failures cannot be provoked on a real host, so this behavior has only injected and modeled coverage.
- **A failed free is both fatal and retained.** Michael's decision on Terminus `54ff490b53a2`. The first failure starts shutdown through `Pump` or `Present`, and the same recorded error also goes with the window's `Destroyed.Err`, because that first report loses to any earlier failure. `Run` shows one record once, matching by pointer identity, and removes only the reported part of a join. A pointer-typed sentinel that two failures share directly counts as one record, as it did for cleanup errors before; the Win32 records are each wrapped, so they do not. If a later retry fails differently, only the diagnostics show it, since each allocation keeps its first error. Michael accepted this as a bounded policy.
- **`appcore.RunBackend` is a test seam in production code.** It is `Run` on a supplied backend, without the once-per-process guard. Tests in `win32` cannot import `appcore`, and `appcore`'s `run` is unexported, so the join needs one exported name. The package is internal, so this adds no public API.
- **Other windows acknowledge destruction during a loop.** The work order's rule concerns the retiring object's own stack, so closing window B during a drag of A closes B's `Done` without waiting for the mouse. Only the loop's own window waits for `Pump`. Holding everything to `Pump` would be simpler, and is a small change if preferred.
- **Uncovered client area erases black**, not the class's white, until the next frame arrives. Black is a fixed choice in the backend, independent of the view's colors.
- **The system menu and other non-size/move modal loops still pause callbacks.** The work order covers size/move only. Wakes stay latched and delivery resumes afterward. The manual audition records it.
- **The refusal path stays.** No shipping backend returns `ErrUnsupportedContent` now, but it remains in the backend contract and core, exercised with the fake.

## Review status

Terminus `20228367ad3f` on 2026-11-06, the full rubric over the working tree before the Windows handoff, is clean with no uncovered files. It reviews this tree on Linux and does not stand in for native acceptance. See the journal for the full Terminus record of this stage: [2026-11-05](../../docs/journal/2026-11-05.md) and [2026-11-06](../../docs/journal/2026-11-06.md).
