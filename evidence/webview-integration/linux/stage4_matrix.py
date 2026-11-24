#!/usr/bin/env python3
"""Run webview integration stage 4's Linux native matrix and retain a manifest:
the mixed-view audition (mixed_drive.py) under Wayland, XWayland and XWayland
at GDK_SCALE=2, each in its own isolated headless GNOME Shell, then stage 2's
integrated browser matrix with the synthetic mixer's stage 4 regression
(stage2_matrix.py), because the mixer example changed.

    python3 evidence/webview-integration/linux/stage4_matrix.py --out DIR [--base SHORT_TMP] [--mixed-only]
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
sys.dont_write_bytecode = True  # importing stage2_matrix must leave no __pycache__ in the tree
sys.path.insert(0, str(HERE))
from stage2_matrix import now, versions  # noqa: E402


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    parser.add_argument("--base", default="/tmp")
    parser.add_argument("--mixed-only", action="store_true", help="skip the browser matrix and the mixer regression")
    args = parser.parse_args()
    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0")
    binary = Path(args.base) / "dfx2-s4-mixer"
    subprocess.run(["go", "build", "-o", str(binary), "./examples/mixer"], cwd=ROOT, env=env, check=True)
    git = lambda *a: subprocess.run(["git", *a], cwd=ROOT, capture_output=True, text=True, check=True).stdout
    manifest = dict(revision=git("rev-parse", "HEAD").strip(), working_tree=git("status", "--short"),
                    mixer_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(), kernel=os.uname().release,
                    go=subprocess.run(["go", "version"], capture_output=True, text=True).stdout.strip(),
                    packages=versions(), started=now(), runs=[])
    for name, backend, scale in (("wayland", "wayland", None), ("x11", "x11", None), ("x11-scale2", "x11", 2)):
        command = [sys.executable, "-B", str(NESTED), str(binary), "--out", str(out / "mixed" / name / "session"), "--base", args.base,
                   "--timeout", "1500"]
        if scale:
            command += ["--gdk-scale", str(scale)]
        command += ["--exec", sys.executable, "-B", str(HERE / "mixed_drive.py"), str(binary), "--out", str(out / "mixed" / name),
                    "--gdk-backend", backend]
        started = now()
        (out / "mixed").mkdir(exist_ok=True)
        with open(out / "mixed" / f"{name}.log", "w") as log:
            code = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT).returncode
        summary_path = out / "mixed" / name / "summary.json"
        summary = json.loads(summary_path.read_text()) if summary_path.exists() else {}
        manifest["runs"].append(dict(name=f"mixed-{name}", started=started, finished=now(), exit_code=code,
                                     cases=[(c["case"], c["ok"]) for c in summary.get("cases", [])]))
        print(json.dumps(manifest["runs"][-1]), flush=True)
    if not args.mixed_only:
        started = now()
        code = subprocess.run([sys.executable, "-B", str(HERE / "stage2_matrix.py"), "--out", str(out / "integrated"), "--base", args.base],
                              cwd=ROOT).returncode
        manifest["runs"].append(dict(name="integrated", started=started, finished=now(), exit_code=code))
        print(json.dumps(manifest["runs"][-1]), flush=True)
    manifest["finished"] = now()
    (out / "manifest.json").write_text(json.dumps(manifest, indent=1) + "\n")
    return 0 if all(r["exit_code"] == 0 for r in manifest["runs"]) else 1


if __name__ == "__main__":
    sys.exit(main())
