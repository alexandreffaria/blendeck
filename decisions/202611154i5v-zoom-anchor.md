---
id: "202611154i5v"
title: Where should the timeline zoom toward?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T05:25:41Z
created_by: claude-code
tags: [controls, zoom]
depends_on: []
recommendation:
  option: playhead
  why: >-
    If you tend to scrub to a spot and then zoom to look closer, playhead keeps that spot
    in view; pick center if you often zoom while looking at strips away from the playhead.
options:
  - id: center
    title: The middle of the view
    summary: Zooming keeps whatever is in the middle of the timeline in the middle.
    pros:
      - Same as Blender's zoom keys (by default)
    cons:
      - "An off-center playhead drifts out: a quarter of the way in, gone after ~9 clicks"
  - id: playhead
    title: The playhead
    summary: >-
      The playhead stays at its spot on screen while the view scales around it; if it is
      off screen, a click first centers the view on it, then zooms.
    pros:
      - The frame you're on never leaves the view
    cons:
      - Differs from Blender's keyboard zoom
      - Needs extra work in the add-on
resolution:
  choice: playhead
  resolved_at: 2026-11-15T05:39:31Z
applied_at: 2026-11-15T05:43:50Z
---

## Context

Turning the zoom knob scales the timeline around some point. Right now it is the middle
of the view.
