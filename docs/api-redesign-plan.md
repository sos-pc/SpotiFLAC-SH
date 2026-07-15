# Plan — cohérence de l'API (read / manage / admin) + accès DB

> **Statut : documenté, pas commencé.** Ce chantier a été identifié le 2026-07-15 pendant le suivi
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

## 4. Prochaine étape

Reprendre ce document au moment de démarrer le chantier : confirmer §3, puis exécuter les phases dans
l'ordre (1 → 2 → 3 → 4, la 4 pouvant être découplée dans le temps si jugée trop grosse pour être
groupée avec le reste).
