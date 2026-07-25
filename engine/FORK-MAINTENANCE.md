# Fork maintenance — keep the diff tiny

This fork exists to be the SpotiFLAC download engine while staying cheap to maintain.
It stays cheap **only** if our changes are small and ADDITIVE, so pulling upstream
never conflicts. That discipline is the whole reason we forked instead of running a
black box (or reimplementing in Go).

## Our entire diff from upstream
- `engine/shim.py`             — engine-agnostic HTTP adapter the Go app calls (NEW file)
- `engine/Dockerfile`          — builds fork + shim as our engine image (NEW file)
- `engine/requirements.txt`    — shim-only deps (NEW file)
- `engine/FORK-MAINTENANCE.md` — this file (NEW file)

**Zero edits to their provider / resolution / matching code.** Everything under
`SpotiFLAC/` stays exactly as upstream ships it.

## Rules to keep it that way
1. Behavior changes go through the **shim**, not their core:
   - disable tagging via their flags (`enrich_metadata=False`, `embed_lyrics=False`),
   - pick providers / order via the `services` list,
   - anything else the shim can pass as a parameter.
2. Edit their core **only** when a flag genuinely can't do it (a bug, a new route).
   When you must: keep it surgical, and log it under "Core edits" below so you know
   your exact conflict points at merge time.

## Syncing upstream (do it when a provider breaks, or every few months)
```bash
git remote add upstream https://github.com/BartolomeoRusso9/SpotiFLAC-Module-Version
git fetch upstream
git merge upstream/main         # clean if the rules above held
docker build -f engine/Dockerfile -t spotiflac-engine .   # rebuild
# re-test one track through the shim before deploying (see docs runbook Phase 0)
```
A route dies → upstream usually fixes it → you `git merge` and rebuild. You touch code
only when a flag can't get you there.

## Core edits (log every non-additive change here)
- (none yet)
