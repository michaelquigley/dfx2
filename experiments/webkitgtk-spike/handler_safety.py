#!/usr/bin/env python3
"""The handler-safety follow-up: run each probe in a disposable process and
retain its output, traces and a judged summary.

    python3 -B handler_safety.py BINARY OUTDIR --reserve N [--debug-file JSC_DEBUG]

--debug-file is Ubuntu's debuginfod file for the installed
libjavascriptcoregtk (matched by build ID); with it, handler offsets are
symbolized and SIGXCPU registration is traced in gdb. The reservation N is
the application's explicit choice, never inferred here.
"""
import argparse
import collections
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

from feasibility_env import record

HERE = Path(__file__).resolve().parent
JSC = "/usr/lib/x86_64-linux-gnu/libjavascriptcoregtk-4.1.so.0"


def report_of(path):
    report, events = None, []
    for line in Path(path).read_text().splitlines():
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            continue
        events.append(e)
        if e.get("event") == "signal-probe":
            report = e["report"]
    return report, events


def run(binary, out, name, args, strace=None, timeout=180):
    command = [str(binary), "-signal-probe", *args]
    if strace:
        command = ["strace", "-f", "-qq", "-e", strace, "-e", "signal=all", "-o", str(out / f"{name}.strace"), *command]
    with open(out / f"{name}.stdout", "w") as so, open(out / f"{name}.stderr", "w") as se:
        try:
            code = subprocess.run(command, stdout=so, stderr=se, timeout=timeout, start_new_session=True).returncode
        except subprocess.TimeoutExpired:
            code = "hang"
    report, events = report_of(out / f"{name}.stdout")
    stderr = [l for l in (out / f"{name}.stderr").read_text().splitlines() if l and not l.startswith(("\t", "goroutine ", "created by "))][:4]
    return {"name": name, "command": command, "exit": code, "report": report, "events": events, "stderr": stderr}


def attribute(trace, main, sig):
    """Deliveries of sig to the main thread, and tgkill(main, sig) senders, by
    the phase the main thread last announced."""
    marks = {"getgid": "js", "getppid": "go", "getuid": "c"}
    phase, delivered, kills = None, collections.Counter(), collections.Counter()
    for line in trace.read_text().splitlines():
        pid = line.split()[0]
        m = re.match(r"\d+\s+(\w+)\((.*)", line)
        if m and m.group(1) in marks and pid == main:
            phase = marks[m.group(1)]
        elif m and m.group(1) == "tgkill" and f", {main}, {sig}" in m.group(2):
            kills[(pid, phase)] += 1
        elif f"--- {sig} " in line and pid == main:
            delivered[phase] += 1
    return delivered, kills


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("binary", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--reserve", type=int, required=True)
    p.add_argument("--debug-file", type=Path)
    args = p.parse_args()
    out = args.output
    out.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    record(binary, out)
    reserve = ["-signal-reserve", str(args.reserve)]
    kernel_sig = f"SIGRT_{args.reserve - 32}"  # strace numbers real-time signals from the kernel's SIGRTMIN, 32
    results = []

    def judge(name, ok, **detail):
        results.append({"check": name, "ok": bool(ok), **detail})
        print(json.dumps({"check": name, "ok": bool(ok), **{k: v for k, v in detail.items() if k != "runs"}}, default=str), flush=True)

    # 1. The transition, traced: who signals the main thread, and in which phase.
    t = run(binary, out, "transition-traced", ["suspension-transition", *reserve], strace="trace=getgid,getppid,getuid,tgkill")
    main_pid = str(t["report"]["pid"]) if t["report"] else ""
    names = {str(x["tid"]): x["name"] for x in (t["report"] or {}).get("threads", [])}
    delivered, kills = attribute(out / "transition-traced.strace", main_pid, kernel_sig) if t["report"] else ({}, {})
    senders = collections.Counter()
    for (sender, phase), n in kills.items():
        senders[(names.get(sender, "exited " + sender), phase)] += n
    with open(out / "transition-traced.strace", "rb") as src, gzip.open(out / "transition-traced.strace.gz", "wb", 9) as dst:
        shutil.copyfileobj(src, dst)
    (out / "transition-traced.strace").unlink()
    stack = (t["report"] or {}).get("thread_stack", {})
    judge("JSC suspends the registered main thread while it runs Go", t["exit"] == 0 and delivered.get("go", 0) > 0
          and stack.get("goroutine_stack_inside_mapping") is False,
          delivered_to_main_by_phase=dict(delivered), tgkill_senders_by_phase={f"{s} during {ph}": n for (s, ph), n in senders.items()},
          thread_stack=stack)

    # Untraced: the scanners' CPU while the main thread stays in Go.
    untraced = [run(binary, out, f"transition-{i + 1}", ["suspension-transition", *reserve]) for i in range(3)]
    ticks = collections.Counter()
    for r in untraced:
        for ph in (r["report"] or {}).get("phases", []):
            for name, (go, c) in ph["scanner_ticks_go_c"].items():
                ticks[(name, "go")] += go
                ticks[(name, "c")] += c
    judge("untraced: scanning threads burn CPU only while the main thread is in Go",
          all(r["exit"] == 0 for r in untraced) and sum(v for (n, ph), v in ticks.items() if ph == "c") == 0,
          scanner_cpu_ticks={f"{n} during {ph}": v for (n, ph), v in sorted(ticks.items())}, exits=[r["exit"] for r in untraced])

    # 3. The narrow fault-handler path, and an injected identity failure.
    def recovered(r):
        res = [e for e in r["events"] if e.get("event") == "fault-result"]
        return res[0]["recovered"] if res else None

    for fault in ("segv", "bus"):
        r = run(binary, out, f"narrow-{fault}", ["narrow", *reserve, "-signal-fault", fault])
        rep = r["report"] or {}
        judge(f"narrow: identified fault handlers only, {fault} recovered",
              r["exit"] == 0 and rep.get("fault_handlers_identified") and "nil pointer" in str(recovered(r))
              and [d["signal"] for d in rep.get("unprotected_native_handlers", [])] == [args.reserve]
              and all(d["received_by_go"] for d in rep.get("delivery") or []),
              identity=rep.get("fault_handler_identity"), recovered=recovered(r))
    r = run(binary, out, "narrow-foreign-segv", ["narrow-foreign", *reserve, "-signal-fault", "segv"])
    rep = r["report"] or {}
    judge("narrow, injected SIGBUS change: identity refused, nothing modified, Go fault still fatal",
          rep.get("fault_handlers_identified") is False and r["exit"] == 2 and recovered(r) is None,
          failed=[c["check"] for c in rep.get("fault_handler_identity", []) if not c["passed"]], stderr=r["stderr"])
    timelines = {}
    for order in ("jsc-first", "webkit-first"):
        r = run(binary, out, f"narrow-webkit-{order}", ["narrow", *reserve, "-signal-webkit", "-signal-order", order, "-signal-fault", "segv"])
        rep = r["report"] or {}
        timelines[order] = [(x["second"], x["sigxcpu"]["owner"], x["sched_rr_threads"]) for x in rep.get("page_timeline", [])]
        judge(f"narrow with WebKit ({order}): recovered; SIGXCPU recorded, not patched",
              r["exit"] == 0 and rep.get("fault_handlers_identified") and "nil pointer" in str(recovered(r)),
              unprotected=[(d["signal"], d["owner"]) for d in rep.get("unprotected_native_handlers", [])], sigxcpu_timeline=timelines[order],
              limits={k: v for k, v in rep.items() if k.endswith("_limit")})

    # 2. SIGXCPU: symbols and the registration path, when debug info is given.
    if args.debug_file:
        build = re.search(r"Build ID: (\w+)", subprocess.check_output(["readelf", "-n", JSC], text=True)).group(1)
        dbuild = re.search(r"Build ID: (\w+)", subprocess.check_output(["readelf", "-n", str(args.debug_file)], text=True)).group(1)
        symbols = {}
        for off in ("0x196d3f0", "0x196b2d0", "0x1961f30"):
            symbols[off] = subprocess.check_output(["addr2line", "-f", "-C", "-e", str(args.debug_file), off], text=True).split("\n")[:2]
        judge("handler offsets symbolized from the matching debug file", build == dbuild, build_id=build,
              debug_sha256=hashlib.sha256(args.debug_file.read_bytes()).hexdigest(), symbols=symbols)
        ddir = out / "debug-dir"
        (ddir / ".build-id" / build[:2]).mkdir(parents=True, exist_ok=True)
        os.symlink(args.debug_file.resolve(), ddir / ".build-id" / build[:2] / (build[2:] + ".debug"))
        script = (HERE / "handler_safety_xcpu.gdb").read_text().replace("@DEBUGDIR@", str(ddir))
        (out / "xcpu.gdb").write_text(script)
        counts = {}
        for order in ("jsc-first", "webkit-first"):
            text = subprocess.run(["gdb", "-q", "-batch", "-x", str(out / "xcpu.gdb"), "--args", str(binary), "-signal-probe", "narrow",
                                   *reserve, "-signal-webkit", "-signal-order", order], capture_output=True, text=True, timeout=240).stdout
            kept = [l for l in text.splitlines() if l.startswith(("===", "#"))]
            (out / f"xcpu-{order}.gdb.txt").write_text("\n".join(kept) + "\n")
            counts[order] = dict(collections.Counter(l for l in kept if l.startswith("===")))
        shutil.rmtree(ddir)
        judge("SIGXCPU registration traced in both orders", True, breakpoints=counts)
    summary = {"reserve": args.reserve, "checks": results, "passed": all(r["ok"] for r in results)}
    (out / "summary.json").write_text(json.dumps(summary, indent=1, default=str) + "\n")
    print(json.dumps({"passed": summary["passed"], "failed": [r["check"] for r in results if not r["ok"]]}))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
