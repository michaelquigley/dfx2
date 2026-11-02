---
title: snap horizontal stems in slight hinting
state: researching
created: 2026-11-01
tags: [enhancement, spike]
---

`experiments/text-spike/text/hint.go` snaps only a font's alignment zones. add what FreeType's autohinter does next, still vertically only:
- find each glyph's horizontal stems: horizontal outline segments, paired with an opposite-direction segment that overlaps it horizontally, closer together than about 0.15 em.
- round each stem's thickness to at least a pixel and its position to whole pixels. an edge near a zone takes the zone's position.
- interpolate the outline's other points between the fitted edges.

rerun `go run ./compare`, check the 13 px dash and the bars of `e`, `t` and `f` in `compare-zoom.png`, and add the new profile distances to the table in `docs/future/text.md`.

## why

with zones alone, the hinted spike measures close to Pango's slight hinting, but strokes in the middle of a glyph still spread across two pixel rows at small sizes. that's the visible gap left.
