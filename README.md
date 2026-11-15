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
| Any knob you pick | Moves the playhead 10 frames per click | 1 frame per click |
| Another knob you pick | Zooms the timeline 4 steps per click (about 27%), around the playhead | 1 step per click (7.5%) |
| A button you pick | Soft-cuts (like K) the selected strips under the playhead, or all of them if none is selected, once per press; Ctrl+Z undoes it | The same |
| Another button you pick | Undoes once per press, like Ctrl+Z, anywhere in Blender. Nothing while you drag a strip or another operator is running | The same |
| A third button you pick | Ripple delete: deletes the strips under the playhead (the selected ones among them, if any) and closes the gap on every channel; the playhead goes to the join. Locked strips are never deleted; if a strip stays under the playhead or a locked strip would have to move, the gap stays open. Ctrl+Z undoes it | The same |
| A fourth button you pick | Play / pause, like Space in the video editor: plays the scene the video editor shows, and pausing leaves the playhead where it stopped. The scrub knob, cut and ripple delete keep working while it plays; undo pauses it, as Ctrl+Z does | The same |
| A fifth button you pick | Plays backwards; each further press plays 10% faster backwards, up to 4x. Silent: Blender only plays sound forward at normal speed | The same |
| A sixth button you pick | Plays forward (normal speed, with sound); each further press plays 10% faster, up to 4x, silent. The opposite button switches to normal speed the other way, play / pause stops and the next play is normal speed. Both stop at the scene's first or last frame | The same |

More come one at a time.

**Selection follows the playhead** (like Premiere): whenever the playhead
moves, the strips under it are selected and the rest deselected, so the cut
and ripple delete buttons act on them. Select some of them by hand to cut or
delete only those. Turn it off in the add-on's preferences or its panel.

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
   "Play forward / faster" and press the buttons you want for those.

The add-on's panel is in the video editor's sidebar (N), tab Blendeck. Its
preferences have the bridge's address and "Stop at scene start and end".
If Blender is busy for more than 15 seconds (a long save or render), the
bridge drops it and the add-on reconnects by itself a second later.

## The page

- **Monitor**: every MIDI message, live, with what it did. Use it to see
  what a control sends.
- **Bindings**: control → action, with frames (scrub) or zoom steps (zoom) per click and the knob's
  encoding (most send 1 / 127 for one click right / left). Button actions
  (cut, undo, ripple delete, playback) have neither.
- **Modifiers**: buttons like Fn that switch bindings while held.
- **Profiles**: named sets of bindings; the selected one is active.
- **Blender**: connection, scene, playhead, and test buttons.

Flags: `-config path`, `-addr 127.0.0.1:7870`, `-link 127.0.0.1:7871`,
`-no-browser`, `-verbose`. Settings live in `config.json` next to the exe.

## Platforms

Windows only for now: MIDI input uses winmm. The rest is portable Go and
compiles everywhere; other platforms need a MIDI backend.

## License

MIT.
