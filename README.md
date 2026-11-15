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

More come one at a time.

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
   while holding Fn, and **Save**.

The add-on's panel is in the video editor's sidebar (N), tab Blendeck. Its
preferences have the bridge's address and "Stop at scene start and end".

## The page

- **Monitor**: every MIDI message, live, with what it did. Use it to see
  what a control sends.
- **Bindings**: control → action, with frames per click and the knob's
  encoding (most send 1 / 127 for one click right / left).
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
