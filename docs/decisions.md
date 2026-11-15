# Decisions

Product decisions the owner made, newest first. Each comes from a decision
card in the owner's president queue (the card id is in brackets).

## Scrub knob stops at the scene's ends (202611153dil)

By default, scrubbing stops at the scene's first and last frame: turning past
frame 250 of a 1–250 scene leaves the playhead on 250, and a playhead already
outside the range can't go further out. At 10 frames per click a fast spin
would otherwise overshoot into frames that aren't rendered. It stays a
checkbox in the add-on ("Stop at scene start and end"), ticked by default;
untick it to scrub freely like Blender's own timeline.

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

## Scrub stops at the scene's ends (202611153dil)

By default the scrub knob stops at the scene's first and last frame, so a
fast spin at 10 frames a click can't overshoot into frames that aren't
rendered. A playhead already outside the range is never pulled back. The
add-on's "Stop at scene start and end" checkbox turns it off to move freely,
like Blender's own scrubbing.
