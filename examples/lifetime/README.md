# Lifetime example

Two blank native windows demonstrate shared application lifetime, independent window tasks and cooperative shutdown. Window titles update at about 10 Hz. No widgets, paint API, text engine or browser integration is involved.

Linux/amd64 requires the system GTK 3 runtime and a desktop display. No development headers or cgo are needed to run. Windows/amd64 selects the Win32 backend without a browser loader or COM initialization. Its scripted matrix and [Michael's audition](../../evidence/foundation/windows/README.md) have run natively; note that an interactive resize defers every window's title updates there.

```sh
CGO_ENABLED=0 go run ./examples/lifetime
CGO_ENABLED=0 go run ./examples/lifetime -pause-after 20s
CGO_ENABLED=0 go run ./examples/lifetime -resident
```

Close A while B's title counter continues, resize B, then close the final window. `-pause-after` stops each window's counter task after the interval, leaving its window open and idle. In resident mode, closing all windows preserves the app-owned console task. Type `open` to create another window, `status` for an ownership/wake snapshot, and `quit` to cancel and drain. End-of-file ends the console task but does not implicitly quit the resident application. Ctrl+C or SIGTERM cancels `Run` and drains owned work.

Output is JSON lines. `opened`, `update`, `paused`, `closed`, ownership snapshots and the final `drained`/`return` records distinguish accepted posts, applied titles and completed cleanup. Pure caller cancellation is logged with its context error and exits 0; a task/backend failure exits 1. Invalid arguments exit 2. A blocking terminal reader is deliberately outside registered task ownership; it may remain blocked until the executable exits.

## Bounded scripts

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-lifetime ./examples/lifetime
timeout 20s /tmp/dfx2-lifetime -script lifecycle
timeout 20s /tmp/dfx2-lifetime -resident -script lifecycle
timeout 20s /tmp/dfx2-lifetime -script idle
timeout 20s /tmp/dfx2-lifetime -resident -script idle
timeout 20s /tmp/dfx2-lifetime -script failure
timeout 20s /tmp/dfx2-lifetime -script cancel
timeout 20s /tmp/dfx2-lifetime -script create-close
```

`lifecycle` waits for applied title updates, closes A, observes further updates on B and closes B. In resident mode it then reopens C, observes updates, closes C and explicitly quits. `idle` pauses both window tasks, waits for completion/wakes to drain, and measures one second with zero application wakes or wake dispatches. Compositor events are outside that counter. `failure` must exit 1 with `controlled lifetime task failure`; `cancel` must report `context canceled` after cleanup. `create-close` uses an internal backend hook to notify close while a real native widget's creation is still in progress, then verifies cancellation and drain. It does not audition browser creation or teardown.

Scripts have a 15-second cooperative context deadline. The outer 20-second timeout is an operator backstop, not a framework cleanup deadline. See the [Linux evidence and driver](../../evidence/foundation/linux/README.md) for retained commands, exit codes and observations.
