"""Blendeck: drive Blender from a MIDI controller through the Blendeck bridge.

The bridge (blendeck.exe) reads the controller and listens on localhost; this
add-on connects to it, applies the commands it sends, and reports the playhead
back so the bridge's page can show it."""

import errno
import select
import socket
import time

import bpy

from . import protocol

ADDON_VERSION = "0.1.2"
TICK = 0.01  # seconds between polls while connected
RETRY = 1.0  # seconds between connection attempts
PING = 5.0  # the bridge drops an add-on that says nothing for 15 s


class _Link:
    sock = None  # connected socket
    pending = None  # socket still connecting
    pending_since = 0.0
    buf = None  # incoming lines
    out = b""  # bytes not yet written
    status = "Not connected"
    last_state = None
    last_sent = 0.0


link = _Link()
CONNECT_TIMEOUT = 1.0
MAX_OUT = 64 * 1024


def _prefs():
    addon = bpy.context.preferences.addons.get(__package__)
    return addon.preferences if addon else None


def _scene():
    # Blender 5 edits video in the workspace's sequencer scene, which can
    # differ from the window's scene
    scene = getattr(bpy.context, "sequencer_scene", None) or getattr(bpy.context, "scene", None)
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
    for sock in (link.sock, link.pending):
        if sock is not None:
            try:
                sock.close()
            except OSError:
                pass
    link.sock = None
    link.pending = None
    link.buf = None
    link.out = b""
    link.last_state = None
    _set_status(reason)


def _connect(prefs):
    """Starts a connection without blocking: on Windows a refused loopback
    connect takes ~100 ms, which would freeze Blender's UI every retry."""
    host = protocol.bridge_host(prefs.host)
    if host is None:
        _set_status("Host must be an IP address, like 127.0.0.1")
        return
    family = socket.AF_INET6 if ":" in host else socket.AF_INET
    s = socket.socket(family, socket.SOCK_STREAM)
    s.setblocking(False)
    err = s.connect_ex((host, prefs.port))
    if err not in (0, errno.EINPROGRESS, errno.EWOULDBLOCK, getattr(errno, "WSAEWOULDBLOCK", 10035)):
        s.close()
        _set_status("Bridge not running")
        return
    link.pending = s
    link.pending_since = time.monotonic()


def _finish_connect():
    """Checks a pending connection; returns True once it is up."""
    s = link.pending
    try:
        _, writable, failed = select.select([], [s], [s], 0)
    except (OSError, ValueError):
        writable, failed = [], [s]
    err = s.getsockopt(socket.SOL_SOCKET, socket.SO_ERROR) if (writable or failed) else 0
    if failed or err:
        _close("Bridge not running")
        return False
    if not writable:
        if time.monotonic() - link.pending_since > CONNECT_TIMEOUT:
            _close("Bridge not running")
        return False
    link.pending = None
    s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    link.sock = s
    link.buf = protocol.LineBuffer()
    link.out = b""
    _set_status("Connected")
    _send({"type": "hello", "blender": bpy.app.version_string, "addon": ADDON_VERSION})
    return True


def _send(msg):
    if link.sock is None:
        return
    link.out += protocol.encode(msg)
    if len(link.out) > MAX_OUT:
        _close("Bridge stopped reading")
        return
    link.last_sent = time.monotonic()
    _flush()


def _flush():
    """Writes what the socket takes now and keeps the rest, so a line is never
    cut in half by a full send buffer."""
    while link.sock is not None and link.out:
        try:
            n = link.sock.send(link.out)
        except BlockingIOError:
            return
        except OSError:
            _close("Bridge went away")
            return
        link.out = link.out[n:]


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
    elif time.monotonic() - link.last_sent > PING:
        _send({"type": "ping"})


def tick():
    # The timer stops for good if this raises, so nothing may escape
    try:
        return _tick()
    except Exception as e:
        print("Blendeck:", e)
        try:
            _close("Error: %s" % e)
        except Exception:
            link.sock = None
        return RETRY


def _tick():
    prefs = _prefs()
    if prefs is None:
        return RETRY
    if not prefs.enabled:
        if link.sock is not None:
            _close("Turned off")
        _set_status("Turned off")
        return RETRY
    if link.sock is None:
        if link.pending is None:
            _connect(prefs)
            if link.pending is None:
                return RETRY
        if not _finish_connect():
            return TICK if link.pending is not None else RETRY
    _flush()
    _apply(_read(), prefs)
    return TICK


class BlendeckPreferences(bpy.types.AddonPreferences):
    bl_idname = __package__

    enabled: bpy.props.BoolProperty(name="Connect to bridge", default=True)
    host: bpy.props.StringProperty(name="Host", description="The bridge's IP address", default="127.0.0.1")
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
