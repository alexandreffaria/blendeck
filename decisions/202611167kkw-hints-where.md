---
id: "202611167kkw"
title: Where should the control hints show?
status: resolved
kind: choice
priority: normal
created: 2026-11-16T02:48:01Z
created_by: claude-code
tags: [ux, hints]
depends_on: []
recommendation:
  option: hold-fn
  why: >-
    It needs no mouse and takes no screen space the rest of the time. Pick the corner if you
    would rather have it always visible, or the sidebar if a list flashing up during a slow
    Fn turn would bother you.
options:
  - id: hold-fn
    title: Hold Fn to see them
    summary: >-
      Holding Fn for about half a second without touching anything else shows the list over
      the timeline; it goes when Fn is released or another control is used.
    pros:
      - No mouse, nothing on screen otherwise
    cons:
      - Covers part of the timeline while shown
      - Flashes up if you hold Fn a moment before a fine turn
  - id: sidebar
    title: A list in Blender's sidebar
    summary: >-
      The Blendeck tab of the timeline's sidebar (opened with N) gets a collapsible Controls
      list.
    pros:
      - Uses a tab that already exists
      - Stays open while you learn
    cons:
      - Narrows the timeline while open
      - Shows only while the Blendeck tab is selected, instead of Strip
  - id: corner
    title: Small dim text in a timeline corner
    summary: >-
      The list is drawn small and dim in the timeline's top-left corner, where channels are
      usually empty, with a checkbox to hide it.
    pros:
      - A true glance, no action at all
      - Dim enough to ignore
    cons:
      - A 14-line block on screen all the time, over strips if you stack channels high
  - id: page
    title: On the setup page
    summary: >-
      The setup page already lists this under Controls; this adds a compact read-only
      version at the top.
    pros:
      - Least new to build
      - Room for a clear layout
    cons:
      - Means leaving Blender to look, unless it sits on a second monitor
resolution:
  choice: hold-fn
  resolved_at: 2026-11-16T02:53:51Z
applied_at: 2026-11-16T04:58:34Z
---

## Context

The hints would be one line per control, using your names for the controls and the bindings,
like "D2  Scrub timeline · Fn: fine": 14 lines for today's profile.
