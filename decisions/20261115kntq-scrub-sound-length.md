---
id: "20261115kntq"
title: How much sound should each click of the scrub knob play?
status: resolved
kind: choice
priority: normal
created: 2026-11-15T23:50:58Z
created_by: claude-code
tags: [ux, audio, timeline]
depends_on: []
recommendation:
  option: step
  why: >-
    Turning steadily without Fn, you hear every frame you pass over instead of 1 in 10. Keep one frame if you
    want each click without Fn to sound only the frame it lands on.
options:
  - id: frame
    title: One frame
    summary: Each click plays 1 frame of sound (40 ms at 25 fps) from where it lands, as built now.
    pros:
      - You hear exactly the frame you land on
      - Same as Blender's own audio scrubbing
    cons:
      - Without Fn you hear 1 frame in 10
  - id: step
    title: The frames the click moved over
    summary: >-
      Each click plays the frames it moved, landing frame included, forward in time: 10
      frames (0.4 s at 25 fps) without Fn, or just the landing frame with Fn, as in the first option.
    pros:
      - Up to about 2.5 clicks a second, no frames are skipped
    cons:
      - Forward, the sound ends on the playhead; backwards, it starts there
      - Turning faster cuts each stretch short
  - id: longer
    title: A fixed 0.2 s
    summary: Each click plays 0.2 s (5 frames at 25 fps) from where it lands.
    pros:
      - Syllables and beats are recognisable at either step
    cons:
      - Plays past the frame you land on
      - With Fn, slow turning sounds like stuttering playback
resolution:
  choice: step
  resolved_at: 2026-11-16T00:11:23Z
applied_at: 2026-11-16T00:26:04Z
---

## Context

The scrub knob now plays the sound under the playhead on every click that moves it, and each click cuts
off the last one's sound. A click moves 10 frames, or 1 with Fn. How long should each click
play?
