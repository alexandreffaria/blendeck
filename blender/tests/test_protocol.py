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

    def test_bridge_host(self):
        self.assertEqual(protocol.bridge_host(" 127.0.0.1 "), "127.0.0.1")
        self.assertEqual(protocol.bridge_host("LOCALHOST"), "127.0.0.1")
        self.assertEqual(protocol.bridge_host("::1"), "::1")
        self.assertIsNone(protocol.bridge_host("example.com"))
        self.assertIsNone(protocol.bridge_host(""))

    def test_encode(self):
        self.assertEqual(protocol.encode({"type": "hello"}), b'{"type":"hello"}\n')


class ZoomTest(unittest.TestCase):
    def test_total(self):
        msgs = [
            {"type": "zoom", "clicks": 3},
            {"type": "zoom", "clicks": -1},
            {"type": "zoom", "clicks": "9"},
            {"type": "zoom", "clicks": True},
            {"type": "zoom", "clicks": 1.5},
            {"type": "zoom"},
            {"type": "scrub", "frames": 10, "clicks": 100},
        ]
        self.assertEqual(protocol.total_zoom(msgs), 2)

    def test_scrub_ignores_zoom(self):
        self.assertEqual(protocol.total_scrub([{"type": "zoom", "clicks": 5, "frames": 5}]), 0)

    def test_total_is_capped(self):
        cap = protocol.MAX_ZOOM_PER_TICK
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": 10**9}]), cap)
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": -(10**9)}]), -cap)
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": 20}] * 5), cap)

    def test_in_and_out_cancel(self):
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": 4}, {"type": "zoom", "clicks": -4}]), 0)
        self.assertEqual(protocol.total_zoom([]), 0)

    def test_plan(self):
        self.assertEqual(protocol.zoom_plan(3), ("in", 3))
        self.assertEqual(protocol.zoom_plan(-2), ("out", 2))
        self.assertIsNone(protocol.zoom_plan(0))


class ZoomAnchorTest(unittest.TestCase):
    def test_on_screen_stays_put(self):
        self.assertEqual(protocol.zoom_anchor(0, 800), (0, 0))
        self.assertEqual(protocol.zoom_anchor(150, 800), (0, 150))
        self.assertEqual(protocol.zoom_anchor(800, 800), (0, 800))

    def test_off_screen_centers_first(self):
        # Right of the view: pan right by the distance past the middle
        self.assertEqual(protocol.zoom_anchor(1300, 800), (900, 400))
        # Left of the view: pan left
        self.assertEqual(protocol.zoom_anchor(-50, 800), (-450, 400))
        self.assertEqual(protocol.zoom_anchor(-1, 801), (-401, 400))

    def test_correction(self):
        self.assertEqual(protocol.zoom_correction(170, 150), 20)
        self.assertEqual(protocol.zoom_correction(130, 150), -20)
        self.assertEqual(protocol.zoom_correction(150, 150), 0)
        self.assertEqual(protocol.zoom_correction(150.6, 150), 1)


class ManifestTest(unittest.TestCase):
    def test_manifest_matches_addon(self):
        import re
        import tomllib

        here = os.path.join(os.path.dirname(__file__), "..", "blendeck")
        with open(os.path.join(here, "blender_manifest.toml"), "rb") as f:
            manifest = tomllib.load(f)
        for key in ("schema_version", "id", "version", "name", "tagline", "maintainer", "type", "blender_version_min", "license"):
            self.assertIn(key, manifest)
        with open(os.path.join(here, "__init__.py")) as f:
            m = re.search(r'ADDON_VERSION = "([^"]+)"', f.read())
        self.assertEqual(m.group(1), manifest["version"], "ADDON_VERSION and the manifest disagree")


if __name__ == "__main__":
    unittest.main()
