#!/usr/bin/env python3
"""Webview integration stage 4's mixed-view audition: the synthetic mixer's
native window and a browser view over one model, in each browser mode, inside
the nested GNOME Shell that controls_drive.py provides (--exec).

Input is real: the shell's virtual pointer and keyboard press the native
window and the page's own controls, located from what the page reports with
-audition. Results come from the mixer's JSON lines: edit and command records,
page reports and status.

    python3 controls_drive.py MIXER --out DIR --exec python3 mixed_drive.py MIXER --out DIR
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import sys
import threading
import time
import urllib.request

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("controls_drive", HERE.parents[1] / "synthetic-mixer/linux/controls_drive.py")
drive = importlib.util.module_from_spec(spec)
spec.loader.exec_module(drive)
Failure, Shell, Mixer, PRESSED, RELEASED = drive.Failure, drive.Shell, drive.Mixer, drive.PRESSED, drive.RELEASED
# The browser matrix's child observer, reused as is: this driver is the
# mixers' subreaper, so WebKit's descendants are reaped and judged by the same
# narrow rule after each mixer exits.
spec = importlib.util.spec_from_file_location("webview_drive", HERE / "webview_drive.py")
children = importlib.util.module_from_spec(spec)
spec.loader.exec_module(children)


class Run:
    """One mixer process with a native window and a browser view."""

    def __init__(self, shell, binary, mode, out, env, extra=(), log=None):
        self.shell, self.mode, self.out = shell, mode, out
        self.mixer = Mixer(binary, ["-browser", mode, "-audition", "-paused", *extra], env, out / (log or f"{mode}.jsonl"))
        self.checks = []
        self.clicks = []  # each real click: where the page said, where it landed
        self.names = {}   # descendants seen while the mixer ran, for naming reaped children
        threading.Thread(target=self.sample, daemon=True).start()

    def sample(self):
        while self.mixer.process.poll() is None:
            self.names.update(children.descendants(self.mixer.process.pid))
            time.sleep(0.25)

    def check(self, name, ok, **detail):
        self.checks.append(dict(check=name, ok=bool(ok), **detail))
        if not ok:
            raise Failure(f"{name}: {detail}")

    def wait(self, what, pred, timeout=20, since=0):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            for e in self.mixer.all[since:]:
                if pred(e):
                    return e
            time.sleep(0.05)
        raise Failure(f"timed out waiting for {what}")

    def mark(self):
        return len(self.mixer.all)

    def page(self, view, pred=lambda p: True, timeout=20, since=None):
        since = self.mark() if since is None else since
        return self.wait(f"page {view}", lambda e: e["event"] == "page" and e["window"] == view and pred(e["page"]), timeout, since)["page"]

    def settled(self, view, channel, accept, timeout=10):
        """The fader value two distinct consecutive page reports agree on:
        a cursor moves past each report once, so one report never counts twice."""
        cursor, last = self.mark(), None
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            while cursor < len(self.mixer.all):
                e = self.mixer.all[cursor]
                cursor += 1
                if e["event"] != "page" or e["window"] != view:
                    continue
                value = self.channel(e["page"], channel)["fader"]
                if last is not None and value == last and accept(value):
                    return value
                last = value
            time.sleep(0.05)
        raise Failure(f"no settled {channel} fader in page {view}")

    def edit(self, view, channel, control, since, timeout=10):
        return self.wait(f"edit {view} {channel} {control}", lambda e: e["event"] == "edit" and e["window"] == view and
                         e["channel"] == channel and e["control"] == control, timeout, since)

    def resolved(self, seq, since=0, timeout=10):
        return self.wait(f"command #{seq} resolved", lambda e: e["event"] == "command" and e["sequence"] == seq and
                         e["state"] != "accepted", timeout, since)

    def status(self):
        at = self.mark()
        self.mixer.command("status")
        return self.wait("status", lambda e: e["event"] == "status", 15, at)

    def opened(self, kind=None, since=0):
        return self.wait("opened", lambda e: e["event"] == "opened" and (kind is None or e.get("kind", "native") == kind), 30, since)

    def closed(self, name, since=0):
        return self.wait(f"closed {name}", lambda e: e["event"] == "closed" and e["window"] == name, 60, since)

    # Locating the page's controls: viewport CSS pixels to compositor
    # coordinates. The view fills the window's client area, which ends at the
    # frame's bottom-right, so the title bar is above it.
    def screen(self, title, page, at):
        frame = self.shell.find(title)
        scale = page["scale"]
        vw, vh = page["viewport"]
        return frame["x"] + frame["w"] - vw * scale + at[0] * scale, frame["y"] + frame["h"] - vh * scale + at[1] * scale

    def click(self, title, page, at):
        self.shell.activate(title)
        time.sleep(0.3)
        x, y = self.screen(title, page, at)
        self.clicks.append(dict(title=title, at=at, screen=[x, y], frame=self.shell.find(title), viewport=page["viewport"], scale=page["scale"]))
        self.shell.pointer(x, y)
        time.sleep(0.15)
        self.shell.button(PRESSED)
        time.sleep(0.08)
        self.shell.button(RELEASED)

    def press_drag(self, title, page, start, end, steps=8):
        """Press at start and move to end in steps, holding the button: the
        caller releases."""
        self.shell.activate(title)
        time.sleep(0.3)
        x0, y0 = self.screen(title, page, start)
        x1, y1 = self.screen(title, page, end)
        self.clicks.append(dict(title=title, drag=[start, end], screen=[[x0, y0], [x1, y1]]))
        self.shell.pointer(x0, y0)
        time.sleep(0.15)
        self.shell.button(PRESSED)
        for i in range(1, steps + 1):
            time.sleep(0.04)
            self.shell.pointer(x0 + (x1 - x0) * i / steps, y0 + (y1 - y0) * i / steps)

    def keys(self, title, *names):
        self.shell.activate(title)
        time.sleep(0.3)
        for n in names:
            self.shell.tap(n)
            time.sleep(0.12)

    def channel(self, page, cid):
        return next(c for c in page["channels"] if c["id"] == cid)

    def screenshot(self, name, title):
        try:
            self.shell.screenshot(self.out / f"{self.mode}-{name}.png", self.shell.find(title))
        except Failure as err:
            self.checks.append(dict(check=f"screenshot {name}", ok=True, note=str(err)))

    def finish(self, timeout=60):
        code = self.mixer.process.wait(timeout=timeout)
        ret = self.wait("return", lambda e: e["event"] == "return", 5)
        self.descendants()
        return code, ret

    def descendants(self, killed=()):
        """The mixer's browser descendants, after it exited: each reaped one
        exited acceptably and none remains. An empty list means no child exit
        status was observed (the mixer reaped them itself), not a clean exit."""
        reaped, remaining = children.reap(dict(self.names))
        problems = [x for x in (children.child_problem(c, killed) for c in reaped) if x]
        self.reaped = reaped
        self.check("browser descendants ended acceptably and none remain", not problems and not remaining,
                   reaped=reaped, remaining=remaining, problems=problems)


def place(r, native, browser):
    """Side by side from the top-left, wholly on the stage, so real input lands."""
    r.stage = json.loads(r.shell.eval("JSON.stringify([global.stage.width, global.stage.height])"))
    r.shell.find(native)
    r.shell.find(browser)
    r.shell.move_window(native, 0, 0)
    r.shell.move_window(browser, r.shell.find(native)["w"] + 10, 0)
    time.sleep(0.5)


def both_directions(r, native, browser, view):
    """Edits in both directions, pending, delayed and rejected outcomes, and
    reorder/removal, between native window A and browser view `view`."""
    place(r, native, browser)
    p = r.page(view, lambda p: len(p["channels"]) == 6, timeout=40)
    r.check("browser view shows the model", [c["id"] for c in p["channels"]] == list("abcdef"), order=[c["id"] for c in p["channels"]])

    # External change: both views follow, and nothing is submitted.
    at = r.mark()
    r.mixer.command("gain a 0.25")
    p = r.page(view, lambda p: abs(r.channel(p, "a")["fader"] - 0.25) < 0.005, since=at)
    r.check("external change reaches the browser view without a command", not [e for e in r.mixer.all[at:] if e["event"] in ("edit", "command")])

    # Browser to native, real pointer: the page's Mute for channel b.
    at = r.mark()
    r.click(browser, p, r.channel(p, "b")["muteAt"])
    e = r.edit(view, "b", "mute", at)
    rec = r.resolved(e["sequence"], at)
    s = r.status()
    b = next(c for c in s["channels"] if c["id"] == "b")
    r.check("browser click is a command the device applies, seen by the model", e["value"] is True and rec["state"] == "applied" and b["muted"], edit=e, record=rec)
    r.screenshot("after-browser-mute", native)

    # Browser fader: a real drag of its thumb from the middle to a quarter of
    # its track, then the keyboard.
    p = r.page(view)
    c = r.channel(p, "c")
    at = r.mark()
    r.press_drag(browser, p, c["faderAt"], [c["faderLeft"] + c["faderWidth"] * 0.25, c["faderAt"][1]])
    # Held at the end of the drag: the page shows the dragged value, and
    # nothing has been sent.
    # The held value is settled: two distinct consecutive reports agree,
    # after the last pointer move had time to land.
    time.sleep(0.4)
    held = r.settled(view, "c", lambda v: v < 0.4)
    edits = lambda: [x for x in r.mixer.all[at:] if x["event"] == "edit"]
    r.check("a held fader drag sends nothing", not edits(), held=held)
    r.shell.button(RELEASED)
    e = r.edit(view, "c", "gain", at)
    # Settled: the command applies, and a report newer than that shows it;
    # only then recount.
    applied = r.resolved(e["sequence"], at)
    after = r.mixer.all.index(applied) + 1
    r.page(view, lambda p: abs(r.channel(p, "c")["fader"] - e["value"]) < 0.005 and r.channel(p, "c")["pending"] == "", since=after)
    r.check("the release sends exactly one gain edit, of the held value", len(edits()) == 1 and abs(e["value"] - held) < 0.005,
            edit=e, held=held, edits=len(edits()))
    first = e["value"]
    at = r.mark()
    r.keys(browser, "right")
    e = r.edit(view, "c", "gain", at)
    r.check("browser keyboard step is one gain edit", abs(e["value"] - (first + 0.01)) < 0.0051, edit=e, first=first)

    # Five quick key presses: their edits and commands arrive in key order,
    # and the last one is applied and shown. Parity evidence: the page orders
    # its submissions, as a native view's input is ordered; the deterministic
    # check is mixer_page_order.js.
    base = e["value"]
    at = r.mark()
    r.shell.activate(browser)
    time.sleep(0.3)
    for _ in range(5):
        r.shell.tap("right")
        time.sleep(0.03)
    r.wait("five browser edits", lambda _: len([x for x in r.mixer.all[at:] if x["event"] == "edit" and x["window"] == view]) >= 5, 15, at)
    quick = [x for x in r.mixer.all[at:] if x["event"] == "edit" and x["window"] == view]
    values = [round(x["value"], 2) for x in quick]
    accepted = [x for x in r.mixer.all[at:] if x["event"] == "command" and x["state"] == "accepted" and x["window"] == view]
    last = r.resolved(quick[-1]["sequence"], at)
    p = r.page(view, lambda p: abs(r.channel(p, "c")["fader"] - quick[-1]["value"]) < 0.005 and r.channel(p, "c")["pending"] == "", since=r.mark())
    r.check("five quick key presses submit in key order and the last applies", len(quick) == 5 and
            values == [round(base + 0.01 * i, 2) for i in range(1, 6)] and
            [x["sequence"] for x in accepted] == [x["sequence"] for x in quick] and last["state"] == "applied",
            values=values, sequences=[x["sequence"] for x in quick], accepted=[x["sequence"] for x in accepted], last=last["state"])

    # Native to browser, real keyboard: Tab, Tab, Space toggles channel a.
    # The browser view sees the native window's edit.
    at = r.mark()
    r.keys(native, "tab", "tab", "space")
    e = r.edit("A", "a", "mute", at)
    r.resolved(e["sequence"], at)
    p = r.page(view, lambda p: r.channel(p, "a")["mute"] is e["value"], since=at)
    r.check("native edit reaches the browser view", True, edit=e)

    # Pending and delayed: with a device delay the browser's edit is pending in
    # both views until it is applied.
    r.mixer.fixture("delay 1500ms")
    at = r.mark()
    r.keys(browser, "right")
    e = r.edit(view, "c", "gain", at)
    p = r.page(view, lambda p: f"#{e['sequence']}" in r.channel(p, "c")["pending"] and "1500 ms" in p["commands"], since=at)
    s = r.status()
    cs = next(x for x in s["channels"] if x["id"] == "c")
    r.check("a delayed browser edit is pending in the model and the page", cs["gain_pending"], channel=cs)
    r.screenshot("pending", browser)
    rec = r.resolved(e["sequence"], at, timeout=10)
    p = r.page(view, lambda p: r.channel(p, "c")["pending"] == "", since=r.mark())
    r.check("the pending value clears once applied", rec["state"] == "applied", record=rec)
    r.mixer.fixture("delay 0s")

    # Rejected: the device refuses the next command; both views show the error.
    r.mixer.fixture("reject 1")
    at = r.mark()
    r.keys(browser, "right")
    e = r.edit(view, "c", "gain", at)
    rec = r.resolved(e["sequence"], at)
    p = r.page(view, lambda p: "rejected" in p["error"], since=at)
    s = r.status()
    r.check("a rejected browser edit shows in both views", rec["state"] == "rejected" and "rejected" in s["last_error"], record=rec, error=p["error"])

    # Reorder and removal arrive as snapshots; nothing is submitted.
    at = r.mark()
    r.mixer.command("reorder")
    r.page(view, lambda p: [c["id"] for c in p["channels"]] == list("fedcba"), since=at)
    r.mixer.command("remove c")
    r.page(view, lambda p: [c["id"] for c in p["channels"]] == list("fedba"), since=at)
    r.mixer.command("insert c")
    r.page(view, lambda p: [c["id"] for c in p["channels"]] == list("fedbac"), since=at)
    r.mixer.command("order a b c d e f")
    r.page(view, lambda p: [c["id"] for c in p["channels"]] == list("abcdef"), since=at)
    r.check("reorder and removal reach the browser view without commands", not [e for e in r.mixer.all[at:] if e["event"] in ("edit", "command")])


def run_mode(shell, binary, mode, out, env):
    r = Run(shell, binary, mode, out, env)
    result = dict(case=mode, ok=False)
    try:
        r.opened("native")
        b = r.opened("browser")
        view = b["window"]
        native, browser = "dfx2 mixer A", f"dfx2 mixer {view} ({mode})"
        both_directions(r, native, browser, view)
        if mode == "inprocess":
            # The browser closes first: the native window and the workers go on.
            at = r.mark()
            r.mixer.command(f"close {view}")
            r.closed(view, at)
            r.mixer.command("resume")
            t0 = r.status()["tick"]
            time.sleep(0.5)
            e_at = r.mark()
            r.keys(native, "space")
            e = r.edit("A", "a", "mute", e_at)
            rec = r.resolved(e["sequence"], e_at)
            r.check("after the browser closed, the sampler runs and native edits apply", r.status()["tick"] > t0 and rec["state"] == "applied")
            r.mixer.command("close A")
        elif mode == "loopback":
            # The native window closes first: the browser view goes on, and its
            # last-window close cancels the outstanding edit.
            at = r.mark()
            r.mixer.command("close A")
            r.closed("A", at)
            p = r.page(view)
            e_at = r.mark()
            r.click(browser, p, r.channel(p, "d")["muteAt"])
            e = r.edit(view, "d", "mute", e_at)
            rec = r.resolved(e["sequence"], e_at)
            r.check("after the native window closed, browser edits apply", rec["state"] == "applied")
            r.mixer.fixture("delay 10s")
            p = r.page(view)
            e_at = r.mark()
            r.click(browser, p, r.channel(p, "e")["muteAt"])
            e = r.edit(view, "e", "mute", e_at)
            r.mixer.command(f"close {view}")
        else:
            # Attachment: closing the attached view leaves the fixture server,
            # which a reopened view uses; it stops only with the application.
            url = r.wait("fixture server", lambda e: e["event"] == "fixture-server")["url"]
            at = r.mark()
            r.mixer.command(f"close {view}")
            r.closed(view, at)
            with urllib.request.urlopen(url + "/api/state?view=probe", timeout=5) as resp:
                r.check("the fixture server survives its attached window", resp.status == 200)
            at = r.mark()
            r.mixer.command("browser")
            again = r.opened("browser", at)["window"]
            p = r.page(again, lambda p: len(p["channels"]) == 6, timeout=40, since=at)
            e_at = r.mark()
            r.click(f"dfx2 mixer {again} ({mode})", p, r.channel(p, "f")["muteAt"])
            e = r.edit(again, "f", "mute", e_at)
            rec = r.resolved(e["sequence"], e_at)
            r.check("a reopened attached view edits through the surviving server", rec["state"] == "applied")
            r.mixer.command("close A")
            r.mixer.command(f"close {again}")
            result["fixture_url"] = url
        code, ret = r.finish()
        k = ret["commands"]
        r.check("the last window's close ends the application cleanly, commands conserved", code == 0 and ret["commands_conserved"], exit=code, ret=ret)
        if mode == "loopback":
            r.check("last-window cancellation cancels the outstanding edit", k["canceled"] >= 1, commands=k)
        if mode == "attach":
            try:
                urllib.request.urlopen(result["fixture_url"] + "/", timeout=2)
                stopped = False
            except OSError:
                stopped = True
            r.check("the fixture server stopped with the application", stopped)
        result.update(ok=True, commands=k)
    except Exception as err:  # recorded, then the process is ended
        result["failure"] = f"{type(err).__name__}: {err}"
        if r.mixer.process.poll() is None:
            r.mixer.process.kill()
            r.mixer.process.wait()
        children.reap(dict(r.names), timeout=5)  # leave no zombie for the next case
    if not result["ok"]:
        for name, title in (("failure-native", "dfx2 mixer A"), ("failure-browser", f"dfx2 mixer B ({mode})")):
            r.screenshot(name, title)
    result["checks"], result["clicks"], result["stage"] = r.checks, r.clicks, getattr(r, "stage", None)
    result["reaped"] = getattr(r, "reaped", None)
    result["stderr"] = Path(str(out / f"{mode}.jsonl") + ".stderr").read_text().splitlines()
    return result


def resident(shell, binary, out, env):
    """Resident reopen: both windows close and the application stays; a new
    native window and browser view open and work; quit drains."""
    r = Run(shell, binary, "inprocess", out, env, extra=("-resident",), log="resident.jsonl")
    result = dict(case="resident", ok=False)
    try:
        r.opened("native")
        view = r.opened("browser")["window"]
        r.page(view, lambda p: len(p["channels"]) == 6, timeout=40)
        at = r.mark()
        r.mixer.command(f"close {view}")
        r.mixer.command("close A")
        r.closed(view, at)
        r.closed("A", at)
        s = r.status()
        r.check("resident with no window", s["windows"] == 0 and r.mixer.process.poll() is None, windows=s["windows"])
        at = r.mark()
        r.mixer.command("open")
        r.mixer.command("browser")
        native = r.opened("native", at)["window"]
        again = r.opened("browser", at)["window"]
        p = r.page(again, lambda p: len(p["channels"]) == 6, timeout=40, since=at)
        e_at = r.mark()
        r.click(f"dfx2 mixer {again} (inprocess)", p, r.channel(p, "a")["muteAt"])
        e = r.edit(again, "a", "mute", e_at)
        rec = r.resolved(e["sequence"], e_at)
        r.check("reopened views share the model and command path", rec["state"] == "applied", native=native, browser=again)
        r.mixer.command("quit")
        code, ret = r.finish()
        r.check("quit drains cleanly", code == 0 and ret["commands_conserved"], exit=code)
        result["ok"] = True
    except Exception as err:
        result["failure"] = f"{type(err).__name__}: {err}"
        if r.mixer.process.poll() is None:
            r.mixer.process.kill()
            r.mixer.process.wait()
        children.reap(dict(r.names), timeout=5)
    result["checks"], result["reaped"] = r.checks, getattr(r, "reaped", None)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--out", required=True)
    parser.add_argument("--gdk-backend", default="wayland")
    parser.add_argument("--only", nargs="*")
    args = parser.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GDK_BACKEND=args.gdk_backend, NO_AT_BRIDGE="1")
    if children.libc.prctl(children.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
        raise OSError("PR_SET_CHILD_SUBREAPER")
    shell = Shell()
    binary = str(Path(args.binary).resolve())
    results = []
    for mode in args.only or ["inprocess", "loopback", "attach", "resident"]:
        if mode == "resident":
            results.append(resident(shell, binary, out, env))
        else:
            results.append(run_mode(shell, binary, mode, out, env))
        print(json.dumps(dict(case=results[-1]["case"], ok=results[-1]["ok"], failure=results[-1].get("failure"))), flush=True)
    summary = dict(binary=binary, gdk_backend=os.environ.get("GDK_BACKEND"), gdk_scale=os.environ.get("GDK_SCALE"),
                   passed=all(r["ok"] for r in results), cases=results)
    (out / "summary.json").write_text(json.dumps(summary, indent=2, default=str) + "\n")
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
