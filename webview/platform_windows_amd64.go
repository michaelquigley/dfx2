package webview

import (
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/webview2"
)

// platformFactory selects WebView2. The embedded loader is extracted and the
// system runtime loaded on the first Open, so only applications that open a
// browser window touch WebView2.
func platformFactory() platform.BrowserFactory { return webview2.Factory{} }

// needsGCSignal: WebView2 runs JavaScript in its own processes and needs no
// signal from the application.
const needsGCSignal = false
