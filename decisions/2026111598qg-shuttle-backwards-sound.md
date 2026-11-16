---
id: "2026111598qg"
title: Should backwards playback have sound?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T18:20:43Z
created_by: claude-code
tags: [playback, shuttle, audio]
depends_on: []
recommendation:
  option: silent
  why: >-
    Reversed speech carries no words, so it rarely helps you find a spot; pick reversed if you
    find cuts by sound, like a beat or a clap.
options:
  - id: silent
    title: Keep it silent
    summary: Backwards playback has no sound at any speed.
    pros:
      - Nothing distracting while you rewind
    cons:
      - Can't find a spot by sound going back
  - id: reversed
    title: Play the sound reversed, up to 5x
    summary: "Backwards, you hear the audio played in reverse; silent past the same 5x limit as forward."
    pros:
      - Hear beats and claps going back
    cons:
      - Speech is gibberish
resolution:
  choice: silent
  resolved_at: 2026-11-15T18:31:31Z
applied_at: 2026-11-15T18:36:40Z
---

## Context

Faster forward playback now has sound up to 5x; backwards is still silent. Built for now:
silent.
