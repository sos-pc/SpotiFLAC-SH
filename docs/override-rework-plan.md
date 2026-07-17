# Refonte de la sélection de service — plan d'implémentation

> **🧭 Plan — 2026-07-17.** Décision produit prise (modèle aligné sur l'UI, voir §1). **Investigation
> faite, implémentation pas commencée.** Ce document dit *ce qu'on veut*, *l'écart avec l'existant*
> (trois défauts trouvés en creusant, au-delà de l'override), *tout ce que ça touche*, et *les
> sous-questions encore ouvertes*. À lire avec la carte de l'existant :
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

**Conclusion :** implémenter la cible n'est pas « retirer l'override ». C'est **faire fonctionner la
boucle auto pour de vrai** (URLs par service, détection de succès réelle) et **décider où vit la
boucle** (front ou back).

## 4. Ce que ça touche

### Backend

| Fichier | Changement |
|---------|-----------|
| [`jobs_helpers.go`](../jobs_helpers.go) `buildDownloadRequest` | **Retirer les réécritures de `service`** (3.1, 3.4). Garder la résolution d'URL/ISRC, mais la stocker **par service** (voir 3.3), pas dans un `serviceURL` unique biaisé Tidal. |
| [`backend/downloader.go`](../backend/downloader.go) | Devient **l'unique autorité** de dispatch + fallback. La boucle `auto` doit lire une URL **par service** et détecter le **vrai** succès de chaque tentative. Aligner le défaut `AutoOrder`. |
| [`download_service.go`](../download_service.go) `ApplySettingsFallbacks` | Vérifier que `AutoOrder`/défauts sont cohérents avec la nouvelle logique. |
| `DownloadRequest` (champ `ServiceURL`) | Probablement remplacer par une map d'URLs par service, ou résoudre à la demande dans le dispatch. **Décision d'archi.** |

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

## 7. Sous-questions encore ouvertes (décision utilisateur)

1. **Où vit la boucle de fallback : backend seul ?** (recommandé — le front devient « envoie un service
   ou `auto` »). Confirme-t-on la suppression de la boucle client `downloadFallback.ts` ?
2. **Suivi de progression mono-piste.** Si le front n'itère plus et envoie `auto`, il doit suivre le
   job via la queue/SSE comme le batch. OK pour uniformiser sur ce modèle ?
3. **URL par service : map dans `DownloadRequest`, ou résolution paresseuse dans le dispatch ?** Choix
   d'archi backend — impacte la taille du diff.
4. **Deezer est `disabled` dans l'UI** ([GeneralTab.tsx:212](../frontend/src/components/settings/GeneralTab.tsx))
   mais présent dans les chaînes et le dispatch. On le garde dans la chaîne `auto` ou on l'exclut ?

## 8. Ordre d'implémentation suggéré (phasé, révisable)

1. **Backend d'abord, sans toucher au front** : retirer les réécritures de service (3.1/3.4), faire
   marcher la boucle `auto` avec URL par service (3.3). Testable en prod via `/downloads/track` avec
   `service` explicite et `auto`. À ce stade, la boucle front (3.2) devient redondante mais inoffensive.
2. **Frontend ensuite** : réduire `downloadFallback.ts` à un envoi simple, recâbler `useDownload`,
   uniformiser le suivi via la queue.
3. **UI/réglages** : aligner le défaut `AutoOrder` affiché/exécuté, revoir le libellé `allowFallback`.
4. **Valider S6** sur logs lisibles (maintenant possible), puis **porter `qobuz_api.go`** — Qobuz est
   enfin réellement atteignable.

Chaque phase est buildable/testable seule. Rien n'est codé tant que §7 n'est pas tranché.
