// Package dfx2 provides application and independent window lifetimes.
//
// Call Run once from main. Setup and posted callbacks execute on the UI thread.
// Cancellation is cooperative: Run keeps dispatching cleanup until all registered
// tasks and native operations finish. Never wait for Window.Done on the UI thread.
// Native backends are not yet implemented; this stage returns
// ErrUnsupportedPlatform without loading native libraries.
package dfx2
