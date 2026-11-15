---
id: "202611153dil"
title: What should the scrub knob do at the scene's first and last frame by default?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T04:40:24Z
created_by: claude-code
tags: [ux, timeline]
depends_on: []
recommendation:
  option: stop
  why: >-
    At 10 frames per click, overshooting the end on a fast spin seems likely. Pick free if
    you often work outside the frame range on purpose, such as on handles or extending a cut.
options:
  - id: stop
    title: Stop at the ends
    summary: >-
      Turning past frame 250 of a 1–250 scene leaves the playhead on 250; a playhead already
      outside the range can't go further out.
    pros:
      - Fast spins can't overshoot into unrendered frames
    cons:
      - Going outside the range needs the mouse or unticking the box
  - id: free
    title: Move freely
    summary: The knob moves the playhead anywhere, like dragging it with the mouse.
    pros:
      - Same feel as Blender's own scrubbing
      - Can inspect or extend past the ends
    cons:
      - Easy to spin past the end and lose your place
resolution:
  choice: stop
  resolved_at: 2026-11-15T04:45:16Z
applied_at: 2026-11-15T06:04:08Z
---

## Context

Both behaviours already exist behind a "Stop at scene start and end" checkbox in the add-on;
this sets what it starts as (now: ticked). Blender's own scrubbing lets the playhead go past
the scene's ends.
