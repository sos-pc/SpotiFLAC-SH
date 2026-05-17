#!/usr/bin/env bash
# check-upstream.sh — Vérifie les changements upstream sur les fichiers trackés
# Usage: ./check-upstream.sh [--verbose]
#
# Placer à la racine de SpotiFLAC-web/
# Setup requis : git remote add upstream https://github.com/spotbye/SpotiFLAC.git

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

# ─────────────────────────────────────────────────────────────────────────────
# CLASSIFICATION DES FICHIERS
#
# PURE UPSTREAM : existent dans upstream ET chez nous, peu ou pas modifiés.
#   → checkout direct si pas de conflits
#
# NEW_UPSTREAM : existent dans upstream mais PAS chez nous localement.
#   → contenu potentiellement intéressant à intégrer manuellement
#
# NEVER_TRACK : nos fichiers custom (n'existent pas dans upstream ou
#   complètement réécrits). Jamais de sync.
#
# Fichiers NEVER_TRACK (pour mémoire, non checkés) :
#   backend/deezer.go       → supprimé dans upstream, on le garde
#   backend/downloader.go   → notre helper, n'existe pas upstream
#   backend/uploader.go     → notre helper, n'existe pas upstream
#   backend/spotify_metadata.go → notre réécriture de spotify.go/spotfetch.go
#   auth.go, server.go, jobs.go, watcher.go, history.go → 100% custom
#   frontend/src/           → 100% custom
# ─────────────────────────────────────────────────────────────────────────────

TRACKED_FILES=(
    "backend/tidal.go"
    "backend/amazon.go"
    "backend/qobuz.go"
    "backend/metadata.go"
    "backend/musicbrainz.go"
    "backend/songlink.go"
    "backend/cover.go"
    "backend/lyrics.go"
)

# Fichiers qui existent dans upstream mais pas (ou différemment nommés) chez nous.
# On vérifie s'ils ont changé dans upstream — intégration manuelle uniquement.
NEW_UPSTREAM_FILES=(
    "backend/spotfetch.go"   # upstream renommé depuis spotify.go — notre équivalent : backend/spotify_metadata.go
)

# Fichiers MIXED : base commune mais modifiés des deux côtés.
MIXED_FILES=(
    "app.go"
    "go.mod"
)

# ─── Vérifications préliminaires ─────────────────────────────────────────────
echo -e "${BOLD}╔══════════════════════════════════════════════════════╗${RESET}"
echo -e "${BOLD}║   SpotiFLAC — Upstream Sync Checker                 ║${RESET}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════╝${RESET}"
echo ""

if ! git remote get-url upstream &>/dev/null; then
    echo -e "${RED}✗ Remote 'upstream' non configuré.${RESET}"
    echo ""
    echo "  Exécuter :"
    echo "  git remote add upstream https://github.com/spotbye/SpotiFLAC.git"
    exit 1
fi

echo -e "${CYAN}→ Fetch upstream...${RESET}"
git fetch upstream --quiet
echo ""

UPSTREAM_HEAD=$(git rev-parse upstream/main)
UPSTREAM_DATE=$(git log -1 --format="%ci" upstream/main)
UPSTREAM_MSG=$(git log -1 --format="%s" upstream/main)
LOCAL_HEAD=$(git rev-parse HEAD)

echo -e "  Upstream HEAD : ${YELLOW}${UPSTREAM_HEAD:0:8}${RESET} — ${UPSTREAM_DATE:0:10} — ${UPSTREAM_MSG}"
echo -e "  Local HEAD    : ${YELLOW}${LOCAL_HEAD:0:8}${RESET}"
echo ""

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
echo -e "   (checkout direct si pas de conflits)"
echo ""

CHANGED_COUNT=0
UNCHANGED_COUNT=0

for FILE in "${TRACKED_FILES[@]}"; do
    if ! git show "upstream/main:${FILE}" &>/dev/null 2>&1; then
        echo -e "  ${YELLOW}⚠${RESET} ${FILE} — ${YELLOW}disparu de upstream${RESET} (supprimé ou renommé ?)"
        continue
    fi

    DIFF_OUTPUT=$(git diff "${COMMON_ANCESTOR}" upstream/main -- "${FILE}" 2>/dev/null || true)

    if [[ -z "$DIFF_OUTPUT" ]]; then
        echo -e "  ${GREEN}✓${RESET} ${FILE}"
        ((UNCHANGED_COUNT++)) || true
    else
        LINES_ADDED=$(echo "$DIFF_OUTPUT" | grep -c '^+[^+]' || true)
        LINES_REMOVED=$(echo "$DIFF_OUTPUT" | grep -c '^-[^-]' || true)
        echo -e "  ${RED}✗${RESET} ${FILE} — ${RED}+${LINES_ADDED} / -${LINES_REMOVED} lignes upstream${RESET}"
        ((CHANGED_COUNT++)) || true

        if $VERBOSE; then
            echo ""
            echo "$DIFF_OUTPUT" | head -80 | sed 's/^/      /'
            echo "      [...]"
            echo ""
        fi
    fi
done

echo ""
echo -e "  ${GREEN}✓ Synchronisés : ${UNCHANGED_COUNT}${RESET}  |  ${RED}✗ À mettre à jour : ${CHANGED_COUNT}${RESET}"
echo ""

# ─── Analyse fichiers NEW UPSTREAM (pas d'équivalent direct local) ───────────
echo -e "${BOLD}══ Nouveaux fichiers upstream (intégration manuelle) ═══${RESET}"
echo -e "   (existent upstream mais pas localement sous ce nom)"
echo ""

for FILE in "${NEW_UPSTREAM_FILES[@]}"; do
    if ! git show "upstream/main:${FILE}" &>/dev/null 2>&1; then
        echo -e "  ${YELLOW}?${RESET} ${FILE} — non trouvé dans upstream non plus (supprimé ?)"
        continue
    fi

    # Récupère la date du dernier commit upstream qui a touché ce fichier
    LAST_CHANGED=$(git log upstream/main --oneline -1 -- "${FILE}" 2>/dev/null || true)

    # Taille du fichier upstream
    LINE_COUNT=$(git show "upstream/main:${FILE}" 2>/dev/null | wc -l || echo "?")

    echo -e "  ${CYAN}~${RESET} ${FILE} — ${LINE_COUNT} lignes upstream"
    if [[ -n "$LAST_CHANGED" ]]; then
        echo -e "      Dernier commit : ${LAST_CHANGED}"
    fi

    # Afficher les notes associées si définies
    case "$FILE" in
        "backend/spotfetch.go")
            echo -e "      ${YELLOW}Note :${RESET} Renommé depuis spotify.go. Notre équivalent : backend/spotify_metadata.go"
            echo -e "      Contient : SpotifyClient TOTP, Filter{Track,Album,Playlist,Artist,Search}"
            echo -e "      Action   : comparer les fonctions Filter* avec notre spotify_metadata.go"
            ;;
    esac
    echo ""

    if $VERBOSE; then
        echo -e "      ${CYAN}--- Contenu upstream/${FILE} (50 premières lignes) ---${RESET}"
        git show "upstream/main:${FILE}" 2>/dev/null | head -50 | sed 's/^/      /'
        echo "      [...]"
        echo ""
    fi
done

# ─── Analyse fichiers MIXED ───────────────────────────────────────────────────
echo -e "${BOLD}══ Fichiers MIXED (inspection manuelle requise) ════════${RESET}"
echo ""

for FILE in "${MIXED_FILES[@]}"; do
    if ! git show "upstream/main:${FILE}" &>/dev/null 2>&1; then
        echo -e "  ${YELLOW}?${RESET} ${FILE} — non trouvé dans upstream"
        continue
    fi

    DIFF_OUTPUT=$(git diff "${COMMON_ANCESTOR}" upstream/main -- "${FILE}" 2>/dev/null || true)

    if [[ -z "$DIFF_OUTPUT" ]]; then
        echo -e "  ${GREEN}✓${RESET} ${FILE} — identique"
    else
        LINES_ADDED=$(echo "$DIFF_OUTPUT" | grep -c '^+[^+]' || true)
        LINES_REMOVED=$(echo "$DIFF_OUTPUT" | grep -c '^-[^-]' || true)
        echo -e "  ${YELLOW}~${RESET} ${FILE} — ${YELLOW}+${LINES_ADDED} / -${LINES_REMOVED} lignes upstream (merger manuellement)${RESET}"

        if $VERBOSE; then
            echo ""
            echo "$DIFF_OUTPUT" | head -80 | sed 's/^/      /'
            echo "      [...]"
            echo ""
        fi
    fi
done

echo ""

# ─── Commandes utiles ─────────────────────────────────────────────────────────
if [[ "$CHANGED_COUNT" -gt 0 ]]; then
    echo -e "${BOLD}══ Commandes pour intégrer les changements ════════════${RESET}"
    echo ""
    echo "  # Voir le diff détaillé d'un fichier (depuis le merge-base) :"
    echo "  git diff \$(git merge-base HEAD upstream/main) upstream/main -- backend/tidal.go | less"
    echo ""
    echo "  # Copier un fichier pure upstream directement (si aucun conflit) :"
    echo "  git checkout upstream/main -- backend/tidal.go"
    echo "  git commit -m 'chore: sync backend/tidal.go from upstream'"
    echo ""
    echo "  # Voir le contenu d'un fichier upstream sans l'appliquer :"
    echo "  git show upstream/main:backend/spotfetch.go | less"
    echo ""
fi

echo -e "${BOLD}══ Résumé ════════════════════════════════════════════${RESET}"
echo ""
if [[ "$CHANGED_COUNT" -eq 0 ]]; then
    echo -e "  ${GREEN}Fichiers pure upstream : tous à jour.${RESET}"
else
    echo -e "  ${RED}${CHANGED_COUNT} fichier(s) pure upstream à mettre à jour.${RESET}"
    echo -e "  Lance ${CYAN}--verbose${RESET} pour voir les diffs inline."
fi
echo ""