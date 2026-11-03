# Shutdown investigation, 2026-11-03

**The original crash strongly matches WebKit's graphics teardown race. It is intermittent, and a runtime replacement has not been proved to cure it here.** Both the installed 2.52.6 and comparison 2.54.2 passed Michael's repeat manual audition. The normal host close order is unchanged.

## Evidence from the original crash

The [symbolized frames](core-graphics-threads.txt) come from the original 11:36:25 WebKitWebProcess crash, not a new reproduction. Exact-build-id symbols were fetched from Ubuntu's debuginfod service:

- WebKit: `9788492834087d9d0cdd85171ee0da0e43699eb1`.
- Mesa EGL: `37b258bf6e6801c1214cbc6bd6c26a17b731e7af`.

The main thread detects allocator corruption while `__run_exit_handlers` frees an exit-handler block. Concurrently, another thread is running:

```text
__call_tls_dtors
  → ~SkiaGLContext
  → ~GLContext
  → eglDestroyContext
  → _eglLockDisplay / futex wait
```

This is much stronger evidence than an allocator error alone. It matches the mechanism described and changed by WebKit's [graphics teardown fix e0b1fdd](https://github.com/WebKit/WebKit/commit/e0b1fddaa46813e6b7165037198bb7c34768c1f6), associated with [305909](https://bugs.webkit.org/show_bug.cgi?id=305909) and [315577](https://bugs.webkit.org/show_bug.cgi?id=315577). The fix moves display destruction out of exit handlers and coordinates context destruction with display termination. The `webkitgtk-2.52.6` source has the old path; the `webkitgtk-2.54.2` source includes `destroySharedDisplay` and the new lifetime handling. A core captures the resulting state, not the earlier corrupting write, so this remains a strong diagnosis rather than a causally isolated proof.

The 6 GB extracted core and debug files remain under `/tmp/dfx2-webkit-core`, outside the repository. The retained text suppresses frame arguments; it does not include the core, environment, or application memory.

## Comparisons

| Run | Observation |
| --- | --- |
| Installed 2.52.6, in-process, docked inspector, 90 seconds | Twelve rounds of 14/14 page checks, programmatic focus/scroll/resize; host and observed WebKit children exited 0. |
| Installed 2.52.6, loopback, docked inspector, 70 seconds | Same workload, then a slow request and GTK `delete-event` close; no child crash. A targeted Ctrl+Alt+4 diagnostic returned false, so this run does **not** prove selection of the Network tab. That unsuccessful key-injection code was removed. |
| Comparison 2.54.2 libraries, in-process, 10-second smoke check | 14/14 checks, docked inspector, no child crash. |
| Michael, installed 2.52.6, loopback | “Neither version crashes”; approximately 296 seconds from launch to close. Host and both observed web processes exited 0. |
| Michael, comparison 2.54.2, loopback | Same report; approximately 206 seconds. Host and both observed web processes exited 0. |
| Inspector-first diagnostic, installed 2.52.6, in-process | Explicit inspector close before window destruction with a slow request pending; no child crash. This is **not** an established workaround and is not the default. |

The manual comparison used identical Go application logic and docked inspectors. Titles identified the loaded runtime version. Logs and observer JSON are alongside this file. Because the old version also passed, these results cannot measure a fix's effectiveness or a failure rate.

### Isolated comparison libraries

No system packages were installed or changed. Debian 2.54.2-2 WebKit and JavaScriptCore packages, plus their missing dependencies, were downloaded, SHA-256 checked, and extracted under `/tmp/dfx2-webkit-runtime/root`. The [manifest](runtime-manifest.json) records package paths, versions and hashes. The WebKit/JSC hashes came from Debian's HTTPS package pages; the remaining hashes came from its HTTPS package index.

The working comparison used:

```sh
env LD_LIBRARY_PATH=/tmp/dfx2-webkit-runtime/root/usr/lib/x86_64-linux-gnu \
  WEBKIT_INJECTED_BUNDLE_PATH=/tmp/dfx2-webkit-runtime/root/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1/injected-bundle \
  /tmp/dfx2-webkitgtk-investigate -mode loopback -dock-devtools
```

The observer's `/proc` library paths verify that the host and WebKit children loaded the new WebKit and JavaScriptCore libraries. The executable launchers remained the system's small WebKit helper stubs, which call the process entry point exported by the loaded library. This is a library-level diagnostic comparison using mixed distribution packages, **not a deployment recipe**. `WEBKIT_EXEC_PATH` was not honored by the release build and is not needed in the command above.

### Excluded diagnostic failures

- An attempt to evaluate JavaScript in the inspector was invalid: its public accessor returns `WebKitWebViewBase`, not `WebKitWebView`. WebKit rejected the call; the diagnostic left an async token pending until its watchdog killed the host. That run says nothing about the original crash. The invalid call and its binding were removed.
- A separate attempt to replace the helper directory in a bubblewrap mount namespace failed during WebContext creation, before a page loaded. It was discarded. The working manual comparison used the library setup above.

Neither failed probe is counted as a reproduction or a runtime regression.

## Improved audition

`exitwatch.py` makes the test supervisor a Linux child subreaper. It samples descendant identities and WebKit/JSC library paths, waits after the host exits, and records adopted-child wait statuses. It fails on abnormal host exit, observed child failure, lingering children, or late fatal diagnostics. SIGKILL is allowed only for processes identified as `bwrap` helpers; their statuses are still recorded. Already-reaped descendants cannot have their wait status recovered, so the output distinguishes observations from exit records. This is stronger coverage, not a proof against every child failure.

The ordinary driver now uses this observer and adds a docked-inspector busy close in each transport. Its fourteen cases and retained JSON are in [`gate/`](gate/). The added unit tests prove that a successful host followed by a failed orphan, an unexplained SIGKILL, or a late allocator diagnostic fails the supervisor; a successful orphan drains normally.

Example focused reproduction, from the experiment directory:

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-webkitgtk-investigate .
python3 exitwatch.py --output /tmp/exit-study --timeout 120 -- \
  /tmp/dfx2-webkitgtk-investigate -mode inprocess -dock-devtools \
  -exercise-exit -window-close -quit-after 90s
```

`-exercise-exit` performs twelve browser-check rounds, DOM focus/value/scroll changes, and twenty-four resize requests, then starts a slow fetch one second before timed close. These are synthetic interactions, not trusted keyboard input or a substitute for Michael's audition. `-window-close` routes timed close through GTK's delete event. `-close-inspector` opts into the alternative close-order probe. None changes the default lifecycle contract.

## Disposition

Keep the shared-handler design evidence, but carry a runtime prerequisite: production Linux webview validation must include a runtime with the relevant graphics-teardown fixes, and child exits must be observed. Do not add sleep, disable rendering, suppress the crash, or change handler cancellation to hide this race. The JSC SIGUSR1 collision is separate and still unresolved.

The runtime lead is substantially narrowed; the remedy remains unconfirmed on this host because the old runtime did not fail during comparison. The roadmap item stays open for validating a supported runtime or backport and repeating the full manual sequence under the exit observer.
