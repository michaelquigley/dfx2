package webview

import "github.com/michaelquigley/dfx2/internal/platform"

// platformFactory returns this platform's browser implementation. None is
// built yet: the Linux and Windows hosts arrive in their own stages, each
// selected here by build tag, so the root module's common startup never
// reaches them.
func platformFactory() platform.BrowserFactory { return nil }
