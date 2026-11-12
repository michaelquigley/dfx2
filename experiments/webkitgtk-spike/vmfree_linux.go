//go:build linux

package main

// The VM-free feasibility host, following the handler-safety result: the Go
// process creates no JavaScriptCore VM and evaluates no JavaScript. Browser
// JavaScript runs only in WebKit's web processes; automation is page-driven,
// with results posted to each window's own handler (feasibility.Page's
// /api/report, or the attachment server's). Error-page actions are driven by
// synthesized GTK pointer events on the view.
//
// WebKit still initializes JSC in this process (InitializeWebKit2 calls
// JSC::initialize), which installs JSC's thread suspend/resume handler on its
// GC signal. With -signal-reserve the application's explicit reservation is
// validated and passed to the private JSConfigureSignalForGC before WebKit's
// first type is initialized; without it JSC keeps its default. Neither is an
// approved mechanism, and avoiding a VM says nothing about SIGXCPU or about
// signal coexistence in general.

import (
	"fmt"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

var gdk struct {
	widgetWindow   func(uintptr) uintptr
	eventNew       func(int32) uintptr
	eventSetDevice func(uintptr, uintptr)
	eventFree      func(uintptr)
	display        func() uintptr
	defaultSeat    func(uintptr) uintptr
	seatPointer    func(uintptr) uintptr
	mainDoEvent    func(uintptr)
	reload         func(uintptr)
	title          func(uintptr) string
}

func loadVMFree() error {
	for lib, bindings := range map[string]map[string]any{
		"libgtk-3.so.0": {"gtk_widget_get_window": &gdk.widgetWindow, "gtk_main_do_event": &gdk.mainDoEvent},
		"libgdk-3.so.0": {"gdk_event_new": &gdk.eventNew, "gdk_event_set_device": &gdk.eventSetDevice, "gdk_event_free": &gdk.eventFree,
			"gdk_display_get_default": &gdk.display, "gdk_display_get_default_seat": &gdk.defaultSeat, "gdk_seat_get_pointer": &gdk.seatPointer},
		"libwebkit2gtk-4.1.so.0": {"webkit_web_view_reload": &gdk.reload, "webkit_web_view_get_title": &gdk.title},
	} {
		h, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return err
		}
		for name, dest := range bindings {
			addr, err := purego.Dlsym(h, name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			purego.RegisterFunc(dest, addr)
		}
	}
	return nil
}

// signalView reads the dispositions that matter to this path: the fault
// signals JSC takes over only when its first VM is created, SIGXCPU, which
// WebKit's real-time thread support installs, JSC's default GC signal and
// the reservation.
func signalView(p *probe, reserve int) map[string]disposition {
	all := p.snapshot("view")
	p.stages = nil
	out := map[string]disposition{}
	for name, sig := range map[string]int{"SIGSEGV": 11, "SIGBUS": 7, "SIGXCPU": 24, "SIGUSR1": 10} {
		out[name] = find(all, sig)
	}
	if reserve != 0 {
		out[fmt.Sprintf("reserved %d", reserve)] = find(all, reserve)
	}
	return out
}

// reserveGCSignal validates the application's reservation and configures
// JSC with it. It must run before WebKit initializes JSC. It binds only
// libc's real-time range and the configuration entry point: no context, VM
// or evaluation function is looked up.
func reserveGCSignal(p *probe, sig int) (map[string]any, error) {
	libc, err := purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, err
	}
	var rtmin, rtmax func() int32
	for name, dest := range map[string]any{"__libc_current_sigrtmin": &rtmin, "__libc_current_sigrtmax": &rtmax} {
		addr, err := purego.Dlsym(libc, name)
		if err != nil {
			return nil, err
		}
		purego.RegisterFunc(dest, addr)
	}
	p.rtmin, p.rtmax = int(rtmin()), int(rtmax())
	checks := p.validateReservation(sig, p.snapshot("before-reservation"))
	p.stages = nil
	report := map[string]any{"signal": sig, "checks": checks}
	for _, c := range checks {
		if !c.Passed {
			return report, fmt.Errorf("reservation refused: %s", c.Check)
		}
	}
	h, err := purego.Dlopen("libjavascriptcoregtk-4.1.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return report, err
	}
	addr, err := purego.Dlsym(h, "JSConfigureSignalForGC")
	if err != nil {
		return report, fmt.Errorf("configuration entry point unavailable: %w", err)
	}
	purego.RegisterFunc(&p.jsc.configure, addr)
	signal.Ignore(syscall.Signal(sig))
	if err := setDisposition(sig, kernelSigaction{}); err != nil {
		return report, err
	}
	report["configured"] = p.jsc.configure(int32(sig))
	return report, nil
}

// GdkEventButton and GdkEventMotion on linux/amd64 (GTK 3) share this
// prefix; is_hint and button occupy the same word.
type gdkPointerEvent struct {
	kind      int32
	window    uintptr
	sendEvent int8
	time      uint32
	x, y      float64
	axes      uintptr
	state     uint32
	button    uint32
	device    uintptr
	xr, yr    float64
}

const (
	gdkMotionNotify  = 3
	gdkButtonPress   = 4
	gdkButtonRelease = 7
	gdkButton1Mask   = 1 << 8
)

// click delivers motion, press and release at (x, y) in the view's
// coordinates through gtk_main_do_event, the path GTK takes for pointer
// input, on successive main-loop iterations. The page sees an ordinary
// trusted click; nothing is evaluated.
func (f *feas) click(w *fwin, x, y float64) {
	window := gdk.widgetWindow(w.view)
	pointer := gdk.seatPointer(gdk.defaultSeat(gdk.display()))
	if window == 0 || pointer == 0 {
		f.event("click", "window", w.label, "error", "no window or pointer")
		return
	}
	send := func(kind int32, state, button uint32) {
		e := gdk.eventNew(kind)
		ev := (*gdkPointerEvent)(unsafe.Add(unsafe.Pointer(nil), e)) // C memory
		ev.window = n.gObjectRef(window)                             // gdk_event_free drops it
		ev.time = uint32(time.Now().UnixMilli())
		ev.x, ev.y, ev.state, ev.button = x, y, state, button
		if kind == gdkMotionNotify {
			ev.button = 0 // is_hint
		}
		gdk.eventSetDevice(e, pointer)
		gdk.mainDoEvent(e)
		gdk.eventFree(e)
	}
	send(gdkMotionNotify, 0, 0)
	f.post.post(func() {
		if w.closing {
			return
		}
		send(gdkButtonPress, 0, 1)
		f.post.post(func() {
			if !w.closing {
				send(gdkButtonRelease, gdkButton1Mask, 1)
			}
			f.event("click", "window", w.label, "x", x, "y", y)
		})
	})
}
