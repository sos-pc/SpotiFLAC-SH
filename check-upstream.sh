#!/usr/bin/env bash
# check-upstream.sh — Surveille les changements upstream pertinents pour nous.
# Usage: ./check-upstream.sh [--verbose]
#
# Placer à la racine du repo.
# Setup requis : git remote add upstream https://github.com/spotbye/SpotiFLAC.git
#
# Contrairement à l'ancienne version, ce script :
#   1. Découvre automatiquement TOUS les fichiers backend/*.go (+ app.go,
#      main.go, go.mod) côté upstream, au lieu d'une liste figée — un
#      nouveau fichier upstream ne peut plus passer inaperçu.
#   2. Classe chaque fichier via .github/upstream-map.txt (MAPPED / IGNORE),
#      tout le reste = UNMAPPED (triage manuel requis).
#   3. Compare depuis .github/upstream-last-reviewed.txt, PAS depuis
#      `git merge-base` — ce dernier n'avance jamais puisqu'on ne fait
#      jamais de vrai merge, donc le diff resterait cumulatif à l'infini.
#      Après un triage, avancer manuellement ce marqueur (voir le rappel en
#      fin de script).
#   4. Ne liste que ce qui a réellement changé — pas de bruit sur ce qui est
#      stable.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

VERBOSE=false
if [[ "${1:-}" == "--verbose" || "${1:-}" == "-v" ]]; then
    VERBOSE=true
fi

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

MAP_FILE=".github/upstream-map.txt"
BASELINE_FILE=".github/upstream-last-reviewed.txt"

echo -e "${BOLD}╔══════════════════════════════════════════════════════╗${RESET}"
echo -e "${BOLD}║   SpotiFLAC — Upstream Watch                        ║${RESET}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════╝${RESET}"
echo ""

if ! git remote get-url upstream &>/dev/null; then
    echo -e "${RED}✗ Remote 'upstream' non configuré.${RESET}"
    echo "  git remote add upstream https://github.com/spotbye/SpotiFLAC.git"
    exit 1
fi

echo -e "${CYAN}→ Fetch upstream...${RESET}"
git fetch upstream --quiet
echo ""

UPSTREAM_HEAD=$(git rev-parse upstream/main)
UPSTREAM_DATE=$(git log -1 --format="%ci" upstream/main)
BASELINE=$(cat "$BASELINE_FILE")

echo -e "  Upstream HEAD : ${YELLOW}${UPSTREAM_HEAD:0:8}${RESET} — ${UPSTREAM_DATE:0:10}"
echo -e "  Baseline      : ${YELLOW}${BASELINE:0:8}${RESET} (dernier triage — ${BASELINE_FILE})"
echo ""

if [[ "$UPSTREAM_HEAD" == "$BASELINE" ]]; then
    echo -e "${GREEN}✓ Rien de nouveau depuis le dernier triage.${RESET}"
    exit 0
fi

COMMIT_COUNT=$(git rev-list --count "${BASELINE}..upstream/main" 2>/dev/null || echo "?")
echo -e "${YELLOW}! ${COMMIT_COUNT} commits upstream depuis le dernier triage${RESET}"
echo ""

# ─── Commits : séparer code vs docs-only ─────────────────────────────────────
echo -e "${BOLD}Commits de code (hors README/docs) :${RESET}"
CODE_COMMITS=0
DOC_ONLY_COMMITS=0
for SHA in $(git rev-list "${BASELINE}..upstream/main" --reverse); do
    TOUCHED=$(git show --name-only --format="" "$SHA")
    NON_DOC=$(echo "$TOUCHED" | grep -vE '^(README\.md|docs/|\.github/)' || true)
    if [[ -n "$NON_DOC" ]]; then
        MSG=$(git log -1 --format="%h %s" "$SHA")
        echo "  $MSG"
        ((CODE_COMMITS++)) || true
    else
        ((DOC_ONLY_COMMITS++)) || true
    fi
done
[[ "$CODE_COMMITS" -eq 0 ]] && echo "  (aucun)"
echo ""
echo -e "  ${CODE_COMMITS} commits de code, ${DOC_ONLY_COMMITS} commits README/docs-only (masqués)"
echo ""

if [[ "$CODE_COMMITS" -eq 0 ]]; then
    echo -e "${GREEN}✓ Tous les commits depuis le dernier triage sont README/docs-only. Rien à trianger.${RESET}"
    echo ""
    echo -e "  Avancer quand même le marqueur pour ne pas les revoir la prochaine fois :"
    echo -e "  ${CYAN}echo ${UPSTREAM_HEAD} > ${BASELINE_FILE}${RESET}"
    exit 0
fi

# ─── Charger la classification ───────────────────────────────────────────────
declare -A MAP_STATUS
declare -A MAP_NOTE
while IFS='|' read -r path status note; do
    [[ -z "$path" || "$path" == \#* ]] && continue
    MAP_STATUS["$path"]="$status"
    MAP_NOTE["$path"]="$note"
done < "$MAP_FILE"

# ─── Découverte auto des fichiers upstream ───────────────────────────────────
UPSTREAM_FILES=$(git ls-tree -r --name-only upstream/main -- backend/ 2>/dev/null | grep '\.go$' || true)
UPSTREAM_FILES="${UPSTREAM_FILES}
$(git ls-tree upstream/main -- app.go main.go 2>/dev/null | awk '{print $4}')"

MAPPED_CHANGED=""
UNMAPPED_CHANGED=""
IGNORED_COUNT=0
MAPPED_UNCHANGED_COUNT=0

while IFS= read -r FULLPATH; do
    [[ -z "$FULLPATH" ]] && continue
    KEY="$FULLPATH"
    [[ "$FULLPATH" == backend/* ]] && KEY="${FULLPATH#backend/}"

    STATUS="${MAP_STATUS[$KEY]:-UNMAPPED}"
    NOTE="${MAP_NOTE[$KEY]:-needs manual triage — not yet classified in $MAP_FILE}"

    if [[ "$STATUS" == "IGNORE" ]]; then
        ((IGNORED_COUNT++)) || true
        continue
    fi

    DIFF_OUTPUT=$(git diff "${BASELINE}" upstream/main -- "$FULLPATH" 2>/dev/null || true)
    if [[ -z "$DIFF_OUTPUT" ]]; then
        [[ "$STATUS" == "MAPPED" ]] && ((MAPPED_UNCHANGED_COUNT++)) || true
        continue
    fi

    ADDED=$(echo "$DIFF_OUTPUT" | grep -c '^+[^+]' || true)
    REMOVED=$(echo "$DIFF_OUTPUT" | grep -c '^-[^-]' || true)
    LINE="  ${FULLPATH} (+${ADDED}/-${REMOVED}) → ${NOTE}"

    if [[ "$STATUS" == "MAPPED" ]]; then
        MAPPED_CHANGED="${MAPPED_CHANGED}${LINE}\n"
    else
        UNMAPPED_CHANGED="${UNMAPPED_CHANGED}${LINE}\n"
    fi

    if $VERBOSE; then
        echo -e "${CYAN}--- $FULLPATH ---${RESET}"
        echo "$DIFF_OUTPUT" | head -60
        echo "  [...]"
        echo ""
    fi
done <<< "$UPSTREAM_FILES"

echo -e "${BOLD}══ Fichiers MAPPED modifiés (on sait où regarder) ══════${RESET}"
[[ -n "$MAPPED_CHANGED" ]] && echo -e "$MAPPED_CHANGED" || echo "  (aucun)"
echo ""

echo -e "${BOLD}══ Fichiers UNMAPPED modifiés (triage manuel requis) ═══${RESET}"
[[ -n "$UNMAPPED_CHANGED" ]] && echo -e "$UNMAPPED_CHANGED" || echo "  (aucun)"
echo ""

echo -e "  ${GREEN}${MAPPED_UNCHANGED_COUNT} fichiers mappés inchangés${RESET}  |  ${CYAN}${IGNORED_COUNT} fichiers ignorés (desktop-only)${RESET}"
echo ""

# ─── go.mod : dérive des dépendances ─────────────────────────────────────────
GOMOD_DIFF=$(git diff "${BASELINE}" upstream/main -- go.mod 2>/dev/null || true)
if [[ -n "$GOMOD_DIFF" ]]; then
    echo -e "${BOLD}══ go.mod — dépendances upstream modifiées ═════════════${RESET}"
    echo "$GOMOD_DIFF" | grep -E '^[+-][^+-]' | head -20
    echo ""
fi

echo -e "${BOLD}══ Une fois trianger ═══════════════════════════════════${RESET}"
echo "  echo ${UPSTREAM_HEAD} > ${BASELINE_FILE}"
echo "  git add ${BASELINE_FILE} && git commit -m 'chore(upstream): mark ${UPSTREAM_HEAD:0:8} as reviewed'"
echo ""
