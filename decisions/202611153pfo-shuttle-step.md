---
id: "202611153pfo"
title: How much faster should each press of forward or backwards play?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T18:20:43Z
created_by: claude-code
tags: [playback, shuttle]
depends_on: ["2026111504tn"]
recommendation:
  option: quarter
  why: >-
    A step you can feel that still allows small speed-ups; if the top speed goes to 8x or more,
    half or double saves many presses.
options:
  - id: tenth
    title: "+0.1x a press (as now)"
    summary: "1x, 1.1x, 1.2x…: 10 presses for each 1x faster."
    pros:
      - Finest control over speed
    cons:
      - Each press is hard to notice
  - id: quarter
    title: "+0.25x a press"
    summary: "1x, 1.25x, 1.5x…: 4 presses for each 1x faster."
    pros:
      - Each press is noticeable
    cons:
      - Coarser than now
  - id: half
    title: "+0.5x a press"
    summary: "1x, 1.5x, 2x…: 2 presses for each 1x faster."
    pros:
      - Clearly faster each press
    cons:
      - No gentle speed-ups
  - id: double
    title: Double each press
    summary: "1x, 2x, 4x, 8x…, and the opposite button halves instead of easing off by a step."
    pros:
      - Top speed in a few presses
    cons:
      - Big jumps only
resolution:
  choice: quarter
  resolved_at: 2026-11-15T18:32:27Z
applied_at: 2026-11-15T18:36:40Z
---

## Context

You chose +0.1x earlier and found the speed wasn't going up; measured in Blender, each press
does add 0.1x. The opposite button eases off by the
same step.
