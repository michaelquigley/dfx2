#!/usr/bin/env python3
"""Run real GTK/WebKit windows; save evidence and fail on missing observations."""
import argparse
import os
from pathlib import Path
import subprocess
import time

p = argparse.ArgumentParser()
p.add_argument("binary", type=Path)
p.add_argument("output", type=Path)
args = p.parse_args()
args.output.mkdir(parents=True, exist_ok=True)
binary = str(args.binary.resolve())
summary = []
environment = subprocess.check_output(["pkg-config", "--modversion", "webkit2gtk-4.1", "gtk+-3.0", "libsoup-3.0"], text=True)
(args.output / "environment.txt").write_text(
    "WebKitGTK, GTK, Soup versions:\n" + environment +
    "session=" + os.getenv("XDG_SESSION_TYPE", "unknown") + "\n" +
    "GDK_BACKEND=" + os.getenv("GDK_BACKEND", "default") + "\n" +
    subprocess.check_output(["go", "version"], text=True))

def run(name, mode, flags, expected, code=0, sockets=False, terminate=False):
    command = [binary, "-mode", mode, *flags]
    path = args.output / (name + ".log")
    start = time.monotonic()
    with path.open("w") as out:
        out.write("command: " + " ".join(command) + "\n")
        out.flush()
        proc = subprocess.Popen(command, stdout=out, stderr=subprocess.STDOUT)
        try:
            if sockets:
                time.sleep(2)
                listing = subprocess.check_output(["ss", "-ltnp"], text=True)
                owned = [line for line in listing.splitlines() if f"pid={proc.pid}," in line]
                (args.output / (name + "-listeners.txt")).write_text("\n".join(owned) + "\n")
                assert bool(owned) == (mode == "loopback"), owned
            if terminate:
                deadline = time.monotonic() + 10
                while "start GET /api/slow" not in path.read_text():
                    assert time.monotonic() < deadline, "slow request never started"
                    time.sleep(.05)
                proc.terminate()
            result = proc.wait(timeout=25)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait()
    elapsed = time.monotonic() - start
    text = path.read_text()
    assert result == code, (name, result, text[-2000:])
    for observation in [*expected, "closed: requests=0 native-async=0 handlers=0"]:
        assert observation in text, (name, "missing", observation)
    assert "CRITICAL" not in text and "checks FAIL" not in text, name
    line = f"PASS {name} ({elapsed:.2f}s)"
    summary.append(line)
    (args.output / "summary.txt").write_text("\n".join(summary) + "\n")
    print(line, flush=True)

for mode in ("inprocess", "loopback"):
    run(mode + "-checks", mode,
        ["-autorun", "checks", "-quit-after", "10s", "-resize-after", "6s",
         "-snapshot", str((args.output / (mode + ".png")).resolve())],
        ["checks PASS 14/14", "navigation refused", '"secure":true', '"storage":"yes"', "audition: restore", "snapshot:"], sockets=True)
    run(mode + "-reload", mode, ["-autorun", "reload", "-quit-after", "6s"],
        ["reload: page reloaded"])
    run(mode + "-close-busy", mode,
        ["-autorun", "slow", "-close-busy", "300ms", "-quit-after", "10s"],
        ["closing: close while busy", 'tag="busy" saw cancellation'])
    run(mode + "-sigterm", mode, ["-autorun", "slow", "-quit-after", "10s"],
        ["closing: signal terminated", 'tag="busy" saw cancellation'], terminate=True)
    run(mode + "-startup-close", mode, ["-quit-after", "1ms"], ["closing: timed exit"])
run("inprocess-close-reading", "inprocess", ["-autorun", "checks", "-close-reading", "-quit-after", "10s"],
    ["closing: close during native body read", "async=1"])
run("loopback-listener-failure", "loopback", ["-break-listener", "2s", "-quit-after", "10s"],
    ["closing: server failure", "loopback server:"], code=1)
