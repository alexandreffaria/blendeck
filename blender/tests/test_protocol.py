import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "blendeck"))

import protocol  # noqa: E402


class LineBufferTest(unittest.TestCase):
    def test_split_across_reads(self):
        b = protocol.LineBuffer()
        self.assertEqual(b.feed(b'{"type":"scr'), [])
        self.assertEqual(b.feed(b'ub","frames":10}\n{"type":"scrub",'), [{"type": "scrub", "frames": 10}])
        self.assertEqual(b.feed(b'"frames":-1}\n'), [{"type": "scrub", "frames": -1}])

    def test_skips_junk(self):
        b = protocol.LineBuffer()
        self.assertEqual(b.feed(b'nope\n\n[1,2]\n{"type":"x"}\n'), [{"type": "x"}])

    def test_long_line(self):
        b = protocol.LineBuffer()
        with self.assertRaises(ValueError):
            b.feed(b"a" * (protocol.MAX_LINE + 1))


class ScrubTest(unittest.TestCase):
    def test_total(self):
        msgs = [
            {"type": "scrub", "frames": 10},
            {"type": "scrub", "frames": -3},
            {"type": "scrub", "frames": "9"},
            {"type": "scrub", "frames": True},
            {"type": "other", "frames": 100},
            {"type": "scrub"},
        ]
        self.assertEqual(protocol.total_scrub(msgs), 7)

    def test_target_unclamped(self):
        self.assertEqual(protocol.scrub_target(5, -10, 1, 250, False), -5)

    def test_target_clamped(self):
        self.assertEqual(protocol.scrub_target(5, -10, 1, 250, True), 1)
        self.assertEqual(protocol.scrub_target(245, 10, 1, 250, True), 250)
        self.assertEqual(protocol.scrub_target(100, 10, 1, 250, True), 110)

    def test_outside_range_is_not_pulled_back(self):
        # Playhead at 300 with the scene ending at 250: moving left works,
        # moving right stays put instead of jumping to 250.
        self.assertEqual(protocol.scrub_target(300, 10, 1, 250, True), 300)
        self.assertEqual(protocol.scrub_target(300, -10, 1, 250, True), 290)
        self.assertEqual(protocol.scrub_target(-5, -1, 1, 250, True), -5)

    def test_encode(self):
        self.assertEqual(protocol.encode({"type": "hello"}), b'{"type":"hello"}\n')


if __name__ == "__main__":
    unittest.main()
