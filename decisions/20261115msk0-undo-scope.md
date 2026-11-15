---
id: "20261115msk0"
title: Should the undo button work anywhere in Blender, or only while a Video Sequencer is showing?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T07:24:24Z
created_by: claude-code
tags: [controls, undo]
depends_on: []
recommendation:
  option: anywhere
  why: >-
    Blender has one undo history for everything, so the sequencer-only option only stops
    presses while no Video Sequencer is showing; pick it if you often work in other
    workspaces with the Loupedeck within reach.
options:
  - id: anywhere
    title: Anywhere
    summary: >-
      A press always undoes the last step, whatever workspace is showing, like Ctrl+Z.
    pros:
      - Same as Ctrl+Z
      - Works while you fix something outside the Video Sequencer
    cons:
      - A stray press while modelling or compositing undoes that work
  - id: sequencer-only
    title: Only while a Video Sequencer is showing
    summary: >-
      A press undoes only when a window shows a Video Sequencer with its strips; otherwise
      it does nothing.
    pros:
      - A stray press can't undo 3D or compositing work done away from video editing
    cons:
      - A silent press when the sequencer is hidden
      - Still undoes whatever was last, which may not be a sequencer edit
resolution:
  choice: anywhere
  resolved_at: 2026-11-15T07:34:39Z
---

## Context

The add-on can't tell which editor your mouse is in, so "only the Video Sequencer" can only
mean "only while one is showing".
