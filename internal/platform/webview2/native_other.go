//go:build !(windows && amd64)

package webview2

import "errors"

// install binds nothing here: the WebView2 host runs only on windows/amd64,
// and the webview package never selects this factory elsewhere. The portable
// state machine is still tested on any platform over a recorded api.
func install() error { return errors.New("the WebView2 host runs only on windows/amd64") }

// Notices returns the loader's redistribution texts; they are embedded only
// in Windows builds.
func Notices() (license, notice string) { return "", "" }
