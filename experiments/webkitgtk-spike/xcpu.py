#!/usr/bin/env python3
"""SIGXCPU in the VM-free host: who owns it, whether a Go subscriber still
hears it, and what WebKit's thread-priority support does to scheduling and
limits, through the host's whole lifecycle.

    python3 -B xcpu.py BINARY OUTDIR --reserve N --debug-dir DIR \
        [--runtime ROOT --runtime-debug-dir DIR2] [--only NAME ...]

`control-2.52.6` runs the installed runtime: WebKit is expected to replace
Go's handler. With --runtime, `runtime-2.54.2` runs the same lifecycle
against an isolated library extraction under ROOT (LD_LIBRARY_PATH and
WEBKIT_INJECTED_BUNDLE_PATH; a mixed-package diagnostic, not a deployment),
where no SIGXCPU handler is expected. Every host is a disposable process
under vmwatch_gdb.py; a SIGXCPU subscriber is registered through os/signal
before WebKit initializes (-feas-xcpu). At each stage the host reports
SIGXCPU's kernel disposition, thread scheduling and limits, and SIGXCPU is
sent twice: by tgkill to a native thread, and process-directed from this
driver; the Go subscriber must (or, in the control, cannot) receive it.

Also recorded: every sigaction/signal call site in each runtime's two
libraries (static.json), whether RealtimeKit grants this environment's
processes anything (rtkit.json; it asks for a throwaway child only), and an
unmonitored run per runtime, since gdb slows WebKit's 20 ms D-Bus calls.
"""
import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

import feasibility as fz
import vmfree as vf
from exitwatch import enable_subreaper
from feasibility_env import record

check = fz.check
LIBDIR = "usr/lib/x86_64-linux-gnu"


def sigaction_sites(lib):
    """Every call to a sigaction/signal PLT stub in lib, with the constant
    last loaded into %edi before it (None when the signal is not a
    constant), and the nearest exported symbol."""
    out = subprocess.run(["objdump", "-d", "--no-show-raw-insn", str(lib)], capture_output=True, text=True, check=True).stdout.splitlines()
    sites, recent, func = [], [], ""
    for line in out:
        m = re.match(r"^([0-9a-f]+) <(.*)>:", line)
        if m:
            func, recent = m.group(2), []
            continue
        recent = (recent + [line])[-10:]
        c = re.search(r"call\s+[0-9a-f]+ <((?:__)?(?:sigaction|signal|bsd_signal|sysv_signal|sigset)[^@>]*)@plt>", line)
        if c:
            sig = None
            for r in reversed(recent[:-1]):
                mm = re.search(r"mov\s+\$0x([0-9a-f]+),%edi", r)
                if mm:
                    sig = int(mm.group(1), 16)
                    break
                if re.search(r",%[er]di$", r.strip()):
                    break
            sites.append({"call": c.group(1), "signal": sig, "address": line.split(":")[0].strip(), "near": func})
    return sites


def rtkit_grant():
    """Whether RealtimeKit grants this environment's processes anything: ask
    it to promote a throwaway child, read the kernel's view, end the child."""
    child = subprocess.Popen([sys.executable, "-I", "-c", "import time; time.sleep(30)"])
    time.sleep(0.3)
    pid = child.pid

    def stat():
        f = Path(f"/proc/{pid}/stat").read_text()
        f = f[f.rindex(")") + 1:].split()
        return {"policy": int(f[38]), "rt_priority": int(f[37]), "nice": int(f[16])}
    out = {"before": stat()}
    try:
        for method, sig, arg in (("MakeThreadRealtimeWithPID", "ttu", "5"), ("MakeThreadHighPriorityWithPID", "tti", "-10")):
            r = subprocess.run(["busctl", "--system", "call", "org.freedesktop.RealtimeKit1", "/org/freedesktop/RealtimeKit1",
                                "org.freedesktop.RealtimeKit1", method, sig, "--", str(pid), str(pid), arg], capture_output=True, text=True)
            out[method] = {"exit": r.returncode, "message": (r.stderr or r.stdout).strip(), "after": stat()}
    finally:
        child.kill()
        child.wait()
    props = {}
    for prop in ("RTTimeUSecMax", "MaxRealtimePriority", "MinNiceLevel"):
        r = subprocess.run(["busctl", "--system", "get-property", "org.freedesktop.RealtimeKit1", "/org/freedesktop/RealtimeKit1",
                            "org.freedesktop.RealtimeKit1", prop], capture_output=True, text=True)
        props[prop] = r.stdout.strip() or r.stderr.strip()
    out["properties"] = props
    out["cgroup"] = Path("/proc/self/cgroup").read_text().strip()
    return out


def lifecycle(expect_webkit):
    def body(host, result, out):
        pid = host.ready["pid"]
        stages = []

        def probe(stage):
            st = host.expect("status", host.send("status"))
            probes = {}
            for mode in ("native", "external"):
                m = host.send("xcpu-native" if mode == "native" else "xcpu-wait")
                host.expect("xcpu-armed", m, mode=mode)
                if mode == "external":
                    os.kill(pid, signal.SIGXCPU)  # process-directed, as the kernel sends RLIMIT_CPU's
                probes[mode] = host.expect("xcpu", m, mode=mode, timeout=10)
            stages.append({"stage": stage, "sigxcpu": st["signals"]["SIGXCPU"], "record": st["record"],
                           "delivery": {k: {"received_by_go": v["received_by_go"], "target": v.get("target_name"),
                                            "rr_before": [t["name"] for t in v["before"]["sched"]["not_default"] or [] if t["policy"] == "RR"],
                                            "rr_after": [t["name"] for t in v["after"]["sched"]["not_default"] or [] if t["policy"] == "RR"]}
                                        for k, v in probes.items()}})

        host.open("A", "loopback")
        # Which WebKit and JSC the host really loaded.
        maps = Path(f"/proc/{pid}/maps").read_text().splitlines()
        result["host_libraries"] = sorted({l.split()[-1] for l in maps if "libwebkit2gtk" in l or "libjavascriptcoregtk" in l})
        time.sleep(1.5)  # WebKit installs from a main-loop callback
        probe("first window (loopback)")
        host.open("I", "inprocess")
        for label in ("A", "I"):
            host.page(label, "run=checks", f"{label}-checks")
        probe("second window (in-process), pages run")
        host.expect("inspector", host.send("inspect A"), window="A", attached=True)
        time.sleep(1)
        probe("inspector docked")
        host.close("A")
        host.close("I")
        time.sleep(1)
        probe("no windows")
        host.open("A2", "loopback")
        host.page("A2", "run=checks", "A2-checks")
        time.sleep(1)
        probe("reopened")
        host.close("A2")
        start = [e for e in host.all if e.get("event") == "signals" and e["stage"] == "start"][0]
        result["stages"] = stages
        result["start"] = {"sigxcpu": start["dispositions"]["SIGXCPU"], "record": start["record"]}
        owners = [s["sigxcpu"]["owner"] for s in stages]
        delivered = [s["delivery"][m]["received_by_go"] for s in stages for m in ("native", "external")]
        if expect_webkit:
            check(result, "control: WebKit replaces Go's SIGXCPU handler and the Go subscriber hears nothing",
                  start["dispositions"]["SIGXCPU"]["owner"] == "go" and all(o.startswith("native:libjavascriptcoregtk") for o in owners)
                  and not any(delivered), owners=owners, delivered=delivered)
        else:
            check(result, "SIGXCPU stays Go's at every stage and the Go subscriber receives every delivery",
                  start["dispositions"]["SIGXCPU"]["owner"] == "go" and all(o == "go" for o in owners) and all(delivered),
                  owners=owners, delivered=delivered)
        return {}
    return body


def unmonitored(run, name, env, expect_webkit):
    """The same host without gdb, whose breakpoints slow WebKit's 20 ms
    RealtimeKit calls: scheduling as an application would see it, then one
    SIGXCPU to a native thread (preferring a SCHED_RR one) to show WebKit's
    demotion path or Go's delivery."""
    if run.args.only and name not in run.args.only:
        return None
    out = run.args.output / name
    out.mkdir(parents=True, exist_ok=True)
    xdg = run.root / f"xdg-{name}-{os.getpid()}"
    result = {"scenario": name, "checks": [], "ok": False, "env": env}
    host = None
    try:
        host = fz.Host(run.binary, out, ["-feas-root", str(run.root), "-feas-vmfree", "-signal-reserve", str(run.args.reserve), "-feas-xcpu"], xdg, env=env)
        pid = host.ready["pid"]
        host.open("A", "loopback")
        host.open("I", "inprocess")
        time.sleep(3)
        maps = Path(f"/proc/{pid}/maps").read_text().splitlines()
        result["host_libraries"] = sorted({l.split()[-1] for l in maps if "libwebkit2gtk" in l or "libjavascriptcoregtk" in l})
        st = host.expect("status", host.send("status"))
        result["settled"] = {"sigxcpu": st["signals"]["SIGXCPU"], "record": st["record"]}
        m = host.send("xcpu-native")
        host.expect("xcpu-armed", m, mode="native")
        result["native_probe"] = host.expect("xcpu", m, mode="native", timeout=10)
        received = result["native_probe"]["received_by_go"]
        check(result, "control: the Go subscriber does not hear SIGXCPU" if expect_webkit else "the Go subscriber hears SIGXCPU",
              received != expect_webkit, received=received)
        host.close("A")
        host.close("I")
    except (fz.Failure, OSError, KeyError, TypeError, ValueError) as err:
        result["failure"] = f"{type(err).__name__}: {err}"
    finally:
        if host:
            observed = host.finish(timeout=60)
            result["exit"] = {k: observed[k] for k in ("host_exit", "timed_out", "children_pending", "child_failure", "fatal_log")}
            import shutil
            shutil.rmtree(xdg, ignore_errors=True)
    ex = result.get("exit", {})
    result["ok"] = "failure" not in result and all(c["ok"] for c in result["checks"]) and ex.get("host_exit") == 0 and not any(
        ex.get(k) for k in ("timed_out", "children_pending", "fatal_log", "child_failure"))
    (out / "result.json").write_text(json.dumps(result, indent=1, default=str) + "\n")
    print(json.dumps({"scenario": name, "ok": result["ok"], "failure": result.get("failure"), "exit": result.get("exit")}), flush=True)
    return result


class Run(vf.Run):
    def __init__(self, args, env, debug_dir, expect_webkit):
        super().__init__(args, env=env, debug_dir=debug_dir)
        self.flags = [*self.flags, "-feas-xcpu"]
        self.expect_webkit = expect_webkit

    def judge(self, result, hits, totals):
        xcpu = [h for h in hits if h["kind"] == "libc-sigaction" and h.get("signal") == 24 and h.get("installs")]
        rt = {k: (totals or {}).get(k, {}).get("hits") for k in ("realtime-threads-created", "realtime-thread-registered", "realtime-promote",
                                                               "realtime-rtkit", "realtime-sigxcpu-callback", "realtime-demote-all",
                                                               "highpriority-threads-created", "highpriority-thread-registered",
                                                               "highpriority-rtkit", "scheduling-attributes")}
        result["monitor"]["sigxcpu_installs"] = [{"thread": h["thread_name"], "frames": h["frames"][:6]} for h in xcpu]
        result["monitor"]["thread_priority"] = rt
        exit_rec = result.get("signals", {}).get("exit")
        if self.expect_webkit:
            check(result, "monitor: one native SIGXCPU installation, from RealTimeThreads", len(xcpu) == 1
                  and any("setupSignalHandler" in f for f in xcpu[0]["frames"][:4]), installs=len(xcpu))
            if rt["realtime-sigxcpu-callback"] is not None:
                check(result, "control: each SIGXCPU reached WebKit's eventfd callback and demotion pass", rt["realtime-sigxcpu-callback"] >= 1
                      and rt["realtime-demote-all"] == rt["realtime-sigxcpu-callback"], thread_priority=rt)
        else:
            check(result, "monitor: no native SIGXCPU installation and no RealTimeThreads", not xcpu and not rt["realtime-threads-created"],
                  installs=len(xcpu), thread_priority=rt)
        start = result.get("start", {}).get("record", {}).get("limits")
        stages = [s["record"]["limits"] for s in result.get("stages", [])]
        result["limits_changed"] = [l for l in stages if l != start]
        result["exit_sigxcpu"] = (exit_rec or {}).get("SIGXCPU")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("binary", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--reserve", type=int, required=True)
    p.add_argument("--debug-dir", required=True, help="debug links for the installed runtime")
    p.add_argument("--runtime", type=Path, help="isolated extraction root of another runtime")
    p.add_argument("--runtime-debug-dir", help="debug links for that runtime")
    p.add_argument("--root", default="/tmp/dfx2-feasibility")
    p.add_argument("--only", nargs="*")
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    enable_subreaper()
    record(args.binary.resolve(), args.output)
    results = []
    static = {}
    roots = {"2.52.6 (installed)": Path("/")}
    if args.runtime:
        roots["2.54.2 (isolated)"] = args.runtime
    for name, root in roots.items():
        for lib in ("libjavascriptcoregtk-4.1.so.0", "libwebkit2gtk-4.1.so.0"):
            path = (root / LIBDIR / lib).resolve()
            sites = sigaction_sites(path)
            static[f"{name} {lib}"] = {"path": str(path), "sites": sites, "sigxcpu_sites": [x for x in sites if x["signal"] == 24]}
    (args.output / "static.json").write_text(json.dumps(static, indent=1) + "\n")
    rtkit = rtkit_grant()
    (args.output / "rtkit.json").write_text(json.dumps(rtkit, indent=1) + "\n")
    print(json.dumps({"static_sigxcpu_sites": {k: len(v["sigxcpu_sites"]) for k, v in static.items()},
                      "rtkit": {k: v["message"] for k, v in rtkit.items() if isinstance(v, dict) and "message" in v}}), flush=True)
    control = Run(args, {}, args.debug_dir, True)
    control.vscenario("control-2.52.6", lifecycle(True))
    results += control.results
    results += [r for r in [unmonitored(control, "unmonitored-2.52.6", {}, True)] if r]
    if args.runtime:
        lib = str(args.runtime / "usr/lib/x86_64-linux-gnu")
        env = {"LD_LIBRARY_PATH": lib, "WEBKIT_INJECTED_BUNDLE_PATH": lib + "/webkit2gtk-4.1/injected-bundle"}
        other = Run(args, env, args.runtime_debug_dir, False)
        other.vscenario("runtime-2.54.2", lifecycle(False))
        results += other.results
        results += [r for r in [unmonitored(other, "unmonitored-2.54.2", env, False)] if r]
    summary = {"reserve": args.reserve, "runtime": str(args.runtime) if args.runtime else None, "static": {k: v["sigxcpu_sites"] for k, v in static.items()},
               "rtkit": rtkit, "scenarios": results,
               "passed": all(r["ok"] for r in results)}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=1, default=str) + "\n")
    print(json.dumps({"passed": summary["passed"], "failed": [r["scenario"] for r in results if not r["ok"]]}))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
