# PR review checklist

Run against every pull request's diff before it's merged, by an agent that
didn't write it (`/review-pr <number>`). Each line is pass, fail or n/a, with
the file and line for every fail. One fail means don't merge yet.

Items marked **CI** are also enforced by `.github/workflows/pr-guard.yml`;
the reviewer still confirms the check is green.

## Shape

1. **CI** Base branch is `main` and the head is not `main`.
2. **CI** No file over 1 MB, no binaries other than images and fonts (no
   `.exe`, `.zip`), no `.env` or key files.
3. No secrets or personal data in the diff (tokens, emails, local paths
   with a user name).
4. No files under `decisions/` (president's cards travel by Sync).

## Bridge and add-on

5. Nothing listens beyond loopback; page routes that change state still go
   through the Host and JSON checks in `internal/web`.
6. Everything that grows from outside input has a cap (config sizes, logs,
   buffers, connections, loops).
7. Nothing blocks on the winmm callback thread, and the Blender timer can't
   raise or stall Blender's UI (no blocking socket calls, no long loops).
8. The MIDI message → action path is covered by engine tests, including
   modifier held/released and controls not learned yet.

## Tests and CI

9. The change has tests: a bug fix has a test that fails without it, a
    feature has tests for its main path and its edges.
10. The commands in CLAUDE.md's "Before you push" pass, and CI is green.
11. The PR description says what changed, why, and how it was tried on the
    real device or in Blender (or why it wasn't).
