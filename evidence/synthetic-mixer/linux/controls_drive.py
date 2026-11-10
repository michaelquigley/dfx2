#!/usr/bin/env python3
"""Audition examples/mixer -controls with real compositor input, on an isolated
headless GNOME Shell. Nothing reaches the operator's desktop: the nested session
has its own home, runtime directory, session bus and virtual monitor. The shell
runs in unsafe mode only so this driver can read window geometry and create
Clutter virtual input devices through org.gnome.Shell.Eval.

    python3 controls_drive.py /path/to/mixer --out DIR [--base SHORT_TMP]
    python3 controls_drive.py /path/to/mixer --out DIR --exec COMMAND...

--exec runs another audition inside the nested session instead of this one, with
that session's display environment. --base holds the runtime directory. Wayland socket paths must stay under 108
bytes, so it has to be a short path. The driver re-runs itself under
dbus-run-session; the --inner flag marks that second process.
"""
import argparse
import json
import os
from pathlib import Path
import platform
import queue
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time

from PIL import Image, ImageChops

BTN_LEFT, PRESSED, RELEASED = 1, 1, 0  # CLUTTER_BUTTON_PRIMARY
KEYS = dict(tab=0xff09, shift=0xffe1, space=0x20, up=0xff52, down=0xff54, left=0xff51, right=0xff53,
            pageup=0xff55, pagedown=0xff56, home=0xff50, end=0xff57, letter=0x61)
STRIP_X, STRIP_STEP, FADER_X, FADER_Y, FADER_H, THUMB = 16, 122, 18, 16, 160, 12  # strips 110 wide, 12 apart
CHECK_X, CHECK_Y = 24, 195  # the middle of the Mute box, below the 160-tall fader and an 8 gap
TRAVEL = FADER_H - THUMB


class Failure(Exception):
    pass


class Shell:
    def eval(self, script):
        out = subprocess.run(["gdbus", "call", "--session", "-d", "org.gnome.Shell", "-o", "/org/gnome/Shell",
                              "-m", "org.gnome.Shell.Eval", script], capture_output=True, text=True, timeout=20)
        if out.returncode != 0:
            raise Failure(f"Eval failed: {out.stderr.strip()}")
        match = re.fullmatch(r"\((true|false), '(.*)'\)\n?", out.stdout, re.S)
        if not match or match.group(1) != "true":
            raise Failure(f"Eval refused {script!r}: {out.stdout}")
        return match.group(2)

    def wait_ready(self):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            try:
                self.eval("1")
                return
            except (Failure, subprocess.TimeoutExpired):
                time.sleep(0.25)
        raise Failure("nested shell never answered Eval")

    def leave_overview(self):
        # The shell starts in the overview, where windows take no input.
        self.eval("import('resource:///org/gnome/shell/ui/main.js').then(m => m.overview.hide()); 1")
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            self.eval("import('resource:///org/gnome/shell/ui/main.js').then(m => { globalThis.__overview = m.overview.visible; }); 1")
            time.sleep(0.2)
            if self.eval("globalThis.__overview === false") == "true":
                return
        raise Failure("the overview never closed")

    def devices(self):
        self.eval("""const seat = global.stage.context.get_backend().get_default_seat();
globalThis.__p = seat.create_virtual_device(imports.gi.Clutter.InputDeviceType.POINTER_DEVICE);
globalThis.__k = seat.create_virtual_device(imports.gi.Clutter.InputDeviceType.KEYBOARD_DEVICE); 1""")

    def windows(self):
        # Eval encodes its result as JSON itself.
        return json.loads(self.eval("global.get_window_actors().map(a => { const w = a.meta_window; const r = w.get_frame_rect(); "
                                    "return {title: w.get_title(), x: r.x, y: r.y, w: r.width, h: r.height, focused: w.has_focus()}; })"))

    def find(self, title, timeout=20):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            for w in self.windows():
                if w["title"] == title:
                    return w
            time.sleep(0.1)
        raise Failure(f"no window titled {title!r}: {self.windows()}")

    def on_window(self, title, action):
        self.eval(f"global.get_window_actors().map(a => a.meta_window).filter(w => w.get_title() === {json.dumps(title)}).forEach(w => {{ {action} }}); 1")

    def move_window(self, title, x, y):
        self.on_window(title, f"w.move_frame(true, {x}, {y});")

    def activate(self, title):
        self.on_window(title, "w.activate(global.get_current_time());")

    def pointer(self, x, y):
        self.eval(f"__p.notify_absolute_motion(0, {float(x)}, {float(y)}); 1")

    def button(self, state):
        self.eval(f"__p.notify_button(0, {BTN_LEFT}, {state}); 1")

    def key(self, name, state):
        self.eval(f"__k.notify_keyval(0, {KEYS[name]}, {state}); 1")

    def tap(self, name):
        self.key(name, PRESSED)
        self.key(name, RELEASED)

    def screenshot(self, path, frame):
        """The compositor's own pixels for a window's frame: what reached the
        display, unlike a retained-image snapshot."""
        out = subprocess.run(["gdbus", "call", "--session", "-d", "org.gnome.Shell.Screenshot", "-o", "/org/gnome/Shell/Screenshot",
                              "-m", "org.gnome.Shell.Screenshot.ScreenshotArea", str(frame["x"]), str(frame["y"]), str(frame["w"]),
                              str(frame["h"]), "false", str(path)], capture_output=True, text=True, timeout=20)
        if out.returncode != 0 or "true" not in out.stdout:
            raise Failure(f"screenshot failed: {out.stdout} {out.stderr}")


class Mixer:
    def __init__(self, binary, flags, env, log):
        self.events = queue.Queue()
        self.all = []
        self.log = open(log, "w")
        self.stderr = open(str(log) + ".stderr", "w")
        self.process = subprocess.Popen([binary, *flags], env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.stderr, text=True)
        threading.Thread(target=self.read, daemon=True).start()

    def read(self):
        for line in self.process.stdout:
            self.log.write(line)
            self.log.flush()
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            self.all.append(event)
            self.events.put(event)

    def command(self, line):
        self.process.stdin.write(line + "\n")
        self.process.stdin.flush()

    def expect(self, name, timeout=15, match=lambda e: True):
        deadline = time.monotonic() + timeout
        while True:
            left = deadline - time.monotonic()
            if left <= 0:
                raise Failure(f"timed out waiting for {name}")
            try:
                event = self.events.get(timeout=left)
            except queue.Empty as err:
                raise Failure(f"timed out waiting for {name}") from err
            if event["event"] == name and match(event):
                return event

    def edits(self):
        return [e for e in self.all if e["event"] == "edit"]

    def commands(self):
        return [e for e in self.all if e["event"] == "command"]

    def fixture(self, line):
        self.command(line)
        self.expect("fixture")

    def status(self):
        self.command("status")
        return self.expect("status")

    def external(self, line):
        self.command(line)
        self.expect("external", match=lambda e: e["command"] == line)


class Audition:
    def __init__(self, shell, mixer, scale):
        self.shell, self.mixer, self.scale = shell, mixer, scale
        self.origin = {}
        self.results = []

    def settle(self, commands=True, timeout=10):
        """Every native event sent so far has been processed once a frame-free
        status round trip finds the queue empty twice; with commands, every
        accepted command has also been resolved. Wall time only guards a hang."""
        deadline = time.monotonic() + timeout
        quiet = 0
        while quiet < 2:
            if time.monotonic() > deadline:
                raise Failure("input queue never drained" + (" or commands stayed outstanding" if commands else ""))
            time.sleep(0.05)
            st = self.mixer.status()
            done = st["input_queued"] == 0 and (not commands or st["commands_outstanding"] == 0)
            quiet = quiet + 1 if done else 0
        time.sleep(0.15)  # compositor delivery to the client has no observable barrier

    def channel(self, status, cid):
        return next(c for c in status["channels"] if c["id"] == cid)

    def commands_since(self, mark):
        return self.mixer.commands()[mark:]

    def edits_since(self, mark):
        return self.mixer.edits()[mark:]

    def mark(self):
        self.settle()
        return len(self.mixer.edits())

    def at(self, window, x, y):
        ox, oy = self.origin[window]
        return ox + x * self.scale, oy + y * self.scale

    def fader(self, window, strip, value):
        x = STRIP_X + strip * STRIP_STEP + FADER_X
        y = FADER_Y + THUMB / 2 + (1 - value) * TRAVEL
        return self.at(window, x, y)

    def check_point(self, window, strip):
        return self.at(window, STRIP_X + strip * STRIP_STEP + CHECK_X, CHECK_Y)

    def press_at(self, point):
        self.shell.pointer(*point)
        self.shell.button(PRESSED)

    def release_at(self, point):
        self.shell.pointer(*point)
        self.shell.button(RELEASED)

    def calibrate(self, window):
        """Find the drawing area on screen from the fader's own answer. Title bars
        differ between CSD and server-side frames. Y comes from the edited value;
        X from the frame's left edge, which the 36-unit-wide fader tolerates."""
        title = f"dfx2 mixer {window}"
        frame = self.shell.find(title)
        self.shell.activate(title)
        guess_x = frame["x"] + (STRIP_X + FADER_X) * self.scale
        for guess_top in range(frame["y"] + 90 * self.scale, frame["y"] + 170 * self.scale, 20 * self.scale):
            mark = self.mark()
            self.press_at((guess_x, guess_top))
            self.shell.button(RELEASED)
            self.settle()
            found = self.edits_since(mark)
            if found:
                value = found[0]["value"]
                self.origin[window] = (frame["x"], guess_top - (FADER_Y + THUMB / 2 + (1 - value) * TRAVEL) * self.scale)
                self.mixer.external("gain a 0.5")
                return
        raise Failure(f"calibration found no fader in window {window}: {frame}")

    def snap(self, name, commands=True, window=""):
        self.settle(commands=commands)
        self.mixer.command(f"snapshot {name} {window}".strip())
        self.mixer.expect("snapshot")

    def screenshot(self, name, window):
        out = Path(self.mixer.log.name).parent
        self.shell.screenshot(out / f"{name}.png", self.shell.find(f"dfx2 mixer {window}"))

    def case(self, name, body):
        started = time.monotonic()
        try:
            detail = body() or {}
            self.results.append(dict(case=name, ok=True, seconds=round(time.monotonic() - started, 2), **detail))
        except Failure as err:
            self.results.append(dict(case=name, ok=False, error=str(err)))
            self.shell.button(RELEASED)
            raise

    def require(self, condition, message, **detail):
        if not condition:
            raise Failure(f"{message}: {json.dumps(detail, default=str)}")

    def drag_outside_window(self):
        window, frame = "A", self.shell.find("dfx2 mixer A")
        self.shell.activate("dfx2 mixer A")
        self.mixer.external("gain a 0.5")
        mark = self.mark()
        self.press_at(self.fader(window, 0, 0.5))
        self.settle()
        self.require(not self.edits_since(mark), "a press at the current value edited", edits=self.edits_since(mark))
        for value in (0.6, 0.7, 0.8):
            self.shell.pointer(*self.fader(window, 0, value))
            self.settle()
        inside = self.edits_since(mark)
        self.require(len(inside) >= 3 and abs(inside[-1]["value"] - 0.8) < 0.02 and all(e["channel"] == "a" and e["control"] == "gain" for e in inside),
                     "drag did not follow the pointer", edits=inside)
        # Well outside the window, above it and to its right.
        outside = (frame["x"] + frame["w"] + 120, max(frame["y"] - 30, 0))
        self.shell.pointer(*outside)
        self.settle()
        moved = self.edits_since(mark)
        self.require(moved[-1]["value"] == 1, "capture did not track outside the window", edits=moved[-3:])
        below = (frame["x"] + frame["w"] + 120, frame["y"] + frame["h"] + 200)
        self.shell.pointer(*below)
        self.settle()
        moved = self.edits_since(mark)
        self.require(moved[-1]["value"] == 0, "capture did not clamp below the window", edits=moved[-3:])
        count = len(moved)
        self.shell.button(RELEASED)
        self.settle()
        self.require(len(self.edits_since(mark)) == count, "release at the last value emitted a duplicate", edits=self.edits_since(mark)[-3:])
        mark = self.mark()
        self.shell.pointer(*self.fader(window, 0, 0.9))
        self.settle()
        self.require(not self.edits_since(mark), "motion after an outside release still edited", edits=self.edits_since(mark))
        return dict(edits=count, release_outside=True)

    def checkbox(self):
        window = "A"
        self.shell.activate("dfx2 mixer A")
        self.mixer.external("mute a off")
        mark = self.mark()
        point = self.check_point(window, 0)
        self.press_at(point)
        self.settle()
        self.require(not self.edits_since(mark), "a checkbox activated on press")
        self.shell.button(RELEASED)
        self.settle()
        edits = self.edits_since(mark)
        self.require(len(edits) == 1 and edits[0]["value"] is True and edits[0]["control"] == "mute", "release inside", edits=edits)
        mark = self.mark()
        self.press_at(point)
        self.release_at((point[0] + 900, point[1] + 400))
        self.settle()
        self.require(not self.edits_since(mark), "release outside activated", edits=self.edits_since(mark))
        return dict()

    def keyboard(self):
        self.shell.activate("dfx2 mixer A")
        self.mixer.external("gain a 0.5")
        self.mixer.external("mute a off")
        self.snap("controls-before")
        # A press focuses; then Tab traversal from there.
        self.press_at(self.fader("A", 0, 0.5))
        self.shell.button(RELEASED)
        mark = self.mark()
        for name in ("up", "up", "right", "down", "left", "pageup", "pagedown", "home", "home", "down", "end"):
            self.shell.tap(name)
            self.settle()
        values = [round(e["value"], 6) for e in self.edits_since(mark)]
        self.require(values == [0.51, 0.52, 0.53, 0.52, 0.51, 0.61, 0.51, 0, 1], "fader keys", values=values)
        mark = self.mark()
        self.shell.tap("tab")
        self.shell.key("space", PRESSED)
        self.settle()
        self.require(not self.edits_since(mark), "Space activated on press")
        self.shell.key("space", RELEASED)
        self.settle()
        self.shell.tap("tab")  # onto channel b's fader
        self.shell.tap("up")
        self.settle()
        edits = self.edits_since(mark)
        self.require([(e["channel"], e["control"]) for e in edits] == [("a", "mute"), ("b", "gain")], "Tab traversal", edits=edits)
        mark = self.mark()
        self.shell.key("shift", PRESSED)
        self.shell.tap("tab")
        self.shell.key("shift", RELEASED)
        self.shell.key("space", PRESSED)
        self.shell.key("space", RELEASED)
        self.settle()
        edits = self.edits_since(mark)
        self.require([(e["channel"], e["control"]) for e in edits] == [("a", "mute")], "Shift+Tab", edits=edits)
        mark = self.mark()
        for _ in range(3):
            self.shell.tap("letter")
        self.settle()
        self.require(not self.edits_since(mark), "an unused key edited")
        self.settle()
        self.snap("controls-focused")
        return dict(values=values)

    def independent_focus(self):
        # Each window keeps its own focused control; keys go to the active window's.
        self.shell.activate("dfx2 mixer A")
        self.press_at(self.fader("A", 0, 0.5))
        self.shell.button(RELEASED)
        self.settle()
        self.shell.activate("dfx2 mixer B")
        self.settle()
        self.press_at(self.fader("B", 2, 0.5))
        self.shell.button(RELEASED)
        self.settle()
        mark = self.mark()
        self.shell.tap("up")
        self.settle()
        self.shell.activate("dfx2 mixer A")
        self.settle()
        self.shell.tap("up")
        self.settle()
        self.shell.activate("dfx2 mixer B")
        self.settle()
        self.shell.tap("up")
        self.settle()
        edits = self.edits_since(mark)
        seen = [(e["window"], e["channel"]) for e in edits]
        self.require(seen == [("B", "c"), ("A", "a"), ("B", "c")], "focus leaked between windows", edits=edits)
        return dict(sequence=seen)

    def external_during_drag(self):
        self.shell.activate("dfx2 mixer A")
        self.mixer.external("gain b 0.5")
        mark = self.mark()
        self.mixer.external("gain b 0.9")
        self.mixer.external("mute b on")
        self.settle()
        self.require(not self.edits_since(mark), "an external change echoed as an edit", edits=self.edits_since(mark))
        self.press_at(self.fader("A", 1, 0.9))
        self.settle()
        base = len(self.edits_since(mark))
        self.mixer.external("gain b 0.1")
        self.mixer.external("gain b 0.3")
        self.settle()
        self.require(len(self.edits_since(mark)) == base, "an external change during a drag echoed", edits=self.edits_since(mark))
        self.shell.pointer(*self.fader("A", 1, 0.6))
        self.settle()
        edits = self.edits_since(mark)
        self.require(len(edits) == base + 1 and abs(edits[-1]["value"] - 0.6) < 0.02, "the gesture lost its value", edits=edits)
        self.shell.button(RELEASED)
        self.settle()
        return dict(edits=len(self.edits_since(mark)))

    def reorder_during_drag(self):
        self.shell.activate("dfx2 mixer A")
        self.mixer.external("gain a 0.5")
        mark = self.mark()
        self.press_at(self.fader("A", 0, 0.8))
        self.settle()
        self.mixer.external("reorder")  # a is now the last strip; the pointer sits over c
        self.settle()
        self.shell.pointer(*self.fader("A", 0, 0.3))
        self.settle()
        self.shell.button(RELEASED)
        self.settle()
        edits = self.edits_since(mark)
        self.require(edits and all(e["channel"] == "a" for e in edits), "a reorder retargeted the gesture", edits=edits)
        self.mixer.external("reorder")
        mark = self.mark()
        self.press_at(self.fader("A", 0, 0.2))
        self.shell.button(RELEASED)
        self.settle()
        self.require(self.edits_since(mark) and self.edits_since(mark)[0]["channel"] == "a", "press after reordering back", edits=self.edits_since(mark))
        return dict()

    def remove_during_drag(self):
        self.shell.activate("dfx2 mixer A")
        self.mixer.external("gain a 0.5")
        mark = self.mark()
        self.press_at(self.fader("A", 0, 0.7))
        self.settle()
        count = len(self.edits_since(mark))
        self.mixer.external("remove a")
        self.settle()
        self.shell.pointer(*self.fader("A", 0, 0.2))
        self.settle()
        self.shell.button(RELEASED)
        self.settle()
        self.require(len(self.edits_since(mark)) == count, "a removed target edited", edits=self.edits_since(mark))
        self.mixer.external("insert a")  # back as the last strip
        self.settle()
        mark = self.mark()
        self.shell.pointer(*self.fader("A", 5, 0.2))
        self.settle()
        self.require(not self.edits_since(mark), "reinserted fader inherited the gesture")
        self.shell.button(PRESSED)
        self.shell.button(RELEASED)
        self.settle()
        edits = self.edits_since(mark)
        self.require(edits and edits[0]["channel"] == "a", "reinserted fader is not interactive", edits=edits)
        self.mixer.external("order a b c d e f")
        self.settle()
        return dict()

    def focus_loss_during_drag(self):
        self.shell.activate("dfx2 mixer A")
        strip = 1  # channel b, with the strips back in order
        self.mixer.external("gain b 0.5")
        mark = self.mark()
        self.press_at(self.fader("A", strip, 0.8))
        self.settle()
        count = len(self.edits_since(mark))
        self.shell.activate("dfx2 mixer B")
        self.settle()
        self.shell.pointer(*self.fader("A", strip, 0.1))
        self.settle()
        self.shell.button(RELEASED)
        self.settle()
        edits = self.edits_since(mark)
        self.require(len(edits) == count, "a drag survived focus loss", edits=edits)
        # Nothing is stuck: a fresh press works.
        self.shell.activate("dfx2 mixer A")
        self.settle()
        mark = self.mark()
        self.press_at(self.fader("A", strip, 0.2))
        self.shell.button(RELEASED)
        self.settle()
        self.require(self.edits_since(mark), "no press after focus loss")
        return dict()

    def hover_is_idle(self):
        self.shell.activate("dfx2 mixer A")
        self.settle()
        before = self.mixer.status()
        for x, y in ((10, 10), (60, 60), (120, 60), (200, 40), (300, 90)):
            self.shell.pointer(*self.at("A", x, y))
            time.sleep(0.05)
        self.shell.tap("letter")
        time.sleep(0.5)
        after = self.mixer.status()
        frames = after["frames"] - before["frames"]
        self.require(frames == 0, "hover or an unused key caused frames", before=before, after=after)
        time.sleep(3)
        quiet = self.mixer.status()
        deltas = {k: quiet[k] - after[k] for k in ("frames", "frame_timers", "presents", "evaluations")}
        self.require(not any(deltas.values()), "settled controls did not go idle", deltas=deltas)
        return dict(hover_frames=frames, quiet=deltas, status_evaluations=quiet["evaluations"])

    def close_during_drag(self):
        """Close window B mid-drag by name; window C keeps responding."""
        self.shell.activate("dfx2 mixer B")
        self.settle()
        mark = self.mark()
        self.press_at(self.fader("B", 2, 0.5))
        self.settle()
        self.mixer.command("close B")
        self.mixer.expect("closed", match=lambda e: e["window"] == "B")
        self.shell.button(RELEASED)
        self.settle()
        count = len(self.edits_since(mark))
        self.shell.activate("dfx2 mixer C")
        self.settle()
        mark = self.mark()
        self.press_at(self.fader("C", 2, 0.9))
        self.shell.button(RELEASED)
        self.settle()
        self.require(self.edits_since(mark), "the surviving window stopped responding")
        return dict(edits_before_close=count)

    # Stage 4: the command worker, its fixtures and the application's lifetime.

    def focus_fader(self, window, strip):
        """Focus a strip's fader with a press at its current value; the press may
        report that value once, which the cases ignore by marking afterwards."""
        value = self.shown(self.mixer.status(), strip)
        self.press_at(self.fader(window, strip, value))
        self.shell.button(RELEASED)
        self.settle()

    def shown(self, status, strip):
        c = status["channels"][strip]
        return c["pending_gain"] if c["gain_pending"] else c["gain"]

    def delayed_commands(self):
        """A 700 ms device delay: edits show at once as pending values, keep
        their order, and the window stays responsive while they wait."""
        self.shell.activate("dfx2 mixer A")
        self.mixer.external("gain d 0.5")
        self.mixer.external("mute d off")
        self.settle()
        self.focus_fader("A", 3)
        self.mixer.fixture("delay 700ms")
        mark, cmark = self.mark(), len(self.mixer.commands())
        for _ in range(3):
            self.shell.tap("up")
        self.settle(commands=False)
        values = [round(e["value"], 6) for e in self.edits_since(mark)]
        self.require(values == [0.51, 0.52, 0.53], "delayed fader keys", values=values)
        pending = self.mixer.status()
        self.require(pending["commands_outstanding"] >= 2, "commands were not held", status=pending["commands"])
        d = self.channel(pending, "d")
        self.require(d["gain_pending"] and d["pending_gain"] == 0.53 and d["gain"] < 0.53, "pending value not published", d=d)
        # Responsive while the device lags: a toggle edits at once, and an
        # external change on another channel renders.
        self.shell.tap("tab")
        self.shell.key("space", PRESSED)
        self.shell.key("space", RELEASED)
        frames = self.mixer.status()["frames"]
        self.mixer.external("gain e 0.7")
        self.settle(commands=False)
        during = self.mixer.status()
        edits = self.edits_since(mark)
        self.require(edits[-1]["control"] == "mute" and edits[-1]["value"] is True and during["commands_outstanding"] >= 1,
                     "a toggle waited for delayed commands", edits=edits[-2:], outstanding=during["commands_outstanding"])
        self.require(during["frames"] > frames, "an external change did not render while commands waited")
        self.snap("delayed-pending", commands=False, window="A")
        self.screenshot("screen-delayed-pending", "A")
        self.settle(timeout=15)
        records = self.commands_since(cmark)
        applied = [r for r in records if r["state"] == "applied"]
        self.require([r["value"] for r in applied] == [0.51, 0.52, 0.53, True] and [r["sequence"] for r in applied] == sorted(r["sequence"] for r in applied),
                     "delayed commands out of order", applied=applied)
        latencies = [r["latency_ms"] for r in applied]
        # Each waited at least the delay at the device; one at a time, so the
        # three gain commands accepted together resolve about 700 ms apart.
        self.require(all(l >= 700 for l in latencies) and latencies[2] >= 2000, "commands skipped the device delay", latencies=latencies)
        after = self.channel(self.mixer.status(), "d")
        self.require(after["gain"] == 0.53 and after["muted"] and not after["gain_pending"] and not after["mute_pending"], "device state", d=after)
        self.mixer.fixture("delay 0s")
        return dict(latencies_ms=latencies)

    def rejected_commands(self):
        """The device refuses an earlier command while a later one is pending,
        then refuses the latest: the view reconciles to the device's value,
        shows the error, and reconciliation submits nothing."""
        self.shell.activate("dfx2 mixer A")
        self.shell.key("shift", PRESSED)
        self.shell.tap("tab")  # back onto d's fader from its mute
        self.shell.key("shift", RELEASED)
        self.settle()
        self.mixer.fixture("delay 400ms")
        self.mixer.fixture("reject 1")
        mark, cmark = self.mark(), len(self.mixer.commands())
        self.shell.tap("up")
        self.shell.tap("up")
        self.settle()
        resolved = [(r["state"], r["value"]) for r in self.commands_since(cmark) if r["state"] != "accepted"]
        self.require(resolved == [("rejected", 0.54), ("applied", 0.55)], "earlier rejection", resolved=resolved)
        self.require(self.channel(self.mixer.status(), "d")["gain"] == 0.55, "the later edit did not apply")
        self.mixer.fixture("reject 1")
        self.shell.tap("up")
        self.settle()
        status = self.mixer.status()
        d = self.channel(status, "d")
        self.require(d["gain"] == 0.55 and not d["gain_pending"], "rejection did not reconcile to the device", d=d)
        self.require("rejected: device refused" in status["last_error"] and "0.56" in status["last_error"], "error not shown", error=status["last_error"])
        self.snap("rejected-error", window="A")
        self.screenshot("screen-rejected-error", "A")
        time.sleep(0.5)  # a quiet stretch in which reconciliation must not submit
        self.settle()
        edits = self.edits_since(mark)
        accepted = [r for r in self.commands_since(cmark) if r["state"] == "accepted"]
        self.require(len(edits) == 3 and len(accepted) == 3, "reconciliation submitted commands", edits=edits, accepted=accepted)
        self.mixer.fixture("delay 0s")
        return dict(last_error=status["last_error"])

    def overloaded_commands(self):
        """A held device and 70 key edits: 64 queue behind the one at the
        device, the rest are refused at once and shown, nothing merges, and
        releasing the device applies the accepted ones in order."""
        self.shell.activate("dfx2 mixer A")
        self.settle()
        self.mixer.fixture("delay 60s")
        mark, cmark = self.mark(), len(self.mixer.commands())
        for i in range(70):
            self.shell.tap("up" if i % 2 == 0 else "down")
        self.settle(commands=False)
        edits = self.edits_since(mark)
        records = self.commands_since(cmark)
        accepted = [r for r in records if r["state"] == "accepted"]
        refused = [r for r in records if r["state"] == "rejected" and r.get("reason") == "queue full"]
        refused_edits = [e for e in edits if e.get("refused")]
        # 64 queue behind the command the device holds; if the worker had not
        # yet taken the first, 64 in all. Either way nothing is merged or lost.
        self.require(len(edits) == 70 and len(accepted) + len(refused) == 70 and len(accepted) in (64, 65) and len(refused_edits) == len(refused),
                     "overload", edits=len(edits), accepted=len(accepted), refused=len(refused))
        self.require([r["sequence"] for r in refused] == [e["sequence"] for e in edits[-len(refused):]], "refusals were not the newest edits")
        status = self.mixer.status()
        self.require(status["commands_outstanding"] == len(accepted) and "queue full" in status["last_error"], "overload not shown", status=status["commands"])
        self.snap("overload-error", commands=False, window="A")
        self.screenshot("screen-overload-error", "A")
        self.mixer.fixture("delay 0s")
        self.settle(timeout=20)
        applied = [r for r in self.commands_since(cmark) if r["state"] == "applied"]
        self.require([r["sequence"] for r in applied] == [r["sequence"] for r in accepted], "applied order", applied=[r["sequence"] for r in applied])
        d = self.channel(self.mixer.status(), "d")
        self.require(d["gain"] == accepted[-1]["value"] and not d["gain_pending"], "device after overload", d=d, last=accepted[-1])
        return dict(accepted=len(accepted), refused=len(refused))

    def external_while_pending(self):
        """The device changes under a pending edit: no command, the pending value
        stays shown, and the device ends at the later of the two in its order."""
        self.shell.activate("dfx2 mixer A")
        self.settle()
        self.mixer.fixture("delay 800ms")
        mark, cmark = self.mark(), len(self.mixer.commands())
        self.shell.tap("up")
        self.settle(commands=False)
        edit = self.edits_since(mark)
        self.require(len(edit) == 1, "the key did not edit", edits=edit)
        self.mixer.external("gain d 0.2")
        self.settle(commands=False)
        d = self.channel(self.mixer.status(), "d")
        self.require(d["gain"] == 0.2 and d["gain_pending"] and d["pending_gain"] == edit[0]["value"], "pending under an external change", d=d)
        self.settle()
        d = self.channel(self.mixer.status(), "d")
        accepted = [r for r in self.commands_since(cmark) if r["state"] == "accepted"]
        self.require(d["gain"] == edit[0]["value"] and len(self.edits_since(mark)) == 1 and len(accepted) == 1, "external change echoed or lost", d=d)
        self.mixer.fixture("delay 0s")
        return dict()

    def timing(self):
        """Observations, not promises: frame rate, view evaluations and process
        CPU with the feed running, with text churn, and after everything settles."""
        def measure(seconds):
            before = self.mixer.status()
            t0 = time.monotonic()
            time.sleep(seconds)
            after = self.mixer.status()
            dt = time.monotonic() - t0
            return dict(seconds=round(dt, 2), frames_per_s=round((after["frames"] - before["frames"]) / dt, 1),
                        evaluations_per_s=round((after["evaluations"] - before["evaluations"]) / dt, 1),
                        presents_per_s=round((after["presents"] - before["presents"]) / dt, 1),
                        cpu_percent=round(100 * (after["cpu_seconds"] - before["cpu_seconds"]) / dt, 1),
                        ticks=after["tick"] - before["tick"]), after
        self.mixer.command("resume")
        self.mixer.expect("feed", match=lambda e: e["running"])
        time.sleep(1)
        running, _ = measure(5)
        self.mixer.fixture("churn 36")
        time.sleep(1)
        churn, status = measure(5)
        self.snap("timing-churn", commands=False, window="A")
        self.mixer.fixture("churn off")
        self.mixer.command("pause")
        self.mixer.expect("feed", match=lambda e: not e["running"])
        self.require(running["ticks"] >= 120, "the 30 Hz producer did not run", running=running)
        # Settled: no animation deadline for 250 ms, then three quiet seconds.
        deadline, since = time.monotonic() + 10, None
        while True:
            if time.monotonic() > deadline:
                raise Failure("meters never settled")
            animating = self.mixer.status()["animating"]
            if animating:
                since = None
            elif since is None:
                since = time.monotonic()
            elif time.monotonic() - since >= 0.25:
                break
            time.sleep(0.02)
        quiet, _ = measure(3)
        self.require(quiet["frames_per_s"] == 0 and quiet["evaluations_per_s"] == 0 and quiet["presents_per_s"] == 0,
                     "settled mixer did not go idle", quiet=quiet)
        timers = self.mixer.status()["frame_timers"]
        time.sleep(1)
        self.require(self.mixer.status()["frame_timers"] == timers, "a deadline timer ran while settled")
        return dict(running=running, churn=churn, quiet=quiet, text=status["text"])

    def close_first_window(self):
        """Open C, then close A, the first window: B and C keep working, and so do
        the application's producer and command worker."""
        self.mixer.command("open")
        self.mixer.expect("opened", match=lambda e: e["window"] == "C")
        self.shell.find("dfx2 mixer C")
        self.mixer.command("close A")
        self.mixer.expect("closed", match=lambda e: e["window"] == "A")
        time.sleep(0.5)
        self.shell.move_window("dfx2 mixer C", 40, 80)
        time.sleep(0.5)
        self.calibrate("C")
        status = self.mixer.status()
        self.require(status["windows"] == 2 and status["tasks"] >= 2, "closing A changed the application", status=status)
        out = {}
        for window, strip, value in (("B", 1, 0.7), ("C", 2, 0.3)):
            self.shell.activate(f"dfx2 mixer {window}")
            self.settle()
            mark, cmark = self.mark(), len(self.mixer.commands())
            self.press_at(self.fader(window, strip, value))
            self.shell.button(RELEASED)
            self.settle()
            applied = [r for r in self.commands_since(cmark) if r["state"] == "applied"]
            self.require(self.edits_since(mark) and applied, f"window {window} stopped working after A closed")
            out[window] = applied[-1]["value"]
        return out

    def shutdown_outstanding(self):
        """Quit with five commands waiting on the device: each is canceled,
        none is reported applied, and Run returns cleanly."""
        self.shell.activate("dfx2 mixer C")
        self.settle()
        self.press_at(self.fader("C", 4, self.shown(self.mixer.status(), 4)))
        self.shell.button(RELEASED)
        self.settle()
        self.mixer.fixture("delay 60s")
        mark, cmark = self.mark(), len(self.mixer.commands())
        for _ in range(5):
            self.shell.tap("up")
        self.settle(commands=False)
        self.require(len(self.edits_since(mark)) == 5 and self.mixer.status()["commands_outstanding"] == 5, "commands not outstanding")
        self.mixer.command("quit")
        self.mixer.expect("drained", timeout=20)
        records = self.commands_since(cmark)
        accepted = sorted(r["sequence"] for r in records if r["state"] == "accepted")
        canceled = sorted(r["sequence"] for r in records if r["state"] == "canceled")
        applied = [r for r in records if r["state"] == "applied"]
        returned = [e for e in self.mixer.all if e["event"] == "return"][-1]
        self.require(accepted == canceled and len(accepted) == 5 and not applied, "shutdown", accepted=accepted, canceled=canceled, applied=applied)
        self.require(returned["commands_conserved"] and returned["commands"]["outstanding"] == 0, "command totals", returned=returned)
        return dict(canceled=canceled, totals=returned["commands"])

def text_audition(shell, binary, out, env, budget, scale):
    """A separate mixer process with a churn label: changing words and numbers
    force eviction; counters must stay within the budget between frames, and
    a window that has evicted must render exactly what a fresh window renders.
    The small glyph budget scales with the square of the display scale, as
    glyph pixels do. It has to sit above the view's steady working set (a
    fresh window holds about 50 KB at scale 2) and below every glyph the churn
    alphabet can produce (about 215 KB at scale 2), or nothing is evictable."""
    flag = budget if budget == "default" else f"{48 * 1024 * scale * scale}:{64 * 1024}:48"
    out = out / f"text-{budget}"
    out.mkdir(exist_ok=True)
    mixer = Mixer(binary, ["-controls", "-paused", "-text-budget", flag, "-churn", "36", "-snapshots", str(out)], env, out / "mixer.jsonl")
    result = dict(budget=budget, flag=flag, passed=False)
    try:
        mixer.expect("opened", match=lambda e: e["window"] == "B", timeout=60)
        shell.find("dfx2 mixer A")
        shell.find("dfx2 mixer B")
        time.sleep(1)
        shell.move_window("dfx2 mixer A", 40, 80)
        shell.move_window("dfx2 mixer B", 40 + shell.find("dfx2 mixer A")["w"] + 200, 80)
        mixer.command("resume")
        mixer.expect("feed", match=lambda e: e["running"])
        samples = []
        for _ in range(8):
            time.sleep(0.5)
            samples.append(mixer.status())
        mixer.command("pause")
        mixer.expect("feed", match=lambda e: not e["running"])
        time.sleep(2.5)  # meters fall at 0.5 per second; the audition compares settled images
        shell.activate("dfx2 mixer A")
        time.sleep(0.5)
        shell.screenshot(out / f"screen-text-{budget}-A.png", shell.find("dfx2 mixer A"))
        mixer.command("open")
        mixer.expect("opened", match=lambda e: e["window"] == "C")
        shell.find("dfx2 mixer C")
        time.sleep(1.5)  # C renders the snapshot A last rendered: no publication since the pause
        final = mixer.status()
        for w in ("A", "C"):
            mixer.command(f"snapshot text-{budget}-{w} {w}")
            mixer.expect("snapshot")
        limit = final["text_budget"]
        over = [(i, w, s) for i, st in enumerate(samples + [final]) for w, s in st["text"].items()
                if s["glyph_bytes"] > limit["glyph_bytes"] or s["paragraph_bytes"] > limit["paragraph_bytes"] or s["paragraph_entries"] > limit["paragraph_entries"]]
        a, c = Image.open(out / f"text-{budget}-A.png"), Image.open(out / f"text-{budget}-C.png")
        same = a.size == c.size and ImageChops.difference(a.convert("RGB"), c.convert("RGB")).getbbox() is None
        evictions = final["text"]["A"]
        result.update(samples=[st["text"] for st in samples], final=final["text"], budget_limits=limit, over_budget=over,
                      a_matches_fresh_c=same, size=a.size, glyph_evictions=evictions["glyph_evictions"], paragraph_evictions=evictions["paragraph_evictions"])
        mixer.command("quit")
        mixer.expect("drained", timeout=20)
        returned = [e for e in mixer.all if e["event"] == "return"][-1]
        code = mixer.process.wait(timeout=20)
        result.update(exit=code, returned=returned["error"])
        evicted = evictions["paragraph_evictions"] > 0 and (budget != "small" or evictions["glyph_evictions"] > 0)
        result["passed"] = code == 0 and returned["error"] == "<nil>" and not over and same and evicted
    except (Failure, OSError, ValueError, KeyError, subprocess.SubprocessError) as err:
        result["failure"] = f"{type(err).__name__}: {err}"
    finally:
        if mixer.process.poll() is None:
            mixer.process.terminate()
            mixer.process.wait(timeout=10)
    return result


def inner(args):
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    home = Path(os.environ["HOME"])
    shell_log = open(home / "shell.log", "w")
    compositor = subprocess.Popen(["gnome-shell", "--wayland", "--headless", "--unsafe-mode", "--virtual-monitor", "3840x2160",
                                   "--wayland-display", "dfx2-audition"], stdout=shell_log, stderr=subprocess.STDOUT)
    summary = dict(gdk_backend=args.gdk_backend, gdk_scale=args.gdk_scale, cases=[])
    status = 1
    mixer = audition = None
    try:
        shell = Shell()
        shell.wait_ready()
        shell.leave_overview()
        shell.devices()
        display = re.search(r"Using public X11 display (:\d+)", (home / "shell.log").read_text())
        env = {k: v for k, v in os.environ.items() if k not in ("DISPLAY", "WAYLAND_DISPLAY")}
        env.update(WAYLAND_DISPLAY="dfx2-audition", GDK_BACKEND=args.gdk_backend, NO_AT_BRIDGE="1", GTK_THEME="Adwaita")
        if display:
            env["DISPLAY"] = display.group(1)
            auth = sorted(Path(os.environ["XDG_RUNTIME_DIR"]).glob(".mutter-Xwaylandauth.*"))
            if auth:
                env["XAUTHORITY"] = str(auth[0])
        if args.gdk_scale:
            env["GDK_SCALE"] = str(args.gdk_scale)
        if args.exec:
            env.pop("GDK_BACKEND", None)  # the audition being run chooses its own
            status = subprocess.run(args.exec, env=env, cwd=args.cwd, timeout=args.timeout).returncode
            summary["exec"] = args.exec
            return status
        mixer = Mixer(args.binary, ["-controls", "-paused", "-snapshots", str(out)], env, out / "mixer.jsonl")
        mixer.expect("opened", match=lambda e: e["window"] == "A", timeout=60)
        mixer.expect("opened", match=lambda e: e["window"] == "B")
        audition = Audition(shell, mixer, args.gdk_scale or 1)
        shell.find("dfx2 mixer A")
        shell.find("dfx2 mixer B")
        time.sleep(1)
        shell.move_window("dfx2 mixer A", 40, 80)
        shell.move_window("dfx2 mixer B", 40 + shell.find("dfx2 mixer A")["w"] + 200, 80)
        time.sleep(0.5)
        audition.settle()
        summary["windows"] = shell.windows()
        summary["first_status"] = mixer.status()
        audition.calibrate("A")
        audition.calibrate("B")
        summary["origins"] = audition.origin
        for name in ("drag_outside_window", "checkbox", "keyboard", "independent_focus", "external_during_drag",
                     "reorder_during_drag", "remove_during_drag", "focus_loss_during_drag", "hover_is_idle",
                     "delayed_commands", "rejected_commands", "overloaded_commands", "external_while_pending", "timing",
                     "close_first_window", "close_during_drag", "shutdown_outstanding"):
            audition.case(name, getattr(audition, name))
        drained = [e for e in mixer.all if e["event"] == "drained"][-1]  # shutdown_outstanding quit
        returned = [e for e in mixer.all if e["event"] == "return"][-1]
        code = mixer.process.wait(timeout=20)
        ownership = ("windows", "tasks", "native_completions", "queued", "input_queued", "native_windows", "native_retired", "dispatch_targets")
        problems = [k for k in ownership if drained[k] != 0]
        stderr = Path(str(mixer.log.name) + ".stderr").read_text()
        summary.update(exit=code, returned=returned["error"], drained=drained, ownership_problems=problems,
                       commands=returned.get("commands"), commands_conserved=returned.get("commands_conserved"), stderr=stderr)
        status = 0 if code == 0 and returned["error"] == "<nil>" and not problems and returned.get("commands_conserved") and not stderr else 1
        if status == 0 and args.text:
            for budget in ("small", "default"):
                result = text_audition(shell, args.binary, out, env, budget, args.gdk_scale or 1)
                summary.setdefault("text", []).append(result)
                if not result["passed"]:
                    status = 1
    except (Failure, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as err:
        summary["failure"] = f"{type(err).__name__}: {err}"
    finally:
        if mixer:
            summary["cases"] = audition.results if audition else []
            summary["edits"] = len(mixer.edits())
            if mixer.process.poll() is None:
                mixer.process.terminate()
                try:
                    mixer.process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    mixer.process.kill()
        compositor.send_signal(signal.SIGTERM)
        try:
            compositor.wait(timeout=10)
        except subprocess.TimeoutExpired:
            compositor.kill()
        shell_log.close()
        shutil.copy(home / "shell.log", out / "shell.log")
    summary["passed"] = status == 0
    if args.exec:
        return status
    (out / "summary.json").write_text(json.dumps(summary, indent=2, default=str) + "\n")
    print(json.dumps(dict(out=str(out), passed=status == 0, failure=summary.get("failure"),
                          cases=[(c["case"], c["ok"]) for c in summary["cases"]])))
    return status


def outer(args):
    root = Path(tempfile.mkdtemp(prefix="dx", dir=args.base))
    try:
        for name in ("home", "run", "cache", "config", "data"):
            (root / name).mkdir(mode=0o700)
        env = {k: v for k, v in os.environ.items() if k not in ("DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS")}
        env.update(HOME=str(root / "home"), XDG_RUNTIME_DIR=str(root / "run"), XDG_CACHE_HOME=str(root / "cache"),
                   XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"),
                   XDG_SESSION_TYPE="wayland", GSETTINGS_BACKEND="memory")
        command = ["dbus-run-session", "--", sys.executable, "-B", __file__, str(Path(args.binary).resolve()), "--out", str(Path(args.out).resolve()),
                   "--gdk-backend", args.gdk_backend, "--inner"]
        if args.gdk_scale:
            command += ["--gdk-scale", str(args.gdk_scale)]
        if not args.text:
            command += ["--no-text"]
        if args.exec:
            command += ["--cwd", os.getcwd(), "--exec", *args.exec]
        Path(args.out).mkdir(parents=True, exist_ok=True)
        with open(Path(args.out) / "session.stderr.log", "w") as stderr:
            result = subprocess.run(command, env=env, stdout=subprocess.PIPE, stderr=stderr, text=True, timeout=args.timeout)
        sys.stdout.write(result.stdout)
        return result.returncode
    finally:
        shutil.rmtree(root, ignore_errors=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("--out", required=True)
    parser.add_argument("--gdk-backend", default="wayland")
    parser.add_argument("--gdk-scale", type=int)
    parser.add_argument("--base", default=None)
    parser.add_argument("--timeout", type=int, default=300)
    parser.add_argument("--inner", action="store_true")
    parser.add_argument("--cwd", default=None)
    parser.add_argument("--no-text", dest="text", action="store_false", help="skip the separate text-cache auditions")
    parser.add_argument("--exec", nargs=argparse.REMAINDER, default=None)
    args = parser.parse_args()
    return inner(args) if args.inner else outer(args)


if __name__ == "__main__":
    sys.exit(main())
