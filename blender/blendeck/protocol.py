"""Wire format and playhead math for the Blendeck link. No bpy here, so it can
be tested without Blender."""

import ipaddress
import json
import math
from collections import namedtuple

MAX_LINE = 64 * 1024
# Zoom steps applied per tick at most, so a burst of commands can't keep
# Blender's UI busy zooming. Each step is one zoom operator call per editor.
MAX_ZOOM_PER_TICK = 40
# Cuts applied per tick at most; a button can't be pressed faster than a few
# times a tick, so more than this is a flood, not someone editing.
MAX_CUTS_PER_TICK = 8
# Undos applied per tick at most. Each press is one undo, like Ctrl+Z, but
# presses that piled up while Blender was busy shouldn't undo a whole
# session at once, and one undo of a big file can take a good fraction of a
# second. Undos past this in one read are dropped.
MAX_UNDOS_PER_TICK = 4
# Ripple deletes applied per tick at most. Unlike cuts, two in a row both
# do something (the second deletes what slid under the playhead), so they
# are never merged.
MAX_RIPPLES_PER_TICK = 8
# Play / pause presses applied per tick at most. Each press toggles, so two
# in a row with nothing between cancel out and are dropped together.
MAX_TOGGLES_PER_TICK = 8
# Playback speeds, in tenths of normal speed: 10 is 1x forward, -10 is 1x
# backwards, 0 is stopped. Each press of the forward or backwards button
# adds SHUTTLE_STEP (10%), up to MAX_SHUTTLE_SPEED (4x) either way.
NORMAL_SPEED = 10
SHUTTLE_STEP = 1
MAX_SHUTTLE_SPEED = 40
# Forward / backwards presses applied per tick at most.
MAX_SHUTTLES_PER_TICK = 8
# Cuts the jump knob moves over per tick at most, either way. A fast spin
# sends a few clicks a tick; more than this is a flood.
MAX_JUMPS_PER_TICK = 32


class LineBuffer:
    """Splits a byte stream into JSON objects, one per line. A line longer than
    MAX_LINE raises ValueError, and the caller should drop the connection."""

    def __init__(self):
        self._buf = b""

    def feed(self, data):
        self._buf += data
        messages = []
        while True:
            i = self._buf.find(b"\n")
            if i < 0:
                break
            line, self._buf = self._buf[:i], self._buf[i + 1:]
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if isinstance(msg, dict):
                messages.append(msg)
        if len(self._buf) > MAX_LINE:
            self._buf = b""
            raise ValueError("line too long")
        return messages


def encode(msg):
    return (json.dumps(msg, separators=(",", ":")) + "\n").encode()


def total_scrub(messages):
    """Adds up the frames of every scrub command, ignoring malformed ones."""
    total = 0
    for m in messages:
        if m.get("type") != "scrub":
            continue
        frames = m.get("frames")
        if isinstance(frames, int) and not isinstance(frames, bool):
            total += frames
    return total


def total_zoom(messages):
    """Adds up the clicks of every zoom command (positive zooms in), ignoring
    malformed ones, and caps the result at MAX_ZOOM_PER_TICK either way."""
    total = 0
    for m in messages:
        if m.get("type") != "zoom":
            continue
        clicks = m.get("clicks")
        if isinstance(clicks, int) and not isinstance(clicks, bool):
            total += clicks
    return max(-MAX_ZOOM_PER_TICK, min(MAX_ZOOM_PER_TICK, total))


def edit_steps(messages):
    """The scrubs, cuts, undos, ripple deletes and play / pause presses in
    messages, in order: ("scrub", frames), ("cut", None), ("undo", None),
    ("ripple_delete", None) and ("play_pause", None). Order matters, since
    a cut or delete lands where the playhead is at that moment and an undo
    takes back what came before it. Scrubs in a row are added up, cuts in a row with no scrub between
    become one (a second cut at the same frame does nothing), and cuts past
    MAX_CUTS_PER_TICK are dropped; scrubs after them still count. Every undo
    is kept, one per press, never merged, up to MAX_UNDOS_PER_TICK, and so is
    every ripple delete, up to MAX_RIPPLES_PER_TICK. Play / pause presses in
    a row with nothing between are a toggle each, so a pair cancels out: an
    even run leaves nothing, an odd one a single toggle. At most
    MAX_TOGGLES_PER_TICK presses count. Forward and backwards presses are
    ("shuttle", 1) and ("shuttle", -1), one each, never merged, up to
    MAX_SHUTTLES_PER_TICK. Jumps between cuts are ("jump", clicks): jumps
    in a row are added up, and at most MAX_JUMPS_PER_TICK clicks count
    either way in one read. Malformed commands are ignored."""
    steps = []
    cuts = 0
    undos = 0
    ripples = 0
    toggles = 0
    shuttles = 0
    jumps = 0
    for m in messages:
        kind = m.get("type")
        if kind == "scrub":
            frames = m.get("frames")
            if not isinstance(frames, int) or isinstance(frames, bool) or frames == 0:
                continue
            if steps and steps[-1][0] == "scrub":
                steps[-1] = ("scrub", steps[-1][1] + frames)
            else:
                steps.append(("scrub", frames))
        elif kind == "cut":
            if cuts >= MAX_CUTS_PER_TICK or (steps and steps[-1][0] == "cut"):
                continue
            cuts += 1
            steps.append(("cut", None))
        elif kind == "undo":
            if undos >= MAX_UNDOS_PER_TICK:
                continue
            undos += 1
            steps.append(("undo", None))
        elif kind == "ripple_delete":
            if ripples >= MAX_RIPPLES_PER_TICK:
                continue
            ripples += 1
            steps.append(("ripple_delete", None))
        elif kind == "play_pause":
            if toggles >= MAX_TOGGLES_PER_TICK:
                continue
            toggles += 1
            if steps and steps[-1][0] == "play_pause":
                steps.pop()  # play then pause at once: nothing
            else:
                steps.append(("play_pause", None))
        elif kind == "shuttle":
            clicks = m.get("clicks")
            if type(clicks) is not int or clicks not in (1, -1) or shuttles >= MAX_SHUTTLES_PER_TICK:
                continue
            shuttles += 1
            steps.append(("shuttle", clicks))
        elif kind == "jump":
            clicks = m.get("clicks")
            if type(clicks) is not int or clicks == 0:
                continue
            room = MAX_JUMPS_PER_TICK - jumps
            clicks = max(-room, min(room, clicks))
            if clicks == 0:
                continue
            jumps += abs(clicks)
            if steps and steps[-1][0] == "jump":
                steps[-1] = ("jump", steps[-1][1] + clicks)
                if steps[-1][1] == 0:
                    steps.pop()  # right then left: back where it was
            else:
                steps.append(("jump", clicks))
    return steps


def edit_points(spans):
    """The frames the jump knob stops on, sorted: every start and end of the
    strips in spans (a list of (start, end) pairs, end exclusive, so the end
    is the first frame after the strip, where the next one would start)."""
    points = set()
    for start, end in spans:
        points.add(start)
        points.add(end)
    return sorted(points)


def jump_target(points, frame, clicks):
    """Where the playhead lands moving clicks edit points from frame
    (points: sorted, see edit_points): right when clicks is positive, left
    when negative. A playhead on an edit point moves to the next one. Past
    the last edit point that way it stops on the last one; with none that
    way, None (the playhead stays)."""
    if clicks > 0:
        ahead = [p for p in points if p > frame]
    elif clicks < 0:
        ahead = [p for p in points if p < frame][::-1]
    else:
        return None
    if not ahead:
        return None
    return ahead[min(abs(clicks), len(ahead)) - 1]


def shuttle_speed(current, direction):
    """The playback speed after a press of the forward (direction 1) or
    backwards (-1) button, from current (both in tenths, see NORMAL_SPEED).
    Already going that way: 10% faster, up to MAX_SHUTTLE_SPEED. Going the
    other way faster than 1x: 10% slower, easing off down to 1x. Going the
    other way at 1x, or stopped: normal speed in this direction (decision
    20261115618c)."""
    if current * direction > 0:
        return direction * min(MAX_SHUTTLE_SPEED, abs(current) + SHUTTLE_STEP)
    if abs(current) > NORMAL_SPEED:
        return current + direction * SHUTTLE_STEP
    return direction * NORMAL_SPEED


def shuttle_frame(origin, elapsed, fps, speed, start, end):
    """Where playback the add-on drives is after elapsed seconds, having
    started at frame origin: (frame, at_end). It stops at the scene's first
    frame going backwards and its last going forward (at_end is then True);
    a playhead already outside that range runs on to where it started."""
    pos = origin + elapsed * fps * speed / NORMAL_SPEED
    lo, hi = min(start, origin), max(end, origin)
    if speed < 0 and pos <= lo:
        return lo, True
    if speed > 0 and pos >= hi:
        return hi, True
    return max(lo, min(hi, int(math.floor(pos)))), False


def play_pause_plan(playing, editor, window):
    """What the play / pause button does, like Space: "pause" if anything
    is playing (the playhead stays where it stopped), else "play" from a
    video editor showing the scene the edits go to (editor: one was found),
    since that plays the scene it shows, which in Blender 5 can differ from
    the window's. With no such editor, "play_window" if a window shows that
    scene itself (window), else None: playing from anywhere else would play
    another scene."""
    if playing:
        return "pause"
    if editor:
        return "play"
    if window:
        return "play_window"
    return None


def undo_blocked(modal_operators, interface_locked):
    """Why an undo must wait, or None. An undo frees and reloads Blender's
    data, so it is refused while a modal operator (a strip being dragged, a
    knife, a box select) still points into that data, and while a render has
    locked the interface. A refused undo is dropped, not kept for later."""
    if modal_operators:
        return "an operator is running (%s)" % ", ".join(sorted(set(modal_operators)))
    if interface_locked:
        return "the interface is locked"
    return None


def cuttable(start, end, frame, locked):
    """Whether a strip covering start..end (end exclusive) is cut at frame:
    the playhead must be strictly inside it, so a strip that starts or ends
    right there is left alone, and a locked one is never cut."""
    return not locked and start < frame < end


def cut_targets(spans, frame):
    """Indexes of the strips the cut button cuts at frame (spans: a list of
    Span): the unlocked strips the playhead is strictly inside, and if any of
    those is selected, only the selected ones."""
    under = [i for i, s in enumerate(spans) if cuttable(s.start, s.end, frame, s.locked)]
    return [i for i in under if spans[i].selected] or under


def follow_selection(spans, frame):
    """For "selection follows playhead": whether each strip (a list of Span)
    should be selected with the playhead at frame. The unlocked strips under
    it (start <= frame < end) are, every other strip isn't. Right after a cut
    that selects the right halves, which start at the playhead."""
    return [not s.locked and s.start <= frame < s.end for s in spans]


# A strip as ripple_plan sees it: start..end (end exclusive), whether it is
# locked (itself or its channel), whether it is selected, and the indexes of
# the strips it is an effect of (an effect is removed with its inputs).
Span = namedtuple("Span", "start end locked selected inputs", defaults=(False, False, ()))

# What a ripple delete does. delete: indexes to remove, each before any of
# its inputs. shift: frames to move later strips left (0 leaves the gap
# open). move: indexes to move, leftmost first. playhead: the frame to put
# the playhead on, or None to leave it.
RipplePlan = namedtuple("RipplePlan", "delete shift move playhead")


def ripple_plan(spans, frame):
    """Plans a ripple delete at frame over spans (a list of Span), or
    returns None when there is nothing to delete.

    Deleted: the unlocked strips under the playhead (start <= frame < end);
    if any of them is selected, only the selected ones. A strip an effect
    depends on goes with the effect, so a strip whose effect is locked is
    kept.

    The gap closes on every channel, so nothing drifts out of sync: it runs
    from the deleted strips' start to their end, trimmed to where every
    channel is empty, and everything starting at or after its end moves left
    by its length. It stays open, and nothing moves, when a strip that is
    kept still covers the playhead or when a locked strip would have to move.
    With the gap closed, the playhead goes to the join."""
    n = len(spans)
    under = [i for i, s in enumerate(spans) if not s.locked and s.start <= frame < s.end]
    chosen = [i for i in under if spans[i].selected] or under
    if not chosen:
        return None
    # Effects of a deleted strip go with it; a locked one keeps its inputs
    dependents = [[] for _ in range(n)]
    for i, s in enumerate(spans):
        for j in s.inputs:
            if 0 <= j < n and j != i:
                dependents[j].append(i)

    def closure(i, seen):
        stack = [i]
        while stack:
            k = stack.pop()
            if k in seen:
                continue
            seen.add(k)
            stack.extend(dependents[k])
        return seen

    delete = set()
    for i in chosen:
        group = closure(i, set())
        if not any(spans[k].locked for k in group):
            delete |= group
    if not delete:
        return None
    order = _effects_first(delete, spans)
    kept = [i for i in range(n) if i not in delete]
    if any(spans[i].start <= frame < spans[i].end for i in kept):
        return RipplePlan(order, 0, [], None)
    lo = min(spans[i].start for i in delete)
    hi = max(spans[i].end for i in delete)
    lo = max([lo] + [spans[i].end for i in kept if spans[i].end <= frame])
    hi = min([hi] + [spans[i].start for i in kept if spans[i].start > frame])
    move = sorted((i for i in kept if spans[i].start >= hi), key=lambda i: (spans[i].start, i))
    if hi <= lo or any(spans[i].locked for i in move):
        return RipplePlan(order, 0, [], None)
    return RipplePlan(order, hi - lo, move, lo)


def _effects_first(delete, spans):
    """delete's indexes, each before the strips it is an effect of: removing
    an input also removes its effects, so they must already be gone."""
    order = []
    left = set(delete)
    while left:
        # A strip goes when no strip still waiting is an effect of it
        needed = {j for i in left for j in spans[i].inputs if j in left and j != i}
        ready = sorted(left - needed) or sorted(left)  # a cycle can't happen; never loop forever
        order.extend(ready)
        left -= set(ready)
    return order


def zoom_plan(clicks):
    """What to run for a zoom of clicks steps: ("in" or "out", times), or
    None for nothing."""
    if clicks > 0:
        return ("in", clicks)
    if clicks < 0:
        return ("out", -clicks)
    return None


def zoom_anchor(x, width):
    """Before zooming around the playhead drawn at x pixels in a region width
    pixels wide: (pan, target). pan is how far to pan first, in pixels (a
    positive pan moves the view right, so the playhead moves left); target is
    where the playhead should sit after the zoom. On screen, it stays where it
    is; off screen, the view first centers on it."""
    if 0 <= x <= width:
        return 0, x
    center = width // 2
    return int(round(x - center)), center


def zoom_correction(x, target):
    """The pan, in pixels, that moves the playhead from x back to target."""
    return int(round(x - target))


def scrub_target(frame, delta, start, end, clamp):
    """Where the playhead lands after moving delta frames. With clamp, it
    stops at the scene's start and end frames, but a playhead already outside
    that range is never pulled back past where it is."""
    target = frame + delta
    if not clamp:
        return target
    lo = min(start, frame)
    hi = max(end, frame)
    return max(lo, min(hi, target))


def bridge_host(text):
    """The bridge's IP address, or None. Names other than localhost are
    refused, so connecting never waits on a DNS lookup on Blender's UI thread."""
    text = text.strip()
    if text.lower() == "localhost":
        return "127.0.0.1"
    try:
        return str(ipaddress.ip_address(text))
    except ValueError:
        return None
