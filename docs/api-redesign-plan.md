# Plan — cohérence de l'API (read / manage / admin) + accès DB

> **Statut re-vérifié contre le code le 2026-07-19 : toujours rien de commencé.** Les 4 phases sont
> intactes. Vérification faite parce que beaucoup de travail API a eu lieu le 18/19 (migration des
> réglages : `/downloads/track`, `/files/exists`, `/media/*`, suppression de `/files/m3u8`) — mais
> c'était un chantier **orthogonal** : qui décide des réglages, pas ce que l'API expose ni qui y accède.
>
> **État mesuré :**
>
> | Phase | Vérification | État |
> |---|---|---|
> | 1 | `AudioMetadata` (backend/filemanager.go:33) a `genre`+`isrc` ; `meta.FullTrackTags` a en plus **`SpotifyID`, `ReleaseDate`, `Copyright`** | ❌ écart réel |
> | 2 | seuls endpoints admin : `retag-legacy`, `library-rebuild`, `retag-incomplete-metadata`, `logs` | ❌ rien |
> | 3 | aucune console SQL | ❌ rien |
> | 4 | 72 `read`, 68 `manage` via `v1RequirePermission` ; 12 routes via `v1RequireAdmin` | ❌ non fait |
>
> ⚠️ **Correction d'une affirmation faite en séance** : j'avais dit que le niveau `admin` était « déclaré
> mais mort » parce que `v1RequirePermission(…, "admin")` n'est jamais appelé. **C'est faux.** Le niveau
> transite par `v1RequireAdmin`, qui lit `JWTClaims.IsAdmin` ; pour une clé API celui-ci vaut
> « la clé porte la permission `admin` **ET** le compte propriétaire est toujours admin », re-vérifié à
> chaque requête ([api_keys.go:161-180](../api_keys.go), avec garde explicite contre l'escalade et
> contre un admin rétrogradé). Les trois niveaux fonctionnent. La phase 4 est donc une question de
> **cohérence**, pas de sécurité.
>
> **Statut d'origine (2026-07-15) : documenté, pas commencé.** Ce chantier a été identifié le 2026-07-15 pendant le suivi
> de R10 (convergence du retag), mais volontairement **pas entamé** — décision explicite de
> l'utilisateur de le documenter d'abord et d'y revenir plus tard. Ne pas coder sans repasser par ce
> document pour confirmer les décisions ouvertes (§3).

## 0. Comment on en est arrivé là

En corrigeant R10 (voir `audit-refactoring-couche2.md` §R10 et l'historique de session), deux besoins
concrets sont apparus en cours de route :

1. **`AudioMetadata` (l'API du File Manager, `GET /api/v1/files/metadata`) a été étendue champ par
   champ** — d'abord `genre`+`isrc` (pour vérifier le travail du retag), puis on allait devoir
   ajouter `copyright` pour investiguer les 2 pistes encore bloquées dessus après le fix. Ajouter un
   champ à chaque fois qu'on en a besoin est le symptôme d'une API qui n'expose pas tout ce qu'elle
   pourrait exposer facilement (`meta.ReadFullTrackTags` — utilisé par le retag — lit déjà tout :
   `isrc`, `genre`, `copyright`, `spotify_id`).
2. **Debugger en prod a nécessité une clé API admin passée en clair dans le chat**, faute d'un accès
   direct à la base — j'ai dû construire des scripts Python ad hoc pour parcourir `GET /files` +
   `GET /files/metadata` récursivement afin de retrouver des pistes précises, alors qu'une requête
   SQL directe aurait pris 2 secondes. La clé a été utilisée avec l'accord explicite de
   l'utilisateur, mais reste **à révoquer** après usage — c'était un pis-aller, pas l'outil qu'on
   veut avoir normalement sous la main.

L'utilisateur a formulé l'objectif ainsi (verbatim, traduit du contexte) :

> Faire en sorte que l'API ait accès à tous les champs plutôt que d'ajouter modif après modif. Avec
> les accès admin, pouvoir au moins lire la DB si ce n'est la modifier manuellement. Le but : que
> l'API puisse automatiser les fonctions de l'app en mode **write**, lire les données en mode
> **read**, et servir d'outil de dev/debug en mode **admin**. Il faudra sans doute revoir les
> endpoints pour reconstruire un outil cohérent.

## 1. Ce qui existe déjà (reconnaissance du 2026-07-15)

**Le modèle à 3 niveaux n'est pas à inventer — il existe déjà, partiellement appliqué.**

```go
// api_keys.go
type APIKey struct {
    ...
    Permissions []string // "read", "manage", "admin"
    // "download" = alias legacy de "manage" (pre-rename, encore honoré)
}

// api_v1.go
func v1RequirePermission(w http.ResponseWriter, r *http.Request, perm string) bool {
    user := GetUserFromContext(r)
    if user.IsAdmin || !user.IsAPIKey { return true } // session navigateur = accès complet
    for _, p := range user.Permissions {
        if p == perm || (perm == "manage" && p == "download") { return true }
    }
    return false // 403
}
```

C'est exactement `read` / `manage` (= write/automatisation) / `admin` (= dev/debug, accès total).
**Le problème n'est pas l'absence du modèle, c'est son application incohérente** sur les ~70 routes
existantes (ex. `GET /files/metadata` est actuellement `admin`-only alors que c'est de la lecture
pure — cf. commentaire dans `api_files.go:260-262`).

### Schéma SQLite du catalogue (7 tables, propre, rien de sensible)

| Table | Rôle |
|---|---|
| `tracks` | identité Spotify d'une piste (ISRC, genre, copyright, etc. — dénormalisé, voir migration 0005) |
| `albums` | stub album (nom, label, copyright, cover) |
| `library_files` | pont piste ↔ fichier disque (provider, qualité, statut, chemin) |
| `download_attempts` | journal permanent de chaque tentative de téléchargement |
| `watchlist_tracks` | contenu courant de chaque watchlist |
| `playlist_snapshots` / `playlist_snapshot_tracks` | historique versionné des playlists |

Migrations : `backend/db/migrations/0001` à `0005`. Aucune table ne contient de secret — c'est de la
métadonnée musicale et de l'état de téléchargement.

### BoltDB — ce qui est sensible, à ne PAS exposer tel quel

- **Clés API** (`bucketAPIKeys`) : déjà hashées SHA-256 (`KeyHash`), la clé brute n'est jamais
  stockée — rien à protéger de plus ici.
- **Token Tidal Premium personnel** (flow device-code, `/api/v1/auth/tidal/*`) : stocké côté serveur
  quelque part en BoltDB — **emplacement exact pas encore localisé** lors de la reconnaissance,
  à faire avant d'exposer un outil de lecture générique.
- **Configs proxy** (`bucketProxies`) : listes d'URLs. À vérifier si certaines embarquent un token
  dans la query string (pattern courant chez les proxies communautaires) — pas confirmé.
- **Utilisateurs** (`bucketUsers`, `UserProfile`) : pas de mot de passe stocké localement (l'auth
  délègue entièrement à Jellyfin) — rien de sensible dans cette struct précise.

**Conclusion reconnaissance : un outil "lire toute la DB" sans discrimination risque de faire fuiter
le token Tidal personnel** (capture d'écran, copier-coller dans un ticket de support) même si l'accès
lui-même reste admin-only. À traiter par exclusion explicite, pas en confiant tout à la barrière
d'authentification seule.

## 2. Plan proposé (4 phases)

| Phase | Contenu | Risque | Effort |
|---|---|---|---|
| **1** | `AudioMetadata` expose tous les champs de `FullTrackTags` en une fois (`copyright`, `spotify_id` en plus de `genre`/`isrc` déjà faits) | nul | petit |
| **2** | Endpoint(s) admin de **browse structuré** du catalogue SQLite (lister/filtrer/paginer par table) — répond à « lire la DB » sans risque d'injection ni d'exposition incontrôlée | faible | moyen |
| **3** | Console SQL admin, **`SELECT` uniquement** par défaut — le vrai outil de debug ad hoc (aurait évité les scripts Python de cette session) | faible si restreint en lecture ; sérieux si écriture arbitraire ouverte | moyen |
| **4** | Audit + réorganisation cohérente des ~70 endpoints existants sur les 3 niveaux `read`/`manage`/`admin`, correction des incohérences trouvées (ex. `/files/metadata`) | gros chantier, nombreux fichiers touchés | grand |

## 3. Décisions ouvertes (à trancher avant de coder)

1. **Phase 4 — maintenant ou différée ?** C'est le plus gros morceau ; il peut être fait
   indépendamment des phases 1-3 et attendre.
2. **Écriture manuelle sur la DB — SQL arbitraire ou actions vétées ?**
   Une console qui accepte `UPDATE`/`DELETE` sur le réseau est puissante mais dangereuse (une requête
   mal formée peut corrompre/effacer des données réelles sans confirmation possible côté serveur).
   Alternative plus sûre : une liste fermée d'actions admin (« forcer le statut d'un fichier »,
   « réinitialiser un token », etc.) plutôt que du SQL brut. **Pas tranché.**
3. **Redaction des secrets BoltDB** : finir de localiser le token Tidal personnel et vérifier les
   configs proxy avant d'exposer tout outil de lecture générique — exclusion par défaut de ces
   champs, à confirmer comme principe.

## 3bis. Plan d'exécution détaillé (établi le 2026-07-19)

### Phase 1 — exposer tous les champs · *petit, risque nul, aucune décision requise*

**Écart mesuré :** `AudioMetadata` omet `SpotifyID`, `ReleaseDate`, `Copyright` que
`meta.FullTrackTags` lit déjà.

1. Ajouter les 3 champs à `AudioMetadata` (`backend/filemanager.go:33`).
2. Les remplir là où la struct est construite — **à confirmer avant de coder** : vérifier que le
   lecteur utilisé par `GET /api/v1/files/metadata` est bien `ReadFullTrackTags` et non un lecteur
   partiel distinct. Si c'est un autre lecteur, le vrai travail est de le faire converger.
3. Frontend : `FileManagerPage` affiche ces métadonnées — l'affichage des nouveaux champs est
   **optionnel**, l'API seule suffit à lever la friction (scripts de debug).
4. Mettre à jour `api-reference.md`.

**Critère de fin :** `GET /files/metadata` sur une piste retaguée renvoie son `copyright` et son
`spotify_id` sans modification de code supplémentaire.

### ⚠️ Phase 1 — périmètre CORRIGÉ après vérification (2026-07-19)

Le plan supposait « ajouter deux champs à une struct ». **Faux.** `/files/metadata` ne passe **pas**
par `ReadFullTrackTags` : la chaîne est `ReadFileMetadata` → `backend.ReadAudioMetadata` →
`readFlacMetadata` / `readMp3Metadata` / `readM4aMetadata`. **Il existe deux lecteurs de tags
indépendants**, chacun avec son propre dispatch par format.

| Format | Lecteur File Manager | Lecteur retag |
|---|---|---|
| FLAC | `readFlacMetadata` — 9 champs | `readFullTrackTagsFromFlac` — **11** (+`SpotifyID`, +`Copyright`) |
| MP3 | `readMp3Metadata` — 9 | `readFullTrackTagsFromMp3` — **11** |
| M4A | `readM4aMetadata` → `readMetadataWithFFprobe` | `readFullTrackTagsFromFFprobe` → `util.ReadFFprobeTags` |

**Le lecteur du retag n'est PAS un sur-ensemble strict.** Sur M4A, celui du File Manager est plus
tolérant sur les clés ffprobe :

| Donnée | File Manager | Retag |
|---|---|---|
| disque | `disc` | **`disk`** — chacun une clé *différente*, pas un sur-ensemble |
| artiste d'album | `album_artist` **ou** `albumartist` | `album_artist` |
| année | `date` **ou** `year`, **la plus longue gagne** | `date` |
| ISRC | `isrc` **ou** `tsrc` | `isrc` |

**Correction du 2026-07-19, après relecture du lecteur supprimé** (`git show b4a0121^`). J'avais décrit
l'écart comme « le File Manager tolère les deux orthographes, le retag une seule ». C'est vrai pour
3 lignes sur 4, mais faux deux fois :

- **disque** : chacun lisait une clé *différente* et unique. L'union couvre bien les deux, mais la
  forme de l'écart n'était pas celle que j'ai décrite.
- **année** : le File Manager ne prenait pas la première clé trouvée, il gardait **la valeur la plus
  longue** (`if metadata.Year == "" || len(value) > len(metadata.Year)`), donc la plus précise —
  `2020-03-05` plutôt que `2020`. Un `firstTag(tags, "date", "year")` aurait perdu le mois et le
  jour sur tout fichier portant les deux tags. D'où `longestTag()`, distinct de `firstTag()`.

→ **Brancher naïvement `/files/metadata` sur `ReadFullTrackTags` perdrait des données M4A.**

**Phase 1 révisée :** (1) fusionner les tolérances de clés dans le lecteur du retag ; (2) faire passer
`ReadAudioMetadata` par `ReadFullTrackTags` + mapper vers `AudioMetadata` enrichi ; (3) un test
d'équivalence entre les deux lecteurs sur les mêmes fichiers. **Effort : petit → petit-moyen. Risque :
nul → réel mais identifié.**

#### ✅ Phase 1 — faite (2026-07-19), reste à vérifier en prod

1. `firstTag(tags, keys...)` ajouté dans `backend/meta/spotify_index.go` ;
   `readFullTrackTagsFromFFprobe` accepte désormais les deux orthographes de chacune des quatre
   données du tableau ci-dessus.
2. `AudioMetadata` gagne `SpotifyID` et `Copyright` ; `ReadAudioMetadata` délègue à
   `meta.ReadFullTrackTags` et mappe `ReleaseDate → Year`.
3. Les quatre lecteurs devenus orphelins (`readFlacMetadata`, `readMp3Metadata`, `readM4aMetadata`,
   `readMetadataWithFFprobe`, ~154 lignes) sont supprimés, avec leurs imports. **Il ne reste qu'un
   seul lecteur de tags dans le projet** — c'était le vrai sujet de la phase.

**Écart assumé avec le plan :** le point (3) demandait « un test d'équivalence entre les deux
lecteurs ». Ce test n'a plus d'objet une fois le second lecteur supprimé. Le test écrit
(`backend/meta/ffprobe_tag_aliases_test.go`) couvre à la place le risque précis que la vérification
avait mis au jour : les alias de clés ffprobe, y compris le cas « clé présente mais vide » qui ne
doit pas masquer l'autre orthographe.

#### ✅ Vérifié en prod (2026-07-19)

- **FLAC** (`Yadnus - !!!.flac`) : `"spotify_id": "6xglGBh5aaqL10Jx8jRChr"`,
  `"copyright": "2007 Warp Records"`. **Critère de fin atteint.**
- **MP3** (`Tour De France - Powerplant.mp3`) : `copyright` présent ; `spotify_id` et `isrc` vides —
  fichier ajouté à la main, jamais retagué, donc cohérent.
- **M4A : non vérifiable.** Inventaire de la bibliothèque : 2589 `.flac`, 1 `.mp3`, **0 `.m4a`**.
  Le format est pourtant bien produit, par le chemin Amazon (`backend/amazon/client.go:211`) — son
  absence vient du provider Amazon bloqué derrière la vérification navigateur, pas d'un format mort.
  Le chemin des alias reste donc **couvert en unitaire et par relecture du câblage, pas de bout en
  bout**. À retester quand Amazon redeviendra utilisable (chantier couche API externe).

**Note d'exploitation :** la racine de bibliothèque est `/home/nonroot/Music` (pas `/music`), et
`/files/metadata` est **admin-only** (`v1RequireAdmin`, `api_files.go:331`) — un endpoint de lecture
pure protégé au niveau le plus haut, à réexaminer en phase 4.

### Phase 2 — parcours structuré du catalogue · *moyen, 1 décision*

**Périmètre mesuré :** 7 tables — `tracks`, `albums`, `library_files`, `download_attempts`,
`watchlist_tracks`, `playlist_snapshots`, `playlist_snapshot_tracks`.

- `GET /api/v1/admin/db/tables` → liste + nombre de lignes.
- `GET /api/v1/admin/db/{table}?limit&offset&order&filtre` → lecture paginée, **liste blanche de
  tables et de colonnes** (pas de nom de table venant du client dans le SQL).
- Réutilise `v1RequireAdmin`, déjà en place.

**Décision tranchée le 2026-07-19 : égalité + recherche texte.** Plages écartées (elles ne servaient
vraiment que sur `download_attempts.started_at`, que `order`+`dir`+pagination couvrent déjà).

> ⚠️ **La question était mal posée dans ce plan.** J'écrivais « plus le filtre est riche, plus la
> surface d'injection est large ». **Faux dans cette conception.** Ce serait vrai en construisant le
> `WHERE` par concaténation. Avec des valeurs liées et des identifiants validés contre le schéma,
> ajouter `LIKE` n'ajoute **aucune** surface d'injection : même mécanique, un opérateur de plus. Le
> vrai arbitrage était donc l'utilité contre le code à écrire et tester, pas la sécurité.

**Vérifié le 2026-07-19 — mieux placé que prévu :**
- 7 tables confirmées, et **36 fonctions d'accès existent déjà** (`tracks.go` 5, `albums.go` 3,
  `library_files.go` 7, `download_attempts.go` 11, `snapshots.go` 5, `watchlists.go` 5).
- Le handle est directement atteignable : `Container.Catalog *sql.DB` ([container.go:13](../container.go)).
- **Aucun secret dans le catalogue.** Colonnes relevées : métadonnées musicales + `user_id`,
  `downloaded_by`, `file_path`, `error`. Ni token, ni identifiant, ni mot de passe. La préoccupation de
  redaction du §3.3 vise **BoltDB**, pas SQLite — elle ne bloque donc pas cette phase.
- Reste sensible à la vie privée sur une instance multi-utilisateur : `user_id` / `downloaded_by`
  disent qui a téléchargé quoi. Endpoint admin, donc cohérent.

#### ✅ Phase 2 — faite (2026-07-19), reste à vérifier en prod

`api_catalog.go` + `api_catalog_test.go`. `GET /admin/db/tables` et `GET /admin/db/{table}`
(`limit`/`offset`/`order`/`dir`/`q` + égalité sur toute colonne), sous `v1RequireAdmin`.

**Deux corrections au périmètre annoncé ici :**
- **8 tables, pas 7** : `schema_migrations` existe aussi (créée par `migrate.go`, hors migrations).
  Elle est exposée — elle ne contient que la liste des migrations appliquées, ce qu'on veut
  justement pouvoir consulter quand un déploiement se comporte bizarrement.
- La liste blanche n'est **pas écrite à la main** mais lue du schéma vivant. La migration 0005 avait
  déjà ajouté 5 colonnes à `tracks` : une liste manuelle aurait dérivé dès celle-là, et une colonne
  oubliée devient *invisible sans erreur*.

**Choix de conception à retenir :** un paramètre inconnu est un **400, pas un silence**. Ignorer
`?statuz=failed` renverrait toutes les lignes en ayant l'air de réussir — le pire résultat possible
pour un outil d'enquête.

**Deux pièges attrapés en testant plutôt qu'en supposant :**
1. `ORDER BY "rowid"` — SQLite résout un identifiant entre guillemets qui ne correspond à aucune
   colonne comme un **littéral chaîne** au lieu d'échouer. Tout aurait alors été trié par une
   constante, et la pagination aurait répété et sauté des lignes. Vérifié en demandant `DESC` et en
   contrôlant que l'ordre s'inverse réellement — un simple test de non-chevauchement des pages
   n'aurait rien vu, l'ordre de scan de SQLite restant stable sous un tri constant.
2. `/admin/db/tables` et `/admin/db/{table}` matchent la même URL. Le littéral l'emporte bien, mais
   si la règle changeait, `tables` serait lu comme un nom de table et renverrait 404. Test posé.

`q` échappe `%` et `_` : chercher `100%` doit trouver un album littéralement nommé ainsi, pas toute
la table.

#### ✅ Vérifié en prod (2026-07-19)

Listing des 8 tables OK. `q=yadnus` → 1 piste. `artist_name=!!!` → 2. `status=failed` trié
`started_at desc` → 260 échecs. Refus corrects : colonne inconnue 400, table inconnue 404, `dir`
invalide 400, `limit=0` 400, `limit=99999` plafonné à 500, sans clé 401, clé invalide 401. Les
tentatives d'injection sur le nom de table et de colonne repartent en 404/400 avec le nom hostile
cité tel quel dans le message — donc jamais exécuté ; `tracks` a toujours ses 2619 lignes après.
`q=100%` renvoie **1** ligne sur 2619, l'album `100% YES` : l'échappement tient sur données réelles.

**🐛 Trouvé sur mon propre endpoint en le testant (corrigé) :** `?album_id=` renvoyait « 0 lignes »
alors que **les 2619** pistes ont un `album_id` vide — elles sont `NULL`, et `NULL = ''` n'est jamais
vrai en SQL. Correct au sens SQL, **trompeur** en pratique : on conclut l'inverse de la réalité.
Une valeur de filtre vide couvre désormais `IS NULL OR = ''`. Même principe que le refus des
paramètres inconnus : un outil d'enquête ne doit jamais répondre « rien » de façon plausible et fausse.

**🔍 Observation (hors périmètre, non corrigée) :** la table `albums` est **vide** (0 ligne pour 2619
pistes) et `tracks.album_id` est `NULL` partout. Cause : `UpsertAlbum` n'a **aucun appelant** —
`jobs_catalog.go:14` porte le commentaire « *A later commit will plumb the album ID through and
enable UpsertAlbum* », jamais fait. Ce n'est pas une régression, c'est une fonctionnalité restée
inachevée. À traiter séparément.

### Phase 3 — console SQL en lecture · *moyen, 2 décisions*

- `POST /api/v1/admin/db/query` avec `SELECT` **uniquement**, refus explicite de tout autre verbe,
  `LIMIT` imposé, délai d'exécution borné.
- Réponse en colonnes/lignes génériques.

**Décisions requises :**
1. **Écriture manuelle : SQL arbitraire ou actions fermées ?** Une console acceptant `UPDATE`/`DELETE`
   sur le réseau peut corrompre des données sans confirmation possible. L'alternative — une liste
   fermée d'actions admin — est plus sûre mais moins souple. **Non tranché.**
2. **Redaction des secrets** : localiser le token Tidal personnel et les configs proxy dans BoltDB,
   et poser l'exclusion par défaut **comme principe** avant d'exposer le moindre outil générique.

**Vérifié le 2026-07-19 — une option que le plan n'avait pas envisagée :** la base est ouverte en
lecture-écriture (`sql.Open("sqlite", dsn)`, [backend/db/db.go:46](../backend/db/db.go)). Ouvrir une
**seconde connexion en `mode=ro`** rendrait le « SELECT seul » **structurel** plutôt que dépendant
d'une analyse de la requête pour en refuser les verbes. C'est une garantie bien plus solide qu'un
filtrage de chaîne, et ça désamorce largement la décision « SQL arbitraire ou actions fermées ».

### Phase 4 — audit des ~70 endpoints · *gros, à faire en dernier*

> ⚠️ **Deuxième correction, dans l'autre sens (2026-07-19).** J'avais d'abord dit « le niveau `admin`
> est mort » (faux), puis « la phase 4 est de la cohérence, pas de la sécurité » — **cette seconde
> affirmation était trop large**. Le *mécanisme* admin est sain ; le problème est ailleurs : des routes
> **sans aucun contrôle de niveau**.
>
> **Audit des 72 routes v1** (délégations aux handlers nommés résolues) : 24 `read`, 21 `manage`,
> 8 `admin` inline, 4 `admin` dans la fonction — et un reste **sans contrôle**, dont :
>
> | Route | Ce qu'elle fait | Portée |
> |---|---|---|
> | `DELETE /auth/tidal` | `tidal.DeleteTidalToken()` — **sans argument utilisateur** | le token est un **singleton de processus** → déconnecte Tidal pour **toute l'instance** |
> | `POST /auth/tidal/device/start` + `/poll` | `PollTidalDeviceAuth` lie le compte globalement | **remplacer** le compte Tidal de l'instance |
> | `DELETE /auth/keys/{id}` | révoque une clé | limité au compte de l'appelant, mais faisable **avec une clé `read`** |
>
> Toutes exigent `v1Auth` (authentifié) mais **aucun niveau**. Conséquence concrète : une clé API
> `read` — la moins privilégiée — peut couper Tidal, c'est-à-dire le **seul provider qui fonctionne**
> (mesuré le 07-19). Recouvrable par ré-authentification device-code, mais c'est un déni de service
> à un clic.
>
> **✅ MESURÉ EN PROD le 2026-07-19, avec contrôles négatifs.** L'accessibilité par une clé `read`
> n'était qu'une déduction ; elle est maintenant établie. Clé API temporaire créée avec la seule
> permission `read` (en-tête `X-API-Key`, pas `Authorization`), puis révoquée et vérifiée inopérante.
>
> | Appel | Résultat | Ce que ça prouve |
> |---|---|---|
> | `GET /settings` (`read`) | **200** | la clé fonctionne |
> | `POST /downloads/track` (`manage`) | **403** | le cloisonnement fonctionne |
> | `GET /admin/logs` (`admin`) | **403** | le cloisonnement fonctionne |
> | `GET /auth/tidal/status` | **200** | atteignable **sans niveau** |
> | `GET /auth/keys` | **200** | atteignable **sans niveau** |
>
> Les deux 403 sont l'essentiel : ils démontrent que la clé est réellement limitée, donc que les 200
> ne viennent pas d'un cloisonnement globalement cassé mais bien de **l'absence de contrôle sur ces
> routes précises**.
>
> `DELETE /auth/tidal` n'a **pas** été appelé — inutile de déconnecter une instance pour prouver un
> point. Il porte le même `v1Auth` sans contrôle de niveau (handler lu ligne à ligne), donc il est
> atteignable par construction.
>
> **Ça remonte la phase 4 dans la file** : ce n'est plus seulement du rangement.

Le travail est :

1. Recenser chaque endpoint et le niveau qu'il exige aujourd'hui.
2. Repérer les incohérences réelles — ex. `GET /files/metadata` est `v1RequireAdmin` alors que lire
   les tags d'un fichier ressemble à du `read`.
3. Trancher : garder deux mécanismes (`v1RequirePermission` + `v1RequireAdmin`) ou tout unifier sur
   `v1RequirePermission(…, "admin")`. Deux mécanismes pour trois niveaux est ce qui m'a fait conclure
   à tort que `admin` était mort — le prochain lecteur trébuchera pareil.
4. Corriger, en assumant les ruptures de compatibilité pour les clés existantes.

### Ordre recommandé

**1 → 2 → 3 → 4.** La phase 1 est indépendante et livrable seule. Les phases 2 et 3 se tiennent
(la 3 sans la 2 laisse sans repères sur ce qu'on peut interroger). La 4 en dernier : c'est la plus
lourde, et faire les 3 premières donne une vision concrète des incohérences à corriger.

## 4. Prochaine étape

Reprendre ce document au moment de démarrer le chantier : confirmer §3, puis exécuter les phases dans
l'ordre (1 → 2 → 3 → 4, la 4 pouvant être découplée dans le temps si jugée trop grosse pour être
groupée avec le reste).
