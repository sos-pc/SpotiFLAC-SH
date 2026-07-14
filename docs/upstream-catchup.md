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
| **S5** ✅ | Client Tidal | tidal.go, tidal_community.go | **lu en entier** — gap réel : pas de validation mimeType-vs-qualité (même famille que S2), fallback HI_RES→LOSSLESS déjà présent chez nous | 2 |
| **S6** ✅ | Client Qobuz | qobuz.go, qobuz_api.go, qobuz_community.go | **lu en entier** — hypothèse concrète et vérifiable sur le 401 (`searchByISRC` utilise encore l'ancien app_id non signé), + gap de scoring indépendant (voir §S6) | **2** |
| **S7** ✅ | Client Amazon | amazon.go | **lu en entier** — relie S4 : mp4ff_decrypt remplace notre déchiffrement à clé unique, potentiellement déjà cassé si notre proxy a évolué pareil (voir §S7) | 2 (lié à S4) |
| **S8** ✅ | Client Spotify authentifié | spotfetch, spotify_metadata, spotify_totp | **lu en entier** — 2 fixes candidats + 1 écart mineur, TOTP confirmé identique, retry 401/403 déjà couvert chez nous | 4 (portage) |
| **S9** ✅ | Résolution de liens + ISRC cross-provider | songlink.go, link_resolver.go, songstats.go, isrc_cache.go, isrc_finder.go, isrc_helper.go | **lu en entier** — notre vraie chaîne (`jobs_helpers.go`) a déjà 4 étages, plus robuste que prévu ; ISRC-direct reste le meilleur candidat de portage (voir §S9) | 3 (portage) |
| **S10** ✅ | Enrichissement métadonnées | musicbrainz.go, cover.go, lyrics.go, lyrics_reader.go | **lu en entier** — R10 a probablement 2 causes (MusicBrainz + résolution ISRC en amont via Song.link, lié à S9), pas une seule (voir §S10) | **2** |
| **S11** | Lecture/écriture de tags | metadata.go, tagging.go, upc_tags.go | Sujet retrouvé, lié à une nouvelle dépendance (voir §S11) | 4 |
| **S12** | Formatage noms/artistes | artist_format.go, filename.go | Pas encore lu en détail | 4 |
| **S13** | Utilitaires bas signal | config, progress, filemanager, history, analysis, ffmpeg, resample, recent_fetches | Balayage rapide seulement | 5 |
| **S14** | go.mod | dépendances upstream | Diff déjà vu, pas encore creusé | 5 |

**Recommandation de séquencement.** S2 et S3 sont des gains rapides déjà cernés. S6 et S10 restent
prioritaires (bug Qobuz 401 parké, R10 sur-sélection genre déférée). S9 relu en entier : notre chaîne
de fallback (`jobs_helpers.go`, 4 étages) est plus solide qu'estimé au premier passage — le seul ajout
vraiment justifié est l'ISRC-direct (indépendant, coût faible, s'appuie sur S8), pas une refonte
complète. S4 est hors séquence — question de fond, pas un item technique à prioriser normalement. S8 et
S9 sont lus ; leur portage réel (cache token Spotify, retry TOTP, étage ISRC-direct) reste à faire,
priorité modérée, aucun n'est urgent.

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

**Contexte ajouté après lecture de S7 :** ce n'est pas une feature indépendante — c'est le remplacement
du déchiffrement Amazon Music qu'on a déjà (`ffmpeg -decryption_key`, une seule clé). Upstream y est
passé parce que leur proxy communautaire renvoie maintenant plusieurs clés par fichier
(`key_specs []string`) que `ffmpeg -decryption_key` ne peut pas exprimer. Voir §S7 pour le détail —
ça ne change pas la nature de la décision à prendre, mais ça change l'enjeu : ce n'est pas hypothétique,
c'est potentiellement déjà en train de casser des téléchargements Amazon chez nous si notre proxy a
évolué pareil.

### S5 — Client Tidal ✅ lu

**Fichiers :** `tidal.go` (946 lignes de diff), `tidal_community.go` (déjà couvert en S1). Comparé à
`backend/tidal/client.go` (1030 lignes).

**Beaucoup du diff est du bruit de signature** (ajout de `metadataSeparator`/`isrcOverride`/
`spotifyComposer` partout, même motif que S6/S8 — pas urgent, déjà noté ailleurs).

**Une vraie trouvaille, dans le même esprit que S2 mais pour la qualité plutôt que la durée :**
`DownloadFromManifest` upstream vérifie maintenant le `mimeType` réellement livré contre la qualité
demandée (`isLosslessRequested && !isActualLossless → abort "Aborting download"`) — **avant** d'écrire
le fichier. On a le même parsing de `mimeType` chez nous (`backend/tidal/client.go:350,376`) mais on se
contente de logger `"Downloading non-FLAC file"` en debug et de continuer : **rien n'empêche
aujourd'hui de sauvegarder un flux qualité inférieure sous une extension/tag FLAC si Tidal en sert un
silencieusement.** Chez upstream, ce rejet déclenche en plus le passage à l'API Tidal miroir suivante
(`tryDownloadAcrossTidalAPIs` boucle sur plusieurs miroirs, réessaie tant qu'aucun ne renvoie la bonne
qualité) — donc pas juste un rejet, un vrai retry ciblé.

**Vérifié : le fallback HI_RES→LOSSLESS existe déjà chez nous** (`backend/tidal/client.go:550,673`,
`AllowFallback`) — pas un gap, upstream a la même logique.

**Recommandation :** porter la validation mimeType-vs-qualité-demandée dans `DownloadFromManifest`,
dans la continuité de S2 (même famille de bug : accepter silencieusement un flux qui ne correspond pas
à ce qui a été demandé).

### S6 — Client Qobuz

**Fichiers :** `qobuz.go` (+350/-125), `qobuz_api.go` (+343/-0, nouveau), `qobuz_community.go`.

**Triangé le 2026-07-15, diffs lus en entier (`qobuz.go` 701 lignes, `qobuz_api.go` 413 lignes),
comparés à `backend/qobuz/client.go`.**

**`qobuz_api.go`** scrape le bundle JS du web player officiel (`open.qobuz.com/track/1` → `main.js`)
pour en extraire `app_id`/`app_secret` par regex, signe chaque requête (MD5 de `path + params triés +
timestamp + secret`), cache le résultat 24h sur disque, revalide en sondant une recherche connue, et
se rafraîchit automatiquement sur 400/401. Un `app_id`/`app_secret` par défaut (`712109809` /
`589be88e...`) sert de repli si le scraping échoue — ce sont les identifiants publics du web player
Qobuz lui-même (embarqués dans leur propre site, pas un secret privé façon S1).

**Découverte clé en comparant à notre `searchByISRC` actuel** (`backend/qobuz/client.go:182`) :
**on utilise encore l'ancien `appID = "798273057"`, en requête non signée** — exactement la valeur
qu'upstream a abandonnée dans ce diff. Le flux complet chez nous est : `searchByISRC` (recherche
publique non authentifiée, ce vieil app_id) → si ça trouve un `track.ID` → `GetDownloadURL` → 
`musicdl.me` en premier. **Le bug "Qobuz 401" documenté comme venant de musicdl.me pourrait en fait
se produire une étape plus tôt, dans `searchByISRC` lui-même** — musicdl.me ne serait jamais atteint
si la recherche non signée échoue déjà. Pas confirmé par des logs (je n'ai pas accès à la prod), mais
c'est une hypothèse concrète et vérifiable, pas une supposition en l'air.

**Deuxième trouvaille indépendante du sujet 401 :** `qobuz.go` upstream a aussi un vrai algorithme de
scoring des résultats de recherche (`scoreQobuzSearchCandidate` — titre/artiste/album normalisés,
pénalités sur les mots-clés "karaoke/instrumental/cover/..."), et un fallback recherche-par-nom si la
recherche par ISRC échoue. **Notre `searchByISRC` prend juste `Items[0]` sans scoring et sans
fallback** — un vrai gap indépendant de la question 401, avec un risque de mauvais matchs silencieux.

**Autre point confirmé :** `DownloadTrack` upstream résout maintenant l'ISRC via
`linkClient.GetISRCDirect(spotifyID)` (le chemin direct-Spotify de S9) plutôt que via Song.link/Deezer
— renforce encore la priorité de S9. Ils embarquent aussi l'UPC album (récupéré via la même voie S9)
dans les tags — **on n'a aucun support UPC actuellement** (zéro occurrence dans `backend/meta/` ou
`backend/qobuz/`).

**`GetDownloadURL` lui-même n'utilise PAS l'API signée** — il essaie `q.customURL` (instance perso
configurée), puis leur proxy communautaire chiffré (`qobuz_community.go`, déjà écarté en S1). Donc
`qobuz_api.go` ne remplacerait que l'étape de recherche/matching chez nous, pas l'obtention de l'URL de
streaming elle-même — **à garder en tête pour ne pas sur-promettre** : ça peut réparer `searchByISRC`,
pas forcément tout le pipeline Qobuz.

**Recommandation : candidat de portage sérieux**, mais à valider d'abord en observant si l'échec réel
en prod est bien dans `searchByISRC` (pas juste dans `musicdl.me`) avant d'investir le temps de porter
`qobuz_api.go`.

### S7 — Client Amazon ✅ lu

**Fichier :** `amazon.go` (517 lignes de diff). Comparé à `backend/amazon/client.go`.

**Découverte qui relie S4 et S7 : `mp4ff_decrypt.go` n'est pas une feature à part, c'est le
remplacement du mécanisme de déchiffrement Amazon Music.** Ancien code (encore ce qu'on a
aujourd'hui, `backend/amazon/client.go:271-278`) : une **seule** clé de déchiffrement
(`apiResp.DecryptionKey`), passée directement à `ffmpeg -decryption_key <clé> -i ... -c copy`. Nouveau
code upstream : leur proxy communautaire renvoie maintenant `key_specs []string` (**plusieurs clés**,
probablement une par segment/piste protégée), qu'`ffmpeg -decryption_key` ne sait exprimer qu'à clé
unique — d'où le passage à `mp4ff` qui accepte une table de clés (`keysByKID map[string][]byte`).

**Implication concrète pour la décision S4 :** ce n'est pas juste "voulons-nous ajouter du
déchiffrement DRM", c'est potentiellement "notre déchiffrement Amazon actuel (clé unique) risque de ne
plus suffire si leur service de contenu a évolué vers plusieurs clés par fichier" — mais je n'ai aucun
moyen de vérifier si `amazon.spotbye.qzz.io` (notre proxy) a fait la même évolution que le proxy
upstream sans accès aux logs de prod. À vérifier concrètement (un téléchargement Amazon échoue-t-il
avec un message clé/déchiffrement ?) avant de trancher S4 uniquement sur des principes.

**Reste du diff** : essentiellement du bruit de signature (mêmes paramètres `metadataSeparator`/
`isrcOverride`/`spotifyComposer` que S5/S6/S8) et un renommage cosmétique
(`DownloadFromAfkarXYZ` → `downloadFromCommunity`, on a déjà fait cette transition nous-mêmes). Rien
d'autre à signaler.

### S8 — Client Spotify authentifié (spotfetch.go / spotify_metadata.go / spotify_totp.go) ✅ lu

**Triangé le 2026-07-15, diffs lus en entier et comparés ligne à ligne à `backend/spotify/`.**

**Correction supplémentaire par rapport au découpage précédent :** les fichiers `isrc_cache.go`/
`isrc_finder.go`/`isrc_helper.go` n'appartiennent pas à ce sujet — `isrc_helper.go::ResolveTrackISRC`
appelle `GetCachedISRC` puis `NewSongLinkClient().GetISRCDirect(...)`, et `isrc_finder.go` ajoute une
méthode à `SongLinkClient` (`lookupSpotifyISRC`). Ce sont des outils internes de `SongLinkClient`, pas
du client Spotify authentifié. **Déplacés vers S9**, voir cette section pour leur contenu réel.

**`spotify_totp.go` : rien à porter, confirmé.** Secret TOTP et version (`61`) **identiques
byte-pour-byte** aux nôtres (`backend/spotify/client.go:59-60`) — upstream a juste extrait le même code
dans un fichier séparé.

**`spotify_metadata.go` : rien de neuf d'utile.** Uniquement des types, deux champs ajoutés (`UPC` sur
l'album, `ArtistIds`) — mineur, pas d'ISRC exposé ici (confirmé par grep sur tout le diff).

**`spotfetch.go` (452 lignes de diff) — plusieurs trouvailles, à départager :**

| Changement | Verdict | Pourquoi |
|---|---|---|
| Cache du token d'accès au niveau package (partagé entre instances, expiry réelle ~55 min) | **Candidat au portage** | On instancie a priori un `SpotifyClient` par usage sans partager le token entre eux — moins de handshakes TOTP-signés, donc moins de surface pour se faire limiter. À vérifier : combien de fois `NewSpotifyClient()` est appelé par téléchargement/sync. |
| Retry TOTP sur 3 fenêtres d'horloge (`now`, `now-30s`, `now+30s`) | **Candidat au portage** | Protège contre le clock drift qui invaliderait silencieusement le TOTP. On n'a rien d'équivalent. Petit, sûr. |
| Retry auto sur 401/403 dans `Query()` (invalide le cache, réinitialise, retente 1×) | **Déjà couvert, on est même en avance** | Notre `Query()` (`backend/spotify/client.go:332-352`) gère déjà le 401 (reset + retry) **et** le 429 avec `Retry-After` + backoff exponentiel (10s/30s/60s) — chose qu'on n'a pas vue dans ce diff upstream. Rien à porter ici. |
| `(?s)` ajouté à la regex de `stripHTMLTags` | **Candidat au portage, trivial** | Sans ce flag, `.` ne matche pas les retours à la ligne dans une bio/description HTML multi-lignes — un vrai bug de troncature. Un caractère à changer si on a l'équivalent. |
| Paramètre `separator string` ajouté à tous les `Filter*` (remplace `", "` en dur) | **Écart réel, priorité basse** | Chez nous, `backend/spotify/client.go` a aussi `", "` en dur dans tous les `Filter*`. On a bien un séparateur configurable (`GetSeparator()` dans `backend/util/filename.go`) mais il n'atteint que le nommage de fichier, pas la construction du champ `Artists` par Spotify. Pas confirmé si ça a un impact utilisateur visible (le tag final passe peut-être par un autre re-join) — à vérifier avant de trancher, pas urgent. |

### S9 — Résolution de liens + ISRC cross-provider ✅ lu

**Fichiers :** `songlink.go` (840 lignes de diff), `link_resolver.go`, `songstats.go`, `isrc_cache.go`,
`isrc_finder.go`, `isrc_helper.go`. Triangé le 2026-07-15, diffs lus en entier.

**Correction importante après lecture du vrai chemin d'exécution.** Une première passe sur le seul
package `backend/songlink/` avait laissé penser qu'on n'a qu'une chaîne à 2 maillons sans fallback
automatique. En fait la vraie chaîne de production vit dans **`jobs_helpers.go::getStreamingURLs`**
(pas dans `songlink/` lui-même) et a **4 étages** :
1. Recherche Deezer par nom (pas de rate-limit) — `GetDeezerSearchFallback`
2. API officielle Song.link (rate-limitée, `songLinkSem`) — `GetAllURLsFromSpotify`
3. Scraping via iTunes + song.link `/i/{appleMusicID}` — `ScrapeSongLinkViaAppleMusic`
4. Scraping direct `song.link/s/{spotifyID}` (`__NEXT_DATA__`) — `ScrapeSongLinkHTML`

C'est plus robuste que ce que la doc `EXTERNAL_APIS.md` seule laisse penser. Cela dit, **upstream a
fait une refonte complète et différente**, pas juste ajouté un maillon :

| Différence avec notre chaîne à 4 étages | Ce qu'upstream fait | Verdict |
|---|---|---|
| Pas de résolution ISRC indépendante des URLs de streaming | `isrc_finder.go` : ISRC obtenu **en premier**, directement depuis le microservice interne Spotify (`spclient.wg.spotify.com/metadata/4/...`) via le même token TOTP anonyme qu'on a déjà (voir S8) — avant même d'essayer Song.link/Deezer/Songstats | **Bon candidat.** Indépendant de tout ce qu'on a déjà ; coût faible (TOTP déjà identique, juste l'appel HTTP + conversion d'ID base62→hex à ajouter). Cache BoltDB inclus (`isrc_cache.go`). |
| Pas de Songstats du tout | `songstats.go` : source alternative (scrape `songstats.com/{ISRC}`, JSON-LD `sameAs`) à Song.link, activable/priorisable via réglage utilisateur (`orderedLinkResolvers`) | **Candidat, priorité moyenne.** Vraie source indépendante de plus, mais nécessite d'abord l'ISRC (dépend du point précédent) et représente plus de code neuf à maintenir qu'un simple appel. |
| Notre `ScrapeSongLinkHTML` récupère l'ISRC seulement via Deezer en second temps | Leur scraper lit l'ISRC **directement** dans `pageData.entityData.isrc` du blob `__NEXT_DATA__` de song.link, sans repasser par Deezer | **Petit gain si le champ existe vraiment** — à vérifier sur une vraie page song.link avant de coder (le site a pu changer sa structure entre nos deux implémentations). |
| Notre `checkQobuzAvailability` renvoie un simple booléen, recherche non-authentifiée (`app_id` en dur) | La leur renvoie **l'URL Qobuz réelle**, via une recherche authentifiée avec les identifiants scrapés dans `qobuz_api.go` (S6) | **Lié à S6** — pas la peine de le faire avant d'avoir tranché qobuz_api.go. |
| Normalisation d'URL Amazon simple | Regex dédiées pour extraire l'ASIN (`trackAsin=`, `/albums/.../B...`, `/tracks/B...`) vers une forme canonique `music.amazon.com/tracks/{ASIN}?musicTerritory=US` | À vérifier si nos liens Amazon actuels posent un problème concret avant de le porter — pas de symptôme connu. |

**Recommandation de séquencement pour le codage :** commencer par le point ISRC-direct (indépendant,
autonome, plus proche de S8 déjà fait) en l'ajoutant comme **étage 0** (avant Deezer) de la chaîne dans
`jobs_helpers.go::getStreamingURLs` — pas à la place des étages existants. Songstats et la normalisation
Amazon peuvent attendre.

### S10 — Enrichissement métadonnées ✅ lu

**Fichiers :** `musicbrainz.go`, `cover.go`, `lyrics.go`, `lyrics_reader.go` (nouveau). Comparé à
`backend/meta/musicbrainz.go` et à la vraie boucle `retagIncompleteMetadata`/`retagOneTrack`
(`api_admin.go:648+`) et `providerutil/genremeta.go`, pas juste au fichier musicbrainz isolé.

**R10, nuancé après avoir tracé le vrai chemin d'appel — deux causes possibles, pas une seule.**

Upstream a ajouté à `musicbrainz.go` : throttle à 1100ms entre requêtes (le rate-limit MusicBrainz
documenté est ~1 req/s), backoff dédié sur 503, dédup des appels concurrents pour la même ISRC, cache
mémoire, et retourne maintenant une vraie erreur si aucun genre n'est trouvé (au lieu d'un succès
silencieux vide).

**Première hypothèse (MusicBrainz lui-même) : partiellement déjà couverte.** `retagIncompleteMetadata`
a bien un throttle **entre pistes** (`retagIncompleteMetadataThrottle = 1 * time.Second`,
`api_admin.go:600`) — proche du rythme upstream, pas absent comme je le pensais avant de vérifier. Mais
`backend/meta/musicbrainz.go` lui-même n'a aucun throttle propre, aucun cache, et aucune dédup —
si un téléchargement normal tourne en parallèle du batch de retag, les deux tapent MusicBrainz sans se
coordonner, dépassant le rythme malgré le délai du batch.

**Deuxième hypothèse (plus probable en tracant `retagOneTrack`) : l'ISRC ne se résout même pas jusqu'à
MusicBrainz.** `retagOneTrack` → `providerutil.FetchGenreMetadataAsync` → `resolveISRCFromSpotifyURL`
→ **`songlink.GetISRC`** (le même chemin fragile identifié en S9, Song.link documenté "heavily
rate-limited"). Si cette résolution échoue, `resolvedISRC == ""` et **la fonction retourne un résultat
vide silencieusement — musicbrainz.go n'est jamais appelé, aucune erreur distincte ne remonte.** Vu
d'en haut, ça ressemble exactement à "sur-sélectionne et skip 99%" : plein de pistes "traitées" sans
genre, sans qu'on puisse distinguer "MusicBrainz n'a pas de genre pour ce morceau" de "on n'a même pas
pu obtenir l'ISRC".

**Recommandation : ne pas porter musicbrainz.go seul en pariant que c'est LA cause.** Deux pistes à
considérer ensemble : (a) les améliorations MusicBrainz elles-mêmes (cache/dédup/erreur explicite sur
genre vide — utiles indépendamment), et (b) brancher `resolveISRCFromSpotifyURL` sur le chemin
ISRC-direct de S9 plutôt que sur Song.link seul, ce qui pourrait avoir plus d'impact sur le taux de
succès réel que le throttling MusicBrainz. Idéalement instrumenter `retagOneTrack` pour distinguer les
deux causes d'échec avant de choisir où investir.

**`cover.go` / `lyrics.go` / `lyrics_reader.go` : signal plus faible.** `cover.go` couvre plus que les
pochettes de morceau (avatars/headers/galerie) mais surtout du bruit de signature + du code
macOS-spécifique (`ApplyMacOSFLACFileIcon`, non applicable). `lyrics.go` a perdu
`FetchLyricsFromSpotifyAPI` (source de lyrics Spotify retirée côté upstream, à vérifier si on l'a et
pourquoi eux l'ont enlevée avant de s'en inquiéter). `lyrics_reader.go` (nouveau) lit les lyrics déjà
embarquées dans un fichier — utile en théorie, mais la moitié de ses fonctions publiques
(`SelectLyricsFiles`/`SelectLyricsFolder`) sont des dialogues Wails desktop, pas applicables ici.

### S11 — Lecture/écriture de tags ✅ lu

**Fichiers :** `metadata.go` (572 lignes de diff), `tagging.go` (nouveau, 527 lignes), `upc_tags.go`
(nouveau, 80 lignes, petit et autonome).

**Confirmation : c'est une vraie migration d'architecture chez eux, pas un ajout.**
`embedMetadataToMP3`/`embedMetadataToM4A` (code par-format fait main : `id3v2` pour MP3, écriture
d'atomes custom pour M4A) ont été **supprimés** de `metadata.go`, remplacés par `tagging.go::TagFile`
qui passe tout par `go.senan.xyz/taglib` — une seule lib pour tous les formats au lieu de 3+.

**Point d'attention avant même d'évaluer l'intérêt : `go.senan.xyz/taglib` est un binding CGO vers la
lib C++ TagLib** (à vérifier avec certitude, mais c'est la nature connue de ce module) — **potentiellement
incompatible avec notre build `CGO_ENABLED=0`** (Dockerfile scratch, mentionné explicitement dans
`.kiro/HANDOFF.md` comme contrainte). Si confirmé, ce n'est pas une question de "combien d'effort",
c'est structurellement bloqué sans changer notre stratégie de build — **à vérifier en premier**, avant
de considérer quoi que ce soit d'autre sur ce sujet.

**`upc_tags.go`** : petit helper autonome de normalisation des clés de tag UPC (`UPC`, `BARCODE`,
`TXXX:UPC`, atomes iTunes `----:com.apple.itunes:upc`, etc.) — indépendant de la question CGO, portable
facilement si on décide un jour de supporter l'UPC (lié à la découverte UPC de S6/S9).

**Recommandation : ne pas prioriser** — gros effort (changement de lib de tagging entière), gain
incertain, et un vrai risque de blocage technique (CGO) à lever avant de considérer la question plus
sérieusement. `upc_tags.go` seul est portable à part si besoin.

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
