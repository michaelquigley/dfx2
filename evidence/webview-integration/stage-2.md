# Webview integration stage 2: Linux browser integration

Stage 2 of the [work order](../../docs/future/webview-integration-work-order.md) hosts `webview` windows on Linux/amd64 in the system WebKitGTK 4.1, inside the GTK backend's main loop, in all three modes, with the GC-signal reservation Michael approved on 2026-11-13. Built behavior is in [webview.md](../../docs/current/webview.md#linux-host). Implemented on `a4dac69` plus the uncommitted tree. Windows still refuses browser windows; nothing here is Windows evidence.

Host: Ubuntu 26.04.1 LTS, Linux 7.0.0-43-generic amd64, Go 1.27.1 (module language 1.26), installed WebKitGTK and JavaScriptCore 2.52.6, GTK 3.24.52, libsoup 3.6.6, GLib 2.88.0; exact package versions are in the [run manifest](linux/stage-2/manifest.json). Every build, test and vet uses `CGO_ENABLED=0`, except the race-detector repetitions, which need cgo for the detector only.

## What was added

- **`internal/platform/webkit`**: the WebKitGTK attachment. Bindings load lazily through purego on the first attachment (`native_linux_amd64.go`); five permanent trampolines route by numeric token (`callbacks_linux_amd64.go`); the approved GC-signal reservation (`signal_linux_amd64.go`); the attachment (`browser_linux_amd64.go`); portable policy for navigation methods, Retry, origins, filter rules and the error page (`policy.go`); bounded post-release removal (`cleanup.go`); the audit snapshot (`audit.go`).
- **GTK backend**: creates browser shells (a toplevel with no drawing area) and supplies their parent descriptor.
- **Platform contract**: `BrowserConfig.GCSignal`.
- **Public API**: `webview.Options.LinuxGCSignal`, required on Linux and inert elsewhere; `webview` selects the WebKitGTK factory on Linux/amd64 only.
- **Core**: `appcore.BrowserWindows` and `appcore.AuditionBrowser`, internal audition hooks beside the existing ones; no public method.
- **`examples/webview`**: the audition host and its page, also the manual audition ([README](../../examples/webview/README.md)). The attachment's audition methods (`AuditionLoad`, `AuditionErrorAction`, `AuditionURI`, `AuditionState`) navigate only as a user could, through navigation policy, and refuse before initialization completes.
- **Drivers**: [`linux/webview_drive.py`](linux/webview_drive.py), the integrated browser matrix, and [`linux/stage2_matrix.py`](linux/stage2_matrix.py), which runs it under three display configurations plus the stage 4 regression matrix and records the manifest.

## Decisions within the work order's latitude

- **Option spelling.** `Options.LinuxGCSignal syscall.Signal`. Zero on Linux, or any negative value, is an invalid option, latched like other opening configuration errors. Range, disposition, mask, `os/signal` ignore and `JSC_SIGNAL_FOR_GC` agreement are checked natively before WebKit's first object; failure there is a fatal initialization error. After Go releases the signal, a failure is latched for the process.
- **Per-window ephemeral contexts**, not one shared context: each window's cookies, storage, cache and network process are its own, and closing a window ends its WebKit processes (observed). The in-process origin is `dfx2://app` for every window; routing comes from each context's own scheme handler.
- **The only on-disk resource is the content filter**, compiled into a per-window temporary directory; it is removed after release through the core's cleanup path, with the work order's 20 × 100 ms bound and a named `FileDeletionResidue` after that.
- **Attachments get a filter too**, carrying only the child-frame rule, so subframes are refused for attachments as for owned windows. Attachments' subresources are not confined.
- **Response copies are tracked to their release.** Responses are copied into `g_malloc` storage owned by a GBytes whose free function, possibly on another thread, hops back to the UI thread through an idle source; only then is the request released. The attachment reports released only when no copy remains.
- **Retry methods are remembered per URI** (the last 16 allowed navigations), because `decide-policy` also sees subframe navigations; an in-process navigation reported without a method is a GET unless WebKit marks it a form submission, which is then unknown and gets no Retry.

## WebKitGTK limits found natively

Michael accepted these Linux custom-scheme limits on 2026-11-14. Applications needing cookies or HTTP redirects choose loopback explicitly; no emulation or silent fallback is added.

- **In-process redirects are not followed.** A 3xx response from the custom scheme reaches the page itself: `fetch` sees the 302, and a top-level redirect leaves the window blank on its own origin. Nothing leaves the origin (the collector saw nothing). Applications that need redirects use loopback.
- **No cookies for the custom scheme.** `document.cookie` and `Set-Cookie` have no effect in-process; local and session storage work and are isolated per window.
- **In-process handlers cannot produce a load failure**, because the adapter answers a deliberate abort with a fixed 502 (a stage 1 decision). The error page and the unknown-method Retry rule are therefore exercised natively through loopback and attachments, and the in-process form rule portably.
- **Some routes stop before navigation policy.** WebKit blocks a script-initiated `window.open` itself, and the framework policy's `form-action 'self'` blocks an outside form; the policy refusal is the backstop. The matrix judges every route by the collector and the window's URI.

## Portable checks

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=300s
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
CGO_ENABLED=0 go test ./internal/platform/webkit ./internal/platform/gtk ./webview ./internal/appcore -count=50 -run 'Test[^B]|TestB[^i]'
CGO_ENABLED=0 GOMAXPROCS=1 go test ./internal/platform/webkit ./webview ./internal/appcore -count=20 -run 'Test[^B]|TestB[^i]'
go test -race ./internal/platform/webkit ./webview ./internal/appcore -count=10 -run 'Test[^B]|TestB[^i]'   # cgo for the detector only
CGO_ENABLED=0 go test . -run TestDependencyBoundaries -count=1
CGO_ENABLED=0 go test ./internal/platform/webkit ./internal/platform/gtk -run Headers -count=1 -v
python3 -B -m unittest -v evidence/webview-integration/linux/test_webview_drive.py   # the driver's child-exit acceptance
```

All passed. The repetitions exclude the header comparisons, which only reread headers. The new portable tests:

- **Policy** (`policy_test.go`): method classification and Retry, per-URI final methods across redirect hops and bounded memory, parsed-origin allowance, filter rules for owned windows and attachments, error-page escaping and action parsing.
- **Attachment state machine** over recorded native calls (`browser_linux_amd64_test.go`): the first navigation waits for filter, credential and core readiness; a closing window never navigates and releases a late filter; close settles waiting requests, finishes a reading request only when its canceled read returns, schedules destruction only after the last owed callback, and reports released only after WebKit frees every response copy; error-page actions are single-use, forged nonces are refused and Retry is withheld from form submissions; residue after bounded attempts. Removing the copy-release guard, or the owed-callback guard before destruction, makes these tests fail.
- **Reservation** (`signal_linux_amd64_test.go`): refusals for a missing signal, range, a foreign disposition, `JSC_SIGNAL_FOR_GC` conflict and a missing entry point, all before JavaScriptCore is called; release from Go; idempotent repetition; an inconsistent later reservation; a latched refusal after Go released the signal. The tests restore dispositions and Go's ignore state, so they repeat in one process.
- **ABI** (`abi_linux_amd64_test.go`): every binding except the private `JSConfigureSignalForGC` and glibc's real-time range functions matches the installed WebKitGTK 4.1, libsoup 3, GLib/Gio and GTK headers.
- **`webview`**: Linux requires `LinuxGCSignal`; the refusal path still runs on Linux with the factory removed. The dependency walk now also requires that it sees `internal/platform/webkit`, which stays out of the root's imports.

## Native Linux matrix

```sh
python3 evidence/webview-integration/linux/stage2_matrix.py --out evidence/webview-integration/linux/stage-2-final --base /tmp
```

[`linux/stage-2-final/`](linux/stage-2-final/), 2026-11-14, on `a4dac69` plus the working tree recorded in its [manifest](linux/stage-2-final/manifest.json), each display configuration in its own isolated headless GNOME Shell. The driver starts fresh hosts per case, is their child subreaper and reaps every WebKit descendant after each host exits.

The final candidate, after all six review findings below were fixed: the `webview` host hashed `c985e240c5437042fb3bd5e41230fbba5a9bdfa7ac666b1c86d08809fc7a43f1`. **All ten cases passed in each configuration: Wayland, XWayland, and XWayland at `GDK_SCALE=2` (30 of 30)**, judged by the corrected child-exit check. Every host's stderr was empty except `browser_failure`'s own printed error; every reaped WebKit network and web process exited 0; only children identified as `bwrap` helpers ended by SIGKILL; no descendant or filter directory remained. Raw events, per-host stderr and summaries are under each configuration's directory. Rebuilt afterward, the host hashes identically, so this result applies to the final tree.

Earlier runs, kept: [`linux/stage-2/`](linux/stage-2/) (2026-11-13, host `e86dc257d135983b35f98675b46a7bde068db61c295670e15077b1aa7ada62de`) is the candidate after R1–R4, before F1 and F2; it also passed 30 of 30 and 8 of 8 under the earlier child check. The first candidate's run, before R1–R4, also passed 30 of 30 and 8 of 8 (host `246e67fc…`); its directory was replaced by the next run before preservation was asked for, and only this summary remains.

Observations recorded beside the checks: in the Wayland run, the loopback error-page case's handler counted 9 GETs of the failing path for 4 failed GET navigations, the transport redelivery the feasibility spike also saw, while POST counts never moved on Retry or Return to start; the closed in-process window's network and web processes exited while the host continued (four WebKit processes before, two after); the killed web process was reported as reason 0 (crashed).

What each case establishes:

| Case | Checks |
| --- | --- |
| `owned_basics` | Both owned transports load the page with stylesheet, image and script; relative fetch with an encoded query; a 1005-byte POST body; an ordinary 404; the HttpOnly credential is invisible to `document.cookie`; only the loopback endpoint listens (`ss`), and a client without the credential gets 403; loopback streams its first chunk before the handler finishes; closing the in-process window leaves the loopback one |
| `in_process_alone` | An in-process window's host owns no listening socket |
| `confinement` | Loopback and in-process pages aim images, scripts, stylesheets, fetch, XHR, beacons, WebSocket, EventSource, iframes, redirected subresources, a service worker and another window's endpoint at a collector on another port: the collector receives nothing and the other endpoint's handler sees nothing. Location, redirect, popup and form navigations to the collector leave each window on its own origin, with nothing collected |
| `storage` | Two in-process windows keep separate local and session storage; a navigation keeps a window's own; two loopback windows on one host do not share cookies across ports; a reopened window starts empty (resident reopen) |
| `error_pages_loopback` | A failed GET shows the error page with Retry, which repeats the GET; Return to start loads the start page; a failed POST offers no Retry, a forced Retry is refused, and Return to start adds no POST; a 303 after a POST fails as a retryable GET whose Retry adds no POST; a 307-preserved POST offers no Retry; actual 404 and 500 pages display normally |
| `attachment` | Navigation and a redirect to an undeclared origin are refused, with nothing collected; a failed GET offers Retry; a failed form POST offers none and Return to start adds no POST; closing the window leaves the separately owned service answering |
| `close_cases` | Close during initialization in both owned transports; close with a handler that ignores cancellation, in both transports, keeps the window (and `Done`) pending until the handler is released; close right after a 900 KB in-process upload starts |
| `close_one_keep_one` | Closing one browser window leaves another browser window and a native window working; closing the last window exits a window application; the closed window's WebKit processes exit while the host continues (recorded) |
| `lazy_loading` | A host with only a native window maps neither `libwebkit2gtk` nor `libjavascriptcoregtk`; the first browser window maps both |
| `browser_failure` | Killing a window's web process fails the application with "web process terminated", and it drains and exits |

Every host exit is also checked: exit status and `Run`'s result as expected; every reaped descendant exited 0, except that a child identified as a `bwrap` sandbox helper may end by SIGKILL (its `--die-with-parent` design; an unnamed child gets no such allowance) and the deliberately killed web process by its signal; no descendant left after 15 seconds; no GLib critical, JavaScriptCore handler warning or panic on stderr; no `dfx2-webview-filter-*` directory left in the host's own temporary directory.

### Not covered natively

- **Close while a response awaits native completion**, and close during a body read at a chosen point: native timing cannot be held there. The attachment state-machine tests cover both orderings; the matrix's upload close is best effort.
- **The in-process unknown-method Retry rule** cannot arise from a handler (above); it is covered portably.
- **Cache isolation**: each window has its own handler, so a cache count cannot distinguish sessions; ephemeral per-window contexts are the claim, not a measured cache.
- **The inspector** opens only in the manual audition. **Mixed DPI** beyond XWayland at `GDK_SCALE=2`, **native X11** and **display-scale changes** are not exercised.
- **Signal coexistence** is not re-investigated, per the work order. The matrix confirms only that JavaScriptCore printed no handler-replacement warning for SIGUSR1; signal delivery is not exercised.

## Foundation and UI regressions

The GTK backend and the shared core's opening path changed, so the synthetic mixer's stage 4 matrix ran unchanged as the last step of the same run, into [`linux/stage-2-final/regression/`](linux/stage-2-final/regression/): mixer `00101522d45827e57b4a5e5a2fdd282eeff0442df88744a32c68ff2750e94dce`, lifetime `1163d7353f7a8f3ed364591a2b59d2828e2e3627b5e325e48096ddf7999eaf57` (unchanged since the previous run: the later fixes touched only the WebKit package and the driver). All eight runs exited 0: the controls and command audition with its text-cache runs and stage 1's regression matrix under Wayland, XWayland and XWayland at scale 2, and the foundation lifetime matrix under Wayland and XWayland. The [audit](linux/stage-2-final/regression/audit.json) read 55 application process logs, each draining once with every ownership count at zero and empty stderr, and decoded all 78 PNGs. Both binaries hash identically when rebuilt from the final tree. Neither links the browser package; `lazy_loading` above is the native-only library check.

## Manual audition

Passed, reported by Michael on 2026-11-14 using the [example's five-step checklist](../../examples/webview/README.md#manual-audition-linux). He reported that the audition worked fine and clarified the error-page observation: the ordinary failed GET offers Retry and Return to start; **POST to a failing page offers only Return to start**, as required. No manual defect remains from that observation.

## Operator-review follow-up (2026-11-14)

Michael passed the manual audition and accepted the in-process no-redirect/no-cookie limits. He authorized narrowing the driver's deliberate-kill exception: only the actual targeted PID ending by SIGKILL is excused, rather than every discovered web PID ending by any signal. The named bwrap exception is unchanged. The regression's SIGSEGV case failed against the old check; all five driver tests now pass, including rejection of a different PID and a nonzero normal exit.

The affected `browser_failure` case was rerun under Wayland, XWayland and XWayland at scale 2 into [`linux/stage-2-driver-rerun/`](linux/stage-2-driver-rerun/): **3/3 pass**, with raw child statuses and the exact commands retained in its manifest. The rebuilt browser hash remains `c985e240c5437042fb3bd5e41230fbba5a9bdfa7ac666b1c86d08809fc7a43f1`. Product code did not change in this follow-up, so the other final-candidate native and manual results carry forward; the full matrices were not repeated. Earlier evidence remains intact. The deliberately killed PID was not returned to the driver's reaper in these three runs: WebKit reported the crash and the application failed and drained, while the exact PID/signal acceptance branches are established by the synthetic unit tests. Reaped children were clean or named bwrap SIGKILLs; do not infer a collected exit status for an unreaped PID.

The authorized canon alignment updates the existing transport, native-ownership, failure and validation qualities and the rubric to cover the Linux host, its accepted limitations and the approved signal mechanism. No new quality or blocking disposition was added. All canon edits remain uncommitted, including the stage 1 alignment; unrelated project edits were preserved. Review against the aligned canon is recorded below. Stage 3 has not started.

## Review

Terminus `b217459700b2` reviewed the first candidate: not clean, four blocking findings, triaged one at a time with Michael. Each fix has a regression shown to fail before it.

| Finding | Disposition |
| --- | --- |
| R1: a body read ending in the endpoint's ID-less abort lost the admitted request's ID, so the request was never released and the endpoint never drained | The read callback carries the admitted ID into such decisions; the endpoint's refusal convention is unchanged. `TestBodyReadAbortsReleaseTheAdmittedRequest` (real endpoint; a failed read, and cancellation during a read) |
| R2: native request metadata was copied into unbounded Go storage before admission | The browser passes views of WebKit's strings, valid through the synchronous request call, and measures metadata natively first, stopping past the limit the endpoint publishes; an oversized request reaches the endpoint with only its measured size and is refused with the documented 431. The transient header slice is built only within the limit and covered by the endpoint's new fixed transfer reserve, out of the existing buffer budget. Public limits are unchanged. `TestOversizedNativeMetadataIsRefusedBeforeAnyHeaderSlice`, `TestOrdinaryHeadersSurviveTheirNativeStorage` (retained data intact after the native storage is overwritten), `TestFiniteRefusesABrowserMeasuredOversizeWithoutHeaders` |
| R3: the ID-zero refusal leaked its GError | One `failNative` helper finishes and frees for all three failure paths. `TestNativeFailuresFinishOnceAndFreeTheirErrors` |
| R4: web-process termination was suppressed once closing began | Termination is a failure whenever it arrives; destruction disconnects the signal first. Verifying it reaches `Run` exposed a second suppression: the core kept only the first failure, so a crash after an earlier failure vanished. The core's browser-failure path now joins a later failure as a distinct record. `TestTerminationWhileClosingReachesRun` (real core and attachment; window close, and after an earlier application failure), `TestBrowserFailureAfterAnEarlierFailureIsKept` |

Terminus `ed898b0bb169` reviewed the candidate after R1–R4: not clean, two new blocking findings, again triaged one at a time.

| Finding | Disposition |
| --- | --- |
| F1: closing suppressed every filter-save and credential completion error, not just cancellation | While closing, only `g-io-error-quark`/`G_IO_ERROR_CANCELLED` is silent; any other failure reaches `host.Failed`; a filter saved after close is released without being installed or navigating. `TestInitializationResultsAfterClose` (canceled, genuine, code 19 from another domain, and succeeded, for both operations, each settling so the attachment releases) |
| F2: the driver's child check ignored nonzero exit statuses | `child_problem` requires exit 0 from every reaped descendant; only a child identified as `bwrap` may end by SIGKILL, and a deliberately killed process by its signal. `test_webview_drive.py` |

Terminus `dee216eeb62b` reviewed the final candidate, with its native evidence: **clean, no findings**. Coverage notes only the example's static page assets (HTML, CSS, SVG, JavaScript), which no project quality's territory includes; no file is uncovered by a code quality. A clean review is not native validation or acceptance. Michael subsequently passed the manual audition and approved the limitations and canon alignment above. The follow-up review is recorded below.

## Review under the aligned canon

Terminus `bbf66b8ac6b0` returned **not clean**, with one blocking finding: a new navigation to the same failed URI leaves the previous error page's nonce valid (`browser_linux_amd64.go:649–652`). An old Retry/Return action can therefore still be accepted after that navigation succeeds. Michael accepted the correction. The URI-only invalidation was reproduced with both fake native calls and an actual same-URI recovery on the previous binary; the correction and validation are recorded below.

Coverage: 791 covered, one excluded, four uncovered of 796 starting-point files. The uncovered files are the example's HTML, CSS, SVG and JavaScript assets, the same known territory gap. The driver correction and canon alignment were not flagged. The earlier clean review remains historical, not the latest verdict.

## Same-URI action correction

Every allowed navigation now clears the preceding error action nonce, including navigation to the same URI. No new state, binding or callback was added. WebKit's alternate error document bypasses ordinary navigation-policy decisions: see [PolicyChecker at the tested 2.52.6 tag](https://github.com/WebKit/WebKit/blob/webkitgtk-2.52.6/Source/WebCore/loader/PolicyChecker.cpp#L159). The native error-page cases must still establish that the current Retry/Return actions remain usable; the source observation alone is not that evidence.

`TestNewNavigationInvalidatesSameURIErrorActions` failed before the fix for both captured old action URLs: Retry and Return each caused a native load after a new same-URI navigation. Both pass now, together with the existing single-use/current-action test over 30 repetitions. The new native `error_actions_same_uri` case changes a service response from connection failure to success at the identical URL and waits for the recovered document's title before checking that no old error action is available. It failed against the old browser binary; that intended failure is retained in [`linux/stage-2-actions-before/`](linux/stage-2-actions-before/).

The full no-cgo suite, Linux vet, Windows and Darwin build/vet, and Python driver tests pass. Mixer and lifetime rebuild to their final-regression hashes (`00101522…` and `1163d735…`), so the unchanged native-only/drawn regression evidence carries forward. The new browser matrix passes **33/33**: all eleven cases under Wayland, XWayland and XWayland at scale 2, in [`linux/stage-2-actions-rerun/`](linux/stage-2-actions-rerun/). Its browser hash is `9b5713ef4a02ce5b960825e6cc736b149718f8f5a343603c9a96fe21dda65fc5`, matching the separately rebuilt binary. Existing GET/POST/303/307 Retry/Return cases and the new same-URI case all pass. This correction was included in the clean review below. Michael's manual audition above describes the preceding candidate; this follow-up changes only error-action invalidation and retains that earlier observation without claiming another manual run.

## Latest review and handoff

Terminus `4dc5c9de1ddf` returned **clean**, with no findings, under the aligned canon. Coverage is 900 covered, one excluded and four uncovered of 905 starting-point files. The four uncovered files remain the example's static HTML, CSS, SVG and JavaScript assets; the verdict does not claim coverage of them. The reviewer identified no material violation of the selected qualities and explicitly noted the focused tests, native reruns and disclosed limits.

Stage 2 is ready for Michael's acceptance and commit. His manual audition passed before the narrow action-lifetime correction; the corrected candidate passes all 33 native browser cases, including the current error actions and same-URI recovery. The accepted custom-scheme limits and canon alignment are recorded above. Only this verdict record and the journal changed after review. No further review loop is needed without another substantive change. Both repositories remain uncommitted; stage 3 has not started and Windows hosting remains unsupported.
