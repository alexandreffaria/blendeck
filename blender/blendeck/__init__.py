"""Blendeck: drive Blender from a MIDI controller through the Blendeck bridge.

The bridge (blendeck.exe) reads the controller and listens on localhost; this
add-on connects to it, applies the commands it sends, and reports the playhead
back so the bridge's page can show it."""

import errno
import math
import select
import socket
import time
from time import perf_counter as _perf
from time import time as _wall

import bpy

from . import protocol

ADDON_VERSION = "0.16.0"
TICK = 0.01  # seconds between polls while connected
RETRY = 1.0  # seconds between connection attempts
PING = 5.0  # the bridge drops an add-on that says nothing for 15 s
# A turn of the move or trim knob is one undo step: it is pushed once the
# knob has been still this long, or right before any other command.
MOVE_UNDO_DELAY = 0.5
REPORT = 1.0  # seconds between latency reports while commands arrive
PICTURE_WAIT = 1.0  # a picture not drawn this long after a command isn't coming


class _Link:
    sock = None  # connected socket
    pending = None  # socket still connecting
    pending_since = 0.0
    buf = None  # incoming lines
    out = b""  # bytes not yet written
    status = "Not connected"
    last_state = None
    last_sent = 0.0
    move_undo = 0.0  # when the move or trim knob last changed strips with no undo step yet (0: none waiting)
    undo_message = "Move strips"  # what that waiting undo step is called
    note = ""  # what the last save press did, shown in the panel


link = _Link()


class _Meter:
    """How long commands take, measured here and reported to the bridge
    (see _measure). waits, pictures and totals are the samples since the
    last report, in milliseconds; tick the longest gap between two ticks,
    in seconds; dropped the presses left out of a read. pending is the
    command whose picture hasn't been seen yet: (when its tick started, how
    long it had waited by then). drawn is when the preview last drew."""

    waits = []
    pictures = []
    totals = []
    tick = 0.0
    dropped = 0
    last_tick = 0.0
    last_report = 0.0
    pending = None
    drawn = 0.0
    handle = None


meter = _Meter()


def _meter_reset():
    meter.waits, meter.pictures, meter.totals = [], [], []
    meter.tick = 0.0
    meter.dropped = 0
    meter.last_tick = 0.0
    meter.pending = None


def _on_preview_draw():
    """Runs each time a video editor's preview has drawn. Must never raise:
    Blender would print it on every redraw."""
    try:
        meter.drawn = _perf()
    except Exception:
        pass


def _watch_preview():
    """Starts noting when the preview draws. Never raises: without it the
    meter still reports how long commands waited."""
    try:
        if meter.handle is None:
            meter.handle = bpy.types.SpaceSequenceEditor.draw_handler_add(_on_preview_draw, (), "PREVIEW", "POST_PIXEL")
    except Exception as e:
        print("Blendeck: the latency meter can't see the preview:", e)


def _unwatch_preview():
    try:
        if meter.handle is not None:
            bpy.types.SpaceSequenceEditor.draw_handler_remove(meter.handle, "PREVIEW")
    except Exception as e:
        print("Blendeck: preview watch not removed:", e)
    meter.handle = None


def _playhead():
    """The edited scene's frame, or None. Never raises."""
    try:
        scene = _scene()
        return None if scene is None else scene.frame_current
    except Exception:
        return None


def _keep(samples, value):
    if len(samples) < protocol.MAX_LATENCY_SAMPLES:
        samples.append(value)


def _measure(messages, started, wall, moved):
    """The latency meter, once per tick after the commands were applied.
    started and wall are when this tick began (perf counter, and seconds
    since 1970 to compare with the bridge's stamps); moved says the tick
    changed what the preview shows. A command's wait is from the bridge
    taking it to this tick starting; its picture time from there to the
    preview's next draw, seen on a later tick. About once a second, while
    there is something to say, the numbers go to the bridge. Never raises."""
    try:
        now = _perf()
        if meter.last_tick:
            meter.tick = max(meter.tick, started - meter.last_tick)
        meter.last_tick = started
        if meter.pending is not None:
            applied, waited = meter.pending
            if meter.drawn > applied:
                picture = (meter.drawn - applied) * 1000
                _keep(meter.pictures, picture)
                _keep(meter.totals, waited + picture)
                meter.pending = None
            elif now - applied > PICTURE_WAIT:
                meter.pending = None  # no preview on screen, or nothing changed in it
        meter.dropped += protocol.presses_dropped(messages)
        # Before this tick's own command is noted: a knob that keeps turning
        # has a picture awaited at the end of every tick, but none here, so
        # the reports don't wait for it to stop. A command and its picture
        # always go out together.
        if now - meter.last_report >= REPORT and meter.pending is None:
            report = protocol.latency_report(meter.waits, meter.pictures, meter.totals, meter.tick * 1000, meter.dropped)
            if report is not None:
                _send(report)
                meter.last_report = now
                meter.waits, meter.pictures, meter.totals = [], [], []
                meter.tick = 0.0
                meter.dropped = 0
            elif not messages:
                meter.tick = 0.0  # idle: a gap from before the next command isn't its wait
        oldest = protocol.oldest_stamp(messages)
        if oldest is not None:
            waited = wall * 1000 - oldest
            if 0 <= waited <= protocol.MAX_WAIT_MS:
                _keep(meter.waits, waited)
                if moved and meter.pending is None:
                    meter.pending = (started, waited)
    except Exception as e:
        print("Blendeck: latency meter:", e)


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
        if win.screen is None:
            continue  # a window still opening or closing
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
    _meter_reset()
    _shuttle_stop()  # nothing would drive it any more
    _move_undo()  # nothing more will come for that turn of the knob
    stopped.running = False  # a stop seen after reconnecting isn't this one
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
    link.last_state = None  # the bridge starts from nothing: send the state again
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


def _timelines():
    """(window, area, region) for every video editor showing a timeline: the
    WINDOW region, which is the strips view. A Preview-only editor's WINDOW
    region is hidden (no size) and is skipped."""
    wm = bpy.context.window_manager
    if not wm:
        return
    for win in wm.windows:
        screen = win.screen
        if screen is None:
            continue
        for area in screen.areas:
            if area.type != "SEQUENCE_EDITOR":
                continue
            space = area.spaces.active
            if getattr(space, "view_type", "SEQUENCER") not in ("SEQUENCER", "SEQUENCER_PREVIEW"):
                continue
            for region in area.regions:
                if region.type == "WINDOW" and region.width > 1 and region.height > 1:
                    yield win, area, region
                    break


def _playhead_x(region, frame):
    """Where frame is drawn in region, in pixels from its left edge (may be
    outside 0..width when the playhead is off screen)."""
    v2d = region.view2d
    _, y = v2d.region_to_view(0, region.height // 2)
    x, _ = v2d.view_to_region(frame, y, clip=False)
    return x


def _zoom_one(region, op, times):
    """Zooms one timeline times steps around its playhead: the playhead keeps
    its spot on screen, or is centered first if it is off screen. Runs inside
    that editor's temp_override."""
    scene = _scene()
    if scene is None:
        for _ in range(times):
            op()
        return
    frame = scene.frame_current
    pre, target = protocol.zoom_anchor(_playhead_x(region, frame), region.width)
    if pre:
        bpy.ops.view2d.pan(deltax=pre, deltay=0)
    # view2d.zoom_in/out zoom around the view's middle; one pan afterwards
    # puts the playhead back where it was
    for _ in range(times):
        op()
    shift = protocol.zoom_correction(_playhead_x(region, frame), target)
    if shift:
        bpy.ops.view2d.pan(deltax=shift, deltay=0)


def _zoom(clicks):
    """Zooms every timeline in (clicks > 0) or out, one view2d zoom step per
    click, around the playhead. In the video editor that zooms time only.
    An editor that fails is skipped, never raised: one bad editor must not
    cost the connection."""
    plan = protocol.zoom_plan(clicks)
    if plan is None:
        return
    direction, times = plan
    op = bpy.ops.view2d.zoom_in if direction == "in" else bpy.ops.view2d.zoom_out
    for win, area, region in list(_timelines()):
        try:
            with bpy.context.temp_override(window=win, area=area, region=region):
                _zoom_one(region, op, times)
        except Exception as e:
            print("Blendeck: zoom skipped:", e)


def _pan(percent):
    """Slides every timeline percent of its own width: positive shows later
    frames, negative earlier. Only the view moves; the playhead and the
    strips stay. An editor that fails is skipped, never raised."""
    if not percent:
        return
    for win, area, region in list(_timelines()):
        try:
            pixels = protocol.pan_pixels(region.width, percent)
            if not pixels:
                continue
            with bpy.context.temp_override(window=win, area=area, region=region):
                bpy.ops.view2d.pan(deltax=pixels, deltay=0)
        except Exception as e:
            print("Blendeck: pan skipped:", e)


def _view_all():
    """Fits every timeline to the whole edit, like Home over the strips:
    every strip in view, on every channel. Only the view changes. An editor
    that fails is skipped, never raised."""
    for win, area, region in list(_timelines()):
        try:
            with bpy.context.temp_override(window=win, area=area, region=region):
                bpy.ops.sequencer.view_all()
        except Exception as e:
            print("Blendeck: view all skipped:", e)


def _recenter():
    """Centers every timeline on the playhead, keeping its zoom, so the
    view lands where playback stopped. Only the view moves. An editor that
    fails is skipped, never raised."""
    scene = _scene()
    if scene is None:
        return
    frame = scene.frame_current
    for win, area, region in list(_timelines()):
        try:
            pixels = protocol.center_pan(_playhead_x(region, frame), region.width)
            if not pixels:
                continue
            with bpy.context.temp_override(window=win, area=area, region=region):
                bpy.ops.view2d.pan(deltax=pixels, deltay=0)
        except Exception as e:
            print("Blendeck: recenter skipped:", e)


class _Stopped:
    """Whether playback ran at the last tick: Blender's own or the
    add-on's. When it goes from running to stopped, whatever stopped it,
    the timelines recenter on the playhead (see _watch_stop)."""

    running = False


stopped = _Stopped()


def _watch_stop(recenter=True):
    """Recenters the timelines once, on the tick playback is seen to stop,
    unless recenter is false (a view-all press in the same read: the whole
    edit is what was asked for). Never raises."""
    try:
        running = bool(shuttle.speed) or _playing()
        if stopped.running and not running and recenter:
            _recenter()
        stopped.running = running
    except Exception as e:
        print("Blendeck: recenter skipped:", e)


def _level(ed):
    """The strips and channels the video editor is showing: those of the
    meta strip being edited (Tab into a meta), or the top level. Blender
    4.2 and 4.3 call the strips "sequences" (renamed in 4.4)."""
    stack = list(ed.meta_stack)
    owner = stack[-1] if stack else ed
    strips = getattr(owner, "strips", None)
    if strips is None:
        strips = owner.sequences
    return strips, getattr(owner, "channels", None)


def _channel_locked(channels, number):
    try:
        return bool(channels[number].lock)
    except (IndexError, KeyError, TypeError, AttributeError):
        return False


def _spans(strips, channels):
    """The strips of a level as protocol.Span, in the same order, without
    effect inputs (see _ripple_delete for those)."""
    out = []
    for s in strips:
        start, end = _span(s)
        locked = bool(s.lock) or _channel_locked(channels, s.channel)
        out.append(protocol.Span(start, end, locked, bool(s.select)))
    return out


def _linked_spans(items, channels):
    """The strips of a level as protocol.Span with their effect inputs and
    channels, in the same order."""
    index = {s.as_pointer(): i for i, s in enumerate(items)}
    spans = []
    for s in items:
        start, end = _span(s)
        inputs = tuple(index[x.as_pointer()] for x in (getattr(s, "input_1", None), getattr(s, "input_2", None)) if x is not None and x.as_pointer() in index)
        locked = bool(s.lock) or _channel_locked(channels, s.channel)
        spans.append(protocol.Span(start, end, locked, bool(s.select), inputs, s.channel))
    return spans


def _cut(scene, follow=False):
    """Soft-cuts (like K) at the playhead in the level the editor shows: the
    selected strips under it, or every strip under it when none of those is
    selected (see protocol.cut_targets). Locked strips and strips on locked
    channels are skipped, and one undo step covers the whole cut. Both halves
    keep the strip's selection, unless follow (selection follows the
    playhead): then the right half, which starts at the playhead, is selected
    and the left half deselected. Strips that weren't cut keep their
    selection, so a hand-picked selection survives for the next edit. A
    strip that fails is skipped, never raised. Returns how many strips were
    cut."""
    try:
        ed = scene.sequence_editor
        if ed is None:
            return 0
        frame = scene.frame_current
        strips, channels = _level(ed)
        items = list(strips)
        targets = [items[i] for i in protocol.cut_targets(_spans(items, channels), frame)]
    except Exception as e:
        # An editor this add-on doesn't understand: no cut, link kept
        print("Blendeck: cut skipped:", e)
        return 0
    done = 0
    for strip in targets:
        try:
            # Checked again strip by strip: cutting one can already have cut
            # another (connected strips, effects on it)
            start, end = _span(strip)
            locked = strip.lock or _channel_locked(channels, strip.channel)
            if protocol.cuttable(start, end, frame, locked):
                right = strip.split(frame, "SOFT")
                done += 1
                if follow:
                    strip.select = False
                    if right is not None:
                        right.select = True
        except Exception as e:
            print("Blendeck: cut skipped a strip:", e)
    if done:
        try:
            bpy.ops.ed.undo_push(message="Cut at playhead")
        except Exception as e:
            print("Blendeck: no undo step for the cut:", e)
        _redraw()
    return done


def _undo_context():
    """What bpy.ops.ed.undo needs to run from a timer, which has no window:
    the first window with a screen, and in it a video editor's strips
    region if there is one. Returns (override dict, why not)."""
    wm = bpy.context.window_manager
    if not wm:
        return None, "no window manager"
    for win in wm.windows:
        screen = win.screen
        if screen is None:
            continue
        ctx = {"window": win, "screen": screen}
        for w, area, region in _timelines():
            if w == win:
                ctx["area"], ctx["region"] = area, region
                break
        return ctx, None
    return None, "no window"


def _undo():
    """Runs Blender's undo once, like Ctrl+Z. Refused while a modal
    operator runs in any window or the interface is locked (see
    protocol.undo_blocked), and a no-op with nothing left to undo. Never
    raises. Returns True if it undid something. Every bpy object fetched
    before this may point at freed data afterwards: fetch again."""
    try:
        wm = bpy.context.window_manager
        modal = []
        for win in (wm.windows if wm else ()):
            modal.extend(op.bl_idname for op in getattr(win, "modal_operators", ()))
        why = protocol.undo_blocked(modal, bool(getattr(wm, "is_interface_locked", False)))
        if why is None:
            ctx, why = _undo_context()
        if why is not None:
            print("Blendeck: undo skipped:", why)
            return False
        with bpy.context.temp_override(**ctx):
            if not bpy.ops.ed.undo.poll():
                return False  # nothing left to undo
            done = "FINISHED" in bpy.ops.ed.undo()
    except Exception as e:
        print("Blendeck: undo skipped:", e)
        return False
    _redraw()
    return done


def _note(text):
    """Sets the panel's save note. Never raises: a panel that can't redraw
    must not drop the bridge."""
    if link.note != text:
        link.note = text
        try:
            _redraw()
        except Exception as e:
            print("Blendeck: panel not redrawn:", e)


SAVE_AS = "Save As window open: pick a name and folder"
SAVE_AS_OP = "WM_OT_save_as_mainfile"  # how the open window shows in modal_operators


def _save():
    """Saves the .blend file in place, like Ctrl+S on a file saved before.
    A file never saved opens Blender's Save As window instead, as Ctrl+S
    does, in the first window. Refused, like undo, while a modal operator
    runs (a Save As window already open counts) or the interface is locked
    (a render). Never raises. Returns True if it saved."""
    try:
        wm = bpy.context.window_manager
        modal = []
        for win in (wm.windows if wm else ()):
            modal.extend(op.bl_idname for op in getattr(win, "modal_operators", ()))
        if SAVE_AS_OP in modal:
            # A press while it is open leaves it to you. Only a window for
            # a file never saved gets the note _save_as_done follows up.
            _note(SAVE_AS if not bpy.data.filepath else "Not saved: the Save As window is open")
            return False
        why = protocol.undo_blocked(modal, bool(getattr(wm, "is_interface_locked", False)))
        if why is None:
            ctx, why = _undo_context()
        if why is not None:
            print("Blendeck: save skipped:", why)
            _note("Not saved: " + why)
            return False
        if not bpy.data.filepath:
            with bpy.context.temp_override(**ctx):
                bpy.ops.wm.save_as_mainfile("INVOKE_DEFAULT")
            _note(SAVE_AS)
            return False
        with bpy.context.temp_override(**ctx):
            done = "FINISHED" in bpy.ops.wm.save_mainfile()
    except Exception as e:
        print("Blendeck: save failed:", e)
        _note("Not saved: %s" % e)
        return False
    _note(("Saved at " + time.strftime("%H:%M:%S")) if done else "Not saved")
    return done


def _save_as_done():
    """Once the Save As window a press opened has closed, the panel says
    whether it saved. Never raises."""
    try:
        if link.note != SAVE_AS:
            return
        if bpy.data.filepath:
            _note("Saved at " + time.strftime("%H:%M:%S"))
            return
        wm = bpy.context.window_manager
        for win in (wm.windows if wm else ()):
            if any(op.bl_idname == SAVE_AS_OP for op in getattr(win, "modal_operators", ())):
                return  # still open
        _note("Not saved: Save As was cancelled")
    except Exception as e:
        print("Blendeck: save note skipped:", e)


def _span(strip):
    """Where a strip sits: (start, end), end exclusive. Blender 5 names them
    left_handle and right_handle; before that, frame_final_start and end."""
    if hasattr(strip, "left_handle"):
        return strip.left_handle, strip.right_handle
    return strip.frame_final_start, strip.frame_final_end


def _move_left(strip, frames):
    """Moves a strip frames to the left, handles and content together."""
    if hasattr(strip, "content_start"):
        strip.content_start -= frames
    else:
        strip.frame_start -= frames


def _delete_op(scene, strips, items, delete, ctx):
    """Deletes items[i] for each i in delete with Blender's own Delete
    (bpy.ops.sequencer.delete), which stops the background prefetch render
    first. strips.remove() doesn't: in Blender 5.2 it frees a strip's data
    while the prefetch thread may still be rendering it, and Blender
    crashes. Only the strips to delete are selected for it; the others get
    their selection back. No undo step of its own (the caller pushes one
    for the whole ripple). Returns (how many went, whether any stayed).
    Never raises."""
    try:
        goners = {items[i].name for i in delete}
    except Exception as e:
        print("Blendeck: ripple delete skipped:", e)
        return 0, True
    before = {}
    try:
        for s in items:
            before[s.name] = s.select
            s.select = s.name in goners
        with bpy.context.temp_override(scene=scene, sequencer_scene=scene, **ctx):
            # The operator edits the scene Blender says it is editing: if
            # that isn't this one, it would delete whatever is selected there
            here = getattr(bpy.context, "sequencer_scene", None) or bpy.context.scene
            if here != scene:
                raise RuntimeError("the video editor shows another scene")
            bpy.ops.sequencer.delete("EXEC_DEFAULT", False)
    except Exception as e:
        print("Blendeck: ripple delete couldn't remove the strips:", e)
    # A deleted strip's Python object is freed: only look strips up again
    left = set()
    try:
        for s in list(strips):
            left.add(s.name)
            if s.name in before:
                s.select = before[s.name]
    except Exception as e:
        print("Blendeck: ripple delete couldn't restore the selection:", e)
    stayed = goners & left
    return len(goners) - len(stayed), bool(stayed)


def _delete_each(strips, items, delete):
    """Deletes items[i] for each i in delete with strips.remove(), for a
    Blender with no window (nothing draws there, so no prefetch runs).
    Effects come before their inputs in delete: removing an input also
    removes its effects, and a removed strip must not be touched again.
    Returns (how many went, whether any stayed). Never raises."""
    removed = 0
    failed = False
    for i in delete:
        try:
            strips.remove(items[i])
            removed += 1
        except Exception as e:
            failed = True
            print("Blendeck: ripple delete couldn't remove a strip:", e)
    return removed, failed


def _ripple_delete(scene):
    """Deletes the strips under the playhead and closes the gap they leave
    on every channel, in the level the editor shows (see
    protocol.ripple_plan for which strips and when the gap closes). One
    undo step covers it all. Nothing here raises: an editor it can't read
    is skipped, and if a strip can't be removed, nothing moves, so later
    strips never land on one that is still there. Returns how many strips
    were deleted."""
    try:
        ed = scene.sequence_editor
        if ed is None:
            return 0
        frame = scene.frame_current
        strips, channels = _level(ed)
        items = list(strips)
        spans = _linked_spans(items, channels)
        plan = protocol.ripple_plan(spans, frame)
    except Exception as e:
        print("Blendeck: ripple delete skipped:", e)
        return 0
    if plan is None:
        return 0
    ctx, _ = _undo_context()
    if ctx is not None:
        removed, failed = _delete_op(scene, strips, items, plan.delete, ctx)
    else:
        removed, failed = _delete_each(strips, items, plan.delete)
    if plan.shift and not failed:
        # Leftmost first: each lands where the gap, or a strip already
        # moved, was. Effects with inputs follow their inputs by themselves.
        for i in plan.move:
            strip = items[i]
            if spans[i].inputs:
                continue
            try:
                _move_left(strip, plan.shift)
            except Exception as e:
                print("Blendeck: ripple delete couldn't move a strip:", e)
        if plan.playhead is not None:
            try:
                _seek(scene, plan.playhead)
            except Exception as e:
                print("Blendeck: ripple delete left the playhead:", e)
    if removed:
        try:
            bpy.ops.ed.undo_push(message="Ripple delete")
        except Exception as e:
            print("Blendeck: no undo step for the ripple delete:", e)
        _redraw()
    return removed


def _move(scene, frames, before=False):
    """The move knob: moves the selected strips and every strip after them
    (before them, with before) frames right, or left when negative, in the
    level the editor shows, stopping against strips that stay (see
    protocol.move_plan). The playhead and selection stay. The undo step
    waits for the knob to stop (see _move_undo). Never raises. Returns
    whether anything moved."""
    try:
        ed = scene.sequence_editor
        if ed is None:
            return False
        strips, channels = _level(ed)
        items = list(strips)
        spans = _linked_spans(items, channels)
        plan = protocol.move_plan(spans, frames, before)
    except Exception as e:
        print("Blendeck: move skipped:", e)
        return False
    if plan is None:
        return False
    moved = False
    for i in plan.move:
        if spans[i].inputs:
            continue  # an effect follows its inputs by itself
        try:
            _move_left(items[i], -plan.shift)
            moved = True
        except Exception as e:
            # Stop here: the rest would close in on a strip that didn't move
            print("Blendeck: move couldn't move a strip:", e)
            break
    if moved:
        _knob_turned("Move strips")
        _redraw()
    return moved


# Strip types whose handles stop at the end of their content. The others
# (color, text, adjustment, a single image) can be as long as you like.
_CONTENT_TYPES = {"MOVIE", "SOUND", "SCENE", "MOVIECLIP", "MASK", "META"}


def _content(strip):
    """How far a strip's handles can go: (lo, hi), the first frame of its
    content and the frame after the last one, or (None, None) for no
    limit."""
    kind = getattr(strip, "type", "")
    if kind == "IMAGE":
        elements = getattr(strip, "elements", None)
        if elements is None or len(elements) <= 1:
            return None, None
    elif kind not in _CONTENT_TYPES:
        return None, None
    if hasattr(strip, "content_start") and hasattr(strip, "content_end"):
        return int(math.ceil(strip.content_start)), int(math.floor(strip.content_end))
    # Before Blender 5: the content starts at frame_start and lasts
    # frame_duration frames
    start = strip.frame_start
    return int(math.ceil(start)), int(math.floor(start + strip.frame_duration))


def _set_handle(strip, which, frame):
    """Puts a strip's start (which "start") or end handle on frame,
    without moving its content."""
    if which == "start":
        if hasattr(strip, "left_handle"):
            strip.left_handle = frame
        else:
            strip.frame_final_start = frame
    elif hasattr(strip, "right_handle"):
        strip.right_handle = frame
    else:
        strip.frame_final_end = frame


def _trim(scene, frames, shrink=False):
    """The trim knob: moves the handles at the playhead frames right, or
    left when negative, in the level the editor shows, and the playhead
    with them (see protocol.trim_plan for which handles and how far). Turn
    left on a clip's start to show more of its beginning, right on its end
    to show more of its end; where two clips meet, the cut rolls, or with
    shrink (Fn) only the clip turned into gets shorter. The undo
    step waits for the knob to stop, like the move knob's. Never raises.
    Returns whether anything changed."""
    try:
        ed = scene.sequence_editor
        if ed is None:
            return False
        strips, channels = _level(ed)
        items = list(strips)
        clips = []
        for s in items:
            start, end = _span(s)
            locked = bool(s.lock) or _channel_locked(channels, s.channel)
            follows = getattr(s, "input_1", None) is not None
            lo, hi = _content(s)
            clips.append(protocol.Clip(start, end, locked, s.channel, follows, lo, hi))
        plan = protocol.trim_plan(clips, scene.frame_current, frames, shrink)
    except Exception as e:
        print("Blendeck: trim skipped:", e)
        return False
    if plan is None:
        return False
    # Shrinking handles first, so where two clips meet on a channel the one
    # growing never lands on the other: right, starts shrink; left, ends.
    if plan.shift > 0:
        order = [(i, "start") for i in plan.starts] + [(i, "end") for i in plan.ends]
    else:
        order = [(i, "end") for i in plan.ends] + [(i, "start") for i in plan.starts]
    done = []
    for i, which in order:
        clip = clips[i]
        frame = (clip.start if which == "start" else clip.end) + plan.shift
        try:
            _set_handle(items[i], which, frame)
            done.append((i, which))
        except Exception as e:
            # Put back the handles already moved, last first, so a picture
            # and its sound never end up out of step
            print("Blendeck: trim couldn't move a handle:", e)
            for k, w in reversed(done):
                try:
                    _set_handle(items[k], w, clips[k].start if w == "start" else clips[k].end)
                except Exception as e2:
                    print("Blendeck: trim couldn't put a handle back:", e2)
            done = []
            break
    changed = bool(done)
    if changed:
        try:
            _seek(scene, plan.playhead)
        except Exception as e:
            print("Blendeck: trim left the playhead:", e)
        _knob_turned("Trim handle")
        _redraw()
    return changed


def _knob_turned(message):
    """Notes that the move or trim knob just changed strips: their undo
    step, called message, waits for the knob to stop. A turn of the other
    knob first gets its own step."""
    if link.move_undo and link.undo_message != message:
        _move_undo()
    link.move_undo = time.monotonic()
    link.undo_message = message


def _move_undo(now=None):
    """Pushes the undo step for the move or trim knob's last turn, if one
    is waiting: always when now is None, else once the knob has been still
    for MOVE_UNDO_DELAY. Never raises."""
    if not link.move_undo:
        return
    if now is not None and now - link.move_undo < MOVE_UNDO_DELAY:
        return
    link.move_undo = 0.0
    try:
        bpy.ops.ed.undo_push(message=link.undo_message)
    except Exception as e:
        print("Blendeck: no undo step for the knob:", e)


def _follow(scene):
    """Selection follows the playhead: selects the unlocked strips under it
    in the level the editor shows and deselects the rest, like Premiere's
    option of that name. Never raises. Returns whether anything changed."""
    try:
        ed = scene.sequence_editor
        if ed is None:
            return False
        strips, channels = _level(ed)
        items = list(strips)
        want = protocol.follow_selection(_spans(items, channels), scene.frame_current)
        changed = False
        for strip, on in zip(items, want):
            if bool(strip.select) != on:
                strip.select = on
                changed = True
    except Exception as e:
        print("Blendeck: selection didn't follow the playhead:", e)
        return False
    if changed:
        _redraw()
    return changed


def _channel_muted(channels, number):
    try:
        return bool(channels[number].mute)
    except (IndexError, KeyError, TypeError, AttributeError):
        return False


def _jump(scene, clicks, sound=False):
    """Moves the playhead clicks cuts right (left when negative): to the
    starts and ends of the strips in the level the editor shows, like
    Blender's Up and Down arrows. Sound strips are skipped, so it stops
    only where the picture changes, unless sound is true (Fn, decision
    20261115s2rt). Muted strips and strips on muted channels are skipped,
    as Blender does. Never raises."""
    try:
        ed = scene.sequence_editor
        if ed is None:
            return
        strips, channels = _level(ed)
        spans = [
            _span(s) for s in strips
            if not getattr(s, "mute", False) and not _channel_muted(channels, s.channel)
            and (sound or getattr(s, "type", "") != "SOUND")
        ]
        target = protocol.jump_target(protocol.edit_points(spans), scene.frame_current, clicks)
        if target is not None and target != scene.frame_current:
            _seek(scene, target)
    except Exception as e:
        print("Blendeck: jump skipped:", e)


def _playing():
    """Whether Blender is playing back, in any window. Never raises."""
    try:
        wm = bpy.context.window_manager
        for win in (wm.windows if wm else ()):
            screen = win.screen
            if screen is not None and getattr(screen, "is_animation_playing", False):
                return True
    except Exception:
        pass
    return False


def _seek(scene, frame):
    """Moves the playhead to frame. While Blender plays with Sync to Audio
    (the video editor's default), frame_set alone is undone by the next
    playback step, which follows the sound: assigning frame_current moves
    the sound too. frame_set then runs the frame-change handlers right
    away, so selection has followed before the next edit in the same
    read."""
    if _playing():
        scene.frame_current = frame
    scene.frame_set(frame)
    if shuttle.speed:
        _shuttle_anchor(scene)  # the add-on's playback carries on from here


def _window_scene(win):
    """The scene a window's video editors show: its workspace's sequencer
    scene in Blender 5, else the window's scene."""
    return getattr(getattr(win, "workspace", None), "sequencer_scene", None) or getattr(win, "scene", None)


def _play_context(scene):
    """Where to start playback from a timer, which has no window:
    (override, editor found, window found). Playing from a video editor
    plays the scene it shows; from anywhere else, Blender plays the
    window's scene, which in Blender 5 can be another one. So: a video
    editor in a window showing scene (its strips region if it has one),
    else such a window by itself."""
    wm = bpy.context.window_manager
    window = None
    for win in (wm.windows if wm else ()):
        screen = win.screen
        if screen is None:
            continue
        if window is None and getattr(win, "scene", None) == scene:
            window = {"window": win, "screen": screen}
        if _window_scene(win) != scene:
            continue
        for area in screen.areas:
            if area.type == "SEQUENCE_EDITOR":
                ctx = {"window": win, "screen": screen, "area": area}
                for region in area.regions:
                    if region.type == "WINDOW":
                        ctx["region"] = region
                        break
                return ctx, True, window is not None
    return window, False, window is not None


def _play_pause(scene):
    """Starts or pauses playback, like Space in the video editor: it
    plays the scene the edits go to, and a pause leaves the playhead where
    it stopped. It also stops playback the forward and backwards buttons
    started, whatever its speed, so the next play is at normal speed.
    Playback makes no undo steps. Never raises. Returns what it did
    ("play", "play_window", "pause") or None."""
    try:
        if shuttle.speed:
            _shuttle_stop()
            return "pause"
        playing = _playing()
        ctx, editor, window = (None, False, False) if playing else _play_context(scene)
        what = protocol.play_pause_plan(playing, editor, window)
        if what is None:
            print("Blendeck: play skipped: no video editor or window shows", scene.name)
            return None
        if what == "pause":
            ctx, why = _undo_context()
            if why is not None:
                print("Blendeck: pause skipped:", why)
                return None
            with bpy.context.temp_override(**ctx):
                bpy.ops.screen.animation_cancel(restore_frame=False)
        else:
            with bpy.context.temp_override(**ctx):
                bpy.ops.screen.animation_play()
    except Exception as e:
        print("Blendeck: play / pause skipped:", e)
        return None
    return what


class _Shuttle:
    """Playback the add-on drives itself, for every speed Blender's own
    can't play. Blender plays at 1x only, so faster than that, and
    backwards at any speed, move the playhead from this add-on's timer, in
    time with the clock. speed is in hundredths (see protocol.NORMAL_SPEED),
    0 when not driving. It runs from origin, the frame it started at or last
    picked up from, since `since`; last is the frame it set, so a move by
    anything else is noticed."""

    speed = 0
    origin = 0
    since = 0.0
    last = None
    scene = ""


shuttle = _Shuttle()


class _Audio:
    """The sound of playback the add-on drives (see _audio_tick). sounds
    maps a strip's key to the handle playing it; started holds every strip
    started since playback last picked up; strips is the scene's sound
    strips then, None until read."""

    sounds = {}
    started = set()
    strips = None
    failed = False


audio = _Audio()


def _audio_stop():
    """Stops every sound the add-on started. Never raises."""
    for handle in audio.sounds.values():
        try:
            handle.stop()
        except Exception:
            pass
    audio.sounds = {}
    audio.started = set()
    audio.strips = None
    audio.failed = False


def _audio_restart():
    """After an edit while the add-on drives playback: the strips heard
    may have changed, so the sound starts again from where playback is,
    read from the strips as they are now."""
    if shuttle.speed:
        _audio_stop()


def _shuttle_stop():
    shuttle.speed = 0
    shuttle.last = None
    _audio_stop()


def _shuttle_anchor(scene):
    shuttle.origin = scene.frame_current
    shuttle.since = time.monotonic()
    shuttle.last = scene.frame_current
    _audio_stop()  # the sound starts again from here, at this speed


def _cancel_native():
    """Pauses Blender's own playback, leaving the playhead."""
    ctx, why = _undo_context()
    if why is not None:
        raise RuntimeError(why)
    with bpy.context.temp_override(**ctx):
        bpy.ops.screen.animation_cancel(restore_frame=False)


def _stop_playback():
    """Stops playback where it is, Blender's own or the add-on's, for the
    scrub knob: it takes the playhead over, so the frame it lands on is
    where playback stopped. Nothing while nothing plays. Never raises; if
    Blender's own can't be paused, it says why and plays on."""
    try:
        if shuttle.speed:
            _shuttle_stop()
        if _playing():
            _cancel_native()
    except Exception as e:
        print("Blendeck: playback not stopped:", e)


def _shuttle(scene, direction):
    """A press of the forward (direction 1) or backwards (-1) button: play
    that way at normal speed, 0.25x faster if it already plays that way, or
    0.25x slower if it plays the other way faster than 1x (see
    protocol.shuttle_speed). 1x forward is Blender's own playback, so
    it has Blender's own sound; every other speed is driven by the add-on,
    with sound forward up to 5x (see _audio_tick). Never raises. Returns
    the new speed, in hundredths, or 0 if it stopped."""
    try:
        current = shuttle.speed or (protocol.NORMAL_SPEED if _playing() else 0)
        speed = protocol.shuttle_speed(current, direction)
        if speed == protocol.NORMAL_SPEED:
            _shuttle_stop()
            if _playing() or _play_pause(scene) is not None:
                return speed
            # Blender's own playback can't start here: drive it instead
        if _playing():
            _cancel_native()
        shuttle.speed = speed
        shuttle.scene = scene.name
        _shuttle_anchor(scene)
        return speed
    except Exception as e:
        print("Blendeck: forward / backwards skipped:", e)
        _shuttle_stop()
        return 0


def _cache_window_range(scene):
    """True when the preview range is one the Cache Window extension
    borrowed: it publishes scene["cache_window"] with the start and end it
    set while it holds the range. Any error reading it means not borrowed."""
    try:
        marker = scene.get("cache_window")
        if marker is None:
            return False
        return (int(marker["start"]) == scene.frame_preview_start
                and int(marker["end"]) == scene.frame_preview_end)
    except Exception:
        return False


def _play_range(scene):
    """The frames playback runs over: the preview range when it is on, as
    for Blender's own playback, else the scene's. A preview range Cache
    Window borrowed to prefetch around the playhead isn't the user's, so it
    is passed over."""
    if getattr(scene, "use_preview_range", False) and not _cache_window_range(scene):
        return scene.frame_preview_start, scene.frame_preview_end
    return scene.frame_start, scene.frame_end


def _shuttle_tick(scene):
    """Moves the playhead for playback the add-on drives, once per timer
    tick. It picks up from wherever something else (the jump knob, an
    edit, the mouse) put the playhead, and stops at the ends of the play
    range (see _play_range), when the edited scene changes, when Blender's
    own playback starts, or when a render starts. Frames
    it can't show in time are skipped, like Blender's Frame Dropping.
    Never raises."""
    if not shuttle.speed:
        return
    try:
        if scene.name != shuttle.scene or _playing() or _rendering():
            _shuttle_stop()
            return
        if scene.frame_current != shuttle.last:
            _shuttle_anchor(scene)
        fps = scene.render.fps / scene.render.fps_base
        elapsed = time.monotonic() - shuttle.since
        start, end = _play_range(scene)
        frame, at_end = protocol.shuttle_frame(shuttle.origin, elapsed, fps, shuttle.speed, start, end)
        if frame != scene.frame_current:
            scene.frame_set(frame)
        shuttle.last = scene.frame_current
        if at_end:
            _shuttle_stop()
            return
        _audio_tick(scene, elapsed, fps)
    except Exception as e:
        print("Blendeck: playback stopped:", e)
        _shuttle_stop()


def _sound_strips(scene):
    """The sound strips heard in scene, as protocol.audio_plan takes them,
    with their sound's file and volume: [((key, start, end, content_start,
    sound_offset), path, volume)]. Muted strips and channels, strips in
    muted meta strips, and sounds packed into the .blend are left out."""
    ed = scene.sequence_editor
    if ed is None or not getattr(scene, "use_audio", True):
        return []
    found = []

    def walk(strips, channels, volume):
        for s in strips:
            if s.mute or _channel_muted(channels, s.channel):
                continue
            if s.type == "META":
                walk(s.strips, getattr(s, "channels", None), volume * getattr(s, "volume", 1.0))
                continue
            sound = getattr(s, "sound", None) if s.type == "SOUND" else None
            if sound is None or sound.packed_file is not None:
                continue
            key = (s.as_pointer(), s.name)
            start, end = _span(s)
            content_start = s.content_start if hasattr(s, "content_start") else s.frame_start
            span = (key, start, end, content_start, s.sound_offset)
            found.append((span, bpy.path.abspath(sound.filepath), s.volume * volume))

    walk(ed.strips, getattr(ed, "channels", None), getattr(scene, "audio_volume", 1.0))
    return found


def _audio_tick(scene, elapsed, fps):
    """Plays the sound of playback the add-on drives, forward up to
    protocol.MAX_AUDIO_SPEED: each sound strip's file, from where the
    playhead is in it, sped up with its pitch kept, started a little before
    it is heard (see protocol.audio_plan). Called once per timer tick. If
    sound can't play (no audio device, a file it can't read), playback
    carries on silent until it next picks up. Never raises."""
    if audio.failed:
        return
    if not protocol.shuttle_has_audio(shuttle.speed):
        if audio.sounds:
            _audio_stop()
        return
    try:
        import aud

        if audio.strips is None:
            audio.strips = {span[0]: (span, path, volume) for span, path, volume in _sound_strips(scene)}
        # Finished sounds make room. status is a STATUS_* number, or in
        # Blender 5.2 a bool: False once the sound is done
        audio.sounds = {k: h for k, h in audio.sounds.items() if h.status and h.status != aud.STATUS_STOPPED}
        plan = protocol.audio_plan(
            [span for span, _, _ in audio.strips.values()],
            shuttle.origin,
            elapsed,
            fps,
            shuttle.speed,
            audio.started,
            len(audio.sounds),
        )
        if not plan:
            return
        device = aud.Device()
        stretch = protocol.NORMAL_SPEED / shuttle.speed
        for key, from_s, to_s, delay in plan:
            audio.started.add(key)
            _, path, volume = audio.strips[key]
            sound = aud.Sound(path).limit(from_s, to_s).timeStretchPitchScale(stretch, 1.0).volume(volume)
            if delay > 0:
                sound = sound.delay(delay)
            audio.sounds[key] = device.play(sound)
    except Exception as e:
        print("Blendeck: playing on silent:", e)
        _audio_stop()
        audio.failed = True


class _Scrub:
    """The sound the last scrub click started (see _scrub_sound): handles
    to stop when the next click lands; error is the last failure printed."""

    handles = []
    error = None


scrub = _Scrub()


def _scrub_stop():
    for handle in scrub.handles:
        try:
            handle.stop()
        except Exception:
            pass
    scrub.handles = []


def _scrub_sound(scene, frm):
    """Plays the sound of a scrub that moved the playhead from frame frm
    to where it is, since a script's frame change doesn't trigger
    Blender's own audio scrubbing: the frames it moved over (see
    protocol.scrub_frames) of every sound strip heard there, at the
    strip's volume. What the previous click started is
    stopped first. Silent while anything plays (playback the scrub
    couldn't stop), since playback has its own sound. Never raises; a
    failure is printed once until it changes."""
    _scrub_stop()
    if _playing() or shuttle.speed:
        return
    try:
        import aud

        found = _sound_strips(scene)
        files = {span[0]: (path, volume) for span, path, volume in found}
        fps = scene.render.fps / scene.render.fps_base
        plan = protocol.scrub_sounds([span for span, _, _ in found], frm, scene.frame_current, fps)
        if not plan:
            return
        device = aud.Device()
        for key, from_s, to_s, delay in plan:
            path, volume = files[key]
            sound = aud.Sound(path).limit(from_s, to_s).volume(volume)
            if delay > 0:
                sound = sound.delay(delay)
            scrub.handles.append(device.play(sound))
        scrub.error = None
    except Exception as e:
        _scrub_stop()
        if str(e) != scrub.error:
            scrub.error = str(e)
            print("Blendeck: scrubbing is silent:", e)


def _rendering():
    try:
        return bool(bpy.app.is_job_running("RENDER"))
    except Exception:
        return False


def _persistent(f):
    """bpy.app.handlers.persistent, so the handler survives opening a file;
    a no-op where bpy has no handlers (the tests' fake bpy)."""
    handlers = getattr(bpy.app, "handlers", None)
    return getattr(handlers, "persistent", lambda g: g)(f)


@_persistent
def on_frame_change(scene, depsgraph=None):
    """Runs after every playhead move, whatever moved it (a knob, the mouse,
    playback). Must never raise: Blender would print it every frame."""
    try:
        prefs = _prefs()
        if prefs is None or not prefs.follow_selection or _rendering():
            return
        _follow(scene)
    except Exception as e:
        print("Blendeck:", e)


def _apply(messages, prefs):
    # View all first: a zoom or pan in the same read then applies to the
    # whole-edit view instead of being thrown away by it
    if protocol.wants_view_all(messages):
        _view_all()
    _zoom(protocol.total_zoom(messages))
    _pan(protocol.total_pan(messages))
    scene = _scene()
    if scene is None:
        return
    dropped = protocol.presses_dropped(messages)
    if dropped:
        # Never silently: every press counts (decision 202611163di7), so
        # the ones a flood cost are said
        print("Blendeck: %d presses dropped: more than %d cuts, undos and ripple deletes waited while Blender was busy" % (dropped, protocol.MAX_PRESSES_PER_TICK))
    for kind, frames in protocol.edit_steps(messages):
        if kind in ("move", "move_before"):
            _move(scene, frames, kind == "move_before")
            _audio_restart()
            continue
        if kind in ("trim", "trim_shrink"):
            _trim(scene, frames, kind == "trim_shrink")
            _audio_restart()
            continue
        # Anything else ends the knob's turn: its undo step comes first, so
        # an undo takes back the whole turn
        _move_undo()
        if kind == "cut":
            # Only the strips cut change selection (see _cut): a full
            # re-follow here would replace a hand-picked selection
            _cut(scene, follow=prefs.follow_selection)
            _audio_restart()
            continue
        if kind == "undo":
            # Blender's undo stops its own playback; stop the add-on's too
            _shuttle_stop()
            _undo()
            # The undo reloaded the data the old scene pointed at
            scene = _scene()
            if scene is None:
                return
            continue
        if kind == "ripple_delete":
            if _ripple_delete(scene) and prefs.follow_selection:
                _follow(scene)  # the playhead may not have moved
            _audio_restart()
            continue
        if kind == "play_pause":
            _play_pause(scene)
            continue
        if kind == "shuttle":
            _shuttle(scene, frames)  # for a shuttle, frames is the direction
            continue
        if kind in ("jump", "jump_sound"):
            _jump(scene, frames, kind == "jump_sound")  # for a jump, frames is how many cuts
            continue
        # The scrub knob takes the playhead over: playback stops, and the
        # frame the knob leaves it on is where it stopped
        _stop_playback()
        target = protocol.scrub_target(scene.frame_current, frames, scene.frame_start, scene.frame_end, prefs.clamp)
        if target != scene.frame_current:
            was = scene.frame_current
            _seek(scene, target)
            if prefs.scrub_audio:
                _scrub_sound(scene, was)
    # After the edits, not before: the playhead is still on the frame the
    # last tick put on screen, so a cut or ripple delete pressed during
    # playback the add-on drives lands on the picture that was shown, not
    # on where the clock has got to since (frames nobody has seen)
    _shuttle_tick(scene)
    if link.sock is None:
        return  # the read just found the bridge gone
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
        if link.sock is not None or link.pending is not None:
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
    started, wall = _perf(), _wall()
    messages = _read()
    before = _playhead()
    _apply(messages, prefs)
    _measure(messages, started, wall, before != _playhead() or protocol.moves_picture(messages))
    if protocol.wants_save(messages):
        # After the edits, so the file holds every one from the same read,
        # and with the move knob's undo step pushed. It needs no scene.
        _move_undo()
        _save()
    else:
        _save_as_done()  # from the next tick on, once the window is listed
    _watch_stop(not protocol.wants_view_all(messages))
    _move_undo(time.monotonic())
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
    scrub_audio: bpy.props.BoolProperty(
        name="Sound while scrubbing",
        description="Each click of the scrub knob plays the sound of the frames it moved over",
        default=True,
    )
    follow_selection: bpy.props.BoolProperty(
        name="Selection follows playhead",
        description="Whenever the playhead moves, the strips under it are selected and the rest deselected, so cut and ripple delete act on them. Works with or without the bridge connected",
        default=True,
    )

    def draw(self, context):
        col = self.layout.column()
        col.prop(self, "enabled")
        col.prop(self, "clamp")
        col.prop(self, "scrub_audio")
        col.prop(self, "follow_selection")
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
        if link.note:
            col.label(text=link.note, icon="FILE_TICK" if link.note.startswith("Saved") else "INFO")
        if prefs:
            col.prop(prefs, "enabled")
            col.prop(prefs, "clamp")
            col.prop(prefs, "scrub_audio")
            col.prop(prefs, "follow_selection")


classes = (BlendeckPreferences, SEQUENCER_PT_blendeck)


def register():
    for c in classes:
        bpy.utils.register_class(c)
    if not bpy.app.timers.is_registered(tick):
        bpy.app.timers.register(tick, first_interval=0.5, persistent=True)
    _remove_handler()
    bpy.app.handlers.frame_change_post.append(on_frame_change)
    _watch_preview()


def _remove_handler():
    """Takes on_frame_change out of the frame-change handlers, also a copy
    from an earlier load of this add-on (a reload makes a new function)."""
    hs = bpy.app.handlers.frame_change_post
    for h in list(hs):
        if h is on_frame_change or (getattr(h, "__name__", "") == "on_frame_change" and getattr(h, "__module__", "") == __name__):
            hs.remove(h)


def unregister():
    _remove_handler()
    _unwatch_preview()
    if bpy.app.timers.is_registered(tick):
        bpy.app.timers.unregister(tick)
    _close("Not connected")
    _scrub_stop()
    for c in reversed(classes):
        bpy.utils.unregister_class(c)
