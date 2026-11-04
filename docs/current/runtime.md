# Foundation runtime

Stage 1 implements the portable application/window lifetime in the root Go module, `github.com/michaelquigley/dfx2` (Go 1.26). It has a private backend contract and a controllable fake, with no external dependencies, cgo, or experimental imports. Native backend selectors currently return `ErrUnsupportedPlatform` on every target, including Linux and Windows. No native libraries load, no real windows open, and the lifetime example belongs to stage 2. This is a tested lifecycle core, not an auditioned native runtime. Stage acceptance requires Terminus and Michael's review; the validation record is in [stage-1.md](../../evidence/foundation/stage-1.md).

The [foundation work order](../future/foundation-work-order.md) still governs the remaining backend and audition stages. Widgets, painting, animation, text, browser hosting and public native handles are outside this implementation.

## Public contract

Call `Run(ctx, Options, setup)` once from the main goroutine. Package initialization locks that goroutine to the startup OS thread; native library initialization will belong inside `Run`. Every second call returns `ErrAlreadyRun`, even after a failed first call. Nil context/setup and invalid modes are rejected before backend initialization. A context canceled before setup skips setup and follows cleanup.

Root types alias the concrete private core types so future content packages can share their identity. Use the `App` passed to setup and windows returned by `App.NewWindow`; their zero values are not usable owners.

| Operation | Calling context | Behavior |
| --- | --- | --- |
| `App.NewWindow(WindowOptions)` | UI thread | Synchronous creation; positive logical client width and height required |
| `App.Go(name, work)`, `Window.Go(name, work)` | UI thread | Register ownership before starting a goroutine with the owner's context |
| `Window.SetTitle(title)` | UI thread | Reject closing windows; native failure initiates application shutdown |
| `App.Post(f)`, `Window.Post(f)` | Any goroutine | Queue without waiting; never execute inline |
| `App.Quit()`, `Window.Close()` | Any goroutine | Seal admission and cancel immediately; request native cleanup without waiting |
| `App.Context()`, `Window.Context()`, `Window.Done()` | Any goroutine | Read lifetime signals; `Done` waits for all window ownership to drain |

The backend supplies the owner-thread check. Creation, task registration and title changes reject other threads with `ErrWrongThread`. There is no synchronous cross-thread wrapper or nested event-loop helper. Setup and posted functions run on the UI thread. Never block it waiting for a task, `Done`, or anything requiring UI dispatch.

`WindowApplication` is the default. After setup finishes, zero starting/open windows initiates application shutdown, even if closing windows still have work. Application tasks are canceled immediately so window tasks depending on them can finish. Setup can close its first window and create a replacement before this rule applies. Starting windows count as live.

`ResidentApplication` keeps running with zero windows until `Quit`, caller cancellation or failure. An application task can post a callback to create another window. Ordinary window close cancels only that window's tasks and preserves other windows and application services.

## Posting and ownership

Each owner admits at most 256 pending public callbacks. `Post` returns nil for acceptance, `ErrBusy` without waiting when full, and `ErrClosed` once closing begins. Nil callbacks/work are validation errors. Successful posting does not promise execution: close discards every callback that has not started. Each dispatch turn runs at most 64 callbacks across owners, preserving FIFO order within an owner and yielding back to native dispatch afterward. Cross-owner ordering is unspecified. Dequeue under the producer mutex is the callback's start point; a callback already started may finish after close.

Tasks have unique numeric IDs even when their diagnostic names repeat. A task owns its resources until its function returns and its completion is reconciled by UI dispatch. Task completion never requires a successful public post. Tasks must close their own I/O resources before returning. Services created outside `Run` remain the caller's responsibility.

Native windows are registered before creation calls that can deliver reentrant callbacks. Their states are starting, open, closing and closed. Native handles are never owner identities. Destruction and private operation completions use numeric IDs; stale or duplicate events cannot settle a replacement window reusing a handle. An internal completion is admitted before native work starts; its settlement callback receives closing state and must release late results instead of attaching them. This seam is tested with fake native operations and proves no browser teardown behavior.

Close, quit, task completion and native release use internal state independent of public queue capacity. Native teardown runs on the UI thread outside the producer mutex, allowing reentrant notifications. A window's `Done` closes exactly once after creation returns, native destruction is acknowledged, registered tasks return and private completions settle. Routing remains registered until then.

## Cooperative shutdown and failure

Application shutdown seals admission, cancels application and window contexts, and requests teardown of all windows. The backend continues pumping until ownership drains. Only the core's terminal transition permits dispatch-target release. A task that ignores cancellation or a native operation that never completes keeps `Done` and `Run` pending. There is no timeout that frees reachable objects or fabricates completion.

Unexpected setup, task, window-creation and backend failures begin application shutdown. The first application failure and distinct cleanup errors are joined on return. Creation errors are both returned by `NewWindow` and latched, including invalid dimensions, so ignoring them cannot leave a partially initialized app running. Queue-full, closed-owner and wrong-thread rejections are operation errors. Application panics at setup, posted-function and task boundaries become errors with stacks; they do not unwind into native callbacks.

Caller cancellation returns the caller context error after cleanup. Ordinary `Quit` and last-window exit return nil unless a failure occurs. Task cancellation/deadline errors are normal only when every error leaf matches the owner's context error sampled immediately after application work returns, before waiting for the lifecycle mutex. Setup uses the same classification boundary. Later cancellation while completion accounting waits cannot erase an earlier independent cancellation failure. A task returning `DeadlineExceeded` while its owner reports `Canceled` remains a failure, including wrapped and joined errors. A real error arriving during shutdown remains a failure; joining cancellation with a real error does not hide that error.

An application-level wake target serves every owner, including a resident app with zero windows. Wake ownership is coalesced under the producer mutex. Wake failure clears its pending flag and schedules one retry after 10 ms, independent of application-context cancellation. A second consecutive failure is treated as persistent: it is logged and latched, initiates cooperative shutdown and keeps retrying until dispatch recovers. No subsequent producer is needed for recovery. Retries stop before releasing the target. A permanently broken native loop may prevent return.

A background supervisor logs outstanding windows/states, task IDs/names, pending completions and wake state every five seconds during shutdown. It works while the UI thread is stalled. `Options.Logf` runs on that supervisor goroutine, outside the lifecycle mutex. It must be fast, safe alongside the caller's logging and must not call UI methods. Nil uses `log.Printf`. The runtime contains a log-hook panic as an application failure. A blocking log hook can stall diagnostics/retries and return, so it violates the hook contract. No periodic timer runs during healthy idle operation; diagnostic and retry timers exist only when needed and stop before dispatch-target release.

## Private backend contract and limits

`internal/platform` defines initialization, owner check, synchronous creation, destruction request, title change, asynchronous wake, one-turn pump and final release. It imports no implementation package. Events carry numeric IDs and owned values; permanent native trampolines and their numeric routing are obligations for the later backends. The core cannot prove native ABI or thread behavior through a fake.

Failed backend initialization releases partial resources before returning. Failed creation leaves no native window. Successful creation owes destruction acknowledgment, possibly reentrantly during creation. A destruction error still owes acknowledgment and is not permission to drop a live object. A pump failure leaves routing/dispatch valid for continued cleanup. Release removes remaining queued sources before dropping routing.

`internal/platform/fake` can fail creation/wakes/pump, delay destruction and operation completion, reuse handles and deliver creation/destruction notifications reentrantly. Tests inject the owner check and clock. Dependency tests walk transitive imports to keep platform packages independent of appcore, root and UI/content, keep browser packages out of root dependencies and prevent resolving experiment modules. Actual Linux and Windows auditions, native ABI checks and demonstration builds await their stages; Windows cross-compilation is not Windows execution.
