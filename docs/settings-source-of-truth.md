# Réglages — pas de source unique de vérité

> **🔍 Constat — 2026-07-17.** Relevé par lecture du code + observation en prod (session methammer,
> in-app browser) pendant la vérif de la refonte de sélection de service. **Problème d'architecture
> réel** signalé par l'utilisateur : « les settings ne sont pas appliqués partout, il n'y a pas une
> seule vérité ». Ce document décrit *pourquoi*, avec preuves, et les décisions à prendre. Rien n'est
> corrigé ici. Index : [README.md](README.md). Voisins :
> [service-selection-map.md](service-selection-map.md), [override-rework-plan.md](override-rework-plan.md).

## 0. Le constat en une phrase

Un même réglage existe dans **trois stockages** côté client/serveur, et atteint le backend par **deux
mécanismes différents selon le chemin de téléchargement**. Résultat : deux téléchargements peuvent
utiliser des réglages différents, et ce que l'UI affiche n'est pas toujours ce qui tourne.

## 1. Trois stockages de réglages

| # | Stockage | Écrit par | Lu par |
|---|----------|-----------|--------|
| A | **Serveur** (BoltDB, par utilisateur) | `PUT /api/v1/settings` (bouton *Save Changes*) | `ApplySettingsFallbacks` (backend, à chaque download) **et** `loadSettings()` (frontend au boot) |
| B | **localStorage** (`spotiflac-settings`) | `saveSettings()` **uniquement** | `getSettingsFromLocalStorage()` |
| C | **Cache mémoire** (`cachedSettings`) | `loadSettings()` (depuis A) ou fallback depuis B | `getSettings()` |

`getSettings()` = `cachedSettings ?? localStorage` ([settings.ts](../frontend/src/lib/settings.ts)).

> **⚠️ Correction (2026-07-18).** Ce paragraphe affirmait que « `loadSettings` recopie le serveur »
> dans B. **C'était faux**, et c'était précisément la cause racine : `localStorage.setItem(SETTINGS_KEY,…)`
> n'existait qu'à **un seul endroit**, dans `saveSettings()`. Erreur de déduction — la structure du code
> le suggérait, la vérification (`grep SETTINGS_KEY`) n'avait pas été faite avant de l'écrire.

**Divergence observée en prod (2026-07-17)** : à un instant, `localStorage.downloader = "auto"` alors
que l'UI et le serveur montraient `"qobuz"`. **Mécanisme réel** : B n'étant jamais rafraîchi depuis A,
un changement fait depuis un autre navigateur, un autre appareil ou une migration serveur laissait la
copie locale périmée **indéfiniment** — et `getSettings()` la servait à chaque chargement de page,
jusqu'à ce que l'utilisateur appuie sur *Save*. Ce n'était donc pas une course au boot, mais une
péremption permanente.

> **✅ Corrigé le 2026-07-18 (`b8a9f0d`, étape 6)** — voir §7.5 étape 6.

## 2. Deux mécanismes de livraison au backend — la conséquence directe

| Chemin | Comment les réglages arrivent au backend |
|--------|-------------------------------------------|
| **Batch** (`POST /jobs`) | Le frontend **envoie** tous les réglages dans `jobSettings` (voir `useDownload.ts enqueueTracksBatch`). Le backend les prend tels quels. |
| **Mono-piste** (`POST /downloads/track`) | Le frontend envoie un **sous-ensemble** ; le backend **remplit le reste depuis les réglages SERVEUR** via [`ApplySettingsFallbacks`](../download_service.go) : `OutputDir`, `FilenameFormat`, `AudioFormat`, `AutoOrder`, `EmbedLyrics`, `EmbedMaxQualityCover`, `AllowFallback`, `UseFirstArtistOnly`, `UseSingleGenre`, `EmbedGenre`, `TrackNumber`. |

**Donc** : pour un même réglage (ex. `autoOrder`), le batch utilise la valeur **frontend** (cache/
localStorage) et le mono-piste utilise la valeur **serveur**. Si les deux divergent (§1 — ex. un
changement d'UI non sauvegardé), **les deux chemins de téléchargement se comportent différemment**.
C'est littéralement « les settings pas appliqués partout ».

> **Vérifié le 2026-07-17** : `Source=auto`, chaîne `deezer-qobuz-amazon-tidal` sauvegardée. Un
> download mono-piste envoie `service:"auto"` **sans** `auto_order` → le backend le remplit depuis le
> serveur → la chaîne **est** honorée (Teardrop est revenu en 16-bit, cohérent avec Deezer en tête).
> Le mécanisme *marche* quand serveur = UI ; il **casse** dès qu'ils divergent.

## 3. Autres problèmes voisins (constatés)

- **Défauts `autoOrder` divergents** (déjà en [service-selection-map §4](service-selection-map.md)) :
  **cinq sites, trois valeurs différentes** — `DEFAULT_SETTINGS` = `tidal-qobuz-amazon-deezer`, les deux
  migrations « champ absent » = `tidal-qobuz-amazon` (**sans Deezer**), le placeholder UI = pareil, et
  l'exécution backend = `tidal-amazon-qobuz` (**ordre inverse**). Celui qui *tourne* est le backend :
  un utilisateur qui n'a rien réglé obtenait une chaîne affichée nulle part — Deezer jamais tenté,
  Amazon et Qobuz dans l'ordre inverse de l'écran de réglages.
  **✅ Corrigé le 2026-07-18 (`b8a9f0d`)** : un seul constant, `DEFAULT_AUTO_ORDER` (TS) /
  `defaultAutoOrder` (Go) = `tidal-qobuz-amazon-deezer`.
- **Champ `format` de l'historique trompeur** : un fichier 16-bit venu de Deezer s'affiche
  `format: "HI_RES_LOSSLESS"` — c'est l'`audio_format` **demandé** (qualité Tidal) stocké tel quel,
  pas le service ni la qualité réelle du fichier.
- **Statut mono-piste marqué « done » dès l'enqueue** (malhonnêteté préexistante, suivie en
  [override-rework-plan §8 phase 2b](override-rework-plan.md)).

## 4. La décision à prendre : un seul modèle

Il faut choisir **qui détient la vérité** et l'appliquer aux deux chemins :

1. **Frontend autoritaire** : le frontend envoie **tous** les réglages effectifs, le backend ne
   remplit **rien** depuis le serveur (ou seulement en dernier recours si un champ est absent *et*
   qu'aucun réglage utilisateur n'est joignable). C'est le modèle du **batch** aujourd'hui.
   - *Pour* : ce que l'UI montre est exactement ce qui tourne. Un seul endroit décide.
   - *Contre* : le frontend doit tout envoyer (surface plus large) ; un client tiers de l'API doit
     fournir tous les champs.
2. **Backend autoritaire** : le frontend n'envoie que l'essentiel (URL/piste + éventuels overrides
   ponctuels), le backend applique **toujours** les réglages serveur de l'utilisateur. C'est le modèle
   du **mono-piste** aujourd'hui.
   - *Pour* : les réglages vivent à un seul endroit (serveur) ; l'API est simple à appeler.
   - *Contre* : un changement d'UI n'a d'effet qu'après *Save* ; le frontend doit refléter fidèlement
     l'état serveur (le cache/localStorage ne doivent jamais faire autorité).

**Le point commun des deux** : supprimer la divergence en **ne laissant qu'une source décider** par
chemin, et rendre les deux chemins cohérents. Le patch d'un seul champ (le `auto_order` de `7d8eae4`,
reverté) va dans le sens inverse : il crée une source **mixte**, pire que l'un ou l'autre modèle.

## 5. Recommandation

**Backend autoritaire (option 2)** semble le plus proche de l'existant et le plus robuste : les
réglages sont déjà persistés par utilisateur côté serveur, `ApplySettingsFallbacks` existe déjà, et ça
donne une seule source (le serveur). Il resterait à :
- faire passer le **batch** aussi par un remplissage serveur (au lieu d'envoyer `jobSettings`), ou au
  moins garantir que ce que le batch envoie = les réglages serveur ;
- garantir côté frontend que `getSettings()` reflète **toujours** le serveur (jamais un localStorage
  périmé) — corriger la priorité cache/localStorage et le timing du boot ;
- aligner les défauts `autoOrder` (§3).

## 5bis. DÉCISION (2026-07-17) — backend autoritaire, un seul override

**Tranché : modèle backend autoritaire.** Le **serveur** est la seule vérité des réglages de
téléchargement. Un download n'accepte **qu'un seul override ponctuel : `service`** (le choix de source
du download en cours) ; tout le reste vient du serveur. Le frontend édite les réglages et les
**sauvegarde** sur le serveur, les **affiche**, mais ne les **envoie plus** pour piloter un
téléchargement.

Consigne utilisateur : **cartographier tout ce que ça touche avant de coder** — « je veux pas avoir
deux systèmes qui se disputent ». C'est le §7.

## 6. Note de méthode (l'erreur qui a mené ici)

Ce constat est né d'un **diagnostic raté** : j'avais conclu à un « bug de chaîne » depuis un log à 401
sans vérifier le réglage réel (`Source` était sur `Qobuz`, pas `auto` — le 401 était donc correct) ni
capturer le payload envoyé. J'ai poussé un correctif (`7d8eae4`) sur cette base fausse, puis reverté
(`76e96a7`) après avoir capturé la vérité terrain dans le navigateur. La leçon est celle de tout le
projet : **capturer ce qui se passe réellement avant de conclure** — ici, lire le réglage effectif et
le corps de la requête, pas interpréter un log ambigu.

## 7. Carte de migration vers backend-autoritaire (tout ce que ça touche)

Relevé exhaustif par lecture du code (2026-07-17). Le but : aucun endroit oublié où frontend et
backend calculeraient/décideraient la même chose.

### 7.1 Les 30 réglages, classés

| Catégorie | Réglages | Sort sous backend-autoritaire |
|-----------|----------|-------------------------------|
| **Téléchargement** (→ serveur seul) | `downloadPath`, `downloader`, `folderPreset`, `folderTemplate`, `filenamePreset`, `filenameTemplate`, `filenameFormat`, `artistSubfolder`, `albumSubfolder`, `trackNumber`, `embedLyrics`, `embedMaxQualityCover`, `tidalQuality`, `qobuzQuality`, `amazonQuality`, `autoOrder`, `autoQuality`, `allowFallback`, `createPlaylistFolder`, `createM3u8File`, `jellyfinMusicPath`, `useFirstArtistOnly`, `useSingleGenre`, `embedGenre`, `operatingSystem` | le backend les lit ; le frontend **ne les envoie plus** (sauf `service` en override) |
| **UI pure** (→ restent frontend) | `theme`, `themeMode`, `fontFamily`, `sfxEnabled` | inchangés, jamais envoyés au backend |
| **À vérifier** | `spotFetchAPIUrl` (endpoint provider) | probablement config serveur, pas UI — à trancher |

### 7.2 Les consommateurs frontend des réglages de téléchargement

| Fichier | Rôle | Réglages lus | Ce qui change |
|---------|------|--------------|---------------|
| [`hooks/useDownload.ts`](../frontend/src/hooks/useDownload.ts) | enqueue **mono-piste + batch** | ~15 champs | **arrête d'envoyer** les réglages ; n'envoie que l'identité piste + `service` |
| [`components/WatchlistPage.tsx`](../frontend/src/components/WatchlistPage.tsx) | enqueue **watchlist** (3ᵉ chemin !) | ~17 champs (dont qualités) | idem — arrête d'envoyer |
| [`hooks/useCover.ts`](../frontend/src/hooks/useCover.ts), [`hooks/useLyrics.ts`](../frontend/src/hooks/useLyrics.ts) | embed cover/paroles **après** download | `folderTemplate`, `filenameTemplate`, `trackNumber` | **recalculent le chemin du fichier** côté client → doivent utiliser le chemin **retourné par le backend**, pas le recalculer |
| [`components/FileManagerPage.tsx`](../frontend/src/components/FileManagerPage.tsx), [`components/ArtistInfo.tsx`](../frontend/src/components/ArtistInfo.tsx) | parcourir/localiser des fichiers | `downloadPath` | lecture seule d'affichage ; doit refléter le serveur |
| [`App.tsx`](../frontend/src/App.tsx) | thème/police + `downloadPath` | UI + `downloadPath` | UI pure reste ; `downloadPath` en lecture |

### 7.3 Les points de duplication « deux systèmes » — À RÉSOUDRE

C'est le cœur de ta crainte. Chaque ligne = un calcul fait **des deux côtés** aujourd'hui.

| # | Duplication | Frontend | Backend | Résolution cible |
|---|-------------|----------|---------|------------------|
| D1 | **Chemin de sortie + nom de fichier** | `resolveOutputPath` ([utils.ts:70](../frontend/src/lib/utils.ts)) — lit `downloadPath`, `folderTemplate`, `createPlaylistFolder`, `operatingSystem`, `useFirstArtistOnly` | `buildOutputDir` ([jobs_helpers.go:181](../jobs_helpers.go)) + `BuildExpectedFilename` ([util/filename.go:28](../backend/util/filename.go)) | **backend seul** calcule ; le frontend ne calcule plus pour les downloads |
| D2 | **Check d'existence** (« déjà téléchargé ») | `CheckFilesExistence` avec chemin calculé client | worker `checkFileExists` (path calculé serveur) | l'endpoint `/files/exists` calcule le chemin **serveur** à partir des réglages serveur |
| D3 | **Trois chemins d'enqueue** | mono-piste + batch (`useDownload`) + watchlist (`WatchlistPage`) — chacun assemble les réglages | worker unique | les trois passent par le serveur (réglages non envoyés) |
| D4 | **Embed cover/paroles** | `useCover`/`useLyrics` recalculent le chemin pour trouver le fichier | le download connaît le vrai chemin | le frontend utilise le chemin **retourné** (historique/job), ne recalcule pas |
| D5 | **M3U8** | `createM3U8` ([useDownload](../frontend/src/hooks/useDownload.ts)) — `jellyfinMusicPath`, `downloadPath` | watcher backend génère aussi des M3U8 | décider **un seul** générateur (probablement backend) |

### 7.4 Ce que le backend a déjà (faisabilité)

`buildOutputDir`, `BuildExpectedFilename`, `SanitizeFolderPath`, `ApplySettingsFallbacks`,
`EffectiveDownloadSettings` existent déjà. La brique manquante côté mono-piste : appliquer les
**templates de dossier** (le mono-piste via `/downloads/track` ne fait aujourd'hui que
`OutputDir = settings.DownloadPath`, sans template — c'est le frontend qui applique le template). Il
faut router le mono-piste par la même logique `buildOutputDir` que les jobs.

### 7.5 Plan phasé (révisé, ancré dans la carte)

1. **Backend calcule le chemin/nom pour le mono-piste** (D1) — router `/downloads/track` par
   `buildOutputDir`/`BuildExpectedFilename` avec les réglages serveur. Backend seul, testable seul.
   — ✅ **codé + VÉRIFIÉ EN PROD le 2026-07-17 (`f06e8b1`).** `DownloadSettings` gagne
   `folderTemplate`/`createPlaylistFolder` ; le handler ignore l'`output_dir` client (base = serveur,
   confinée) ; le job mono-piste porte les réglages serveur de chemin/nom. Qualité/embed encore issus
   de la requête (étape ultérieure). **Preuve prod** : un `/downloads/track` avec `output_dir` **bidon**
   (`ZZZ_BOGUS_CLIENT_PATH`) → fichier écrit à `/…/Music/Thylacine/Transsiberian/Swiss Sounds - Thylacine.flac`
   (template serveur `{artist}/{album}` + `filenameTemplate` serveur), la valeur client **ignorée**.
2. **`/files/exists` calcule le chemin serveur** (D2) — ne plus accepter un chemin calculé client.
   — ✅ **codé + VÉRIFIÉ EN PROD le 2026-07-18 (`4d03d7c`).** `outputSubfolder` extrait de
   `buildOutputDir` (une seule implémentation partagée) ; le handler ignore l'`output_dir` client et
   dérive le sous-dossier par piste du template serveur. **Preuve prod** : `/files/exists` avec
   `output_dir` bidon sur Roygbiv → `exists:true` à `/…/Music/Boards of Canada/Music Has The Right To Children/…`
   (valeur client ignorée) ; piste inexistante → `false`. Limite connue (reportée étape 3) : pas de
   contexte playlist par piste, donc `createPlaylistFolder=true` non reflété dans le pré-check (le
   check backend au download le rattrape).
3. **Backend-autoritaire pour les enqueues, puis frontend arrête d'envoyer** (D3).
   - **3-backend — ✅ codé + VÉRIFIÉ EN PROD le 2026-07-18 (`cc16d97`).** `serverJobSettings` construit
     des `JobSettings` **entièrement serveur** (sauf `service`). `DownloadTrack` (mono) et le handler
     `/jobs` (batch **non-watchlist**) l'utilisent → ce que le client envoie est **ignoré**. Les
     watchlists gardent leur modèle propre (`getWatchlistSettings`, enqueue par le watcher). `AutoQuality`
     ajouté à la vue serveur. **Preuve prod** : `/downloads/track service=auto` avec `audio_format`
     bidon → log `audio_format=HI_RES_LOSSLESS` (qualité serveur), chaîne `deezer-qobuz-amazon-tidal`
     (autoOrder serveur, aucun envoyé), chemin `{artist}/{album}` serveur. Valeurs client ignorées.
   - **Watchlists — ✅ TRANCHÉ : suivent le global (`de524a4`), CI verte.** L'utilisateur a acté que la
     copie par-watchlist (`WatchedPlaylist.Settings`) était **cachée et jamais exposée dans l'UI** — donc
     fonctionnalité morte. Les watchlists suivent désormais les réglages **globaux** du propriétaire
     partout : `getWatchlistSettings` (override worker), enqueue/requeue du watcher, racine M3U8/scan
     (`watchlistOutputRoot`), et repair/rebuild admin. `WatchedPlaylist.Settings` devient vestigial
     (écrit, jamais lu). **VÉRIFIÉ EN PROD le 2026-07-18** : une sync de la watchlist `/all` (dont les
     réglages stockés ont `service:""`, qui retombait sur **tidal** avant) produit
     `[Download] Resolving requested_service=auto audio_format=HI_RES_LOSSLESS` +
     `order=deezer-qobuz-amazon-tidal` — soit le `downloader`, la qualité et la chaîne **globaux**.
   - **3-frontend — ✅ codé le 2026-07-18 (`e52f499`), CI verte (tsc + lint).** Le client n'envoie plus
     **aucun** réglage : identité de la piste + contexte (`position`, `playlist_name`) + le seul override
     `service`. `downloadFallback` perd `resolveOutputPath` et 9 champs ; le batch envoie `{ service }`
     au lieu d'un blob de 15 champs ; `buildExistenceCheckRequests` perd son argument `Settings` ; les
     appels `CheckFilesExistence` passent `""` pour les dossiers. Artistes envoyés **bruts** (le serveur
     applique `useFirstArtistOnly`). `playlist_name` ajouté au type (contexte, pas réglage) → lève la
     limite playlist de l'étape 2.
     **Trou de l'étape 2 fermé au passage** : `/files/exists` dérivait le *dossier* côté serveur mais
     prenait encore le *nom de fichier* du client (`filename_format`, `include_track_number`,
     `use_album_track_number`) et un artiste déjà rogné — à moitié autoritaire, et cassé dès que le
     client cesserait de les envoyer. Le handler les dérive maintenant tous du serveur, avec le même
     trimming premier-artiste que `buildDownloadRequest`.
     **✅ VÉRIFIÉ EN PROD le 2026-07-18** (in-app browser, session methammer, bundle
     `index-DVtMfTAH.js`, `fetch` instrumenté). Téléchargement de *Matrix — Dizzy Gillespie* :
     - `POST /downloads/track` porte **uniquement** `service:"auto"` + identité + `position`.
       **Absents** : `output_dir`, `filename_format`, `audio_format`, `folder_name`, `track_number`,
       `use_album_track_number`, `embed_lyrics`, `embed_max_quality_cover`, `use_first_artist_only`,
       `use_single_genre`, `embed_genre`, `api_url`.
     - `POST /files/exists` porte `output_dir:""`, `root_dir:""`, sans `filename_format` /
       `include_track_number` / `relative_path`.
     - **Discriminant** : le serveur log `audio_format=HI_RES_LOSSLESS` et
       `order=deezer-qobuz-amazon-tidal` alors que **le client n'a envoyé ni l'un ni l'autre** →
       les deux viennent des réglages serveur. Preuve directe du modèle autoritaire.
     - Chaîne complète observée : `deezer` (HTML au lieu de JSON) → `qobuz` (401, S6) →
       `amazon` (DNS mort) → **`tidal` Success** → `[Jobs] Done`.
     - `album_artist:"Dizzy Gillespie, Astrud Gilberto"` part **brut**, les deux artistes : confirme
       que `useFirstArtistOnly` n'est pas hardcodé (réglage réel, cf. §7.6).
4. **Cover/paroles placées par le serveur** (D4) — **✅ codé le 2026-07-18 (`5f70f65`)**.
   Les routes `/media/cover` et `/media/lyrics` prenaient `output_dir`, `filename_format` et les
   drapeaux de numérotation **du client**, qui les calculait avec `resolveOutputPath` — une **seconde
   implémentation** de la règle de placement, qui **ne correspondait pas** à `outputSubfolder` :
   elle ajoutait un dossier playlist dès que la vue n'était pas un album, **même quand le template
   contenait déjà `{album}`**, ce que le serveur ne fait jamais. Avec `createPlaylistFolder` activé,
   la pochette atterrissait **un dossier à côté** du morceau.
   - Les deux routes passent par `mediaPlacement()`, qui appelle **le même `outputSubfolder`** que
     `buildOutputDir`. Partager l'implémentation *est* le correctif : un sidecar calculé par une règle
     « presque identique » est un sidecar au mauvais endroit.
   - **Incohérence de point d'appel corrigée aussi** : dans la vue « track » isolée, le morceau partait
     avec `playlistName=undefined` ([TrackInfo.tsx:99](../frontend/src/components/TrackInfo.tsx)) et la
     pochette avec `track.album_name` ([App.tsx](../frontend/src/App.tsx)). Forwarder la valeur du
     client telle quelle aurait **préservé** l'écart au lieu de le fermer.
   - **Numérotation** : le client envoie l'index de liste **et** le numéro de piste d'album en deux
     champs bruts, le serveur choisit — même pattern que `/files/exists`
     ([file_service.go:254](../file_service.go)). À noter : `meta.LyricsDownloadRequest.UseAlbumTrackNumber`
     est **déclaré mais jamais lu** — le choix a toujours été fait avant cette couche.
   - `resolveOutputPath` et ses deux types sont **supprimés** (plus aucun appelant).
   - **✅ VÉRIFIÉ EN PROD le 2026-07-18** (bundle `index-MX5xzzT-.js`), pochette de *Matrix — Dizzy
     Gillespie* depuis la vue « track » isolée :
     - requête = `cover_url`, `track_name`, `artist_name`, `album_name`, `album_artist`,
       `release_date`, `position`, `disc_number`. **Absents** : `output_dir`, `filename_format`,
       `track_number` — et **`playlist_name` absent**, ce qui est précisément la correction (il valait
       `track.album_name` avant).
     - chemin renvoyé par le serveur :
       `/home/nonroot/Music/Dizzy Gillespie/Best of Perception .../Matrix - Dizzy Gillespie.jpg`
       → `{artist}/{album}` + `{title} - {artist}`, sans dossier playlist parasite.
     - **preuve du placement** : listing du dossier =
       `Matrix - Dizzy Gillespie.flac` + `Matrix - Dizzy Gillespie.jpg`. La pochette est bien **dans le
       même dossier que le morceau**, même nom de base.
     - Les paroles ont échoué (`lyrics not found in any source`) : c'est un manque côté fournisseur
       tiers pour ce titre instrumental, pas un défaut du chemin — la requête, elle, était conforme.
5. **Un seul emplacement M3U8** (D5) — **✅ codé le 2026-07-18**.
   Il n'y avait pas deux *générateurs* mais **un seul writer** (`SystemService.CreateM3U8File`) piloté
   par **deux orchestrateurs divergents** :

   | | Watchlists | Téléchargements manuels (avant) |
   |---|---|---|
   | Réglages | serveur | **client** (`createM3u8File`, `jellyfinMusicPath`, `downloadPath`) |
   | Emplacement | `<racine>/Playlists/<nom> [<id>].m3u8` | `<dossier de sortie>/<nom>.m3u8` |
   | Protections | anti-rétrécissement + désambiguïsation | aucune |

   **Décision de l'utilisateur (2026-07-18) : aligner le manuel sur les watchlists.**
   - Nouvelle brique commune `writeM3U8ToPlaylistsDir()` — **un seul endroit décide où va un M3U8**.
   - Le garde anti-rétrécissement devient un paramètre explicite, parce que la bonne réponse diffère :
     une watchlist ne l'active que si des pistes n'ont pas pu être résolues (une liste plus courte
     entièrement résolue = la playlist a vraiment rétréci) ; un lot manuel l'active **toujours**, étant
     par nature un sous-ensemble qui ne doit jamais écraser le fichier complet.
   - L'endpoint `/files/m3u8` lit `createM3u8File` et `jellyfinMusicPath` **du serveur** et renvoie le
     `m3u8GenerationResult` ; le client n'annonce plus « playlist créée » quand le serveur a refusé
     d'écrire.
   - `resolvePlaylistBaseDir` et le paramètre `isAlbum` des deux handlers de lot sont **supprimés** —
     c'était le **dernier calcul de chemin côté client**.
   - **Conséquence assumée** : les M3U8 manuels déjà écrits à côté de la musique **restent où ils
     sont**. Les nouveaux vont dans `Playlists/`. Ménage manuel à prévoir.
   - Changement de journalisation mineur : le refus de rétrécissement est désormais loggé
     `[M3U8]` au lieu de `[Watcher] M3U8:`.
   - **✅ VÉRIFIÉ EN PROD le 2026-07-18** — 2 pistes sélectionnées dans la playlist « Add?6 » :
     - requête = `m3u8_name`, `source_id`, `file_paths`. **Absents** : `output_dir`,
       `jellyfin_music_path`, `music_root` — les trois réglages ont quitté le client.
     - réponse = `{written:true, skipped:false, total:2, resolved:2, unresolved:0}` (le
       `m3u8GenerationResult`, que le client sait maintenant interpréter).
     - **emplacement** : `/home/nonroot/Music/Playlists/Add 6 [34a1c4a9].m3u8`, à côté des M3U8 de
       watchlists (`all [957f2ab0].m3u8`, `26 [fbe144be].m3u8`) — **même dossier, même convention de
       nommage** (nom assaini + suffixe de désambiguïsation).
   - **✅ Contenu vérifié** (`cat` dans le conteneur, fourni par l'utilisateur) — la réécriture du
     préfixe Jellyfin est **mesurée**, plus déduite :
     ```
     #EXTM3U
     /Multimedia/Musique/Spotiflac/Rey Pila/Velox Veritas/Casting a Shadow - Rey Pila.flac
     /Multimedia/Musique/Spotiflac/Rey Pila/ESTAN STRANGE I (Deluxe)/Blind Date - Rey Pila.flac
     ```
     `/home/nonroot/Music/` → `/Multimedia/Musique/Spotiflac/`, en-tête présent, chemins absolus.
     Note pour une prochaine fois : l'API n'expose **aucun** endpoint de lecture de fichier texte
     (`/files/audio` liste un dossier, il ne lit pas), donc ce contrôle passe forcément par le conteneur.
6. **`getSettings()` reflète toujours le serveur** — **✅ codé le 2026-07-18 (`b8a9f0d`)**.
   Le diagnostic du §1 était à revoir : ce n'était pas un problème de *timing* de boot mais de
   **péremption permanente** — `loadSettings()` ne remplissait que le cache mémoire, `localStorage`
   n'était écrit que par `saveSettings()`.
   - **Cause racine fermée** : `rememberSettings()` enregistre désormais dans le cache **et** dans
     `localStorage`, qui devient un *cache du serveur* et non plus une source rivale.
   - **Danger côté écriture fermé aussi** : `updateSettings()` partait de `getSettings()` ; avec un
     cache froid la base est périmée, et comme une mise à jour partielle **réécrit l'objet entier**,
     changer un champ depuis l'UI pouvait **réverter tous les autres** vers ce que ce navigateur avait
     vu en dernier — écrasement silencieux du serveur. Il charge maintenant d'abord si le cache est froid.
     (Fonction sans appelant à ce jour ; corrigée plutôt que laissée en piège.)
   - **Fenêtre résiduelle rendue visible** : `getSettings()` loggue une fois par chargement quand il doit
     répondre depuis `localStorage`. `SettingsPage` appelle déjà `loadSettings()` au montage et écrase
     ses deux états, donc sa fenêtre est brève et auto-corrigée.
   - **Défauts `autoOrder` alignés** (§3) : `DEFAULT_AUTO_ORDER` / `defaultAutoOrder`.
   - Vérifié : `go build`, `go vet`, `tsc -b`, `eslint` 0 erreur.
   - **✅ VÉRIFIÉ EN PROD le 2026-07-18** (bundle `index-DGoIymbZ.js`), par **test d'empoisonnement** :
     `localStorage` forcé à `downloader:"deezer"`, `autoOrder:"POISON-qobuz-amazon-tidal"`,
     `tidalQuality:"HI_RES_LOSSLESS"` **sans toucher au serveur**, puis rechargement sans aucune action
     utilisateur → le poison a disparu et `localStorage` correspond de nouveau au serveur
     (`auto` / `deezer-qobuz-amazon-tidal` / `LOSSLESS`). **Avec l'ancien code le poison survivait**,
     `loadSettings()` n'écrivant jamais `localStorage`.
     **Interprétation verrouillée** : `saveSettings()` est le seul autre écrivain, et le chemin qui
     l'appelle au boot (`getSettingsWithDefaults`, si `downloadPath` est vide) ne se déclenche pas ici —
     `downloadPath` est renseigné, vérifié dans la même sonde. Le seul écrivain restant est donc
     `rememberSettings()`.
     Défauts : les deux occurrences de `tidal-qobuz-amazon` restant dans le bundle sont des
     `SelectItem` (options du menu déroulant), aucun défaut n'y pointe plus.

#### ✅ Limite de l'étape 6 corrigée le 2026-07-18 (`396698b`) — l'avertissement était consommé au boot

L'avertissement de `getSettings()` **se déclenche bien** (observé : `[web] [warning] getSettings()
called before the server settings were loaded`), mais son déclencheur est
[App.tsx:115](../frontend/src/App.tsx) — un `useLayoutEffect` qui lit les réglages **synchronement
avant le premier paint** pour appliquer thème/police. Cette lecture est **délibérée et correcte** :
attendre le serveur provoquerait un flash de thème erroné, et les valeurs de la session précédente
sont le bon compromis. Les lecteurs critiques (téléchargement) tournent sur action utilisateur, cache
déjà chaud.

**Le défaut** : l'avertissement étant *one-shot*, cette lecture attendue le consomme à chaque
chargement — il ne signalera donc plus une lecture froide réellement anormale survenant plus tard,
typiquement après un `loadSettings()` en échec. **Il masque le cas qu'il devait attraper.**

**Correction (`396698b`)** : le booléen est remplacé par un état de chargement explicite
`SettingsLoadState = "idle" | "loading" | "loaded" | "failed"`.

| État au moment d'une lecture froide | Signification | Journalisation |
|---|---|---|
| `idle` / `loading` | **attendu** — lecture pré-paint d'`App.tsx`, thème/police | `debug` |
| `failed` | **anormal** — le serveur n'a répondu ni en lecture ni en écriture ; toute lecture suivante sert du possiblement périmé, sans correction à venir | `warning`, **une fois par tentative** |
| `loaded` | le cache répond, cette branche n'est pas atteinte | — |

Le drapeau d'avertissement est **réarmé au début de chaque tentative de chargement** : une nouvelle
tentative qui échoue est de nouveau signalée, au lieu de rester muette pour le reste du processus.

Cas particulier traité : **être déconnecté n'est pas un échec de chargement** — il n'y a alors aucune
copie serveur par utilisateur sur laquelle faire autorité, donc ce chemin sort avant d'entrer dans la
machine à états et ne produit aucun avertissement.

Vérifié : `tsc -b` propre, `eslint` 0 erreur.
**✅ VÉRIFIÉ EN PROD le 2026-07-18** (bundle `index-D4-nQCUC.js`) :
- **Cas attendu** : au boot, la ligne est désormais `[web] [debug] getSettings() before the server
  load finished` — **plus de `[warning]`**. La régression est fermée.
- **Cas anormal atteignable** : `/api/v1/settings` (GET + PUT) forcé en échec via interception `fetch`,
  puis remontage de la page Settings → les deux journaux `Failed to load settings from backend` **et**
  `Failed to migrate settings to backend` apparaissent, ce qui prouve que le `catch` posant
  `settingsLoadState = "failed"` est exécuté.
- **Limite du test live** : l'émission du `warning` elle-même exige `cachedSettings === null` ; dans une
  session déjà chargée le cache est chaud, donc `getSettings()` répond depuis le cache sans passer par
  la branche — comportement correct. La reproduire demanderait un **boot** avec `/settings` coupé avant
  le chargement du JS (injection pré-boot impossible dans le navigateur intégré). L'émission est donc
  vérifiée par lecture de code, l'entrée dans l'état `failed` par observation live.

Ordre impératif : **1 avant 3** (le backend doit savoir calculer le chemin avant que le frontend
arrête de l'envoyer, sinon les fichiers atterrissent au mauvais endroit).

### 7.6 `useFirstArtistOnly` — levée de doute (2026-07-18)

Question posée pendant la revue de l'étape 3-frontend : « garder systématiquement le premier artiste,
c'est un réglage inexistant dans l'UI et je ne suis pas ok avec ça comme défaut, encore moins
hardcodé ». Vérifié — **le réglage existe, il n'est ni hardcodé ni activé par défaut** :

| Point | Réalité | Preuve |
|---|---|---|
| Présent dans l'UI ? | **Oui**, switch « Use First Artist Only » | [FilesTab.tsx:199](../frontend/src/components/settings/FilesTab.tsx) — onglet **File Management**, pas *General* |
| Défaut | **`false`** | [settings.ts:152](../frontend/src/lib/settings.ts) + migration qui force `false` s'il est absent |
| Valeur en prod | **`False`** | `GET /api/v1/settings` |
| Effet backend | trimming **seulement si vrai** | `if s.UseFirstArtistOnly { artist = getFirstArtistStatic(artist) }` |

**Piège à retenir** : ce réglage est dans *File Management*, pas dans *General* — un `grep` limité à
`GeneralTab.tsx` conclut à tort qu'il n'existe pas dans l'UI. C'est exactement l'erreur commise ici.

**Ce que l'étape 3 a changé** : *avant*, le frontend rognait l'artiste (selon **son** cache) **et** le
backend rognait à nouveau (selon les réglages **serveur**) — deux décideurs pouvant diverger, cas
d'école du problème §1/§2. Désormais le client envoie l'artiste brut et **seul** le serveur décide.
Le changement **réduit** le risque de trimming inattendu ; il ne l'introduit pas.
