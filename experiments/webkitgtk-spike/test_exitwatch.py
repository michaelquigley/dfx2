import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class ExitWatchTest(unittest.TestCase):
    def run_case(self, child_code):
        # The host exits 0 immediately; its orphan child survives long enough
        # to prove the observer waits beyond the original host-only gate.
        source = "import os,time,sys\nif os.fork(): os._exit(0)\ntime.sleep(.2)\n" + child_code
        with tempfile.TemporaryDirectory() as tmp:
            prefix = Path(tmp) / "case"
            result = subprocess.run([sys.executable, str(Path(__file__).with_name("exitwatch.py")),
                                     "--output", str(prefix), "--", sys.executable, "-c", source],
                                    capture_output=True, text=True, timeout=10)
            return result.returncode, json.loads(prefix.with_suffix(".json").read_text())

    def test_successful_orphan_drains(self):
        code, result = self.run_case("sys.exit(0)")
        self.assertEqual(code, 0)
        self.assertEqual(result["host_exit"], 0)
        self.assertEqual([p["exit"] for p in result["adopted_exits"]], [0])

    def test_nonzero_orphan_fails_after_successful_host(self):
        code, result = self.run_case("sys.exit(7)")
        self.assertEqual(code, 1)
        self.assertEqual(result["host_exit"], 0)
        self.assertTrue(result["child_failure"])

    def test_late_allocator_diagnostic_fails(self):
        code, result = self.run_case("print('free(): corrupted unsorted chunks', flush=True)")
        self.assertEqual(code, 1)
        self.assertFalse(result["child_failure"])
        self.assertTrue(result["fatal_log"])

    def test_killed_non_helper_fails(self):
        code, result = self.run_case("import signal\nos.kill(os.getpid(), signal.SIGKILL)")
        self.assertEqual(code, 1)
        self.assertTrue(result["child_failure"])


if __name__ == "__main__":
    unittest.main()
