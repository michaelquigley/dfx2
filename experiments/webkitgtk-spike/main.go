package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const inprocessOrigin = "dfx2://spike"

type config struct {
	windowClose                                      bool
	mode, autorun, snapshot                          string
	devtools                                         bool
	dockDevtools                                     bool
	exerciseExit                                     bool
	closeInspector                                   bool
	closeReading                                     bool
	quitAfter, closeBusy, resizeAfter, breakListener time.Duration
	width, height                                    int
}

func main() {
	var c config
	flag.StringVar(&c.mode, "mode", "", "loopback or inprocess")
	flag.StringVar(&c.autorun, "autorun", "", "fixture script: checks, slow, or reload")
	flag.StringVar(&c.snapshot, "snapshot", "", "write a webview snapshot before timed exit (PNG)")
	flag.BoolVar(&c.devtools, "devtools", false, "enable and open the inspector")
	flag.BoolVar(&c.dockDevtools, "dock-devtools", false, "audition: open and dock the inspector")
	flag.BoolVar(&c.exerciseExit, "exercise-exit", false, "audition: repeat checks, scroll, focus and resize before timed exit")
	flag.BoolVar(&c.closeInspector, "close-inspector", false, "audition comparison: explicitly close inspector before destroying window")
	flag.BoolVar(&c.closeReading, "close-reading", false, "audition: close with the first native body read outstanding")
	flag.DurationVar(&c.quitAfter, "quit-after", 0, "audition: close after this duration")
	flag.DurationVar(&c.closeBusy, "close-busy", 0, "audition: close this long after /api/slow starts")
	flag.DurationVar(&c.resizeAfter, "resize-after", 0, "audition: resize/minimize/restore starting after this duration")
	flag.DurationVar(&c.breakListener, "break-listener", 0, "audition: close loopback listener after this duration")
	flag.IntVar(&c.width, "width", 1000, "window width")
	flag.IntVar(&c.height, "height", 760, "window height")
	flag.BoolVar(&c.windowClose, "window-close", false, "audition: timed exit through GTK delete-event")
	feasible := flag.Bool("feasibility", false, "feasibility passes B-D: a multi-window host driven by console commands (see feasibility.py)")
	var fc feasConfig
	flag.BoolVar(&fc.filter, "feas-filter", true, "with -feasibility: native content filter limiting loads to each window's origin")
	flag.BoolVar(&fc.csp, "feas-csp", true, "with -feasibility: owned endpoints add a same-origin Content-Security-Policy")
	flag.BoolVar(&fc.cookie, "feas-cookie", true, "with -feasibility: owned loopback endpoints require a native-installed credential cookie")
	flag.BoolVar(&fc.cookieFail, "feas-cookie-fail", false, "with -feasibility: injected cookie installation failure")
	flag.BoolVar(&fc.vmfree, "feas-vmfree", false, "with -feasibility: create no host JavaScriptCore VM; page-driven automation only (see vmfree.py)")
	flag.BoolVar(&fc.xcpu, "feas-xcpu", false, "with -feas-vmfree: subscribe to SIGXCPU through os/signal before WebKit starts; record scheduling and limits (see xcpu.py)")
	flag.StringVar(&fc.root, "feas-root", filepath.Join(os.TempDir(), "dfx2-feasibility"), "with -feasibility: experiment-owned directory for compiled filters")
	signalProbe := flag.String("signal-probe", "", "feasibility pass A, one disposable process: baseline, configured, configured-onstack, configured-onstack-all, late, env, no-symbol or no-signal")
	signalFault := flag.String("signal-fault", "", "with -signal-probe: after JSC initialization, recover a Go fault: segv or bus")
	signalWebKit := flag.Bool("signal-webkit", false, "with -signal-probe: also bring up GTK and a WebKit view running a page")
	signalOrder := flag.String("signal-order", "jsc-first", "with -signal-webkit: jsc-first (proposed) or webkit-first")
	signalReserve := flag.Int("signal-reserve", 0, "with a reserved* -signal-probe variant, or -feas-vmfree: the signal the application reserves for JSC")
	signalThreads := flag.Int("signal-js-threads", 1, "with -signal-probe: 2 also runs JavaScript on two more Go threads in one context group")
	flag.Parse()
	if *feasible {
		log.SetFlags(log.Ltime | log.Lmicroseconds)
		fc.reserve = *signalReserve
		if err := runFeasibility(fc); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *signalProbe != "" {
		run := func() error {
			return runSignalProbe(*signalProbe, *signalFault, *signalWebKit, *signalOrder, *signalThreads)
		}
		if strings.HasPrefix(*signalProbe, "hidden-") || strings.HasPrefix(*signalProbe, "reserved") {
			run = func() error { return runReservationProbe(*signalProbe, *signalReserve) }
		}
		if *signalProbe == "suspension-transition" || strings.HasPrefix(*signalProbe, "narrow") {
			run = func() error {
				return runHandlerSafetyProbe(*signalProbe, *signalReserve, *signalFault, *signalWebKit, *signalOrder)
			}
		}
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if c.dockDevtools {
		c.devtools = true
	}
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if (c.mode != "loopback" && c.mode != "inprocess") || c.width <= 0 || c.height <= 0 || (c.mode != "loopback" && c.breakListener != 0) {
		fmt.Fprintln(os.Stderr, "choose -mode loopback|inprocess, positive dimensions; -break-listener is loopback only")
		os.Exit(2)
	}
	switch c.autorun {
	case "", "checks", "slow", "reload":
	default:
		log.Fatal("unknown -autorun")
	}
	if err := runHost(c); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
