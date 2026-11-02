//go:build linux

package main

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// GTK must be driven from the thread that started the process.
func init() { runtime.LockOSThread() }

// GTK 3 and cairo, loaded at run time: no cgo, no headers.
var (
	gtkInit                     func(argc, argv uintptr)
	gtkWindowNew                func(typ int32) uintptr
	gtkWindowSetTitle           func(w uintptr, title string)
	gtkWindowSetDefaultSize     func(w uintptr, width, height int32)
	gtkDrawingAreaNew           func() uintptr
	gtkContainerAdd             func(container, widget uintptr)
	gtkWidgetShowAll            func(w uintptr)
	gtkWidgetQueueDraw          func(w uintptr)
	gtkWidgetGetAllocatedWidth  func(w uintptr) int32
	gtkWidgetGetAllocatedHeight func(w uintptr) int32
	gtkWidgetGetScaleFactor     func(w uintptr) int32
	gtkMain                     func()
	gtkMainQuit                 func()

	gSignalConnectData func(instance uintptr, signal string, handler, data, destroy uintptr, flags int32) uint64

	cairoImageSurfaceCreateForData func(data unsafe.Pointer, format, w, h, stride int32) uintptr
	cairoSurfaceSetDeviceScale     func(surface uintptr, x, y float64)
	cairoSetSourceSurface          func(cr, surface uintptr, x, y float64)
	cairoPaint                     func(cr uintptr)
	cairoSurfaceDestroy            func(surface uintptr)
)

func load() error {
	libs := map[string]uintptr{}
	for _, name := range []string{"libgtk-3.so.0", "libgobject-2.0.so.0", "libcairo.so.2"} {
		h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return fmt.Errorf("loading %s: %w", name, err)
		}
		libs[name] = h
	}
	gtk, gobj, cairo := libs["libgtk-3.so.0"], libs["libgobject-2.0.so.0"], libs["libcairo.so.2"]
	purego.RegisterLibFunc(&gtkInit, gtk, "gtk_init")
	purego.RegisterLibFunc(&gtkWindowNew, gtk, "gtk_window_new")
	purego.RegisterLibFunc(&gtkWindowSetTitle, gtk, "gtk_window_set_title")
	purego.RegisterLibFunc(&gtkWindowSetDefaultSize, gtk, "gtk_window_set_default_size")
	purego.RegisterLibFunc(&gtkDrawingAreaNew, gtk, "gtk_drawing_area_new")
	purego.RegisterLibFunc(&gtkContainerAdd, gtk, "gtk_container_add")
	purego.RegisterLibFunc(&gtkWidgetShowAll, gtk, "gtk_widget_show_all")
	purego.RegisterLibFunc(&gtkWidgetQueueDraw, gtk, "gtk_widget_queue_draw")
	purego.RegisterLibFunc(&gtkWidgetGetAllocatedWidth, gtk, "gtk_widget_get_allocated_width")
	purego.RegisterLibFunc(&gtkWidgetGetAllocatedHeight, gtk, "gtk_widget_get_allocated_height")
	purego.RegisterLibFunc(&gtkWidgetGetScaleFactor, gtk, "gtk_widget_get_scale_factor")
	purego.RegisterLibFunc(&gtkMain, gtk, "gtk_main")
	purego.RegisterLibFunc(&gtkMainQuit, gtk, "gtk_main_quit")
	purego.RegisterLibFunc(&gSignalConnectData, gobj, "g_signal_connect_data")
	purego.RegisterLibFunc(&cairoImageSurfaceCreateForData, cairo, "cairo_image_surface_create_for_data")
	purego.RegisterLibFunc(&cairoSurfaceSetDeviceScale, cairo, "cairo_surface_set_device_scale")
	purego.RegisterLibFunc(&cairoSetSourceSurface, cairo, "cairo_set_source_surface")
	purego.RegisterLibFunc(&cairoPaint, cairo, "cairo_paint")
	purego.RegisterLibFunc(&cairoSurfaceDestroy, cairo, "cairo_surface_destroy")
	return nil
}

// gdkEventKey is the start of GdkEventKey on 64-bit platforms.
type gdkEventKey struct {
	typ       int32
	_         int32
	window    uintptr
	sendEvent int8
	_         [3]byte
	time      uint32
	state     uint32
	keyval    uint32
}

const (
	keyPlus, keyEqual, keyMinus, key0, keyQ = 0x2b, 0x3d, 0x2d, 0x30, 0x71
	keyKPAdd, keyKPSubtract, keyEscape      = 0xffab, 0xffad, 0xff1b
	cairoFormatARGB32                       = 0
)

func runWindow(a *app, title string, w, h int) error {
	if err := load(); err != nil {
		return err
	}
	gtkInit(0, 0)
	win := gtkWindowNew(0)
	gtkWindowSetTitle(win, title)
	gtkWindowSetDefaultSize(win, int32(w), int32(h))
	area := gtkDrawingAreaNew()
	gtkContainerAdd(win, area)
	base := a.size

	// purego callbacks are never freed: one per signature, made once. A real
	// backend would route them to its windows by the user data pointer.
	draw := purego.NewCallback(func(widget, cr, _ uintptr) uintptr {
		sc := gtkWidgetGetScaleFactor(widget)
		dw := gtkWidgetGetAllocatedWidth(widget) * sc
		dh := gtkWidgetGetAllocatedHeight(widget) * sc
		if dw <= 0 || dh <= 0 {
			return 1
		}
		a.render(int(dw), int(dh), float32(sc))
		s := cairoImageSurfaceCreateForData(unsafe.Pointer(&a.frame.Pix[0]), cairoFormatARGB32, dw, dh, dw*4)
		cairoSurfaceSetDeviceScale(s, float64(sc), float64(sc))
		cairoSetSourceSurface(cr, s, 0, 0)
		cairoPaint(cr)
		cairoSurfaceDestroy(s)
		runtime.KeepAlive(a.frame.Pix)
		return 1
	})
	key := purego.NewCallback(func(_ uintptr, ev *gdkEventKey, _ uintptr) uintptr {
		switch ev.keyval {
		case keyPlus, keyEqual, keyKPAdd:
			a.size = min(a.size*1.15, 400)
		case keyMinus, keyKPSubtract:
			a.size = max(a.size/1.15, 4)
		case key0:
			a.size = base
		case keyEscape, keyQ:
			gtkMainQuit()
			return 1
		default:
			return 0
		}
		gtkWidgetQueueDraw(area)
		return 1
	})
	quit := purego.NewCallback(func(_, _ uintptr) uintptr { gtkMainQuit(); return 0 })

	gSignalConnectData(area, "draw", draw, 0, 0, 0)
	gSignalConnectData(win, "key-press-event", key, 0, 0, 0)
	gSignalConnectData(win, "destroy", quit, 0, 0, 0)
	gtkWidgetShowAll(win)
	gtkMain()
	return nil
}
