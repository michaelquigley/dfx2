---
title: engine-neutral text seam with a Pango engine
state: researching
created: 2026-11-01
tags: [feature, spike]
---

in `experiments/text-spike`, stop go-text's types crossing the `Engine` seam:
- give the seam its own types: an opaque font handle that the glyph cache keys on, and a shaped run (font, direction, rune range, line metrics, and glyphs with id, advance, offsets and cluster).
- keep line breaking in shared code, over shaped runs, with break opportunities from go-text's `segmenter`. wrap a copy of the runs each time, so rewrapping can't lose spaces.

then add a Pango engine through purego:
- Pango's itemize and shape for runs, not its own line breaking.
- glyphs rasterized through cairo into coverage masks with the desktop's font options.
- selected with `-engine go|pango`, with its own row in `compare`.

expect the Pango engine's row to land on Pango's grayscale + slight-hinting row, and record the profile distances in `docs/future/text.md`.

## why

it proves the claim in `docs/future/text.md` that a native engine is a moderate addition behind a neutral seam, and settles the seam's shape before dfx2 code depends on it.
