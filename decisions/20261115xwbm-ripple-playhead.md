---
id: "20261115xwbm"
title: Where should the playhead be after a ripple delete?
status: resolved
kind: ab
priority: normal
created: 2026-11-15T07:18:07Z
created_by: claude-code
tags: [controls, ripple-delete]
depends_on: []
recommendation:
  option: edit-point
  why: >-
    You see the new join right away; pick stay if you want the playhead's frame number never
    to change.
options:
  - id: edit-point
    title: At the join
    summary: The playhead moves to where the gap was, on the first frame of what came after it.
    pros:
      - Shows the new join
      - The next press deletes what starts at the join
    cons:
      - The playhead's frame number goes back
  - id: stay
    title: Where it was
    summary: The playhead keeps its frame number; what came after the gap slides under it.
    pros:
      - The playhead never jumps
    cons:
      - It lands partway into the next clip, or past it if that clip is short
resolution:
  choice: edit-point
  resolved_at: 2026-11-15T07:30:03Z
applied_at: 2026-11-15T07:38:28Z
---

## Context

With the gap closed, the frame under the playhead shows later footage. If the gap stays
open, the playhead doesn't move. The button now does the recommended option.
