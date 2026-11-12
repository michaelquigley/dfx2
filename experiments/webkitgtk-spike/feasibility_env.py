"""Environment record shared by the integration feasibility drivers: host,
runtime versions, resolved library paths, source revision with any dirty
diff, and the probe binary's hash. No credentials or profile contents."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess


def _out(*command):
    try:
        return subprocess.run(command, capture_output=True, text=True, timeout=20).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return "unavailable"


def record(binary, output):
    root = Path(__file__).resolve().parents[2]
    libs = {}
    for name in ("libwebkit2gtk-4.1.so.0", "libjavascriptcoregtk-4.1.so.0", "libgtk-3.so.0", "libsoup-3.0.so.0", "libglib-2.0.so.0"):
        path = Path("/usr/lib/x86_64-linux-gnu") / name
        libs[name] = str(path.resolve()) if path.exists() else "missing"
    env = {
        "os": _out("sh", "-c", ". /etc/os-release; echo $PRETTY_NAME"), "kernel": platform.release(), "go": _out("go", "version"),
        "session": os.getenv("XDG_SESSION_TYPE", "unknown"), "gdk_backend": os.getenv("GDK_BACKEND", "default"),
        "pkg_config": dict(zip(("webkit2gtk-4.1", "javascriptcoregtk-4.1", "gtk+-3.0", "libsoup-3.0", "glib-2.0"),
                               _out("pkg-config", "--modversion", "webkit2gtk-4.1", "javascriptcoregtk-4.1", "gtk+-3.0", "libsoup-3.0", "glib-2.0").split())),
        "packages": _out("dpkg-query", "-W", "-f", "${Package} ${Version}\n", "libwebkit2gtk-4.1-0", "libjavascriptcoregtk-4.1-0",
                         "libgtk-3-0t64", "libsoup-3.0-0", "libglib2.0-0t64").splitlines(),
        "library_paths": libs,
        "revision": _out("git", "-C", str(root), "rev-parse", "HEAD"),
        "dirty": _out("git", "-C", str(root), "status", "--short", "--", "experiments", "docs/future/webview-feasibility-spike.md").splitlines(),
        "binary": str(binary), "binary_sha256": hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
    }
    (Path(output) / "environment.json").write_text(json.dumps(env, indent=1) + "\n")
    diff = _out("git", "-C", str(root), "diff", "--", "experiments")
    (Path(output) / "source.diff").write_text(diff + "\n")
    return env
