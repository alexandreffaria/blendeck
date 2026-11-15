# Blendeck

Drives Blender's video editor from a Loupedeck+ (or any MIDI controller).
Three parts:

- `cmd/blendeck` + `internal/`: the Go bridge. Reads MIDI (winmm, no cgo),
  maps it through the active profile (`internal/engine`), sends commands to
  Blender (`internal/link`), serves the setup page (`internal/web`).
- `blender/blendeck`: the Blender extension. Connects to the bridge on
  127.0.0.1:7871 and applies commands. `protocol.py` has no `bpy` so it can be
  tested without Blender.
- `internal/web/static/index.html`: the page (monitor, learn, profiles).

## How work is done

One control at a time. Each control is a branch and a PR: build it, have a
fresh agent review it, fix, review again, until the review says ready.

- **Never commit to `main` directly.** Branch from an up-to-date `main`:
  `feat/…`, `fix/…`, `chore/…` or `ci/…`.
- **No stacked PRs.** Every PR's base is `main`.
- **Every PR gets a second review** before merge: a fresh agent runs
  `/review-pr <number>` against `.github/REVIEW_CHECKLIST.md` and posts the
  result on the PR. Fix what it finds and ask for another review.
- **Product decisions go to president** (`decisions/`, see the
  `decision-cards` skill), not chat. Never commit files under `decisions/`
  in a PR; stage files by path.

## Rules that have bitten before

- The bridge listens on loopback only. The page checks the Host header and
  needs a JSON body for anything that changes state; keep both.
- Anything that grows from outside input needs a cap: config sizes
  (`config.Max*`), the event log, the link's line length and client count.
- winmm calls the MIDI callback on its own thread: never block in it.
  `midi.Conn.deliver` drops instead of waiting.
- `windows.NewCallback` has a process-wide limit; it is created once.
- The add-on's timer must never raise (it would stop for good): keep the
  try/except in `tick`.

## Before you push

```sh
gofmt -l .                # must print nothing
go vet ./...
go test ./...
GOOS=linux go vet ./...   # the non-Windows build still compiles
python -I -m unittest discover -s blender/tests
```

To try it for real: `./build.ps1`, run `blendeck.exe`, install
`dist/blendeck-addon.zip` in Blender (Get Extensions › Install from Disk).
