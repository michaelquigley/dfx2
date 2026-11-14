#!/usr/bin/env python3
"""Run webview integration stage 2's Linux native matrix and retain a manifest:
the integrated browser matrix (webview_drive.py) under Wayland, XWayland and
XWayland at GDK_SCALE=2, each in its own isolated headless GNOME Shell, then
the synthetic mixer's stage 4 matrix as the foundation/UI regression, because
the GTK backend changed.

    python3 evidence/webview-integration/linux/stage2_matrix.py --out DIR [--base SHORT_TMP] [--skip-regression]
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
HERE = ROOT / "evidence/webview-integration/linux"
NESTED = ROOT / "evidence/synthetic-mixer/linux/controls_drive.py"
PACKAGES = ("libwebkit2gtk-4.1-0", "libjavascriptcoregtk-4.1-0", "libgtk-3-0t64", "libsoup-3.0-0", "libglib2.0-0t64", "bubblewrap", "gnome-shell")


def now():
    return datetime.datetime.now().astimezone().isoformat(timespec="seconds")


def versions():
    out = {}
    for p in PACKAGES:
        r = subprocess.run(["dpkg-query", "-W", "-f=${Version}", p], capture_output=True, text=True)
        out[p] = r.stdout.strip() or "absent"
    return out


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    parser.add_argument("--base", default="/tmp")
    parser.add_argument("--skip-regression", action="store_true")
    args = parser.parse_args()
    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0")
    binary = Path(args.base) / "dfx2-s2-webview"
    subprocess.run(["go", "build", "-o", str(binary), "./examples/webview"], cwd=ROOT, env=env, check=True)
    git = lambda *a: subprocess.run(["git", *a], cwd=ROOT, capture_output=True, text=True, check=True).stdout
    manifest = dict(revision=git("rev-parse", "HEAD").strip(), working_tree=git("status", "--short"),
                    webview_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(), kernel=os.uname().release,
                    go=subprocess.run(["go", "version"], capture_output=True, text=True).stdout.strip(),
                    packages=versions(), started=now(), runs=[])
    for name, backend, scale in (("wayland", "wayland", None), ("x11", "x11", None), ("x11-scale2", "x11", 2)):
        command = [sys.executable, "-B", str(NESTED), str(binary), "--out", str(out / name / "session"), "--base", args.base,
                   "--timeout", "1500"]
        if scale:
            command += ["--gdk-scale", str(scale)]
        command += ["--exec", sys.executable, "-B", str(HERE / "webview_drive.py"), str(binary), "--out", str(out / name),
                    "--gdk-backend", backend]
        started = now()
        with open(out / f"{name}.log", "w") as log:
            code = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT).returncode
        summary = json.loads((out / name / "summary.json").read_text()) if (out / name / "summary.json").exists() else {}
        manifest["runs"].append(dict(name=name, started=started, finished=now(), exit_code=code,
                                     cases=[(c["case"], c["ok"]) for c in summary.get("cases", [])]))
        print(json.dumps(manifest["runs"][-1]), flush=True)
    if not args.skip_regression:
        started = now()
        code = subprocess.run([sys.executable, "-B", str(ROOT / "evidence/synthetic-mixer/linux/stage4_matrix.py"),
                               "--out", str(out / "regression"), "--base", args.base], cwd=ROOT).returncode
        manifest["runs"].append(dict(name="regression", started=started, finished=now(), exit_code=code))
        print(json.dumps(manifest["runs"][-1]), flush=True)
    manifest["finished"] = now()
    (out / "manifest.json").write_text(json.dumps(manifest, indent=1) + "\n")
    return 0 if all(r["exit_code"] == 0 for r in manifest["runs"]) else 1


if __name__ == "__main__":
    sys.exit(main())
