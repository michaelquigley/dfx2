# Mixer example

The synthetic mixer, at stage 3: by default one window with text and an animated level meter, and with `-controls` two windows of channel strips over one model, with faders and mute checkboxes. Everything is drawn by `ui` from an application-owned synthetic source. No audio device or Sessionmixer code is involved. Stage 4 adds the full channel set, the ordered command worker and the rest of the fixture controls.

Linux/amd64 needs the system GTK 3 and Cairo runtime and a desktop display; no headers or cgo. Windows/amd64 needs only system DLLs; its presentation and size/move scripts passed the [native audition](../../evidence/synthetic-mixer/windows/results.md) on Windows 10 over Remote Desktop. Real Windows DPI transitions remain untested.

```sh
CGO_ENABLED=0 go run ./examples/mixer
CGO_ENABLED=0 go run ./examples/mixer -pause-after 5s
CGO_ENABLED=0 go run ./examples/mixer -resident
CGO_ENABLED=0 go run ./examples/mixer -controls
CGO_ENABLED=0 go run ./examples/mixer -font "Ubuntu Sans,sans-serif"
```

An application task samples a deterministic synthetic level at 30 Hz, publishes it under a mutex and calls `Invalidate` on each subscribed window. The view reads a snapshot: a title, a numeric readout that changes every sample, the meter, a line of fallback text (Greek, Cyrillic, Hebrew, Arabic, Japanese, color emoji, a mixed-direction phrase) and a paragraph that rewraps as the window resizes. Pausing the feed lets the meter's peak fall at 0.5 per second; once it reaches the level, the window does no further work until something changes.

With `-controls`, drag a fader with the left button, or Tab to it and use the arrow keys (0.01), Page Up and Page Down (0.1), and Home and End (0 and 1). Click a checkbox, or press and release Space on it. Every user edit prints an `edit` record and updates the shared model, so the other window follows. The console also accepts `gain <a|b|c> <0..1>`, `mute <a|b|c> on|off`, `reorder`, `order a b c`, `remove <id>`, `insert <id>` and `snapshot <name>`. The model commands are external changes: they print `external` and never an `edit`. A strip's fader and checkbox come first so the [input auditions](../../evidence/synthetic-mixer/stage-3.md) can press them at computed positions.

`-paused` starts with the feed paused and `-pause-after` pauses it after an interval, for idle checks. In resident mode the console accepts `open`, `close` (the newest window), `pause`, `resume`, `status` and `quit`. Ctrl+C or SIGTERM cancels and drains, then exits 0. The first run indexes system fonts into the user cache directory, which can delay the first frame.

Output is JSON lines: `opened`, `closed`, `feed`, ownership/status snapshots and final `return`/`drained` records. Status includes frames rendered by live windows, windows with a pending animation deadline, deadline timers armed, application wakes and dispatches, and the backend's presents, exposure paints, paint failures and, on Windows, size/move loops, modal turns, timers and refusals.

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

[`evidence/synthetic-mixer/linux/drive.py`](../../evidence/synthetic-mixer/linux/drive.py) runs the portable scripts plus a SIGTERM case with time bounds and process inspection. [`evidence/synthetic-mixer/windows/audition.ps1`](../../evidence/synthetic-mixer/windows/audition.ps1) is the Windows equivalent.
