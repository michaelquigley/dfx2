#!/usr/bin/env python3
"""Passes B-D of the integration feasibility spike on Linux.

    python3 -B feasibility.py BINARY OUTDIR [--only SCENARIO ...]

Each scenario runs its own feasibility host (BINARY -feasibility ...) under
the descendant observer, with its own XDG data/cache/config directories, and
drives it through console commands. A controlled collector and a stoppable
attachment server run here, on 127.0.0.1, so their lifetimes and counters are
independent of the browser. Judgments use server-side counters and native
events, never only what a page displays. Timeouts are hang guards and count
as failures. The endpoint credential never appears here: external checks use
none or a wrong one, and the right one is exercised only by the host's own
selftest.
"""
import argparse
import http.client
import http.server
import json
import os
from pathlib import Path
import queue
import shutil
import socket
import subprocess
import sys
import threading
import time

from exitwatch import descendants, enable_subreaper, fatal_log, observe
from feasibility_env import record

HERE = Path(__file__).resolve().parent
STATIC = HERE.parent / "webview2-spike" / "feasibility" / "static"
CREDENTIAL = "dfx2-endpoint"
TYPES = {"index.html": "text/html; charset=utf-8", "probe.js": "text/javascript", "probe.css": "text/css", "logo.svg": "image/svg+xml"}


class Failure(Exception):
    pass


class Server:
    """A loopback HTTP server whose requests are recorded by this driver."""

    def __init__(self, name, handle, port=0):
        self.name, self.handle, self.port = name, handle, port
        self.lock = threading.Lock()
        self.requests = []
        self.httpd = None
        self.start()

    def start(self):
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *a):
                pass

            def _any(self):
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length) if length else b""
                cookies = self.headers.get("Cookie") or ""
                entry = {"method": self.command, "path": self.path, "credential": CREDENTIAL + "=" in cookies,
                         "app_cookie": [c.split("=")[0].strip() for c in cookies.split(";") if c.strip() and CREDENTIAL not in c],
                         "origin": self.headers.get("Origin"), "upgrade": self.headers.get("Upgrade"), "t": round(time.time(), 3)}
                with outer.lock:
                    outer.requests.append(entry)
                outer.handle(self, body)

            do_GET = do_POST = do_HEAD = do_OPTIONS = _any

        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        self.httpd.daemon_threads = True
        self.port = self.httpd.server_address[1]
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()

    def stop(self):
        self.httpd.shutdown()
        self.httpd.server_close()

    @property
    def origin(self):
        return f"http://127.0.0.1:{self.port}"

    def count(self, method=None, path=None):
        with self.lock:
            return sum(1 for r in self.requests if (method is None or r["method"] == method) and (path is None or r["path"].split("?")[0] == path))

    def snapshot(self):
        with self.lock:
            return list(self.requests)


def reply(h, status, body=b"", ctype="text/plain", headers=None):
    # One request per connection: a stopped server must not keep answering
    # through a browser's kept-alive connection.
    h.close_connection = True
    h.send_response(status)
    h.send_header("Connection", "close")
    h.send_header("Content-Type", ctype)
    h.send_header("Content-Length", str(len(body)))
    for k, v in (headers or {}).items():
        h.send_header(k, v)
    h.end_headers()
    if h.command != "HEAD":
        h.wfile.write(body)


def collector_handle(h, body):
    reply(h, 200, b"collected\n")


def attachment_handle(state):
    state.setdefault("lock", threading.Condition())

    def handle(h, body):
        path = h.path.split("?")[0]
        name = path.lstrip("/") or "index.html"
        if h.command == "GET" and name in TYPES:
            reply(h, 200, (STATIC / name).read_bytes(), TYPES[name], {"Cache-Control": "no-store"})
        elif path == "/api/query":
            reply(h, 200, json.dumps({"x": h.path.partition("x=")[2]}).encode(), "application/json")
        elif path == "/api/echo":
            reply(h, 200, json.dumps({"length": len(body)}).encode(), "application/json")
        elif path == "/api/cached":
            state["cached"] += 1
            n = str(state["cached"])
            reply(h, 200, ("cached " + n).encode(), headers={"Cache-Control": "max-age=600", "X-Count": n})
        elif path == "/api/form":
            reply(h, 303, headers={"Location": "/?posted=1"})
        elif path == "/post-303":
            reply(h, 303, headers={"Location": "/broken"})
        elif path == "/post-307":
            reply(h, 307, headers={"Location": "/broken-post"})
        elif path in ("/broken", "/broken-post"):
            h.close_connection = True  # no response at all: a transport failure
            h.connection.shutdown(socket.SHUT_RDWR)
        elif path == "/missing":
            reply(h, 404, b"<title>404 from the server</title><h1>404 from the server</h1>", "text/html")
        elif path == "/e500":
            reply(h, 500, b"<title>500 from the server</title><h1>500 from the server</h1>", "text/html")
        elif path == "/api/report" and h.command == "POST":
            # Page-driven results, for the VM-free host (vmfree.py).
            with state["lock"]:
                state.setdefault("reports", []).append(json.loads(body))
                state["lock"].notify_all()
            reply(h, 204)
        else:
            reply(h, 404, b"not found\n")
    return handle


class Host:
    def __init__(self, binary, out, flags, xdg, wrap=(), env=None):
        """wrap prefixes the command, as an external monitor would."""
        self.out = out
        env = dict(os.environ, XDG_DATA_HOME=str(xdg / "data"), XDG_CACHE_HOME=str(xdg / "cache"), XDG_CONFIG_HOME=str(xdg / "config"), **(env or {}))
        for d in ("data", "cache", "config"):
            (xdg / d).mkdir(parents=True, exist_ok=True)
        self.log = open(out / "host.jsonl", "w")
        self.stderr = open(out / "host.stderr", "w")
        self.proc = subprocess.Popen([*wrap, str(binary), "-feasibility", *flags], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=self.stderr, text=True, env=env, start_new_session=True)
        self.cond = threading.Condition()
        self.all = []
        self.seen = {}  # descendant identities, sampled while the host runs
        self.running = True
        threading.Thread(target=self._read, daemon=True).start()
        threading.Thread(target=self._sample, daemon=True).start()
        self.ready = self.expect("ready", 0, timeout=60)

    def _read(self):
        for line in self.proc.stdout:
            self.log.write(line)
            self.log.flush()
            try:
                e = json.loads(line)
            except json.JSONDecodeError:
                continue
            with self.cond:
                self.all.append(e)
                self.cond.notify_all()

    def _sample(self):
        while self.running:
            descendants(os.getpid(), self.seen)
            time.sleep(0.05)

    def send(self, line):
        """Send a command; returns the event mark before it."""
        with self.cond:
            mark = len(self.all)
        self.proc.stdin.write(line + "\n")
        self.proc.stdin.flush()
        return mark

    def expect(self, name, since, timeout=20, **match):
        """The first matching event at or after since; nothing is consumed."""
        deadline = time.monotonic() + timeout
        with self.cond:
            i = since
            while True:
                while i < len(self.all):
                    e = self.all[i]
                    i += 1
                    if e.get("event") == name and all(e.get(k) == v for k, v in match.items()):
                        self.last = i
                        return e
                left = deadline - time.monotonic()
                if left <= 0:
                    raise Failure(f"timed out waiting for {name} {match}")
                self.cond.wait(left)

    def error_page(self, label, since):
        """Wait for a top-level load failure and its error page's content."""
        failed = self.expect("load-failed", since, window=label, error_page=True)
        self.expect("load", self.last, window=label, load_event=2, uri=failed["uri"])
        self.expect("load", self.last, window=label, load_event=3, uri=failed["uri"])
        return failed

    def events(self, name, since=0, **match):
        with self.cond:
            return [e for e in self.all[since:] if e.get("event") == name and all(e.get(k) == v for k, v in match.items())]

    def open(self, label, mode, url="", loaded=True):
        m = self.send(f"open {label} {mode} {url}".strip())
        self.expect("opened", m, window=label)
        if loaded:
            self.expect("load", m, window=label, load_event=3)
        return m

    def navigate(self, label, body, timeout=20):
        """Run JavaScript that navigates, and wait for the resulting load."""
        m = self.send(f"js {label} {body}")
        return self.expect("load", m, window=label, load_event=3, timeout=timeout)

    def load(self, label, url, timeout=20):
        m = self.send(f"load {label} {url}")
        return self.expect("load", m, window=label, load_event=3, timeout=timeout)

    def js(self, label, body, timeout=30):
        m = self.send(f"js {label} {body}")
        e = self.expect("js", m, window=label, body=body, timeout=timeout)
        if e.get("error"):
            raise Failure(f"js {body}: {e['error']}")
        return e["result"]

    def status(self):
        return self.expect("status", self.send("status"))["windows"]

    def close(self, label):
        return self.expect("closed", self.send(f"close {label}"), window=label)

    def snapshot(self, label, path):
        return self.expect("snapshot", self.send(f"snapshot {label} {path}"), window=label)

    def children(self):
        seen = {}
        descendants(self.proc.pid, seen)
        return sorted(p["name"] for p in seen.values())

    def finish(self, timeout=40):
        if self.proc.poll() is None:
            self.send("quit")
        observed = observe(self.proc, timeout=timeout, seen=self.seen)
        self.running = False
        self.log.close()
        self.stderr.close()
        observed["fatal_log"] = fatal_log((self.out / "host.stderr").read_text())
        (self.out / "exit.json").write_text(json.dumps(observed, indent=1) + "\n")
        return observed


def manifest(xdg):
    return sorted(str(p.relative_to(xdg)) for p in xdg.rglob("*") if p.is_file())


class Run:
    def __init__(self, args):
        self.args, self.results = args, []
        self.binary = args.binary.resolve()
        self.root = Path(args.root)
        self.root.mkdir(parents=True, exist_ok=True)

    def scenario(self, name, flags, body, crash=False):
        if self.args.only and name not in self.args.only:
            return
        out = self.args.output / name
        out.mkdir(parents=True, exist_ok=True)
        xdg = self.root / f"xdg-{name}-{os.getpid()}"
        result = {"scenario": name, "flags": flags, "checks": [], "ok": False}
        host = None
        started = time.monotonic()
        try:
            host = Host(self.binary, out, ["-feas-root", str(self.root), *flags], xdg)
            result["detail"] = body(host, result, out)
        except (Failure, OSError, KeyError, TypeError, ValueError) as err:
            result["failure"] = f"{type(err).__name__}: {err}"
        finally:
            if host:
                observed = host.finish()
                result["exit"] = {k: observed[k] for k in ("host_exit", "timed_out", "children_pending", "child_failure", "fatal_log")}
                result["adopted_exits"] = [(p.get("name"), p.get("exit")) for p in observed["adopted_exits"]]
                result["observed_processes"] = sorted({p.get("name") for p in observed["observed_processes"]})
                exit_event = [e for e in host.all if e.get("event") == "exit"]
                result["filter_store_removal"] = exit_event[-1].get("filter_store_removal") if exit_event else None
                result["xdg_manifest"] = manifest(xdg) if xdg.exists() else []
                shutil.rmtree(xdg, ignore_errors=True)
        result["seconds"] = round(time.monotonic() - started, 1)
        clean_exit = result.get("exit", {}).get("host_exit") == (-9 if crash else 0) and not any(result.get("exit", {}).get(k) for k in ("timed_out", "children_pending", "fatal_log"))
        if not crash:
            clean_exit = clean_exit and not result.get("exit", {}).get("child_failure")
        result["ok"] = "failure" not in result and all(c["ok"] for c in result["checks"]) and clean_exit
        (out / "result.json").write_text(json.dumps(result, indent=1, default=str) + "\n")
        self.results.append(result)
        print(json.dumps({"scenario": name, "ok": result["ok"], "failure": result.get("failure"),
                          "failed_checks": [c["check"] for c in result["checks"] if not c["ok"]], "exit": result.get("exit")}), flush=True)


def check(result, name, ok, **detail):
    result["checks"].append({"check": name, "ok": bool(ok), **detail})
    return ok


def wait_until(f, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if f():
            return True
        time.sleep(0.05)
    return False


# ---------------------------------------------------------------- scenarios

def loopback_basics(host, result, out):
    host.open("A", "loopback")
    checks = host.js("A", "return probe.checks()")
    check(result, "assets, query and POST through the credential", checks["css"] and checks["img"] and checks["query"] == "é&ü" and checks["echoLength"] == 1005, checks=checks)
    check(result, "credential invisible to JavaScript", checks["cookieVisible"] is False)
    host.navigate("A", "location.reload(); return 1")
    again = host.js("A", "return probe.checks()")
    installs = len(host.events("cookie-installed", window="A"))
    check(result, "reload keeps authentication without reprovisioning", again["css"] and again["query"] == "é&ü" and installs == 1, installs=installs)
    host.navigate("A", "location.href = 'api/redirect'; return 1")
    redirected = host.js("A", "return {uri: location.href, body: document.body.textContent}")
    check(result, "same-origin redirect authenticated, no credential in the URL", "redirected" in redirected["body"], redirected=redirected)
    host.navigate("A", "location.href = '/'; return 1")
    chunks = host.js("A", "return probe.stream()", timeout=20)
    texts = "".join(c["text"] for c in chunks)
    check(result, "loopback stream: chunk 1 visible before the handler finished", len(chunks) >= 2 and "chunk-2 after ack" in texts, chunks=chunks)
    port = host.status()["A"]["port"]
    statuses = {}
    for name, headers in (("none", {}), ("wrong credential", {"Cookie": CREDENTIAL + "=wrong"}),
                          ("wrong credential and Origin", {"Cookie": CREDENTIAL + "=wrong", "Origin": "http://127.0.0.1:1"})):
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
    host.snapshot("A", out / "loopback-page.png")
    return {"status": st}


def confinement(filter_on, csp_on):
    def body(host, result, out):
        collector = Server("collector", collector_handle)
        try:
            host.open("A", "loopback")
            host.open("B", "loopback")
            a_port, b_port = host.status()["A"]["port"], host.status()["B"]["port"]
            home = f"http://127.0.0.1:{a_port}/"
            b_before = host.status()["B"]["handler"]
            seen = host.js("A", f"return probe.attacks('{collector.origin}', 'http://127.0.0.1:{b_port}')", timeout=40)
            navigations, marks = {}, {}
            for how in ("location", "redirect", "popup", "form"):
                marks[how] = host.send(f"js A return probe.navigate('{how}', '{collector.origin}')")
                time.sleep(1.5)
                navigations[how] = host.js("A", "return location.href")
                if not navigations[how].startswith(home):
                    host.load("A", home)
            time.sleep(1)
            got = collector.snapshot()
            b_after = host.status()["B"]["handler"]
            leaked = [r for r in got if r["credential"]]
            enforced = filter_on or csp_on
            check(result, "collector received nothing" if enforced else "control: collector receives requests",
                  (not got) if enforced else bool(got), received=[(r["method"], r["path"], r["credential"]) for r in got])
            check(result, "no credential reached the collector" if enforced else "control: credential leaks without enforcement",
                  (not leaked) if enforced else bool(leaked), leaked=len(leaked))
            check(result, "A's page never invoked B's handler", b_after.get("GET /api/count", 0) == b_before.get("GET /api/count", 0), b_before=b_before, b_after=b_after)
            refused = lambda how, path: any(e["uri"].startswith(collector.origin + path) for e in host.events("navigation-refused", marks[how], window="A"))
            popup_create = bool(host.events("popup-refused", marks["popup"], window="A"))
            arrived = lambda path: any(r["path"].startswith(path) for r in got)
            # Which mechanism stopped each top-level attempt. "silent" means no
            # native decision was asked and the collector saw nothing.
            mechanism = {
                "location": "navigation policy" if refused("location", "/nav") else ("silent" if not arrived("/nav") else "NOT REFUSED"),
                "redirect": "navigation policy" if refused("redirect", "/redirect-nav") else ("silent" if not arrived("/redirect-nav") else "NOT REFUSED"),
                "popup": "create signal" if popup_create else ("silent (window.open returned null)" if not arrived("/popup") else "NOT REFUSED"),
                "form": "navigation policy" if refused("form", "/form") else ("silent" if not arrived("/form") else "NOT REFUSED"),
            }
            stayed = all(u.startswith(home) for u in navigations.values())
            check(result, "top-level navigation, redirect, popup and form to another port never left the origin",
                  stayed and "NOT REFUSED" not in mechanism.values(), mechanism=mechanism, final_uris=navigations)
            return {"page_view": seen, "collector": got, "mechanism": mechanism,
                    "refusals": [(e["kind"], e["uri"].replace(collector.origin, "COLLECTOR")) for e in host.events("navigation-refused", window="A")]}
        finally:
            collector.stop()
    return body


def inprocess_isolation(host, result, out):
    collector = Server("collector", collector_handle)
    try:
        host.open("I1", "inprocess")
        host.open("I2", "inprocess")
        checks = host.js("I1", "return probe.checks()")
        check(result, "in-process page, assets, query and POST", checks["css"] and checks["query"] == "é&ü" and checks["echoLength"] == 1005, checks=checks)
        before = host.status()
        counted = host.js("I1", "return fetch('dfx2://spike/api/count').then(r => r.json())")
        after = host.status()
        check(result, "naming the shared origin reaches only the window's own handler",
              after["I1"]["handler"].get("GET /api/count", 0) == before["I1"]["handler"].get("GET /api/count", 0) + 1
              and after["I2"]["handler"].get("GET /api/count", 0) == before["I2"]["handler"].get("GET /api/count", 0), i1=after["I1"]["handler"], i2=after["I2"]["handler"])
        host.js("I1", f"return probe.attacks('{collector.origin}', '')", timeout=40)
        time.sleep(1)
        check(result, "an in-process page cannot reach the collector", not collector.snapshot(), received=collector.snapshot())
        i1 = host.js("I1", "return probe.storageSet('I1')")
        i2 = host.js("I2", "return probe.storageRead()")
        check(result, "same in-process origin, separate sessions: I2 cannot read I1's storage", i2 == {"cookie": "", "local": None, "session": None}, i1=i1, i2=i2)
        return {"seen_count": counted}
    finally:
        collector.stop()


def cookie_close_race(host, result, out):
    m = host.send("open A loopback")
    host.send("close A")
    closed = host.expect("closed", m, window="A")
    late = host.events("cookie-late", m, window="A") + host.events("filter-late", m, window="A")
    check(result, "close during installation: no navigation, ownership drained",
          not host.events("navigate", m, window="A") and closed["native_async"] == 0 and closed["endpoint"]["handled"] == 0, late=late, closed=closed)
    return {"late": late}


def cookie_failure(host, result, out):
    m = host.send("open A loopback")
    failed = host.expect("cookie-failed", m, window="A")
    closed = host.expect("closed", m, window="A")
    check(result, "injected installation failure: visible, no navigation, drained",
          "injected" in failed["error"] and not host.events("navigate", m, window="A") and closed["native_async"] == 0 and closed["endpoint"]["handled"] == 0,
          failed=failed, closed=closed)
    return {}


def storage(host, result, out):
    attach = Server("attachment", attachment_handle({"cached": 0}))
    try:
        url = attach.origin + "/"
        processes = {}
        host.open("A", "attach", url)
        host.open("B", "attach", url)
        processes["two windows"] = host.children()
        a = host.js("A", "return probe.storageSet('A')")
        b = host.js("B", "return probe.storageRead()")
        check(result, "B cannot read A's cookie, local or session storage", b == {"cookie": "", "local": None, "session": None}, a=a, b=b)
        host.js("B", "return probe.storageSet('B')")
        cached = lambda: attach.count("GET", "/api/cached")
        n0 = cached()
        a_fetch = [host.js("A", "return probe.cached()") for _ in range(2)]
        n1 = cached()
        host.js("A", "return probe.cachedImage()")
        n2 = cached()
        host.js("B", "return probe.cachedImage()")
        n3 = cached()
        check(result, "B's request for A's cached resource reaches the server", n3 > n2, server_requests={"A fetch x2": n1 - n0, "A image x2": n2 - n1, "B image x2": n3 - n2})
        result["observations"] = {"cache": {"A fetch x2 reached server": n1 - n0, "A image x2 reached server": n2 - n1, "B image x2 reached server": n3 - n2}}
        host.navigate("A", "location.reload(); return 1")
        a_reloaded = host.js("A", "return probe.storageRead()")
        check(result, "reload keeps A's session", a_reloaded == {"cookie": "vis=A", "local": "A", "session": "A"}, a=a_reloaded)
        # Close A while B keeps making requests.
        m = host.send("close A")
        b_during = [host.js("B", "return fetch('api/query?x=during').then(r => r.json())") for _ in range(3)]
        host.expect("closed", m, window="A")
        processes["after closing A"] = host.children()
        check(result, "B kept working while A closed", all(x == {"x": "during"} for x in b_during), b_during=b_during)
        host.open("A2", "attach", url)
        a2 = host.js("A2", "return probe.storageRead()")
        b_after = host.js("B", "return probe.storageRead()")
        check(result, "reopened A has a fresh session", a2 == {"cookie": "", "local": None, "session": None}, a2=a2)
        check(result, "B is unchanged", b_after == {"cookie": "vis=B", "local": "B", "session": "B"}, b=b_after)
        for label in ("B", "A2"):
            host.close(label)
        processes["no windows"] = host.children()
        conn = http.client.HTTPConnection("127.0.0.1", attach.port, timeout=5)
        conn.request("GET", "/api/query?x=after-browser")
        survived = conn.getresponse().status
        check(result, "the attachment server outlives its browsers", survived == 200, status=survived)
        return {"processes": processes, "app_cookie_names_per_cached_request": [r["app_cookie"] for r in attach.snapshot() if r["path"].startswith("/api/cached")]}
    finally:
        attach.stop()


def error_pages(host, result, out):
    attach = Server("attachment", attachment_handle({"cached": 0}))
    url = attach.origin + "/"
    try:
        host.open("E", "attach", url)
        # A later GET fails while the service is down.
        attach.stop()
        m = host.send("js E location.href = 'api/query?x=later'; return 1")
        failed = host.error_page("E", m)
        host.snapshot("E", out / "error-get.png")
        page = host.js("E", "return {retry: !!document.getElementById('retry'), start: !!document.getElementById('start'), scripts: document.scripts.length, text: document.body.innerText}")
        check(result, "failed GET: Retry and Return to start, no script", page["retry"] and page["start"] and page["scripts"] == 0, failed=failed, page=page)
        m = host.send("js E location.href = 'dfx2-error:retry/forged'; return 1")
        check(result, "a forged action is refused", bool(host.expect("error-action-refused", m, window="E")))
        attach.start()
        gets = attach.count("GET", "/api/query")
        m = host.send("js E document.getElementById('retry').click(); return 1")
        host.expect("error-action", m, window="E", action="retry")
        host.expect("load", m, window="E", load_event=3)
        check(result, "explicit Retry repeats the failed GET once the service returns", attach.count("GET", "/api/query") == gets + 1)
        # A failed POST navigation offers only Return to start.
        host.load("E", url)
        attach.stop()
        m = host.send("js E document.getElementById('edit').submit(); return 1")
        failed_post = host.error_page("E", m)
        host.snapshot("E", out / "error-post.png")
        post_page = host.js("E", "return {retry: !!document.getElementById('retry'), start: !!document.getElementById('start')}")
        check(result, "failed POST shows only Return to start", post_page == {"retry": False, "start": True} and failed_post["method"] in ("POST", "FORM"), failed=failed_post)
        attach.start()
        m = host.send("js E document.getElementById('start').click(); return 1")
        host.expect("error-action", m, window="E", action="start")
        host.expect("load", m, window="E", load_event=3)
        check(result, "Return to start is a GET and replays nothing", attach.count("POST") == 0, posts=attach.count("POST"))
        # POST, 303 to a failing GET: Retry repeats only the GET.
        m = host.send("js E const f=document.getElementById('edit'); f.action='/post-303'; f.submit(); return 1")
        failed_303 = host.error_page("E", m)
        check(result, "a GET reached by 303 after a POST offers Retry of the GET", failed_303["method"] == "GET" and failed_303["from_post"] and failed_303["retry_offered"], failed=failed_303)
        m = host.send("js E document.getElementById('retry').click(); return 1")
        host.expect("error-action", m, window="E", action="retry")
        host.error_page("E", m)
        check(result, "Retry after the 303 replays no POST", attach.count("POST", "/post-303") == 1 and attach.count("GET", "/broken") == 2,
              post_303=attach.count("POST", "/post-303"), broken=attach.count("GET", "/broken"))
        host.navigate("E", "document.getElementById('start').click(); return 1")
        # POST, 307 keeps the method: the failure is a POST, so no Retry.
        m = host.send("js E const f=document.getElementById('edit'); f.action='/post-307'; f.submit(); return 1")
        failed_307 = host.error_page("E", m)
        page_307 = host.js("E", "return {retry: !!document.getElementById('retry')}")
        check(result, "a POST preserved by 307 offers no Retry", failed_307["method"] == "POST" and page_307 == {"retry": False}
              and attach.count("POST", "/post-307") == 1 and attach.count("POST", "/broken-post") == 1, failed=failed_307)
        host.navigate("E", "document.getElementById('start').click(); return 1")
        # Ordinary HTTP errors, failed fetches and failed assets keep their page.
        m = host.send("status")
        host.load("E", attach.origin + "/missing")
        missing = host.js("E", "return document.body.innerText")
        host.load("E", attach.origin + "/e500")
        e500 = host.js("E", "return document.body.innerText")
        host.load("E", url)
        fetched = host.js("E", "return fetch('/broken').then(() => 'ok', e => 'rejected: ' + e.name)")
        host.js("E", "const i=document.createElement('img'); i.src='/broken'; document.body.appendChild(i); return 1")
        time.sleep(1)
        still = host.js("E", "return location.pathname + ' ' + !!document.getElementById('edit')")
        check(result, "404 and 500 display; a failed fetch or asset leaves the page", "404 from the server" in missing and "500 from the server" in e500
              and not host.events("load-failed", m, window="E") and fetched.startswith("rejected") and still == "/ true",
              missing=missing, e500=e500, fetched=fetched, still=still)
        # The initial load failing: the service is down before the window opens.
        attach.stop()
        m = host.open("F", "attach", url, loaded=False)
        initial = host.error_page("F", m)
        attach.start()
        m = host.send("js F document.getElementById('retry').click(); return 1")
        host.expect("error-action", m, window="F", action="retry")
        loaded = host.expect("load", m, window="F", load_event=3)
        check(result, "initial-load failure, then explicit Retry", loaded["uri"] == url and not host.events("load-failed", m, window="F"), initial=initial)
        return {"posts": [r for r in attach.snapshot() if r["method"] == "POST"]}
    finally:
        try:
            attach.stop()
        except OSError:
            pass


def owned_load_failure(host, result, out):
    for label, mode in (("L", "loopback"), ("P", "inprocess")):
        host.open(label, mode)
        m = host.send(f"js {label} location.href = '/api/abort'; return 1")
        failed = host.error_page(label, m)
        host.snapshot(label, out / f"error-{mode}.png")
        page = host.js(label, "return {retry: !!document.getElementById('retry'), cookie: document.cookie}")
        check(result, f"{mode}: native load failure shows the error page without exposing the credential",
              page["retry"] and CREDENTIAL not in page["cookie"], failed=failed, page=page)
        host.navigate(label, "document.getElementById('start').click(); return 1")
        check(result, f"{mode}: Return to start reloads the start page", host.js(label, "return location.pathname") == "/")
    return {}


def shutdown(mode, how):
    def body(host, result, out):
        host.open("S", mode)
        if how == "inspector":
            host.expect("inspector", host.send("inspect S"), window="S", attached=True)
        if how in ("pending", "inspector"):
            host.send("js S fetch('api/slow'); return 1")
            time.sleep(0.5)
        closed = host.close("S")
        pending = closed["handler"].get("GET /api/slow", 0)
        check(result, f"{how} close drains", closed["native_async"] == 0 and closed["native_requests"] == 0 and closed["handlers_drained"]
              and (how == "ordinary" or pending == 1), closed=closed)
        return {}
    return body


def forced_crash(run, flags=()):
    """SIGKILL a host that owns an on-disk filter store: the residue stays,
    named by its process; the next host uses a fresh directory and neither
    scans nor deletes another instance's. No scavenger runs."""
    def body(host, result, out):
        host.open("A", "loopback")
        store = Path(host.ready and host.events("filter-store")[0]["path"])
        os.killpg(host.proc.pid, 9)
        host.proc.wait()
        result["exit_expected"] = "SIGKILL"
        residue = sorted(str(p.relative_to(store)) for p in store.rglob("*"))
        check(result, "a killed host leaves its own filter store, named by its pid", store.exists() and str(host.proc.pid) in store.name, residue=residue)
        (out / "second").mkdir(exist_ok=True)
        second = Host(run.binary, out / "second", ["-feas-root", str(run.root), *flags], run.root / f"xdg-forced-crash-second-{os.getpid()}")
        try:
            second.open("B", "loopback")
            fresh = Path(second.events("filter-store")[0]["path"])
            check(result, "the next host starts with its own fresh store", fresh != store and fresh.name == f"filters-{second.proc.pid}")
        finally:
            observed = second.finish()
        check(result, "the next host removed only its own store; the residue remains", store.exists() and not fresh.exists() and observed["host_exit"] == 0,
              second_exit=observed["host_exit"])
        shutil.rmtree(store)  # the driver's own cleanup of its experiment residue, after the checks
        return {"residue_store": str(store), "fresh_store": str(fresh)}
    return body


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("binary", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--root", default="/tmp/dfx2-feasibility", help="experiment-owned directory for filters and XDG data")
    p.add_argument("--only", nargs="*")
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    enable_subreaper()
    record(args.binary.resolve(), args.output)
    run = Run(args)
    run.scenario("loopback-basics", [], loopback_basics)
    run.scenario("confinement-filter-csp", [], confinement(True, True))
    run.scenario("confinement-filter-only", ["-feas-csp=false"], confinement(True, False))
    run.scenario("confinement-csp-only", ["-feas-filter=false"], confinement(False, True))
    run.scenario("confinement-control", ["-feas-filter=false", "-feas-csp=false"], confinement(False, False))
    run.scenario("inprocess-isolation", [], inprocess_isolation)
    run.scenario("cookie-close-race", [], cookie_close_race)
    run.scenario("cookie-failure-injected", ["-feas-cookie-fail"], cookie_failure)
    run.scenario("storage", [], storage)
    run.scenario("error-pages", [], error_pages)
    run.scenario("owned-load-failure", [], owned_load_failure)
    run.scenario("forced-crash", [], forced_crash(run), crash=True)
    for mode in ("loopback", "inprocess"):
        for how in ("ordinary", "pending", "inspector"):
            run.scenario(f"shutdown-{mode}-{how}", [], shutdown(mode, how))
    summary = {"scenarios": run.results, "passed": all(r["ok"] for r in run.results)}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=1, default=str) + "\n")
    print(json.dumps({"passed": summary["passed"], "failed": [r["scenario"] for r in run.results if not r["ok"]]}))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
