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
# Pan per tick at most, in percent of the view's width either way: four
# views. A fast spin at 5% a click sends far less; more is a flood.
MAX_PAN_PER_TICK = 400
# Cut, undo and ripple delete presses applied per tick at most, all three
# together. One read holds every press made while Blender was busy, and each
# of them counts, in the order it was made (decision 202611163di7): nobody
# presses this many by hand during one freeze, so more is a flood. Presses
# past it are dropped and reported (see presses_dropped).
MAX_PRESSES_PER_TICK = 64
# Play / pause presses applied per tick at most. Each press toggles, so two
# in a row with nothing between cancel out and are dropped together.
MAX_TOGGLES_PER_TICK = 8
# Playback speeds, in hundredths of normal speed: 100 is 1x forward, -100
# is 1x backwards, 0 is stopped. Each press of the forward or backwards
# button adds SHUTTLE_STEP (0.25x, decision 202611153pfo), up to
# MAX_SHUTTLE_SPEED (8x, decision 2026111504tn) either way.
NORMAL_SPEED = 100
SHUTTLE_STEP = 25
MAX_SHUTTLE_SPEED = 800
# Forward / backwards presses applied per tick at most.
MAX_SHUTTLES_PER_TICK = 8
# Cuts the jump knob moves over per tick at most, either way. A fast spin
# sends a few clicks a tick; more than this is a flood.
MAX_JUMPS_PER_TICK = 32
# Move knob commands applied per tick at most. Moves of one kind in a row
# are added up, so this only bounds a flood that keeps switching between
# the knob with and without Fn.
MAX_MOVES_PER_TICK = 32
# Trim knob commands applied per tick at most, like MAX_MOVES_PER_TICK.
MAX_TRIMS_PER_TICK = 32
# Playback the add-on drives has sound going forward up to MAX_AUDIO_SPEED
# (5x), at its normal pitch (decision 202611150o5i); faster, and backwards
# (decision 2026111598qg), it is silent. Sound is
# started AUDIO_AHEAD seconds before it is heard, and at most
# MAX_AUDIO_SOUNDS sounds play or wait at once.
MAX_AUDIO_SPEED = 500
AUDIO_AHEAD = 2.0
MAX_AUDIO_SOUNDS = 32
# A scrub knob click plays the frames it moved over, landing frame included
# (decision 20261115kntq); clicks added up in one read play at most the
# last MAX_SCRUB_SECONDS of the move, nearest the landing frame.
MAX_SCRUB_SECONDS = 0.5


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


def total_pan(messages):
    """Adds up the percent of every pan command (positive shows later
    frames), ignoring malformed ones, and caps the result at
    MAX_PAN_PER_TICK either way."""
    total = 0
    for m in messages:
        if m.get("type") != "pan":
            continue
        clicks = m.get("clicks")
        if isinstance(clicks, int) and not isinstance(clicks, bool):
            total += clicks
    return max(-MAX_PAN_PER_TICK, min(MAX_PAN_PER_TICK, total))


def wants_view_all(messages):
    """Whether messages hold a view-all press. Presses in one read are one:
    fitting the view twice changes nothing."""
    return any(m.get("type") == "view_all" for m in messages)


def wants_save(messages):
    """Whether messages hold a save press. Presses in one read are one:
    saving twice in a row writes the same file."""
    return any(m.get("type") == "save" for m in messages)


# The bridge sends at most this many hints, modifiers per hint and
# characters per text (config.MaxHint*); more than that is cut here too, so
# the list stays small whatever is on the other end.
MAX_HINTS = 40
MAX_HINT_HELD = 2
MAX_HINT_LEN = 40


def _hint_text(value):
    """value as one line of at most MAX_HINT_LEN characters, or "" when it
    isn't text."""
    if not isinstance(value, str):
        return ""
    text = " ".join(value.split())
    if len(text) > MAX_HINT_LEN:
        text = text[:MAX_HINT_LEN - 1] + "…"
    return text


def _held_text(plain, name):
    """name without what it repeats of plain: "Scrub timeline (fine)" next
    to "Scrub timeline" is "fine"."""
    if plain and name.startswith(plain):
        rest = name[len(plain):].strip(" ,:-")
        if rest.startswith("(") and rest.endswith(")"):
            rest = rest[1:-1].strip()
        if rest:
            return rest
    return name


def hints(messages):
    """What the controls do, from the newest {"type":"hints"} in messages:
    (profile, [(control, text), ...], show) with one pair per control, like
    ("D2", "Scrub timeline · Fn: fine"); show says whether to draw the
    list now. None when messages hold no hints, so what there is stays. Anything malformed is left out
    rather than raising: the bridge may be newer or older than this."""
    found = None
    for m in messages:
        if m.get("type") == "hints":
            found = m
    if found is None:
        return None
    lines = []
    items = found.get("hints")
    for h in items if isinstance(items, list) else []:
        if len(lines) >= MAX_HINTS:
            break
        if not isinstance(h, dict):
            continue
        control = _hint_text(h.get("control"))
        plain = _hint_text(h.get("name"))
        parts = [plain] if plain else []
        held = h.get("held")
        for hh in (held if isinstance(held, list) else [])[:MAX_HINT_HELD]:
            if not isinstance(hh, dict):
                continue
            modifier, name = _hint_text(hh.get("modifier")), _hint_text(hh.get("name"))
            if modifier and name:
                parts.append("%s: %s" % (modifier, _held_text(plain, name)))
        if control and parts:
            lines.append((control, " · ".join(parts)))
    return _hint_text(found.get("profile")), lines, found.get("show") is True


def hint_rows(count, height, line_height):
    """How many of count lines go in each column so the list fits in
    height pixels: as few columns as that takes, filled evenly. At least 1,
    however little room there is."""
    if count < 1:
        return 1
    fit = max(1, min(count, int(height // line_height) if line_height > 0 else count))
    columns = -(-count // fit)
    return -(-count // columns)


def pan_pixels(width, percent):
    """How far to pan a region width pixels wide for percent of its width,
    in pixels (positive shows later frames)."""
    return int(round(width * percent / 100.0))


# Latency samples kept between two reports at most, and the oldest a
# command may be and still count: older than that is a clock that was set,
# not a wait.
MAX_LATENCY_SAMPLES = 256
MAX_WAIT_MS = 60_000


# A picture that took this long to draw after a command is slow: the
# playhead visibly lags the hand. With everything set right, a jump to a
# place never shown costs about 110 ms on long footage; under prefetch or
# without proxies it is several hundred.
SLOW_PICTURE_MS = 150
# What the add-on can name as the reason, in the order it is shown.
SLOW_CAUSES = ("proxies_off", "prefetch_on", "cache_window")
_CAUSE_TEXT = {
    "proxies_off": "Use Proxies is off in the preview",
    "prefetch_on": "Prefetch Frames is on",
    "cache_window": "Cache Window is on",
}


def slow_causes(proxies_off, prefetch_on, cache_window):
    """The reasons to name for a slow picture, from what is set."""
    found = (("proxies_off", proxies_off), ("prefetch_on", prefetch_on), ("cache_window", cache_window))
    return [name for name, on in found if on]


def slow_note(picture_max, causes):
    """The lines the panel shows after a slow picture: how slow, then each
    known reason. Empty when the picture wasn't slow."""
    if not picture_max >= SLOW_PICTURE_MS:
        return []
    lines = ["Slow picture: %d ms" % round(picture_max)]
    lines += [_CAUSE_TEXT[c] for c in SLOW_CAUSES if c in causes]
    return lines


def oldest_stamp(messages):
    """The earliest "t" (when the bridge took the command, in milliseconds
    since 1970) among messages, or None when none carries a usable one."""
    oldest = None
    for m in messages:
        t = m.get("t")
        if type(t) not in (int, float) or not t > 0:
            continue
        if oldest is None or t < oldest:
            oldest = t
    return oldest


def moves_picture(messages):
    """Whether messages hold an edit that redraws the preview even when the
    playhead stays: a cut, an undo, a ripple delete, a move or a trim."""
    return any(m.get("type") in ("cut", "undo", "ripple_delete", "move", "trim") for m in messages)


def _med_max(values):
    if not values:
        return 0.0, 0.0
    ordered = sorted(values)
    return round(ordered[len(ordered) // 2], 1), round(ordered[-1], 1)


def latency_report(waits, pictures, totals, tick, dropped):
    """The message that tells the bridge how long commands took since the
    last report, all in milliseconds, or None when there is nothing to say.
    waits: from the bridge taking a command to the add-on applying it.
    pictures: from there to the preview having drawn. totals: both. tick:
    the longest gap between two ticks. dropped: presses left out of a read."""
    if not waits and not dropped:
        return None
    wait_med, wait_max = _med_max(waits)
    picture_med, picture_max = _med_max(pictures)
    total_med, total_max = _med_max(totals)
    return {
        "type": "latency",
        "samples": len(waits),
        "wait_med": wait_med,
        "wait_max": wait_max,
        "picture_med": picture_med,
        "picture_max": picture_max,
        "total_med": total_med,
        "total_max": total_max,
        "tick_max": round(tick, 1),
        "dropped": dropped,
    }


def edit_steps(messages):
    """The scrubs, cuts, undos, ripple deletes and play / pause presses in
    messages, in order: ("scrub", frames), ("cut", None), ("undo", None),
    ("ripple_delete", None) and ("play_pause", None). Order matters, since
    a cut or delete lands where the playhead is at that moment and an undo
    takes back what came before it. Scrubs in a row are added up, and cuts
    in a row with no scrub between become one (a second cut at the same
    frame does nothing). Every undo is kept, one per press, never merged,
    and so is every ripple delete (two in a row both do something: the
    second deletes what slid under the playhead). Cuts, undos and ripple
    deletes past MAX_PRESSES_PER_TICK, all three together, are dropped (see
    presses_dropped); scrubs after them still count. Play / pause presses in
    a row with nothing between are a toggle each, so a pair cancels out: an
    even run leaves nothing, an odd one a single toggle. At most
    MAX_TOGGLES_PER_TICK presses count. Forward and backwards presses are
    ("shuttle", 1) and ("shuttle", -1), one each, never merged, up to
    MAX_SHUTTLES_PER_TICK. Jumps between video edits are ("jump", clicks)
    and jumps between all cuts, sound included ("sound": true), are
    ("jump_sound", clicks): jumps of one kind in a row are added up, and at
    most MAX_JUMPS_PER_TICK clicks count either way in one read, both kinds
    together. Moves of the selected strips and those after them are
    ("move", frames), of the selected strips and those before them
    ("move_before", frames): moves of one kind in a row are added up, at
    most MAX_MOVES_PER_TICK commands count. Turns of the trim knob are
    ("trim", frames), and with Fn held ("shrink": true) ("trim_shrink",
    frames), added up the same way (shrinks only while they keep one
    direction), at most MAX_TRIMS_PER_TICK commands both kinds together. Malformed commands are ignored."""
    return _edit_steps(messages)[0]


def presses_dropped(messages):
    """How many cut, undo and ripple delete presses in messages edit_steps
    leaves out because more than MAX_PRESSES_PER_TICK came in one read. A
    cut merged into the cut before it isn't one: it would have done
    nothing."""
    return _edit_steps(messages)[1]


def _edit_steps(messages):
    """(steps, presses dropped): see edit_steps and presses_dropped."""
    steps = []
    presses = 0
    dropped = 0
    toggles = 0
    shuttles = 0
    jumps = 0
    moves = 0
    trims = 0
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
        elif kind in ("cut", "undo", "ripple_delete"):
            if kind == "cut" and steps and steps[-1][0] == "cut":
                continue
            if presses >= MAX_PRESSES_PER_TICK:
                dropped += 1
                continue
            presses += 1
            steps.append((kind, None))
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
            sound = m.get("sound", False)
            if type(clicks) is not int or clicks == 0 or type(sound) is not bool:
                continue
            room = MAX_JUMPS_PER_TICK - jumps
            clicks = max(-room, min(room, clicks))
            if clicks == 0:
                continue
            jumps += abs(clicks)
            step = "jump_sound" if sound else "jump"
            if steps and steps[-1][0] == step:
                steps[-1] = (step, steps[-1][1] + clicks)
                if steps[-1][1] == 0:
                    steps.pop()  # right then left: back where it was
            else:
                steps.append((step, clicks))
        elif kind == "move":
            frames = m.get("frames")
            before = m.get("before", False)
            if type(frames) is not int or frames == 0 or type(before) is not bool or moves >= MAX_MOVES_PER_TICK:
                continue
            moves += 1
            step = "move_before" if before else "move"
            if steps and steps[-1][0] == step:
                steps[-1] = (step, steps[-1][1] + frames)
                if steps[-1][1] == 0:
                    steps.pop()  # right then left: back where it was
            else:
                steps.append((step, frames))
        elif kind == "trim":
            frames = m.get("frames")
            if type(frames) is not int or frames == 0 or trims >= MAX_TRIMS_PER_TICK:
                continue
            trims += 1
            step = "trim_shrink" if m.get("shrink") is True else "trim"
            # Shrinking never grows anything back, so a turn the other way
            # is its own step: added up, right 3 then left 1 would shrink 2
            # where read apart it shrinks 3 and the left does nothing
            if steps and steps[-1][0] == step and (step == "trim" or (steps[-1][1] > 0) == (frames > 0)):
                steps[-1] = (step, steps[-1][1] + frames)
                if steps[-1][1] == 0:
                    steps.pop()  # right then left: back where it was
            else:
                steps.append((step, frames))
    return steps, dropped


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
    backwards (-1) button, from current (both in hundredths, see
    NORMAL_SPEED). Already going that way: SHUTTLE_STEP faster, up to
    MAX_SHUTTLE_SPEED. Going the other way faster than 1x: SHUTTLE_STEP
    slower, easing off down to 1x. Going the other way at 1x, or stopped:
    normal speed in this direction (decision 20261115618c)."""
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


def shuttle_has_audio(speed):
    """Whether playback the add-on drives at speed (in hundredths) has sound:
    forward, up to MAX_AUDIO_SPEED."""
    return 0 < speed <= MAX_AUDIO_SPEED


def audio_plan(strips, origin, elapsed, fps, speed, started, playing, ahead=AUDIO_AHEAD):
    """The sounds to start now for playback the add-on drives, which left
    frame origin elapsed seconds ago at speed (in hundredths). strips are
    (key, start, end, content_start, sound_offset): the strip's frames
    [start, end) on the timeline, the frame its sound's first sample lands
    on, and the seconds of sound skipped before that. Every strip that is
    heard within ahead seconds and whose key isn't in started (every strip
    started since playback last picked up) gives (key, from_s, to_s,
    delay): play its sound from from_s to to_s seconds, after delay
    seconds. With the playing sounds already playing or waiting, at most
    MAX_AUDIO_SOUNDS."""
    if not shuttle_has_audio(speed) or fps <= 0:
        return []
    rate = fps * speed / NORMAL_SPEED  # timeline frames per second
    now = origin + elapsed * rate
    horizon = now + ahead * rate
    room = MAX_AUDIO_SOUNDS - playing
    plan = []
    for key, start, end, content_start, sound_offset in sorted(strips, key=lambda s: s[1]):
        if room <= 0:
            break
        if key in started or end <= now or start >= horizon:
            continue
        frm = max(start, now)
        from_s = max(0.0, (frm - content_start) / fps + sound_offset)
        to_s = (end - content_start) / fps + sound_offset
        if to_s <= from_s:
            continue
        plan.append((key, from_s, to_s, (frm - now) / rate))
        room -= 1
    return plan


def scrub_frames(frm, to, fps):
    """The timeline frames [lo, hi) a scrub from frame frm to frame to
    plays, forward in time: the frames it moved over, landing frame
    included and the one it left not, so going forward the sound ends on
    the playhead and going backwards it starts there. A 1-frame move
    plays just the landing frame. A move longer than MAX_SCRUB_SECONDS is
    cut to the part nearest the landing frame. None if it didn't move."""
    if to == frm or fps <= 0:
        return None
    most = max(1, int(MAX_SCRUB_SECONDS * fps))
    if to > frm:
        return max(frm + 1, to + 1 - most), to + 1
    return to, min(frm, to + most)


def scrub_sounds(strips, frm, to, fps):
    """What a scrub from frame frm to frame to plays: (key, from_s, to_s,
    delay) for every strip (as for audio_plan) heard in the frames
    scrub_frames gives: its sound from from_s to to_s seconds, after delay
    seconds, so strips one after another sound one after another. At most
    MAX_AUDIO_SOUNDS."""
    span = scrub_frames(frm, to, fps)
    if span is None:
        return []
    lo, hi = span
    plan = []
    for key, start, end, content_start, sound_offset in sorted(strips, key=lambda s: s[1]):
        if len(plan) >= MAX_AUDIO_SOUNDS:
            break
        a, b = max(lo, start), min(hi, end)
        if a >= b:
            continue
        from_s = max(0.0, (a - content_start) / fps + sound_offset)
        to_s = (b - content_start) / fps + sound_offset
        if to_s > from_s:
            plan.append((key, from_s, to_s, (a - lo) / fps))
    return plan


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


# A strip as ripple_plan and move_plan see it: start..end (end exclusive),
# whether it is locked (itself or its channel), whether it is selected, the
# indexes of the strips it is an effect of (an effect is removed with its
# inputs, and moves with them) and its channel.
Span = namedtuple("Span", "start end locked selected inputs channel", defaults=(False, False, (), 0))

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


# What a move does. move: indexes of the strips to move, in the order to
# move them so none lands on one that hasn't moved yet. shift: frames,
# right when positive.
MovePlan = namedtuple("MovePlan", "move shift")


def move_plan(spans, frames, before=False):
    """Plans the move knob over spans (a list of Span): frames to the right,
    or left when negative, like G in Blender. Returns None when nothing
    moves.

    What moves: the selected unlocked strips and, on every channel, every
    strip that starts at or after the earliest of them starts, so picture,
    sound and titles after it stay in sync; with before, every strip that
    ends at or before the latest of them ends instead. Effects move with
    their inputs and inputs with their effects; a group of those with a
    locked strip in it stays, and so does every locked strip.

    Moving strips stop against strips that stay on their channel, as far as
    the gap between them allows, so nothing goes over another strip; when
    there is no gap left, nothing moves."""
    n = len(spans)
    chosen = [i for i, s in enumerate(spans) if s.selected and not s.locked]
    if not chosen or frames == 0:
        return None
    if before:
        edge = max(spans[i].end for i in chosen)
        wanted = set(chosen) | {i for i, s in enumerate(spans) if s.end <= edge}
    else:
        edge = min(spans[i].start for i in chosen)
        wanted = set(chosen) | {i for i, s in enumerate(spans) if s.start >= edge}
    # Strips tied by effects move together, or not at all
    links = [set() for _ in range(n)]
    for i, s in enumerate(spans):
        for j in s.inputs:
            if 0 <= j < n and j != i:
                links[i].add(j)
                links[j].add(i)
    move = set()
    seen = set()
    for i in sorted(wanted):
        if i in seen:
            continue
        group = set()
        stack = [i]
        while stack:
            k = stack.pop()
            if k in group:
                continue
            group.add(k)
            stack.extend(links[k])
        seen |= group
        if not any(spans[k].locked for k in group):
            move |= group
    if not move:
        return None
    shift = frames
    stay = {}  # channel: the strips that stay on it
    for i in range(n):
        if i not in move:
            stay.setdefault(spans[i].channel, []).append(spans[i])
    for i in move:
        m = spans[i]
        for o in stay.get(m.channel, ()):
            if shift > 0 and o.start >= m.end:
                shift = min(shift, o.start - m.end)
            elif shift < 0 and o.end <= m.start:
                shift = max(shift, o.end - m.start)
    if shift == 0:
        return None
    order = sorted(move, key=lambda i: (spans[i].start, i), reverse=shift > 0)
    return MovePlan(order, shift)


# A strip as trim_plan sees it: start..end (end exclusive), whether it is
# locked (itself or its channel), its channel, whether it follows other
# strips (an effect with inputs: its ends are theirs, it is never trimmed
# itself), and how far its handles can go: lo and hi are the first frame
# of its content and the frame after the last one, or None where a handle
# can go as far as it likes (a color or a text strip, a single image).
Clip = namedtuple("Clip", "start end locked channel follows lo hi", defaults=(False, 0, False, None, None))

# What the trim knob does. starts and ends: indexes of the strips whose
# start or end handle moves. shift: frames, right when positive. playhead:
# where the playhead goes, on the moved handles.
TrimPlan = namedtuple("TrimPlan", "starts ends shift playhead")


def trim_plan(clips, frame, frames, shrink=False):
    """Plans the trim knob over clips (a list of Clip) with the playhead at
    frame: the handles at the playhead move frames right, or left when
    negative, and the playhead goes with them. Returns None when nothing
    moves.

    With shrink (the trim knob with Fn held, decision 202611150cft) only
    the clips turned into get shorter: right moves only starts, left only
    ends. Where two clips meet, that opens a gap instead of rolling the
    cut, and the neighbor never changes; a lone handle turned the other way
    stays put.

    Which handles: the start of every unlocked strip that starts at the
    playhead and the end of every one that ends there, on every channel, so
    a picture and its sound stay in sync. Where one strip ends and the next
    starts, both move: the cut between them rolls.

    How far: a handle never goes past its strip's content (lo, hi), never
    leaves the strip less than one frame long, and stops against a strip
    that stays on its channel. An effect whose edge is there (it follows
    its inputs) stops them against the strips on its own channel too.
    Every handle moves the same amount, so they all stop at the first
    limit; with none left that way, nothing moves."""
    if frames == 0:
        return None
    starts = [i for i, c in enumerate(clips) if not c.locked and not c.follows and c.start == frame]
    ends = [i for i, c in enumerate(clips) if not c.locked and not c.follows and c.end == frame]
    if shrink:
        if frames > 0:
            ends = []
        else:
            starts = []
    if not starts and not ends:
        return None
    moving_starts, moving_ends = set(starts), set(ends)
    # An effect with an edge there grows and shrinks with its inputs, so it
    # must not run into its own neighbors either. Shrinking, only the edges
    # of the side that moves follow.
    follow_starts = not shrink or bool(starts)
    follow_ends = not shrink or bool(ends)
    edge_starts = moving_starts | {i for i, c in enumerate(clips) if follow_starts and c.follows and c.start == frame}
    edge_ends = moving_ends | {i for i, c in enumerate(clips) if follow_ends and c.follows and c.end == frame}
    shift = frames
    for i in starts:
        c = clips[i]
        if shift > 0:
            shift = min(shift, c.end - 1 - c.start)
        elif c.lo is not None:
            shift = max(shift, min(0, c.lo - c.start))
    for i in ends:
        c = clips[i]
        if shift < 0:
            shift = max(shift, c.start + 1 - c.end)
        elif c.hi is not None:
            shift = min(shift, max(0, c.hi - c.end))
    for i in sorted(edge_starts):
        c = clips[i]
        for j, o in enumerate(clips):
            # A neighbor whose end rolls with this start is no limit
            if shift < 0 and j != i and o.channel == c.channel and o.end <= c.start and j not in edge_ends:
                shift = max(shift, o.end - c.start)
    for i in sorted(edge_ends):
        c = clips[i]
        for j, o in enumerate(clips):
            if shift > 0 and j != i and o.channel == c.channel and o.start >= c.end and j not in edge_starts:
                shift = min(shift, o.start - c.end)
    if shift == 0 or shift * frames < 0:
        return None
    return TrimPlan(starts, ends, shift, frame + shift)


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


def center_pan(x, width):
    """The pan, in pixels, that puts the playhead drawn at x in the middle
    of a region width pixels wide (a positive pan moves the view right)."""
    return int(round(x - width // 2))


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
