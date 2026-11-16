---
id: "20261115u2qg"
title: How much faster should each press play, and up to what speed?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T09:36:50Z
created_by: claude-code
tags: [playback, shuttle]
depends_on: []
recommendation:
  option: add
  why: >-
    Even steps are easy to predict for the small speed-ups you asked for; pick double if you
    mostly shuttle to get somewhere far away fast.
options:
  - id: add
    title: "+0.1x a press, up to 4x"
    summary: "1x, 1.1x, 1.2x, 1.3x…; 4x after 30 presses past 1x."
    pros:
      - Even, predictable steps
    cons:
      - Many presses to go really fast
  - id: multiply
    title: "×1.1 a press, up to 4x"
    summary: "1x, 1.1x, 1.21x, 1.33x…; 4x after 15 presses past 1x."
    pros:
      - Small steps first, then speeds up quicker
    cons:
      - Speeds aren't round numbers
  - id: double
    title: Double each press, up to 8x
    summary: "1x, 2x, 4x, 8x, like Premiere's J and L."
    pros:
      - Far in a few presses
    cons:
      - No small speed-ups
resolution:
  choice: add
  resolved_at: 2026-11-15T09:48:25Z
applied_at: 2026-11-15T09:50:31Z
---

## Context

You said "let's say 10% each click". Each option sets both the step and the top speed. Above 1x,
playback is silent. Built for now: +0.1x a press, up to 4x.
