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
| **S6** 🟠 | Client Qobuz | qobuz.go, qobuz_api.go, qobuz_community.go | **hypothèse re-vérifiée 2026-07-17** (401 non signé / 200 signé, toujours vrai) mais **validation prod bloquée** : erreurs providers rendues attribuables (✅ 07-17), mais l'override de service (`jobs_helpers.go:263`) empêche d'observer un vrai chemin Qobuz — chantier héritage à refondre avant de porter (voir §S6) | **bloqué** (observabilité) |
| **S7** ✅ | Client Amazon | amazon.go | **lu en entier** — relie S4 : mp4ff_decrypt remplace notre déchiffrement à clé unique, potentiellement déjà cassé si notre proxy a évolué pareil (voir §S7) | 2 (lié à S4) |
| **S8** ✅ | Client Spotify authentifié | spotfetch, spotify_metadata, spotify_totp | **lu en entier** — 2 fixes candidats + 1 écart mineur, TOTP confirmé identique, retry 401/403 déjà couvert chez nous | 4 (portage) |
| **S9** 🟢 | Résolution de liens + ISRC cross-provider | songlink.go, link_resolver.go, songstats.go, isrc_cache.go, isrc_finder.go, isrc_helper.go | **ISRC-direct implémenté (2026-07-15)** — câblé aux 2 pipelines (téléchargement + genre/R10), build+vet verts ; songstats/UPC/court-circuit Qobuz reportés (voir §S9) | fait (v1) |
| **S10** 🟢 | Enrichissement métadonnées | musicbrainz.go, cover.go, lyrics.go, lyrics_reader.go | **R10 résolu et vérifié en prod (2026-07-15)** — chaîne de genre Apple→Deezer→MusicBrainz + fix bug copyright (`joinAlbumCopyright` excluait `P` sans repli) ; retag mesuré en prod : 234→25 pistes, stable sur 2 runs (voir §S10) | fait |
| **S11** ✅ | Lecture/écriture de tags | metadata.go, tagging.go, upc_tags.go | **lu en entier** — vraie migration de lib de tagging, blocage CGO initialement soupçonné infirmé après lecture de S14 | 4 |
| **S12** | Formatage noms/artistes | artist_format.go, filename.go | Pas encore lu en détail | 4 |
| **S13** ✅ | Utilitaires bas signal | config, progress, filemanager, history, analysis, ffmpeg, resample, recent_fetches | **balayé** — signal effectivement faible confirmé, rien à porter sauf resample.go (feature, pas bug, à soumettre si intéressant) | 5 |
| **S14** ✅ | go.mod | dépendances upstream | **lu en entier** — rien de nouveau au-delà de S4/S11, a permis de corriger S11 (pas de blocage CGO probable) | 5 |

**Les 14 sujets sont maintenant lus (2026-07-15).** Bilan pour la prochaine étape (implémentation) :

**À implémenter, par ordre de valeur/risque :**
1. **S2 — validation durée post-téléchargement.** Petit, autonome, corrige un vrai trou (preview Tidal
   30s non détecté). ⚠️ **Bloqué : à faire APRÈS la régression ffmpeg**
   (voir [`ffmpeg-runtime-regression.md`](ffmpeg-runtime-regression.md)). `GetAudioDuration` passe par
   un parsing natif pour le FLAC mais par **ffprobe pour tout le reste** — or ffprobe ne s'exécute pas
   dans l'image `scratch` actuelle. La validation serait donc silencieusement inopérante hors FLAC.
2. **S5 — validation mimeType-vs-qualité sur Tidal.** Même famille que S2, découvert en le lisant.
3. ~~**S9 — étage ISRC-direct**~~ ✅ **fait le 2026-07-15** (voir §S9). Câblé aux 2 pipelines ; sert de
   brique pour S10 (reste à instrumenter R10 pour mesurer l'effet réel).
4. ~~**S10 — 2 correctifs**~~ ✅ **fait le 2026-07-15** (voir §S10). Chaîne de genre multi-sources +
   fix du bug copyright — mesuré en prod : le retag converge de 234 à 25 pistes (les 25 restantes
   sont un vrai « aucune source ne connaît », retentées à chaque run sans jamais bloquer). Il reste 2
   pistes bloquées sur `copyright` malgré le fix (probablement un album sans aucune entrée
   copyright chez Spotify, ni C ni P — hypothèse non vérifiée) : signal trop faible pour agir dessus.
5. **S6 — porter `qobuz_api.go`** (recherche signée). Hypothèse re-vérifiée le 2026-07-17 (401 non
   signé / 200 signé, toujours vrai), mais **le portage est bloqué en amont, pas prêt** : la
   validation prod exige d'observer un vrai chemin Qobuz, or l'override de service
   (`jobs_helpers.go:263`, décision héritée) le rend inobservable. Préalable fait le 07-17 : les
   erreurs providers sont désormais attribuables. **Reste à trancher/refondre l'override** (chantier
   transverse : UI + mécanisme de fallback) avant de porter les 350 lignes (voir §S6).
6. **S3 — retry/cooldown 429/503** adapté à nos clients providers (aucun n'en a aujourd'hui).

**Décision utilisateur requise avant tout code, pas un item technique :**
- **S4 — déchiffrement DRM (mp4ff).** Enjeu concret trouvé en S7 (notre déchiffrement Amazon à clé
  unique peut déjà être insuffisant si leur proxy a évolué vers plusieurs clés) — mais reste une
  question de fond à trancher avec l'utilisateur, pas un simple portage.

**Fonctionnalités produit à proposer, pas des bugs :**
- **S12 — retélécharger avec suffixe** (`_01`, `_02`...) au lieu d'écraser/sauter.
- **S13 — resampling FLAC** (`resample.go`), à confirmer si distinct de notre "audio converter" actuel.

**Pas prioritaire / gros effort pour gain incertain :**
- **S11 — migration vers `go.senan.xyz/taglib`** (remplacerait 3+ libs de tagging). Le blocage CGO
  initialement soupçonné est probablement infirmé (voir S14, présence de `wazero`), mais ça reste un
  gros changement d'architecture pour un bénéfice pas clairement établi.
- **S8 — cache token Spotify + retry TOTP.** Petits, sûrs, mais pas urgents.

Rien d'autre n'a été identifié comme actionnable dans S1 (déjà fait) ni S14 (pur diff de dépendances,
tout est déjà rattaché à un autre sujet).

---

## 1. Sujets détaillés

### S1 — Infra communautaire partagée ✅

**Fait le 2026-07-14.** Fichiers : `community_apikey.go`, `community_endpoints.go`,
`tidal_community.go`, `qobuz_community.go`, `http_headers.go`, `provider_endpoints.go`,
`download_cancel.go`.

- `community_apikey.go` / `community_endpoints.go` contiennent une clé API et des URLs de proxy
  communautaires **chiffrées en AES-GCM avec la clé de déchiffrement dérivable d'un literal dans le
  même fichier** — trivialement déchiffrable mais délibérément obscurci. Deux items exploitables en
  ont été extraits sans toucher aux secrets : S2 (validation durée) et S3 (pattern retry/cooldown).

  > **Position révisée le 2026-07-15 (décision utilisateur).** La version initiale de ce document
  > tranchait : « jamais déchiffré ni réutilisé, service privé de l'upstream ». **Ce n'est plus la
  > position du projet** — le déchiffrement des endpoints communautaires n'est pas exclu.
  >
  > Ce qui a changé, factuellement : `amazon.spotbye.qzz.io` — l'endpoint Amazon que **nous codons
  > déjà en dur depuis toujours**, hérité de l'upstream avant qu'il ne chiffre sa config — **ne
  > résout plus** (voir [`third-party-layer-status.md`](third-party-layer-status.md)). On utilise
  > donc déjà cette infrastructure ; la question n'est pas « commencer à s'en servir » mais « suivre
  > un endpoint qui a bougé ».
  >
  > **Distinction à garder en tête, parce qu'elle est réelle.** Les identifiants Qobuz et Apple
  > qu'on exploite déjà (§S6, §S10) sont ceux du **lecteur web public du service lui-même**,
  > embarqués dans leur propre site, utilisés comme un client normal accède au service. Les secrets
  > communautaires sont d'une autre nature : c'est **l'infrastructure privée de spotbye**, qu'il
  > héberge et paie, et qu'il a délibérément obfusquée — l'intention est explicite. Ce n'est pas un
  > verrou de sécurité (la clé est dans le même fichier), c'est un panneau.
  >
  > Ce document consigne la décision, pas un jugement. À rouvrir si l'usage devait dépasser le
  > remplacement d'un endpoint déjà utilisé (ex. : consommer massivement leur quota).
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

> ⚠️ **La question est sans objet en l'état (constat du 2026-07-15).** `amazon.spotbye.qzz.io` — notre
> seule source Amazon — **ne résout plus** : le sous-domaine a été supprimé (le domaine parent vit
> toujours). Il n'y a plus d'endpoint dont déchiffrer quoi que ce soit, et FFmpeg lui-même ne
> s'exécute pas dans l'image actuelle ([`ffmpeg-runtime-regression.md`](ffmpeg-runtime-regression.md)),
> donc le déchiffrement actuel à clé unique est cassé de toute façon.
>
> **Ordre logique révisé :** (1) retrouver l'endpoint Amazon courant — l'upstream l'a déplacé dans sa
> config chiffrée, et le déchiffrement de celle-ci **n'est plus exclu** (voir §S1, position révisée) ;
> (2) réparer FFmpeg ; (3) **alors seulement** se demander si le multi-clés mp4ff est nécessaire —
> question qui ne peut être tranchée qu'en observant ce que le nouvel endpoint renvoie réellement.
>
> Voir [`third-party-layer-status.md`](third-party-layer-status.md) pour le contexte : Amazon n'est
> pas un incident isolé, toute la couche tierce s'érode.

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

**Correction après relecture complète de `DownloadByURLWithFallback` (passe de vérification du
2026-07-15) : le retry-vers-le-miroir-suivant ne serait PAS automatique chez nous comme je l'avais dit.**
Notre flux est **en deux phases séparées**, pas entrelacé comme la version upstream actuelle :
1. `getDownloadURLRotated(apis, trackID, quality)` — tourne sur les miroirs Tidal pour trouver
   **une réponse API valide** (vérifie juste que l'appel réussit, jamais le contenu réel du fichier).
2. `downloader.DownloadFile(downloadURL, outputFilename)` — télécharge avec le **seul** miroir retenu
   en phase 1. Si ça échoue ici (ligne 688-690), **l'erreur remonte directement, sans revenir en phase 1
   avec un autre miroir**.

Upstream a justement fusionné ces deux phases (`tryDownloadAcrossTidalAPIs`) pour que le rejet
qualité déclenche un vrai changement de miroir. **Ajouter seulement la validation chez nous, sans
restructurer, donnerait un résultat plus modeste** : le job échoue proprement avec un message clair
au lieu de sauvegarder silencieusement le mauvais format — un vrai progrès, mais pas le retry
automatique vers un autre miroir que la formulation initiale laissait entendre. Refaire le flux en
une seule phase entrelacée est possible mais plus invasif — à trancher explicitement avant de coder,
pas à supposer.

**Recommandation, révisée :** deux options à présenter à l'utilisateur avant de coder — (a) ajouter
juste la validation dans `DownloadFromManifest` (petit, sûr, échec propre au lieu de silencieux, pas de
retry-miroir automatique) ou (b) fusionner `getDownloadURLRotated` + `DownloadFile` en une boucle
entrelacée comme upstream (plus de travail, comportement complet). Ne pas décider ça seul.

### S6 — Client Qobuz 🟠 hypothèse re-vérifiée, mais **validation prod bloquée par 2 défauts d'observabilité** (2026-07-17)

> **Mise à jour 2026-07-17 — accès prod obtenu, tentative de valider la condition que ce plan pose
> lui-même (« l'échec réel est-il dans `searchByISRC` ou plus loin ? »). Résultat : les deux moitiés
> de l'hypothèse tiennent toujours, mais la validation est *structurellement impossible* en l'état.**
>
> - **Re-vérifié en direct (les observations 🌍 se re-testent avant d'être citées) :** notre appel
>   non signé (`app_id=798273057`) → **HTTP 401** sur 3 ISRC ; le même appel **signé** avec la paire
>   publique (`app_id=712109809` / `589be88e…`) → **HTTP 200** avec résultats réels (Autechre, Joby
>   Burgess, !!!). Inchangé depuis le 07-15.
> - **Bloqueur 1 — erreurs inattribuables (corrigé le 07-17).** La chaîne nue `API returned status
>   401` sortait de **4 endroits** : `songlink/client.go` (×3) **et** `qobuz/client.go:198`.
>   Impossible de dire lequel a parlé dans les logs prod — c'est *exactement* le piège de R12, et j'ai
>   failli y retomber en lisant `…and fallbacks failed` (qui vient en fait de **`tidal/client.go`**,
>   pas de musicdl.me). **Corrigé :** chaque erreur porte maintenant son provider
>   (`qobuz: unsigned ISRC search returned status %d`, `songlink: …`, `tidal: …`). C'est le préalable
>   qui rend la validation de S6 possible.
> - **Bloqueur 2 — Qobuz ne peut pas être forcé (ouvert, chantier à part).** Un job
>   `service:"qobuz", allowFallback:false` a **réussi via Tidal** : [`jobs_helpers.go:263`](../jobs_helpers.go)
>   écrase le service demandé (`service = "tidal"`) dès qu'un ISRC résout vers Tidal, **sans consulter
>   `allowFallback`**. Tant que ce court-circuit existe, on ne peut pas observer un vrai chemin Qobuz
>   en prod, donc pas valider le fix. **Confirmé par l'utilisateur comme une décision *héritée*** (de
>   l'époque où aucun autre service ne marchait) — à ré-analyser en profondeur : touche l'UI (le
>   réglage de chaîne de fallback ne serait plus honoré), le mécanisme de fallback lui-même, et
>   probablement d'autres points. **Ne pas coder `qobuz_api.go` avant d'avoir tranché ça** : porter
>   350 lignes sans pouvoir mesurer l'effet, c'est précisément ce que ce plan interdit.
>
> **Ordre révisé :** (1) attribution des erreurs ✅ → (2) trancher/refondre l'override de service →
> (3) alors valider S6 sur des logs lisibles → (4) porter `qobuz_api.go`.
>
> **Cartographie de l'étape (2) faite le 2026-07-17 :** [`service-selection-map.md`](service-selection-map.md).
> L'override n'est qu'une des **4 couches** qui décident du service ; la carte montre lesquelles, les
> pièges de nommage (`allowFallback` = repli *de qualité*, pas de service), et les décisions produit à
> trancher avant de coder. **Constat prod majeur :** l'override est *universel* (tout téléchargement
> passe par le worker unique, prouvé en prod), donc le `searchByISRC` de Qobuz est quasi inatteignable
> — le « Qobuz 401 » qui motivait S6 est masqué par la bascule Tidal. **Conséquence : refondre
> l'override passe *devant* le portage `qobuz_api.go` en priorité**, pas seulement avant.

> **Vérifié en direct le 2026-07-15 (pas juste lu dans le diff) — les deux moitiés de l'hypothèse
> sont maintenant des faits, pas des suppositions :**
>
> - Notre `searchByISRC` actuel (`app_id=798273057`, requête non signée), **rejoué mot pour mot
>   contre l'API Qobuz réelle** sur 3 ISRC différents (Daft Punk, Kendrick Lamar, Rick Astley) →
>   **HTTP 401** à chaque fois (`"User authentication is required"`). Confirmé que c'est bien un
>   problème d'auth et pas de format de requête (un `app_id` vide donne 500, pas 401).
> - Le mécanisme exact d'upstream (relu à la source via `git show upstream/main:backend/qobuz_api.go`,
>   le diff original n'était plus dans le scratchpad après compaction) — scraper
>   `open.qobuz.com/track/1` → bundle JS → regex `app_id:"(\d{9})",app_secret:"([a-f0-9]{32})"` →
>   signer chaque requête (MD5 de `path_normalisé + params_triés(hors app_id/request_ts/request_sig)
>   + timestamp + secret`) — **reproduit en direct, même code de signature, mêmes 3 ISRC → HTTP 200,
>   résultats réels.**
> - **Trouvaille en bonus qui change le calibrage du portage :** la paire d'identifiants publics
>   codée en dur dans `qobuz_api.go` comme repli (`app_id=712109809`,
>   `app_secret=589be88e4538daea11f509d29e4a23b1`) **fonctionne aussi aujourd'hui, sans aucun
>   scraping**. Donc le scraping n'est pas nécessaire pour réparer le bug *maintenant* — mais sans
>   lui, on recasse dès que Qobuz fait tourner cette paire publique (upstream l'a bien fait une fois,
>   c'est le sens même de ce diff).
>
> **Plan de portage (pas encore codé, prêt à démarrer sur feu vert) :**
> - Dans le périmètre : remplacer l'appel non signé de `searchByISRC`
>   (`backend/qobuz/client.go:182-199`) par un appel signé ; scraping avec repli sur la paire codée en
>   dur si le scraping échoue ; cache **en mémoire** (mutex + variable process, pas de fichier disque
>   comme upstream — même motif que le token Apple de S9, `genre_apple.go`) ; refresh auto sur 400/401.
> - Hors périmètre pour ce lot : le scoring des résultats (`scoreQobuzSearchCandidate`) — gap réel
>   mais indépendant, `Items[0]` marche pour l'instant ; `GetDownloadURL` ne change pas (n'utilise pas
>   l'API signée non plus chez upstream).
> - `QobuzTrack`/`QobuzSearchResponse` (déjà présents) — inchangés, la réponse signée a la même forme.
> - Risque faible : un seul point d'appel, dégradation propre (repli sur la paire codée en dur qui
>   marche aujourd'hui), pas de changement de structure de données en aval.

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

### S9 — Résolution de liens + ISRC cross-provider ✅ lu · 🟢 ISRC-direct implémenté (2026-07-15)

> **Implémenté le 2026-07-15 — étage ISRC-direct.** Résolution de l'ISRC directement depuis le
> catalogue interne Spotify (`spclient.wg.spotify.com/metadata/4/track/{gid}`), via le **même token
> TOTP anonyme** que `SpotifyClient.Query()` (réutilisé, pas redupliqué). Câblé aux **2 points prévus** :
> - `backend/spotify/identifiers.go` (nouveau) : `SpotifyClient.GetTrackISRC(id)` — conversion base62→hex
>   du GID, GET metadata, parse+validation de forme de l'`external_id[isrc]`. Fonctions pures
>   (`spotifyIDToGID`, `parseTrackISRC`, `looksLikeISRC`) testées unitairement (`identifiers_test.go`,
>   16 sous-tests verts, sans réseau).
> - `backend/songlink/isrc_cache.go` (nouveau) : cache ISRC dans le **BoltDB partagé** (bucket
>   `SpotifyTrackISRC`, câblé dans `main.go` via `InitISRCCacheDBShared`) — no-op si non initialisé.
> - `backend/songlink/client.go` : `GetISRCDirect` (cache → Spotify → cache) réutilise un
>   `*spotify.SpotifyClient` **partagé et paresseux** porté par le `SongLinkClient` (1 handshake TOTP par
>   instance, pas par piste). `GetISRC` essaie le direct **en premier** puis retombe sur la chaîne
>   Song.link→Deezer → **bénéficie automatiquement au pipeline genre/R10** (son unique appelant
>   `resolveISRCFromSpotifyURL`, voir S10).
> - `jobs_helpers.go` : `getStreamingURLs` enrichit/override le champ `isrc` du résultat de la chaîne
>   avec l'ISRC-direct (plus autoritaire car sans matching par nom ; peut aussi **rattraper un échec
>   total** de la chaîne Deezer/Song.link).
>
> **Décisions actées (v1) :** ISRC seul (UPC différé — rien ne le consomme encore, lié à S6/S11) ;
> placement additif/override, **pas** de court-circuit de la chaîne pour Qobuz (optimisation de latence
> reportée). `go build ./...` + `go vet ./...` verts sur tout le module.
>
> **Reste ouvert (hors de ce lot) :** (a) brancher explicitement `resolveISRCFromSpotifyURL` +
> instrumenter R10 pour mesurer l'effet réel (voir S10) ; (b) factoriser les 3 implémentations
> d'ISRC-resolution en une seule au lieu de les laisser diverger (dette notée au point 2 ci-dessous).

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

**Carte complète des chemins ISRC chez nous, établie en repassant à la source (2026-07-15) — il y a en
réalité 3 chemins distincts, pas une seule chaîne :**

1. **Pipeline de téléchargement, couche primaire** — `jobs_worker.go:124` appelle
   `jm.getStreamingURLs(job)` **avant** `backend.ExecuteDownload`. C'est la chaîne à 4 étages
   documentée ci-dessus.
2. **Pipeline de téléchargement, filet de sécurité** — `backend/downloader.go`, dans le goroutine
   `resolveISRC` d'`ExecuteDownload` : si `req.ISRC` (déjà résolu par l'étape 1) est vide, un
   **deuxième essai indépendant** tourne (Deezer par nom, puis Song.link API/HTML — seulement 2
   étages, pas les 4 ; pas de tentative Apple Music ici). Ce n'est pas un système concurrent
   incohérent comme je l'ai cru un instant en le découvrant — c'est un vrai filet de sécurité en
   cascade, mais dupliqué (deux implémentations légèrement différentes du même besoin, jamais
   partagées).
3. **Pipeline genre/MusicBrainz (S10/R10), complètement séparé** — `providerutil/genremeta.go::
   resolveISRCFromSpotifyURL` appelle **exclusivement** `songlink.GetISRC()` (le chemin à 2 maillons
   d'origine : Song.link → Deezer, sans Apple Music ni HTML scraping). C'est le seul appelant vivant de
   `GetISRC()` — confirmé par recherche exhaustive. C'est le maillon le plus faible des trois, et
   c'est précisément lui qui alimente R10 (voir S10).

**Recommandation de séquencement pour le codage, révisée :** l'ISRC-direct doit être ajouté à **au
moins 2 endroits** pour avoir son plein effet, pas un seul comme je l'avais dit initialement :
- Dans `jobs_helpers.go::getStreamingURLs` (étage 0, avant Deezer) pour le pipeline de téléchargement.
- Dans `songlink.GetISRC()` lui-même (bénéficie automatiquement de son unique appelant,
  `resolveISRCFromSpotifyURL`) pour le pipeline genre/R10 — voir S10.

Vu la duplication trouvée au point 2, il y a aussi une question de fond à soulever (pas forcément à
résoudre dans ce lot) : factoriser les 2-3 implémentations d'ISRC-resolution en une seule, plutôt que
d'ajouter l'ISRC-direct séparément à chacune et les laisser diverger davantage.

### S10 — Enrichissement métadonnées ✅ lu · 🟢 chaîne de genre implémentée (2026-07-15)

> **Implémenté le 2026-07-15 — chaîne multi-sources, et ce n'est PAS un portage.**
> S10 a établi que l'upstream n'a aucune réponse (toujours MusicBrainz seul, même algo de tags
> folksonomie). La chaîne ci-dessous est donc **notre propre conception**, née de mesures directes :
> - `backend/meta/genre.go` — la chaîne **Apple → Deezer → MusicBrainz**, **tous ancrés sur l'ISRC**
>   (aucun match par nom). `formatGenreNames` unifie le rendu (Title Case, cap 5, `util.Separator`).
> - `backend/meta/genre_apple.go` — token JWT soulevé du bundle du web player Apple (même motif que
>   `qobuz_api.go` upstream), caché, **re-soulevé sur 401** (durée de vie mesurée : **35 jours**).
> - `backend/meta/genre_deezer.go` — ISRC → album → genres, sans auth.
> - `api_status.go` — `pingAppleMusic` dans `coreServices` : sonde **l'étage Apple seul** de bout en
>   bout (d'où `ResolveGenreFrom`), sinon le check virerait au vert grâce à Deezer alors qu'Apple est
>   mort.
> - `FetchMusicBrainzMetadata` **supprimée** (devenue injoignable ; son formatage vit dans
>   `formatGenreNames`).
>
> **Mesures qui ont guidé la conception (toutes vérifiées, pas supposées) :**
> - API **publique** Spotify : **inutilisable** avec notre token anonyme — quota nul, backoff
>   escaladant 14s→29s→58s. Exigerait une app enregistrée.
> - iTunes **par ISRC** : **0/8** (le paramètre `isrc` est silencieusement ignoré).
> - **Apple par ISRC** : répond sur tous les vrais ISRC testés ; **40 requêtes concurrentes à ~850
>   req/s sans un seul 429**.
> - **Deezer par ISRC** : 8/8, mais genre d'**album** (grossier).
> - Les ISRC doivent venir de **Spotify** (S9) : 5/5 sur Apple, contre 2/3 avec ceux de Deezer.
>
> **Correction de ce document :** j'y avais noté « Spotify fournit du genre nativement au niveau
> artiste » comme piste prometteuse. **C'est faux pour l'API qu'on utilise** — les réponses réelles
> capturées contiennent **zéro** champ genre (track, album, artist). Le `Genres []string` de notre
> struct est vestigial (il calque l'API *publique*). Déduction depuis la struct au lieu de vérifier
> la réponse.
>
> **Ce que ça ne corrige PAS — R10 reste ouvert.** La chaîne améliore la **couverture** ; elle ne
> corrige pas la **non-convergence**. La clause `genre = ''` (`backend/db/tracks.go:190-193`)
> re-sélectionnera éternellement toute piste dont aucune source ne connaît le genre. C'est un
> correctif séparé (retirer `genre` de la clause, ou `last_retag_at`), et c'est **le vrai R10**.

### S10 — analyse d'origine ✅ lu

**Fichiers :** `musicbrainz.go`, `cover.go`, `lyrics.go`, `lyrics_reader.go` (nouveau). Comparé à
`backend/meta/musicbrainz.go` et à la vraie boucle `retagIncompleteMetadata`/`retagOneTrack`
(`api_admin.go:648+`) et `providerutil/genremeta.go`, pas juste au fichier musicbrainz isolé.

**Réponse directe à la question posée dans le libellé de R10 ("le bon service de tag pour le genre") :
non, upstream n'a pas changé de service.** Vérifié explicitement : ils utilisent toujours exclusivement
MusicBrainz, avec exactement le même algorithme de sélection qu'avant ce diff (inchangé, donc pas
touché par ce lot de commits) — `recording.Tags` (des tags folksonomie libres, pas une taxonomie de
genre officielle), soit le tag au `Count` le plus élevé (`useSingleGenre`), soit les 5 premiers tags
tels quels. Aucun filtrage des tags non-genre (ex. "seen live", "vinyl", "2020s" sont des tags
MusicBrainz valides mais pas des genres) — ce n'est pas nouveau, ni corrigé, ni le sujet de ce diff.

**Piste intéressante trouvée en cherchant, jamais exploitée même par upstream :**
`spotify_metadata.go::ArtistInfoMetadata.Genres []string` — Spotify **fournit bien du genre**
nativement (au niveau artiste, pas piste) dans les réponses de leur API interne. Mais **upstream ne
l'extrait jamais** de la réponse GraphQL brute — le seul endroit où ce champ est renseigné dans tout
leur code, c'est `Genres: []string{}` (un placeholder vide). Donc même eux n'ont pas creusé cette voie
alternative — pas un renoncement après essai, juste jamais tenté. À garder en tête comme option
propre (zéro appel réseau supplémentaire, la donnée arrive avec la réponse Spotify qu'on récupère déjà)
si on veut vraiment explorer "un autre service", plutôt que de chercher du côté Discogs/Last.fm.

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

**Deuxième hypothèse (confirmée comme le seul chemin possible, pas juste "plus probable") : l'ISRC ne
se résout même pas jusqu'à MusicBrainz.** `retagOneTrack` → `providerutil.FetchGenreMetadataAsync` →
`resolveISRCFromSpotifyURL` → **`songlink.GetISRC`**. Vérifié par recherche exhaustive (passe du
2026-07-15) : `songlink.GetISRC()` n'a **qu'un seul appelant vivant dans tout le repo**, c'est celui-ci
— donc c'est bien LE chemin emprunté à chaque retag, pas une possibilité parmi d'autres. Et
`GetISRC()` lui-même n'a que 2 maillons (Song.link → Deezer, sans Apple Music ni HTML scraping — les
étages 3 et 4 de la chaîne à 4 niveaux du pipeline de téléchargement, voir S9, n'existent pas ici),
donc c'est objectivement le chemin de résolution ISRC le plus fragile des trois qu'on a dans tout le
repo. Si cette résolution échoue, `resolvedISRC == ""` et **la fonction retourne un résultat vide
silencieusement — musicbrainz.go n'est jamais appelé, aucune erreur distincte ne remonte.** Vu d'en
haut, ça ressemble exactement à "sur-sélectionne et skip 99%" : plein de pistes "traitées" sans genre,
sans qu'on puisse distinguer "MusicBrainz n'a pas de genre pour ce morceau" de "on n'a même pas pu
obtenir l'ISRC".

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

**Correction après avoir lu `go.mod` (S14) : la question CGO que j'avais soulevée ici est probablement
sans objet.** J'avais supposé `go.senan.xyz/taglib` = binding CGO vers la lib C++ TagLib, ce qui aurait
bloqué structurellement notre build `CGO_ENABLED=0`. Mais `go.mod` upstream ajoute aussi
`github.com/tetratelabs/wazero` (un runtime WASM en Go pur) comme nouvelle dépendance indirecte — signe
que `taglib` est vraisemblablement une compilation WASM de TagLib exécutée via wazero, **pas** un
binding CGO natif. Si confirmé (pas vérifié avec certitude, juste déduit du graphe de dépendances), ça
lève le blocage que j'avais signalé — reste un gros changement d'architecture (remplacer 3+ libs par
une seule), mais plus un blocage de build.

**`upc_tags.go`** : petit helper autonome de normalisation des clés de tag UPC (`UPC`, `BARCODE`,
`TXXX:UPC`, atomes iTunes `----:com.apple.itunes:upc`, etc.) — portable facilement si on décide un jour
de supporter l'UPC (lié à la découverte UPC de S6/S9).

**Recommandation : toujours pas prioritaire dans l'immédiat** (gros effort, gain incertain), mais moins
bloqué qu'initialement estimé — à vérifier concrètement (essayer de builder avec `CGO_ENABLED=0`) si le
sujet revient sur la table. `upc_tags.go` seul est portable à part si besoin.

### S12 — Formatage noms/artistes ✅ lu

**Fichiers :** `artist_format.go` (nouveau, 90 lignes), `filename.go` (146 lignes de diff). Lus en
entier.

**`artist_format.go`** : découpe une chaîne d'artistes en valeurs de tag séparées (dédupliquées),
séparateur configurable virgule/point-virgule. Différent de notre `useFirstArtistOnly` (qui ne garde
que le premier artiste) — ça sert plutôt à écrire **plusieurs valeurs de tag artiste distinctes**
(convention multi-valeur ID3/Vorbis) au lieu d'une chaîne jointe. Même thème que le `separator` non
propagé jusqu'aux tags trouvé en S8 — pas un item séparé à traiter, un symptôme de plus du même écart.

**`filename.go`** : nouveaux tokens de template (`{isrc}`, `{upc}`, `{category}`, `{creator}`,
`{total_tracks}`, `{total_discs}`, `{artists}`) — mineur, extension du vocabulaire de nommage.

**Découverte indépendante, plus intéressante que le fichier lui-même :**
`ResolveOutputPathForDownload` — un mécanisme "retélécharger avec suffixe" (`_01`, `_02`, ...) au lieu
d'écraser ou de sauter un fichier existant. **On n'a rien d'équivalent** (zéro occurrence dans tout le
repo). C'est une vraie fonctionnalité produit, pas un bug — à soumettre à l'utilisateur comme option
plutôt qu'à trancher ici.

### S13 — Utilitaires bas signal ✅ balayé

**Fichiers :** `config.go`, `progress.go`, `filemanager.go`, `history.go`, `analysis.go`, `ffmpeg.go`,
`resample.go`, `recent_fetches.go`. Balayage structurel (signatures de fonctions, pas ligne à ligne) —
confirme que le signal est bien faible sur ce lot, comme prévu au découpage initial.

- `config.go` — que des réglages déjà couverts ailleurs (S9 : `GetLinkResolverSetting`, S12 :
  `GetRedownloadWithSuffixSetting`) ou de la plomberie de config desktop propre à eux.
- `progress.go` — confirmé : pattern d'état global de queue Wails, exactement ce qu'on a supprimé
  nous-mêmes en v3.4.0 ("removed dual queue system"). Rien à porter.
- `filemanager.go` / `history.go` — diffs trop petits pour être structurels (+34/-1 et +11/-13),
  probablement des ajustements internes mineurs. Rien identifié.
- `analysis.go` — restructuration vers un decode-PCM-vers-base64 pour analyse **côté frontend**
  (pattern desktop Wails). On a déjà notre propre `backend/audio/analysis.go` + `spectrum.go`,
  architecture différente (web) — pas comparable terme à terme.
- `ffmpeg.go` (607 lignes) — quasi entièrement de la découverte/installation locale de ffmpeg
  (Homebrew, chemins multi-OS) pour une appli desktop. Sans objet : on bundle ffmpeg à un chemin fixe
  dans Docker.
- `resample.go` — **nouvelle fonctionnalité** (changer sample rate/bit depth d'un FLAC), pas un bug.
  Pas confirmé si ça recoupe notre "audio converter" existant ou si c'est vraiment différent (conversion
  de format vs resampling). Laissé volontairement `UNMAPPED` dans `.github/upstream-map.txt` — à
  soumettre comme option produit si intéressant, pas une correction à porter.
- `recent_fetches.go` — cache JSON local de recherches récentes pour l'UI desktop. On a déjà
  `backend/history.go` (multi-utilisateur, BoltDB) qui couvre un besoin équivalent en mieux.

**Rien de ce lot ne justifie une lecture ligne à ligne supplémentaire.**

### S14 — go.mod ✅ lu

Diff complet lu. Rien de nouveau au-delà de ce qui est déjà couvert ailleurs, mais une correction utile
trouvée en le lisant en dernier :

- `+github.com/Eyevinn/mp4ff v0.52.0` — lié à S4, déjà couvert.
- `+go.senan.xyz/taglib v0.11.1` + `+github.com/tetratelabs/wazero v1.10.1` (indirect) — lié à S11.
  **La présence de `wazero` (runtime WASM Go pur) a permis de corriger le point CGO soulevé en S11** —
  voir la mise à jour de cette section.
- `-github.com/mewkiz/flac v1.0.13` (+ ses deps transitives `mewkiz/pkg`, `mewpkg/term`,
  `icza/bitio`) — l'ancienne lib de lecture FLAC d'upstream, retirée parce que `taglib` (S11) la
  remplace. **On utilise encore `mewkiz/flac` nous-mêmes** (`go.mod` actuel) — cohérent avec le fait
  qu'on n'a pas fait la même migration, pas un signe de dérive à corriger isolément.
- `wailsapp/wails/v2 v2.11.0→v2.12.0`, `+golang.org/x/image`, `+git.sr.ht/~jackmordaunt/go-toast/v2`
  et le reste des indirects Wails — tous sans objet, écosystème desktop qu'on n'a pas.

**Rien à agir spécifiquement sur ce sujet au-delà de S4/S11 déjà traités.**

---

## 2. Recoupement avec les changelogs de release (2026-07-15)

L'utilisateur a fourni les notes de version publiques (v7.1.0 → v7.1.9) pour vérifier si le triage
fichier-par-fichier avait raté des sujets — un changelog capture le niveau produit ("Lyrics Manager
est un outil à part entière") qu'un diff de code seul peut sous-pondérer. 8 items identifiés comme non
couverts par S1-S14, tous creusés avec la même rigueur (lecture directe du code, pas de suppositions).

**2 vraies lacunes confirmées :**

- **Gestionnaire de paroles ("Lyrics Manager", v7.1.8/v7.1.9).** Vérifié : `app.go` expose
  `ReadEmbeddedLyrics`, `ExtractLyricsToLRC`, `ScanLyricsFolder`, `SaveLyrics` — un vrai outil batch
  (scanner un dossier, lire/éditer/extraire les paroles déjà embarquées). Chez nous,
  `backend/meta/lyrics.go` et la route `/api/v1/media/lyrics` ne font que **récupérer et embarquer**
  des paroles (LRCLIB) — rien pour en lire/éditer/extraire depuis un fichier déjà tagué. Écart réel et
  net. Portable via notre File Manager existant (parcours de dossier déjà là) plutôt que des dialogues
  desktop.
- **Formats WAV/AIFF/Opus dans le convertisseur audio (v7.1.8).** Vérifié : `backend/audio/ffmpeg.go`
  (`allowedConvertOutputFormats`) **ne supporte que `mp3` et `m4a`**. Upstream ajoute WAV/AIFF (PCM
  16/24-bit selon le sample format source, big/little-endian selon le conteneur) et Opus (`libopus`,
  192k par défaut) — arguments ffmpeg exacts trouvés dans `backend/ffmpeg.go` upstream, portage direct
  et à faible risque.

**6 résolus après vérification — pas des lacunes :**

- **"Implemented download rate limiting" (v7.1.8).** Cherché explicitement (rate/delay/throttle/
  bandwidth dans tout `backend/`) — aucun mécanisme distinct trouvé. Very probablement la même chose
  que "Added API cooldown information" de la même release, décrite deux fois différemment dans le
  changelog — déjà couvert par S1/S3.
- **"Automatic queue stopping when API enters cooldown" (v7.1.9).** `SetCommunityCooldown` /
  `SetRateLimitCooldown` (`progress.go`) ne font qu'exposer un état — aucune boucle de queue backend
  ne le consulte pour s'auto-arrêter. C'est un comportement **frontend** (leur UI desktop regarde cet
  état et met en pause côté client). Pertinent seulement si/quand on porte S3 et qu'on veut le même
  réflexe UX côté web — pas un item backend à part.
- **192kHz (v7.1.0).** Aucune constante ni logique dédiée trouvée, même chez upstream. Cette entrée de
  changelog date d'avant/pile notre baseline et décrit vraisemblablement une capacité déjà inhérente au
  palier `HI_RES_LOSSLESS` qu'on a déjà, pas un palier séparé.
- **Tidal "variants" (v7.1.5).** Aucune occurrence du terme dans le code. Chronologiquement collée à
  "custom HiFi API instances" (v7.1.6) — vraisemblablement la même fonctionnalité que
  `getConfiguredTidalAPIAttemptList`/`GetCustomTidalAPISetting`, déjà couverte en S5.
- **Vérifier les fichiers existants par ISRC (v7.1.6).** Vérifié : `GetExistingFileCheckModeSetting` +
  `buildExistingFileLookupIndex` scannent le filesystem à la demande pour matcher par ISRC plutôt que
  par nom de fichier. On a déjà résolu ce problème autrement (et plus robustement) avec le catalogue
  SQLite persistant (`checkCatalogDedup`, `library-rebuild`) — pas un gap, une architecture différente
  pour le même besoin.
- **Génération automatique de fichier de log (v7.1.9).** `App.CreateLogFile` exporte une liste de logs
  en mémoire vers un `.txt` local, à la demande (bouton desktop). On a déjà mieux : un ring buffer
  (`applog.go`) exposé via `GET /api/v1/admin/logs` et une page Debug Logs dédiée unifiant back+front.
  Pas un gap.

---

## 3. Où sont les choses

- `.github/upstream-map.txt` — classification mécanique fichier par fichier (MAPPED/IGNORE/non listé
  = UNMAPPED), lue par le workflow et `check-upstream.sh`. Source de vérité pour "où ça mappe".
- Issue GitHub label `upstream-sync` — état live du diff (commits, fichiers modifiés depuis la
  dernière baseline), régénérée automatiquement chaque lundi ou sur `workflow_dispatch`.
- Ce document — pourquoi chaque sujet compte, dans quel ordre, et ce qu'on a déjà appris en lisant le
  code. Source de vérité pour le contenu et la priorité.
- `.github/upstream-last-reviewed.txt` — SHA de la baseline actuelle. À avancer manuellement une fois
  un sujet réellement traité (voir instructions dans l'issue `upstream-sync`).
