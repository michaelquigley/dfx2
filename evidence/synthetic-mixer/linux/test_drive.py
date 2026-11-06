#!/usr/bin/env python3
"""Regression tests for the large case's checks. No display needed:
python3 evidence/synthetic-mixer/linux/test_drive.py"""
import json
import tempfile
import unittest
from pathlib import Path

import drive

RETAINED = Path(__file__).parent / "stage-2-preflight"


def retained_steps(backend):
    with open(RETAINED / backend / "large.jsonl") as f:
        events = [json.loads(line) for line in f]
    return [e for e in events if e["event"] == "large-size"]


def step(requested, width, height, bytes_=None):
    return dict(requested=requested, first_surface=f"{width}x{height}@1 visible=true",
                bytes=width * height * 4 if bytes_ is None else bytes_, snapshot=f"large-{requested}.png")


def steps_of(*surfaces):
    return [step(f"{i}", w, h) for i, (w, h) in enumerate(surfaces)]


GROWING = steps_of((984, 761), (1240, 900), (1600, 1000), (2400, 1300), (3200, 1400))
CLAMPED = steps_of((984, 761), (1240, 900), (1600, 1000), (1600, 1000), (1600, 1000))


def png_header(width, height):
    return b"\x89PNG\r\n\x1a\n" + b"\0\0\0\x0dIHDR" + width.to_bytes(4, "big") + height.to_bytes(4, "big")


class LargeSizes(unittest.TestCase):
    def test_retained_wayland_run_that_never_grew_is_missing_coverage(self):
        failures = drive.large_size_failures(retained_steps("wayland"))
        self.assertEqual(len(failures), 2, failures)
        self.assertTrue(all("missing coverage" in f for f in failures), failures)

    def test_retained_x11_run_that_grew_has_the_sizes(self):
        self.assertEqual(drive.large_size_failures(retained_steps("x11")), [])

    def test_clamped_growth_past_the_failure_sizes_passes(self):
        self.assertEqual(drive.large_size_failures(CLAMPED), [])

    def test_growth_that_stays_inside_the_failure_sizes_is_missing_coverage(self):
        for top in (2_999_999, 3_000_000):
            steps = steps_of((400, 300), (500, 300), (600, 300), (700, 300), (800, 300))
            steps[-1]["bytes"] = top
            failures = drive.large_size_failures(steps)
            self.assertEqual(len(failures), 1, failures)
            self.assertIn("missing coverage", failures[0])

    def test_one_byte_past_the_failure_sizes_passes(self):
        steps = steps_of((400, 300), (500, 300), (600, 300), (700, 300), (800, 300))
        steps[-1]["bytes"] = 3_000_001
        self.assertEqual(drive.large_size_failures(steps), [])

    def test_shrinking_fails(self):
        steps = steps_of((984, 761), (1600, 1000), (1240, 900), (2400, 1300), (3200, 1400))
        failures = drive.large_size_failures(steps)
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("shrank", failures[0])

    def test_no_net_growth_fails_even_past_the_failure_sizes(self):
        failures = drive.large_size_failures(steps_of(*[(1600, 1000)] * 5))
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("did not grow", failures[0])

    def test_missing_steps_fail(self):
        self.assertIn("4 large image steps", drive.large_size_failures(GROWING[:4])[0])


class LargeSnapshots(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.directory = Path(directory.name)

    def write(self, steps, sizes=None):
        """Writes each step's snapshot, at its surface size unless overridden,
        and returns the snapshot events a run would have emitted."""
        events = []
        for i, e in enumerate(steps):
            width, height = (sizes or {}).get(i, drive.size_of(e["first_surface"]))
            path = self.directory / e["snapshot"]
            path.write_bytes(png_header(width, height))
            events.append(dict(event="snapshot", path=str(path)))
        return events

    def test_snapshots_matching_each_surface_pass(self):
        self.assertEqual(drive.large_failures(GROWING, self.write(GROWING)), [])

    def test_clamped_requests_compare_with_the_surface_not_the_request(self):
        steps = [dict(e, requested="3200x1400") for e in CLAMPED]
        self.assertEqual(drive.large_failures(steps, self.write(steps)), [])

    def test_image_at_the_request_instead_of_a_clamped_surface_fails(self):
        steps = [dict(e, requested="3200x1400") for e in CLAMPED]
        failures = drive.large_snapshot_failures(steps, self.write(steps, {4: (3200, 1400)}))
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("does not match surface", failures[0])

    def test_mismatched_dimensions_fail(self):
        for wrong in ((984, 761), (3200, 1399), (3199, 1400)):
            failures = drive.large_snapshot_failures(GROWING, self.write(GROWING, {4: wrong}))
            self.assertEqual(len(failures), 1, failures)
            self.assertIn("does not match surface", failures[0])

    def test_step_with_no_snapshot_event_fails(self):
        events = self.write(GROWING)
        failures = drive.large_snapshot_failures(GROWING, events[:-1])
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("no snapshot written", failures[0])

    def test_leftover_file_without_an_event_does_not_count(self):
        self.write(GROWING)  # on disk, but this run reported none of them
        self.assertEqual(len(drive.large_snapshot_failures(GROWING, [])), 5)

    def test_step_naming_no_snapshot_fails(self):
        events = self.write(GROWING)
        steps = [dict(e) for e in GROWING]
        del steps[0]["snapshot"]
        failures = drive.large_snapshot_failures(steps, events)
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("no snapshot written", failures[0])

    def test_reported_snapshot_that_is_gone_fails(self):
        events = self.write(GROWING)
        Path(events[2]["path"]).unlink()
        failures = drive.large_snapshot_failures(GROWING, events)
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("unreadable snapshot", failures[0])

    def test_reported_snapshot_that_is_not_a_png_fails(self):
        events = self.write(GROWING)
        Path(events[2]["path"]).write_bytes(b"not an image, but long enough to read a header")
        failures = drive.large_snapshot_failures(GROWING, events)
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("unreadable snapshot", failures[0])

    def test_retained_x11_run_took_no_snapshots_so_it_cannot_pass_now(self):
        self.assertEqual(len(drive.large_failures(retained_steps("x11"), [])), 5)

    def test_the_case_fails_on_a_mismatched_snapshot_when_the_sizes_are_good(self):
        failures = drive.large_failures(GROWING, self.write(GROWING, {4: (984, 761)}))
        self.assertEqual(len(failures), 1, failures)
        self.assertIn("does not match surface", failures[0])


class LargeCase(unittest.TestCase):
    """The case itself, with the process replaced by recorded events."""

    def audition(self, steps, snapshots):
        events = [dict(e, event="large-size") for e in steps] + snapshots
        original = drive.run
        drive.run = lambda *args: (events, 0, False, True, [], [], False)
        self.addCleanup(setattr, drive, "run", original)
        return drive.audition("mixer", None, "large", [], {}, 1)["failures"]

    def test_missing_snapshots_fail_the_case(self):
        failures = self.audition(GROWING, [])
        self.assertEqual(sum("no snapshot written" in f for f in failures), 5, failures)

    def test_a_run_that_never_grew_fails_the_case(self):
        failures = self.audition(retained_steps("wayland"), [])
        self.assertEqual(sum("missing coverage" in f for f in failures), 2, failures)


if __name__ == "__main__":
    unittest.main()
