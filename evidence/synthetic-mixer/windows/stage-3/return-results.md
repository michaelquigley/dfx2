# Synthetic mixer stage 3: Windows return results

Status: **executed on Windows on 2026-11-09 at `c8f880a`.** The Windows test-gate gap recorded in [results.md](results.md) is closed on this host: the two previously timing-sensitive unit tests passed 300 of 300 repetitions, and the unmodified stage 2 audition passed with every gate enabled, including the nested foundation matrix. No runner, test, runtime, backend or example code changed here. This is not acceptance of stage 3, and stage 4 is not started.

The unit tests are modeled tests even when run on Windows; they say nothing native. The matrix cases are native observations on this desktop, through real input and real window messages.

## Host

The same host and session as [results.md](results.md#host): Windows 10 Pro 10.0.19045, go1.27.3 windows/amd64, `CGO_ENABLED=0`, Windows PowerShell 5.1.19041.6456 not elevated, Remote Desktop session 2 (`rdp-tcp#10` before and after; `host.json` still records `RDP-Tcp#0` from the agent shell's environment), Microsoft Remote Display Adapter 3440×1440 at 96 DPI. All 35 `first_surface` records in this run are `@1`.

## Revision

`c8f880a29f6902f72973ad189d8154184062a7b1`, local `main` equal to the transferred commit "synthetic-mixer: stage 3 round 2", committed 2026-11-09 15:03 local. The worktree was clean before the first command; both runners recorded it as clean apart from `stage-3/return-run/` (`return-run/matrix/host.json` and `return-run/matrix/foundation/host.json`). `ui/layout_test.go` still holds `TestHugeEqualFlexWeightsShareEquallyAndFinish` at line 117. Relative to `35dc8c4`, the only non-test source changes are under `internal/platform/gtk`; the appcore and Win32 changes are the four test files named in the Linux return, and both new regressions (`TestQueueInputDeliversWholeBatchBeforeReturningWithAWakePending`, `TestRetryBaselineWaitsForTeardownFreeToBeRecorded`) are included in the package runs. The Win32 regression is also individually listed in `tests-win32-verbose.log`. Windows runtime and mixer sources are unchanged, so the controls matrix and the manual audition in results.md stand as the evidence for them and were not repeated.

## What ran, in order

| Local time | Command | Outcome |
| --- | --- | --- |
| 15:05:58–15:06:11 | `CGO_ENABLED=0 go test ./internal/appcore ./internal/platform/win32 -count=300 -timeout=300s` | **passed, exit 0**: `ok` for both packages (6.1 s and 5.5 s). Output in `return-run/tests/go-test-300.log`, command and exit code in `return-run/tests/command.txt` |
| 15:11:16–15:14:26 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\audition.ps1 -OutDir evidence/synthetic-mixer/windows/stage-3/return-run/matrix` | **passed, exit 0**, every gate on: `tests` (`./...`, 9 packages ok), `tests-win32-verbose` (all PASS), `tests-repeated` (`-count=50`, both ok), `vet` (empty), three builds (empty logs); then all 17 mixer cases with no failures; then the nested foundation matrix, which passed its own `go test ./...` gate and all 9 lifecycle cases, exit 0. Console in `return-run/matrix-console.log`, command and exit code in `return-run/matrix-command.txt` |

No retries were needed, so there is one directory per step.

## Results

- **Repeated tests.** On the previous transfer the appcore overflow test failed about 44% of runs and the Win32 acknowledgment test about 1.5%, in both the 50-count gate and isolation. At this revision neither failed in 300 repetitions of each package, in the matrix's own 50-count gate, or in the single-pass gates of either runner. That is consistent with the Linux diagnosis that the failures were in the test barriers, not the core; it does not prove the absence of a flake below the 1-in-300 level.
- **Mixer matrix, 17 of 17**: fonts, animate, resize, minimize, expose, large, close-sample, reopen, two, modal-move, modal-size, modal-idle, modal-close-self, modal-close-other, modal-quit, signal, inspection, each exit 0 with an empty `failures` array and empty stderr. Modal move and size turns show 0 paint failures; the final inspection drains to zero windows, tasks, queued callbacks, queued input, native windows and retired records, as in the earlier diagnostic run. Captures and snapshots for the large, resize, animate, expose and fonts cases are under `return-run/matrix/captures` and `return-run/matrix/snapshots`.
- **Foundation matrix, 9 of 9**: lifecycle-window, lifecycle-resident, idle-window, idle-resident, failure, cancel, create-close, signal, inspection, with empty `failures` arrays. The `failure` case exits 1 by design, as it did in every earlier Windows run.
- **Runner changes: none.** The tracked `audition.ps1` and the tracked foundation `audition.ps1` ran as committed. The scratch copies from the earlier run were not used.

## Remaining limitations

- **DPI** remains untested on this host: one Remote Desktop display at 96 DPI, no scale change possible, every surface `@1`. Unchanged from results.md.
- **Closing a window during a drag by hand** remains unexercised here, as recorded in results.md; the automated `close_during_drag` case covers the console close.
- **The Linux manual audition** is not claimed here, and nothing in this run speaks to it.
- `unfurl` is not available on this host, so this file and the journal entry were not run through it.

Nothing is committed from this session. Michael commits and transfers the tree back to Linux for the return checks and review.
