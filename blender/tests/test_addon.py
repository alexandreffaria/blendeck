"""Runs the add-on's connection code against a fake bpy and real loopback
sockets, so the parts that talk to the bridge are tested without Blender."""

import contextlib
import json
import os
import socket
import sys
import time
import types
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


class _Prefs:
    enabled = True
    host = "127.0.0.1"
    port = 0
    clamp = True


class _Render:
    fps = 24
    fps_base = 1.0


class _Scene:
    name = "Edit"
    frame_current = 10
    frame_start = 1
    frame_end = 100
    render = _Render()
    sequence_editor = None

    def frame_set(self, f):
        self.frame_current = f


class _Zoom:
    """Fake view2d.zoom_in/zoom_out: records (direction, region name) for
    each call, using the region the add-on put in temp_override."""

    def __init__(self):
        self.calls = []
        self.override = None
        self.fail_in = set()  # region names whose zoom raises like a failed poll

    @contextlib.contextmanager
    def temp_override(self, **kw):
        self.override = kw
        try:
            yield
        finally:
            self.override = None

    def op(self, direction):
        def call():
            region = self.override["region"]
            if region.name in self.fail_in:
                raise RuntimeError("Operator bpy.ops.view2d.zoom_in.poll() failed, context is incorrect")
            self.calls.append((direction, region.name))
            region.view2d.zoom(direction)
            return {"FINISHED"}

        return call

    def pan(self, deltax=0, deltay=0):
        region = self.override["region"]
        self.calls.append(("pan", region.name))
        region.view2d.pan(deltax)
        return {"FINISHED"}


class _View2D:
    """Time axis of a strips region, as Blender 5.2 behaves: zoom_in takes
    3.75% off each side around the middle (zoom_out undoes it), and pan moves
    the view by deltax region pixels."""

    def __init__(self, width, xmin=0.0, xmax=250.0):
        self.width, self.xmin, self.xmax = width, xmin, xmax

    def zoom(self, direction):
        size = self.xmax - self.xmin
        new = size * 0.925 if direction == "in" else size / 0.925
        mid = (self.xmin + self.xmax) / 2
        self.xmin, self.xmax = mid - new / 2, mid + new / 2

    def pan(self, deltax):
        dx = deltax * (self.xmax - self.xmin) / self.width
        self.xmin, self.xmax = self.xmin + dx, self.xmax + dx

    def region_to_view(self, x, y):
        return self.xmin + x * (self.xmax - self.xmin) / self.width, 0.0

    def view_to_region(self, x, y, clip=True):
        return int((x - self.xmin) * self.width / (self.xmax - self.xmin)), 0


ZOOM = _Zoom()


class _Strip:
    """A strip as Blender 5.2's data API has it: split(frame, method) keeps
    the left part and adds the right part to the same level, copying lock
    and selection like the real Strip.split. Every split goes into log."""

    def __init__(self, name, channel, start, end, log, level, lock=False, select=False, fail=False):
        self.name, self.channel = name, channel
        self.frame_final_start, self.frame_final_end = start, end
        self.lock, self.select, self.fail = lock, select, fail
        self.log, self.level = log, level

    def split(self, frame, method):
        if self.fail:
            raise RuntimeError("Strip can't be split")
        self.log.append((self.name, frame, method))
        right = _Strip(self.name + ".001", self.channel, frame, self.frame_final_end, self.log, self.level, self.lock, self.select)
        self.frame_final_end = frame
        self.level.append(right)
        return right


class _Channel:
    def __init__(self, lock=False):
        self.lock = lock


class _Owner:
    """The editor or a meta strip: its strips and its channels (0..128)."""

    def __init__(self, log):
        self.log = log
        self.strips = []
        self.channels = [_Channel() for _ in range(129)]

    def add(self, name, channel, start, end, **kw):
        s = _Strip(name, channel, start, end, self.log, self.strips, **kw)
        self.strips.append(s)
        return s

    def spans(self):
        return sorted((s.name, s.frame_final_start, s.frame_final_end) for s in self.strips)


class _Editor(_Owner):
    def __init__(self):
        super().__init__([])
        self.meta_stack = []


UNDO = []  # messages of the undo steps pushed


def zooms():
    """The zoom calls made so far, without the pans that anchor them."""
    return [c for c in ZOOM.calls if c[0] != "pan"]


def _region(name, type="WINDOW", width=800, height=300):
    return types.SimpleNamespace(name=name, type=type, width=width, height=height, view2d=_View2D(max(width, 1)))


def _area(type, view_type, regions):
    return types.SimpleNamespace(
        type=type,
        spaces=types.SimpleNamespace(active=types.SimpleNamespace(view_type=view_type)),
        regions=regions,
        tag_redraw=lambda: None,
    )


def _install_fake_bpy():
    bpy = types.ModuleType("bpy")
    prefs = _Prefs()
    scene = _Scene()
    bpy.context = types.SimpleNamespace(
        preferences=types.SimpleNamespace(addons={"blendeck": types.SimpleNamespace(preferences=prefs)}),
        scene=scene,
        sequencer_scene=None,
        window_manager=types.SimpleNamespace(windows=[]),
        temp_override=ZOOM.temp_override,
    )
    bpy.ops = types.SimpleNamespace(
        view2d=types.SimpleNamespace(zoom_in=ZOOM.op("in"), zoom_out=ZOOM.op("out"), pan=ZOOM.pan),
        ed=types.SimpleNamespace(undo_push=lambda message="": UNDO.append(message) or {"FINISHED"}),
    )
    bpy.app = types.SimpleNamespace(version_string="5.2.2")
    bpy.types = types.SimpleNamespace(AddonPreferences=object, Panel=object)
    bpy.props = types.SimpleNamespace(BoolProperty=lambda **k: None, StringProperty=lambda **k: None, IntProperty=lambda **k: None)
    sys.modules["bpy"] = bpy
    return prefs, scene


PREFS, SCENE = _install_fake_bpy()
import blendeck as addon  # noqa: E402


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class Bridge:
    """A stand-in for the bridge: accepts connections and reads lines."""

    def __init__(self):
        self.srv = socket.socket()
        self.srv.bind(("127.0.0.1", 0))
        self.srv.listen(4)
        self.srv.setblocking(False)
        self.port = self.srv.getsockname()[1]

    def accept(self, ticks=200):
        for _ in range(ticks):
            addon.tick()
            try:
                conn, _ = self.srv.accept()
                conn.settimeout(0.3)
                return conn
            except BlockingIOError:
                time.sleep(0.005)
        raise AssertionError("add-on never connected")

    def close(self):
        self.srv.close()


def tick_until(cond, ticks=200):
    for _ in range(ticks):
        addon.tick()
        if cond():
            return True
        time.sleep(0.005)
    return False


def read_lines(conn):
    data = b""
    try:
        while True:
            d = conn.recv(65536)
            if not d:
                break
            data += d
    except socket.timeout:
        pass
    return [json.loads(line) for line in data.decode().splitlines() if line.strip()]


class AddonTest(unittest.TestCase):
    def setUp(self):
        addon._close("reset")
        PREFS.enabled = True
        PREFS.host = "127.0.0.1"
        SCENE.frame_current = 10

    def tearDown(self):
        addon.bpy.context.window_manager.windows = []
        addon._close("reset")
        ZOOM.calls.clear()
        ZOOM.fail_in.clear()
        SCENE.sequence_editor = None
        UNDO.clear()

    def _screen(self):
        """One window with: a strips timeline, a strips+preview editor, a
        preview-only editor, a 3D view, and an editor whose strips region
        is collapsed to nothing."""
        areas = [
            _area("SEQUENCE_EDITOR", "SEQUENCER", [_region("header", "HEADER"), _region("strips")]),
            _area("SEQUENCE_EDITOR", "SEQUENCER_PREVIEW", [_region("preview", "PREVIEW"), _region("both")]),
            _area("SEQUENCE_EDITOR", "PREVIEW", [_region("preview-only", "PREVIEW"), _region("hidden-a", width=1, height=1)]),
            _area("VIEW_3D", "", [_region("3d")]),
            _area("SEQUENCE_EDITOR", "SEQUENCER", [_region("collapsed", height=0)]),
        ]
        win = types.SimpleNamespace(screen=types.SimpleNamespace(areas=areas), scene=None)
        addon.bpy.context.window_manager.windows = [win]

    def test_zoom_every_timeline(self):
        self._screen()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        def net(region):
            return zooms().count(("in", region)) - zooms().count(("out", region))

        # In and out in one read cancel down to 2 in (a split read gives
        # the same net zoom); scrub alongside still applies
        conn.sendall(b'{"type":"zoom","clicks":3}\n{"type":"zoom","clicks":-1}\n{"type":"scrub","frames":5}\n')
        self.assertTrue(tick_until(lambda: net("strips") == 2 and net("both") == 2))
        self.assertEqual({r for _, r in zooms()}, {"strips", "both"})
        self.assertEqual(SCENE.frame_current, 15)
        ZOOM.calls.clear()
        conn.sendall(b'{"type":"zoom","clicks":-2}\n')
        self.assertTrue(tick_until(lambda: len(zooms()) == 4))
        self.assertEqual(set(zooms()), {("out", "strips"), ("out", "both")})
        conn.close()
        bridge.close()

    def test_zoom_capped_per_tick(self):
        self._screen()
        addon._zoom(addon.protocol.total_zoom([{"type": "zoom", "clicks": 10**6}]))
        self.assertEqual(zooms().count(("in", "strips")), addon.protocol.MAX_ZOOM_PER_TICK)

    def test_zoom_failure_skips_that_editor(self):
        self._screen()
        ZOOM.fail_in.add("strips")
        addon._zoom(2)  # must not raise
        self.assertEqual(zooms(), [("in", "both"), ("in", "both")])

    def test_zoom_any_error_skips_that_editor(self):
        self._screen()
        strips = addon.bpy.context.window_manager.windows[0].screen.areas[0].regions[1]
        strips.view2d = None  # a region gone stale: AttributeError, not RuntimeError
        addon._zoom(1)
        self.assertEqual(zooms(), [("in", "both")])

    def _strips(self):
        self._screen()
        return addon.bpy.context.window_manager.windows[0].screen.areas[0].regions[1]

    def test_zoom_keeps_playhead_in_place(self):
        region = self._strips()
        SCENE.frame_current = 50  # 0..250 over 800 px: drawn at x=160
        before = region.view2d.view_to_region(50, 0)[0]
        addon._zoom(4)
        v = region.view2d
        self.assertLess(v.xmax - v.xmin, 250 * 0.75, "it zoomed in")
        self.assertLessEqual(abs(v.view_to_region(50, 0)[0] - before), 1)
        addon._zoom(-4)
        self.assertLessEqual(abs(v.view_to_region(50, 0)[0] - before), 1)
        self.assertAlmostEqual(v.xmax - v.xmin, 250, delta=0.01)

    def test_zoom_centers_playhead_off_screen(self):
        region = self._strips()
        SCENE.frame_current = 400  # past the right edge of 0..250
        addon._zoom(1)
        x = region.view2d.view_to_region(400, 0)[0]
        self.assertLessEqual(abs(x - 400), 2, "playhead centered in the 800 px region")

    def test_zoom_without_scene_still_zooms(self):
        self._screen()
        addon.bpy.context.scene = None
        try:
            addon._zoom(1)
        finally:
            addon.bpy.context.scene = SCENE
        self.assertEqual(sorted(zooms()), [("in", "both"), ("in", "strips")])

    def test_zoom_without_windows(self):
        addon.bpy.context.window_manager.windows = [types.SimpleNamespace(screen=None)]
        addon._zoom(1)
        self.assertEqual(ZOOM.calls, [])

    def _editor(self):
        """Strips around frame 50: A and C cross it, B is locked, D starts
        after it, E ends at it, F sits on a locked channel."""
        ed = _Editor()
        ed.add("A", 1, 1, 101, select=True)
        ed.add("B", 2, 1, 101, lock=True)
        ed.add("C", 3, 1, 101)
        ed.add("D", 4, 60, 160)
        ed.add("E", 5, 1, 50)
        ed.add("F", 6, 1, 101)
        ed.channels[6].lock = True
        SCENE.sequence_editor = ed
        SCENE.frame_current = 50
        return ed

    def test_cut_everything_under_playhead(self):
        ed = self._editor()
        self.assertEqual(addon._cut(SCENE), 2)
        self.assertEqual(ed.log, [("A", 50, "SOFT"), ("C", 50, "SOFT")])
        self.assertEqual(
            ed.spans(),
            [("A", 1, 50), ("A.001", 50, 101), ("B", 1, 101), ("C", 1, 50), ("C.001", 50, 101), ("D", 60, 160), ("E", 1, 50), ("F", 1, 101)],
        )
        # One undo step for the whole cut, and selection left as it was
        self.assertEqual(UNDO, ["Cut at playhead"])
        self.assertEqual({s.name for s in ed.strips if s.select}, {"A", "A.001"})
        # Again at the same frame: nothing left to cut, and no empty undo step
        self.assertEqual(addon._cut(SCENE), 0)
        self.assertEqual(UNDO, ["Cut at playhead"])

    def test_cut_inside_meta(self):
        ed = self._editor()
        meta = _Owner(ed.log)
        meta.add("M1", 1, 1, 101)
        ed.meta_stack = [meta]
        self.assertEqual(addon._cut(SCENE), 1)
        self.assertEqual(ed.log, [("M1", 50, "SOFT")])
        self.assertEqual(meta.spans(), [("M1", 1, 50), ("M1.001", 50, 101)])

    def test_cut_failure_skips_that_strip(self):
        ed = self._editor()
        ed.strips[0].fail = True
        self.assertEqual(addon._cut(SCENE), 1)
        self.assertEqual(ed.log, [("C", 50, "SOFT")])
        self.assertEqual(UNDO, ["Cut at playhead"])

    def test_cut_undo_failure_doesnt_raise(self):
        self._editor()
        orig = addon.bpy.ops.ed.undo_push
        addon.bpy.ops.ed.undo_push = lambda message="": 1 / 0
        try:
            self.assertEqual(addon._cut(SCENE), 2)
        finally:
            addon.bpy.ops.ed.undo_push = orig

    def test_cut_on_blender_4_2(self):
        # Before 4.4 the strips are "sequences"
        ed = self._editor()
        ed.sequences = ed.strips
        del ed.strips
        self.assertEqual(addon._cut(SCENE), 2)
        self.assertEqual(ed.log, [("A", 50, "SOFT"), ("C", 50, "SOFT")])

    def test_cut_unknown_editor_doesnt_raise(self):
        SCENE.sequence_editor = types.SimpleNamespace(meta_stack=[])  # no strips at all
        self.assertEqual(addon._cut(SCENE), 0)
        SCENE.sequence_editor = types.SimpleNamespace()  # no meta_stack either
        self.assertEqual(addon._cut(SCENE), 0)
        self.assertEqual(UNDO, [])

    def test_cut_without_strips(self):
        self.assertEqual(addon._cut(SCENE), 0)  # no sequence editor at all
        SCENE.sequence_editor = _Editor()
        self.assertEqual(addon._cut(SCENE), 0)
        self.assertEqual(UNDO, [])

    def test_cut_after_scrub_in_the_same_read(self):
        ed = self._editor()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        SCENE.frame_current = 40
        # Scrub to 50, cut, scrub on to 70, cut: one read, cuts in order
        conn.sendall(b'{"type":"scrub","frames":10}\n{"type":"cut"}\n{"type":"scrub","frames":20}\n{"type":"cut"}\n')
        self.assertTrue(tick_until(lambda: len(UNDO) == 2))
        self.assertEqual(SCENE.frame_current, 70)
        cuts = [(name, frame) for name, frame, _ in ed.log]
        self.assertEqual(cuts[:2], [("A", 50), ("C", 50)])
        self.assertEqual(sorted(cuts[2:]), [("A.001", 70), ("C.001", 70), ("D", 70)])
        conn.close()
        bridge.close()

    def test_cut_flood_capped(self):
        ed = self._editor()
        addon._apply([{"type": "cut"}, {"type": "scrub", "frames": 1}] * 30, PREFS)
        self.assertEqual(len(UNDO), addon.protocol.MAX_CUTS_PER_TICK)
        self.assertEqual(SCENE.frame_current, 80)
        self.assertTrue(ed.log)

    def test_closed_port_never_blocks(self):
        PREFS.port = free_port()
        worst = 0.0
        deadline = time.monotonic() + 1.5
        while time.monotonic() < deadline:
            t = time.perf_counter()
            addon.tick()
            worst = max(worst, time.perf_counter() - t)
            time.sleep(0.005)
        self.assertLess(worst, 0.02, "a tick blocked for %.1f ms" % (worst * 1000))
        self.assertIsNone(addon.link.sock)

    def test_hello_state_and_scrub(self):
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        for _ in range(3):
            addon.tick()
        lines = read_lines(conn)
        self.assertEqual(lines[0]["type"], "hello")
        self.assertEqual(lines[1], {"type": "state", "scene": "Edit", "frame": 10, "start": 1, "end": 100, "fps": 24.0})

        conn.sendall(b'{"type":"scrub","frames":10}\n{"type":"scrub","frames":-3}\n')
        self.assertTrue(tick_until(lambda: SCENE.frame_current == 17))
        self.assertEqual(read_lines(conn)[-1]["frame"], 17)
        conn.close()
        bridge.close()

    def test_reconnect_sends_state_again(self):
        bridge = Bridge()
        PREFS.port = bridge.port
        first = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        addon.tick()
        first.close()  # bridge restarted, or dropped an idle add-on
        tick_until(lambda: addon.link.sock is None)
        second = bridge.accept(ticks=400)
        tick_until(lambda: addon.link.sock is not None)
        for _ in range(3):
            addon.tick()
        types_seen = [m["type"] for m in read_lines(second)]
        self.assertEqual(types_seen[:2], ["hello", "state"])
        second.close()
        bridge.close()

    def test_turning_off_closes_pending_connection(self):
        PREFS.port = free_port()
        addon.tick()
        PREFS.enabled = False
        addon.tick()
        self.assertIsNone(addon.link.pending)
        self.assertIsNone(addon.link.sock)
        self.assertEqual(addon.link.status, "Turned off")

    def test_hostname_refused_without_lookup(self):
        PREFS.host = "example.com"
        addon.tick()
        self.assertIsNone(addon.link.pending)
        self.assertIn("IP address", addon.link.status)

    def test_tick_never_raises(self):
        orig = addon._tick
        addon._tick = lambda: 1 / 0
        try:
            self.assertEqual(addon.tick(), addon.RETRY)
        finally:
            addon._tick = orig

    def test_unsent_bytes_kept_whole(self):
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)

        class Partial:
            """A socket that takes 5 bytes per send, then blocks."""

            def __init__(self, real):
                self.real, self.calls = real, 0

            def send(self, data):
                self.calls += 1
                if self.calls % 2 == 0:
                    raise BlockingIOError
                return self.real.send(data[:5])

            def __getattr__(self, name):
                return getattr(self.real, name)

        addon.link.sock = Partial(addon.link.sock)
        addon._send({"type": "ping"})
        for _ in range(20):
            addon._flush()
        addon.link.sock = addon.link.sock.real
        self.assertIn({"type": "ping"}, read_lines(conn))
        conn.close()
        bridge.close()


if __name__ == "__main__":
    unittest.main()
