//go:build linux

// Command compare draws the same text with the spike's pure-Go engine and
// with Pango, as GTK 3 draws it, at several sizes, light on dark and dark on
// light, into compare.png, with the small sizes magnified 5× into
// compare-zoom.png. Five rows per size, each pair isolating one difference:
//
//  1. dfx2 spike: grayscale, unhinted, quarter-pixel pen positions
//  2. dfx2 spike with its slight hinting: 1 vs 2 is the spike's hinting
//  3. Pango with the desktop's settings: what a GTK 3 label looks like
//  4. Pango, grayscale with the desktop's hinting: 3 vs 4 is LCD antialiasing,
//     2 vs 4 the two hintings
//  5. Pango, grayscale, unhinted: 4 vs 5 is Pango's hinting, 1 vs 5 the
//     rasterizers
//
// Run it with:
//
//	go run ./compare [-font "Ubuntu Sans"] [-out dir] [text]
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"dfx2textspike/text"
)

const sample = "Hamburgefonstiv — quick brown fox jumps 0123 Il1|O0 rn m ffi"

var sizes = []float32{11, 13, 14.67, 18, 24}

type theme struct{ bg, fg, label text.RGB }

var themes = []theme{
	{bg: text.RGB{R: 0x1e, G: 0x1f, B: 0x24}, fg: text.RGB{R: 0xe8, G: 0xe6, B: 0xe3}, label: text.RGB{R: 0x8a, G: 0x8c, B: 0x93}},
	{bg: text.RGB{R: 0xff, G: 0xff, B: 0xff}, fg: text.RGB{R: 0x1d, G: 0x1d, B: 0x1f}, label: text.RGB{R: 0x77, G: 0x77, B: 0x7c}},
}

const (
	colW   = 1000 // each theme's column
	labelW = 230  // the labels' room at the left of a column
	zoom   = 5
	zoomW  = 150 // the width of each row magnified, from the text's start
)

type row struct {
	label string
	draw  func(f *text.Frame, size float32, x, y int, t theme) int
}

// crop is a row drawn at a size to magnify.
type crop struct {
	label string
	y, h  int
}

func main() {
	log.SetFlags(0)
	family := flag.String("font", "Ubuntu Sans", "font family both engines draw with")
	out := flag.String("out", ".", "directory to write compare.png and compare-zoom.png to")
	flag.Parse()
	msg := strings.Join(flag.Args(), " ")
	if msg == "" {
		msg = sample
	}

	cache, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	eng, err := text.NewGoText([]string{*family}, filepath.Join(cache, "dfx2-text-spike"))
	if err != nil {
		log.Fatal(err)
	}
	if err := loadPango(); err != nil {
		log.Fatal(err)
	}
	hinted, err := text.NewGoText([]string{*family}, filepath.Join(cache, "dfx2-text-spike"))
	if err != nil {
		log.Fatal(err)
	}
	hinted.Hinting = true
	labels := text.NewSystem(eng)

	desk, deskName := desktopOptions()
	gray := options{antialias: antialiasGray, hint: desk.hint, metrics: metricsOn}
	unhinted := options{antialias: antialiasGray, hint: hintNone, metrics: metricsOff}
	spike := func(e text.Engine) func(*text.Frame, float32, int, int, theme) int {
		sys := text.NewSystem(e)
		para := map[float32]*text.Paragraph{}
		return func(f *text.Frame, size float32, x, y int, t theme) int {
			p := para[size]
			if p == nil {
				p = text.NewParagraph(msg)
				para[size] = p
			}
			return sys.Draw(f, p, size, x, y, colW-labelW-8, t.fg)
		}
	}
	var pangoFamily string
	pango := func(o options) func(*text.Frame, float32, int, int, theme) int {
		return func(f *text.Frame, size float32, x, y int, t theme) int {
			h, fam := pangoRow(f, msg, *family, size, x, y, o, t.fg, t.bg)
			pangoFamily = fam
			return h
		}
	}
	rows := []row{
		{"dfx2 spike (go-text)", spike(eng)},
		{"dfx2 spike + slight hinting", spike(hinted)},
		{"Pango, desktop: " + deskName, pango(desk)},
		{"Pango, grayscale + " + hintName(desk.hint), pango(gray)},
		{"Pango, grayscale, unhinted", pango(unhinted)},
	}

	var f text.Frame
	f.Resize(2*colW, 2400)
	var crops []crop
	profiles := map[float32][][]float64{} // light on dark, by size, a profile a row
	height := 0
	for ti, t := range themes {
		x0 := ti * colW
		fill(&f, x0, 0, colW, f.H, t.bg)
		y := 16
		for _, size := range sizes {
			y += labels.Draw(&f, text.NewParagraph(sizeLabel(size)), 14, x0+12, y, labelW, t.fg) + 4
			for _, r := range rows {
				labels.Draw(&f, text.NewParagraph(r.label), 11, x0+24, y+int(size*0.25), labelW-24, t.label)
				h := r.draw(&f, size, x0+labelW, y, t)
				if ti == 0 {
					profiles[size] = append(profiles[size], profile(&f, x0+labelW, y, colW-labelW-8, h, t.bg))
				}
				if ti == 0 && size <= 14.67 {
					crops = append(crops, crop{label: fmt.Sprintf("%s px · %s", trim(size), r.label), y: y, h: h})
				}
				y += h + 6
			}
			y += 14
		}
		height = max(height, y)
	}
	report(rows, profiles)
	sheet := cropFrame(&f, 0, 0, f.W, height)
	if err := writePNG(filepath.Join(*out, "compare.png"), sheet); err != nil {
		log.Fatal(err)
	}

	// The small sizes, magnified with nearest-neighbor so pixels stay pixels.
	zh := 8
	for _, c := range crops {
		zh += 18 + c.h*zoom + 10
	}
	var z text.Frame
	z.Resize(zoomW*zoom+16, zh)
	z.Clear(themes[0].bg)
	y := 8
	for _, c := range crops {
		labels.Draw(&z, text.NewParagraph(c.label), 12, 8, y, zoomW*zoom, themes[0].label)
		y += 18
		for r := range c.h * zoom {
			sy := c.y + r/zoom
			for col := range zoomW * zoom {
				sx := labelW + col/zoom
				copy(z.Pix[((y+r)*z.W+8+col)*4:][:4], f.Pix[(sy*f.W+sx)*4:][:4])
			}
		}
		y += c.h*zoom + 10
	}
	if err := writePNG(filepath.Join(*out, "compare-zoom.png"), &z); err != nil {
		log.Fatal(err)
	}
	log.Printf("family: go-text %q, Pango %q; desktop: %s", eng.Family(), pangoFamily, deskName)
}

// desktopOptions reads GNOME's font rendering settings, as GTK applies them:
// GTK 3 hints metrics to whole pixels whenever it hints.
func desktopOptions() (options, string) {
	get := func(key, def string) string {
		b, err := exec.Command("gsettings", "get", "org.gnome.desktop.interface", key).Output()
		if err != nil {
			return def
		}
		return strings.Trim(strings.TrimSpace(string(b)), "'")
	}
	aa, hint, order := get("font-antialiasing", "grayscale"), get("font-hinting", "slight"), get("font-rgba-order", "rgb")
	o := options{antialias: antialiasGray, hint: hintSlight, metrics: metricsOn, subpixel: subpixelRGB}
	switch aa {
	case "rgba":
		o.antialias = antialiasSubpixel
	case "none":
		o.antialias = 1
	}
	o.hint = map[string]int32{"none": 1, "slight": 2, "medium": 3, "full": 4}[hint]
	if o.hint == 0 {
		o.hint = hintSlight
	}
	o.subpixel = map[string]int32{"rgb": 1, "bgr": 2, "vrgb": 3, "vbgr": 4}[order]
	name := aa + " + " + hint + " hinting"
	if aa == "rgba" {
		name = "LCD (" + order + ") + " + hint + " hinting"
	}
	return o, name
}

func hintName(h int32) string {
	return map[int32]string{1: "no", 2: "slight", 3: "medium", 4: "full"}[h] + " hinting"
}

func sizeLabel(size float32) string {
	s := trim(size) + " px per em"
	if size == 14.67 {
		s += " (11 pt, the desktop's UI size)"
	}
	return s
}

func trim(v float32) string { return strconv.FormatFloat(float64(v), 'f', -1, 32) }

func fill(f *text.Frame, x, y, w, h int, c text.RGB) {
	for r := y; r < y+h && r < f.H; r++ {
		for col := x; col < x+w && col < f.W; col++ {
			p := f.Pix[(r*f.W+col)*4:]
			p[0], p[1], p[2], p[3] = c.B, c.G, c.R, 255
		}
	}
}

func cropFrame(f *text.Frame, x, y, w, h int) *text.Frame {
	c := &text.Frame{}
	c.Resize(w, h)
	for r := range h {
		copy(c.Pix[r*w*4:(r+1)*w*4], f.Pix[((y+r)*f.W+x)*4:])
	}
	return c
}

func writePNG(path string, f *text.Frame) error {
	img := image.NewRGBA(image.Rect(0, 0, f.W, f.H))
	for i := 0; i < len(f.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = f.Pix[i+2], f.Pix[i+1], f.Pix[i], 255
	}
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(w, img); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// refRow is the row the others are measured against: Pango, grayscale, with
// the desktop's hinting, which is GTK's look but for LCD antialiasing.
const refRow = 3

// profile is a row's ink summed along each scanline, normalized: how its
// text sits vertically, whatever its glyphs' horizontal positions.
func profile(f *text.Frame, x, y, w, h int, bg text.RGB) []float64 {
	lum := func(b, g, r byte) float64 { return (float64(r) + float64(g) + float64(b)) / 3 }
	l0 := lum(bg.B, bg.G, bg.R)
	p := make([]float64, h)
	var sum float64
	for r := range h {
		for c := x; c < x+w; c++ {
			px := f.Pix[((y+r)*f.W+c)*4:]
			v := lum(px[0], px[1], px[2]) - l0
			if v < 0 {
				v = -v
			}
			p[r] += v
		}
		sum += p[r]
	}
	for r := range p {
		p[r] /= sum
	}
	return p
}

// distance is how much ink two profiles put on different scanlines (0 to
// 2), at the vertical offset (within 3 pixels) where they agree best.
func distance(a, b []float64) float64 {
	best := 2.0
	for shift := -3; shift <= 3; shift++ {
		var d float64
		for i := -3; i < max(len(a), len(b))+3; i++ {
			d += math.Abs(at(a, i) - at(b, i+shift))
		}
		best = min(best, d)
	}
	return best
}

func at(p []float64, i int) float64 {
	if i < 0 || i >= len(p) {
		return 0
	}
	return p[i]
}

func report(rows []row, profiles map[float32][][]float64) {
	fmt.Printf("Distance of each row's vertical ink profile from %q (0 = the same, light on dark):\n\n", rows[refRow].label)
	fmt.Printf("%-34s", "")
	for _, size := range sizes {
		fmt.Printf("%8s", trim(size)+"px")
	}
	fmt.Println()
	for i, r := range rows {
		if i == refRow {
			continue
		}
		fmt.Printf("%-34s", r.label)
		for _, size := range sizes {
			fmt.Printf("%8.3f", distance(profiles[size][i], profiles[size][refRow]))
		}
		fmt.Println()
	}
}
