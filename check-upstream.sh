#!/usr/bin/env bash
# check-upstream.sh — what changed in spotbye/SpotiFLAC that we might want.
#
# Usage:
#   ./check-upstream.sh [--verbose]     human-readable report
#   ./check-upstream.sh --github        machine-readable, for upstream-check.yml
#
# Setup: git remote add upstream https://github.com/spotbye/SpotiFLAC.git
#
# ─── How this works, and why it changed ──────────────────────────────────────
#
# spotbye is a PORT SOURCE, not a dependency: we read it for ideas, we never
# merge it. So the only question worth asking is "did anything change that we
# might want to copy?", and the only expensive part is a human answering it.
#
# It used to classify all ~46 upstream files through .github/upstream-map.txt,
# which required naming a local path for each of the ~30 we watched. Eight of
# those paths went stale the moment the cleanup plan deleted the code they
# pointed at, and the report then said "MAPPED changed, go look" at directories
# that no longer existed — forever, since nothing makes a stale mapping fail.
#
# Now: everything is watched EXCEPT what .github/upstream-ignore.txt names. A
# new upstream file is reported by default rather than needing to be added to a
# list first, and the ignore file has nothing to rot — it names upstream paths
# only, never ours.
#
# The workflow (.github/workflows/upstream-check.yml) calls this script rather
# than reimplementing it, which it used to do inline. The two had already
# drifted apart.
#
# This script is also what advances the baseline: when everything that changed
# is ignored, there is nothing for a human to decide, so it says so and the
# workflow moves the marker on its own. That is what stops a backlog from
# building up the way it did (35 commits un-triaged by 2026-07-30).

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

MODE=human
case "${1:-}" in
    --verbose|-v) MODE=verbose ;;
    --github)     MODE=github ;;
    "")           ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
esac

IGNORE_FILE=".github/upstream-ignore.txt"
BASELINE_FILE=".github/upstream-last-reviewed.txt"

if $([ "$MODE" = github ] && echo false || echo true); then
    BOLD=$'\033[1m'; RESET=$'\033[0m'; RED=$'\033[0;31m'
    GREEN=$'\033[0;32m'; CYAN=$'\033[0;36m'
else
    BOLD=""; RESET=""; RED=""; GREEN=""; CYAN=""
fi

emit() { [ "$MODE" = github ] && echo "$1=$2" >> "${GITHUB_OUTPUT:-/dev/stdout}" || true; }
say()  { [ "$MODE" != github ] && echo -e "$1" || true; }

if ! git remote get-url upstream >/dev/null 2>&1; then
    echo "no 'upstream' remote. Run:" >&2
    echo "  git remote add upstream https://github.com/spotbye/SpotiFLAC.git" >&2
    exit 1
fi
git fetch upstream --quiet

UPSTREAM_HEAD=$(git rev-parse upstream/main)
UPSTREAM_DATE=$(git log -1 --format="%ci" upstream/main)
BASELINE=$(tr -d '[:space:]' < "$BASELINE_FILE")

emit upstream_head "$UPSTREAM_HEAD"
emit upstream_short "${UPSTREAM_HEAD:0:8}"

# ─── Do the values that ROTATE still match theirs? ───────────────────────────
#
# Deliberately above every early exit. This is not a file diff and it is not
# gated on the baseline: our copy can fall behind a rotation while nothing has
# "changed since the last review" — a triage that looked at spotify_metadata.go
# and decided not to port leaves exactly that state, and so does an
# auto-advanced marker. The question "are we still holding the same values" has
# to be asked every run or it is not being asked.
#
# What it can and cannot promise is in check-spotify-constants.py. Short
# version: it detects that we have fallen behind a source that usually knows,
# not that our path still works.
# python3 on a runner, python on a Git Bash box - and on Windows `python3`
# EXISTS on PATH as a Microsoft Store stub that prints an ad and exits 49.
# So presence is not the test; running is. `command -v python3` succeeded
# happily and every local invocation reported "the check could not run".
PY_BIN=""
for _py in python3 python; do
    if command -v "$_py" >/dev/null 2>&1 && "$_py" -c "pass" >/dev/null 2>&1; then
        PY_BIN="$_py"
        break
    fi
done

if [ "$MODE" = github ]; then
    SPOTIFY_MD=$("${PY_BIN:-python3}" ./check-spotify-constants.py --md 2>/dev/null \
        || echo "- ⚠️ the constants check could not run")
    [ -n "$SPOTIFY_MD" ] || SPOTIFY_MD="- ✅ aligned — TOTP secret, version and every persisted-query hash we issue"
    SPOTIFY_BAD=$(printf '%s\n' "$SPOTIFY_MD" | grep -c '⚠️' || true)
    emit spotify_drift_count "$SPOTIFY_BAD"
    {
        echo "spotify_drift<<__EOF__"
        printf '%s\n' "$SPOTIFY_MD"
        echo "__EOF__"
    } >> "${GITHUB_OUTPUT:-/dev/stdout}"
else
    say "$("${PY_BIN:-python3}" ./check-spotify-constants.py 2>/dev/null \
        || echo 'Spotify constants: the check could not run')"
    say ""
fi

if [ "$UPSTREAM_HEAD" = "$BASELINE" ]; then
    say "${GREEN}Up to date${RESET} — upstream is at the reviewed baseline (${UPSTREAM_HEAD:0:8})."
    emit has_changes false
    emit can_auto_advance false
    exit 0
fi

COMMIT_COUNT=$(git rev-list --count "${BASELINE}..upstream/main")

# ─── Which files changed, minus the ones we chose not to care about ──────────
# Scope: their Go source. Everything else (README, .github, assets) is noise for
# a port source — a docs change cannot contain logic we want.
CHANGED=$(git diff --name-only "${BASELINE}" upstream/main -- 'backend/*.go' '*.go' go.mod 2>/dev/null || true)

WATCHED=""
IGNORED_COUNT=0
while IFS= read -r f; do
    [ -n "$f" ] || continue
    key="${f#backend/}"
    if grep -qE "^${key//./\\.}\|" "$IGNORE_FILE" 2>/dev/null; then
        IGNORED_COUNT=$((IGNORED_COUNT + 1))
        continue
    fi
    WATCHED="${WATCHED}${f}"$'\n'
done <<< "$CHANGED"

WATCHED=$(printf '%s' "$WATCHED" | sed '/^$/d')
WATCHED_COUNT=$([ -z "$WATCHED" ] && echo 0 || printf '%s\n' "$WATCHED" | wc -l | tr -d ' ')

emit commit_count "$COMMIT_COUNT"
emit ignored_count "$IGNORED_COUNT"
emit watched_count "$WATCHED_COUNT"

# ─── Nothing we watch moved → no human needed, advance the marker ────────────
if [ "$WATCHED_COUNT" -eq 0 ]; then
    say "${GREEN}Nothing to review${RESET} — ${COMMIT_COUNT} commit(s) since ${BASELINE:0:8},"
    say "  all touching files we deliberately ignore (${IGNORED_COUNT}) or nothing of ours."
    say ""
    say "  Baseline can advance to ${UPSTREAM_HEAD:0:8} with no decision to make."
    emit has_changes false
    emit can_auto_advance true
    exit 0
fi

# ─── Something we watch moved → report it ────────────────────────────────────
emit has_changes true
emit can_auto_advance false

say ""
say "${BOLD}══ Upstream — spotbye/SpotiFLAC ════════════════════════${RESET}"
say "  HEAD      ${UPSTREAM_HEAD:0:8}  (${UPSTREAM_DATE:0:10})"
say "  baseline  ${BASELINE:0:8}"
say "  ${COMMIT_COUNT} commit(s), ${WATCHED_COUNT} watched file(s) changed, ${IGNORED_COUNT} ignored"
say ""

say "${BOLD}══ Files worth a look ══════════════════════════════════${RESET}"
while IFS= read -r f; do
    [ -n "$f" ] || continue
    ADDED=$(git diff "${BASELINE}" upstream/main -- "$f" | grep -c '^+[^+]' || true)
    REMOVED=$(git diff "${BASELINE}" upstream/main -- "$f" | grep -c '^-[^-]' || true)
    say "  ${RED}${f}${RESET} (+${ADDED}/-${REMOVED})"
    if [ "$MODE" = verbose ]; then
        say "${CYAN}$(git diff "${BASELINE}" upstream/main -- "$f")${RESET}"
    fi
done <<< "$WATCHED"
say ""

# Report the list in a form the workflow can paste into an issue body.
if [ "$MODE" = github ]; then
    {
        echo "watched_files<<__EOF__"
        while IFS= read -r f; do
            [ -n "$f" ] || continue
            ADDED=$(git diff "${BASELINE}" upstream/main -- "$f" | grep -c '^+[^+]' || true)
            REMOVED=$(git diff "${BASELINE}" upstream/main -- "$f" | grep -c '^-[^-]' || true)
            echo "- \`${f}\` (+${ADDED}/-${REMOVED})"
        done <<< "$WATCHED"
        echo "__EOF__"
    } >> "${GITHUB_OUTPUT:-/dev/stdout}"
fi

say "${BOLD}══ Once triaged ════════════════════════════════════════${RESET}"
say "  echo ${UPSTREAM_HEAD} > ${BASELINE_FILE}"
say "  git add ${BASELINE_FILE} && git commit -m 'chore(upstream): reviewed ${UPSTREAM_HEAD:0:8}'"
say ""
say "  Decided we do not want one of these at all? Add it to ${IGNORE_FILE}"
say "  with a reason — that is how it stops coming back."
say ""
