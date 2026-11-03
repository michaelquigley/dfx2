package main

import (
	"flag"
	"fmt"
	"log"
	"os"
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
	flag.Parse()
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
