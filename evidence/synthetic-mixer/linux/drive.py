#!/usr/bin/env python3
"""Run bounded Linux native auditions of examples/mixer and retain logs, exits,
retained-image snapshots and process facts. Waits are compositor pacing and
hang backstops; ownership and idle results come from the example's own
synchronized counters. Requires Python 3 and Pillow for PNG validation."""
import argparse
import json
import os
from pathlib import Path
import platform
import queue
import signal
import subprocess
import threading
import time

from PIL import Image

OWNERSHIP = ("windows", "tasks", "native_completions", "queued", "native_windows", "native_retired", "dispatch_targets")


def command(*args):
    return subprocess.run(args, capture_output=True, text=True, check=True).stdout.strip()


def child_processes(pid):
    """Children of every thread, recursively; identities only, never env."""
    found = {}
    pending = [str(pid)]
    while pending:
        parent = pending.pop()
        for task_children in Path(f"/proc/{parent}/task").glob("*/children"):
            try:
                ids = task_children.read_text().split()
            except FileNotFoundError:
                continue
            for child in ids:
                if child in found:
                    continue
                try:
                    base = Path(f"/proc/{child}")
                    argv = base.joinpath("cmdline").read_bytes().decode().split("\0")
                    payload = next((a for a in argv if a.startswith("/usr/libexec/glycin-loaders/")), "")
                    found[child] = dict(pid=child, comm=base.joinpath("comm").read_text().strip(),
                                        exe=str(base.joinpath("exe").resolve()), loader=payload)
                    pending.append(child)
                except FileNotFoundError:
                    pass
    return list(found.values())


def run(binary, out, name, flags, env, signal_after=None):
    lines, events, incoming = [], [], queue.Queue()
    maps, children, inspected, signaled, timed_out = [], [], False, False, False
    with (out / (name + ".stderr.log")).open("w") as stderr:
        proc = subprocess.Popen([binary, *flags], stdout=subprocess.PIPE, stderr=stderr, text=True, env=env)

        def read():
            for line in proc.stdout:
                incoming.put(line)
            incoming.put(None)
        threading.Thread(target=read, daemon=True).start()
        deadline = time.monotonic() + 40
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                timed_out = True
                proc.kill()
                break
            try:
                line = incoming.get(timeout=remaining)
            except queue.Empty:
                timed_out = True
                proc.kill()
                break
            if line is None:
                break
            lines.append(line)
            event = json.loads(line)
            events.append(event)
            if event["event"] in ("animating", "two-running", "before-minimize", "reopen-cycle", "resized", "sampling", "large-size", "exposure", "font-coverage") and not inspected:
                maps = sorted(set(m.split()[-1] for m in Path(f"/proc/{proc.pid}/maps").read_text().splitlines() if "/" in m))
                children = child_processes(proc.pid)
                inspected = True
            if signal_after and not signaled and event["event"] == signal_after:
                proc.send_signal(signal.SIGTERM)
                signaled = True
        try:
            code = proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            timed_out = True
            proc.kill()
            code = proc.wait()
        proc.stdout.close()
    (out / (name + ".jsonl")).write_text("".join(lines))
    (out / (name + ".maps.txt")).write_text("\n".join(maps) + "\n")
    return events, code, timed_out, inspected, maps, children, signaled


def by(events, name):
    return [e for e in events if e["event"] == name]


def size_of(surface):
    dims = surface.split("@")[0].split("x")
    return int(dims[0]), int(dims[1])


# The spike's black windows were frames of 1.9-3.0 MB.
LARGE_FAILURE_BYTES = 3_000_000


def large_size_failures(steps):
    """Sizes the host reached. It may clamp a request, so requests are not compared."""
    if len(steps) != 5:
        return [f"{len(steps)} large image steps, not 5"]
    sizes = [e["bytes"] for e in steps]
    failures = []
    if any(later < earlier for earlier, later in zip(sizes, sizes[1:])):
        failures.append(f"large surface shrank: {sizes}")
    if sizes[-1] <= sizes[0]:
        failures.append(f"missing coverage: large surface did not grow: {sizes}")
    if max(sizes) <= LARGE_FAILURE_BYTES:
        failures.append(f"missing coverage: largest surface {max(sizes)} bytes is not beyond the spike's failure sizes")
    return failures


def png_size(path):
    """Check chunk integrity and decode pixels before accepting a snapshot.
    Pillow's verify does not decode IDAT, and its decoder alone can overlook
    chunk CRCs. Both checks are required. Keep truncated-image loading disabled.
    """
    with open(path, "rb") as f:
        # verify stops at the IEND header without checking its CRC. Our
        # snapshots must end with the complete, zero-length IEND chunk.
        f.seek(-12, os.SEEK_END)
        if f.read() != b"\0\0\0\0IEND\xae\x42\x60\x82":
            raise ValueError("missing or incomplete PNG end chunk")
        f.seek(0)
        try:
            with Image.open(f) as image:
                if image.format != "PNG":
                    raise ValueError("not a PNG")
                image.verify()
            f.seek(0)
            with Image.open(f) as image:
                image.load()
                return image.size
        except (SyntaxError, Image.DecompressionBombError) as err:
            raise ValueError(f"invalid PNG: {err}") from err


def large_snapshot_failures(steps, snapshots):
    """Each step's retained image, written during this run, has the size of the
    physical surface observed at that step. The event is required because a file
    alone could be left over from an earlier run."""
    written = {Path(e["path"]).name: e["path"] for e in snapshots}
    failures = []
    for e in steps:
        where = f'large step {e["requested"]}'
        path = written.get(e.get("snapshot"))
        if path is None:
            failures.append(f"{where}: no snapshot written")
            continue
        try:
            got = png_size(path)
        except (OSError, ValueError) as err:
            failures.append(f"{where}: unreadable snapshot: {err}")
            continue
        if got != size_of(e["first_surface"]):
            failures.append(f'{where}: snapshot {got[0]}x{got[1]} does not match surface {e["first_surface"]}')
    return failures


def large_failures(steps, snapshots):
    return large_size_failures(steps) + large_snapshot_failures(steps, snapshots)


def audition(binary, out, name, flags, env, scale, signal_after=None):
    events, code, timed_out, inspected, maps, children, signaled = run(binary, out, name, flags, env, signal_after)
    failures, observations = [], {}
    if code != 0 or timed_out:
        failures.append(f"exit={code}, timed_out={timed_out}")
    drained = by(events, "drained")
    if len(drained) != 1:
        failures.append(f"{len(drained)} drained events")
    elif any(drained[-1][k] != 0 for k in OWNERSHIP) or not drained[-1]["terminal"] or drained[-1]["release_pending"]:
        failures.append(f"ownership did not drain: {drained[-1]}")
    for quiet in by(events, "quiet"):
        observations["quiet"] = quiet
        # Framework frames, deadline timers, wakes and presents must not grow;
        # OS exposure paints may.
        if any(quiet[k] != 0 for k in ("frames", "frame_timers", "wakes", "dispatches", "presents", "animating")):
            failures.append(f"settled window not quiet: {quiet}")
    for e in by(events, "animating") + by(events, "two-running"):
        # Core and native counters are sampled separately: a frame in flight
        # may be counted before its present.
        if e["frames"] < 10 or not e["frames"] - len(by(events, "opened")) <= e["presents"] <= e["frames"]:
            failures.append(f"animation frames/presents: {e}")
    if name.startswith("resize"):
        resized = by(events, "resized")
        observations["resized"] = [(e["requested"], e["first_surface"]) for e in resized]
        if len(resized) != 4:
            failures.append("resize steps missing")
        for e in resized:
            want = [int(v) * scale for v in e["requested"].split("x")]
            if list(size_of(e["first_surface"])) != want:
                failures.append(f"surface {e['first_surface']} for requested {e['requested']} at scale {scale}")
    if name.startswith("minimize"):
        before, during, later, restored = (by(events, k) for k in ("before-minimize", "minimized", "minimized-later", "restored"))
        if before and during and later and restored:
            observations["minimize"] = dict(
                visible_while_minimized=during[-1]["first_surface"], frames_while_minimized=later[-1]["first_frames"] - during[-1]["first_frames"],
                timers_while_minimized=later[-1]["frame_timers"] - during[-1]["frame_timers"],
                paints_while_minimized=later[-1]["paints"] - during[-1]["paints"], restored=restored[-1]["first_surface"])
            # X11 reports iconified state, so suspension is required there; a
            # Wayland session that never reports it is recorded, not failed.
            m = observations["minimize"]
            if env.get("GDK_BACKEND") == "x11" and (m["frames_while_minimized"] != 0 or m["timers_while_minimized"] != 0
                                                     or "visible=false" not in m["visible_while_minimized"]
                                                     or "visible=true" not in m["restored"]
                                                     or restored[-1]["first_frames"] <= later[-1]["first_frames"]):
                failures.append(f"iconify did not suspend and restore: {m}")
        else:
            failures.append("minimize steps missing")
    if name.startswith("close-sample"):
        sample = by(events, "close-sample")
        observations["close-sample"] = sample[-1] if sample else None
        if not sample or sample[-1]["refused_after_close"] == 0 or sample[-1]["accepted"] == 0:
            failures.append("close did not race live invalidation")
    if name.startswith("reopen") and len(by(events, "reopen-cycle")) != 5:
        failures.append("reopen cycles missing")
    if name.startswith("two"):
        one = by(events, "one-running")
        if not one or one[-1]["frames_after_close"] == 0:
            failures.append("remaining window stopped after the other closed")
    if name.startswith("expose"):
        exposure = by(events, "exposure")
        observations["exposure"] = exposure[-1] if exposure else None
        if not exposure or exposure[-1]["frames"] != 0 or exposure[-1]["presents"] != 0 or exposure[-1]["paints"] == 0:
            failures.append("exposure did not repaint the retained image alone")
    if name.startswith("large"):
        steps = by(events, "large-size")
        observations["large"] = [(e["requested"], e["first_surface"], e["bytes"]) for e in steps]
        failures += large_failures(steps, by(events, "snapshot"))
    if name.startswith("fonts"):
        observations["fonts"] = {e["sample"]: e["faces"] for e in by(events, "font-coverage")}
        if len(observations["fonts"]) != 7:
            failures.append("font samples missing")
    if signal_after:
        returned = by(events, "return")
        if not signaled or not returned or "context canceled" not in returned[-1]["error"]:
            failures.append("SIGTERM cancellation not observed")
    snapshots = [e["path"] for e in by(events, "snapshot")]
    missing = [p for p in snapshots if not Path(p).is_file()]
    if missing:
        failures.append(f"missing snapshots {missing}")
    forbidden = [m for m in maps if any(x in m.lower() for x in ("webkit", "javascriptcore", "webview2"))]
    browser = [c for c in children if any(x in (c["comm"] + " " + c["exe"]).lower() for x in ("webkit", "webview", "jsc", "chrome", "chromium"))]
    unknown = [c for c in children if not c["loader"] and not c["comm"].startswith("glycin-")]
    if forbidden or browser or unknown:
        failures.append(f"forbidden={forbidden} browser={browser} unclassified={unknown}")
    if not inspected:
        failures.append("process inspection did not run")
    result = dict(name=name, command=[binary, *flags], exit_code=code, timed_out=timed_out, inspected=inspected,
                  cairo_loaded=any("libcairo" in m for m in maps), children=children, snapshots=[Path(p).name for p in snapshots],
                  observations=observations, events=len(events), failures=failures)
    print(json.dumps(result), flush=True)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--gdk-backend", choices=("wayland", "x11"), default="wayland")
    parser.add_argument("--gdk-scale", type=int, default=0, help="force GTK's integer scale (X11 only); 0 leaves the session's")
    parser.add_argument("--cases", default="animate,resize,minimize,close-sample,reopen,two,expose,large,fonts,signal")
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    snapshots = args.out / "snapshots"
    snapshots.mkdir(exist_ok=True)
    env = dict(os.environ, GDK_BACKEND=args.gdk_backend)
    if args.gdk_scale:
        env["GDK_SCALE"] = str(args.gdk_scale)
    scale = args.gdk_scale or 1
    host = dict(os=platform.platform(), architecture=platform.machine(), go=command("go", "version"),
                gtk=command("pkg-config", "--modversion", "gtk+-3.0"), cairo=command("pkg-config", "--modversion", "cairo"),
                session=os.environ.get("XDG_SESSION_TYPE"), gdk_backend=args.gdk_backend, gdk_scale=args.gdk_scale or None,
                wayland_display=os.environ.get("WAYLAND_DISPLAY"), display=os.environ.get("DISPLAY"),
                date=command("date", "--iso-8601=seconds"))
    binary = str(Path(args.binary).resolve())
    snap = ["-snapshots", str(snapshots.resolve())]
    cases = dict(animate=(["-script", "animate", *snap], None), resize=(["-script", "resize", *snap], None),
                 minimize=(["-script", "minimize"], None), **{"close-sample": (["-script", "close-sample"], None)},
                 reopen=(["-script", "reopen"], None), two=(["-script", "two"], None),
                 expose=(["-script", "expose"], None), large=(["-script", "large", *snap], None), fonts=(["-script", "fonts"], None),
                 signal=(["-script", "animate"], "animating"))
    results = [audition(binary, args.out, name, cases[name][0], env, scale, cases[name][1]) for name in args.cases.split(",")]
    (args.out / "summary.json").write_text(json.dumps(dict(host=host, results=results), indent=2) + "\n")
    return int(any(r["failures"] for r in results))


if __name__ == "__main__":
    raise SystemExit(main())
