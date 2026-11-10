# Synthetic mixer stage 4: Windows results

Status: **executed on Windows on 2026-11-10 at `34852d5`; the automated audition passed on its first run, and Michael's manual audition found nothing wrong ("everything seemed to work fine").** `stage4.ps1` ran as committed with every gate enabled: the repeated gates, the unmodified stage 2 regression matrix with its nested foundation matrix, the 17 stage 4 controls and command cases, and both text-cache runs, exit 0. No runner, test, runtime, backend or example code changed on this host. One measurement limitation is recorded under timing: the process CPU figure on this host read zero through the timing case, so the Windows CPU observation is unusable as recorded; frame, evaluation and present rates are fine. This is not acceptance of stage 4.

Unit tests are modeled even when run on Windows. The matrix, controls, command and text-cache cases are native observations on this desktop, through real `SendInput` mouse and keyboard, real capture and real window messages; screen captures are `BitBlt` copies of the desktop, and snapshots are the retained images handed to GDI.

## Host

| Item | Value |
| --- | --- |
| Windows | Microsoft Windows 10 Pro 10.0.19045, 64-bit |
| Go | go1.27.3 windows/amd64, `CGO_ENABLED=0` throughout |
| PowerShell | Windows PowerShell 5.1.19041.6456, 64-bit, not elevated |
| Session | Remote Desktop, the only way onto this host: `rdp-tcp#13`, session 2, active before and after. `host.json` records `RDP-Tcp#0` because the agent shell's environment predates client reconnects |
| Display | Microsoft Remote Display Adapter, 3440×1440, 32 bpp, one screen at 96 DPI. Every `first_surface` in every process of this run is `@1`; the text-cache driver read a system DPI scale of 1. The listed Basic Display Adapter at 1024×768 is not the session's display. Both 760-wide windows fit on screen side by side |
| Fonts | `sans-serif` resolves to Verdana, as in the earlier stages; Greek and Cyrillic churn text came from the same face with no fallback box |

## Revision

`34852d529672bf882c4fa2c070f7556d2e13202c`, local `main` equal to the transferred commit "synthetic-mixer: stage 4; linux validation; makefile". The worktree was clean before the run (`git status --porcelain` empty); the runner's `run/revision.txt` and both `host.json` files record it as clean apart from `stage-4/run/`. The candidate checks hold: `examples/mixer/commands.go` and `examples/mixer/mixer_test.go` exist, `examples/mixer/main.go` opens controls windows at 760×520, `internal/appcore/content_test.go` holds `TestASelectedWindowMadeIneligibleMidTurnDoesNotRender` at line 847, and `frame` in `internal/appcore/content.go` rechecks `eligibleLocked` under the mutex before starting. The mixer built by the runner and the manual-audition binary both hash `94c5d83fb14dd86e…` (built after `run/` existed). Linux hashes are not comparable; compare revisions.

## What ran

One command, 16:12:46 to 16:18:31 local, exit 0, console in `run/stage4-console.log` and the command with its exit code in `run/stage4-command.txt`:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\stage-4\stage4.ps1 -OutDir evidence/synthetic-mixer/windows/stage-4/run
```

No retries were needed, so there is one run directory.

### Gates, `run/gates/`

| Gate | Command | Result |
| --- | --- | --- |
| `mixer-repeated` | `go test ./examples/mixer -count=100 -timeout=600s` | ok, exit 0, 58.6 s |
| `ui-repeated` | `go test ./ui -count=50 -timeout=600s` | ok, exit 0, 8.9 s |
| `text-repeated` | `go test ./internal/text/... -count=50 -timeout=600s` | ok for both packages, exit 0 |
| `vet-controlsdrive` | `go vet -C evidence/synthetic-mixer/windows/stage-4/controlsdrive .` | clean, exit 0 |

### Stage 2 regression matrix, `run/matrix/`, unchanged runner, every gate on

`tests` (`./...`, 9 packages ok), `tests-win32-verbose` (all PASS), `tests-repeated` (appcore and Win32, `-count=50`, both ok), `vet` and the three builds (empty logs). Then all 17 cases passed, each exit 0 with an empty `failures` array and empty stderr: fonts, animate, resize, minimize, expose, large, close-sample, reopen, two, modal-move, modal-size, modal-idle, modal-close-self, modal-close-other, modal-quit, signal, inspection. Captures and snapshots are under `run/matrix/captures` and `run/matrix/snapshots`. The nested foundation matrix, `run/matrix/foundation/`, passed its own `go test ./...` gate, vet and build, and all 9 lifecycle cases (lifecycle-window, lifecycle-resident, idle-window, idle-resident, failure, cancel, create-close, signal, inspection), exit 0; the `failure` case exits 1 by design, as in every earlier Windows run.

### Stage 4 controls and command cases, `run/controls/`

`summary.json`: `passed` true, `clean` true, exit 0, `return` `<nil>`, `commands_conserved` true, no ownership problems, empty `mixer.stderr.log`, 119 edits, first surface `760x520@1`. The `drained` record is all zero: windows, tasks, queued callbacks, queued input, native windows, retired records, paint failures, no pending release.

| Case | Result | Observed |
| --- | --- | --- |
| `drag_outside_window`, `checkbox`, `keyboard`, `independent_focus`, `external_during_drag`, `reorder_during_drag`, `remove_during_drag`, `focus_loss_during_drag`, `hover_is_idle` | all pass | The nine stage 3 cases over six strips |
| `delayed_commands` | pass | Latencies 701, 1391, 2081, 2555 ms at a 700 ms device delay (Linux: 701, 1391, 2081, 2446) |
| `rejected_commands` | pass | Last error `#38 d gain 0.56 rejected: device refused`; device stayed at 0.55 |
| `overloaded_commands` | pass | 65 accepted, 5 refused as `queue full`, at a 60 s delay |
| `external_while_pending` | pass | |
| `timing` | pass | Below |
| `close_first_window` | pass | Window C opened, A closed; B and C still edit and apply |
| `close_during_drag` | pass | B closed by name mid-drag; C kept responding |
| `shutdown_outstanding` | pass | Sequences 115 to 119 canceled, none applied |

Command totals at `return`, balanced: 114 accepted, of which 107 applied, 2 rejected by the device and 5 canceled at shutdown (`shutdown before the device received it`); 5 refused as `queue full`. The Linux run's 117 accepted differs by the Linux driver's extra calibration presses, as in stage 3.

### Timing observations

Measurements on this host, over Remote Desktop, two windows of 760×520; they are observations for the record, not pass criteria or guarantees.

| Interval | Frames/s, both windows | Evaluations/s | Presents/s | Producer ticks | Process CPU as recorded |
| --- | --- | --- | --- | --- | --- |
| Feed running, 5 s | 68.1 | 68.1 | 68.1 | 150 | 0% |
| With 36 churn words, 5 s | 67.7 | 67.9 | 67.7 | 150 | 0% |
| Paused and settled, 3 s | 0 | 0 | 0 | 0 | 0% |

A further second after settling armed no new deadline timer. About 34 frames per second per window, against about 60 on Linux. That is in line with this host's earlier Windows observations: the stage 2 and stage 3 modal cases rendered 30 to 38 frames per second per window here. The difference from Linux is not characterized; the Windows timer granularity and the Remote Desktop session are both candidates, and nothing was profiled.

**The CPU column is a measurement failure on this host, not a near-zero cost.** The example reads `GetProcessTimes`, which this Windows accounts in 15.625 ms clock ticks, and the driver reports the difference across each interval. Over the whole audition the process accrued 0.94 s, almost all of it during the input-driven cases; through the timing case, 443 frames of churn and the settled interval, `cpu_seconds` rose by 0.03 s and the three measured intervals each saw no change at all. Timer-paced work that runs just after each clock tick and sleeps before the next is a known blind spot of tick-sampled accounting, which fits what was recorded. The figure is therefore unusable as a CPU observation here. The default-budget text counters taken in the same case are good: paragraph evictions 755 in A and 741 in B under the 1 MiB default, no glyph evictions, within budget. Nothing was changed: the example's CPU reader is a reporting aid outside the pass criteria. A concrete resolution for Michael's decision would be to read `QueryProcessCycleTime` on Windows, or to report the measured interval as unavailable when the tick count does not move, so that the record cannot show a false zero.

### Text-cache runs, `run/controls/text-small/` and `run/controls/text-default/`

Both passed: every sample within budget, A's retained image equal to the fresh window C's pixel for pixel at 760×520, evictions present where required, `return` `<nil>`, exit 0, empty stderr, drained to zero.

| Budget | Flag | Scale | A glyph evictions | A paragraph evictions | Over budget | A equals fresh C |
| --- | --- | --- | --- | --- | --- | --- |
| small | `49152:65536:48` (48 KiB glyphs × 1², 64 KiB paragraphs, 48 entries) | 1 | 5,821 | 919 | 0 of 9 samples | yes |
| default | `default` (16 MiB glyphs, 1 MiB paragraphs, 256 entries) | 1 | 0 | 559 | 0 of 9 samples | yes |

Final counters under the small budget: A 34,714 glyph bytes in 224 entries and 25,025 paragraph bytes in 17 entries, with 5,780 transient glyphs and 270 transient paragraphs served over the run; fresh C 37,690 glyph bytes and 51,581 paragraph bytes, nothing transient. Under the default budget A held 1,032,953 paragraph bytes in 245 entries at the end, just under the 1 MiB limit, with 258 transient paragraphs. These compare with Linux at scale 1: 9,324 glyph and 1,138 paragraph evictions (small), 563 paragraph evictions (default); the Windows glyph set is smaller because Verdana's glyph bitmaps differ from Noto Sans's.

### What the screen captures show

I looked at all six desktop captures and the matching retained snapshots. All are complete 760×520 images of window A, nothing off screen or covered.

- `screen-delayed-pending.png`: six strips, crisp text, Keys reading `0.51 live` with `pending 0.53 #34, mute #35` on two lines, the focus ring around Keys' Mute, the status lines `3 outstanding · 35 accepted · 32 applied`, `Device delay 700ms`, `No command errors.` The retained `delayed-pending.png` is the same state one acknowledgment earlier (`0.50 live`, 4 outstanding).
- `screen-rejected-error.png`: Keys `0.55 muted`, `in sync`, the focus ring on its fader, `2 rejected by device`, and `Last error: #38 d gain 0.56 rejected: device refused`.
- `screen-overload-error.png`: Keys `pending 0.56 #103`, `65 outstanding · 103 accepted`, `5 refused`, `Device delay 1m0s`, `Last error: #108 d gain 0.55 refused: queue full`.
- `screen-timing-churn.png`: meters moving with live dB values, `feed running · tick 373`, and three full lines of Greek, Cyrillic and Latin churn text, crisp, no fallback boxes.
- `screen-text-small-A.png` and `screen-text-default-A.png`: settled meters, all strips `0.50 live`, `in sync`, three churn lines each; identical in content to the retained `text-small-A.png` and `text-default-A.png`.

Fader thumbs, Mute boxes and the focus ring render as in stage 3. The faders' appearance and the absence of mouse-wheel adjustment are already roadmap items and were not touched.

## Manual audition

**Operator-reported pass, 2026-11-10.** Michael ran the handoff's checklist on this desktop after the automated run, with `C:\Users\Michael\AppData\Local\Temp\dfx2-mixer.exe -controls` built from this revision (hash `94c5d83fb14dd86e…`, the same binary the automated audition used). The instructions he followed were the handoff's steps 1 to 7 in order, with the drain before step 3 made explicit (`delay 0s`, wait for `0 outstanding`, then `reject 1`), and step 8 noted as unavailable over Remote Desktop. His report, verbatim:

> everything seemed to work fine.

That is an overall operator observation of steps 1 to 7: appearance, delayed edits shown pending in both windows and catching up in order, a rejected edit reconciling to the device's value with the error shown, overload refusing visibly and draining in order, external changes during a drag never echoing, closing either window, and shutdown canceling outstanding commands. It is not a per-step transcript, and it is not DPI coverage: step 8 was not performed because scaling cannot change in this session. Nothing in this section is inferred from the automated run.

## Runner changes

None. `stage4.ps1`, `controlsdrive` and the tracked stage 2 and foundation runners ran as committed.

## Remaining limitations

- **DPI** untested on this host: one Remote Desktop display at 96 DPI, no scale change possible, every surface `@1`, the text-cache small budget bracket exercised at scale 1 only.
- **Process CPU** on Windows is unmeasured in effect, as above.
- **Frame rate** on this host is about half of Linux's per window, uncharacterized.
- `ReleaseCapture` inside a real size/move loop, capture loss to a menu or another process, and real DPI transitions remain modeled only, as recorded in stage 3.
- `unfurl` is not available on this host, so this file and the journal entry were not run through it; Linux follow-up.

Nothing is committed from this session. Michael commits and transfers the tree back to Linux.
