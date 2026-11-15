# Decisions

Product decisions the owner made, newest first. Each comes from a decision
card in the owner's president queue (the card id is in brackets).

## Selection follows the playhead (20261115pnow)

Asked what should be selected after a cut, the owner chose Premiere's
"selection follows playhead": whenever the playhead moves (a knob, the
mouse, playback), the strips under it are selected and every other strip is
deselected, so the cut and ripple delete buttons act on what's under it.
Locked strips and strips on locked channels aren't selected. Right after a
cut, the right halves are selected, since they now start at the playhead.
It's the add-on preference "Selection follows playhead", on by default, and
it does nothing while Blender renders.

## The cut button cuts the selected strips under the playhead, else all (202611159rr6)

Of the strips the playhead is inside, the selected ones are cut; with none
of them selected, all of them are. Locked strips and strips on locked
channels never are. With selection following the playhead, that is normally
everything under it; selecting some of those strips by hand afterwards cuts
just those.

## The cut button makes a soft cut (20261115o21w)

A soft cut, like Blender's K: both halves keep the hidden media past the
cut, so either side can be dragged back out later.

## The undo button works anywhere in Blender (20261115msk0)

Undo runs whatever area is in use, like Ctrl+Z, not only while a video
editor is showing.

## Fn + the undo button also undoes (20261115fla1)

No redo on Fn: the undo button undoes with or without Fn held. (It was
"redo" briefly; the owner reopened the card and chose this before it was
built.)

## Ripple delete deletes the selected strips under the playhead, else all (20261115e3it)

Of the strips under the playhead, the selected ones are deleted; with none
of them selected, all of them are. Locked strips and strips on locked
channels never are. That is the cut button's rule (202611159rr6). The owner
wants the playhead to select everything under it, so normally everything
under it goes (see "Selection follows the playhead").

## Ripple delete closes the gap on all channels (202611153gow)

The gap is the deleted strips' span, trimmed to where every channel is
empty, and everything after it moves left on every channel, so picture,
sound and titles stay in sync. If a strip that isn't deleted is still under
the playhead, the strips are deleted and the gap stays open.

## A locked strip keeps the gap open (20261115msw5)

If closing the gap would move a locked strip or a strip on a locked
channel, the strips are deleted and nothing moves.

## After a ripple delete the playhead is at the join (20261115xwbm)

With the gap closed, the playhead goes to where the gap was, the first frame
of what came after it. If the gap stays open, the playhead doesn't move.

## No Fn variant of ripple delete (202611156qj4, reverses 20261115d0tu)

Ripple delete has its own button, and Fn + that button does the same ripple
delete. The owner first wanted Fn + the cut button to ripple delete
(20261115d0tu), then decided against any Fn for ripple delete: Fn + the cut
button just cuts, and the default config has no Fn ripple delete binding.

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
