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
    scrub_audio = False  # tests that want it turn it on
    follow_selection = False  # tests that want it turn it on
    carried_over = False


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

    def view_all(self):
        """Fake sequencer.view_all: the whole edit (frames 0..1000 here) in
        view, logged as ("view_all", region name)."""
        region = self.override["region"]
        if region.name in self.fail_in:
            raise RuntimeError("Operator bpy.ops.sequencer.view_all.poll() failed, context is incorrect")
        self.calls.append(("view_all", region.name))
        region.view2d.xmin, region.view2d.xmax = 0.0, 1000.0
        return {"FINISHED"}

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

    def __init__(self, name, channel, start, end, log, level, lock=False, select=False, fail=False, type="MOVIE", head=0, tail=0):
        self.name, self.channel, self.type = name, channel, type
        self.frame_final_start, self.frame_final_end = start, end
        # Frames of content hidden before the start and after the end
        self.head, self.tail = head, tail
        self.lock, self.select, self.fail = lock, select, fail
        self.log, self.level = log, level
        self.input_1 = self.input_2 = None
        self.fixed = False  # an effect with inputs: it can't be moved itself

    def as_pointer(self):
        return id(self)

    # Blender 5 names for the handles; the add-on uses these when present
    # Like Blender 5.2's: a handle moves over the content, which stays put,
    # and can go past it (Blender doesn't stop it)
    @property
    def left_handle(self):
        return self.frame_final_start

    @left_handle.setter
    def left_handle(self, value):
        if type(value) is not int:
            raise TypeError("left_handle expected an int type")
        if self.fail:
            raise RuntimeError("handle can't move")
        self.head += value - self.frame_final_start
        self.frame_final_start = value

    @property
    def right_handle(self):
        return self.frame_final_end

    @right_handle.setter
    def right_handle(self, value):
        if type(value) is not int:
            raise TypeError("right_handle expected an int type")
        if self.fail:
            raise RuntimeError("handle can't move")
        self.tail -= value - self.frame_final_end
        self.frame_final_end = value

    @property
    def content_start(self):
        return float(self.frame_final_start - self.head)

    @content_start.setter
    def content_start(self, value):
        if self.fixed:
            raise AttributeError('attribute "content_start" is read-only')
        d = int(value) - int(self.content_start)
        self.frame_final_start += d
        self.frame_final_end += d

    @property
    def content_end(self):
        return self.frame_final_end + self.tail

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


class _Saver:
    """Fake bpy.ops.wm.save_mainfile, as a timer sees it: it needs a window
    in the context. Logs the temp_override keys of each save."""

    def __init__(self):
        self.calls = []
        self.frames = []  # the playhead when each save ran
        self.fail = False

    def __call__(self):
        kw = ZOOM.override or {}
        if not kw.get("window"):
            raise RuntimeError("Operator bpy.ops.wm.save_mainfile.poll() failed, context is incorrect")
        if self.fail:
            raise RuntimeError("Cannot open file for writing: Permission denied")
        self.calls.append(sorted(kw))
        self.frames.append(sys.modules["bpy"].context.scene.frame_current)
        return {"FINISHED"}


SAVER = _Saver()


class _SaveAs:
    """Fake bpy.ops.wm.save_as_mainfile("INVOKE_DEFAULT"), as Blender 5.2
    behaves from a timer (checked in a real 5.2): with a window in the
    context it opens the file window and returns RUNNING_MODAL, and the
    window shows in the first window's modal_operators."""

    def __init__(self):
        self.calls = []

    def __call__(self, how="EXEC_DEFAULT"):
        kw = ZOOM.override or {}
        if not kw.get("window"):
            raise RuntimeError("Operator bpy.ops.wm.save_as_mainfile.poll() failed, context is incorrect")
        self.calls.append(how)
        kw["window"].modal_operators = [types.SimpleNamespace(bl_idname="WM_OT_save_as_mainfile")]
        return {"RUNNING_MODAL"}


SAVE_AS = _SaveAs()


class _SeqDelete:
    """Fake bpy.ops.sequencer.delete("EXEC_DEFAULT", undo): like Blender
    5.2's, it deletes the selected strips of the level the editor shows (in
    the scene the override names) and their effects, after stopping the
    prefetch render. A strip that can't go makes it raise before anything
    goes. Records (names deleted, undo)."""

    def __init__(self):
        self.calls = []

    def __call__(self, context="EXEC_DEFAULT", undo=True):
        scene = ZOOM.override["sequencer_scene"]
        ed = scene.sequence_editor
        strips = (ed.meta_stack[-1] if ed.meta_stack else ed).strips
        chosen = [s for s in strips if s.select]
        if any(s.name in strips.fail for s in chosen):
            raise RuntimeError("Strip can't be removed")
        self.calls.append((sorted(s.name for s in chosen), undo))
        for s in chosen:
            if s in strips:
                _Level.remove(strips, s)  # Blender's own removal, not strips.remove()
        return {"FINISHED"}


SEQ_DELETE = _SeqDelete()


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
        preferences=types.SimpleNamespace(addons={"synergy": types.SimpleNamespace(preferences=prefs)}),
        scene=scene,
        sequencer_scene=None,
        window_manager=types.SimpleNamespace(windows=[]),
        temp_override=ZOOM.temp_override,
    )
    bpy.ops = types.SimpleNamespace(
        view2d=types.SimpleNamespace(zoom_in=ZOOM.op("in"), zoom_out=ZOOM.op("out"), pan=ZOOM.pan),
        ed=types.SimpleNamespace(undo_push=lambda message="": UNDO.append(message) or {"FINISHED"}, undo=ED_UNDO),
        screen=types.SimpleNamespace(animation_play=PLAYER.play, animation_cancel=PLAYER.cancel),
        sequencer=types.SimpleNamespace(view_all=ZOOM.view_all, delete=SEQ_DELETE),
        wm=types.SimpleNamespace(save_mainfile=SAVER, save_as_mainfile=SAVE_AS),
    )
    bpy.data = types.SimpleNamespace(filepath="")
    bpy.app = types.SimpleNamespace(version_string="5.2.2")
    bpy.types = types.SimpleNamespace(AddonPreferences=object, Panel=object)
    bpy.props = types.SimpleNamespace(BoolProperty=lambda **k: None, StringProperty=lambda **k: None, IntProperty=lambda **k: None)
    sys.modules["bpy"] = bpy
    return prefs, scene


PREFS, SCENE = _install_fake_bpy()
import synergy as addon  # noqa: E402


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
        SAVER.calls.clear()
        SAVER.frames.clear()
        SAVER.fail = False
        SAVE_AS.calls.clear()
        SEQ_DELETE.calls.clear()
        addon.bpy.data.filepath = ""
        addon.link.note = ""
        addon._shuttle_stop()
        addon.bpy.context.sequencer_scene = None
        PREFS.follow_selection = False
        PREFS.scrub_audio = False
        addon._scrub_stop()
        addon.scrub.error = None
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

    def test_pan_every_timeline(self):
        self._screen()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        regions = addon.bpy.context.window_manager.windows[0].screen.areas
        strips, both = regions[0].regions[1], regions[1].regions[1]
        assert (strips.name, both.name) == ("strips", "both")
        # 5% right, 15% left and 5% right in one read: 5% left in all, on
        # every timeline; the playhead stays
        conn.sendall(b'{"type":"pan","clicks":5}\n{"type":"pan","clicks":-15}\n{"type":"pan","clicks":5}\n')
        self.assertTrue(tick_until(lambda: abs(strips.view2d.xmin + 12.5) < 0.01 and abs(both.view2d.xmin + 12.5) < 0.01))
        self.assertAlmostEqual(strips.view2d.xmax - strips.view2d.xmin, 250, delta=0.01)
        self.assertEqual(ZOOM.calls.count(("pan", "strips")), 1)
        self.assertEqual(SCENE.frame_current, 10)
        conn.close()
        bridge.close()

    def test_view_all_every_timeline(self):
        self._screen()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        # Two presses in one read are one fit; the playhead stays
        conn.sendall(b'{"type":"view_all"}\n{"type":"view_all"}\n')
        self.assertTrue(tick_until(lambda: ("view_all", "both") in ZOOM.calls))
        self.assertEqual(ZOOM.calls, [("view_all", "strips"), ("view_all", "both")])
        self.assertEqual(SCENE.frame_current, 10)
        conn.close()
        bridge.close()

    def test_view_all_then_zoom_in_one_read(self):
        region = self._strips()
        SCENE.frame_current = 500
        addon._apply([{"type": "zoom", "clicks": 1}, {"type": "view_all"}], PREFS)
        # The fit comes first, so the zoom lands on the whole edit
        self.assertEqual(ZOOM.calls[0], ("view_all", "strips"))
        self.assertIn(("in", "strips"), ZOOM.calls)
        self.assertLess(region.view2d.xmax - region.view2d.xmin, 1000)

    def test_view_all_failure_skips_that_editor(self):
        self._screen()
        ZOOM.fail_in.add("strips")
        addon._view_all()  # must not raise
        self.assertEqual(ZOOM.calls, [("view_all", "both")])

    def test_save_on_press(self):
        self._undo_scene()
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        # Two presses in one read are one save
        conn.sendall(b'{"type":"save"}\n{"type":"save"}\n')
        self.assertTrue(tick_until(lambda: SAVER.calls))
        self.assertEqual(SAVER.calls, [["area", "region", "screen", "window"]])
        self.assertTrue(addon.link.note.startswith("Saved at "))
        conn.close()
        bridge.close()

    def test_save_after_edits_in_same_read(self):
        self._undo_scene()
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        conn.sendall(b'{"type":"save"}\n{"type":"scrub","frames":5}\n')
        self.assertTrue(tick_until(lambda: SAVER.calls))
        # The scrub landed before the file was written
        self.assertEqual(SAVER.frames, [55])
        conn.close()
        bridge.close()

    def test_save_untitled_opens_save_as(self):
        self._undo_scene()
        self.assertFalse(addon._save())
        self.assertEqual(SAVER.calls, [])
        self.assertEqual(SAVE_AS.calls, ["INVOKE_DEFAULT"])
        self.assertEqual(addon.link.note, addon.SAVE_AS)
        # Still open: the note stays, and another press opens no second one
        addon._save_as_done()
        self.assertEqual(addon.link.note, addon.SAVE_AS)
        self.assertFalse(addon._save())
        self.assertEqual(SAVE_AS.calls, ["INVOKE_DEFAULT"])
        self.assertEqual(addon.link.note, addon.SAVE_AS)

    def test_save_as_then_saved(self):
        self._undo_scene()
        addon._save()
        win = addon.bpy.context.window_manager.windows[0]
        win.modal_operators = []
        addon.bpy.data.filepath = "C:/edits/new.blend"
        addon._save_as_done()
        self.assertTrue(addon.link.note.startswith("Saved at "))

    def test_save_while_own_save_as_open_on_saved_file(self):
        # You opened Save As yourself on a file saved before, then pressed save
        self._undo_scene()
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        win = addon.bpy.context.window_manager.windows[0]
        win.modal_operators = [types.SimpleNamespace(bl_idname="WM_OT_save_as_mainfile")]
        self.assertFalse(addon._save())
        self.assertEqual(SAVER.calls, [])
        self.assertEqual(addon.link.note, "Not saved: the Save As window is open")
        # Cancelling it then doesn't claim a save
        win.modal_operators = []
        addon._save_as_done()
        self.assertEqual(addon.link.note, "Not saved: the Save As window is open")

    def test_save_as_cancelled(self):
        self._undo_scene()
        addon._save()
        addon.bpy.context.window_manager.windows[0].modal_operators = []
        addon._save_as_done()
        self.assertEqual(addon.link.note, "Not saved: Save As was cancelled")

    def test_save_untitled_on_press(self):
        self._undo_scene()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        conn.sendall(b'{"type":"save"}\n')
        self.assertTrue(tick_until(lambda: SAVE_AS.calls))
        for _ in range(3):
            addon.tick()
        self.assertEqual(SAVE_AS.calls, ["INVOKE_DEFAULT"])
        self.assertEqual(addon.link.note, addon.SAVE_AS)
        conn.close()
        bridge.close()

    def test_save_untitled_refused_while_rendering(self):
        self._undo_scene()
        addon.bpy.context.window_manager.is_interface_locked = True
        self.assertFalse(addon._save())
        self.assertEqual(SAVE_AS.calls, [])

    def test_save_refused_during_modal_operator(self):
        self._undo_scene()
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        addon.bpy.context.window_manager.windows[0].modal_operators = [types.SimpleNamespace(bl_idname="TRANSFORM_OT_seq_slide")]
        self.assertFalse(addon._save())
        self.assertEqual(SAVER.calls, [])
        self.assertIn("TRANSFORM_OT_seq_slide", addon.link.note)

    def test_save_refused_while_rendering(self):
        self._undo_scene()
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        addon.bpy.context.window_manager.is_interface_locked = True
        self.assertFalse(addon._save())
        self.assertEqual(SAVER.calls, [])

    def test_save_without_window(self):
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        addon.bpy.context.window_manager.windows = [types.SimpleNamespace(screen=None)]
        self.assertFalse(addon._save())
        self.assertEqual(SAVER.calls, [])

    def test_save_failure_doesnt_raise(self):
        self._undo_scene()
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        SAVER.fail = True
        self.assertFalse(addon._save())
        self.assertIn("Permission denied", addon.link.note)
        self.assertIsNone(ZOOM.override, "the override ended")

    def test_save_note_redraw_failure_doesnt_raise(self):
        self._undo_scene()
        addon.bpy.data.filepath = "C:/edits/trip.blend"
        # An area whose redraw fails: the save still counts, nothing raises
        area = addon.bpy.context.window_manager.windows[0].screen.areas[0]
        area.tag_redraw = lambda: 1 / 0
        self.assertTrue(addon._save())
        self.assertTrue(addon.link.note.startswith("Saved at "))

    def test_view_all_without_windows(self):
        addon.bpy.context.window_manager.windows = [types.SimpleNamespace(screen=None)]
        addon._view_all()
        self.assertEqual(ZOOM.calls, [])

    def test_pan_a_whole_view(self):
        region = self._strips()
        addon._pan(100)
        self.assertAlmostEqual(region.view2d.xmin, 250, delta=0.01)
        addon._pan(-100)
        self.assertAlmostEqual(region.view2d.xmin, 0, delta=0.01)
        self.assertEqual(zooms(), [])

    def test_pan_nothing(self):
        self._screen()
        addon._pan(0)
        self.assertEqual(ZOOM.calls, [])

    def test_pan_failure_skips_that_editor(self):
        region = self._strips()
        both = addon.bpy.context.window_manager.windows[0].screen.areas[1].regions[1]
        region.view2d = None  # a region gone stale
        addon._pan(5)  # must not raise
        self.assertAlmostEqual(both.view2d.xmin, 12.5, delta=0.01, msg="the other editor still panned")

    def test_pan_without_windows(self):
        addon.bpy.context.window_manager.windows = [types.SimpleNamespace(screen=None)]
        addon._pan(5)
        self.assertEqual(ZOOM.calls, [])

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
        self.assertEqual(len(UNDO), 30, "every cut made while Blender was busy counts")
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
        self.assertEqual(len(ED_UNDO.calls), 50, "every press counts")
        ED_UNDO.calls.clear()
        ED_UNDO.left = 200
        addon._apply([{"type": "undo"}] * (addon.protocol.MAX_PRESSES_PER_TICK + 9), PREFS)
        self.assertEqual(len(ED_UNDO.calls), addon.protocol.MAX_PRESSES_PER_TICK)

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
        self._screen()  # a window: deletes go through Blender's Delete
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
        # Blender's Delete refuses as a whole: nothing goes, nothing moves
        ed = self._ripple_editor()
        ed.strips[1].select = True  # V2
        ed.strips.fail.add("V2")
        self.assertEqual(addon._ripple_delete(SCENE), 0)
        self.assertEqual(len(ed.strips), 7)
        self.assertEqual(SCENE.frame_current, 70)
        self.assertEqual(UNDO, [])
        self.assertEqual([s.name for s in ed.strips if s.select], ["V2"], "selection given back")

    def test_ripple_delete_uses_blenders_delete(self):
        # strips.remove() frees a strip while Blender's prefetch render may
        # still read it (a crash in 5.2); Blender's Delete stops it first
        ed = self._ripple_editor()
        ed.strips.remove = lambda strip: self.fail("strips.remove() used with a window open")
        ed.strips[0].select = True  # V1, before the playhead
        ed.strips[-1].select = True  # T, after the gap
        self.assertEqual(addon._ripple_delete(SCENE), 2)
        self.assertEqual(SEQ_DELETE.calls, [(["A2", "V2"], False)], "only the clips under the playhead, no undo step of its own")
        self.assertEqual(UNDO, ["Ripple delete"])
        self.assertEqual(sorted(s.name for s in ed.strips if s.select), ["T", "V1"], "selection given back")
        self.assertEqual(ed.spans(), [("A1", 1, 50), ("A3", 50, 150), ("T", 100, 130), ("V1", 1, 50), ("V3", 50, 150)])

    def test_ripple_delete_operator_failure_doesnt_raise(self):
        ed = self._ripple_editor()
        orig = addon.bpy.ops.sequencer.delete

        def refuse(*a):
            raise RuntimeError("Operator bpy.ops.sequencer.delete.poll() failed, context is incorrect")

        addon.bpy.ops.sequencer.delete = refuse
        try:
            self.assertEqual(addon._ripple_delete(SCENE), 0)
        finally:
            addon.bpy.ops.sequencer.delete = orig
        self.assertEqual(len(ed.strips), 7)
        self.assertEqual(UNDO, [])

    def test_ripple_delete_never_deletes_in_another_scene(self):
        ed = self._ripple_editor()
        addon.bpy.context.sequencer_scene = _Scene()  # Blender edits another scene
        self.assertEqual(addon._ripple_delete(SCENE), 0)
        self.assertEqual(SEQ_DELETE.calls, [])
        self.assertEqual(len(ed.strips), 7)
        self.assertEqual(UNDO, [])

    def test_ripple_delete_without_a_window(self):
        # No window, nothing draws, no prefetch: strips go one by one, and
        # one that can't go stops the gap closing
        ed = self._ripple_editor()
        addon.bpy.context.window_manager.windows = []
        ed.strips.fail.add("A2")
        self.assertEqual(addon._ripple_delete(SCENE), 1)
        self.assertEqual(SEQ_DELETE.calls, [])
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
        self.assertEqual(len(UNDO), 30, "every ripple delete made while Blender was busy counts")
        self.assertEqual(len(ed.strips), 10)

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

    def test_scrub_while_playing_stops_it_there(self):
        # The scrub knob takes the playhead over: playback pauses, and the
        # playhead stays on the frame the knob left it on
        scene = self._seek_scene()
        self._play_window(sequencer_scene=scene)
        addon.bpy.context.sequencer_scene = scene
        PLAYER._set(True)
        PREFS.clamp = False
        try:
            addon._apply([{"type": "scrub", "frames": 30}], PREFS)
        finally:
            PREFS.clamp = True
        self.assertEqual(PLAYER.calls, [("cancel", False)])
        self.assertFalse(addon._playing())
        self.assertEqual(scene.log, [("frame_set", 130)])

    def test_scrub_against_the_end_still_stops_playback(self):
        # A click that can't move the playhead (the clamp at the last
        # frame) still means the knob took over
        self._play_window()
        SCENE.frame_current = SCENE.frame_end
        PLAYER._set(True)
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)
        self.assertFalse(addon._playing())
        self.assertEqual(SCENE.frame_current, SCENE.frame_end)

    def test_scrub_while_playing_that_cant_pause_moves_the_sound_too(self):
        # If Blender's playback can't be paused it plays on, and the scrub
        # still lands. With Sync to Audio, frame_set alone is undone by the
        # next playback step: frame_current is assigned first, which moves
        # the sound
        scene = self._seek_scene()
        self._play_window(sequencer_scene=scene)
        addon.bpy.context.sequencer_scene = scene
        PLAYER._set(True)
        PLAYER.fail = True
        PREFS.clamp = False
        try:
            addon._apply([{"type": "scrub", "frames": 30}], PREFS)
        finally:
            PREFS.clamp = True
        self.assertEqual(scene.log, [("assign", 130), ("frame_set", 130)])
        self.assertTrue(addon._playing())

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
        # Play, scrub and cut in one read: the scrub pauses, the
        # frame-change handler still runs at once, so the cut takes the
        # strips under the new playhead
        ed = self._editor()
        self._play_window()
        PREFS.follow_selection = True
        FRAME_HANDLERS.append(addon.on_frame_change)
        addon._apply([{"type": "play_pause"}, {"type": "scrub", "frames": 20}, {"type": "cut"}], PREFS)
        self.assertFalse(addon._playing())
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

    def _strips_of(self, win):
        return [a for a in win.screen.areas if a.type == "SEQUENCE_EDITOR"][0].regions[1]

    def test_pause_recenters_on_playhead(self):
        win = self._play_window()
        region = self._strips_of(win)
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        read_lines(conn)
        try:
            conn.sendall(b'{"type":"play_pause"}\n')
            self.assertTrue(tick_until(lambda: addon._playing()))
            SCENE.frame_current = 400  # played on past the right edge of 0..250
            conn.sendall(b'{"type":"play_pause"}\n')
            self.assertTrue(tick_until(lambda: not addon._playing()))
            v = region.view2d
            self.assertLessEqual(abs(v.view_to_region(400, 0)[0] - 400), 2, "playhead centered")
            self.assertAlmostEqual(v.xmax - v.xmin, 250, delta=0.01, msg="zoom kept")
            pans = ZOOM.calls.count(("pan", "strips"))
            self.assertEqual(pans, 1)
            for _ in range(5):
                addon.tick()
            self.assertEqual(ZOOM.calls.count(("pan", "strips")), pans, "only once per stop")
        finally:
            conn.close()
            bridge.close()

    def test_stop_from_blender_recenters(self):
        win = self._play_window()
        region = self._strips_of(win)
        PLAYER._set(True)
        addon._watch_stop()
        SCENE.frame_current = 300
        self.assertEqual(ZOOM.calls, [], "no recenter while playing")
        PLAYER._set(False)  # Space in Blender
        addon._watch_stop()
        self.assertLessEqual(abs(region.view2d.view_to_region(300, 0)[0] - 400), 2)

    def test_shuttle_reaching_the_end_recenters(self):
        win = self._play_window()
        region = self._strips_of(win)
        addon.shuttle.speed = 200
        addon._watch_stop()
        SCENE.frame_current = 250
        addon._shuttle_stop()
        addon._watch_stop()
        self.assertLessEqual(abs(region.view2d.view_to_region(250, 0)[0] - 400), 2)

    def test_view_all_in_the_same_read_wins_over_recenter(self):
        self._play_window()
        PLAYER._set(True)
        addon._watch_stop()
        SCENE.frame_current = 300
        PLAYER._set(False)
        addon._watch_stop(recenter=False)
        self.assertEqual(ZOOM.calls, [], "the view-all view stays")
        self.assertFalse(addon.stopped.running, "the stop is used up")
        addon._watch_stop()
        self.assertEqual(ZOOM.calls, [])

    def test_no_recenter_without_a_stop(self):
        self._play_window()
        for _ in range(3):
            addon._watch_stop()
        self.assertEqual(ZOOM.calls, [])

    def test_recenter_failure_skips_that_editor(self):
        win = self._play_window()
        broken = self._strips_of(win)
        broken.view2d = None  # this editor can't be read
        other = self._play_window()
        region = self._strips_of(other)
        PLAYER._set(True)
        addon._watch_stop()
        SCENE.frame_current = 300
        PLAYER._set(False)
        addon._watch_stop()  # must not raise
        self.assertFalse(addon.stopped.running)
        self.assertLessEqual(abs(region.view2d.view_to_region(300, 0)[0] - 400), 2, "the other editor still recentered")

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

    def test_scrub_while_shuttling_stops_it_there(self):
        advance = self._clock()
        scene = self._shuttle_scene()
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(1.0)
        # From 100, the frame on screen: the 24 frames the clock ran on
        # since the last tick were never shown
        addon._apply([{"type": "scrub", "frames": 300}], PREFS)
        self.assertEqual(scene.frame_current, 400)
        self.assertEqual(addon.shuttle.speed, 0)
        advance(1.0)
        addon._apply([], PREFS)
        self.assertEqual(scene.frame_current, 400, "the playhead stays where the knob left it")
        # The next play is Blender's own, at normal speed
        addon._apply([{"type": "play_pause"}], PREFS)
        self.assertEqual([c[0] for c in PLAYER.calls], ["play"])

    def test_cut_while_shuttling_lands_on_the_frame_shown(self):
        # The clock has moved on since the last picture (a slow frame): the
        # cut belongs on that picture, and playback then carries on
        advance = self._clock()
        ed = self._editor()  # A, C cross 50
        self._play_window()
        SCENE.frame_end = 1000
        self.addCleanup(setattr, SCENE, "frame_end", 100)
        SCENE.frame_current = 50
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)
        advance(0.25)  # 6 frames at 24 fps, not shown yet
        addon._apply([{"type": "cut"}], PREFS)
        self.assertTrue(ed.log)
        self.assertEqual({f for _, f, _ in ed.log}, {50}, "cut where the picture was")
        self.assertEqual(SCENE.frame_current, 44, "then playback carries on by the clock")
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
        old = list(plays)
        self.assertTrue(old, "1.5x has sound from the tick of the press")
        del plays[:]
        addon._apply([{"type": "shuttle", "clicks": 1}] * 2, PREFS)  # 2x
        self.assertEqual(addon.shuttle.speed, 200)
        self.assertTrue(all(h.status == 0 for _, _, h in old), "the 1.5x sounds stopped")
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

    def test_scrub_plays_the_frames_it_moved_over(self):
        # With and without Fn: every click that moves the playhead plays
        # the frames it moved over, landing frame included, and stops the
        # last click's sound
        plays = self._fake_aud()
        PREFS.scrub_audio = True
        PREFS.clamp = False
        self.addCleanup(setattr, PREFS, "clamp", True)
        scene = self._shuttle_scene()  # 24 fps, playhead on 100
        scene.sequence_editor = types.SimpleNamespace(
            strips=[
                self._sound("voice", 50, 400, 20, offset=1.0),
                self._sound("music", 145, 400, 145),
                self._sound("off", 50, 400, 50, mute=True),
            ],
            channels=None,
        )
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)  # 100 to 110: frames 101-110
        self.assertEqual([p[0] for p in plays], ["C:/edit/voice.wav"])
        self.assertEqual(plays[0][1], [("limit", 81 / 24 + 1.0, 91 / 24 + 1.0), ("volume", 0.5)])
        addon._apply([{"type": "scrub", "frames": 1}], PREFS)  # Fn: just frame 111
        self.assertEqual(plays[0][2].status, 0, "the last click's sound stopped")
        self.assertEqual(plays[1][1][0], ("limit", 91 / 24 + 1.0, 92 / 24 + 1.0))
        scene.frame_current = 150
        addon._apply([{"type": "scrub", "frames": -10}], PREFS)  # backwards: frames 140-149
        self.assertEqual([p[0] for p in plays[2:]], ["C:/edit/voice.wav", "C:/edit/music.wav"])
        self.assertEqual(plays[2][1][0], ("limit", 120 / 24 + 1.0, 130 / 24 + 1.0))
        self.assertEqual(plays[3][1], [("limit", 0.0, 5 / 24), ("volume", 0.5), ("delay", 5 / 24)])
        # Not moving (the clamp at a range end) plays nothing
        PREFS.clamp = True
        scene.frame_current = scene.frame_end
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)
        self.assertEqual(len(plays), 4)

    def test_scrub_is_silent_turned_off_or_over_playback_it_cant_stop(self):
        plays = self._fake_aud()
        self._clock()
        scene = self._shuttle_scene()
        scene.sequence_editor = types.SimpleNamespace(strips=[self._sound("voice", 1, 1000, 1)], channels=None)
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)  # turned off
        self.assertEqual(plays, [])
        PREFS.scrub_audio = True
        PLAYER._set(True)  # Blender's own playback
        PLAYER.fail = True  # that can't be paused: it has its own sound
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)
        self.assertEqual(plays, [])

    def test_the_scrub_that_stops_playback_is_heard(self):
        # Playback stops first, so the click sounds like any other scrub
        plays = self._fake_aud()
        self._clock()
        scene = self._shuttle_scene()
        scene.sequence_editor = types.SimpleNamespace(strips=[self._sound("voice", 1, 1000, 1)], channels=None)
        PREFS.scrub_audio = True
        addon._apply([{"type": "shuttle", "clicks": -1}], PREFS)  # backwards: the add-on drives it
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)
        self.assertEqual(len(plays), 1)
        PLAYER._set(True)  # Blender's own playback
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)
        self.assertEqual(len(plays), 2)
        self.assertFalse(addon._playing())

    def test_scrub_without_a_sound_device_still_moves(self):
        plays = self._fake_aud()
        PREFS.scrub_audio = True
        scene = self._shuttle_scene()
        scene.sequence_editor = types.SimpleNamespace(strips=[self._sound("voice", 1, 1000, 1)], channels=None)
        sys.modules["aud"].Device = lambda: (_ for _ in ()).throw(RuntimeError("no audio device"))
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)
        addon._apply([{"type": "scrub", "frames": 10}], PREFS)
        self.assertEqual(scene.frame_current, 120)
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


    def _move_editor(self):
        """Video (1) and audio (2): clips at 1-50 and 50-100, then 120-200
        after a gap; a title (3) at 150. The middle clips are selected."""
        ed = _Editor()
        for name, ch in (("V", 1), ("A", 2)):
            ed.add(name + "1", ch, 1, 50)
            ed.add(name + "2", ch, 50, 100, select=True)
            ed.add(name + "3", ch, 120, 200)
        ed.add("T", 3, 150, 180)
        SCENE.sequence_editor = ed
        SCENE.frame_current = 70
        return ed

    def test_move_after_right_then_back_left(self):
        ed = self._move_editor()
        addon._apply([{"type": "move", "frames": 1}, {"type": "move", "frames": 4}], PREFS)
        self.assertEqual(ed.spans(), [
            ("A1", 1, 50), ("A2", 55, 105), ("A3", 125, 205),
            ("T", 155, 185), ("V1", 1, 50), ("V2", 55, 105), ("V3", 125, 205),
        ])
        self.assertEqual(SCENE.frame_current, 70, "the playhead stays")
        # Left as far as you like: they stop against V1 and A1
        addon._apply([{"type": "move", "frames": -100}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] in ("V2", "A2", "T")], [("A2", 50, 100), ("T", 150, 180), ("V2", 50, 100)])
        addon._apply([{"type": "move", "frames": -1}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] == "V2"], [("V2", 50, 100)])

    def test_move_before_stops_at_the_next_clip(self):
        ed = self._move_editor()
        addon._apply([{"type": "move", "frames": 50, "before": True}], PREFS)
        self.assertEqual(ed.spans(), [
            ("A1", 21, 70), ("A2", 70, 120), ("A3", 120, 200),
            ("T", 150, 180), ("V1", 21, 70), ("V2", 70, 120), ("V3", 120, 200),
        ])

    def test_move_skips_locked_and_unselected(self):
        ed = self._move_editor()
        ed.channels[3].lock = True  # the title stays
        addon._apply([{"type": "move", "frames": 10}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] in ("T", "V3")], [("T", 150, 180), ("V3", 130, 210)])
        for s in ed.strips:
            s.select = False
        before = ed.spans()
        addon._apply([{"type": "move", "frames": 10}], PREFS)
        self.assertEqual(ed.spans(), before, "nothing selected: nothing moves")

    def test_move_effect_follows_its_input(self):
        ed = self._move_editor()
        fx = ed.add("FX", 4, 120, 200)
        fx.input_1, fx.fixed = ed.strips[2], True  # an effect on V3
        addon._apply([{"type": "move", "frames": 10}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] in ("V3", "FX")], [("FX", 120, 200), ("V3", 130, 210)])

    def test_move_one_undo_step_per_turn(self):
        ed = self._move_editor()
        for _ in range(3):
            addon._apply([{"type": "move", "frames": 1}], PREFS)
            addon._move_undo(time.monotonic())
        self.assertEqual(UNDO, [], "the knob is still turning")
        addon._move_undo(time.monotonic() + addon.MOVE_UNDO_DELAY)
        self.assertEqual(UNDO, ["Move strips"])
        addon._move_undo(time.monotonic() + 10)
        self.assertEqual(UNDO, ["Move strips"], "one step a turn")
        # Another command ends the turn at once, before it runs
        addon._apply([{"type": "move", "frames": 1}, {"type": "cut"}], PREFS)
        self.assertEqual(UNDO, ["Move strips"] * 2 + ["Cut at playhead"])
        # A move that does nothing makes no step
        addon._apply([{"type": "move", "frames": 1, "before": True}], PREFS)
        for s in ed.strips:
            s.select = False
        addon._apply([{"type": "move", "frames": 1}], PREFS)
        addon._close("gone")
        self.assertEqual(UNDO, ["Move strips"] * 2 + ["Cut at playhead", "Move strips"], "a dropped link pushes the waiting step")

    def _trim_editor(self):
        """The owner's flow: video (1) and sound (2) clips at 50-100 and
        120-200, a gap between, each with 30 frames of footage hidden on
        either side; a color strip (3) at 120-140."""
        ed = _Editor()
        for name, ch in (("V", 1), ("A", 2)):
            ed.add(name + "1", ch, 50, 100, head=30, tail=30)
            ed.add(name + "2", ch, 120, 200, head=30, tail=30)
        ed.add("C", 3, 120, 140, type="COLOR")
        SCENE.sequence_editor = ed
        SCENE.frame_current = 120
        return ed

    def test_trim_start_into_the_gap_and_back(self):
        ed = self._trim_editor()
        addon._apply([{"type": "trim", "frames": -3}, {"type": "trim", "frames": -2}], PREFS)
        self.assertEqual(ed.spans(), [("A1", 50, 100), ("A2", 115, 200), ("C", 115, 140), ("V1", 50, 100), ("V2", 115, 200)])
        self.assertEqual(SCENE.frame_current, 115, "the playhead rides the handle")
        self.assertEqual(ed.strips[1].content_start, 90.0, "the footage stays put")
        # As far as the gap goes, then back the other way
        addon._apply([{"type": "trim", "frames": -100}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] == "V2"], [("V2", 100, 200)])
        self.assertEqual(SCENE.frame_current, 100)
        addon._apply([{"type": "trim", "frames": 4}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] in ("V2", "A2")], [("A2", 104, 200), ("V2", 104, 200)])

    def test_trim_end_and_the_cut_rolls(self):
        ed = self._trim_editor()
        SCENE.frame_current = 100
        addon._apply([{"type": "trim", "frames": 100}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] in ("V1", "A1")], [("A1", 50, 120), ("V1", 50, 120)])
        self.assertEqual(SCENE.frame_current, 120)
        # Now the clips meet: right rolls the cut as far as V1's footage
        # goes (10 more frames), and V2 / A2 / C lose as much
        addon._apply([{"type": "trim", "frames": 50}], PREFS)
        self.assertEqual(ed.spans(), [("A1", 50, 130), ("A2", 130, 200), ("C", 130, 140), ("V1", 50, 130), ("V2", 130, 200)])
        addon._apply([{"type": "trim", "frames": -5}], PREFS)
        self.assertEqual(ed.spans(), [("A1", 50, 125), ("A2", 125, 200), ("C", 125, 140), ("V1", 50, 125), ("V2", 125, 200)])

    def test_trim_shrink_at_a_join_opens_a_gap(self):
        ed = self._trim_editor()
        SCENE.frame_current = 100
        # Close the gap first: V1 / A1 end at 120 where V2 / A2 start
        addon._apply([{"type": "trim", "frames": 20}], PREFS)
        self.assertEqual(SCENE.frame_current, 120)
        # Fn right: only V2 / A2 / C (following nothing) lose their start
        addon._apply([{"type": "trim", "frames": 3, "shrink": True}], PREFS)
        self.assertEqual(ed.spans(), [("A1", 50, 120), ("A2", 123, 200), ("C", 123, 140), ("V1", 50, 120), ("V2", 123, 200)])
        self.assertEqual(SCENE.frame_current, 123, "the playhead rides the moved handles")
        # On V1 / A1's end, Fn left: only they lose frames
        SCENE.frame_current = 120
        addon._apply([{"type": "trim", "frames": -4, "shrink": True}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] in ("V1", "A1", "V2", "A2")], [("A1", 50, 116), ("A2", 123, 200), ("V1", 50, 116), ("V2", 123, 200)])
        self.assertEqual(SCENE.frame_current, 116)
        # Fn right on that lone end would grow it: nothing moves
        addon._apply([{"type": "trim", "frames": 2, "shrink": True}], PREFS)
        self.assertEqual(SCENE.frame_current, 116)
        self.assertEqual([x for x in ed.spans() if x[0] == "V1"], [("V1", 50, 116)])

    def test_trim_skips_locked_and_needs_a_handle(self):
        ed = self._trim_editor()
        ed.channels[3].lock = True
        ed.strips[3].lock = True  # A2
        addon._apply([{"type": "trim", "frames": -5}], PREFS)
        self.assertEqual(ed.spans(), [("A1", 50, 100), ("A2", 120, 200), ("C", 120, 140), ("V1", 50, 100), ("V2", 115, 200)])
        SCENE.frame_current = 110
        before = ed.spans()
        addon._apply([{"type": "trim", "frames": -5}], PREFS)
        self.assertEqual(ed.spans(), before, "no handle at the playhead")
        self.assertEqual(SCENE.frame_current, 110)

    def test_trim_failure_stops_and_doesnt_raise(self):
        ed = self._trim_editor()
        SCENE.frame_current = 100
        ed.strips[0].fail = True  # V1's handle can't move
        addon._apply([{"type": "trim", "frames": 5}], PREFS)  # must not raise
        self.assertEqual([x for x in ed.spans() if x[0] in ("V1", "A1")], [("A1", 50, 100), ("V1", 50, 100)])
        self.assertEqual(SCENE.frame_current, 100)
        SCENE.sequence_editor = types.SimpleNamespace(meta_stack=[])  # one it can't read
        addon._apply([{"type": "trim", "frames": 1}], PREFS)
        self.assertEqual(addon.link.move_undo, 0.0)

    def test_trim_failure_puts_back_what_moved(self):
        ed = self._trim_editor()
        SCENE.frame_current = 100
        ed.strips[2].fail = True  # A1's handle can't move, after V1's did
        addon._apply([{"type": "trim", "frames": 5}], PREFS)
        self.assertEqual([x for x in ed.spans() if x[0] in ("V1", "A1")], [("A1", 50, 100), ("V1", 50, 100)], "picture and sound stay in step")
        self.assertEqual(SCENE.frame_current, 100)
        self.assertEqual(addon.link.move_undo, 0.0, "nothing changed: no undo step")

    def test_trim_on_blender_4_2(self):
        # Before 4.4: "sequences" and frame_final_start/end; before 5, the
        # footage runs from frame_start for frame_duration frames
        ed = _Editor()
        ed.sequences = ed.strips
        del ed.strips

        class Old:
            type = "MOVIE"

            def __init__(self, start, end, frame_start, duration):
                self.name, self.channel, self.lock, self.select = "old", 1, False, False
                self.frame_final_start, self.frame_final_end = start, end
                self.frame_start, self.frame_duration = frame_start, duration

        old = Old(50, 100, 40, 80)  # footage 40..120
        ed.sequences.append(old)
        SCENE.sequence_editor = ed
        SCENE.frame_current = 50
        addon._apply([{"type": "trim", "frames": -100}], PREFS)
        self.assertEqual((old.frame_final_start, SCENE.frame_current), (40, 40))
        addon._apply([{"type": "trim", "frames": 1}], PREFS)
        SCENE.frame_current = 100
        addon._apply([{"type": "trim", "frames": 100}], PREFS)
        self.assertEqual((old.frame_final_start, old.frame_final_end, SCENE.frame_current), (41, 120, 120))

    def test_trim_and_move_each_get_an_undo_step(self):
        ed = self._trim_editor()
        ed.strips[1].select = True  # V2
        addon._apply([{"type": "trim", "frames": -2}], PREFS)
        addon._move_undo(time.monotonic())
        self.assertEqual(UNDO, [], "the knob is still turning")
        addon._apply([{"type": "move", "frames": 1}], PREFS)
        self.assertEqual(UNDO, ["Trim handle"], "the other knob ends the trim's turn")
        addon._move_undo(time.monotonic() + addon.MOVE_UNDO_DELAY)
        self.assertEqual(UNDO, ["Trim handle", "Move strips"])

    def test_move_nothing_to_do(self):
        addon._apply([{"type": "move", "frames": 1}], PREFS)  # no sequence editor
        SCENE.sequence_editor = types.SimpleNamespace(meta_stack=[])  # one it can't read
        addon._apply([{"type": "move", "frames": 1}], PREFS)
        self.assertEqual(addon.link.move_undo, 0.0)

    def test_move_over_the_link(self):
        ed = self._move_editor()
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        read_lines(conn)
        try:
            conn.sendall(b'{"type":"move","frames":3}\n{"type":"move","frames":2,"before":true}\n')
            self.assertTrue(tick_until(lambda: ("V3", 123, 203) in ed.spans() and ("V1", 3, 52) in ed.spans()))
            self.assertTrue(tick_until(lambda: UNDO == ["Move strips"], ticks=200))
        finally:
            conn.close()
            bridge.close()

class OldAddonTest(unittest.TestCase):
    """The add-on before the rename was the Blendeck extension, which Blender
    keeps as another add-on until it is uninstalled."""

    OLD = "bl_ext.user_default.blendeck"

    def setUp(self):
        addon._close("reset")
        self.saved = {n: getattr(PREFS, n) for n in addon.CARRIED_PREFS}
        PREFS.enabled = True
        PREFS.host = "127.0.0.1"
        PREFS.carried_over = False

    def tearDown(self):
        addon.bpy.context.preferences.addons.pop(self.OLD, None)
        addon._close("reset")
        for n, v in self.saved.items():
            setattr(PREFS, n, v)
        PREFS.carried_over = False

    def test_waits_for_the_old_addon_and_takes_its_settings(self):
        bridge = Bridge()
        self.addCleanup(bridge.close)
        PREFS.port = 1
        old = types.SimpleNamespace(
            enabled=True, host="127.0.0.1", port=bridge.port, clamp=False, scrub_audio=True, follow_selection=True
        )
        addon.bpy.context.preferences.addons[self.OLD] = types.SimpleNamespace(preferences=old)
        for _ in range(5):
            addon.tick()
        self.assertEqual(addon.link.status, addon.OLD_ADDON_ON)
        self.assertIsNone(addon.link.sock)
        self.assertIsNone(addon.link.pending)
        self.assertEqual((PREFS.port, PREFS.clamp, PREFS.scrub_audio, PREFS.follow_selection), (bridge.port, False, True, True))
        self.assertTrue(PREFS.carried_over)

        # Copied once: a change made here afterwards stays
        PREFS.clamp = True
        addon.tick()
        self.assertTrue(PREFS.clamp)

        # Once it is uninstalled this one connects, and says hello as before
        del addon.bpy.context.preferences.addons[self.OLD]
        conn = bridge.accept()
        self.addCleanup(conn.close)
        self.assertTrue(tick_until(lambda: addon.link.sock is not None))
        hello = read_lines(conn)[0]
        self.assertEqual((hello["type"], hello["addon"]), ("hello", addon.ADDON_VERSION))

    def test_without_the_old_addon_nothing_is_carried(self):
        addon.tick()
        self.assertFalse(PREFS.carried_over)
        self.assertNotEqual(addon.link.status, addon.OLD_ADDON_ON)


class MeterTest(unittest.TestCase):
    """The latency meter (_measure), on clocks the test moves."""

    def setUp(self):
        self.perf = [100.0]
        self.sent = []
        for name, value in (("_perf", lambda: self.perf[0]), ("_send", self.sent.append)):
            self.addCleanup(setattr, addon, name, getattr(addon, name))
            setattr(addon, name, value)
        addon._meter_reset()
        addon.meter.last_report = 0.0
        addon.meter.drawn = 0.0
        self.addCleanup(addon._meter_reset)

    def tick(self, messages=(), moved=False, wall=5.0):
        """One tick starting now."""
        addon._measure(list(messages), self.perf[0], wall, moved)

    def test_wait_and_picture(self):
        addon.meter.last_report = self.perf[0]  # one just went
        # Taken by the bridge at 4.990 s, applied at 5.000 s: waited 10 ms
        self.tick([{"type": "scrub", "frames": 1, "t": 4990}], moved=True)
        self.perf[0] += 0.170
        addon._on_preview_draw()  # drawn 170 ms after the tick began
        self.perf[0] += 0.008
        self.tick()
        self.assertEqual(self.sent, [], "not a second since the last report")
        self.perf[0] += 1.0
        self.tick()
        self.assertEqual(len(self.sent), 1)
        r = self.sent[0]
        self.assertEqual((r["type"], r["samples"], r["wait_max"], r["picture_max"], r["total_max"]), ("latency", 1, 10.0, 170.0, 180.0))
        self.assertEqual(r["tick_max"], 1000.0, "the longest gap between two ticks")

    def test_reports_about_once_a_second(self):
        for i in range(30):
            self.tick([{"type": "zoom", "clicks": 1, "t": 5000 - 3}])
            self.perf[0] += 0.125
        self.assertEqual(len(self.sent), 4)
        self.assertEqual(self.sent[1]["samples"], 8)
        self.assertEqual(self.sent[1]["wait_med"], 3.0)

    def test_reports_while_the_knob_keeps_turning(self):
        # Slow footage: every tick has a scrub in it and a picture still to
        # come, for 5 s. The reports must not wait for the knob to stop.
        for i in range(40):
            self.tick([{"type": "scrub", "frames": -1, "t": 5000 - 20}], moved=True)
            self.perf[0] += 0.117
            addon._on_preview_draw()
            self.perf[0] += 0.008
        self.assertEqual(len(self.sent), 5)
        for r in self.sent[1:]:
            self.assertEqual(r["samples"], 8)
            self.assertEqual((r["wait_max"], r["picture_med"], r["total_max"], r["tick_max"]), (20.0, 117.0, 137.0, 125.0))
        self.assertLessEqual(len(addon.meter.waits), 8, "samples don't pile up across reports")

    def test_the_stall_before_a_command_is_reported(self):
        for _ in range(300):  # idle for 3 s
            self.tick()
            self.perf[0] += 0.01
        self.perf[0] += 0.29  # Blender busy for 300 ms, commands waiting
        self.tick([{"type": "zoom", "clicks": 1, "t": 5000 - 250}])
        self.perf[0] += 0.01
        self.tick()
        self.assertEqual(len(self.sent), 1)
        self.assertEqual((self.sent[0]["wait_max"], self.sent[0]["tick_max"]), (250.0, 300.0))

    def test_idle_says_nothing(self):
        for _ in range(500):
            self.tick()
            self.perf[0] += 0.01
        self.assertEqual(self.sent, [])

    def test_no_preview_gives_up_on_the_picture(self):
        self.tick([{"type": "scrub", "frames": 1, "t": 4995}], moved=True)
        self.perf[0] += addon.PICTURE_WAIT + 0.1
        self.tick()
        self.assertEqual(len(self.sent), 1)
        self.assertEqual((self.sent[0]["samples"], self.sent[0]["wait_max"], self.sent[0]["total_max"]), (1, 5.0, 0.0))
        self.assertIsNone(addon.meter.pending)

    def test_a_draw_before_the_command_is_not_its_picture(self):
        addon._on_preview_draw()
        self.perf[0] += 0.05
        self.tick([{"type": "cut", "t": 4999}], moved=True)
        self.perf[0] += 0.01
        self.tick()
        self.assertEqual(self.sent, [], "still waiting for a draw after the cut")

    def test_clock_nonsense_is_not_a_sample(self):
        self.tick([{"type": "cut", "t": 9_000_000}])  # from the future
        self.tick([{"type": "cut", "t": 1}], wall=5000.0)  # over a minute old
        self.tick([{"type": "cut"}])  # an older bridge: no stamp
        self.assertEqual(addon.meter.waits, [])
        self.assertEqual(self.sent, [])

    def test_samples_are_capped(self):
        addon.meter.last_report = self.perf[0]  # no report during the flood
        for _ in range(addon.protocol.MAX_LATENCY_SAMPLES * 3):
            self.tick([{"type": "zoom", "clicks": 1, "t": 4999}])
        self.assertEqual(len(addon.meter.waits), addon.protocol.MAX_LATENCY_SAMPLES)

    def test_dropped_presses_are_reported(self):
        self.tick([{"type": "undo", "t": 4999}] * (addon.protocol.MAX_PRESSES_PER_TICK + 3))
        self.assertEqual(self.sent[0]["dropped"], 3)

    def test_never_raises(self):
        addon._measure(None, "x", None, True)  # prints, doesn't raise
        addon._perf = lambda: 1 / 0
        addon._on_preview_draw()
        addon._measure([], 1.0, 1.0, False)

    def test_preview_watched_while_registered(self):
        calls = []
        space = types.SimpleNamespace(
            draw_handler_add=lambda f, args, region, when: calls.append(("add", region, when)) or "handle",
            draw_handler_remove=lambda h, region: calls.append(("remove", h, region)),
        )
        old = addon.bpy.types
        addon.bpy.types = types.SimpleNamespace(SpaceSequenceEditor=space)
        self.addCleanup(setattr, addon.bpy, "types", old)
        addon._watch_preview()
        addon._watch_preview()  # once
        addon._unwatch_preview()
        addon._unwatch_preview()
        self.assertEqual(calls, [("add", "PREVIEW", "POST_PIXEL"), ("remove", "handle", "PREVIEW")])
        # A Blender without it: no meter picture, nothing raised
        addon.bpy.types = types.SimpleNamespace()
        addon._watch_preview()
        self.assertIsNone(addon.meter.handle)


class HintsTest(unittest.TestCase):
    """What the controls do, drawn over the timeline while the bridge says
    to show it (decision 202611167kkw)."""

    MSG = {"type": "hints", "profile": "Video editing", "show": False, "hints": [
        {"control": "D2", "name": "Scrub timeline", "held": [{"modifier": "Fn", "name": "Scrub timeline (fine)"}]},
        {"control": "C5", "name": "Cut at playhead"},
        {"control": "C6", "name": "Ripple delete"},
    ]}

    def setUp(self):
        self.redraws = 0
        self.drawn = []  # texts, in the order drawn
        self.boxes = []  # the background's corners, once per draw

        def redraw():
            self.redraws += 1

        blf = types.SimpleNamespace(
            size=lambda font, size: None,
            dimensions=lambda font, text: (7.0 * len(text), 10.0),
            color=lambda font, r, g, b, a: None,
            position=lambda font, x, y, z: self.at.append((x, y)),
            draw=lambda font, text: self.drawn.append(text),
        )
        self.at = []
        shader = types.SimpleNamespace(bind=lambda: None, uniform_float=lambda name, value: None)
        gpu = types.SimpleNamespace(
            shader=types.SimpleNamespace(from_builtin=lambda name: shader),
            state=types.SimpleNamespace(blend_set=lambda mode: None),
        )
        batch = types.SimpleNamespace(
            batch_for_shader=lambda sh, kind, data: self.boxes.append(data["pos"]) or types.SimpleNamespace(draw=lambda sh: None),
        )
        extras = types.SimpleNamespace(batch=batch)
        for name, mod in (("blf", blf), ("gpu", gpu), ("gpu_extras", extras), ("gpu_extras.batch", batch)):
            old = sys.modules.get(name)
            sys.modules[name] = mod
            self.addCleanup(sys.modules.__setitem__, name, old) if old is not None else self.addCleanup(sys.modules.pop, name, None)
        self.context = types.SimpleNamespace(
            region=types.SimpleNamespace(width=1200, height=400),
            preferences=types.SimpleNamespace(system=types.SimpleNamespace(ui_scale=1.0)),
            window_manager=None,
            scene=None,
        )
        for obj, name, value in ((addon, "_redraw", redraw), (addon.bpy, "context", self.context), (addon.link, "hints", None), (addon.hints_draw, "failed", False)):
            self.addCleanup(setattr, obj, name, getattr(obj, name))
            setattr(obj, name, value)

    def apply(self, *messages):
        addon._apply(list(messages), _Prefs())

    def test_drawn_only_while_shown(self):
        self.apply(self.MSG)
        self.assertEqual(self.redraws, 1)
        addon._draw_hints()
        self.assertEqual((self.drawn, self.boxes), ([], []), "hidden until the bridge says show")

        self.apply(dict(self.MSG, show=True))
        self.assertEqual(self.redraws, 2)
        addon._draw_hints()
        self.assertEqual(self.drawn, [
            "Video editing",
            "D2", "Scrub timeline \u00b7 Fn: fine",
            "C5", "Cut at playhead",
            "C6", "Ripple delete",
        ])
        (box,) = self.boxes
        xs, ys = [x for x, _ in box], [y for _, y in box]
        self.assertTrue(0 <= min(xs) and max(xs) <= 1200 and 0 < min(ys) and max(ys) <= 400, box)
        for x, y in self.at:
            self.assertTrue(min(xs) < x < max(xs) and min(ys) < y < max(ys), (x, y, box))

        self.apply(dict(self.MSG, show=True))  # the same again: nothing to redraw
        self.assertEqual(self.redraws, 2)
        self.apply(self.MSG)
        self.assertEqual(self.redraws, 3)
        del self.drawn[:]
        addon._draw_hints()
        self.assertEqual(self.drawn, [])

    def test_other_commands_leave_them_alone(self):
        self.apply(dict(self.MSG, show=True))
        self.apply({"type": "zoom", "clicks": 0})
        self.assertTrue(addon.link.hints[2])
        self.assertEqual(self.redraws, 1)

    def test_columns_when_the_timeline_is_short(self):
        self.context.region.height = 120  # room for the title and one row
        self.apply(dict(self.MSG, show=True))
        addon._draw_hints()
        rows = {y for _, y in self.at}
        self.assertEqual(len(rows), 2, "the title's row and one row of three columns")
        self.assertEqual(len(self.drawn), 7)

    def test_nothing_to_show(self):
        self.apply(dict(self.MSG, show=True, hints=[]))
        addon._draw_hints()
        self.assertEqual(self.boxes, [])

    def test_gone_with_the_bridge(self):
        self.apply(dict(self.MSG, show=True))
        addon._close("Bridge went away")
        self.assertIsNone(addon.link.hints)
        addon._draw_hints()
        self.assertEqual(self.drawn, [])

    def test_never_raises(self):
        self.apply(dict(self.MSG, show=True))
        sys.modules["blf"].draw = lambda font, text: 1 / 0
        printed = []
        self.addCleanup(setattr, addon, "print", print)
        addon.print = lambda *a: printed.append(a)
        addon._draw_hints()
        addon._draw_hints()
        self.assertEqual(len(printed), 1, "said once, not on every redraw")
        self.context.region = None
        addon._draw_hints()

    def test_timeline_watched_while_registered(self):
        calls = []
        space = types.SimpleNamespace(
            draw_handler_add=lambda f, args, region, when: calls.append(("add", f, region, when)) or "handle",
            draw_handler_remove=lambda h, region: calls.append(("remove", h, region)),
        )
        old = addon.bpy.types
        addon.bpy.types = types.SimpleNamespace(SpaceSequenceEditor=space)
        self.addCleanup(setattr, addon.bpy, "types", old)
        addon._watch_timeline()
        addon._watch_timeline()  # once
        addon._unwatch_timeline()
        addon._unwatch_timeline()
        self.assertEqual(calls, [("add", addon._draw_hints, "WINDOW", "POST_PIXEL"), ("remove", "handle", "WINDOW")])
        addon.bpy.types = types.SimpleNamespace()
        addon._watch_timeline()  # a Blender without it: nothing raised
        self.assertIsNone(addon.hints_draw.handle)


class SlowPictureTest(unittest.TestCase):
    """What the add-on says after a slow picture, and why."""

    def setUp(self):
        self.perf = [100.0]
        self.sent = []
        for name, value in (("_perf", lambda: self.perf[0]), ("_send", self.sent.append)):
            self.addCleanup(setattr, addon, name, getattr(addon, name))
            setattr(addon, name, value)
        addon._meter_reset()
        addon.meter.last_report = 0.0
        addon.meter.drawn = 0.0
        addon.meter.slow = []
        self.addCleanup(addon._meter_reset)
        self.addCleanup(setattr, addon.meter, "slow", [])
        # A preview that doesn't use proxies, and prefetch on
        wm = addon.bpy.context.window_manager
        old_windows = wm.windows
        self.addCleanup(setattr, wm, "windows", old_windows)
        self.space = types.SimpleNamespace(view_type="SEQUENCER_PREVIEW", use_proxies=False)
        area = types.SimpleNamespace(type="SEQUENCE_EDITOR", spaces=types.SimpleNamespace(active=self.space), regions=[], tag_redraw=lambda: None)
        self.areas = [area]
        wm.windows = [types.SimpleNamespace(screen=types.SimpleNamespace(areas=self.areas), scene=SCENE)]
        self.ed = types.SimpleNamespace(use_prefetch=True)
        old_ed = getattr(SCENE, "sequence_editor", None)
        self.addCleanup(setattr, SCENE, "sequence_editor", old_ed)
        SCENE.sequence_editor = self.ed
        old_scene = getattr(addon.bpy.context, "scene", None)
        self.addCleanup(setattr, addon.bpy.context, "scene", old_scene)
        addon.bpy.context.scene = SCENE

    def move(self, picture):
        """One command whose picture takes picture seconds, then its report."""
        addon._measure([{"type": "scrub", "frames": -1, "t": 4995}], self.perf[0], 5.0, True)
        self.perf[0] += picture
        addon._on_preview_draw()
        self.perf[0] += 1.1
        addon._measure([], self.perf[0], 5.0, False)
        return self.sent[-1]

    def test_slow_picture_names_what_is_set(self):
        report = self.move(0.430)
        self.assertEqual((report["slow"], report["causes"]), (True, ["proxies_off", "prefetch_on"]))
        self.assertEqual(addon.meter.slow, ["Slow picture: 430 ms", "Use Proxies is off in the preview", "Prefetch Frames is on"])

    def test_fast_picture_says_nothing_and_clears_the_note(self):
        self.move(0.430)
        report = self.move(0.012)
        self.assertNotIn("slow", report)
        self.assertNotIn("causes", report)
        self.assertEqual(addon.meter.slow, [])

    def test_slow_with_everything_set_right(self):
        self.space.use_proxies = True
        self.ed.use_prefetch = False
        report = self.move(0.200)
        self.assertEqual((report["slow"], report["causes"]), (True, []))
        self.assertEqual(addon.meter.slow, ["Slow picture: 200 ms"])

    def test_a_report_without_pictures_keeps_the_note(self):
        # A zoom has no picture to judge: what was said about the last one stays
        self.move(0.430)
        addon._measure([{"type": "zoom", "clicks": 1, "t": 4999}], self.perf[0], 5.0, False)
        self.perf[0] += 1.1
        addon._measure([], self.perf[0], 5.0, False)
        self.assertNotIn("slow", self.sent[-1])
        self.assertEqual(addon.meter.slow[0], "Slow picture: 430 ms")

    def test_unreadable_blender_never_raises(self):
        addon.bpy.context.window_manager.windows = [types.SimpleNamespace(screen=object())]
        report = self.move(0.430)
        self.assertTrue(report["slow"])
        addon._slow_check({})  # a report it can't read: printed, not raised

    def test_cache_window_holding_the_range_is_named(self):
        self.space.use_proxies = True
        self.ed.use_prefetch = False
        old = (SCENE.frame_preview_start, SCENE.frame_preview_end) if hasattr(SCENE, "frame_preview_start") else None
        SCENE.frame_preview_start, SCENE.frame_preview_end = 906, 1343
        SCENE.get = lambda key, default=None: {"start": 906, "end": 1343} if key == "cache_window" else default
        self.addCleanup(delattr, SCENE, "get")
        if old is None:
            self.addCleanup(delattr, SCENE, "frame_preview_end")
            self.addCleanup(delattr, SCENE, "frame_preview_start")
        else:
            self.addCleanup(setattr, SCENE, "frame_preview_end", old[1])
            self.addCleanup(setattr, SCENE, "frame_preview_start", old[0])
        self.assertEqual(self.move(0.430)["causes"], ["cache_window"])
        # A marker left behind that no longer matches the range isn't Cache Window holding it
        SCENE.frame_preview_end = 2000
        self.assertEqual(self.move(0.430)["causes"], [])

    def test_only_editors_showing_a_preview_count_for_proxies(self):
        self.ed.use_prefetch = False
        self.space.view_type = "SEQUENCER"  # a timeline only: it draws no picture
        self.assertEqual(self.move(0.430)["causes"], [])
        self.space.view_type = "PREVIEW"
        self.assertEqual(self.move(0.430)["causes"], ["proxies_off"])

    def test_other_editors_are_not_read(self):
        self.ed.use_prefetch = False
        self.space.use_proxies = True
        # A 3D viewport whose space happens to look like a preview without proxies
        other = types.SimpleNamespace(view_type="PREVIEW", use_proxies=False)
        self.areas.append(types.SimpleNamespace(type="VIEW_3D", spaces=types.SimpleNamespace(active=other), regions=[], tag_redraw=lambda: None))
        self.assertEqual(self.move(0.430)["causes"], [])

    def test_the_note_ends_by_itself(self):
        # The reasons were read when it was slow; the user may have fixed
        # them and not moved since
        redraws = []
        self.areas[0].tag_redraw = lambda: redraws.append(1)
        self.move(0.430)
        self.assertTrue(addon.meter.slow)
        self.perf[0] += addon.SLOW_KEEP - 2
        addon._measure([], self.perf[0], 5.0, False)
        self.assertTrue(addon.meter.slow)
        del redraws[:]
        self.perf[0] += 3
        addon._measure([], self.perf[0], 5.0, False)
        self.assertEqual(addon.meter.slow, [])
        self.assertTrue(redraws, "the panel is redrawn without the note")

    def test_a_new_note_redraws_the_panel(self):
        redraws = []
        self.areas[0].tag_redraw = lambda: redraws.append(1)
        self.move(0.430)
        self.assertTrue(redraws)

    def test_closing_the_link_clears_the_note(self):
        self.move(0.430)
        addon._close("Not connected")
        self.assertEqual(addon.meter.slow, [])


if __name__ == "__main__":
    unittest.main()
