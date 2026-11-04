# Foundation stage 3: Windows

Host: Microsoft Windows 10 Pro 10.0.19045, 64-bit, amd64; Go 1.27.3 windows/amd64 with module language version 1.26; Windows PowerShell 5.1.19041.6456; Cygwin git. The session is Remote Desktop (`RDP-Tcp#0`) on a Microsoft Remote Display Adapter at 3440×1440; a Microsoft Basic Display Adapter at 1024×768 is also enumerated. Revision `0d46ac7` was checked out clean before the first run. All Go builds, tests and vet use `CGO_ENABLED=0`. Host, commands and per-case results are retained in [run/host.json](run/host.json) and [run/summary.json](run/summary.json).

Status: the automated Windows matrix below **passed natively on 2026-11-04**, nine cases, after two corrections to the runner, and [Michael's manual audition](#michaels-manual-audition) passed the same day with one recorded behavior: title updates for every window defer during an interactive resize. DPI changes could not be tested over RDP. **Stage 3 is not accepted.** Terminus did not review the Windows changes: neither Terminus, the `terminus-canon` rubric nor the grimoire is installed on this host, and absence is not a clean verdict. `unfurl` is also absent, so the Markdown authored here is unchecked. The Linux return checks have not run.

The [Linux preparation](#linux-preparation) section is the record the Linux session handed over; its cross-build facts stand but are superseded as Windows evidence by the native runs below.

## Portable and ABI checks, executed on Windows

```powershell
$env:CGO_ENABLED = "0"
go test ./... -count=1 -timeout=120s
go vet ./...
go build -o "$env:TEMP\dfx2-foundation-audition\lifetime.exe" ./examples/lifetime
go test ./internal/platform/win32 -count=1 -v
```

All pass. The root package, `internal/appcore` and `internal/platform/win32` report `ok`; vet is clean; the example builds. The twelve `win32` tests ran on Windows itself: x64 layouts, syscall argument counts, creation-close reentrancy with handle reuse, failed-sizing rollback, rearming wakes yielding to input and paint, wakes inside native creation waiting for the pump, unexpected `WM_QUIT`, native close leaving other windows and dispatch alive, final wake failure with concurrent coalescing, `GetMessage` failure recovery, DPI unit separation and dispatch-release failure retention. They still use injected calls; the native matrix below is the execution evidence.

## Native automated matrix

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\foundation\windows\audition.ps1
```

[audition.ps1](audition.ps1) repeats the tests, vet and builds, records the host, then runs each case with a 20-second kill backstop. Fixture scripts keep their 15-second cooperative deadline. Logs are UTF-8 under [run/](run/): `<case>.jsonl`, `<case>.stderr.log` (all empty), `process-inspection.json`. Binaries build into `%TEMP%\dfx2-foundation-audition\`.

| Case | Result |
| --- | --- |
| lifecycle-window | A closed after 3 applied updates; B went from 3 to 6 while the dispatch target stayed alive; closing B drained everything; exit 0 |
| lifecycle-resident | A and B closed with the app task alive at zero windows and one live dispatch target; C reopened, applied 3 updates, closed; explicit quit drained; exit 0 |
| idle-window, idle-resident | After both counter tasks paused and completions drained, one second recorded 0 application wakes and 0 wake dispatches; exit 0 |
| failure | `Run` returned `controlled lifetime task failure` after ownership drained; exit 1 as expected |
| cancel | `Run` returned `context canceled` after cleanup; exit 0 |
| create-close | A real `CreateWindowExW` call received close through the internal hook before `Create` returned; no counter task attached; drained; exit 0 |
| signal | Default mode under [ctrlbreak](ctrlbreak/main.go): a `CTRL_BREAK_EVENT` generated through the console subsystem after 3 applied updates per window; both windows closed, `Run` returned `context canceled`; exit 0 |
| inspection | Resident instance with two open windows: 31 loaded modules, all under `C:\WINDOWS`; no WebView2, Edge or Chromium module; no descendant processes; `quit` through the console task drained; exit 0 |

Every final `drained` record has zero content windows, tasks, queued callbacks, native completions, native windows, retired records and dispatch targets, with `terminal` true and `release_pending` false. The lifecycle records show the dispatch target still registered after each per-window close and at zero resident windows, so ordinary window destruction did not post application quit. This is ownership evidence, not proof about every user32 resource.

The inspected module list includes `combase.dll`, `OLEAUT32.dll`, `MSCTF.dll`, `textinputframework.dll` and `CoreMessaging.dll`. Those arrive with `user32`'s text-services and input machinery when a window is created; the root module contains no COM initialization (nothing references `CoInitialize`, `ole32` or `combase`), and the experiments that do are separate modules. They are recorded, not described as browser or COM use by this runtime.

## Runner corrections made on Windows

The PowerShell runner had never executed before this session. Two changes were needed; the Go runtime, backend and example were not changed.

1. **Console input byte order mark.** The first run's inspection case timed out: the fixture logged `console-command` (unrecognized input) the moment `quit` was sent, never quit, and was killed at 20 seconds. A byte-level probe showed the fixture receiving `EF BB BF 71 75 69 74`. .NET Framework's `Process` builds the redirected stdin writer from `Console.InputEncoding`, which is UTF-8 with a preamble under code page 65001, and flushes that preamble when it enables AutoFlush at start, so even bytes written straight to the base stream arrive after a mark. The runner now sets `[Console]::InputEncoding` to UTF-8 without a signature before starting fixtures; the probe then received `quit\r\n` and the inspection case passed. The example's console reader was deliberately left as is: a keyboard user never types a mark, and tolerating one would be a Windows-host-interop decision for the operator, not a runner fix.
2. **Console-signal case.** The handoff listed console-signal delivery as unproven. [ctrlbreak](ctrlbreak/main.go), a separate module like the experiments, starts the fixture in its own process group on the runner's hidden console, waits for applied updates, calls `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, group)`, relays the fixture's output and exits with its code. Go maps that event to `os.Interrupt`, which the example turns into context cancellation. It exercises the console subsystem's delivery into the process, not keyboard input, console close, logoff or shutdown. The runner also now echoes the inspection result like the other cases.

## Michael's manual audition

Michael ran these from a native Windows terminal in the same RDP session, with the example built to `%TEMP%`:

```powershell
$env:CGO_ENABLED = "0"
go build -o "$env:TEMP\dfx2-lifetime.exe" .\examples\lifetime
& "$env:TEMP\dfx2-lifetime.exe"
& "$env:TEMP\dfx2-lifetime.exe" -pause-after 20s
& "$env:TEMP\dfx2-lifetime.exe" -resident
```

1. Default mode, native close of A while B updated, resize, close of B: the `drained` line arrived. Michael observed that **resizing either A or B pauses both windows' title updates** until the resize ends. This is the documented behavior of the Win32 backend, not a lost update: `WM_APP` only latches readiness in the window procedure, and public callbacks run when control returns to `Pump`. An interactive resize or move runs inside `DefWindowProc`'s modal size loop, so the pump does not regain control until the mouse is released, and every owner's queued callbacks wait. GTK does not behave this way. A roadmap card records the follow-up; no runtime change was made during the audition.
2. `-pause-after 20s`: "works fine." Updates stopped with both windows open; resizing and closing a paused window did not resume anything.
3. `-resident`: "success." Both windows closed, `status` showed zero windows with the console service alive, `open` produced an updating window, `quit` drained.
4. Default mode with a keyboard Ctrl+C in the terminal. Michael's final lines:

```
{"count":13,"event":"update","time":"2026-11-04T14:36:59.0366602-05:00","window":"B"}
{"event":"task-ended","task":"counter-B","time":"2026-11-04T14:36:59.0970729-05:00"}
{"event":"task-ended","task":"counter-A","time":"2026-11-04T14:36:59.0970729-05:00"}
{"applied":13,"event":"closed","time":"2026-11-04T14:36:59.102542-05:00","window":"A"}
{"dispatch_targets":1,"dispatches":29,"event":"ownership","native_completions":0,"native_problem":"","native_retired":0,"native_windows":0,"queued":0,"release_pending":true,"tasks":0,"terminal":true,"time":"2026-11-04T14:36:59.1030788-05:00","wakes":29,"windows":0}
{"applied":13,"event":"closed","time":"2026-11-04T14:36:59.102542-05:00","window":"B"}
{"dispatch_targets":0,"dispatches":29,"event":"ownership","native_completions":0,"native_problem":"","native_retired":0,"native_windows":0,"queued":0,"release_pending":true,"tasks":0,"terminal":true,"time":"2026-11-04T14:36:59.1030788-05:00","wakes":29,"windows":0}
{"dispatch_targets":0,"dispatches":29,"event":"drained","native_completions":0,"native_problem":"","native_retired":0,"native_windows":0,"queued":0,"release_pending":false,"tasks":0,"terminal":true,"time":"2026-11-04T14:36:59.1030788-05:00","wakes":29,"windows":0}
{"error":"context canceled","event":"return","time":"2026-11-04T14:36:59.1030788-05:00"}
```

   A keyboard Ctrl+C reached the fixture as cancellation; both windows closed, ownership drained to zero and `Run` returned `context canceled`. Console close, logoff and shutdown were not observed.
5. DPI: display scaling cannot be changed in this RDP session, so **DPI transitions are untested**. The `WM_DPICHANGED` path has only the portable arithmetic/protocol test behind it.

## Linux return checks

After Michael commits this tree and it is pulled on Linux: rerun `CGO_ENABLED=0 go test ./... -count=1`, `go vet ./...`, the Linux example build, the Windows and darwin cross-builds, `GOOS=windows GOARCH=amd64 go vet -C evidence/foundation/windows/ctrlbreak .`, and the affected Linux native scenarios; confirm the Windows files arrived as mode 100644. Terminus must review the Windows changes on a host that has the canon. Stage 4 does not start before those checks and Michael's stage review.

## Linux preparation

Status when handed over: the Win32 implementation was built and tested with injected calls on Linux/amd64, and cross-built and vetted for Windows/amd64, with no Windows execution. Stage 2 was accepted and committed as `b2983e4`; its Linux observations remain in [the stage 2 record](../linux/README.md). The common shutdown changes made during stage 3 have a separate [Linux native preflight](../linux/stage-3-preflight/summary.json); those results do not stand in for Windows.

With Go 1.27.1 linux/amd64 and `CGO_ENABLED=0`, these checks passed:

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

The Go x64 layout/offset checks follow the SDK definitions of [WNDCLASSEXW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-wndclassexw), [MSG](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-msg) and [CREATESTRUCTW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-createstructw), using Windows LLP64 scalar widths. A source check compares every raw syscall argument count with the SDK function declarations. The implementation relies on the documented [WM_CREATE failure rollback](https://learn.microsoft.com/en-us/windows/win32/winmsg/wm-create), [GetMessage return/error semantics](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getmessagew), [PeekMessage ordering/filtering](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-peekmessagew) and [WM_DPICHANGED rectangle contract](https://learn.microsoft.com/en-us/windows/win32/hidpi/wm-dpichanged).

The common release path stops wake retries before native target release while retaining independent diagnostics until release completes. The Linux Wayland regression matrix passed all eight cases with the updated core; logs, counters and module mappings are in `../linux/stage-3-preflight/`.

Terminus reviewed the Linux preparation tree in round `21fb27e3d2bc`: clean, no blocking or advisory findings, no uncovered files. That is the preparation review, not Windows acceptance, and it predates the runner changes above.
