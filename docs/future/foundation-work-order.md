# Foundation runtime work order

Reviewed work order paired with [foundation.md](foundation.md). Implement the first application-lifetime slice: a no-cgo runtime on Linux/amd64 and Windows/amd64, several native windows in one loop, owned background tasks, and ordinary versus resident application lifetime. The result is a small two-window demonstration and a reusable private core. It does not yet implement the mixer widgets, text renderer, animation helpers or webview hosts described by the broader spec.

The stages below belong to this work order. Subsequent mixer and webview work needs its own work order. Mercurius returned `ready_to_build` with no blocking findings on 2026-11-03; Michael requested the implementation handoff and will commit before launching it. All code changes stay uncommitted for the operator.

## Grounding and reuse

The root currently has no Go module or production packages. Experiments are independent modules and remain intact. Their code is evidence and a source of mechanisms to adapt, not a dependency of the new root module.

| Existing code | Carry forward | Rebuild for the foundation |
| --- | --- | --- |
| `experiments/text-spike/window_windows.go` | Locked startup thread, one window-procedure trampoline, numeric window IDs, DPI-aware creation | `WM_DESTROY` currently calls `PostQuitMessage`; one window must not terminate the application loop |
| `experiments/text-spike/window_linux.go` | Runtime GTK loading and native window setup | Callbacks are created within `runWindow`, and destroy calls `gtkMainQuit`; use permanent trampolines and per-window routing |
| `experiments/webkitgtk-spike/host_linux.go` | `gtk_init_check`, permanent callbacks, queued GLib wakeup, cancellation while pumping | Replace global `active` routing and the single-window poster with application/window ownership |
| `experiments/webview2-spike/adapt/dispatch.go` and tests | Close/post synchronization, worker completion accounting, coalescing | HTTP-specific state stays in the experiment; a failed final wake must not wait for another request to arrive |
| `experiments/text-spike/meter.go` and tests | Publish-before-wake and clearing the pending bit before reading | Reuse the race scenarios, not the ticker or rendering code; application tasks must not wait on UI callbacks to finish |

The new runtime must not import these modules, dfx, dfw, mygo, or hardware packages. Start with Go 1.26 and the experiment's purego v0.11.1. No text or browser dependencies are needed in this slice. Introduce `df` facilities only for an actual need, not to fill an otherwise empty abstraction.

## Package and file plan

| Location | Responsibility |
| --- | --- |
| Root `go.mod`, `go.sum`, `app.go`, `window.go`, `doc.go` | Module `github.com/michaelquigley/dfx2`; public API and contract documentation |
| `internal/appcore/` | Concrete application/window types, lifecycle state, task accounting, ordinary post queues and private completion handling |
| `internal/platform/` | Small backend contract and native options/events |
| `internal/platform/gtk/` | Linux bindings, shared main context, application wake source and window registry |
| `internal/platform/win32/` | Windows bindings, application dispatch window, shared message loop and window registry |
| `internal/platform/fake/` | Controllable event source, failures and delayed/reentrant callbacks for lifecycle tests |
| `internal/appcore/*_test.go`, root import test | Observable lifecycle tests and dependency-boundary enforcement |
| `examples/lifetime/` | Two native windows, worker-fed titles, resident reopen/quit and scripted audition modes |
| `docs/current/runtime.md`, example README, `evidence/foundation/` | Built contract, commands and concise per-host results |

Root public types can alias the concrete `internal/appcore` types, keeping one type identity. Later `ui` and `webview` packages can use package-private-to-the-module integration points in `appcore` without a root/content import cycle. Do not publish a generic content-plugin interface now. Platform packages depend on neither root, appcore, UI, nor webview packages; backend events carry IDs and copied values, with policy applied by appcore. A build-tagged selector in appcore chooses the real backend; tests inject the fake privately. The platform contract never imports its implementations, avoiding a contract/backend cycle.

## Initial public contract

These names are the planning proposal to implement after review. Concrete native creation remains synchronous in this slice; asynchronous content creation is represented by the private completion accounting and fake tests, not by a premature public browser API.

```go
type Mode uint8
const (
    WindowApplication Mode = iota
    ResidentApplication
)

type Options struct {
    Mode Mode
    Logf func(string, ...any) // nil uses the standard logger
}

type WindowOptions struct {
    Title string
    Width, Height int // logical client dimensions, both positive
}

func Run(context.Context, Options, func(*App) error) error

func (*App) NewWindow(WindowOptions) (*Window, error)
func (*App) Post(func()) error
func (*App) Go(name string, work func(context.Context) error) error
func (*App) Quit()
func (*App) Context() context.Context

func (*Window) Post(func()) error
func (*Window) Go(name string, work func(context.Context) error) error
func (*Window) Close()
func (*Window) Context() context.Context
func (*Window) Done() <-chan struct{}
func (*Window) SetTitle(string) error
```

`Run` is called once from the main goroutine. A platform initialization file locks that goroutine to the startup OS thread before `main`; native libraries load only inside `Run`. A second or overlapping `Run` returns `ErrAlreadyRun`. Unsupported OS/architecture combinations compile an explicit `ErrUnsupportedPlatform` path. Invalid mode, nil context/setup/work/post callbacks, and invalid dimensions fail before creating native objects or tasks. User-facing validation errors are ordinary errors with operation context.

`NewWindow`, `Go` registration and `SetTitle` are UI-thread operations; reject wrong-thread calls with `ErrWrongThread`. `Post`, `Close`, `Quit`, context access and reading `Done` are safe from any goroutine. The native backend establishes the owner thread ID; tests use an injected owner check. No synchronous cross-thread call wrapper and no nested pumping helper is included.

`Go` registers a named task before starting its goroutine, deriving context from its owner and incrementing that owner's outstanding count. It returns `ErrClosed` without starting work once the owner is closing. Names aid shutdown diagnostics; internal task IDs are unique even when names repeat. This is ownership tracking, not a worker pool. A task that owns an I/O resource must close it before returning. A service owned outside `Run` is the caller's cleanup responsibility.

`Close` and `Quit` are idempotent requests. They immediately seal the applicable ordinary admission path and cancel the applicable context; native cleanup executes on the UI thread. They do not wait. `Done` closes only after that window's native work and registered tasks drain. Never wait for it from a UI callback. An app task can use `App.Post` to open another window while resident; a window task cannot keep a window application alive after its last window has started closing.

## Lifetime and failure policy

Implement the initial failure policy proposed in the spec's application-ownership section. Recover application panics at setup, posted-callback and registered-task boundaries, preserving the stack in the error report. Never unwind a Go panic through a native callback.

Record the first non-cancellation failure and any distinct cleanup errors, joined on return. Cancellation from the caller's `Run` context returns its context error after cleanup. `Quit` and ordinary last-window exit return nil unless a real failure occurs. A task's `context.Canceled` or `context.DeadlineExceeded` is normal only if its owner context is already done; unrelated task deadlines remain errors. Preserve real errors even if they race with shutdown.

Public creation errors are returned to the caller and latched as application failures, so ignoring the returned error does not leave the app half initialized. Queue-full, closed-owner and wrong-thread errors are caller-visible operation failures, not automatic application failures. The policy is documented in the spec; sizes and mechanisms live here.

Use monotonic owner IDs, never native handles or reusable map positions as identity. Starting windows count toward application liveness. Perform all initial setup before evaluating the last-window rule; zero windows at setup completion exits in window mode and remains running in resident mode. When the last starting/open window enters closing in window mode, begin application shutdown immediately, including cancellation of app tasks. Do not wait for that window's tasks before canceling the app tasks they might depend on.

Window close seals ordinary work, cancels tasks, stops content work, disconnects interactive callbacks and begins native teardown. Retain routing state until native destruction and all tracked completions are finished. A reentrant creation/destruction callback must find a valid record in the appropriate state. Late callbacks may settle cleanup only, never reopen a window. Window `Done` and removal from the registry happen once. The native application dispatch target outlives every window and all internal completions, including periods with zero resident windows.

Application shutdown seals app admission, closes all windows, cancels app tasks and continues native dispatch until every owned count reaches zero. Do not call `gtk_main_quit` or `PostQuitMessage` from a per-window destruction callback. Only appcore's terminal transition authorizes leaving the loop and releasing its dispatch target. A shutdown diagnostic runs every five seconds while work remains, reporting window IDs/states, task names and pending completion counts. It stops when drained; this is not a deadline that releases live objects. The log hook must be fast and must not call back into the UI. Tests use a controllable clock rather than sleeping five seconds.

## Dispatch and native integration

Keep two paths: bounded public callbacks, and internal lifetime notifications. Each application/window public queue holds at most 256 pending callbacks. This is a conservative starting bound for small desktop applications, not a latency promise. Public `Post` returns `ErrBusy` without waiting when full, `ErrClosed` after closure, and otherwise returns nil for acceptance. It always queues, even on the owner thread. Process public callbacks in FIFO order per owner, at most 64 per dispatch turn, then yield to native events; cross-owner order is unspecified. Recheck closure before starting each callback. A callback already executing may finish after close is requested; unstarted callbacks are discarded.

Never put task completion, close, quit or native release behind the bounded public queue. Track these as private pending state with one completion per admitted operation. They stay deliverable during closing and cannot receive `ErrBusy`. Tasks must be able to finish without a public post succeeding. Use an application-level wake target and coalesce wakes; payloads contain no borrowed Go pointers. Reconcile pending state and clear/rearm wake ownership under the same synchronization used by producers, so a notification during draining is not lost.

Adapt the spikes' synchronization, but fix their dependency on a subsequent producer retrying a failed wake. A failed native wake clears the pending flag and starts one internal retry timer; retry after 10 ms, with at most one retry outstanding. The retry belongs to the dispatch target's cleanup lifetime, survives application-context cancellation while undelivered work remains, and stops before releasing that target. Persistent failure is logged and initiates cooperative shutdown through the latched control state; keep retrying so cleanup can resume if delivery recovers. Do not report completion or destroy the dispatch target while stranded callbacks still own resources. A permanently broken native event loop can prevent return, just as a stuck native callback can; diagnostics must expose that condition. The fake must test a single final producer followed by a failed wake and recovery without another producer.

Linux uses GTK 3 loaded through purego, with `gtk_init_check` returning a useful error when no display is available. Create permanent callback trampolines once per signature, route by numeric userdata, and never allocate a trampoline per window or operation. Use a queued GLib source as in the WebKit spike; do not substitute `g_main_context_invoke`, which may execute on the worker. An app-owned source drains all owners and remains available with zero windows. Stop/remove sources and retries before releasing application routing state. Do not load WebKit or JavaScriptCore.

Windows uses `syscall`, one permanent window procedure and numeric creation tokens. Establish per-monitor DPI awareness before creating windows; carry logical client dimensions and scale separately. Use a hidden/message-only application dispatch window that remains alive after content windows close. Route `WM_APP` wakeups there; the app controls the one message loop. Treat `GetMessage` failure as an application failure, with outstanding ownership reported rather than a clean return. No COM or WebView2 initialization is needed in this slice. Preserve creation records before calling `CreateWindowEx`, since it calls the window procedure synchronously.

Native windows only need title, client dimensions, show, close and native event processing here. Leave them blank; the title identifies the window and shows a worker update counter. Do not extract a public paint API just for this demonstration. DPI changes must keep sizes coherent, but full mixed-DPI presentation remains later work.

## Stages and acceptance

### Stage 1: portable lifecycle core

Create the module, public types, appcore and the platform/fake contract. Keep native selection as explicit unsupported stubs until its backend stage lands. The fake can delay completion, invoke callbacks reentrantly during creation/destruction, fail creation/wakeup and reuse a fake native handle. Root APIs must remain testable on a host with no display or native libraries.

Tests prove externally meaningful outcomes: closing one window preserves the other; last-window close cancels app tasks before waiting for window tasks; resident mode survives zero windows and reopens; setup with zero windows follows the mode; close during startup never attaches late results; late callbacks cannot target a new window reusing a handle; task registration races cannot lose accounting; post/close races discard unstarted callbacks; saturation cannot block close or completion; retry wakes the final queued item; a non-cooperative task keeps `Done`/`Run` pending and appears in diagnostics until explicitly released by the test; task failure/panic returns an error after all other cooperative work drains. Each test releases its own intentionally blocked work.

Test the private native-completion seam with the fake, including a callback that itself arrives reentrantly during destruction. Do not claim this proves browser teardown. Use barriers and the fake clock; wall-clock deadlines may guard hangs, not determine scheduling correctness. Enforce transitive import restrictions so a platform package cannot reach UI/appcore through a helper, and verify no root dependency resolves to an experiment module.

### Stage 2: Linux backend and demonstration

Implement GTK and `examples/lifetime`. Default mode opens windows A and B with distinct titles. One task per window posts a counter update at roughly 10 Hz; each task exits on context cancellation and handles `ErrClosed` without waiting for its post to run. A `-pause-after` option stops updates without closing windows so idle behavior is observable. In resident mode, an app-owned task offers a console command channel for `open` and `quit`, and closing both windows keeps that channel alive. Keep potentially blocking stdin reading outside registered tasks; a cancellable app task consumes its messages so stdin cannot hold up `Run` on exit.

Also provide bounded scripted modes that close A, verify B keeps updating, close B, reopen C in resident mode and explicitly quit. Emit structured or unambiguous event lines, with live-window/task counts at shutdown. Include a controlled task-failure case and close during creation through a test hook; diagnostic hooks remain internal to the example or tests, not new public API. The executable handles termination signals by canceling `Run`'s context and reports the resulting error appropriately.

On the Linux desktop, manually close A while B updates, resize B, pause updates, close the final window, and repeat in resident mode with reopening. Run automated scripts under a timeout and retain counters and exit status. Verify no WebKit/JSC library mapping or browser child exists. Paused operation must show zero application wakeups after queued work drains; compositor events are not an application polling timer.

### Stage 3: Windows backend and host handoff

Implement Win32 against the same public contract and tests. Cross-build on Linux, then have the operator commit and check out the same revision on the Windows development host. The fresh Windows implementer reads both artifacts and runs the portable tests natively before the GUI audition. Exercise the same two-window, resident reopen, pause, signal/cancel and failure scenarios. Verify no WebView2 runtime is loaded and ordinary window close never posts application quit by itself.

Retain OS/architecture, Go version, display/session details, commands, exit codes and the event log in `evidence/foundation/windows/`. Distinguish RDP observation from local display behavior; if DPI cannot change, say so. The Windows agent leaves changes for the operator to commit. Pull them back on Linux and rerun the portable tests and both target builds before continuing. Do not migrate the application to Windows-only semantics to make this stage pass.

### Stage 4: return checks and handoff

Run the full acceptance matrix against the final common tree on both platforms, rerunning only affected scenarios after fixes. Confirm both mode examples use the same lifecycle core. Write `docs/current/runtime.md` from what shipped, including public method thread requirements, queue acceptance versus execution, cooperative shutdown, failure policy and current platform limits. Add a concise example README and evidence index with unresolved platform observations. Update root orientation and changelog to acknowledge the root module now contains code. Keep the broader foundation spec's unrealized mixer/webview intent; do not delete it as though this work order implemented the whole framework.

## Verification and review

All root builds/tests use `CGO_ENABLED=0`. Run `go test ./...`, `go vet ./...` and build `./examples/lifetime` on Linux and Windows amd64. From Linux cross-build the Windows example and run Windows-targeted vet; Windows tests must execute on the Windows host, not be counted as run by cross-compilation. Build an unsupported target such as darwin/amd64 to verify its stub compiles. The race detector is not a required gate under `CGO_ENABLED=0`; portable concurrency tests use explicit ordering and adversarial close/wake scenarios instead. Native ABI declarations need focused layout/signature checks against headers or SDK definitions used by the implementer.

Native auditions are essential: the fake cannot verify toolkit thread ownership or OS message-loop behavior. A passing host exit counter is not browser-child evidence; this slice intentionally has no browser. Preserve its native-only dependency test when later hosts arrive.

Each implementation stage goes through Terminus and Michael's review. The dfx2 rubric lives at `../terminus-canon/projects/dfx2/rubric.yaml`; it was established and successfully loaded on 2026-11-03. Ensure the shared canon is available on the reviewing host. Rubric validation is not a code-review verdict. Review findings are triaged with Michael one at a time under the grimoire convention.

## Completion boundary

Done means the shared application/window lifetime is implemented, tested and auditioned on both hosts, the resident and window modes behave as specified, and the built contract is documented. It does not mean the full immediate-mode framework, animation, text, webview or sessionmixer integration is complete. Those remain the next consumers of this foundation. Root code must never silently import experimental modules to make an example appear further along.
