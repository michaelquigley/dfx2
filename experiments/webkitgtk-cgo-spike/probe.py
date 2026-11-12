#!/usr/bin/env python3
"""Does cgo change signal coexistence? Every case is one disposable process.

    python3 -B probe.py CGO_BINARY PUREGO_BINARY OUTDIR --debug-dir DIR

CGO_BINARY is this module built with CGO_ENABLED=1; PUREGO_BINARY is
../webkitgtk-spike built with CGO_ENABLED=0 (its VM-free host is the
comparison). DIR holds .build-id links to Ubuntu's debuginfod files for the
installed libjavascriptcoregtk and libwebkit2gtk, as in that experiment.

Cases:
  monitored            the phase script under ../webkitgtk-spike/vmwatch_gdb.py
  control-vm           positive control: a host JSC context, same monitor
  unmonitored          the phase script without gdb
  reserved-env-60      explicit reservation through JSC_SIGNAL_FOR_GC (monitored)
  stray-*              hazard: one signal after the page loads; does the process survive
  crash-frames         the stray SIGUSR1 crash, stopped in gdb for its frames
  purego-*             the purego VM-free host: dispositions and the same strays
"""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
SPIKE = HERE.parent / "webkitgtk-spike"
sys.path.insert(0, str(SPIKE))
import feasibility as fz  # noqa: E402
import vmfree as vf  # noqa: E402
from feasibility_env import record  # noqa: E402
from exitwatch import enable_subreaper  # noqa: E402


def events(path):
    out = []
    for line in Path(path).read_text().splitlines():
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def run(out, name, binary, args, debug_dir=None, timeout=120):
    d = out / name
    d.mkdir(parents=True, exist_ok=True)
    command, env = [str(binary), *args], dict(os.environ)
    if debug_dir:
        wrap, menv = vf.monitor(debug_dir, d / "vmwatch.jsonl")
        command, env = [*wrap, *command], {**env, **menv}
    with open(d / "stdout.jsonl", "w") as so, open(d / "stderr.txt", "w") as se:
        try:
            code = subprocess.run(command, stdout=so, stderr=se, env=env, timeout=timeout, start_new_session=True, cwd="/").returncode
        except subprocess.TimeoutExpired:
            code = "hang"
    r = {"case": name, "command": command[-len(args) - 1:], "exit": code, "events": events(d / "stdout.jsonl")}
    err = (d / "stderr.txt").read_text().splitlines()
    r["stderr_head"] = [l for l in err if l.strip() and not l.startswith(("\t", "goroutine ", "created by "))][:6]
    if debug_dir and (d / "vmwatch.jsonl").exists():
        hits, totals, inferior = vf.watched(d / "vmwatch.jsonl")
        r["monitor"] = vf.summarize(hits, totals)
        # With cgo, the Go runtime installs its own handlers through libc
        # (x_cgo_sigaction), so libc sigaction hits are no longer all native.
        installs = r["monitor"].pop("native_sigaction_installs")
        r["monitor"]["go_runtime_sigaction_installs"] = sorted({s for s, _, f in installs if "x_cgo_sigaction" in f})
        r["monitor"]["native_sigaction_installs"] = [x for x in installs if "x_cgo_sigaction" not in x[2]]
        r["monitor"]["unresolved"] = [k for k, v in (totals or {}).items() if (v["locations"] == 0 or v["pending"]) and not v.get("optional")]
        r["monitor"]["vm_hits"] = {k: totals[k]["hits"] for k in vf.VM_KINDS} if totals else None
    return r


def phases(r):
    return {e["phase"]: e for e in r["events"] if e.get("event") == "phase"}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("cgo", type=Path)
    p.add_argument("purego", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--debug-dir", required=True)
    p.add_argument("--root", default="/tmp/dfx2-feasibility")
    args = p.parse_args()
    out = args.output.resolve()  # subprocesses run from /
    out.mkdir(parents=True, exist_ok=True)
    enable_subreaper()
    record(args.cgo.resolve(), out)
    # Build and runtime dependencies of the cgo executable.
    deps = {"needed": [l.split("[")[1].rstrip("]") for l in subprocess.run(["readelf", "-d", str(args.cgo)], capture_output=True, text=True).stdout.splitlines() if "(NEEDED)" in l],
            "interpreter": subprocess.run(["readelf", "-l", str(args.cgo)], capture_output=True, text=True).stdout.split("interpreter: ")[-1].split("]")[0],
            "resolved": sorted({l.split("=>")[1].split("(")[0].strip() for l in subprocess.run(["ldd", str(args.cgo)], capture_output=True, text=True).stdout.splitlines() if "=>" in l}),
            "pkg_config": {m: subprocess.run(["pkg-config", "--modversion", m], capture_output=True, text=True).stdout.strip() for m in ("webkit2gtk-4.1", "gtk+-3.0", "javascriptcoregtk-4.1")},
            "cc": subprocess.run(["gcc", "--version"], capture_output=True, text=True).stdout.splitlines()[0],
            "size_bytes": args.cgo.stat().st_size}
    (out / "deps.json").write_text(json.dumps(deps, indent=1) + "\n")
    results, checks = [], []

    def check(name, ok, **detail):
        checks.append({"check": name, "ok": bool(ok), **detail})
        print(json.dumps({"check": name, "ok": bool(ok)}), flush=True)

    # 1. The phase script, watched; the positive control; unwatched; reserved.
    mon = run(out, "monitored", args.cgo, [], args.debug_dir)
    ctl = run(out, "control-vm", args.cgo, ["-vm-control", "-faults=false"], args.debug_dir)
    plain = run(out, "unmonitored", args.cgo, [])
    env60 = run(out, "reserved-env-60", args.cgo, ["-gc-env", "60"], args.debug_dir)
    results += [mon, ctl, plain, env60]
    check("monitor resolved every required watch in the cgo host", mon.get("monitor") and not mon["monitor"]["unresolved"],
          unresolved=(mon.get("monitor") or {}).get("unresolved"))
    check("positive control: host VM, Go main thread registered, fault handlers installed",
          ctl["exit"] == 0 and ctl["monitor"]["vm_hits"]["vm-constructed"] > 0 and ctl["monitor"]["vm_hits"]["thread-registered-for-gc"] > 0
          and ctl["monitor"]["vm_hits"]["fault-handlers-finalized"] > 0, vm_hits=ctl["monitor"]["vm_hits"])
    for r in (mon, env60):
        check(f"{r['case']}: VM-free (all VM, registration, suspension and fault-handler kinds 0)",
              r["exit"] == 0 and r["monitor"]["vm_hits"] and not any(r["monitor"]["vm_hits"].values()), vm_hits=r["monitor"]["vm_hits"])
    for r in (mon, plain, env60):
        ph = phases(r)
        progress = [e["progress"] for e in ph.values()]
        # Application-visible Go progress keeps running while gtk_main runs.
        ok = r["exit"] == 0 and len(ph) == 4 and progress == sorted(progress) and len(set(progress)) == 4
        for name, e in ph.items():
            ok = ok and all(e["go_faults"][k].startswith("runtime error") for k in ("nil", "bus")) \
                and e["http_faults"]["/api/query?x=alive"].startswith("200")
            ok = ok and all(d.get("received_by_go") for d in e["deliveries"] if "skipped" not in d and d.get("error") != "no native thread")
        check(f"{r['case']}: faults recovered, endpoint alive, every Go-owned signal delivered, at every phase", ok)
    after = phases(mon).get("after-load", {}).get("dispositions", {})
    check("unmodified: after page load JSC owns SIGUSR1 and SIGXCPU, Go keeps SIGSEGV, SIGBUS, SIGHUP, SIGUSR2",
          after.get("SIGUSR1", {}).get("owner", "").startswith("native:libjavascriptcoregtk") and after.get("SIGXCPU", {}).get("owner", "").startswith("native:libjavascriptcoregtk")
          and all(after.get(s, {}).get("owner") == "go" for s in ("SIGSEGV", "SIGBUS", "SIGHUP", "SIGUSR2")),
          after_load={k: v["owner"] for k, v in after.items()})
    after60 = phases(env60).get("after-load", {}).get("dispositions", {})
    check("JSC_SIGNAL_FOR_GC=60: SIGUSR1 stays Go's, 60 is JSC's; SIGXCPU still JSC's",
          after60.get("SIGUSR1", {}).get("owner") == "go" and after60.get("reserved 60", {}).get("owner", "").startswith("native:libjavascriptcoregtk")
          and after60.get("SIGXCPU", {}).get("owner", "").startswith("native:"), after_load={k: v["owner"] for k, v in after60.items()})

    # 2. Strays, each a disposable process.
    strays = [("stray-usr1-external", ["-stray", "10", "-stray-mode", "external"], False),
              ("stray-usr1-self", ["-stray", "10", "-stray-mode", "self"], False),
              ("stray-usr1-native", ["-stray", "10", "-stray-mode", "native"], False),
              ("stray-xcpu-external", ["-stray", "24", "-stray-mode", "external"], True),
              ("stray-xcpu-native", ["-stray", "24", "-stray-mode", "native"], True),
              ("stray-60-reserved-external", ["-gc-env", "60", "-stray", "60", "-stray-mode", "external"], False),
              ("stray-usr1-reserved-60-external", ["-gc-env", "60", "-stray", "10", "-stray-mode", "external"], True)]
    for name, a, survives in strays:
        r = run(out, name, args.cgo, [*a, "-faults=false"], timeout=90)
        survived = [e for e in r["events"] if e.get("event") == "stray-survived"]
        r["survived"] = bool(survived) and r["exit"] == 0
        r["received_by_go"] = survived[0]["delivery"].get("received_by_go") if survived else None
        results.append(r)
        check(f"{name}: {'survives' if survives else 'kills the process'}", r["survived"] == survives,
              exit=r["exit"], received_by_go=r["received_by_go"], stderr=r["stderr_head"][:3])

    # 3. Where the stray SIGUSR1 faults: stop in gdb at the SIGSEGV.
    gdb = ["gdb", "-q", "-batch", "-nx", "-ex", f"set debug-file-directory {args.debug_dir}:/usr/lib/debug", "-ex", "set pagination off",
           "-ex", "set startup-with-shell off", "-ex", "handle all nostop noprint pass", "-ex", "handle SIGSEGV stop print nopass",
           "-ex", "run", "-ex", "bt 8", "-ex", "info registers rip", "-ex", "kill", "--args", str(args.cgo), "-stray", "10", "-stray-mode", "external", "-faults=false"]
    text = subprocess.run(gdb, capture_output=True, text=True, timeout=180, cwd="/").stdout
    frames = [l for l in text.splitlines() if l.startswith(("#", "Thread", "rip"))]  # gdb's lines, not the probe's JSON
    (out / "crash-frames.txt").write_text("\n".join(frames) + "\n")
    check("the stray SIGUSR1 faults inside JSC's suspend handler", any("signalHandlerSuspendResume" in l for l in frames), frames=frames[:6])

    # 4. The purego VM-free host on the same runtime.
    def purego(name, flags, sig, survives):
        d = out / name
        d.mkdir(parents=True, exist_ok=True)
        xdg = Path(args.root) / f"xdg-{name}-{os.getpid()}"
        host = fz.Host(args.purego.resolve(), d, ["-feas-root", args.root, "-feas-vmfree", *flags], xdg)
        pid = host.ready["pid"]
        r = {"case": name, "flags": flags, "signal": sig}
        try:
            host.open("A", "loopback")
            time.sleep(1.5)
            st = host.expect("status", host.send("status"))
            r["dispositions"] = {k: v["owner"] for k, v in st["signals"].items()}
            os.kill(pid, sig)
            time.sleep(1.5)
            r["alive_after"] = host.proc.poll() is None
        finally:
            observed = host.finish(timeout=30)
        r["exit"] = observed["host_exit"]
        r["stderr_head"] = [l for l in (d / "host.stderr").read_text().splitlines() if l.strip() and not l.startswith(("\t", "goroutine "))][:4]
        results.append(r)
        check(f"{name}: {'survives' if survives else 'kills the process'}", r["alive_after"] == survives, exit=r["exit"],
              dispositions=r["dispositions"], stderr=r["stderr_head"][:3])

    purego("purego-stray-usr1", [], signal.SIGUSR1, False)
    purego("purego-stray-60-reserved", ["-signal-reserve", "60"], 60, False)
    purego("purego-stray-xcpu", [], signal.SIGXCPU, True)

    summary = {"checks": checks, "passed": all(c["ok"] for c in checks),
               "cases": [{k: v for k, v in r.items() if k != "events"} for r in results]}
    (out / "summary.json").write_text(json.dumps(summary, indent=1, default=str) + "\n")
    print(json.dumps({"passed": summary["passed"], "failed": [c["check"] for c in checks if not c["ok"]]}))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
