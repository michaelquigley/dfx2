# Webview integration feasibility spike

Current direction (2026-11-12): the [isolated cgo comparison](webview-cgo-spike.md) found the same signal conflicts with both bindings. Michael chose to stop expanding the signal investigation and monitor the compatibility risk during representative dfx2 and dfw development/use. Single-executable distribution remains primary. Retain the findings and existing probes; do not add signal patches, a helper or a newer-runtime requirement on their account. This decision accepts continued development with an unresolved risk, not a claim of signal safety or approval of a new GC-signal policy. Earlier investigation instructions below are historical; they do not authorize another round. Windows work remains unstarted.

Prove the native mechanisms that the [webview integration draft](webview-integration.md) depends on, before writing its production work order. Michael approved this Linux-first spike, followed by a Windows host handoff. The result is evidence and a mechanism recommendation, not root-framework browser integration.

The agreed target is a native mixer and a webview over one application model. This spike does not build that target. It answers four narrower questions: can we provision the browser's loopback credential safely, isolate and retire temporary sessions, show a useful load-error page with safe retry, and coexist with Go's signal handling on Linux without cgo?

## Approved next task: Linux closeout and Windows handoff

Michael approved this bounded closeout on 2026-11-12. Use the existing purego VM-free host and installed runtime; do not continue the cgo comparison or signal investigation. This task prepares evidence and a transferable candidate, not root-framework integration.

1. **Reconcile the current summary.** Update the Linux feasibility record's opening/status to distinguish historical mechanisms from the VM-free candidate and the decision to monitor signals. Preserve old logs and conclusions as dated evidence. State the exact experimental GC-signal setup used by the candidate; reusing the existing explicit reservation for this fixture is allowed, but does not choose a production API or approve a new signal mechanism. Keep the graphics-teardown uncertainty separate. A supported newer runtime is not a prerequisite to this closeout or Windows transfer; no claim that the installed runtime contains that fix follows. Correct the cgo record's unsupported claim that `RLIMIT_RTTIME` signals are confined to the overrunning thread, using the upstream kernel source identified in the latest journal; no new hazardous probe is needed.
2. **Rerun affected browser cases.** Identify which earlier cases exercise paths changed by the shared page/driver work, then rerun them with the VM-free host in a new directory. Use page-driven reports and native input for error-page actions, not host JavaScript-result conversion. Include the corrected forced-crash fixture case where affected. If an old driver assumes host JS evaluation, adapt only the necessary driver seam or map its assertions to the VM-free runner; document any remaining uncovered assertions. Run the original single-window regression when shared host paths changed. Record the candidate binary hash, source revision/dirty diff, commands, runtime versions, result counts and native exits. Do not rerun the signal matrices or add another instrumentation project. Failures remain findings; do not reinterpret them as passes.
3. **Prepare Michael's manual check.** Supply exact commands for the same candidate and leave a VM-free error-page window ready when the desktop is available. Give two short actions: failed GET, restore endpoint and click Retry; failed POST, click Return to start and verify no POST replay. The driver/server should collect the counts so Michael only has to click and report what he sees. Record his actual observation when supplied; automated clicks do not close the manual item. If interaction is unavailable, give reproducible launch steps and mark it pending without blocking independent handoff preparation.
4. **Refresh the Windows handoff.** Reconcile the portable fixture, page-driven report route and relevant Linux outcomes with `experiments/webview2-spike/evidence/windows/integration-feasibility/README.md`. Windows implements and runs passes B–D: cookies/confinement, session/profile lifetime, error pages and shutdown. Linux signal work is not a dependency. Keep the candidate awaiting Michael's commit, and require the Windows session to record its checked-out commit plus any dirty diff. Never invent a commit hash for the uncommitted tree.

Run the affected portable tests/vet and driver tests. Run `unfurl -i` separately on each authored Markdown file. Keep old evidence intact and leave root code, public API, canon and system packages untouched. No new runtime requirement, helper, cgo conversion, signal patch or extra abstraction. Append only durable decisions and live state to the journal. Return the results, manual status and handoff readiness; do not commit, push or start Windows work. Michael's commit and subsequent Windows validation precede the production work order.

## Scope and starting points

Extend `experiments/webkitgtk-spike` and `experiments/webview2-spike`, which remain separate modules. The Linux module imports the Windows experiment's portable adapter/fixture through a local module replacement; keep both directories in the checkout. Shared changes must still compile for both hosts. Reuse the existing callback routing, copied-data worker boundary, dispatcher and lifecycle instrumentation. Add only the native interfaces and fixture operations needed here.

Read the project `AGENTS.md`, recent journal entries, [the integration draft](webview-integration.md), [the retained webview findings](webview.md), and both experiment READMEs. Linux must also read the [shutdown investigation](../../experiments/webkitgtk-spike/evidence/linux/shutdown/README.md) and its observer limits. Windows needs no grimoire checkout: this brief and the repository documents supply the task.

No root Go code, public API, mixer refactor, canon edits, new frontend toolchain, JavaScript RPC layer or production transport implementation. Keep `CGO_ENABLED=0`. Do not copy mygo code or add it as a dependency. Native callbacks are permanent shared trampolines; route by numeric IDs and retain native ownership explicitly. Do not add a cgo helper to make the proof pass.

Keep prior evidence intact. New modes or flags must distinguish these probes from the old single-window fixtures. Preserve ordinary modes where practical; if a shared path changes, rerun the affected earlier cases and name the changed behavior rather than relabeling old evidence. Two browser windows in one host process are needed for isolation checks; no native mixer window is required in this spike.

## Pass A: Linux baseline and signal coexistence

Record Go, OS, desktop/backend, GTK, WebKit, JavaScriptCore and Soup versions and the actual loaded library paths. Capture the current host's baseline before changing it. Old `/tmp` comparison libraries may be gone; do not assume a journal path still exists. Use a supported runtime containing the graphics-teardown fixes for the final native result. If unavailable, report that prerequisite separately from what the installed runtime can demonstrate. Do not install or replace system packages as an incidental spike change.

Inspect `../dfw/webview/jsc_signal_linux.go` and its probe tests as a lead only. They use cgo and are not a no-cgo implementation to copy into this module. Establish the required ABI and initialization order from the actual runtime/headers or upstream definitions, retaining citations and versions in the result. A private `JSConfigureSignalForGC` dependency, if needed, must be identified as private, with symbol availability and supported-version limits explicit. This spike may evaluate it; successful evaluation does not make it an accepted production dependency.

The proof must observe relevant signal dispositions before and after JSC initialization, show that existing Go handlers are preserved, and exercise both Go work/GC and actual JavaScript execution/allocation/GC activity where observable. A missing warning or a rendered page is insufficient evidence. Use an explicit application-reserved signal, accounting for runtime/libc reservations. Inferring availability is withdrawn after the reservation follow-up: subscribed and unsubscribed signals can have identical Go-owned kernel dispositions. Checks cannot verify the application promise that neither it nor its dependencies uses or later reclaims the signal. No hard-coded signal number, suppression of diagnostics, or overwrite of an existing handler.

Exercise failure cases such as unavailable configuration support or no suitable signal without silently retaining the collision. These can use injected discovery results, explicitly labeled as modeled. Run hazardous signal probes in disposable subprocesses; do not alter the operator's shell or other applications. Report initialization order, selection rationale, observed disposition changes, workload and limits. If safe coexistence cannot be established, stop treating Linux as ready for production integration and return that evidence; independent browser-policy probes may still produce explicitly limited diagnostic results.

### Approved follow-up: handler safety

Completed with a negative result: UI-thread-only host VM use does not make GC suspension safe. Preserve that evidence; do not continue its combined-host path. The next authorized investigation is below.

The reservation probe is complete; do not repeat its campaign. Michael accepted explicit reservation as the proposed application contract and authorized this focused follow-up. Windows work has not started. Linux production readiness and the private-symbol/fault-handler-adjustment decisions remain pending.

1. Trace JSC's registration and suspension targets using the relevant runtime source. Establish whether UI-thread-only host JSC use is sufficient, including when that same OS thread has returned from native code to Go. Distinguish host-process JSC work from JavaScript in browser children. Targeted probes must exercise the suspected transition; more successful repetitions without a mechanism explanation do not settle it.
2. Identify the SIGXCPU handler, its installer, purpose and installation conditions. Observe its behavior without guessing that early JSC initialization permanently excludes it.
3. In the proposed safe experimental path, restrict any SA_ONSTACK adjustment to explicitly identified SIGSEGV/SIGBUS handlers, preserving the handler/mask/other flags and verifying their identity. Do not carry the broad patch-new-native-handlers helper into that path. Unknown or changed handlers are evidence requiring review, not permission for a wider patch. Leave the GC suspend handler unchanged. Preserve the old broad-patch/hazard probes as historical comparisons if needed.
4. If the mechanism has a defensible safety basis, exercise the combined explicit reservation, initialization and narrow fault-handler adjustment in the actual multi-window feasibility host. Test recovered Go faults, relevant signal delivery, page/handler activity and close while the integrated host is active. Passes B–D previously established separate mechanisms; they did not exercise this combined path. If safety cannot be justified, stop with that result rather than running a larger matrix to imply safety.

Return a compatibility contract with source evidence, exact tested runtime versions and explicit gaps, or a precise account of why the approach cannot provide one. Stay in the experiments, keep cgo disabled, retain old evidence and do not change the production API or canon. Do not replace system runtimes incidentally. Stop for review after this follow-up; the private-symbol and fault-handler mechanism are not approved for production by this task.

### Approved next investigation: no JSC VM in the Go host

Completed with bounded positive evidence on WebKitGTK 2.52.6: required exercised paths remained VM-free, with separate positive controls. This supports continuing the VM-free design, not a general signal-safety or cross-version claim. The next task is SIGXCPU below.

Michael approved testing this narrower route after the handler-safety result. It preserves browser-side JavaScript and both HTTP transports; it excludes creating or using a JSC VM in the Go process. This is a feasibility hypothesis, not production approval. Windows work remains unstarted.

1. Trace required WebKit UI-process paths through cookies, content filters, isolated storage, navigation/redirects, error-page actions, requests/responses, inspector use and teardown. Determine whether any implicitly creates a JSC VM or registers a Go-owned thread for GC scanning. Identify the exact path and version rather than inferring safety from unchanged fault handlers alone.
2. Add a distinct experimental host path with no direct JSC context creation, forced JSC initialization, native JavaScript evaluation/result conversion or script-message callbacks producing host JSCValues. Replace the fixture's `js` automation with page-driven scenarios and results sent through its existing handler. Do not add a second listener to the in-process transport or a new public JS bridge. Exercise error-page actions through actual input or a narrowly identified mechanism that does not create a host VM.
3. Instrument host-process VM creation and thread registration independently of the fixture. Prove the instrumentation with a separate disposable positive-control process that intentionally creates a VM and registers a Go thread; do not contaminate the VM-free process with that control. Combine observed absence with source-path evidence. Distinguish browser children, which legitimately run JavaScript, from the Go host.
4. If the required paths remain VM-free, exercise them together in two live windows, including pending requests, close/reopen and error retry. Record exact operations covered, untested paths and runtime versions. Preserve earlier results as browser-policy evidence, not proof of the new path.

Do not pursue signal-mask changes around native calls/callbacks, revive the host-VM workaround, or carry its fault-handler patch into this path. Continue to require an explicit reservation for any experimental GC-signal configuration, but keep the private symbol and production mechanism unapproved. Record SIGXCPU installation/disposition as a separate unresolved safety issue; do not patch or dismiss it as a consequence of VM avoidance. Failure to create a host VM does not by itself prove all signal coexistence safe.

Stop for review with either the bounded VM-free compatibility evidence or the exact required operation that prevents it. No root framework, canon or production API changes; cgo stays disabled, old evidence remains, and agents do not commit or push.

### Approved next investigation: SIGXCPU

Michael authorized this focused investigation on 2026-11-12. Preserve the VM-free host boundary; Linux production readiness and the private GC-signal configuration symbol remain unapproved. Windows work is unstarted.

1. Find whether a supported WebKit configuration can prevent installation of its SIGXCPU handler. Trace the relevant entry points and initialization timing using the exact runtime source; distinguish public/supported controls from private symbols, undocumented environment variables and downstream changes. Merely disabling real-time thread promotion is insufficient: in 2.52.6, the `RealTimeThreads` constructor schedules handler setup independently of `m_enabled` ([source](https://github.com/WebKit/WebKit/blob/webkitgtk-2.52.6/Source/WTF/wtf/linux/RealTimeThreads.cpp)).
2. If a prevention mechanism exists, exercise it before the required host operations and check handler installation directly. Retain resolved VM/thread-registration instrumentation. Use a control showing the ordinary host installs the handler. Observe both kernel disposition and delivery to an explicitly registered Go SIGXCPU subscriber, and check that no later registration or close/reopen silently reinstalls WebKit's handler. Record thread scheduling and any process resource-limit side effects; an absence of warnings or denied real-time promotion is not proof of prevention.
3. If prevention is unavailable, return the smallest proposed compatibility mechanism and its precise consequences for Go/application SIGXCPU behavior, WebKit's real-time demotion path and native-thread delivery. Do not implement a new handler patch, wrapper or signal-mask workaround in this pass. Source analysis may recommend a change, but that decision comes back to Michael first.

Run potentially terminating signal tests only in disposable subprocesses with bounded supervision. Do not alter the operator's shell, system scheduling configuration, installed runtime or unrelated processes. Maintain the explicit experimental GC-signal reservation and the prohibition on host-VM APIs; neither becomes production-approved through this task. Do not reopen the completed reservation campaign or rerun all old probes without a changed path that warrants it.

Report tested runtime versions, source references, discriminating controls, observed effects and unavailable coverage. A result on 2.52.6 remains diagnostic; validation on a supported runtime with the graphics-teardown fixes is still required. Before eventual handoff, rerun the old driver cases affected by its recent changes; Michael's manual error-page check remains separate and cannot be replaced by synthetic clicks. Stop for review when this investigation has an answer. Root code and canon stay untouched; agents do not commit or push.

## Pass B: cookie delivery and endpoint confinement

For owned loopback, create an isolated window session, generate a random endpoint credential and install it through the native cookie API as an HttpOnly session cookie. Navigate only after installation succeeds. The endpoint validates the credential, exact assigned Host and applicable Origin before invoking the fixture handler. Legitimate initial/resource requests may lack Origin; absence alone cannot authorize them. No token in the page URL, JavaScript, screenshot or ordinary logs.

Use a controlled second loopback listener as a collector, at the same host but another port. Do not use an Internet service or unrelated local service as an attack target. The collector records safe counters and whether the reserved credential was present, never its value. Cookie scope does not isolate ports, so successful navigation blocking alone cannot establish this policy.

Demonstrate all of the following:

| Case | Required observation |
| --- | --- |
| Initial document, separate CSS/JS/image assets, relative fetch, GET query and POST body | Authenticated requests reach the normal handler; JavaScript cannot read the HttpOnly credential |
| Reload and same-origin redirects | Authentication still works without reprovisioning or credential-bearing URLs |
| Loopback streaming | At least two response chunks become visible before the handler completes; finite in-process buffering is not used |
| Ordinary external HTTP client without a credential, with an incorrect credential, or with rejected Host/Origin | Access refused before the application handler runs; retain separate admission and handler counts |
| Cross-port resource, fetch, form/navigation, popup and iframe attempts, including redirects from an allowed URL | Disallowed network requests do not reach the collector, not merely become unreadable to JavaScript; no credential escapes |
| Two windows with separate endpoint credentials | Each can use its endpoint; neither can invoke the other's handler using its own browser context |
| Close during cookie installation and installation failure | No late navigation; callback ownership drains; failure is visible |

The list is a discriminating probe set, not proof of a general browser sandbox. State exactly which enforcement mechanisms cover which request classes. If relying on content policy headers, include how they compose with handler-provided policy, redirects and error pages; do not quietly rewrite application assets or weaken the agreed boundary. Additional network paths discovered during investigation must be reported as supported, denied, or untested rather than disappearing from the claim.

Repeat endpoint-routing isolation with two in-process windows and their platform-specific origins. In-process hosting does not need the loopback cookie, but another window or unapproved document must not reach its handler merely by naming the origin. Keep this distinct from ordinary browser same-origin response-read protection: count actual handler invocation.

A small fixture/client may deliberately supply the right credential in an internal test to establish that the endpoint check works; do not publish it or mistake that probe for a new external-client provisioning API. Tokens, additional cookie sessions and unauthenticated access remain future policies. The access check belongs before the handler, separate from browser credential installation.

## Pass C: temporary sessions and native release

Use two windows in one process, with independently owned sessions. Test cookies, local storage, session storage and an observable cached resource. Reload preserves the appropriate live session; B cannot read A's values or reuse A's authenticated/cache state. Close A while B continues to make requests. Reopen A with a new session and verify fresh state while B remains unchanged. An independent HTTP test client must still reach a separately owned attachment server after its browser closes.

Windows' existing `webview2-userdata/<mode>` directory is persistent and cannot satisfy this test. Never use the operator's normal browser profile. If temporary on-disk profiles are required, allocate unique clearly identified directories under a dedicated experiment-owned temporary root. Record paths and ownership without recording credentials or profile contents.

Establish when native users release each profile: callbacks, controller/context/environment references and relevant browser processes. Native close returning or a host counter reaching zero is not by itself proof that a browser child released its files. Document the evidence and any gaps. Do not kill a shared browser process merely to make a directory disappear or close A successfully at B's expense.

Only after native use ends, attempt bounded deletion retries. Log attempts and elapsed time. On persistent deletion failure, finish with a cleanup error naming the residue and never reuse it. While native use remains outstanding, continue waiting/reporting instead; filesystem deletion failure is not permission to abandon ownership. Inject a post-release deletion failure to prove the distinction, and label it injected. Leave native timing and actual deletion results separate. Retry timing is a spike parameter to report, not a new framework constant.

A forced-crash probe may leave experiment-owned residue; demonstrate freshness on next startup without scanning or deleting other instances' directories. No automatic scavenger is required. Do not commit profiles or bulk browser caches. Retain a directory manifest and cleanup outcome, not their private contents.

## Pass D: load-error page and safe retry

Implement the smallest host-local error page that can show a top-level load failure when the endpoint itself is unreachable. It offers Retry for a failed GET and Return to start as a GET of the configured initial URL. For a failed POST, offer only Return to start. Both actions remain subject to the window's origin policy. No automatic retry or generic reload that can resubmit a POST.

Use a controlled attachment server that can stop and restart to create a real network load failure; its lifetime is independent of the browser. For owned modes, distinguish a load failure from fatal owned-server failure: killing the owned server is not evidence for a recoverable page-load error. Use an appropriate native load failure or a labeled injection for that branch, and report which actually ran.

Verify initial and subsequent GET failure, explicit Retry after service returns, failed POST navigation, and redirects whose originating request was POST. Server-side counters must establish that no edit is replayed by Retry or Return to start; do not rely only on what the page displays. Keep successful HTTP 404/500 responses visible as ordinary responses. Failed fetches and assets must not replace the entire page.

The error UI must not gain universal origin access, expose the endpoint credential, accept an arbitrary destination from page-controlled markup, or create a popup/subframe exception. Show how its buttons are connected to the native host and how policy checks still apply. A narrowly scoped internal mechanism does not authorize a public JavaScript RPC API. Keep a screenshot of the error page and a manual check of its two actions.

## Evidence and stopping conditions

Use new directories under each experiment, for example `evidence/linux/integration-feasibility/` and `evidence/windows/integration-feasibility/`. Each result records source revision plus any dirty diff, binary hash, runtime/library versions, invocation, transport and observed outcome. Retain raw process/fixture/collector logs, a compact machine-readable summary, representative screenshots and a prose result mapping each required observation to its evidence. Keep secrets, SDK binaries, browser profiles and core dumps out of git.

Carry the Linux descendant observer forward. Separate successful host drain from observed child exits and from statuses the observer cannot obtain. Include ordinary close, close with a pending request and docked-inspector close in both transports. This monitors the old shutdown issue; a passing run does not prove its historical cause or remedy.

Use a bounded driver timeout as a failing hang guard, not a successful shutdown deadline. Mark timeout/forced termination as failure and retain outstanding ownership. Missing native coverage is untested/blocked, never a pass. Use deterministic injected checks for failure paths that cannot be provoked natively; no requirement for thousands of repetitions or a broad new mutation campaign.

If a mechanism cannot meet the agreed behavior, preserve the failed probe and report the smallest conflict plus alternatives. Do not silently permit off-origin credential delivery, shared/persistent storage, handler abandonment or POST replay. Bring any design change back to Michael. No speculative workaround or new platform guarantee is accepted just because a probe stopped failing.

## Linux delivery and Windows handoff

First complete the Linux probes and update the experiment README with exact new build/run commands and flags. Keep the original quick checks available:

```sh
# From experiments/webkitgtk-spike
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test dfx2webview2spike/adapt dfx2webview2spike/fixture
python3 -B -m unittest -v test_exitwatch

# From experiments/webview2-spike
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...
```

These commands are existing portable checks, not the new native audition. Supply executable native commands after adding the probes; do not hand off placeholders or claim a cross-build executed Windows. Run the affected old native cases when shared host paths change, documenting any intentional incompatibility in the new probe mode. Root framework tests/native matrices are not required when root code and dependencies are untouched.

Before transfer, create `experiments/webview2-spike/evidence/windows/integration-feasibility/README.md` with the candidate commit placeholder clearly awaiting Michael's commit, implemented probe flags, exact PowerShell commands, expected outcomes and manual steps. It must tell the Windows implementer which portable fixture changes already landed and which native mechanisms remain to implement. Michael commits; the Windows operator checks out that exact candidate and records its hash.

The Windows implementer follows this brief's passes B–D and the common evidence requirements, skipping only Linux signal work. Use the existing pinned SDK/loader acquisition in `setup-loader.ps1`, retain the known-loader-path and checksum checks, and run the IDL binding test with `WEBVIEW2_IDL` set. Validate added interfaces against the actual SDK. No grimoire, Terminus installation or root-framework work is required on the Windows host.

Run the executable and drivers built from the recorded candidate; if Windows changes code, record the resulting dirty tree and rerun affected checks on that code. Record Runtime/SDK versions, Windows build, local console/RDP conditions and untested scenarios. Keep the session active for interactive evidence; a black capture from a minimized RDP session is not a successful screenshot. Audition both transports, two simultaneous windows, error/retry interaction and independent close/reopen. Linux-only signal conclusions do not transfer to Windows; Windows-only profile conclusions do not transfer to Linux.

Both implementers leave changes uncommitted for Michael. After his Windows commit returns to Linux, inspect changed code and raw results, rerun affected portable/shared-fixture checks and Linux native probes if their paths changed, and reconcile the mechanism recommendations. Neither agent commits or pushes.

## Completion

The report answers each question with demonstrated / failed / untested, names the proposed production mechanism and its version dependencies, and lists any exact contract decision needed from Michael. Experimental packages remain excluded comparison evidence; do not treat a Terminus coverage exclusion as a review of the spike. No root-framework stage is accepted by these results.

Once both host results are understood, revise the focused integration spec, ground its private platform extension and write the production work order for Mercurius review. Detailed production adapter budgets, general HTTP edge cases, fixture model integration, browser-crash recovery and full framework validation belong to that later work, not this mechanism spike.
