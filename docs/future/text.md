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
