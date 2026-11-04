# Foundation stage 4: final validation and handoff

Michael accepted and committed stage 3 through `405adb3`, then authorized stage 4 on 2026-11-04. This final Linux return uses that revision plus documentation and evidence changes. The runtime, backend, example, portable tests and Windows runner/helper sources are byte-for-byte unchanged from the Windows-tested `b3a7214` tree. No Go or audition-script behavior changes in stage 4 require another Windows handoff.

Stage 4 Terminus review is clean; Michael's acceptance remains pending. The fulfilled runtime work order is retired in this change; [runtime.md](../../docs/current/runtime.md) carries the built contract, and [foundation.md](../../docs/future/foundation.md) retains the subsequent mixer, animation, text and webview design. Nothing in this record claims those features exist. The operator commits; no commit or push was made by the agent.

## Final common-tree checks

Executed on Linux/amd64 with Go 1.27.1 and `CGO_ENABLED=0`. Every command below exited 0:

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=60s
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go build -o /tmp/dfx2-stage4-lifetime ./examples/lifetime
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/dfx2-stage4-lifetime.exe ./examples/lifetime
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet -C evidence/foundation/windows/ctrlbreak ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -C evidence/foundation/windows/ctrlbreak -o /tmp/dfx2-stage4-ctrlbreak.exe .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -C evidence/foundation/windows/ctrlbreak -o /tmp/dfx2-stage4-stall.exe ./stall
```

The root, appcore, GTK and Win32 test packages pass. The suite covers independent windows, last-window ordering, resident reopen, startup/reentrant/late completion, handle reuse, admission and post/close races, saturated public queues with internal cleanup, final failed-wake recovery without another producer, failures delivered after shutdown, and outstanding-work diagnostics during stalled UI dispatch and final release. Barriers and a controllable clock establish concurrency ordering; timeout guards do not arrange it. Both native backends use this same core, selected by build tags. The transitive import checks keep platform code private and independent of appcore/UI, and keep browser and experiment modules out of root dependencies.

GTK layout/signature checks and the real GLib source-priority regression pass on Linux. Win32 layout/signature/protocol tests pass here with injected calls; they also executed on Windows during stage 3. The target builds and vet above are compilation/static evidence, not Windows execution. The Go race detector is not a required no-cgo gate.

## Native acceptance matrix

The final Linux native run used:

```sh
python3 evidence/foundation/linux/drive.py /tmp/dfx2-stage4-lifetime --out evidence/foundation/linux/stage-4 --gdk-backend wayland
```

All eight cases passed; [summary.json](linux/stage-4/summary.json) retains host/session versions, commands, exits and process inspection alongside per-case JSON lines, stderr and module mappings. The Windows column below uses the nine-case [native rerun](windows/timeout-fix-run/summary.json) at `b3a7214`, not a cross-build. Its [host record](windows/timeout-fix-run/host.json), event logs and completion records are retained. The Windows process-tree timeout regression also [passed natively](windows/timeout-run/summary.json).

| Scenario | Linux Wayland | Windows 10 amd64, RDP |
| --- | --- | --- |
| Close A, keep B updating, then close B | Passed, exit 0 | Passed, exit 0 |
| Resident zero windows, reopen C, close C and quit | Passed, exit 0 | Passed, exit 0 |
| Paused window and resident modes | Both passed; zero application wakes/dispatches in measured idle interval | Both passed; zero application wakes/dispatches in measured idle interval |
| Controlled task failure | Failure retained after drain; expected exit 1 | Failure retained after drain; expected exit 1 |
| Caller cancellation | `context canceled` after drain; exit 0 | `context canceled` after drain; exit 0 |
| Close during native creation | Passed, exit 0 | Passed through actual `CreateWindowExW` callback; exit 0 |
| Executable signal handling | Real SIGTERM; canceled after drain; exit 0 | Console-subsystem CTRL_BREAK_EVENT; canceled after drain; exit 0 |
| Native-only module/process inspection | No browser mappings/children; GTK image-loader helpers retained | 31 system modules, no browser runtime or descendants in separate resident inspection |

The final Linux and retained Windows event logs were rechecked in stage 4: exactly one final `drained` event per case, zero windows/tasks/queued callbacks/native completions/native windows/retired records/dispatch targets, terminal true and release pending false. Expected exits match every case; there are no timed-out or failed cases. Every Windows completion record reports no cleanup failures. Ownership counters show the framework's accounting, not a proof of all native resources being leak-free.

Michael's [Linux manual audition](linux/README.md) passed both modes. His [Windows manual audition](windows/README.md#michaels-manual-audition) passed native close, resize, pause, resident reopen/quit and keyboard Ctrl+C. This is retained operator observation; it was not repeated by the Linux agent in stage 4.

## Accepted residuals and follow-on work

- Win32's modal resize/move loop defers public callbacks for every window until the native call returns. Posts remain bounded and may return `ErrBusy`; diagnostics run independently. The [roadmap item](../../docs/future/roadmap/dispatch-public-callbacks-during-win32-modal-size-and-move-loops.md) preserves the follow-up.
- Windows execution was through RDP on Windows 10, not a local physical display. Real display-scale transitions remain untested on both hosts; Win32 DPI tests prove arithmetic/protocol only. Native X11 was not auditioned. Console close, logoff and shutdown were not observed.
- Cooperative cancellation can leave `Run` pending for stuck owned work or native teardown. The diagnostic observer does not authorize freeing it. `Logf` must be fast and may not synchronously reenter UI work.
- GTK image-loader `bwrap`/`glycin-svg` helpers and Windows system text-services modules are recorded as observed; they are not hidden or called browser activity. Native-only observations do not validate later browser teardown.

These limitations are reflected in the [built runtime contract](../../docs/current/runtime.md). Widgets, rendering, text, animation, webview transport and product integration require subsequent work orders.

## Review

The dfx2 Terminus rubric is present and loads from the sibling canon. Before retiring the runtime work order, its project qualities were read against the implementation: lifecycle, ownership, dispatch, failure diagnostics and validation still describe the built runtime. The staged-stub clauses remain explicitly historical/scoped, and UI/webview qualities explicitly apply only when introduced. No expired exemption, missing runtime invariant or canon edit was identified.

Prior clean reviews remain indexed in the stage records. Stage 4 review `8e1fbe8512e1` was clean with one advisory: the opening of `docs/future/direction.md` still claimed nothing was built. Michael approved correcting it to identify the built runtime and link its current contract while preserving subsequent UI/webview intent. Final review `c3a5a2a15cd1` returned clean after that correction, with no findings or coverage gaps across all 37 changed files. Michael's acceptance is the subsequent operator commit.
