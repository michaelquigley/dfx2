// Package dfx2 provides application and independent window lifetimes.
//
// Call Run once from main. Setup and posted callbacks execute on the UI thread.
// Cancellation is cooperative: Run keeps dispatching cleanup until all registered
// tasks and native operations finish. Never wait for Window.Done on the UI thread.
// Linux/amd64 uses GTK 3 loaded at Run time through purego. Other targets return
// ErrUnsupportedPlatform until their backend stage lands. Platform operations
// run on the startup OS thread; Post, Close and Quit are safe from workers.
package dfx2
