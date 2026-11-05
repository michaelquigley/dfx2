# Synthetic mixer stage 1: Linux text and animated meter

Stage 1 of the [work order](../../docs/future/synthetic-mixer-work-order.md) is implemented in the working tree on top of `02b51a5`; nothing is committed. Built behavior is in [ui.md](../../docs/current/ui.md). Terminus review status and Michael's acceptance are recorded at the end. Stage 2 (Win32 presentation and modal scheduling) has not started.

Host: Linux 7.0.0-42-generic amd64, Go 1.27.1 (module language 1.26), GTK 3.24.52, Cairo 1.18.4, GNOME on Wayland with XWayland. Every build, test and vet uses `CGO_ENABLED=0`. Dependencies are the spike's measured pins: purego v0.11.1, go-text/typesetting v0.3.5, x/image v0.23.0.

## Portable checks

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=120s
CGO_ENABLED=0 go test ./internal/appcore -count=300 -timeout=300s
CGO_ENABLED=0 go test ./internal/text/... -count=20
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -v ./internal/platform/gtk -run Headers -count=1
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/dfx2-mixer.exe ./examples/mixer
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
```

All pass. The repeated runs exercise the concurrency fixtures; the Go race detector needs cgo and is not a substitute. Windows and Darwin results are compilation and static analysis only: nothing has executed on Windows in this stage, and Windows `ui.Open` is an explicit unsupported-content refusal tested only with injected calls.

What the deterministic tests establish, with barriers, a fake presentation sink and a fake monotonic clock that fails the test if a frame-deadline timer is armed while a window should be idle. Negative frame assertions follow two barriers observed through the fake backend's `AfterEvent`, which runs on the UI thread after the core returns: the surface notification itself was processed, then a whole later wake dispatch, including its frame step, completed (Terminus F2). Three intentional rendering faults confirmed those assertions discriminate: rendering on unchanged surfaces, on hidden windows and on zero-sized surfaces each failed its target test 100 of 100 times, each at its `noFrame` assertion, with every iteration in its own process; production logic was restored and the tests rerun afterward.

| Contract | Tests |
| --- | --- |
| First frame waits for a visible, non-zero surface; zero size never spins; identical surface notifications (exposure) present nothing and never re-evaluate the view | `TestFirstFrameWaitsForVisibleSizedSurface` |
| An invalidation racing a frame causes a later frame; a frame clears only the generation it sampled | `TestInvalidationRacingAFrameSurvivesAndClearsOnlyItsGeneration` |
| 10,000 invalidations coalesce into one frame without touching a full 256-slot post queue; closed windows refuse | `TestRepeatedInvalidationNeverUsesPostSlots` |
| One-shot 1/60 s deadlines from monotonic time; one frame after a 250 ms gap (no catch-up); no timer once settled | `TestAnimationUsesOneShotDeadlinesFromMonotonicTime` |
| A late supervisor evaluation keeps an armed timer and wakes for a deadline first seen due | `TestLateDeadlineEvaluationNeverDropsTheFrame` (fails against the pre-fix decision; see below) |
| Close stops the deadline timer, skips presenting a frame in flight, refuses invalidation, releases content once after native teardown | `TestCloseCancelsDeadlineAndSealsContent`, `TestCloseDuringFrameSkipsPresentationAndReleasesAfterTeardown` (counts every presentation attempt and requires zero; with the presentation guard removed it failed 100 of 100 isolated runs, Terminus round 2) |
| A content-release panic after an earlier frame failure is retained as a cleanup failure, with its stack, alongside the original | `TestReleasePanicAfterFailureIsRetained` (failed before the fix with no release stack in `Run`'s error; Terminus round 3) |
| A stalled content release keeps the window counted and named in diagnostics; `Done`, `Run` and dispatch-target release wait for it | `TestStalledContentReleaseStaysVisibleToDiagnostics` (fails against removal before release; Terminus F1) |
| Hidden windows keep the dirty mark, arm no timer and render once at current time when restored | `TestHiddenWindowSuspendsDeadlinesAndRestoresOnce` |
| At most one frame per window per dispatch turn, rotating order, public callbacks still served | `TestFramesAreFairAndBoundedPerDispatchTurn` |
| View panic, rendering failure and presentation (native allocation) failure cancel and drain with window/frame context; every content released; no presentation after teardown | `TestContentFailuresUseCancelAndDrain` |
| Unsupported content refuses before native work without latching; ordinary windows keep working | `TestUnsupportedContentIsExplicitAndNotLatched`, `TestContentWindowsAreUnsupportedBeforeNativeWork` (Win32, injected) |
| A refused content window reevaluates last-window shutdown: with the last ordinary window's destruction acknowledged reentrantly during the refused creation, window mode shuts down and resident mode stays alive until `Quit` | `TestUnsupportedRefusalParticipatesInLastWindowShutdown` (window mode failed 3 of 3 runs before the fix with `Run` pending; Terminus `c99d277e970b`) |
| GTK: copy into Cairo-owned storage honouring its stride; Go slice never aliased; replacement only after success; error surfaces destroyed; paint uses only the retained image; surface state copied and deduplicated, iconified hidden; failed content creation leaves no native object; pixels released after the final widget reference and before acknowledgment | `internal/platform/gtk/content_linux_amd64_test.go` (replaced native entry points) |
| Header ABI of every new GTK/GDK/Cairo binding | `TestBindingsMatchHeaders` reports PASS, not SKIP |
| Checked image arithmetic and the 256 MiB limit | `TestImageArithmeticIsCheckedAndBounded` |
| Renderer clipping, compositing, antialiased lines, failed resize keeps the previous frame | `internal/render/canvas_test.go` |
| One view evaluation per frame; semantic record, layout and commands; ink only inside widgets; scale doubles geometry; overflow clipped; elapsed-time meter decay and animation only above the level; removed keys discard state; programmer errors panic with context; oversized surface fails and keeps the frame; release drops resources | `ui/ui_test.go` |
| Wrap growth is charged before it is kept: a cached paragraph never exceeds the paragraph budget during a frame (narrower rewraps are lent as scratch, the stored wrap and earlier references stay intact, no reshaping, and eviction between frames makes room so a later frame stores the wrap); an uncached paragraph's growth is charged to scratch exactly and refused past the limit, on first wrap and on narrower rewrap | `TestAdmittedWrapGrowthStaysWithinBudget`, `TestTransientWrapGrowthIsChargedAndRefused` (all five subtests fail against the original `Lines`; local review after the manual audition) |
| Rewrap keeps spaces without reshaping; fallback; spaces after fallback runs return to the primary face; mixed direction keeps every cluster exactly once and reverses RTL runs; UAX #14 breaks; slight hinting snaps zones; quarter-pixel rasters; unsupported color data is a visible placeholder | `internal/text/gotext/engine_test.go` with pinned fonts |
| Cache pressure: each entry fits but the frame's working set does not; budgets hold at every step; transient use stays within its scratch limit and is released; current-frame references stay valid; forced eviction between frames; evicted storage is not recycled; a retained paragraph rewraps at a new width without reshaping and with fresh-wrap spacing | `internal/text/system_test.go` (deterministic engine) |
| Root package imports no UI, renderer, text or font package; platform cannot reach them | `TestDependencyBoundaries` (a temporary root import of `internal/text/gotext` was confirmed to fail it) |

Text fixtures are pinned faces loaded in a fixed order: Go Regular from x/image and a vendored Noto Sans Hebrew, with sources and licenses in [`internal/text/testdata`](../../internal/text/testdata/README.md). The space-after-emoji test loads the host's Noto Color Emoji by path and skips without it; it ran (PASS) on this host and is host evidence, not a pinned fixture.

## Native Linux auditions

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-mixer ./examples/mixer
python3 evidence/synthetic-mixer/linux/drive.py /tmp/dfx2-mixer --out evidence/synthetic-mixer/linux/wayland --gdk-backend wayland
python3 evidence/synthetic-mixer/linux/drive.py /tmp/dfx2-mixer --out evidence/synthetic-mixer/linux/x11 --gdk-backend x11
python3 evidence/synthetic-mixer/linux/drive.py /tmp/dfx2-mixer --out evidence/synthetic-mixer/linux/x11-scale2 --gdk-backend x11 --gdk-scale 2
```

[drive.py](linux/drive.py) bounds each case to 40 seconds, retains JSON event logs, stderr, module maps and a `summary.json` with host versions, and inspects child processes across all threads after the first frames. The retained runs were refreshed on 2026-11-05 at 16:03–16:05 EST from the code reviewed clean by Terminus `e1c0b03d8249`, after the wrap-accounting, scene-lifetime and SVG-fallback corrections. All 21 cases passed: exit 0, no stderr output, exactly one final `drained` record with zero windows, tasks, queued callbacks, completions, native windows, retired references and dispatch targets.

| Case | Wayland | X11 (XWayland) | X11, `GDK_SCALE=2` |
| --- | --- | --- | --- |
| `animate`: feed, pause, settle, 5 s quiet | 0 frames, timers, wakes, dispatches, presents, paints | same | same |
| `resize`: four native resizes | surfaces 760×320, 380×240, 980×420, 560×260 at scale 1; quiet after | same | surfaces doubled at scale 2; quiet after |
| `minimize`: iconify 2 s, restore | window minimized (0 paints) but GTK reported no iconified state: 62 frames and 31 deadline timers continued | hidden: 0 frames, 0 timers, 0 paints; restored visible and rendering | same at scale 2 |
| `close-sample`: close inside a 1 kHz invalidation loop with the 30 Hz sampler | 905 accepted, 458 refused after close; drained | 906 / 458 | 916 / 458 |
| `reopen`: five open/close cycles, resident | passed | passed | passed |
| `two`: close one of two animating windows | survivor kept rendering; quiet after | same | same |
| SIGTERM during animation | `context canceled` after drain; exit 0 | same | same |

The quiet interval is measured by the example from the runtime's own counters after no animation deadline has existed for 250 ms. Exposure paints may legitimately occur during it: the retained Wayland `minimize` run recorded one after restoration, and no other run recorded any. Every inspected process mapped `libcairo` and no WebKit, JavaScriptCore or WebView2 library. The Wayland runs show GTK's `bwrap`/`glycin-svg` image-loader helpers, as in the foundation record; the X11 runs started none.

Retained-image snapshots under each run's `snapshots/` are the exact pixels copied into the Cairo surface, written with `cairo_surface_write_to_png`; they are not compositor captures. They show the label text, the changing numeric readout, Greek, Cyrillic, Hebrew, Arabic (right-to-left runs in visual order), Japanese with UAX #14 breaks between ideographs, color emoji from Noto Color Emoji, the mixed-direction phrase, rewrapping at each width, and the decaying peak marker.

Fonts, as resolved by go-text's fontscan for the default `sans-serif` query on this host (checked with a temporary probe of the shared engine, not inferred): Noto Sans for Latin, Greek and Cyrillic; DejaVu Sans for Hebrew and Arabic; Noto Sans CJK JP for Japanese; Noto Color Emoji (PNG strikes) for the emoji. The desktop interface font is not what `sans-serif` selects here; `-font` chooses families explicitly. No COLR emoji font resolved, so the placeholder path is covered only by tests.

### Coverage limits

- Scale: the only non-1 scale is `GDK_SCALE=2` forced on X11, an environment-selected integer factor. No native monitor scale change, fractional scaling or mixed-DPI move was exercised; the desktop was at scale 1.
- Wayland minimization is not detectable through GTK 3 in this session, so rendering and deadline suspension while minimized are established on X11 and in the fake-backend tests only. Michael accepted this exception; see the pacing decision below.
- Resizes and iconify/restore come from internal hooks calling `gtk_window_resize`, `gtk_window_iconify`, `gtk_window_deiconify` and `gtk_window_present`, not from user input. Michael subsequently reported that he checked all listed manual audition steps and that they looked great; that visual result does not establish minimized frame/deadline suspension on Wayland.
- LCD subpixel antialiasing and gamma-correct blending are absent, as in the spike.

## Review and acceptance

Before implementation the dfx2 canon was read; drift is recorded in the [journal](../../docs/journal/2026-11-05.md) for deliberate canon work, and no canon file was changed.

Terminus rounds, each finding triaged with Michael one at a time and fixed on his decision:

| Review | Verdict | Findings and disposition |
| --- | --- | --- |
| `3a6df1fb600c` | not clean | F1: windows left accounting before content release returned; now registered until release returns, with `content-release-pending` in diagnostics. F2: negative frame assertions lacked a completed-dispatch barrier; now surface-processed and completed-dispatch barriers, verified by three rendering faults failing 100/100 |
| `f96efd42aa7d` | not clean | F1: close-during-frame test could not detect a presentation; now counts presentations, verified by removing the guard (100/100 failures) |
| `4ef483dc63cd` | not clean | F1: a release panic was hidden behind an earlier failure; now a retained cleanup failure, test failed before the fix |
| `a4fde2f86da2` | **clean** | none; the only uncovered files are the two font fixture files |

Review logs are under `/home/michael/.local/share/terminus/dfx2/<review>/` on this host. One unexplained `FAIL` from a 300-repetition appcore run during F2 work, whose output was not captured, did not recur in 9,900 further repetitions; it is recorded in the journal as open.

Michael reports the manual audition passed: he checked all of the following by hand and said it "looked great." Stage acceptance remains pending. The checked sequence was: `CGO_ENABLED=0 go run ./examples/mixer` (text quality against the desktop, interactive resize and rewrap, exposure by covering the window, minimize and restore by hand, close) and `CGO_ENABLED=0 go run ./examples/mixer -resident` (`open` twice, `pause`, `resume`, `close`, `quit`).

### Local review after manual audition

The full `CGO_ENABLED=0 go test ./... -count=1 -timeout=120s` suite passed. A separate temporary overlay test exposed a paragraph-budget gap in `internal/text/system.go`: wrapping 100 repetitions of `a ` at a narrow width grows the estimated payload from 9,217 to 21,217 bytes. An admitted paragraph exceeds its 18,434-byte cache budget until frame end; a transient paragraph with a 9,217-byte scratch limit keeps reporting only 9,217 bytes after wrapping and returns no error. Both assertions failed on that tree; the reproduction is under `/tmp/dfx2-stage1-review/` on this host.

Michael directed the fix before stage acceptance. `System.Lines` now returns an error and charges a new wrap before keeping it. An admitted paragraph stores the wrap only within the paragraph budget; otherwise the lines are lent as transient scratch for the frame and the shortfall is evicted between frames (which also corrected `EndFrame` making room only for entry-count shortfalls, never byte-only ones). An unadmitted paragraph's growth is charged to scratch and refused past the limit, storing nothing. Budgets were not raised. With the original logic restored temporarily (signature adapted only), every subtest of the two permanent regressions failed; the fix was restored and verified. The review's reproduction, adapted only for the new return value, passes: the admitted entry stays at its 9,225-byte shaped payload with the narrow lines lent as scratch, and the transient wrap is refused at `9225 + 12000 bytes > 9225`. The base payload is 8 bytes larger than the review's 9,217 because `Paragraph` gained a frame field. The affected packages passed 50 repetitions; the full no-cgo suite, vet (Linux, Windows, Darwin) and the cross-builds passed. Terminus status for this correction follows.

Terminus `c99d277e970b` reviewed the correction and found one blocker in older stage 1 code: refusing unsupported content closed the starting window record without reevaluating last-window shutdown. Michael accepted adding `lastWindowLocked()` after the refusal. The first proposed regression, closing the last ordinary window from another goroutine during the refused creation, passed before the fix: that window's destruction acknowledgment arrives after the refusal and reevaluates the rule itself. A temporary overlay probe showed the defect is reached when the acknowledgment is delivered reentrantly during the refused `Create`, as the backend contract permits. On Michael's decision the regression uses that trigger in both modes; the goroutine-close variant was dropped. Window mode failed 3 of 3 runs before the fix; after it both modes passed 300 repetitions, as did the appcore suite. The full no-cgo suite, vet (Linux, Windows, Darwin) and cross-builds passed. Terminus `d19f88e83e6b` then returned **clean**, and the three native matrices were rerun on that tree: 21 of 21 cases passed. Stage acceptance remains Michael's.

### Wayland pacing decision

Michael accepts continued rendering while minimized on GTK/Wayland when avoiding it would add significant complexity. The [isolated pacing experiment](../../experiments/gtk-pacing-spike/README.md) found substantial throttling with native frame opportunities, but not guaranteed suspension, and additional cadence/restoration tradeoffs. The spec and work order now carry this exception. No production scheduler change was made; X11 hidden-state suspension remains in place. This resolves the Wayland acceptance decision, not the separate canon drift or the unexplained historical test failure.

### Aligned-canon review

On Michael's direction the sibling canon was aligned with the built UI, including text/cache bounds, retained-image ownership, invalidation, content-release accounting, the unsupported-content exception and the accepted GTK/Wayland limitation. The canon changes are uncommitted. The strict loader/coverage audit succeeds; the tracked-tree audit has no gaps, while the working-tree review still reports the two font fixture files as uncovered.

Terminus `8ccaf2015355` reviewed the working tree with all 11 rubric qualities selected and returned **not clean**: F1, displaced wraps can remain live within a frame without scratch accounting; F2, the retained scene keeps transient lines after frame-end accounting resets. Stage acceptance remains pending.

Michael accepted F1. Replacing a cached wrap returned in the current frame now retains and charges its old buffer as scratch; replacing an unused prior-frame wrap needs no scratch reservation. Transient paragraphs charge each new wrap in full and retain earlier wraps until `EndFrame`. A failed reservation leaves the paragraph and retained-byte accounting unchanged. Budgets were not raised. The permanent regressions retain successive wide/narrow results, check their aggregate accounting and contents, exhaust scratch on a later replacement, and distinguish an unused prior-frame wrap from one returned by a current-frame cache hit. Before the fix, both repeated-wrap cases and the cache-hit displacement case failed. After the fix they pass, as do the full no-cgo suite and Linux vet. Native matrices have not been rerun for this correction; the results above belong to the earlier reviewed tree.

Michael subsequently accepted F2. A deferred cleanup clears each scene command's borrowed text lines before `EndFrame`, leaving inspection metadata intact. `TestFrameDropsTransientSceneLines` forces unadmitted paragraphs and checks successful rendering, canvas-resize failure, rasterization error and rasterization panic. Every case failed before the fix because the retained scene still referenced text lines after scratch accounting reset. All four pass after the fix; rendering-path probes also establish that the lines and their scratch charge remain available during rendering. The full no-cgo suite and Linux vet pass. Neither native matrices nor Terminus have been rerun since these two corrections; stage acceptance remains pending.

The subsequent aligned-canon review `d109478e043f` returned **not clean** with one blocking finding: SVG glyphs without fallback outline segments silently produced an empty bitmap instead of the required visible placeholder. Michael accepted the fix: SVG glyphs use their fallback outline when present and the existing placeholder otherwise. The new SVG case in `TestUnsupportedGlyphDataIsVisible` failed before the fix with an empty bitmap and passes afterward. A control test confirms that an SVG with an outline produces the same pixels as that outline and that an ordinary space remains blank. The full no-cgo suite and Linux vet pass. The two earlier accounting findings were not raised again. No subsequent Terminus review has run; native matrices still predate the accounting corrections.

Terminus `e1c0b03d8249` subsequently reviewed the corrected working tree against all 11 aligned rubric qualities and returned **clean**, with zero blocking findings and zero advisories. The two font fixture files remain uncovered by project-local qualities. Native matrices still predate the accounting and SVG corrections and need refreshing before final stage acceptance. The dfx2 and companion canon changes remain uncommitted; stage 2 has not started.

The final native refresh is complete on that reviewed code: Wayland 7/7, X11 7/7 and X11 with `GDK_SCALE=2` 7/7. All stderr logs are empty; five-second settled intervals show zero frames, timers, wakes, dispatches and presents. X11 minimization suspends work and restores correctly at both scales; Wayland retains the accepted limitation (62 frames and 31 timers, zero paints during the measured minimized interval). Updated logs, summaries and snapshots replace the earlier retained matrix evidence above. The settled Wayland snapshot and narrow scale-2 resize snapshot were also visually checked for text, meter and wrapping. No code changed after the clean review. Stage 1 is ready for Michael's acceptance; neither repo has been committed and stage 2 has not begun. The historical unexplained appcore failure remains unresolved, and no real monitor scale change or Windows UI execution is claimed.
