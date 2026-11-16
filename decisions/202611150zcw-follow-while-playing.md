---
id: "202611150zcw"
title: Should selection keep following the playhead while it plays?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T08:44:09Z
created_by: claude-code
tags: [playback, selection]
depends_on: []
recommendation:
  option: follow
  why: >-
    It keeps one rule, paused or playing; pick hold if you hand-pick strips before playing to cut
    just those while it plays.
options:
  - id: follow
    title: Keep following
    summary: The selection changes as playback passes each strip, so it always matches what's under the playhead.
    pros:
      - Cut while playing takes what you see
      - Same rule as when paused
    cons:
      - Selection keeps changing on screen
      - Playing replaces a hand-picked selection right away
  - id: hold
    title: Hold still, catch up on pause
    summary: Selection stays as it was while playing; when playback stops, the strips under the playhead are selected.
    pros:
      - A calm timeline while you watch
      - Hand-picked strips stay picked until you pause
    cons:
      - "A cut or delete while playing can skip strips that weren't selected before play"
resolution:
  choice: follow
  resolved_at: 2026-11-15T09:48:07Z
applied_at: 2026-11-15T09:49:39Z
---

## Context

You chose Premiere's "selection follows playhead": the unlocked strips under the playhead are
selected and the rest deselected. It was built to follow during playback too, and still does.
