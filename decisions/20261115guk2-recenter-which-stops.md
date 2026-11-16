---
id: "20261115guk2"
title: Which stops should recenter the timeline on the playhead?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T19:38:36Z
created_by: claude-code
tags: [playback, timeline]
depends_on: []
recommendation:
  option: any
  why: >-
    The playhead can be off screen after any stop, so recentering always helps, at the cost of
    the view also moving on stops nobody meant. Pick pauses if undo and renders shouldn't move
    it, controller if Blender's own keys shouldn't.
options:
  - id: any
    title: Every stop
    summary: >-
      Everything that stops playback recenters, including undo, a fast or backwards shuttle
      reaching the end, Space or the pause button in Blender, and a render or scene switch.
    pros:
      - The playhead is always in view after playback
    cons:
      - "Stops nobody meant, like undo or starting a render, move the view too"
      - Stops from Blender move the view, which stock Blender never does
  - id: pauses
    title: Deliberate pauses, from either device
    summary: >-
      Play / pause on the Loupedeck and Space or the pause button in Blender recenter; undo, the
      shuttle reaching the end, renders and scene switches don't.
    pros:
      - The view moves only when you chose to stop
    cons:
      - Space in Blender no longer behaves as stock
      - The playhead can stay off screen after undo or the shuttle ending
  - id: controller
    title: Every stop from the Loupedeck
    summary: >-
      The Loupedeck's play / pause, undo and shuttle reaching the end recenter; stops from Blender
      (Ctrl+Z included), renders and scene switches don't.
    pros:
      - Blender's own keys and buttons behave as stock
    cons:
      - The view stays off the playhead after a stop from Blender
  - id: button
    title: Only the Loupedeck's play / pause button
    summary: Only that button's pause recenters; every other stop leaves the view alone.
    pros:
      - Blender is untouched, and undo never moves the view
    cons:
      - The playhead can stay off screen after any other stop
resolution:
  choice: any
  resolved_at: 2026-11-15T20:16:24Z
applied_at: 2026-11-15T20:25:12Z
---

## Context

You asked to recenter the timeline on the playhead "when we stop". It is merged, keeping the
zoom, and recenters on every stop; the question is which stops should.
