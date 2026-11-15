# Decisions

Product decisions the owner made, newest first. Each comes from a decision
card in the owner's president queue (the card id is in brackets).

## Zoom knob: Fn means finer (20261115fu2b)

A plain click on the zoom knob zooms 4 steps (about 27%), and 1 step (7.5%)
with Fn held. That matches the scrub knob, where Fn also means finer (10
frames, 1 with Fn), and a 5-second view still opens out to a 10-minute edit
in about 16 clicks. Default bindings: "Zoom timeline" step 4, "Zoom timeline
(fine)" step 1 with Fn.

## Zoom anchors on the playhead (202611154i5v)

Zooming keeps the playhead at its spot on screen, so the frame you scrubbed
to stays in view. If the playhead is off screen, the view first centers on
it, then zooms. (Blender's own zoom keys zoom around the view's middle.)
