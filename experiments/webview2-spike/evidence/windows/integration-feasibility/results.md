# Windows integration feasibility: results

Executed on Windows on 2026-11-12, passes B–D, against the pinned SDK and the installed WebView2 Runtime. Everything under "native" ran on this desktop in real WebView2 windows with real browser processes; the portable tests are modeled. Nothing is committed.

**Summary.** Per-window temporary profiles with observed browser-process exit before deletion, native cookie installation before navigation, host-local error pages with Retry and Return to start, and in-process routing isolation all work on WebView2 as the integration draft asks. Native interception of every request is the primary confinement mechanism and is required: WebView2's navigation cancellation is not a network boundary. Three findings need Michael's decisions, below: WebSocket handshakes are not interceptable and are stopped only by CSP; a cancelled top-level GET navigation still sends its request, with the credential; and the browser's own transport re-sent a failed POST navigation once. The driver reports these as failed checks (3 of 22 scenarios), deliberately; nothing was weakened to pass.

## Host

| Item | Value |
| --- | --- |
| Windows | Microsoft Windows 10 Pro 10.0.19045.6466, 64-bit |
| WebView2 Runtime | Evergreen 151.0.4129.93, the installed one (`-runtime-dir` unused) |
| SDK | Microsoft.Web.WebView2 1.0.4129.50; `WebView2Loader.dll` SHA-256 `a9a09232…` matches the pin, loaded from beside the executable |
| Go | go1.27.3 windows/amd64, `CGO_ENABLED=0` throughout |
| Session | Remote Desktop, session 2, connected and not minimized for every run; a minimized client would capture black |
| Display | Microsoft Remote Display Adapter, 3440×1440, system DPI 96, scale 1 for every window. No other scale was available |

## Revision

Checked out `f71c306a493f8172407fb53dbb9ba44d65e8686b` ("webview: integration feasibility spikes and windows handoff"), clean at checkout. The Windows work is the dirty tree recorded in `run/environment.json` and reproduced here; `git diff` of the tracked files is in `run/source.diff` beside it, and the new files are listed:

- modified: `bindings.go` (new IIDs and slots, all checked by `TestBindingsMatchIDL` against the SDK's `WebView2.idl`), `com_windows.go` (vtable bound 64 to 192 slots, `ICoreWebView2_13` reaches 105; `callbackLive`), `host_windows.go` (records the loader hash and Runtime version it found), `host_other.go`, `main.go` (the `-feasibility` flags)
- new: `feasibility_windows.go` (the probe host), `evidence/windows/integration-feasibility/drive/` (the driver), this file, `run/`

Binaries: `dfx2-webview2-spike.exe` SHA-256 `8bb9133502cebf83ca1fe050b94809446ce0736d046d327a740485f4b1981941`, `feas-drive.exe` `a93bed96…`, built from that tree with `CGO_ENABLED=0`. The final run started 13:59:56 local and ended 14:01:35; the single-window regression followed at 14:01:37.

## Portable checks

From `experiments\webview2-spike`, all pass: `go test ./...` (root, adapt, feasibility, fixture), `go vet ./...`, and `go test -run IDL -v .` with `WEBVIEW2_IDL` set, which now also checks the cookie manager, cookie list, `ICoreWebView2_2`, `ICoreWebView2_13`, profile, `ICoreWebView2Environment5`, browser-process-exited, response-received, frame-navigation-starting and capture-preview bindings. `gofmt -l` is empty. These are modeled.

## The Windows mechanisms

What `feasibility_windows.go` does, so the results below can be read:

- **Per-window session.** Each window creates its own environment with `CreateCoreWebView2EnvironmentWithOptions` over its own user-data folder `<root>\profile-<pid>-<n>` (root `%TEMP%\dfx2-feasibility`, here `C:\Cygwin64\tmp\dfx2-feasibility`). The Runtime reports the profile at `…\EBWebView\Default` inside it. Each window therefore has its own browser process; two in-process windows with the same origin do not share one.
- **Credential before navigation.** On the controller callback: `ICoreWebView2_2::get_CookieManager`, `CreateCookie(dfx2-endpoint, token, 127.0.0.1, /)`, `put_IsHttpOnly(TRUE)`, `put_SameSite(STRICT)`, no expiry (`IsSession` true), `AddOrUpdateCookie`. That call is synchronous and has no completion; the host then calls `GetCookies(origin)` and navigates only when its completion lists the credential cookie. Whether the first request carried it is the endpoint's evidence.
- **Confinement.** `AddWebResourceRequestedFilter("*", ALL)`: every request of the window reaches the host, which answers anything outside the window's origin with a synthetic 403 and no network request. Plus `NavigationStarting` cancel outside the origin, `FrameNavigationStarting` cancel for every subframe, `NewWindowRequested` handled with no window, and the guard's CSP (`-feas-csp`). `-feas-filter=false` removes the interception for attribution.
- **Error page.** `NavigationCompleted` with `IsSuccess` false, not `OPERATION_CANCELED`, and no `WebResourceResponseReceived` for the document request: the host records the failed URI, its method (from the document-context `WebResourceRequested` request) and whether a POST preceded a redirect, then `NavigateToString`s script-free markup whose two links point at `https://dfx2-error.invalid/{retry,start}/<nonce>`. `NavigationStarting` cancels those and performs the recorded GET. Retry is offered only for a GET; an unseen method counts as POST. `IsBuiltInErrorPageEnabled` is off.
- **Release and deletion.** Close: stop, remove handlers, `Controller.Close`, release. Then wait for `ICoreWebView2Environment5::BrowserProcessExited`, release the environment one loop iteration later, and only then `RemoveAfterRelease` (20 attempts, 100 ms apart). Completions still owed (GetCookies, CapturePreview) also block teardown, unless WebView2 has already released their handler objects, which it does at Close; those are recorded as `completion-dropped`.
- **No native JavaScript.** Automation is page-driven (`?run=…`, results to `/api/report`), clicks are real `SendInput` pointer input at the error page's button positions, snapshots come from `CapturePreview`.

## Scenarios

`feas-drive.exe -exe dfx2-webview2-spike.exe -out evidence\windows\integration-feasibility\run`. Each scenario is its own host process; `run/<scenario>/result.json` has every check with its detail, `host.jsonl` the host's events, `host.stderr` its log. Exit criteria for every scenario: host exit 0 (killed, for the forced crash), no timeout, every browser process the host reported gone afterwards, no callback object alive, no callback off the UI thread, nothing alarming on stderr, no profile of that host left under the root.

| Scenario | Result | What it showed |
| --- | --- | --- |
| `loopback-basics` | pass, 9 checks | Below |
| `confinement-filter-csp` (default) | pass | Collector received nothing; no credential escaped |
| `confinement-filter-only` | **1 class leaked** | WebSocket handshake reached the collector with the credential (finding 1) |
| `confinement-csp-only` | **2 requests leaked** | Cancelled top-level GET navigations reached the collector with the credential (finding 2) |
| `confinement-control` | pass (the control leaks) | 16 requests reached the collector, 12 with the credential: the probes discriminate |
| `inprocess-isolation` | pass | Below |
| `cookie-close-race`, `close-during-controller`, `close-during-cookie`, `cookie-failure-injected` | pass | Below |
| `removal-failure-injected` | pass | Below |
| `storage` | pass, 7 checks | Below |
| `error-pages` | **1 check failed**, 11 passed | The failed check is the browser transport re-sending a POST (finding 3); every host behavior passed |
| `owned-load-failure` | pass | Loopback abort and in-process failure both show the page; Return to start works |
| `shutdown-{loopback,inprocess}-{ordinary,pending,inspector}` | pass, 6 of 6 | Drained, browser exit observed, profile deleted |
| `combined` | pass, 8 checks | Two live windows, pending requests, close and reopen, Retry in both transports |
| `forced-crash` | pass | Residue stays named by pid; the next host uses a fresh folder and deletes only its own |
| single-window regression (`evidence\windows\drive`) | pass | 14 of 14 checks in both modes, every close clean, prerequisites as before; `run/single-window-regression/summary.txt` |

### Pass B: credential and confinement

`loopback-basics`:

- The first document load was admitted: at the first `status`, 4 admitted, 4 handled, no refusals. The cookie was added synchronously (`S_OK`, HttpOnly, SameSite strict, session) and `GetCookies` listed it 404 ms after the window opened, before the navigation. So the credential installed through the native API is in effect on the very first request; no cookie-less request ever reached the guard.
- Document, CSS, script, image, relative fetch, GET query (`é&ü` round-tripped) and a 1005-byte POST all authenticated; `document.cookie` does not contain the credential.
- Native reload kept authentication with one installation; a same-origin redirect ended at `/api/query?x=redirected` with the handler called once more and no refusal; the stream delivered chunk 1, the page acknowledged it, then chunk 2 (`chunk-2 after ack`).
- External Go clients: no credential 403, wrong credential 403, wrong credential plus Origin 403. The host's own selftest with the right credential: 200; wrong Host 403; wrong Origin 403; exact Origin 200. The guard counted no-credential 1, wrong-credential 2, host 1, origin 1, and handled equals admitted throughout.
- WebView2 also requests `/favicon.ico` from the origin. It is same-origin, carries the credential and is admitted; the handler answers 404. Linux counts do not include it.

Collector results, two loopback windows A and B, A's page attacking the collector on another port and B's endpoint. "Reached" means the collector logged the request; the credential column says whether the request carried it.

| Request class | Control (no enforcement) | CSP only | Interception only | Both (default) |
| --- | --- | --- | --- | --- |
| img, script, stylesheet, font, fetch, XHR, sendBeacon, `<a ping>`, EventSource, prefetch, img and fetch redirected cross-port | **reached**, credential on all but font, EventSource and redirected fetch | not observed | answered natively, never sent | not observed |
| WebSocket | **reached, with credential** | not observed | **reached, with credential** | not observed |
| Cross-port iframe | **reached** (frame navigation refused, request sent anyway) | not observed | answered natively | not observed |
| Same-origin iframe | subframe navigation refused natively | refused and CSP | refused natively | refused and CSP |
| Top-level `location` and redirected navigation | navigation refused, **request reached with credential** | navigation refused, **request reached with credential** | refused and answered natively | refused and answered natively |
| Form POST cross-port | navigation refused, not reached | CSP `form-action`, never proposed | navigation refused, not reached | CSP |
| `window.open` | `NewWindowRequested` handled, returned null, not reached | same | same | same |
| B's endpoint from A's page | A's page never invoked B's handler, in every variant | | | |

Every variant kept the window inside its origin (final URIs stayed home). The two single-mechanism variants fail the driver's "collector received nothing" checks by design; the default passes.

`inprocess-isolation`: two in-process windows with the same `https://dfx2-spike.invalid` origin, separate environments, profiles (`profile-2828-1`, `profile-2828-2`) and browser processes (10288, 9752). A page naming the origin's `/api/count` reached only its own window's handler (I1 +1, I2 unchanged); its attacks reached nothing at the collector; I2 could not read I1's cookie, local or session storage.

Races and injected failure (all four windows closed with no navigation, handler count 0, ownership drained, profile deleted):

- `cookie-close-race`: `open` then `close` at once lands in controller creation here (environment creation completes before the next command runs). The late controller callback delivered a controller, which was closed and never attached; its browser process exit was observed and the profile deleted.
- `close-during-controller` (hook): same path, with the close posted just before `CreateCoreWebView2Controller`.
- `close-during-cookie` (hook): the close arrives after `AddOrUpdateCookie` and before the `GetCookies` completion. `Controller.Close` released the completion handler without invoking it; the host records `completion-dropped` for it and no navigation happened. This is the WebView2 analogue of Linux's late completion: there is none, the handler is simply released.
- `cookie-failure-injected`: the replaced `AddOrUpdateCookie` result is visible as `cookie-failed … injected`, the window closes, nothing navigates.

### Pass C: sessions and release

`storage`, two attachment windows on one origin:

- B could not read A's application cookie, local or session storage; reload kept A's; closing A while B made three requests left B working; reopened A2 had a fresh session; B was unchanged; the attachment server answered an independent client after every browser closed.
- Cache: A's two fetches of `/api/cached` reached the server once, A's two image loads once, and B's two image loads once. So this browser does serve a repeated cacheable resource from its own session's cache (Linux did not), and B's request still went to the server: positive evidence that the cache is per window.
- Each window's `closed` record carries its own browser process id and `profile_removed` true.

Release and deletion, across every closed window of the run:

- `BrowserProcessExited` arrived after `Controller.Close` in every window that had a controller: 255–295 ms in all but one case; in `shutdown-inprocess-ordinary` it took 6.56 s, during which the host reported `release-pending` every 2 s and deleted nothing. Deletion then succeeded on the first attempt in every case, 43–55 ms.
- `removal-failure-injected`: after a real exit, the injected `remove` failed 20 times over 1.91 s; the record says `injected: true`, `removed: false`, and names the residue; the window's `closed` carries `removal_error`; the host still exited 0; the folder was still there (the driver removed it afterwards, recorded as its own cleanup).
- `forced-crash`: the killed host's `profile-11652-1` remained, named by its pid; its browser process was gone; the next host used `profile-6384-1`, removed only that, and the residue stayed until the driver cleaned it. No scavenger exists.
- After every host exited, no browser process it had reported was alive, and no profile of that host remained under the root (except the two designed residues above).

### Pass D: error page

`error-pages`, one attachment window and a stoppable server that closes every connection:

- Failed GET (server stopped): the page shows Retry and Return to start, no script. After the server returned, a clicked Retry repeated that GET exactly once (server count +1) and loaded.
- A page-supplied action (`…/retry/forged`) is refused on its nonce.
- Failed POST: only Return to start; a click where Retry would be does nothing; Return to start is a GET and no POST other than reports reached the server.
- POST, 303 to a failing GET: Retry offered for the GET (`from_post` true); the clicked Retry was one GET navigation; `/post-303` was received once.
- POST, 307 to a failing target: the failure is a POST, no Retry; `/post-307` once. **Finding 3:** the server received `POST /broken-post` twice for that one navigation, from two client ports 1 ms apart (`58878`, `58879`). The host issued one navigation and one document request; the browser's transport re-sent the POST when the first connection closed without a response. The same happened to failed GETs: one navigation to `/broken` reached the server 2 then 3 times.
- 404 and 500 display as the server's pages (titles `404 from the server`, `500 from the server`); `NavigationCompleted` reports them with `IsSuccess` false and the response received, so the host leaves them alone. A failed fetch and a failed image leave the page in place.
- Initial-load failure (server down before the window opened): the page, then a clicked Retry loaded the start page.
- `owned-load-failure`: loopback `/api/abort` fails with `ERROR_HTTP_INVALID_SERVER_RESPONSE` (8), in-process `/api/abort` with `HOST_NAME_NOT_RESOLVED` (13) because the host installs no response and the `.invalid` origin cannot resolve; both show the page and Return to start reloads the start page.
- Screenshots (desktop captures, complete windows): `run/error-pages/screen-error-get.png`, `screen-error-post.png`, `screen-error-initial.png`, `run/owned-load-failure/screen-error-loopback.png`, `screen-error-inprocess.png`; retained renders beside them (`error-*.png`). I looked at the GET, POST and in-process captures: the failed URI, the explanatory line and the blue buttons are crisp; the POST page shows only Return to start.

Two WebView2 facts the error page depends on, found during development and kept in `run/development/`:

- `NavigateToString` navigates to a `data:text/html;base64,…` URI with an opaque origin; the host's navigation policy must admit exactly that navigation while its page is pending, or the window stays blank.
- After a redirect, a failed navigation's `HttpStatusCode` is the redirect's (303 here) while `IsSuccess` is false. Judging "the server answered" by that status left the failure unhandled, and about one second later WebView2 issued a fresh GET to the failed URI by itself, with no host or page action. The host now judges by `WebResourceResponseReceived` for the document request, and always replaces a failure promptly, so that automatic reload never gets a chance; it was not observed again in the final run.

### Shutdown

The six shutdown scenarios and `combined`: ordinary close, close with `/api/slow` pending (the handler had run once), and close with devtools open (`OpenDevToolsWindow`, a separate window; WebView2 has no docked inspector). Every one drained its handlers and pending requests, observed its browser process exit, deleted its profile, and the host exited 0 with no callback object alive. `combined` additionally closed A with a request pending while I kept answering, reopened as A2 with a fresh session, and retried `/api/abort` in both transports: one allowed navigation per click, nothing automatic (loopback handler calls per navigation: 3 and 3; in-process fails before the handler).

## Findings for decision

Review disposition (2026-11-12): Michael accepted the resolutions for all three findings. Native interception and framework-enforced CSP are both mandatory for owned Windows webviews. Navigation cancellation controls accepted navigation, but cannot be credited with preventing the request; interception supplies that network boundary for the observed cancelled GETs. The default already supplies all three mechanisms. The error-page promise excludes framework POST replay, not browser transport redelivery; application command handling owns any deduplication required. Retain the single-mechanism failures and raw results as evidence.

The returned native run above is unchanged. A subsequent driver-only adjustment records the redirected POST's delivery count as an observation, compares POST counts before and after the 303 Retry and 307 Return actions, and checks that those actions introduce no POST document requests. It does not change the host or add a deduplication mechanism. The adjusted driver still needs a targeted native `error-pages` run on Windows; cross-builds do not establish execution. Do not relabel the retained 19/22 result as a new passing run.

1. **WebSocket handshakes bypass native interception.** `WebResourceRequested`, even on `*` with all contexts, does not see WebSocket connections; under interception alone the handshake to another port carried the credential. The guard's CSP (`connect-src 'self'`) stopped it, and it is outside page control because the guard adds it to every owned response. On Windows, CSP is therefore load-bearing for this class, not a second line. Smallest resolutions: keep both mechanisms mandatory on Windows and say so in the spec; or additionally keep the credential out of WebSocket reach another way (none found in this SDK).
2. **Cancelling a navigation does not stop its request.** `NavigationStarting` and `FrameNavigationStarting` with `Cancel` kept the window on its page, but the GET request still left and reached the collector with the credential (top-level, redirected top-level and cross-port iframe, in the control and CSP-only variants). Form POST navigations were not sent. Interception is the network boundary; navigation policy is only a display boundary on WebView2. The default configuration already has both; the spec should not describe navigation policy as preventing the request on Windows. CSP does not govern top-level navigation, so interception cannot be optional.
3. **The browser's transport re-sends a failed POST.** One `307`-preserved POST navigation reached the server twice, over two connections 1 ms apart, when the first connection closed without a response. This is Chromium's retry on a connection that closed before any response byte, not the host's Retry or Return to start (neither was clicked yet). Failed GETs showed the same 2–3 requests per navigation, as the Linux record noted. Nothing in the host can prevent it. Smallest resolutions: treat edit commands as idempotent at the application (the mixer's sequence-numbered commands already are), or have owned endpoints answer every POST before closing a connection; the spec should say that "no replay by Retry" does not mean "at most one delivery".

Also for the spec, not conflicts: the credential cookie is scoped to `127.0.0.1` without a port, so every mechanism above matters; WebView2 requests `/favicon.ico` from an owned origin; and `Controller.Close` releases still-pending completion handlers without invoking them, so release accounting must tolerate a handler that is freed rather than called.

## Untested, or not covered here

- Real DPI other than 96; nothing in these probes depends on scale, and the click positions are computed from the window's DPI, but only scale 1 ran.
- Preconnect and DNS prefetch (the collector counts HTTP requests), service workers, WebRTC, media elements, `<object>`, manifest, CSS `@import`: untested, not supported. `AddWebResourceRequestedFilterWithRequestSourceKinds` (newer SDKs) was not tried.
- Browser-process crash recovery, `ProcessFailed` handling beyond logging, several app processes sharing a user-data folder (never done here: one folder per window).
- The error page's own `document.cookie` is uncovered by design: it runs no script, and the cookie is HttpOnly.
- Linux's signal work: not a Windows concern and not ported.

## Manual check

The driver's `-manual` mode (`run/manual-check/`, log in `run/manual-check.log`) opened one attachment window, failed a GET and then a form POST with the server stopped, restored the server before each prompt, and counted what reached it. It does not click. Run at 14:16 local on 2026-11-12, after Michael was asked to click Retry on the first page and Return to start on the second:

- **Step 1, failed GET:** the page offered Retry (`offered_retry` true). The click at 14:16:45 produced one `retry` action; the GET reached the server exactly once more and the page loaded (`/api/query?x=later`). No POST.
- **Step 2, failed POST:** the page offered no Retry (`offered_retry` false). The click at 14:16:50 produced one `start` action; the start page loaded; **no form POST reached the server at any point** (`form_posts_total_reaching_server` 0).
- The host then quit cleanly: browser exit observed 303 ms after `Close`, profile deleted on the first attempt, exit 0, no callback object alive.

The two clicks were Michael's, five seconds apart, while the automated driver was idle. His report, verbatim:

> worked the way it worked on linux.

On Linux he had confirmed both pages and both actions ("it worked exactly as you described"). The server counts above agree: one GET repeated by Retry, no POST ever replayed. **The manual check is done.**
