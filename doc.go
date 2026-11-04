// Package dfx2 provides application and independent window lifetimes.
//
// Call Run once from main. Setup and posted callbacks execute on the UI thread.
// Cancellation is cooperative: Run keeps dispatching cleanup until all registered
// tasks and native operations finish. Never wait for Window.Done on the UI thread.
// Linux/amd64 uses GTK 3 through purego; Windows/amd64 uses Win32 through syscall.
// Native libraries initialize inside Run. Other targets return
// ErrUnsupportedPlatform. Platform operations
// run on the startup OS thread; Post, Close and Quit are safe from workers.
package dfx2
