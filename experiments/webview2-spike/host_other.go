//go:build !(windows && amd64)

package main

import (
	"errors"
	"net/http"
	"runtime"
)

// runHost is the unsupported-platform entry point. The handler and the
// adapter build and test everywhere; the WebView2 host exists only for the
// one architecture it was tested on.
func runHost(c config, h http.Handler) error {
	return errors.New("the WebView2 host runs only on windows/amd64; this is " + runtime.GOOS + "/" + runtime.GOARCH +
		" (go test ./... still covers the handler and the adapter)")
}
