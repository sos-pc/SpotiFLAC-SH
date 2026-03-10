#!/usr/bin/env bash
# check-upstream.sh — Vérifie les changements upstream sur les fichiers trackés
# Usage: ./check-upstream.sh [--verbose]
#
# Placer à la racine de SpotiFLAC-web/
# Setup requis : git remote add upstream https://github.com/afkarxyz/SpotiFLAC.git

set -euo pipefail

VERBOSE=false
if [[ "${1:-}" == "--verbose" || "${1:-}" == "-v" ]]; then
    VERBOSE=true
fi

# ─── Couleurs ────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

# ─── Fichiers à tracker (PURE UPSTREAM) ──────────────────────────────────────
TRACKED_FILES=(
    "backend/tidal.go"
    "backend/deezer.go"
    "backend/amazon.go"
    "backend/qobuz.go"
    "backend/spotify.go"
    "backend/metadata.go"
    "backend/musicbrainz.go"
    "backend/songlink.go"
    "backend/uploader.go"
    "backend/cover.go"
    "backend/lyrics.go"
    "backend/downloader.go"
)

# ─── Fichiers MIXED (à inspecter manuellement) ───────────────────────────────
MIXED_FILES=(
    "app.go"
    "go.mod"
)

# ─── Vérifications préliminaires ─────────────────────────────────────────────
echo -e "${BOLD}╔══════════════════════════════════════════════════════╗${RESET}"
echo -e "${BOLD}║   SpotiFLAC — Upstream Sync Checker                 ║${RESET}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════╝${RESET}"
echo ""

# Vérifier que upstream est configuré
if ! git remote get-url upstream &>/dev/null; then
    echo -e "${RED}✗ Remote 'upstream' non configuré.${RESET}"
    echo ""
    echo "  Exécuter:"
    echo "  git remote add upstream https://github.com/afkarxyz/SpotiFLAC.git"
    exit 1
fi

echo -e "${CYAN}→ Fetch upstream...${RESET}"
git fetch upstream --quiet
echo ""

# Commit upstream le plus récent
UPSTREAM_HEAD=$(git rev-parse upstream/main)
UPSTREAM_DATE=$(git log -1 --format="%ci" upstream/main)
UPSTREAM_MSG=$(git log -1 --format="%s" upstream/main)
LOCAL_HEAD=$(git rev-parse HEAD)

echo -e "  Upstream HEAD : ${YELLOW}${UPSTREAM_HEAD:0:8}${RESET} — ${UPSTREAM_DATE:0:10} — ${UPSTREAM_MSG}"
echo -e "  Local HEAD    : ${YELLOW}${LOCAL_HEAD:0:8}${RESET}"
echo ""

# ─── Commits upstream depuis le dernier sync ─────────────────────────────────
COMMON_ANCESTOR=$(git merge-base HEAD upstream/main)
COMMIT_COUNT=$(git rev-list --count "${COMMON_ANCESTOR}..upstream/main")

if [[ "$COMMIT_COUNT" -eq 0 ]]; then
    echo -e "${GREEN}✓ Aucun nouveau commit upstream depuis le dernier sync.${RESET}"
    exit 0
fi

echo -e "${YELLOW}! ${COMMIT_COUNT} nouveaux commits dans upstream/main depuis le dernier sync commun${RESET}"
echo ""

echo -e "${BOLD}Commits upstream récents :${RESET}"
git log "${COMMON_ANCESTOR}..upstream/main" --oneline | while read -r line; do
    echo "  ${line}"
done
echo ""

# ─── Analyse fichiers PURE UPSTREAM ──────────────────────────────────────────
echo -e "${BOLD}══ Fichiers PURE UPSTREAM ══════════════════════════════${RESET}"
echo ""

CHANGED_COUNT=0
UNCHANGED_COUNT=0
MISSING_UPSTREAM=0

for FILE in "${TRACKED_FILES[@]}"; do
    # Vérifier si le fichier existe dans upstream
    if ! git show "upstream/main:${FILE}" &>/dev/null 2>&1; then
        echo -e "  ${YELLOW}?${RESET} ${FILE} — ${YELLOW}non trouvé dans upstream${RESET} (nouveau fichier local?)"
        ((MISSING_UPSTREAM++)) || true
        continue
    fi

    # Diff entre notre version et upstream
    DIFF_OUTPUT=$(git diff HEAD upstream/main -- "${FILE}" 2>/dev/null || true)

    if [[ -z "$DIFF_OUTPUT" ]]; then
        echo -e "  ${GREEN}✓${RESET} ${FILE}"
        ((UNCHANGED_COUNT++)) || true
    else
        LINES_ADDED=$(echo "$DIFF_OUTPUT" | grep -c '^+[^+]' || true)
        LINES_REMOVED=$(echo "$DIFF_OUTPUT" | grep -c '^-[^-]' || true)
        echo -e "  ${RED}✗${RESET} ${FILE} — ${RED}+${LINES_ADDED} / -${LINES_REMOVED} lignes${RESET}"
        ((CHANGED_COUNT++)) || true

        if $VERBOSE; then
            echo ""
            echo "$DIFF_OUTPUT" | head -60 | sed 's/^/      /'
            echo "      ..."
            echo ""
        fi
    fi
done

echo ""
echo -e "  ${GREEN}✓ Synchronisés : ${UNCHANGED_COUNT}${RESET}  |  ${RED}✗ Modifiés : ${CHANGED_COUNT}${RESET}  |  ${YELLOW}? Absents upstream : ${MISSING_UPSTREAM}${RESET}"
echo ""

# ─── Analyse fichiers MIXED ───────────────────────────────────────────────────
echo -e "${BOLD}══ Fichiers MIXED (inspection manuelle conseillée) ═════${RESET}"
echo ""

for FILE in "${MIXED_FILES[@]}"; do
    if ! git show "upstream/main:${FILE}" &>/dev/null 2>&1; then
        echo -e "  ${YELLOW}?${RESET} ${FILE} — non trouvé dans upstream"
        continue
    fi

    DIFF_OUTPUT=$(git diff HEAD upstream/main -- "${FILE}" 2>/dev/null || true)

    if [[ -z "$DIFF_OUTPUT" ]]; then
        echo -e "  ${GREEN}✓${RESET} ${FILE} — identique"
    else
        LINES_ADDED=$(echo "$DIFF_OUTPUT" | grep -c '^+[^+]' || true)
        LINES_REMOVED=$(echo "$DIFF_OUTPUT" | grep -c '^-[^-]' || true)
        echo -e "  ${YELLOW}~${RESET} ${FILE} — ${YELLOW}+${LINES_ADDED} / -${LINES_REMOVED} lignes (merger manuellement)${RESET}"
    fi
done

echo ""

# ─── Commandes utiles ─────────────────────────────────────────────────────────
if [[ "$CHANGED_COUNT" -gt 0 ]]; then
    echo -e "${BOLD}══ Commandes pour intégrer les changements ════════════${RESET}"
    echo ""
    echo "  # Voir le diff détaillé d'un fichier :"
    echo "  git diff HEAD upstream/main -- backend/tidal.go | less"
    echo ""
    echo "  # Copier un fichier pure upstream directement :"
    echo "  git checkout upstream/main -- backend/deezer.go"
    echo "  git add backend/deezer.go"
    echo "  git commit -m 'chore: sync backend/deezer.go from upstream'"
    echo ""
    echo "  # Voir tous les fichiers upstream modifiés en une commande :"
    echo "  git diff --stat HEAD upstream/main -- backend/"
    echo ""
fi

echo -e "${BOLD}══ Résumé ════════════════════════════════════════════${RESET}"
echo ""
if [[ "$CHANGED_COUNT" -eq 0 ]]; then
    echo -e "  ${GREEN}Tout est à jour ! Aucune action requise.${RESET}"
else
    echo -e "  ${RED}${CHANGED_COUNT} fichier(s) upstream ont évolué.${RESET}"
    echo -e "  Utilise ${CYAN}--verbose${RESET} pour voir les diffs inline."
    echo -e "  Consulte ${CYAN}UPSTREAM-SYNC-STRATEGY.md${RESET} pour le workflow complet."
fi
echo ""
