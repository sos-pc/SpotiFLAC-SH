# Rattrapage upstream — spotbye/SpotiFLAC

> **Périmètre.** Ce document suit le rattrapage manuel de ~6 mois de dérive avec l'upstream
> (`spotbye/SpotiFLAC`), jamais trié depuis le fork initial. C'est distinct du système automatique
> (`.github/workflows/upstream-check.yml`, `check-upstream.sh`, `.github/upstream-map.txt`), qui
> détecte et classe les fichiers modifiés mais **ne lit ni ne juge leur contenu** — ce document est
> la couche humaine par-dessus : qu'est-ce que chaque fichier fait vraiment, qu'est-ce qui vaut le
> coup de porter, et pourquoi.
>
> **Contrainte directrice.** Même principe que `audit-refactoring-couche2.md` : chaque finding est
> vérifié par lecture directe (pas déduit du nom de fichier ni de la taille du diff), un sujet à la
> fois, present 2-3 options avant toute décision structurante, build+vet+test avant chaque commit.
>
> **Baseline actuelle :** `00d3fb92` (2026-02-26) → upstream HEAD `3f755f58` (2026-07-12), 33 commits
> (13 de code, 20 README/docs-only). Voir l'issue GitHub `upstream-sync` pour l'état live du diff.

---

## 0. Résumé exécutif

| ID | Sujet | Fichiers upstream | Statut | Priorité |
|----|-------|--------------------|--------|----------|
| **S1** ✅ | Infra communautaire partagée | community_apikey, community_endpoints, tidal_community, qobuz_community, http_headers, provider_endpoints, download_cancel | **fait** — verdicts rendus | — |
| **S2** | Validation post-téléchargement | download_validation.go | Trouvaille concrète, prête à porter | **1** |
| **S3** | Retry/cooldown réseau (429/503) | community_endpoints.go (`doCommunityRequest`) | Pattern identifié, à adapter | **1** |
| **S4** | Déchiffrement DRM | mp4ff_decrypt.go | À part — décision utilisateur requise avant tout triage technique | — |
| **S5** | Client Tidal | tidal.go, tidal_community.go | Pas encore lu en détail | 3 |
| **S6** | Client Qobuz | qobuz.go, qobuz_api.go, qobuz_community.go | Piste sérieuse trouvée (voir §S6) | **2** |
| **S7** | Client Amazon | amazon.go | Pas encore lu en détail | 3 |
| **S8** | Client Spotify + résolution d'identifiants | spotfetch, spotify_metadata, spotify_totp, isrc_cache, isrc_finder, isrc_helper | Scope corrigé après lecture (voir §S8) | 3 |
| **S9** | Résolution de liens cross-provider | songlink.go, link_resolver.go, songstats.go | Sujet retrouvé (absent du premier passage) | 3 |
| **S10** | Enrichissement métadonnées | musicbrainz.go, cover.go, lyrics.go, lyrics_reader.go | Lien direct avec R10 (voir §S10) | **2** |
| **S11** | Lecture/écriture de tags | metadata.go, tagging.go, upc_tags.go | Sujet retrouvé, lié à une nouvelle dépendance (voir §S11) | 4 |
| **S12** | Formatage noms/artistes | artist_format.go, filename.go | Pas encore lu en détail | 4 |
| **S13** | Utilitaires bas signal | config, progress, filemanager, history, analysis, ffmpeg, resample, recent_fetches | Balayage rapide seulement | 5 |
| **S14** | go.mod | dépendances upstream | Diff déjà vu, pas encore creusé | 5 |

**Recommandation de séquencement.** S2 et S3 sont des gains rapides déjà cernés. S6 et S10 sont
prioritaires parce qu'ils touchent des problèmes déjà ouverts chez nous (bug Qobuz 401 parké, R10
sur-sélection genre déférée). S4 est hors séquence — c'est une question de fond, pas un item technique
à prioriser normalement.

---

## 1. Sujets détaillés

### S1 — Infra communautaire partagée ✅

**Fait le 2026-07-14.** Fichiers : `community_apikey.go`, `community_endpoints.go`,
`tidal_community.go`, `qobuz_community.go`, `http_headers.go`, `provider_endpoints.go`,
`download_cancel.go`.

- `community_apikey.go` / `community_endpoints.go` contiennent une clé API et des URLs de proxy
  communautaires **chiffrées en AES-GCM avec la clé de déchiffrement dérivable d'un literal dans le
  même fichier** — trivialement déchiffrable mais délibérément obscurci. **Décision : jamais déchiffré
  ni réutilisé**, service privé de l'upstream. Deux items exploitables en ont été extraits sans
  toucher aux secrets : S2 (validation durée) et S3 (pattern retry/cooldown).
- `http_headers.go` / `provider_endpoints.go` : déjà équivalents chez nous
  (`backend/providerutil/useragent.go`, endpoint Amazon `amazon.spotbye.qzz.io` déjà identique).
- `download_cancel.go` : système d'annulation globale "stop all downloads", conçu pour une appli
  desktop mono-utilisateur. Non applicable à notre serveur multi-utilisateur (annulerait les
  téléchargements de tout le monde à la fois). On annule par job dans `jobs_worker.go`.

### S2 — Validation post-téléchargement

**Fichier :** `download_validation.go` (44 lignes, nouveau upstream).

Compare la durée réelle du fichier téléchargé à la durée Spotify attendue : si le fichier fait ~30s
alors qu'on attendait 60s+, c'est très probablement un **preview Tidal glissé à la place du morceau
complet** — le fichier est rejeté. Vérifié : on n'a **aucun équivalent** (`Duration` ne sert que pour
les paroles et l'affichage). Directement lié au problème documenté dans le README (proxys Tidal
communautaires limités à `assetPresentation: PREVIEW` sans compte Premium).

**Port cible :** `jobs_worker.go` / `backend/audio`, pas une copie telle quelle.

### S3 — Retry/cooldown réseau (429/503)

**Fichier source :** `community_endpoints.go`, fonction `doCommunityRequest`.

Respecte `Retry-After`, gère un cooldown propre sur 503 (message utilisateur), retente jusqu'à 6x sur
429/502/504. Vérifié : **zéro occurrence** de 429/`Retry-After`/503 dans `backend/tidal/`,
`backend/qobuz/`, `backend/amazon/` — aucune résilience actuelle face au rate-limit. Le code exact ne
se porte pas (câblé à leurs URLs chiffrées, voir S1) mais le pattern est indépendant.

**Note :** upstream branche ce pattern sur son état global `progress.go` (`SetRateLimitCooldown` etc.)
— voir S13, ce pattern de queue globale est celui qu'on a déjà supprimé nous-mêmes en v3.4.0. Ne pas
réintroduire cette dépendance ; brancher plutôt sur notre système SSE/jobs existant.

### S4 — Déchiffrement DRM (mp4ff_decrypt.go)

**Statut : en attente d'une décision utilisateur, volontairement pas trianger techniquement.**

247 lignes + nouvelle dépendance `github.com/Eyevinn/mp4ff v0.52.0`. Déchiffrement CENC de contenu
MP4 chiffré (clés par piste). Différent en nature des autres fichiers de ce rattrapage — question de
conformité/ToS à trancher explicitement avant toute évaluation technique.

### S5 — Client Tidal

**Fichiers :** `tidal.go` (+270/-343 depuis la baseline), `tidal_community.go`. Pas encore lu en
détail. Notre équivalent : `backend/tidal/{client,params,auth,device}.go`.

### S6 — Client Qobuz

**Fichiers :** `qobuz.go` (+350/-125), `qobuz_api.go` (+343/-0, nouveau), `qobuz_community.go`.

**`qobuz_api.go` scrape et met en cache les identifiants "open app" de Qobuz** pour signer des
requêtes directement contre leur API — une voie différente de notre `musicdl.me` (qui retourne 401
depuis longtemps, problème parké dans les handoffs précédents) et différente aussi du simple proxy
communautaire. **Piste sérieuse pour résoudre le bug Qobuz 401** — pas encore vérifiée en profondeur,
mais c'est la trouvaille la plus prometteuse de tout ce rattrapage à ce stade.

### S7 — Client Amazon

**Fichier :** `amazon.go` (+170/-145). Pas encore lu en détail. Endpoint de base déjà identique aux
deux côtés (`amazon.spotbye.qzz.io`, voir S1) donc probablement des ajustements plus fins à comparer,
pas un changement d'infra.

### S8 — Client Spotify + résolution d'identifiants

**Fichiers :** `spotfetch.go`, `spotify_metadata.go`, `spotify_totp.go`, `isrc_cache.go`,
`isrc_finder.go`, `isrc_helper.go`.

**Correction par rapport au découpage initial (basé sur les noms de fichiers) :** les fichiers
`isrc_*` ne sont **pas** du matching de bibliothèque locale. En lisant le contenu : c'est un client
Spotify à **token anonyme** (`spotifyAnonymousToken`, `fetchSpotifyTrackRawData`), une deuxième voie
indépendante du client TOTP de `spotfetch.go` pour résoudre l'ISRC d'un morceau. `spotify_metadata.go`
est uniquement des définitions de types (pas de logique) — le pendant de nos `apiTrackResponse` etc.
issus de R2.

**À faire avant tout portage :** comparer `spotfetch.go` (fonctions `Filter*`) avec la réécriture
typée de R2 (`docs/audit-refactoring-couche2.md`) pour ne pas revenir en arrière dessus.

### S9 — Résolution de liens cross-provider

**Fichiers :** `songlink.go`, `link_resolver.go`, `songstats.go`.

**Sujet complètement absent du premier découpage** (basé sur les noms de fichiers) — `songlink.go`
n'était assigné nulle part. Ensemble cohérent : comment on retrouve l'URL Tidal/Qobuz/Amazon/Deezer
d'un morceau Spotify (scraping Song.link/Odesli, API Songstats, lookup Deezer par ISRC, vérification
de dispo Qobuz par slug URL). Notre équivalent : `backend/songlink/`.

### S10 — Enrichissement métadonnées

**Fichiers :** `musicbrainz.go`, `cover.go`, `lyrics.go`, `lyrics_reader.go` (nouveau, 302 lignes).

**`musicbrainz.go` contient la logique de throttling/dédup/genre-fetch — probablement la réponse à
R10** (`retag-incomplete-metadata` sur-sélectionne sur le genre et skip ~99% des candidats, déféré
explicitement par l'utilisateur : *"on verra ça plus tard quand on fera une analyse de l'upstream, on
cherchera le moyen d'utiliser le bon service de tag pour le genre"* — `docs/audit-refactoring-couche2.md`
§6). **C'est ce moment.** Ne pas le perdre de vue en traitant ce sujet.

`cover.go` est plus large que son nom : couvre aussi avatars/headers/galerie d'images de profil, pas
seulement les pochettes de morceau.

### S11 — Lecture/écriture de tags

**Fichiers :** `metadata.go` (549 lignes), `tagging.go` (nouveau, 449 lignes), `upc_tags.go`.

**Sujet complètement absent du premier découpage** — `metadata.go` n'était assigné nulle part.
`tagging.go` est construit autour d'une **nouvelle dépendance upstream, `go.senan.xyz/taglib`** (voir
S14/go.mod) — a priori une migration en cours chez eux depuis le code par-format fait main de
`metadata.go` vers une lib unifiée. Comparer les deux avant de porter l'un ou l'autre. Notre
équivalent : `backend/meta/metadata.go` + `backend/meta/tag_write_lock.go`.

### S12 — Formatage noms/artistes

**Fichiers :** `artist_format.go`, `filename.go`. Pas encore lu en détail. `artist_format.go`
(`SplitArtistCredits`) est probablement lié à notre réglage `useFirstArtistOnly`.

### S13 — Utilitaires bas signal

**Fichiers :** `config.go`, `progress.go`, `filemanager.go`, `history.go`, `analysis.go`, `ffmpeg.go`,
`resample.go`, `recent_fetches.go`.

`progress.go` confirmé après lecture : pattern d'état global de queue/progress typique Wails —
exactement ce qu'on a supprimé nous-mêmes en v3.4.0 ("removed dual queue system"). Rien à porter,
juste une confirmation qu'on a eu raison. `resample.go` (feature de resampling FLAC) : à vérifier si
déjà couvert par notre "audio converter" existant ou vraiment nouveau — pas encore tranché, voir
`.github/upstream-map.txt` (laissé volontairement `UNMAPPED`).

### S14 — go.mod

Diff déjà vu : `+github.com/Eyevinn/mp4ff v0.52.0` (S4), `+go.senan.xyz/taglib v0.11.1` (S11),
Wails v2.11.0→v2.12.0 (n/a), `-github.com/mewkiz/flac v1.0.13` — upstream a retiré cette dépendance
qu'on utilise encore nous-mêmes ; probablement remplacée par `taglib`, à confirmer avant de
s'inquiéter.

---

## 2. Où sont les choses

- `.github/upstream-map.txt` — classification mécanique fichier par fichier (MAPPED/IGNORE/non listé
  = UNMAPPED), lue par le workflow et `check-upstream.sh`. Source de vérité pour "où ça mappe".
- Issue GitHub label `upstream-sync` — état live du diff (commits, fichiers modifiés depuis la
  dernière baseline), régénérée automatiquement chaque lundi ou sur `workflow_dispatch`.
- Ce document — pourquoi chaque sujet compte, dans quel ordre, et ce qu'on a déjà appris en lisant le
  code. Source de vérité pour le contenu et la priorité.
- `.github/upstream-last-reviewed.txt` — SHA de la baseline actuelle. À avancer manuellement une fois
  un sujet réellement traité (voir instructions dans l'issue `upstream-sync`).
