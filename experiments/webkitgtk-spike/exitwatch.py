#!/usr/bin/env python3
"""Linux diagnostic supervisor: observe orphaned browser children after host exit.

Only children adopted by this subreaper have wait statuses. Processes already
reaped by WebKit are listed as observed, not claimed to have exited normally.
"""
import argparse
import ctypes
import json
import os
from pathlib import Path
import signal
import subprocess
import time


def enable_subreaper():
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER
        raise OSError(ctypes.get_errno(), "PR_SET_CHILD_SUBREAPER")


def descendants(root, seen):
    todo = [root]
    visited = set()
    while todo:
        pid = todo.pop()
        if pid in visited:
            continue
        visited.add(pid)
        try:
            for task in Path(f"/proc/{pid}/task").iterdir():
                try:
                    todo.extend(int(p) for p in (task / "children").read_text().split())
                except (FileNotFoundError, ProcessLookupError):
                    pass
            if pid != root:
                cmd = Path(f"/proc/{pid}/cmdline").read_bytes().replace(b"\0", b" ").decode(errors="replace").strip()
                if cmd:
                    # exec changes a bwrap helper into WebKit; retain the last identity.
                    libraries = seen.get(pid, {}).get("libraries", [])
                    if not libraries:
                        try:
                            libraries = sorted({line.split()[-1] for line in Path(f"/proc/{pid}/maps").read_text().splitlines()
                                                if any(lib in line for lib in ("libwebkit2gtk", "libjavascriptcoregtk"))})
                        except (FileNotFoundError, ProcessLookupError, PermissionError):
                            pass
                    seen[pid] = {"pid": pid, "command": cmd, "libraries": libraries,
                                 "name": Path(f"/proc/{pid}/comm").read_text().strip()}
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            pass


def observe(proc, timeout=120, child_timeout=15):
    seen = {}
    started = time.monotonic()
    deadline = started + timeout
    timed_out = False
    while proc.poll() is None:
        descendants(os.getpid(), seen)
        if time.monotonic() >= deadline:
            timed_out = True
            os.killpg(proc.pid, signal.SIGKILL)
            break
        time.sleep(.05)
    host_code = proc.wait()
    host_elapsed = time.monotonic() - started
    deadline = time.monotonic() + child_timeout
    exits = []
    children_pending = False
    while True:
        descendants(os.getpid(), seen)
        try:
            pid, status = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            break
        if pid:
            exits.append({**seen.get(pid, {"pid": pid, "name": "unobserved"}),
                          "exit": os.waitstatus_to_exitcode(status),
                          "core_dumped": os.WCOREDUMP(status)})
        elif time.monotonic() >= deadline:
            children_pending = True
            break
        else:
            time.sleep(.05)
    # SIGKILL is reported explicitly: bwrap namespace helpers use it during
    # normal teardown. Do not turn that into a claim of graceful child exit.
    bad = [p for p in exits if p["exit"] != 0 and not
           (p["exit"] == -signal.SIGKILL and p["name"] == "bwrap")]
    return {"host_pid": proc.pid, "host_exit": host_code,
            "host_seconds": round(host_elapsed, 3), "timed_out": timed_out,
            "children_pending": children_pending, "adopted_exits": exits,
            "observed_processes": list(seen.values()),
            "child_failure": bool(bad), "seconds": round(time.monotonic() - started, 3)}


def fatal_log(text):
    return any(s in text for s in ("free():", "malloc():", "corrupted size", "double free",
                                  "assertion failed", "CRITICAL", "checks FAIL"))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--output", type=Path, required=True, help="output prefix for .log and .json")
    p.add_argument("--timeout", type=float, default=120)
    p.add_argument("command", nargs=argparse.REMAINDER)
    args = p.parse_args()
    command = args.command
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        p.error("command required after --")
    enable_subreaper()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.with_suffix(".log").open("w") as log:
        proc = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=log,
                                stderr=subprocess.STDOUT, start_new_session=True)
        result = observe(proc, args.timeout)
    result["fatal_log"] = fatal_log(args.output.with_suffix(".log").read_text())
    result["command"] = command
    args.output.with_suffix(".json").write_text(json.dumps(result, indent=2) + "\n")
    failed = (result["host_exit"] != 0 or result["timed_out"] or result["children_pending"]
              or result["child_failure"] or result["fatal_log"])
    print(json.dumps({k: v for k, v in result.items() if k not in
                      ("observed_processes", "command", "adopted_exits")}))
    return int(failed)


if __name__ == "__main__":
    raise SystemExit(main())
