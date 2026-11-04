# Foundation stage 2: Linux

Host: Linux 7.0.0-42-generic, amd64, glibc 2.43; Go 1.27.1 with module language version 1.26; GTK 3.24.52 and GLib 2.88.0. The desktop session is Wayland; the automated matrix explicitly selects `GDK_BACKEND=wayland`. Host/version/command details are retained in [wayland/summary.json](wayland/summary.json). All Go builds, tests and vet use `CGO_ENABLED=0`.

Current status: Terminus review `bfdf98e8bd7d` returned **clean**, with zero blocking and zero advisory findings and no uncovered files. Stage 2 is ready for Michael's review/commit. Stage 3 has not started.

## Portable and ABI checks

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=60s
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -v ./internal/platform/gtk -run Headers -count=1
CGO_ENABLED=0 go build -o /tmp/dfx2-lifetime ./examples/lifetime
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/dfx2-lifetime.exe ./examples/lifetime
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
```

These checks pass. `TestBindingsMatchHeaders` reports PASS, not SKIP, against the installed GTK 3/GLib/GObject headers under `/usr/include/gtk-3.0/gtk/` and `/usr/include/glib-2.0/`. It compares every bound symbol's argument count and scalar/pointer ABI with the declarations. The backend uses no native structs. Fake-native tests cover concurrent wake coalescing, rearming during dispatch, release sealing, owner-thread rejection and destruction acknowledgment after the native call stack and final reference release. The portable core's existing lifecycle/concurrency tests still pass.

Windows and Darwin commands are compilation/static analysis only. Windows still uses its explicit unsupported backend in this stage. No Windows tests or GUI auditions have executed here.

## Native automated matrix

```sh
python3 evidence/foundation/linux/drive.py /tmp/dfx2-lifetime \
  --out evidence/foundation/linux/wayland --gdk-backend wayland
```

[drive.py](drive.py) bounds each process to 20 seconds, retains JSON event logs and stderr, records actual exits, inspects native mappings, and follows children across all OS threads recursively. Script deadlines remain cooperative; driver termination is only a hang backstop. The matrix passed:

| Case | Result |
| --- | --- |
| Window lifecycle | A closed; B received additional applied title updates; closing B drained all tracked work; exit 0 |
| Resident lifecycle | A/B closed while the app task survived; C reopened, updated, closed and the app explicitly quit; exit 0 |
| Idle, both modes | After window tasks/completions drained, a one-second interval recorded 0 application wakes and 0 wake dispatches; exit 0 |
| Controlled task failure | `Run` preserved `controlled lifetime task failure` after ownership drained; exit 1 as expected |
| Caller cancellation | `Run` reported `context canceled` after cleanup; exit 0 |
| Close during creation | A real native widget was allocated; an internal hook delivered close before `Create` returned; no counter task attached; cleanup drained; exit 0 |
| SIGTERM | Signal delivered after native title updates; cooperative cleanup completed and returned `context canceled`; exit 0 |

Each final `drained` record has zero windows, tasks, queued callbacks, native completions, native windows and retired native references, with terminal state true. This is ownership evidence, not proof about all toolkit resources.

Inspected cases have no WebKit/JavaScriptCore/WebView2 mappings and no browser children. The short creation-close case exits before the driver's process-inspection point, which is explicitly marked `inspected: false`; it is not counted as process-inspection evidence. This host does spawn `/usr/bin/bwrap` wrappers around `/usr/libexec/glycin-loaders/2+/glycin-svg`. The first narrower probe inspected only the main thread and missed those helpers; the retained matrix supersedes it. Helper identities are recorded without copying their environment or full command lines. No environment workaround was added to suppress them.

Without display variables, `GDK_BACKEND=x11 /tmp/dfx2-lifetime -script lifecycle` returns exit 1 with `GTK 3 could not open the display; check DISPLAY/WAYLAND_DISPLAY and the desktop session`. Setup and window creation do not run after that initialization failure.

## Michael's manual audition

Michael ran the requested two commands and reported: **“everything worked well.”**

1. `/tmp/dfx2-lifetime -pause-after 20s`: close A while B updates; resize B; observe pause; close B and confirm exit.
2. `/tmp/dfx2-lifetime -resident`: close A/B; enter `open` and observe a new updating window; enter `quit`.

That report covers the requested window controls, resize, pause, final-window exit and resident reopen/quit. It is separate from the scripted counters. Native X11, display-scale changes, Windows, drawing/text and browser teardown were not auditioned in this stage.

## Backend references and review

Native bindings were grounded in the installed headers. GLib documents [queued idle source dispatch](https://docs.gtk.org/glib/func.idle_add_full.html) and [one-turn main-context iteration](https://docs.gtk.org/glib/method.MainContext.iteration.html); GTK documents [`gtk_init_check` failure semantics](https://docs.gtk.org/gtk3/func.init_check.html). These justify queued worker wakeups, blocking idle dispatch and returning a display error without creating widgets.

Stage 2 review `47f8a4aed1d3` returned `not_clean` with one blocking finding: a priority-0 application source could starve GTK resize/redraw during continuous rearming. Michael accepted `G_PRIORITY_DEFAULT_IDLE` (200), behind GTK resize/redraw priorities 110/120.

`TestDispatchYieldsToNativeResizeAndRedraw` exercises the installed GLib scheduler with continuously rearmed backend dispatch and native-priority probes. Before the fix, 64 iterations dispatched application work 64 times and never ran either probe. After the fix it passed 200 runs. This test needs no display or GTK initialization; it skips explicitly if GLib is unavailable. The portable suite remains usable on hosts without native libraries. The full no-cgo suite, vet and diff checks passed, and the rebuilt example passed all eight native script cases again; the retained `wayland/` logs reflect the corrected priority.

Review `bfdf98e8bd7d` returned `clean` after the priority fix and its validation, superseding the pending review status. Logs are under `/home/michael/.local/share/terminus/dfx2/bfdf98e8bd7d/` on this host. All stage 2 changes remain in the working tree for Michael; no commit or push was made by the agent. Windows implementation and actual Windows execution remain stage 3, using the operator-mediated host handoff in the work order.
