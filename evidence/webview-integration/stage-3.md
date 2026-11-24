# Webview integration stage 3: Windows browser integration

**Current state (2026-11-24):** candidate `c1bd0d6` has returned Windows automated validation (17 browser cases passed, `body_close` untested), passing native mixer/lifetime regressions, passing Linux return checks, and Michael's reported manual pass for both Windows auditions. The latest Terminus verdict remains not clean with the dispositions below. Stage 3 is accepted with the recorded limits and follow-ups following canon alignment `76e1f94`. Earlier sections retain their historical state.

Stage 3 of the [work order](../../docs/future/webview-integration-work-order.md) hosts `webview` windows on Windows/amd64 in the system WebView2 Runtime, inside the Win32 backend's message loop, in all three modes, from one executable with the pinned loader embedded. Built behavior is in [webview.md](../../docs/current/webview.md#windows-host). Implemented on the Windows host at `59ff40e` plus the uncommitted tree recorded in the [run's environment](windows/stage-3/run-connected/environment.json); returned here as commit `d328fbb`. Linux evidence is unchanged by this stage except where the shared example and the audit type changed (below); the Linux return rebuilds and reruns.

Host: Windows 10 Pro 10.0.19045.6466, amd64, Go 1.27.3 (module language 1.26), Evergreen WebView2 Runtime 151.0.4129.93, over Remote Desktop. The session was **disconnected** while the host was implemented and first validated (`query session` state `Disc`, reporting 1920×1080 at 96 DPI) and **connected** for the final runs (`Active`, 3840×1600 at 96 DPI). Every build, test and vet uses `CGO_ENABLED=0`. Terminus is not installed on this host; review runs on the Linux return.

**Result for the returned revision `d328fbb`: the native matrix passes 16 of 16 cases in the connected session**, with the Win32 size/move loop established by a title-bar drag and both screen captures written, the spike's adjusted `error-pages` scenario passes its targeted rerun, and Michael's manual audition passed by his report (below). The earlier disconnected run, 15 of 16 with the size/move case untested, is kept beside it. *That validation predates the [return corrections](#return-corrections--2026-11-20) and the triage that followed, validated on Windows at `ad2e37b` with 17 cases passed and `body_close` untested. [Windows return](#windows-validation-of-the-committed-corrections--2026-11-20).*

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

Windows ran the committed corrections at `ad2e37b`, and the Linux return fixed V4 in `f00df33`. Michael accepted the three endpoint and upload decisions on 2026-11-21. Stage acceptance still requires review of the returned tree, deliberate canon alignment and Michael's acceptance. [Contract decisions](#contract-decisions-on-the-linux-return)

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

   Only these readings establish whether a `ReadableStream` upload arrives as a successful null stream (served as an empty body), as a failed getter (now a network error) or with content. They also show whether the host can tell it from an empty POST.

4. **Observations to record from these runs:**
   - **Release after the browser process exited (T1):** whether any window's release waited on a response stream. Compare the `browser-process-exited` event with the `closed` reply, and note any response streams that diagnostics still list.
   - **A crash during close (T2), if one is observed:** its `browser-process-exited` event and the reported failure. The modeled test supplies the controlled ordering.
   - **`body-unsupported` events:** none are expected.
   - **Timing limits:** keep the probe's. Its "32 ms after close" measured result handling on the UI thread, not stream lifetime, and these runs don't measure post-deferral stream lifetime either.
5. **Return** the results and the run directory for Michael's commit, as in the stage 3 return. The Linux return then reviews them, and the V4 fix follows there.

### Windows validation of the committed corrections — 2026-11-20

Run on the Windows host at **`ad2e37bdd7e2e2109d254168a0697bf84134ab1c`** (Michael's commit of the U4 tree), clean tree, `CGO_ENABLED=0` throughout, no production code changed. Host: Windows 10 Pro 10.0.19045.6466, amd64, **Go 1.27.4** (the earlier Windows runs used 1.27.3), Evergreen WebView2 Runtime **151.0.4129.93**, SDK 1.0.4129.50 (the embedded loader's digest `a9a09232…` unchanged), Remote Desktop session `Active` (`rdp-tcp#2`; the driver's environment file records the stale `SESSIONNAME` `RDP-Tcp#1`), 3840×1600 at 96 DPI. Raw run: [`windows/stage-3/corrections/`](windows/stage-3/corrections/), host executable SHA-256 `498a67ba1c5c8d9e94e5b30d3e863efc61009780da3a992c4c45a801ba5a698a`, driver `c44d8c67…`, built with the commands in [Native Windows matrix](#native-windows-matrix) and run with the handoff's `-out`.

**Checks.** `go test ./... -count=1 -timeout=900s` passes for every package; `go vet ./...` passes. The IDL check against the pinned `WebView2.idl` passes. `TestWindowsExecutableCarriesLoaderNotices` passes on this host, cross-building `examples/webview` here: the shipping executable carries the DLL and both complete notice texts. **The race repetitions were not run**: this host has no C toolchain (`-race requires cgo`); the Linux return's ×10 race runs stand as the race evidence.

**Native matrix: 17 of 18 pass; `body_close` is untested.** Every host exited as expected (`browser_failure` and `missing_runtime` with their expected errors), no browser process survived its host, no `dfx2-webview2-*` directory remained, and nothing but the supervisor's shutdown diagnostic reached stderr. The sixteen stage 3 regressions all pass, including `size_move` by caption drag. `close_cases-reading` is, as the handoff says, not evidence of a close during a read.

| New case | Result |
| --- | --- |
| `body_reader` | A 900,000-byte in-process upload was echoed intact (status 200, length and SHA-256 equal) and no body reader remained afterwards (`Reads` 0). The empty POST was answered as an empty body. The `get_Content` comparison is below |
| `body_close` | **Untested.** Six attempts closed a fresh host's window 0, 5, 10, 20, 40 and 80 ms after the page was told to upload 900 KB; every `closed` reply carried a well-formed `claimed-reads=0`, every host exited 0 with no leftover, and every stderr was empty. No close landed on a claimed read, so the native close-during-read ordering is still covered only by the modeled tests |

Per-attempt `claimed-reads` for `body_close`: 0 ms → 0, 5 ms → 0, 10 ms → 0, 20 ms → 0, 40 ms → 0, 80 ms → 0. All six drains clean (exit 0, no temp leftovers, empty stderr).

**Targeted `get_Content` comparison** (`body_reader`, window A, in-process; the handler counts are the window's cumulative counts when each run finished, after the 900 KB `uploadEcho` had already added one `POST /api/echo`):

| Run | Page's report | `request-content` events after the run's mark | Handler counts |
| --- | --- | --- | --- |
| `emptyPost` | status 200, echo length 0, SHA-256 of the empty body | one: `get_Content S_OK null=true` (seq 14) | `POST /api/echo` 2, `POST /api/report` 2 |
| `streamUpload` | status 200, echo length 0, SHA-256 of the empty body | one: `get_Content S_OK null=true` (seq 19) | `POST /api/echo` 3, `POST /api/report` 3 |

Reading: on this Runtime a `ReadableStream` upload arrives at interception as a **successful null stream**, exactly as an empty POST does. The getter does not fail, so the request is served as an empty body, the handler runs, and the page sees 200 with nothing delivered. Interception cannot tell the two apart (the stage 3 probe also found no `Content-Length` on intercepted requests). This matches the model's handling of a successful null; the streaming-upload contract remains the Linux review's decision, and nothing here contradicts the implementation.

**Observations the handoff asked for:**

- **T1, release after the browser process exited:** no window's release observably waited on a response stream. The three hosts that printed the five-second shutdown diagnostic (`in_process_alone`, `storage`, `response_race`, the same quit-with-window-open pattern as before) each listed `response-streams=0` and `body-readers=0`; the only `closing=true` status strings captured show `response-streams=0`. The `browser-process-exited` audit events themselves are not in the driver's status snapshots (a closed window leaves `status` before they could be sampled), so the comparison rests on the diagnostics and the `closed` replies, all of which arrived; the explicit close in `profile_lifecycle` took 320 ms.
- **T2, a crash during close:** none observed. The only browser-process failure is `browser_failure`'s deliberate kill, reported once as "WebView2 browser process exited (reason unexpected, exit code 1, )", with the application draining and exiting 1.
- **`body-unsupported` events:** none in any host log, as expected.
- **Timing limits:** unchanged. These runs measured no post-deferral stream lifetime; the body reads were too fast for any close to land on a claimed read.

**What this does and does not establish.** The committed body reader, metadata bound and notice retention execute natively on this Runtime without regression across the eighteen cases; the worker path handled every in-process body and left nothing behind. It does not establish the close-during-read ordering natively (untested, above), and the race detector did not run here. Nothing is committed from this validation; Michael commits the run directory and this record, and the Linux return reviews them with V4 first.

### Linux return — V4, 2026-11-21

Implemented on Linux at `fdf7f47` (Michael's commit of the Windows validation). It is the only production change on this return. The Windows result above is unchanged: **17 of 18 pass, `body_close` untested.**

**The fix.** `newBrowser` in `internal/platform/webkit/browser_linux_amd64.go` now checks the result of connecting the context's `download-started` signal. If it fails, creation calls `abandon` and returns an error naming the signal, the same initialization-failure path the required view signals use. The connection is attempted before the view exists, so the unwind releases the context and drops every routing token registered so far: the in-process scheme handler's and the signal's own. Nothing else is held. Before the fix, the result was discarded: creation succeeded, and that window would have let downloads proceed uncancelled.

**Regression.** `TestUnconnectableDownloadSignalUnwindsCreation` injects a `g_signal_connect` failure for `download-started` only, in in-process and attach modes. It checks that `newBrowser` returns no browser and an error naming the signal. It also checks that no routing token stays registered, that the context is unreferenced once, that no view is destroyed or added to the shell, and that every connection made was disconnected. Before the fix, both modes created the browser. A mutation that refuses creation without `abandon` fails the test, leaving two tokens registered in-process and one in attach mode.

**Portable checks.** All pass:
- the full no-cgo suite;
- the V4, policy-failure and declaration tests ×50;
- `webkit` ×3 under the race detector, and `webview` and `appcore` ×5;
- vet for linux, windows/amd64 and darwin/arm64;
- the Windows and linux/arm64 builds.

**Linux native.** Only the browser binary changed (unstamped `44057cb4…`). Mixer (`db2b1653…`) and lifetime (`80abb433…`) are identical to [`linux/stage-3-u4/`](linux/stage-3-u4/), so its 8/8 stage 4 regression carries forward. The browser matrix reran into [`linux/stage-3-v4/`](linux/stage-3-v4/), with stamped SHA-256 `86ba9ad6991587eb7a923e7e0f420c277c2a6f28c73ed56a1d5f8a11ae0797d9`.
- **Result:** 33/33. Every host matched its expected exit, `browser_failure` with its expected exit 1. Child-exit coverage is as in N6: none unobserved on Wayland, and the same 8 hosts per X11 session with no child exit status observed, where the final check found no remaining descendants.
- **Logs:** no panic, race report or GLib critical.
- **What it establishes natively:** only the success path. `download-started` connected on WebKitGTK 2.52.6 for every browser the 33 cases created, since a failed connection would now have refused creation.
- **What it does not:** the matrix has no download case. Every audit records `Downloads: 0`, and no response was refused as undisplayable. So neither the failure path nor download cancellation itself ran natively here. The failure path has modeled coverage only.

This is now the current Linux browser evidence, with the stage 4 regression from `stage-3-u4`.

| Run | Session | Hosts with reaped children | Hosts with no child exit status observed |
| --- | --- | --- | --- |
| V4 | Wayland | 15 | none |
| V4 | X11 | 7 | the same 8 as in N6 |
| V4 | X11 at scale 2 | 7 | the same 8 as in N6 |

### Contract decisions on the Linux return

Michael accepted all three on 2026-11-21.

- Each in-process endpoint reserves one metadata limit for the browser's temporary request data during synchronous admission. At default limits this increases the reserve from 32 KiB to 64 KiB per endpoint. Custom `Buffers` must cover that reserve, the refusal slots and one maximal request.
- If WebView2 fails to obtain the request body or a supplied stream lacks the agility required by the reader, the admitted request aborts locally. The page sees a network error, the handler is skipped and the application continues. A successful null stream follows the ordinary absent-stream rules, including the declared-length checks.
- WebView2 in-process windows support buffered uploads, including a `Uint8Array`. JavaScript `ReadableStream` request bodies are unsupported in this mode. On Runtime 151.0.4129.93 the host received exactly the same getter result for the streaming upload and the empty POST. The handler received zero bytes in both cases. [Windows comparison](windows/stage-3/corrections/body_reader.result.json)

The Windows matrix remains 17 passed with `body_close` untested. The Linux browser matrix passed 33/33 after V4 and the eight foundation and mixer regression runs carry forward from U4.


### Full-rubric review after the contract decisions

Terminus round `73e31fc4d23e` reviewed the full tracked tree against all 11 qualities. Verdict **not clean**, with seven blocking findings and zero advisories. The same nine files lack project-local quality coverage (four webview example assets, the WebView2 loader and its two notices, and the Hebrew font fixture and licence).

| Finding | Area |
| --- | --- |
| Readiness can authorize the first navigation after context cancellation | Shared browser lifetime |
| The message-only dispatch window can outlive the application | WebView2 native ownership |
| Response-stream cloning can panic on allocation failure | WebView2 COM callbacks |
| Reload and history navigation with an unknown method can offer Retry | WebKitGTK, previously tracked as V5 |
| A surface can become ineligible during its own frame and still receive a presentation | Immediate-mode scheduling |
| Wrap accounting uses slice length while larger backing arrays remain allocated | Text cache budgets |
| Host exit can race the stdout reader's final event | Linux webview audition driver |

Michael approved the readiness fix. The core now checks the window context under the same lock as the window and attachment state before granting first navigation. The modeled regression cancels the application context after browser creation while the window is still open, then delivers readiness on the UI thread. The old guard granted navigation and failed the test. The corrected gate refused it. The existing live-window and closing-window controls also passed, across 200 repetitions of the three cases.

The full no-cgo suite, Linux vet, ten race-enabled appcore runs and the Windows/amd64 cross-build passed. The native matrices above predate this shared-core change. The remaining six findings await disposition.

#### WebView2 dispatch-window lifetime

The second finding in round `73e31fc4d23e` was confirmed by inspection. The original one-time binding created a message-only HWND and registered its class. Neither had a release path, and a failed window creation left the class registered. Michael approved using the existing attachment lifetime to own both.

The first live attachment creates the dispatch window before browser initialization starts. The final attachment destroys it on the UI thread after all browser callback producers have finished. A failed creation unregisters its class and unwinds the loader and profile. If destruction fails, that attachment reports the error and remains pending through a paced retry on the UI thread. If unregistration fails after the HWND is gone or creation failed, the host reports the error and tracks class cleanup through shutdown.

The portable tests exercise callback delivery with two attachments, final destruction, reopening with a reused numeric HWND, a delayed wake retry belonging to the old queue, and cleanup after failed creation or destruction. For the Windows return, run `go test ./internal/platform/webview2 -run '^TestNativeDispatchWindowClosesInsideDispatchAndReopens$' -count=10`. It uses User32 to dispatch the last callback, checks the HWND is gone, and runs another full open/close cycle with the same class name. This test was cross-compiled on Linux. Native execution on the new tree is pending.

The full no-cgo suite, 100 repetitions of the modeled dispatcher and sharing tests, ten race-enabled WebView2 and appcore runs, Linux and Windows vet, and the Windows/amd64 cross-build passed. With last-close cleanup removed in a test overlay, the sharing test found the HWND still live. Removing the failed-create rollback separately exposed the missing class unregistration. Five findings from the round remain for triage.

#### WebView2 response-stream clone allocation

Michael approved the third finding in round `73e31fc4d23e`. When the engine calls `IStream::Clone`, the callback clears the output pointer and returns `E_OUTOFMEMORY` if the native COM header allocation fails. Only a successfully allocated stream enters the object registry and acquires a reference to the shared body. A successful clone starts at the original's current offset and advances independently. The initial response-stream and handler constructors retain their existing allocation-failure behavior.

The failure regression starts at a nonzero offset and injects a null allocation into the COM callback. It checks the HRESULT, null output, registry size, body reference count, next byte read from the original, and single release of the body. The companion success case releases the original and continues reading the clone. On Linux a temporary Go overlay ran the Windows callback and tests with stand-ins for native allocation, free and callback registration. The old callback panicked on the injected failure. Both corrected cases passed 200 repetitions and 50 race-enabled repetitions using native-memory stand-ins for the COM headers and the read destination. The full no-cgo suite, Windows/amd64 build, WebView2 package vet for Windows and Windows test cross-compilation also passed. Run `go test ./internal/platform/webview2 -run '^TestStreamClone' -count=10` on the Windows return for coverage with the native allocation and free bindings. Four findings from this round remain for triage.

#### WebKitGTK Retry on reload and history navigation

Michael approved the fourth finding in round `73e31fc4d23e`. On a reload or history traversal, the classifier now withholds Retry when WebKit omits the method. A reported GET still permits Retry, and Return to start remains available. The error-page test covers missing, GET and POST methods for both navigation types. Both missing-method cases offered Retry with the old classifier.

A separate instrumented example on nested Wayland recorded the raw method and navigation type before classification. It submitted a form, reloaded its POST result using WebKit's reload operation, navigated away, then went Back and Forward. WebKitGTK 2.52.6 reported a form resubmission (type 4) for the reload in both transports. Back and Forward to the POST result were history traversals (type 2). All three omitted the method in-process and reported POST on loopback. The reload sent another POST in each transport. The history traversals restored the result without another handler call. These successful navigations establish what the engine supplied. The error-page button assertions ran in the Go tests. [Native observations](linux/stage-3-retry-method/post-history.json)

The ordinary browser binary passed all 33 cases across Wayland, XWayland and XWayland at scale 2. The mixer and lifetime matrices were not repeated in this run. Three findings from this round remain for triage. [Matrix manifest](linux/stage-3-retry-method/manifest.json)

#### Surface eligibility at presentation

Michael approved the fifth finding in round `73e31fc4d23e`. A view can receive a synchronous surface change during its own frame. The core now checks eligibility before presenting the result. If the open window has become hidden or zero-sized, it retains a redraw for restoration. The frame still accounts for the input it consumed and can release capture.

The fake backend delivers each surface change inside an input-only frame with no outstanding invalidation or animation. Both cases presented an extra image with the old guard. The hidden case also lost its redraw. The corrected cases check zero presentations while suspended, one capture release, one new image on restore and no replay of consumed input. Independently restoring the open-only guard, dropping the redraw mark or tying capture release to presentation each failed the new test. The focused frame and capture tests passed 200 repetitions. The new test also passed 300 repetitions at `GOMAXPROCS=1`. The appcore suite passed ten race-enabled runs. The full no-cgo suite, Linux and Windows vet, the Windows/amd64 build and appcore test cross-compilation passed.

The [native Linux browser matrix](linux/stage-3-frame-presentation/manifest.json) passed 33/33 across Wayland, XWayland and XWayland at scale 2. All [eight mixer and lifetime regression runs](linux/stage-3-frame-presentation/regression/manifest.json) passed. The audit found clean drains in 55 process logs and decoded all 78 PNGs. The synchronous change during a window's own frame was exercised with the fake backend. Native Windows validation of the corrected core is pending. Two findings in this round remain for triage.

#### Retained wrap capacity

Michael approved the sixth finding in round `73e31fc4d23e`. On this Linux/amd64 build, wrapping 100 repetitions of `"ab "` with the test engine used 100 of 146 line slots and 300 of 400 positioned-glyph slots. The arrays held 20,976 bytes while the old accounting charged 15,200. Paragraph-cache and transient-scratch decisions now charge both backing arrays by capacity.

The regression measures those arrays independently and uses their combined size to set three boundaries. The wrap fits an exact cache budget, goes to scratch if the cache is one byte short, and is refused if both limits are one byte short. All three failed with the old calculation. Reverting either capacity charge alone also failed all three. The full no-cgo suite, 100 repetitions of the text suite, ten race-enabled runs of text and UI, Linux vet and the Windows/amd64 cross-build passed.

All [eight native Linux mixer and lifetime runs](linux/stage-3-wrap-capacity/manifest.json) passed. The audit found clean drains in 55 process logs and decoded all 78 PNGs. The rebuilt browser example matched the [previous 33-case Linux browser run](linux/stage-3-frame-presentation/manifest.json) byte for byte. The corrected accounting awaits native Windows execution. The Linux audition driver's stdout reader is the last outstanding finding in this round.

#### Drain host stdout before reading the exit result

The seventh finding in round `73e31fc4d23e` concerned the Linux audition driver. After the host exited, a 200 ms sleep allowed the driver to inspect an incomplete event list and close the log while its reader could still write. The driver now waits for that thread within the original exit deadline. A timeout or reader error fails the case. The log stays open while a reader is still draining.

Two regression tests cover a held return event and a read error following a return. Both failed with the old driver and passed 200 repetitions each after the change. The [Linux browser matrix](linux/stage-3-stdout-drain/manifest.json) passed all 33 cases. Each of the 45 host logs contains exactly one return as its final host event. Commands share the same logs and can appear after that event. The browser binary matches the prior run byte for byte. The eight mixer and lifetime runs from the wrap-capacity correction still apply.

All seven findings from this round have now received fixes. A fresh Terminus verdict and native Windows validation of the corrected tree remain pending.

#### Full-rubric review after the seven corrections

Terminus round `0a080a023bed` reviewed the staged candidate on `f00df33` against canon `3a7c821` and all 11 qualities. Verdict **not clean**, with five blocking findings and zero advisories. Of 8,304 files in scope, 7,094 were covered, 1,201 excluded and the same nine assets and fixtures uncovered.

| Finding | Status at review |
| --- | --- |
| WebView2 handler allocation can panic inside a COM callback | Confirmed by inspection. Environment completion allocates the exit and controller handlers, and the COM invocation entry calls the handler directly. The earlier allocation fix covered response-stream cloning. |
| A task calling `runtime.Goexit` can strand its owner | Completion bookkeeping currently follows the call to the task. |
| A newer pointer move can replace a deferred move | Re-raises the coalescing behavior Michael accepted at T4 and V2. |
| The paragraph budget omits spare slots in the shaped-run array | The Go text engine appends runs. The paragraph charge counts populated runs. The previous correction covered the arrays retained by a wrap. |
| Sampled browser descendants without an observed exit status are absent from the per-host record | Revisits N6 and U5. Even a host with some observed statuses may have others the driver missed. |

The first finding is up for disposition. Native Windows execution of the corrected candidate is still pending.

#### Scope decisions and narrow corrections for `0a080a023bed`

On 2026-11-24 Michael approved the allocation-error and shaped-run accounting corrections, with emphasis on completing the framework's rough implementation. General callback panic containment and `runtime.Goexit` support are deferred. Tasks using `runtime.Goexit` currently strand completion bookkeeping, as disclosed in `runtime.md`. Deferred pointer motion keeps the T4/V2 decision. The child-exit observer keeps the N6/U5 coverage limit, including potentially unobserved exits among hosts with some observed child statuses.

WebView2's handler allocator now returns nil on a failed native allocation. Every constructor call checks before using the handler or registering an owed completion and reports the existing out-of-memory HRESULT with the handler name. The modeled regression injects failure at all seven construction sites and checks failure reporting, no navigation and release after close. It failed against the old code and passes with the correction. A Windows-only allocator/registry test is cross-compiled and awaits host execution.

Paragraph accounting now charges the shaped-run backing array's capacity. A test engine supplies one populated run in eight slots. Tests for a cached entry, admission falling back to scratch, and insufficient scratch all failed against the old calculation and pass with the correction.

The full no-cgo suite, race checks for WebView2, text and UI, Linux and Windows vet, Windows build and WebView2 test cross-compilation pass. Windows execution of this candidate remains pending.


The [targeted Wayland controls and text-cache audition](linux/stage-3-run-capacity/manifest.json) passed. Its audit covered three process logs and 15 PNGs. The rebuilt Linux browser binary matches the latest 33-case stdout-drain matrix, so that evidence carries forward. The full mixer matrix predates the shaped-run correction; this run validates the affected text-cache behavior on Wayland.

#### Review of the narrow corrections

Terminus `fd12d66d3b4c` returned **not clean**, with five blocking findings and one advisory. All 11 qualities reviewed 8,332 files against canon `3a7c821`. Coverage was 7,122 covered, 1,201 excluded and the same nine uncovered assets and fixtures. Neither corrected allocation handling nor shaped-run accounting was raised.

| Finding | Inspection and scope |
| --- | --- |
| Cross-owner callback order restarts each turn | Confirmed. Each pass services one callback per owner, so one busy application cannot starve an ordinary small set of windows. Cross-turn fairness becomes material with more than 64 simultaneously busy owners. The record establishes the ordering limitation, without a reproduced native starvation case. |
| Input overflow can leave native capture while the view has no captured control | Confirmed in code. Input loss clears UI gesture state, but requests capture release only when that state held a control. Win32 takes capture before the view sees a press. Native button-up and focus loss still release it. |
| WebKitGTK Retry lacks reliable final-method correlation in every case | Generic missing-method navigation still defaults to GET, and records are keyed by URI. This extends the earlier reload/history correction. A failed top-level load and a subframe using the same URI can compete for the recorded method. Retry loads a GET. No new native reproduction was run in this review turn. |
| GTK has multiple permanent trampolines of the same signature | Confirmed. The set is created once for the process and does not grow with windows. This conflicts with the one-per-signature convention. |
| Body-refusal tests rely on elapsed time | Confirmed. Two fixtures wait 20 ms to infer that a handler never started. The reviewer proposes observing synchronous completion registration instead. |
| Future integration document describes built contracts | Advisory. This is the existing arc-closeout documentation work. |

These findings await disposition under Michael's narrowed scope. The recommendation is to transfer this candidate for native Windows validation and track the remaining items as scoped follow-up work. This review result is recorded as not clean; the proposal does not change the verdict or claim stage acceptance.

#### Review under the calibrated canon

Terminus `6b8257f2c523` reviewed the candidate against committed canon `8efb2f4` and all 11 qualities. Verdict **not clean**, with three blocking findings and zero advisories. The development-stage boundary appeared in all nine dfx2 quality bodies in the generated reviewer prompt. The same nine assets and fixtures remain uncovered. The review did not execute native tests.

| Finding | Verification and disposition |
| --- | --- |
| Initial response-stream allocation panics on failure | Confirmed in `newStreamObject`. Unlike the corrected handler and clone allocation paths, this constructor still panics. A deferred response has already left the request map before delivery. No new allocation-failure reproduction was run. Awaiting disposition. |
| WebView2 response serialization exceeds its metadata charge | Inspection confirms that the response headers coexist with a UTF-8 header block and its UTF-16 conversion, while the endpoint charges two metadata copies. The extra storage remains bounded by the metadata limit and synchronous delivery. No measured peak-memory reproduction was run. Awaiting disposition under the development-stage scope. |
| Same-URL response can suppress a later navigation's error page | Reproduced with a test overlay that delivers the older response before the current navigation's failed completion. `TestLateSameURLResponseCannotSuppressTheErrorPage` fails with `no error page for the failed navigation`. The existing test delivers that response after the error page is pending. No native Windows reproduction was run. Recommended as the next narrow correction. |

The overlay lived in `/tmp` and changed no repository source. These findings are recorded without accepting their proposed fixes or claiming stage acceptance. Native Windows revalidation remains pending.

#### Navigation-response correction for `6b8257f2c523`

Michael approved the navigation finding. The regression now delivers the older same-URL response before the newer navigation's failed completion. It failed against the previous implementation with `no error page for the failed navigation` and passes after the correction.

The host classifies only the matching `NavigationCompleted` event, using its existing navigation ID, success and HTTP status. It no longer keeps a generation map or a response flag inferred from resource URLs. Successful navigations and final 2xx/4xx/5xx responses display normally. Failed cancellations and closing remain suppressed. Failed completions with no final HTTP status, including a retained 1xx/3xx status, show the error page. Microsoft's [completion HTTP-status contract](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2navigationcompletedeventargs2) identifies this status with the navigation and documents the successful-response case where `window.stop()` leaves `IsSuccess` false. The existing native evidence establishes that a failed redirect destination can retain the redirect's 3xx status.

Resource responses remain audit-only, named `resource-response` rather than `document-response`. The Windows driver follows the name change. Its existing native race case still covers a response arriving after the error page is pending; the before-completion ordering is covered by the modeled regression, not claimed as native evidence.

The focused navigation tests passed 100 repetitions. They cover the late response, superseded completions, failed redirect targets, HTTP 404/500 and cancellations. A browser-level test also checks that final 200/404/500 statuses display without any resource-response event. The full no-cgo suite, three race-enabled WebView2 runs, Linux and Windows vet, Windows build and WebView2 test cross-compilation passed. The Linux browser binary remains byte-identical to the 33-case stdout-drain run (`b07c9b8f…`), so that evidence carries forward. Native Windows execution and a fresh Terminus review remain pending. The allocation and serialization findings remain unchanged and await disposition.

#### Response-stream allocation correction for `6b8257f2c523`

Michael approved the allocation finding. `newStreamObject` now returns zero when its native allocation fails. Delivery detects that result, settles the deferral and request through the existing abort path, reports the out-of-memory error, and returns without creating or installing a response. No general callback recovery was added.

A regression joins the browser to the real finite endpoint for an immediate 413 refusal and a deferred handler response. The modeled allocator returns zero. Both cases failed against the previous delivery logic, which installed an empty response without reporting a failure. They now check one allocation failure, no response creation or installation, the expected deferral completion, one request release and a completed endpoint/window drain. Both passed 100 repetitions. A Windows-only allocator test additionally checks zero returned, an unchanged stream registry and no final-release callback for an object that was never created. That test is cross-compiled, not executed here; the old constructor's panic was confirmed by inspection.

The full no-cgo suite, three race-enabled WebView2 runs, Linux and Windows vet, Windows build and WebView2 test cross-compilation pass. The Linux browser binary still matches the stdout-drain matrix (`b07c9b8f…`). No native matrix reran. Native Windows execution and the next Terminus review remain pending. Response serialization accounting is the remaining finding from this round and is unchanged.

#### Response-serialization reserve for `6b8257f2c523`

Michael approved a conservative allowance within the existing budget. Each endpoint now keeps four metadata limits plus 64 bytes aside for the one synchronous response serialization on the UI thread. It covers admitted responses and fixed refusals. This is separate from the existing request-transfer and refusal reserves. WebView2 pre-sizes its UTF-8 header block; the allowance covers its capacity with allocation rounding, the UTF-16 conversion and the short native reason phrase. The reserve remains unavailable to admissions for the endpoint's lifetime. Per-request response accounting and public default limits are unchanged. At the defaults the added reserve is 256 KiB plus 64 bytes, and sixteen maximal requests still fit. Custom buffers must also cover the new reserve.

The tight-budget delivery regression models the simultaneous serialized header block and UTF-16 conversion beside the charged response. It failed before the fix with 5,152 serialization bytes and no separate reserve. It now passes, including 100 repetitions. The full no-cgo suite, three race-enabled runs of endpoint and WebView2, Linux and Windows vet, Windows build and WebView2 test cross-compilation pass. The [Linux browser matrix](linux/stage-3-response-budget/manifest.json) passed 33/33 across Wayland, XWayland and XWayland at scale 2, on browser binary `d3c8d8dc4f14ff577acd5db756747ac10c4001eaee936ed53fe79b44aa033c42`. Every host matched its expected exit. The existing N6 child-status observation limit remains. The mixer and lifetime binaries match the current run-capacity evidence byte for byte, so their recorded evidence carries forward. Native Windows validation and the next Terminus verdict remain pending.

#### Review after the three corrections

Terminus `af5151522c39` reviewed the staged candidate under canon `8efb2f4` and all 11 qualities. Verdict **not clean**, with four blocking findings and one advisory. The same nine assets and fixtures remain uncovered. None of the three findings from `6b8257f2c523` was repeated. The reviewer did not run tests or native auditions.

| Finding | Inspection and next disposition |
| --- | --- |
| WebView2's blocking response can fail to install | Confirmed unchecked results: `refuse` silently returns on failed response creation and ignores `put_Response` failure. The caller then records `blocked`. No native request-escape reproduction was run. Recommended for the next narrow investigation because an unsuccessful interception may leave the request on the network path. |
| Retry can inherit a prior redirect hop's method | Confirmed that `start` preserves the previous method until a matching document request arrives. The new response-correlation fix did not change this policy. Whether this produces a material problem under the accepted GET-only Retry behavior needs assessment; no reproduction was run. |
| Loader unload failure loses ownership tracking | Confirmed that loader state is cleared before `freeLibrary`, and its error is returned through cleanup. The review proposes retaining ownership with paced retries. No real unload failure was reproduced. Disposition must weigh that additional mechanism against the agreed development-stage scope. |
| Synchronous browser startup returns `ErrClosed` from Open | Confirmed that `Start` has no error result and reports failure through the host. `Run` retains the underlying cause; Open can observe the closing state. No new regression was run. Awaiting disposition. |
| Runtime overview omits Windows browser support | Advisory. The overview still says only Linux hosts browser windows. The current webview guide describes Windows hosting and its pending native revalidation. |

The findings are recorded for disposition, without authorizing their proposed fixes or claiming acceptance. The new Linux browser evidence remains 33/33. Native Windows revalidation remains pending.

#### Disposition after `af5151522c39`

On 2026-11-24 Michael approved recording and deferring the failed blocking-response risk and loader-unload retry machinery. Neither native failure has been observed in this work. The potential consequences remain recorded; the deferral does not establish safe behavior on those paths. Follow-up prompts are [failed blocking responses](../../docs/future/roadmap/investigate-failed-webview2-blocking-responses.md) and [loader unload](../../docs/future/roadmap/revisit-webview2-loader-unload-failure.md). No production code changed for either finding.

The runtime overview advisory is corrected: it now names both built browser hosts and distinguishes Windows support from the pending native revalidation of corrections.

The remaining two findings were assessed by code inspection. Retry can inherit an earlier hop's GET until a new document request supplies its method, but the action still issues GET and does not replay a POST. Synchronous startup failure can give Open `ErrClosed`, while the underlying failure still reaches Run. The recommendation is to track these as follow-up work without blocking Windows transfer. That is the assessment of this review session, not a claim that Michael separately approved both dispositions.

Terminus remains **not clean**. No stage acceptance is claimed. The recommended next milestone is native Windows revalidation of the candidate Michael commits, followed by review of that evidence.

### Windows revalidation of `c1bd0d6` — 2026-11-24

Run on the Windows host at **`c1bd0d6510dacbf855f271dae261de02e6c86473`** (Michael's commit of the Linux-side corrections: readiness, dispatch-window lifetime, stream-clone allocation, navigation completion, frame presentation, text capacity, response serialization reserve). The worktree was clean before the run (`git status --porcelain` empty); afterwards it holds only the three untracked run directories named below. `CGO_ENABLED=0` throughout; no production code, test, driver or runner changed. Host: Windows 10 Pro 10.0.19045.6466, amd64, Go 1.27.4, Windows PowerShell 5.1.19041.6456 (not elevated), Evergreen WebView2 Runtime **151.0.4129.93** (also recorded by the driver in `summary.json`), SDK 1.0.4129.50, Remote Desktop session `rdp-tcp#3` (session 2) `Active` before and after every run (the drivers' environment files record the agent shell's stale `SESSIONNAME` `RDP-Tcp#1`), one Microsoft Remote Display Adapter at 3440×1440 and 96 DPI (scale 1; every `first_surface` of every process is `@1`). The display differs from the 2026-11-20 run's 3840×1600; both 760-wide mixer windows fit side by side.

**Binaries.** Built from the clean tree before any evidence existed:

| Binary | SHA-256 |
| --- | --- |
| `examples/webview` → `dfx2-webview.exe` (host of the browser matrix) | `6cea568dbec8f37534335fce95f3929f7777954704d7be5bc35fc21c06824465` |
| `evidence/webview-integration/windows/drive` → `dfx2-wvdrive.exe` | `66b9735dbeb2014cbb4055847860ac2efb33892d965f6d904e1d06bc942b54cf` |
| `examples/mixer` → `dfx2-mixer.exe` (reference and manual-audition binary) | `4b15d057aa17f88acf32f35b81d5604b0e773b386c0fda7c59cf97352be79bcf` |
| `examples/lifetime` → `dfx2-lifetime.exe` (reference) | `8292e3f63135aa0d925f042d2bc951b65ecbd972a47a86690c357dc49f54a773` |

The mixer and foundation runners build their own copies after the first run directory exists, so Go stamps `vcs.modified` and those hash differently from the same source: `mixer.exe` `4668a34d…`, `desk.exe` `60a0909b…`, `controlsdrive4.exe` `8fd1b940…`, `lifetime.exe` `a34561e9…`, `ctrlbreak.exe` `998edd2a…`. Compare revisions, not hashes, with Linux.

**Checks (all pass).**

- `go test ./... -count=1 -timeout=900s`: all 14 packages with tests ok. `go vet ./...` and `go build ./...` clean.
- The IDL check: `TestBindingsMatchIDL` PASS with `WEBVIEW2_IDL` at the pinned SDK's `WebView2.idl`.
- `TestWindowsExecutableCarriesLoaderNotices` PASS (1.84 s), cross-building `examples/webview` on this host.
- **Windows-only tests executed here for the first time** (they were cross-compiled on Linux): `TestStreamCloneAllocationFailure`, `TestStreamCloneHasIndependentPositionAndSharesBody`, `TestHandlerAllocationFailureKeepsRegistryEmpty`, `TestResponseStreamAllocationFailureKeepsRegistryEmpty`, `TestNativeDispatchWindowClosesInsideDispatchAndReopens` and `TestWithinWindowsPaths`, all PASS; the dispatch-window test also passed `-count=10`. Only the dispatch-window test exercises real Win32: it creates the message-only window, dispatches through User32 and checks the HWND is gone before reopening with the same class. The clone and allocation tests run the real COM callbacks over Windows-allocated memory with an injected allocation failure; no Runtime is involved. The rest of the suite, including the readiness, frame-presentation, text-capacity, navigation and endpoint-budget regressions (`TestBrowserReadyRefusesNavigationAfterContextCancellation`, `TestSurfaceBecomesIneligibleDuringItsOwnFrame`, `TestWrapBudgetsIncludeSpareCapacity`, `TestParagraphBudgetChargesRunArrayCapacity`, `TestNavigationMethodAndErrorPageActions`, `TestNavigationHTTPResponsesDisplayWithoutResourceEvents`, `TestLateSameURLResponseCannotSuppressTheErrorPage`, the serialization-reserve extension of `TestFiniteDeliveryStaysWithinTheAdmissionReservation`, `TestHandlerAllocationFailureStopsInitialization`, `TestResponseStreamAllocationFailureDrainsEndpoint`), is modeled even when it runs on Windows.
- **Race detector: not run.** `-race` needs cgo and this host has no C compiler (`gcc` not found); nothing was installed. The Linux race runs recorded above stand as the race evidence.

**Native browser matrix: 17 of 18 pass; `body_close` is untested.** Raw run: [`windows/stage-3/revalidation-c1bd0d6/`](windows/stage-3/revalidation-c1bd0d6/) (88 files), run with the handoff's command. Every host exited as expected (`browser_failure` and `missing_runtime` with their expected errors), no browser process survived its host, no `dfx2-webview2-*` directory remained in TEMP, and nothing but the supervisor's five-second shutdown diagnostic reached stderr. The driver's response audit expectation is now `resource-response`, and the events arrived under that name.

| Case | Checks | Result |
| --- | --- | --- |
| `owned_basics` | 8 | pass |
| `in_process_alone` | 1 | pass |
| `confinement` | 11 | pass |
| `storage` | 6 | pass; closes all four browser windows of a resident host and reopens one, which starts empty |
| `error_pages_loopback` | 10 | pass: GET failure offers Retry and Retry repeats the GET; POST failure withholds Retry and no action replays the POST; a 303 target is a retryable GET and Retry after it resubmits no POST; a 307 keeps the POST and withholds Retry; 404 and 500 display as ordinary pages |
| `error_actions_same_uri` | 3 | pass |
| `attachment` | 5 | pass (attached GET failure offers Retry, attached POST failure withholds it) |
| `response_race` | 4 | pass, see below |
| `close_cases` | 5 | pass |
| `close_one_keep_one` | 3 | pass: the closed window's browser process exited while the host continued, B kept working, the native window remained |
| `lazy_loading` | 3 | pass: the last close unloads the loader and removes its directory |
| `profile_lifecycle` | 5 | pass: browser process exited when the window reported closed, profile gone, nothing framework-owned left in TEMP |
| `size_move` | 6 | pass by caption drag: `ModalLoops` 1, `ModalTurns` 8, the page's request completed while the loop ran, the shell moved |
| `browser_failure` | 2 | pass: the deliberate kill reported once as "WebView2 browser process exited (reason unexpected, exit code 1, )", application failed and drained |
| `missing_runtime` | 3 | pass |
| `exe_only` | 4 | pass |
| `body_reader` | 3 | pass: the 900,000-byte in-process upload echoed intact, no body reader remained, the empty POST answered as an empty body |
| `body_close` | 0 | **untested** |

Per-attempt `claimed-reads` for `body_close`: 0 ms → 0, 5 ms → 0, 10 ms → 0, 20 ms → 0, 40 ms → 0, 80 ms → 0. Every `closed` reply was well formed, every host exited 0 with `<nil>`, no TEMP leftovers, empty stderr. Six valid zeros with clean drains is untested by the handoff's rule: no close landed on a claimed read, so the native close-during-read ordering is still covered only by the modeled tests, exactly as on 2026-11-20.

**`response_race`.** The page's fetch of the race URL was held at the service, the navigation to the same URL failed with Retry (`error-page`, `status 8 http 0`), the held older response was then released and produced no `resource-response` or `load` event while the error page was pending (`events_after_error_page` null), and the error page's Retry was still the current action. This is the delayed-response-after-the-error-page ordering only. The other ordering, a same-URL response arriving before the failed navigation completes, is covered by the modeled regression (`TestLateSameURLResponseCannotSuppressTheErrorPage`) and not by this native case.

**Request bodies and `get_Content`.** The production `request-content` event is emitted only for a null getter result, so the seven events in `body_reader-body.jsonl` all read `get_Content S_OK null=true` and the 900 KB upload has none. Side by side, handler counts cumulative for window A when each run finished:

| Run | Page's report | `request-content` after the run's mark | Handler counts |
| --- | --- | --- | --- |
| `emptyPost` | status 200, echo length 0, SHA-256 of the empty body | one: `get_Content S_OK null=true` (seq 13) | `POST /api/echo` 2, `POST /api/report` 2 |
| `streamUpload` | status 200, echo length 0, SHA-256 of the empty body | one: `get_Content S_OK null=true` (seq 18) | `POST /api/echo` 3, `POST /api/report` 3 |

Unchanged from 2026-11-20 and consistent with the accepted in-process upload contract: a `ReadableStream` upload arrives as the same successful null stream as an empty POST. **No `body-unsupported` event** appears in any host log.

**Response-stream release and shutdown.** Three hosts printed the shutdown diagnostic (`in_process_alone`, `storage`, `response_race`, the quit-with-window-open pattern as before); each lists `native-requests=0 body-readers=0 response-streams=0` with `controller-closed=true`, the endpoint `closed=true drained=true` where one existed, and `native-problem=""`. No crash during close: the only browser-process failure is `browser_failure`'s deliberate kill. The explicit closes in `profile_lifecycle`, `close_one_keep_one` and `storage` completed with the browser process gone.

**Captures.** `screen-loopback.png` shows the loopback start page (origin, blue square, "stylesheet loaded", the buttons and form) and `screen-error-page.png` the framework's "This page did not load" page with the failing URI, "Nothing was submitted." and both actions; both complete and crisp.

**Native regressions rerun on this revision.** All pass, into fresh directories; no runner, test or example changed.

- **Synthetic mixer stage 4**, [`evidence/synthetic-mixer/windows/stage-4/run-c1bd0d6/`](../synthetic-mixer/windows/stage-4/run-c1bd0d6/) (`stage4.ps1`, 12:57:28 to 13:03:11 local, exit 0; console in `stage4-console.log`). Gates: `examples/mixer` ×100 (59.4 s), `ui` ×50, `internal/text/...` ×50 and the `controlsdrive` vet, all ok. The stage 2 regression matrix passed all 17 cases, each exit 0 with no failures and empty stderr, and its nested foundation matrix passed its 9 cases (`failure` with its expected exit 1). `modal-move`: 12 s in the loop, 363/362 frames for A/B (least 30/s each), 122 title updates each, 393 modal turns, 392 timers, 0 refusals; `modal-size`: 388/366 frames (least 31/30), 429 turns, 429 timers, 0 refusals; "show window contents while dragging" on. Fonts: Verdana for Latin, Greek and Cyrillic, Arial for Hebrew and Arabic, MS Gothic for Japanese, as before. The 17 stage 4 controls and command cases passed: clean, `commands_conserved`, 119 edits, first surface `760x520@1`, 114 accepted of which 107 applied, 2 rejected by the device (`#38 d gain 0.56 rejected: device refused`) and 5 canceled at shutdown (sequences 115 to 119), 5 refused as `queue full` at the 60 s delay; delayed latencies 700, 1390, 2084, 2561 ms at a 700 ms device delay. Timing: 68.3 frames/evaluations/presents per second with the feed running, 67.9 with 36 churn words, 0 settled; the process CPU column again reads 0% through every interval, the known measurement failure on this host, not a cost. Text cache: small budget (`49152:65536:48` at scale 1) A evicted 5,799 glyphs and 912 paragraphs with no sample over budget and A's retained image equal to fresh C's pixel for pixel (final A 34,500 glyph bytes in 224 entries and 27,201 paragraph bytes in 17; C 37,690 and 61,413); default budget 650 paragraph evictions, no glyph evictions, A equal to C. Process inspection: 28 modules, no descendants. I looked at the six desktop captures of window A: six crisp strips, the pending line (`pending 0.53 #34, mute #35`), the rejection error line, the overload line (`65 outstanding`, `5 refused`, `queue full`), live meters with three lines of Greek, Cyrillic and Latin churn and the settled text-cache state; nothing off screen or covered. The `fonts` capture shows the fallback line with its emoji as boxes, as in every earlier Windows run.
- **Foundation lifetime matrix**, [`evidence/foundation/windows/run-c1bd0d6/`](../foundation/windows/run-c1bd0d6/) (`audition.ps1`, 13:03:11 to 13:03:28 local, exit 0; `audition-console.log`): tests, vet and builds ok; all 9 cases with their expected exits (`failure` 1, the rest 0), none timed out, no failures, every final `drained` record at zero with no native problem, empty stderr, 28 modules and no descendants in the inspection.

**Manual auditions: not yet performed.** No operator was present during this session, and the agent cannot drive the desktop. The webview checklist in `examples/webview/README.md` and the mixer inspection (ordinary controls, resizing, closing one window while another continues, shutdown) remain for Michael with the clean-tree binaries above (`C:\Cygwin64\tmp\dfx2-webview.exe -open native,inprocess,loopback -devtools`, then `-open native,loopback`; `C:\Cygwin64\tmp\dfx2-mixer.exe -controls`). Record the performer and the observations here when done; this run's DPI remains a single 96 DPI display with no scale change possible.

**What this establishes.** The committed corrections execute natively on Runtime 151.0.4129.93 without regression across the eighteen browser cases, the seventeen mixer regression cases, the seventeen controls cases, both text-cache runs and the nine lifetime cases, and the first native execution of the Windows-only dispatch-window, clone and allocation tests passes. It does not establish the close-during-read ordering (untested), the response-before-failed-completion ordering (modeled only), the race detector on Windows, or the manual checklist. No fixture, driver or runner changed. Nothing contradicts the implementation or its recorded claims. Terminus remains not clean and stage 3 is not accepted; the deferred findings from `af5151522c39` are unchanged.

### Linux review of the `c1bd0d6` Windows revalidation

Return commit `bbff57c` contains evidence and documentation only. No production code, test or driver changed. The browser summary records 17 passes and one untested case, `body_close`. Its 33 host logs each contain exactly one return, and the recorded host results contain no temporary leftovers. The synthetic-mixer summaries record 17 controls cases, two text-cache runs and the 17 regression cases with the nested foundation run. The standalone foundation matrix has nine expected results. The review found no new implementation issue in those results. Representative error-page and rejected-command screen captures were also inspected; this does not substitute for the manual audition.

The full no-cgo suite and Linux vet pass on the returned tree. Native Linux matrices were not rerun because the return changed no implementation. Current documentation now identifies the revalidated revision. The return's reference to the before-completion regression was corrected to `TestLateSameURLResponseCannotSuppressTheErrorPage`; its native coverage claim is unchanged.

Michael confirmed that the Windows manual auditions have not yet been performed. These remain the next hands-on step. The close-during-read ordering remains untested natively, the before-completion response ordering remains modeled only, and Windows race/DPI coverage remains as disclosed above. The recorded Terminus verdict and deferred findings are unchanged. No new Terminus round was run, and stage acceptance is not claimed.

### Windows manual return — 2026-11-24

After the Linux return review and the request to perform both Windows auditions, Michael reported: "all seemed to work reasonably well." This records an operator-reported manual pass for the webview checklist and the mixer controls/resize/close audition on the returned candidate. No specific defect or measurement was supplied, so no new performance, DPI or timing claim is made.

The manual prerequisite is closed. The disclosed `body_close` and response-ordering coverage limits remain, as do the recorded review dispositions. The recommendation is to accept stage 3 with those limits and follow-up dispositions, deliberately align the canon, then proceed to the mixed native/browser mixer in stage 4. This manual result does not itself change the Terminus verdict or record formal stage acceptance.

### Stage 3 closeout — canon `76e1f94`

Michael committed the deliberate canon alignment after the automated return, Linux checks and his manual pass. This closes stage 3 with the documented coverage limits and development-stage follow-ups. The alignment accepts inherited GET eligibility during a redirect and Open's local startup-error limitation as follow-up work, alongside the previously deferred blocking-response failure and loader-unload machinery. The historical Terminus verdict remains not clean; acceptance reflects the recorded dispositions, not a rewritten verdict.

The implementation target was `c1bd0d6`, the Windows evidence returned in `bbff57c`, and the Linux review/manual record was committed as `e0adfd2`. Stage 4 is the mixed native/browser mixer described in the existing work order. The overall webview arc remains open; its intent documents are not retired at this stage boundary.
