#!/usr/bin/env python3
"""The no-host-VM investigation: the feasibility host's required paths with
no JavaScriptCore VM in the Go process, watched from outside.

    python3 -B vmfree.py BINARY OUTDIR --debug-dir DIR --reserve N [--only NAME ...]

DIR holds .build-id links to Ubuntu's debuginfod files for the installed
libjavascriptcoregtk (required: the watched functions are local symbols)
and, for readable backtraces, libwebkit2gtk. N is the application's explicit
GC-signal reservation, never inferred.

Every host runs under gdb with vmwatch_gdb.py, which logs JSC entry points,
VM construction, JSLock acquisition, GC thread registration, thread
suspension, fault-handler finalization, real-time thread registration and
native sigaction calls in the Go host only; WebKit's children are never
followed. Two positive controls run first, each in its own disposable
process: the direct JSC probe (a VM on the Go main thread) and the ordinary
feasibility host running one native JavaScript call. The VM-free host is
started with -feas-vmfree: it binds no evaluation or result-conversion
function and refuses `js`. Its scenarios are page-driven (?run=..., results
posted to each window's own handler) and use native load/reload and
synthesized GTK pointer input for the error page. Judgments are server-side
counters, native events and the monitor's totals.
"""
import argparse
import http.client
import json
import os
from pathlib import Path
import shutil
import sys
import threading
import time
import urllib.parse

import feasibility as fz
from exitwatch import enable_subreaper
from feasibility_env import record

HERE = Path(__file__).resolve().parent
JSC = "/usr/lib/x86_64-linux-gnu/libjavascriptcoregtk-4.1.so.0"
# Totals that must stay zero in the VM-free host.
VM_KINDS = ["vm-constructed", "heap-constructed", "global-context-create", "global-context-create-in-group", "context-group-create",
            "jsc-context-new", "jsc-virtual-machine-new", "jslock-acquired", "thread-registered-for-gc", "thread-group-add",
            "thread-suspend", "suspend-handler-ran", "fault-handlers-finalized"]
q = urllib.parse.quote


def monitor(debug_dir, log):
    wrap = ["gdb", "-q", "-batch", "-nx", "-ex", f"set debug-file-directory {debug_dir}:/usr/lib/debug", "-x", str(HERE / "vmwatch_gdb.py"), "--args"]
    return wrap, {"VMWATCH_LOG": str(log)}


def watched(log):
    hits, totals, exit_ = [], None, None
    for line in Path(log).read_text().splitlines():
        e = json.loads(line)
        if e["kind"] == "totals":
            totals = e["watches"]
        elif e["kind"] == "inferior-exit":
            exit_ = e
        elif e["kind"] not in ("monitor-start", "run-error"):
            hits.append(e)
    return hits, totals, exit_


def summarize(hits, totals):
    """Totals, plus each kind's first hit: thread and the frames that matter."""
    first = {}
    for h in hits:
        if h["kind"] not in first and "main_thread" in h:
            first[h["kind"]] = {"main_thread": h["main_thread"], "thread_name": h["thread_name"], "go_frames": h["go_frames"][:4],
                                "frames": h["frames"][:10], **({"signal": h["signal"]} if "signal" in h else {})}
    # Who installed each native handler: the first caller outside libc.
    sigactions = sorted({(h["signal"], h["thread_name"], next((f for f in h["frames"] if "@ libc.so" not in f), "?"))
                         for h in hits if h["kind"] == "libc-sigaction" and h.get("installs")})
    return {"totals": totals, "first_hits": first, "native_sigaction_installs": sigactions}


class VHost(fz.Host):
    def __init__(self, binary, out, flags, xdg, debug_dir, env=None):
        self.watch_log = out / "vmwatch.jsonl"
        wrap, menv = monitor(debug_dir, self.watch_log) if debug_dir else ((), {})  # no debug_dir: unmonitored
        super().__init__(binary, out, flags, xdg, wrap=wrap, env={**menv, **(env or {})})
        self.origins = {}

    def open(self, label, mode, url="", loaded=True):
        m = self.send(f"open {label} {mode} {url}".strip())
        e = self.expect("opened", m, window=label)
        self.origins[label] = url.rstrip("/") if mode == "attach" else e["origin"]
        if loaded:
            self.expect("load", m, window=label, load_event=3)
        return m

    def page(self, label, query, rid, server=None, timeout=30):
        """Load the probe page with ?run=...&id=rid and wait for its report:
        a host event for owned windows, the attachment server's record for
        attached ones."""
        since = len(server_reports(server)) if server else None
        m = self.send(f"load {label} {self.origins[label]}/?{query}&id={rid}")
        return self.await_report(label, rid, m, server, since, timeout)

    def await_report(self, label, rid, mark, server=None, since=0, timeout=30):
        if server:
            state = server
            deadline = time.monotonic() + timeout
            with state["lock"]:
                while True:
                    for r in state.get("reports", [])[since:]:
                        if r["id"] == rid:
                            return r
                    left = deadline - time.monotonic()
                    if left <= 0:
                        raise fz.Failure(f"timed out waiting for report {rid}")
                    state["lock"].wait(left)
        deadline = time.monotonic() + timeout
        while True:
            e = self.expect("report", mark, window=label, timeout=max(0.1, deadline - time.monotonic()))
            if e["report"]["id"] == rid:
                if e["report"].get("error"):
                    raise fz.Failure(f"report {rid}: {e['report']['error']}")
                return e["report"]
            mark = self.last

    def reload(self, label, rid, mark=None):
        m = self.send(f"reload {label}")
        return self.await_report(label, rid, m)

    def click(self, label, at):
        return self.expect("click", self.send(f"click {label} {at[0]} {at[1]}"), window=label)

    def error_page(self, label, since):
        failed = super().error_page(label, since)
        page = self.expect("error-page", since, window=label, uri=failed["uri"])
        return failed, page


def server_reports(state):
    with state["lock"]:
        return list(state.get("reports", []))


class Run(fz.Run):
    def __init__(self, args, env=None, debug_dir=None):
        super().__init__(args)
        self.flags = ["-feas-vmfree", "-signal-reserve", str(args.reserve)]
        self.env, self.debug_dir = env or {}, debug_dir or args.debug_dir
        self.monitored = bool(self.debug_dir)

    def vscenario(self, name, body, flags=None):
        if self.args.only and name not in self.args.only:
            return
        out = self.args.output / name
        out.mkdir(parents=True, exist_ok=True)
        xdg = self.root / f"xdg-{name}-{os.getpid()}"
        result = {"scenario": name, "flags": self.flags if flags is None else flags, "checks": [], "ok": False}
        host = None
        started = time.monotonic()
        try:
            host = VHost(self.binary, out, ["-feas-root", str(self.root), *result["flags"]], xdg, self.debug_dir, self.env)
            result["detail"] = body(host, result, out)
        except (fz.Failure, OSError, KeyError, TypeError, ValueError) as err:
            result["failure"] = f"{type(err).__name__}: {err}"
        finally:
            if host:
                observed = host.finish(timeout=60)
                result["exit"] = {k: observed[k] for k in ("host_exit", "timed_out", "children_pending", "child_failure", "fatal_log")}
                result["observed_processes"] = sorted({p.get("name") for p in observed["observed_processes"]})
                result["signals"] = {e["stage"]: e["dispositions"] for e in host.all if e.get("event") == "signals"}
                result["reservation"] = (host.ready or {}).get("reservation")
                exit_event = [e for e in host.all if e.get("event") == "exit"]
                result["filter_store_removal"] = exit_event[-1].get("filter_store_removal") if exit_event else None
                result["refused_js"] = len([e for e in host.all if e.get("event") == "error" and e.get("command") == "js"])
                shutil.rmtree(xdg, ignore_errors=True)
        hits, totals, inferior = watched(out / "vmwatch.jsonl") if (out / "vmwatch.jsonl").exists() else ([], None, None)
        if self.monitored:
            self.monitor_checks(result, hits, totals, inferior)
        else:
            result["monitor"] = "not run: browser cases only (--no-monitor)"
        end = result.get("signals", {}).get("exit", {})
        fz.check(result, "SIGSEGV and SIGBUS still Go's at exit", end.get("SIGSEGV", {}).get("owner") == "go" and end.get("SIGBUS", {}).get("owner") == "go",
                 exit_signals=end)
        if hasattr(self, "judge"):
            self.judge(result, hits, totals)
        result["seconds"] = round(time.monotonic() - started, 1)
        ex = result.get("exit", {})
        clean = ex.get("host_exit") == 0 and not any(ex.get(k) for k in ("timed_out", "children_pending", "fatal_log", "child_failure"))
        result["ok"] = "failure" not in result and all(c["ok"] for c in result["checks"]) and clean
        (out / "result.json").write_text(json.dumps(result, indent=1, default=str) + "\n")
        self.results.append(result)
        print(json.dumps({"scenario": name, "ok": result["ok"], "failure": result.get("failure"),
                          "failed_checks": [c["check"] for c in result["checks"] if not c["ok"]], "exit": result.get("exit"),
                          "vm_hits": {k: v["hits"] for k, v in (totals or {}).items() if v["hits"]}}), flush=True)

    def monitor_checks(self, result, hits, totals, inferior):
        result["monitor"] = summarize(hits, totals)
        result["monitor"]["inferior_exit"] = inferior
        unresolved = [k for k, v in (totals or {}).items() if (v["locations"] == 0 or v["pending"]) and not v.get("optional")]
        fz.check(result, "monitor: every watched function resolved in the host", totals is not None and not unresolved, unresolved=unresolved,
                 optional_unresolved=[k for k, v in (totals or {}).items() if (v["locations"] == 0 or v["pending"]) and v.get("optional")])
        fz.check(result, "monitor: no host VM, JSLock, GC thread registration, suspension or fault-handler finalization",
                 totals is not None and all(totals[k]["hits"] == 0 for k in VM_KINDS),
                 nonzero={k: totals[k]["hits"] for k in VM_KINDS if totals and totals[k]["hits"]})
        fz.check(result, "monitor: WebKit initialized JSC in the host", totals is not None and totals["jsc-initialize"]["hits"] > 0)


check = fz.check

# ------------------------------------------------------------ positive controls


def controls(run):
    """Each control is its own process; nothing here touches a VM-free host."""
    out = run.args.output
    results = []

    def control(name, command, feed=None, expect=None):
        d = out / name
        d.mkdir(parents=True, exist_ok=True)
        wrap, env = monitor(run.args.debug_dir, d / "vmwatch.jsonl")
        xdg = run.root / f"xdg-{name}-{os.getpid()}"
        for sub in ("data", "cache", "config"):
            (xdg / sub).mkdir(parents=True, exist_ok=True)
        env.update(XDG_DATA_HOME=str(xdg / "data"), XDG_CACHE_HOME=str(xdg / "cache"), XDG_CONFIG_HOME=str(xdg / "config"))
        import subprocess
        with open(d / "stdout", "w") as so, open(d / "stderr", "w") as se:
            p = subprocess.Popen([*wrap, str(run.binary), *command], stdin=subprocess.PIPE, stdout=so, stderr=se, text=True,
                                 env=dict(os.environ, **env), start_new_session=True)
            if feed:
                feed(p, d / "stdout")
            try:
                code = p.wait(timeout=120)
            except subprocess.TimeoutExpired:
                os.killpg(p.pid, 9)
                code = "hang"
        shutil.rmtree(xdg, ignore_errors=True)
        hits, totals, inferior = watched(d / "vmwatch.jsonl")
        s = summarize(hits, totals)
        r = {"control": name, "command": command, "exit": code, "monitor": s}
        r["ok"] = code == 0 and expect(hits, totals, r)
        (d / "result.json").write_text(json.dumps(r, indent=1, default=str) + "\n")
        results.append(r)
        print(json.dumps({"control": name, "ok": r["ok"], "exit": code, "hits": {k: v["hits"] for k, v in totals.items() if v["hits"]}}), flush=True)

    def go_main_registered(hits, totals, r):
        reg = [h for h in hits if h["kind"] == "thread-registered-for-gc"]
        r["registered_threads"] = [{"main_thread": h["main_thread"], "thread_name": h["thread_name"], "go_frames": h["go_frames"][:3]} for h in reg]
        return (totals["vm-constructed"]["hits"] > 0 and totals["fault-handlers-finalized"]["hits"] > 0
                and any(h["main_thread"] and h["go_frames"] for h in reg))

    if not run.args.only or "control-direct" in run.args.only:
        control("control-direct", ["-signal-probe", "suspension-transition", "-signal-reserve", str(run.args.reserve)],
                expect=lambda h, t, r: go_main_registered(h, t, r) and t["thread-suspend"]["hits"] > 0)

    def feed_js(p, stdout):
        def wait_for(pred, timeout=60):
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                for line in stdout.read_text().splitlines():
                    try:
                        e = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if pred(e):
                        return e
                time.sleep(0.1)
            raise fz.Failure("control host timed out")
        try:
            wait_for(lambda e: e.get("event") == "ready")
            p.stdin.write("open A loopback\n")
            p.stdin.flush()
            wait_for(lambda e: e.get("event") == "load" and e.get("load_event") == 3)
            p.stdin.write("js A return 1\n")
            p.stdin.flush()
            wait_for(lambda e: e.get("event") == "js")
        finally:
            p.stdin.write("quit\n")
            p.stdin.flush()

    if not run.args.only or "control-webkit-js" in run.args.only:
        control("control-webkit-js", ["-feasibility", "-feas-root", str(run.root)], feed=feed_js, expect=go_main_registered)
    return results


# ------------------------------------------------------------ scenarios


def loopback_basics(host, result, out):
    host.open("A", "loopback")
    checks = host.page("A", "run=checks", "c")["result"]
    check(result, "assets, query and POST through the credential", checks["css"] and checks["img"] and checks["query"] == "é&ü" and checks["echoLength"] == 1005, checks=checks)
    check(result, "credential invisible to JavaScript", checks["cookieVisible"] is False)
    again = host.reload("A", "c")["result"]
    installs = len(host.events("cookie-installed", window="A"))
    check(result, "native reload keeps authentication without reprovisioning", again["css"] and again["query"] == "é&ü" and installs == 1, installs=installs)
    before = host.status()["A"]
    loaded = host.load("A", host.origins["A"] + "/api/redirect")
    after = host.status()["A"]
    check(result, "same-origin redirect authenticated, no credential in the URL",
          loaded["uri"].endswith("/api/query?x=redirected") and after["handler"].get("GET /api/query", 0) == before["handler"].get("GET /api/query", 0) + 1
          and after["endpoint"]["refused"] == before["endpoint"]["refused"], uri=loaded["uri"])
    chunks = host.page("A", "run=stream", "s")["result"]
    texts = "".join(c["text"] for c in chunks)
    check(result, "loopback stream: chunk 1 visible before the handler finished", len(chunks) >= 2 and "chunk-2 after ack" in texts, chunks=chunks)
    port = host.status()["A"]["port"]
    statuses = {}
    for name, headers in (("none", {}), ("wrong credential", {"Cookie": fz.CREDENTIAL + "=wrong"}),
                          ("wrong credential and Origin", {"Cookie": fz.CREDENTIAL + "=wrong", "Origin": "http://127.0.0.1:1"})):
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        conn.request("GET", "/api/query?x=external", headers=headers)
        statuses[name] = conn.getresponse().status
        conn.close()
    self_test = host.expect("selftest", host.send("selftest A"), window="A")["statuses"]
    st = host.status()["A"]
    check(result, "external clients without the right credential refused", all(s == 403 for s in statuses.values()), statuses=statuses)
    check(result, "right credential: wrong Host or Origin refused; exact or absent Origin admitted",
          self_test == {"right credential": 200, "right credential, wrong Host": 403, "right credential, wrong Origin": 403, "right credential, exact Origin": 200},
          selftest=self_test)
    check(result, "refusals never reached the handler", st["endpoint"]["refused"] == {"no-credential": 1, "wrong-credential": 2, "host": 1, "origin": 1}
          and st["endpoint"]["handled"] == st["endpoint"]["admitted"], endpoint=st["endpoint"])
    js = host.expect("error", host.send("js A return 1"), command="js")
    check(result, "the VM-free host refuses native JavaScript", "refused" in js["error"])
    host.snapshot("A", out / "loopback-page.png")
    return {"status": st}


def confinement(host, result, out, filter_on=True, csp_on=True):
    collector = fz.Server("collector", fz.collector_handle)
    try:
        host.open("A", "loopback")
        host.open("B", "loopback")
        home = host.origins["A"] + "/"
        b_before = host.status()["B"]["handler"]
        seen = host.page("A", f"run=attacks&collector={q(collector.origin)}&other={q(host.origins['B'])}", "atk", timeout=45)["result"]
        marks, final = {}, {}
        for how in ("location", "redirect", "form"):
            marks[how] = host.send(f"load A {home}?run=armed&id=nav-{how}&then=navigate&how={how}&collector={q(collector.origin)}")
            host.await_report("A", f"nav-{how}", marks[how])
            time.sleep(1.5)
            final[how] = host.status()["A"]["uri"]
            if not final[how].startswith(home):
                host.load("A", home)
        marks["popup"] = len(host.all)
        popup = host.page("A", f"run=popup&collector={q(collector.origin)}", "popup")["result"]
        time.sleep(1)
        got = collector.snapshot()
        b_after = host.status()["B"]["handler"]
        enforced = filter_on or csp_on
        leaked = [r for r in got if r["credential"]]
        check(result, "collector received nothing" if enforced else "control: collector receives requests",
              (not got) if enforced else bool(got), received=[(r["method"], r["path"], r["credential"]) for r in got])
        check(result, "no credential reached the collector" if enforced else "control: credential leaks without enforcement",
              (not leaked) if enforced else bool(leaked), leaked=len(leaked))
        check(result, "A's page never invoked B's handler", b_after.get("GET /api/count", 0) == b_before.get("GET /api/count", 0), b_before=b_before, b_after=b_after)
        refused = lambda how, path: any(e["uri"].startswith(collector.origin + path) for e in host.events("navigation-refused", marks[how], window="A"))
        arrived = lambda path: any(r["path"].startswith(path) for r in got)
        mechanism = {
            "location": "navigation policy" if refused("location", "/nav") else ("silent" if not arrived("/nav") else "NOT REFUSED"),
            "redirect": "navigation policy" if refused("redirect", "/redirect-nav") else ("silent" if not arrived("/redirect-nav") else "NOT REFUSED"),
            "form": "navigation policy" if refused("form", "/form") else ("silent" if not arrived("/form") else "NOT REFUSED"),
            "popup": "create signal" if host.events("popup-refused", marks["popup"], window="A") else f"silent (window.open returned {popup})",
        }
        check(result, "top-level navigation, redirect, popup and form to another port never left the origin",
              all(u.startswith(home) for u in final.values()) and "NOT REFUSED" not in mechanism.values(), mechanism=mechanism, final_uris=final)
        return {"page_view": seen, "mechanism": mechanism, "collector": got,
                "refusals": [(e["kind"], e["uri"].replace(collector.origin, "COLLECTOR")) for e in host.events("navigation-refused", window="A")]}
    finally:
        collector.stop()


def inprocess_isolation(host, result, out):
    collector = fz.Server("collector", fz.collector_handle)
    try:
        host.open("I1", "inprocess")
        host.open("I2", "inprocess")
        checks = host.page("I1", "run=checks", "c")["result"]
        check(result, "in-process page, assets, query and POST", checks["css"] and checks["query"] == "é&ü" and checks["echoLength"] == 1005, checks=checks)
        before = host.status()
        host.page("I1", f"run=count&url={q('dfx2://spike/api/count')}", "count")
        after = host.status()
        check(result, "naming the shared origin reaches only the window's own handler",
              after["I1"]["handler"].get("GET /api/count", 0) == before["I1"]["handler"].get("GET /api/count", 0) + 1
              and after["I2"]["handler"].get("GET /api/count", 0) == before["I2"]["handler"].get("GET /api/count", 0), i1=after["I1"]["handler"], i2=after["I2"]["handler"])
        host.page("I1", f"run=attacks&collector={q(collector.origin)}", "atk", timeout=45)
        time.sleep(1)
        check(result, "an in-process page cannot reach the collector", not collector.snapshot(), received=collector.snapshot())
        i1 = host.page("I1", "run=storageSet&tag=I1", "set")["result"]
        i2 = host.page("I2", "run=storageRead", "read")["result"]
        check(result, "same in-process origin, separate sessions: I2 cannot read I1's storage", i2 == {"cookie": "", "local": None, "session": None}, i1=i1, i2=i2)
        return {}
    finally:
        collector.stop()


def storage(host, result, out):
    state = {"cached": 0}
    attach = fz.Server("attachment", fz.attachment_handle(state))
    try:
        url = attach.origin + "/"
        host.open("A", "attach", url)
        host.open("B", "attach", url)
        a = host.page("A", "run=storageSet&tag=A", "a-set", state)["result"]
        b = host.page("B", "run=storageRead", "b-read", state)["result"]
        check(result, "B cannot read A's cookie, local or session storage", b == {"cookie": "", "local": None, "session": None}, a=a, b=b)
        host.page("B", "run=storageSet&tag=B", "b-set", state)
        cached = lambda: attach.count("GET", "/api/cached")
        n0 = cached()
        host.page("A", "run=cached", "a-cached-1", state)
        host.page("A", "run=cached", "a-cached-2", state)
        n1 = cached()
        host.page("A", "run=cachedImage", "a-image", state)
        n2 = cached()
        host.page("B", "run=cachedImage", "b-image", state)
        n3 = cached()
        check(result, "B's request for A's cached resource reaches the server", n3 > n2, server_requests={"A fetch x2": n1 - n0, "A image x2": n2 - n1, "B image x2": n3 - n2})
        since = len(server_reports(state))
        host.page("A", "run=storageRead", "a-read", state)
        m = host.send("reload A")
        reloaded = host.await_report("A", "a-read", m, state, since + 1)["result"]
        check(result, "native reload keeps A's session", reloaded == {"cookie": "vis=A", "local": "A", "session": "A"}, a=reloaded)
        m = host.send("close A")
        during = [host.page("B", f"run=query&x=during{i}", f"b-during-{i}", state)["result"] for i in range(3)]
        host.expect("closed", m, window="A")
        check(result, "B kept working while A closed", during == [{"x": f"during{i}"} for i in range(3)], during=during)
        host.open("A2", "attach", url)
        a2 = host.page("A2", "run=storageRead", "a2-read", state)["result"]
        b_after = host.page("B", "run=storageRead", "b-read-2", state)["result"]
        check(result, "reopened A has a fresh session", a2 == {"cookie": "", "local": None, "session": None}, a2=a2)
        check(result, "B is unchanged", b_after == {"cookie": "vis=B", "local": "B", "session": "B"}, b=b_after)
        for label in ("B", "A2"):
            host.close(label)
        conn = http.client.HTTPConnection("127.0.0.1", attach.port, timeout=5)
        conn.request("GET", "/api/query?x=after-browser")
        survived = conn.getresponse().status
        check(result, "the attachment server outlives its browsers", survived == 200, status=survived)
        return {}
    finally:
        attach.stop()


def no_action(host, label, since, wait=2.0):
    time.sleep(wait)
    return not host.events("error-action", since, window=label)


def error_pages(host, result, out):
    state = {"cached": 0}
    attach = fz.Server("attachment", fz.attachment_handle(state))
    url = attach.origin + "/"
    try:
        host.open("E", "attach", url)
        # A later page-initiated GET fails while the service is down.
        since = len(server_reports(state))
        m = host.send(f"load E {url}?run=armed&id=g1&delay=1500&then=go&to={q('api/query?x=later')}")
        host.await_report("E", "g1", m, state, since)
        attach.stop()
        failed, page = host.error_page("E", m)
        host.snapshot("E", out / "error-get.png")
        check(result, "failed GET: Retry and Return to start, no script", page["retry"] and not page["script"], failed=failed, page=page)
        attach.start()
        gets = attach.count("GET", "/api/query")
        m = host.send("status")
        host.click("E", page["retry_at"])
        host.expect("error-action", m, window="E", action="retry")
        host.expect("load", m, window="E", load_event=3)
        check(result, "clicked Retry repeats the failed GET once the service returns", attach.count("GET", "/api/query") == gets + 1)
        # A page that names an action itself is refused: wrong nonce.
        since = len(server_reports(state))
        m = host.send(f"load E {url}?run=armed&id=forge&then=go&to={q('dfx2-error:retry/forged')}")
        host.await_report("E", "forge", m, state, since)
        check(result, "a page-supplied action is refused", bool(host.expect("error-action-refused", m, window="E")))
        # A failed POST navigation offers only Return to start.
        since = len(server_reports(state))
        m = host.send(f"load E {url}?run=armed&id=p1&delay=1500&then=submit")
        host.await_report("E", "p1", m, state, since)
        attach.stop()
        failed_post, post_page = host.error_page("E", m)
        host.snapshot("E", out / "error-post.png")
        check(result, "failed POST shows only Return to start", not post_page["retry"] and failed_post["method"] in ("POST", "FORM"), failed=failed_post)
        attach.start()
        m = host.send("status")
        host.click("E", post_page["retry_at"])
        check(result, "a click where Retry would be does nothing on the POST page", no_action(host, "E", m))
        m = host.send("status")
        host.click("E", post_page["start_at"])
        host.expect("error-action", m, window="E", action="start")
        host.expect("load", m, window="E", load_event=3)
        edits = lambda: [r for r in attach.snapshot() if r["method"] == "POST" and r["path"] != "/api/report"]
        check(result, "clicked Return to start is a GET and replays nothing", not edits(), edits=edits())
        # POST, 303 to a failing GET: Retry repeats only the GET.
        since = len(server_reports(state))
        m = host.send(f"load E {url}?run=armed&id=p303&then=submit&action={q('/post-303')}")
        failed_303, page_303 = host.error_page("E", m)
        check(result, "a GET reached by 303 after a POST offers Retry of the GET", failed_303["method"] == "GET" and failed_303["from_post"] and page_303["retry"], failed=failed_303)
        m = host.send("status")
        host.click("E", page_303["retry_at"])
        host.expect("error-action", m, window="E", action="retry")
        failed_again, page_again = host.error_page("E", m)
        check(result, "Retry after the 303 replays no POST", attach.count("POST", "/post-303") == 1 and attach.count("GET", "/broken") == 2,
              post_303=attach.count("POST", "/post-303"), broken=attach.count("GET", "/broken"))
        m = host.send("status")
        host.click("E", page_again["start_at"])
        host.expect("load", m, window="E", load_event=3, uri=url)
        # POST, 307 keeps the method: the failure is a POST, so no Retry.
        m = host.send(f"load E {url}?run=armed&id=p307&then=submit&action={q('/post-307')}")
        failed_307, page_307 = host.error_page("E", m)
        check(result, "a POST preserved by 307 offers no Retry", failed_307["method"] == "POST" and not page_307["retry"]
              and attach.count("POST", "/post-307") == 1 and attach.count("POST", "/broken-post") == 1, failed=failed_307)
        m = host.send("status")
        host.click("E", page_307["start_at"])
        host.expect("load", m, window="E", load_event=3, uri=url)
        # Ordinary HTTP errors, failed fetches and failed assets keep their page.
        m = host.send("status")
        def titled(path):
            host.load("E", attach.origin + path)
            time.sleep(0.3)  # the title can follow load-finished
            return host.status()["E"].get("title")
        missing, e500 = titled("/missing"), titled("/e500")
        broken = host.page("E", "run=broken", "broken", state)["result"]
        check(result, "404 and 500 display; a failed fetch or asset leaves the page", missing == "404 from the server" and e500 == "500 from the server"
              and not host.events("load-failed", m, window="E") and broken["fetched"].startswith("rejected") and broken["path"] == "/" and broken["form"],
              missing=missing, e500=e500, broken=broken)
        # The initial load failing: the service is down before the window opens.
        attach.stop()
        m = host.open("F", "attach", url, loaded=False)
        initial, initial_page = host.error_page("F", m)
        attach.start()
        m = host.send("status")
        host.click("F", initial_page["retry_at"])
        host.expect("error-action", m, window="F", action="retry")
        loaded = host.expect("load", m, window="F", load_event=3)
        check(result, "initial-load failure, then clicked Retry", loaded["uri"] == url and not host.events("load-failed", m, window="F"), initial=initial)
        return {"edits": edits()}
    finally:
        try:
            attach.stop()
        except OSError:
            pass


def owned_load_failure(host, result, out):
    for label, mode in (("L", "loopback"), ("P", "inprocess")):
        host.open(label, mode)
        m = host.send(f"load {label} {host.origins[label]}/api/abort")
        failed, page = host.error_page(label, m)
        host.snapshot(label, out / f"error-{mode}.png")
        check(result, f"{mode}: native load failure shows the error page", page["retry"] and not page["script"], failed=failed)
        m = host.send("status")
        host.click(label, page["start_at"])
        host.expect("error-action", m, window=label, action="start")
        loaded = host.expect("load", m, window=label, load_event=3)
        check(result, f"{mode}: clicked Return to start reloads the start page", loaded["uri"] == host.origins[label] + "/", uri=loaded["uri"])
    return {}


def shutdown(mode, how):
    def body(host, result, out):
        host.open("S", mode)
        if how == "inspector":
            host.expect("inspector", host.send("inspect S"), window="S", attached=True)
        if how in ("pending", "inspector"):
            host.page("S", "run=slow", "slow")
            time.sleep(0.3)
        closed = host.close("S")
        pending = closed["handler"].get("GET /api/slow", 0)
        check(result, f"{how} close drains", closed["native_async"] == 0 and closed["native_requests"] == 0 and closed["handlers_drained"]
              and (how == "ordinary" or pending == 1), closed=closed)
        return {}
    return body


def combined(host, result, out):
    """Two live owned windows, one per transport, with pending requests,
    close/reopen of one while the other works, and error retry in both."""
    host.open("A", "loopback")
    host.open("I", "inprocess")
    for label in ("A", "I"):
        c = host.page(label, "run=checks", f"{label}-checks")["result"]
        check(result, f"{label}: page, assets, query and POST", c["css"] and c["query"] == "é&ü" and c["echoLength"] == 1005)
    host.page("A", "run=storageSet&tag=A", "A-set")
    host.page("A", "run=slow", "A-slow")
    host.page("I", "run=slow", "I-slow")
    time.sleep(0.3)
    m = host.send("close A")
    during = [host.page("I", f"run=query&x=during{i}", f"I-during-{i}")["result"] for i in range(3)]
    closed = host.expect("closed", m, window="A")
    check(result, "A closed with its request pending and drained, while I kept working",
          closed["native_async"] == 0 and closed["handlers_drained"] and closed["handler"].get("GET /api/slow") == 1 and during == [{"x": f"during{i}"} for i in range(3)],
          closed=closed, during=during)
    # The reopened window gets its own label: status is keyed by label, and
    # the closed A is still listed there.
    host.open("A2", "loopback")
    fresh = host.page("A2", "run=storageRead", "A2-read")["result"]
    check(result, "reopened A: fresh session at a new endpoint", fresh == {"cookie": "", "local": None, "session": None}, fresh=fresh)
    for label in ("A2", "I"):
        before = host.status()[label]["handler"].get("GET /api/abort", 0)
        m = host.send(f"load {label} {host.origins[label]}/api/abort")
        failed, page = host.error_page(label, m)
        first_page_mark = host.last
        mid = host.status()[label]["handler"].get("GET /api/abort", 0)
        m = host.send("status")
        host.click(label, page["retry_at"])
        host.expect("error-action", m, window=label, action="retry")
        again, page2 = host.error_page(label, m)
        after = host.status()[label]["handler"].get("GET /api/abort", 0)
        # Retry is judged by navigations: exactly one allowed navigation to
        # the failed URI between the click and its error page, and none
        # between the first error page and the click. Handler calls are not
        # a navigation count over loopback: one navigation reached the
        # handler 1-3 times (recorded below), below the page. The in-process
        # transport fails /api/abort natively, before the handler.
        navs = [e for e in host.events("navigation-allowed", m, window=label) if e["uri"] == failed["uri"]]
        idle = [e for e in host.events("navigation-allowed", first_page_mark, window=label) if e["uri"] == failed["uri"] and host.all.index(e) < m]
        check(result, f"{label}: clicked Retry repeats the failed GET once, nothing automatic", again["uri"] == failed["uri"] and len(navs) == 1
              and not idle and navs[0]["method"] == "GET" and (mid > before if label == "A2" else after == before),
              retry_navigations=len(navs), handler_calls={"first navigation": mid - before, "retry": after - mid})
        m = host.send("status")
        host.click(label, page2["start_at"])
        host.expect("load", m, window=label, load_event=3, uri=host.origins[label] + "/")
    host.page("I", "run=slow", "I-slow-2")
    host.page("A2", "run=slow", "A2-slow-2")
    time.sleep(0.3)
    for label in ("I", "A2"):
        c = host.close(label)
        check(result, f"{label}: final close with a request pending drains", c["native_async"] == 0 and c["native_requests"] == 0 and c["handlers_drained"], closed=c)
    return {}


def unreserved(host, result, out):
    """Without a reservation: where JSC's suspend handler lands."""
    host.open("A", "loopback")
    host.page("A", "run=checks", "c")
    host.close("A")
    st = [e for e in host.all if e.get("event") == "signals"]
    result["observations"] = {"SIGUSR1 at ready": st[1]["dispositions"]["SIGUSR1"] if len(st) > 1 else None}
    return {}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("binary", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--debug-dir", help="required unless --no-monitor")
    p.add_argument("--no-monitor", action="store_true", help="browser cases only: no gdb, no positive controls, no disposition case; "
                   "adds the confinement attribution variants and the forced crash")
    p.add_argument("--reserve", type=int, required=True)
    p.add_argument("--root", default="/tmp/dfx2-feasibility")
    p.add_argument("--only", nargs="*")
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    enable_subreaper()
    record(args.binary.resolve(), args.output)
    if not args.no_monitor and not args.debug_dir:
        p.error("--debug-dir is required unless --no-monitor")
    run = Run(args)
    if args.no_monitor:
        run.debug_dir, run.monitored = None, False
    control_results = [] if args.no_monitor else controls(run)
    run.vscenario("loopback-basics", loopback_basics)
    run.vscenario("confinement-filter-csp", confinement)
    if args.no_monitor:
        for name, filt, csp, extra in (("confinement-filter-only", True, False, ["-feas-csp=false"]),
                                       ("confinement-csp-only", False, True, ["-feas-filter=false"]),
                                       ("confinement-control", False, False, ["-feas-filter=false", "-feas-csp=false"])):
            run.vscenario(name, lambda h, r, o, f=filt, c=csp: confinement(h, r, o, f, c), flags=[*run.flags, *extra])
    run.vscenario("inprocess-isolation", inprocess_isolation)
    run.vscenario("cookie-close-race", fz.cookie_close_race)
    run.vscenario("cookie-failure-injected", fz.cookie_failure, flags=[*run.flags, "-feas-cookie-fail"])
    run.vscenario("storage", storage)
    run.vscenario("error-pages", error_pages)
    run.vscenario("owned-load-failure", owned_load_failure)
    for mode in ("loopback", "inprocess"):
        for how in ("ordinary", "pending", "inspector"):
            run.vscenario(f"shutdown-{mode}-{how}", shutdown(mode, how))
    run.vscenario("combined", combined)
    if args.no_monitor:
        # The old forced-crash case needs no host JavaScript: run it with the
        # candidate's flags through feasibility.py's runner.
        run.scenario("forced-crash", run.flags, fz.forced_crash(run, run.flags), crash=True)
    else:
        run.vscenario("unreserved-dispositions", unreserved, flags=["-feas-vmfree"])
    summary = {"reserve": args.reserve, "controls": control_results, "scenarios": run.results,
               "passed": all(c["ok"] for c in control_results) and all(r["ok"] for r in run.results)}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=1, default=str) + "\n")
    print(json.dumps({"passed": summary["passed"], "failed": [c["control"] for c in control_results if not c["ok"]] + [r["scenario"] for r in run.results if not r["ok"]]}))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
