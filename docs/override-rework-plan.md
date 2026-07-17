# Refonte de la sélection de service — plan d'implémentation

> **🧭 Plan — 2026-07-17. Décisions produit + sous-questions §7 TRANCHÉES ; phase 1 prête à coder.**
> Modèle aligné sur l'UI (§1). Ce document dit *ce qu'on veut*, *l'écart avec l'existant* (trois défauts
> trouvés en creusant, au-delà de l'override), *tout ce que ça touche*, et les décisions actées.
> **Point clé confirmé : la chaîne de fallback existe déjà (`ExecuteDownload`) — on la réutilise, on ne
> la recode pas** (§3). À lire avec la carte de l'existant :
> [service-selection-map.md](service-selection-map.md). Index : [README.md](README.md).

## 1. La décision

**Le backend doit se comporter comme l'UI le montre déjà** :

- **`auto`** = le serveur essaie les services dans l'ordre de la **chaîne paramétrable** (`autoOrder`,
  le menu déroulant existant). La chaîne est **conservée et honorée** — ce n'est plus un réglage
  ignoré.
- **Service explicite** (`tidal`/`qobuz`/`amazon`/`deezer`) = **forcer** ce service. Il télécharge, ou
  il **échoue** — plus de bascule muette vers un autre service.

> ⚠️ **Ceci supersède** une intention antérieure (« le réglage de chaîne ne doit plus être pris en
> compte »). Après clarification : la chaîne **reste**, elle doit juste **marcher vraiment**. La carte
> §6 Q3 est corrigée en conséquence.

## 2. Comportement cible, précis

| `service` reçu | Comportement attendu |
|----------------|----------------------|
| `auto` | Résoudre les URLs/ISRC nécessaires, puis essayer chaque service de `autoOrder` dans l'ordre, s'arrêter au premier **vrai** succès de téléchargement. Échec seulement si **tous** échouent. |
| `tidal` | Tidal uniquement. Résout l'URL Tidal (directe ou par ISRC/nom). Échoue si Tidal ne l'a pas. **Aucune** bascule. |
| `qobuz` | Qobuz uniquement (→ `searchByISRC`, aujourd'hui 401 tant que S6 non porté). Échoue proprement. |
| `amazon` | Amazon uniquement, via son `amazon_url`. |
| `deezer` | Deezer uniquement, via SpotifyID. |

**Invariant clé :** un service explicite n'est **jamais** réécrit. La résolution d'URL/ISRC (S9) reste,
mais elle ne **décide** plus du service — elle ne fait que fournir l'URL au service déjà choisi.

## 3. L'écart avec l'existant — 4 problèmes, pas 1

L'override est le plus visible, mais l'investigation en a trouvé trois autres qui font que
l'« auto + chaîne » ne fonctionne quasiment pas aujourd'hui. **Tous vérifiés par lecture directe.**

### 3.1 L'override écrase le choix explicite (le connu)

[`jobs_helpers.go:263/281/285`](../jobs_helpers.go) réécrit `service` en `tidal`/`qobuz` selon ce que
la résolution trouve. Prouvé en prod : un `service:"qobuz"` rend `Done` (donc parti sur Tidal).

### 3.2 L'`auto` mono-piste est cassé par l'asynchrone

La boucle de fallback **côté frontend** ([`downloadFallback.ts:178`](../frontend/src/lib/downloadFallback.ts))
itère les services et teste `response.success` pour décider de continuer. **Mais `/downloads/track`
renvoie `success:true` dès l'*enqueue***, pas après le téléchargement
([`download_service.go:190`](../download_service.go)). Donc la boucle **s'arrête au premier service dont
l'URL est résolue** (Tidal en général), l'empile, croit avoir réussi, et **ne teste jamais les
suivants**. Le vrai téléchargement tourne ensuite en job mono-service **sans aucun fallback backend**.
→ Pour une piste seule, la « chaîne » au-delà du premier maillon est **du code mort**.

### 3.3 `serviceURL` est un champ unique biaisé Tidal

Dans la boucle `auto` **backend**, `ensureTidalServiceURL` met une URL **Tidal** dans `req.ServiceURL`
([downloader.go:376](../backend/downloader.go)). Or `amazonParams.URL = req.ServiceURL`
([downloader.go:316](../backend/downloader.go)) : si la boucle atteint Amazon, elle lui passe une **URL
Tidal**. → `auto`→amazon est cassé. Il faut une URL **par service**, pas un champ unique.

### 3.4 L'override collapse `auto`→`qobuz`

Toujours en `auto` : si la recherche Tidal par nom échoue et qu'un ISRC existe,
[jobs_helpers.go:285](../jobs_helpers.go) force `service="qobuz"` — **le reste de la chaîne (amazon) est
sauté**. La chaîne n'est donc pas respectée même côté backend.

**Conclusion :** implémenter la cible n'est pas « retirer l'override », mais ce n'est pas non plus
« coder une chaîne de fallback ».

> ✅ **La chaîne de fallback EXISTE déjà** (signalé par l'utilisateur, vérifié le 2026-07-17) :
> `backend.ExecuteDownload` ([downloader.go:441](../backend/downloader.go)) lit `AutoOrder`, itère les
> services dans l'ordre, s'arrête au premier succès, passe au suivant sur erreur. Le worker l'appelle
> ([jobs_worker.go:148](../jobs_worker.go)). **On ne la recode pas — on la réutilise.**
>
> Il y a même **deux** boucles auto aujourd'hui : celle-ci (backend) *et* le doublon frontend
> `downloadFallback.ts` (cassé par l'async, 3.2). La refonte **supprime le doublon frontend** et
> **garde** la boucle backend. Bilan : **moins de composants**, pas plus.

Ce qui reste à faire n'est donc que : (a) **retirer** ce qui empêche la boucle existante de tourner
(l'override, 3.1/3.4) ; (b) un **petit plombage** pour qu'elle atteigne Amazon (3.3, ci-dessous) ;
(c) **supprimer** le doublon frontend (3.2). On enlève plus qu'on ajoute.

## 4. Ce que ça touche

### Backend

| Fichier | Changement |
|---------|-----------|
| [`jobs_helpers.go`](../jobs_helpers.go) `buildDownloadRequest` | **Retirer les réécritures de `service`** (3.1, 3.4) — c'est une **suppression**. Et **cesser de jeter `streamingURLs["amazon_url"]`** : le porter dans la requête (voir la ligne `DownloadRequest`). |
| [`backend/downloader.go`](../backend/downloader.go) `ExecuteDownload` | **Inchangé sur la boucle** — c'est déjà l'autorité de dispatch + fallback, on la garde telle quelle. Seul ajustement : faire lire à Amazon *son* URL (via le nouveau champ) au lieu du `ServiceURL` Tidal. Aligner le défaut `AutoOrder`. |
| [`download_service.go`](../download_service.go) `ApplySettingsFallbacks` | Vérifier que `AutoOrder`/défauts sont cohérents avec la nouvelle logique. |
| `DownloadRequest` (champ `ServiceURL`) | **Petit plombage, pas d'archi.** Aujourd'hui un seul champ, mis à l'URL Tidal pour `auto` → Amazon reçoit une URL Tidal. Ajouter de quoi transporter l'URL Amazon (un champ `AmazonURL`, ou passer les 2 URLs à `runService`). La donnée existe déjà côté `buildDownloadRequest`, elle est juste jetée. |

### Frontend

| Fichier | Changement |
|---------|-----------|
| [`downloadFallback.ts`](../frontend/src/lib/downloadFallback.ts) | La boucle client-side devient **inutile et trompeuse** (3.2). Cible : le front envoie **`auto`** (ou un service explicite) et laisse le backend itérer. → cette fonction se réduit fortement, voire disparaît. |
| [`useDownload.ts`](../frontend/src/hooks/useDownload.ts) | Le download mono-piste (ligne 263) passe par `downloadWithAutoFallback` ; à recâbler sur un simple envoi `service` + suivi via la queue/SSE. |
| [`GeneralTab.tsx`](../frontend/src/components/settings/GeneralTab.tsx) | Le sélecteur Source + la chaîne restent (c'est le modèle voulu). Corriger le **défaut affiché** (`tidal-qobuz-amazon`) pour qu'il **corresponde** au défaut exécuté (3 défauts divergents, carte §4). Revoir le libellé `allowFallback` (« quality », pas service — pas un blocage, mais source de confusion). |

### Contrat / réglages

- Unifier le défaut `AutoOrder` (une seule valeur, partagée front/back).
- Clarifier que `service=auto` est la **seule** valeur qui déclenche la chaîne.

## 5. Changements de comportement visibles (à assumer)

- **Un service explicite qui n'a pas la piste échoue** au lieu de « trouver quand même ». C'est **le
  but**, mais c'est un changement observable pour qui avait pris l'habitude inverse. À communiquer.
- **`auto` mono-piste va réellement basculer** entre services (aujourd'hui il ne le fait pas). Donc des
  pistes qui « échouaient » silencieusement pourraient se mettre à réussir via un autre service — et
  inversement, le service final peut changer.
- **Qobuz explicite remontera son vrai 401** tant que S6 n'est pas porté (c'est correct : ça valide S6,
  et l'erreur est maintenant attribuable — `qobuz: unsigned ISRC search returned status …`).

## 6. Surface de test

- Unitaire : `buildDownloadRequest` ne réécrit **jamais** un service explicite ; en `auto`, produit
  bien une URL **par service** attendu.
- Unitaire : boucle `auto` backend s'arrête au premier vrai succès, tente le suivant sur échec réel.
- Intégration prod (comme S6) : `service:"qobuz"` explicite → doit atteindre `searchByISRC` (observer
  l'erreur attribuée) ; `auto` → doit tenter la chaîne dans l'ordre, observable dans les logs.
- Non-régression : batch/watchlist (le chemin `EnqueueBatch`) suit la même logique que le mono-piste.

## 7. Sous-questions — TRANCHÉES le 2026-07-17

1. **Boucle de fallback : backend seul.** ✅ La boucle client `downloadFallback.ts` est supprimée ; le
   front envoie un service ou `auto`, `ExecuteDownload` (existant) itère.
2. **Suivi de progression : unifié via SSE.** ✅ Le mono-piste suit le job via le flux SSE comme le
   batch (répare au passage le « affiché terminé dès l'enqueue »).
3. ~~**URL par service : map ou paresseuse ?**~~ **Question mal posée — retirée.** L'utilisateur a
   pointé (à raison) que la boucle existe déjà (`ExecuteDownload`) et qu'il ne faut pas la recoder.
   Ce n'est pas un choix d'archi mais un **plombage** : porter l'`amazon_url` déjà récupérée jusqu'à la
   boucle (§3.3, §4). Défaut retenu : un champ dans `DownloadRequest`, pas de map ni de résolution
   paresseuse.
4. **Deezer : réactivé.** ✅ Remettre Deezer comme Source sélectionnable (retirer `disabled` /
   « unavailable » dans [GeneralTab.tsx:212](../frontend/src/components/settings/GeneralTab.tsx)) et le
   garder dans la chaîne `auto`. Son fonctionnement réel sera traité **plus tard** — décision produit
   assumée : l'UI et la chaîne redeviennent cohérentes, le cas « est-ce que Deezer marche » est un
   sujet distinct.

## 8. Ordre d'implémentation suggéré (phasé, révisable)

1. **Backend d'abord, sans toucher au front** : retirer les réécritures de service (3.1/3.4), faire
   marcher la boucle `auto` avec URL par service (3.3). Testable en prod via `/downloads/track` avec
   `service` explicite et `auto`. À ce stade, la boucle front (3.2) devient redondante mais inoffensive.
   — ✅ **codé + VÉRIFIÉ EN PROD le 2026-07-17** (`658c814`). Preuve par exécution réelle sur 3 cas :
   `service:"qobuz"` → `Failed: qobuz: unsigned ISRC search returned status 401` (avant phase 1 :
   `Done` via l'override Tidal) → **l'override est parti ET S6 est validé** (l'échec est bien dans
   `searchByISRC`) ; `service:"amazon"` → tente Amazon avec son propre ID (`Failed: all Amazon proxies
   failed`, proxy tiers mort — routage correct, pas de bascule Tidal) ; `auto` → `Done` via Tidal
   (1er de la chaîne). Chaque service explicite est honoré, plus aucune réécriture.
2. **Frontend ensuite**, en deux temps :
   - **2a — ✅ codé + VÉRIFIÉ NAVIGATEUR le 2026-07-17 (`24a9c75`, `7d8eae4`).** `downloadFallback.ts`
     ne fait plus de boucle client : il garde son pré-traitement et finit sur **un seul** `downloadTrack`
     portant `settings.downloader` (y compris `auto`) — sélection/fallback côté serveur (phase 1).
     `DownloadRequest.service` élargi à `"auto"`, `useDownload.ts` intact.
     **Vérif navigateur (session methammer, in-app browser)** : bundle déployé (`index-36QmQn7V.js`, plus
     aucune chaîne de la boucle client), un clic Download → séquence `/search` → `/files/exists` → **un
     seul** `/downloads/track` (plus de rafale), `service:"auto"` confirmé.
     **⚠️ Bug attrapé par cette vérif** : le mono-piste n'envoyait pas `auto_order`, donc le backend
     retombait sur son défaut (`tidal-amazon-qobuz`) au lieu de la chaîne de l'utilisateur — la chaîne
     n'était **pas honorée**. Corrigé (`7d8eae4`) : `auto_order` transmis pour `service:"auto"`. **À
     re-vérifier navigateur après redéploiement** (l'ordre de la chaîne effectif doit suivre le réglage
     UI).
   - **2b — à faire, avec vérif navigateur** : suivi de statut réel par SSE (aujourd'hui le mono-piste
     est marqué « terminé » dès l'enqueue — malhonnêteté *préexistante*, pas introduite par 2a). Câbler
     `useJobsStreamEvent` pour refléter done/failed/skipped réels par piste.
3. **UI/réglages** : aligner le défaut `AutoOrder` affiché/exécuté, revoir le libellé `allowFallback`.
4. **Valider S6** sur logs lisibles (maintenant possible), puis **porter `qobuz_api.go`** — Qobuz est
   enfin réellement atteignable.

Chaque phase est buildable/testable seule. **§7 est tranché — la phase 1 (backend seul) peut
démarrer.** Elle est testable isolément en prod (via `/downloads/track` avec `service` explicite et
`auto`) sans toucher au frontend, exactement comme la validation S6.
