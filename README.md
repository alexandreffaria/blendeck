# Blendeck

Use a Loupedeck+ (or any MIDI controller) with Blender's video editor.

The Loupedeck software doesn't support Blender, but the Loupedeck+ shows up
as a plain MIDI device. Blendeck reads it directly: a small program (the
bridge) turns knob clicks and button presses into Blender commands, and a
Blender extension carries them out. A page on `http://127.0.0.1:7870` shows
every message the device sends, lets you assign controls by touching them,
and keeps profiles.

## Controls so far

| Control | Does | With Fn held |
| --- | --- | --- |
| Any knob you pick | Moves the playhead 10 frames per click. While it plays, at any speed, the first click stops playback, and the playhead stays where the knob leaves it | 1 frame per click |
| Another knob you pick | Zooms the timeline 4 steps per click (about 27%), around the playhead | 1 step per click (7.5%) |
| A button you pick | Soft-cuts (like K) the selected strips under the playhead, or all of them if none is selected, once per press; Ctrl+Z undoes it | The same |
| Another button you pick | Undoes once per press, like Ctrl+Z, anywhere in Blender. Nothing while you drag a strip or another operator is running | The same |
| A third button you pick | Ripple delete: deletes the strips under the playhead (the selected ones among them, if any) and closes the gap on every channel; the playhead goes to the join. Locked strips are never deleted; if a strip stays under the playhead or a locked strip would have to move, the gap stays open. Ctrl+Z undoes it | The same |
| A fourth button you pick | Play / pause, like Space in the video editor: plays the scene the video editor shows, and pausing leaves the playhead where it stopped. Cut and ripple delete keep working while it plays; the scrub knob and undo pause it | The same |
| A fifth button you pick | Plays backwards; each further press plays 0.25x faster backwards, up to 8x. Silent | The same |
| A sixth button you pick | Plays forward (normal speed, with sound); each further press plays 0.25x faster, up to 8x, with sound at its normal pitch up to 5x and silent past that. That sound leaves out strips packed into the .blend, plays retimed strips at their normal speed and ignores volume animation; strips moved with the mouse while it plays are heard where they were until the next press, jump or edit from the Loupedeck. The opposite button eases off 0.25x a press down to normal speed, then switches direction; play / pause or the scrub knob stops and the next play is normal speed. Faster than normal or backwards, they stop at the first or last frame (of the preview range, when it's on and isn't one the Cache Window extension borrowed); normal speed forward is Blender's own playback and loops | The same |
| A third knob you pick | Jumps the playhead to the next video edit per click right, the previous one per click left: the start and end of every strip that isn't sound, on any channel, like Blender's Up and Down arrows. Sound strips, muted strips and muted channels are skipped; past the last edit it stays put | Stops at every cut, sound strips' too |
| A fourth knob you pick | Moves the selected strips and every strip after them, on all channels, 10 frames per click (right or left), like G in the video editor. They stop against the next strip instead of going over it; locked strips stay put. The playhead stays, and one turn of the knob is one Ctrl+Z. ("Move selected and earlier strips" can still be bound to a knob of its own) | 1 frame per click |
| A fifth knob you pick | Slides the timeline view 5% of its width per click: right shows later frames, left earlier ones. Only the view moves: the playhead, the strips and playback stay as they are, so you can look ahead while it plays | The same |
| A sixth knob you pick | Trims the clip handles at the playhead a frame per click, and the playhead rides along. On a clip's start, left shows more of its beginning and right less; on a clip's end, right shows more of its end and left less. Every clip on any channel with a handle there moves, so a picture and its sound stay in sync; where two clips meet, the cut between them rolls. A handle stops at the end of the clip's footage, at the next clip on its channel, and one frame from the clip's other end; locked strips stay put, and with no handle at the playhead nothing happens. One turn of the knob is one Ctrl+Z. To lengthen a clip: open a gap with the move knob, jump to the clip's start or end, and turn | The same |
| A seventh button you pick | Zooms and slides every timeline to show the whole edit, like Home over the strips: every strip in view. Only the view changes; the playhead and playback stay as they are | The same |
| An eighth button you pick | Saves the .blend file in place, like Ctrl+S, after any edit sent with it. A file never saved opens Blender's Save As window, as Ctrl+S does, in the first Blender window; you pick the name and folder. Nothing while you drag a strip or render; the panel shows when it last saved or why it didn't | The same |

More come one at a time.

**Selection follows the playhead** (like Premiere): whenever the playhead
moves, the strips under it are selected and the rest deselected, so the cut
and ripple delete buttons act on them. Select some of them by hand to cut or
delete only those. Turn it off in the add-on's preferences or its panel.

**The timeline recenters when playback stops**: whatever stops it (play /
pause, the end of the play range, Space or undo in Blender), every timeline
slides so the playhead is in the middle, at the same zoom.

## Setup (Windows)

1. Close the Loupedeck software if it's running; only one program can open
   the device.
2. Build (needs Go 1.26+ and Python 3): `.\build.ps1`. This makes
   `blendeck.exe` and `dist\blendeck-addon.zip`.
3. In Blender 4.2 or newer: Edit › Preferences › Get Extensions › ⌄ ›
   Install from Disk, and pick `dist\blendeck-addon.zip`.
4. Run `blendeck.exe`. The page opens. Under **Bindings**, click **Learn**
   on "Scrub timeline" and turn the knob. Under **Modifiers**, click
   **Learn** on Fn and press Fn. Then **Learn** on "Scrub timeline (fine)"
   while holding Fn, and **Save**. Do the same for "Zoom timeline" and
   "Zoom timeline (fine)" with the knob you want for zoom, and **Learn** on
   "Cut at playhead" and press the button you want for cutting, and on
   "Undo" and press the button you want for undo, on "Ripple delete"
   and press the button you want for it, on "Play / pause" and press the
   button you want for playback, and on "Play backwards / faster" and
   "Play forward / faster" and press the buttons you want for those, and
   on "Jump between video edits" and turn the knob you want for it, then
   on "Jump between all cuts" and turn the same knob with Fn held,
   and on "Move selected and later strips" and "Move selected and later
   strips (fine)" (with Fn) and turn the knob you want for moving strips,
   and on "Trim handle at playhead" and turn the knob you want for
   trimming,
   and on "Pan timeline" and turn the knob you want for sliding the view,
   and on "View whole timeline" and press the button you want for it,
   and on "Save file" and press the button you want for saving.
   A `config.json` from an older version has no row for a new control:
   **Add binding**, pick its action, and learn it.

The add-on's panel is in the video editor's sidebar (N), tab Blendeck. Its
preferences have the bridge's address and "Stop at scene start and end".
If Blender is busy for more than 15 seconds (a long save or render), the
bridge drops it and the add-on reconnects by itself a second later.

## The page

- **Monitor**: every MIDI message, live, with what it did. Use it to see
  what a control sends.
- **Bindings**: control → action, with frames (scrub, moving strips, trimming), zoom steps (zoom) or percent of the view (pan) per click and the knob's
  encoding (most send 1 / 127 for one click right / left). Jumping between
  edits or cuts has an encoding and is always one per click. Button actions
  (cut, undo, ripple delete, playback, view whole timeline, save) have neither.
- **Modifiers**: buttons like Fn that switch bindings while held.
- **Profiles**: named sets of bindings; the selected one is active.
- **Controls**: name each control after its label on the device ("Knob 1",
  "Wheel"), and the page shows that name instead of its MIDI number in the
  bindings, the modifiers and the monitor. Every profile shares the names.
- **Blender**: connection, scene, playhead, and test buttons.

- **Startup**: Start with Windows (also in the tray menu).

Flags: `-config path`, `-addr 127.0.0.1:7870`, `-link 127.0.0.1:7871`,
`-no-browser`, `-verbose`. Settings live in `config.json` next to the exe.

## Running in the background

`blendeck.exe` has no console window. It sits in the notification area as
the knob mascot: hover it to see whether the controller and Blender are
connected, click it for the page, right-click it for **Open settings page**,
**Start with Windows** and **Quit Blendeck** (which closes the MIDI device,
the Blender link and the page cleanly). Starting it again while it runs
just opens the page.

**Start with Windows** adds a per-user entry
(`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, no admin rights)
that starts this copy of the exe with `-no-browser` when you sign in. If you
move the exe, turn it on again from the new place.

It logs to `blendeck.log` next to `config.json`; past 1 MB it moves to
`blendeck.log.1` and starts over. If it can't start (say, another program
holds the ports), it says why in a message box.

The tray icon is drawn from the page's mascot by `go run ./scripts/mkicon`
(into `internal/tray/blendeck.ico`); run it again if the mascot changes.

## Platforms

Windows only for now: MIDI input uses winmm. The rest is portable Go and
compiles everywhere; other platforms need a MIDI backend.

## License

MIT.
