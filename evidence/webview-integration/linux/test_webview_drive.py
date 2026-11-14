"""Unit tests for webview_drive.py's acceptance of reaped descendants.

    python3 -B -m unittest -v evidence/webview-integration/linux/test_webview_drive.py
"""
import signal
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from webview_drive import child_problem  # noqa: E402


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
