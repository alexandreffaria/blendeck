---
id: "20261115pb4a"
title: When fast or backwards playback reaches the first or last frame, what should happen?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T09:34:25Z
created_by: claude-code
tags: [playback, shuttle]
depends_on: []
recommendation:
  option: stop
  why: >-
    At high speed it is easy to fly past the end, and looping would land you at the other end of
    the edit; pick loop if you shuttle through short edits over and over.
options:
  - id: stop
    title: Stop there
    summary: Playback stops on the first frame (going backwards) or the last (going forward).
    pros:
      - Never jumps to the other end
    cons:
      - Differs from normal-speed playback, which loops
  - id: loop
    title: Loop round
    summary: Playback jumps to the other end and carries on, like Blender's normal playback.
    pros:
      - Same as normal-speed playback
    cons:
      - Easy to overshoot and land at the other end
resolution:
  choice: stop
  resolved_at: 2026-11-15T09:48:18Z
applied_at: 2026-11-15T09:50:31Z
---

## Context

Normal-speed forward play is Blender's own and loops at the end. Faster or backwards playback
is the add-on's; built for now: it stops at the end. Both use the preview range when it's on.
