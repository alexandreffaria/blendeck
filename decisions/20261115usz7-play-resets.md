---
id: "20261115usz7"
title: When fast or backwards playback is on, what should play / pause do?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T09:36:50Z
created_by: claude-code
tags: [playback, shuttle, play-pause]
depends_on: ["20261115nsi1"]
recommendation:
  option: stop
  why: >-
    Play / pause then always stops whatever plays; pick normal if you mostly shuttle to a spot
    and then want to watch from there.
options:
  - id: stop
    title: Stop
    summary: Playback stops, the way a plain pause does; the next play / pause plays forward at 1x.
    pros:
      - Play / pause always stops what plays
    cons:
      - From fast forward, two presses to watch at 1x
  - id: normal
    title: Back to 1x forward
    summary: Playback carries on forward at 1x, with sound, whichever way it went; the next press pauses.
    pros:
      - One press from shuttling to watching
    cons:
      - No one-press stop while shuttling
  - id: same-way
    title: Back to 1x the same way
    summary: Fast forward drops to 1x forward with sound; backwards at any speed drops to 1x backwards; the next press pauses.
    pros:
      - Keeps the direction you chose
    cons:
      - No one-press stop while shuttling
      - Backwards at 1x is still silent
resolution:
  choice: stop
  resolved_at: 2026-11-15T09:48:31Z
applied_at: 2026-11-15T09:50:31Z
---

## Context

You said play / pause "resets both directions". Built for now: it stops, and the next play is
at normal speed.
