# dfx2 text spike

A throwaway spike of dfx2's text ideas: a bare window showing the text from
the command line, drawn by a Go-native text system, without cgo. What it
showed, and where it leaves the design, is in
[`docs/future/text.md`](../../docs/future/text.md). It is its own Go module,
so the root module never builds it.

```sh
CGO_ENABLED=0 go run . "Hello, dfx2 🎛️"
CGO_ENABLED=0 go run . -size 48 -font "Inter,sans-serif" "Some text"
go run . -png out.png -scale 2 "Hello"      # headless, to a file
```

In the window, `+`/`-` zoom, `0` resets, and `Esc` or `q` quits. Each
frame logs what it cost: glyphs drawn, glyphs rasterized, how many times
the text has been shaped and wrapped, and how full the atlases are.

## Comparing with Pango

```sh
CGO_ENABLED=0 go run ./compare -out /tmp      # compare.png and compare-zoom.png
```

This draws the same text at 11, 13, 14.67 (11 pt, GNOME's UI size), 18
and 24 px, light on dark and dark on light, in five rows per size:

1. the spike, unhinted
2. the spike with its slight hinting
3. Pango with the desktop's settings (what a GTK 3 label draws)
4. Pango, grayscale with the desktop's hinting
5. Pango, grayscale, unhinted

Each pair of rows isolates one difference. It also prints how far each
row's vertical ink profile is from row 4, which measures vertical
sharpness whatever the glyphs' horizontal positions. `compare-zoom.png` magnifies
the small sizes 5×. View `compare.png` at 100%: scaled down, the LCD rows
look tinted.

## The pieces

- `text/engine.go`: the seam. An `Engine` shapes a paragraph, falling back
  to another font for each rune the preferred one lacks, and rasterizes
  one glyph at a size and a subpixel offset. Everything else is shared code.
- `text/gotext.go`: a pure-Go engine. go-text's `fontscan` indexes the
  system's fonts (cached in `~/.cache/dfx2-text-spike`) and picks
  fallbacks. Its HarfBuzz port shapes. `x/image/vector` rasterizes
  outlines, and color emoji come from the PNG strikes of Noto Color Emoji.
- `text/system.go`: the shared part.
  - A `Paragraph` is shaped once for a size and rewrapped from the same
    runs only when its width changes. Resize the window and watch "wrapped"
    climb while "shaped" stays at 1.
  - Glyphs are rasterized on demand, keyed by face, glyph, size and one
    of 4 subpixel positions. Baselines snap to whole pixels, pens don't.
  - Slight hinting (`text/hint.go`, on by default, `-hint=false` turns it
    off) works as FreeType's autohinter does in light mode, vertically
    only. One vertical scale puts the x-height's overshoot on a pixel. The
    baseline, x-height, cap height, ascenders and descenders round to whole
    pixels, small overshoots are suppressed, and everything between is
    interpolated. Advances don't change.
  - Bitmaps are packed into a coverage atlas and a color atlas, which grow
    and track a dirty rectangle, the part a GPU renderer would upload.
  - Compositing is on the CPU into a premultiplied BGRA frame.
- `window_linux.go`: GTK 3 and cairo loaded with purego. It makes one
  callback per signature, and the window draws only when GTK asks.

## Not here (yet)

- The two-zone atlas: transient glyphs aren't evicted, the atlas only grows.
- COLR/CPAL color fonts: Segoe UI Emoji is skipped, so emoji are pinned to
  the system emoji font.
- Snapping a glyph's own horizontal stems (the bar of an `e`, a dash),
  which FreeType's autohinter does. `text/hint.go` does only its zones.
- Gamma or contrast correction when blending, subpixel (LCD)
  antialiasing.
- Carets, selection, ellipsis.
- A GPU renderer.
- Engine-neutral run types: go-text's `shaping.Output` crosses the seam,
  so a native engine would need its own types there.
