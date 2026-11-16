---
id: "20261115618c"
title: While playing one way, what should the opposite button do?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T09:34:25Z
created_by: claude-code
tags: [playback, shuttle]
depends_on: ["20261115u2qg"]
recommendation:
  option: switch
  why: >-
    Switch matches Premiere and reverses in one press; pick ease off if you want to back off
    speed without losing direction.
options:
  - id: switch
    title: Switch direction at normal speed
    summary: "At 1.5x forward, the backwards button plays backwards at 1x; its next press is 1.1x backwards."
    pros:
      - One press to reverse
      - Same as Premiere's J and L
    cons:
      - No way to ease off without stopping
  - id: ease
    title: Ease off to 1x, then switch
    summary: "At 1.5x forward, the backwards button gives 1.4x, then 1.3x, down to 1x forward; the next press plays backwards at 1x."
    pros:
      - Back off speed without stopping
    cons:
      - Reversing from 2x takes 11 presses
  - id: stop-first
    title: Stop first, then switch
    summary: "At any speed, the backwards button stops playback; its next press plays backwards at 1x."
    pros:
      - Can't reverse by accident
    cons:
      - Two presses to reverse
      - Play / pause already stops
resolution:
  choice: ease
  resolved_at: 2026-11-15T09:48:44Z
applied_at: 2026-11-15T09:50:30Z
---

## Context

Each press of the forward or backwards button plays faster that way (counts assume +0.1x a
press). Built for now: the opposite button switches direction at normal speed.
