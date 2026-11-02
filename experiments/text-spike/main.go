// Command dfx2-text-spike shows the text given on its command line in a bare
// window, drawn by a Go-native text system: fonts found and shaped in Go
// (go-text), glyphs rasterized on demand into atlases, composited on the CPU
// and handed to a window without cgo: GTK through purego on Linux, Win32
// through syscall on Windows, where -meter adds a background-fed meter.
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
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"dfx2textspike/text"
)

const sample = "Hello, dfx2 — every glyph is rasterized on demand, at any size. " +
	"Fallback per rune: Ελληνικά, Кириллица, עברית, العربية, 日本語, emoji 🎛️🎚️🎹."

var (
	dark  = text.RGB{R: 0x1e, G: 0x1f, B: 0x24}
	light = text.RGB{R: 0xe8, G: 0xe6, B: 0xe3}
)

// para is a paragraph drawn at rel times the app's size.
type para struct {
	p   *text.Paragraph
	rel float32
}

type app struct {
	sys    *text.System
	paras  []para
	size   float32 // pixels per em at scale 1
	frame  text.Frame
	quiet  bool
	bg, fg text.RGB
	meter  *meterView // the -meter fixture, or nil
	perf   perf
}

// setLight picks dark text on a light background, or the reverse.
func (a *app) setLight(on bool) {
	a.bg, a.fg = dark, light
	if on {
		a.bg, a.fg = light, dark
	}
}

// render draws a frame of w×h device pixels at scale device pixels a DIP.
func (a *app) render(w, h int, scale float32) {
	a.sys.BeginFrame()
	a.frame.Resize(w, h)
	a.frame.Clear(a.bg)
	margin := int(24 * scale)
	y := margin
	if a.meter != nil {
		y = a.meter.draw(a, margin, y, w-2*margin, scale)
	}
	for _, p := range a.paras {
		y += a.sys.Draw(&a.frame, p.p, a.size*p.rel*scale, margin, y, max(w-2*margin, 1), a.fg)
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
	lightBG := flag.Bool("light", false, "dark text on a light background")
	sizes := flag.String("sizes", "", "draw the text once per size: comma-separated pixels per em at scale 1, overriding -size")
	meter := flag.Bool("meter", false, "add a synthetic level meter fed by a background worker at about 30 Hz (its window is Windows only)")
	framelog := flag.Bool("framelog", true, "log every frame's text counters (default false with -meter)")
	fonts := flag.Bool("fonts", false, "log the font each run of the text resolves to, and the glyphs it lacks")
	flag.Parse()
	framelogSet := false
	flag.Visit(func(f *flag.Flag) { framelogSet = framelogSet || f.Name == "framelog" })
	if *meter && !framelogSet {
		*framelog = false
	}

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
	rels := []float32{1}
	if *sizes != "" {
		rels = rels[:0]
		for i, f := range strings.Split(*sizes, ",") {
			v, err := strconv.ParseFloat(strings.TrimSpace(f), 32)
			if err != nil || v <= 0 {
				log.Fatalf("-sizes: bad size %q", f)
			}
			if i == 0 {
				*size = v
			}
			rels = append(rels, float32(v / *size))
		}
	}
	a := &app{sys: text.NewSystem(eng), size: float32(*size), quiet: !*framelog}
	a.setLight(*lightBG)
	for _, rel := range rels {
		for _, line := range strings.Split(msg, "\n") {
			if len(rels) > 1 {
				line = fmt.Sprintf("%g px: %s", math.Round(float64(a.size*rel)*100)/100, line)
			}
			a.paras = append(a.paras, para{text.NewParagraph(line), rel})
		}
	}
	if *fonts {
		logFonts(eng, msg)
	}
	if *meter {
		a.meter = newMeterView()
	}

	if *out != "" {
		s := float32(*scale)
		a.render(int(float32(*width)*s), int(float32(*height)*s), s)
		if err := writePNG(*out, &a.frame); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *meter && runtime.GOOS != "windows" {
		log.Fatal("the -meter window is Windows only in this spike; -png renders it anywhere")
	}
	if err := runWindow(a, "dfx2 text spike", *width, *height); err != nil {
		log.Fatal(err)
	}
}

// logFonts logs the font each shaped run of msg uses, with how many of its
// glyphs are missing (.notdef) and what kinds of glyph data the rest have:
// the engine draws outlines and PNG bitmaps, nothing else.
func logFonts(eng *text.GoText, msg string) {
	log.Printf("fonts: Latin text resolves to %q", eng.Family())
	for _, line := range strings.Split(msg, "\n") {
		rs := []rune(line)
		for _, r := range eng.Shape(rs, 32) {
			kinds := map[string]int{}
			for _, g := range r.Glyphs {
				if g.GlyphID == 0 {
					kinds["missing"]++
					continue
				}
				kinds[eng.GlyphKind(r.Face, g.GlyphID)]++
			}
			log.Printf("fonts: %q for %q: %v", eng.FaceFamily(r.Face), string(rs[r.Runes.Offset:r.Runes.Offset+r.Runes.Count]), kinds)
		}
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
