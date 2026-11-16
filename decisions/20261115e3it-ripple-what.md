---
id: "20261115e3it"
title: Which strips should ripple delete delete?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T07:18:07Z
created_by: claude-code
tags: [controls, ripple-delete]
depends_on: []
recommendation:
  option: selected-else-all
  why: >-
    Same rule as your cut button; pick all if a stray selection should never leave a strip
    behind.
options:
  - id: selected-else-all
    title: Selected strips under the playhead, else all under it
    summary: The selected strips under the playhead are deleted, or all of them if none is selected.
    pros:
      - Same rule as the cut button
      - Deselect a title to keep it (the gap then stays open)
    cons:
      - A stray selection deletes less than you expect
  - id: all
    title: Everything under the playhead
    summary: Every strip the playhead is on is deleted, selected or not.
    pros:
      - "Cut twice, park the playhead, press: the whole section goes"
    cons:
      - To keep a strip, such as a music bed, you must lock it, and the gap then stays open
  - id: selected
    title: Selected strips, wherever they are
    summary: The selected strips are deleted wherever they are, like Blender's Delete.
    pros:
      - Can delete strips the playhead isn't on
    cons:
      - Strips selected in several places leave several gaps to close
      - Nothing happens with nothing selected
resolution:
  choice: selected-else-all
  note: playhead should select everything under it
  resolved_at: 2026-11-15T07:29:57Z
applied_at: 2026-11-15T07:38:25Z
---

## Context

Locked strips are never deleted; effects on a deleted strip go with it, and a strip with a
locked effect on it is kept. If
selection always follows the playhead (your cut answer), the first two options differ only
after you deselect something.
