#!/usr/bin/env python3
"""Pass A of the integration feasibility spike: run each JSC signal variant in
its own disposable process, retain its output, and judge it.

    python3 -B signal_probe.py BINARY OUTDIR [--runtime-lib DIR]

Every probe is a separate process with a hang guard; a timeout is recorded as
a hang, never as success. Nothing here signals the operator's shell or other
applications: the probes signal only themselves. Repeated cases count
outcomes; they are observations, not failure rates.
"""
import argparse
import collections
import json
import os
from pathlib import Path
import subprocess
import sys

from feasibility_env import record


def run(binary, out, name, args, env_extra=None, timeout=40, strace=False):
    env = dict(os.environ, **(env_extra or {}))
    command = [str(binary), "-signal-probe", *args]
    if strace:
        command = ["strace", "-f", "-qq", "-e", "trace=none", "-e", "signal=all", "-o", str(out / f"{name}.strace"), *command]
    with open(out / f"{name}.stdout", "w") as so, open(out / f"{name}.stderr", "w") as se:
        try:
            code = subprocess.run(command, stdout=so, stderr=se, env=env, timeout=timeout, start_new_session=True).returncode
        except subprocess.TimeoutExpired:
            code = "hang"
    report = None
    raised = None
    events = []
    for line in (out / f"{name}.stdout").read_text().splitlines():
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            continue
        events.append(e.get("event"))
        if e.get("event") == "signal-probe":
            report = e["report"]
        if e.get("event") == "raised-reserved-signal":
            raised = e
    stderr = (out / f"{name}.stderr").read_text()
    result = {"name": name, "command": command, "env": env_extra or {}, "exit": code, "events": events,
              "stderr_lines": [l for l in stderr.splitlines() if not l.startswith(("\t", "goroutine ", "created by "))][:6]}
    if report:
        final = report["stages"][-1]["dispositions"]
        result.update(ready=report.get("ready"), reason=report.get("reason"), selected=report.get("selected_signal"),
                      fault=report.get("fault"), delivery=report.get("delivery"), onstack_added=report.get("onstack_added"),
                      onstack_added_after_webkit=report.get("onstack_added_after_webkit"),
                      unprotected=[(d["signal"], d["owner"]) for d in report.get("unprotected_native_handlers") or []],
                      non_go=[(d["signal"], d["owner"], d.get("flags")) for d in final if d["owner"] not in ("go", "default")],
                      workload=report.get("workload"), second_thread=report.get("second_thread"),
                      configure_result=report.get("configure_result"), configure_after_init=report.get("configure_result_after_init"),
                      rtmin=report.get("libc_sigrtmin"), rtmax=report.get("libc_sigrtmax"), symbol=report.get("configure_symbol"))
        reservation = {k: report[k] for k in ("inferred_selection", "hidden_subscriber", "hidden_received_before_selection",
                                              "go_reports_ignored_after_release", "hidden_received_after_release", "reservation_checks",
                                              "audit_after_init", "audit_after_workload", "audit_after_violation", "injected") if k in report}
        if "kernel_view" in report:
            reservation["kernel_view_indistinguishable"] = report["kernel_view"]["indistinguishable"]
        if reservation:
            result["reservation"] = reservation
    if "raising-reserved-signal" in events:
        # After the report: raising the signal JSC now owns, as the old
        # subscriber's sender would. No closing event means the process died.
        result["raise"] = "old subscriber received" if raised and raised["old_subscriber_received"] else (
            "not received; survived" if raised else f"process died (exit {code})")
    if strace:
        counts = collections.Counter()
        pid = report["pid"] if report else None
        for line in (out / f"{name}.strace").read_text().splitlines():
            parts = line.split()
            if len(parts) > 2 and parts[1] == "---":
                counts[(parts[2], "main" if str(pid) == parts[0] else "other")] += 1
        result["delivered"] = {f"{s}@{t}": n for (s, t), n in sorted(counts.items())}
    return result


def reservation_cases(case, outcome):
    """The inferred selector's flaw, then the explicit-reservation prototype.
    A check passing here means the probe observed what it claims, including
    the flaw itself."""
    res = lambda r: r.get("reservation") or {}
    jsc_owns = lambda r, key: (res(r).get(key) or {}).get("jsc_owns_it")
    failed = lambda r: [c["check"] for c in res(r).get("reservation_checks") or [] if not c["passed"]]
    raises = lambda rs: dict(collections.Counter(r.get("raise", "not raised") for r in rs))
    declared_ok = lambda r: all(delivered_to_go(r, s) for s in (10, 12, 1))

    case("hidden-subscriber", ["hidden-subscriber"], lambda rs: (
        rs[0]["exit"] == 0 and res(rs[0]).get("inferred_selection") == rs[0]["rtmax"] == (res(rs[0]).get("hidden_subscriber") or {}).get("signal")
        and res(rs[0]).get("hidden_received_before_selection") is True and res(rs[0]).get("kernel_view_indistinguishable") is True
        and res(rs[0]).get("go_reports_ignored_after_release") is True and res(rs[0]).get("hidden_received_after_release") is False,
        "flaw reproduced: the inferred selector took a subscribed signal; release silently cancelled the subscription"))
    case("hidden-subscriber-jsc", ["hidden-subscriber-jsc"], lambda rs: (
        all(res(r).get("hidden_received_after_release") is False and jsc_owns(r, "audit_after_init") for r in rs if r.get("reservation")),
        {"exits": outcome(rs), "raising the taken signal": raises(rs)}), repeat=10)
    case("reserved", ["reserved", "-signal-reserve", "60"], lambda rs: (
        all(r["exit"] == 0 and r["ready"] and not failed(r) and jsc_owns(r, "audit_after_init") and jsc_owns(r, "audit_after_workload")
            and declared_ok(r) and not any("Overriding" in l for l in r["stderr_lines"]) for r in rs),
        {"exits": outcome(rs)}), repeat=10)
    # libc's SIGRTMIN here, but Go's signal table treats 34 as libc-internal
    # (musl's SIGSYNCCALL) and installs no handler: a default disposition
    # that is not availability, so the owner check refuses it.
    case("reserved-34", ["reserved", "-signal-reserve", "34"], lambda rs: (
        rs[0]["exit"] == 0 and rs[0]["ready"] is False and failed(rs[0]) == ["kernel disposition is Go's handler"],
        "refused: Go leaves 34 to libc; its default disposition is not availability"))
    case("reserved-35", ["reserved", "-signal-reserve", "35"], lambda rs: (
        rs[0]["exit"] == 0 and rs[0]["ready"] and jsc_owns(rs[0], "audit_after_init") and declared_ok(rs[0]), "the lowest Go-owned real-time signal"))
    case("reserved-missing", ["reserved"], lambda rs: (
        rs[0]["exit"] == 0 and rs[0]["ready"] is False and "supplied" in failed(rs[0])
        and not any(o.startswith("native:libjavascriptcore") for _, o, _ in rs[0]["non_go"]), "refused; nothing inferred, JSC never initialized"))
    for sig, why in ((10, "SIGUSR1, declared"), (33, "libc-internal"), (65, "beyond SIGRTMAX")):
        case(f"reserved-invalid-{sig}", ["reserved", "-signal-reserve", str(sig)], lambda rs, why=why: (
            rs[0]["exit"] == 0 and rs[0]["ready"] is False and bool(failed(rs[0])), f"refused: {why}"))
    case("reserved-foreign", ["reserved-foreign", "-signal-reserve", "60"], lambda rs: (
        rs[0]["exit"] == 0 and rs[0]["ready"] is False and failed(rs[0]) == ["kernel disposition is Go's handler"],
        "injected native owner: refused"))
    case("reserved-env-mismatch", ["reserved", "-signal-reserve", "60"], lambda rs: (
        rs[0]["exit"] == 0 and rs[0]["ready"] is False and failed(rs[0]) == ["JSC_SIGNAL_FOR_GC unset or equal"], "refused"),
        env={"JSC_SIGNAL_FOR_GC": "63"})
    case("reserved-env-equal", ["reserved", "-signal-reserve", "60"], lambda rs: (
        rs[0]["exit"] == 0 and rs[0]["ready"] and jsc_owns(rs[0], "audit_after_init"), "accepted"), env={"JSC_SIGNAL_FOR_GC": "60"})
    case("reserved-violate-before", ["reserved-violate-before", "-signal-reserve", "60"], lambda rs: (
        all(not failed(r) and res(r).get("hidden_received_after_release") is False for r in rs if r.get("reservation")),
        {"note": "every host check passed; the undeclared subscriber was silently lost", "exits": outcome(rs), "raising the taken signal": raises(rs)}),
        repeat=10)
    case("reserved-violate-after", ["reserved-violate-after", "-signal-reserve", "60"], lambda rs: (
        rs[0]["exit"] == 0 and jsc_owns(rs[0], "audit_after_init") and jsc_owns(rs[0], "audit_after_violation") is False,
        "a later Notify reinstalls Go's handler; the audit sees it after the fact"))


def delivered_to_go(r, sig):
    return any(d["signal"] == sig and d["received_by_go"] for d in r.get("delivery") or [])


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("binary", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--runtime-lib", type=Path, help="diagnostic comparison: run the core cases against these libraries")
    p.add_argument("--set", choices=("default", "reservation"), default="default",
                   help="reservation: the inferred selector's flaw and the explicit-reservation prototype, instead of the default cases")
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    record(binary, args.output)
    cases = []

    def case(name, flags, expect, env=None, repeat=1, strace=False, timeout=40):
        runs = [run(binary, args.output, name if repeat == 1 else f"{name}-{i + 1}", flags, env, timeout, strace) for i in range(repeat)]
        ok, detail = expect(runs)
        cases.append({"case": name, "flags": flags, "env": env or {}, "repeat": repeat, "ok": ok, "detail": detail, "runs": runs})
        print(json.dumps({"case": name, "ok": ok, "detail": detail}), flush=True)

    outcome = lambda runs: collections.Counter(str(r["exit"]) for r in runs)
    sigusr1_jsc = lambda r: any(s == 10 and o.startswith("native:libjavascriptcore") for s, o, _ in r.get("non_go") or [])

    if args.set == "reservation":
        reservation_cases(case, outcome)
        summary = {"binary": str(binary), "set": "reservation", "cases": cases, "passed": all(c["ok"] for c in cases)}
        (args.output / "summary.json").write_text(json.dumps(summary, indent=1, default=str) + "\n")
        print(json.dumps({"passed": summary["passed"], "failed": [c["case"] for c in cases if not c["ok"]]}))
        return 0 if summary["passed"] else 1

    case("baseline", ["baseline"], lambda rs: (rs[0]["exit"] == 0 and sigusr1_jsc(rs[0]) and not delivered_to_go(rs[0], 10)
                                               and any("Overriding existing handler for signal 10" in l for l in rs[0]["stderr_lines"]),
                                               "JSC takes SIGUSR1 with a warning; the application's SIGUSR1 is lost"))
    case("baseline-repeat", ["baseline"], lambda rs: (True, {"exits": outcome(rs), "note": "external SIGUSR1 reaches JSC's suspend handler"}), repeat=10)
    case("baseline-segv", ["baseline", "-signal-fault", "segv"],
         lambda rs: (rs[0]["exit"] == 2 and any("SA_ONSTACK" in l for l in rs[0]["stderr_lines"]), "a recovered Go nil dereference is fatal"))
    case("configured", ["configured"], lambda rs: (
        rs[0]["exit"] == 0 and rs[0]["ready"] and rs[0]["selected"] == rs[0]["rtmax"] and not sigusr1_jsc(rs[0])
        and all(delivered_to_go(rs[0], s) for s in (10, 12, 1)) and not any("Overriding" in l for l in rs[0]["stderr_lines"]),
        f"signal {rs[0].get('selected')} released and configured; Go keeps SIGUSR1/SIGUSR2/SIGHUP; no JSC warning"))
    for fault in ("segv", "bus"):
        case(f"configured-{fault}", ["configured", "-signal-fault", fault],
             lambda rs: (rs[0]["exit"] == 2 and any("SA_ONSTACK" in l for l in rs[0]["stderr_lines"]),
                         "still fatal: JSC's fault handlers, not the GC signal, cause it"))
    case("configured-onstack", ["configured-onstack"], lambda rs: (
        all(r["exit"] == 0 and r["onstack_added"] == [7, 11] and [s for s, _ in r["unprotected"]] == [r["selected"]] for r in rs),
        {"exits": outcome(rs)}), repeat=10)
    for fault in ("segv", "bus"):
        case(f"configured-onstack-{fault}", ["configured-onstack", "-signal-fault", fault],
             lambda rs: (rs[0]["exit"] == 0 and "nil pointer" in str((rs[0].get("fault") or {}).get("recovered")), "recovered"))
        case(f"configured-onstack-webkit-{fault}", ["configured-onstack", "-signal-webkit", "-signal-fault", fault],
             lambda rs: (rs[0]["exit"] == 0 and "nil pointer" in str((rs[0].get("fault") or {}).get("recovered")), "recovered with WebKit up"))
    case("configured-onstack-webkit", ["configured-onstack", "-signal-webkit"], lambda rs: (
        rs[0]["exit"] == 0 and all(delivered_to_go(rs[0], s) for s in (10, 12, 1)) and not rs[0]["onstack_added_after_webkit"],
        {"non_go": rs[0].get("non_go")}))
    case("webkit-first", ["configured", "-signal-webkit", "-signal-order", "webkit-first"], lambda rs: (
        rs[0]["exit"] == 0, {"non_go": rs[0].get("non_go"), "note": "observation: WebKit initializes JSC itself"}))
    case("configured-onstack-all", ["configured-onstack-all"],
         lambda rs: (True, {"exits": outcome(rs), "note": "SA_ONSTACK on the suspend signal: hazard probe"}), repeat=10, timeout=20)
    case("late", ["late"], lambda rs: (rs[0]["exit"] == 0 and rs[0]["configure_after_init"] is False and not rs[0]["ready"] and sigusr1_jsc(rs[0]),
                                       "refused after initialization; the collision remains and readiness is false"))
    case("env", ["env"], lambda rs: (rs[0]["exit"] == 0 and rs[0]["ready"] and rs[0]["selected"] == 63 and not sigusr1_jsc(rs[0])
                                     and any(s == 63 and o.startswith("native:libjavascriptcore") for s, o, _ in rs[0]["non_go"]),
                                     "operator's signal released and used"), env={"JSC_SIGNAL_FOR_GC": "63"})
    case("env-invalid", ["env"], lambda rs: (rs[0]["exit"] == 0 and rs[0]["ready"] is False, "refused"), env={"JSC_SIGNAL_FOR_GC": "10"})
    case("no-symbol", ["no-symbol"], lambda rs: (rs[0]["exit"] == 0 and rs[0]["ready"] is False and not any(o.startswith("native:libjavascriptcore") for _, o, _ in rs[0]["non_go"]),
                                                 "injected: refused before JSC initialization"))
    case("no-signal", ["no-signal"], lambda rs: (rs[0]["exit"] == 0 and rs[0]["ready"] is False
                                                 and not any(o.startswith("native:libjavascriptcore") for _, o, _ in rs[0]["non_go"]),
                                                 "injected: refused before JSC initialization"))
    case("strace-one-thread", ["configured-onstack"], lambda rs: (rs[0]["exit"] == 0, {"delivered": rs[0].get("delivered")}), strace=True, timeout=90)
    case("strace-two-threads", ["configured-onstack", "-signal-js-threads", "2"],
         lambda rs: (rs[0]["exit"] == 0, {"delivered": rs[0].get("delivered"), "second_thread": rs[0].get("second_thread")}), strace=True, timeout=90)
    case("two-threads", ["configured-onstack", "-signal-js-threads", "2"], lambda rs: (True, {"exits": outcome(rs)}), repeat=10)
    if args.runtime_lib:
        lib = str(args.runtime_lib)
        comparison = {"LD_LIBRARY_PATH": lib, "WEBKIT_INJECTED_BUNDLE_PATH": lib + "/webkit2gtk-4.1/injected-bundle"}
        case("comparison-baseline-segv", ["baseline", "-signal-fault", "segv"],
             lambda rs: (rs[0]["exit"] == 2, "comparison libraries: same fatal fault"), env=comparison)
        case("comparison-configured-onstack-webkit-segv", ["configured-onstack", "-signal-webkit", "-signal-fault", "segv"],
             lambda rs: (rs[0]["exit"] == 0 and "nil pointer" in str((rs[0].get("fault") or {}).get("recovered")), "comparison libraries: recovered"),
             env=comparison)
    summary = {"binary": str(binary), "cases": cases, "passed": all(c["ok"] for c in cases)}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=1, default=str) + "\n")
    print(json.dumps({"passed": summary["passed"], "failed": [c["case"] for c in cases if not c["ok"]]}))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
