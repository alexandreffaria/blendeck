---
id: "202611153gow"
title: When ripple delete closes the gap, which channels move?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T07:18:07Z
created_by: claude-code
tags: [controls, ripple-delete]
depends_on: ["20261115e3it"]
recommendation:
  option: all-sync
  why: >-
    Picture and sound never drift apart and nothing else is touched; pick cut-through or
    own-channels if you often delete under a strip that runs on, like a music bed.
options:
  - id: all-sync
    title: All channels, by the span that is empty on all of them
    summary: >-
      The deleted span, trimmed to where every channel is empty, closes on all channels; a
      kept strip under the playhead keeps it open.
    pros:
      - Video, its sound and titles stay in sync
    cons:
      - A music bed under the playhead keeps the gap open (a plain delete)
      - A short strip on another channel can leave part of the gap open
  - id: own-channels
    title: Only the channels of the deleted strips
    summary: Each channel a strip was deleted from closes its own gap; other channels don't move.
    pros:
      - Works under a music bed or a long title
    cons:
      - Strips after the gap drift out of sync with channels that didn't move
  - id: cut-through
    title: All channels, cutting the span out of strips that run across it
    summary: >-
      The whole deleted span closes on all channels; kept strips across it, such as a music
      bed, lose that span too.
    pros:
      - Sync kept, even under a music bed
    cons:
      - Shortens strips you didn't pick, like the music bed
resolution:
  choice: all-sync
  resolved_at: 2026-11-15T07:30:25Z
applied_at: 2026-11-15T07:38:24Z
---

## Context

After the strips are deleted, later strips move left to close the gap. The button now does
the recommended option.
