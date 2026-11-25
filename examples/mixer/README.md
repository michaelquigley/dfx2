# Mixer example

The synthetic mixer, at stage 4: by default one window with text and an animated level meter, and with `-controls` the mixer itself, two windows of six channel strips over one application-owned model, with faders, mute checkboxes, meters and an ordered command worker standing in for a device. Everything is drawn by `ui`. No audio device or Sessionmixer code is involved.

Linux/amd64 needs the system GTK 3 and Cairo runtime and a desktop display; no headers or cgo. Windows/amd64 needs only system DLLs; its presentation and size/move scripts passed the [native audition](../../evidence/synthetic-mixer/windows/results.md) on Windows 10 over Remote Desktop. Real Windows DPI transitions remain untested. The stage 4 mixer has passed its Linux automated audition, and Michael reported that the Linux hands-on checklist worked well. The [Windows automated and manual audition](../../evidence/synthetic-mixer/windows/stage-4/results.md) also passed; CPU timing remains unavailable as useful evidence on that host. Stage 4 was accepted at `d6b94f9`. The two input corrections made after those auditions have portable-test and cross-build coverage; native evidence remains tied to its recorded revision, as detailed in the [stage record](../../evidence/synthetic-mixer/stage-4.md).

From the repository root, `make` installs `mixer` and `lifetime` into Go's configured `GOBIN`, or `GOPATH/bin` when `GOBIN` is unset, with cgo disabled. With that directory on `PATH`, start the hands-on mixer audition with:

```sh
make
mixer -controls
```

`make test` runs the root tests and vet. `make clean` cleans the root packages and removes only this module's installed example binaries. Direct run commands also work:

```sh
CGO_ENABLED=0 go run ./examples/mixer
CGO_ENABLED=0 go run ./examples/mixer -pause-after 5s
CGO_ENABLED=0 go run ./examples/mixer -resident
CGO_ENABLED=0 go run ./examples/mixer -controls
CGO_ENABLED=0 go run ./examples/mixer -controls -delay 300ms -churn 24
CGO_ENABLED=0 go run ./examples/mixer -font "Ubuntu Sans,sans-serif"
CGO_ENABLED=0 go run ./examples/mixer -browser inprocess
CGO_ENABLED=0 go run ./examples/mixer -browser loopback -delay 800ms
CGO_ENABLED=0 go run ./examples/mixer -browser attach
```

An application task samples a deterministic synthetic level at 30 Hz, publishes it under a mutex and calls `Invalidate` on each subscribed window. The view reads a snapshot: a title, a numeric readout that changes every sample, the meter, a line of fallback text (Greek, Cyrillic, Hebrew, Arabic, Japanese, color emoji, a mixed-direction phrase) and a paragraph that rewraps as the window resizes. Pausing the feed lets the meter's peak fall at 0.5 per second; once it reaches the level, the window does no further work until something changes.

`-paused` starts with the feed paused and `-pause-after` pauses it after an interval, for idle checks. In resident mode the console accepts `open`, `close` (the newest window), `pause`, `resume`, `status` and `quit`. Ctrl+C or SIGTERM cancels and drains, then exits 0. The first run indexes system fonts into the user cache directory, which can delay the first frame.

Output is JSON lines: `opened`, `closed`, `feed`, `edit`, `command`, `external`, `fixture`, with `-browser` also `fixture-server` and, with `-audition`, `page`, ownership/status snapshots and final `return`/`drained` records. Status includes frames rendered by live windows, windows with a pending animation deadline, deadline timers armed, application wakes and dispatches, and the backend's presents, exposure paints, paint failures and, on Windows, size/move loops, modal turns, timers and refusals.

## The mixer (`-controls`)

Six channels with stable IDs `a` to `f` (Kick, Snare, Bass, Keys, Vocal, Room) appear in two windows, A and B, over one model. Each strip shows a fader, a Mute checkbox, a level meter, the channel's name, its level in dB, the device's own gain and mute, and any pending edit. Below the strips are the command totals, the device fixture settings and the latest command error.

Drag a fader with the left button, or Tab to it and use the arrow keys (0.01), Page Up and Page Down (0.1), and Home and End (0 and 1). Click a checkbox, or press and release Space on it. Every user edit prints an `edit` record and becomes a command.

**Data flow.** The model is application state with a mutex. Every change, whether a 30 Hz level sample, a device acknowledgment or a console fixture, builds a new owned `Snapshot` under the mutex and then calls `Invalidate` on the subscribed windows outside it. Views read the latest snapshot and never mutate it. A window that closes after a publication collected it refuses with `dfx2.ErrClosed`, which is counted, not an error. The producer and the command worker are application tasks registered with `app.Go`, so closing either window leaves them and the other window running.

**Commands.** A user edit is submitted to a FIFO of 64 commands and never waits. It gets the next sequence number and is either `accepted` or, when the queue is full or the application is shutting down, `rejected` at once and shown as the latest error. Nothing is merged or dropped silently. The worker hands accepted commands to the synthetic device one at a time, in order, after the device delay. The device applies each one, or rejects it when told to or when the channel has been removed. At shutdown, the command at the device and everything queued are `canceled`, never reported as applied. Each record is a JSON `command` line with its state, sequence, window, channel, control, value, reason and latency since acceptance; the final `return` record carries the totals and whether they balance.

**Pending values.** This policy is the example's, not the widgets'. An accepted command makes its value pending on that channel, so both windows show it and the next keyboard edit or toggle starts from it, however stale the device's snapshot. Acknowledgments are device snapshots. A resolution clears the pending value only if it is still that command's, so an earlier acknowledgment never shows a superseded value. When the device rejects the latest edit, the view returns to the device's value and the error is shown. A refused submission sets no pending value. Reconciling never submits anything, because a value the view supplies never reports a change. An external change to a control with a pending edit updates the device's value underneath; the pending value stays shown until its command resolves, and the device ends with whichever came last.

**Console.** These are external changes, the device or its operator acting underneath the application; they print `external` and never an `edit` or a command:

| Command | Effect |
| --- | --- |
| `gain <id> <0..1>`, `mute <id> on\|off` | Change the device's value. |
| `reorder`, `order <six ids>` | Reverse, or set, the strip order. |
| `remove <id>`, `insert <id>` | Hide a channel, or bring it back as the last strip. |

These operate the fixture:

| Command | Effect |
| --- | --- |
| `delay <duration>` | Device delay before each command, such as `700ms`; `0s` releases a held command at once. Also `-delay`. |
| `reject <n>` | The device rejects the next `n` commands it receives. |
| `churn <n>`, `churn off` | Up to 48 words of text that changes with every publication, to exercise the text caches. Also `-churn`. |
| `pause`, `resume` | Stop or restart the 30 Hz producer. |
| `open`, `close [name]` | Open another window, or close one by name (the newest without a name). |
| `snapshot <name> [window]` | Write a window's retained image (the first window's by default) to `-snapshots`. |
| `status`, `quit` | Print counters, or quit. |

`status` adds view evaluations, command totals, each channel's device and pending values, process CPU seconds and, from the UI thread, each window's text cache counters and budget. `-text-budget small` (or explicit `GLYPH_BYTES:PARAGRAPH_BYTES:ENTRIES`) shrinks every window's text caches for eviction auditions. A budget below the view's steady working set evicts nothing: the excess is served transiently every frame.

A strip's fader and checkbox come first, so the [input auditions](../../evidence/synthetic-mixer/stage-4.md) can press them at computed positions; strips are 110 logical units wide and 12 apart.

## Mixed views (`-browser`)

`-browser inprocess`, `-browser loopback` or `-browser attach` opens native window A and a browser view B over the same model, the [webview stage 4](../../evidence/webview-integration/stage-4.md) demonstration. It implies `-controls`, and every console command above works unchanged. Without `-browser` the mixer is native-only, as before; browser support is loaded only when the first browser view opens.

- **`inprocess`** serves the page through the browser's request interception, with no listener.
- **`loopback`** serves it from a window-owned server on 127.0.0.1, behind that window's credential cookie.
- **`attach`** starts a fixture server owned by the application, not by any window: on `-attach-addr` (default `127.0.0.1:0`, printed as `fixture-server`), without authentication or the framework's policy, because it is an audition fixture, not a production service. The browser view attaches to it, closing that view leaves it serving, and it stops when the application drains. Its shutdown waits for every handler. A 10 s read timeout, covering headers and body, keeps a stalled client from holding one open forever.

On Linux, `-gc-signal` (default 60) is the real-time signal this application reserves for JavaScriptCore; nothing else in the example uses it. It is inert elsewhere.

The page shows what the native strips show: each channel's name, level, gain fader and Mute box, the device's own gain and mute, any pending edit with its sequence number, the command totals, the device delay and pending rejections, and the latest error. Strips follow the model's order, and removed channels disappear.

**Data flow.** The page's handlers are thin and live in `browser.go`; the model and the command queue know nothing of HTTP.

- **Polls.** `GET /api/state` returns the latest snapshot as JSON. The page keeps exactly one poll outstanding, waiting 100 ms after each before the next.
- **Edits.** `POST /api/edit` submits one user edit through the same ordered command path, the same `edit` record and the same pending-value policy as a native edit, attributed to the view by name. A page submits its edits one at a time, in input order: each starts only after the previous one is answered or has failed. The answer is only the submission's outcome; the device's answer arrives in later snapshots.
- **Reconciliation.** Showing a snapshot never submits anything: the page assigns values to its controls, which fires no events. It leaves both the fader and Mute alone while that channel has edits queued or unanswered, and also leaves a fader alone while the pointer holds it. A fader edit is sent when the drag is released or on each key press; a Mute edit on each toggle.
- **No retries.** A failed or refused edit is shown and never retried.

The console adds `browser`, which opens another browser view in the same mode, and `close NAME` closes either kind of window. The sampler, the command worker and the attach fixture server are application tasks, so they outlive every request and window. Closing the last window ends a window application and cancels whatever is still queued; `-resident` keeps it running for `open` and `browser`.

`-audition` makes each page also report what it shows and where its controls are (`page` lines), for the [automated mixed-view audition](../../evidence/webview-integration/linux/mixed_drive.py). `-devtools` enables the browser's inspector.

## Bounded scripts

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-mixer ./examples/mixer
/tmp/dfx2-mixer -script animate -snapshots /tmp/snaps
/tmp/dfx2-mixer -script resize -snapshots /tmp/snaps
/tmp/dfx2-mixer -script minimize
/tmp/dfx2-mixer -script close-sample
/tmp/dfx2-mixer -script reopen
/tmp/dfx2-mixer -script two
/tmp/dfx2-mixer -script expose
/tmp/dfx2-mixer -script large -snapshots /tmp/snaps
/tmp/dfx2-mixer -script fonts
```

`animate` runs the feed, pauses it, waits until no animation deadline has existed for 250 ms and measures a five-second quiet interval. `resize` drives four native resizes through an internal audition hook and records the copied surface after each. `minimize` iconifies and restores the window through the hook and records frames, timers and paints meanwhile. `close-sample` closes the window between invalidations from a 1 kHz loop while the sampler runs, then keeps invalidating and counts refusals. `reopen` opens and closes five windows in resident mode, then quits. `two` runs two windows, closes one and confirms the other keeps rendering. `expose` asks for native repaints of a settled window and counts frames and paints. `large` steps through growing sizes, starting at the text spike's recorded failure size, with a retained snapshot per step when `-snapshots` is set. `fonts` records which face and glyph format serve each sample script on this host. `signal` opens two windows, reports three rounds of observed title and frame progress on each, and waits for a console control event; a window that stops progressing for 10 seconds fails the run. `session` runs the animated window with no time bound and records status once a second, for a person to lock or disconnect the session around it; closing the window ends it. `-snapshots` writes the retained image exactly as handed to GTK as PNG; it is not a screen capture.

Six scripts exercise native size/move loops, on Windows, and each opens two windows with posted title counters: `modal-move`, `modal-size`, `modal-idle`, `modal-close-self`, `modal-close-other` and `modal-quit`. Each emits a `drag-request` event and waits up to `-modal-wait` (default 20 s) for a loop to start, from the [`desk`](../../evidence/synthetic-mixer/windows/desk/main.go) audition driver or from a person dragging a window. `modal-move` and `modal-size` report frames and applied title updates per second for each window while the loop lasts.

[`evidence/synthetic-mixer/linux/stage4_matrix.py`](../../evidence/synthetic-mixer/linux/stage4_matrix.py) runs the stage 4 mixer audition, the scripts below and the foundation matrix in an isolated nested GNOME Shell, and [`windows/stage-4/stage4.ps1`](../../evidence/synthetic-mixer/windows/stage-4/stage4.ps1) is the Windows equivalent. [`evidence/synthetic-mixer/linux/drive.py`](../../evidence/synthetic-mixer/linux/drive.py) runs the portable scripts plus a SIGTERM case with time bounds and process inspection. It requires Python 3 with Pillow (Debian/Ubuntu package `python3-pil`) to verify and decode retained PNG snapshots. [`evidence/synthetic-mixer/windows/audition.ps1`](../../evidence/synthetic-mixer/windows/audition.ps1) is the Windows equivalent.
