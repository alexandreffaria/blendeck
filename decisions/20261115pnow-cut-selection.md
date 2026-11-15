---
id: "20261115pnow"
title: What should be selected after the cut button cuts?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T06:02:25Z
created_by: claude-code
tags: [controls, cut]
depends_on: ["202611159rr6"]
recommendation:
  option: unchanged
  why: >-
    If the button cuts everything, this mostly decides what Delete takes after a cut: pick right
    if you often cut then delete the tail. With a selection-based scope, unchanged keeps
    later cuts working in both directions.
options:
  - id: unchanged
    title: Leave selection as it was
    summary: Both halves of a strip keep its selection; nothing else changes.
    pros:
      - Later cuts reach the same strips they would have, with any scope
    cons:
      - Delete right after a cut removes both halves of selected strips
  - id: right
    title: Select the right halves
    summary: Only the parts after the playhead of the strips just cut end up selected.
    pros:
      - "Cut, then Delete: trims off the part of each cut strip after the playhead"
      - Cutting forward through the same strips still works
    cons:
      - Replaces your selection, and can select strips you hadn't
      - With a selection-based scope, cutting back skips the other halves and new strips
  - id: left
    title: Select the left halves
    summary: Only the parts before the playhead of the strips just cut end up selected.
    pros:
      - "Cut, then Delete: trims off the part of each cut strip before the playhead"
      - Cutting back through the same strips still works
    cons:
      - Replaces your selection, and can select strips you hadn't
      - With a selection-based scope, cutting forward skips the other halves and new strips
resolution:
  choice: custom
  note: let's do premiere style "selection follow playhead" just have everything under the play head selected, that's how we will use with ripple delete next
  resolved_at: 2026-11-15T06:18:00Z
---

## Context

After a cut, each cut strip is two strips. Blender's K keeps the mouse's side
selected; the button has no mouse side. "Selection-based scope" means cutting only selected
strips, or selected-else-all.
