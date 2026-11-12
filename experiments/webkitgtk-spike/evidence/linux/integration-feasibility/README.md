# Linux integration feasibility, 2026-11-11

The Linux half of the [feasibility spike](../../../../../docs/future/webview-feasibility-spike.md), run against the [integration draft](../../../../../docs/future/webview-integration.md). Source: revision `9b48257` plus the uncommitted experiment changes recorded in each run's `source.diff` and `environment.json`. Ubuntu 26.04.1 LTS, kernel 7.0.0-42, Wayland desktop session, Go 1.27.1, `CGO_ENABLED=0`. Installed runtime: WebKitGTK and JavaScriptCore 2.52.6, GTK 3.24.52, libsoup 3.6.6, GLib 2.88.0, loaded from `/usr/lib/x86_64-linux-gnu` (paths in each `environment.json`). Nothing here touches root framework code.

**Runtime prerequisite, separately:** the installed 2.52.6 does not contain WebKit's graphics-teardown fix ([shutdown investigation](../shutdown/README.md)). No supported runtime with it is installed on this host, and none was installed for this spike. The only fixed libraries available are the earlier isolated Debian 2.54.2 extraction, a mixed-package diagnostic comparison, not a supported deployment; Pass A ran two comparison cases against it. All other results describe 2.52.6.

## Status, 2026-11-12: Linux closeout

This record accumulated in order. The sections below it are dated evidence, kept with their own conclusions. This section says which of them still stand.

- **The candidate.** The purego VM-free host, `-feasibility -feas-vmfree -signal-reserve 60`, built with `CGO_ENABLED=0` and run on the installed WebKitGTK 2.52.6.
  - The Go process creates no JavaScriptCore VM. It binds no JavaScript evaluation or result conversion and registers no script-message handler. JavaScript runs only in WebKit's children.
  - Automation is page-driven: results are posted to each window's own handler. Error-page actions are driven by native pointer input.
  - Closeout results are [below](#closeout-rerun-2026-11-12).
- **Experimental GC-signal setup in the candidate.** The fixture reuses the explicit reservation from the [reservation follow-up](#signal-reservation-follow-up), unchanged:
  - `-signal-reserve 60` supplies the application's choice, and the host runs its sanity checks. Its declared subscriptions are SIGINT and SIGTERM, plus SIGXCPU with `-feas-xcpu`.
  - The host releases 60 from Go (`signal.Ignore`, then `SIG_DFL`), then calls the private `JSConfigureSignalForGC(60)` before WebKit's first type initializes JSC.

  This is fixture setup. It does not choose a production API: the `JSC_SIGNAL_FOR_GC` route and restoring Go's handler after initialization, from the cgo comparison, remain undecided. Nor does it approve a signal mechanism.
- **Signal compatibility is a documented, monitored risk (Michael's decision, 2026-11-12).** Known on 2.52.6, in the VM-free host, with either binding:
  - JSC's suspend handler occupies the GC signal: 60 here, SIGUSR1 if unmodified. Any signal sent to it kills the process ([cgo comparison](../../../../webkitgtk-cgo-spike/README.md)).
  - WebKit replaces Go's SIGXCPU handler, so subscribers lose SIGXCPU. Its handler lacks `SA_ONSTACK`.
  - The kernel sends `RLIMIT_RTTIME`'s SIGXCPU process-directed: Linux v7.0 `kernel/time/posix-cpu-timers.c`, where `check_thread_timers` calls `check_rlimit` with `PIDTYPE_TGID`. It can therefore reach a Go thread. Not reproduced natively.

  No version floor, helper, cgo adoption or signal patch follows from this. Revisit on representative failures, using ordinary runtime and crash records.
- **Historical, kept as evidence:**
  - Pass A's host-VM mechanism: inferred signal choice (withdrawn), host JSC VM plus a fault-handler `SA_ONSTACK` adjustment (not a compatibility contract).
  - The first passes B–D, which ran with a host VM through the `js` command: browser-policy evidence only.
  - The SIGXCPU investigation's 2.54.1-minimum proposal (not accepted).
  - The cgo comparison's proposals (not adopted).
- **Graphics teardown is separate.** The installed 2.52.6 lacks WebKit's graphics-teardown fix ([shutdown investigation](../shutdown/README.md)), and nothing here claims otherwise. A supported newer runtime is not a prerequisite for this closeout or the Windows transfer; it is still needed before production readiness.

## Answers

| Question | Linux result |
| --- | --- |
| JSC signal coexistence without cgo | **A documented, monitored risk; not resolved** (Michael's decision, 2026-11-12). The VM-free candidate keeps JSC's fault handlers out of the Go process. JSC's suspend handler still occupies the GC signal, and any signal sent on it kills the process. On 2.52.6, WebKit also takes SIGXCPU. Production GC-signal policy is undecided. The earlier answers in this table's history (Pass A through the cgo comparison) are dated evidence in the sections below. |
| Native cookie delivery and endpoint confinement | **Demonstrated** for the probe set below, with a native content filter as the primary enforcement and the endpoint's CSP as an independent second mechanism. |
| Temporary sessions and release | **Demonstrated** with in-memory ephemeral sessions. No on-disk profile is needed on Linux, so post-release profile deletion has nothing to delete. The deletion mechanism is exercised on the one on-disk resource this host owns, compiled content filters, and portably with an injected failure. |
| Load-error page and safe retry | **Demonstrated** automatically, in all three transports; rerun with the VM-free candidate using clicked actions ([closeout](#closeout-rerun-2026-11-12)). **[Manual check](#manual-check): done**, confirmed by Michael on 2026-11-12. |

## Closeout rerun, 2026-11-12 ([`closeout/`](closeout/))

**Candidate.** `/tmp/dfx2-wk-candidate`, SHA-256 `d48a609825839951ea27fbd122e2def7432dee04f4f9d1f99d4d2efb202c07dd`.

- **Source:** revision `9b48257` plus the uncommitted tree recorded in `closeout/browser/source.diff` and `environment.json`.
- **Build:** `CGO_ENABLED=0 go build`, Go 1.27.1.
- **Runtime:** installed packages `libwebkit2gtk-4.1-0` and `libjavascriptcoregtk-4.1-0` 2.52.6-0ubuntu0.26.04.1, `libgtk-3-0t64` 3.24.52, `libsoup-3.0-0` 3.6.6, `libglib2.0-0t64` 2.88.0. Kernel 7.0.0-43, Wayland.

No commit hash exists for this candidate. The tree awaits Michael's commit.

**What is affected.** Since the first passes B–D run, these changed:
- the shared page: a `/api/report` route, an `OnReport` hook, and `probe.js`'s page-driven runner
- the feasibility host: VM-free mode, absolutely positioned error-page buttons, and the window URI in `status`
- the driver: a wrappable `Host`, the attachment server's report route, and `<title>` on its 404/500 pages
- `main.go`'s flag dispatch

Every B–D case loads the page, so all of them were rerun with the candidate. `main.go` sits on the ordinary host's startup path, so the original single-window regression was rerun too. No signal matrix was rerun.

| Run | Command | Result |
| --- | --- | --- |
| Browser cases | `python3 -B vmfree.py /tmp/dfx2-wk-candidate closeout/browser --no-monitor --reserve 60` | **19 of 19 scenarios, 88 of 88 checks.** Every host exited 0, except `forced-crash`'s deliberate −9; no observed child failed and no fatal log appeared. |
| `combined` again | the same with `--only combined`, into `closeout/browser-combined-rerun/` | Passed (9 of 9 checks) |
| Single-window regression | `python3 -B audition.py /tmp/dfx2-wk-candidate closeout/regression-audition` | **14 of 14** |
| Manual-check driver | `python3 -B manual_check.py /tmp/dfx2-wk-candidate closeout/manual-check-selftest --reserve 60 --self-test` | Both steps behave as designed. Harness validation only: the driver clicked. |

`--no-monitor` runs only the browser cases: no gdb, no positive controls and no disposition case. It adds the three confinement attribution variants and the old `forced-crash` case, run with the candidate's flags; that case needs no host JavaScript. The VM-free property itself comes from the monitored [`vm-free/`](#no-host-vm-investigation) run. Here the host's own record still shows SIGSEGV and SIGBUS as Go's at exit in every scenario.

**Rerun scenarios:**
- `loopback-basics`
- `confinement-{filter-csp,filter-only,csp-only,control}`
- `inprocess-isolation`
- `cookie-close-race`, `cookie-failure-injected`
- `storage`
- `error-pages`
- `owned-load-failure`
- `shutdown-{loopback,inprocess}-{ordinary,pending,inspector}`
- `combined`
- `forced-crash`

**Matches the first run.**
- Confinement attribution is identical in all four variants. The control's collector received the same 12 paths, 10 of them carrying the credential; with enforcement it received nothing.
- `forced-crash`: the killed host's filter store remains, named by its pid; the next host used a fresh store and removed only its own.

**A driver defect, found and fixed.** In `combined` the reopened loopback window reused the label `A`. The host's `status` is keyed by label, and the closed `A` is still listed there, so its Retry handler counts came out incoherent ("first navigation 3, retry −3"). The check passed on its navigation events, which are unaffected, but the count observation was invalid. The rerun gives the reopened window its own label (`A2`) and recorded 3 handler calls for the first navigation and 1 for the Retry, each with exactly one allowed navigation.

The earlier `vm-free/combined*` runs had the same label reuse. Treat their handler-call numbers the same way. Their navigation-based Retry judgment is unaffected.

**Assertions the VM-free runner covers differently, or not at all.** These are the cases where the old suite read the page through host JavaScript.
- **Error-page content.** The old suite read the buttons and `document.scripts` from the page. Now the host records its own markup: whether Retry is offered, and that it has no `<script`. Clicks at the button positions decide the rest (a click where Retry would be does nothing on a POST page), and screenshots are kept.
- **A forged action.** The old suite issued it from the error page. Now it comes from an ordinary page in the window; the error page itself runs no script.
- **Same-origin redirect.** Checked by handler counts, the final URI and the absence of refusals, not by reading the page text.
- **Credential visible on the owned error page: uncovered.** The old `document.cookie` check cannot run without script on that page. It rests on the cookie being HttpOnly and the error page running no script.
- **Child process lists.** `storage` no longer snapshots the child process list at each stage. Process names are still recorded for the whole run.

**Manual check.** See [below](#manual-check). Automated and self-test clicks do not close it.

## Manual check

**Status: done (2026-11-12).** Michael redid the check deliberately (`closeout/manual-check-2/`) and reported: "it worked exactly as you described."

- **Failed GET:** the error page offered Retry and Return to start. Retry loaded the page.
- **Failed POST:** only Return to start was offered. It returned to the probe page.

The server counts agree with his account. The GET was repeated exactly once, and no POST reached the server in either step. The host exited 0 with no child failure.

An earlier run (`closeout/manual-check/`) recorded the same counts, but Michael clicked it without knowing it was the check, so it does not count as the manual result.

The driver does not click. It counts what reaches its attachment server and records the result. What Michael reports is recorded here separately; automated and self-test clicks do not close this item.

```sh
cd experiments/webkitgtk-spike
CGO_ENABLED=0 go build -o /tmp/dfx2-wk-candidate .   # or reuse the candidate above
python3 -B manual_check.py /tmp/dfx2-wk-candidate evidence/linux/integration-feasibility/closeout/manual-check \
  --reserve 60 --step-timeout 3000
```

A window titled `dfx2 feasibility M — attach` opens. The driver prints one JSON line per step.

1. **Failed GET.** The page navigates to a GET while the server is down, and the error page appears with Retry and Return to start. The driver restores the server, then prints `STEP 1 of 2`. **Click Retry.** The driver expects exactly one more GET and no POST.
2. **Failed POST.** The page submits its form while the server is down, and the error page appears with only Return to start. The driver restores the server, then prints `STEP 2 of 2`. **Click Return to start.** The driver expects the start page to load and no POST at all, so nothing is replayed.

Report what you saw: the buttons offered on each page, and where each click took you. The counts are in `closeout/manual-check/result.json`.

## Pass A: signals ([`signals/`](signals/))

`signal_probe.py` runs 24 cases, each in a disposable subprocess under a hang guard; `summary.json` judges each, and each case keeps its stdout, stderr and, for two cases, a compressed strace. Dispositions are read with `rt_sigaction` from the kernel, and each handler is attributed by address to Go (the handler on SIGURG), default, ignore, or the library containing it. Initialization order and behavior come from WebKit source at `webkitgtk-2.52.6`, checked against `webkitgtk-2.54.2`:

- `JSConfigureSignalForGC(int)` is declared only in `Source/JavaScriptCore/API/JSBasePrivate.h`, a header the installed packages do not ship. It is exported by both runtimes' `libjavascriptcoregtk-4.1.so.0` but is **private**. It fails only once the suspend signal is configured, and validates nothing; an invalid signal later hits a `RELEASE_ASSERT` (`Source/JavaScriptCore/API/JSBase.cpp`).
- `Thread::initializePlatformThreading` (`Source/WTF/wtf/posix/ThreadingPOSIX.cpp`) uses SIGUSR1, or `JSC_SIGNAL_FOR_GC`, unless the private call came first. It installs `SA_RESTART | SA_SIGINFO`, no `SA_ONSTACK`, without chaining, and warns when replacing anything but `SIG_DFL`. Its handler only suspends a thread whose stack pointer lies within that thread's own stack.
- `SignalHandlers::finalize` (`Source/WTF/wtf/threads/Signals.cpp`, once, from `Config::finalize`) installs SIGSEGV and SIGBUS with `SA_SIGINFO` only, saves the previous action, and chains unhandled faults to it with the same `siginfo` and `ucontext`, on the same stack.

Observed on 2.52.6:

| Case | Observation |
| --- | --- |
| No configuration | JSC takes SIGUSR1 from Go with its warning; the application's `signal.Notify(SIGUSR1)` stops receiving it. An ordinary external SIGUSR1 crashed 7 of 10 runs: JSC's suspend handler ran on a goroutine stack and faulted. |
| Any configuration | Once JSC initializes in the Go process, it replaces Go's **SIGSEGV and SIGBUS** handlers. A recovered nil dereference or SIGBUS on a goroutine is then fatal: `non-Go code set up signal handler without SA_ONSTACK flag`. Configuring the GC signal does not change this. The 2.54.2 comparison libraries behave the same. |
| Policy selection (**superseded**) | libc's real-time range is 34–64, but Go's own signal table treats 32–34 as libc-internal and installs no handler there (found in the follow-up). Almost every other catchable signal starts with Go's handler, so a default disposition proves nothing. This first policy took the highest real-time signal still owned by Go, unblocked and not in the application's declared list, here 64. It releases it through `signal.Ignore`, returns it to `SIG_DFL` so JSC replaces nothing, then calls `JSConfigureSignalForGC` before any WTF initialization. JSC then installs on 64 with no warning; Go keeps SIGUSR1, SIGUSR2 and SIGHUP and delivers them to the application. **The follow-up shows that the inference can take a signal another component subscribed to without declaring it; the observations in this row stand, the selection rule does not.** |
| Proposed order (`configured-onstack`) | After that, the host forces JSC's one-time initialization with a C-API context before WebKit starts, and adds `SA_ONSTACK` to the SIGSEGV and SIGBUS handlers JSC just installed, keeping their handler and mask. Go then recovers both faults, with and without WebKit running a page, on 2.52.6 and on the 2.54.2 comparison. 10 of 10 workload runs clean; WebKit installed nothing further in this order. |
| `SA_ONSTACK` on the GC signal too | **Hangs** 3 of 10 runs, as the source predicts. Never add it there. |
| Workload | About 3 s of JavaScript allocation with `JSGarbageCollect` and private `JSSynchronousGarbageCollectForDebugging` on the host thread, beside goroutines allocating, calling `runtime.GC` and running a tight loop that only asynchronous preemption stops. strace: about 24,000 SIGURG preemptions and **no** GC-signal deliveries with JavaScript on one thread. With JavaScript on two Go threads in one context group, about 3,200 GC-signal deliveries, about half to the idle main thread; 10 of 10 runs, plus the traced run, clean. |
| Late configuration | Refused natively after initialization; the probe reports not ready. |
| `JSC_SIGNAL_FOR_GC` preset | Respected. The host releases that signal from Go first, and refuses an operator choice outside libc's range or already in use. JSC also prints `ERROR: invalid option: JSC_SIGNAL_FOR_GC=63` from its option scanner while using the value. |
| Missing symbol, no usable signal | Injected discovery results, labeled as such: refused before any JSC initialization, so the collision is never silently kept. |
| WebKit-initialized order (observation) | GTK initialization sets SIGPIPE to ignore. WebKit context creation installs only the GC signal. After a page loads, an unidentified handler appears on **SIGXCPU (24)**, in `libjavascriptcoregtk` near WTF's thread code, a plain handler without `SA_ONSTACK`. It did not appear when JSC was initialized first. |

**Proposed mechanism, for decision.** Before loading WebKit: take the real-time signal the application explicitly reserved for JSC, run the sanity checks in the follow-up, and release it from Go. Never infer one. Call the private `JSConfigureSignalForGC`, refusing to start a browser if it is absent or refuses. Force JSC initialization. Add `SA_ONSTACK` to JSC's fault handlers, never its suspend handler. Then audit the reservation and any later native handler without `SA_ONSTACK`. Supported range: the symbol is present and behaves as above in 2.52.6 and the 2.54.2 comparison, and is private in both.

**Residual gaps.** JSC's suspend handler still runs without `SA_ONSTACK`, so it can run on a goroutine stack when JSC suspends a Go thread executing Go code. That was observed only when JavaScript ran on more than one Go thread, and without failure; it is not proven safe. Production should confine host-process JSC use to the UI thread. The SIGXCPU handler's installer is unidentified, and only process-directed SIGXCPU would reach it. The reservation is a promise by the application, which the host can only partly check; see below.

## Signal reservation follow-up ([`signals-reservation/`](signals-reservation/))

Run 2026-11-11 after a host reboot, on the same 2.52.6 runtime; the 2.54.2 comparison libraries were lost with `/tmp` and were not used. `python3 -B signal_probe.py BINARY OUT --set reservation` runs 14 cases, the repeated ones 10 times, each in a disposable subprocess. The variants are separate from the earlier ones (`reservation_linux.go`), whose code and results are unchanged.

**The flaw.** An independent component calls `signal.Notify` on 64 without telling the host. The subscription works, and the kernel shows 64 exactly as it shows unsubscribed 63: Go's handler, same flags. `os/signal` cannot list subscriptions. The inferred selector picks 64. Releasing it with `signal.Ignore` cancels the subscription silently (`signal.Ignored` turns true), and the signal no longer reaches the subscriber. Through JSC initialization, raising 64 afterwards, as that component's sender would, killed the process in 8 of 10 runs and was simply lost in 2: JSC's suspend handler ran on a goroutine stack and faulted. A lost subscription is therefore a crash risk as well as a silent behavior change.

**Explicit reservation (prototype only, `-signal-reserve N`).** The application names a signal it reserves for JSC before browser initialization, and promises not to register, ignore, reset or otherwise repurpose it afterwards. Nothing is inferred: without a reservation the host refuses and JSC is never initialized. The host's checks are sanity checks:

| Check | Catches | Cannot establish |
| --- | --- | --- |
| Supplied | No reservation | — |
| In libc's real-time range | 10, 33, 65 refused | That no other library uses it |
| Kernel disposition is Go's handler | A native owner (injected `SIG_IGN`: refused); 32–34, which Go leaves to libc, including `SIGRTMIN` (34 refused) | That no Go subscriber exists |
| Not blocked on the initializing thread | A blocked signal | Masks of later threads |
| Not among declared subscriptions | SIGUSR1 refused | Undeclared subscriptions |
| Not ignored through `os/signal` | A prior `signal.Ignore` | — |
| `JSC_SIGNAL_FOR_GC` unset or equal | A conflicting operator setting (63 against 60: refused) | — |
| Audit after initialization | A later `signal.Notify` on N: Go reinstalls its handler over JSC's, and the audit sees it after the fact | Anything that leaves the disposition alone; it detects, it does not prevent |

Accepted reservations: 60 in 10 of 10 runs, and 35, the lowest Go-owned real-time signal. JSC owned N after initialization and after the workload, with no warning, and Go still delivered SIGUSR1, SIGUSR2 and SIGHUP to the application.

**What depends on cooperation.** With an undeclared subscriber already on 60, every check passed. Release still cancelled that subscription silently, and raising 60 afterwards killed the process in 8 of 10 runs. No check available to the host can see that subscriber. The reservation turns an unprovable inference into an explicit, documented application obligation; it does not make the host able to verify it. A violation after initialization is detectable only by auditing, and only once Go has reinstalled its handler; by then JSC's suspension on that signal is already broken. The reservation also does nothing for the suspend handler's safety on goroutine stacks, for JSC's fault handlers, or for SIGXCPU.

## Handler-safety follow-up ([`handler-safety/`](handler-safety/))

Run 2026-11-11 on kernel 7.0.0-43, Ubuntu packages `libwebkit2gtk-4.1-0` and `libjavascriptcoregtk-4.1-0` 2.52.6-0ubuntu0.26.04.1, Go 1.27.1, with the reservation 60 supplied explicitly. `python3 -B handler_safety.py BINARY OUT --reserve 60 --debug-file JSC_DEBUG` runs every probe in its own process and passed all 9 checks. Each probe's purpose is to establish a mechanism, not to count successes. Handler identities come from Ubuntu's debuginfod file for this exact build: build ID `e01f2b5c37edc2b2ae9d18b3d93306694d33ed27`, SHA-256 `212595dc04e5a6218865b65785c7faa19e45616e1d9a0ac3c83460fafc1187df`, kept outside git. Source citations are `webkitgtk-2.52.6`; 2.54.2 was not compared for these files.

**Result: this approach cannot provide a compatibility contract.** Keeping host-process JSC use on the UI thread does not make suspension safe. The defect is the GC suspend handler's interaction with Go stacks, and no adjustment available to the host removes it.

### 1. Registration and suspension targets

- **Registration.** `JSLock::didAcquireLock` (`Source/JavaScriptCore/runtime/JSLock.cpp`) adds a thread to its VM's `MachineThreads` thread group whenever it takes the API lock and was not the last owner. Only `Thread::didExit` (`Source/WTF/wtf/Threading.cpp`) removes it. Releasing the lock does not.
- **Releasing heap access.** When the lock count reaches zero, `JSLock::willReleaseLock` releases heap access. `Heap::stopTheMutator` (`Source/JavaScriptCore/heap/Heap.cpp`) then stops a mutator without access "instantly": the collector needs nothing from it. `shouldCollectInCollectorThread` is `!m_requests.isEmpty() && !(m_worldState.load() & mutatorHasConnBit)`. So once a C API call returns, the "JSC Heap Collector Thread", which always exists, can run a whole collection while the thread that called JSC executes something else.
- **Scanning.** The conservative-scan constraint calls `MachineThreads::tryCopyOtherThreadStacks` (`Source/JavaScriptCore/heap/MachineStackMarker.cpp`). It calls `Thread::suspend` on every registered thread except the current one, whichever thread conducts.
- **The retry loop.** `Thread::suspend` (`Source/WTF/wtf/posix/ThreadingPOSIX.cpp`) sends the reserved signal with `pthread_kill` and waits. The handler, `signalHandlerSuspendResume`, checks `thread->m_stack.contains(approximateStackPointer)`, the thread's OS stack bounds recorded at adoption. If the check fails, it sets `m_platformRegisters = nullptr`, posts and returns, and `suspend` yields and sends again. Nothing bounds that loop.
- **Other senders.** `VMTraps` and `ThreadMessage::sendMessage` target only a VM's owner thread, and are not enabled by default. The sampling profiler is off by default (`useSamplingProfiler` false, `usePollingTraps` false, `watchdog` 0 in `OptionsList.h`).

The targeted probe (`suspension-transition`) runs on the locked main thread, the role the GTK thread plays. It alternates 12 times: JavaScript through the C API, 250 ms of pure Go on its goroutine stack, then 250 ms blocked in `usleep` on its OS stack, each phase announced by a distinct syscall. The goroutine stack lies outside the thread's `[stack]` mapping. Traced, the main thread received the reserved signal **527 times during Go phases**, 11 during JavaScript, and none during C. `tgkill` shows the senders: the collector thread 234 times and a `HeapHelper` 293 times while the main thread ran Go. Untraced, `HeapHelper` threads burned CPU only during Go phases (16 ticks in 3 runs, 0 during C). The scanner keeps re-signalling a thread that is running Go until it happens to run on its OS stack.

Each delivery runs JSC's handler without `SA_ONSTACK`, so the kernel pushes the signal frame onto the goroutine stack. On this CPU the minimum frame is 2,032 bytes (`AT_MINSIGSTKSZ`). A goroutine is only guaranteed 928 bytes below its stack pointer (`stackGuard` = `stackNosplit` 800 + `StackSmall` 128, Go 1.27.1). A delivery at the wrong moment therefore writes below the goroutine's stack into adjacent memory: corruption, not a reported fault. The rate makes this a matter of when, not whether: hundreds of deliveries per second of Go work on the UI thread after a collection starts. Adding `SA_ONSTACK` moves the handler onto Go's signal stack, which is also outside the recorded bounds, so suspension never succeeds. That is the hang the earlier `configured-onstack-all` probe showed in 3 of 10 runs.

**JavaScript in browser children is a separate matter.** The web process is not Go, so its JSC is unaffected. The problem exists only because a JSC VM runs in the Go host, and only for threads that have taken that VM's lock.

### 2. SIGXCPU

- **What it is.** `WTF::sigxcpuHandler` (`Source/WTF/wtf/linux/RealTimeThreads.cpp:167` in this build) writes to an eventfd. The main loop then demotes WebKit's real-time threads, so that exceeding `RLIMIT_RTTIME` does not lead to SIGKILL.
- **Installation.** `RealTimeThreads::setupSignalHandler`, once per process, through `callOnMainThread` from the singleton's constructor, which the first `registerThread` triggers. It uses `SA_RESTART` only, with no `SA_ONSTACK` and no chaining, and it replaces Go's SIGXCPU handler.
- **Registration path (gdb).** `Thread::establishHandle` registers real-time-policy threads, here created for WTF `WorkQueue`s while a WebKit GObject is constructed. Promotion runs on our main loop.
- **Order.** With WebKit initialized first: 3 registrations, 3 promotions, handler installed (`narrow-webkit-webkit-first`). With JSC first: 1 registration, no promotion, and the handler not installed within 9 s. The source gives no reason that order should exclude it permanently; the difference is recorded, not explained.
- **Limits.** This session's `RLIMIT_RTTIME` is 200 ms, soft and hard. SIGXCPU is sent to the process, so it may land on a Go thread.
- **Not patched here.** It is a second native handler without `SA_ONSTACK` and a second taken Go signal, and needs review.

### 3. Narrow fault-handler adjustment

The `narrow` path adds `SA_ONSTACK` only to SIGSEGV and SIGBUS. It first verifies five properties that `SignalHandlers::finalize` (`Source/WTF/wtf/threads/Signals.cpp`) gives JSC's handler: installed by the forced initialization, one handler for both signals, not the suspend handler, flags exactly `SA_SIGINFO` (plus libc's `SA_RESTORER`), and a mask of every signal except the GC signal (and the four the kernel or glibc exclude). Handler, mask and other flags are preserved, and nothing else is touched. In this build the handler is `WTF::jscSignalHandler`.

- Both faults were recovered, with and without WebKit, in both orders.
- The GC handler stays unprotected, as required, and SIGXCPU is recorded but not patched.
- With an injected SIGBUS change after initialization, identity fails on four checks, nothing is modified, and a Go fault is still fatal.

This removes the fault-handler hazard **only**. It does not touch the suspension problem above.

### 4. Combined host run: not done

The safety argument fails at item 1, so the combined path was not run in the multi-window host. Doing so would only have shown successes that the mechanism above says are not guarantees.

### Directions for decision (untested)

- **No JSC VM in the Go process.** In every run so far, WebKit alone (context, view, page load) installed only the suspend handler and, in one order, SIGXCPU. JSC's fault handlers appeared only after the host itself created a JSC context. If the host never creates one, and never uses APIs that make JSCValues in the UI process (`evaluate_javascript`, `call_async_javascript_function`, script message handlers), no Go thread would be registered for suspension, and no fault-handler change would be needed. That fits the agreed design, which has no JavaScript RPC. It is a hypothesis: which WebKit UI-process paths create a VM must be traced, and the feasibility host's own `js` command would have to go. The reservation would still be needed, because WTF still installs the suspend handler; SIGXCPU would remain.
- **Block the reserved signal on the UI thread except during C calls.** A pending signal would then be taken only on the OS stack. Every native call and every callback from C would have to toggle the mask, and the delay would stall JSC's collection. This is invasive, and nothing here establishes it is reliable.

Neither is approved or implemented here.

## No-host-VM investigation ([`vm-free/`](vm-free/))

Run 2026-11-11 on kernel 7.0.0-43, Ubuntu packages `libwebkit2gtk-4.1-0` and `libjavascriptcoregtk-4.1-0` 2.52.6-0ubuntu0.26.04.1, GTK 3.24.52, libsoup 3.6.6, GLib 2.88.0, Go 1.27.1, Wayland, `CGO_ENABLED=0`, with the reservation 60 supplied explicitly. Run with `python3 -B vmfree.py BINARY OUT --debug-dir DIR --reserve 60`; versions, revision and diff are in `vm-free/environment.json` and `source.diff`.

**Result: on this runtime, every required path ran with no JSC VM in the Go host.** The paths covered were cookies, content filters, isolated storage, navigation and redirects, error-page actions, both request transports, the inspector, and teardown. Both positive controls caught a host VM where one exists. This is bounded VM-free compatibility evidence for 2.52.6, not signal safety.

### Instrumentation

[`vmwatch_gdb.py`](../../../vmwatch_gdb.py) runs each host under gdb and logs breakpoint hits from outside. The host does not know it is watched. Watched in `libjavascriptcoregtk`, resolved through Ubuntu's debuginfod file (build ID `e01f2b5c…`, SHA-256 `212595dc…` as above): `JSC::initialize`, `VM::VM`, `Heap::Heap`, `JSGlobalContextCreate`, `JSGlobalContextCreateInGroup`, `JSContextGroupCreate`, `jsc_context_new`, `jsc_virtual_machine_new`, `JSLock::didAcquireLock`, `MachineThreads::addCurrentThread` (GC scanning registration), `ThreadGroup::addCurrentThread`, `Thread::suspend`, the suspend handler, `SignalHandlers::finalize` (fault-handler installation), and `RealTimeThreads`. Libc's `sigaction` is also watched; the Go runtime calls `rt_sigaction` directly, so every hit there is native code. WebKit frames use the matching `libwebkit2gtk` debug file (build ID `9788492834087d9d0cdd85171ee0da0e43699eb1`, SHA-256 `816a7cafa62a0f53a267fa49c0dc0a05a124732e95161bf770c5aa7683dc44b2`), also kept outside git. gdb follows no forks, so WebKit's web and network processes, which do run JavaScript, are never in the count. Each run checks that every watched function resolved to a location.

Two positive controls run first, each in its own disposable process. Neither touches a VM-free host.

| Control | Observed |
| --- | --- |
| `control-direct`: `-signal-probe suspension-transition -signal-reserve 60` | A VM built on the Go main thread (`JSGlobalContextCreateInGroup` ← purego `syscallX` ← `runtime.asmcgocall`), two `MachineThreads` registrations of that thread, `HeapHelper` calling `Thread::suspend` from `tryCopyOtherThreadStacks`, 305 suspend-handler entries, and fault handlers installed by `SignalHandlers::finalize` (`Signals.cpp:603/605`). |
| `control-webkit-js`: the ordinary feasibility host, one `js` command | The VM is created by the result conversion, on the Go main thread: `toJSC` (`Shared/glib/JavaScriptEvaluationResultGLib.cpp:52`) → `API::SerializedScriptValue::deserializationContext` (`Shared/API/APISerializedScriptValue.cpp:102`) → `ensureContext` (`:64`) → `JSGlobalContextCreate` → `VM::VM` → `JSLock::didAcquireLock` → `MachineThreads::addCurrentThread`. Fault handlers follow, and SIGUSR1 is taken because that host configures no reservation. |

The second control identifies the operation in the earlier B–D runs that created a host VM. Those runs stay valid as browser-policy evidence only.

### Source paths (webkitgtk-2.52.6)

A delegated reader went through these files at the tag. The quoted lines were not independently re-read, and the gdb frames above confirm the conversion path.

- **Creates a host VM.** `JavaScriptEvaluationResult::toJSC` uses `deserializationContext()`. That is a process-wide singleton: on first use it calls `JSGlobalContextCreate`, and it releases the context after 10 s idle on the main run loop. Its callers are `evaluate_javascript`, `call_async_javascript_function`, `run_javascript` results, script messages (`WebKitUserContentManager.cpp`: `jsMessage.toJSC()`, and a reply's destructor calls `jsc_value_new_undefined(jscContextGetOrCreate(deserializationContext()))` even when nothing is sent), and the deprecated `webkit_web_view_get_javascript_global_context`.
- **Process-lifetime consequence.** `VM::VM` calls `Config::finalize()` (`JavaScriptCore/runtime/VM.cpp`). Once per process, that runs `SignalHandlers::finalize()` and freezes the WTF configuration. A single stray JavaScript-result or script-message call therefore installs the fault handlers for the rest of the process, even after the context is released. Registration of a thread lasts until that thread exits.
- **Initializes JSC without a VM.** `webkitInitialize` runs once, from class initialization of `WebKitWebContext`, `WebKitUserContentManager`, `WebKitUserContentFilterStore` and `WebKitWebsiteDataManager`. It calls `InitializeWebKit2` → `JSC::initialize`, which installs the suspend handler (`ThreadingPOSIX.cpp:217` in the controls) but no fault handlers. The remote inspector server starts only with `WEBKIT_INSPECTOR_SERVER`/`WEBKIT_INSPECTOR_HTTP_SERVER`, unset here. `JSC::initialize` hit 181–1,637 times per host: WebKit calls it as a once-guard.
- **No JSC use found.** `WebKitCookieManager.cpp`, `WebKitURISchemeRequest.cpp`, `WebKitURISchemeResponse.cpp`, `WebKitNavigationPolicyDecision.cpp`, `WebKitPolicyDecision.cpp`, `WebKitWebResource.cpp`, `WebKitFormSubmissionRequest.cpp`, `gtk/WebKitWebInspector.cpp`, `Inspector/gtk/WebInspectorUIProxyGtk.cpp`, and `Inspector/WebInspectorUIProxy.cpp` (its test evaluation is an IPC to the inspector's web process) were read whole. In `WebKitWebView.cpp`, `load_uri`, `load_alternate_html`, `reload`, dispose and the script-dialog paths were read. Content-filter rules are parsed with WTF's `JSON::Value::parseJSON` (`WebCore/contentextensions/ContentExtensionParser.cpp`), not JSC.
- **Read partially or not at all.** `WebKitWebsiteDataManager.cpp` and `WebKitWebContext.cpp` were read for about the first 40,000 characters, with no hits. Not read: the `get_snapshot` body, the tail of `WebKitWebView.cpp`, and the `WebPageProxy.cpp` completion lambda (the control's frames pass through it to `toJSC`). Observation covers the snapshot.

### VM-free host

`-feas-vmfree` is a separate mode of the feasibility host:

- It binds neither `call_async_javascript_function` nor `jsc_value_to_json`, refuses `js`, and registers no script-message handler.
- Its only JSC symbol is the private `JSConfigureSignalForGC`, called before WebKit's first class initialization, after the existing reservation checks.
- Automation is page-driven. The driver loads `?run=NAME&id=ID`; `probe.js` runs the probe and posts the result to its own handler's `/api/report`, as ordinary same-origin traffic. That goes through the guard over loopback and through the scheme handler in-process, with no extra listener. Attached windows post to the attachment server. `then=` actions (navigate, go, submit) follow the report.
- Reloads use `webkit_web_view_reload`.
- Error-page actions use synthesized GTK pointer events: motion, press and release at the button centres, through `gtk_main_do_event`. The page sees an ordinary click on the link, and the host intercepts it in `decide-policy` as before. The error page now places its buttons absolutely; its markup is unchanged otherwise, with no script.

All 16 scenarios, rows below, ran under the monitor. In each, every watched function resolved, `JSC::initialize` hit, the 13 VM, registration, suspension and fault-handler kinds stayed at **0**, SIGSEGV and SIGBUS were Go's at exit, the host exited 0 and every observed child exited cleanly.

| Scenario | Covered (checks as in passes B–D unless noted) |
| --- | --- |
| `loopback-basics` | Native cookie, assets, query, POST, credential invisible; native reload without reprovisioning; same-origin redirect (handler counts, no refusals); stream; external refusals and selftest; `js` refused; snapshot |
| `confinement-filter-csp` | Attacks against the collector and B's endpoint, top-level location, redirect, form and popup. The collector received nothing, by the same mechanisms as before. |
| `inprocess-isolation` | Two `dfx2://spike` windows: own handler only, no collector traffic, separate storage |
| `cookie-close-race`, `cookie-failure-injected` | As before: no late navigation, ownership drained, injected failure visible |
| `storage` | Two attached windows: storage and cache separation, native reload keeps A's session, close A while B works, fresh reopened A, attachment server survives |
| `error-pages` | Every Pass D case with clicked actions: GET failure and Retry; a page-supplied `dfx2-error:` action refused; POST failure with no Retry (a click at Retry's position does nothing); 303 and 307; 404/500 by title; failed fetch and asset keep the page; initial-load failure and Retry |
| `owned-load-failure` | Loopback and in-process native load failure, clicked Return to start |
| `shutdown-{loopback,inprocess}-{ordinary,pending,inspector}` | Close with `/api/slow` pending and with a docked inspector; everything drains |
| `combined` | Loopback A and in-process I live together: checks; pending requests in both; A closed with its request pending while I serves three requests; A reopened with a fresh session and a new endpoint; error, clicked Retry and Return to start in both; final closes with requests pending |
| `unreserved-dispositions` | No reservation: where the suspend handler goes |

**`combined`'s first recorded run failed one check of mine.** It counted Retry by handler calls, and over loopback one `/api/abort` navigation reached the handler 3 times, the Retry once (the dry run saw 2 and 2). This happens below the page. It is consistent with the network layer re-sending a GET whose kept-alive connection closed without a response, but that cause is not verified. Native events show exactly one allowed navigation per click and none in between. The check now counts navigations and records handler calls. The failed run stays in `vm-free/combined/`, and the rerun of that scenario alone passed (`vm-free/combined-rerun/`).

### Signals still installed (unresolved)

- **Suspend handler.** WebKit's JSC initialization installs JSC's suspend handler in the VM-free host. With the reservation it sits on 60 (`WTF::Thread::signalHandlerSuspendResume`, `SA_SIGINFO|SA_RESTART`, no `SA_ONSTACK`). Without one it replaces Go's SIGUSR1 (`unreserved-dispositions`). Nothing sent it here, because no thread was registered and no VM existed to scan. The reservation therefore remains required, and still uses the unapproved private symbol. What the handler would do if an unrelated sender raised the signal was not tested.
- **SIGXCPU.** In every VM-free host, `RealTimeThreads::setupSignalHandler` (`RealTimeThreads.cpp:191`) installed `WTF::sigxcpuHandler` (`SA_RESTART` only, offset `0x196d3f0`), replacing Go's handler, after 2–9 real-time thread registrations per run. Recorded only, not patched. Avoiding a VM does not touch it.
- **GLib and GTK** set SIGPIPE through libc `signal()` three times at startup (`gtk_init_check`, GIO's D-Bus proxy, gvfs). Recorded, not analyzed.

### Not covered

- Any API that produces a `JSCValue` in the UI process (the list above), by design.
- User scripts and style sheets, web process extensions and `WebKitUserMessage`, downloads, printing, find, reading or deleting cookies, fetching or clearing website data, favicons, context menus, permission and notification requests, file choosers, script and authentication dialogs (source only), and the remote inspector server.
- GTK 4 and the 2022 API, X11, and any WebKit version but 2.52.6. 2.54.2 was not compared: its libraries were lost in a reboot.
- Signal safety in general. A VM-free host is only one condition.
- The manual check of the error page's buttons is still Michael's. The automated clicks are synthesized toolkit events, not a person.

## SIGXCPU investigation ([`sigxcpu/`](sigxcpu/))

Run 2026-11-12 on kernel 7.0.0-43, Wayland, Go 1.27.1, `CGO_ENABLED=0`, with the VM-free host (`-feas-vmfree -signal-reserve 60 -feas-xcpu`). Runtimes:

- **Installed:** Ubuntu's 2.52.6-0ubuntu0.26.04.1.
- **Isolated:** Debian's 2.54.2-2 `libwebkit2gtk-4.1-0` and `libjavascriptcoregtk-4.1-0`, plus their missing dependencies. These were rebuilt from the [shutdown investigation's manifest](../shutdown/runtime-manifest.json) with every SHA-256 checked, and extracted under `/tmp/dfx2-webkit-runtime/root`; nothing was installed. They were loaded through `LD_LIBRARY_PATH` and `WEBKIT_INJECTED_BUNDLE_PATH`, and the host's `/proc/PID/maps` confirms which libraries loaded. This is a mixed-package diagnostic, not a deployment.

Command: `python3 -B xcpu.py BINARY OUT --reserve 60 --debug-dir DIR --runtime ROOT --runtime-debug-dir DIR2`. The 2.54.2 debug files came from Debian's debuginfod and are kept outside git. JSC build ID `767e6e97cf4d4b5fcef7e5554dae3ff52a3089e9`, SHA-256 `f2e3c4df082a427b41e6064b173589a89a0903d7631431b8c2e5dda47855edd7`; WebKit `4ff3a51a34486b0b80b7f9239272b8c281e18fe6`, SHA-256 `4ea8c59bcff49f53c972cfb310bf59b5602ba8eb106957e3d3600c20e5fb3671`.

**Result: on 2.52.6, no supported configuration prevents the SIGXCPU handler. Upstream WebKitGTK removed it in 2.54.1.** With Debian's 2.54.2 libraries, the VM-free host kept Go's SIGXCPU handler through its whole lifecycle, and a Go subscriber received every SIGXCPU sent to it. The proposal below is a minimum runtime of 2.54.1, not a handler change.

### Why 2.52.6 cannot be configured out

Sources: `webkitgtk-2.52.6`, read directly at the tag.

- `RealTimeThreads`' constructor schedules `setupSignalHandler` on the main thread unconditionally (`Source/WTF/wtf/linux/RealTimeThreads.cpp:76–77`). That function installs `sigxcpuHandler` with `SA_RESTART` only (`:191`).
- `m_enabled` and `setEnabled` (`:99`) only gate promotion and demotion. `setEnabled` is `WTF_EXPORT_PRIVATE`, and it can only run after construction, by which point installation is already scheduled.
- The singleton is constructed by the first `SCHED_RR` WTF thread. `QOS::UserInteractive` maps to `SCHED_RR` (`Source/WTF/wtf/posix/ThreadingPOSIX.cpp:292–293`), and `establishHandle` registers such threads (`:329`). `Thread::setCurrentThreadIsUserInteractive` does too, off the main thread (`Source/WTF/wtf/Threading.cpp:389`).
- The UI process creates such threads unconditionally, with hard-coded QoS and no setting, environment variable or public API:
  - `AuxiliaryProcessProxy::didFinishLaunching` creates every web and network process connection's receive queue as `UserInteractive` (`Source/WebKit/UIProcess/AuxiliaryProcessProxy.cpp:359`).
  - `WebsiteDataStore::resolveDirectoriesAsynchronously` does the same for its queue (`Source/WebKit/UIProcess/WebsiteData/WebsiteDataStore.cpp:520`).

  The monitor's frames show both in the host. Preventing construction would mean launching no web process.
- Ubuntu's build matches upstream here. Its debug information places the install at `RealTimeThreads.cpp:77` and `:191`. A scan of the installed library finds exactly one `sigaction(24, …)` call site, which symbolizes to the constructor's lambda.
- **2.54.0** keeps the same constructor. It also lowers the process's `RLIMIT_RTTIME` to 200 ms (soft limit 80%) on its direct `sched_setscheduler` path.
- **2.54.1** cherry-picks 319617@main, "[Linux] Remove RT threads" ([bug 319535](https://bugs.webkit.org/show_bug.cgi?id=319535), commit `6d1d5b0598`). `RealTimeThreads.cpp` is deleted. `HighPriorityThreads` raises priority with nice levels through RealtimeKit and sets a DVFS floor with `sched_setattr`, with no `sigaction`. The file is identical in 2.54.1 and 2.54.2.
- **Debian's 2.54.2-2 binaries** have no `sigaction(24, …)` site in either library (`sigxcpu/static.json`). Their other sites (signals 4, 7, 11, and the non-constant thread-suspend and fault-handler sites) match 2.52.6's.

### Runs

The lifecycle opens a loopback window, adds an in-process window, runs pages in both, docks an inspector, closes everything, then reopens. At each of those five stages the host reports SIGXCPU's kernel disposition, scheduling and limits. SIGXCPU is then sent twice: by `tgkill` to a native thread, and process-directed from the driver. Each time, the test checks whether the Go subscriber, registered through `os/signal` before WebKit initialized, received it.

| Run | Observed |
| --- | --- |
| `control-2.52.6` (monitored) | One native installation, from `setupSignalHandler` on the main loop, after the first window. At all five stages SIGXCPU belonged to `libjavascriptcoregtk` (`WTF::sigxcpuHandler`, offset `0x196d3f0`, `SA_RESTART` only). The Go subscriber received **0 of 10**. |
| `demotion-path/control-2.52.6` | The same run with two more watches: 10 SIGXCPUs → 10 eventfd callbacks (`RealTimeThreads::signalCallback`) → 10 `demoteAllThreadsFromRealTime` passes. |
| `unmonitored-2.52.6` | Without gdb: the same owner, and a native-thread SIGXCPU was not received. |
| `runtime-2.54.2` (monitored) | Isolated libraries loaded. Go's handler at start, at all five stages and at exit. **10 of 10 received.** No native `sigaction(24)`, no `RealTimeThreads`; `HighPriorityThreads` was created once, with 11 registrations. |
| `unmonitored-2.54.2` | Without gdb: Go's handler, and the native-thread SIGXCPU was received. |

Every monitored run kept the VM-free result: every required watch resolved, the 13 VM, registration, suspension and fault-handler kinds stayed at 0, SIGSEGV and SIGBUS were Go's at exit, and every child exited cleanly.

### Scheduling and limits

- **RealtimeKit denies this environment.** The daemon is active, with `RTTimeUSecMax` 200000, `MaxRealtimePriority` 20 and `MinNiceLevel` −15. Asked directly to promote a throwaway child of this harness, it answered "Access denied" for both real-time and high-priority requests (`sigxcpu/rtkit.json`). The harness runs in a systemd user-service scope. Why access is denied was not established, and a session where RealtimeKit grants may behave differently.
- **2.52.6 promotion.** WebKit made 11 promotion attempts. `sched_setscheduler(SCHED_RR | SCHED_RESET_ON_FORK)` returned −1 for each, since `RLIMIT_RTPRIO` is 0. Seven fell back to RealtimeKit, and their threads stayed `SCHED_OTHER`; four targets had already exited. No `SCHED_RR` thread existed at any stage, monitored or not, so `RLIMIT_RTTIME` never applied, and the demotion passes had nothing to demote. Where RealtimeKit grants, the host's IPC receive threads would run at `SCHED_RR` priority 5 inside the Go process. That was not observed.
- **2.54.2 priority.** There were no policy changes, and nice levels did not move: RealtimeKit denied all 11 high-priority requests. The utilization floor did apply: `uclamp.min` 1024 on the IPC `ReceiveQueue` and `PressureMonitor` threads, and 204 on default-QoS queues (`WebsiteDataStore`, `VBlankMonitor`, `ListStore Queue`). Go's threads were unaffected. The power and performance effect was not measured.
- **Limits.** `RLIMIT_RTTIME` was 200000/200000 µs before WebKit and unchanged at every stage, with either runtime. `RLIMIT_RTPRIO` 0, `RLIMIT_NICE` 0 and `RLIMIT_CPU` unlimited also never changed. 2.54.0's tightening path was not run.
- Mesa's `SCHED_BATCH` nice-19 threads (`disk$0`, `traceq0`) appear with both runtimes. They are not WebKit's.

### Proposal for decision

**Recommended: require WebKitGTK ≥ 2.54.1 on Linux.** That is the same supported-runtime requirement the graphics-teardown fix already imposes, with a stated minimum.

The mechanism is a startup check of the public `webkit_get_major/minor/micro_version`, refusing older runtimes. Optionally, the host would also audit, detection only, that SIGXCPU is still Go's after the first window loads, and fail loudly if not. That catches distribution backports or patches. It involves no handler patch, wrapper or signal mask.

Consequences on 2.54.1 and later, as observed with 2.54.2:

- **Go and application SIGXCPU** keep their meaning. `os/signal` subscribers receive both process-directed signals and signals delivered to native threads. Without a subscriber, Go's signal table treats SIGXCPU as notify-only, so it is ignored, not fatal.
- **WebKit's demotion path** no longer exists. There are no real-time threads, so `RLIMIT_RTTIME` never sends WebKit a SIGXCPU. Instead WebKit sets nice levels, when RealtimeKit grants them, and DVFS utilization floors on its own threads.
- **Native-thread delivery:** a SIGXCPU sent to a WebKit or GLib thread runs Go's handler and reaches the subscriber (observed with `tgkill`).

Caveat: the 2.54.2 evidence is Debian binaries on an Ubuntu 26.04 base. A supported runtime must be validated, and Ubuntu's eventual 2.54 package could differ.

**If 2.52.x had to be supported, nothing is clean.** None of the following is implemented or recommended:

- **Accept WebKit's ownership.** Application subscribers silently never fire (observed). A process-directed SIGXCPU, for example from `RLIMIT_CPU`, can land on any thread. On a Go thread running on a goroutine stack, WebKit's handler, which lacks `SA_ONSTACK`, would push its signal frame there. That is the same corruption class as the GC suspend handler, at a rate set by external senders rather than the collector. Not provoked here.
- **Restore Go's handler after WebKit installs its own.** WebKit's demotion is lost. Where RealtimeKit grants `SCHED_RR`, a real-time IPC thread that exceeds the 160 ms soft limit is never demoted, and the hard limit then kills the whole process with SIGKILL. This would be safe only with promotion also disabled through the private `RealTimeThreads::setEnabled(false)`: two private manipulations of another library.
- **Forward from Go's handler to WebKit's.** `os/signal` cannot chain to a handler installed after Go, so this would need a native shim.

### Not covered

- A session where RealtimeKit grants; real `RLIMIT_RTTIME` or `RLIMIT_CPU` expiry and the SIGKILL path. The probes are `tgkill` and `kill`.
- 2.54.0 and 2.54.1 binaries (source only), Ubuntu 2.54 packages, GTK 4 and WPE.
- 2.54.2 with its own matching dependencies.
- SIGXCPU handling in WebKit's own web and network processes, which are not Go.

## Pass B: credential and confinement ([`passes-b-d/`](passes-b-d/))

These passes ran with a host JSC VM: the `js` command's result conversion creates one, as `control-webkit-js` shows. They are browser-policy evidence, not evidence of the VM-free path, which was rerun separately [above](#no-host-vm-investigation).

`feasibility.py` drives `-feasibility` hosts. Each window has its own ephemeral WebKit context. An owned loopback endpoint's credential is 32 random bytes, installed with `webkit_cookie_manager_add_cookie` as a host-only HttpOnly, SameSite=Strict session cookie for `127.0.0.1`, path `/`, before the first navigation. The guard (`dfx2webview2spike/feasibility`) checks the credential, the exact Host and any Origin before the handler, keeps separate admission and handler counters, and strips the credential from the application's cookies. Navigation also waits for the content filter. The collector and the attachment server are this driver's own loopback listeners, on random ports. Tokens never appear in logs.

| Required observation | Evidence |
| --- | --- |
| Document, CSS, JS, image, relative fetch, GET query, POST body authenticated; credential unreadable | `loopback-basics`: 6 admitted, 6 handled; `document.cookie` lacks it |
| Reload and same-origin redirect, no reprovisioning or credential in URLs | `loopback-basics`: one cookie installation across reload and redirect |
| Streaming | `loopback-basics`: chunk 1 arrived, the page acknowledged it, then chunk 2: visible before the handler finished |
| External client without, or with a wrong, credential, or wrong Host or Origin | Refused 403 before the handler: no credential 1, wrong credential 2, Host 1, Origin 1; handled equals admitted. Host and Origin refusals with the right credential come from the host's own selftest, the only place the credential is presented deliberately |
| Cross-port resources, fetch, forms, navigation, popups, iframes, redirects from an allowed URL | Below |
| Two windows, separate credentials | `confinement-*`: A's attempts on B's endpoint never ran B's handler |
| Close during installation, installation failure | `cookie-close-race`: late completions arrive after close, no navigation, ownership drained. `cookie-failure-injected` (injected): visible failure, no navigation, drained |

Collector results, the attempt arriving with the credential except where noted:

| Request class | No enforcement (control) | CSP only | Content filter only | Both |
| --- | --- | --- | --- | --- |
| img, script, stylesheet, font, fetch, XHR, sendBeacon, `<a ping>`, WebSocket | **reached** | blocked | blocked | blocked |
| EventSource; fetch redirected cross-port | **reached**, without credential | blocked | blocked | blocked |
| img redirected cross-port | **reached** | blocked | blocked | blocked |
| Cross-port iframe | refused by navigation policy | blocked | refused by navigation policy | blocked |
| Same-origin iframe | loaded | blocked | blocked | blocked |
| Top-level location and redirected navigation | navigation policy | navigation policy | navigation policy | navigation policy |
| `window.open` | `create` refused | `create` refused | suppressed before `create`; returned null | suppressed |
| Form POST | navigation policy | CSP `form-action`, never proposed | navigation policy | CSP |
| `<link rel=prefetch>` | not observed | not observed | not observed | not observed |

The control proves the probes discriminate: ten requests delivered the credential to another port. The content filter alone blocked every subresource class, so it is the proposed primary mechanism. It is native, compiled per window to "block everything; allow this origin; block child-frame documents". It is applied before the first navigation and is outside page or handler control. CSP alone also blocked every subresource class, but depends on response headers. The guard adds its policy as a separate header, so an application's own CSP can only narrow it. Error pages and redirects are covered because the filter and navigation policy act in the browser, not on a response. **Not tested:** preconnect or DNS prefetch (the collector counts HTTP requests, not bare connections), service workers (CSP `worker-src 'none'`), WebRTC, media elements, `<object>`, manifest, favicons, and CSS `@import`. These are untested, not supported.

In-process (`inprocess-isolation`): both windows have origin `dfx2://spike`, and each context registers its own scheme handler. A page naming the origin reaches only its own window's handler (I2's count unchanged). Neither page can reach the collector, and their storage is separate.

## Pass C: sessions and release ([`passes-b-d/storage/`](passes-b-d/storage/), [`forced-crash/`](forced-crash/))

- **Isolation and lifetime:** windows A and B attach to one origin. B cannot read A's application cookie, local storage or session storage. Reload keeps A's; closing A while B keeps fetching leaves B working; reopened A has a fresh session; B is unchanged; and the attachment server answers an independent client after every browser closes.
- **Cache:** no cache answered a repeated cacheable request (`max-age=600`), by fetch or image, even within one window. B's requests reached the server, so no cross-window reuse was possible to observe. That is weak evidence for cache isolation and a limitation of ephemeral sessions as probed.
- **Processes:** two windows ran two network and two web processes, so each context has its own. After A closed, one of each. Child exits are in each `exit.json`.
- **No profile on disk:** every scenario ran with its own XDG data, cache and config directories. Afterwards they held only fontconfig, Mesa shader, GStreamer registry, sound, PulseAudio and gvfs files, never WebKit website data. Linux temporary storage is in memory.
- **Release and deletion:** compiled content filters are the host's one on-disk resource, under `filters-<pid>` in an experiment-owned root. Each host deletes its store after its WebKit references are released, once per run, and records attempts and time. A post-release deletion failure is portable and injected: `TestRemovalWaitsForReleaseAndNamesResidue` shows the call refusing an unreleased directory, naming persistent residue, and retrying. A killed host leaves its pid-named store; the next host uses a fresh one and neither scans nor deletes another's.

## Pass D: error page ([`passes-b-d/error-pages/`](passes-b-d/error-pages/), [`passes-b-d/owned-load-failure/`](passes-b-d/owned-load-failure/))

- **The page:** `load-failed` for the main frame (other than policy refusals and cancellations) replaces the document with host markup through `webkit_web_view_load_alternate_html`. It has no script, and its two buttons are links to `dfx2-error:retry/<nonce>` and `dfx2-error:start/<nonce>`. The host intercepts those in `decide-policy`, checks the per-failure nonce (a forged nonce is refused), and loads only what it recorded with an ordinary GET, which passes the same origin policy. The markup carries no destination, and the page gains no origin access, popup or frame exception. Retry appears only when the failed request was a GET.
- **Server-counted results** against a stoppable attachment server, closing connections so a stopped server is really unreachable:
  - Failed GET, then Retry once the server returned: one more GET.
  - Failed POST: only Return to start, which is a GET; 0 POSTs reached the server.
  - POST, 303 to a failing GET: Retry offered for the GET; after Retry, still 1 POST and 2 GETs.
  - POST, 307 to a failing target: the failure is the POST, so no Retry; 1 POST each, never replayed.
  - 404 and 500 display as ordinary pages; a failed fetch or image leaves the page; an initial-load failure, then Retry, loads.
- **Owned transports:** natively, a loopback handler that aborts the connection, and an in-process scheme request finished with an error. Neither error page exposes the credential.
- **In-process method:** custom schemes report no HTTP method, so in-process form navigations are treated as POST and get no Retry, including GET forms.
- **Screenshots:** `error-get.png`, `error-post.png`, `error-loopback.png`, `error-inprocess.png`.
- **Manual:** the two buttons were exercised with page-initiated `click()` only. **Michael's manual check of both actions is pending.**

## Shutdown monitoring and regression

`shutdown-{loopback,inprocess}-{ordinary,pending,inspector}` close a window plainly, with `/api/slow` running, and with a docked inspector. Each drains its native requests, native operations and handlers, and every observed WebKit child exits 0. bwrap helpers exit 0 or by SIGKILL, which the observer reports explicitly. The observer now names a child it adopted unsampled from its zombie's `comm` before reaping it. Without that, the first run (`passes-b-d-first`, kept) reported two unidentified SIGKILL exits as child failures. This does not establish the historical crash's cause or remedy.

Shared host change: the idle poster can use another trampoline. The original single-window driver then passed all 14 cases on the same binary, in [`regression-audition/`](regression-audition/).

A native crash found and fixed here: destroying a window's context inside an async completion let the completion's `GTask` later finalize the cookie manager after its context. The fix defers destruction one main-loop iteration after the last native completion; production cleanup needs the same rule.

## Exact commands

```sh
cd experiments/webkitgtk-spike
CGO_ENABLED=0 go build -o /tmp/dfx2-wk-feas .
python3 -B signal_probe.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/signals-reservation --set reservation
python3 -B signal_probe.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/signals \
  --runtime-lib /tmp/dfx2-webkit-runtime/root/usr/lib/x86_64-linux-gnu
python3 -B feasibility.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/passes-b-d
python3 -B feasibility.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/forced-crash --only forced-crash
python3 -B audition.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/regression-audition
python3 -B vmfree.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/vm-free --debug-dir DIR --reserve 60
python3 -B vmfree.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/vm-free/combined-rerun --debug-dir DIR --reserve 60 --only combined
python3 -B xcpu.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/sigxcpu --reserve 60 --debug-dir DIR \
  --runtime /tmp/dfx2-webkit-runtime/root --runtime-debug-dir DIR2
python3 -B xcpu.py /tmp/dfx2-wk-feas evidence/linux/integration-feasibility/sigxcpu/demotion-path --reserve 60 --debug-dir DIR --only control-2.52.6
CGO_ENABLED=0 go build -o /tmp/dfx2-wk-candidate .
python3 -B vmfree.py /tmp/dfx2-wk-candidate evidence/linux/integration-feasibility/closeout/browser --no-monitor --reserve 60
python3 -B vmfree.py /tmp/dfx2-wk-candidate evidence/linux/integration-feasibility/closeout/browser-combined-rerun --no-monitor --reserve 60 --only combined
python3 -B audition.py /tmp/dfx2-wk-candidate evidence/linux/integration-feasibility/closeout/regression-audition
python3 -B manual_check.py /tmp/dfx2-wk-candidate evidence/linux/integration-feasibility/closeout/manual-check --reserve 60
```

`DIR` holds `.build-id/e0/<rest of the JSC build ID>.debug` and `.build-id/97/<rest of the WebKit build ID>.debug` links to the two debuginfod files. `DIR2` holds the same for Debian's 2.54.2 build IDs.

The final `passes-b-d` summary records 17 of 18 passing. Its forced-crash entry failed on a driver bug, the missing second-host directory; the corrected run is `forced-crash/`.
