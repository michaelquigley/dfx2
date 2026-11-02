# Text in dfx2

dfx draws text the way Dear ImGui does. Its `fonts.go` bakes atlases at
fixed sizes, with glyph ranges and merged icon fonts. There's no shaping, no
automatic fallback between fonts, no bidi and no hinting, and scaling blurs.
This document is where dfx2's text stands after the first experiments.

## The shape explored

**An engine behind a narrow seam.** The engine picks fonts with per-rune
fallback, shapes, runs bidi and rasterizes single glyphs at a size and
subpixel offset. Everything else is shared code, the same for every engine:
- wrapping shaped runs to a width
- assembling lines
- the glyph cache and its atlases
- compositing

The idea comes from mygo ([mygo-study.md](mygo-study.md)), which also puts
carets, selection, ellipsis and justification in shared code.

**Shape once, rewrap per width.** A paragraph is shaped once per size. A new
width rewraps the same shaped runs, so a resize doesn't reshape.

**Glyphs on demand.** A glyph is cached by face, glyph, size and one of 4
subpixel x positions. Baselines snap to whole pixels, pens don't.

## The spike

[`experiments/text-spike`](../../experiments/text-spike) is a bare GTK 3
window, opened through purego with `CGO_ENABLED=0`, that shows the text
given on the command line. Its README says how to run it.
- **Engine:** go-text. `fontscan` indexes the system fonts and picks
  fallbacks, its HarfBuzz port shapes, `x/image/vector` rasterizes, and
  Noto Color Emoji's PNG strikes give color emoji.
- **Hinting:** its own slight hinting (`text/hint.go`).
- **Comparison:** `go run ./compare` draws the same text with the spike and
  with Pango, as GTK 3 does, and measures the difference.

### Measurements

These were taken on the target desktop, 2026-11-01: GNOME on Wayland, with
LCD antialiasing (`rgba`, RGB order), slight hinting, and Ubuntu Sans 11 pt
(14.67 px) as the UI font. Both engines drew Ubuntu Sans.

The measure is the distance of each row's vertical ink profile (ink summed
per scanline) from Pango's grayscale + slight-hinting row. This isolates
vertical sharpness, since horizontal positions differ between engines.
0 means identical.

| | 11 px | 13 px | 14.67 px | 18 px | 24 px |
|---|---|---|---|---|---|
| spike, unhinted | 0.062 | 0.053 | 0.135 | 0.159 | 0.094 |
| spike, slight hinting | 0.014 | 0.021 | 0.040 | 0.047 | 0.048 |
| Pango, unhinted (for scale) | 0.203 | 0.162 | 0.077 | 0.160 | 0.094 |

What the measurements show:
- **Rasterizers.** Unhinted, the spike and Pango look alike: rasterization
  in pure Go isn't the weak point.
- **Hinting.** It was the gap at UI sizes, and about 170 lines closed most
  of it. Michael judged the hinted spike "nearly identical" to Pango on
  screen.
- **LCD antialiasing.** It's the desktop's setting, and the spike lacks it.
- **Widths.** Pango rounds glyph positions to whole pixels, so its lines
  come out shorter: about 12 px over 60 characters at 14.67 px. The spike
  keeps the font's fractional advances.

### Slight hinting, as built

It follows FreeType's autohinter in light mode, vertically only, so advances
don't change:
- **One vertical scale** per font and size puts the top of the x-height's
  overshoot on a whole pixel, rounding up from 0.375 of a pixel (FreeType's
  `(scaled + 40) & ~63`).
- **Zones.** The baseline, x-height, cap height, ascender and descender
  edges round to whole pixels.
- **Overshoots** round to 0 below half a pixel, to half a pixel below three
  quarters, else to 1.
- **Everything between** is interpolated linearly.

A first version that rounded the x-height and cap height plainly, without
the scale, helped at 11–14.67 px but made 18 and 24 px worse (0.220 and
0.156). Don't go back to it.

It doesn't snap a glyph's own horizontal stems (the bar of `e`, a dash, the
bars of `t` and `f`) as the autohinter does. At 13 px the dash still spans
two pixel rows. See the roadmap card on stem snapping.

## Problems hit (keep these)

- **go-text's `LineWrapper` mutates its input.** It zeroes the advance of
  each line's trailing space in place, in glyph slices it shares with the
  runs it was given. Wrapping cached runs again at another width then loses
  spaces. Wrap a copy of the glyph slices. The spike's
  `TestRewrapKeepsSpaces` covers it.
- **A space never changes the font.** go-text follows Pango's rule here
  (`ignoreFaceChange`), so a space after an emoji stays in the emoji font.
  Noto Color Emoji's space is wider than an em. The spike's engine moves
  spaces inside fallback-font runs back to the preferred font
  (`spacesToPrimary`).
- **Emoji fallback can land on a COLR font.** On this machine, `fontscan`'s
  fallback picked Segoe UI Emoji (in `~/.fonts`), whose layered COLR glyphs
  the spike can't draw. Adding the generic `fontscan.Emoji` family to the
  query after the preferred ones, as browsers do, picks Noto Color Emoji.
  Real COLR/CPAL support means walking a paint graph.
- **Cairo's LCD rendering needs an opaque surface.** On an ARGB32 surface it
  tinted whole words instead of fringing glyph edges. The comparison draws
  Pango's LCD row on RGB24, as GTK draws into a window.

## Windows results (2026-11-02)

The [Windows native spike](windows-native-spike.md) put the same engine and
CPU renderer in a no-cgo Win32 window (`window_windows.go`) and added a
background-fed meter (`-meter`). The evidence is in
[`experiments/text-spike/evidence/windows/`](../../experiments/text-spike/evidence/windows/).

### Host and conditions

- Windows 10 Pro 22H2, build 19045.6466, amd64. It's a QEMU/KVM guest
  (i440FX), with 8 logical CPUs of an i7-6950X.
- **Session: remote, over RDP** (`RDP-Tcp#0`). There's one display, the
  Microsoft Remote Display Adapter at 3440×1440, at 96 dpi (100%). Every
  window result below is RDP presentation, not a local GPU or monitor.
- ClearType on (`FontSmoothing=2`, `FontSmoothingType=2`, RGB order), in
  the session's registry. Whether RDP delivers it to the client was not
  verified.
- Go 1.27.3 windows/amd64 (the module declares 1.26). `CGO_ENABLED=0`
  throughout.
- Fonts, from `-fonts`: `Segoe UI,sans-serif` resolves Latin, Greek,
  Cyrillic, Hebrew and Arabic to Segoe UI. 日本語 falls back to MS Gothic.
  The emoji land in Segoe UI Emoji, whose glyphs are COLR paint, and come
  out blank (see below).

### Commands and tests run

From `experiments/text-spike`, with `CGO_ENABLED=0`:
- `go test ./...` passes: `TestRewrapKeepsSpaces`, and new tests for the
  meter feed (coalescing, a failed post, pause, stop/join) and for the
  Win32 struct layouts.
- `go vet ./...` is clean, and so is `go vet` with `GOOS=linux` and
  `GOOS=darwin`.
- `GOOS=linux go build . ./compare` cross-builds. The Linux window and
  compare were not run.
- `go build -o dfx2-text-spike.exe .` builds, and `-png` (with
  `-scale 1.5`, `-meter`, `-sizes`, `-light`) renders and exits.

A scripted driver ran the real window. It launched the exe, captured the
client area from the screen, resized it with `SetWindowPos`, minimized and
restored it, posted keys (`WM_CHAR`) and `WM_CLOSE`, and timed the exit.
That's automated evidence of native behavior. It isn't Michael's
observation, so the two are kept apart below.

### A bug found: Go-heap frames fail in `SetDIBitsToDevice`

Presenting the frame from a Go `[]byte` failed intermittently: a black or
stale window at some sizes, and different sizes on different runs.
`SetDIBitsToDevice` returned 0 lines with `GetLastError` 0, and the paint
DC's clip box covered the whole client. Retrying never helped. The failures
tracked the buffer's address exactly: every failing frame (1.9–3.0 MB)
straddled a 4 MB boundary, and no succeeding frame did
([log](../../experiments/text-spike/evidence/windows/present-failure.log),
[capture](../../experiments/text-spike/evidence/windows/before-fix-present-failure-984x761.png)).
On Windows, Go reserves its heap in 4 MB arenas, each its own
`VirtualAlloc`. The likely cause is that GDI rejects a source buffer that
spans two allocations. That's an inference: it matches every observation,
but it isn't documented. The fix gives the frame memory from one
`VirtualAlloc` (`window.pixels`), and rendering writes straight into it.
Afterwards, every scripted size and resize run presented with no failures.
**Any dfx2 surface handed to GDI (and probably any OS blit API) should be
native-allocated, not Go-heap.**

### Audition

"Automated" is the scripted driver or the counters. "Observed" is what
Michael saw in the window.

| Check | Automated | Observed |
| --- | --- | --- |
| Launch, close, relaunch | Each run launched, and `WM_CLOSE` exited in 30–50 ms, metering or paused. A real-close test exited too. | Observed: Michael reported it "looked great". |
| Resize and exposure | 6–32 scripted resizes per run (200–2000 px wide), minimize 2 s and restore: captures readable, no lost spaces, body text rewrapped and not reshaped. The meter kept updating through programmatic resizes. | Observed: "looked great". |
| DPI | Untested: the RDP display is fixed at 96 dpi. The `WM_DPICHANGED` path has never run. | **Untested**: Michael can't change display scaling on this host. |
| Text | 11/13/14/18/24 px, dark and light, zoom: [sizes](../../experiments/text-spike/evidence/windows/text-sizes-dark.png), [zoomed, light](../../experiments/text-spike/evidence/windows/text-zoomed-light.png) | Observed: "looked great". No side-by-side reference app or settings were recorded. |
| Active updates | 30.0 ticks/s, ~30 paints/s, render avg 0.08–0.3 ms (first frame ~56 ms, rasterizing). 0–1 coalesced per 5 s, 0 failed posts. CPU 297 ms per 10 s (~3% of one core), frame log off. | Observed: "looked great". |
| Paused updates | 10 s paused: 0 ticks, 0 posts, 0 paints. Zoom while paused repainted once. | Observed: "looked great". |
| PNG | `-png` at scale 1 and 1.5, with and without `-meter`, writes and exits. | n/a |

Counters come from `s` in the window and the 5 s and exit summaries
(README). Measured with the per-frame log off, which `-meter` defaults to.

### Michael's judgment and what it implies

Michael auditioned the window over RDP. Every check except DPI "looked
great": the text, the meter, resizing, pausing and closing. He couldn't
change display scaling on this host, so DPI is untested.

- **Next design step:** the no-cgo Win32 window and the
  background-to-UI wakeup are proven enough to design the platform
  layer's main-thread loop and wakeup contract on.
- **Needs another run:** DPI (100% to 125/150/200%, and moving between
  monitors) on a machine where scaling can change.
- **Untested:** text on a local console rather than RDP, and against a
  side-by-side Windows reference app.

### Findings

- **Win32 through `syscall` carries the window.** One window procedure for
  the process, windows routed by an id in `GWLP_USERDATA`, and per-monitor
  v2 awareness all hold up. A settled window does nothing.
- **The background-to-UI pattern works as sessionmixer needs it.** The
  worker publishes under a mutex and posts a payload-free `WM_APP` only when
  none is pending. Exposure re-presents without re-rendering (paints >
  renders while minimized). Pausing stops the ticker, so a paused window is
  idle.
- **The readout reshapes every tick.** A number that changes each frame is
  a new paragraph each frame (shaped ≈ readouts in the counters). The body
  text is shaped once. A real meter readout wants cached digit glyphs or a
  shaping cache keyed by string.
- **COLR emoji are blank on Windows by default.** Without Noto Color Emoji
  installed, `fontscan.Emoji` resolves to Segoe UI Emoji, whose glyphs are
  `GlyphColor` (COLR paint). They keep their advance but draw nothing. This
  is the known renderer gap, not a window problem.

## Paths forward

- **Pure Go.** go-text with our hinting is good enough on Linux at every
  size measured, with no native text dependency. Still missing: stem
  snapping, optionally LCD antialiasing and gamma-corrected blending, then
  carets, selection and ellipsis.
- **Pango behind the seam.** This gives hinting identical to GTK's, from
  FreeType. It needs the seam to stop leaking go-text types: a font handle,
  and a shaped run whose glyphs carry an id, advance, offsets and cluster.
  Pango would be called for itemizing and shaping only, keeping line
  breaking in shared code. That's a few hundred lines through purego. It's
  Linux only, so "configurable" really means pure Go everywhere, optionally
  native per platform.
- **Either way:**
  - Keep each engine in its own package, so an app links only the one it
    uses.
  - Expect engines to differ in fallback choices (fontconfig versus
    `fontscan`) and in widths (Pango's rounding). Write layout tests per
    engine or against one.

Not explored yet:
- the two-zone atlas (the spike's atlases only grow)
- COLR/CPAL fonts
- a GPU renderer for glyph quads
- rich-text spans and decorations
