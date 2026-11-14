package webview

import (
	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/webkit"
)

// platformFactory selects WebKitGTK. Its libraries load on the first Open, so
// only applications that open a browser window load WebKit.
func platformFactory() platform.BrowserFactory { return webkit.Factory{} }

// needsGCSignal: JavaScriptCore needs an application-reserved signal.
const needsGCSignal = true
