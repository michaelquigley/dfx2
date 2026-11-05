# Mixer example

The synthetic mixer, at stage 1: one window with text and an animated level meter, drawn by `ui` from an application-owned synthetic source. No audio device or Sessionmixer code is involved. Later stages add channel strips, faders, mute checkboxes and two windows over one model.

Linux/amd64 needs the system GTK 3 and Cairo runtime and a desktop display; no headers or cgo. On Windows the example exits with an unsupported-content error until Win32 presentation lands.

```sh
CGO_ENABLED=0 go run ./examples/mixer
CGO_ENABLED=0 go run ./examples/mixer -pause-after 5s
CGO_ENABLED=0 go run ./examples/mixer -resident
CGO_ENABLED=0 go run ./examples/mixer -font "Ubuntu Sans,sans-serif"
```

An application task samples a deterministic synthetic level at 30 Hz, publishes it under a mutex and calls `Invalidate` on each subscribed window. The view reads a snapshot: a title, a numeric readout that changes every sample, the meter, a line of fallback text (Greek, Cyrillic, Hebrew, Arabic, Japanese, color emoji, a mixed-direction phrase) and a paragraph that rewraps as the window resizes. Pausing the feed lets the meter's peak fall at 0.5 per second; once it reaches the level, the window does no further work until something changes.

`-paused` starts with the feed paused and `-pause-after` pauses it after an interval, for idle checks. In resident mode the console accepts `open`, `close` (the newest window), `pause`, `resume`, `status` and `quit`. Ctrl+C or SIGTERM cancels and drains, then exits 0. The first run indexes system fonts into the user cache directory, which can delay the first frame.

Output is JSON lines: `opened`, `closed`, `feed`, ownership/status snapshots and final `return`/`drained` records. Status includes frames rendered by live windows, windows with a pending animation deadline, deadline timers armed, application wakes and dispatches, and the backend's presents and exposure paints.

## Bounded scripts

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-mixer ./examples/mixer
/tmp/dfx2-mixer -script animate -snapshots /tmp/snaps
/tmp/dfx2-mixer -script resize -snapshots /tmp/snaps
/tmp/dfx2-mixer -script minimize
/tmp/dfx2-mixer -script close-sample
/tmp/dfx2-mixer -script reopen
/tmp/dfx2-mixer -script two
```

`animate` runs the feed, pauses it, waits until no animation deadline has existed for 250 ms and measures a five-second quiet interval. `resize` drives four native resizes through an internal audition hook and records the copied surface after each. `minimize` iconifies and restores the window through the hook and records frames, timers and paints meanwhile. `close-sample` closes the window between invalidations from a 1 kHz loop while the sampler runs, then keeps invalidating and counts refusals. `reopen` opens and closes five windows in resident mode, then quits. `two` runs two windows, closes one and confirms the other keeps rendering. `-snapshots` writes the retained image exactly as handed to GTK as PNG; it is not a screen capture.

[`evidence/synthetic-mixer/linux/drive.py`](../../evidence/synthetic-mixer/linux/drive.py) runs these plus a SIGTERM case with time bounds and process inspection.
