# Sélection de service & fallback — cartographie de l'existant

> **🔍 Carte — 2026-07-17.** Relevé par lecture directe du code (pas déduit), en préparation de la
> refonte de l'override de service découvert en validant S6 (voir
> [upstream-catchup.md §S6](upstream-catchup.md)). **Ne décide rien** — décrit *comment ça marche
> aujourd'hui*, où sont les points de décision, et ce qu'une refonte toucherait. Les questions de
> design ouvertes sont en §6, non tranchées. Index : [README.md](README.md).

## 0. Le constat en une phrase

**Quatre couches décident indépendamment du service de téléchargement**, avec des sémantiques qui se
recouvrent mal et deux chemins d'entrée qui se comportent différemment. L'override signalé
(`jobs_helpers.go:263`) n'est qu'un des quatre — le plus visible parce qu'il *écrase* un choix
explicite, mais pas le seul endroit à revoir.

## 1. Les quatre points de décision

| # | Où | Rôle | Honore le choix explicite ? |
|---|----|------|------------------------------|
| A | **Frontend** [`lib/downloadFallback.ts`](../frontend/src/lib/downloadFallback.ts) | Téléchargement **interactif** (bouton). Lit `settings.downloader` ; si `auto`, itère la chaîne `autoOrder` **côté client** en appelant `/downloads/track` une fois par service. | oui |
| B | **Backend** [`backend/downloader.go:433`](../backend/downloader.go) | Dispatch réel. `switch req.Service` ; pour `auto`, itère `AutoOrder` **côté serveur**. | oui |
| C | **Backend jobs** [`jobs_helpers.go:236`](../jobs_helpers.go) `buildDownloadRequest` | Chemin **batch / watchlist**. Pré-résout des URLs (S9/songlink) **puis réécrit `service`** en `tidal`/`qobuz` selon ce qu'il trouve. | **non — c'est l'override** |
| D | **Backend** [`download_service.go:41`](../download_service.go) `ApplySettingsFallbacks` | Remplit les réglages vides (`AutoOrder`, `AllowFallback`…) depuis les défauts utilisateur. | n/a (ne choisit pas le service) |

### Le fait structurant : deux chemins d'entrée divergent

- **Interactif** (`POST /api/v1/downloads/track`, [api_jobs.go:163](../api_jobs.go)) : passe par **A → B**.
  **Pas** de `buildDownloadRequest`. Le service explicite est respecté.
- **Batch / watchlist** (`POST /api/v1/jobs` → worker, [jobs_worker.go:126](../jobs_worker.go)) : passe
  par **C → B**. `buildDownloadRequest` s'interpose et **peut réécrire le service**.

**Conséquence concrète :** un utilisateur qui choisit « qobuz » comme Source voit ses téléchargements
manuels partir sur Qobuz, mais ses **synchronisations de watchlist réécrites en Tidal** en silence.
Même réglage, deux comportements. C'est *exactement* ce qui a rendu S6 invalidable : impossible
d'observer un vrai chemin Qobuz via un job.

## 2. L'override, précisément (point C)

Dans `buildDownloadRequest`, trois réécritures :

```go
// jobs_helpers.go — après avoir trouvé une URL/ID Tidal via ISRC ou recherche nom :
if service != "tidal" && service != "auto" {
    service = "tidal"                                  // :263 et :281
    audioFormat = firstNonEmpty(s.TidalQuality, "LOSSLESS")
}
// ... et si la recherche Tidal échoue mais qu'un ISRC existe :
} else if … && service != "qobuz" {
    service = "qobuz"                                  // :285-288
}
```

Deux propriétés à retenir pour la refonte :

- **L'override n'est PAS conditionné par `allowFallback`.** Il réécrit le service inconditionnellement.
  Notre lecture commune (« `allowFallback:false` aurait dû l'empêcher ») était fausse — voir §3.
- **Il court-circuite le dispatch propre de B.** `backend/downloader.go` sait pourtant honorer un
  service explicite (`case "qobuz": …`) **et** faire du fallback `auto`/`AutoOrder`. L'override décide
  avant que ce switch ne voie quoi que ce soit.

## 3. Pièges de nommage (à clarifier dans la refonte)

| Terme | Ce que le nom suggère | Ce que c'est **réellement** |
|-------|------------------------|------------------------------|
| `allowFallback` | « essayer d'autres services » | **repli de qualité** (24→16 bit). UI : *« Allow Quality Fallback (16-bit) »* ([GeneralTab.tsx:548](../frontend/src/components/settings/GeneralTab.tsx)). Orthogonal au choix de service. |
| `settings.downloader` (front) vs `service` (API) | — | **même chose, deux noms.** Le front stocke `downloader`, l'envoie comme `service` ([useDownload.ts:98](../frontend/src/hooks/useDownload.ts)). |
| `autoOrder` | l'ordre de la chaîne `auto` | correct — mais **lu à deux endroits** (front `downloadFallback.ts:168`, back `downloader.go:443`) et avec **trois défauts différents** (voir §4). |

## 4. Dérive des valeurs par défaut de `autoOrder`

Trois défauts coexistent quand l'utilisateur n'a rien réglé :

| Endroit | Défaut |
|---------|--------|
| UI (placeholder affiché) [GeneralTab.tsx:224](../frontend/src/components/settings/GeneralTab.tsx) | `tidal-qobuz-amazon` |
| Exécution front [downloadFallback.ts:168](../frontend/src/lib/downloadFallback.ts) | `tidal-amazon-qobuz` |
| Exécution back [downloader.go:445](../backend/downloader.go) | `tidal-amazon-qobuz` |

Donc l'ordre **affiché** à l'utilisateur (qobuz avant amazon) n'est pas l'ordre **exécuté** (amazon
avant qobuz) tant qu'il ne touche pas le sélecteur. Bug mineur, mais à corriger dans le même geste.

## 5. Rayon d'action d'une refonte

Ce qu'il faudra toucher, minimum, pour rendre le comportement cohérent :

- **Backend** : `jobs_helpers.go` (l'override lui-même + la pré-résolution d'URL), `download_service.go`
  (`ApplySettingsFallbacks`), `backend/downloader.go` (le dispatch — potentiellement la source unique de
  vérité si on y centralise). Aligner les défauts `AutoOrder`.
- **Frontend** : `downloadFallback.ts` (la 4ᵉ couche — décider si le fallback reste côté client, côté
  serveur, ou les deux), `useDownload.ts`, et l'UI `GeneralTab.tsx` (sélecteur Source + chaîne + le
  libellé quality-fallback).
- **Contrat API** : le sens de `service`/`auto`, et si `autoOrder` reste un réglage exposé.

## 6. Questions de design — OUVERTES, non tranchées ici

1. **Faut-il une seule couche de fallback ?** Aujourd'hui A (front) et B (back) implémentent tous deux
   la boucle `auto`. Choix : fallback autoritatif **côté serveur** (le front n'envoie qu'un service ou
   `auto`), ou l'inverse. Impacte directement quel code disparaît.
2. **Que devient l'override C ?** Le but légitime derrière (« si le service demandé n'a pas la piste,
   en prendre une autre plutôt qu'échouer ») est le rôle de `auto`. Piste : supprimer la réécriture et
   laisser B décider — mais alors un job `service:"qobuz"` qui échoue **échoue** au lieu de basculer,
   sauf si l'utilisateur a choisi `auto`. C'est un changement de comportement observable.
3. **Le réglage de chaîne de fallback.** L'utilisateur a indiqué qu'il **ne devrait plus être pris en
   compte** dans la refonte — à préciser : retiré de l'UI ? ignoré à l'exécution ? remplacé par un
   ordre fixe ? Décision produit, à acter avant de coder.
4. **Cohérence des deux chemins.** Interactif et batch doivent-ils suivre exactement la même logique de
   sélection ? (recommandation implicite : oui, mais c'est le cœur de la décision.)

## 7. Comment cette carte a été établie

Lecture directe des fichiers cités, graphe d'appel vérifié (`buildDownloadRequest` n'a qu'un appelant :
`jobs_worker.go:126` ; `DownloadTrack` a deux entrées : batch via `EnqueueBatch`, direct via
`api_jobs.go:163`). Aucune supposition sur le nom d'un fichier ou d'un réglage — chaque affirmation
pointe une ligne. Rien n'a été exécuté ni modifié : c'est une carte, pas un patch.
