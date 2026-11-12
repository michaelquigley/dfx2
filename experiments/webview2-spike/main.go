// Command dfx2-webview2-spike hosts the fixture page in WebView2 without
// cgo, serving one Go http.Handler either over loopback HTTP or by
// intercepting the webview's requests and dispatching them in-process.
// See README.md.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"dfx2webview2spike/fixture"
)

// inprocessOrigin is the reserved origin the in-process mode navigates to.
// .invalid can never resolve (RFC 2606), so a request that escapes
// interception fails instead of reaching some real host.
const inprocessOrigin = "https://dfx2-spike.invalid"

type config struct {
	mode          string // "loopback" or "inprocess"
	devtools      bool
	autorun       string
	userData      string
	loader        string
	runtimeDir    string
	width, height int
	breakListener time.Duration
	closeDuring   string // test hook: post WM_CLOSE just before "env" or "controller" creation

	// The integration feasibility probes (-feasibility): a separate
	// multi-window, console-driven mode. See feasibility_windows.go and
	// evidence/windows/integration-feasibility/README.md.
	feas feasConfig
}

type feasConfig struct {
	enabled     bool
	root        string // experiment-owned directory for per-window profiles
	filter      bool   // WebResourceRequested "*" enforcement: other origins never reach the network
	csp         bool   // the guard's Content-Security-Policy on owned responses
	cookie      bool   // install the loopback endpoint credential natively before navigating
	cookieFail  bool   // injected: cookie installation reports failure
	removeFail  bool   // injected: profile deletion fails after release
	closeDuring string // test hook: close the next window during "env", "controller" or "cookie"
}

func main() {
	var c config
	flag.StringVar(&c.mode, "mode", "", `transport: "loopback" (127.0.0.1:0 HTTP server) or "inprocess" (intercepted, no listener)`)
	flag.BoolVar(&c.devtools, "devtools", false, "enable devtools and open them once the page loads")
	flag.StringVar(&c.autorun, "autorun", "", `page script to run on load: "checks", "slow" or "reload" (for the audition driver)`)
	flag.StringVar(&c.userData, "user-data", "", `WebView2 user-data folder (default: webview2-userdata\<mode> beside the executable)`)
	flag.StringVar(&c.loader, "loader", "", "path to WebView2Loader.dll (default: beside the executable)")
	flag.StringVar(&c.runtimeDir, "runtime-dir", "", "fixed-version Runtime folder instead of the installed Evergreen Runtime (an empty folder simulates a missing Runtime)")
	flag.IntVar(&c.width, "width", 1000, "client width in DIPs")
	flag.IntVar(&c.height, "height", 760, "client height in DIPs")
	flag.DurationVar(&c.breakListener, "break-listener", 0, "loopback only: close the listener after this long, to exercise server failure")
	flag.StringVar(&c.closeDuring, "close-during", "", `test hook: post WM_CLOSE to the window just before "env" or "controller" creation starts`)
	flag.BoolVar(&c.feas.enabled, "feasibility", false, "run the integration feasibility probe host instead of a single window (console-driven; see evidence/windows/integration-feasibility)")
	flag.StringVar(&c.feas.root, "feas-root", "", `with -feasibility: experiment-owned directory for per-window profiles (default %TEMP%\dfx2-feasibility)`)
	flag.BoolVar(&c.feas.filter, "feas-filter", true, "with -feasibility: answer every request outside a window's origin natively, so it never reaches the network")
	flag.BoolVar(&c.feas.csp, "feas-csp", true, "with -feasibility: add the owned endpoint's Content-Security-Policy")
	flag.BoolVar(&c.feas.cookie, "feas-cookie", true, "with -feasibility: install the loopback credential cookie natively before the first navigation")
	flag.BoolVar(&c.feas.cookieFail, "feas-cookie-fail", false, "with -feasibility: injected cookie installation failure")
	flag.BoolVar(&c.feas.removeFail, "feas-remove-fail", false, "with -feasibility: injected profile deletion failure after native release")
	flag.StringVar(&c.feas.closeDuring, "feas-close-during", "", `with -feasibility: test hook, close the next opened window during "env", "controller" or "cookie"`)
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if c.feas.enabled {
		if err := runFeasibility(c); err != nil {
			log.Printf("error: %v", err)
			os.Exit(1)
		}
		return
	}
	if c.mode != "loopback" && c.mode != "inprocess" {
		fmt.Fprintln(os.Stderr, `dfx2-webview2-spike: -mode must be "loopback" or "inprocess"`)
		flag.Usage()
		os.Exit(2)
	}
	if c.breakListener != 0 && c.mode != "loopback" {
		fmt.Fprintln(os.Stderr, "dfx2-webview2-spike: -break-listener applies only to -mode loopback")
		os.Exit(2)
	}
	switch c.autorun {
	case "", "checks", "slow", "reload":
	default:
		fmt.Fprintf(os.Stderr, "dfx2-webview2-spike: unknown -autorun %q\n", c.autorun)
		os.Exit(2)
	}

	h := fixture.Handler(log.Printf)
	if err := runHost(c, h); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}
