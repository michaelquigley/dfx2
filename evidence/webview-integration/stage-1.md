# Webview integration stage 1: portable lifetime and endpoint contracts

Stage 1 of the [work order](../../docs/future/webview-integration-work-order.md) builds the public `webview` package, the private browser attachment contract, browser ownership in the core, the finite in-process adapter and the owned loopback server, all against fake native seams. No platform hosts a browser: GTK and Win32 refuse browser shells, and `webview.Open` refuses with `ErrUnsupportedContent` before native work on every target. Built behavior is in [webview.md](../../docs/current/webview.md). Implemented on `5f807d4` plus the uncommitted tree; nothing here is native browser evidence.

Host: Linux 7.0.0-43-generic amd64, Go 1.27.1 (module language 1.26). Every build, test and vet uses `CGO_ENABLED=0`, except the race-detector repetitions, which need cgo for the detector itself and say so.

## What was added

- **Platform contract** (`internal/platform`): `WindowOptions.Content` is now a content kind (plain, drawn, browser) instead of a boolean, with drawn behavior unchanged. `browser.go` adds the browser-parent descriptor, configuration, factory, attachment, the core's host interface and in-process request/response values. `Backend` gains `BrowserParent`; GTK and Win32 refuse browser shells and parent lookups with `ErrUnsupportedContent` before native work.
- **Fake seams** (`internal/platform/fake`): browser shells, parent lookup and its failure, `Call` for native callbacks that run on the UI thread outside dispatch turns, and a fake browser factory that records attachments, holds or releases them, delivers late readiness and drives in-process requests, recording contract violations.
- **Core** (`internal/appcore/browser.go`): `OpenBrowser`, browser ownership in the window record, browser close before shell destruction, endpoint start/close/drain, completions published from workers and settled on the UI thread, cleanup tasks with a narrow file-residue exception, bounds forwarding, and browser diagnostics in `Inspect`, `InspectBrowser` and the shutdown supervisor.
- **Endpoints** (`internal/endpoint`): the finite in-process adapter and the owned loopback server, with the framework security policy and fixed failure responses. `internal/origin` canonicalizes origins.
- **Public API** (`webview`): `Open`, `Window`, `Mode`, `Options`, `Limits`, `ServerLimits`, `ErrUnsupportedContent`. The platform factory is `nil` on every target until the native stages.

## Decisions within the work order's latitude

The work order lets the implementer settle local names and helper placement; these are the choices that a reader of the code might otherwise question.

- **Only explicitly identified file-deletion residue is nonfatal.** Michael approved narrowing the original blanket exception after Terminus `56ec511e74de`. After native release and bounded deletion attempts, a directly returned `platform.FileDeletionResidue` with a path and cause retains the error without canceling other windows. Ordinary errors, recovered panics and joins containing another failure take the existing fatal cleanup path. Live native objects and running cleanup still keep the window registered.
- **`http.ErrAbortHandler` in-process answers with a fixed 502.** There is no connection to sever, and the attachment contract has no "fail after waiting" call; adding one for this case alone seemed premature. It is not an application failure. An unexpected panic answers with a fixed 500 and fails the application.
- **Every admitted in-process request reserves its worst case at admission**, not incrementally: metadata, body (declared, or limit plus detection byte), one native read buffer, a full response, its native copy and response metadata. No allocation can then fail mid-request under aggregate pressure, and with the defaults sixteen worst cases fit. Limits that cannot hold one worst case are rejected as options.
- **Refusals without admission draw on 16 bounded slots**, reserved out of the buffer budget at 1 KiB each (a test measures every fixed response against that). When all are in native delivery the browser is told to abort, owing nothing.
- **Metadata has a 1 KiB minimum**, because every response carries the framework policy (about 370 bytes) and fixed headers.
- **Native body reads are granted at most 64 KiB at a time** and must match a declared length exactly; close cancels reads in progress.
- **Informational responses (1xx) fail with 502 in-process**, as unsupported features rather than silently dropped.
- **Loopback refuses upgrades with 501** before admission, after the access check; unauthorized requests get 403 before anything else. The server's error log is discarded.
- **The loopback credential is a 256-bit random value** in a cookie named `dfx2_endpoint`; the browser contract carries it to the attachment, which must install it HttpOnly before navigating.
- **The framework policy is committed as the response header is sent**, after the handler's own changes, so a handler can tighten but not remove it.
- **A shell destroyed while its browser still used it is fatal**, including a reentrant destruction during creation.

## Portable checks

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=300s
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./... && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
CGO_ENABLED=0 go test ./internal/appcore ./internal/endpoint ./internal/origin ./webview -count=50 -timeout=600s
CGO_ENABLED=0 GOMAXPROCS=1 go test ./internal/appcore ./internal/endpoint ./webview -count=20
go test -race ./internal/appcore ./internal/endpoint ./webview -count=30 -timeout=900s   # cgo for the detector only
CGO_ENABLED=0 go test . -run TestDependencyBoundaries -count=1
```

All passed. The dependency test now also requires that the root reaches neither `webview`, `internal/endpoint`, `internal/origin` nor `net/http`, that platform packages never reach `internal/endpoint`, and that the walk actually sees `webview` and `internal/endpoint`. The Windows test binaries for `webview`, `internal/endpoint` and `internal/appcore` compile; cross-builds are not execution.

### Discriminating tests

Each contract below has a test that failed when the behavior was deliberately broken in a scratch edit, with the tree restored afterward (`/tmp/dfx2-mutations/mutate.py`, kept out of the repository):

| Broken behavior | Test that caught it |
| --- | --- |
| An abandoned in-process handler releases its admission | `TestFiniteAbandonedHandlerKeepsAdmissionUntilItReturns` |
| The in-process endpoint drains while a handler runs | `TestFiniteCloseCancelsAndDrainsOnlyAfterHandlersReturn` |
| A late result is attached after close | `TestFiniteLateResultAfterCloseIsReleasedNotAttached` |
| An oversized response publishes its prefix | `TestFiniteLimitFailuresAreRequestLocalAndRecover` |
| Admission ignores the aggregate budget | `TestFiniteAdmissionAndAggregatePressureRefuseWithoutQueueing` |
| The loopback endpoint drains while a handler runs | `TestLoopbackCloseCancelsAndDrainsOnlyAfterHandlersReturn` |
| The loopback access layer ignores `Origin` | `TestLoopbackAccessLayerGuardsTheHandler` |
| The framework policy is added before the handler, so it can be removed | `TestLoopbackAccessLayerGuardsTheHandler` |
| The shell is destroyed before its browser releases it | `TestShellOutlivesItsBrowserAndEndpointDrain` |
| A browser closed during creation is still started | `TestCloseDuringBrowserCreationNeverStartsNavigation` |
| Readiness is granted to a closing window | `TestCloseDuringBrowserCreationNeverStartsNavigation` |
| A refused browser shell skips the last-window recheck | `TestUnsupportedBrowserHostingLatchesNothing` |
| A window drains before its endpoint | `TestShellOutlivesItsBrowserAndEndpointDrain` |
| Cleanup residue cancels the application | `TestBrowserCleanupResidueIsRetainedWithoutCanceling` |
| A late completion is settled as if the window were open | `TestEndpointCompletionsSettleOnTheUIThreadAndHoldTheWindow` |

Terminus findings added more, each confirmed to fail against the code it replaced: `TestFiniteOversizedResponseMetadataIsNeverCopied`, `TestFiniteDeliveryStaysWithinTheAdmissionReservation` and `TestLoopbackListenerFailureIsReportedBeforeDrainInEitherOrder` from round 1; `TestLoopbackListenerFailureOverlappingCloseIsKept` and `TestFiniteOversizedURLIsRefusedBeforeParsing` from round 2; the overflowing-limit cases in `TestLimitsNormalize`, with `TestLimitsAtTheirCeilingKeepAdmissionArithmeticExact`, from round 3; `TestFiniteBodyIsAllocatedOnceWithinItsReservation` from round 4. A private listener seam injects the accept failures; the overlap test holds `Accept` until the server's shutdown has closed the listener.

Two of the table's mutations were not caught at first: the shell-destruction mutation was invisible to a test whose browser released synchronously, and the recheck mutation was masked by the other window's later destruction event, which rechecks too. Both tests were tightened until the mutation failed them.

Other coverage, without a mutation run: starting-window accounting, close during shell creation, readiness refused after close, unexpected parent loss, browser failure draining every window, fatal parent/endpoint/browser creation failures releasing what they acquired, cross-window endpoint cancellation, cleanup failure after an earlier failure, refused native requests outside in-process mode, responses only to an open browser, duplicate publication, close during body reading and while the browser holds a response copy, reentrant release from inside `Respond`, routing by origin, metadata/limit failures and recovery, the bounded refusal reserve, writer interfaces, `HEAD`, informational responses, panics versus `ErrAbortHandler` versus deliberate 500s in both modes, loopback credential/Host/Origin/upgrade checks, connection and handler limits, streaming before the handler returns, close draining, endpoint independence, option validation and the three modes end to end through the public package on the fake backend.

### Not covered

- Nothing native: browser creation, cookie installation, request interception, confinement, CSP enforcement, storage isolation, error pages, child-process failure and release order all belong to stages 2 and 3. The fake browser proves only the core's side of each contract.

## Native Linux regressions

The shared core and both backends changed (content kinds, browser-shell refusal), so the stage 4 Linux matrix was rerun on this tree, unchanged, inside isolated headless GNOME Shells:

```sh
python3 evidence/synthetic-mixer/linux/stage4_matrix.py --out evidence/webview-integration/linux/stage-1-regression --base /tmp
```

[`linux/stage-1-regression/`](linux/stage-1-regression/), 2026-11-12, on `5f807d4` plus the final working tree recorded in its [manifest](linux/stage-1-regression/manifest.json): mixer `f41a89649bee3f914043233e64e72b2bbd3f00bcd2bc1eaab484359ce34a89da`, lifetime `f18746c06de6306ac1e39a540813e46cba6f36f0f57bbbaeb02ff16080cf4a6c`. All eight runs exited 0: the controls and command audition with its text-cache runs and stage 1's regression matrix under Wayland, XWayland and XWayland at `GDK_SCALE=2`, and the foundation lifetime matrix under Wayland and XWayland. The [audit](linux/stage-1-regression/audit.json) read 55 application process logs, each draining once with every ownership count at zero and empty stderr, and decoded all 78 PNGs. Rebuilt afterward with `CGO_ENABLED=0`, both binaries hash identically.

An earlier full run on the round 1 tree also passed 8/8 with a clean audit; it was replaced rather than kept, because gofmt and a comment later shifted line numbers in linked packages and so changed both binaries. This is regression evidence for native-only and drawn windows; it exercises no browser path.

## Review

Four Terminus rounds over the working tree, as Michael directed: run up to four, then stop for his review. Each finding was fixed before the next round, outside the usual one-finding-per-turn gate, with a discriminating test confirmed to fail against the replaced code. Every finding was in `internal/endpoint`; none touched the core, platform contract or public package. These dispositions await Michael's review and veto.

| Round | Review | Finding | Disposition |
| --- | --- | --- | --- |
| 1 | `896be4c84f89` | Response headers were cloned and converted before the metadata limit applied | Measured on the handler's map before any copy |
| 1 | | Delivery charged two response-metadata copies (and the Go body's length, not capacity) against one reserved | Both copies reserved at admission; the Go result charged at capacity |
| 1 | | A listener failure was dropped when cancellation or close won the race, and drain could precede the failure | Failures no longer filtered by owner state; published before serving ends |
| 2 | `c6e65fafcda1` | `net/http` reports `ErrServerClosed` for an accept failure that overlaps shutdown | Real accept failures captured in the listener wrapper |
| 2 | | The request URL was parsed before the metadata limit applied | Metadata measured first |
| 3 | `d10e52ff2315` | Large positive limits overflowed the reservation arithmetic | Buffers at most 2^50; each byte limit within Buffers |
| 4 | `c88036256c36` | Unknown-length body growth held two buffers beyond the body reservation | Body allocated once at its reserved capacity |

**At the implementer handoff, round 4's correction had not been reviewed**: the four-round budget was spent. Its test fails against the growth it replaced, and all portable gates pass on the final tree. The handoff verdict was therefore `not_clean` with that one finding fixed; subsequent review is recorded below.

A self-review between rounds 3 and 4 also made the adapter parse a Go-owned copy of the request URL, since `url.Parse` keeps substrings of its input and request strings may view native memory.

## Operator-review follow-up

Michael approved fixing another response-metadata gap: `headerSize` skipped map entries with nil or empty value slices, although `Header.Clone` retained them. The adapter now charges each such name and entry overhead before copying; oversized metadata takes the existing 502 path. Normal empty-header semantics are preserved.

`TestFiniteEmptyHeaderEntriesCannotBypassMetadataLimit` failed on the old code in both subtests: 4,096 entries were copied under a 1 KiB limit. Both pass after the fix. `TestFiniteEmptyContentTypeStillSuppressesSniffing` guards the legitimate small case. These and the existing oversized-metadata/delivery-reservation tests pass 30 repetitions. The full no-cgo suite, Linux vet, and Windows cross-build/vet pass. The Linux regression binaries rebuild to the exact hashes above, so that native-only/drawn evidence carries forward; no native browser claim is added.

Terminus `56ec511e74de` reviewed this tree: `not_clean`, with all 307 starting-point files covered. It raised one blocker: browser cleanup routes every error, including a recovered panic, through the nonfatal residue path. It also advised updating the stale webview summary in `AGENTS.md`. Michael accepted F1: the cleanup exception is now narrowed as described above. Michael also accepted F2: `AGENTS.md` now distinguishes the built portable contracts from pending native browser hosts and links the current behavior. The metadata fix was not flagged. Stage 2 has not started.

`TestUnexpectedBrowserCleanupFailureCancelsOtherWindows` failed in all eight cases against the old classification: ordinary errors, panics, a residue joined with another error, and panicking with a residue, each with another window alive in window and resident modes. All pass with the fix, with panic stacks and error causes retained. The cleanup tests pass 100 repetitions; the existing nonfatal residue and earlier-failure retention tests remain passing. The full no-cgo suite, Linux vet, Windows build/vet and Darwin build pass. Because the shared core changed, the native Linux matrix was rerun into [`linux/stage-1-cleanup-rerun/`](linux/stage-1-cleanup-rerun/), preserving the preceding evidence. All eight runs pass; the raw audit is clean across 55 process logs and 78 PNGs. The manifest records mixer SHA-256 `942f9b2b628e41c3028c5a9462f3a365ea51a0577fae30be4153790ef18822ec` and lifetime SHA-256 `1a762689b55ffce3efeaef2de1608c25295ca16aaf37c8a6fd802312d94ef513`. These are native-only/drawn regression results, not browser execution.

Michael also approved aligning the lifetime and failure qualities in `../terminus-canon` with this narrow exception. Those edits are uncommitted; broader webview canon drift is still separate.

## Review before transport canon alignment

Terminus `9a76ddc582ad` returned **clean**, with no findings and all 578 starting-point files covered. It reviewed the cleanup classification correction and the accepted `AGENTS.md` orientation update under the canon's narrow file-residue exception. The reviewer found no material violation of the selected qualities; this does not add native browser evidence or constitute stage acceptance. Only this verdict record and the journal changed after the review.

## Review under aligned canon

Terminus `7f8e730c211d` returned **not clean**, with all 578 starting-point files covered. Its one blocker is an implicit loopback response: a handler that returns without writing bypasses the response wrapper's commit methods, so net/http's implicit 200 lacks the mandatory framework CSP. Michael accepted the fix: normal handler return now commits the framework policy before net/http sends an implicit 200. The earlier clean review is preserved above, but is not the latest verdict.

`TestLoopbackImplicitResponseKeepsFrameworkPolicy` exercises a real local HTTP server with header-only handlers, both without an application CSP and with a permissive one. Both cases failed before the fix and pass afterward; the implicit 200, empty body and application headers are preserved, and the framework policy is present exactly once alongside the application's policy. The endpoint suite passes 30 repetitions; the full no-cgo suite, Linux vet and Windows build/vet pass. Mixer and lifetime rebuild to the cleanup-rerun hashes above, so that native regression evidence still applies. The correction was included in Terminus `44c6d5722451`, recorded below.

## URL and parser-response review

Terminus `44c6d5722451` returned **not clean**, with all 578 starting-point files covered. The implicit-response fix was not flagged. The review raised two blockers: in-process metadata reservations omit parsed/decoded URL and RequestURI storage and parsing precedes aggregate admission; net/http's own pre-handler parser rejections bypass the framework CSP. Michael accepted F1; it is fixed as recorded below. Michael accepted a narrow F2 exception for standard-library protocol rejections before the endpoint handler runs; the aligned canon now states it. No parser replacement or connection-output interception was added.

The F1 fix reserves extra URL representation storage before cloning/parsing and reuses the parsed URL for origin checking. Refusal rolls back that provisional reservation. The allowance stays with the request until final release and is included in worst-case limit validation; configured metadata and aggregate limits are unchanged.

`TestFiniteEncodedURLStorageFitsMetadataReservation` measures the backing ranges of the actual retained strings and merges aliases rather than repeating the accounting formula. Before the fix it found 30,027 retained bytes against 13,083 charged. `TestFiniteAggregateAdmissionPrecedesURLParsing` also failed before the fix: under aggregate pressure, parsing still classified a malformed URL as 400 rather than refusing admission with 503. Both now pass, reservations release, and URL path/query semantics are unchanged. The existing aggregate-pressure fixture now uses a near-limit URL because worst-case capacity includes URL workspace. Finite/limit tests pass 30 repetitions; the full no-cgo suite, Linux vet, Windows build/vet and Darwin build pass. Both native audition hashes still match the cleanup rerun. This correction was included in review `5c7f92167c60` below.

The F2 exception is covered by `TestLoopbackProtocolRejectionsStayOutsideApplicationResponses`: real credentialed raw requests receive 400 for malformed headers and 431 for oversized headers, with bounded plain-text bodies and no application-handler call or credential in the body. The test accepts net/http's connection reset after unread oversized input only with its complete fixed error body. A subsequent normal request reaches the handler and carries the mandatory framework policy. The endpoint suite passes. This pass changes tests, documentation and canon only; there is no new runtime mechanism.

Both dispositions were included in review `5c7f92167c60`. The full no-cgo suite, Linux vet, Windows endpoint test-binary build and Windows vet pass after the protocol-response test was added.

## Final-status review

Terminus `5c7f92167c60` returned **not clean**, with all 578 starting-point files covered. Neither URL accounting nor the accepted pre-handler protocol-response exception was flagged. Michael accepted its one finding: the loopback wrapper treated status 101 as interim although net/http commits it as final, so the framework CSP was added too late. The final-status condition now includes 101, matching net/http; upgrade support is unchanged.

`TestLoopbackSwitchingProtocolsCommitsFrameworkPolicy` failed against the old condition: the real HTTP response carried only the handler's permissive policy. It passes with the one-line correction. The full no-cgo suite, Linux and Windows vet, and Windows endpoint test-binary build pass. The change is confined to the endpoint and its test, which the native-only/drawn audition binaries do not link. This correction was included in the clean review below.

## Latest review

Terminus `ffc14d98a8ea` returned **clean**, with no findings and all 578 starting-point files covered. It reviewed the 101 correction under the aligned canon and found no material violations of the selected contracts. This is review of the portable implementation and its tests, not native browser execution or stage acceptance. Only this verdict record and the journal changed afterward. No further review is needed absent another substantive change.

## Open before acceptance

- **Canon alignment.** Michael authorized updating the existing transport quality and rubric to cover the built portable contracts while keeping native browser hosting pending. Those edits and the earlier narrow residue alignment are uncommitted in `../terminus-canon`. The clean review above exercised the aligned canon and the final correction.
- **Linux signal decision** remains the prerequisite for stage 2.
