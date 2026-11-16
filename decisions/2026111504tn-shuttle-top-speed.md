---
id: "2026111504tn"
title: How fast should forward and backwards playback go at most?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T18:24:55Z
created_by: claude-code
tags: [playback, shuttle]
depends_on: []
recommendation:
  option: five
  why: >-
    Forward keeps its sound the whole way, a little faster than now; pick 8x or 16x if you often
    cross long edits and don't mind silence there.
options:
  - id: four
    title: "4x (as now)"
    summary: Forward has sound at every speed.
    pros:
      - Forward always has sound
    cons:
      - Slowest across a long edit
  - id: five
    title: 5x
    summary: "Forward has sound at every speed, and the top is 25% faster than now."
    pros:
      - Forward always has sound
    cons:
      - Only a little faster than now
  - id: eight
    title: 8x
    summary: Forward has sound up to 5x and is silent from there to 8x.
    pros:
      - Twice as fast as now at the top
    cons:
      - Silent at the fastest speeds
  - id: sixteen
    title: 16x
    summary: Forward has sound up to 5x and is silent from there to 16x.
    pros:
      - Crosses a long edit fastest
    cons:
      - Silent at most of the range
      - Easy to overshoot
resolution:
  choice: eight
  resolved_at: 2026-11-15T18:32:00Z
applied_at: 2026-11-15T18:36:40Z
---

## Context

Forward playback faster than 1x now plays sound up to 5x, as you asked, though you haven't
heard it yet; backwards stays silent. The top speed is 4x today.
