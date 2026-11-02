# Windows native spike

Prove the no-cgo native path on Michael's Windows development host by extending `experiments/text-spike`. Bring back working code and observed results that let us judge Windows window behavior, pure-Go text quality, and background-driven drawing before designing dfx2's permanent platform API.

This is an experiment, not the first implementation of the framework. Read `AGENTS.md`, the recent `docs/journal/` entries, [direction.md](direction.md), [text.md](text.md), and the [spike README](../../experiments/text-spike/README.md) on arrival. The existing Linux window and renderer are the starting point. mygo is a source of ideas only: do not copy its code or add it as a dependency.

## Context and scope

dfx2 succeeds dfx and dfw without source compatibility. Its native API will retain immediate-mode programming with a smoother, idiomatic Go feel. Sessionmixer is the first real application. Its background meter sampler targets 30 Hz and publishes cached levels independently of drawing; hardware events also update fader values. The Windows experiment should exercise that background-to-UI path with synthetic data. Sessionmixer's current ALSA/cgo hardware integration is outside this work.

Keep the experiment in its existing Go module. Use Win32 through `syscall`, with `CGO_ENABLED=0` and no new dependencies. Preserve the pure-Go text engine, CPU compositing, PNG output, and Linux window. Keep new abstractions local to the experiment; the results will inform the framework design afterward.

## Stage 1: window, text, and DPI

Add `window_windows.go` and narrow `window_other.go` to exclude both Linux and Windows. Implement the existing `runWindow` entry point:

- Lock the main goroutine to its OS thread and run the Win32 message loop there. Create the window procedure once with `syscall.NewCallback`; route window state by a stable identifier rather than allocating callbacks per window or paint.
- Present the existing opaque BGRA CPU frame during `WM_PAINT`, using `BeginPaint`/`EndPaint` and `SetDIBitsToDevice` with a top-down 32-bit DIB. Keep the pixel storage alive through the synchronous native call. Skip rendering zero-sized clients.
- Set per-monitor-v2 DPI awareness before creating the window. Use the actual client size in device pixels and a DPI/96 scale for `app.render`. On `WM_DPICHANGED`, adopt the new DPI and suggested bounds, then redraw at the new resolution. Do not stretch an old bitmap to simulate scaling. See Microsoft's [DPI-change contract](https://learn.microsoft.com/en-us/windows/win32/hidpi/wm-dpichanged) and [bitmap presentation API](https://learn.microsoft.com/en-us/windows/win32/api/wingdi/nf-wingdi-setdibitstodevice).
- Retain `+`/`-` zoom, `0` reset, `Esc`/`q` quit, and normal window-manager close. Redraw on exposure, resize, DPI changes, and input that changes the picture. A settled window must not run a continuous repaint loop.
- Report native initialization failures as errors and release owned window/GDI resources on exit. Keep startup, painting, and shutdown understandable without building a reusable backend framework.

The existing `compare/main.go` is not build-tagged, but calls helpers available only in `pango_linux.go`. Add the Linux build constraint to that command so Windows package-wide builds and tests work. Preserve the Pango comparison on Linux; do not port it as part of this spike.

## Stage 2: synthetic meter and wakeup

Add an opt-in `-meter` fixture with a visible level bar and a pause/resume control, documented in the README. A background goroutine should produce deterministic synthetic levels at approximately 30 Hz. This is enough to exercise sessionmixer's update pattern; no mixer widgets or hardware packages are needed.

The worker publishes the latest level through synchronized Go state and wakes the window using a posted message. The UI thread consumes the state, requests a paint, and owns all text layout, atlas, and framebuffer mutation. `PostMessageW` queues work to the window's owning thread without waiting for it; see its [contract](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-postmessagew). Keep the wakeup payload free of borrowed Go pointers. Coalesce pending updates so a delayed UI displays the latest state instead of replaying a backlog.

Pausing should stop the producer's timer and repaint requests; resuming restarts it. Stop and join the worker as part of shutdown, with no worker posts after the window is destroyed. The shared `-png` path should still render once and exit. The meter fixture may be Windows-only for this experiment; document that choice rather than implying Linux support.

Keep existing text counters and add enough instrumentation to distinguish updates, paints, and render time. Avoid logging every worker tick. Measure performance with verbose logging disabled, and document how to obtain the counters. Repainting unchanged text at a fixed size and width should reuse its cached shaping and glyphs.

## Build and verification

Work on the actual Windows host and record its architecture. Go 1.26 is declared by the experiment. From `experiments/text-spike`, PowerShell:

```powershell
$env:CGO_ENABLED = '0'
go test ./...
go vet ./...
go build -o dfx2-text-spike.exe .
.\dfx2-text-spike.exe -font "Segoe UI,sans-serif" -size 14
.\dfx2-text-spike.exe -font "Segoe UI,sans-serif" -size 14 -hint=false
.\dfx2-text-spike.exe -font "Segoe UI,sans-serif" -size 14 -meter
```

The last command is the fixture to implement. Document any additional audition switches. Add focused tests where useful for new wakeup/lifecycle logic; do not present a build or headless test as evidence of native-window behavior. Keep the existing rewrap regression passing.

Michael auditions the actual window with the implementing agent. Record each check as observed, failed, or untested:

| Check | Evidence to collect |
| --- | --- |
| Launch, close, relaunch | Normal startup and exit, including close while the meter is active or paused; no hung process. |
| Resize and exposure | Repeated narrow/wide resize, cover/uncover, minimize/restore; readable redraws and no lost spaces. Record meter behavior during interactive resize. |
| DPI | 100%, 125%, 150%, and 200% where the host supports them. Move between differently scaled monitors if available. Record unavailable cases explicitly. |
| Text | 11, 13, 14, 18, and 24 DIP sizes, hinting on/off, zoom/reset, Latin labels and numerals, multilingual sample and emoji. Record the resolved fonts and missing glyphs. |
| Active updates | Meter keeps changing without mouse movement; input and resize remain responsive. Report update/paint counts, render timings, and observed CPU usage with logging conditions. |
| Paused updates | After pending work drains, no periodic updates or paints attributable to the fixture; exposure and input still repaint. Observe at rest for about ten seconds. OS-triggered paints are legitimate. |
| PNG | Existing headless output still works and exits, including a non-default `-scale`. |

For text judgment, inspect at native display size, on light and dark backgrounds. Add a small audition option for the background if needed. Compare with text in an identified Windows application using a matched font and approximate size where possible. Record the reference application and settings; do not claim it uses DirectWrite or a particular antialiasing mode without verification. This is a visual comparison, not a repeat of the Linux Pango metric. Keep a few representative screenshots at original resolution, plus a clearly labeled enlarged crop if useful.

The renderer handles outline glyphs and PNG bitmap strikes, but not COLR paint graphs. Missing color emoji may expose that known limitation or font fallback behavior. Diagnose and record it separately from window presentation. A text-quality gap is a valid experiment result; do not expand this task into a new text engine to make the result look successful.

## Return evidence

Update the experiment README with Windows commands and controls. Add a Windows results section to [text.md](text.md), keeping the existing Linux measurements intact. Include:

- Windows version/build, CPU architecture, Go version, display resolution/scaling, session type (local or remote), relevant font names, and ClearType settings if known.
- Commands and tests actually run, the audition table with results, representative screenshots, and counter/timing observations with their measurement conditions.
- Michael's text-quality judgment, reproducible failures, and the implications for the next design step: what worked, what needs another spike, and what remains untested.

Put selected small evidence files under `experiments/text-spike/evidence/windows/` and link them from the results. Keep executables, font caches, bulk captures, and temporary logs out of the commit. Use the day's `docs/journal/YYYY-MM-DD.md` only for durable context not already carried by the code or results, including any unfinished cross-host verification.

On return to Linux, run the experiment's tests and vet with `CGO_ENABLED=0`, build the Linux commands, cross-build the Windows command for the tested architecture, and smoke-test the Linux window. These checks verify integration; they do not replace the Windows audition. If anything cannot be exercised, leave it explicitly unverified.

## Deferred (and Why)

- **WebView2 and the in-process HTTP endpoint:** a separate experiment after this one, to isolate COM, request handling, and shutdown behavior. The transport contract is still open; retain the loopback option.
- **DirectWrite, a neutral text seam, and hinting improvements:** first measure what the existing engine does on Windows. Its shortcomings are findings, not instructions to redesign it here.
- **GPU rendering, production layout/widgets, IME, accessibility, and sessionmixer migration:** this spike establishes platform evidence; it does not establish a complete toolkit or settle those contracts.
- **Additional Windows architectures and versions:** verify the available host first and state the tested scope. Cross-compilation alone is not runtime support evidence.

## Cross-host handoff

Michael commits the brief and settled design notes here, makes that commit available to the Windows host, and checks out a dedicated branch there (suggested name: `spike/windows-native`). The Windows implementation session reads this brief from the checkout and keeps its code and findings on that branch. Avoid concurrent edits to the same experiment and result files from Linux while the Windows work is in flight.

After implementation and audition, Michael commits the code and evidence on Windows and makes the branch available here. Fetch and integrate that branch in a clean Linux worktree, run the return checks, then resume design from the recorded results. Agents leave reviewable working-tree changes; commits and pushes remain Michael's actions unless explicitly delegated for that commit. No host-specific conversation or harness memory should be required to understand the outcome.
