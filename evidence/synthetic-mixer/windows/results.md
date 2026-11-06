# Synthetic mixer stage 2: Windows results

Status: **two automated matrices executed on this host on 2026-11-06. The first failed only the driver's screen capture in `large`; after one runner correction the second passed every case, including the foundation matrix. The lock-screen and Remote Desktop transitions were performed and the window survived both. Michael's manual audition passed, with a small resize flicker and an ignored Alt+F4 during a drag recorded. DPI is untested on this host.** Nothing here declares stage 2 accepted, starts stage 3, or changes `docs/current/runtime.md`; those wait on the Linux return checks and Michael's review. Nothing is committed from this session.

Every line below says whether it is a **native observation** (this host ran it) or **modeled** (Linux injected tests, cross-builds). The [handoff](README.md) lists the matrix; the [stage 2 record](../stage-2.md) has the Linux preparation and its open items, which this file does not close.

## Host

| Item | Value |
| --- | --- |
| Windows | Microsoft Windows 10 Pro 10.0.19045, 64-bit |
| Go | go1.27.3 windows/amd64, `CGO_ENABLED=0` throughout |
| PowerShell | Windows PowerShell 5.1.19041.6456, 64-bit, not elevated |
| Session | Remote Desktop. `query session` at the start: `rdp-tcp#4`, session 2, active. The runner's `host.json` records `RDP-Tcp#0` because `$env:SESSIONNAME` in the agent's shell predates a client reconnect; the shell runs in session 2 either way |
| Display | Microsoft Remote Display Adapter, 3440×1440, 32 bpp, one screen; the listed Basic Display Adapter at 1024×768 is not the session's display |
| Show window contents while dragging | on (`driver-host` in every modal log: `show_contents_while_dragging: true`) |
| Screen lock | no screensaver executable and no inactivity-lock policy; display idle timeout 10 minutes on AC, which does not lock |
| Fonts | `sans-serif` resolves to Verdana here (see fonts below) |

Revision tested: `c9f23578e634983ddccff6f34e5a4c7975a53f9e`, local `main` equal to the transferred commit. The worktree was clean apart from `run/` when the runner recorded it (`host.json`: `?? evidence/synthetic-mixer/windows/run/`). The runner correction below and the files of this session came afterwards and are uncommitted.

## Preflight (native)

Before the desktop-dependent matrix, from the same revision: `go test ./... -count=1` passed for all ten packages with tests, `go vet ./...` passed, and `mixer`, `desk` (with `go vet`) and `ctrlbreak` built. The runner repeated the tests, the Win32 package verbosely and `internal/appcore` plus `internal/platform/win32` at `-count=50`, all passing; its logs are `run/tests.log`, `run/tests-win32-verbose.log`, `run/tests-repeated.log` and `run/vet.log`.

## First automated matrix: `run/` (native)

Run on 2026-11-06 from 13:47:44 to 13:50:58 local with the mouse untouched and the Remote Desktop client connected and unlocked throughout. Started as:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\audition.ps1
```

| Case | Result | Native observation |
| --- | --- | --- |
| `fonts` | pass | seven coverage lines, capture 0 black, 0.87 background |
| `animate` | pass | 60 frames in 2 s; after the pause and settle, 5 s with 0 frames, timers, wakes, dispatches, presents; capture 0 black, 0.87 background |
| `resize` | pass | 760×320, 380×240, 980×420, 560×260 each reached exactly at scale 1, four retained snapshots; quiet afterward |
| `minimize` | pass | minimized: `visible=false`, frames held at 28 and deadline timers at 2 across the 1.5 s check; restored: `visible=true`, 77 frames |
| `expose` | pass | 5 invalidations, 5 paints, 0 frames, 0 presents; capture 0 black |
| `large` | **fail: driver capture only** | surfaces 984×761, 1240×900, 1600×1000, 2400×1300 and 3200×1400 all reached exactly as requested (none clamped on the 3440×1440 screen), 2,995,296 to 17,920,000 bytes, never shrinking; all five retained snapshots matched their surfaces; `paint_failures` 0; quiet afterward. The driver's screen capture failed on four of five steps: `GetDIBits: The operation completed successfully.` (zero lines, no error); the one capture that worked, 1600×1000, was 0 black and 0.98 background. See the correction below |
| `close-sample` | pass | 510 invalidations accepted before the close, 292 refused after it |
| `reopen` | pass | five open/close cycles in resident mode |
| `two` | pass | 37 frames on B in the second after A closed |
| `modal-move` | pass | see below |
| `modal-size` | pass | see below |
| `modal-idle` | pass | see below |
| `modal-close-self` | pass | see below |
| `modal-close-other` | pass | see below |
| `modal-quit` | pass | see below |
| `signal` | pass | three `update` records per window with strictly rising `applied` and `frames` (A: 1/2, 2/4, 3/7; B: 1/1, 2/3, 3/6), then the console control event: `return` is `context canceled`, drained |
| `inspection` | pass | 31 loaded modules, all system DLLs plus `mixer.exe`; no WebView2, Edge or Chromium module; no child processes. `combase.dll`, `OLEAUT32.dll`, `MSCTF.dll` and the text-input DLLs are present, as in the foundation record, through `user32` |
| foundation matrix | pass | all nine lifecycle cases in `run/foundation/`, exit 0 |

Every passing case ended with `return` nil (or `context canceled` for `signal`), an empty stderr, and a final `drained` record with zero windows, tasks, queued callbacks, native completions, native windows, retired records, dispatch targets and paint failures. `summary.json` lists the one failed case; the runner threw at its end as designed.

### Modal loop progress (native)

These are observations on this host, not promises. The driver dragged window A by its caption (`modal-move`) or bottom-right corner (`modal-size`) for 13 s with the pointer moving every 16 ms; the fixture samples whole seconds while `ModalActive` holds.

| Case | Whole seconds in the loop | Frames per second, A | Frames per second, B | Title updates per second, A / B | Modal turns | Modal timers | Refusals | `ErrBusy` posts |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `modal-move` | 12 | 29–31 (363 total) | 29–31 (363 total) | 10–11 / 10–11 (121 each) | 410 | 409 | 0 | 0 |
| `modal-size` | 13 | 34–45 (518 total) | 29–33 (397 total) | 10 / 10 (130 each) | 531 | 531 | 0 | 0 |

In every sampled second both windows rendered and both title counters advanced. Both loops ended when the driver released, `ModalActive` cleared within the 10 s bound, and the settled 5 s interval was all zeros. The 30 Hz feed is the frame rate's ceiling here, so the ~30 frames per second says the loop kept pace with the sampler, not what the loop could sustain; during the size drag A rendered extra frames for its size changes. One timer per turn: the modal timer and modal turn counts match to within one.

`modal-idle` (18 s hold: press, one nudge, then no input): with producers paused and meters settled inside the loop, 3 s showed 0 frames, modal timers, modal turns, wakes and dispatches while `ModalActive` stayed true. A posted callback then ran inside the loop with 17 ms latency, and resuming the feed rendered 30 frames in the next second, still inside the loop. The whole hold used 33 modal turns.

`modal-close-self`: A, the dragged window, was closed from a worker 3 s into the drag. Its `Done` closed 16 ms after `Close`, the loop was no longer active at that point, the driver saw the window gone and stopped (`window_alive: false`), and B rendered 34 frames in the following second. Drained with zero native windows and no paint failure.

`modal-close-other`: B was closed 3 s into the drag of A. Its `Done` closed in 33 ms **while the loop was still active** (`modal_active_at_done: true`), A rendered 29 frames in the following second, the drag ran its full 8 s, then everything settled and drained. One invalidation was refused after the close, the expected race with the sampler.

`modal-quit`: `Quit` 2 s into the drag, inside the loop: `return` nil, both windows closed, drained.

### What this establishes about the modeled assumptions

The handoff names four things only this host could establish:

1. **The real size/move loop dispatches `WM_APP` and `WM_TIMER` to the message-only window.** Native: yes. Hundreds of modal turns ran per loop, with posted title updates applied and frames presented every second while `DefWindowProc` held the UI thread.
2. **Destroying the loop's own window from a timer inside it.** Native: `modal-close-self` ended the loop, acknowledged destruction, and drained; no hang, no stray `WM_EXITSIZEMOVE` handling problem surfaced. Whether `WM_EXITSIZEMOVE` arrives is not logged, so the exact message sequence remains unobserved; the behavior the backend depends on held.
3. **Frames at the spike's black-window sizes present from `VirtualAlloc` storage.** Native: all five sizes presented with zero paint failures and matching retained snapshots on both runs, and on the second run every size was screen-captured with no black inside the window (the second matrix below).
4. **Locking the screen and disconnecting Remote Desktop.** Native: both performed; the window survived both with paints continuing at about 39 per second and zero paint failures (the section below).

## Runner correction: `desk` capture destination

**What failed.** `desk`'s `capture` copied the client area into a GDI bitmap, then `GetDIBits` into a Go `[]byte`. On four of five `large` steps `GetDIBits` returned zero lines with `GetLastError` 0.

**Diagnosis**, in [large-capture-diagnosis/](large-capture-diagnosis/): a probe repeating that sequence from the screen DC with no window and no input reproduced it. With the destination on the Go heap, 15 to 17 of 24 attempts failed; touching every page first, reading 64 rows per call, `runtime.GC()` before allocating, and retrying did not help. With the destination from one `VirtualAlloc`, 0 of 24 failed. `VirtualQuery` over each Go buffer showed that **every failing buffer spanned two `VirtualAlloc` reservations (Go's Windows heap arenas, 4 MiB apart) and every succeeding buffer spanned one**, 31 of 31 attempts in two runs. This is the failure the text spike recorded for `SetDIBitsToDevice` (`docs/future/text.md`, "Go-heap frames fail in `SetDIBitsToDevice`"), seen from the reading side, and the reason the backend keeps each window's frame in one `VirtualAlloc` allocation. The kernel-side rule remains an inference from the address correlation, as it was for the spike.

**Change.** `desk/main.go`'s `capture` now allocates its `GetDIBits` destination with `VirtualAlloc(MEM_COMMIT|MEM_RESERVE, PAGE_READWRITE)` and releases it with `VirtualFree(MEM_RELEASE)` after copying into the PNG image; nothing else in the driver changed, and `go vet` passes on it. No Go runtime, backend or example code changed. No check was weakened: the `large` case still requires five captures and judges them as before.

**Verification.** A hand rerun of `large` alone with the corrected driver at 13:57 is **not** evidence: Michael's Remote Desktop session had disconnected, so every `BitBlt` from the screen failed with an invalid handle ([large-rerun-disconnected/](large-capture-diagnosis/large-rerun-disconnected/)). The second full matrix below is the verification: all five captures succeeded.

## Second automated matrix: `run-2/` (native)

Run on 2026-11-06 from 15:39:41 to 15:42:41 local, after Michael reconnected (`query session`: `rdp-tcp#5`, session 2, active) and with the mouse untouched, from the same revision with the corrected driver:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\audition.ps1 -OutDir evidence/synthetic-mixer/windows/run-2
```

**All seventeen cases and the foundation matrix passed; runner exit 0.** `host.json` records the worktree as the first run's output, the correction, the diagnosis and this file, all uncommitted. Every case drained to zeros with empty stderr, and the inspection again found 31 system modules, no browser module and no child process.

`large` now has all five screen captures, each matching its retained snapshot's size:

| Step | Surface | Bytes | Capture black | Capture background |
| --- | --- | --- | --- | --- |
| 0 | 984×761 | 2,995,296 | 0 | 0.97 |
| 1 | 1240×900 | 4,464,000 | 0 | 0.98 |
| 2 | 1600×1000 | 6,400,000 | 0 | 0.98 |
| 3 | 2400×1300 | 12,480,000 | 0 | 0.99 |
| 4 | 3200×1400 | 17,920,000 | 0.049 | 0.92 |

The 3200×1400 window's client area extends below the 1440-pixel screen, so its capture's bottom holds the taskbar and, below that, the black outside the screen (`captures/large-4-3200x1400.png`); the window content above it is complete, with the text and meter at the top. That black is the screen's edge, not the blit. 984×761 is the text spike's recorded black-window size; here it presented and captured with no black at all.

Modal progress on the second run, the same shape as the first:

| Case | Whole seconds | Frames per second, A | Frames per second, B | Titles per second, A / B | Turns | Timers | Refusals |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `modal-move` | 12 | 29–31 (363) | 29–31 (362) | 10–11 / 10–11 (121 each) | 412 | 412 | 0 |
| `modal-size` | 12 | 33–48 (503) | 30–32 (372) | 10 / 10 (120 each) | 566 | 567 | 0 |

`modal-idle`: 3 s of zeros inside the live loop, a post ran inside it in 13 ms, resuming rendered 29 frames in a second, 33 turns for the whole hold. `modal-close-self`: `Done` in 12 ms, loop gone, B rendered 34 frames in the next second. `modal-close-other`: `Done` in 21 ms with the loop still active, A rendered 30 frames in the next second. `modal-quit`: drained, `return` nil. `signal`: three rising updates per window (A: 1/2, 2/3, 3/6; B: 1/1, 2/2, 3/5), then `context canceled`.

Other cases on the second run: `resize` surfaces 760×320, 380×240, 980×420, 560×260; `minimize` held frames at 28 and timers at 2 while `visible=false`, restored to 77 frames; `expose` 5 paints, 0 frames, 0 presents; `close-sample` 511 accepted, 292 refused after close; `two` 37 frames after the close; `animate` quiet all zeros.

## Fonts (native)

`font-coverage` on both runs:

| Sample | Face and glyph format |
| --- | --- |
| Latin, Greek, Cyrillic | Verdana, outlines |
| Hebrew, Arabic | Arial, outlines |
| Japanese | MS Gothic, outlines |
| Emoji | Segoe UI Emoji, `colr paint (placeholder)` and `empty outline` |

So `sans-serif` resolves to Verdana on this host rather than Segoe UI. In the screen capture `run-2/captures/fonts.png` (560×260, what the display showed): the text is crisp and in place; the fallback line reads "Fallback: Ελληνικά, Кириллица, العربية, עברית, 日本語, emoji ☐☐☐ — mixed שלום world direction." with the Arabic in connected forms and the Hebrew and Arabic words laid right to left, the Japanese drawn, the mixed-direction "שלום" inside the Latin phrase, and the three emoji as hollow boxes with no blank gap, the known COLR limitation, not a failure. This is the capture's content; Michael's own look at the window is the manual step.

## Manual audition (native, Michael's observations)

Steps 10 and 11 are recorded above. Steps 1 to 9 were performed by Michael on 2026-11-06 from a normal terminal with the same `dfx2-mixer.exe`, over the same Remote Desktop session. His report, in his words, with the step it answers:

1. Look at it: "looks good."
2. Drag by hand, move and size: "there's a small amount of flicker while resizing, but otherwise everything is working great." He did not paste the `modal-summary` lines; the scripted equivalents are in `run/` and `run-2/`. The flicker while growing is the erase of the not-yet-covered area, which the handoff said to note; it is recorded, not judged here.
3. Hold still on the title bar: "holding the title bar causes a <1s pause, but then the feed continues." So a press without movement stalls briefly before `WM_ENTERSIZEMOVE`, and modal delivery begins once the loop is established; this is the behavior the stage record listed as unknown, now observed.
4. System menu left open: "pauses as suggested." The known limit, not a failure.
5. Minimize, restore, cover and uncover: "clean restore."
6. Alt+F4 during a drag: "during a drag, the alt+f4 is ignored." The size/move loop consumed it and the drag continued; nothing closed, nothing failed. Recorded as what this host does.
7. Keyboard Ctrl+C in the terminal: "yes" (both windows drain and `return` shows `context canceled`).
8. Resident mode, `open`, `open`, `status`, `close`, `close`, `open`, `quit`: "seems to work fine."
9. DPI: "i have no way to change the DPI over RDP (which is the only way to access this host)." Untested, as recorded under DPI.

Nothing in his report looked wrong to him beyond the resize flicker and the ignored Alt+F4, both recorded above.

## DPI

Every `first_surface` in every log reads `@1`: this session runs at 100 % scale with one display. No scale change and no monitor transition was performed; Remote Desktop here cannot change display scaling. **DPI transitions are untested on this host.** The Linux injected `WM_DPICHANGED` test is synthetic and is not native coverage.

## Lock screen and Remote Desktop (native)

Both transitions were performed, with the logs in `run-2/session/`. In both, **the window survived**: `return` is `<nil>`, `paint_failures` is 0, the exit code is 0 and stderr is empty. Paints never stopped while the session was locked or disconnected, so the blit path was exercised throughout and did not fail; nothing here calls for an exception. The fixture was the same `examples/mixer` built from `c9f2357` into Michael's `%TEMP%`, started with `-script session` from the agent's shell in session 2 with stdout and stderr redirected as the handoff's command does.

**Lock screen (`lock.jsonl`).** Started 15:46:15 local; the agent requested the lock with `LockWorkStation` at 15:46:30.7, after 15 s of animation; Michael stayed locked for at least a minute, unlocked, reported "looks fine after unlocking", and closed the window at 15:48:05. The unlock time was not recorded (it lies between 15:47:30 and about 15:47:55). Paints per second: 38.2 before the lock, 39.5 from the lock request to the close (minimum 31 in any second, 94 samples). The recorder did not capture this run's exit code; the fixture's `return` is nil, which exits 0 in `main.go`.

**Remote Desktop (`rdp.jsonl`, `rdp-session.log`).** Started 15:48:39; the recorder sampled `query session` every 5 s. Two disconnects by closing the client window:

| Phase | Session state | Paints per second (minimum in any second) |
| --- | --- | --- |
| before, 15:48:39–15:49:34 | `rdp-tcp#5` active | 38.9 (28) |
| first disconnect, 15:49:34–15:49:54 | `Disc`, about 20 s | 39.6 (33) |
| reconnected, 15:49:54–15:50:45 | `rdp-tcp#6` active; Michael resized the window at 15:50:22–24, 560×260 to 850×216, 480×477, 591×297 | 42.4 (31) |
| second disconnect, 15:50:45–15:56:13 | `Disc`, 5 min 28 s | 39.3 (31), 327 samples |
| after, 15:56:13–15:57:22 | `rdp-tcp#7` active, 3440×1440 | 39.4 (31) |

Michael's report after the second reconnect: "it all looks correct. still moving. looks good." The agent then posted `WM_CLOSE` to the window at 15:57:22; the fixture drained and exited 0. Both reconnects came back at the same 3440×1440, so `first_surface` stayed at scale 1 throughout and **a reconnect at a different client resolution was not performed**. The `screen=` column in `rdp-session.log` comes from a long-lived .NET process and did not change during the disconnects, although a fresh process earlier saw the 1920×1200 `WinDisc` display; treat that column as unreliable and the `query session` state as the record.

What this does not show: the exact message traffic during the transitions, and any host where a disconnected session's paint fails. On this Windows 10 host over Remote Desktop, neither locking nor disconnecting made `BeginPaint`, the fill or `SetDIBitsToDevice` fail.

**Incidental native observation, not the scripted test:** the hand rerun of `large` at 13:57:02–13:57:15 local ran while the Remote Desktop session was disconnected (`query session` at 13:57:47: session 2 `Disc`; the desktop was the 1920×1200 `WinDisc` display, so the 2400×1300 request was clamped to 1924×1181). During it, 296 frames presented with `paints` rising in step, `paint_failures` 0, no `native_problem`, `return` nil and a clean drain. Whether the session was already disconnected at the start or disconnected during the run is inferred from the first capture failing at 13:57:03, not observed. It says that in this disconnected state `BeginPaint`, the fill and `SetDIBitsToDevice` did not fail; it says nothing about the reconnect, which step 11 measures.

## Still open after this run

- The three documented timer-fallback limits (`SetTimer` failure: 10 ms pacing of about 100 messages per second, no retries inside a loop, continuous wakes postponing a retry) have only injected coverage. `SetTimer` never failed on this host, so nothing here exercises them.
- The earlier Linux failures (X11 minimize, Wayland resize and exposure, Wayland `large` stalling at 984×761 in an idle session) and the historical unexplained `internal/appcore` `FAIL` remain unresolved; nothing on Windows explains them.
- The system-menu modal loop still pauses callbacks by design; the manual audition records it.
- A failed `VirtualFree` cannot be provoked here; its handling stays injected-only. No run logged `Win32 image cleanup`.
