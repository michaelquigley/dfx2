# Synthetic mixer stage 4: Windows handoff

Status: **executed on Windows at `34852d5`, with automated and operator-reported manual passes.** See [results.md](results.md) for retained evidence and the CPU-measurement limitation. The instructions below describe that audition. Subsequent portable checkbox-release and fader-press deferral corrections (`TestDeferredCheckboxReleaseKeepsValidatedTarget` and `TestDeferredFaderPressKeepsItsTarget`/`KeepsItsResolvedValue`) have not executed on Windows yet; the retained results describe `34852d5`. Cross-builds and modeled tests on Linux do not count as Windows execution. The [stage 4 record](../../stage-4.md) holds the Linux results this handoff is paired with.

**Candidate.** Run Michael's next commit containing stage 4: the finished `examples/mixer -controls`, with six channels, the ordered command worker, its fixtures, and this directory. Confirm that `examples/mixer/commands.go` and `examples/mixer/mixer_test.go` exist, that `examples/mixer/main.go` opens controls windows at 760×520, and that `internal/appcore/content_test.go` contains `TestASelectedWindowMadeIneligibleMidTurnDoesNotRender`: the candidate includes a shared-core correction from Terminus `d00a189682b1`, which the gates and the regression matrix below exercise on this host. Record the actual revision. Windows binaries hash differently from Linux ones: compare revisions, not hashes. Go stamps `vcs.modified` as soon as `run\` exists, so a build's hash changes after the first run.

You do not need the grimoire or Terminus on the Windows host. Everything you need is in this repository. Do not commit from an agent session; Michael commits and transfers the work. `unfurl` was not available on earlier Windows hosts; if it is still missing, say so in the results rather than leaving Markdown unformatted silently.

## What this proves

Stage 4 adds no framework or backend behavior. It finishes the example as an application that exercises everything built so far: two windows of six channel strips over one model, a 30 Hz producer and an ordered command worker that the application owns, snapshot publication followed by invalidation, a 64-command FIFO that refuses visibly when full, and the example's own pending-value policy across a delayed, rejecting synthetic device. See the example's [README](../../../../examples/mixer/README.md).

This host has to show natively:

1. Delayed, rejected and overloaded commands: edits show at once as pending values, keep their order, and drawing stays responsive while the device lags. A rejection reconciles to the device's value and is shown, without extra commands. With the device held, 64 commands queue behind the one it holds and the rest are refused at once, visibly.
2. External changes while dragging and while a command is pending: none echoes as an edit or a command.
3. Reorder and removal during capture, keyboard-only operation and focus in two windows, all re-run with six strips.
4. Closing either window, A as well as B, leaves the other window, the producer and the worker running. Shutdown with five commands outstanding cancels all five, reports none as applied, and returns cleanly.
5. Idle after the feed, the commands and the meters settle: zero frames, evaluations, presents and new deadline timers.
6. Text that changes with every publication forces eviction under a small budget; cache counters stay within their budgets; a window that has evicted renders exactly what a freshly opened window renders.
7. The existing presentation, size/move and foundation lifetime matrices still pass with every gate enabled, because the example and the shared text system run in their context.
8. Timing observations: frame rate, view evaluations and process CPU while the feed runs, with text churn, and once settled. These are observations for the record, not pass criteria.

If anything here cannot meet the cases below on a real host, report that as evidence. Do not weaken the checks or the runner.

## Prerequisites

The same as [stage 3](../stage-3/README.md#prerequisites): Windows 10 or later, amd64, Go 1.26 or newer on `PATH`, `git`, `CGO_ENABLED=0`, a clean checkout of the transferred revision, and **a connected, unlocked, interactive desktop**. Over Remote Desktop, keep the client connected and not minimized, and turn off the automatic screen lock. **Hands off the mouse and keyboard during the automated runs.** Run PowerShell on a non-elevated desktop. Two 760-wide windows sit side by side 40 pixels apart from x=40, so a desktop at least about 1640 pixels wide at 100% keeps window B fully on screen; record the resolution either way.

## Automated run

From the repository root in Windows PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\stage-4\stage4.ps1 -OutDir evidence/synthetic-mixer/windows/stage-4/run
```

It runs, in order, stopping at the first failure:

1. **Gates**, into `run\gates\`: the example's fixtures 100 times, `./ui` 50 times, the text packages 50 times, and vet of the stage 4 driver. Each log starts with its command and exit code.
2. **The stage 2 regression matrix, unchanged**, into `run\matrix\`: its full suite, verbose Win32 tests, 50-repetition appcore/Win32 gate, vet and builds, all 17 presentation and size/move cases, and the nested foundation lifecycle matrix with its own test gate. Its [case table](../README.md#automated-matrix) says what each must show. It takes several minutes.
3. **The stage 4 audition**, into `run\controls\`: `controlsdrive` runs `mixer -controls -paused`, finds both windows, drives them through `SendInput` and the console, and judges the `edit` and `command` logs. Then it runs two separate text-cache processes, into `run\controls\text-small\` and `run\controls\text-default\`.

If a case dies mid-drag the runner releases the button; by hand, `& "$env:TEMP\dfx2-mixer-audition\controlsdrive4.exe" -release`.

| Case | What must hold |
| --- | --- |
| `drag_outside_window` … `hover_is_idle` | The nine stage 3 cases, as in the [stage 3 table](../stage-3/README.md#automated-runs), now over six strips. `remove_during_drag` reinserts channel a as the sixth strip. |
| `delayed_commands` | At a 700 ms device delay, three Up presses edit 0.51, 0.52, 0.53 at once, as pending values. While they wait, a mute toggle edits immediately and an external change renders. They apply in order, each after at least 700 ms, the third after at least 2 s; the device ends at 0.53, muted, nothing pending. |
| `rejected_commands` | With the device told to reject one command, two Up presses give a rejected 0.54 and an applied 0.55. Rejecting the next, Up gives 0.56, rejected: the device stays at 0.55, nothing is pending, the error names 0.56, and a quiet half second adds no edit or command. |
| `overloaded_commands` | At a 60 s delay, 70 alternating Up/Down presses give 70 edits: 64 or 65 accepted (65 once the worker holds the first), the rest refused as `queue full`, which are exactly the newest edits and are shown. Setting the delay to zero applies every accepted command in sequence; the device ends at the last accepted value. |
| `external_while_pending` | At an 800 ms delay, one Up press is pending when an external `gain d 0.2` lands: the device shows 0.2 with the edit still pending. After resolution the device holds the edit's value, with one edit and one command in all. |
| `timing` | The feed runs 5 s (at least 120 producer ticks), then 5 s with 36 words of churn; then, paused and settled, 3 s show zero frames, evaluations and presents, and a further second no new deadline timer. Rates and CPU percentages are recorded, not judged. |
| `close_first_window` | `open` adds window C, then `close A`: two windows remain, application tasks survive, and presses in B and in C each edit and apply. |
| `close_during_drag` | Window B closed by name mid-drag ends cleanly, and window C keeps responding. |
| `shutdown_outstanding` | At a 60 s delay, five Up presses in C are outstanding. `quit` cancels exactly those five, applies none, and `return` reports balanced totals. |

The run must end with `return` `<nil>`, exit 0, a `drained` record of zero windows, tasks, queued callbacks, completions, queued input, native windows, retired records, dispatch targets and paint failures, `commands_conserved` true, and an empty `mixer.stderr.log`.

Each text-cache run starts the mixer with 36 churn words, runs the feed 4 s while sampling cache counters twice a second, pauses and settles, captures window A, opens a fresh window C, and snapshots A and C. It passes when every sample stays within the budget, A's retained image equals C's pixel for pixel, and A has evicted paragraphs and, under the small budget, glyphs. The small budget is 48 KiB of glyph pixels times the square of the display scale, 64 KiB of paragraphs and 48 entries: it has to sit above a fresh window's working set and below every glyph the churn alphabet can produce, or nothing is evictable. The driver reads the scale from the system DPI; record it. At an unusual scale the bracket may not hold; report the counters rather than changing the budget.

`controlsdrive` has never run; it extends the stage 3 driver, which passed on this host. If the driver itself is wrong, correct it and record exactly what changed and why. Do not change the Go runtime, backend or example to make a case pass without reporting it: that is a finding.

Screen captures (`screen-*.png`) show what reached the display; retained snapshots (`delayed-pending.png`, `rejected-error.png`, `overload-error.png`, `timing-churn.png`, `controls-*.png`, `text-*-A.png`, `text-*-C.png`) show what the framework handed to GDI. Look at the screen captures and say whether the text is crisp and complete, and whether the error and pending lines are visible. A capture of a window partly off screen or covered is a limitation to record, not a pass.

## Manual audition

**Required.** The delayed, rejected and pending-edit behavior is new in this stage, so the earlier stages' manual passes do not cover it. Build once, then run from a normal terminal:

```powershell
$env:CGO_ENABLED = "0"
go build -o "$env:TEMP\dfx2-mixer.exe" .\examples\mixer
& "$env:TEMP\dfx2-mixer.exe" -controls
```

The feed is live, so meters and dB numbers move. Record what you see, in plain words:

1. **Look at it.** Six strips per window: a fader, Mute, a meter, the name, a dB number, the device's value and `in sync`. Then three status lines. Text crisp, nothing clipped at this size.
2. **Delay.** Type `delay 1s`. Press Up a few times on a focused fader: the fader moves at once, the strip reads `pending …`, the other window shows the same pending value, and the device value catches up about once a second. The window stays smooth throughout. Drag a fader briefly during the delay and release: it should stay where you released it, not jump back. A drag sends one command per frame, so a long one fills the queue: then `Last error` reads `queue full`, and the fader settles on the last accepted value. That is the specified behavior, not a fault.
3. **Rejection.** Type `reject 1`, then press Up once. When it resolves, the fader returns to the device's value and `Last error` names the rejected command. Nothing else changes.
4. **Overload.** Type `delay 30s`, then hold Up or drag a fader up and down for a few seconds. `Last error` should read `queue full` and `refused` should count up. Type `delay 0s`: the backlog applies in order and the strip ends `in sync`.
5. **External changes and a drag.** Type `delay 0s`. Hold a fader on channel a, and type `gain a 0.2`, `reorder`, `remove a`, `insert a`, `order a b c d e f`. Nothing should print `edit`; the dragged fader keeps your value until release or until its channel is removed.
6. **Close either window.** Close A with its close button: B keeps working and the meters keep moving. Type `open`, then close the new window. Closing the last window ends the program; the final `return` line should show `"commands_conserved":true`.
7. **Shutdown with work outstanding.** Run it again, type `delay 30s`, press Up a few times, then close both windows or type `quit`. The `command` lines should show those commands `canceled`, none `applied`.
8. **DPI**, only with real variation available, as in stage 3. **If scaling cannot change on this host, record DPI as untested.**

## What to return

Commit nothing from an agent session. Add `evidence/synthetic-mixer/windows/stage-4/results.md` beside this file, with:

- Host: Windows edition and build, Go version, local console or Remote Desktop, display adapter, resolution and DPI.
- Revision tested, and whether the worktree was clean apart from `run/`.
- Each gate's result, the regression matrix result with the foundation matrix, each stage 4 case, both text-cache results with their counters, the timing observations, and any runner correction with its reason.
- What the screen captures show, including any off-screen or covered limitation.
- Each manual step's result, in plain words, including anything that looked wrong.
- DPI: every scale actually seen, every real transition performed, and everything untested.

Keep the whole `run\` directory, and never overwrite an earlier run: pass a new `-OutDir` for a second one. `chmod 644` new files if the host's git marks them executable (see the 2026-11-03 journal). Add a short entry to the day's file in `docs/journal/`.

Then Michael commits and returns the tree to Linux, where return checks and review run.
