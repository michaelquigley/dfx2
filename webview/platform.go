//go:build !(linux && amd64) && !(windows && amd64)

package webview

import "github.com/michaelquigley/dfx2/internal/platform"

// platformFactory returns this platform's browser implementation: none.
func platformFactory() platform.BrowserFactory { return nil }

const needsGCSignal = false
