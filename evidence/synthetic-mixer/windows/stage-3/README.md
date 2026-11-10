# Synthetic mixer stage 3: Windows handoff

Status: **The paragraph-key candidate `c1ad2bb` passed Windows validation; its return is recorded in [paragraph-key-results.md](paragraph-key-results.md).** The instructions below describe that completed audition. Prior results remain in [results.md](results.md) and [return-results.md](return-results.md). The [stage 3 record](../../stage-3.md) distinguishes those runs from current preparation. Stage 3 still awaits final review and Michael's acceptance.

**Candidate.** Run Michael's next commit containing the paragraph-key ownership and accounting fix accepted from Terminus `ce97d67355f5`. Its Linux mixer SHA-256 is `5c7a4efc9611c938c551fec6327af529cab2d6cfc8e86b3d7da15f0faf978076`; native evidence is in [`linux/stage-3/paragraph-key-rerun/`](../../linux/stage-3/paragraph-key-rerun/). Windows binaries hash differently: compare revisions. Confirm that `internal/text/key_test.go` contains `TestParagraphKeyOwnsOnlyItsSubstringAndSurvivesCacheHits`, and that admission in `internal/text/system.go` clones the key and charges its UTF-8 bytes. The flex fix must also remain present. Record the actual revision; do not reuse the earlier candidate's result.

Run the original regression matrix with every gate enabled and the controls matrix, writing new evidence under `paragraph-key-run/` as below. The regression runner's full suite includes the new text tests. Also run `go test ./internal/text/... -count=100` with `CGO_ENABLED=0`, retaining the command, output and exit code. Keep prior runs untouched. Manual audition results remain recorded; this cache-only correction does not request a repeat of the full manual checklist. Report any new visual or native failure instead of weakening checks.

You do not need the grimoire or Terminus on the Windows host. Everything you need is in this repository. Do not commit from an agent session; Michael commits and transfers the work.

## What this proves

Stage 3 adds rows, columns, scopes, faders and checkboxes. Only the input path differs by platform: `internal/platform/win32/input_amd64.go` copies native messages into queued events, and never runs the view. It takes native capture on a left press, so a drag keeps tracking outside the window.

The original Windows audition tested these platform assumptions; its results and remaining limits are in [results.md](results.md):

1. That `SetCapture`, `ReleaseCapture` and `WM_CAPTURECHANGED` behave as the model assumes, in that order. In particular, a real drag must continue outside the window, and the backend's own release must not read as a capture loss.
2. That signed client coordinates arrive negative left of and above a window under capture.
3. That Tab, the arrows, Page Up, Page Down, Home, End and Space arrive as `WM_KEYDOWN` and `WM_KEYUP` without a dialog manager in the way, and that other keys keep their native meaning.
4. That `WM_KILLFOCUS` fires when another window is activated during a drag, and that releasing capture from it is safe.
5. That a client drag and a title-bar size/move loop coexist: pressing a title bar has to end any capture cleanly, and the size/move loop's own capture must survive a gesture canceled during a modal turn.
6. That logical coordinates map correctly at a real DPI other than 100%, if this host has one.

If input cannot meet the contracts below on a real host, report that as evidence for a design revision. Do not weaken the checks or the runner.

## Prerequisites

The same as [stage 2](../README.md): Windows 10 or later, amd64, Go 1.26 or newer on `PATH`, `git`, `CGO_ENABLED=0`, a clean checkout of the transferred revision, and **a connected, unlocked, interactive desktop**. Over Remote Desktop, keep the client connected and not minimized for the whole run, and turn off the automatic screen lock. **Hands off the mouse and keyboard during the automated runs.** Both drivers send real input to the desktop, and the controls driver sends keystrokes to the window it activates. Run PowerShell on a non-elevated desktop, so input injection is not blocked by an elevated window.

## Automated runs

From the repository root in Windows PowerShell, run the regression matrix first and then the controls:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\audition.ps1 -OutDir evidence/synthetic-mixer/windows/stage-3/paragraph-key-run/matrix
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\stage-3\controls.ps1 -OutDir evidence/synthetic-mixer/windows/stage-3/paragraph-key-run/controls
```

The first is stage 2's matrix, unchanged: tests, vet and builds, every presentation and size/move case, and the foundation's lifecycle matrix, into `paragraph-key-run\matrix\`. It takes several minutes. Its [case table](../README.md#automated-matrix) says what each must show. It needs repeating on this candidate because shared text ownership and accounting changed.

The second builds `examples/mixer` and `controlsdrive` and writes `paragraph-key-run\controls\`: `summary.json`, `mixer.jsonl`, `mixer.stderr.log`, `revision.txt`, and the retained images `controls-before.png` and `controls-focused.png`. If a case dies mid-drag the runner releases the button; by hand, `& "$env:TEMP\dfx2-mixer-audition\controlsdrive.exe" -release`.

The driver runs `mixer -controls -paused`, finds both windows, and for each case presses and types through `SendInput` and `SetCursorPos`, sends model changes through the fixture's console, and judges the fixture's `edit` log:

| Case | What must hold |
| --- | --- |
| `drag_outside_window` | A press at the current value edits nothing. Motion edits follow the pointer. Above and outside the window the value clamps to 1, and below it to 0, while capture holds. A release outside at the last value adds no duplicate edit, and later motion edits nothing. |
| `checkbox` | A press edits nothing. A release inside toggles once. A release after dragging away toggles nothing. |
| `keyboard` | Arrow, page and end keys from 0.5 give exactly 0.51, 0.52, 0.53, 0.52, 0.51, 0.61, 0.51, 0, 1, with a repeat at a limit adding nothing. Tab and Space toggle mute, on release only. Tab and Up edit the next strip's fader. Shift+Tab goes back. A letter key edits nothing. |
| `independent_focus` | Each window keeps its own focused control: keys alternate B/c, A/a, B/c as the windows are activated. |
| `external_during_drag` | Model changes through the console, with and without a drag in progress, produce no edit. The gesture keeps its value and the pointer still edits. |
| `reorder_during_drag` | Reversing the strips mid-drag leaves every edit on channel a. After reordering back, a fresh press targets a. |
| `remove_during_drag` | Removing the channel mid-drag stops its edits, with no final edit at release. Reinserting does not inherit the gesture, and the fader works after a fresh press. |
| `focus_loss_during_drag` | Activating the other window mid-drag ends the gesture: later motion and the release edit nothing, and a fresh press works. |
| `hover_is_idle` | Moving the pointer with no button and pressing a letter cause zero frames, and three quiet seconds show zero frames, deadline timers, presents and evaluations. |
| `close_during_drag` | Closing the dragged window ends it cleanly, and the other window keeps responding. |

Then `quit` must return `<nil>` with exit 0, a `drained` record of zero windows, tasks, queued callbacks, completions, queued input, native windows, retired records, dispatch targets and paint failures, and an empty stderr.

`controlsdrive` passed on the earlier Windows candidate; the current candidate has not run there yet. If the runner itself is wrong, correct it and record exactly what changed and why. Do not change the Go runtime, backend or example to make a case pass without reporting it: that is a finding.

## Manual audition

Build once, then run from a normal terminal:

```powershell
$env:CGO_ENABLED = "0"
go build -o "$env:TEMP\dfx2-mixer.exe" .\examples\mixer
& "$env:TEMP\dfx2-mixer.exe" -controls
```

The feed is live, so meters move. Record what you see, in plain words:

1. **Look at it.** Three strips in each window, the text crisp and aligned, the Mute boxes square and the focus ring outside them.
2. **Drag a fader by hand.** It should follow the pointer exactly and keep tracking outside the window, including past each screen edge. Release outside the window. Note any lag or flicker.
3. **Keyboard only.** Without touching the mouse: Tab to a fader, then arrows, Page Up, Page Down, Home, End; Tab to Mute, then Space. Hold an arrow to check auto-repeat, and hold Space to check it toggles once, at release. Shift+Tab backward through the wrap. Does the Tab key ever leave the window?
4. **Two windows.** Focus a control in each window with the mouse. Alt+Tab between them, and check that each keeps its own focus and shows the ring dimmed while inactive, and that keys only affect the active window's control. Edit one window and watch the other follow.
5. **Alt+Tab during a drag.** Hold a fader and Alt+Tab away. The gesture should end with no extra edit, and nothing should stay pressed when you return. Release in the other window.
6. **Title bar and controls.** Drag a title bar while the meters move, then immediately operate a fader. Start a fader drag and press Alt+F4 or close the other window with the keyboard, if your host passes it through.
7. **External changes.** Type `gain a 0.2`, `mute a on`, `reorder`, `order a b c`, `remove a` and `insert a` in the console. Try the last ones during a drag on channel a. Nothing should print `edit`, and the view should follow at once.
8. **Resize.** Shrink a window until strips clip: nothing should paint outside its strip, and a clipped control should take no press. Grow it back.
9. **DPI**, only with real variation available. At a scale other than 100%, repeat step 2, checking the thumb stays under the pointer. Move a window between monitors at different scales, if you can. **If scaling cannot change on this host, for example over Remote Desktop, record DPI as untested.** Injected tests are synthetic and do not count as native coverage.

## What to return

Commit nothing from an agent session. Add `evidence/synthetic-mixer/windows/stage-3/results.md` beside this file, with:

- Host: Windows edition and build, Go version, local console or Remote Desktop, display adapter, resolution and DPI.
- Revision tested, and whether the worktree was clean apart from `run/`.
- Both automated results, each case, and any runner correction with its reason.
- Each manual step's result, in plain words, including anything that looked wrong.
- DPI: every scale actually seen in `first_surface`, every real transition performed, and everything untested.

Keep the whole `run\` directory, and never overwrite an earlier run: pass a new `-OutDir` to each script for a second one.

Then Michael commits and returns the tree to Linux, where the final checks and review run.
