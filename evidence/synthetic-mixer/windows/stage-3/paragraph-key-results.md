# Synthetic mixer stage 3: Windows results for the paragraph-key candidate

Status: **executed on Windows on 2026-11-10 at `c1ad2bb`.** The text package tests passed 100 of 100 repetitions, the unmodified stage 2 regression matrix passed with every gate enabled including the nested foundation runner, and the stage 3 controls matrix passed 10 of 10. No runner, test, runtime, backend or example code changed on this host. This is not acceptance of stage 3, and stage 4 is not started. The earlier records, [results.md](results.md) for `35dc8c4` and [return-results.md](return-results.md) for `c8f880a`, are untouched.

Unit tests are modeled even when run on Windows. The matrix and controls cases are native observations on this desktop, through real `SendInput` mouse and keyboard, real capture and real window messages.

## Host

The same host and session as [results.md](results.md#host): Windows 10 Pro 10.0.19045, go1.27.3 windows/amd64, `CGO_ENABLED=0` throughout, Windows PowerShell 5.1.19041.6456 not elevated, Remote Desktop session 2 (`rdp-tcp#12` after the run; `host.json` still records `RDP-Tcp#0` from the agent shell's environment), Microsoft Remote Display Adapter 3440×1440 at 96 DPI. Every `first_surface` in this run, across the matrix, the foundation cases and the controls fixture, is `@1`.

## Revision

`c1ad2bb82dd99cdd1a41ce96bcd84ba2921951e7`, local `main` equal to the transferred commit "text cache improvements; regression coverage; docs". The worktree was clean before the first command; every runner recorded it as clean apart from `stage-3/paragraph-key-run/` (`matrix/host.json`, `matrix/foundation/host.json`, `controls/revision.txt`). The candidate checks the README asks for hold: `internal/text/key_test.go` contains `TestParagraphKeyOwnsOnlyItsSubstringAndSurvivesCacheHits` (line 36); admission in `internal/text/system.go` clones the key with `strings.Clone` after admission succeeds and adds its UTF-8 length to `keyBytes`, `p.bytes` and `paragraphBytes`; `ui/layout_test.go` still holds `TestHugeEqualFlexWeightsShareEquallyAndFinish` (line 117). Relative to `c8f880a` the non-evidence changes are `internal/text/system.go`, two text test files, and documentation. The Linux hash `5c7a4efc…` is not comparable; compare revisions.

## What ran, in order

| Local time | Command | Outcome |
| --- | --- | --- |
| 11:02 | `CGO_ENABLED=0 go test ./internal/text/... -count=100` | **passed, exit 0**: `ok` for `internal/text` (1.4 s) and `internal/text/gotext` (2.6 s). Output in `paragraph-key-run/tests/go-test-text-100.log`, command and exit code in `paragraph-key-run/tests/command.txt` |
| 11:05:42–11:08:52 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\audition.ps1 -OutDir evidence/synthetic-mixer/windows/stage-3/paragraph-key-run/matrix` | **passed, exit 0**, every gate on: `tests` (`./...`, 9 packages ok, including both text packages), `tests-win32-verbose` (all PASS), `tests-repeated` (`-count=50`, both ok), `vet` and three builds (empty logs); all 17 mixer cases with no failures; the nested foundation matrix passed its own `go test ./...` gate, vet and build, and all 9 lifecycle cases, exit 0. Console in `paragraph-key-run/matrix-console.log`, command and exit code in `paragraph-key-run/matrix-command.txt` |
| 11:08:52–11:09:19 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\stage-3\controls.ps1 -OutDir evidence/synthetic-mixer/windows/stage-3/paragraph-key-run/controls` | **passed, exit 0**, 10 of 10, `clean` true, 33 edits, `quit` returned `<nil>`, empty stderr. Console in `paragraph-key-run/controls-console.log`, command and exit code in `paragraph-key-run/controls-command.txt` |

No retries were needed, so there is one directory per step.

## Results

- **Text tests, 100 of 100.** Both text packages passed every repetition.
- **Mixer matrix, 17 of 17**: fonts, animate, resize, minimize, expose, large, close-sample, reopen, two, modal-move, modal-size, modal-idle, modal-close-self, modal-close-other, modal-quit, signal, inspection, each exit 0 with an empty `failures` array and empty stderr. Captures and snapshots are under `matrix/captures` and `matrix/snapshots`.
- **Foundation matrix, 9 of 9**: lifecycle-window, lifecycle-resident, idle-window, idle-resident, failure, cancel, create-close, signal, inspection, with empty `failures` arrays. The `failure` case exits 1 by design, as in every earlier Windows run.
- **Controls matrix, 10 of 10**: drag_outside_window, checkbox, keyboard, independent_focus, external_during_drag, reorder_during_drag, remove_during_drag, focus_loss_during_drag, hover_is_idle, close_during_drag. The fixture's final `drained` record shows zero windows, tasks, queued callbacks, queued input, native windows, retired records and paint failures, and no ownership problems. The 33 edits match the `35dc8c4` Windows run in count. `controls-before.png` and `controls-focused.png` are retained.
- **Runner changes: none.** Both tracked auditions and `controls.ps1` ran as committed.

## Remaining limits

- **DPI** untested on this host: one Remote Desktop display at 96 DPI, no scale change possible, every surface `@1`. Unchanged from results.md.
- **Manual audition** not repeated for this cache-only correction, as the README allows; the `35dc8c4` manual observations in results.md stand, with the same gaps: closing a window during a drag by hand was not exercisable here, and `ReleaseCapture` inside a real size/move loop, capture loss to a menu or another process, and real DPI transitions remain modeled only.
- **The Linux manual audition** is not claimed here.
- `unfurl` is not available on this host, so this file and the journal entry were not run through it.

Nothing is committed from this session. Michael commits and transfers the tree back to Linux.
