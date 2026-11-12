#!/usr/bin/env python3
"""Michael's manual error-page check, on the VM-free candidate.

    python3 -B manual_check.py BINARY OUTDIR --reserve N [--step-timeout SECONDS]

One window, attached to a stoppable server this driver owns. The driver
sets up each failure and waits for a person to click; it never clicks.

  Step 1, failed GET: the page navigates to a GET while the server is down,
  the error page appears, the server comes back. Click Retry. The server
  must see exactly one more GET and no edit.

  Step 2, failed POST: the page submits its form while the server is down,
  the error page appears with only Return to start, the server comes back.
  Click Return to start. The server must see no POST at all.

Prompts and results go to stdout and OUTDIR/result.json. The automated
counts are not the manual result: record what the person saw separately.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import sys
import time
import urllib.parse

import feasibility as fz
import vmfree as vf
from exitwatch import enable_subreaper
from feasibility_env import record

q = urllib.parse.quote


def say(**kv):
    print(json.dumps(kv), flush=True)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("binary", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--reserve", type=int, required=True)
    p.add_argument("--step-timeout", type=float, default=1800)
    p.add_argument("--root", default="/tmp/dfx2-feasibility")
    p.add_argument("--self-test", action="store_true", help="the driver clicks, to check this script; never the manual result")
    args = p.parse_args()
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=True)
    enable_subreaper()
    record(args.binary.resolve(), out)
    root = Path(args.root)
    xdg = root / f"xdg-manual-{os.getpid()}"
    state = {"cached": 0}
    attach = fz.Server("attachment", fz.attachment_handle(state))
    url = attach.origin + "/"
    edits = lambda: [r for r in attach.snapshot() if r["method"] == "POST" and r["path"] != "/api/report"]
    result = {"binary": str(args.binary), "flags": ["-feas-vmfree", "-signal-reserve", str(args.reserve)], "steps": {},
              "manual_observation": "pending: Michael's report, recorded separately", "self_test": args.self_test}
    if args.self_test:
        result["manual_observation"] = "not applicable: self-test, the driver clicked"
    click = lambda at: host.click("M", at) if args.self_test else None
    host = vf.VHost(args.binary.resolve(), out, ["-feas-root", str(root), *result["flags"]], xdg, None)
    try:
        host.open("M", "attach", url)

        # Step 1: a failed GET, then Retry by hand.
        since = len(vf.server_reports(state))
        m = host.send(f"load M {url}?run=armed&id=g&delay=1500&then=go&to={q('api/query?x=manual')}")
        host.await_report("M", "g", m, state, since)
        attach.stop()
        failed, page = host.error_page("M", m)
        host.snapshot("M", out / "step1-error-get.png")
        attach.start()
        gets = attach.count("GET", "/api/query")
        mark = host.send("status")
        say(manual="STEP 1 of 2", do="In the window titled 'dfx2 feasibility M', click Retry.", failed_uri=failed["uri"],
            retry_offered=page["retry"])
        click(page["retry_at"])
        action = host.expect("error-action", mark, window="M", timeout=args.step_timeout)
        loaded = host.expect("load", host.last, window="M", load_event=3, timeout=30)
        time.sleep(0.5)
        result["steps"]["1 failed GET, Retry"] = {
            "error_page": {"uri": failed["uri"], "method": failed["method"], "retry_offered": page["retry"]},
            "clicked": action["action"], "loaded": loaded["uri"],
            "server": {"GET /api/query before": gets, "after": attach.count("GET", "/api/query"), "edits": edits()},
            "ok": action["action"] == "retry" and attach.count("GET", "/api/query") == gets + 1 and not edits()}
        say(manual="STEP 1 recorded", **result["steps"]["1 failed GET, Retry"])

        # Step 2: a failed POST, then Return to start by hand.
        host.load("M", url)
        since = len(vf.server_reports(state))
        m = host.send(f"load M {url}?run=armed&id=p&delay=1500&then=submit")
        host.await_report("M", "p", m, state, since)
        attach.stop()
        failed, page = host.error_page("M", m)
        host.snapshot("M", out / "step2-error-post.png")
        attach.start()
        starts = attach.count("GET", "/")
        mark = host.send("status")
        say(manual="STEP 2 of 2", do="Click Return to start. (There should be no Retry button.)", failed_uri=failed["uri"],
            method=failed["method"], retry_offered=page["retry"])
        click(page["start_at"])
        action = host.expect("error-action", mark, window="M", timeout=args.step_timeout)
        loaded = host.expect("load", host.last, window="M", load_event=3, timeout=30)
        time.sleep(0.5)
        result["steps"]["2 failed POST, Return to start"] = {
            "error_page": {"uri": failed["uri"], "method": failed["method"], "retry_offered": page["retry"]},
            "clicked": action["action"], "loaded": loaded["uri"],
            "server": {"GET / before": starts, "after": attach.count("GET", "/"), "edits": edits()},
            "ok": action["action"] == "start" and not page["retry"] and loaded["uri"] == url and not edits()}
        say(manual="STEP 2 recorded", **result["steps"]["2 failed POST, Return to start"])
        say(manual="done", do="Report what you saw; the window closes now.")
    except fz.Failure as err:
        result["failure"] = str(err)
        say(manual="stopped", failure=str(err))
    finally:
        observed = host.finish(timeout=60)
        result["exit"] = {k: observed[k] for k in ("host_exit", "timed_out", "children_pending", "child_failure", "fatal_log")}
        try:
            attach.stop()
        except OSError:
            pass
        shutil.rmtree(xdg, ignore_errors=True)
        (out / "result.json").write_text(json.dumps(result, indent=1, default=str) + "\n")
    return 0 if "failure" not in result and all(s["ok"] for s in result["steps"].values()) else 1


if __name__ == "__main__":
    sys.exit(main())
