---
id: "202611150o5i"
title: When playing faster with sound, should voices keep their pitch?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T18:20:43Z
created_by: claude-code
tags: [playback, shuttle, audio]
depends_on: []
recommendation:
  option: keep
  why: >-
    Speech stays easy to follow, so you can find a line by ear; pick chipmunk if you'd rather
    hear clearly how fast it's going.
options:
  - id: keep
    title: Keep the pitch
    summary: At 2x a voice sounds like a fast talker at its own pitch.
    pros:
      - Words stay clear
    cons:
      - Can sound warbly at 3x to 5x
  - id: chipmunk
    title: Raise the pitch with the speed
    summary: At 2x a voice sounds an octave higher, like a tape played fast.
    pros:
      - Speed is obvious by ear
    cons:
      - Words get hard to follow past 2x
resolution:
  choice: keep
  resolved_at: 2026-11-15T18:31:18Z
applied_at: 2026-11-15T18:36:40Z
---

## Context

Faster forward playback now has sound, up to 5x for now. Built for now: the pitch is kept.
