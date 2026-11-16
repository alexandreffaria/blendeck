---
id: "20261115nsi1"
title: When you pause with the play button, where should the playhead be?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T08:44:09Z
created_by: claude-code
tags: [playback, play-pause]
depends_on: []
recommendation:
  option: stay
  why: >-
    Pick stay if you pause to cut where you stopped, since cut and ripple delete act at the
    playhead; pick back if you mostly replay passages to judge them.
options:
  - id: stay
    title: Stay where it stopped
    summary: Pausing leaves the playhead on the frame playback reached, like Space in Blender.
    pros:
      - Pause, then cut right there
      - Same as Space
    cons:
      - Watching a passage again needs the scrub knob
  - id: back
    title: Jump back to where play started
    summary: Pausing puts the playhead back on the frame where you pressed play, like Esc in Blender.
    pros:
      - Watch a passage again and again without the knob
    cons:
      - To cut where you stopped, you scrub there first
resolution:
  choice: stay
  resolved_at: 2026-11-15T09:48:12Z
applied_at: 2026-11-15T09:49:38Z
---

## Context

The new button plays and pauses the video editor. Built for now: the playhead stays where it
stopped.
