# Linux hands-on audition, 2026-11-03

Michael ran both modes with devtools docked in the main window. His overall observation was that everything seemingly worked fine, except GNOME reported “WebKitWebProcess stopped unexpectedly” when each application closed. The audition covered the checklist of browser checks, typing/Tab/scrolling, resize/minimize/restore during work, headers, reload, navigation, and close during Slow; no separate per-control observations were supplied.

Both retained host logs contain the browser's `checks PASS 14/14` report. Both cancel the pending Slow handler on window close and print zero tracked work. **Both then contain `free(): corrupted unsorted chunks`.** The host's “closed” line is not evidence that WebKit children exited cleanly.

The [crash excerpt](crash-excerpt.txt) is from Apport's report for the in-process run at 11:36:25, package `libwebkit2gtk-4.1-0 2.52.6-0ubuntu0.26.04.1`. It identifies a WebKitWebProcess child, SIGABRT, and allocator corruption detected under `__run_exit_handlers`. Only selected diagnostic fields and stack frames are retained; the full core, process environment and memory are not repository artifacts. The loopback log has the same allocator diagnostic, but its earlier crash report was not separately preserved by Apport.

Michael copied these inspector headers:

Loopback:

```http
HTTP/1.1 200 OK
Cache-Control: no-store
Content-Length: 69
Content-Type: application/json; charset=utf-8
Date: Tue, 03 Nov 2026 16:32:48 GMT
X-Spike-Repeat: one, two
X-Spike-Unicode: cafÃ©
```

In-process:

```http
Cache-Control: no-store
Content-Type: application/json; charset=utf-8
X-Spike-Repeat: one, two
X-Spike-Unicode: cafÃ©
```

These are inspector presentation observations. They do not establish the raw HTTP header-line representation or `Set-Cookie` behavior.

## Follow-up investigation

Eight short runs did **not** reproduce SIGABRT: Go in-process with/without inspector; both Go transports with inspector and all checks; both with inspector, checks and programmatic resize/minimize/restore; and two minimal PyGObject hosts, one requesting inspector attachment. Go runs lasted 4–12 s; the PyGObject runs lasted 4 s and 8 s. This does not reproduce the full several-minute manual interaction sequence or exclude a Go-host bug.

The [followup](followup/) directory retains logs, exit observations and the two scratch diagnostic programs. `reap.py` makes itself a Linux child subreaper so it can observe descendants after the host exits; it writes results to `/tmp/dfx2-webkit-exit-repro`. It is an observational tool, not a pass/fail gate. All host exits were 0; adopted-child exits were 0 or SIGKILL, with no SIGABRT. Child roles were not mapped, so these results should not be described as all children exiting normally. `gi-repro.py` uses system PyGObject, independently of Go/purego; this is a diagnostic comparison, not a dependency of the experiment. Example:

```sh
python3 followup/reap.py gi-docked /usr/bin/python3 followup/gi-repro.py --inspector --attach
python3 followup/reap.py go-checks /tmp/dfx2-webkitgtk-spike -mode inprocess -devtools -autorun checks -quit-after 12s
```

There is a credible upstream lead: WebKit [305909](https://bugs.webkit.org/show_bug.cgi?id=305909) and [315577](https://bugs.webkit.org/show_bug.cgi?id=315577) concern graphics teardown and heap corruption during child-process exit. The [September 24 fix](https://github.com/WebKit/WebKit/commit/e0b1fddaa46813e6b7165037198bb7c34768c1f6) moves display destruction out of process-exit handlers and coordinates it with graphics threads. The upstream `webkitgtk-2.52.6` tag still registers the old `std::atexit` handler in `PlatformDisplay.cpp`, and its `WebProcessGLib.cpp` clears contexts without the newer `destroySharedDisplay` path. The installed Ubuntu package's changelog is dated August 21.

That is **a candidate explanation, not a diagnosis**: this stack shows where heap damage was detected, not who caused it. No patched-runtime comparison was run. No system packages or host teardown code were changed, and neither graphics acceleration nor crash reporting was disabled.

The ordinary `audition.py` waits for the Go host and checks its log immediately. It neither waits for browser descendants nor tests devtools; its original twelve passing cases cannot certify child-process shutdown.
