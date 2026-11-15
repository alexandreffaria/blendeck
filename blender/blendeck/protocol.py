"""Wire format and playhead math for the Blendeck link. No bpy here, so it can
be tested without Blender."""

import ipaddress
import json

MAX_LINE = 64 * 1024
# Zoom steps applied per tick at most, so a burst of commands can't keep
# Blender's UI busy zooming. Each step is one zoom operator call per editor.
MAX_ZOOM_PER_TICK = 40


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
