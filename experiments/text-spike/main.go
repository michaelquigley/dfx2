// Command dfx2-text-spike shows the text given on its command line in a bare
// window, drawn by a Go-native text system: fonts found and shaped in Go
// (go-text), glyphs rasterized on demand into atlases, composited on the CPU
// and handed to a GTK window opened through purego, without cgo.
//
//	CGO_ENABLED=0 go run . "Hello, dfx2 🎛️"
//	go run . -png out.png "Hello"     # headless, to a file
//
// In the window, + and - zoom (every size is rasterized on demand, never
// scaled), 0 resets, Esc or q quits. Each frame logs what it cost.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"dfx2textspike/text"
)

const sample = "Hello, dfx2 — every glyph is rasterized on demand, at any size. " +
	"Fallback per rune: Ελληνικά, Кириллица, עברית, العربية, 日本語, emoji 🎛️🎚️🎹."

var (
	bg = text.RGB{R: 0x1e, G: 0x1f, B: 0x24}
	fg = text.RGB{R: 0xe8, G: 0xe6, B: 0xe3}
)

type app struct {
	sys   *text.System
	paras []*text.Paragraph
	size  float32 // pixels per em at scale 1
	frame text.Frame
	quiet bool
}

// render draws a frame of w×h device pixels at scale device pixels a DIP.
func (a *app) render(w, h int, scale float32) {
	a.sys.BeginFrame()
	a.frame.Resize(w, h)
	a.frame.Clear(bg)
	margin := int(24 * scale)
	y := margin
	for _, p := range a.paras {
		y += a.sys.Draw(&a.frame, p, a.size*scale, margin, y, max(w-2*margin, 1), fg)
	}
	if a.quiet {
		return
	}
	st := a.sys.Stats
	log.Printf("frame %dx%d@%g size %.0fpx: drew %d glyphs, rasterized %d (%d ever); shaped %d, wrapped %d ever; atlases: mask %d glyphs %dx%d %.0f%%, color %d glyphs",
		w, h, scale, a.size*scale, st.FrameGlyphs, st.FrameRasterized, st.Rasterized, st.Shapes, st.Wraps,
		a.sys.Mask.Glyphs(), a.sys.Mask.W, a.sys.Mask.H, 100*a.sys.Mask.Used(), a.sys.Color.Glyphs())
}

func main() {
	log.SetFlags(0)
	size := flag.Float64("size", 32, "font size, in pixels per em at scale 1")
	families := flag.String("font", "sans-serif", "font families, comma-separated, in order of preference")
	out := flag.String("png", "", "render to this PNG file instead of opening a window")
	width := flag.Int("width", 900, "window or image width, in DIPs")
	height := flag.Int("height", 360, "window or image height, in DIPs")
	scale := flag.Float64("scale", 1, "device pixels a DIP, for -png")
	hint := flag.Bool("hint", true, "slight hinting: snap the font's alignment zones to whole pixels, vertically")
	flag.Parse()

	msg := strings.Join(flag.Args(), " ")
	if msg == "" {
		msg = sample
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	eng, err := text.NewGoText(strings.Split(*families, ","), filepath.Join(cache, "dfx2-text-spike"))
	if err != nil {
		log.Fatal(err)
	}
	eng.Hinting = *hint
	a := &app{sys: text.NewSystem(eng), size: float32(*size)}
	for _, line := range strings.Split(msg, "\n") {
		a.paras = append(a.paras, text.NewParagraph(line))
	}

	if *out != "" {
		s := float32(*scale)
		a.render(int(float32(*width)*s), int(float32(*height)*s), s)
		if err := writePNG(*out, &a.frame); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := runWindow(a, "dfx2 text spike", *width, *height); err != nil {
		log.Fatal(err)
	}
}

func writePNG(path string, f *text.Frame) error {
	img := image.NewRGBA(image.Rect(0, 0, f.W, f.H))
	for i := 0; i < len(f.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = f.Pix[i+2], f.Pix[i+1], f.Pix[i], f.Pix[i+3]
	}
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(w, img); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "wrote", path)
	return nil
}
