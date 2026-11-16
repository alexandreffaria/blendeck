# Decisions

Product decisions the owner made, newest first. Each comes from a decision
card in the owner's president queue (the card id is in brackets).

## The jump knob stops at video edits; Fn stops at every cut (20261115s2rt)

Turned on its own, the jump knob skips sound strips and stops only at the
starts and ends of strips that aren't sound, where the picture changes.
With Fn held it stops at every cut, sound included. The owner picked
"skip sound cuts" for Fn, then asked for the opposite: skipping sound is
the everyday jump.

## Each forward or backwards press is 0.25x (202611153pfo, replaces 20261115u2qg)

The forward and backwards buttons play at 1x on the first press and 0.25x
faster on each further press that way (1.25x, 1.5x, ...): 5x after 16
presses. The owner first chose 0.1x a press (20261115u2qg), then found the
speed didn't seem to go up when trying it on the Loupedeck.

## Forward and backwards playback go up to 8x (2026111504tn)

The top speed either way is 8x, 28 presses past 1x. It was 4x
(20261115u2qg).

## Fast forward playback has sound up to 5x, at its normal pitch (202611150o5i)

Only 1x forward is Blender's own playback. Faster forward playback is the
add-on's, which plays the sound strips time-stretched so voices keep their
pitch, up to 5x; past 5x it is silent, as the owner asked.

## Backwards playback is silent (2026111598qg)

Backwards playback has no sound at any speed.

## The opposite button eases off, then switches (20261115618c)

Playing faster than 1x one way, the other button slows it a step (0.25x) a
press down to 1x; at 1x, its next press plays the other way at 1x. Easing off forward
lands on Blender's own 1x playback, with sound.

## Fast and backwards playback stops at the ends (20261115pb4a)

Playback the add-on drives stops on the first frame going backwards and the
last going forward (of the preview range, when it's on), rather than looping
like Blender's own playback.

## Play / pause stops fast and backwards playback (20261115usz7)

During fast or backwards playback, play / pause stops it where it is (the
playhead stays, as for a plain pause, 20261115nsi1), and the next press plays
forward at 1x.

## Pausing leaves the playhead where it stopped (20261115nsi1)

The play / pause button pauses like Space in Blender: the playhead stays on
the frame playback reached, so a cut or ripple delete lands right there. It
doesn't jump back to where play started (Blender's Esc).

## Fn + the play button also plays or pauses (20261115ftlg)

No Fn variant: like the undo and ripple delete buttons, the play button does
the same with or without Fn, and the default config has no Fn play binding.

## Selection keeps following the playhead while it plays (202611150zcw)

During playback, selection follows the playhead frame by frame, the same
rule as when paused, so a cut or ripple delete while playing takes what is
under the playhead. Playing replaces a hand-picked selection right away.

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

## Saving a file never saved opens Save As (20261115d8e0)

The save button on a file that has no name yet opens Blender's Save As
window, as Ctrl+S does, in the first Blender window; you pick the name and
folder. A press while that window is open does nothing more. The Blendeck
panel says the window is open, then "Saved at ..." or that Save As was
cancelled.

## Every stop recenters the timeline (20261115guk2)

Whatever stops playback recenters the timeline on the playhead, keeping the
zoom: the play / pause button, undo, the shuttle reaching an end, Space or
the pause button in Blender, a render or a scene switch. The playhead is
always in view after playback, at the cost of the view also moving on stops
nobody meant.

## A scrub click plays the frames it moved over (20261115kntq)

Each click of the scrub knob plays the sound of the frames it moved over,
landing frame included, forward in time: 10 frames without Fn, just the
landing frame with Fn. Going forward the sound ends on the playhead, going
backwards it starts there. Turning steadily, no frames are skipped; the
next click cuts the last one's sound off. Clicks added up in one read play
at most the last half second, nearest the playhead.
