# Synthetic mixer stage 2: Windows handoff

Status: **prepared on Linux; nothing here has executed on Windows.** Cross-builds and injected tests are preparation, not acceptance. Stage 2 is accepted only after this audition runs on a Windows host, the results return for Linux checks and review, and Michael accepts them. The [stage 2 record](../stage-2.md) has the Linux preparation and its limits.

You do not need the grimoire or Terminus on the Windows host. Everything you need is in this repository. Do not commit from an agent session; Michael commits and transfers the work.

## What this proves

Stage 2 presents the existing CPU-rendered UI on Win32 and keeps every window moving during a native size/move loop:

- `DefWindowProc` runs the size/move loop inside `Pump`'s dispatch. During that loop, a one-shot timer owned by the application's message-only dispatch window delivers one bounded core turn, only when no turn is active and no synchronous native call is unfinished. `internal/platform/win32/modal_amd64.go` holds the guards.
- Retained frames live in one `VirtualAlloc` allocation per window and reach the screen through `BeginPaint`, `SetDIBitsToDevice` and `EndPaint`. `WM_PAINT` never evaluates the view.

Four things have only ever been modeled, and only this host can establish them:

1. That the real size/move loop dispatches `WM_APP` and `WM_TIMER` to a message-only window.
2. What the real loop does when its own window is destroyed from a timer inside it. The backend assumes it ends without `WM_EXITSIZEMOVE`, and acknowledges destruction only once `Pump` regains control.
3. That frames at the sizes where the spike saw black windows present correctly from `VirtualAlloc` storage.
4. What locking the screen and disconnecting Remote Desktop do to painting. A failed paint step fails the application, with no exception for these transitions. Manual steps 10 and 11 find out whether that is right.

Handling of a failed `VirtualFree` has injected coverage only, because this audition cannot provoke one. A shutdown that never finishes and logs `Win32 image cleanup` in `native-problem` is that path, and it is a finding to return.

If modal dispatch cannot meet the progress and guard contracts on a real host, report that as evidence for a design revision. Do not restore the old pause or weaken the checks.

## Prerequisites

- Windows 10 or later, amd64, with Go 1.26 or newer on `PATH` and `git`. No C compiler; `CGO_ENABLED=0` throughout. No GTK or WebView2.
- A clean checkout of the transferred revision. The runner records `git rev-parse HEAD` and `git status --short`.
- **A connected, unlocked, interactive desktop.** The modal cases send real mouse input and the captures read the screen. Over Remote Desktop, keep the client connected and not minimized for the whole run. Turn off the automatic screen lock for the run. If the session locks or disconnects during the matrix, that run is invalid: rerun it, and keep the invalid run's logs if a paint failure appears in them. Manual steps 10 and 11 test those transitions on purpose.
- **Hands off the mouse during the automated run.** The driver moves the real pointer and presses the left button for up to about 20 seconds per modal case, for 80 seconds or more in total. Run the matrix in PowerShell started on a non-elevated desktop, so input injection is not blocked by an elevated window.

## Automated matrix

From the repository root in Windows PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\audition.ps1
```

This runs the tests, vet and builds, then each case below with a 90-second kill backstop, and then the foundation's lifecycle matrix into `run\foundation\` (the `-SkipFoundation` switch skips it, for a rerun of only the mixer cases). It takes several minutes, stops with an error listing failed cases, and writes everything under `evidence\synthetic-mixer\windows\run\`:

- `summary.json`, `host.json`, and `<case>.jsonl`, `<case>.stderr.log`, `<case>.completion.json` per case.
- `captures\*.png`: screen captures, what actually reached the display.
- `snapshots\*.png`: retained images, exactly as handed to GDI.
- `process-inspection.json`: loaded modules and descendant processes.

If a case kills the driver mid-drag, the runner releases the button with `desk.exe -release`. To do it by hand: `& "$env:TEMP\dfx2-mixer-audition\desk.exe" -release`.

| Case | What must hold |
| --- | --- |
| `fonts` | Records the face and glyph format serving Latin, Greek, Cyrillic, Hebrew, Arabic, Japanese and emoji on this host. Runs first because a cold font index can delay the first frame. |
| `animate` | The meter animates; after the feed pauses and the peak settles, five seconds show zero frames, deadline timers, wakes, dispatches and presents. Screen capture is not black. |
| `resize` | Four native resizes each reach the matching physical surface, with retained snapshots. Quiet afterward. |
| `minimize` | While minimized: `visible=false`, zero frames and zero deadline timers. Restoring renders and reports visible. Quiet afterward. |
| `expose` | Five invalidations repaint with zero frames and zero presents. Under desktop composition, occlusion sends no `WM_PAINT`, so this uses `InvalidateRect`. |
| `large` | Five sizes from the spike's 984×761 failure size upward, each screen-captured and snapshotted. The surface must never shrink, must end larger than it started and must exceed 3.0 MB, past the spike's 1.9–3.0 MB failures. The host may clamp a request, so each step's retained snapshot must match that step's observed physical surface. A failure that says `missing coverage` means this host did not reach those sizes; it is not a pass. |
| `close-sample`, `reopen`, `two` | Close racing a live sampler, five open/close cycles, and one window surviving the other's close. |
| `modal-move`, `modal-size` | Two animated windows with posted title counters while the driver drags A for 13 seconds: at least ten whole seconds inside the loop, and **in every one of them** both windows' frames and title counters advanced. Quiet after the loop. |
| `modal-idle` | Holding still inside the loop with producers stopped and meters settled: three seconds of zero frames, modal timers, modal turns, wakes and dispatches while the loop stays active. Then a post runs and the feed renders, still inside it. |
| `modal-close-self` | The dragged window is closed from a worker during the drag. `Done` closes, the loop is gone, the other window keeps rendering. |
| `modal-close-other` | The other window is closed during the drag. Its `Done` closes while the loop is still active, and the dragged window keeps rendering. |
| `modal-quit` | `Quit` during the drag drains cleanly and returns nil. |
| `signal` | Before the event, each window reports three `update` records whose `applied` and `frames` counts strictly rise. A window that stops progressing for 10 seconds fails the script, so the event is never sent. The event then makes `Run` return `context canceled` and drain. |
| `inspection` | A resident content window loads no WebView2, Edge or Chromium module and starts no browser child. |
| foundation matrix | `evidence\foundation\windows\audition.ps1`: the runtime's lifecycle regressions in the new dispatch context. |

Every case also requires a final `drained` record with zero windows, tasks, queued callbacks, native completions, native windows, retired records and dispatch targets, zero paint failures, and an empty stderr. Ownership counters are evidence of ownership, not proof about every user32 resource.

PowerShell was not available on the Linux preparation host, so neither `audition.ps1`, including its `Test-SignalProgress` and `Test-LargeImages` checks, nor `desk` has ever run. That validation is pending Windows execution. If the runner itself is wrong, as happened during the foundation stage, correct it and record exactly what changed and why in the results. Do not change the Go runtime, backend or example to make a case pass without reporting it: that is a finding.

## Manual audition

Build once, then run these from a normal terminal:

```powershell
$env:CGO_ENABLED = "0"
go build -o "$env:TEMP\dfx2-mixer.exe" .\examples\mixer
```

1. **Look at it.** `& "$env:TEMP\dfx2-mixer.exe"`. Check that text is crisp and correctly positioned, the meter animates, and wrapping follows resizes. Record what the fallback line actually shows: Hebrew and Arabic shaping and direction, Japanese, and the emoji. Segoe UI Emoji's COLR glyphs are expected to draw as hollow boxes, a known limitation, and blank space would be a failure.
2. **Drag by hand.** `& "$env:TEMP\dfx2-mixer.exe" -script modal-move -modal-wait 60s`. Within 60 seconds, drag either window by its title bar for at least ten seconds. The script ends when you release. Both meters and both title counters should keep moving. Repeat with `-script modal-size`, dragging a border, and note any flicker or black gaps while growing (the area not yet covered by a frame is erased black until the next frame). Read `modal-summary` from the output.
3. **Hold still.** Run `modal-move` again, press on the title bar without moving for a few seconds. Windows waits briefly before a press becomes a drag; record whether everything keeps moving from the press onward or only after the first movement.
4. **System menu.** Open a window's system menu with Alt+Space or by right-clicking the title bar, and leave it open. **This is expected to pause callbacks and frames** because only size/move loops have modal delivery. Record what you see; it is a known limit, not a failure.
5. **Minimize, restore and occlude.** In `& "$env:TEMP\dfx2-mixer.exe"`, minimize, restore, cover and uncover the window. It should restore showing current state with no burst.
6. **Close while dragging.** Hold a title-bar drag and press Alt+F4 with your other hand, if your host passes it through. Record what happens; it should end cleanly with `drained`.
7. **Keyboard Ctrl+C** in the terminal: both drain and `return` shows `context canceled`.
8. **Resident mode.** `& "$env:TEMP\dfx2-mixer.exe" -resident`, then type `open`, `open`, `status`, `close`, `close`, `open`, `quit`.
9. **DPI**, only with real variation available. With two monitors at different scales, drag a window between them, slowly and quickly. Otherwise change the display scale in Settings while the window is open. Run `status` in resident mode to read `first_surface`; check that text stays sharp and sized correctly. **If scaling cannot change on this host, for example over Remote Desktop, record DPI transitions as untested.** Injected tests are synthetic and do not count as native coverage.
10. **Lock screen.** Start a logged session, and watch the meter animate for at least 10 seconds:

    ```powershell
    $dir = "evidence\synthetic-mixer\windows\run\session"
    New-Item -ItemType Directory -Force $dir | Out-Null
    $p = Start-Process "$env:TEMP\dfx2-mixer.exe" -ArgumentList "-script session" -NoNewWindow -Wait -PassThru -RedirectStandardOutput "$dir\lock.jsonl" -RedirectStandardError "$dir\lock.stderr.log"
    "exit code $($p.ExitCode)"
    ```

    Note the clock time, press Win+L, stay locked for at least 60 seconds, unlock, and note the time again. If the window is still open, confirm the meter is moving and shows no stale or black content, wait 10 seconds, then close the window to end the script.
11. **Remote Desktop disconnect and reconnect.** In a Remote Desktop session to this host, start the same command with `rdp.jsonl` and `rdp.stderr.log` as the outputs, building again first if `$env:TEMP` differs in that session. Note the time, disconnect by closing the client window rather than signing out, stay away for at least 60 seconds, reconnect, and note the time again. Then check and end it as in step 10. Reconnect once more at a different client resolution if you can, and record the `first_surface` it reports. If this host cannot do this, **record the transition as untested**; a lock performed from inside a Remote Desktop session is not a substitute.

Steps 10 and 11 have two acceptable outcomes, and neither is a reason to change code on this host:

- **The window survives.** `return` shows `<nil>` and `paint_failures` is zero. Report paints per second before, during and after the transition from the `session-status` lines. If paints stopped while the session was away, say so: the blit path was not exercised during that time, and that limits what the run shows.
- **The application fails.** The window closes, the exit code is 1, and `return` carries a `Win32 paint:` error. Do not add an exception or filter it out. Return the error text, the times and the logs. Michael decides from that evidence whether to define a narrow exception.

Anything else, such as a window left black without a failure or an application that never finishes draining, is a finding.

## What to return

Commit nothing from an agent session. Add `evidence/synthetic-mixer/windows/results.md` beside this file, with:

- Host: Windows edition and build, Go version, local console or Remote Desktop, display adapter, resolution, and the "Show window contents while dragging" setting (`driver-host` in any modal log).
- Revision tested, and whether the worktree was clean apart from `run/`.
- The matrix result, each case, and any runner correction with its reason.
- From `modal-move` and `modal-size`: seconds in the loop, frames and title updates per second for each window, and modal turns, timers and refusals. These are observations, not promises.
- Fonts: the `font-coverage` lines, and what the fallback line looks like on screen.
- DPI: every scale actually seen in `first_surface`, every real transition performed, and everything untested.
- Lock screen and Remote Desktop: the lock, disconnect and reconnect times, how each session started and ended, paints per second before, during and after, the `return` error text and exit code, what the window showed afterward, and every transition left untested.
- Each manual step's result, in plain words, including anything that looked wrong.

Keep the whole `run\` directory, and never overwrite an earlier run: pass `-OutDir evidence/synthetic-mixer/windows/run-2` for a second one.

Then Michael commits and returns the tree to Linux, where the final checks and review run. Update `docs/current/runtime.md`'s modal-loop paragraph **only** on that return, once these results establish the behavior.
