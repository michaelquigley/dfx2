"""Unit tests for the Linux webview audition driver.

    python3 -B -m unittest -v evidence/webview-integration/linux/test_webview_drive.py
"""
import io
import queue
import signal
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).parent))
from webview_drive import Failure, Host, child_problem  # noqa: E402


class HostOutput(unittest.TestCase):
    def exited_host(self, stdout):
        h = Host.__new__(Host)
        h.name, h.log, h.events, h.queue = "probe", io.StringIO(), [], queue.Queue()
        h.process = SimpleNamespace(stdout=stdout, wait=lambda timeout: 0)
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        h.stderr_path = Path(directory.name) / "stderr"
        h.stderr_path.touch()
        h.read_error = None
        h.reader = threading.Thread(target=h._read)
        h.reader.start()
        self.addCleanup(h.reader.join, 5)
        return h

    def test_return_is_read_before_the_log_closes(self):
        held, release = threading.Event(), threading.Event()

        def stdout():
            held.set()
            if not release.wait(5):
                raise AssertionError("test did not release stdout")
            yield '{"event":"return","error":"<nil>"}\n'

        h = self.exited_host(stdout())
        self.addCleanup(release.set)
        self.assertTrue(held.wait(5))
        with self.assertRaisesRegex(Failure, "stdout"):
            h.finish(timeout=0)
        self.assertFalse(h.log.closed)
        release.set()
        self.assertEqual(h.finish(timeout=5), (0, "<nil>", ""))
        self.assertEqual(h.events, [{"event": "return", "error": "<nil>"}])
        self.assertTrue(h.log.closed)

    def test_read_error_after_return_fails_the_run(self):
        def stdout():
            yield '{"event":"return","error":"<nil>"}\n'
            raise OSError("pipe read failed")

        h = self.exited_host(stdout())
        with self.assertRaisesRegex(Failure, "pipe read failed"):
            h.finish()
        self.assertTrue(h.log.closed)


class ChildAcceptance(unittest.TestCase):
    def test_clean_exit_is_accepted(self):
        self.assertIsNone(child_problem(dict(pid=10, name="WebKitWebProces", exit=0)))

    def test_nonzero_exit_is_rejected(self):
        self.assertIsNotNone(child_problem(dict(pid=11, name="WebKitNetworkPr", exit=1)))
        self.assertIsNotNone(child_problem(dict(pid=12, name="?", exit=3)))

    def test_unexpected_signal_is_rejected(self):
        self.assertIsNotNone(child_problem(dict(pid=13, name="WebKitWebProces", signal=signal.SIGSEGV)))
        self.assertIsNotNone(child_problem(dict(pid=14, name="bwrap", signal=signal.SIGTERM)))

    def test_named_bwrap_sigkill_is_the_only_exception(self):
        self.assertIsNone(child_problem(dict(pid=15, name="bwrap", signal=signal.SIGKILL)))
        # An unnamed child, even one that may have been bwrap, does not inherit it.
        self.assertIsNotNone(child_problem(dict(pid=16, name="?", signal=signal.SIGKILL)))

    def test_deliberately_killed_process_is_excused_only_for_its_signal(self):
        self.assertIsNone(child_problem(dict(pid=17, name="WebKitWebProces", signal=signal.SIGKILL), killed={17}))
        self.assertIsNotNone(child_problem(dict(pid=17, name="WebKitWebProces", signal=signal.SIGSEGV), killed={17}))
        self.assertIsNotNone(child_problem(dict(pid=18, name="WebKitWebProces", signal=signal.SIGKILL), killed={17}))
        self.assertIsNotNone(child_problem(dict(pid=17, name="WebKitWebProces", exit=2), killed={17}))


if __name__ == "__main__":
    unittest.main()
