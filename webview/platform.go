//go:build !(linux && amd64)

package webview

import "github.com/michaelquigley/dfx2/internal/platform"

// platformFactory returns this platform's browser implementation: none yet.
// The Windows host arrives in its own stage.
func platformFactory() platform.BrowserFactory { return nil }

const needsGCSignal = false
