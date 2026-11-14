#!/usr/bin/env python3
"""Integrated Linux browser matrix for webview integration stage 2.

Runs examples/webview inside an isolated headless GNOME Shell, through the
synthetic mixer's nested-session driver:

    python3 evidence/synthetic-mixer/linux/controls_drive.py BINARY --out OUT/session --base /tmp \\
        --exec python3 -B evidence/webview-integration/linux/webview_drive.py BINARY --out OUT --gdk-backend wayland

Every case starts fresh host processes. Results are page-driven: the page
posts probe results to its own handler, which the host reports as events;
the host evaluates no JavaScript. Cross-origin attempts are judged by a
collector's records, never by what the page sees. The driver is the hosts'
child subreaper, so WebKit's descendants are reaped and their exit statuses
recorded after each host exits.
"""
import argparse
import ctypes
import http.server
import json
import os
from pathlib import Path
import queue
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import traceback
import urllib.parse
import urllib.request

SIGNAL = 60


class Failure(Exception):
    pass


def require(condition, message, **detail):
    if not condition:
        raise Failure(message + (" " + json.dumps(detail, default=str) if detail else ""))


# --- servers ---------------------------------------------------------------

class Recorder(http.server.ThreadingHTTPServer):
    """A collector or attachment server that records every request."""
    daemon_threads = True

    def __init__(self, routes=None):
        self.records, self.lock, self.routes = [], threading.Lock(), routes or {}
        super().__init__(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.serve_forever, daemon=True).start()

    @property
    def origin(self):
        return f"http://127.0.0.1:{self.server_address[1]}"

    def hits(self, prefix=""):
        with self.lock:
            return [r for r in self.records if r["path"].startswith(prefix)]

    def count(self, method, path):
        return len([r for r in self.hits(path) if r["method"] == method and r["path"].split("?")[0] == path])

    def stop(self):
        self.shutdown()
        self.server_close()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def handle_any(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        record = dict(method=self.command, path=self.path, cookie=self.headers.get("Cookie", ""),
                      origin=self.headers.get("Origin", ""), upgrade=self.headers.get("Upgrade", ""), t=time.time())
        with self.server.lock:
            self.server.records.append(record)
        route = self.server.routes.get(self.path.split("?")[0])
        if route == "drop":
            self.close_connection = True  # no response at all: a load failure
            return
        if isinstance(route, tuple):  # (status, location)
            self.send_response(route[0])
            self.send_header("Location", route[1])
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        text = route if isinstance(route, str) else "<!doctype html><title>collected</title>"
        if self.path.startswith("/post?"):
            target = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)["target"][0]
            text = (f'<!doctype html><title>posting</title><form id="f" method="post" action="{target}"><input name="x" value="1"></form>'
                    '<script>document.getElementById("f").submit()</script>')
        data = text.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_HEAD = handle_any


ATTACH_PAGE = "<!doctype html><title>attached service</title><p>attached service</p>"


# --- host ------------------------------------------------------------------

class Host:
    def __init__(self, binary, out, name, env, resident=True, flags=()):
        self.name, self.events, self.queue = name, [], queue.Queue()
        self.log = open(out / f"{name}.jsonl", "w")
        self.stderr_path = out / f"{name}.stderr"
        args = [binary, "-gc-signal", str(SIGNAL)] + (["-resident"] if resident else []) + list(flags)
        self.process = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=open(self.stderr_path, "w"), text=True, env=env)
        threading.Thread(target=self._read, daemon=True).start()
        self.names = {}
        threading.Thread(target=self._sample, daemon=True).start()
        self.ready = self.expect("ready", timeout=60)

    def _sample(self):
        """Remembers descendants' names, so children reaped after a host
        exits by itself can still be identified."""
        while self.process.poll() is None:
            self.names.update(descendants(self.process.pid))
            time.sleep(0.25)

    def _read(self):
        for line in self.process.stdout:
            self.log.write(line)
            self.log.flush()
            try:
                event = json.loads(line)
            except ValueError:
                continue
            self.events.append(event)
            self.queue.put(event)

    def send(self, line):
        self.log.write(json.dumps({"command": line}) + "\n")
        self.process.stdin.write(line + "\n")
        self.process.stdin.flush()

    def expect(self, name, timeout=20, match=lambda e: True):
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                event = self.queue.get(timeout=max(0.05, deadline - time.time()))
            except queue.Empty:
                break
            if event["event"] == name and match(event):
                return event
            if event["event"] == "error":
                raise Failure(f"host error: {event}")
        raise Failure(f"{self.name}: no {name} event within {timeout}s")

    def quiet(self, name, seconds, match=lambda e: True):
        """Fails if a matching event arrives within seconds."""
        try:
            event = self.expect(name, timeout=seconds, match=match)
        except Failure:
            return
        raise Failure(f"{self.name}: unexpected {name}: {event}")

    def open(self, label, mode, *rest):
        """Opens a window and waits until its first page has loaded: driven
        navigation is refused while the browser initializes."""
        self.send(" ".join(["open", label, mode, *rest]))
        start = self.expect("opened", timeout=30, match=lambda e: e["window"] == label)["start"]
        deadline = time.time() + 30
        while time.time() < deadline:
            w = self.window(label)
            if w and w.get("browser", {}).get("Navigated") and (w.get("uri") or "").startswith(start) and w.get("title"):
                return start
            time.sleep(0.2)
        raise Failure(f"{self.name}/{label}: first page never loaded")

    def run(self, label, start, name, timeout=20, **query):
        """Loads start?run=name&..., waits for the page's report."""
        rid = f"{name}-{len(self.events)}"
        q = urllib.parse.urlencode(dict(run=name, id=rid, **query))
        self.send(f"go {label} {start}?{q}")
        return self.expect("report", timeout=timeout, match=lambda e: e["window"] == label and e["report"]["id"] == rid)["report"]

    def status(self):
        self.send("status")
        return self.expect("status")

    def window(self, label):
        return self.status()["windows"].get(label)

    def events_of(self, label):
        w = self.window(label)
        return w["browser"]["Events"] if w and w.get("browser") else []

    def wait_event(self, label, kind, timeout=15, after=0, match=lambda e: True):
        """Polls the attachment's policy events for a new one of kind."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            events = self.events_of(label)
            found = [e for e in events[after:] if e["kind"] == kind and match(e)]
            if found:
                return found[0], len(events)
            time.sleep(0.2)
        raise Failure(f"{self.name}/{label}: no {kind} policy event")

    def finish(self, timeout=30):
        """Waits for exit; returns (code, Run's error, stderr)."""
        try:
            code = self.process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            self.process.kill()
            raise Failure(f"{self.name}: host did not exit within {timeout}s")
        time.sleep(0.2)
        returned = [e for e in self.events if e["event"] == "return"]
        self.log.close()
        return code, returned[-1]["error"] if returned else None, self.stderr_path.read_text()


# --- process observation ---------------------------------------------------

libc = ctypes.CDLL(None, use_errno=True)
PR_SET_CHILD_SUBREAPER = 36


def descendants(pid):
    """Live descendants of pid with their command names."""
    parents = {}
    for entry in Path("/proc").iterdir():
        if entry.name.isdigit():
            try:
                stat = (entry / "stat").read_text()
                parents[int(entry.name)] = (int(stat.rsplit(")", 1)[1].split()[1]), stat[stat.index("(") + 1:stat.rindex(")")])
            except (OSError, ValueError, IndexError):
                pass
    out, frontier = {}, {pid}
    while frontier:
        nxt = {p for p, (ppid, _) in parents.items() if ppid in frontier}
        for p in nxt:
            out[p] = parents[p][1]
        frontier = nxt
    return out


def reap(names, timeout=15):
    """Collects every adopted child after a host exited."""
    statuses, deadline = [], time.time() + timeout
    while time.time() < deadline:
        try:
            pid, status = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            return statuses, []
        if pid == 0:
            time.sleep(0.1)
            continue
        entry = dict(pid=pid, name=names.get(pid, "?"))
        if os.WIFSIGNALED(status):
            entry["signal"] = os.WTERMSIG(status)
        else:
            entry["exit"] = os.WEXITSTATUS(status)
        statuses.append(entry)
    remaining = [p for p in descendants(os.getpid())]
    return statuses, remaining


# The one abnormal end accepted: WebKit runs its bwrap sandbox helpers with
# --die-with-parent, so they end by SIGKILL when the host exits. Only a child
# identified by name as bwrap qualifies; an unnamed one does not.
EXPECTED_SIGNAL = ("bwrap", signal.SIGKILL)


def child_problem(child, killed=()):
    """Why a reaped descendant's end is unacceptable, or None. Every child
    must exit 0, except the identified bwrap helper's SIGKILL and a process
    the case killed deliberately, for its signal only."""
    if "signal" in child:
        if (child["pid"] in killed and child["signal"] == signal.SIGKILL) or (child["name"], child["signal"]) == EXPECTED_SIGNAL:
            return None
        return f"child {child} killed by a signal"
    if child.get("exit") != 0:
        return f"child {child} exited unsuccessfully"
    return None


def listeners(pid):
    """TCP listening sockets owned by pid."""
    out = subprocess.run(["ss", "-ltnpH"], capture_output=True, text=True).stdout
    return [line.split()[3] for line in out.splitlines() if f"pid={pid}," in line]


def mapped(pid, needle):
    return any(needle in line for line in Path(f"/proc/{pid}/maps").read_text().splitlines())


# --- cases -----------------------------------------------------------------

class Matrix:
    def __init__(self, binary, out, env):
        self.binary, self.out, self.env = binary, out, env
        self.results, self.hosts = [], []

    def host(self, name, resident=True, flags=()):
        """A resident host by default: a window application with no window
        after setup exits at once, by the last-window rule. Each host has its
        own temporary directory, so its leftovers are its own."""
        tmp = Path(self.env["TMPDIR"]) / name
        tmp.mkdir()
        h = Host(self.binary, self.out, name, dict(self.env, TMPDIR=str(tmp)), resident, flags)
        h.tmp = tmp
        self.hosts.append(h)
        return h

    def done(self, h, expect_error=None, killed=()):
        """Ends a host and checks its exit, stderr, descendants and storage."""
        names = dict(h.names)
        names.update(descendants(h.process.pid))
        if h.process.poll() is None:
            h.send("quit")
        code, error, stderr = h.finish()
        children, remaining = reap(names)
        problems = []
        if expect_error is None:
            if code != 0 or error != "<nil>":
                problems.append(f"exit {code} error {error}")
        elif expect_error not in str(error) or code == 0:
            problems.append(f"expected failure {expect_error!r}, got exit {code} error {error}")
        for c in children:
            if (problem := child_problem(c, killed)) is not None:
                problems.append(problem)
        if remaining:
            problems.append(f"descendants remain: {remaining}")
        bad = [l for l in stderr.splitlines() if "CRITICAL" in l or "Overriding existing handler" in l or "panic" in l]
        if bad:
            problems.append(f"stderr: {bad}")
        leftovers = sorted(p.name for p in h.tmp.glob("dfx2-webview-filter-*"))
        if leftovers:
            problems.append(f"filter storage left: {leftovers}")
        record = dict(host=h.name, exit=code, error=error, children=children, stderr_lines=len(stderr.splitlines()))
        require(not problems, "; ".join(problems), **record)
        return record

    def case(self, name, body):
        started = time.time()
        result = dict(case=name)
        try:
            result.update(body() or {})
            result["ok"] = True
        except Exception as err:
            result.update(ok=False, failure=f"{type(err).__name__}: {err}", trace=traceback.format_exc())
            for h in self.hosts:
                if h.process.poll() is None:
                    h.process.kill()
                    h.process.wait()
            reap({}, timeout=5)
        self.hosts = []
        result["seconds"] = round(time.time() - started, 1)
        self.results.append(result)
        print(json.dumps(dict(case=name, ok=result["ok"], failure=result.get("failure"))), flush=True)

    # Both owned transports serve the page, assets, fetch and a POST body.
    def owned_basics(self):
        h = self.host("owned-basics")
        out = {}
        for label, mode in (("P", "inprocess"), ("L", "loopback")):
            start = h.open(label, mode)
            r = h.run(label, start, "checks")["result"]
            require(r["css"] and r["img"] and r["query"] == "é&ü" and r["echo"]["length"] == 1005 and r["echo"]["method"] == "POST"
                    and r["missing"] == 404 and r["cookie"] == "", f"{mode} checks", result=r)
            out[mode] = dict(origin=r["origin"], start=start)
        require(out["inprocess"]["origin"] == "dfx2://app", "in-process origin", **out)
        ports = listeners(h.process.pid)
        loopback_port = urllib.parse.urlparse(out["loopback"]["start"]).port
        require(ports == [f"127.0.0.1:{loopback_port}"], "only the loopback endpoint listens", listeners=ports)
        # The loopback credential stays in the browser: a client without it is refused.
        try:
            urllib.request.urlopen(out["loopback"]["start"], timeout=5)
            refused = None
        except urllib.error.HTTPError as err:
            refused = err.code
        require(refused == 403, "uncredentialed loopback request", status=refused)
        stream = h.run("L", out["loopback"]["start"], "stream")["result"]
        require(stream["chunks"][:2] == ["chunk-1\n", "chunk-2 after ack\n"], "loopback streams before the handler returns", stream=stream)
        h.send("close P")
        h.expect("closed", match=lambda e: e["window"] == "P")
        require(listeners(h.process.pid) == [f"127.0.0.1:{loopback_port}"], "in-process close leaves the loopback window")
        h.send("close L")
        h.expect("closed", match=lambda e: e["window"] == "L")
        out["exit"] = self.done(h)
        return out

    def in_process_alone(self):
        """In-process operation with no application listener at all."""
        h = self.host("inprocess-alone")
        start = h.open("P", "inprocess")
        h.run("P", start, "checks")
        ports = listeners(h.process.pid)
        require(ports == [], "in-process host listens on nothing", listeners=ports)
        return dict(listeners=ports, exit=self.done(h))

    # Owned windows keep subresources and navigation on their origin.
    def confinement(self):
        collector = Recorder()
        h = self.host("confinement")
        l_start = h.open("L", "loopback")
        o_start = h.open("O", "loopback")
        p_start = h.open("P", "inprocess")
        other = o_start.rstrip("/")
        before = h.window("O")["handler"].get("GET /api/query", 0)
        seen = {}
        seen["loopback"] = h.run("L", l_start, "attacks", timeout=40, collector=collector.origin, other=other)["result"]
        seen["inprocess"] = h.run("P", p_start, "attacks", timeout=40, collector=collector.origin)["result"]
        require(not collector.hits(), "subresources reached the collector", hits=collector.hits())
        require(h.window("O")["handler"].get("GET /api/query", 0) == before, "a page reached another window's endpoint")
        navigations = {}
        for label, start in (("L", l_start), ("P", p_start)):
            for how in ("location", "redirect", "popup", "form"):
                after = len(h.events_of(label))
                h.run(label, start, "armed", then="navigate", how=how, collector=collector.origin)
                if how != "location":
                    # Other routes may stop before navigation policy: WebKit
                    # blocks a script-initiated window.open itself, the
                    # framework policy's form-action blocks an outside form,
                    # and WebKitGTK does not follow redirects from in-process
                    # (custom-scheme) responses at all. The collector and the
                    # window's URI judge every route; policy refusals are
                    # recorded where they happened.
                    time.sleep(1.5)
                    event = [e for e in h.events_of(label)[after:] if e["kind"] in ("popup-refused", "navigation-refused")] or "stopped before navigation policy"
                else:
                    event, _ = h.wait_event(label, "navigation-refused", after=after)
                    time.sleep(1)
                uri = h.window(label)["uri"]
                require(uri.startswith(start), f"{label} {how} left the origin", uri=uri)
                navigations[f"{label}-{how}"] = event
        require(not collector.hits(), "navigation reached the collector", hits=collector.hits())
        collector.stop()
        return dict(page_views=seen, refused=navigations, collector_hits=0, exit=self.done(h))

    # Sessions are per window, survive navigation and die with the window.
    def storage(self):
        h = self.host("storage")
        a = h.open("A", "inprocess")
        b = h.open("B", "inprocess")
        set_a = h.run("A", a, "storageSet", tag="a")["result"]
        read_b = h.run("B", b, "storageRead")["result"]
        reread_a = h.run("A", a, "storageRead")["result"]
        # WebKit keeps no cookies for the custom in-process scheme: storage
        # here is local and session storage; cookies are checked over loopback.
        require(set_a["local"] == "a" and set_a["session"] == "a", "A stored", result=set_a)
        require(read_b["local"] is None and read_b["session"] is None, "B sees A's storage", result=read_b)
        require(reread_a["local"] == "a" and reread_a["session"] == "a", "A lost its session on navigation", result=reread_a)
        # Cookies ignore ports, so two loopback windows on one host share
        # nothing only because their sessions are separate.
        l1 = h.open("L1", "loopback")
        l2 = h.open("L2", "loopback")
        set_l1 = h.run("L1", l1, "storageSet", tag="l1")["result"]
        reread_l1 = h.run("L1", l1, "storageRead")["result"]
        read_l2 = h.run("L2", l2, "storageRead")["result"]
        require("vis=l1" in set_l1["cookie"] and "vis=l1" in reread_l1["cookie"], "loopback page cookie kept", set=set_l1, reread=reread_l1)
        require("vis=" not in read_l2["cookie"], "loopback windows share cookies across ports", result=read_l2)
        for label in ("A", "B", "L1", "L2"):
            h.send(f"close {label}")
            h.expect("closed", match=lambda e, label=label: e["window"] == label)
        a = h.open("A", "inprocess")
        fresh = h.run("A", a, "storageRead")["result"]
        require(fresh["local"] is None and "vis=" not in fresh["cookie"], "reopened window kept old storage", result=fresh)
        return dict(set=set_a, other=read_b, reload=reread_a, loopback=reread_l1, other_port=read_l2, reopened=fresh, exit=self.done(h))

    # Load failures: framework error page, Retry only for a known GET.
    def error_pages_loopback(self):
        h = self.host("errors-loopback")
        start = h.open("L", "loopback")
        h.run("L", start, "armed")
        out = {}

        def failure(trigger):
            after = len(h.events_of("L"))
            trigger()
            event, _ = h.wait_event("L", "error-page", after=after)
            return event

        def counts():
            return h.window("L")["handler"]

        # GET: Retry repeats it; Return to start loads the start page.
        get = failure(lambda: h.send(f"go L {start}api/fail"))
        require(get["method"] == "GET" and get.get("retry", False), "GET failure offers Retry", event=get)
        retry = failure(lambda: h.send("action L retry"))
        require(retry["uri"] == f"{start}api/fail", "Retry repeated the failed GET", event=retry)
        h.send("action L start")
        h.wait_event("L", "error-action", after=0, match=lambda e: e["detail"] == "start")
        out["get"] = [get, retry]
        # POST: no Retry; actions never resubmit it.
        post = failure(lambda: h.run("L", start, "armed", then="submit", action="api/fail"))
        require(post["method"] == "POST" and not post.get("retry", False), "POST failure withholds Retry", event=post)
        posts = counts().get("POST /api/fail", 0)
        after = len(h.events_of("L"))
        h.send("action L retry")
        h.wait_event("L", "error-action-refused", after=after)
        h.send("action L start")
        h.wait_event("L", "navigation-allowed", after=after, match=lambda e: e["uri"] == start)
        time.sleep(1)
        require(counts().get("POST /api/fail", 0) == posts, "an error-page action replayed the POST", before=posts, after=counts())
        out["post"] = dict(event=post, posts_seen_by_handler=posts)
        # 303 after a POST: the failure is the GET, which Retry may repeat.
        see = failure(lambda: h.run("L", start, "armed", then="submit", action="api/see-other"))
        require(see["uri"] == f"{start}api/fail" and see["method"] == "GET" and see.get("retry", False), "303 target is a retryable GET", event=see)
        posts = counts().get("POST /api/see-other", 0)
        failure(lambda: h.send("action L retry"))
        require(counts().get("POST /api/see-other", 0) == posts, "Retry after 303 resubmitted the POST")
        out["see_other"] = see
        # 307 keeps the POST: no Retry.
        temp = failure(lambda: h.run("L", start, "armed", then="submit", action="api/temporary"))
        require(temp["method"] == "POST" and not temp.get("retry", False), "307 keeps POST and withholds Retry", event=temp)
        out["temporary"] = temp
        # Actual HTTP errors display normally.
        for code in (404, 500):
            after = len(h.events_of("L"))
            h.send(f"go L {start}api/status?code={code}")
            deadline = time.time() + 10
            while time.time() < deadline and h.window("L")["title"] != f"status {code}":
                time.sleep(0.2)
            require(h.window("L")["title"] == f"status {code}", f"{code} page shown", title=h.window("L")["title"])
            require(not [e for e in h.events_of("L")[after:] if e["kind"] == "error-page"], f"{code} became a load failure")
        out["handler"] = counts()
        out["exit"] = self.done(h)
        return out

    # A successful new load of the failed URI retires the old error actions.
    def error_actions_same_uri(self):
        service = Recorder({"/": ATTACH_PAGE, "/recover": "drop"})
        try:
            h = self.host("same-uri-actions")
            h.open("X", "attach", service.origin + "/", service.origin)
            target = service.origin + "/recover"
            after = len(h.events_of("X"))
            h.send(f"go X {target}")
            failure, _ = h.wait_event("X", "error-page", after=after)
            require(failure.get("retry", False), "initial failure has no Retry", event=failure)
            # The response changes, not its URL. The title comes from that
            # successful document, so no sleep stands in for load progress.
            service.routes["/recover"] = "<!doctype html><title>recovered same URI</title>"
            h.send(f"go X {target}")
            deadline = time.time() + 15
            while time.time() < deadline and h.window("X")["title"] != "recovered same URI":
                time.sleep(0.2)
            require(h.window("X")["title"] == "recovered same URI", "same-URI recovery did not load")
            for action in ("retry", "start"):
                command = f"action X {action}"
                h.send(command)
                rejected = h.expect("error", timeout=5, match=lambda e: e.get("command") == command)
                require(rejected["error"] == "no error page is showing", "old error action still available", event=rejected)
            return dict(failure=failure, recovered=target, exit=self.done(h))
        finally:
            service.stop()

    # An attached service: policy, error actions, and survival.
    def attachment(self):
        collector = Recorder()
        service = Recorder({"/": ATTACH_PAGE, "/fail": "drop", "/fail-post": "drop", "/elsewhere": (302, collector.origin + "/redirected")})
        h = self.host("attachment")
        start = h.open("X", "attach", service.origin + "/", service.origin)
        deadline = time.time() + 15
        while time.time() < deadline and h.window("X")["title"] != "attached service":
            time.sleep(0.2)
        require(h.window("X")["title"] == "attached service", "attached page loaded")
        out = dict(start=start)
        after = len(h.events_of("X"))
        h.send(f"go X {collector.origin}/outside")
        out["outside"], _ = h.wait_event("X", "navigation-refused", after=after)
        after = len(h.events_of("X"))
        h.send(f"go X {service.origin}/elsewhere")
        out["redirect"], _ = h.wait_event("X", "navigation-refused", after=after, match=lambda e: e["uri"].endswith("/redirected"))
        require(not collector.hits(), "attachment navigated outside its origins", hits=collector.hits())
        after = len(h.events_of("X"))
        h.send(f"go X {service.origin}/fail")
        get, _ = h.wait_event("X", "error-page", after=after)
        require(get.get("retry", False), "attached GET failure offers Retry", event=get)
        after = len(h.events_of("X"))
        h.send("action X retry")
        h.wait_event("X", "error-page", after=after)
        posts = service.count("POST", "/fail-post")
        after = len(h.events_of("X"))
        h.send(f"go X {service.origin}/post?target=/fail-post")
        post, _ = h.wait_event("X", "error-page", after=after, match=lambda e: e["uri"].endswith("/fail-post"))
        require(post["method"] == "POST" and not post.get("retry", False), "attached POST failure withholds Retry", event=post)
        posted = service.count("POST", "/fail-post")
        h.send("action X start")
        h.wait_event("X", "error-action", after=after, match=lambda e: e["detail"] == "start")
        time.sleep(1)
        require(service.count("POST", "/fail-post") == posted, "Return to start resubmitted the POST")
        out.update(get=get, post=post, posts_delivered=posted - posts)
        # The service is independent: closing the window leaves it serving.
        h.send("close X")
        h.expect("closed", match=lambda e: e["window"] == "X")
        out["exit"] = self.done(h)
        with urllib.request.urlopen(service.origin + "/", timeout=5) as r:
            require(r.status == 200, "service stopped with its window")
        out["service_requests"] = len(service.hits())
        service.stop()
        collector.stop()
        return out

    # Close while initializing, while handlers ignore cancellation, while
    # a request body is being read.
    def close_cases(self):
        out = {}
        for mode in ("inprocess", "loopback"):
            h = self.host(f"close-init-{mode}")
            h.send(f"open A {mode}")
            h.send("close A")
            h.expect("opened", match=lambda e: e["window"] == "A")
            h.expect("closed", timeout=30, match=lambda e: e["window"] == "A")
            out[f"init-{mode}"] = self.done(h)
        for mode in ("inprocess", "loopback"):
            h = self.host(f"close-handler-{mode}")
            start = h.open("A", mode)
            h.run("A", start, "hold")
            deadline = time.time() + 10
            while time.time() < deadline and h.window("A")["handler"].get("GET /api/hold", 0) == 0:
                time.sleep(0.1)
            require(h.window("A")["handler"].get("GET /api/hold", 0) == 1, "held handler entered")
            h.send("close A")
            h.quiet("closed", 2, match=lambda e: e["window"] == "A")  # the handler still runs
            h.send("release A")
            h.expect("closed", match=lambda e: e["window"] == "A")
            out[f"handler-{mode}"] = self.done(h)
        h = self.host("close-reading")
        start = h.open("A", "inprocess")
        h.run("A", start, "upload")
        h.send("close A")
        h.expect("closed", timeout=30, match=lambda e: e["window"] == "A")
        out["reading"] = self.done(h)
        return out

    # Several windows: closing one leaves the others and the application.
    def close_one_keep_one(self):
        # A window application: the native window opens in setup, as N.
        h = self.host("close-one", resident=False, flags=["-open", "native"])
        a = h.open("A", "inprocess")
        b = h.open("B", "loopback")
        engines = lambda: sorted(n for n in descendants(h.process.pid).values() if n.startswith("WebKit"))
        before = engines()
        h.send("close A")
        h.expect("closed", match=lambda e: e["window"] == "A")
        time.sleep(2)
        after = engines()  # recorded, not asserted: engine-child exit is not host drain
        r = h.run("B", b, "checks")["result"]
        require(r["css"] and r["echo"]["length"] == 1005, "B kept working", result=r)
        require("N" in h.status()["windows"], "native window closed with A")
        h.send("close B")
        h.expect("closed", match=lambda e: e["window"] == "B")
        h.send("close N")  # the last window: the application exits by itself
        h.expect("closed", match=lambda e: e["window"] == "N")
        return dict(engines_before_close=before, engines_after_close=after, exit=self.done(h))

    # Native-only startup loads no browser library; the first window does.
    def lazy_loading(self):
        h = self.host("lazy-loading")
        h.send("native N")
        time.sleep(2)
        before = {lib: mapped(h.process.pid, lib) for lib in ("libwebkit2gtk", "libjavascriptcoregtk")}
        require(not any(before.values()), "browser libraries loaded without a browser window", maps=before)
        h.open("A", "inprocess")
        after = {lib: mapped(h.process.pid, lib) for lib in ("libwebkit2gtk", "libjavascriptcoregtk")}
        require(all(after.values()), "browser libraries not loaded by the first window", maps=after)
        return dict(before=before, after=after, exit=self.done(h))

    # A browser-process failure cancels and drains the application.
    def browser_failure(self):
        h = self.host("browser-failure")
        h.send("native N")
        start = h.open("A", "inprocess")
        h.run("A", start, "checks")
        web = [pid for pid, name in descendants(h.process.pid).items() if name.startswith("WebKitWebProces")]
        require(web, "no web process found", descendants=descendants(h.process.pid))
        os.kill(web[0], signal.SIGKILL)
        return dict(killed=web[0], exit=self.done(h, expect_error="web process terminated", killed={web[0]}))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--out", required=True)
    parser.add_argument("--gdk-backend", default="wayland")
    parser.add_argument("--only", nargs="*")
    args = parser.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    if libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
        raise OSError(ctypes.get_errno(), "PR_SET_CHILD_SUBREAPER")
    scratch = Path(tempfile.mkdtemp(prefix="dfx2-wv-"))
    env = dict(os.environ, GDK_BACKEND=args.gdk_backend, TMPDIR=str(scratch), NO_AT_BRIDGE="1")
    m = Matrix(str(Path(args.binary).resolve()), out, env)
    cases = ["owned_basics", "in_process_alone", "confinement", "storage", "error_pages_loopback", "error_actions_same_uri", "attachment",
             "close_cases", "close_one_keep_one", "lazy_loading", "browser_failure"]
    for name in args.only or cases:
        m.case(name, getattr(m, name))
    summary = dict(binary=args.binary, gdk_backend=args.gdk_backend, gdk_scale=os.environ.get("GDK_SCALE"),
                   passed=all(r["ok"] for r in m.results), cases=m.results)
    (out / "summary.json").write_text(json.dumps(summary, indent=2, default=str) + "\n")
    shutil.rmtree(scratch, ignore_errors=True)
    print(json.dumps(dict(passed=summary["passed"], cases=[(r["case"], r["ok"]) for r in m.results])))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
