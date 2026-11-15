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
    follow_selection = False  # tests that want it turn it on


# Blender's frame_change_post: called with the scene after every frame_set
FRAME_HANDLERS = []


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
        for h in list(FRAME_HANDLERS):
            h(self, None)


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

    def __init__(self, name, channel, start, end, log, level, lock=False, select=False, fail=False, type="MOVIE"):
        self.name, self.channel, self.type = name, channel, type
        self.frame_final_start, self.frame_final_end = start, end
        self.lock, self.select, self.fail = lock, select, fail
        self.log, self.level = log, level
        self.input_1 = self.input_2 = None
        self.fixed = False  # an effect with inputs: it can't be moved itself

    def as_pointer(self):
        return id(self)

    # Blender 5 names for the handles; the add-on uses these when present
    @property
    def left_handle(self):
        return self.frame_final_start

    @property
    def right_handle(self):
        return self.frame_final_end

    @property
    def content_start(self):
        return float(self.frame_final_start)

    @content_start.setter
    def content_start(self, value):
        if self.fixed:
            raise AttributeError('attribute "content_start" is read-only')
        d = int(value) - self.frame_final_start
        self.frame_final_start += d
        self.frame_final_end += d

    def split(self, frame, method):
        if self.fail:
            raise RuntimeError("Strip can't be split")
        self.log.append((self.name, frame, method))
        right = _Strip(self.name + ".001", self.channel, frame, self.frame_final_end, self.log, self.level, self.lock, self.select)
        self.frame_final_end = frame
        self.level.append(right)
        return right


class _Level(list):
    """A level's strips: remove() works like Blender 5.2's, removing the
    effects that use the strip too, and raising for a strip that is gone."""

    def __init__(self):
        super().__init__()
        self.fail = set()  # names whose removal raises

    def remove(self, strip):
        if strip.name in self.fail:
            raise RuntimeError("Strip can't be removed")
        if strip not in self:
            raise ReferenceError("StructRNA of type Strip has been removed")
        super().remove(strip)
        for other in list(self):
            if strip in (getattr(other, "input_1", None), getattr(other, "input_2", None)):
                self.remove(other)


class _Channel:
    def __init__(self, lock=False):
        self.lock = lock
        self.mute = False


class _Owner:
    """The editor or a meta strip: its strips and its channels (0..128)."""

    def __init__(self, log):
        self.log = log
        self.strips = _Level()
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


class _Freed:
    """What a scene the undo reloaded looks like to old Python references:
    any use is a bug (in Blender it can crash)."""

    def __getattr__(self, name):
        raise ReferenceError("StructRNA of type Scene has been removed (.%s)" % name)


class _EdUndo:
    """Fake bpy.ops.ed.undo. Like Blender 4.2's, its poll fails without a
    window and screen in the context (a timer has none). Each undo reloads
    the scene: the old object is freed and bpy.context.scene is a new one
    with the playhead at restore_frame, keeping the strips editor."""

    def __init__(self):
        self.calls = []  # the temp_override keys of each undo
        self.left = 0  # undo steps left
        self.fail = False
        self.restore_frame = 1

    def poll(self):
        kw = ZOOM.override or {}
        return bool(kw.get("window") and kw.get("screen")) and self.left > 0

    def __call__(self):
        if not self.poll():
            raise RuntimeError("Operator bpy.ops.ed.undo.poll() failed, context is incorrect")
        if self.fail:
            raise RuntimeError("undo failed")
        self.calls.append(sorted(ZOOM.override))
        self.left -= 1
        ctx = sys.modules["bpy"].context
        old = ctx.scene
        new = _Scene()
        new.frame_current = self.restore_frame
        new.sequence_editor = old.sequence_editor
        old.__class__ = _Freed
        ctx.scene = new
        return {"FINISHED"}


ED_UNDO = _EdUndo()


class _Player:
    """Fake bpy.ops.screen.animation_play and animation_cancel, as Blender
    5.2 behaves (checked in a real 5.2 from a timer): playing from a video
    editor plays the scene it shows, its window's workspace sequencer
    scene; from any other area, or with no area, it plays the window's
    scene. While anything plays, every screen's is_animation_playing is
    true. cancel(restore_frame=False) leaves the playhead where it is and,
    with nothing playing, passes through."""

    def __init__(self):
        self.calls = []
        self.fail = False

    def _screens(self):
        return [w.screen for w in sys.modules["bpy"].context.window_manager.windows if w.screen is not None]

    def _set(self, on):
        for screen in self._screens():
            screen.is_animation_playing = on

    def playing(self):
        return any(getattr(s, "is_animation_playing", False) for s in self._screens())

    def play(self, reverse=False, sync=False):
        kw = ZOOM.override or {}
        if self.fail or not kw.get("window"):
            raise RuntimeError("Operator bpy.ops.screen.animation_play.poll() failed, context is incorrect")
        if self.playing():  # like Space: a second play stops
            self.calls.append(("stop",))
            self._set(False)
            return {"FINISHED"}
        win, area = kw["window"], kw.get("area")
        if area is not None and area.type == "SEQUENCE_EDITOR":
            ws = getattr(win, "workspace", None)
            scene = getattr(ws, "sequencer_scene", None) or win.scene
        else:
            scene = win.scene
        self.calls.append(("play", scene.name, sorted(kw)))
        self._set(True)
        return {"FINISHED"}

    def cancel(self, restore_frame=True):
        if self.fail:
            raise RuntimeError("cancel failed")
        self.calls.append(("cancel", restore_frame))
        if not self.playing():
            return {"PASS_THROUGH"}
        self._set(False)
        return {"FINISHED"}


PLAYER = _Player()


class _SeekScene(_Scene):
    """A scene that logs how the playhead was moved: ("assign", f) for
    frame_current = f, ("frame_set", f) for frame_set(f)."""

    def __init__(self):
        object.__setattr__(self, "log", [])

    def __setattr__(self, name, value):
        if name == "frame_current":
            self.log.append(("assign", value))
        object.__setattr__(self, name, value)

    def frame_set(self, f):
        self.log.append(("frame_set", f))
        object.__setattr__(self, "frame_current", f)
        for h in list(FRAME_HANDLERS):
            h(self, None)


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
        ed=types.SimpleNamespace(undo_push=lambda message="": UNDO.append(message) or {"FINISHED"}, undo=ED_UNDO),
        screen=types.SimpleNamespace(animation_play=PLAYER.play, animation_cancel=PLAYER.cancel),
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
        addon.bpy.context.scene = SCENE
        addon.bpy.context.window_manager.is_interface_locked = False
        ED_UNDO.calls.clear()
        ED_UNDO.left = 0
        ED_UNDO.fail = False
        ED_UNDO.restore_frame = 1
        PLAYER.calls.clear()
        PLAYER.fail = False
        addon._shuttle_stop()
        addon.bpy.context.sequencer_scene = None
        PREFS.follow_selection = False
        FRAME_HANDLERS.clear()
        addon.bpy.app.is_job_running = lambda job: False

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

    def test_cut_everything_under_playhead_when_none_selected(self):
        ed = self._editor()
        ed.strips[0].select = False
        self.assertEqual(addon._cut(SCENE), 2)
        self.assertEqual(ed.log, [("A", 50, "SOFT"), ("C", 50, "SOFT")])
        self.assertEqual(
            ed.spans(),
            [("A", 1, 50), ("A.001", 50, 101), ("B", 1, 101), ("C", 1, 50), ("C.001", 50, 101), ("D", 60, 160), ("E", 1, 50), ("F", 1, 101)],
        )
        # One undo step for the whole cut
        self.assertEqual(UNDO, ["Cut at playhead"])
        # Again at the same frame: nothing left to cut, and no empty undo step
        self.assertEqual(addon._cut(SCENE), 0)
        self.assertEqual(UNDO, ["Cut at playhead"])

    def test_cut_selected_only(self):
        # Decision 202611159rr6: the selected strips under the playhead if
        # any, else all of them. A is selected, C isn't.
        ed = self._editor()
        self.assertEqual(addon._cut(SCENE), 1)
        self.assertEqual(ed.log, [("A", 50, "SOFT")])
        # Both halves keep the selection, as Blender's split does
        self.assertEqual({s.name for s in ed.strips if s.select}, {"A", "A.001"})

    def test_cut_selection_elsewhere_cuts_everything(self):
        ed = self._editor()
        ed.strips[0].select = False
        ed.strips[3].select = True  # D, not under the playhead
        self.assertEqual(addon._cut(SCENE), 2)
        self.assertEqual([n for n, _, _ in ed.log], ["A", "C"])

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
        ed.strips[0].select = False  # nothing selected: the cut takes everything under the playhead
        ed.strips[0].fail = True
        self.assertEqual(addon._cut(SCENE), 1)
        self.assertEqual(ed.log, [("C", 50, "SOFT")])
        self.assertEqual(UNDO, ["Cut at playhead"])

    def test_cut_undo_failure_doesnt_raise(self):
        ed = self._editor()
        ed.strips[0].select = False  # nothing selected: the cut takes everything under the playhead
        orig = addon.bpy.ops.ed.undo_push
        addon.bpy.ops.ed.undo_push = lambda message="": 1 / 0
        try:
            self.assertEqual(addon._cut(SCENE), 2)
        finally:
            addon.bpy.ops.ed.undo_push = orig

    def test_cut_on_blender_4_2(self):
        # Before 4.4 the strips are "sequences"
        ed = self._editor()
        ed.strips[0].select = False  # nothing selected: the cut takes everything under the playhead
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
        # Without selection following the playhead, A stays the only
        # selected strip, so only A (then its right half) is cut
        cuts = [(name, frame) for name, frame, _ in ed.log]
        self.assertEqual(cuts, [("A", 50), ("A.001", 70)])
        conn.close()
        bridge.close()

    def test_scrub_then_cut_with_selection_following(self):
        # The owner's flow: the playhead selects what's under it, so a cut
        # after a scrub cuts everything there, whatever was selected before
        ed = self._editor()
        PREFS.follow_selection = True
        FRAME_HANDLERS.append(addon.on_frame_change)
        SCENE.frame_current = 40
        addon._apply([{"type": "scrub", "frames": 10}, {"type": "cut"}], PREFS)
        self.assertEqual([(n, f) for n, f, _ in ed.log], [("A", 50), ("C", 50)])
        # After the cut, the right halves (under the playhead) are selected
        self.assertEqual({s.name for s in ed.strips if s.select}, {"A.001", "C.001"})
        addon._apply([{"type": "scrub", "frames": 20}, {"type": "cut"}], PREFS)
        self.assertEqual(sorted((n, f) for n, f, _ in ed.log[2:]), [("A.001", 70), ("C.001", 70), ("D", 70)])

    def test_hand_picked_selection_survives_cut(self):
        # Review of #7: playhead on 50, only C picked by hand. The cut splits
        # C alone, and the next ripple delete must take only C's right half,
        # never A, which was under the playhead but not picked.
        ed = self._editor()
        ed.strips[0].select = False  # A
        ed.strips[2].select = True  # C
        PREFS.follow_selection = True
        FRAME_HANDLERS.append(addon.on_frame_change)
        addon._apply([{"type": "cut"}], PREFS)
        self.assertEqual([n for n, _, _ in ed.log], ["C"])
        self.assertEqual({s.name for s in ed.strips if s.select}, {"C.001"})
        addon._apply([{"type": "ripple_delete"}], PREFS)
        names = {s.name for s in ed.strips}
        self.assertNotIn("C.001", names)
        self.assertIn("A", names)

    def test_cut_selection_set_before_undo_step(self):
        # The undo step records the right halves selected, so redo matches
        ed = self._editor()
        seen = []
        orig = addon.bpy.ops.ed.undo_push
        addon.bpy.ops.ed.undo_push = lambda message="": seen.append(sorted(s.name for s in ed.strips if s.select)) or {"FINISHED"}
        try:
            addon._cut(SCENE, follow=True)
        finally:
            addon.bpy.ops.ed.undo_push = orig
        self.assertEqual(seen, [["A.001"]])

    def test_selection_follows_playhead(self):
        ed = self._editor()
        PREFS.follow_selection = True
        addon.on_frame_change(SCENE)
        # A and C are under 50; B is locked, F is on a locked channel, D
        # starts later, E ends at 50
        self.assertEqual({s.name for s in ed.strips if s.select}, {"A", "C"})
        SCENE.frame_current = 120
        addon.on_frame_change(SCENE)
        self.assertEqual({s.name for s in ed.strips if s.select}, {"D"})
        SCENE.frame_current = 500
        addon.on_frame_change(SCENE)
        self.assertEqual({s.name for s in ed.strips if s.select}, set())

    def test_selection_follows_inside_meta(self):
        ed = self._editor()
        meta = _Owner(ed.log)
        meta.add("M1", 1, 1, 101)
        meta.add("M2", 2, 60, 90, select=True)
        ed.meta_stack = [meta]
        PREFS.follow_selection = True
        addon.on_frame_change(SCENE)
        self.assertEqual({s.name for s in meta.strips if s.select}, {"M1"})
        # The top level isn't touched while editing inside a meta
        self.assertEqual({s.name for s in ed.strips if s.select}, {"A"})

    def test_selection_doesnt_follow_when_off(self):
        ed = self._editor()
        addon.on_frame_change(SCENE)
        self.assertEqual({s.name for s in ed.strips if s.select}, {"A"})

    def test_selection_doesnt_follow_while_rendering(self):
        ed = self._editor()
        PREFS.follow_selection = True
        addon.bpy.app.is_job_running = lambda job: job == "RENDER"
        addon.on_frame_change(SCENE)
        self.assertEqual({s.name for s in ed.strips if s.select}, {"A"})

    def test_selection_follow_never_raises(self):
        PREFS.follow_selection = True

        class Broken:
            frame_current = 50

            @property
            def sequence_editor(self):
                raise RuntimeError("gone")

        addon.on_frame_change(Broken())  # must not raise
        SCENE.sequence_editor = None
        addon.on_frame_change(SCENE)

    def test_ripple_delete_then_selection_follows(self):
        ed = self._editor()
        PREFS.follow_selection = True
        FRAME_HANDLERS.append(addon.on_frame_change)
        addon._apply([{"type": "ripple_delete"}], PREFS)
        # Whatever ends up under the playhead is selected, nothing else
        under = {s.name for s in ed.strips if not s.lock and not ed.channels[s.channel].lock and s.frame_final_start <= SCENE.frame_current < s.frame_final_end}
        self.assertEqual({s.name for s in ed.strips if s.select}, under)

    def test_frame_change_handler_registered_once(self):
        handlers = types.SimpleNamespace(frame_change_post=[], persistent=lambda f: f)
        timers = types.SimpleNamespace(is_registered=lambda f: True, register=lambda *a, **k: None, unregister=lambda f: None)
        old_app, old_utils = addon.bpy.app, getattr(addon.bpy, "utils", None)
        addon.bpy.app = types.SimpleNamespace(version_string="5.2.2", handlers=handlers, timers=timers, is_job_running=lambda j: False)
        addon.bpy.utils = types.SimpleNamespace(register_class=lambda c: None, unregister_class=lambda c: None)
        try:
            # A stale copy from an earlier load of the add-on
            def on_frame_change(scene, depsgraph=None):
                pass

            on_frame_change.__module__ = addon.__name__
            handlers.frame_change_post.append(on_frame_change)
            addon.register()
            addon.register()
            self.assertEqual(handlers.frame_change_post, [addon.on_frame_change])
            addon.unregister()
            self.assertEqual(handlers.frame_change_post, [])
        finally:
            addon.bpy.app = old_app
            if old_utils is None:
                del addon.bpy.utils
            else:
                addon.bpy.utils = old_utils

    def test_cut_flood_capped(self):
        ed = self._editor()
        addon._apply([{"type": "cut"}, {"type": "scrub", "frames": 1}] * 30, PREFS)
        self.assertEqual(len(UNDO), addon.protocol.MAX_CUTS_PER_TICK)
        self.assertEqual(SCENE.frame_current, 80)
        self.assertTrue(ed.log)

    def _undo_scene(self, left=5):
        """A screen, a throwaway scene the fake undo may free (never SCENE),
        and left undo steps."""
        self._screen()
        scene = _Scene()
        scene.frame_current = 50
        addon.bpy.context.scene = scene
        ED_UNDO.left = left
        return scene

    def test_undo_with_window_from_timer(self):
        self._undo_scene()
        self.assertTrue(addon._undo())
        # Window, screen, and the first strips timeline
        self.assertEqual(ED_UNDO.calls, [["area", "region", "screen", "window"]])
        self.assertIsNone(ZOOM.override, "the override ended")

    def test_undo_without_timeline_still_runs(self):
        self._undo_scene()
        win = addon.bpy.context.window_manager.windows[0]
        win.screen.areas = [a for a in win.screen.areas if a.type != "SEQUENCE_EDITOR"]
        self.assertTrue(addon._undo())
        self.assertEqual(ED_UNDO.calls, [["screen", "window"]])

    def test_undo_nothing_left(self):
        self._undo_scene(left=0)
        self.assertFalse(addon._undo())
        self.assertEqual(ED_UNDO.calls, [])

    def test_undo_without_window(self):
        self._undo_scene()
        addon.bpy.context.window_manager.windows = []
        self.assertFalse(addon._undo())
        addon.bpy.context.window_manager.windows = [types.SimpleNamespace(screen=None)]
        self.assertFalse(addon._undo())
        self.assertEqual(ED_UNDO.calls, [])

    def test_undo_refused_during_modal_operator(self):
        self._undo_scene()
        wm = addon.bpy.context.window_manager
        # A strip being dragged in a second window blocks it too
        busy = types.SimpleNamespace(screen=types.SimpleNamespace(areas=[]), modal_operators=[types.SimpleNamespace(bl_idname="TRANSFORM_OT_seq_slide")])
        wm.windows.append(busy)
        self.assertFalse(addon._undo())
        self.assertEqual(ED_UNDO.calls, [])
        # Dropped, not kept: once the drag ends, the next press undoes once
        busy.modal_operators = []
        self.assertTrue(addon._undo())
        self.assertEqual(len(ED_UNDO.calls), 1)

    def test_undo_refused_while_interface_locked(self):
        self._undo_scene()
        addon.bpy.context.window_manager.is_interface_locked = True
        self.assertFalse(addon._undo())
        self.assertEqual(ED_UNDO.calls, [])

    def test_undo_failure_doesnt_raise(self):
        self._undo_scene()
        ED_UNDO.fail = True
        self.assertFalse(addon._undo())
        addon.bpy.ops.ed.undo = None  # no undo operator at all
        try:
            self.assertFalse(addon._undo())
        finally:
            addon.bpy.ops.ed.undo = ED_UNDO

    def test_undo_then_edit_uses_reloaded_scene(self):
        # Cut at 50, undo (playhead back to 40), scrub 5, cut: the scrub and
        # the second cut use the scene the undo reloaded, never the freed one
        scene = self._undo_scene()
        ed = self._editor()  # built on SCENE: move it to the undo scene
        ed.strips[0].select = False  # nothing selected: the cut takes everything under the playhead
        SCENE.sequence_editor = None
        scene.sequence_editor = ed
        scene.frame_current = 50
        ED_UNDO.restore_frame = 40
        addon._apply([{"type": "cut"}, {"type": "undo"}, {"type": "scrub", "frames": 5}, {"type": "cut"}], PREFS)
        self.assertIsInstance(scene, _Freed)
        now = addon.bpy.context.scene
        self.assertEqual(now.frame_current, 45)
        self.assertEqual([(n, f) for n, f, _ in ed.log][:2], [("A", 50), ("C", 50)])
        # The fake undo leaves the strips as they were: E (1..50) now
        # crosses the playhead too
        self.assertEqual(sorted((n, f) for n, f, _ in ed.log[2:]), [("A", 45), ("C", 45), ("E", 45)])
        self.assertEqual(len(ED_UNDO.calls), 1)

    def test_undo_once_per_command_capped(self):
        self._undo_scene(left=100)
        addon._apply([{"type": "undo"}] * 3, PREFS)
        self.assertEqual(len(ED_UNDO.calls), 3, "three presses, three undos")
        ED_UNDO.calls.clear()
        addon._apply([{"type": "undo"}] * 50, PREFS)
        self.assertEqual(len(ED_UNDO.calls), addon.protocol.MAX_UNDOS_PER_TICK)

    def test_undo_over_the_link(self):
        self._undo_scene(left=10)
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        read_lines(conn)
        ED_UNDO.restore_frame = 7
        try:
            conn.sendall(b'{"type":"undo"}\n')
            self.assertTrue(tick_until(lambda: len(ED_UNDO.calls) == 1))
            # The reloaded scene's playhead goes to the page
            self.assertTrue(any(m.get("type") == "state" and m.get("frame") == 7 for m in read_lines(conn)))
            # Nothing left to undo: pressing again is harmless
            ED_UNDO.left = 0
            conn.sendall(b'{"type":"undo"}\n')
            for _ in range(20):
                addon.tick()
            self.assertEqual(len(ED_UNDO.calls), 1)
            self.assertIsNotNone(addon.link.sock, "link kept")
        finally:
            conn.close()
            bridge.close()

    def _ripple_editor(self):
        """Video (channel 1) and audio (2) cut at 50 and 100, a title (3)
        later on; the playhead on the middle clips."""
        ed = _Editor()
        for name, ch in (("V", 1), ("A", 2)):
            ed.add(name + "1", ch, 1, 50)
            ed.add(name + "2", ch, 50, 100)
            ed.add(name + "3", ch, 100, 200)
        ed.add("T", 3, 150, 180)
        SCENE.sequence_editor = ed
        SCENE.frame_current = 70
        return ed

    def test_ripple_delete_middle_clips(self):
        ed = self._ripple_editor()
        self.assertEqual(addon._ripple_delete(SCENE), 2)
        self.assertEqual(ed.spans(), [("A1", 1, 50), ("A3", 50, 150), ("T", 100, 130), ("V1", 1, 50), ("V3", 50, 150)])
        self.assertEqual({s.name: s.channel for s in ed.strips}, {"V1": 1, "V3": 1, "A1": 2, "A3": 2, "T": 3})
        self.assertEqual(SCENE.frame_current, 50, "playhead at the join")
        self.assertEqual(UNDO, ["Ripple delete"])
        # Again: the clips now under the playhead go
        self.assertEqual(addon._ripple_delete(SCENE), 2)
        self.assertEqual(ed.spans(), [("A1", 1, 50), ("T", 50, 80), ("V1", 1, 50)])
        self.assertEqual(UNDO, ["Ripple delete"] * 2)

    def test_ripple_delete_selected_only(self):
        ed = self._ripple_editor()
        ed.strips[1].select = True  # V2
        self.assertEqual(addon._ripple_delete(SCENE), 1)
        # A2 is still under the playhead: deleted, nothing moves
        self.assertEqual([n for n, _, _ in ed.spans()], ["A1", "A2", "A3", "T", "V1", "V3"])
        self.assertEqual(SCENE.frame_current, 70)

    def test_ripple_delete_skips_locked(self):
        ed = self._ripple_editor()
        ed.channels[2].lock = True  # audio: kept under the playhead, so the gap stays
        self.assertEqual(addon._ripple_delete(SCENE), 1)
        self.assertEqual([x for x in ed.spans() if x[0] in ("A2", "V3")], [("A2", 50, 100), ("V3", 100, 200)])
        ed = self._ripple_editor()
        ed.strips[-1].lock = True  # the title after the gap: nothing moves
        self.assertEqual(addon._ripple_delete(SCENE), 2)
        self.assertEqual([x for x in ed.spans() if x[0] in ("T", "V3")], [("T", 150, 180), ("V3", 100, 200)])
        self.assertEqual(SCENE.frame_current, 70)

    def test_ripple_delete_effect_goes_with_its_input(self):
        ed = self._ripple_editor()
        fx = ed.add("FX", 4, 50, 100)
        fx.input_1, fx.fixed = ed.strips[1], True  # an effect on V2
        follow = ed.add("X", 5, 150, 180)
        follow.input_1, follow.fixed = ed.strips[2], True  # an effect on V3
        self.assertEqual(addon._ripple_delete(SCENE), 3)
        self.assertNotIn("FX", {s.name for s in ed.strips})
        # The effect on V3 isn't moved itself (Blender moves it with V3)
        self.assertEqual([x for x in ed.spans() if x[0] in ("X", "V3")], [("V3", 50, 150), ("X", 150, 180)])

    def test_ripple_delete_remove_failure_moves_nothing(self):
        ed = self._ripple_editor()
        ed.strips.fail.add("A2")
        self.assertEqual(addon._ripple_delete(SCENE), 1)
        self.assertEqual([x for x in ed.spans() if x[0] in ("A2", "V3")], [("A2", 50, 100), ("V3", 100, 200)])
        self.assertEqual(SCENE.frame_current, 70)
        self.assertEqual(UNDO, ["Ripple delete"], "what was deleted can be undone")

    def test_ripple_delete_move_failure_doesnt_raise(self):
        ed = self._ripple_editor()
        ed.strips[2].fixed = True  # V3 refuses to move
        self.assertEqual(addon._ripple_delete(SCENE), 2)
        self.assertEqual(SCENE.frame_current, 50)
        self.assertEqual([x for x in ed.spans() if x[0] in ("A3", "V3")], [("A3", 50, 150), ("V3", 100, 200)])
        self.assertEqual(UNDO, ["Ripple delete"])

    def test_ripple_delete_inside_meta(self):
        ed = self._ripple_editor()
        meta = _Owner(ed.log)
        meta.add("M1", 1, 1, 60)
        meta.add("M2", 1, 60, 120)
        meta.add("M3", 1, 120, 130)
        ed.meta_stack = [meta]
        self.assertEqual(addon._ripple_delete(SCENE), 1)
        self.assertEqual(meta.spans(), [("M1", 1, 60), ("M3", 60, 70)])
        self.assertEqual(len(ed.strips), 7, "the top level is untouched")

    def test_ripple_delete_on_blender_4_2(self):
        # Before 4.4: "sequences", frame_final_start/end and frame_start
        ed = _Editor()
        ed.sequences = ed.strips
        del ed.strips

        class Old:
            def __init__(self, name, start, end):
                self.name, self.channel, self.lock, self.select = name, 1, False, False
                self.frame_final_start, self.frame_final_end, self.frame_start = start, end, start

            def as_pointer(self):
                return id(self)

        for s in (Old("a", 1, 50), Old("b", 50, 100), Old("c", 100, 150)):
            ed.sequences.append(s)
        c = ed.sequences[2]
        SCENE.sequence_editor = ed
        SCENE.frame_current = 60
        self.assertEqual(addon._ripple_delete(SCENE), 1)
        self.assertEqual(c.frame_start, 50)
        self.assertEqual(SCENE.frame_current, 50)

    def test_ripple_delete_nothing_to_do(self):
        self.assertEqual(addon._ripple_delete(SCENE), 0)  # no sequence editor
        SCENE.sequence_editor = types.SimpleNamespace(meta_stack=[])  # an editor it can't read
        self.assertEqual(addon._ripple_delete(SCENE), 0)
        ed = self._ripple_editor()
        SCENE.frame_current = 500  # past every strip
        self.assertEqual(addon._ripple_delete(SCENE), 0)
        self.assertEqual(len(ed.strips), 7)
        self.assertEqual(UNDO, [])

    def test_ripple_delete_undo_failure_doesnt_raise(self):
        self._ripple_editor()
        orig = addon.bpy.ops.ed.undo_push
        addon.bpy.ops.ed.undo_push = lambda message="": 1 / 0
        try:
            self.assertEqual(addon._ripple_delete(SCENE), 2)
        finally:
            addon.bpy.ops.ed.undo_push = orig

    def test_ripple_delete_over_the_link_in_order(self):
        ed = self._ripple_editor()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        SCENE.frame_current = 40
        # Scrub onto the middle clips, delete, scrub on and cut there
        conn.sendall(b'{"type":"scrub","frames":30}\n{"type":"ripple_delete"}\n{"type":"scrub","frames":10}\n{"type":"cut"}\n')
        self.assertTrue(tick_until(lambda: len(UNDO) == 2))
        self.assertEqual(UNDO, ["Ripple delete", "Cut at playhead"])
        self.assertEqual(SCENE.frame_current, 60)
        self.assertEqual(sorted(n for n, _, _ in ed.log), ["A3", "V3"])
        conn.close()
        bridge.close()

    def test_ripple_delete_flood_capped(self):
        ed = _Editor()
        for i in range(40):
            ed.add("S%d" % i, 1, i * 10, i * 10 + 10)
        SCENE.sequence_editor = ed
        SCENE.frame_current = 0
        addon._apply([{"type": "ripple_delete"}] * 30, PREFS)
        self.assertEqual(len(UNDO), addon.protocol.MAX_RIPPLES_PER_TICK)
        self.assertEqual(len(ed.strips), 40 - addon.protocol.MAX_RIPPLES_PER_TICK)

    def _play_window(self, editor=True, window_scene=None, sequencer_scene=None):
        """One window: a properties area, and a video editor if editor. Its
        scene is window_scene (another scene by default, as in Blender 5
        where the window's scene and the edited one differ) and its
        workspace's sequencer scene is sequencer_scene (SCENE by default)."""
        areas = [_area("PROPERTIES", "", [_region("props")])]
        if editor:
            areas.append(_area("SEQUENCE_EDITOR", "SEQUENCER", [_region("header", "HEADER"), _region("strips")]))
        if window_scene is None:
            window_scene = _Scene()
            window_scene.name = "Scene"
        win = types.SimpleNamespace(
            screen=types.SimpleNamespace(areas=areas, is_animation_playing=False),
            scene=window_scene,
            workspace=types.SimpleNamespace(sequencer_scene=sequencer_scene or SCENE),
        )
        addon.bpy.context.window_manager.windows.append(win)
        addon.bpy.context.sequencer_scene = SCENE
        return win

    def test_play_from_video_editor_plays_edited_scene(self):
        self._play_window()
        self.assertEqual(addon._play_pause(SCENE), "play")
        # From the video editor, so the edited scene plays, not the window's
        self.assertEqual(PLAYER.calls, [("play", "Edit", ["area", "region", "screen", "window"])])
        self.assertTrue(addon._playing())
        self.assertIsNone(ZOOM.override, "the override ended")

    def test_pause_leaves_playhead(self):
        self._play_window()
        addon._apply([{"type": "play_pause"}], PREFS)
        SCENE.frame_current = 42  # where playback got to
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertEqual(PLAYER.calls[1], ("cancel", False))
        self.assertFalse(addon._playing())
        self.assertEqual(SCENE.frame_current, 42)
        # And again: play, pause
        addon._apply([{"type": "play_pause"}], PREFS)
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertEqual([c[0] for c in PLAYER.calls], ["play", "cancel", "play", "cancel"])

    def test_two_presses_in_one_read_do_nothing(self):
        self._play_window()
        addon._apply([{"type": "play_pause"}] * 2, PREFS)
        self.assertEqual(PLAYER.calls, [])
        addon._apply([{"type": "play_pause"}] * 3, PREFS)
        self.assertEqual([c[0] for c in PLAYER.calls], ["play"])

    def test_pause_whatever_plays(self):
        # Playback started by hand in another window, on another scene:
        # the button pauses it rather than starting a second one
        other = _Scene()
        other.name = "Other"
        self._play_window(editor=False, window_scene=other, sequencer_scene=other)
        self._play_window()
        PLAYER._set(True)
        self.assertEqual(addon._play_pause(SCENE), "pause")
        self.assertEqual(PLAYER.calls, [("cancel", False)])
        self.assertFalse(addon._playing())

    def test_play_without_video_editor_uses_window_showing_scene(self):
        self._play_window(editor=False, window_scene=SCENE)
        self.assertEqual(addon._play_pause(SCENE), "play_window")
        self.assertEqual(PLAYER.calls, [("play", "Edit", ["screen", "window"])])

    def test_play_skipped_when_it_would_play_another_scene(self):
        # No video editor, and the window shows another scene: playing from
        # it would play that one
        self._play_window(editor=False)
        self.assertIsNone(addon._play_pause(SCENE))
        # A video editor in a window whose workspace edits another scene
        # is no good either
        addon.bpy.context.window_manager.windows = []
        other = _Scene()
        other.name = "Other"
        self._play_window(sequencer_scene=other)
        self.assertIsNone(addon._play_pause(SCENE))
        self.assertEqual(PLAYER.calls, [])

    def test_play_picks_the_window_editing_the_scene(self):
        other = _Scene()
        other.name = "Other"
        self._play_window(sequencer_scene=other)
        self._play_window()
        self.assertEqual(addon._play_pause(SCENE), "play")
        self.assertEqual(PLAYER.calls, [("play", "Edit", ["area", "region", "screen", "window"])])

    def test_play_on_blender_4_2(self):
        # No sequencer scene: the window's scene is the edited one
        win = self._play_window(window_scene=SCENE)
        del win.workspace.sequencer_scene
        addon.bpy.context.sequencer_scene = None
        self.assertEqual(addon._play_pause(SCENE), "play")
        self.assertEqual(PLAYER.calls, [("play", "Edit", ["area", "region", "screen", "window"])])

    def test_play_pause_never_raises(self):
        self._play_window()
        PLAYER.fail = True
        self.assertIsNone(addon._play_pause(SCENE))
        PLAYER._set(True)
        self.assertIsNone(addon._play_pause(SCENE))
        PLAYER.fail = False
        old = addon.bpy.ops.screen
        addon.bpy.ops.screen = None  # no such operators at all
        try:
            self.assertIsNone(addon._play_pause(SCENE))
        finally:
            addon.bpy.ops.screen = old
        addon.bpy.context.window_manager.windows = []
        self.assertIsNone(addon._play_pause(SCENE))
        # A screen without is_animation_playing reads as not playing
        addon.bpy.context.window_manager.windows = [types.SimpleNamespace(screen=types.SimpleNamespace(areas=[]), scene=None)]
        self.assertFalse(addon._playing())
        self.assertIsNone(addon._play_pause(SCENE))

    def test_play_makes_no_undo_step(self):
        self._play_window()
        addon._apply([{"type": "play_pause"}], PREFS)
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertEqual(UNDO, [])

    def _seek_scene(self):
        scene = _SeekScene()
        scene.sequence_editor = None
        scene.frame_current = 100
        scene.log.clear()
        addon.bpy.context.scene = scene
        return scene

    def test_scrub_while_playing_moves_the_sound_too(self):
        # With Sync to Audio, frame_set alone is undone by the next playback
        # step: frame_current is assigned first, which moves the sound
        scene = self._seek_scene()
        self._play_window(sequencer_scene=scene)
        addon.bpy.context.sequencer_scene = scene
        PLAYER._set(True)
        PREFS.clamp = False
        try:
            addon._apply([{"type": "scrub", "frames": 30}], PREFS)
        finally:
            PREFS.clamp = True
        self.assertEqual(scene.log, [("assign", 130), ("frame_set", 130)])
        self.assertTrue(addon._playing(), "scrubbing doesn't stop playback")

    def test_scrub_while_paused_unchanged(self):
        scene = self._seek_scene()
        self._play_window(sequencer_scene=scene)
        addon.bpy.context.sequencer_scene = scene
        PREFS.clamp = False
        try:
            addon._apply([{"type": "scrub", "frames": -30}], PREFS)
        finally:
            PREFS.clamp = True
        self.assertEqual(scene.log, [("frame_set", 70)])

    def test_scrub_while_playing_selection_follows_before_cut(self):
        # Play, scrub and cut in one read: the frame-change handler still
        # runs at once, so the cut takes the strips under the new playhead
        ed = self._editor()
        self._play_window()
        PREFS.follow_selection = True
        FRAME_HANDLERS.append(addon.on_frame_change)
        addon._apply([{"type": "play_pause"}, {"type": "scrub", "frames": 20}, {"type": "cut"}], PREFS)
        self.assertTrue(addon._playing())
        self.assertEqual(SCENE.frame_current, 70)
        self.assertEqual(sorted(n for n, f, _ in ed.log), ["A", "C", "D"])

    def test_ripple_delete_while_playing_moves_the_sound_too(self):
        scene = self._seek_scene()
        ed = _Editor()
        ed.add("V1", 1, 1, 50)
        ed.add("V2", 1, 50, 100)
        ed.add("V3", 1, 100, 200)
        scene.sequence_editor = ed
        scene.frame_current = 70
        scene.log.clear()
        self._play_window(sequencer_scene=scene)
        addon.bpy.context.sequencer_scene = scene
        PLAYER._set(True)
        addon._apply([{"type": "ripple_delete"}], PREFS)
        self.assertEqual(ed.spans(), [("V1", 1, 50), ("V3", 50, 150)])
        self.assertEqual(scene.log, [("assign", 50), ("frame_set", 50)])

    def test_play_pause_over_the_link(self):
        self._play_window()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        read_lines(conn)
        try:
            conn.sendall(b'{"type":"play_pause"}\n')
            self.assertTrue(tick_until(lambda: addon._playing()))
            conn.sendall(b'{"type":"play_pause"}\n')
            self.assertTrue(tick_until(lambda: not addon._playing()))
            self.assertEqual([c[0] for c in PLAYER.calls], ["play", "cancel"])
            self.assertIsNotNone(addon.link.sock, "link kept")
        finally:
            conn.close()
            bridge.close()

    def _clock(self):
        """Puts the add-on on a clock the test moves: returns a function
        that advances it by seconds."""
        now = [1000.0]
        old = addon.time
        addon.time = types.SimpleNamespace(monotonic=lambda: now[0])
        self.addCleanup(setattr, addon, "time", old)

        def advance(seconds):
            now[0] += seconds

        return advance

    def _shuttle_scene(self):
        """The edited scene at 24 fps, frames 1 to 1000, playhead on 100,
        shown in a video editor."""
        self._play_window()
        SCENE.frame_start, SCENE.frame_end = 1, 1000
        SCENE.frame_current = 100
        self.addCleanup(setattr, SCENE, "frame_end", 100)
        return SCENE

    def test_backwards_then_faster(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        self.assertEqual(addon.shuttle.speed, -100)
        self.assertEqual(PLAYER.calls, [], "backwards is the add-on's own playback")
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 76)  # 24 frames back in 1 s
        # Two more presses: 1.5x backwards, from where it is now
        addon._apply([{"type": "shuttle", "clicks": -1}] * 2, PREFS)
        self.assertEqual(addon.shuttle.speed, -150)
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 40)  # 36 frames back from 76

    def test_forward_is_blenders_own_then_faster(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)
        # 1x forward: Blender's playback, with sound
        self.assertEqual([c[0] for c in PLAYER.calls], ["play"])
        self.assertEqual(addon.shuttle.speed, 0)
        self.assertTrue(addon._playing())
        scene.frame_current = 130  # where Blender's playback got to
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)
        # 1.25x: Blender's paused and the add-on drives on from there
        self.assertEqual(PLAYER.calls[1], ("cancel", False))
        self.assertFalse(addon._playing())
        self.assertEqual(addon.shuttle.speed, 125)
        advance(2.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 130 + 60)

    def test_other_way_eases_off_then_switches(self):
        self._clock()
        self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}] * 3, PREFS)
        self.assertEqual(addon.shuttle.speed, -150)
        # The forward button eases off: 1.25x, then 1x backwards
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)
        self.assertEqual(addon.shuttle.speed, -125)
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)
        self.assertEqual(addon.shuttle.speed, -100)
        self.assertEqual(PLAYER.calls, [])
        # At 1x it switches: forward at 1x is Blender's own playback
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)
        self.assertEqual(addon.shuttle.speed, 0)
        self.assertEqual([c[0] for c in PLAYER.calls], ["play"])
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)
        self.assertEqual(addon.shuttle.speed, 150)
        # Easing off forward lands on 1x, Blender's own, with sound
        addon._apply([{"type": "shuttle", "clicks": -1}] * 2, PREFS)
        self.assertEqual(addon.shuttle.speed, 0)
        self.assertTrue(addon._playing())
        self.assertEqual([c[0] for c in PLAYER.calls], ["play", "cancel", "play"])
        # And from Blender's 1x forward, backwards switches
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        self.assertEqual(addon.shuttle.speed, -100)
        self.assertFalse(addon._playing())

    def test_play_pause_stops_and_resets(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}] * 4, PREFS)
        advance(0.5)
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertEqual(addon.shuttle.speed, 0)
        stopped = scene.frame_current
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, stopped, "paused where it was")
        self.assertEqual(PLAYER.calls, [], "nothing of Blender's to pause")
        # The next play is Blender's own, at normal speed
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertEqual([c[0] for c in PLAYER.calls], ["play"])
        # And forward from there is 1.25x, not faster
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)
        self.assertEqual(addon.shuttle.speed, 125)

    def test_stops_at_the_scene_ends(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(60.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 1)
        self.assertEqual(addon.shuttle.speed, 0)
        scene.frame_current = 990
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)  # 1x (Blender's), then 1.25x
        advance(60.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 1000)
        self.assertEqual(addon.shuttle.speed, 0)

    def test_stops_at_the_preview_range(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        scene.use_preview_range = True
        scene.frame_preview_start, scene.frame_preview_end = 80, 200
        self.addCleanup(delattr, scene, "use_preview_range")
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(60.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 80)
        self.assertEqual(addon.shuttle.speed, 0)

    def _cache_window(self, scene, marker):
        """Gives scene a custom property cache_window, as the Cache Window
        extension publishes it while it holds the preview range."""
        props = {"cache_window": marker}
        scene.get = props.get
        self.addCleanup(delattr, scene, "get")

    def test_passes_over_a_range_cache_window_borrowed(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        scene.use_preview_range = True
        # Cache Window's window around the playhead (on 100)
        scene.frame_preview_start, scene.frame_preview_end = 60, 140
        self.addCleanup(delattr, scene, "use_preview_range")
        self._cache_window(scene, {"start": 60, "end": 140, "user_start": 1, "user_end": 250})
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(60.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 1, "the scene's start, not the window's")
        self.assertEqual(addon.shuttle.speed, 0)

    def test_keeps_the_users_range_with_cache_window_idle(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        scene.use_preview_range = True
        scene.frame_preview_start, scene.frame_preview_end = 80, 200
        self.addCleanup(delattr, scene, "use_preview_range")
        self._cache_window(scene, None)
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(60.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 80)

    def test_a_marker_that_doesnt_match_is_the_users_range(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        scene.use_preview_range = True
        scene.frame_preview_start, scene.frame_preview_end = 80, 200
        self.addCleanup(delattr, scene, "use_preview_range")
        # Left over from before the user set their own range
        self._cache_window(scene, {"start": 380, "end": 420, "user_start": 1, "user_end": 1000})
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(60.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 80)

    def test_a_marker_that_cant_be_read_is_the_users_range(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        scene.use_preview_range = True
        scene.frame_preview_start, scene.frame_preview_end = 80, 200
        self.addCleanup(delattr, scene, "use_preview_range")
        self._cache_window(scene, {"start": "soon"})
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(60.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 80)

    def test_a_render_stops_shuttling(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        before = scene.frame_current
        old = getattr(addon.bpy.app, "is_job_running", None)
        self.addCleanup(setattr, addon.bpy.app, "is_job_running", old)
        addon.bpy.app.is_job_running = lambda job: job == "RENDER"
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, before)
        self.assertEqual(addon.shuttle.speed, 0)

    def test_scrub_while_shuttling_carries_on_from_there(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        addon._apply([{"type": "scrub", "frames": 300}], PREFS)
        self.assertEqual(scene.frame_current, 400)
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 376)
        self.assertEqual(addon.shuttle.speed, -100)

    def test_selection_follows_while_shuttling(self):
        advance = self._clock()
        ed = self._editor()  # A, C cross 50; D starts at 60
        self._play_window()
        SCENE.frame_end = 1000
        self.addCleanup(setattr, SCENE, "frame_end", 100)
        SCENE.frame_current = 70
        PREFS.follow_selection = True
        FRAME_HANDLERS.append(addon.on_frame_change)
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(0.5)  # 12 frames back: 58, before D
        addon._apply([], PREFS)
        self.assertEqual(SCENE.frame_current, 58)
        self.assertEqual(sorted(s.name for s in ed.strips if s.select), ["A", "C"])

    def test_undo_stops_it(self):
        advance = self._clock()
        self._shuttle_scene()
        win = addon.bpy.context.window_manager.windows[0]
        win.screen.areas.append(_area("SEQUENCE_EDITOR", "SEQUENCER", [_region("undo-strips")]))
        ED_UNDO.left = 5
        scene = _Scene()
        scene.frame_current = 50
        scene.frame_end = 1000
        addon.bpy.context.scene = scene
        win.workspace.sequencer_scene = scene
        addon.bpy.context.sequencer_scene = None
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        self.assertEqual(addon.shuttle.speed, -100)
        addon._apply([{"type": "undo"}], PREFS)
        self.assertEqual(addon.shuttle.speed, 0)
        now = addon.bpy.context.scene
        before = now.frame_current
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(now.frame_current, before)

    def test_blenders_own_playback_takes_over(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        PLAYER._set(True)  # Space pressed in Blender
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(addon.shuttle.speed, 0)
        self.assertEqual(scene.frame_current, 100, "the add-on stopped driving")

    def test_another_scene_stops_it(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        scene.name = "Other"
        self.addCleanup(setattr, scene, "name", "Edit")
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(addon.shuttle.speed, 0)
        self.assertEqual(scene.frame_current, 100)

    def test_close_stops_it(self):
        self._clock()
        self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        addon._close("Bridge went away")
        self.assertEqual(addon.shuttle.speed, 0)

    def test_forward_without_blender_playback_drives_itself(self):
        # No window shows the scene, so Blender's playback can't start:
        # 1x forward is the add-on's
        advance = self._clock()
        self._play_window(editor=False)
        SCENE.frame_end = 1000
        self.addCleanup(setattr, SCENE, "frame_end", 100)
        SCENE.frame_current = 100
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)
        self.assertEqual(addon.shuttle.speed, 100)
        self.assertEqual(PLAYER.calls, [])
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(SCENE.frame_current, 124)

    def test_shuttle_never_raises(self):
        self._clock()
        self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)  # Blender's own
        PLAYER.fail = True  # its pause fails
        self.assertEqual(addon._shuttle(SCENE, 1), 0)
        self.assertEqual(addon.shuttle.speed, 0)
        PLAYER.fail = False
        PLAYER._set(False)
        addon._shuttle(SCENE, -1)
        addon.bpy.context.scene = _Freed()  # the scene went away under it
        addon._shuttle_tick(addon.bpy.context.scene)
        self.assertEqual(addon.shuttle.speed, 0)

    def _fake_aud(self):
        """A fake aud module: each play is recorded as (path, [ops], handle)."""
        plays = []

        class Sound:
            def __init__(self, path, ops=()):
                self.path, self.ops = path, list(ops)

            def _then(self, *op):
                return Sound(self.path, self.ops + [op])

            def limit(self, a, b):
                return self._then("limit", a, b)

            def timeStretchPitchScale(self, stretch, pitch):
                return self._then("stretch", stretch, pitch)

            def volume(self, v):
                return self._then("volume", v)

            def delay(self, d):
                return self._then("delay", d)

        class Handle:
            status = 1

            def stop(self):
                self.status = 0

        def play(sound):
            h = Handle()
            plays.append((sound.path, sound.ops, h))
            return h

        aud = types.ModuleType("aud")
        aud.Sound = Sound
        aud.Device = lambda: types.SimpleNamespace(play=play)
        aud.STATUS_PLAYING, aud.STATUS_PAUSED, aud.STATUS_STOPPED = 1, 2, 3
        self.addCleanup(sys.modules.pop, "aud", None)
        sys.modules["aud"] = aud
        addon.bpy.path = types.SimpleNamespace(abspath=lambda p: p.replace("//", "C:/edit/"))
        return plays

    def _sound(self, name, start, end, content_start, offset=0.0, mute=False, channel=1, packed=False):
        return types.SimpleNamespace(
            type="SOUND", name=name, mute=mute, channel=channel, volume=0.5,
            frame_final_start=start, frame_final_end=end, frame_start=content_start, sound_offset=offset,
            sound=types.SimpleNamespace(filepath="//%s.wav" % name, packed_file=object() if packed else None),
            as_pointer=lambda: id(name),
        )

    def test_faster_forward_has_sound_at_its_pitch(self):
        plays = self._fake_aud()
        advance = self._clock()
        scene = self._shuttle_scene()  # 24 fps, playhead on 100
        muted = types.SimpleNamespace(mute=True)
        scene.sequence_editor = types.SimpleNamespace(
            strips=[
                self._sound("voice", 50, 400, 20, offset=1.0),
                self._sound("music", 130, 400, 130),  # heard 0.625 s in at 2x
                self._sound("later", 300, 400, 300),  # too far ahead for now
                self._sound("off", 50, 400, 50, mute=True),
                self._sound("muted-channel", 50, 400, 50, channel=2),
                self._sound("packed", 50, 400, 50, packed=True),
            ],
            channels={1: types.SimpleNamespace(mute=False), 2: muted},
        )
        addon._apply([{"type": "shuttle", "clicks": 1}] * 3, PREFS)  # 1x (Blender's), then up to 1.5x
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)  # 2x
        self.assertEqual(addon.shuttle.speed, 200)
        self.assertTrue(all(h.status == 0 for _, _, h in plays), "the 1.5x sounds stopped")
        del plays[:]
        addon._apply([], PREFS)
        self.assertEqual([p[0] for p in plays], ["C:/edit/voice.wav", "C:/edit/music.wav"])
        self.assertEqual(plays[0][1], [("limit", 80 / 24 + 1.0, 380 / 24 + 1.0), ("stretch", 0.5, 1.0), ("volume", 0.5)])
        self.assertEqual(plays[1][1][-1], ("delay", 30 / 48))
        # Later strips start once they are near (frame 220 is 2 s from 316
        # at 2x); nothing plays twice
        advance(2.5)
        addon._apply([], PREFS)
        self.assertEqual([p[0] for p in plays][2:], ["C:/edit/later.wav"])
        # Pausing stops every sound
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertTrue(all(h.status == 0 for _, _, h in plays))

    def test_speed_change_restarts_the_sound(self):
        plays = self._fake_aud()
        self._clock()
        scene = self._shuttle_scene()
        scene.sequence_editor = types.SimpleNamespace(strips=[self._sound("voice", 50, 400, 50)], channels=None)
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)  # 1.25x
        addon._apply([], PREFS)
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)  # 1.5x
        addon._apply([], PREFS)
        self.assertEqual(len(plays), 2)
        self.assertEqual(plays[0][2].status, 0, "the 1.25x sound stopped")
        self.assertEqual(plays[1][1][1], ("stretch", 100 / 150, 1.0))

    def test_an_edit_restarts_the_sound(self):
        # A ripple delete or cut changes what is heard, even when the
        # playhead doesn't move: the sound starts again from the strips as
        # they are now
        plays = self._fake_aud()
        self._clock()
        scene = self._shuttle_scene()
        scene.sequence_editor = types.SimpleNamespace(strips=[self._sound("voice", 50, 400, 50)], channels=None)
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)
        addon._apply([], PREFS)
        self.assertEqual(len(plays), 1)
        scene.sequence_editor.strips = []  # the strip went away
        for edit in ("ripple_delete", "cut"):
            addon._apply([{"type": edit}], PREFS)
            addon._apply([], PREFS)
        self.assertEqual(plays[0][2].status, 0, "the deleted strip's sound stopped")
        self.assertEqual(len(plays), 1, "and nothing replaced it")
        self.assertEqual(addon.shuttle.speed, 125, "playback carries on")

    def test_blender_5_strip_names(self):
        # Blender 5 calls the ends left_handle / right_handle and where the
        # content starts content_start
        strip = types.SimpleNamespace(
            type="SOUND", name="v", mute=False, channel=1, volume=1.0, left_handle=40, right_handle=90,
            content_start=10, sound_offset=0.5, sound=types.SimpleNamespace(filepath="v.wav", packed_file=None),
            as_pointer=lambda: 1,
        )
        SCENE.sequence_editor = types.SimpleNamespace(strips=[strip], channels=None)
        addon.bpy.path = types.SimpleNamespace(abspath=lambda p: p)
        [(span, _, _)] = addon._sound_strips(SCENE)
        self.assertEqual(span[1:], (40, 90, 10, 0.5))

    def test_meta_volume_counts(self):
        meta = types.SimpleNamespace(type="META", mute=False, channel=1, volume=0.5, channels=None,
                                     strips=[self._sound("voice", 50, 400, 50)])
        SCENE.sequence_editor = types.SimpleNamespace(strips=[meta], channels=None)
        addon.bpy.path = types.SimpleNamespace(abspath=lambda p: p)
        [(_, _, volume)] = addon._sound_strips(SCENE)
        self.assertEqual(volume, 0.25)

    def test_jump_while_shuttling_picks_up_the_sound(self):
        # A jump moves the playhead: playback and its sound carry on from
        # the cut it landed on
        plays = self._fake_aud()
        advance = self._clock()
        scene = self._shuttle_scene()  # 24 fps, playhead on 100
        scene.sequence_editor = types.SimpleNamespace(
            strips=[self._sound("A", 50, 200, 50), self._sound("B", 200, 600, 200)], channels=None, meta_stack=[]
        )
        addon._apply([{"type": "shuttle", "clicks": 1}] * 5, PREFS)  # 2x
        addon._apply([], PREFS)
        self.assertEqual([p[0] for p in plays], ["C:/edit/A.wav"])
        addon._apply([{"type": "jump", "clicks": 1, "sound": True}], PREFS)  # to the cut on 200
        self.assertEqual(scene.frame_current, 200)
        addon._apply([], PREFS)
        self.assertEqual(plays[0][2].status, 0, "A's sound stopped")
        self.assertEqual(plays[1][0], "C:/edit/B.wav")
        self.assertEqual(plays[1][1][0], ("limit", 0.0, 400 / 24))
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 248)
        self.assertEqual(addon.shuttle.speed, 200)

    def test_backwards_and_past_5x_are_silent(self):
        plays = self._fake_aud()
        self._clock()
        scene = self._shuttle_scene()
        scene.sequence_editor = types.SimpleNamespace(strips=[self._sound("voice", 1, 1000, 1)], channels=None)
        addon._apply([{"type": "shuttle", "clicks": -1}] * 3, PREFS)
        addon._apply([], PREFS)
        self.assertEqual(plays, [])
        addon._shuttle_stop()
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)
        addon._apply([], PREFS)
        self.assertEqual(len(plays), 1)
        addon._apply([{"type": "shuttle", "clicks": 1}] * 8, PREFS)
        addon._apply([{"type": "shuttle", "clicks": 1}] * 7, PREFS)  # 5x: still sound
        addon._apply([], PREFS)
        self.assertEqual(addon.shuttle.speed, 500)
        at_5x = len(plays)
        self.assertEqual(plays[-1][2].status, 1)
        addon._apply([{"type": "shuttle", "clicks": 1}], PREFS)  # 5.25x
        addon._apply([], PREFS)
        addon._apply([], PREFS)
        self.assertEqual(addon.shuttle.speed, 525)
        self.assertEqual(plays[-1][2].status, 0, "past 5x the sound stops")
        self.assertEqual(len(plays), at_5x, "and none starts")

    def test_sound_that_fails_plays_on_silent(self):
        plays = self._fake_aud()
        advance = self._clock()
        scene = self._shuttle_scene()
        scene.sequence_editor = types.SimpleNamespace(strips=[self._sound("voice", 50, 400, 50)], channels=None)
        sys.modules["aud"].Device = lambda: (_ for _ in ()).throw(RuntimeError("no audio device"))
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)
        advance(1.0)
        addon._apply([], PREFS)
        addon._apply([], PREFS)
        self.assertEqual(addon.shuttle.speed, 125, "playback carries on")
        self.assertEqual(scene.frame_current, 100 + 30)
        self.assertEqual(plays, [])

    def test_shuttle_makes_no_undo_step(self):
        advance = self._clock()
        self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}] * 3, PREFS)
        advance(1.0)
        addon._apply([{"type": "shuttle", "clicks": 1}] * 3, PREFS)
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertEqual(UNDO, [])

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


    # ---------- the jump knob ----------

    def _jump_editor(self):
        """Video cut at 50 and 100, audio cut at 70, a title 150..180."""
        ed = _Editor()
        ed.add("V1", 1, 1, 50)
        ed.add("V2", 1, 50, 100)
        ed.add("V3", 1, 100, 200)
        ed.add("A1", 2, 1, 70, type="SOUND")
        ed.add("A2", 2, 70, 200, type="SOUND")
        ed.add("T", 3, 150, 180, type="TEXT")
        SCENE.sequence_editor = ed
        SCENE.frame_current = 60
        return ed

    def test_jump_right_and_left(self):
        # The knob alone stops where the picture changes: the audio cut on
        # 70 is skipped, the title's ends count (decision 20261115s2rt)
        self._jump_editor()
        for clicks, want in ((1, 100), (1, 150), (-1, 100), (-1, 50), (-2, 1), (-1, 1), (2, 100), (99, 200), (1, 200)):
            addon._apply([{"type": "jump", "clicks": clicks}], PREFS)
            self.assertEqual(SCENE.frame_current, want, (clicks, want))
        self.assertEqual(UNDO, [], "a jump is no edit")

    def test_jump_with_sound(self):
        # With Fn, every cut counts, sound strips' too
        self._jump_editor()
        for clicks, want in ((1, 70), (1, 100), (1, 150), (-1, 100), (-3, 1), (-1, 1), (2, 70), (99, 200), (1, 200)):
            addon._apply([{"type": "jump", "clicks": clicks, "sound": True}], PREFS)
            self.assertEqual(SCENE.frame_current, want, (clicks, want))
        # Both kinds in one read, in order
        SCENE.frame_current = 60
        addon._apply([{"type": "jump", "clicks": 1, "sound": True}, {"type": "jump", "clicks": 1}], PREFS)
        self.assertEqual(SCENE.frame_current, 100)

    def test_jump_skips_muted(self):
        ed = self._jump_editor()
        ed.strips[3].mute = True  # A1: its end at 70 no longer counts, A2's start still does
        ed.channels[3].mute = True  # the title
        J = {"type": "jump", "clicks": 1, "sound": True}
        addon._apply([J], PREFS)
        self.assertEqual(SCENE.frame_current, 70)
        ed.strips[4].mute = True  # A2 too
        addon._apply([J], PREFS)
        self.assertEqual(SCENE.frame_current, 100)
        addon._apply([J], PREFS)
        self.assertEqual(SCENE.frame_current, 200, "the title's channel is muted")

    def test_jump_inside_meta(self):
        ed = self._jump_editor()
        meta = _Owner(ed.log)
        meta.add("M1", 1, 10, 30)
        ed.meta_stack = [meta]
        SCENE.frame_current = 15
        addon._apply([{"type": "jump", "clicks": 1}], PREFS)
        self.assertEqual(SCENE.frame_current, 30)

    def test_jump_selection_follows(self):
        ed = self._jump_editor()
        PREFS.follow_selection = True
        FRAME_HANDLERS.append(addon.on_frame_change)
        addon._apply([{"type": "jump", "clicks": 1, "sound": True}], PREFS)
        self.assertEqual(SCENE.frame_current, 70)
        self.assertEqual(sorted(s.name for s in ed.strips if s.select), ["A2", "V2"])

    def test_jump_nothing_to_do(self):
        addon._apply([{"type": "jump", "clicks": 1}], PREFS)  # no sequence editor
        self.assertEqual(SCENE.frame_current, 10)
        SCENE.sequence_editor = types.SimpleNamespace(meta_stack=[])  # one it can't read
        addon._apply([{"type": "jump", "clicks": 1}], PREFS)
        self.assertEqual(SCENE.frame_current, 10)
        SCENE.sequence_editor = _Editor()  # no strips
        addon._apply([{"type": "jump", "clicks": -1}], PREFS)
        self.assertEqual(SCENE.frame_current, 10)

    def test_jump_while_playing_moves_the_sound_too(self):
        scene = self._seek_scene()
        ed = _Editor()
        ed.add("V1", 1, 1, 50)
        ed.add("V2", 1, 50, 150)
        scene.sequence_editor = ed
        scene.frame_current = 70
        scene.log.clear()
        self._play_window(sequencer_scene=scene)
        addon.bpy.context.sequencer_scene = scene
        PLAYER._set(True)
        addon._apply([{"type": "jump", "clicks": -1}], PREFS)
        self.assertEqual(scene.log, [("assign", 50), ("frame_set", 50)])

    def test_jump_over_the_link(self):
        self._jump_editor()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        read_lines(conn)
        try:
            conn.sendall(b'{"type":"jump","clicks":1}\n{"type":"jump","clicks":1}\n')
            self.assertTrue(tick_until(lambda: SCENE.frame_current == 150))
            self.assertTrue(any(m.get("type") == "state" and m.get("frame") == 150 for m in read_lines(conn)))
        finally:
            conn.close()
            bridge.close()


if __name__ == "__main__":
    unittest.main()
