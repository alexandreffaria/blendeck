---
id: "20261115msw5"
title: What should ripple delete do when closing the gap would move a locked strip or channel?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T07:18:07Z
created_by: claude-code
tags: [controls, ripple-delete]
depends_on: ["202611153gow"]
recommendation:
  option: keep-gap
  why: >-
    Locked means "don't move this", and keeping the gap never breaks sync; pick
    locked-channel-stays if you lock a music channel so deletes leave it alone.
options:
  - id: keep-gap
    title: Delete, but leave the gap open
    summary: The strips are deleted and nothing moves.
    pros:
      - A locked strip never moves
      - Nothing drifts out of sync
    cons:
      - One locked strip later on stops every ripple
      - No sign the gap didn't close
  - id: move-them
    title: Move locked strips too
    summary: >-
      Locked strips and strips on locked channels move left with everything else; they are
      only never deleted.
    pros:
      - The gap always closes
    cons:
      - Ignores the lock, which is meant to stop moving
  - id: do-nothing
    title: Delete nothing
    summary: The press deletes nothing and moves nothing.
    pros:
      - A locked strip never moves
      - You notice the press did nothing
    cons:
      - One locked strip later on stops every delete
  - id: locked-channel-stays
    title: Locked channels sit out
    summary: >-
      A locked channel never moves and is ignored when checking the gap is empty; a locked
      strip on an unlocked channel still keeps the gap open.
    pros:
      - Lock the music channel and ripple deletes close gaps under it
    cons:
      - A locked channel drifts out of sync with the rest
resolution:
  choice: keep-gap
  resolved_at: 2026-11-15T07:30:15Z
---

## Context

Closing the gap moves every later strip left, and some of them may be locked. The button now
does the recommended option.
