//go:build linux

package main

import (
	"fmt"
	"unsafe"

	"dfx2textspike/text"

	"github.com/ebitengine/purego"
)

// Cairo's enums.
const (
	formatARGB32, formatRGB24 = 0, 1

	antialiasGray, antialiasSubpixel = 2, 3
	hintNone, hintSlight             = 1, 2
	metricsOff, metricsOn            = 1, 2
	subpixelRGB                      = 1

	pangoScale = 1024
)

// options is how Pango renders a row: cairo's font options.
type options struct {
	antialias, hint, metrics, subpixel int32
}

var (
	cairoImageSurfaceCreate    func(format, w, h int32) uintptr
	cairoImageSurfaceGetData   func(s uintptr) *byte
	cairoImageSurfaceGetStride func(s uintptr) int32
	cairoSurfaceFlush          func(s uintptr)
	cairoSurfaceDestroy        func(s uintptr)
	cairoCreate                func(s uintptr) uintptr
	cairoDestroy               func(cr uintptr)
	cairoSetSourceRGB          func(cr uintptr, r, g, b float64)
	cairoPaint                 func(cr uintptr)
	cairoMoveTo                func(cr uintptr, x, y float64)
	cairoFontOptionsCreate     func() uintptr
	cairoFontOptionsDestroy    func(o uintptr)
	cairoFontOptionsSetAA      func(o uintptr, v int32)
	cairoFontOptionsSetHint    func(o uintptr, v int32)
	cairoFontOptionsSetMetrics func(o uintptr, v int32)
	cairoFontOptionsSetOrder   func(o uintptr, v int32)

	pangoCairoCreateLayout        func(cr uintptr) uintptr
	pangoCairoShowLayout          func(cr, layout uintptr)
	pangoCairoContextSetOptions   func(ctx, opts uintptr)
	pangoLayoutGetContext         func(layout uintptr) uintptr
	pangoLayoutContextChanged     func(layout uintptr)
	pangoLayoutSetText            func(layout uintptr, s string, n int32)
	pangoLayoutSetFontDescription func(layout, desc uintptr)
	pangoLayoutGetPixelSize       func(layout uintptr, w, h *int32)
	pangoFontDescriptionFromStr   func(s string) uintptr
	pangoFontDescriptionSetAbs    func(desc uintptr, size float64)
	pangoFontDescriptionFree      func(desc uintptr)
	pangoFontDescriptionGetFamily func(desc uintptr) string
	pangoContextLoadFont          func(ctx, desc uintptr) uintptr
	pangoFontDescribe             func(font uintptr) uintptr
	gObjectUnref                  func(o uintptr)
)

func loadPango() error {
	open := func(name string) (uintptr, error) {
		h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return 0, fmt.Errorf("loading %s: %w", name, err)
		}
		return h, nil
	}
	cairo, err := open("libcairo.so.2")
	if err != nil {
		return err
	}
	pango, err := open("libpango-1.0.so.0")
	if err != nil {
		return err
	}
	pc, err := open("libpangocairo-1.0.so.0")
	if err != nil {
		return err
	}
	gobj, err := open("libgobject-2.0.so.0")
	if err != nil {
		return err
	}
	for _, f := range []struct {
		fn   any
		lib  uintptr
		name string
	}{
		{&cairoImageSurfaceCreate, cairo, "cairo_image_surface_create"},
		{&cairoImageSurfaceGetData, cairo, "cairo_image_surface_get_data"},
		{&cairoImageSurfaceGetStride, cairo, "cairo_image_surface_get_stride"},
		{&cairoSurfaceFlush, cairo, "cairo_surface_flush"},
		{&cairoSurfaceDestroy, cairo, "cairo_surface_destroy"},
		{&cairoCreate, cairo, "cairo_create"},
		{&cairoDestroy, cairo, "cairo_destroy"},
		{&cairoSetSourceRGB, cairo, "cairo_set_source_rgb"},
		{&cairoPaint, cairo, "cairo_paint"},
		{&cairoMoveTo, cairo, "cairo_move_to"},
		{&cairoFontOptionsCreate, cairo, "cairo_font_options_create"},
		{&cairoFontOptionsDestroy, cairo, "cairo_font_options_destroy"},
		{&cairoFontOptionsSetAA, cairo, "cairo_font_options_set_antialias"},
		{&cairoFontOptionsSetHint, cairo, "cairo_font_options_set_hint_style"},
		{&cairoFontOptionsSetMetrics, cairo, "cairo_font_options_set_hint_metrics"},
		{&cairoFontOptionsSetOrder, cairo, "cairo_font_options_set_subpixel_order"},
		{&pangoCairoCreateLayout, pc, "pango_cairo_create_layout"},
		{&pangoCairoShowLayout, pc, "pango_cairo_show_layout"},
		{&pangoCairoContextSetOptions, pc, "pango_cairo_context_set_font_options"},
		{&pangoLayoutGetContext, pango, "pango_layout_get_context"},
		{&pangoLayoutContextChanged, pango, "pango_layout_context_changed"},
		{&pangoLayoutSetText, pango, "pango_layout_set_text"},
		{&pangoLayoutSetFontDescription, pango, "pango_layout_set_font_description"},
		{&pangoLayoutGetPixelSize, pango, "pango_layout_get_pixel_size"},
		{&pangoFontDescriptionFromStr, pango, "pango_font_description_from_string"},
		{&pangoFontDescriptionSetAbs, pango, "pango_font_description_set_absolute_size"},
		{&pangoFontDescriptionFree, pango, "pango_font_description_free"},
		{&pangoFontDescriptionGetFamily, pango, "pango_font_description_get_family"},
		{&pangoContextLoadFont, pango, "pango_context_load_font"},
		{&pangoFontDescribe, pango, "pango_font_describe"},
		{&gObjectUnref, gobj, "g_object_unref"},
	} {
		purego.RegisterLibFunc(f.fn, f.lib, f.name)
	}
	return nil
}

// pangoRow draws s in family at size pixels per em with Pango and cairo,
// as GTK 3 does, onto f at (x, y), and returns its height and the family
// Pango resolved.
func pangoRow(f *text.Frame, s, family string, size float32, x, y int, o options, fg, bg text.RGB) (int, string) {
	desc := pangoFontDescriptionFromStr(family)
	defer pangoFontDescriptionFree(desc)
	pangoFontDescriptionSetAbs(desc, float64(size)*pangoScale)

	// Measure on a scratch surface, then draw on one the row's size.
	scratch := cairoImageSurfaceCreate(formatARGB32, 1, 1)
	cr := cairoCreate(scratch)
	layout := newLayout(cr, s, desc, o)
	var w, h int32
	pangoLayoutGetPixelSize(layout, &w, &h)
	font := pangoContextLoadFont(pangoLayoutGetContext(layout), desc)
	got := pangoFontDescribe(font)
	resolved := pangoFontDescriptionGetFamily(got)
	pangoFontDescriptionFree(got)
	gObjectUnref(font)
	gObjectUnref(layout)
	cairoDestroy(cr)
	cairoSurfaceDestroy(scratch)

	w = min(w, int32(f.W-x))
	surface := cairoImageSurfaceCreate(formatRGB24, w, h)
	cr = cairoCreate(surface)
	cairoSetSourceRGB(cr, float64(bg.R)/255, float64(bg.G)/255, float64(bg.B)/255)
	cairoPaint(cr)
	cairoSetSourceRGB(cr, float64(fg.R)/255, float64(fg.G)/255, float64(fg.B)/255)
	layout = newLayout(cr, s, desc, o)
	cairoMoveTo(cr, 0, 0)
	pangoCairoShowLayout(cr, layout)
	gObjectUnref(layout)
	cairoDestroy(cr)
	cairoSurfaceFlush(surface)

	// The row is opaque, so cairo can blend subpixel (LCD) glyphs as GTK does
	// on a window; RGB24 is BGRx in memory, the frame is BGRA.
	stride := int(cairoImageSurfaceGetStride(surface))
	pix := unsafe.Slice(cairoImageSurfaceGetData(surface), stride*int(h))
	for r := 0; r < int(h) && y+r < f.H; r++ {
		copy(f.Pix[((y+r)*f.W+x)*4:((y+r)*f.W+x+int(w))*4], pix[r*stride:r*stride+int(w)*4])
	}
	cairoSurfaceDestroy(surface)
	return int(h), resolved
}

func newLayout(cr uintptr, s string, desc uintptr, o options) uintptr {
	layout := pangoCairoCreateLayout(cr)
	opts := cairoFontOptionsCreate()
	cairoFontOptionsSetAA(opts, o.antialias)
	cairoFontOptionsSetHint(opts, o.hint)
	cairoFontOptionsSetMetrics(opts, o.metrics)
	cairoFontOptionsSetOrder(opts, o.subpixel)
	pangoCairoContextSetOptions(pangoLayoutGetContext(layout), opts)
	cairoFontOptionsDestroy(opts)
	pangoLayoutContextChanged(layout)
	pangoLayoutSetFontDescription(layout, desc)
	pangoLayoutSetText(layout, s, -1)
	return layout
}
