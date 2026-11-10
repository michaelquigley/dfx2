#!/usr/bin/env python3
"""Run the stage 4 Linux native matrix inside isolated headless GNOME Shells and
retain a manifest: the controls and command audition with its text-cache runs,
stage 1's regression matrix and the foundation lifetime matrix, each under
Wayland, XWayland and (except foundation) XWayland at GDK_SCALE=2. Nothing
reaches the operator's desktop.

    python3 evidence/synthetic-mixer/linux/stage4_matrix.py --out DIR [--base SHORT_TMP]

Requires Python 3 with Pillow, gnome-shell, dbus-run-session and gdbus.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import zlib

ROOT = Path(__file__).resolve().parents[3]
LINUX = ROOT / "evidence/synthetic-mixer/linux"


def now():
    return datetime.datetime.now().astimezone().isoformat(timespec="seconds")


def sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


ZERO = ("windows", "tasks", "queued", "native_completions", "input_queued", "native_windows", "native_retired",
        "dispatch_targets", "paint_failures", "release_pending")


def png_ok(path):
    """Chunk CRCs, a complete IEND and a full pixel decode, as drive.py checks."""
    from PIL import Image
    data = path.read_bytes()
    if not data.startswith(b"\x89PNG\r\n\x1a\n"):
        return False
    at = 8
    while at + 12 <= len(data):
        length = int.from_bytes(data[at:at + 4], "big")
        kind, body = data[at + 4:at + 8], data[at + 8:at + 8 + length]
        crc = int.from_bytes(data[at + 8 + length:at + 12 + length], "big")
        if len(body) != length or zlib.crc32(kind + body) != crc:
            return False
        at += 12 + length
        if kind == b"IEND":
            break
    else:
        return False
    with Image.open(path) as image:
        image.load()
    return True


def audit(out):
    """Read every retained process log, not the summaries: each application
    process drains exactly once with zero ownership, writes nothing to stderr,
    and every PNG decodes."""
    result = dict(process_logs=0, problems=[], pngs=0, bad_pngs=[])
    for log in sorted(out.rglob("*.jsonl")):
        events = [json.loads(line) for line in log.read_text().splitlines() if line.startswith("{")]
        drained = [e for e in events if e.get("event") == "drained"]
        if not drained and not any(e.get("event") == "return" for e in events):
            continue  # not an application log
        result["process_logs"] += 1
        if len(drained) != 1:
            result["problems"].append(f"{log}: {len(drained)} drained records")
            continue
        nonzero = [k for k in ZERO if drained[0].get(k) not in (0, False, None)]
        if nonzero or drained[0].get("native_problem") not in ("", None, "<nil>"):
            result["problems"].append(f"{log}: drained with {nonzero} {drained[0].get('native_problem')}")
        for stderr in (log.with_suffix(".stderr.log"), Path(str(log) + ".stderr"), log.parent / "mixer.stderr.log"):
            if stderr.exists() and stderr.read_text().strip():
                result["problems"].append(f"{stderr}: not empty")
    for png in sorted(out.rglob("*.png")):
        result["pngs"] += 1
        try:
            if not png_ok(png):
                result["bad_pngs"].append(str(png))
        except Exception as err:  # noqa: BLE001 - any decode failure is a bad image
            result["bad_pngs"].append(f"{png}: {err}")
    result["clean"] = not result["problems"] and not result["bad_pngs"]
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    parser.add_argument("--base", default="/tmp")
    parser.add_argument("--only", nargs="*", help="run only these named runs")
    parser.add_argument("--audit-only", action="store_true", help="audit an existing run directory's raw logs")
    args = parser.parse_args()
    out = Path(args.out).resolve()
    if args.audit_only:
        result = audit(out)
        (out / "audit.json").write_text(json.dumps(result, indent=1) + "\n")
        print(json.dumps(dict(process_logs=result["process_logs"], pngs=result["pngs"], clean=result["clean"])))
        return 0 if result["clean"] else 1
    out.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0")
    mixer, lifetime = Path(args.base) / "dfx2-s4-mixer", Path(args.base) / "dfx2-s4-lifetime"
    for binary, package in ((mixer, "./examples/mixer"), (lifetime, "./examples/lifetime")):
        subprocess.run(["go", "build", "-o", str(binary), package], cwd=ROOT, env=env, check=True)
    git = lambda *a: subprocess.run(["git", *a], cwd=ROOT, capture_output=True, text=True, check=True).stdout
    manifest = dict(revision=git("rev-parse", "HEAD").strip(), working_tree=git("status", "--short"),
                    mixer_sha256=sha256(mixer), lifetime_sha256=sha256(lifetime), started=now(), runs=[])
    drive = [sys.executable, "-B", str(LINUX / "controls_drive.py")]
    nest = lambda name, backend, scale, inner: [*drive, str(mixer), "--out", str(out / f"nest-{name}"), "--base", args.base,
                                                "--gdk-backend", backend, *(["--gdk-scale", str(scale)] if scale else []),
                                                "--timeout", "900", "--exec", *inner]
    runs = []
    for name, backend, scale in (("wayland", "wayland", None), ("x11", "x11", None), ("x11-scale2", "x11", 2)):
        runs.append((f"controls-{name}", [*drive, str(mixer), "--out", str(out / f"controls-{name}"), "--base", args.base,
                                          "--gdk-backend", backend, *(["--gdk-scale", str(scale)] if scale else []), "--timeout", "900"]))
        runs.append((f"regression-{name}", nest(f"regression-{name}", backend, scale,
                                                [sys.executable, "-B", str(LINUX / "drive.py"), str(mixer), "--out",
                                                 str(out / f"regression-{name}"), "--gdk-backend", backend,
                                                 *(["--gdk-scale", str(scale)] if scale else [])])))
    for name, backend in (("wayland", "wayland"), ("x11", "x11")):
        runs.append((f"foundation-{name}", nest(f"foundation-{name}", backend, None,
                                                [sys.executable, "-B", str(ROOT / "evidence/foundation/linux/drive.py"), str(lifetime),
                                                 "--out", str(out / f"foundation-{name}"), "--gdk-backend", backend])))
    failed = 0
    for name, command in runs:
        if args.only and name not in args.only:
            continue
        started = now()
        with open(out / f"{name}.log", "w") as log:
            code = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, timeout=1200).returncode
        manifest["runs"].append(dict(name=name, command=command, started=started, finished=now(), exit_code=code))
        failed += code != 0
        print(json.dumps(dict(run=name, exit_code=code)), flush=True)
    manifest["finished"] = now()
    (out / "manifest.json").write_text(json.dumps(manifest, indent=1) + "\n")
    result = audit(out)
    (out / "audit.json").write_text(json.dumps(result, indent=1) + "\n")
    print(json.dumps(dict(process_logs=result["process_logs"], pngs=result["pngs"], clean=result["clean"])), flush=True)
    return 1 if failed or not result["clean"] else 0


if __name__ == "__main__":
    sys.exit(main())
