# Linux cgo webview comparison

Status (2026-11-12): the comparison returned the same signal conflicts under cgo and purego on 2.52.6 and stopped before the conditional browser-policy work. See the [result](../../experiments/webkitgtk-cgo-spike/README.md). Michael chose monitoring during representative application work instead of extending this investigation. No further accommodation experiment is authorized by this brief. The result does not establish production signal safety or require adopting cgo.

Determine whether a narrow cgo webview backend can give dfx2 one distributed executable, the distribution's installed WebKitGTK, and correct coexistence with Go with less machinery than the purego approach. Michael authorized this comparison on 2026-11-12. This is a feasibility experiment, not production implementation or approval of signal-handler changes.

## Constraints and placement

Single-executable distribution is a primary requirement. Use the installed Linux runtime first. Do not replace system packages, embed another executable, build an extraction mechanism or introduce a separately shipped helper. Dynamic dependencies on the system browser libraries are expected, as in dfw; list them explicitly.

Create a separate module under `experiments/webkitgtk-cgo-spike/`. It may use `import "C"`, native headers and `CGO_ENABLED=1`. This is the explicit exception to the repository's no-cgo instruction. Keep root Go code, dependencies, canon, existing purego implementations and their evidence unchanged. Immediate-mode-only consumers must still build with `CGO_ENABLED=0`; a mixed application importing a future cgo webview package would require cgo at build time. Windows gains no cgo requirement from this experiment.

Read the recent journal, [integration draft](webview-integration.md), and the original [feasibility record](../../experiments/webkitgtk-spike/evidence/linux/integration-feasibility/README.md), particularly the reservation, handler-safety, VM-free and SIGXCPU investigations. Inspect dfw's cgo entry points and signal probes as leads, not a proven compatibility contract. Do not copy its inferred signal-selection policy.

## First answer: does cgo change the obstacle?

Build the smallest VM-free GTK/WebKit host using public C interfaces. Keep JavaScript in browser children; no evaluation-result conversion, script messages or host JSC context. Use page-driven HTTP reports if needed. Run the Go HTTP handler in the host for this first comparison so its fault recovery is actually exercised alongside WebKit. Keep application-visible Go progress running while the native event loop runs.

Record exact compiler/Go/package versions, build commands, loaded libraries, process/thread topology and which callbacks execute Go. Compare with the existing purego VM-free host on the same installed runtime. Identify concrete effects of cgo on signal dispositions, masks and stacks; do not infer them from the choice of binding or absence of crashes.

Use fresh bounded subprocesses for hazardous probes. Observe before WebKit initialization, after page load, and after teardown/reopen:

- SIGUSR1, SIGUSR2, SIGHUP and SIGXCPU ownership and Go subscriber delivery. Include process-directed and native-thread-directed delivery; record signal masks and stack flags. Explicit sends do not prove the real-time CPU-limit path.
- Recovered Go nil faults and the existing controlled SIGBUS fault probe, while serving requests and after browser initialization. Recovery must leave the process and endpoint usable. Separate expected Go HTTP panic recovery from the framework's future fatal-handler-panic policy.
- Host VM construction, JSC thread registration/suspension and fault-handler installation. Reuse external instrumentation where practical, require watch resolution and a positive control in a separate process. A silent unresolved breakpoint is not a negative result.
- The relationship between C execution and Go callbacks: a C event loop alone does not prove a thread can never run Go or receive a process-directed signal there.

Start with unmodified library signal behavior. An additional comparison may reuse the already explored explicit GC-signal reservation and private configuration call, clearly labeled as a dependency rather than a cgo benefit. Never infer an unused Go signal or silently take a subscription. Do not add handler replacement/chaining, handler-flag patches, startup interposition, per-call signal-mask changes or private scheduling controls to make the comparison pass.

If a known conflict remains, stop the dependent work. Return the reproducer and source-level explanation of why cgo does or does not change it. If a small native accommodation could help, describe its mechanism, application restrictions and failure behavior for Michael's decision before implementing it. Do not port the full browser-policy fixture around an unresolved safety mechanism. A negative result is a useful completed spike.

## Only if the signal mechanism is defensible

Exercise two windows, close one while the other continues, reopen, docked inspector, pending requests at close, and final host/child exit. Keep the old graphics-teardown concern separate: process or binding changes do not establish a fix for it. Record observed failures and limits on the installed runtime honestly.

Reuse the existing shared loopback fixture for cookie-before-navigation, confinement, page-driven reporting and error-page Retry/Return without POST replay. Avoid duplicating its policy implementation or building the mixer. Implement only the C bindings needed for these checks. If shared fixture changes are necessary, rerun the affected old cases and preserve their prior evidence.

For packaging, demonstrate that the built executable runs without its source/build directory, using the installed runtime libraries. List build-time headers/toolchain separately from runtime dependencies. Describe how same-executable window mode could isolate the application's server, but do not implement a general process supervisor or claim that re-execution eliminates Go from the window process. A second-process topology is optional comparison work only if it answers a remaining concrete question.

Show the intended opt-in package boundary with a minimal consumer or dependency inspection, and verify the existing root suite and mixer build with `CGO_ENABLED=0`. Keep native-only startup's no-browser-loading requirement explicit; ordinary dynamic linking may load libraries even in a mode that opens no browser, which must be reported rather than obscured.

## Return and stop

Retain commands, logs and a concise README under the new experiment, distinguishing observation, source argument and untested paths. Report:

1. What became simpler because of cgo, and which signal problems remain.
2. Whether correctness requires private APIs, a newer runtime, application signal restrictions or unapproved native accommodation.
3. Build and runtime dependencies, single-executable distribution, and native-only package isolation.
4. The smallest viable recommendation and its remaining validation, including graphics teardown. Do not equate repeated passing runs with a compatibility contract.

Run tests/vet for the new module and relevant reused fixtures; keep portable checks separate from native execution. Run `unfurl -i` on authored Markdown. Append only durable conclusions and pending decisions to the journal. Leave production docs/canon and the Windows handoff unchanged, and do not commit or push. Return for review before extending the experiment or integrating the result.
