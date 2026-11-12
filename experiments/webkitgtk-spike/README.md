# Linux WebKitGTK spike

An experiment in hosting the system webview from Go with **CGO_ENABLED=0**, through purego, with two transports into the same `http.Handler`:

- `loopback`: `net/http` on a random `127.0.0.1` port.
- `inprocess`: WebKit's registered `dfx2://spike/` URI scheme, with no application listener. This means one Go application process; WebKit still uses its own web/network processes.

This is a single-window experiment, not the dfx2 API. The sibling `../webview2-spike` module supplies the portable adapter, dispatcher, and fixture unchanged, through a local module replacement. The Linux wrapper changes only the fixture's labels and mode detection. Keep both experiments in the checkout. Neither the Windows host nor the native/text spike changes.

## Build and run

Requires Linux, a graphical session, GTK 3, WebKitGTK **4.1** (the Soup 3 variant), and WebKit **2.40 or newer** for request-body access. The tested host has WebKitGTK 2.52.6, GTK 3.24.52 and Soup 3.6.6. Development headers are not needed to build or run. Libraries are loaded at runtime; missing libraries or symbols produce an error. Only linux/amd64 was executed.

From this directory:

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-webkitgtk-spike .
/tmp/dfx2-webkitgtk-spike -mode inprocess -devtools
/tmp/dfx2-webkitgtk-spike -mode loopback -devtools
```

The page has the Windows experiment's controls: run all checks, slow/fast, abort, reload during slow, body limits, new windows, outside navigation, and editable fields. Close the window, Ctrl+C, or SIGTERM for normal teardown. Profiles are ephemeral. New windows and navigation outside the selected origin are refused; this is not a general sandbox for untrusted pages or external subresources.

The ordinary host still prints JavaScriptCore's warning about replacing Go's SIGUSR1 handler, and leaves the collision visible. The integration feasibility probes below examine it: JSC also replaces Go's SIGSEGV and SIGBUS handlers, so the GC signal is not the whole problem. Do not fix either by hard-coding a signal or suppressing stderr.

## Checks and evidence

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test dfx2webview2spike/adapt dfx2webview2spike/fixture
python3 -B -m unittest -v test_exitwatch
python3 -B audition.py /tmp/dfx2-webkitgtk-spike /tmp/webkit-audition
```

The shared fixture tests need local sockets. The audition needs the desktop, Python 3, `ss`, `pkg-config`, and Go. It opens real windows sequentially, writes logs and WebKit-rendered PNGs, and fails on missing observations, wrong exit codes, GLib criticals or browser-check failures. Its Linux child subreaper also waits after host exit, records adopted-child statuses, and rejects observed browser-child failures and late allocator diagnostics. The PNGs show web content, not window-manager decorations or proof of minimize behavior.

Recorded evidence lives in [`evidence/linux/`](evidence/linux/). Both modes pass **14/14** checks. The driver covers reload, close with a handler pending, SIGTERM with a handler pending, close during startup, and listener failure. The in-process-only `-close-reading` case closes with a real Gio body read pending, to exercise asynchronous cancellation and reference cleanup. `-resize-after` requests a resize, minimize and restore; a compositor may ignore those requests. Final screenshots were visually inspected.

The logs distinguish the lifetime contracts: abort/reload cancels loopback handlers; the in-process handler keeps running. Closing cancels both. Every recorded case drains tracked native requests, async operations, and handler workers. These counters are ownership checks, not a leak-detector result or proof of clean browser-child exit. The original driver waited only for the Go host; the current driver adds descendant observation and docked-inspector cases. Its fourteen-case results are in [`evidence/linux/shutdown/gate/`](evidence/linux/shutdown/gate/).

The loopback listener is visible in `ss`; the in-process host owns no TCP listener. Browser-child sockets were not surveyed. Michael subsequently auditioned interaction and inspector presentation: everything seemingly worked, but **both modes produced a WebKitWebProcess heap-corruption crash on exit with a docked inspector**. See the [manual evidence](evidence/linux/manual/README.md). The [shutdown investigation](evidence/linux/shutdown/README.md) symbolized the original core and found a strong match for WebKit's fixed graphics-exit race. Neither the installed 2.52.6 nor isolated 2.54.2 libraries crashed in the repeat manual comparison, so the remedy remains unconfirmed. Default host teardown is unchanged. Mixed DPI, X11, multiple windows and crash recovery remain unauditioned.

For focused exit investigations, use `exitwatch.py` around the executable. `-dock-devtools` opens and docks the inspector; titles include the runtime version. `-exercise-exit` repeats page checks and synthetic focus/scroll/resize work. `-window-close` makes timed exit go through GTK's delete event, and `-close-inspector` explicitly closes devtools before destroying the window as a diagnostic comparison only. Commands and observer limitations are in the investigation report.

## Transport and lifetime

The scheme is registered as secure and CORS-enabled, without granting local or universal access. On this runtime it has origin `dfx2://spike`, secure context APIs, crypto, relative fetch, and session storage. That observation does not establish cookie, service-worker, redirect or authentication parity with HTTPS. Streaming, SSE and WebSockets are outside this buffered adapter.

The shared adapter caps request and response bodies at 1 MiB. Native request reads are asynchronous 64 KiB chunks; an oversized request is stopped at most one chunk beyond the limit, then receives 413 without running the handler. An oversized response becomes 502. There is no aggregate concurrency or memory limit: this is a trusted fixture, not production resource policy.

One locked GTK thread owns native objects. Four permanent purego trampolines route by signature and numeric userdata; none are allocated per request. Workers receive Go copies only. `g_idle_add_full` queues completions onto the main context; `g_main_context_invoke` could instead run on the worker. Native body reads and diagnostic JavaScript/snapshots use async tokens.

Close cancels the window context, closes the dispatcher and poster, cancels Gio operations, settles dispatched requests, and hides the window. The main loop keeps pumping until native async callbacks have finished and released their objects, then destroys the window. Worker/server draining follows. There is no timeout on native cancellation callbacks; a stuck runtime could hold shutdown open. All recorded cancellations completed promptly.

Request-body streams are owned references. Response headers transfer full ownership to `WebKitURISchemeResponse`. `g_bytes_new` copies Go response bytes; native streams never retain Go memory. The declarations were checked against the installed WebKit2-4.1 GIR and GTK/Gio/Soup headers. See WebKit's [URI-scheme registration documentation](https://webkitgtk.org/reference/webkit2gtk/stable/method.WebContext.register_uri_scheme.html) for the asynchronous request retention contract.

The experiment follows the Windows fixture's standard-library logging, so the evidence format remains comparable. This is not a production logging choice for dfx2.

## Integration feasibility probes

The Linux half of [the feasibility spike](../../docs/future/webview-feasibility-spike.md), added beside the ordinary host and inactive unless asked for. The [result](evidence/linux/integration-feasibility/README.md) records what was demonstrated, how, and what remains untested.

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-wk-feas .
python3 -B signal_probe.py /tmp/dfx2-wk-feas OUT/signals [--runtime-lib DIR]
python3 -B feasibility.py /tmp/dfx2-wk-feas OUT/passes-b-d [--only SCENARIO ...]
python3 -B vmfree.py /tmp/dfx2-wk-feas OUT/vm-free --debug-dir DIR --reserve N [--only NAME ...]
python3 -B xcpu.py /tmp/dfx2-wk-feas OUT/sigxcpu --reserve N --debug-dir DIR [--runtime ROOT --runtime-debug-dir DIR2]
python3 -B vmfree.py /tmp/dfx2-wk-feas OUT/browser --no-monitor --reserve N   # closeout: browser cases only, no gdb
python3 -B manual_check.py /tmp/dfx2-wk-feas OUT/manual-check --reserve N     # waits for a person to click
```

The closeout candidate and its results are in the record's [status](evidence/linux/integration-feasibility/README.md#status-2026-11-12-linux-closeout). `manual_check.py` sets up a failed GET and a failed POST in one attached window, and waits for a person to click Retry, then Return to start. It records the server's counts, never clicks (except under `--self-test`, which checks the script and is not the manual result).

| Flag | Meaning |
| --- | --- |
| `-signal-probe VARIANT` | One disposable pass-A process: `baseline`, `configured`, `configured-onstack` (the proposed order), `configured-onstack-all` (a hazard probe), `late`, `env` (honours `JSC_SIGNAL_FOR_GC`), `no-symbol` or `no-signal` (injected discovery results). |
| `-signal-fault segv\|bus` | After JSC initialization, recover a Go fault on a goroutine. |
| `-signal-webkit`, `-signal-order jsc-first\|webkit-first` | Also bring up GTK and a WebKit page, after or before JSC's forced initialization. |
| `-signal-js-threads 2` | Also run JavaScript on two Go threads in one context group. |
| `-signal-probe hidden-subscriber\|hidden-subscriber-jsc` | The inferred selector's flaw: an undeclared `signal.Notify` subscriber on the signal it picks. |
| `-signal-probe suspension-transition -signal-reserve N` | Handler safety: JavaScript, pure Go and C phases on one locked thread, to see where JSC's GC suspension lands. |
| `-signal-probe narrow\|narrow-foreign -signal-reserve N` | The narrow, identity-verified SIGSEGV/SIGBUS adjustment (and an injected identity failure); with `-signal-webkit`, it also records SIGXCPU and real-time threads. Run everything with `handler_safety.py BINARY OUT --reserve N [--debug-file JSC_DEBUG]`. |
| `-signal-probe reserved… -signal-reserve N` | The explicit-reservation prototype (`reserved`, `reserved-violate-before`, `reserved-violate-after`, injected `reserved-foreign`). Run them all with `signal_probe.py … --set reservation`. |
| `-feasibility` | Passes B–D: a multi-window host driven by console commands from `feasibility.py` (`open LABEL loopback\|inprocess\|attach [URL]`, `js`, `load`, `close`, `snapshot`, `selftest`, `inspect`, `status`, `quit`); JSON events on stdout. |
| `-feas-filter=false`, `-feas-csp=false`, `-feas-cookie=false` | Switch off the native content filter, the endpoint CSP or the credential, to attribute enforcement and run the no-enforcement control. |
| `-feas-cookie-fail` | Injected credential-installation failure. |
| `-feas-root DIR` | Experiment-owned directory for compiled content filters (default `$TMPDIR/dfx2-feasibility`). |
| `-feas-vmfree [-signal-reserve N]` | The no-host-VM path: binds no JavaScript evaluation or result conversion and refuses `js`. Adds `reload`, `click LABEL X Y` (synthesized GTK pointer events) and signal records. With `-signal-reserve`, it validates the application's reservation and configures JSC's GC signal before WebKit initializes JSC. Automation is page-driven: `load LABEL URL?run=NAME&id=ID`, with the page posting results to its own handler's `/api/report`. |
| `-feas-xcpu` | With `-feas-vmfree`: subscribes to SIGXCPU through `os/signal` before WebKit starts, records thread scheduling (policy, nice, `uclamp.min`) and limits, and adds `xcpu-native` (`tgkill` to a native thread) and `xcpu-wait` (arm for a process-directed signal from outside), each reporting whether the Go subscriber received it. `xcpu.py` drives it against the installed runtime and, optionally, an isolated one. |

`vmfree.py` runs every host under gdb with [`vmwatch_gdb.py`](vmwatch_gdb.py). The monitor logs JSC VM construction, context creation, JSLock acquisition, GC thread registration, thread suspension, fault-handler finalization, real-time thread registration and native `sigaction` calls, in the Go host only. It needs a directory of `.build-id` links to Ubuntu's debuginfod files for the installed `libjavascriptcoregtk` (and, for readable frames, `libwebkit2gtk`), kept outside git. Two positive controls run first, each in its own process.

Each feasibility window has its own ephemeral WebKit context. Owned loopback windows receive a random endpoint credential through the native cookie manager before their first navigation, and their endpoint admits only requests carrying it ([`dfx2webview2spike/feasibility`](../webview2-spike/feasibility/)). A failed top-level load shows a host-local page whose Retry and Return to start links are intercepted natively. `feasibility.py` runs its own collector and stoppable attachment server, isolates each host's XDG data, cache and config directories, and observes browser children with `exitwatch.py`, which now also names children it adopted unsampled.

