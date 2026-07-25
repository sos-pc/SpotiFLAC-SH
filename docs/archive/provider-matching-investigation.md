# Enquête — Matching cross-provider (Spotify → Qobuz/Amazon)

> **🔍 Investigation en cours — démarrée le 2026-07-21.**
> Ce document consigne les découvertes, les tests, les comparaisons upstream,
> et les décisions au fur et à mesure. Édité live pendant la session.

## 0. Problème de départ

Les téléchargements Qobuz échouent quasi-systématiquement avec :
```
track not found for ISRC: XXXXX
```

Le problème est dans `searchByISRC` (`backend/qobuz/client.go:83`).

## 1. La chaîne actuelle (Spotify → Qobuz)

```
DownloadTrack(spotifyID)
  → songlink.GetISRC(spotifyID)
      → ① GetISRCDirect → Spotify API /v1/tracks/{id} → ISRC ✅ (95% des cas)
      → ② UNIQUEMENT si ① échoue → Song.link → Deezer → ISRC
  → searchByISRC(isrc)
      → Qobuz API track/search?query={ISRC} → ❌ 0 résultat
  → GetDownloadURL(trackID) → proxy communauté qbz-oss.spotbye.qzz.io/api/dl
```

**Note :** `GetISRCDirect` interroge l'API Spotify directement, pas Song.link.
Song.link n'est appelé QUE si Spotify échoue (fallback uniquement).

## 2. Tests effectués le 2026-07-21

### 2.1 Song.link — API (api.song.link/v1-alpha.1/links)

| Track | HTTP | Plateformes | Qobuz ? |
|---|---|---|---|
| Daft Punk - Get Lucky | 400 | `could_not_fetch_entity_data` | ❌ |
| Dizzy - Mohawk | 200 | napster, pandora, yandex, spotify | ❌ |
| Billie Eilish - bad guy | 200 | 11 plateformes (dont tidal, deezer, amazon) | ❌ |

**Conclusion : Song.link ne renvoie PAS de lien Qobuz.** La clé `qobuz` est absente
de `linksByPlatform` pour tous les tests. Song.link n'indexe pas Qobuz.

### 2.2 Song.link — Scrape HTML (song.link/s/{spotifyID})

```
HTTP 200 → __NEXT_DATA__ → pageProps.error.statusCode: 400
```

La page Next.js rend une erreur 400. Le `pageData` ne contient aucun `linksByPlatform`.
**Conclusion : le scrape HTML est mort** (changement côté Song.link).

### 2.3 Qobuz — ISRC search (méthode actuelle)

| Track | ISRC | Résultat |
|---|---|---|
| Mohawk - Dizzy Gillespie | USMC14610158 | ❌ 0 |
| Bloomdido - Dizzy Gillespie | USMC14610157 | ❌ 0 |

**Conclusion : l'index ISRC de Qobuz est lacunaire.** L'ISRC est bien présent dans les
métadonnées Qobuz (visible dans les résultats texte), mais le moteur de recherche
`/track/search` ne l'indexe pas.

### 2.4 Qobuz — Text search (nouvelle approche)

| Requête | Total | Top match pertinent |
|---|---|---|
| `Mohawk Dizzy Gillespie` | 50 | Position 5 : Dizzy Gillespie (ID=50684996) |
| `Bloomdido Dizzy Gillespie` | 50 | Position 4 : Dizzy Gillespie (ID=7246950) |
| `Get Lucky Daft Punk` | 466 | Position 1 : Daft Punk (ID=9140031) |
| `bad guy Billie Eilish` | 1000 | Position 1 : Billie Eilish (ID=77469835) |

**Conclusion : la recherche texte fonctionne parfaitement.** Il faut un filtrage
par nom d'artiste pour éliminer les covers/homonymes (ex: Charlie Parker apparaît
avant Dizzy Gillespie pour "Mohawk").

## 3. Comment l'upstream fait (spotbye/SpotiFLAC)

### 3.1 Chaîne de résolution Qobuz upstream

```
GetISRCDirect(spotifyID) → ISRC
  → qobuz_api.go : recherche ISRC signée (track/search?query=ISRC)
  → SI ÉCHEC → recherche par "track title + artist name"
  → scoreQobuzSearchCandidate() : scoring des résultats
```

**Différences clés avec notre code :**

| Étape | Nous | Upstream |
|---|---|---|
| Recherche ISRC | `searchByISRC` — signée mais ISRC non indexé | Idem, mais avec fallback |
| Fallback si ISRC échoue | ❌ Aucun — échec direct | ✅ Recherche par titre+artiste |
| Scoring des résultats | ❌ `Items[0]` sans filtre | ✅ `scoreQobuzSearchCandidate` : normalise titre/artiste/album, pénalise "karaoke/instrumental/cover" |
| Credentials Qobuz | Paire codée en dur (`712109809`) | `qobuz_api.go` : scrape le bundle JS du web player + cache 24h + paire codée en dur en fallback |

### 3.2 Résolveurs de liens upstream (`link_resolver.go`)

L'upstream orchestre **plusieurs** résolveurs, pas seulement Song.link :

1. **Spotify direct** (`isrc_finder.go`) — ISRC depuis `spclient.wg.spotify.com/metadata/4/track/{gid}`
2. **Song.link API** (`songlink.go`) — ondesli API
3. **Songstats** (`songstats.go`) — scrape `songstats.com/{ISRC}`, JSON-LD `sameAs`
4. **Scrape HTML Song.link** — lit l'ISRC directement dans `pageData.entityData.isrc` (pas via Deezer comme nous)

### 3.3 Ce qu'on a déjà porté

- ✅ `GetISRCDirect` — ISRC depuis l'API Spotify (équivalent de `isrc_finder.go`)
- ✅ Recherche Qobuz signée (`signedQobuzURL` dans `signed_search.go`)
- ✅ Proxy communauté (`qbz-oss.spotbye.qzz.io/api/dl`)
- ✅ Session communauté + Turnstile solver

### 3.4 Ce qui nous manque (gaps identifiés)

| Gap | Priorité | Impact |
|---|---|---|
| **Fallback recherche Qobuz par titre+artiste** | 🔴 Critique | Bloque tous les téléchargements Qobuz |
| **Scoring des résultats Qobuz** | 🟡 Important | Évite les mauvais matchs (covers, homonymes) |
| Songstats (source alternative) | 🟢 Nice-to-have | Source indépendante de plus |
| `qobuz_api.go` (scraping credentials) | 🟢 Confort | Durabilité — la paire codée en dur peut tourner |

### 3.5 Songstats comme résolveur cross-provider alternatif

Songstats (`songstats.com/{ISRC}`) expose du JSON-LD avec des `sameAs`
couvrant 12+ plateformes :

```json
// MusicRecording > sameAs
spotify ✅ | apple music ✅ | deezer ✅ | amazon ✅ | tidal ✅
beatport ✅ | soundcloud ✅ | youtube ✅
qobuz ❌ — ABSENT
```

**Avantages vs Song.link :**
- Pas de rate-limit API (scrape HTML, pas d'API key)
- Pas de 429 observé (contrairement à Song.link)
- Couvre beatport, soundcloud, youtube (que Song.link n'a pas toujours)
- Les liens sont directs (pas besoin de re-résoudre via Deezer pour l'ISRC)

**Inconvénients :**
- Pas de Qobuz (comme Song.link)
- Structure JSON-LD → nécessite un parser
- L'ISRC doit être connu (dépend de `GetISRCDirect` en amont)
- Certains ISRC ne sont pas indexés (ex: `GBLGL1297438` pour Dizzy)

**Verdict :** Bon candidat comme **fallback à Song.link** pour Tidal/Amazon/Deezer.
Ne résout pas le problème Qobuz.

## 4. Solutions pour Spotify/ISRC → Qobuz

### 4.1 Recherches web (2026-07-21)

Recherches : "ISRC to Qobuz", "Spotify ID to Qobuz", "Qobuz API search by ISRC"

### 4.2 Pistes explorées

| Approche | Statut | Détail |
|---|---|---|
| Song.link API | ❌ | Pas de Qobuz dans `linksByPlatform` |
| Song.link HTML | ❌ | Page erreur 400 |
| Songstats | ✅ | Utile pour Tidal/Amazon (JSON-LD `sameAs`), pas Qobuz |
| Musicfetch gratuit | ❌ | WAF Vercel + free tier = Spotify only |
| Musicfetch payant | ❌ | $50/mois |
| Qobuz ISRC search | ❌ | Index ISRC lacunaire (testé Mohawk, Bloomdido) |
| Qobuz text search | ✅ | Fonctionne mais nécessite filtrage artiste |
| Qobuz album/get (unsigned) | ✅ | Découverte #418 : pas de signature, juste `app_id` |
| MusicBrainz → Qobuz album | ✅ | Testé Daft Punk ✅, Billie Eilish ✅, Godspeed ❌ |

### 4.3 Résultats des tests

| Artiste | ISRC | MusicBrainz | album/get | Track ID | Qualité |
|---|---|---|---|---|---|
| Daft Punk | USQX91300108 | ✅ `apk748lfvcgxb` | ✅ 22 tracks | 209446467 | 24-bit |
| Billie Eilish | USUM71900764 | ✅ `wo456u01fehgc` | ✅ 14 tracks | 77469835 | 24-bit |
| Godspeed You! BE | — | ❌ Pas d'URL Qobuz | ❌ Artiste introuvable | ❌ | — |

## 5. Questions ouvertes

- [ ] **Amazon cooldown** : comment ça marche exactement côté `amz-oss.spotbye.qzz.io` ? Est-ce un cron job ou basé sur l'usage ?
- [ ] **Deezer** : les proxies renvoient du HTML — définitivement mort ou juste l'URL qui a changé ?
- [ ] **`tidal-uptime.geeked.wtf`** : le domaine est-il mort ou juste inaccessible depuis le conteneur Docker ?

## 6. Décisions

- [x] ~~Utiliser Song.link pour les liens Qobuz~~ — **ABANDONNÉ** : Song.link n'indexe pas Qobuz
- [x] ~~Utiliser le scrape HTML Song.link~~ — **ABANDONNÉ** : page erreur 400
- [x] ~~Utiliser Songstats pour Qobuz~~ — **ABANDONNÉ** : pas de Qobuz non plus
- [x] ~~Utiliser Musicfetch (gratuit) pour Qobuz~~ — **ABANDONNÉ** : free tier = Spotify only, payé = $50/mois
- [ ] Implémenter le pipeline MusicBrainz → Qobuz avec fallback texte
- [ ] Ajouter un scoring simple (match artiste → premier résultat)

## 7. Plan d'intégration : MusicBrainz → Qobuz

### 7.1 Architecture

```
Spotify ID
  → GetISRCDirect(spotifyID) → ISRC
  → Pipeline Qobuz (voir §7.2)
  → Proxy communauté /api/dl → FLAC
```

Le pipeline remplace l'actuel `searchByISRC(isrc)` dans `DownloadTrackWithISRC`.

### 7.2 Pipeline principal (3 étages)

```
┌──────────────────────────────────────────────────────────┐
│ Étage 1 : MusicBrainz ISRC → Recording → Releases       │
│ GET /ws/2/recording?query=isrc:{ISRC}&inc=releases      │
│                                                          │
│ Pour chaque release :                                    │
│   GET /ws/2/release/{id}?inc=url-rels                   │
│   Filtrer les relations contenant "qobuz.com/album/"    │
│   ou "open.qobuz.com/album/"                            │
│   → Extraire l'album ID (dernier segment après /)       │
│                                                          │
│ ⚠️ Rate-limit MusicBrainz : 1 req/sec max               │
│ ⚠️ Limiter à 20 releases parcourues (sinon trop lent)   │
│ ⚠️ Arrêter dès qu'un release avec Qobuz est trouvé      │
│                                                          │
│ Si trouvé → Étage 3                                      │
│ Si pas trouvé → Étage 2                                  │
└──────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────┐
│ Étage 2 : Qobuz album/search → artiste → discographie   │
│ GET album/search?query={album+titre+artiste} (signé)     │
│                                                          │
│ Si un album correspond (titre + artiste match) :         │
│   → Utiliser son ID pour l'Étage 3                       │
│                                                          │
│ Sinon, chercher l'artiste :                              │
│   GET artist/search?query={artiste} (signé)              │
│   → Récupérer artist_id                                  │
│   → GET artist/get?artist_id={id}&extra=albums           │
│   → Chercher l'album dans les résultats                  │
│                                                          │
│ Si trouvé → Étage 3                                      │
│ Si pas trouvé → Étage 3 avec fallback texte              │
└──────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────┐
│ Étage 3 : Qobuz album/get → tracklist → match ISRC      │
│ GET album/get?album_id={ID}&app_id=712109809 (UNSIGNED!) │
│                                                          │
│ ⚠️ Géo-gaté : ne fonctionne que depuis un pays          │
│    servi par Qobuz (FR, UK, US, DE, etc.)               │
│    → Si 404 : retourner une erreur explicite             │
│                                                          │
│ Parcourir la tracklist :                                 │
│   → Match exact par ISRC → Qobuz Track ID               │
│   → Si pas de match ISRC : match par track_number        │
│     + titre (fuzzy)                                      │
│                                                          │
│ Si match → ✅ Track ID                                   │
│ Si pas de match → ❌ Échec                               │
└──────────────────────────────────────────────────────────┘
```

### 7.3 Fallback texte direct (dernier recours)

```
Qobuz track/search?query={titre}+{artiste} (signé)
  → Filtrer par nom d'artiste (contains, case-insensitive)
  → Prendre le premier match
  → Si plusieurs : scorer par durée si disponible
```

Utilisé uniquement si les étages 1 et 2 n'ont pas trouvé d'album.

### 7.4 Gestion des cas limites

| Cas | Probabilité | Stratégie |
|---|---|---|
| MusicBrainz ne retourne aucun release | Faible | Passer à l'étage 2 |
| Aucun release n'a d'URL Qobuz | Moyenne (indés, petits labels) | Passer à l'étage 2 |
| album/search ne trouve pas l'album | Élevée pour artistes avec noms communs | Chercher l'artiste → discographie |
| artist/get ne retourne pas tous les albums | Élevée (limitation API Qobuz) | Fallback texte direct |
| album/get retourne 404 (géo-blocage) | Si serveur hors zone Qobuz | Erreur explicite : "Serveur dans un pays non servi par Qobuz" |
| ISRC absent de la tracklist | Très faible (testé 2/2) | Match par track_number + fuzzy title |
| Aucun résultat (piste absente de Qobuz) | Variable | Échec normal → le provider auto passe au suivant |

### 7.5 Performance

| Étape | Appels HTTP | Latence estimée |
|---|---|---|
| Spotify → ISRC | 1 (cache) | < 100ms |
| MusicBrainz ISRC → releases | 1 + N (N ≤ 20) | 1-10s |
| Qobuz album/get | 1 | < 500ms |
| **Total (succès MusicBrainz)** | **~3-6** | **~2-11s** |
| Fallback album/search + artist/get | +2-5 | +2-5s |
| Fallback texte | +1 | +1s |
| **Total (pire cas)** | **~8** | **~15s** |

### 7.6 Fichiers à modifier

| Fichier | Modification |
|---|---|
| `backend/qobuz/client.go` | Remplacer `searchByISRC` → nouveau `resolveQobuzTrackID` avec le pipeline à 3 étages |
| `backend/qobuz/client.go` | Modifier `DownloadTrackWithISRC` pour utiliser `resolveQobuzTrackID` |
| `backend/qobuz/signed_search.go` | Ajouter `SignedAlbumSearch`, `SignedArtistSearch` |
| `backend/qobuz/musicbrainz.go` (nouveau) | Fonctions d'interaction avec l'API MusicBrainz |

### 7.7 Ce qui ne change pas

- `GetISRCDirect` : inchangé, déjà fiable
- `GetDownloadURL` et le proxy communauté : inchangés, prennent un track ID
- Le téléchargement et les métadonnées : inchangés
- La signature MD5 pour `track/search` et `album/search` : déjà implémentée dans `signed_search.go`
- `album/get` n'a PAS besoin de signature (découverte #418) — juste `app_id=712109809`
