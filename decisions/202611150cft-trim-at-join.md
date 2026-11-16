---
id: "202611150cft"
title: Where two clips touch, what should the trim knob do?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T21:45:29Z
created_by: claude-code
tags: [trim, controls]
depends_on: []
recommendation:
  option: roll-fn-shrink
  why: >-
    Depends on whether you mostly retime cuts (roll) or shorten one clip (shrink) at joins;
    roll with Fn for shrink gives both and keeps what's built.
options:
  - id: roll
    title: Roll the cut
    summary: >-
      Both handles move together: right lengthens the first clip and shortens the next by
      the same amount.
    pros:
      - No gap left, nothing later moves
      - Retimes a cut in one turn
    cons:
      - A turn at a join always changes the neighbor too
  - id: roll-fn-shrink
    title: Roll, and Fn shrinks one side
    summary: >-
      A plain turn rolls the cut as built; with Fn held (unbound on this knob today), it only
      shortens the clip you turn into, opening a gap.
    pros:
      - Both behaviors on one knob
      - Keeps the plain turn as built
    cons:
      - Two behaviors at a join to remember
  - id: shrink
    title: Shrink the side you turn into
    summary: >-
      Left shortens the first clip's end, right the next clip's start, opening a gap; the
      neighbor never changes.
    pros:
      - Simplest rule, never touches a neighbor
    cons:
      - Can't lengthen anything at a join
      - Leaves a gap to close afterwards
  - id: ripple
    title: Ripple, pushing later clips
    summary: >-
      Right lengthens the first clip's end, left the next clip's start; either way every
      later clip is pushed right to make room.
    pros:
      - Lengthens either clip with no gap step
    cons:
      - Can't shorten either clip at a join
      - Later clips slip against a music bed spanning the cut, as with the move knob
resolution:
  choice: roll-fn-shrink
  resolved_at: 2026-11-15T23:49:19Z
applied_at: 2026-11-16T00:44:42Z
---

## Context

The trim knob moves the clip edges at the playhead, picture and sound together. After a
gap only one clip's edge is there, but the jump knob also lands on every cut where two
clips touch. The PR built "Roll the cut".
