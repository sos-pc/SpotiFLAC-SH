# Sélection de service & fallback — cartographie de l'AVANT-refonte

> **🕰️ DOCUMENT HISTORIQUE — décrit l'état du code AVANT la refonte (relevé le 2026-07-17).**
> **Ne pas le lire comme l'état actuel.** Ce qu'il décrit a depuis été corrigé :
> l'override `jobs_helpers.go:263` est **supprimé** (refonte phase 1) et la boucle de fallback du
> frontend est **supprimée** (phase 2a). Pour l'état courant et ce qui reste :
> [override-rework-plan.md](override-rework-plan.md).
>
> Il reste utile pour **comprendre pourquoi** la refonte était nécessaire : il documente les 4 couches
> de décision, l'universalité de l'override (prouvée en prod), les pièges de nommage
> (`allowFallback` = repli de *qualité*), et la dérive des défauts `autoOrder`. Relevé par lecture
> directe du code, pas déduit. Index : [README.md](README.md).

## 0. Le constat en une phrase

**Quatre couches décident indépendamment du service de téléchargement**, avec des sémantiques qui se
recouvrent mal. Toutes les entrées (interactif, batch, watchlist) convergent vers **un seul worker**,
et l'override signalé (`jobs_helpers.go:263`) s'y interpose **universellement** — il n'est pas
cantonné au batch comme on pouvait le croire (prouvé en prod, voir §1). C'est le plus visible des
quatre parce qu'il *écrase* un choix explicite, mais pas le seul endroit à revoir.

## 1. Les quatre points de décision

| # | Où | Rôle | Honore le choix explicite ? |
|---|----|------|------------------------------|
| A | **Frontend** [`lib/downloadFallback.ts`](../frontend/src/lib/downloadFallback.ts) | Téléchargement **interactif** (bouton). Lit `settings.downloader` ; si `auto`, itère la chaîne `autoOrder` **côté client** en appelant `/downloads/track` une fois par service. | intention seulement — voir ci-dessous |
| B | **Backend** [`backend/downloader.go:433`](../backend/downloader.go) | Dispatch réel. `switch req.Service` ; pour `auto`, itère `AutoOrder` **côté serveur**. | oui, **mais reçoit un `service` déjà réécrit par C** |
| C | **Backend, worker unique** [`jobs_helpers.go:236`](../jobs_helpers.go) `buildDownloadRequest` | **Tout job** (single, batch, watchlist). Pré-résout des URLs (S9/songlink) **puis réécrit `service`** en `tidal`/`qobuz` selon ce qu'il trouve. | **non — c'est l'override** |
| D | **Backend** [`download_service.go:41`](../download_service.go) `ApplySettingsFallbacks` | Remplit les réglages vides (`AutoOrder`, `AllowFallback`…) depuis les défauts utilisateur. | n/a (ne choisit pas le service) |

### Le fait structurant : l'override est UNIVERSEL (corrigé le 2026-07-17)

> ⚠️ **Une version antérieure de cette carte affirmait que les deux chemins « divergent » — que le
> téléchargement interactif contournait l'override. C'est FAUX, falsifié par observation en prod.**
> `DownloadService.DownloadTrack` ne télécharge pas en synchrone : il **crée un job et le pousse dans
> `jm.queue`** ([download_service.go:180-184](../download_service.go)). Donc **toutes** les entrées
> convergent vers le **worker unique** → `buildDownloadRequest` → override C.

Les entrées, toutes vers le même worker :

- **Interactif** (`POST /downloads/track`, [api_jobs.go:163](../api_jobs.go)) — y compris chaque appel
  par-service de la couche A du frontend → **enqueue** → worker → **C**.
- **Batch** (`POST /jobs`) → **enqueue** → worker → **C**.
- **Watchlist** (sync) → `EnqueueBatch` → worker → **C**.

Il n'existe **aucun** chemin de téléchargement qui échappe à l'override côté serveur.

**Preuve empirique (2026-07-17, prod).** Un `POST /downloads/track` avec `service:"qobuz"` sur une
piste fraîche → **`[Jobs] Done track=She`**. Or notre `searchByISRC` renvoie 401 (re-vérifié le même
jour) et `DownloadTrackWithISRC` **retourne immédiatement sur cette erreur**
([qobuz/client.go:540](../backend/qobuz/client.go)) : un job réellement « qobuz » **ne peut pas**
produire un `Done`. Donc le service a été réécrit — l'override a fait basculer vers Tidal, sur le
chemin « interactif » censé le respecter.

**Conséquence — et reformulation de S6 :** pour toute piste que Tidal possède (la majorité),
l'override diverge vers Tidal *avant* que Qobuz ne soit appelé. Le `searchByISRC` de Qobuz est donc
**quasi inatteignable en prod** ; le « Qobuz 401 » qui motivait S6 n'est **pas** un mode d'échec
courant du chemin normal — il est masqué par la bascule Tidal. Porter `qobuz_api.go` (S6) reste juste,
mais son **impact prod est verrouillé par l'override** : Qobuz n'est atteint que quand Tidal échoue
*et* qu'un ISRC existe (mode `auto`, [jobs_helpers.go:285](../jobs_helpers.go)). **La refonte de
l'override est donc le préalable ET le levier le plus fort — devant le portage lui-même.**

La couche frontend A et l'override C se **contredisent** au passage : A demande un service précis, le
serveur en fait un autre en silence, et A croit que son choix a été honoré (le job « réussit »).

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
3. ~~**Le réglage de chaîne de fallback.**~~ **TRANCHÉ le 2026-07-17 : la chaîne est CONSERVÉE et
   honorée.** Modèle retenu = celui déjà affiché par l'UI (`auto` + chaîne paramétrable ; service
   explicite = forcer). Le backend doit s'aligner dessus. *(Corrige une intention antérieure « ne plus
   la prendre en compte ».)* Plan d'implémentation : [override-rework-plan.md](override-rework-plan.md).
4. **La couche frontend A garde-t-elle un rôle ?** Puisque tout est réécrit côté serveur (C), la boucle
   `auto` client-side de `downloadFallback.ts` est au mieux redondante, au pire trompeuse (elle croit
   choisir un service que le serveur remplace). La refonte doit décider : fallback autoritatif côté
   serveur et A réduit à « envoyer un service ou `auto` », ou l'inverse. Ce n'est **pas** une question
   de cohérence entre deux chemins (il n'y en a qu'un) mais de qui, du client ou du serveur, détient la
   logique.

## 7. Comment cette carte a été établie — et une correction

Lecture directe des fichiers cités, graphe d'appel vérifié. **Une première version de cette carte
affirmait que `/downloads/track` téléchargeait en synchrone et contournait l'override** — déduit du
fait que `buildDownloadRequest` n'a qu'un appelant (`jobs_worker.go:126`), sans vérifier que
`DownloadService.DownloadTrack` y menait. **Faux** : il *enqueue* un job (`download_service.go:180`),
donc il passe par le worker comme tout le reste. Falsifié en prod par un `service:"qobuz"` qui a rendu
`Done` (impossible si Qobuz avait vraiment tourné). Même leçon que le reste du projet : *le graphe
d'appel partiel ne suffit pas — il faut suivre la donnée jusqu'au bout, ou l'observer.* Rien n'a été
exécuté en écriture ni modifié dans le code : c'est une carte, corrigée par la mesure.
