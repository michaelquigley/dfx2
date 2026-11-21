package webview2

// runWorker is a body worker's lifetime, shared by the Windows binding and
// the tests: initialize COM; on failure report it and use nothing. Otherwise
// run the work, which releases its COM references before returning, leave
// COM, and only then report the worker finished, so nothing the worker held,
// COM's own per-thread state included, outlives the notification.
func runWorker(initialize func() error, uninitialize func(), work func(), finished func(initErr error)) {
	if err := initialize(); err != nil {
		finished(err)
		return
	}
	work()
	uninitialize()
	finished(nil)
}
