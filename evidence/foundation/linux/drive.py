#!/usr/bin/env python3
"""Run bounded Linux native auditions and retain logs, exits and process facts."""
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


def command(*args):
    return subprocess.run(args, capture_output=True, text=True, check=True).stdout.strip()


def child_processes(pid):
    """Inspect children of every thread and recurse; retain identities, not env."""
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
                    # bwrap is also used by browsers; record the actual loader
                    # payload when present instead of classifying by bwrap alone.
                    payload = next((a for a in argv if a.startswith("/usr/libexec/glycin-loaders/")), "")
                    found[child] = dict(pid=child, comm=base.joinpath("comm").read_text().strip(),
                                        exe=str(base.joinpath("exe").resolve()), loader=payload)
                    pending.append(child)
                except FileNotFoundError:
                    pass
    return list(found.values())


def audition(binary, out, name, flags, env, signal_after_updates=False):
    lines = []
    events = []
    incoming = queue.Queue()
    stderr_path = out / (name + ".stderr.log")
    with stderr_path.open("w") as stderr:
        proc = subprocess.Popen([binary, *flags], stdout=subprocess.PIPE, stderr=stderr,
                                text=True, env=env)
        def read():
            for line in proc.stdout:
                incoming.put(line)
            incoming.put(None)
        reader = threading.Thread(target=read, daemon=True)
        reader.start()
        deadline = time.monotonic() + 20
        inspected = False
        maps, children = [], []
        timed_out = False
        signaled = False
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
            if event["event"] == "update" and not inspected:
                # Native windows and counter callbacks are running now.
                maps = sorted(set(line.split()[-1] for line in
                    Path(f"/proc/{proc.pid}/maps").read_text().splitlines()
                    if "/" in line))
                children = child_processes(proc.pid)
                inspected = True
            if signal_after_updates and not signaled and event["event"] == "update" and event["count"] >= 2:
                proc.send_signal(signal.SIGTERM)
                signaled = True
        try:
            code = proc.wait(timeout=max(1, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            timed_out = True
            proc.kill()
            code = proc.wait()
        reader.join(timeout=1)
        while not incoming.empty():
            line = incoming.get()
            if line is not None:
                lines.append(line)
                events.append(json.loads(line))
        proc.stdout.close()
    (out / (name + ".jsonl")).write_text("".join(lines))
    (out / (name + ".maps.txt")).write_text("\n".join(maps) + "\n")
    returned = [e for e in events if e["event"] == "return"]
    drained = [e for e in events if e["event"] == "drained"]
    failures = []
    expected_code = 1 if "failure" in flags else 0
    if code != expected_code or timed_out:
        failures.append(f"exit={code}, expected={expected_code}, timeout={timed_out}")
    if not returned or not drained:
        failures.append("missing return/drained event")
    elif any(drained[-1][key] != 0 for key in
             ("windows", "tasks", "native_completions", "queued", "native_windows", "native_retired", "dispatch_targets")) or not drained[-1]["terminal"] or drained[-1]["release_pending"]:
        failures.append("ownership did not drain")
    forbidden = [m for m in maps if any(x in m.lower() for x in ("webkit", "javascriptcore", "webview2"))]
    browser_children = [c for c in children if any(token in (c["comm"]+" "+c["exe"]).lower()
                        for token in ("webkit", "webview", "jsc", "chrome", "chromium", "msedge"))]
    unknown_children = [c for c in children if not c["loader"] and not c["comm"].startswith("glycin-")]
    if forbidden or browser_children or unknown_children:
        failures.append(f"forbidden libraries={forbidden}, browser children={browser_children}, unclassified children={unknown_children}")
    if name != "create-close" and not inspected:
        failures.append("native process inspection did not run")
    if "lifecycle" in flags:
        independence = [e for e in events if e["event"] == "verified-independent"]
        if not independence or independence[-1]["after"] <= independence[-1]["before"]:
            failures.append("B did not demonstrate updates after A closed")
        if "-resident" in flags:
            if not any(e["event"] == "resident-zero-windows" and e["windows"] == 0 and e["tasks"] >= 1 for e in events):
                failures.append("resident zero-window lifetime absent")
            if not any(e["event"] == "verified-reopen" for e in events):
                failures.append("resident reopening absent")
    if "idle" in flags:
        if not any(e["event"] == "idle-measurement" and e["wakes"] == 0 and e["dispatches"] == 0 for e in events):
            failures.append("zero-wake idle interval absent")
    if "failure" in flags and (not returned or "controlled lifetime task failure" not in returned[-1]["error"]):
        failures.append("controlled task failure was lost")
    if "create-close" in flags and not any(e["event"] == "verified-create-close" for e in events):
        failures.append("creation-close hook was not verified")
    if signal_after_updates and (not signaled or not returned or "context canceled" not in returned[-1]["error"]):
        failures.append("SIGTERM cancellation was not observed")
    result = dict(name=name, command=[binary, *flags], exit_code=code, timed_out=timed_out,
                  inspected=inspected, children=children, forbidden_libraries=forbidden, browser_children=browser_children,
                  events=len(events), failures=failures)
    print(json.dumps(result), flush=True)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--gdk-backend", choices=("wayland", "x11"), default="wayland")
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GDK_BACKEND=args.gdk_backend)
    host = dict(os=platform.platform(), architecture=platform.machine(), go=command("go", "version"),
                gtk=command("pkg-config", "--modversion", "gtk+-3.0"), glib=command("pkg-config", "--modversion", "glib-2.0"),
                session=os.environ.get("XDG_SESSION_TYPE"), gdk_backend=args.gdk_backend,
                wayland_display=os.environ.get("WAYLAND_DISPLAY"), display=os.environ.get("DISPLAY"),
                date=command("date", "--iso-8601=seconds"))
    results = []
    cases = [("window", ["-script", "lifecycle"]),
             ("resident", ["-resident", "-script", "lifecycle"]),
             ("idle-window", ["-script", "idle"]),
             ("idle-resident", ["-resident", "-script", "idle"]),
             ("failure", ["-script", "failure"]),
             ("cancel", ["-script", "cancel"]),
             ("create-close", ["-script", "create-close"]),
             ("signal", [])]
    for name, flags in cases:
        results.append(audition(str(Path(args.binary).resolve()), args.out, name, flags, env, name == "signal"))
    summary = dict(host=host, results=results)
    (args.out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    return int(any(r["failures"] for r in results))


if __name__ == "__main__":
    raise SystemExit(main())
