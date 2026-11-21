# Webview integration stage 3: Windows browser integration

Stage 3 of the [work order](../../docs/future/webview-integration-work-order.md) hosts `webview` windows on Windows/amd64 in the system WebView2 Runtime, inside the Win32 backend's message loop, in all three modes, from one executable with the pinned loader embedded. Built behavior is in [webview.md](../../docs/current/webview.md#windows-host). Implemented on the Windows host at `59ff40e` plus the uncommitted tree recorded in the [run's environment](windows/stage-3/run-connected/environment.json); returned here as commit `d328fbb`. Linux evidence is unchanged by this stage except where the shared example and the audit type changed (below); the Linux return rebuilds and reruns.

Host: Windows 10 Pro 10.0.19045.6466, amd64, Go 1.27.3 (module language 1.26), Evergreen WebView2 Runtime 151.0.4129.93, over Remote Desktop. The session was **disconnected** while the host was implemented and first validated (`query session` state `Disc`, reporting 1920×1080 at 96 DPI) and **connected** for the final runs (`Active`, 3840×1600 at 96 DPI). Every build, test and vet uses `CGO_ENABLED=0`. Terminus is not installed on this host; review runs on the Linux return.

**Result for the returned revision `d328fbb`: the native matrix passes 16 of 16 cases in the connected session**, with the Win32 size/move loop established by a title-bar drag and both screen captures written, the spike's adjusted `error-pages` scenario passes its targeted rerun, and Michael's manual audition passed by his report (below). The earlier disconnected run, 15 of 16 with the size/move case untested, is kept beside it. *That validation predates the [return corrections](#return-corrections--2026-11-20) and the triage that followed, which have modeled tests and Windows cross-builds only; native revalidation of the current tree is pending ([Windows handoff](#windows-handoff)).*

## What was added

- **`internal/platform/webview2`**: the WebView2 attachment. The state machine (`browser.go`) is portable Go over an `api` function table (`native.go`); `native_windows_amd64.go` binds it to COM through `syscall`, and `com_windows_amd64.go` implements the COM objects the host provides: completion and event handlers with invoke-or-drop settlement, and the agile read-only `IStream` that carries response bodies. `bindings.go` holds the IIDs and vtable slots checked against the pinned `WebView2.idl`; `policy.go` the error page, actions, origin checks and navigation correlation; `loader.go`/`loaderstate.go` the embedded loader's extraction, verification, shared loading and unloading; `cleanup.go` bounded post-release removal. `sdk/` holds the embedded `WebView2Loader.dll` (1.0.4129.50 x64, SHA-256 `a9a09232c25805323d4cfb3fc8f545a190a9c8a99c93262ea99d0b88df99ec90`, Authenticode valid, file version 1.0.4129.50) with its `LICENSE.txt` and `NOTICE.txt`.
- **Win32 backend**: creates browser shells (plain top-level windows the controller fills), reports their surface, re-reports it after a move, forwards focus alone, refuses presentation, and describes the parent HWND.
- **Platform contract**: `platform.BrowserAudit`/`BrowserEvent` (shared by both hosts; `webkit.Audit` is an alias; events carry a sequence number) and the optional `platform.BrowserFocuser`, which the core calls on a shell's focus.
- **Public API**: `webview` selects the WebView2 factory on windows/amd64; `Options.LinuxGCSignal` is inert there. No new public type or method.
- **`examples/webview`**: the shared audition host now drives either engine (`open-close` for staged closes, `devtools`, profile and native counters in `status`, and which creation callbacks had completed when `Open` returned); the page's image probe is engine-neutral (Chromium reports an SVG's intrinsic width, 48, WebKit the attribute width, 24) and gained a `fetchURL` run for the response-correlation case.
- **Driver**: [`windows/drive`](windows/drive/), the integrated Windows browser matrix: sixteen cases over the real host, a collector and an attachment service, Win32 process/module/socket inspection, a title-bar drag through injected pointer input to establish the size/move loop, and best-effort screen captures.
- **Test portability**: `TestLoopbackProtocolRejectionsStayOutsideApplicationResponses` recognizes Winsock's `WSAECONNRESET` (10054) as the connection reset it already accepted (`syscall.ECONNRESET` is a placeholder on Windows); its assertions are unchanged. This was the stage 1 endpoint suite's first Windows run, and it otherwise passed.

## Decisions within the work order's latitude

- **Redistribution terms confirmed.** The SDK's `LICENSE.txt` is a BSD-style license: binary redistribution must reproduce the copyright notice, conditions and disclaimer "in the documentation and/or other materials provided with the distribution", and Microsoft's name may not be used for endorsement. The DLL, `LICENSE.txt` and `NOTICE.txt` are embedded and travel in every Windows executable that links the package (*as returned, only the DLL survived linking; corrected 2026-11-20, see [Return corrections](#return-corrections--2026-11-20)*), are kept in the repository beside the code, and are described in the current document. The root `.gitignore` now excludes this one DLL from its `*.dll` rule, so Michael's commit carries a 164 KB binary.
- **In-process origin `https://dfx2.invalid:443`** (canonical; the browser shows `https://dfx2.invalid`, a handler sees `Host` `dfx2.invalid:443`). Every window names the same origin; each window's own interception routes it. `.invalid` can never resolve, so a request that escaped interception, or an aborted one, fails rather than reaching a network host.
- **Response bodies are a host-implemented `IStream`**, not a shell memory stream, so the engine's final release of the body reports the request released and the adapter's native-copy accounting is exact. The stream claims `IAgileObject` and locks its reads, as the IDL asks; a clone shares the body and its release.
- **Request bodies are read synchronously on the UI thread**, in the adapter's granted chunks, from the stream WebView2 supplies for the already-buffered request body. The spike and this matrix read 900 KB uploads that way. A body stream that was not memory-backed would hold the UI thread for its read, bounded by the request limit plus a detection byte; no worker ever touches an apartment-bound interface. This is disclosed, not proved impossible. *Superseded 2026-11-20: bodies are now read on a COM-initialized worker; see [Return corrections](#return-corrections--2026-11-20).*
- **Correlation by generation.** The failed navigation's method comes from the document-context request; "the server answered" from a response whose URI's latest document request belongs to the current navigation generation. Fetches of the same URL, older navigations' responses and redirect statuses do not count. This implements the stage 3 response-correlation advisory without the fixture's URI-only match.
- **Dropped handlers settle through their final release**, never through a timer: `Controller.Close` releases a pending `GetCookies` handler without invoking it, and the object's last `Release` runs the settlement. An invocation and a release never both settle one operation; the host's own initial reference never counts as the engine's.
- **Profile isolation is checked against the runtime's own report.** `ICoreWebView2Profile::ProfilePath` must lie under the window's directory; a `WEBVIEW2_USER_DATA_FOLDER` override would otherwise silently share a profile.
- **Process failures**: browser, main-frame renderer and frame renderer exits are fatal (as WebKit web-process termination is on Linux); an unresponsive renderer and the auxiliary processes the engine restarts itself are recorded, not fatal.
- **Attachments observe document requests only** (for Retry's method) and get no interception; a cancelled navigation's request may still reach its destination, which the matrix records.
- **Not extracted into a shared policy package**: the Windows mechanics differ from WebKit's and the overlap is small, so `webkit` changed only by the audit alias and the event sequence number.
- **Audit events are numbered.** Both hosts keep the 64 most recent events; a driver that waits by list position breaks once a window has produced more, which the Windows error-page case did. `BrowserEvent.Seq` lets the driver tell new events from seen ones.

## Portable checks

```powershell
$env:CGO_ENABLED = "0"
go test ./... -count=1 -timeout=900s
go vet ./...
$env:GOOS = "linux"; $env:GOARCH = "amd64"; go build ./...; go vet ./...
$env:GOOS = "darwin"; $env:GOARCH = "amd64"; go build ./...; go vet ./...
$env:GOOS = "linux"; $env:GOARCH = "arm64"; go build ./...
Remove-Item Env:GOOS; Remove-Item Env:GOARCH
go test ./internal/platform/webview2 ./internal/platform/win32 ./webview ./internal/appcore -count=50 -run 'Test[^B]|TestB[^i]'
$env:GOMAXPROCS = "1"; go test ./internal/platform/webview2 ./webview ./internal/appcore -count=20 -run 'Test[^B]|TestB[^i]'; Remove-Item Env:GOMAXPROCS
go test . -run TestDependencyBoundaries -count=1
$env:WEBVIEW2_IDL = "C:\Repos\q\products\dfx2\experiments\webview2-spike\.sdk\1.0.4129.50\WebView2.idl"; go test -run IDL -v ./internal/platform/webview2
```

All passed on the Windows host: every package of the no-cgo suite, vet, the Linux and Darwin cross-builds with vet, the linux/arm64 build, fifty repetitions of the attachment, backend, public-package and core tests, twenty at `GOMAXPROCS=1`, the dependency walk (which now also requires seeing `internal/platform/webview2`), and the IDL check of every binding against the pinned SDK (two guessed `DownloadStartingEventArgs` slots were corrected by it). The race detector needs cgo and a C toolchain this host lacks; Linux runs it on the return.

The new modeled tests, all over recorded native calls and needing no Runtime:

- **Attachment state machine** (`browser_test.go`): the first navigation waits for the environment, controller, credential confirmation and core readiness; in-process windows need no credential; a close before the environment callback releases the late environment unattached and still removes the profile and loader; a close before the controller callback closes the late controller and waits for the browser process's exit; a `GetCookies` handler the engine drops settles once and lets release proceed; close settles waiting requests without a response and release does not wait for response bodies, while the request's release does; bodies are read in granted chunks and fixed refusals hold their slot until the engine frees them; oversized headers are measured without conversion; off-origin requests are answered natively and attachments only observe; error-page actions are single-use with Retry only for a GET; a new same-URI navigation retires old actions; a held older same-URL response released after a newer failure cannot suppress the error page, and a 303's status does not answer the redirect target; process-failure kinds are classified and a browser exit before close is fatal once; a profile outside the window's directory is fatal; the loader is shared, unloaded after the last user, and extracted afresh later; a missing Runtime and a synchronous creation failure leave nothing loaded; bounds and focus are kept for a controller created later; staged closes fire at each stage and close at once for a creation already in flight.
- **Policy** (`policy_test.go`): error page and action parsing, canonical origin comparison (the `.invalid` origin's effective port included), path containment, and the navigation generation's correlation rules and bounded memory.
- **Loader** (`loader_test.go`): extraction refuses a blob that is not the pin and leaves nothing, verifies the written file, and the repository's `sdk/WebView2Loader.dll` is the pinned digest; bounded removal names its residue.
- **Win32** (`browser_amd64_test.go`): a browser shell reports its surface, re-reports it after a move, forwards focus alone, paints nothing, refuses presentation, and is a browser parent until destroyed; drawn and unknown windows are not.
- **Core** (`TestShellFocusIsForwardedOnlyToAFocusingBrowser`): focus reaches an attachment that implements `BrowserFocuser` and no other input does; a plain browser is left alone.

## Native Windows matrix

```powershell
go build -o "$env:TEMP\dfx2-webview.exe" .\examples\webview
go build -o "$env:TEMP\dfx2-wvdrive.exe" .\evidence\webview-integration\windows\drive
& "$env:TEMP\dfx2-wvdrive.exe" -exe "$env:TEMP\dfx2-webview.exe" -out evidence\webview-integration\windows\stage-3\run-connected
```

Two runs are kept, both 2026-11-15, both over host executable SHA-256 `e8ba2fd66f42d0522864a61357f5092ce1837ec16ab55ddd16c800737b9e0755`, which the tree rebuilds to identically:

- [`windows/stage-3/run-connected/`](windows/stage-3/run-connected/), 21:01 local, session `Active`: **the final record, 16 of 16.**
- [`windows/stage-3/run/`](windows/stage-3/run/), earlier, session `Disc`: 15 of 16 with `size_move` untested and no captures. Its driver tried to start the loop with `SC_MOVE` by message, which started no loop in the connected session either (a first connected run with that driver, since replaced, reported the same untested case); the final driver drags the caption with injected pointer input, as the mixer arc's driver does, and retries a screen copy that fails after the window reached the display (one `GetDIBits` call returned no lines in a connected trial). Those two driver changes are the only difference between the runs.

Each case starts fresh hosts (`-resident` unless the case says otherwise), each host in its own `TEMP`, so the profile and loader directories it leaves are its own; every host's exit code, `Run` result, stderr, browser processes and temporary storage are checked, and the hosts' JSON event logs, stderr and per-case results are kept. Earlier runs of this matrix were driver development (an index-based event wait, a double unlock on failure, reports from an attached page landing at the service) and were replaced; no host code changed between the first run that exercised the host and the final one except the staged-close hook, the event sequence number and the exit-timing detail.

**16 of 16 cases pass in the connected session.** Every host exited 0 with `Run` returning nil (or the expected failure), no browser process survived its host, no `dfx2-webview2-*` directory remained, and nothing but the supervisor's shutdown diagnostic ever reached stderr.

| Case | Checks |
| --- | --- |
| `owned_basics` | Both owned transports load the page with stylesheet, image and script (origin `https://dfx2.invalid` in-process); relative fetch with an encoded query; a 1005-byte POST echo; an ordinary 404; the credential invisible to `document.cookie`; only the loopback endpoint listens (`netstat`); clients without the credential, or with a wrong one and an Origin, get 403 without reaching the handler; loopback streams its first chunk before the handler finishes; closing the in-process window leaves the loopback one listening |
| `in_process_alone` | An in-process host owns no listening socket |
| `confinement` | Loopback and in-process pages aim images, scripts, stylesheets, fetch, XHR, beacons, a WebSocket, EventSource, iframes, redirected subresources, a service worker and another loopback window's endpoint at a collector on another port: the collector receives nothing and the other endpoint's handler sees nothing. Location, redirect, popup and form navigations to the collector leave each window on its own origin with nothing collected: interception answered the cancelled navigations' requests inside the browser |
| `storage` | Two in-process windows keep separate cookies, local and session storage (cookies work on the Windows in-process origin); a navigation keeps a window's own; two loopback windows on one host share no cookies across ports; four closes and a reopened window starts empty |
| `error_pages_loopback` | A failed GET shows the error page with Retry, which repeats the GET; Return to start loads the start page; a failed POST offers no Retry, a forced Retry is refused, and Return to start adds no POST; a 303 after a POST fails as a retryable GET whose Retry adds no POST; a 307-preserved POST offers no Retry and Return to start adds none; actual 404 and 500 pages display normally |
| `error_actions_same_uri` | After a failed attached GET, the same URL recovers (the service's response changes, not its URL); the old Retry and Return are refused |
| `attachment` | Navigation and a redirect to an undeclared origin are refused and the window stays on its service; a failed GET offers Retry; a failed form POST offers none and Return to start adds no POST; the service answers after its window closed |
| `response_race` | The page's fetch of a URL is held by the service; a navigation to the same URL has its document request dropped and shows the error page with Retry; the held fetch response is then released and produces no document response, load or new navigation; the error page's Retry is still the current one and repeats the GET |
| `close_cases` | Close at each initialization stage the example can reach (controller creation in both transports, credential confirmation over loopback, and before the first navigation in both), each followed by a second window working in the same host; close with a handler that ignores cancellation, in both transports, keeps the window and `Done` pending until the handler is released; close right after a 900 KB in-process upload starts |
| `close_one_keep_one` | A window application with a native window: closing the in-process window leaves the loopback and native windows working and its browser process exits while the host continues; closing the last window ends the application |
| `lazy_loading` | A host with only a native window maps no `WebView2Loader.dll` or `EmbeddedBrowserWebView.dll` and has extracted nothing; the first browser window maps both and extracts one loader directory; the last close unmaps the loader and removes the directory |
| `profile_lifecycle` | The profile is a fresh `dfx2-webview2-profile-*` directory under the host's `TEMP` and exists while the window lives; when the window reports closed its browser process has exited and the directory is gone; nothing framework-owned remains |
| `size_move` | A host with a native window and an in-process browser window: the browser shell's caption is pressed and dragged right with injected pointer input for 4 s; the backend counted one size/move loop and eight bounded modal turns; the page's `checks` run (fetch, POST echo, 404, image) completed while the loop ran; the shell moved (left edge 78 to 237); the loop was no longer active afterwards; both windows closed clean. Without a connected session the case records itself untested instead of failing |
| `browser_failure` | Killing the window's `msedgewebview2.exe` browser process fails the application with "WebView2 browser process exited (reason unexpected, exit code 1)" and it drains and exits 1 |
| `missing_runtime` | With `WEBVIEW2_BROWSER_EXECUTABLE_FOLDER` pointing at an empty directory, a native-only application runs; the first `webview.Open` fails with "WebView2 Runtime not found (HRESULT ERROR_FILE_NOT_FOUND)" naming the installer, fatal to `Run` |
| `exe_only` | The executable copied alone into an empty directory hosts a loopback window; its loader is loaded from `dfx2-webview2-loader-*` under `TEMP`; nothing appears beside the executable |

Observations recorded beside the checks:

- **Environment creation completed synchronously** in every host: when `Open` returned, the environment callback had run and the controller's was pending (`events_at_open` 0, `asyncs_at_open` 1), so a close during environment creation is reachable only in the modeled tests. The attachment registers routing before each creation call, which is why this went unnoticed until the staged-close audition.
- **Transport redelivery**, as on Linux and in the spike: the loopback handler counted 12 GETs of the failing path for four failed GET navigations, 6 POSTs of it for two failed POST navigations (a direct POST and a 307-preserved one: Chromium re-sent each failed POST three times with no host or page action), and 1 POST to the 307 route. The attachment service counted 3 POST deliveries for one failed form submission. No action ever added a POST; Retry is judged by navigations.
- **A refused attachment navigation's request still reached its destination**: the collector recorded `GET /outside` and `GET /redirected` as document requests while the window stayed on its service. Attachments have no interception and no credential; the owned windows' confinement case recorded nothing at the collector.
- **A cancelled error-action navigation produced a document request** for `https://dfx2-error.invalid/…`, which an owned window's interception answered (`blocked`, context 1); for an attachment that host cannot resolve.
- **Nothing was cached**: an in-process `max-age=600` response fetched twice from one window reached the handler twice (`X-Count` 2), so per-window cache isolation is a claim about separate profiles, not a measured cache, as on Linux.
- **The Runtime's client library stays mapped** (`EmbeddedBrowserWebView.dll`) after the loader is unloaded and its directory removed; it is the Runtime's own file in its installation, not the extracted one.
- **Closing a window explicitly released in about 0.3 s** (`profile_lifecycle`: browser process exited and profile gone when `closed` arrived, 270 ms after the command in the disconnected run, 327 ms in the connected one). **Hosts whose open window was closed by `quit` waited more than five seconds for the browser process's exit** (two in the disconnected run, 6.6 s each; three in the connected run: `in_process_alone`, `storage`, `response_race`), as the spike saw once; the supervisor's five-second shutdown diagnostic then appeared on stderr, by design, and every such host exited clean. Not understood; no timeout was added.
- **Screen captures**: `screen-loopback.png` (the loopback audition page) and `screen-error-page.png` (the load-failure page with Retry and Return to start) in the connected run show the windows as displayed; in the disconnected run every copy failed with `BitBlt: Access is denied`.
- **A size/move loop was not started by message.** `SendMessage(WM_SYSCOMMAND, SC_MOVE)` to the shell started no loop in the connected session either; the likely reason is that the window was not the active window (the driver never made it foreground), and this was not pursued. The title-bar press establishes the loop; the backend's counters, not the driver, say whether it did.
- **Winsock reset**: the first Windows run of the stage 1 endpoint suite found only the errno spelling above.

### Not covered natively

- **A resize loop with a browser window**: the matrix drags the caption only; Michael's manual audition resizes and maximizes both windows.
- **Close during environment creation**, which the installed Runtime completes synchronously; modeled only.
- **Close while a response awaits native completion, or during a body read at a chosen point**: native timing cannot be held there; the modeled tests cover both orderings, and the matrix's upload close is best effort.
- **The in-process unknown-method Retry rule** cannot arise from a handler (a deliberate abort answers 502); it is covered portably. Loopback and attachments exercise every action natively.
- **Cache isolation** (nothing was cached), **DPI** other than 96, **lock and disconnect transitions with a browser window open**, and **the inspector**, which opens only in the manual audition.
- **A deleted-profile residue** in production: the removal-failure path is modeled (`TestCleanupResidueIsNamedAfterBoundedAttempts`); no production hook injects a deletion failure, and the spike's injected run remains the native evidence for the bounded attempts.
- **The race detector** (no cgo toolchain here).

## Spike `error-pages` rerun

The adjusted driver from `ff55964` was rebuilt from the clean checkout and run first, as the work order asks. The first attempt, kept at [`error-pages-rerun-attempt-1-disconnected/`](../../experiments/webview2-spike/evidence/windows/integration-feasibility/error-pages-rerun-attempt-1-disconnected/), is **invalid**: the session was disconnected, `SendInput` was refused and the Retry click never happened, so the run timed out after the error page had been shown with both actions. **The rerun from the connected session passes, 12 checks of 12**, in [`error-pages-rerun/`](../../experiments/webview2-spike/evidence/windows/integration-feasibility/error-pages-rerun/) and the [spike record](../../experiments/webview2-spike/evidence/windows/integration-feasibility/results.md#targeted-error-pages-rerun-of-the-adjusted-driver-2026-11-15): the clicked Retry after a 303 was one GET navigation that added no POST, the clicked Return to start after a 307-preserved POST added none, and the transport's own redelivery (2 to 3 requests per failed navigation) is recorded as the observation finding 3 accepted. The production host's error actions were also exercised through navigation policy (`error_pages_loopback`, `attachment`, `error_actions_same_uri`, `response_race`), which needs no input injection.

## Manual audition

**Performed by Michael on 2026-11-16**, from a Cygwin or cmd shell, with the executable the matrix ran (`e8ba2fd6…`), on the same host and connected session, through the [Windows checklist](../../examples/webview/README.md#manual-audition-windows): both owned windows rendering and answering Checks and Stream, Navigate outside and Open popup staying in, the ordinary 404 page, Failing page with clicked Retry and Return to start, the POST error page with Return to start alone, resizing, maximizing and title-bar drags, typing focus after switching windows, the inspector, closing the in-process window first with the others still working, the clean `return` on the last close, and the `native,loopback` reopening. His report: "everything seemed to work fine." No hang, blank window, outside page, warning or surviving browser process was reported. He did not report a display scale other than the host's 96 DPI or any note on resize flicker or drag lag; nothing was measured.

## Linux return

1. Rebuild and run the portable checks on Linux, including the Windows cross-build and vet (`GOOS=windows GOARCH=amd64 go vet ./...` compiles the Windows-only tests) and the race detector repetitions.
2. The `webview` example binary changed (shared audit type, event sequence numbers, probe, commands) and `webkit` changed by the alias and sequence number: rerun the stage 2 browser matrix (`stage2_matrix.py`) and confirm the `img` check still passes with the engine-neutral probe. Mixer and lifetime should rebuild to their stage 2 hashes only if no linked package changed line counts; `internal/platform/platform.go` and `browser_audit.go` are linked by both, so expect to rerun the stage 4 regression matrix too, and compare hashes before carrying evidence forward.
3. Run Terminus against the returned tree and triage one finding at a time.
4. At the Windows handoff no further native task was listed. Findings from the Linux return may require fixes and corresponding Windows revalidation; see the return review below.

## Linux return review — 2026-11-16

The return arrived as clean commit `d328fbb`. The Windows connected summary records 16/16 and the reported manual pass; the embedded loader file matches its pinned SHA-256. These results remain Windows evidence, not acceptance of the issues found below. The approved F1–F3 corrections are recorded below; the additional return issues remain pending.

Linux no-cgo tests fail in `internal/platform/webview2`: the fake profile concatenates Windows backslashes to a Linux temporary path, while `within` uses the host's `filepath` rules; the explicit Windows-path cases also fail on Linux. This stops many state-machine tests at profile validation. The requested ten race-detector repetitions fail on the same assertions (appcore and public webview pass); no clean race-suite verdict is claimed. Linux vet, Windows build/vet, Darwin build/vet and linux/arm64 build pass.

The native return matrix in [`linux/stage-3-return/`](linux/stage-3-return/) passes **33/33 browser cases and 8/8 foundation/UI runs**, with a clean raw audit of **55 process logs and 78 PNGs**. Child exit statuses were not observed for every host; see [N6](#n6--linux-host-exits-reaped-children-and-remaining-descendants). Fresh evidence was required because the shared core, audit type and example changed. The manifest records browser SHA-256 `3c2d80990de22fd54e031a95e9fb786b00a4333322c2ed244383af66c1d003e4`; these passing native runs do not turn the separate portable-test failures into passes.

Terminus `9f14aa4edba1`, full tree under the existing canon, is **not clean** with three blocking findings. Coverage is 4,408 covered, 1,180 excluded and nine uncovered of 5,597 starting-point files. The gaps are the four example static assets, the WebView2 DLL/license/notices and the two Hebrew font/license fixtures. The review ran concurrently with native validation and did not execute tests. Canon still describes Windows browser hosting as pending; alignment remains deliberate later work.

| Finding | Verified return assessment |
| --- | --- |
| F1 — failed native wake discards cleanup | `post` clears `hop.posted` when PostMessage fails. Dropped-handler and response-stream final-release notifications then have no retry; `maybeRelease` can also fall back to inline cleanup instead of a later turn. Approved and fixed below: retain accepted work and independently retry its wake at a bounded pace, without doing deferred teardown inline. |
| F2 — failed body read loses admitted ID | `act` replaces its decision with the endpoint's ID-zero abort and never releases the original request. A temporary overlay probe using that real endpoint convention fails with no release reported for ID 42. The fake host returned the ID on error, masking the integration mismatch. Approved and fixed below; the fake now matches the real endpoint convention. |
| F3 — closing masks initialization errors | Environment, controller and cookie completions test closing before reporting unexpected failures. Approved and fixed below: successful late results release without attaching; failures still reach the host. |

Additional operator-return issues, separate from the three Terminus findings:

- **Portable path tests:** fixed below without changing production profile containment. The original Linux test/race failures were reproducible fixture failures, not a Windows native failure.
- **Request-body threading:** the implementation synchronously calls IStream.Read on the UI thread. The work order explicitly forbids waiting there on browser-supplied streams; the record's byte limit is not a time bound. The successful 900 KB upload does not establish that all such streams are nonblocking. Microsoft's [request-content reference](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2webresourcerequest?view=webview2-1.0.4129.50) does not promise a memory-backed stream, and its [threading guidance](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model) describes the content-stream exception to UI-thread-only access. The safe COM transfer/reading mechanism needs to be established; do not simply move an apartment-bound pointer to a goroutine. No blocked native read was reproduced here.
- **Metadata before admission:** `requestInfo` converts the complete native method and URI into Go strings before `receive` measures them. Header iteration also converts accepted names/values and grows a Go slice before endpoint admission; the transfer reserve currently accounts for a slice of native string views, not these extra Go string copies. The claim that all metadata is measured before conversion is therefore too broad. This is verified by the call order in code, not a new native oversized-request test; the Windows path needs the same pre-copy bound and ownership accounting established for Linux.
- **Notice retention:** a Windows cross-build of `examples/webview` contains the complete pinned DLL but neither complete notice text nor their distinctive text fragments. Only the unused internal `Notices` function references those embedded strings. Their presence in source does not make them travel in the shipped executable as the work order and record promise. Make the notices reachable in the shipped artifact and verify that artifact, without adding a separate distribution requirement. No license interpretation is added by this check.

F1–F3 are fixed below. *The body-threading, metadata and notice issues are corrected in [Return corrections](#return-corrections--2026-11-20), pending native Windows revalidation.* Stage 4 has not started. Remaining fixes, canon alignment and final review are required before stage acceptance.


### F1 correction — retained cleanup wake

The callback queue now retains accepted work across a failed `PostMessage`. One independent Go timer retries the native wake every 10 ms while it keeps failing. New posts cannot accelerate those retries. Success ends the retry chain; a queued retry that finds no work does nothing. Timer callbacks only post the message: dropped-completion settlement, response-stream release and deferred teardown still execute on the UI thread. The inline `afterRelease` fallback is removed. A persistent wake failure leaves ownership pending rather than declaring cleanup complete.

The native queue was first extracted without changing its old discard-on-failure behavior. Six new tests failed against that behavior, including dropped completion, stream release and inline teardown; the two successful-wake/concurrent-post guards passed. With the correction, all eight `TestHop*` tests pass 300 repetitions without cgo and 100 repetitions under the race detector. Tests control retry delivery explicitly, including 20 consecutive failures, recovery without another event, exactly-once delivery, stale retries and callbacks posting for a later dispatch. These are injected queue/attachment tests on Linux, not execution of Win32 `PostMessage`.

Windows/amd64 build and vet pass. The full WebView2 package still fails on the previously recorded Linux path-fixture assertions; this fix does not claim a clean full suite. No new Terminus round or native Windows run has occurred. The Windows native evidence above describes the returned candidate before this fix and needs revalidation after triage.

Linux browser dependencies contain WebKit, not WebView2; no Linux-linked implementation changed, so the native Linux return matrix was not repeated for F1. A default rebuild has a different hash (`8d34b90a…`) and now records `vcs.modified=true` and a dirty module version; no byte-identical binary claim is made.


### F2 correction — preserve the admitted request ID

`act` now retains the original decision's ID and uses it when a later body-read abort omits the ID. A refusal that never had an ID still releases nothing. The endpoint convention is unchanged; the fake host's error decision now matches it.

`TestBodyReadAbortReleasesRealEndpoint` joins the browser's request-decision code to the real finite endpoint with injected native reads. The failed-read and cancellation-at-EOF cases both failed before the fix with no release reported. Cancellation with final body bytes already passed because that endpoint path includes its ID; it remains a guard. All three now check exactly one release of the admitted ID, zero admitted/native/reserved accounting, one drain notification, and no duplicate release when the browser subsequently closes. `TestInitialAbortWithoutIDReleasesNothing` guards an initial refusal from a closed endpoint.

The F1 and F2 focused tests pass 300 repetitions without cgo and 100 under the race detector. Endpoint, appcore and public webview tests pass, as do Windows/amd64 build and vet (including compilation of the new real-endpoint tests). The separate full-package Linux path-fixture failure remains unresolved. No endpoint, shared-core or Linux-host implementation changed. Native Windows execution and another Terminus round remain pending; no stage acceptance is implied.


### F3 correction — retain initialization failures during close

Environment and controller completions now report failed HRESULTs or missing results before taking the closing path. Credential confirmation likewise reports a failed HRESULT, missing cookie or missing HttpOnly flag even while closing. Closing still settles the completion, releases a successful late environment without retaining it, closes a late controller without attaching it, and waits for the browser exit where required. No late result navigates.

No cancellation HRESULT exception was added. This host does not cancel environment creation, and it retains the parent HWND until the browser attachment releases. Microsoft's [controller creation reference](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2environment?view=webview2-1.0.4129.50) identifies parent destruction as a cause of `E_ABORT`; that is not this host's ordinary close path. A cookie completion released without invocation still settles silently through `dropped`, as before. A failure actually delivered to a completion is retained rather than inferred to be cancellation from timing alone. This is the chosen policy, not a claim that native testing has exercised every HRESULT.

The new tests cover failed HRESULTs, `E_ABORT`, success without a result and valid late success for all three operations, plus missing/incorrect cookie verification and a dropped cookie completion. Eleven error cases failed against the previous code; the three valid-success cases and dropped-completion guard already passed. All now pass 300 repetitions. They verify no navigation or attachment, no outstanding completion, deferred release, controller close and waiting for browser exit. These use the native binding model on Linux, not WebView2 execution.

F1–F3 focused tests pass 100 repetitions under the race detector. The existing core tests proving fatal browser failures cancel/drain and survive an earlier application failure pass 300 repetitions; this is separate core coverage, not a new combined native-host/core test. Endpoint, appcore and public webview suites pass, and Windows/amd64 build/vet pass. The unrelated Linux profile-fixture failures remain; no clean full-suite claim is made. No Linux-linked implementation changed. Windows revalidation and another Terminus round remain pending, and nothing is committed.


### Portable profile fixtures — corrected

The fake runtime now constructs its reported profile with `filepath.Join`, using the same host path rules as the temporary directory and `within`. The foreign-profile test also uses an actual foreign host path. Portable containment cases retain nested, case-insensitive, equal-directory, sibling-prefix and unrelated-directory checks. The five literal Windows cases are preserved in `policy_windows_test.go`, where Windows' `filepath` implementation can interpret them; they compile under Windows vet and still require execution on that host. Production `within` is unchanged.

The full Linux no-cgo suite now passes (`go test ./... -count=1 -timeout=900s`), including the previously blocked WebView2 state-machine tests. WebView2, public webview and appcore pass 50 repetitions, 20 at `GOMAXPROCS=1`, and ten under the race detector. Linux and Windows/amd64 vet pass. This supersedes the portable test-failure status recorded in the earlier return/fix sections; their historical results are retained. Only tests changed in this correction, so no native matrix was repeated. The remaining body-read threading, metadata and notice-retention issues still need disposition, followed by review and Windows revalidation. *(Superseded: corrected 2026-11-20, below; review and Windows revalidation remain.)*


### Request-body threading investigation — native capability check needed

The source review establishes the ownership changes needed, but not the actual incoming stream's apartment behavior. No production code changed in this investigation. No Windows execution is available in this session.

Microsoft's [request Content contract](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2webresourcerequest?view=webview2-1.0.4129.50) exposes an IStream and discusses agile/background-STA streams when content is supplied. The [threading guide](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model) allows background content-stream reading, but neither page promises that every incoming browser-created stream advertises agility. This does not establish that a raw incoming pointer may be handed to a Go goroutine.

There is a documented sufficient condition: [IAgileObject](https://learn.microsoft.com/en-us/windows/win32/api/objidl/nn-objidl-iagileobject) marks an object as callable from any apartment. Ordinary [cross-apartment marshaling](https://learn.microsoft.com/en-us/windows/win32/com/single-threaded-apartments) can instead dispatch the call on the owning STA, so using CoMarshalInterThreadInterfaceInStream alone would not establish that the UI is free of the read. An object using the [free-threaded marshaler](https://learn.microsoft.com/en-us/windows/win32/api/combaseapi/nf-combaseapi-cocreatefreethreadedmarshaler) can also support direct cross-apartment access; failure to expose IAgileObject alone is not proof that it is apartment-bound.

**Recommended next step: a focused Windows probe, before the production fix.** Use the existing WebView2 experiment, keep root code unchanged, and record Runtime version and stream-interface HRESULTs for incoming string/JSON, URL-encoded form, Blob and multipart/file POSTs, including a multi-chunk body and the existing large-upload size. Obtain the stream and event deferral on the UI thread. Query IAgileObject without reading or changing the stream position. If supported, retain the stream and read it from a goroutine locked to an OS thread initialized with `CoInitializeEx(COINIT_MULTITHREADED)`, then release it before `CoUninitialize`. Keep event arguments, deferral completion and all WebView2 operations on the UI thread. Verify exact bytes, the UI/reader thread IDs, UI progress and clean release. Do not call a raw unproven interface from another apartment. If the marker is absent, record that result and investigate its marshaler as a separate branch; do not treat a successful generic marshal as proof that reads execute off the UI.

The same small probe should use an explicitly agile test stream whose read is held on a gate: show a second window/posted UI callback progresses while the read is held, request close, then release the gate and verify cleanup exactly once. This is controlled scheduling evidence, not proof that a real browser stream has the same implementation. Exercise real close-during-upload separately and retain its timing limits. A passing result on one Runtime is native evidence for that version; production would still check the needed capability on each stream.

**Conditional production approach:** one tracked worker per admitted body, bounded by existing endpoint admission (16 per window by default), with one granted read buffer and at most one UI completion outstanding. Take the request deferral and register ownership before starting the worker. The worker reads one granted chunk, posts it through the existing private callback queue, and waits for the UI's next decision before reading again. Endpoint methods and browser state stay on the UI thread. This needs no public API, general worker pool or new transport.

On close, stop scheduling reads and settle native interception on the UI thread, but retain the in-flight worker, stream, buffer reservation and endpoint admission until the read actually returns and its stream is released. Then settle exactly once without responding or navigating, and only then permit final environment/loader/profile release. Canceling a Go context is not a way to interrupt IStream.Read; there is no proposed forced thread termination or timeout that pretends it finished. A permanently stuck read would keep its window's Done/Run pending with existing ownership diagnostics, while the UI continues processing other windows. A different cancellation guarantee or refusal policy for unsupported streams needs a decision based on the probe.

The probe was implemented and run on Windows on 2026-11-16 (below). The synchronous production path remains a recorded blocker until the production change is made and revalidated (*the change was made 2026-11-20, below; Windows revalidation is pending*); no fallback, stream restriction or revised contract has been accepted by this investigation.

#### Probe results — 2026-11-16, Windows

Run in `experiments/webview2-spike` at `fdb640b` plus that experiment's uncommitted changes, Runtime 151.0.4129.93, connected session; record and commands in [`experiments/webview2-spike/evidence/windows/body-probe/`](../../experiments/webview2-spike/evidence/windows/body-probe/README.md), raw run in its `run/`. Five scenarios, all passed, every host exiting clean with no worker, stream, callback object, browser process or profile left. No root code changed.

- **Which streams establish agility: all of them.** Every incoming request body stream the browser handed over (43, for string, JSON, URL-encoded form, 16-part Blob and multipart/file POSTs at 64 KiB and 900 KB, plus the page's own report POSTs) answered `QueryInterface(IAgileObject)` with `S_OK`, exposed `IMarshal` whose in-process unmarshal class is `CLSID_InProcFreeMarshaler`, and reported its exact length through `Stat` (`STGTY_STREAM`) at position 0, all queried on the UI thread before any read. The intercepted headers carry no `Content-Length`; `Stat` is where the length is known up front.
- **The bounded worker approach is supported.** With `IAgileObject` established, the raw stream was handed to a goroutine locked to its own OS thread with `CoInitializeEx(COINIT_MULTITHREADED)` (`S_OK` every time), read in 64 KiB chunks, released there, and the thread uninitialized; the result was posted to the UI thread, which alone touched the event arguments, the deferral and WebView2. Exact bytes for all eight body shapes, verified against the page's own SHA-256 (the file part's, for multipart) and the handler's. Reader and UI thread IDs differed in every case. With the UI thread asleep and pumping nothing for 1.5 s, the 900 KB reads still completed in under 3 ms, so the reads execute on the worker and are not dispatched back to the UI apartment. The marshal fallback (`CoMarshalInterThreadInterfaceInStream`) was never needed and is **untested natively**.
- **Responsiveness and close (modeled stream).** A host-implemented agile stream held in `Read` on the worker: the UI thread answered commands and its timer ticked throughout, a second window loaded and ran its requests (two worker reads of its own), and all 17 `Read` calls ran on the worker thread. A close with the read in flight settled nothing natively for the modeled stream, closed the controller, saw the browser process exit after 243 ms, and then waited: 1.5 s later the window was still not released and the UI still answered. Opening the gate let the read return, the worker released the stream (one final release), the UI dropped the late result once and the window closed once, 1.565 s after the close command.
- **Real close during upload.** Of four closes issued 0, 20, 60 and 150 ms after the page was told to POST 900 KB, the 60 ms one landed during the worker's read: native interception was settled once (503) while the worker still read, the read completed all bytes 31.6 ms after the close, the late result was dropped once and the window released after it. That establishes exactly-once settlement in that ordering and that the stream stayed readable 32 ms past its completed deferral on this Runtime; it says nothing about longer survival or about reads that do not return, since every real read returned within 3 ms.
- **Unsupported case found: `ReadableStream` request bodies.** `fetch` with a streaming body and `duplex: "half"` did not reject; the intercepted request arrived with no content at all (`get_Content` null), the handler saw an empty body and the page saw 200. The in-process transport loses a streaming upload silently and cannot distinguish it from an empty POST. This is a contract question for the webview spec, separate from threading.
- **Remaining contract uncertainty:** one Runtime version observed; no stream without the marker was ever seen, so refusal versus marshal for such a stream is a policy choice without native evidence; the timing limits above.
- Observation: the three windows closed while a navigation or fetch was in flight took about 6.5 s for their browser process to exit; the one closed at rest took 275 ms. The stage 3 record's 6.6 s cases match that pattern.

**Smallest production change the evidence supports.** Keep the conditional approach above, with the capability checked per stream: on the UI thread, after admission, query the incoming stream for `IAgileObject`; on `S_OK`, keep the reference and read it on one tracked worker goroutine locked to its own OS thread (`CoInitializeEx(COINIT_MULTITHREADED)`, uninitialized after the release), one granted chunk at a time, each chunk posted through the existing callback queue for the endpoint's next decision, with the request deferral taken and ownership registered before the worker starts. `Stat` gives the length before any read, so an oversize body can be refused without reading it. If the marker is absent, refuse the request with a fixed response and record it, rather than reading on the UI thread or marshaling: nothing observed needs the fallback, and the probe cannot vouch for where a marshaled read runs. On close, settle native interception on the UI thread and retain the worker, stream, buffer and admission until `Read` returns, then settle once and let release proceed, as the probe's host does. No public API, worker pool or transport change. Separately decide the streaming-body contract.


### Linux review of the body-probe return — 2026-11-16

Returned as committed `5b8b88a`, based on candidate `fdb640b`; the Windows journal's "nothing committed" is handoff history. Root production code is unchanged. Portable tests/vet pass in both experiment modules, including the shared feasibility page, and Windows/amd64 cross-build/vet pass for the WebView2 experiment. No Windows execution was performed on Linux and no new Terminus round ran.

The raw logs support the main feasibility result: all incoming `body-stream` records advertise IAgileObject and the free-threaded marshaler; all worker COM initializations recorded S_OK. There are 45 incoming stream records across the five scenarios, or 43 excluding the gate scenario's two real requests from its other window. Worker-stall results and exact-body checks support using the advertised agility to read on a COM-initialized worker. The gate stream separately demonstrates continued UI progress and retained ownership through close. This supports the bounded worker approach, not an untested generic-marshaling fallback.

Two returned claims need narrower wording before they become contract decisions:

- **Post-close timing:** `after_close_ms` is measured inside `recordRead` on the UI thread, not at the end of the native read. The 31.562 ms observation is result-handling delay after close, not proof that the real stream remained readable for 32 ms. The read loop reports 3.158 ms total. `doneAt` is stored but not emitted; `read_done=false` at close establishes that worker release/its done marker had not completed, not exactly which Read call was active when the native deferral completed. Retain the useful exactly-once close/result evidence, without claiming a measured post-deferral stream lifetime.
- **Streaming upload:** `probeBody` takes the same empty-body branch for a failed/non-S_OK `get_Content` and for a null stream, and logs neither the HRESULT nor nullness on that branch. The page's nonempty ReadableStream upload produced an empty handler body and a 200 response, but these logs do not prove WebView2 returned null successfully or that the host cannot distinguish it from an empty POST. Record the HRESULT and nullness in the next targeted Windows validation, comparing a normal empty POST. A failed getter must not be treated as a successful empty body in the production implementation. Do not yet establish a blanket streaming-body limitation from this probe.

Recommended production scope: check IAgileObject on each admitted body, retain the stream for one COM-initialized locked-thread worker, post one granted chunk at a time through the existing private queue, and retain admission/read-buffer/native ownership through actual worker completion. Keep all endpoint decisions and native interception settlement on the UI thread. Refuse unsupported stream capabilities locally without reading on the UI or falling back to generic marshaling; the precise failure response remains part of the implementation decision. Check COM initialization before invoking the stream (the spike records that result but does not branch on failure; all native results happened to be S_OK). Skip Stat-based early sizing for this correction and keep the existing unknown-length admission/buffer rules. No public API or worker-pool abstraction is needed. Production work remains unimplemented pending Michael's decision; metadata and notice retention remain separate open findings. *(Superseded: all three implemented 2026-11-20, below.)*

## Return corrections — 2026-11-20

Implemented on Linux on `5b8b88a`, where F1–F3 and the portable fixtures are committed (`fdb640b`). The tree adds the uncommitted November 16 review notes; nothing new is committed. These are the three production corrections the return review left open. Built behavior is in [webview.md](../../docs/current/webview.md#windows-host). **Not accepted:** the WebView2 changes are verified here only by modeled COM tests and Windows cross-compilation; no Windows execution has happened, and Terminus review and canon alignment follow.

### Body reads off the UI thread

`receive` keeps `get_Content`'s HRESULT. A successful null stream is an empty body. A failed getter is admitted with an unknown length and aborted when the endpoint asks for the body, never served as empty. When the getter yields no stream for a method other than GET or HEAD, a `request-content` event records its HRESULT, so the targeted Windows comparison can tell a successful null from a failure.

When the endpoint first asks to read, the UI thread queries the stream for `IAgileObject`. A stream that answers gets one worker. Before the worker starts, the UI thread takes the deferral, adds a reference to the event arguments and registers the read. The worker is a goroutine locked to its OS thread. It checks `CoInitializeEx(COINIT_MULTITHREADED)` before touching the stream and reads one granted chunk at a time into a buffer no larger than the grant, which fits the endpoint's read reservation. It posts each result through the existing private callback queue, then waits for the next grant. Endpoint decisions, browser state, event arguments and the deferral stay on the UI thread. The worker releases its stream reference, then leaves COM. If initialization failed, it uses nothing, and the UI thread releases the stream and aborts the request.

Close stops further grants, completes the deferral and releases the event arguments at once. As returned for review, close did not stop grants already queued; see [N1](#n1--queued-grants-stopped-at-close). The bookkeeping stays registered, so the worker, stream, buffer, endpoint admission and window release wait for the worker to return. A late result is dropped: it cannot respond, navigate or reach the endpoint. There is no forced termination and no drain timeout.

Unsupported streams are a request-local failure. A stream without `IAgileObject`, a failed read, a failed COM initialization and a failed getter each abort the request without a response, so the page sees a network error and the handler never runs. The request is released once. Its deferral is completed once if one was taken, and the stream is released once. An unsupported stream also records a `body-unsupported` event with the HRESULT. There is no marshaling fallback, no UI-thread fallback, no pool and no `Stat` sizing.

Tests (`body_test.go`, `endpoint_test.go`):
- A held read leaves the UI thread serving: a second request is admitted and answered, and posted work runs.
- Close during a held read settles interception once and waits for the worker.
- Each failure path is checked: read error, unsupported agility, COM initialization and failed getter.
- A null stream is an empty body, and both getter results are recorded.
- `TestCloseDuringReadHoldsEndpointAdmissionUntilTheWorkerReturns` uses the real finite endpoint. Admission, native ownership, the read count and reserved bytes stay held until the worker returns, then drain to zero.
- The existing granted-chunk and body-abort tests now run through the worker.

The fake runtime is now goroutine-safe and waits for every worker during cleanup, so a stuck worker is reported as a leak. Six mutations were each caught: releasing admission at close, release ignoring readers, a late result reaching the endpoint, skipping the agility check, using the stream after failed COM initialization, and serving a failed getter as empty.

### Metadata bounded before copying

`receive` measures before converting. The method and URI come first, then each header in a first pass. Each string's exact UTF-8 length is computed in place: a surrogate pair is 4 bytes and a lone surrogate's U+FFFD is 3. Each native copy is freed once measured. Measurement stops once the size exceeds the limit. An oversized request converts nothing and reaches the endpoint with only its measured size, which returns the documented 431. Within the limit, a second pass converts the headers into a slice of exactly the counted size.

Those converted strings did not fit the old transfer reserve. It was sized for a slice of string views: one metadata limit ÷ 64 × 32 bytes, half a limit. It is now one metadata limit, the largest transient metadata a browser can build within the limit. The public limits and the 431 refusal are unchanged, and the endpoint's admission accounting is not redesigned.

Tests:
- `TestTransferReserveCoversConvertedMetadata` fails at the old size: with 0 headers, 65,472 transient bytes exceed the 32,768 reserve.
- The `webview2` metadata tests check that the measurement matches conversion, including surrogates. They also check that an oversized URI or header set gets the 431 refusal before any conversion, with every native copy freed. Accepted values must stay intact after their native storage is scribbled and freed.

Four metadata mutations were each caught: limiting headers by UTF-16 units, converting the URI before measuring it, growing the header slice rather than sizing it exactly, and never freeing measured strings.

**Open decision:** the wider reserve comes out of `Buffers`. That leaves half a metadata limit less for requests (32 KiB at the defaults). It also raises the smallest `Buffers` value that `Normalize` accepts by the same amount. Sixteen default worst-case requests still fit. The reserve is shared with the Linux host, so the Linux browser binary changed.

### Loader notices retained

The binding now passes the loader through `loaderWithNotices`, which keeps `LICENSE.txt` and `NOTICE.txt` reachable beside the DLL. The executable stays a single file. `TestWindowsExecutableCarriesLoaderNotices` cross-builds `examples/webview` for windows/amd64. It checks that the executable contains the complete DLL and both complete notice texts. Against the returned binding, it fails on both notices.

### Validation on Linux

- Portable suite: `CGO_ENABLED=0 go test ./... -count=1` passes, including the notices test.
- Vet passes for linux, windows/amd64 and darwin/arm64. Windows/amd64 and linux/arm64 builds pass.
- Windows test binaries compile for `webview2`, `endpoint`, `webview` and `appcore`. The Windows drive harness vets.
- Repetitions:
  - Race detector ×10: `webview2`, `endpoint`, `webview`, `appcore` and `webkit`.
  - `GOMAXPROCS=1` ×20: `webview2` and `endpoint`.
  - `webview2` ×50 without cgo.
- The WebView2 IDL check was not run: it needs `WebView2.idl`, which is not on this host. No new vtable slots are used. `IAgileObject`, `CoUninitialize` and the existing stream methods are standard COM.
- Linux native evidence: see [Linux native evidence](#linux-native-evidence) below.

### Linux native evidence

The endpoint's transfer reserve and the example page are linked into the Linux browser binary. Its unstamped build (`-buildvcs=false -trimpath`) changed from `e6552776…` at the return to `63165678…`, so the stage 2 browser matrix reran on the final candidate. Results are in [`linux/stage-3-corrections/`](linux/stage-3-corrections/), with browser SHA-256 `3297ced1f05b1e08a810273203898d3314b49889ab031269fbd54efc18fb7077` in the stamped manifest:

- It passed **33/33**: 11 cases each in nested Wayland, X11 and X11 at scale 2. Every host matched its expected exit, including `browser_failure`, which exited 1 with the expected error. Child exits were not observed for every host; see [N6](#n6--linux-host-exits-reaped-children-and-remaining-descendants).
- The session logs show only the nested shell's environment warnings: evolution-data-server, PipeWire and NetworkManager typelib. There is no panic, race report or GLib critical.

The stage 4 foundation/UI regression (8/8 in [`linux/stage-3-return/regression`](linux/stage-3-return/)) is carried forward, not rerun. No package linked by the mixer or lifetime binaries changed. Their unstamped builds are identical to the return's: mixer `01d02438…` and lifetime `de0ced0f…`. Their stamped hashes differ only by VCS stamping. The Windows-only WebView2 changes are not linked on Linux.

### Terminus round `9c65a72a1b1d` — triage

The working-tree review after these corrections is **not clean**: six blocking findings. Each is verified below as it is triaged, one at a time.

| # | Finding | Verification | Disposition |
| --- | --- | --- | --- |
| N1 | Close closes the grant channel, but a buffered grant is still delivered, so the worker can call `Read` after close | Reproduced: a barrier test recorded one read after close | Approved and fixed below |
| N2 | The worker posts its completion before `CoUninitialize`, so the window can release during COM teardown | Confirmed in `startWorker` | Approved and fixed below |
| N3 | On EOF or abort, the endpoint releases the read reservation and admission while the worker still holds its buffer and stream | Admission part confirmed; the buffer part concerns only how long a dead buffer lingers | Approved with the Windows buffer handoff; fixed below |
| N4 | `request-content` keeps the full converted URL in event history, beyond the transfer reserve | Confirmed in `receive` and the event ring | Approved and fixed below; other URI-bearing events filed separately |
| N5 | The native `body-close` case passes without proving a reader was active at close | Confirmed in `cases.go` | Approved with a correction (count claimed stops, not registered readers); fixed below |
| N6 | "Every host exiting 0" is wrong for the deliberate `browser_failure` hosts, and child-exit coverage limits are unreported | Exit wording confirmed; child-exit gap not yet checked | Approved as a record-only correction below |

#### N1 — queued grants stopped at close

A Go `range` over a closed buffered channel still delivers its queued value. So a grant queued before close, whether the first from `startRead` or a later one from `readDone`, could be claimed and read after close had completed the deferral.

Each read now has an atomic claim state: idle, claimed or stopped. The worker claims a grant with compare-and-swap, idle to claimed, before calling `Read`. After the read it returns claimed to idle, also by compare-and-swap, before posting the result. `stopRead` stores stopped from any state, and nothing leaves stopped. A grant not yet claimed when the read stops is never read. A claimed read is the one in flight, and it stays tracked until the worker returns. The UI thread never waits. A successful claim marks the boundary of "in flight"; the few instructions between the claim and the `Read` call are accepted.

`TestCloseStopsQueuedGrantsBeforeTheyAreClaimed` uses the real finite endpoint, with two cases:

- **Initial grant:** the worker is held before its first claim.
- **Later grant:** after posting its first 64 KiB chunk, the worker is held before it can claim the next grant, which is already queued.

Each case closes in the core's order and checks:

- no read after close;
- admission, the native record, reading state and the reservation all held until the worker returns, then all drained;
- one release, one stream release and one deferral completion.

Before the fix, the initial case recorded one read with none claimed, and the later case two reads with one claimed.

Fixing it exposed a race in two existing close tests. They closed right after `receive`, assuming the worker had already claimed its read; previously that was masked because an unclaimed grant was read anyway. They now wait for a claimed read (`awaitRead`) before closing, and so does the held-read test.

Four claim mutations each fail a test, by assertion or deterministic panic, never by a hang:
- an unconditional store back to idle;
- reading without a claim;
- an unconditional claim;
- a stop that does not mark the state.

The earlier six body and four metadata mutations are still caught.

Focused checks pass:
- `webview2`, `endpoint`, `webview` and `appcore` once;
- the body tests ×200;
- `webview2` ×50 and ×50 at `GOMAXPROCS=1`;
- the race detector ×20 on `webview2` and `endpoint`;
- vet, and the Windows build, vet and test compilation.

`browser.go` is Windows-only, so no Linux binary changed and the Linux evidence above still applies.

#### N2 — a worker finishes only after leaving COM

`readBody` used to post `readFinished` itself, at the end of the work. The Windows wrapper called `CoUninitialize` only after that, so the UI thread could drop the reader and release the window during COM teardown.

The ordering now lives in one portable function, `runWorker` (`worker.go`). The Windows binding calls it with `CoInitializeEx` and `CoUninitialize`, and the test fake calls it with injected stand-ins. It initializes COM. On failure it reports the error once and runs nothing. Otherwise it runs `readBody`, which releases the stream and returns. Then it calls `CoUninitialize` and reports the worker finished. That single report posts `readFinished`. An `S_FALSE` initialization counts as success and still takes the matching `CoUninitialize`.

`TestWorkerFinishesOnlyAfterLeavingCOM` uses the real finite endpoint. Close lands during a claimed read. The read then returns, and the worker is held inside the injected uninitialization while the UI thread is pumped. The test checks that:

- the reader and the endpoint's admission stay tracked;
- the window stays pending;
- the stream was already released.

Once the barrier opens, it checks for one completion, one uninitialization, one window release and one request release, after which the endpoint drains. With the previous ordering kept inside `runWorker`, it failed: the reader was gone, the endpoint drained and the window released while teardown was held.

The COM-initialization failure case now also checks that the failure is reported once and COM is never left. Four `runWorker` mutations each fail a test:

- reporting before teardown (the old order);
- leaving COM after a failed initialization;
- running the work after a failed initialization;
- never reporting a failed initialization.

The claim, body and metadata mutations are still caught. The body set's check for using the stream after a failed initialization is superseded by the third `runWorker` mutation, because `readBody` no longer receives an initialization error.

Focused checks pass:
- `webview2`, `endpoint`, `webview` and `appcore` once;
- the body tests ×200;
- `webview2` ×50 and ×50 at `GOMAXPROCS=1`;
- the race detector ×20 on `webview2` and `endpoint`;
- vet for linux, windows/amd64 and darwin/arm64, and the Windows build and test compilation.

The Linux browser binary does not link `webview2`, and its unstamped hash is unchanged (`63165678…`), so the Linux evidence above still applies.

#### N3 — admission held to the worker on every path; the buffer passed by hand

Previously only close deferred the endpoint's release to the worker. An abort released at once. A delivered response released when WebView2 dropped its stream, which can come before the worker finishes. Either way the slot freed while the worker still held its stream and had not left COM, so live workers could briefly exceed admission. The worker's buffer also stayed referenced by its loop after the endpoint dropped the read reservation.

Michael chose the Windows-only handoff over extending the endpoint's read reservation on both platforms. The extra buffer lifetime comes from the WebView2 worker, and a longer reservation could change admission under byte pressure.

- **Release owed until both finish.** Every path that used to report a release now goes through `release`: abort, failed deferral, bodiless and stream-released responses, and close's settlement of waiting requests. While the request has an unfinished worker, `release` marks the release owed, and `readFinished` reports it after teardown. Otherwise it reports at once. Native delivery and worker teardown can finish in either order, and the release is reported once, after the later. Close still completes deferrals immediately.
- **One buffer per read, passed by hand.** `startRead` allocates the read's buffer in the first grant, within the read reservation. The worker returns it with each result, and the UI thread passes it on in the next grant. Between results the worker holds nothing. The terminal result, a discarded posted result or a queued grant dropped at close takes the last reference. The owed release keeps the read reservation until the worker has finished. The endpoint is unchanged.

Regressions use the real finite endpoint limited to one request:

- **`TestBodyWorkerAndResponseBothHoldAdmission`**, three cases:
  - **EOF:** the response is released at once while the worker is held in teardown.
  - **Read error:** the worker is held in teardown.
  - **Worker finishes first:** the response stream is still held.

  In each, a second request gets 503 while the first holds its slot. The first is reported released once, after the later owner finishes, and a third request is then admitted and answered 200.
- **`TestCloseOwesTheReleaseOfAWaitingRequestToItsWorker`:** close completes the deferral of a request waiting on its handler. Its release and the slot wait for the worker's teardown.
- **`TestCloseStopsQueuedGrantsBeforeTheyAreClaimed`** gained a posted-result case. All three of its cases now check that the read reservation stays held while the buffer sits in a queued grant or a discarded result.
- **The held-read test** checks one buffer across reads.

Before the fix, the EOF and read-error cases failed: the second request was admitted and answered 200, with the first already reported released. The worker-finishes-first case already passed and is now asserted.

Seven mutations each fail a test:

- `release` ignoring an unfinished worker;
- never paying the owed release;
- reporting the stream release directly;
- reporting an abort directly;
- releasing a waiting request directly at close;
- close not owing a reading request's release;
- a new buffer per grant.

The claim, worker, body and metadata mutation sets are still caught, with anchors updated for the renamed field.

Focused checks pass:
- `webview2`, `endpoint`, `webview` and `appcore` once;
- the body tests ×200;
- `webview2` ×50 and ×50 at `GOMAXPROCS=1`;
- the race detector ×20 on `webview2` and `endpoint`;
- vet for linux, windows/amd64 and darwin/arm64, and the Windows build and test compilation.

The Linux browser binary's unstamped hash is still `63165678…`, so the Linux evidence above still applies.

#### N4 — `request-content` keeps only the getter's result

The event used to store the request's converted URL and method in the 64-event ring, past the request call and outside the transfer reserve. It now keeps only the getter's HRESULT and nullness (`get_Content <HRESULT> null=true`). The Windows driver never used the URL or method. It runs `emptyPost` and `streamUpload` one at a time and takes window A's events after a sequence mark before each run. A run that produces no event is now recorded as "no event observed", rather than as an empty list.

`TestRequestContentEventRetainsNoRequestStrings` admits a request whose metadata is within 16 bytes of the limit, with a distinctive URL and a custom method. It checks two things: the one retained event is exactly the expected getter detail, and neither string appears in any event in the history. Before the fix, it and the null-content test failed on the retained URL. Restoring either the URL or the method on its own fails the new test.

Other events keep page-controlled URIs the same way: `blocked`, `document-request`, the navigation events, `error-page` and `error-action`. That behavior predates this stage and is left unchanged; it is filed as the inbox defect [bound page-controlled URIs retained in browser audit events](../../docs/future/roadmap/bound-page-controlled-uris-retained-in-browser-audit-events.md).

Focused checks pass:
- `webview2`, `endpoint`, `webview` and `appcore` once;
- the content and body tests ×200;
- `webview2` ×50 at `GOMAXPROCS=1`;
- the race detector ×20 on `webview2`;
- vet for linux and windows/amd64, and the Windows build and test compilation;
- the Windows drive vets.

The Linux browser binary is unchanged (`63165678…`).

#### N5 — the native close case observes a claimed read at the close boundary

The case slept, logged the reader count from a separate status snapshot, then closed, and it passed whenever the host drained. That doesn't establish a reader at close. Native reads are fast, and the snapshot and the close are separate UI turns.

Michael corrected the proposed observation: count the reads that close actually stops while claimed, not `len(b.reads)`. A worker that has finished reading but is still leaving COM is registered, but it is not reading. `stopRead` now returns whether its atomic swap found `readClaimed`, which is N1's in-flight boundary. `Close` counts those and records one `close` audit event, `claimed-reads=N`, on the UI thread at the boundary. The example host keeps each window's audition handle from `open` and reads its final state when `Done` fires. Both engines' state is mutex-guarded. It carries the last `close` detail in the `closed` reply, so the driver can read it after the window has gone. WebKitGTK records no such event, and its reply carries `close: ""`.

The modeled tests check the observation:

| Case | Expected |
| --- | --- |
| A held claimed read (`TestCloseDuringABodyReadWaitsForTheWorker`, `TestWorkerFinishesOnlyAfterLeavingCOM`) | 1 |
| An initial or later queued grant, or a discarded posted result (`TestCloseStopsQueuedGrantsBeforeTheyAreClaimed`) | 0 |
| A worker held in COM teardown (`TestCloseOwesTheReleaseOfAWaitingRequestToItsWorker`) | 0 |

All six assertions failed before the change, with no observation recorded. Four mutations each fail a test:
- counting `len(b.reads)`, which the teardown case catches;
- counting every reader close stops;
- treating any unstopped read as claimed;
- recording no observation.

`body_close` is now its own driver case, so an untested result cannot hide the `body_reader` checks. It makes six attempts, each in a fresh host, closing 0, 5, 10, 20, 40 and 80 ms after a 900 KB upload starts. Each attempt must:
- close;
- report a well-formed `claimed-reads=N` in its `closed` reply;
- drain cleanly through the usual host checks.

Any missing or malformed observation, or any failed close or drain, fails the case. Only after every attempt succeeds does the case pass, if some close stopped a claimed read, or report untested, if every close recorded zero. Because the matrix summary does not count failures on untested cases, untested is applied last.

The earlier native `close_cases-reading` run is kept as recorded. It shows the window draining after a close shortly after an upload starts, but it does not establish that a read was in flight at the close.

Focused checks pass:
- the full no-cgo suite once;
- the close and body tests ×200;
- `webview2` ×50 at `GOMAXPROCS=1`;
- the race detector ×20 on `webview2` and `endpoint`;
- vet for linux, windows/amd64 and darwin/arm64, and the Windows build and test compilation;
- the Windows drive vets.

The example host's `closed` reply changed, so the Linux browser binary changed: its unstamped hash went from `63165678…` to `8686410c…`. Mixer (`01d02438…`) and lifetime (`de0ced0f…`) are unchanged, so the stage 4 regression still carries forward. The browser matrix reran into [`linux/stage-3-triage/`](linux/stage-3-triage/), with stamped browser SHA-256 `e410adb3f511b3ae30afa5517d0a1aed7d8bb13c43664180a8118a7c0705487a`. It passed 33/33: 11 cases in each of Wayland, X11 and X11 at scale 2. Every host matched its expected exit; child-exit coverage is as described in N6. The session logs show no panic, race report or GLib critical. WebKitGTK's `closed` replies carry `close: ""`. This run supersedes [`linux/stage-3-corrections/`](linux/stage-3-corrections/) as the current Linux browser evidence; the earlier run is kept.

#### N6 — Linux host exits, reaped children and remaining descendants

The Linux browser matrix results stand: 33/33 in each run. The record's earlier wording, "every host exiting 0", was wrong, and it said nothing about child-exit coverage. This corrects the record only. The driver and the raw evidence are unchanged, and no matrix reran for this.

The driver makes three separate observations per host:

1. **Host exit.** In every run and session, every host matched its expected exit. Fourteen hosts exited 0. `browser_failure` exited 1 with the expected error, which the driver requires.
2. **Statuses of children the driver reaped.** The driver is the hosts' subreaper. It records only the descendants it actually reaps, after the host exits.
3. **The final check for remaining descendants.** It passed for every host in every run.

Reaped statuses, per run and session (15 hosts each):

| Run | Session | Hosts with reaped children | Statuses reaped | Hosts with no child exit status observed |
| --- | --- | --- | --- | --- |
| [return](linux/stage-3-return/) | Wayland | 15 | WebKit network and web processes and one unnamed child, each exit 0; `bwrap` helpers, SIGKILL (the one accepted signal) | none |
| return | X11 | 8 | WebKit network and web processes, exit 0 | 7: `attachment`, the five `close_cases` hosts (`close-init-inprocess`, `close-init-loopback`, `close-handler-inprocess`, `close-handler-loopback`, `close-reading`), `browser-failure` |
| return | X11 at scale 2 | 7 | as X11 | 8: those seven and `owned-basics` |
| [corrections](linux/stage-3-corrections/) | Wayland | 15 | as the return's Wayland | none |
| corrections | X11 | 7 | as X11 | the same 8 |
| corrections | X11 at scale 2 | 7 | as X11 | the same 8 |
| [triage](linux/stage-3-triage/) | Wayland | 15 | as the return's Wayland | none |
| triage | X11 | 7 | as X11 | the same 8 |
| triage | X11 at scale 2 | 7 | as X11 | the same 8 |
| [T3](linux/stage-3-t3/) | Wayland | 15 | as the return's Wayland | none |
| T3 | X11 | 7 | as X11 | the same 8 |
| T3 | X11 at scale 2 | 7 | as X11 | the same 8 |
| [U1](linux/stage-3-u1/) | Wayland | 15 | as the return's Wayland | none |
| U1 | X11 | 7 | as X11 | the same 8 |
| U1 | X11 at scale 2 | 7 | as X11 | the same 8 |
| [U2](linux/stage-3-u2/) | Wayland | 15 | as the return's Wayland | none |
| U2 | X11 | 7 | as X11 | the same 8 |
| U2 | X11 at scale 2 | 7 | as X11 | the same 8 |
| [U3](linux/stage-3-u3/) | Wayland | 15 | as the return's Wayland | none |
| U3 | X11 | 7 | as X11 | the same 8 |
| U3 | X11 at scale 2 | 7 | as X11 | the same 8 |
| [U4](linux/stage-3-u4/) | Wayland | 15 | as the return's Wayland | none |
| U4 | X11 | 7 | as X11 | the same 8 |
| U4 | X11 at scale 2 | 7 | as X11 | the same 8 |

For each host in the last column, no child exit status was observed; the final check found no remaining descendants. The return run's X11 session is the one exception to the eight: its `owned-basics` host did report reaped children. Why those statuses were unavailable is still open, and no `bwrap` child was reaped in any X11 session.

### Terminus round `191087eed011` — full rubric, final tree

The fresh full-rubric round on the triaged tree is **not clean**: five blocking findings and one advisory. Nine files have no matching project-local quality: the example's static assets, the WebView2 DLL with its license and notice, and the Hebrew font fixtures. Findings are triaged one at a time.

| # | Finding | Verification | Disposition |
| --- | --- | --- | --- |
| T1 | Attachment teardown (environment, loader, profile, `Released`) does not wait for response streams the engine still holds | Confirmed. Stage 3 had asserted this order deliberately | Approved with a stream-count correction; fixed below |
| T2 | A browser crash during close is unreported: exit failures are suppressed once closing, and `ProcessFailed` is removed by then | Confirmed in code. It matches the Linux R4 decision, which WebView2 never adopted | Approved with an exit-kind HRESULT correction; fixed below |
| T3 | WebKitGTK sets the body length to 0 when a positive `Content-Length` has no stream, and trusts a zero declaration over a present stream | Confirmed in code (stage 2), and in WebView2's `receive` too | Approved (keep the declaration, plus a `NoBody` contract flag); fixed below |
| T4 | `appcore`: after a partly consumed frame, new motion can replace a sampled move the frame deferred | Code path confirmed; no contract breach: deferred motion carries no state, and only edges (which never coalesce) keep deferred state | Documented as intended in `ui.md`; no code change |
| T5 | The current webview doc calls the Windows host natively validated, but that run predates the worker | Confirmed | Approved; the validation claims in `webview.md` and this record's result line are scoped to `d328fbb` |
| A1 | README and AGENTS.md describe Windows browser hosting as unbuilt | Confirmed | Approved; both now name the WebView2 host and its pending native revalidation |

#### T1 — teardown waits for response streams

`maybeRelease` now also waits for `b.streams` to reach zero, and `streamReleased` rechecks it while closing. Close itself is unchanged: it still settles waiting requests and closes the controller at once, so the engine can drop its references. Only the environment, the loader, profile cleanup and `Released` wait for both the browser process's exit and the last response stream's final release.

Michael's correction fixed a counting bug that the gate would have turned into a hang. `deliver` incremented `b.streams` only after a successful response. If `CreateWebResourceResponse` failed after the stream was allocated, the host's own release posted `streamReleased` for a stream that was never counted. Under the gate, the resulting count of −1 would hold shutdown open forever. Each stream is now counted at creation, before the local reference is released, and its final-release callback is the single decrement. The `installed` flag fed only the old condition and is gone. A response that fails `put_Response` drops its stream reference when the response is released, and that stream's final release reports.

The fake now models a stream that no response took: the host's release is its final release.

- **`TestCloseSettlesRequestsAndReleasesOnlyAfterResponseStreamsFree`**, inverted from its stage 3 form. It holds a response stream past the browser process's exit and checks that:
  - the controller is already closed and the waiting request settled;
  - exit-handler removal, the loader, profile cleanup and `Released` are all pending;
  - once the stream is freed, the delivered request is released, and teardown completes once (loader and profile cleanups, one `Released`).
- **`TestFailedResponseCreationReleasesItsStreamOnce`**: response creation fails after the stream is allocated. The stream's one final release releases the request once and returns the count to zero, and close drains.

Before the fix, the first failed with teardown run while the stream was held, and the second with `streams -1`. Three mutations each fail a test: a gate that ignores streams, a stream release that doesn't recheck teardown, and counting after the local release (the old order).

Whether the real engine drops its last stream reference promptly after the controller closes is a Windows validation item. A window whose engine never released a stream would keep `Done` and `Run` pending, visible in diagnostics, with no forced timeout.

Focused checks pass:
- `webview2`, `endpoint`, `webview` and `appcore` once;
- the stream, close and body tests ×200;
- `webview2` ×50 at `GOMAXPROCS=1`;
- the race detector ×20 on `webview2`;
- vet for linux and windows/amd64, and the Windows build and test compilation.

The owed, close, claim, worker, body and metadata mutation sets are all still caught. The Linux browser binary is unchanged (`8686410c…`).

#### T2 — browser-process failures reported during close

`onBrowserProcessExited` reported a failure only when not closing, and Close removes the `ProcessFailed` handler. A crash during close was therefore recorded as an event and never reported. The rule now matches Linux's R4:

- **Before close**, any exit is fatal.
- **During close**, a confirmed normal exit is the expected end. A failed exit reaches `host.Failed` before teardown proceeds.

Michael's correction: the Windows binding discarded the HRESULT from `get_BrowserProcessExitKind`, and a failed getter's zeroed output reads as the normal kind. `exitedArgs` now returns that HRESULT. A failed getter is reported as an unreadable exit kind, carrying the getter's error. The notification itself still marks the process exited, so the release gate proceeds.

Deduplication covers only the same browser-process failure. `onProcessFailed` used to set `browserFailed` but report regardless. Both callbacks now go through `browserFailure`, which reports once, whichever arrives first. A renderer failure (`failedRenderProcess`, `failedFrameRenderProcess`) is still reported on its own.

`TestBrowserProcessFailuresDuringClose` covers six cases, each checking the exact number of reports and that release proceeds only after the exit is handled:

| Case | Expected |
| --- | --- |
| Failed exit during close | 1 report |
| Normal exit during close | none |
| `ProcessFailed` before close, then a failed exit during close | 1 report |
| Failed exit, then `ProcessFailed` (reversed order) | 1 report |
| Renderer failure, then a failed exit during close | 2 reports |
| Unreadable exit kind during close | 1 report, naming the getter's E_FAIL |

Before the fix:
- the failed exit during close produced no report;
- the reversed order produced two reports;
- the renderer case lacked the browser report;
- the unreadable kind read as normal.

Five mutations each fail a test:
- deduplicating renderer failures;
- the old unconditional `ProcessFailed` report;
- ignoring the getter's HRESULT;
- suppressing a failed exit while closing;
- reporting a normal exit while closing.

The core's existing R4 tests cover propagation into `Run`.

Focused checks pass:
- the four packages once;
- the failure and close tests ×200;
- `webview2` ×50 at `GOMAXPROCS=1`, and ×20 under the race detector;
- vet, and the Windows build and test compilation.

The Linux binary is unchanged.

#### T3 — the declared length and the body stream are separate facts

Both adapters rewrote the declared length to 0 when the engine supplied no stream, and treated a zero declaration as an empty body without reading a stream that was present. A positive `Content-Length` with no stream therefore reached the handler as an empty body. A zero declaration with a non-empty stream lost its bytes silently. A page cannot set `Content-Length`, so both cases come only from engine inconsistency.

The private contract now carries the two facts separately:

- **`BodyLength`** is the declaration, or −1 when there is none.
- **`NoBody`** says the engine supplied no stream. WebView2 sets it only when `get_Content` succeeded and returned none; a failed getter keeps its request-local abort. WebKitGTK's getter has no failure result, so a NULL stream sets it.

The endpoint enforces one rule for both engines:

| Declaration | Stream | Result |
| --- | --- | --- |
| Above the limit | either | 413, checked first as before |
| Positive, within the limit | absent | 400, before any handler |
| Zero | present | one detection byte, nothing stored: EOF serves an empty body, a byte is 400 before any handler |
| Zero or none | absent | an empty body, served at once |
| None | present | the bounded unknown-length read, unchanged |

`Finite.Request` now serves at once only when no read is owed. Before, it served whenever `bodyCap == 0`. A zero-declared stream reserves a one-byte read buffer and no stored body. Option B, passing an unknown length instead, was rejected: it would let a handler see `Content-Length: 0` alongside a non-empty body.

Regressions:

- **Endpoint:** `TestDeclaredLengthAndBodyStreamAreSeparate` covers the table and both 413 cases. `TestZeroDeclaredStreamReservesOneByte` checks a reservation exactly one byte above an empty request's, a one-byte read buffer and no stored body.
- **Through each adapter with the real endpoint:** `TestDeclaredLengthAndStreamReachTheEndpointSeparately`, in both `webview2` and `webkit`. Each checks the declaration and `NoBody` the endpoint received, the response or the handler's body, and that no worker starts for a request without a stream. WebView2 adds a failed-getter case: aborted with no response, and the handler never runs.

Against the old code (the pre-T3 endpoint with the adapters as they were), the two contradictory cases failed in both adapters: they were served 200, or reached the handler, instead of 400. The four ordinary cases passed as compatibility checks. In the endpoint's own table, two compatibility cases also failed under the old endpoint, because it could not express them. The adapters' old rewrite to 0 had produced the right outcome for them.

Requests in existing tests that model no stream now set `NoBody` explicitly: the endpoint's `get()` helper, its HEAD request, and three `webview` tests. The WebView2 null-content test now expects `NoBody` with the declaration kept.

Eight mutations each fail a test:

- the endpoint admitting a positive declaration without a stream;
- the endpoint owing no read for a zero-declared stream;
- the endpoint's old `bodyCap` serve rule;
- the 400 check ahead of the 413;
- WebView2 setting `NoBody` on a failed getter;
- WebView2 rewriting a missing stream's declaration to 0;
- WebKit rewriting a missing stream's declaration to 0;
- WebKit never setting `NoBody`.

Two of these, WebView2's failed getter and WebKit's never setting `NoBody`, survived until the adapter tables checked what reached the endpoint.

Focused checks pass:
- the full no-cgo suite once;
- the declaration, null-content and body tests ×50 across `endpoint`, `webview2` and `webkit`;
- `webview2` and `endpoint` ×20 at `GOMAXPROCS=1`;
- the race detector ×10 on `webview2`, `endpoint`, `webview` and `appcore`, and ×3 on `webkit`;
- vet for linux, windows/amd64 and darwin/arm64, and the Windows and linux/arm64 builds and Windows test compilation.

The first race run found a test-only race. In the served cases, the WebView2 table returned while the endpoint's handler goroutine was still publishing its completion through the fake's global native table. The cases now wait for the response's delivery. `webview2` then passed ×10 under the race detector, and the stream and admission tables ×20.

`NoBody` is a field of the shared platform package, which every example links, so all three unstamped hashes changed: mixer `4370e844…`, lifetime `bfd4d592…` and browser `464d20ad…`. The full Linux matrix therefore reran, the stage 4 regression included, into [`linux/stage-3-t3/`](linux/stage-3-t3/). The stamped hashes are browser `5a0a5b180630a7bcb20040cd9950de2b20c1aae75c735acc147c2b33e6c4c245`, mixer `8ca929f4bba5c3afa461c4ad5539db1257eb7cc77688ddc13151ec4deda79fab` and lifetime `eed5d7fde3490237a1c668e22b715897660dbbe79426f2f31d7b22a7aeb10c6b`.

- **Browser:** 33/33, every host matching its expected exit.
- **Stage 4 regression:** 8/8 foundation and UI runs, with a clean raw audit of 55 process logs and 78 PNGs.
- **Child exits:** the same as in N6. Wayland's 15 hosts all reported reaped children. In each X11 session the same 8 hosts had no child exit status observed, and the final check found no remaining descendants.
- **Session logs:** no panic, race report or GLib critical.

This run is now the current Linux evidence for both the browser matrix and the stage 4 regression; the earlier runs are kept. Windows native validation is still pending.

### Terminus round `dfc9032e5091` — full rubric, after T1–T5 and A1

The confirmatory round is **not clean**: five blocking findings, none in code this correction changed. The same nine files have no matching project-local quality.

| # | Finding | Verification | Disposition |
| --- | --- | --- | --- |
| U1 | The finite writer's response snapshot (`Header.Clone`) shares the handler's string storage, so a short slice of a large string keeps that whole allocation alive | Confirmed | Approved; fixed below |
| U2 | The loopback writer forwards handler-generated 1xx informational responses without the framework CSP | Confirmed | Approved, simpler option: handler 1xx responses are dropped; fixed below |
| U3 | WebKitGTK suppresses the whole `WebKitPolicyError` domain as a stopped load, hiding genuine failures such as "cannot show URI" | Confirmed; every suppressed native failure on record is code 102 | Approved; fixed below |
| U4 | A loopback listener failure reported during close is dropped when another failure already latched | Confirmed: `browserHost.Failed` keeps a later failure, `endpointHost.Fail` did not | Approved; fixed below |
| U5 | The Linux driver doesn't report sampled descendants it never reaped | Already decided in N6: record-only correction, driver unchanged | No further action |

#### U1 — the response snapshot owns its strings

`WriteHeader` still measures the handler's header map before copying anything. It now builds the snapshot with `ownedHeader`, copying every name and value with `strings.Clone`, so the retained bytes match the measured bytes. Nil value slices stay nil, because the metadata rule charges their names. Per-key slices stay within the 64-byte per-header charge.

`TestResponseHeaderSnapshotOwnsItsStrings` slices a header name and value from a 1 MiB string. It checks that neither the snapshot nor the delivered response points into that string, that the values are unchanged and that a nil value slice stays nil. Against `Clone` it failed on the name.

Three mutations each fail it: an uncopied name, an uncopied value, and a nil slice turned empty.

Focused checks pass:
- `endpoint`, `webview2`, `webkit`, `webview` and `appcore` once;
- `endpoint` ×50;
- the race detector ×10 on `endpoint` and `webview`;
- vet, and the Windows build.

The browser binary changed (unstamped `49a02bee…`). Mixer and lifetime are unchanged (`4370e844…`, `bfd4d592…`), so the stage 4 regression in [`linux/stage-3-t3/`](linux/stage-3-t3/) carries forward. The browser matrix reran into [`linux/stage-3-u1/`](linux/stage-3-u1/), with stamped browser SHA-256 `d8d52f833709bf8724f2e4b187063fd1cf81820dcc8546f894bf7ab48882c6dd`. It passed 33/33, every host matching its expected exit. Child exits are as in N6: none unobserved on Wayland, and the same 8 hosts in each X11 session. There is no panic, race report or GLib critical. This is now the current Linux browser evidence.

#### U2 — loopback drops a handler's informational responses

Michael chose the simpler of the two options: not forwarding the response at all, rather than sending each 1xx with the policy and then restoring the handler's header map. The loopback writer's `WriteHeader` now ignores codes 100–199 other than 101. This matches the in-process adapter, which already refuses 1xx. A 101 is committed with the framework policy, as before. net/http's automatic `100 Continue` is written by the server, not through this writer, so it is unaffected. Applications lose Early Hints in loopback mode.

`TestLoopbackDropsHandlerInformationalResponses` has a handler send a 103 with a `Link` header and then a 200. The client's `Got1xxResponse` trace hook must see no informational response, and the 200 must carry its body and exactly one framework CSP. Before the fix the 103 was forwarded. A guard that also swallowed 101 fails the existing switching-protocols test.

Focused checks pass:
- the five affected packages once;
- `endpoint` ×20;
- the race detector ×10 on `endpoint` and `webview`;
- vet, and the Windows build.

The browser binary changed (unstamped `9f8cb105…`); mixer and lifetime did not. The browser matrix reran into [`linux/stage-3-u2/`](linux/stage-3-u2/), with stamped browser SHA-256 `e7ddae4ce7eef67353d6243d641ac058eb14b186d87fa6595fe3d730341cc0a3`. It passed 33/33, every host matching its expected exit, with child exits as in N6, and no panic, race report or GLib critical. This is now the current Linux browser evidence.

#### U3 — only WebKit's policy interruption is a stopped load

`loadFailed` now suppresses only `WebKitPolicyError` code 102, `FRAME_LOAD_INTERRUPTED_BY_POLICY_CHANGE`. That is the failure the host's own `webkit_policy_decision_ignore` produces: refused navigations, refused undisplayable responses and error-page actions. The closing and network-cancellation exceptions are unchanged. WebKit's other policy failures now take the error-page path: 100 (cannot show MIME type), 101 (cannot show URI) and 103 (restricted port). Every Linux run on record backs the narrow rule: stage 2 final, the return, T3 and U1 suppressed 396 load failures between them, all `WebKitPolicyError 102`.

`TestOnlyPolicyInterruptionsAreStoppedLoads` drives `loadFailed` with real GError records. The fake now names WebKit's policy and network domains.

- **Stopped loads:** 102, network cancellation, and any failure while closing record `load-stopped` with no error page.
- **Error pages:** 100, 101, 103 and an ordinary network failure each show the error page.

Before the fix, 100, 101 and 103 were suppressed. The old domain-wide check fails the test.

Focused checks pass:
- `webkit`, `webview` and `appcore` once;
- the load-failure and error-action tests ×20;
- `webkit` ×3 under the race detector;
- vet, and the Windows build.

The browser binary changed (unstamped `38a63599…`); mixer and lifetime did not. The browser matrix reran into [`linux/stage-3-u3/`](linux/stage-3-u3/), with stamped browser SHA-256 `91dbb90081ee1d2d1b5037933ddee6f11fceabc84e7343e50ebc854830f228da`. It passed 33/33, every host matching its expected exit, with child exits as in N6. All 99 stopped loads it recorded were `WebKitPolicyError 102`, and no policy-domain failure produced an error page, so the host's own refusals still show none. The session logs show no panic, race report or GLib critical. This is now the current Linux browser evidence.

#### U4 — endpoint failures after an earlier failure reach `Run`

`endpointHost.Fail` now handles a failure the way `browserHost.Failed` has since stage 2's R4. The first failure starts shutdown through `failLocked`. A failure arriving after another one is kept through `cleanupLocked`, and `Run` joins it as a separate record. `resultLocked` already removes repeats. Both kinds of failure the endpoint reports are covered: a loopback listener failure reported before drain, and an owned handler's panic.

`TestEndpointFailureAfterAnEarlierFailureIsKept` runs one case for each. A worker's failure starts shutdown, and after the window has closed the endpoint reports its failure, drains, and the browser releases. `Run` must return both errors. Before the fix, both cases returned only the first.

Focused checks pass:
- the full no-cgo suite;
- `appcore` ×50, and ×20 at `GOMAXPROCS=1`;
- the race detector ×10 on `appcore`, `webview` and `endpoint`;
- vet for linux, windows/amd64 and darwin/arm64, and the Windows and linux/arm64 builds.

The core is linked by every example, and all three unstamped hashes changed: mixer `db2b1653…`, lifetime `80abb433…` and browser `1d79292e…`. The full Linux matrix, the stage 4 regression included, reran. Results are in [`linux/stage-3-u4/`](linux/stage-3-u4/). Stamped hashes: browser `8d8a5d625b9b0f0680c53fc6e2dbeef5109e3c68eba15f29cc9f55a19848f057`, mixer `23149a65b31ffbe3fab5e4aac433625dc92357f278060deb1937d6ffee2c0d42`, lifetime `be05fad50847965640bc91e1bfe2fa296f932dec46ba5b2eeeead9f0098a1e4e`.

- **Browser:** 33/33, every host matching its expected exit, child exits as in N6.
- **Stage 4 regression:** 8/8 foundation and UI runs, with a clean raw audit of 55 process logs and 78 PNGs.
- **Session logs:** no panic, race report or GLib critical.

This is now the current Linux evidence for both the browser matrix and the stage 4 regression.

### Terminus round `f7aee917e2b7` — full rubric, U4 tree

The confirmatory round after U1–U4 is **not clean**: five blocking findings and one advisory, none of them in code this correction changed. The same nine files have no matching project-local quality. Michael ended full-rubric review here for this transfer, since another Linux round cannot supply the native evidence the corrected WebView2 code still needs.

| # | Finding | Decision |
| --- | --- | --- |
| V1 | A native failure inside a Win32 size/move loop is held until the drag ends: the latch that stops ordinary callbacks also blocks modal delivery | The accepted Win32 behavior is kept. After a native failure, ordinary callbacks stop, and the core receives the failure when the drag ends. The resulting cancellation delay is recorded as a known limit in [ui.md](../../docs/current/ui.md). |
| V2 | A new pointer move can replace a deferred move | Closed as the already-decided T4: deferred pointer motion may coalesce. |
| V3 | WebKitGTK can install its content filter after the window's owner has started closing | Tracked as an investigation: [inbox item](../../docs/future/roadmap/investigate-a-webkitgtk-content-filter-completing-after-owner-close.md). The filter can finish before the browser receives `Close`, and `maybeNavigate` still calls `host.Ready()` before the first load. What can happen after owner close, and whether it is observable, is not yet established. |
| V4 | WebKitGTK ignores a failure to connect `download-started` | **To be fixed on the Linux return, before stage acceptance.** Download cancellation is a documented guarantee, so browser creation must unwind on this failure as it does for the required view signals. The fix needs an injected-failure test and a rerun of the affected Linux browser matrix. Listed under [Before stage acceptance](#before-stage-acceptance). |
| V5 | A methodless top-level navigation other than a form counts as GET | Tracked as the narrower Retry question: [inbox item](../../docs/future/roadmap/check-webkitgtk-retry-for-methodless-reload-and-history-navigation.md). Retry always loads with GET, so it never resubmits a POST, but the promise is that Retry is offered for a *known* GET. Methodless reload and history navigation are to be checked against that promise. |
| A2 | `docs/future/webview-integration.md` still presents the built browser contracts as future work | Left with the arc's existing closeout task: stage 4 of the [work order](../../docs/future/webview-integration-work-order.md) retires realized intent documents after acceptance. |

### Status at transfer

The **candidate is the U4 tree**, the working tree after the U4 correction. Michael commits it and transfers that revision to the Windows host. Nothing in it is accepted.

- **Linux native evidence** is current at [`linux/stage-3-u4/`](linux/stage-3-u4/): 33/33 browser cases and 8/8 stage 4 regression runs on that tree.
- **WebView2:** every correction since the return is covered by modeled COM tests and Windows cross-compilation only. That covers the body reader through N1–N5, T1–T3 and U1. None of it has executed natively.
- **Review:** the last full-rubric round, `f7aee917e2b7`, is not clean, with the dispositions above.

Known limits, recorded where they apply:

- **Win32 size/move loops (V1):** cancellation and drain wait for the drag to end after a native failure.
- **Loopback mode (U2):** a handler's 1xx responses, including Early Hints, are not forwarded.
- **X11 child exits (N6):** in the Linux matrices, 8 of 15 hosts per X11 session have no child exit status observed; the final check still finds no remaining descendants.

### Before stage acceptance

1. **Windows native validation** of the committed U4 revision, as described in the [Windows handoff](#windows-handoff) below.
2. **V4 on the Linux return.** If `download-started` cannot be connected, browser creation unwinds as it does for the required view signals. The fix needs an injected-failure test, and the affected Linux browser matrix must rerun.
3. **Linux review of the Windows return.** That includes the `get_Content` comparison, which settles the `ReadableStream` upload contract.
4. **Open decisions** from these corrections:
   - the transfer reserve at one metadata limit, which costs 32 KiB of `Buffers` at the defaults and raises `Normalize`'s minimum;
   - aborting the request, a network error for the page, as the response to an unsupported stream or a failed getter;
   - the streaming-upload contract.
5. **Canon alignment**, by deliberate review.
6. **Michael's acceptance.**

### Windows handoff

Run on the Windows host at the revision Michael commits from the U4 tree, with `CGO_ENABLED=0` throughout. Record that revision, Go, Windows and Runtime versions and the session state, as the stage 3 return did. Change no production code on Windows. If a native result contradicts the modeled behavior, record it and return it for a decision instead of fixing it there.

1. **Portable and Windows checks:**
   - `go test ./... -count=1` and `go vet ./...`.
   - `go test -race -count=10 ./internal/platform/webview2 ./internal/endpoint ./webview ./internal/appcore`, if a race toolchain is available.
   - The IDL check, with `WEBVIEW2_IDL` set to the pinned SDK's `WebView2.idl`. The corrections add no WebView2 vtable slots; `IAgileObject` and `CoUninitialize` are standard COM.
   - `go test ./internal/platform/webview2 -run Notices -v`, to confirm the loader's notices in an executable built on the host that ships.
2. **Native matrix, 18 cases, in a connected session.** Build both executables as in [Native Windows matrix](#native-windows-matrix), then run `& "$env:TEMP\dfx2-wvdrive.exe" -exe "$env:TEMP\dfx2-webview.exe" -out evidence\webview-integration\windows\stage-3\corrections`.
   - **The first 16 cases are the stage 3 regressions;** expect them all to pass. `close_cases-reading` records no close observation, so it is not evidence of a close during a read.
   - **`body_reader`:** a 900,000-byte upload is echoed intact, with no body reader left afterward, and an empty POST is answered as an empty body.
   - **`body_close`:** closes during uploads at six staggered delays. It passes if some close stopped a claimed read (`claimed-reads` of 1 or more in the `closed` reply), and reports untested if every attempt recorded a valid zero. It fails on a missing or malformed observation, or on any close or drain failure. Record the result and the per-attempt counts.
3. **Targeted `get_Content` comparison,** from `body_reader`'s details. For `emptyPost` and `streamUpload`, record:
   - the page's report;
   - the `request-content` events (HRESULT and `null=true`), or "no event observed";
   - the handler counts.

   Only these readings establish whether a `ReadableStream` upload arrives as a successful null stream (served as an empty body), as a failed getter (now a network error) or with content. They also show whether the host can tell it from an empty POST. 4. **Observations to record from these runs:**
   - **Release after the browser process exited (T1):** whether any window's release waited on a response stream. Compare the `browser-process-exited` event with the `closed` reply, and note any response streams that diagnostics still list.
   - **A crash during close (T2), if one is observed:** its `browser-process-exited` event and the reported failure. The modeled test supplies the controlled ordering.
   - **`body-unsupported` events:** none are expected.
   - **Timing limits:** keep the probe's. Its "32 ms after close" measured result handling on the UI thread, not stream lifetime, and these runs don't measure post-deferral stream lifetime either.
5. **Return** the results and the run directory for Michael's commit, as in the stage 3 return. The Linux return then reviews them, and the V4 fix follows there.
