# Foundation runtime

The root Go module, `github.com/michaelquigley/dfx2` (Go 1.26), implements portable application/window lifetime, a private backend contract and a controllable fake. Stage 2 adds Linux/amd64 native GTK 3 windows through purego v0.11.1 and the [lifetime example](../../examples/lifetime/README.md). All builds use `CGO_ENABLED=0`; experiments remain independent modules. Windows and other targets currently return `ErrUnsupportedPlatform` until their backend stage lands. Native drawing, text and browser content remain future work. Stage acceptance requires Terminus and Michael's review; see the [stage 1 record](../../evidence/foundation/stage-1.md) and [Linux stage 2 record](../../evidence/foundation/linux/README.md).

The [foundation work order](../future/foundation-work-order.md) still governs the remaining backend and audition stages. Widgets, painting, animation, text, browser hosting and public native handles are outside this implementation.

## Public contract

Call `Run(ctx, Options, setup)` once from the main goroutine. Package initialization locks that goroutine to the startup OS thread; native library initialization happens inside `Run`. GTK rejects another OS thread before loading libraries. Every second call returns `ErrAlreadyRun`, even after a failed first call. Nil context/setup and invalid modes are rejected before backend initialization. A context canceled before setup skips setup and follows cleanup.

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

`internal/platform` defines initialization, owner check, synchronous creation, destruction request, title change, asynchronous wake, one-turn pump and final release. It imports no implementation package. Events carry numeric IDs and owned values. GTK uses permanent native trampolines and numeric routing; later backends must preserve that contract. The core cannot prove native ABI or thread behavior through a fake.

Failed backend initialization releases partial resources before returning. Failed creation leaves no native window. Successful creation owes destruction acknowledgment, possibly reentrantly during creation. A destruction error still owes acknowledgment and is not permission to drop a live object. A pump failure leaves routing/dispatch valid for continued cleanup. Release removes remaining queued sources before dropping routing.

`internal/platform/fake` can fail creation/wakes/pump, delay destruction and operation completion, reuse handles and deliver creation/destruction notifications reentrantly. Tests inject the owner check and clock. Dependency tests walk transitive imports to keep platform packages independent of appcore, root and UI/content, keep browser packages out of root dependencies and prevent resolving experiment modules. The Linux backend has actual native auditions and installed-header ABI checks. Windows cross-compilation is not Windows execution; its implementation and operator-mediated host audition remain stage 3.

## Linux backend and demonstration

GTK 3, GObject and GLib load on Linux/amd64 at `Run` time. A failed `gtk_init_check` returns a display/session error. Library handles remain loaded for the process: GTK registers global types and library-owned machinery, so application cleanup does not attempt `dlclose`. This is separate from window/source ownership, which drains before return.

The backend acquires the default GLib main context on the startup thread and blocks in one-turn `g_main_context_iteration` calls when idle. Its application-owned source uses `g_idle_add_full` at `G_PRIORITY_DEFAULT_IDLE` (200) and numeric userdata, coalescing pending wakes under the backend mutex. GTK resize/redraw sources run first at priorities 110/120; bounded callback batches alone would not prevent presentation starvation if the application source always rearmed at a higher priority. It never substitutes `g_main_context_invoke`, which can call on the posting thread. Source ownership clears before core dispatch so work arriving during dispatch can rearm it. Release seals posting, removes any queued source, releases routing, and relinquishes the main context after the core supervisor has stopped retries.

Four shared permanent trampolines cover idle, destroy, delete-event and scale-notification signatures. Application and window tokens come from a monotonic numeric registry; native userdata contains no Go pointers. Window records precede native creation. Close requests report to appcore and prevent GTK's default per-window close from controlling application lifetime. Interactive signals disconnect before teardown; destruction acknowledgment waits until the native signal stack returns and the backend's final object reference is released. `Done` therefore cannot precede that release. GTK owns logical client sizing; the backend retains requested logical dimensions separately from the current integer scale factor. Native rendering and full mixed-DPI presentation are outside this stage.

`examples/lifetime` opens blank A/B windows, each with one registered 10 Hz counter task posting title updates. `-pause-after` stops the counter tasks without closing windows. Resident mode owns a cancellable console consumer for `open`, `status` and `quit`; blocking stdin scanning is caller-owned and cannot hold `Run` open. Bounded scripts exercise independent close, resident reopening, paused wake counts, task failure, caller cancellation, creation-close and signals. Internal package functions provide synchronized ownership snapshots and a native creation-close hook; they add no public methods or content/plugin API.

Native validation ran in a Wayland desktop session. Michael manually auditioned both modes and reported that everything worked well. The process checks found no WebKit, JavaScriptCore or WebView2 mappings or browser children. This GTK installation does launch `bwrap`/`glycin-svg` image-loader helpers; their identities are preserved rather than described as browser processes or suppressed. Windows, native X11 and changes of display scale have not been auditioned for this stage.
