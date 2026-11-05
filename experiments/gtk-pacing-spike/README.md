# GTK pacing experiment

This standalone, no-cgo experiment asks whether GTK frame-clock pacing can stop rendering a minimized Wayland window without a reliable minimized-state signal. It imports no dfx2 runtime code and changes no production scheduler.

**Decision, 2026-11-05:** keep the stage 1 scheduler. Michael accepts continued rendering while minimized when GTK/Wayland supplies no hidden-state notification, if avoiding it would add significant complexity. The extra native scheduling state and cadence/restoration tradeoffs found here are not justified for this optimization now. Explicit hidden-state suspension on X11 remains unchanged.

## What was tested

Two GTK windows show a Cairo-rendered animated bar. A separate Go worker publishes samples at 30 Hz. A pending frame request coalesces subsequent invalidations; rendering consumes the latest sample. Native callbacks only record readiness or paint the retained image. Simulated view evaluation and image generation happen after `g_main_context_iteration` returns, outside native callbacks.

The modes are:

- `timer`: a 17 ms one-shot animation timer, without a visibility check. This is a negative control for the Wayland symptom, not a copy of dfx2's backend.
- `tick`: a fresh one-shot `gtk_widget_add_tick_callback` request per frame, without explicit visibility checks. This tests replacing timer pacing outright.
- `paced`: retain the animation timer and reported visibility checks, then wait for a GTK tick before rendering. A pending tick prevents further frame timers or repeated producer wake requests until it arrives.
- `continuous`: keep a GTK tick registered while animation or dirty work remains, remove it when settled, and honor reported hidden state. This tests native pacing without the additional deadline timer.

There is one permanent purego callback per signature, with numeric routing tokens. The new tick callback and registration signatures were checked against the host's GTK headers. A 50 ms control timer runs the experiment's timeline; it is test orchestration, not a rendering timer. These measurements cannot establish total process wakeups, CPU consumption, or battery savings.

The driver minimizes the first window, measures an eight-second interval after settling, restores it, pauses both producers/animations, checks three seconds of idle, requests an exposure, resizes static content, and closes a window with work pending while its peer continues. Both windows are closed automatically. Native iconify/restore calls are scripted; these are not manual window-manager auditions.

## Results

Host: GNOME Shell 50.1, Mutter 50.1, GTK 3.24.52, GLib 2.88.0, Cairo 1.18.4, Linux/amd64, Go 1.27.1. X11 means XWayland in this GNOME Wayland session. The only non-1 scale is forced `GDK_SCALE=2`; no real monitor-scale transition was tested. The simple bar is a pacing fixture, not validation of dfx2's text or DPI renderer.

| Mode and host | Minimized frames, about 8 s | Peer frames in that interval | Result |
| --- | ---: | ---: | --- |
| Timer, Wayland | 468 | 466 | Reproduces continuous background rendering |
| One-shot tick, Wayland | 1 | 480 | Strong throttling, not complete suspension |
| One-shot tick, X11 | 2,042 | 2,019 | Native ticks alone do not provide the intended cap or hidden-state gate |
| Deadline + tick, Wayland | 1 | 342 | Restore, latest sample, idle, exposure, resize and close checks pass; strict suspension fails |
| Deadline + tick, Wayland repeat | 1 | 335 | Same result; latest sample lag at restore was one sample |
| Deadline + tick, X11 | 0 | 304 | All driver checks pass |
| Deadline + tick, X11 scale 2 | 0 | 296 | All driver checks pass |
| Continuous tick, X11 | 0 | 4,604 | Hidden state works, but visible animation is uncapped in this probe |
| Continuous tick, X11 scale 2 | 0 | 2,914 | Same rate problem |
| Continuous tick, Wayland | 1 | 481 | One run failed restoration, exposure and static resize checks |

The continuous-tick Wayland run stopped progressing on the first window after restoration was requested, while the peer continued. The experiment does not establish whether this was a scheduling defect or the compositor declining the scripted restoration. Do not generalize that result into a GTK API defect.

For the deadline-plus-tick candidate, both settled windows had zero new frames, ticks, requests, animation timers and samples over the quiet interval. Exposure painted the retained image without evaluating a view. Approximately 240 samples were published during minimization; only the latest was needed on restore. Closing canceled pending native work and left the peer progressing.

That candidate is promising for throttling, but its visible peer ran at roughly 37–43 frames/s in this fixture. The timer-then-native-opportunity ordering can miss presentation opportunities; smoothing the cadence would be additional work. No variant established universal zero-frame minimization on Wayland. Occasional native callbacks still permitted work; their precise compositor cause was not established. We did not add an inactivity heuristic or compositor-specific protocol hooks to force the result.

The driver deliberately retains the strict zero-frame assertion, so Wayland runs exit **1** for that unmet requirement even when the probe process exits 0 and all other checks pass. The runner does not assert a visible frame-rate cap; the table reports that separately. Do not describe these as a passing production validation matrix.

## Evidence and reproduction

Raw JSON events, stderr and summaries are under `evidence/`. `wayland/` is the initial three-second minimized interval, with an idle-check confound from the second window being occluded. `wayland-long/` and later runs use eight seconds and raise the smaller peer after restore so both can remain visible. Early exploratory runs precede the final additional modes; the retained program can repeat the modes using the final timeline. `host.json` identifies the final source and binary used for the last candidate runs.

From this directory:

```sh
CGO_ENABLED=0 go build -o /tmp/dfx2-gtk-pacing .
CGO_ENABLED=0 go vet ./...
python3 drive.py /tmp/dfx2-gtk-pacing --backend wayland --modes timer tick paced continuous --out /tmp/gtk-pacing-wayland
python3 drive.py /tmp/dfx2-gtk-pacing --backend x11 --modes paced continuous --out /tmp/gtk-pacing-x11
python3 drive.py /tmp/dfx2-gtk-pacing --backend x11 --scale 2 --modes paced --out /tmp/gtk-pacing-scale2
```

A desktop session is required. Each mode takes about 23 seconds; the driver bounds each process to 35 seconds and retains failures. No cgo or native headers are required to build. The module pins purego v0.11.1. Build and vet passed on the final probe; production/native dfx2 validation was not rerun because its code was not changed by this experiment.

## If revisited

Preserve explicit hidden-state suspension. Add a narrow private native-frame request/cancel/readiness mechanism only if the background-rendering cost warrants it. Record a request as pending before native calls, coalesce new invalidations behind it, and exclude windows waiting for native readiness from independent frame timers and wake loops. A grant schedules ordinary UI dispatch; it must not call the view from a tick or paint signal handler. Application posts, background sampling, shutdown and other windows must remain independent of frame permission.

Integration would need first-frame, static resize, restore, in-flight invalidation, close-with-request-pending and late-callback tests, plus visible-cadence validation. The probe alone does not validate those invariants in the production runtime.

GTK documents that its [frame clock](https://docs.gtk.org/gdk3/class.FrameClock.html) may stop or slow work when frames are not visible. Its [tick callback](https://docs.gtk.org/gtk3/method.Widget.add_tick_callback.html) provides updates but does not queue a repaint by itself. Those APIs support investigating platform pacing; they do not promise reliable minimized-state notification or zero callbacks on every compositor. The local observations above, not an API guarantee, establish this experiment's results.
