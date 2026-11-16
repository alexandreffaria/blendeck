---
id: "202611163di7"
title: When Blender freezes and button presses pile up, which ones still count?
status: resolved
kind: choice
priority: normal
created: 2026-11-16T00:04:52Z
created_by: claude-code
tags: [latency, edits]
depends_on: []
recommendation:
  option: all
  why: >-
    With playback paused, every press then does what it would have done without the freeze,
    in the same order. Pick "fresh" if you tend to press again when nothing seems to happen.
options:
  - id: all
    title: Every press, in order
    summary: Presses made during the freeze are all applied in the order they were made, up to 64 per freeze.
    pros:
      - No press made by hand is lost
      - "Paused: same result as with no freeze"
    cons:
      - "Repeated presses all count: five undos are five undos"
      - After a long freeze, several edits land at once, unseen
  - id: fresh
    title: Only presses from the last half second
    summary: Cuts, ripple deletes and undos older than 0.5 s when Blender wakes are thrown away, and the panel says how many.
    pros:
      - Presses repeated while nothing happened don't all fire
      - Nothing happens long after you pressed it
    cons:
      - A kept delete can lose the cut it relied on
      - A press you meant must be redone
  - id: capped
    title: As today, a few per freeze
    summary: The first 4 undos, 8 cuts and 8 ripple deletes of a freeze are applied, and the rest are dropped without a word.
    pros:
      - A stuck or bouncing button can't undo a whole session
      - Nothing to build
    cons:
      - Presses past the limit vanish silently
resolution:
  choice: all
  resolved_at: 2026-11-16T00:11:54Z
applied_at: 2026-11-16T00:23:00Z
---

## Context

The options only differ when Blender freezes for more than about half a second (a save, a
big undo, a proxy build), which is rare; an ordinary slow frame (about 0.2 s) is the same
under all three. This is about cut, ripple delete and undo presses; knob turns and play
and shuttle presses stay as they are. Under every option, a press made while playing lands
where the playhead has got to.
