---
id: "20261115ftlg"
title: What should Fn + the play button do?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T08:44:09Z
created_by: claude-code
tags: [playback, play-pause, fn]
depends_on: ["20261115nsi1"]
recommendation:
  option: same
  why: >-
    You removed the Fn variants of the ripple delete and undo buttons; pick reverse only if you
    won't use the new backwards button, other way if you want both pause behaviours.
options:
  - id: same
    title: The same play / pause
    summary: Fn makes no difference, as with the undo and ripple delete buttons.
    pros:
      - Nothing to remember
      - Same as the other buttons
    cons:
      - "One button, one job"
  - id: reverse
    title: Play backwards
    summary: Fn + the button plays in reverse (Blender's Shift+Ctrl+Space); any press of the button while playing pauses.
    pros:
      - Back up to a cut point without the knob
    cons:
      - One more combination to remember
      - Repeats the new backwards button
  - id: other-pause
    title: Pause the other way
    summary: "While playing, Fn + the button pauses the way the plain press doesn't: jump back to where play started, or stay; while paused, it plays."
    pros:
      - Both pause behaviours, one on each
      - Replay or stop-and-cut, one press each
    cons:
      - One more combination to remember
resolution:
  choice: same
  resolved_at: 2026-11-15T09:49:01Z
applied_at: 2026-11-15T09:49:38Z
---

## Context

A separate backwards button (note 66) is now built too. Built for now: Fn + the play button just
plays or pauses.
