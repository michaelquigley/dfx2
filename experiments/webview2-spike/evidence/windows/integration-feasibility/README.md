# Windows integration feasibility: handoff

Status: **not run on Windows.** Candidate: **awaiting Michael's commit.** There is no commit hash for it yet; never write one down before it exists. Record the commit you check out (`git rev-parse HEAD`) and any dirty tree (`git status --short`, plus `git diff` saved beside your results). Nothing in this directory has executed on Windows, and cross-builds from Linux are not execution.

Handoff direction (2026-11-12): Linux is completing the affected browser reruns and Michael's error-page check under the [closeout task](../../../../../docs/future/webview-feasibility-spike.md#approved-next-task-linux-closeout-and-windows-handoff). Its signal findings are now a monitored compatibility risk, not a request for further Windows work. Do not port Linux signal accommodations or the cgo comparison. This handoff remains `CGO_ENABLED=0` and covers passes B–D. Record the eventual transferred commit and any dirty diff. The Linux closeout results are in the [Linux record's status and closeout sections](../../../../webkitgtk-spike/evidence/linux/integration-feasibility/README.md#status-2026-11-12-linux-closeout).

This is the Windows half of the [feasibility spike](../../../../../docs/future/webview-feasibility-spike.md). Read that brief, the [integration draft](../../../../../docs/future/webview-integration.md), this experiment's [README](../../../README.md), and the [Linux result](../../../../webkitgtk-spike/evidence/linux/integration-feasibility/README.md). Do **passes B–D** and the common evidence requirements; skip pass A (Linux signals). Linux conclusions do not transfer: in particular, Linux needed no on-disk profile, and WebView2 does. You need no grimoire, Terminus or root-framework work. Leave changes uncommitted for Michael; do not commit or push.

## Already landed (portable, shared with Linux)

Package `dfx2webview2spike/feasibility` in this module, used by the Linux host and ready for yours:

| Piece | What it gives you |
| --- | --- |
| `NewToken`, `CookieName` (`dfx2-endpoint`) | A 32-byte random endpoint credential and its cookie name. Never log the value. |
| `Guard(next, token, host, origin, csp, counters)` | Owned loopback access check before the handler: exact credential, exact `Host`, and `Origin` when present. Refusals are 403 by reason, never reaching `next`; it adds `OwnedCSP` as its own header and strips only the credential from the application's cookies. |
| `Count`, `Counters` | Separate admitted, refused-by-reason and handled counts. |
| `NewPage()` | The probe application: `/`, `probe.js`, `probe.css`, `logo.svg`, `/api/query`, `/api/echo` (POST), `/api/form` (POST, 303), `/api/stream` (chunk 2 waits for `/api/stream-ack`), `/api/cached` (`max-age=600`, `X-Count`), `/api/redirect`, `/api/redirect-out?to=` (redirects anywhere, to test enforcement after redirects), `/api/abort` (loopback: drops the connection), `/api/slow` (3 s or cancellation), `/api/count`, `/api/report` (POST JSON to the optional `OnReport` hook). Per-path counts via `Counts()`. |
| `probe.js` functions | `probe.checks()`, `probe.stream()`, `probe.storageSet(tag)`, `probe.storageRead()`, `probe.cached()`, `probe.cachedImage()`, `probe.attacks(collector, otherEndpoint)`, `probe.navigate(how, collector)` with `how` one of `location`, `redirect`, `popup`, `form`. Run them through `ExecuteScript` and read the returned JSON, or page-driven: load `?run=NAME&id=ID` and the page posts its result to `/api/report`, then performs any `then=` action (`navigate`, `go`, `submit`). Linux's VM-free host uses only the page-driven form. |
| `RemoveAfterRelease(dir, released, attempts, interval, remove)` | Bounded deletion only after native release; refuses an unreleased directory (`ErrNotReleased`); names persistent residue in its error; `remove` injectable for the labeled failure test. |

`feasibility_test.go` covers the guard's refusals, policy composition, streaming order and deletion with injected failures. The existing fixture, adapter and Windows host are unchanged.

## Linux outcomes that bear on Windows

The Linux closeout (2026-11-12) reran every B–D case with its VM-free candidate: 19 of 19 scenarios and the 14-case single-window regression passed on WebKitGTK 2.52.6, and Michael's manual Retry/Return check passed. These lessons may transfer. The mechanisms are Linux's and must be re-established for WebView2:

- **Cookie before navigation.** Linux waits for the native cookie call's completion and the content filter's installation before the first navigation. A close during installation must navigate nowhere and drain. An injected installation failure must be visible and navigate nowhere.
- **Enforcement attribution needs the control.** With neither filter nor CSP, the collector received 12 distinct paths, 10 of them carrying the credential. With either mechanism alone, or both, it received nothing. Top-level location and redirect attempts were refused by the host's navigation policy, forms by policy or CSP, popups by the `create` signal or a null `window.open`. Report Windows' equivalent mechanism for each request class, and keep the no-enforcement control.
- **Native teardown order.** On Linux, destroying a window's context inside an async completion crashed later. The fix: tear down only after the last native completion has returned, one loop iteration later. Expect a WebView2 analogue around completed-handler lifetimes; establish it rather than assume it.
- **"Down" servers must close connections.** Kept-alive connections let a stopped server keep answering, so the attachment server sends `Connection: close` on every reply.
- **Method for Retry.** Linux custom-scheme navigations report no HTTP method, so form navigations are treated as POST. Treat any unknown method as POST.
- **Judge Retry by navigations, not handler counts.** Over Linux loopback, one failed navigation reached the Go handler 1 to 3 times below the page (unexplained). One click produced exactly one allowed navigation. Count navigations in your driver, as Linux does with its `navigation-allowed` events.
- **Error page.** Host-generated markup with no script, actions as nonce'd links the host intercepts. Linux places the two buttons at fixed positions so native input can reach them. Its automated runs click with synthesized toolkit input. Michael's manual check of Retry and Return to start is separate and also required on Windows.
- **Page-driven automation is available.** `ExecuteScript` has no Linux-style constraint on Windows, but `?run=…` with `/api/report` gives the same results through the handler, and is the form Linux used.
- **Driver detail.** Do not reuse a window label after closing it if your status is keyed by label. On Linux this made one recorded count invalid.
- **Not dependencies:** Linux's signal findings (a monitored Linux risk), the cgo comparison and WebKitGTK's graphics-teardown question.

## Native work remaining (Windows)

Implement a separate probe mode in this host (for example `-feasibility`, console-driven like Linux's) without changing the ordinary single-window modes. Validate each interface against the pinned SDK's `WebView2.idl` and extend `idl_test.go`.

1. **Per-window temporary sessions.** Each window gets its own environment over a unique user-data folder under an experiment-owned root, for example `%TEMP%\dfx2-feasibility\profile-<pid>-<n>`, never `webview2-userdata\<mode>` and never a browser profile of yours. Record paths, not contents.
2. **Credential before navigation (loopback).** Through `ICoreWebView2_2::get_CookieManager`: `CreateCookie(CookieName, token, "127.0.0.1", "/")`, `put_IsHttpOnly(TRUE)`, `put_SameSite(STRICT)`, session (no expiry), `AddOrUpdateCookie`. Navigate only afterwards. Establish whether `AddOrUpdateCookie` completion is observable before the first request, and say so; if it is not, a cookie-less first request refused by `Guard` is the evidence.
3. **Same-origin enforcement.** Candidates: `AddWebResourceRequestedFilter("*", ALL)` answering any other origin with a synthetic response, so no network request is made; `NavigationStarting` cancel outside the origin; `FrameNavigationStarting` cancel for every subframe; `NewWindowRequested` handled with no window; plus `Guard`'s CSP. Report which mechanism covers which request class, as the Linux table does, including the no-enforcement control.
4. **Error page.** On `NavigationCompleted` with `IsSuccess` false (not cancellation or policy), show host-generated markup with no script, for example via `NavigateToString`. Its Retry and Return to start actions go to a scheme the host intercepts in `NavigationStarting`, with a per-failure nonce; the host loads only what it recorded. Retry only for a failed GET: establish how to learn a navigation's method (for example from the document's `WebResourceRequested` request) and the 303/307 cases. Treat unknown as POST.
5. **Release and deletion.** Close the controller and wait for the browser process to exit (`ICoreWebView2Environment5::add_BrowserProcessExited`, or equivalent evidence) before `RemoveAfterRelease`. Record attempts and elapsed time; inject a post-release failure through `remove` and label it injected. Never kill a browser process another window uses.
6. **In-process isolation.** Two in-process windows with their intercepted origin: a page naming the origin reaches only its own window's handler (count invocations).
7. **A driver** like `webkitgtk-spike/feasibility.py`, in Go under this directory (the existing `evidence/windows/drive` is the pattern). It needs a collector and a stoppable attachment server on `127.0.0.1` that close every connection, so a stopped server is really unreachable. Judge with server counters, not page views.

## Commands

From `experiments\webview2-spike` in Windows PowerShell:

```powershell
$env:CGO_ENABLED = "0"
.\setup-loader.ps1                      # pinned SDK 1.0.4129.50; checks package and loader hashes and signature
go test ./...
go vet ./...
go test ./feasibility -count=1 -v
$env:WEBVIEW2_IDL = "$PWD\.sdk\1.0.4129.50\WebView2.idl"; go test -run IDL -v .
go build -o dfx2-webview2-spike.exe .
```

From `experiments\webkitgtk-spike` (shared fixture checks; this module builds there but runs nothing native on Windows):

```powershell
$env:CGO_ENABLED = "0"
go test dfx2webview2spike/adapt dfx2webview2spike/fixture dfx2webview2spike/feasibility
```

Add your new probe commands here once they exist. Rerun the existing single-window audition (`evidence\windows\drive`) if you change shared host paths.

## Expected outcomes

Mirror the Linux scenarios:

- `loopback-basics`: assets, query, POST, reload, same-origin redirect and streaming authenticated; credential unreadable; external clients refused before the handler.
- `confinement` × {both, filter-equivalent only, CSP only, control}: the control must leak to the collector, or the probes do not discriminate; with enforcement, nothing reaches it.
- `inprocess-isolation`.
- Cookie close race and injected failure.
- `storage` (two windows on one attachment origin; reload; close A while B works; reopen fresh; server survives).
- `error-pages`: GET Retry, POST shows only Return to start, 303 and 307, 404/500 normal, failed fetch or asset keeps the page, initial-load failure.
- Owned-transport load failure: loopback `/api/abort`, in-process failed request.
- Forced-crash residue: the next run uses a fresh folder and deletes no other instance's.
- Shutdown: ordinary, pending request, devtools open, in both transports, with browser-process exit observed.

A missing mechanism is **untested** or **blocked**, never a pass. If WebView2 cannot meet an agreed behavior, keep the failed probe and report the smallest conflict and alternatives; do not weaken the contract.

## Manual steps

With a connected, unlocked desktop (a minimized RDP session yields black captures):

1. Open two windows of each owned transport and one attachment window; click through the probe page; note anything unexpected.
2. Stop the attachment server, navigate, and check the error page; click **Retry** after restarting it and **Return to start** after a failed form POST. Confirm the server shows no repeated POST.
3. Close windows in different orders, including one with devtools open, and watch Task Manager for browser processes that remain.

## What to return

Add `results.md` beside this file: Windows build, Runtime and SDK versions, console or RDP, display and DPI, the checked-out commit from `git rev-parse HEAD` (never a guessed or placeholder hash) and any dirty diff, each scenario's outcome with its mechanism, screenshots of each error page, deletion timing, untested items, and Michael's manual observations. Keep raw logs and summaries under a new run directory; keep profiles, SDK binaries and dumps out of git. Add a short entry to the day's file in `docs/journal/`.
