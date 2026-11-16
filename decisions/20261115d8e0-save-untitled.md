---
id: "20261115d8e0"
title: What should the save button do when the file has never been saved?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T19:48:27Z
created_by: claude-code
tags: [save, controls]
depends_on: []
recommendation:
  option: dialog
  why: >-
    It is what Ctrl+S does, and a new edit needs a name and folder from you anyway. Pick "note"
    if a file window opening in the middle of an edit, from a press you didn't mean, would bother you.
options:
  - id: note
    title: Do nothing and leave a note
    summary: >-
      The press saves nothing, and the Blendeck panel in the video editor's sidebar says
      "Not saved yet: use File > Save As once".
    pros:
      - A stray press never opens a window
    cons:
      - The press seems to do nothing unless the sidebar is open
  - id: dialog
    title: Open Blender's Save As window
    summary: The press opens the same file window as Ctrl+S on a new file; you pick the name and folder.
    pros:
      - Works exactly like Ctrl+S
      - You choose where it goes
    cons:
      - The window waits open until you answer at the keyboard
      - With several Blender windows, opens in the first one
  - id: beside-media
    title: Save next to the footage
    summary: >-
      The press saves it as edit-<date>-<time>.blend in the folder of the first clip on the
      timeline; later presses save over that file.
    pros:
      - Every press saves, with no typing
    cons:
      - Writes into footage folders, which may be read-only or on a NAS
      - With no clip on the timeline yet, does nothing, like "note"
resolution:
  choice: dialog
  resolved_at: 2026-11-15T20:16:39Z
applied_at: 2026-11-15T20:25:12Z
---

## Context

Note 49 will save the .blend file, like Ctrl+S. A file saved before just saves in place; this
card is only about a new file that has no name yet.
