# Synthetic mixer stage 4: the synthetic mixer and complete audition

Stage 4 of the [work order](../../docs/future/synthetic-mixer-work-order.md) is implemented in the working tree on `27e0c4e`, uncommitted. It passes its portable checks and the complete Linux automated native matrix below, rerun after the Terminus correction, and full review `0a9f08c62edc` is clean. **Nothing from this stage has executed on Windows**: the [Windows handoff](windows/stage-4/README.md) is prepared and cross-built only. Michael has passed the Linux manual audition; the Windows automated/manual audition and return, Linux return checks, canon alignment and Michael's acceptance remain. Built behavior is in [ui.md](../../docs/current/ui.md#the-synthetic-mixer) and the example's [README](../../examples/mixer/README.md).

Host: Linux 7.0.0-42-generic amd64, Go 1.27.1 (module language 1.26), GTK 3.24.52, Cairo 1.18.4, GNOME Shell 50.1 for the nested sessions. Every build, test and vet uses `CGO_ENABLED=0`.

## What was added

- **Example** (`examples/mixer`): `model.go` holds the application model, its owned snapshots, the 30 Hz producer, external fixture changes and the pending-value policy. `commands.go` holds the 64-command FIFO, sequence numbers, records and the ordered worker with its device delay. `controls.go` is the six-strip view, now submitting commands. `main.go` gains the console fixtures, `-delay`, `-churn`, `-text-budget`, richer `status` and balanced command totals in `return`. `cpu_*.go` read process CPU time for timing observations. `mixer_test.go` holds the deterministic fixtures.
- **Internal seams, no public API**: `appcore.AuditionContent` returns a live window's content on the UI thread and refuses closing windows. The `ui` content gains an unexported-type `TextStats` method, for internal inspection only. `gotext.Preset` installs a fixture engine before the shared engine first loads. Each has a direct test.
- **Drivers**: `linux/controls_drive.py` is extended in place to six strips, a 3840×2160 virtual monitor, command-aware settling, eight new cases, screen captures through `org.gnome.Shell.Screenshot` and separate text-cache runs. Stage 3's retained runs used its version at `27e0c4e`. `linux/stage4_matrix.py` runs and records the whole Linux matrix. On Windows, `windows/stage-4/controlsdrive` is a patched copy of stage 3's driver, which stays as it ran, plus `stage4.go` and `capture.go`; `windows/stage-4/stage4.ps1` runs gates, the unchanged stage 2 matrix with its nested foundation runner, and the stage 4 audition.

## Decisions the work order left open

Each is example policy, visible in the example's README; none is in the widgets.

- **Pending values are keyed by sequence number.** An accepted command sets its control's pending value and sequence; a resolution clears it only if the sequence still matches. An earlier acknowledgment therefore never shows a superseded value.
- **Rejection reconciles to the device.** A device rejection of the latest edit clears the pending value, so the view shows the device's value; the error names the command. A rejection superseded by a later pending edit leaves that edit pending. Cancellation at shutdown resolves the same way.
- **A refused submission sets nothing pending.** Queue-full and shutting-down refusals are recorded and shown, and the view falls back to the latest accepted value.
- **The device rejects commands for removed channels** (`channel removed`), which demonstrates a device-side failure without the `reject` fixture.
- **External changes during a pending edit** change the device's value underneath; the pending value stays shown until its command resolves, and the device ends with whichever came last.
- **Changing the delay applies to the command already held**, so `delay 0s` drains an overloaded queue at once. With the device held, 65 commands are accepted: one at the device and 64 queued.
- **Submission order is the queue lock's.** Sequence numbers, the pending value and the `accepted` record are all assigned under the queue lock before the command is enqueued, so no record can precede its acceptance.
- **Every user edit is one command**, including each drag frame's. A long drag at a long delay therefore fills the queue; the refusals are visible. The spec forbids coalescing gain edits.
- **The mixer keeps the `-controls` flag**, so the stage 1–2 default view and its drivers are unchanged. Windows are 760×520 logical; strips are 110 wide and 12 apart, fader and checkbox first.

## Portable checks

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=300s
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -v ./internal/platform/gtk -run Headers -count=1
CGO_ENABLED=0 go test . -run TestDependencyBoundaries -count=1
CGO_ENABLED=0 go test ./examples/mixer -count=200 -timeout=600s
CGO_ENABLED=0 GOMAXPROCS=1 go test ./examples/mixer -count=50
CGO_ENABLED=0 go test ./internal/appcore ./ui ./internal/text/... -count=50
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./examples/mixer
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
GOOS=windows GOARCH=amd64 go vet -C evidence/synthetic-mixer/windows/stage-4/controlsdrive .
```

All pass. `TestBindingsMatchHeaders` reports PASS, not SKIP. The root package's transitive imports still exclude UI, render and text packages. The race detector needs cgo and was not run. The Windows and Darwin lines are compilation and static analysis only.

### Deterministic fixtures

`examples/mixer/mixer_test.go` runs the real model, worker, views, `ui` widgets and core through the fake backend, with Go Regular installed by `gotext.Preset` so no host font is consulted. Ordering comes from barriers: the device gate holds each command until the test releases it, input batches arrive inside one posted callback, and idle is decided by the fake's pump hook between dispatch turns, requiring no pending wake, queued callback, animation, queued input or unconsumed generation in any window. Timeouts are hang guards only.

| Contract | Test |
| --- | --- |
| The work order's combined fixture: from mute=false, gain=0.50, two toggles and two Up presses, each followed by a meter sample with the stale device snapshot, give ordered commands on, off, 0.51, 0.52 before any acknowledgment; both windows show the pending values; acknowledgments one by one never show a superseded value. Then a device rejection while a later edit is pending keeps that edit; a rejection of the latest edit returns both windows to 0.54, shows the error, and three further evaluations of both windows submit nothing | `TestPendingEditsAcrossDelayedAcknowledgmentsKeepTheirOrderAndBase` |
| A rejected toggle returns to the device's state, and the next toggle starts from what is shown | `TestARejectedToggleReturnsToTheDevicesState` |
| External changes, reorder, removal and reinsertion never submit, including during a drag, where the gesture keeps its value and stays on its channel after reorder; removal ends it with no edit | `TestExternalChangesNeverSubmitCommandsEvenDuringADrag` |
| 67 key edits behind a held command: 64 queue, 3 are refused at once and shown in both windows, nothing is merged, the views fall back to the latest accepted value, and the device applies all 65 in sequence | `TestTheCommandQueueIsABoundedFIFOThatRefusesWithoutWaiting` |
| Shutdown cancels the held and queued commands without applying them; a later submission is refused; totals balance | `TestShutdownCancelsOutstandingCommandsWithoutApplyingThem` |
| Closing A, or B, leaves the other window, the producer and the worker working | `TestClosingEitherWindowLeavesTheOtherAndTheApplicationsWorkers` |
| A real window, and a fake subscriber, close after a publication collected them: the refusal is counted, the other window renders, and unsubscribing inside that gap cannot deadlock (a guarded publish fails rather than hangs) | `TestAPublicationToleratesASubscriberClosingAfterCollection` |
| Under small budgets with changing text, both caches evict between frames and stay within budget; the evicting window's presented image equals a fresh window's, byte for byte | `TestTextChurnEvictsWithinBudgetAndRendersLikeAFreshWindow` |
| The device delay holds a command and a new setting applies to the held one; cancellation releases it | `TestTheDeviceDelayAppliesToTheHeldCommand` |
| `-text-budget` parsing | `TestTextBudgetFlag` |
| `AuditionContent` is UI-thread only and refuses closing windows; `Preset` precedes the shared engine only | `TestAuditionContentServesOnlyLiveWindowsOnTheUIThread`, `TestPresetPrecedesTheSharedEngineOnly` |

**Fault injection.** 16 faults applied through `go test -overlay`, with the tree untouched, were all caught, each by the test named for that contract: pending display off for gain or for mute; clearing pending regardless of sequence, for gain or for mute; device rejection keeping the pending value; no error shown; a blocking submission instead of refusal; a refusal setting a pending value; shutdown applying held commands; shutdown not refusing later submissions; invalidating while holding the model's mutex (the guarded publish fails); a closed subscriber's refusal panicking; the view echoing a supplied gain or mute as a command; a wrong command value; and churn text that does not change. One further mutation, external changes setting a pending value, survived and was discarded: it is not an echo, and nothing in the contract forbids it. The fault script is a scratch tool, not part of the tree.

## Native Linux auditions

All runs are inside **isolated headless GNOME Shells** with their own home, runtime directory, session bus and a 3840×2160 virtual monitor; nothing reached the operator's desktop. Input is real to the toolkit, through Clutter virtual devices.

```sh
python3 evidence/synthetic-mixer/linux/stage4_matrix.py --out evidence/synthetic-mixer/linux/stage-4/run --base /tmp
```

It builds the mixer and lifetime binaries, records their hashes, the revision and working tree in [`linux/stage-4/run/manifest.json`](linux/stage-4/run/manifest.json), and runs eight processes' worth of matrices, each retaining logs, summaries, retained images and screen captures.

### First full run

[`linux/stage-4/run/`](linux/stage-4/run/), 2026-11-10, mixer `0b08d72b53dbd21dd139ce43b70c4ed0ebe7227c2c715e31d1f1fe5e3747b146` on `27e0c4e` plus this stage's working tree. All eight runs exited 0. **This binary predates one example cleanup** (closed windows are now dropped from the example's name map), so it is not the final candidate; the matrix is rerun on the reviewed tree below.

| Run | Wayland | XWayland | XWayland, `GDK_SCALE=2` |
| --- | --- | --- | --- |
| Stage 3's nine controls cases, over six strips | pass | pass | pass |
| `delayed_commands` (latencies of the four commands, ms) | 701, 1369, 2054, 2450 | 701, 1373, 2055, 2463 | 701, 1375, 2061, 2466 |
| `rejected_commands`, `external_while_pending` | pass | pass | pass |
| `overloaded_commands`: accepted, refused as `queue full` | 65, 5 | 65, 5 | 65, 5 |
| `close_first_window`, `close_during_drag` | pass | pass | pass |
| `shutdown_outstanding`: five canceled, none applied; totals balance (117 accepted: 110 applied, 2 rejected by device, 5 canceled; 5 refused) | pass | pass | pass |
| Exit, `Run` result, final ownership, stderr | 0, nil, all zero, empty | same | same |
| Text, small budget: glyph and paragraph evictions; A equals fresh C | 9,289 and 1,137; equal | 9,359 and 1,140; equal | 1,480 and 1,091; equal |
| Text, default budget: paragraph evictions (glyph 0); A equals fresh C | 567; equal | 563; equal | 556; equal |
| Stage 1 regression matrix (`drive.py`) | 10/10 | 10/10 | 10/10 |
| Foundation lifetime matrix (`evidence/foundation/linux/drive.py`) | 8/8 | 8/8 | not run |
| First surface | 760×520@1 | 760×520@1 | 1520×1040@2 |

The foundation matrix's `failure` case exits 1 by design. No text sample in any run exceeded its budget between frames. The small budget was 48 KiB of glyph pixels at scale 1 and 192 KiB at scale 2, with 64 KiB and 48 entries of paragraphs.

`audit.json` reads the raw logs rather than the summaries: 55 application process logs each drain exactly once with zero windows, tasks, queued callbacks, completions, queued input, native windows, retired records, dispatch targets, paint failures and pending release; application stderr is empty; all 78 PNGs pass chunk CRCs, a complete end chunk and a full decode.

**Timing observations**, two windows, 760×520 logical each. They are measurements on this host, not promises:

| | Wayland | XWayland | XWayland, scale 2 |
| --- | --- | --- | --- |
| Feed running: frames per second, process CPU | 119.6, 38% | 119.6, 34% | 114.8, 76% |
| With 36 churn words | 119.6, 45% | 119.6, 38% | 119.6, 71% |
| Settled, 3 s: frames, evaluations, presents; CPU | 0, 0, 0; 0.7% | 0, 0, 0; 0.5% | 0, 0, 0; 0.5% |

About 60 frames per second per window while the feed runs: the 30 Hz samples invalidate, and meter decay paces animation at 60 Hz. CPU is one core's percentage and includes the driver's own status requests; the settled figure is mostly those. The full CPU frame per window is the obvious cost; no part of it was profiled.

**Images for review.** Each controls run retains snapshots of what the framework handed to GTK (`delayed-pending.png`, `rejected-error.png`, `overload-error.png`, `timing-churn.png`, `controls-before.png`, `controls-focused.png`) and compositor screen captures of the same states (`screen-*.png`), plus `text-small/` and `text-default/` with `screen-text-*-A.png` and the A and fresh-C snapshots. I looked at this run's Wayland `screen-overload-error.png` (65 outstanding, `queue full`, the pending value on Keys, the focus ring) and scale-2 `text-small/screen-text-small-A.png` (six strips, moving meters, crisp text and all three churn lines). Trial runs earlier the same day showed the delay's pending line and the device-refused error with the reconciled fader in the same way. Greek, Cyrillic and Latin churn text comes from the host's Noto Sans; no fallback box appears.

## Unavailable coverage

- **Windows**: nothing from this stage has executed there; see the handoff.
- **Real scale changes**: XWayland's `GDK_SCALE=2` is a forced integer scale at startup, not a monitor transition. Real Windows DPI transitions remain untested from earlier stages.
- **Manual auditions**: required on both hosts. Michael has passed the Linux audition; Windows remains pending. The delayed, rejected and pending-edit behavior is new, so earlier stages' manual passes do not cover it. The Linux steps are [below](#linux-manual-audition); the Windows steps are in the handoff.
- **Display claims**: screen captures come from the nested compositor's own screenshot service, so they show what it composited; they are not a physical display.
- **Fonts**: the Linux runs use the host's `sans-serif`, Noto Sans with DejaVu Sans fallback; the fixtures use Go Regular only, whose missing Greek and Cyrillic render as the visible fallback box.
- The historical unexplained appcore `FAIL` (2026-11-05) and the earlier idle-desktop Linux failures (2026-11-06) remain unexplained.

## Canon drift to flag

Not edited; for Michael's deliberate alignment at acceptance.

- `rubric.yaml` characteristics say "stages 1 through 3 provide …" and "asynchronous model/worker reconciliation is the next mixer stage". Both expire with this stage. The characteristic that "asynchronous model policy … [is] not [a demand] to expand the current stage" should now say that such policy belongs to applications, as the example's does.
- `immediate-mode-state-and-frames` boundary: "asynchronous model/worker reconciliation … remain separate work" expires. The quality could state the built division: widgets start edits from the value last shown but follow what the view supplies, and pending-value or acknowledgment policy is the application's, so framework widgets must not impose one.
- No quality names the example's own invariants: owned snapshots published under the application's lock and invalidated outside it, tolerance of a subscriber closing after collection, a bounded nonblocking command FIFO with visible refusal and no coalescing, ordered delivery, sequence-numbered records, and shutdown cancellation never reported as applied. Whether the canon should hold example invariants, or leave them to the example's tests, is Michael's call.
- `text-data-and-cache-bounds` does not say that an entry the last frame used is never evicted, so a working set above a budget is served transiently every frame rather than evicted. That is built and documented behavior; a reviewer could read the absence of eviction under pressure as a defect.

## Review status

**Terminus `d00a189682b1`** (full tracked tree with the staged stage 4 files, canon `be99161`; 2,595 files, 2 uncovered: the Hebrew fixture font and its licence, as before) was not clean, with one blocker in stage 1's frame scheduler rather than stage 4's code. `frames` selects every due window under the mutex, then renders them one by one with it released, and `frame` rechecked only that the window was still open. A surface change delivered synchronously during an earlier window's frame could therefore run a hidden window's view, or render a zero-sized one into a fatal error. No shipping backend is known to deliver such a change synchronously; the fake does.

Michael accepted the fix: `frame` now rechecks full eligibility under the mutex before starting, and an ineligible window clears its selection and returns with its queued input and invalidation untouched. `TestASelectedWindowMadeIneligibleMidTurnDoesNotRender` covers a hidden and a zero-sized surface. Each window's content makes the other ineligible from inside its own frame, whichever the turn renders first, so the result does not depend on map order or the rotation cursor. It asserts no view run and no presentation while ineligible, the input and invalidation still pending, and exactly one frame and one presentation on restoration, consuming both. Against the old guard both subtests failed (`unexpected frame 2`); with the fix they pass 500 times, and 200 times at `GOMAXPROCS=1`. The portable checks above, 100 repetitions of `./internal/appcore ./ui ./internal/platform/win32 ./internal/platform/gtk` and `./examples/mixer`, and 30 at `GOMAXPROCS=1` of appcore and the example, all pass on the corrected tree.

This changes the shared core, so the first Linux run above no longer describes the candidate; it is rerun below. **Windows validation of the changed core is pending** with the rest of stage 4.

### Rerun on the corrected tree

[`linux/stage-4/eligibility-rerun/`](linux/stage-4/eligibility-rerun/), 2026-11-10 14:54–15:02, mixer `018fa8162703f818318f3c3eb6afc07304dab7713a09cd318cbea3a60bb4f3b6`, lifetime `428f1766af6a825cd5bb40459e3279bfabf618e5efe006788d7720ba55c954fb`, on `27e0c4e` plus this stage's working tree including the eligibility fix and the example cleanup. **This is the current Linux evidence.** All eight runs exited 0, and the raw-log audit is clean: 55 application process logs each drain once with zero ownership and empty stderr, and all 78 PNGs decode.

| Run | Wayland | XWayland | XWayland, `GDK_SCALE=2` |
| --- | --- | --- | --- |
| Stage 4 controls and command cases | 17/17 | 17/17 | 17/17 |
| `delayed_commands` latencies, ms | 701, 1391, 2081, 2446 | 701, 1388, 2074, 2453 | 701, 1377, 2062, 2482 |
| `overloaded_commands`: accepted, refused | 65, 5 | 65, 5 | 65, 5 |
| Totals at `return`, balanced | 117 accepted: 110 applied, 2 rejected, 5 canceled; 5 refused | same | same |
| Text, small budget: glyph, paragraph evictions; A equals fresh C; over budget | 9,324, 1,138; equal; 0 | 9,339, 1,143; equal; 0 | 1,493, 1,102; equal; 0 |
| Text, default budget: paragraph evictions; A equals fresh C | 563; equal | 563; equal | 519; equal |
| Stage 1 regression matrix | 10/10 | 10/10 | 10/10 |
| Foundation lifetime matrix | 8/8 | 8/8 | not run |
| First surface | 760×520@1 | 760×520@1 | 1520×1040@2 |

Timing observations on this run: feed running, 119.8, 119.4 and 117.9 frames per second at 44%, 40% and 95% of one core; with churn, 119.4, 119.2 and 111.0 at 41%, 40% and 100%; settled, zero frames, evaluations and presents at 0.6%, 0.5% and 0.7%. The scale-2 CPU figures are higher than the first run's 76% and 71% on the same host, and that run's scale-2 churn case also held 119.6 frames per second; the variance is unexplained; these measurements alone neither establish nor rule out a regression. At scale 2 the CPU frame is near one core, which is a cost to know before larger windows or more strips.

**Terminus `0a9f08c62edc`** (full staged tree, canon `be99161`, 11 qualities; 2,863 files, 2,649 covered, 212 excluded, 2 uncovered: the Hebrew fixture font and its licence) is **clean, with no findings**. Its scope included the eligibility regression, the example's fixtures, this record and the `eligibility-rerun` evidence. The reviewer did not execute tests. This closes F1 of `d00a189682b1` for the Linux-side tree; it is not acceptance.

## Linux manual audition

**Operator-reported pass, 2026-11-10.** After the seven-step hands-on checklist, Michael reported: "it's all working great." His feedback is that the faders are not as attractive as dfx's and do not respond to the mouse wheel. These are captured as follow-up proposals in the roadmap; neither behavior changed in this candidate. This is an overall observation, not a claim of additional native DPI or individually instrumented scenario coverage.

Required before acceptance, alongside the Windows handoff's own. Run on the desktop:

```sh
make
mixer -controls
```

Then follow the Windows handoff's [manual steps](windows/stage-4/README.md#manual-audition) 1 to 7, which use only the mixer's console and ordinary input: delayed edits showing as pending in both windows and catching up in order, a rejected edit reconciling to the device's value with the error shown, overload refusing visibly and draining in order, external changes during a drag never echoing, closing either window, and shutdown canceling outstanding commands. Record what you see in plain words here, with anything that looked wrong.



After review `0a9f08c62edc`, Michael requested a root Makefile for hands-on use. `make` installed both examples with `CGO_ENABLED=0`, and `make test` passed the suite and vet. An isolated bin-directory check confirmed `make clean` removes only mixer/lifetime while retaining an unrelated file. This Makefile and its documentation postdate that review; no runtime or native-driver source changed, so the retained native runs remain applicable.
