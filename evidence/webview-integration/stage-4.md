# Webview integration stage 4: the mixed mixer

Stage 4 of the [work order](../../docs/future/webview-integration-work-order.md) connects browser views to the synthetic mixer's existing model and ordered command path, beside its native windows. Stage 3 was accepted with documented limits and follow-ups under canon commit `76e1f94`. This stage builds on `e0adfd2` and its staged stage 3 closeout documentation, which is preserved unchanged. Built behavior is in the [mixer README](../../examples/mixer/README.md#mixed-views--browser) and [webview.md](../../docs/current/webview.md#native-and-browser-views-of-one-model). **Stage 4 is not yet accepted.** Candidate `ebfc7ab` and Windows return `e775bf5` are committed. Automated validation and Michael's manual auditions on both hosts are complete, with the limits below; Linux return review found no new implementation issue. Canon alignment and final acceptance remain.

## What was built

The change is confined to `examples/mixer`; no framework package changed.

- **`-browser inprocess|loopback|attach`** opens native window A and browser view B over the one model. It implies `-controls`. Without it the mixer is native-only, as before, and never loads browser support. The `make` install convention is unchanged.
- **`browser.go`** holds the browser side:
  - **The page and its endpoints.** The page's embedded assets (`web/`), and two thin handlers. `GET /api/state` returns the latest snapshot as plain JSON. `POST /api/edit` calls the same `submit` as a native edit, so it goes through the ordered command FIFO, emits the same `edit` record and follows the same pending-value policy. `POST /api/report` exists only with `-audition`.
  - **The attach fixture server.** It is an application task on 127.0.0.1, independent of every window, and shuts down when the application drains. Shutdown has no deadline and waits for every handler, on both exit paths. A 10 s read timeout bounds a stalled request.
  - **Browser windows.** It opens and tracks them, and the console's `browser` command and `close NAME` work on them.
- **The model gained no browser or HTTP types.** The page's JSON is a copy built in `browser.go`. `submit` now returns its sequence and error to the caller.
- **The page** (`web/mixer.js`, 131 lines, under the framework's CSP in the owned modes) shows each channel's name, level, fader, Mute, device values and pending edit, plus the command totals, device delay, pending rejections and the latest error.
  - **Polling.** It keeps one poll outstanding and waits 100 ms after each answer before the next.
  - **Edits.** A fader edit goes out on release or on each key press, and a Mute edit on each toggle. A page submits its edits one at a time, in input order, and never retries them.
  - **Reconciliation.** It assigns control values, which fires no event, so showing a snapshot submits nothing. It leaves alone a fader that is held or still has an edit queued or unanswered.
  - **Reordering.** It moves strips only when the model's order actually changed. Moving every strip on every poll, as a first draft did, cancelled real clicks and drags between press and release.
- **Linux GC signal.** `-gc-signal` (default 60) is the application's explicit reservation, passed only to `webview.Open`. The policy is unchanged, and the example sends, subscribes to and ignores no real-time signal.
- **Distribution.** The Windows build stays one executable, with the loader embedded by the existing package, and every build is cgo-free.

Not built, as directed: generic deduplication, reconnection, device integration, a production Sessionmixer server, or any change for the recorded signal risks, the blocking-response failure, loader unload, Retry-method inheritance or Open's startup-error reporting.

## Current status after Windows return

The candidate is `ebfc7ab`, with Windows evidence and driver changes returned in `e775bf5`. No Go source changed on Windows. The later one-line checkbox reconciliation correction has the focused Node regression below; the native runs remain evidence for ebfc7ab, not a native run of that correction. Stage acceptance remains pending.

- **Current Linux evidence**, all at this candidate's mixer unless noted:
  - **Mixed-view audition:** 12/12, in `linux/stage-4-x/`.
  - **Mixer stage 4 regression:** 8/8, in `linux/stage-4-x/regression/`.
  - **Browser matrix:** 33/33, in `linux/stage-4/integrated/`. No Linux-linked framework file has changed since that run; W3 is Windows-only code.
- **Review:** final full-rubric round `1b552e54b3c0` against canon `00086c5` is not clean. Its three findings are dispositioned below: the checkbox correction is fixed and the two other findings are deferred by Michael. No further full-tree review or native matrix rerun is required for this narrow correction.
- **Manual Linux audition:** Michael reported on 2026-11-24 that everything checked out fine. The browser fader's release-only submission was clarified; no behavior change was requested.
- **Windows run:** done on 2026-11-24 at `ebfc7ab`, see [Windows validation](#windows-validation-of-ebfc7ab--2026-11-24). Michael completed the Windows hands-on checklist the same day and reported that everything worked fine.
- **Deferred to the roadmap:** W2 (malformed handler headers), X1 (Win32 input order) and X2 (UI-thread COM balance).

## Validation on Linux

### Portable (modeled)

`examples/mixer/browser_test.go` runs the real mixer on the fake backend, with its gated device and two native windows, and drives the page's handlers through `httptest`:

- **Both directions.** A browser edit becomes command #1, pending in both native windows and in the next poll, then applied. A native keyboard toggle is pending in the browser view's poll. A device rejection is shown by the native views and in the poll's error.
- **Polling submits nothing.** Twenty polls submit nothing, and the command records are exactly the six expected.
- **Attribution and shutdown.** Edits are attributed to the view. After shutdown, a browser edit is refused at once with "shutting down".
- **Reorder and removal.** Both reach the poll, and an edit for a removed channel is rejected by the device ("channel removed"), as for a native view.
- **Validation.** Seven malformed edits get 400. `GET /api/edit` gets 405. Reports without `-audition` get 404. The page references only its external script.
- **Attached pages.** They name their view; a name that is not a valid name becomes `attach`.
- **The fixture server's lifetime.** It answers after a native window closes, and it no longer answers once the application has drained.

Portable checks:
- the full no-cgo suite;
- the mixer package ×20, and ×5 under the race detector;
- vet for linux, windows/amd64 and darwin/arm64;
- the Windows mixer executable and the linux/arm64 build;
- `node --check` on the page script, and `py_compile` on the new drivers.

### Native, automated

[`linux/stage-4/`](linux/stage-4/), run by [`linux/stage4_matrix.py`](linux/stage4_matrix.py) at `e0adfd2` plus this uncommitted tree. Each session runs in its own isolated headless GNOME Shell. Mixer SHA-256: `ad5be6ea78752782014be3bb2a7ca734c890805fd216a410c5ddb9513002f6cc`.

**Mixed-view audition** ([`linux/mixed_drive.py`](linux/mixed_drive.py)): **12 of 12** cases pass, the same four in each of Wayland, XWayland, and XWayland at `GDK_SCALE=2`. Input is real. The shell's virtual pointer and keyboard press the native window and the page's own controls. The controls are located from what the page reports under `-audition`, and every click and drag is recorded with its computed position. Results come from the mixer's `edit`, `command`, `page` and `status` lines.

- **`inprocess`, `loopback` and `attach`**, each:
  - an external `gain` reaches the page with no command;
  - a real click on the page's Mute is a command the device applies, visible in the model;
  - a real drag of a page fader is exactly one gain edit, sent on release, and a real Right-arrow key press is one 0.01 step;
  - a native Tab, Tab, Space toggle reaches the page;
  - under a 1500 ms device delay, a browser edit is pending in the model and in the page, and clears once applied;
  - a rejection shows in both views;
  - `reorder`, `remove`, `insert` and `order` reach the page with no command.
- **Then each mode closes differently:**
  - **`inprocess`:** the browser closes first. The sampler keeps running and native edits apply. The native close exits with code 0 and commands conserved.
  - **`loopback`:** the native window closes first. Browser edits still apply. Closing the browser, the last window, with a 10 s-delayed edit outstanding exits with **1 canceled** and commands conserved.
  - **`attach`:** closing the attached view leaves the fixture server answering 200. `browser` opens view C on the same server, and C's real click applies. After both windows close the process exits cleanly and the server no longer answers.
- **`resident`:** with both windows closed the process stays, with zero windows. `open` and `browser` reopen views that share the model and command path; a reopened view's click applies. `quit` exits with code 0.
- **Logs:** every mixer stderr is empty, and the session logs show no panic, race report or GLib critical. Each case keeps compositor screenshots of the views after a browser edit and while an edit is pending: 18 PNGs.

**Integrated browser matrix and regression** ([`stage-4/integrated/`](linux/stage-4/integrated/), run by `stage2_matrix.py`):
- **Browser matrix: 33/33**, with browser SHA-256 `b39bf271186c0293…` (stamped). The framework is unchanged; this is the stage 3 matrix rerun against this tree.
- **The synthetic mixer's stage 4 regression: 8/8** controls and foundation runs, with a clean raw audit of 55 process logs and 78 PNGs. Its mixer is the same binary (`ad5be6ea…`), so the native-only default is covered as built here.

**Coverage limits:**
- **What the audition reaches.** It locates the page's controls from the page's own report, and confirms the mapping only through the resulting `edit` records. It does not test pointer capture outside the window.
- **Real browser input.** It covers one drag, single clicks and arrow keys, not every gesture.
- **Visual check.** It does not compare the two views' pixels; the screenshots are a visual record only.
- **Mixer-level close orderings.** Close during a native gesture and close during a browser poll are not separate cases here. The framework's own close cases (stage 3) cover requests in flight.
- **X11 child exits.** As recorded in stage 3's N6, the browser matrix's X11 hosts report no child exit status for some hosts. The final descendant check found none remaining.

### Manual

Michael completed the Linux hands-on audition on 2026-11-24 and reported that everything checked out fine. He observed that browser fader drags submit only on release; this is intentional for this fixture and remains unchanged. This is an operator report, not additional automated measurement. He completed the same checklist on Windows later that day, after the scripted Windows run below, and reported that everything worked fine; again an operator report, with no new measurements and no defect reported. The checklist is below.

The page's submission order also has a deterministic check outside any browser, [`linux/mixer_page_order.js`](linux/mixer_page_order.js), described under S2 below; run it with `node`.

## Hands-on checklist (Linux, then Windows)

`make`, then:

1. **`mixer -browser inprocess`.** Drag a native fader: the browser strip shows the edit pending, then applied. Drag a browser fader, then press the arrow keys on it: the native strip follows. Toggle Mute in each window.
2. **In the console:** type `delay 1500ms`, and edits stay pending in both views until applied. `reject 1` shows the rejection in both. `reorder`, `remove c` and `insert c` move both views, and submit nothing.
3. **Closing order.** Close the browser window first: the native window keeps editing, and closing it exits. Restart, then close the native window first: the browser view keeps editing.
4. **`mixer -browser loopback -delay 3s`.** Make an edit, then close both windows while it is pending. The exit's `return` line counts it canceled, and `commands_conserved` is true.
5. **`mixer -browser attach`.** `close B`, then `browser`: a new view attaches to the same `fixture-server` URL and edits work.
6. **`mixer -browser inprocess -resident`.** Close both windows; the application stays. `open` and `browser` bring back working views, and `quit` exits.

Note anything that looks wrong, slow or confusing; the visual record so far is only the audition's screenshots.

## Windows handoff

Run on the Windows host at the revision Michael commits from this tree, in a connected interactive session, with `CGO_ENABLED=0` throughout. Change no production code there. If a native result contradicts the Linux or modeled behavior, record it and return it for a decision. Record the revision, Go, Windows and Runtime versions, the session state and the display.

1. **Portable checks.** Run `go test ./... -count=1` and `go vet ./...`, and, if Node is installed, `node evidence\webview-integration\linux\mixer_page_order.js`. That check is portable, not a native result. Then `go build -o "$env:TEMP\dfx2-mixer.exe" .\examples\mixer`, and record the executable's SHA-256. It is one file: the WebView2 loader is embedded.
2. **Regressions,** since the mixer example changed and the browser framework did not:
   - The mixer's stage 4 Windows audition: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File evidence\synthetic-mixer\windows\stage-4\stage4.ps1 -OutDir evidence/synthetic-mixer/windows/stage-4/run-stage4-mixed`. That checks that the native-only default is unchanged.
   - The stage 3 browser matrix, 18 cases, built and run as in the [stage 3 handoff](stage-3.md#windows-handoff), with `-out evidence\webview-integration\windows\stage-4\browser`. Its result should match stage 3's (17 pass, `body_close` untested). Any other result needs explaining.
3. **Mixed views, scripted through the console,** for each of `inprocess`, `loopback` and `attach`. Start `& "$env:TEMP\dfx2-mixer.exe" -browser MODE -audition -paused`, keep its JSON lines and stderr, and check:
   - `opened` lines for A (native) and B (`kind: browser`), followed by `page` lines from B showing six channels a–f.
   - `gain a 0.25` brings a `page` line with channel a's `fader` at 0.25, with no `edit` or `command` line.
   - `reorder`, then `remove c`, then `insert c`, each brings a `page` line with the new order and no command.
   - `delay 1500ms` brings a `page` line whose `commands` text shows 1500 ms.
   - **Attach:** `close B` leaves `Invoke-WebRequest <fixture-server url>/api/state?view=probe` answering 200. `browser` then opens view C, whose `page` lines arrive from the same server. After the last window closes and the process exits, the server no longer answers.
   - **Exit:** closing both windows ends the process with exit code 0, a `return` line with `commands_conserved: true`, and nothing on stderr except the supervisor's known shutdown diagnostic.
4. **Mixed views by hand.** No Windows driver injects input into the page. Work through the hands-on checklist above with real mouse and keyboard, in each mode. Record for each edit the `edit` line's `window` (A for native, B for browser) and its `command` lines, and note what each view showed. Item 4's `return` line must count the canceled edit.
5. **Resident reopen.** Run `-browser inprocess -resident -audition`: close both windows, confirm the process stays, run `open` and `browser`, confirm new `page` lines, then `quit` for exit code 0.

Return the run directories, the JSON logs and this record's Windows section for Michael's commit. The Linux return then reviews them.

## Review

Terminus `9e06f9ea97b9`, the full rubric under the committed canon, ran on the validated Linux candidate and is **not clean**: four blocking findings and one advisory. The same nine files as before have no matching project-local quality. Every finding concerns this stage's own code or evidence, none repeats an earlier disposition, and all fall within the stage's development boundary. They are returned for triage, not fixed.

| # | Finding | Assessment | Recommendation |
| --- | --- | --- | --- |
| S1 | On cancellation the attach fixture server calls `Shutdown` with a 2 s timeout, then reports a clean task exit even if the timeout expired with a handler or connection still active | Confirmed (`browser.go`). It contradicts the work order's rule against a timeout that declares live work drained. The handlers never block, so it hasn't happened, but the task's clean exit would be a false claim if it did | Shut down without a timeout, so the task ends only once `Serve` has returned and every connection has finished. `ReadHeaderTimeout` already bounds a client that never sends its headers. Keep any unexpected shutdown error |
| S2 | Successive edits from one page can reach the command queue out of order, and a poll can overwrite a fader while a newer submission is still in flight | Confirmed (`mixer.js`). Each edit is its own concurrent `fetch`, and handlers run concurrently, so two quick key presses can be submitted in either order. The `sending` guard is a set that the first answer clears. Native views are ordered; this breaks parity | Chain each page's submissions so the next starts after the previous answer, still sending each once with no retry. Count each fader's unanswered submissions, and guard it until the count is zero |
| S3 | The mixed-view shutdown checks observe only the mixer process, not its browser descendants | Confirmed for the mixed driver. The browser matrix in the same run does observe them | Reuse `webview_drive.py`'s subreaper, reaping and child checks after each mixer exit. Until then, the record's shutdown results are host-only |
| S4 | The drag check doesn't establish that exactly one edit is sent *on release* | Confirmed. The check counts edits right after the first one arrives, so it would also pass for an edit sent mid-drag with none on release, or for a late second edit | Hold the pointer at the final position, wait for a page report showing it, and check that no edit has arrived. Then release, wait for the edit and a settled page report, and recount |
| A1 | AGENTS.md and README lag the built surface | Partly applies. AGENTS.md's status line is in Michael's staged stage 3 closeout, which this stage preserves. README still says Windows stage 3 "awaits native revalidation", which is stale since `c1bd0d6` | Update README now. AGENTS.md is left for Michael's closeout |

S1 and S2 change the mixer, so the Linux mixed audition and the mixer regression would rerun. S3 and S4 change only the driver, so only the mixed audition would rerun.

**Triage so far:**

- **S1, approved and fixed.** On cancellation the fixture server now calls `Shutdown` with no deadline and then waits for `Serve` to return. Unexpected errors from either are joined into the task's result, and `http.ErrServerClosed` is the expected end. `TestTheAttachFixtureServerWaitsForAHeldHandler` holds an edit's handler inside its publication and closes every window. `Run` must not return for 3 s, past the old 2 s deadline. After the release, the held request answers 200 and `Run` returns cleanly. Against the old code it failed: `Run` returned nil after 2 s with the handler still running. A first version of the test was not discriminating, because its `sync.Once` also held the worker's shutdown publication; only the first publication is held now.
- **S3 and S4, approved for the audition only, and implemented.**
  - **S3:** `mixed_drive.py` makes itself the mixers' subreaper and reuses `webview_drive.py`'s `descendants`, `reap` and `child_problem` unchanged. After each mixer exits, every reaped browser descendant must end acceptably, under the same bwrap SIGKILL exception, and none may remain. An empty reaped list means no child exit status was observed, as in stage 3's N6.
  - **S4:** the fader drag is now checked while the button is still held. The page must show the dragged value with no edit sent. After release, the driver waits for one edit and a settled page report, then requires exactly one edit carrying the held value.
- **S2, approved and fixed** in `mixer.js`, the page alone.
  - **The fix.** Each page's submissions are chained: each starts only after the previous one is answered or has failed. Each is still sent once and never retried. The per-channel guard is now a count of edits queued or unanswered, so the first answer no longer clears it while another edit is outstanding.
  - **The deterministic check.** [`linux/mixer_page_order.js`](linux/mixer_page_order.js) loads the page into a bare Node context, with a minimal element stub and a `fetch` whose answers it releases by hand. It checks seven things:
    - with two edits queued and the first held, only the first POST has started;
    - releasing it starts the second, carrying the second value;
    - while the second is held, an older snapshot leaves the fader at its latest value;
    - releasing the second clears the guard;
    - a failed first submission lets the next one through;
    - the failed one is never retried;
    - the failure is shown.

    Against the previous page it failed twice: both POSTs started at once, and the older snapshot overwrote the fader. Two mutations each fail it: removing the chain, and clearing the guard on the first answer.
  - **The native check (parity only).** The audition adds five quick Right-arrow presses on a browser fader. Their edits must arrive in key order with values rising by 0.01, their commands must be accepted in the same order, and the last must be applied and shown. That ordering is a race the old page could also win by chance, so this is parity evidence, not proof.
- **Reruns after S1–S4.** The browser matrix was not rerun: no framework file and nothing in `examples/webview` changed after it. Its 33/33 above stands for this candidate.
  - **[`linux/stage-4-triage/`](linux/stage-4-triage/):** the mixed audition and the mixer's stage 4 regression, with mixer SHA-256 `9d49596a7f618080…`.
    - **Regression:** 8/8 controls and foundation runs, with a clean raw audit of 55 process logs and 78 PNGs.
    - **Mixed audition:** 11 of 12. X11 and X11 at scale 2 passed all four cases. Wayland's `attach` failed S4's new check: the "held" value (0.23) came from the first page report below 0.4, sampled while the slider was still catching up to the last pointer moves. The release correctly sent one edit, of the final 0.19, with none while held. So the driver, not the page, was at fault.
  - **The driver fix.** The held value must now be settled: after the motion, the driver waits for two consecutive page reports that agree.
  - **[`linux/stage-4-triage-mixed/`](linux/stage-4-triage-mixed/):** the mixed audition only, same mixer binary. **12 of 12** pass in all three sessions, every check included:
    - **The release check** (S4): nothing sent while held, then one edit carrying the held value.
    - **The five-key parity check** (S2): edits 0.21–0.25 in key order, accepted in the same order, the last applied.
    - **The child checks** (S3): none remaining in any case. Wayland reaped WebKit's processes and its `bwrap` helpers, each ending acceptably. On X11 WebKit's processes were reaped in three cases. `inprocess` reaped none, so no child exit status was observed there, as in stage 3's N6.
    - **Logs:** every mixer stderr is empty, and the session logs show no panic, race report or GLib critical. 18 screenshots.

  With the regression above, this is the current Linux evidence. The earlier runs are kept.

### Terminus `327db59389e8` — after S1–S4

The next full-rubric round, on the corrected tree, is **not clean**: five blocking findings and one advisory, with the same nine uncovered files. They are returned for triage, not fixed.

| # | Finding | Assessment | Recommendation |
| --- | --- | --- | --- |
| W4 | The attach fixture closes its listener only on the cancellation path. If `a.Go` refuses the task, the listener is never closed. If `Serve` fails on its own, the task returns without shutting the server down, while handlers may still run | Confirmed in this stage's code (`browser.go`) | Close the listener if registration fails. On either exit path, run the same deadline-free `Shutdown`, and join `Serve`'s error. Add one test for each |
| W5 | The held-drag check can count one page report twice | Confirmed. `wait` rescans the log every 50 ms, so the predicate's `seen.append` runs on the same report again, and a single report can satisfy "two agreeing" | In the driver only, advance a cursor past each report used. After release, wait for the command to apply, then require a report newer than that before recounting. Rerun the mixed audition |
| A2 | The root README still says Windows stage 3 awaits native revalidation | Confirmed. This is A1 from the previous round, still undecided | Replace it with stage 3's accepted status, and link its recorded limits |
| W1 | WebView2 routes an oversized in-process request to the endpoint's 431 before checking its origin | **Already decided.** It was recorded deliberately in stage 3's corrections (journal 2026-11-20: the URI is never converted, so the 431 is answered from the measured size alone), and stage 3 was accepted with it. The request still never leaves the browser | No change for stage 4. Reopen only as a deliberate decision about the refusal slots off-origin traffic can use |
| W2 | In WebView2, a handler header containing NUL makes the serialization fail, and the whole header block, the framework CSP included, becomes the literal `invalid string`. No layer validates header names or values | Confirmed (`utf16z`; `finite.go` measures headers but doesn't validate them). It is accepted stage 3 framework code, outside stage 4. It needs a handler mistake, not a page | Choose between a separate defect item, or a small endpoint fix now: reject invalid names and values with the fixed 502, for both engines. The fix changes a shared, linked package, so it needs a Linux matrix rerun, and the Windows run would cover it |
| W3 | The WebView2 response copy uses `append`, whose capacity can round past the reserved length | Confirmed, a small accounting overshoot from size-class rounding. Stage 3 code, Windows only | `make` plus `copy` is one line. Fix now, since the Windows run is pending anyway, or track it with W2 |

None of these affects what the mixed audition validated about edit order, pending state or close ordering. W4 and W5 are within stage 4's boundary. W1 is already decided. W2 and W3 are pre-existing stage 3 framework findings.

**Triage.** W4, W5, A2 and W3 were approved and fixed. W1 stays as decided, and W2 is recorded and deferred.

- **W4, fixed.**
  - **The fix.** If `a.Go` refuses the fixture task, `startFixture` closes the listener. If `Serve` fails on its own, the task runs the same deadline-free `Shutdown`, waiting for the server's handlers, and joins the serving failure with any shutdown error. The cancellation path is unchanged.
  - **The tests.** Two regressions, run against the old code by restoring each old path:
    - `TestARefusedFixtureTaskClosesItsListener` starts the fixture on a drained application: the start must fail and the port must refuse connections. The old code left the listener open.
    - `TestAFailedFixtureServerDrainsItsHandlers` closes the listener underneath a server with a held handler: `Run` must not return until the handler does, and must then report the serving failure. The old code returned while the handler ran.
  - **The test seam.** A package-level `listenFixture`, which is `net.Listen` outside the test.
- **W5, fixed in the driver only.** `settled` moves a cursor past each page report once, so "two agreeing reports" means two distinct reports. After release, the recount waits for the command to be applied, then for a report newer than that.
- **A2, fixed.** The root README now gives stage 3's accepted status with its record, and stage 4's Linux validation and pending Windows and manual runs.
- **W3, fixed.** `deliver` allocates the response copy at the body's length and copies into it. `TestResponseCopyIsAllocatedAtTheAccountedSize` failed before the fix, with capacity 1024 for a 1000-byte body. It is Windows-only code; the Windows run exercises it natively.
- **W1, kept as decided** in the stage 3 corrections.
- **W2, recorded and deferred.** This is a real defect: in WebView2, a malformed handler header, such as one containing NUL, replaces the whole response header block with `invalid string`, and **the framework CSP is lost** while the response is still delivered. It requires a mistake in an application handler, and shared response validation is framework work beyond this fixture stage. Roadmap: [reject malformed handler headers before native serialization](../../docs/future/roadmap/reject-malformed-handler-headers-before-native-serialization.md).

**Reruns after W3–W5** ([`linux/stage-4-w/`](linux/stage-4-w/)): the mixed audition and the mixer's stage 4 regression, with mixer SHA-256 `da18cd6e7d272557…` for both.

- **Mixed audition: 12 of 12**, the same four cases in each of Wayland, X11 and X11 at scale 2.
  - The settled held-drag check passed, now counting distinct reports.
  - The five-key, child-process and close-ordering checks passed.
  - No descendant remained in any case. Wayland reaped WebKit's processes and its `bwrap` helpers, each ending acceptably. X11's `inprocess` case observed no child exit status, as before.
  - Mixer stderr is empty, and the session logs show no panic, race report or GLib critical.
- **Regression: 8/8** controls and foundation runs, with a clean raw audit of 55 process logs and 78 PNGs.
- **Not rerun:** the browser matrix, since no Linux-linked framework file changed (W3 is Windows-only). Its 33/33 in `stage-4/integrated` stands.

This is now the current Linux evidence. The earlier runs are kept.

### Terminus `7b428eff75ad` — after W3–W5 and A2

The further full-rubric round is **not clean**: three blocking findings and one advisory, with the same nine uncovered files. They are assessed against this stage's scope and returned for triage, not fixed.

| # | Finding | Assessment | Recommendation |
| --- | --- | --- | --- |
| X3 | The attach fixture's deadline-free shutdown waits for every handler, but `ReadHeaderTimeout` bounds only headers. A client that sends headers and then stalls its POST body holds a handler, and with it shutdown, indefinitely | Confirmed, in this stage's code. S1 made the wait deadline-free, which made this reachable; the attached browser itself never stalls a body | Within scope, small: give the fixture server a finite read timeout covering the body, which is at most 64 KiB and normally a few bytes. The handler then fails its read and returns, and shutdown still waits for it. One regression with a stalled body. Changes the mixer, so the mixed audition and the mixer regression rerun |
| X2 | Every WebView2 browser creation calls `CoInitializeEx` (single-threaded apartment) on the UI thread, and nothing balances it with `CoUninitialize`, neither on startup rollback nor on release | Confirmed in accepted stage 3 code, outside this stage. The UI thread lives as long as the process, so in practice only COM's per-thread count grows with each window opened | Out of scope: a roadmap item for balancing UI-thread COM initialization, needing Windows validation |
| X1 | The Win32 pre-dispatch pass peeks keyboard messages before pointer messages, so a pointer press queued before a key can be handled after it | Confirmed (`backend_amd64.go`, filters in order: keys, mouse, non-client mouse). Accepted foundation and synthetic-mixer code, outside this stage | Out of scope: a roadmap item for order-preserving input retrieval, with a modeled pump test, and native Windows validation |
| A3 | `docs/future/webview-integration.md` still asks for review before implementation | Same as stage 3's A2 | Retire realized intent documents at the arc's closeout, as directed; no change now |

**Dispositions.** This round is recorded as **not clean**. By Michael's decision, no further full-rubric round runs before the Windows transfer; the next review assesses the returned Windows evidence and any concrete problems it exposes.

- **X3, fixed narrowly.**
  - **The fix.** The fixture server now has `ReadTimeout` (`fixtureReadTimeout`, 10 s) beside its 5 s `ReadHeaderTimeout`. It bounds reading one request, headers and body: a stalled read fails and its handler returns. It is not a shutdown deadline: shutdown still waits for every handler, and the held-handler regression still passes.
  - **The regression.** `TestAStalledFixtureBodyCannotHoldShutdownOpen` sends complete headers declaring 50 body bytes, sends 5, then stalls, and closes every window. With the timeout shortened to 300 ms, `Run` must return cleanly within 5 s, and the incomplete edit must never be submitted. Before the fix, `Run` was still open after 5 s.
- **X1, filed as a follow-up:** [preserve Win32 click and key order in the pre-dispatch pass](../../docs/future/roadmap/preserve-win32-click-and-key-order-in-the-pre-dispatch-pass.md). It **can reverse a user's click and key order.**
- **X2, filed as a follow-up:** [balance WebView2 UI-thread COM initialization](../../docs/future/roadmap/balance-webview2-ui-thread-com-initialization.md). It **leaves successful COM initialization calls unbalanced.**
- Neither reopens accepted platform work for this handoff.
- **A3** is left for the arc's closeout.

**Reruns after X3** ([`linux/stage-4-x/`](linux/stage-4-x/)): the mixed audition and the mixer's stage 4 regression, with mixer SHA-256 `33ed41e128a49035d940aa19557e542c172f371352843d8a759c49ce61469f28` for both.

- **Mixed audition: 12 of 12**, the same four cases in each of Wayland, X11 and X11 at scale 2, with every check as before. No descendant remained. Wayland reaped WebKit's processes and its `bwrap` helpers, each ending acceptably. X11's `inprocess` case observed no child exit status, as in N6. Mixer stderr is empty, and the session logs show no panic, race report or GLib critical.
- **Regression: 8/8** controls and foundation runs, with a clean raw audit of 55 process logs and 78 PNGs.
- **Not rerun:** the browser matrix, since no Linux-linked framework file changed. Its 33/33 in `stage-4/integrated` stands.

This is the current Linux evidence for the transfer. The earlier runs are kept.

## Windows validation of `ebfc7ab` — 2026-11-24

Run on the Windows host at **`ebfc7ab2909709655d2d71c195a4940610ecf49f`** (Michael's commit of the stage 4 tree), clean tree, `CGO_ENABLED=0` throughout, no production code changed. Host: Windows 10 Pro 10.0.19045.6466, amd64, Go 1.27.4, Evergreen WebView2 Runtime 151.0.4129.93 (the SDK pin 1.0.4129.50 is unchanged), PowerShell 5.1.19041.6456, Remote Desktop session `rdp-tcp#5` (session 2) `Active` throughout (the drivers' environment files record the stale `SESSIONNAME` `RDP-Tcp#1`), one display, 3840×1600 at 96 DPI on the Microsoft Remote Display Adapter. Node is not installed. No person was at the desktop during the scripted run. Michael completed the hands-on checklist afterwards, as recorded below.

**Executables**, built from the clean tree before any run directory existed (`vcs.modified=false`):

| Executable | SHA-256 |
| --- | --- |
| `dfx2-mixer.exe` (`examples/mixer`; one file, loader embedded) | `5f24319376a8c5bca627b1a45dcfdf0412283255d5b4544192defeccd80751c2` |
| `dfx2-webview.exe` (`examples/webview`) | `902d419549ed0fa946067ae7d53e1816d374f31d2fb27984fb545bf08f1264ab` |
| `dfx2-wvdrive.exe` (the browser matrix driver) | `2bed05ad899cc2e35aa01799f43d92d4e34670dca215b97125e83369cbe45386` |

`stage4.ps1` builds its own mixer, `4c470dc476cb1cbfe96ec647da215add7258b024d11eaab7d6bc0d98d07b1acb` (`vcs.modified=true`, because its run directory already existed), with `desk.exe` `f008feb1…`, `controlsdrive4.exe` `4c21228e…` and `ctrlbreak.exe` `9027a303…`.

**Portable checks.** `go test ./... -count=1 -timeout=900s` passes for all 14 packages, and `go vet ./...` passes. W3's modeled regression, `TestResponseCopyIsAllocatedAtTheAccountedSize`, passes on this host. The Node page-order check, `mixer_page_order.js`, was **not run: Node is not installed**; its Linux result stands. The race detector did not run (no C toolchain), as in every Windows return.

**Mixer stage 4 regression** ([`../synthetic-mixer/windows/stage-4/run-stage4-mixed/`](../synthetic-mixer/windows/stage-4/run-stage4-mixed/)): passed, after one runner correction.

- **The first attempt, kept in [`run-stage4-mixed-gate-timeout/`](../synthetic-mixer/windows/stage-4/run-stage4-mixed-gate-timeout/),** stopped in the `mixer-repeated` gate: `go test ./examples/mixer -count=100 -timeout=600s` timed out at ten minutes with `TestAStalledFixtureBodyCannotHoldShutdownOpen` running. No test failed. Stage 4's three fixture-server tests take about 5.5 s per run of the package (`TestTheAttachFixtureServerWaitsForAHeldHandler` 3.2 s, `TestAStalledFixtureBodyCannotHoldShutdownOpen` 1.2 s, `TestAFailedFixtureServerDrainsItsHandlers` 1.1 s), so a hundred runs need about twelve minutes. This is the runner's budget, not a product failure. **Driver correction:** `stage4.ps1`'s `mixer-repeated` timeout is 1800 s instead of 600 s; nothing else changed.
- **The rerun:** all four gates pass (the mixer ×100 in 608 s, `ui` ×50, `internal/text/...` ×50, the `controlsdrive` vet). The regression matrix's 18 results are clean: the 16 scripts, the inspection, and the nested foundation matrix's 9 cases, with `failure` exiting 1 as expected. `modal-move`: 362 and 361 frames in 12 s, at least 29 and 30 per second, 395 turns, 394 timers, 0 refusals. `modal-size`: 397 and 370 frames, at least 31 and 30, 457 turns, 457 timers, 0 refusals. Controls: 17 of 17 checks, 119 edits, 114 accepted, 107 applied, 2 rejected, 5 canceled, 5 refused, commands conserved, clean exit, empty stderr. Both text-cache runs pass, with A matching a fresh C. The native-only default is unchanged. That runner's own manual audition is pending.

**Browser matrix, 18 cases** ([`windows/stage-4/browser/`](windows/stage-4/browser/)), host `902d4195…`, driver `2bed05ad…`, 21:33 to 21:37 local: **18 of 18 pass, none untested.** Every host exited as expected (`browser_failure` and `missing_runtime` with their expected errors on stderr), no browser process survived its host, no `dfx2-webview2-*` directory remained, and nothing else reached stderr but the supervisor's five-second shutdown diagnostic in the three quit-with-window-open hosts (`in_process_alone`, `storage`, `response_race`), each listing `response-streams=0`. Stage 3's sixteen regressions pass again. W3's exact-sized response copy is the framework code these hosts ran.

- **`body_close` passed natively for the first time.** The 0 ms attempt's close landed on a claimed body read (`claimed-reads=1`), and that host exited 0 with an empty stderr and no temporary leftovers; the 5, 10, 20, 40 and 80 ms attempts recorded valid zeros and drained clean. By the handoff's rule that is one native observation of a close stopping a claimed read. It differs from stage 3's baseline (untested) in timing, not in behavior: every earlier attempt also drained. It still measures no post-deferral stream lifetime.
- **An observation, not new to this candidate:** in all six attempts the `closed` reply arrived about 6.5 s after the page reported the upload started, with or without a claimed read. The same interval appears in the `ad2e37b` and `c1bd0d6` runs, while stage 3 measured `profile_lifecycle`'s explicit close, with no upload in flight, at 320 ms. The driver does not measure or judge this; it is read from the hosts' timestamps.
- **`body_reader` and the `get_Content` comparison:** the 900,000-byte upload was echoed intact (status 200, length and SHA-256 equal), no body reader remained, and the empty POST was answered as an empty body. `emptyPost`: one `request-content` event, `get_Content S_OK null=true` (seq 14), status 200, echo length 0, handlers `POST /api/echo` 2 and `POST /api/report` 2. `streamUpload`: the same event (seq 19), status 200, echo length 0, handlers 3 and 3. Unchanged from stage 3. No `body-unsupported` event in any host.
- `response_race` again covers only the delayed response after the error page.

**Mixed views through the console** ([`windows/stage-4/mixed/`](windows/stage-4/mixed/), driver [`windows/stage-4/mixed.ps1`](windows/stage-4/mixed.ps1), 21:39 to 21:40 local): **72 of 72 checks pass** across `inprocess`, `loopback`, `attach` and the resident reopen. Each run starts `dfx2-mixer.exe -browser MODE -audition -paused` with its own `TEMP`, writes console commands to its stdin and reads its JSON lines; the mixer's own stdout and stderr are kept as `MODE.jsonl` and `MODE.stderr`, the checks in `MODE.result.json`. No input reached the page.

- **Every mode:** `opened` A (native) and B (`kind: browser`, naming the mode), then `page` lines from B with channels a to f. `gain a 0.25` brought a page with a's fader at 0.25; `reorder` the order f e d c b a; `remove c` f e d b a; `insert c` f e d b a c; `delay 1500ms` a commands text showing `device delay 1500 ms`. **No `edit` or `command` record appears in any run:** external changes and reconciliation submitted nothing.
- **`inprocess`:** B closed first, the model still took an external change, and closing A ended the process.
- **`loopback`:** A closed first, `gain b 0.75` then reached B's page, and closing B ended the process.
- **`attach`:** `fixture-server` `http://127.0.0.1:64098`. After `close B` the fixture answered `GET /api/state?view=probe` with 200 and six channels. `browser` opened C in attach mode, whose pages showed the same reordered model with a at 0.25, on the one fixture server (a single `fixture-server` line). After `close A` and `close C` the process exited and the probe could no longer connect.
- **Resident:** with both windows closed the process stayed; `open` brought C and `browser` brought D, with new page lines; `gain a 0.4` reached D; `quit` exited 0.
- **Every exit:** code 0, a `return` record with `commands_conserved: true` and 0 canceled, **an empty stderr (no mixer run printed the shutdown diagnostic)**, no `msedgewebview2` process left with the mixer as its parent, and no temporary storage left.
- **A first attempt, kept in [`windows/stage-4/mixed-attempt-1/`](windows/stage-4/mixed-attempt-1/),** failed one check of 72, `inprocess`'s "native window A opened", through a driver bug: a log read that returned exactly one line unrolled to a string, so the scan indexed a character and skipped the first record. The mixer's own output there shows A opened 35 ms before B. The script now wraps every read as an array. The product checks were identical in both attempts.

**Hands-on checklist (handoff item 4).** Not performed by the scripted run; console commands are not a substitute. Michael worked through the checklist with real mouse and keyboard on this host afterwards, the same day, with the clean-tree mixer `C:\Cygwin64\tmp\dfx2-mixer.exe`, and reported that everything worked fine. That is an operator report: no `edit` or `command` lines were collected from it, item 4's canceled-edit count was not recorded, and no defect was reported. The mixer stage 4 runner's own manual audition was not separately reported.

**Limits.** No race detector or Node on this host; one display and one DPI. `mixed.ps1`'s browser-process check looks only for `msedgewebview2.exe` processes whose parent is the mixer; the browser matrix driver's fuller descendant accounting applies to the browser matrix alone. The resident check confirms the process alive 3 s after both closes, not a zero-window ownership record.

**Findings.** None against the implementation: nothing native contradicted the modeled or Linux behavior, and W3's copy ran natively through the whole matrix. Two driver changes: the `stage4.ps1` gate budget and the new `mixed.ps1`.

## Linux return review — 2026-11-25

Reviewed Windows return `e775bf5` against candidate `ebfc7ab`. The return changes evidence, documentation and Windows drivers only: the mixer repetition gate grows from 600 to 1800 seconds, and `mixed.ps1` adds the console-driven mixed-view checks. No Go source, dependency or product behavior changed.

The retained summaries and mixed-view logs support 18/18 browser cases, 72/72 mixed checks, the native mixer regression, 17/17 control checks, both text-cache cases and all nine foundation cases with their expected exits. The mixed logs contain no edit or command records and finish with commands conserved. The first native `body_close` pass is one observed claimed read at close, not a stream-lifetime measurement or a guarantee that every run catches that timing.

The full `CGO_ENABLED=0 go test ./... -count=1`, Linux vet and Node page-order check pass on the returned tree. The initial sandboxed test invocation could not open loopback sockets; the suite passed with local socket access. No Linux native rerun was needed because the return changed no linked product code.

No new implementation finding. Manual results remain operator reports; the Windows pending-edit cancellation count was not retained, and the mixed driver's process check covers direct browser children only. These are disclosed evidence limits, not grounds for another hardening loop. The existing deferred findings and historical not-clean Terminus verdict remain. Canon alignment and stage acceptance are next; intent-document retirement waits for acceptance.

## Final review against aligned canon — 2026-11-25

Full-rubric Terminus `1b552e54b3c0`, against canon `00086c5`, returned **not clean: three blocking findings, no advisories**. All 11 qualities were selected over 10,434 tracked files; 12 assets remain outside project-local territories as in the coverage audit. The reviewer inspected source and did not perform new native runs. The initial assessment below is preserved; Michael's final dispositions follow it.

- **`finite-read-reservation-released-early`**, `internal/endpoint/finite.go:325–357`: stopping or completing a read clears its read-buffer charge before the browser reports request release. Source confirms the early credit and that the Windows callback still holds the result buffer; no native budget-overshoot interleaving was reproduced during this return review. Keeping admission until worker completion is a separate limit. The initial recommendation was a focused accounting check; Michael subsequently deferred that investigation as recorded below.
- **`mixer-poll-overwrites-unanswered-mute`**, `examples/mixer/web/mixer.js:79–82`: reproduced with a temporary extension of the existing controlled Node fixture. Checking Mute, delivering an older unmuted poll before its answer, and toggling again submitted `true, true` instead of `true, false`. The fader's queued/unanswered guard is absent from checkbox reconciliation. Recommendation: the narrow page fix and a permanent regression in the existing Node check.
- **`windows-large-snapshot-header-only`**, `evidence/synthetic-mixer/windows/audition.ps1:215–245`: confirmed that the driver reads only the PNG header. During this review, Pillow fully decoded all five retained large-case snapshots in `run-stage4-mixed/matrix/snapshots`, at the expected dimensions from 984×761 to 3200×1400. The validator can be strengthened, but this run's artifacts are intact. Recommendation: follow-up driver work, not a new native rerun solely for this finding.

At review time the temporary checkbox probe lived in `/tmp`; no product or test source had changed. The subsequent correction and dispositions follow.

### Final dispositions and checkbox correction

Michael approved the narrow checkbox fix and deferred the read-buffer investigation and PNG validator improvement. This closes triage of `1b552e54b3c0`; its verdict remains **not clean**, not a claim that the deferred findings were fixed. No additional full-tree review or native validation campaign was run.

- **Checkbox, fixed:** snapshot reconciliation now uses the existing per-channel queued/unanswered guard for Mute as well as the fader. No submission, server or framework mechanism changed. The permanent Node check delivers an older snapshot between two checkbox activations, verifies `true` then `false` submissions, protects the second unanswered edit, and confirms snapshots resume after both answers. Against the old page, three assertions failed; after the one-line fix, all eleven checks pass. `node --check` also passes. The prior Linux and Windows native runs and manual passes remain tied to their recorded candidate; this post-return correction has focused Node coverage only and changes the mixer's embedded page.
- **Read-buffer accounting, deferred:** [investigation item](../../docs/future/roadmap/investigate-finite-read-buffer-reservation-lifetime.md). The early credit is recorded, but no material application failure was demonstrated; do not expand this closeout into new ownership machinery.
- **PNG gate, deferred:** [driver item](../../docs/future/roadmap/decode-windows-large-image-audition-snapshots.md). The actual five retained images fully decoded; no new native run is needed solely to strengthen the validator.

Canon `00086c5` remains unchanged. Final stage acceptance and retirement of realized intent documents remain for closeout; the review loop is complete under these dispositions.
