# Foundation stage 3: Windows handoff

Status: the Win32 implementation is built and tested with injected calls on Linux/amd64, and cross-built/vetted for Windows/amd64. **No Windows execution has occurred for this runtime. Stage 3 is not accepted.** The operator must commit this prepared tree and transfer the same revision to the Windows host before the fresh Windows session executes it. Stage 4 has not started.

Stage 2 was accepted and committed as `b2983e4`. Its Linux observations remain in [the stage 2 record](../linux/README.md). The common shutdown changes made during stage 3 have a separate [Linux native preflight](../linux/stage-3-preflight/summary.json); those results do not stand in for Windows.

## Linux preparation

With Go 1.27.1 linux/amd64 and `CGO_ENABLED=0`, these checks pass:

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=60s
CGO_ENABLED=0 go test ./internal/appcore ./internal/platform/win32 -count=200 -timeout=120s
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/dfx2-lifetime.exe ./examples/lifetime
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -o /tmp/dfx2-win32.test.exe ./internal/platform/win32
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
```

The compiled Windows executable/test binary have not run. `internal/platform/win32` tests use injected calls, not emulation or native Windows. They cover reentrant creation-close, creation rollback, final native-stack acknowledgment, numeric routing across handle reuse, coalescing/post failure, message retrieval failure/recovery, unexpected quit, native message servicing during continuous rearming, DPI arithmetic and retained dispatch ownership during failed release. Shared tests prove independent diagnostics remain live during a blocked release after wake retries stop.

The Go x64 layout/offset checks follow the SDK definitions of [WNDCLASSEXW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-wndclassexw), [MSG](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-msg) and [CREATESTRUCTW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-createstructw), using Windows LLP64 scalar widths. A source check compares every raw syscall argument count with the SDK function declarations; untyped `Proc.Call` would otherwise compile a wrong count. These are focused static checks, not proof of native calling behavior. The implementation relies on the documented [WM_CREATE failure rollback](https://learn.microsoft.com/en-us/windows/win32/winmsg/wm-create), [GetMessage return/error semantics](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getmessagew), [PeekMessage ordering/filtering](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-peekmessagew) and [WM_DPICHANGED rectangle contract](https://learn.microsoft.com/en-us/windows/win32/hidpi/wm-dpichanged).

The common release path now stops wake retries before native target release while retaining independent diagnostics until release completes. The Linux Wayland regression matrix passes all eight cases with the updated core. Logs/counters/module mappings are in `../linux/stage-3-preflight/`; zero idle wakes, failure propagation, cancellation, creation-close and signal checks remain intact.

## Fresh Windows implementer

Bootstrap as implementation agent from `AGENTS.md` and the grimoire's `agents/agent-roles.md`. Read recent `docs/journal/`, `docs/future/foundation.md`, `docs/future/foundation-work-order.md`, `docs/current/runtime.md` and this handoff. Honor the same ownership/error/posting contracts, no-cgo builds, permanent numeric callback routes, and the one-finding-at-a-time review convention. The operator commits; do not commit or push.

1. Have Michael check out the prepared commit on the Windows development host. Record `git rev-parse HEAD` and `git status --short`. Do not run a different revision and label its results as this tree's evidence.
2. Run the portable tests natively before GUI validation, with `CGO_ENABLED=0`. Run Windows vet, build the example, and execute its Win32 layout/protocol tests on Windows. An amd64 cross-compiled test executable is not an executed test.
3. Run the automated matrix below. The PowerShell script was authored on Linux and **has not executed here**; diagnose and retain any Windows-specific failure. It records host/Go/session/display details, commands/logs/exits, native script outcomes and a long-lived process's module/descendant inspection. It builds into `%TEMP%`, leaving binaries outside the evidence directory.
4. Complete and record the manual controls, console and signal audition below. Distinguish automated facts from Michael's observation. Note RDP versus local console. If display scaling cannot change, record DPI transitions as untested; do not manufacture physical DPI evidence with a synthetic message.
5. Re-run affected checks after fixes, keep `docs/current/runtime.md` accurate, run unfurl on authored Markdown, and take code changes through Terminus. Ensure the shared canon/rubric is present on this host; absence is not clean. Leave changes for Michael to commit.
6. After the operator's Windows commit arrives back on Linux, rerun the portable suite, Linux/Windows builds and affected native scenarios before advancing to stage 4.

## Windows commands and evidence

From the repository root in a native Windows terminal:

```powershell
$env:CGO_ENABLED = "0"
go test ./... -count=1 -timeout=120s
go vet ./...
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\foundation\windows\audition.ps1
```

[audition.ps1](audition.ps1) repeats the tests/vet/build before opening windows and writes UTF-8 logs under `evidence/foundation/windows/run/`. The lifetime scripts have cooperative 15-second deadlines; the runner's 20-second process timeout is only a hang backstop. It checks window/resident lifecycle, idle wake counts, task failure, caller cancellation and close during the real `CreateWindowExW` callback sequence. A separate resident run stays open while loaded modules and descendants are inspected for WebView2/browser runtimes, then quits through its registered console task.

Record the actual PowerShell/Go versions and any script correction rather than treating this preparation as an executed audition. Keep the host record and event log even when a case fails. Report a timed-out/killed fixture as a failed audition, not cooperative cleanup.

## Manual audition still required

Use the binary built by the script (`%TEMP%\dfx2-foundation-audition\lifetime.exe`), retaining the event log:

1. Open default mode. Close A using its native close control while B updates, resize B, then close B and confirm application exit. Observe whether title updates continue or defer during an interactive native resize; record what happened.
2. Run `-pause-after 20s`, observe updates stop while windows stay open, resize/restore the paused window, and close it. No application polling timer should resume without a producer.
3. Run `-resident`. Close both windows, type `status` and confirm zero windows with the console service still alive, type `open` and observe another updating window, then type `quit` and confirm all ownership drains.
4. Run again and send a real Ctrl+C (or an isolated Ctrl+Break). Retain the `context canceled` return, final ownership counts and exit code. The automated `-script cancel` case tests context cancellation but does not prove Windows console-signal delivery. Console close/logoff/shutdown are distinct observations; do not infer them from Ctrl+C.
5. If possible, change scale or move between monitors of different DPI and verify coherent sizing. Record display/session details and limitations. The portable DPI test is arithmetic/protocol evidence only.

All final successful `drained` records must show zero content windows/tasks/queued callbacks/native completions/native windows/retired records/dispatch targets, terminal true and release pending false. Ordinary per-window destruction must never post application quit. No browser loader, COM initialization or WebView2 runtime belongs in this slice.

## Review and live state

Terminus reviewed the Linux preparation tree in round `21fb27e3d2bc`: clean, no blocking or advisory findings, no uncovered files. This is the preparation review, not Windows acceptance. Actual Windows tests, native API behavior, console signals, the PowerShell runner and manual/DPI observations remain unverified. The prior experiment's Windows evidence is reference material, not runtime acceptance evidence. Stage 3 cannot be called complete until the Windows host and Linux return checks are done.
