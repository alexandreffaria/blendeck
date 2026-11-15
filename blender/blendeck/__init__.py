"""Blendeck: drive Blender from a MIDI controller through the Blendeck bridge.

The bridge (blendeck.exe) reads the controller and listens on localhost; this
add-on connects to it, applies the commands it sends, and reports the playhead
back so the bridge's page can show it."""

import socket

import bpy

from . import protocol

ADDON_VERSION = "0.1.0"
TICK = 0.01  # seconds between polls while connected
RETRY = 1.0  # seconds between connection attempts


class _Link:
    sock = None
    buf = None
    status = "Not connected"
    last_state = None
    next_try = 0.0
    clock = 0.0


link = _Link()


def _prefs():
    addon = bpy.context.preferences.addons.get(__package__)
    return addon.preferences if addon else None


def _scene():
    scene = getattr(bpy.context, "scene", None)
    if scene is None:
        wm = bpy.context.window_manager
        if wm and wm.windows:
            scene = wm.windows[0].scene
    return scene


def _set_status(text):
    if link.status != text:
        link.status = text
        _redraw()


def _redraw():
    wm = bpy.context.window_manager
    if not wm:
        return
    for win in wm.windows:
        for area in win.screen.areas:
            if area.type == "SEQUENCE_EDITOR":
                area.tag_redraw()


def _close(reason):
    if link.sock is not None:
        try:
            link.sock.close()
        except OSError:
            pass
    link.sock = None
    link.buf = None
    link.last_state = None
    _set_status(reason)


def _connect(prefs):
    try:
        s = socket.create_connection((prefs.host, prefs.port), timeout=0.05)
    except OSError:
        _set_status("Bridge not running")
        return
    s.setblocking(False)
    s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    link.sock = s
    link.buf = protocol.LineBuffer()
    _set_status("Connected")
    _send({"type": "hello", "blender": bpy.app.version_string, "addon": ADDON_VERSION})


def _send(msg):
    if link.sock is None:
        return
    try:
        link.sock.sendall(protocol.encode(msg))
    except BlockingIOError:
        pass  # the bridge is slow to read; a later state message catches up
    except OSError:
        _close("Bridge went away")


def _read():
    messages = []
    while link.sock is not None:
        try:
            data = link.sock.recv(65536)
        except BlockingIOError:
            break
        except OSError:
            _close("Bridge went away")
            break
        if not data:
            _close("Bridge went away")
            break
        try:
            messages.extend(link.buf.feed(data))
        except ValueError:
            _close("Bad data from bridge")
            break
    return messages


def _apply(messages, prefs):
    scene = _scene()
    if scene is None:
        return
    delta = protocol.total_scrub(messages)
    if delta:
        target = protocol.scrub_target(scene.frame_current, delta, scene.frame_start, scene.frame_end, prefs.clamp)
        if target != scene.frame_current:
            scene.frame_set(target)
    state = (scene.name, scene.frame_current, scene.frame_start, scene.frame_end, scene.render.fps / scene.render.fps_base)
    if state != link.last_state:
        link.last_state = state
        _send({"type": "state", "scene": state[0], "frame": state[1], "start": state[2], "end": state[3], "fps": state[4]})


def tick():
    prefs = _prefs()
    if prefs is None:
        return RETRY
    if not prefs.enabled:
        if link.sock is not None:
            _close("Turned off")
        _set_status("Turned off")
        return RETRY
    if link.sock is None:
        _connect(prefs)
        if link.sock is None:
            return RETRY
    try:
        _apply(_read(), prefs)
    except Exception as e:  # never let the timer die
        print("Blendeck:", e)
    return TICK


class BlendeckPreferences(bpy.types.AddonPreferences):
    bl_idname = __package__

    enabled: bpy.props.BoolProperty(name="Connect to bridge", default=True)
    host: bpy.props.StringProperty(name="Host", default="127.0.0.1")
    port: bpy.props.IntProperty(name="Port", default=7871, min=1, max=65535)
    clamp: bpy.props.BoolProperty(
        name="Stop at scene start and end",
        description="Scrubbing stops at the scene's first and last frame",
        default=True,
    )

    def draw(self, context):
        col = self.layout.column()
        col.prop(self, "enabled")
        col.prop(self, "clamp")
        row = col.row()
        row.prop(self, "host")
        row.prop(self, "port")
        col.label(text="Status: " + link.status)


class SEQUENCER_PT_blendeck(bpy.types.Panel):
    bl_label = "Blendeck"
    bl_space_type = "SEQUENCE_EDITOR"
    bl_region_type = "UI"
    bl_category = "Blendeck"

    def draw(self, context):
        prefs = _prefs()
        col = self.layout.column()
        icon = "CHECKMARK" if link.sock is not None else "ERROR"
        col.label(text=link.status, icon=icon)
        if prefs:
            col.prop(prefs, "enabled")
            col.prop(prefs, "clamp")


classes = (BlendeckPreferences, SEQUENCER_PT_blendeck)


def register():
    for c in classes:
        bpy.utils.register_class(c)
    if not bpy.app.timers.is_registered(tick):
        bpy.app.timers.register(tick, first_interval=0.5, persistent=True)


def unregister():
    if bpy.app.timers.is_registered(tick):
        bpy.app.timers.unregister(tick)
    _close("Not connected")
    for c in reversed(classes):
        bpy.utils.unregister_class(c)
