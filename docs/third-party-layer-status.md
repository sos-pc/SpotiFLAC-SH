# État des lieux — la couche de services tiers (2026-07-15, complété le 2026-07-18)

> **🌍 Observation — relevé initial le 2026-07-15** (sondes live : DNS, `/api/v1/apis/status` en prod,
> requêtes directes aux API). **C'est le document le plus périssable du repo** : il décrit des
> services tiers qui meurent sans prévenir — c'est tout son sujet. **Re-tester avant de citer.**
> Index : [README.md](README.md).

> **🔄 Mise à jour du 2026-07-18 — observations en prod, l'érosion continue.** Relevées pendant les
> vérifications de la refonte de sélection de service (logs de téléchargements réels, pas des sondes) :
>
> | Service | Observation (07-17/18) |
> |---|---|
> | **Deezer** (Deezmate) | **Cassé** : `all Deezer proxies failed: failed to decode response: invalid character '<'` — le proxy renvoie du **HTML** au lieu de JSON. Et sur une autre piste : `deezer: no results`. |
> | **Amazon** (`amazon.spotbye.qzz.io`) | **Mort** : `dial tcp: lookup amazon.spotbye.qzz.io: no such host` — confirme la disparition DNS déjà notée. |
> | **Songlink / Odesli** | **429 récurrents** : `songlink: API returned status 429`, puis `Rate limited, skipping calls for 5 minutes`. Fait échouer la résolution Amazon **et** Tidal quand elle est le seul chemin. |
> | **Qobuz** | 401 sur la recherche non signée — **attendu**, corrigé par le portage S6 (pas encore fait). |
> | **Tidal** | Fonctionne (le seul qui aboutit régulièrement). |
>
> **Conséquence pratique** : la chaîne `auto` finit presque toujours sur **Tidal**, les trois autres
> échouant. La redondance annoncée par la chaîne est donc largement théorique aujourd'hui.

> **Ce document existe parce que R12 s'est trompé de temporalité.** L'audit couche 2
> ([`audit-refactoring-couche2.md`](audit-refactoring-couche2.md) §R12) a constaté le 13/07 une
> « couche de résolution/proxy tierce **dégradée sur la fenêtre observée** » et conclu à un incident
> borné. Trois jours plus tard, rien n'est revenu — et plusieurs de ces services sont morts
> **définitivement** (sous-domaine DNS supprimé, pas en panne). Ce n'est pas un hoquet, c'est une
> érosion.

> ## 🔴 NOUVEAU — 2026-07-19 : Tidal échoue par intermittence
>
> Observé en prod pendant une vérification : **3 échecs pour 1 succès** sur une fenêtre de quelques
> minutes, tous avec
> `tidal: download URL — status 401 from primary and all fallbacks failed`
> (HI_RES_LOSSLESS **et** LOSSLESS), alors que `[Tidal Auth] Token refreshed successfully` apparaît
> juste avant. L'authentification passe donc, c'est l'obtention de l'URL de téléchargement qui est
> refusée.
>
> Les pistes touchées venaient d'une sync de watchlist, pas de mes tests — l'incident est indépendant
> de tout changement de code. **Ce n'est pas une panne totale : des téléchargements aboutissent
> encore.**
>
> **Ce que ça implique :** Tidal est la seule route qui fonctionnait. Si la dégradation se confirme,
> le chantier [external-api-layer.md](external-api-layer.md) — jusqu'ici recommandé « ne rien faire
> tant que Tidal tient » — change de priorité. À re-mesurer avant toute décision.

> ## ⚠️ CORRECTION MAJEURE — 2026-07-18 : « Amazon est mort » était FAUX
>
> Le service Amazon **n'a pas disparu, il a déménagé**. En déchiffrant la config d'endpoints de
> l'amont (`community_endpoints.go`, autorisation utilisateur sur les API communautaires), puis en
> sondant en direct :
>
> | Hôte | Sonde du 2026-07-18 |
> |---|---|
> | `amazon.spotbye.qzz.io` (**le nôtre**) | 🔴 DNS échoue |
> | `amz-oss.spotbye.qzz.io` (**celui de l'amont**) | 🟢 **HTTP 200** — `{"ok":true,"has_wvd":true,"has_login":true}` |
> | `qbz-oss.spotbye.qzz.io` (Qobuz, amont) | 🟢 **HTTP 200** — `{"success":true,"data":{"status":"ok"}}` |
>
> **Ce n'est pas un simple changement d'URL** : le protocole diffère (nous `GET /api/track/<ASIN>`,
> eux `POST /api/dl` + en-têtes signés).
>
> **Le vrai obstacle, mesuré :** l'accès communautaire exige une **session obtenue par vérification
> humaine dans un navigateur** (amorçage → `challenge_url` → l'utilisateur complète un défi → *grant*
> → session signée, `X-Sig-Platform: desktop`). Une sonde réelle s'arrête sur
> `browser integration is not ready`. Ce n'est donc pas un portage de code mais une fonctionnalité
> produit, avec l'utilisateur dans la boucle. **Aucun contournement automatique ne sera implémenté.**
>
> **Et `musicdl.me`** (notre remplaçant Qobuz de mai 2026) est mort de son côté : HTTP 500 avec un
> corps chiffré `application/octet-stream`, **identique avec la bonne clé, une fausse clé et sans
> clé** — l'endpoint échoue avant même de regarder l'authentification.
>
> Voir [upstream-catchup.md §S6](upstream-catchup.md).

## 1. L'état, vérifié en direct le 2026-07-15

| Service | Rôle | État | Nature de la panne |
|---|---|---|---|
| **Amazon proxy** (`amazon.spotbye.qzz.io`) | seule source Amazon | 🔴 DNS mort | ⚠️ **relecture 07-18 : le service a déménagé sur `amz-oss.spotbye.qzz.io`, qui répond 200.** C'est notre configuration qui est périmée, pas le service qui a disparu. |
| **SpotFetch** (`spotify.afkarxyz.fun/api`) | métadonnées Spotify alternatives | 🔴 timeout | ⚠️ **`useSpotFetchAPI = True` en prod** — donc activé alors qu'il ne répond pas |
| **`tidal-uptime.geeked.wtf`** | source de découverte auto des proxies Tidal | 🔴 DNS mort | la goroutine de découverte échoue à chaque cycle (`[Discovery] Failed to fetch tidal-uptime`) |
| **Qobuz** (`app_id` en dur) | recherche par ISRC | 🔴 **401** | identifiant révoqué par Qobuz — reproduit sur 3 ISRC, voir `upstream-catchup.md` §S6 |
| **Proxies Tidal communautaires** (×4 monochrome) | fallback Tidal | 🟡 PREVIEW only | limités à 30s sans compte Premium |
| **`hifi-api.kennyy.com.br`** | proxy Tidal | 🔴 timeout | |
| Song.link / odesli | résolution ISRC/liens | 🟢 **200** | (contrairement à ce que R12 affirmait) |
| Deezer | ISRC + genre album | 🟢 ok | |
| Apple Music | genre piste (via token web player) | 🟢 ok | ajouté 2026-07-15, voir §S10 |
| MusicBrainz | genre (dernier recours) | 🟢 ok | |

## 2. Ce que ça implique

**La prod ne tient que grâce au token Tidal Premium personnel de l'utilisateur** (`/auth/tidal/status`
→ `connected: true`). L'ordre configuré est `autoOrder = tidal-qobuz-amazon-deezer` :

1. **Tidal** — ✅ fonctionne, mais **uniquement via le token Premium personnel**. Les proxies
   communautaires, eux, sont en PREVIEW only.
2. **Qobuz** — 🔴 401 (voir S6)
3. **Amazon** — 🔴 endpoint inexistant
4. **Deezer** — 🟢

Autrement dit : **si le token Tidal expire ou est révoqué, il ne reste que Deezer.** Les trois autres
maillons sont cassés simultanément, et personne ne le voit parce que Tidal absorbe tout.

**Aggravant :** le choix de service de l'utilisateur est **silencieusement écrasé** au profit de
Tidal dès que Tidal trouve la piste par ISRC ([`jobs_helpers.go:263-266`](../jobs_helpers.go)) —
donc même un `service: "qobuz"` explicite part sur Tidal. Vérifié en direct. Conséquence : les
pannes de Qobuz/Amazon sont **invisibles** tant que Tidal marche. C'est pourquoi les logs de prod ne
contiennent aucune trace de 401 Qobuz aujourd'hui, alors que le bug est bien réel et reproductible.

## 3. Conséquence directe sur S4 (déchiffrement DRM mp4ff)

`upstream-catchup.md` §S4 pose la question « faut-il porter le déchiffrement DRM multi-clés ? ».
**Cette question est sans objet en l'état** : il n'y a plus d'endpoint Amazon dont déchiffrer quoi
que ce soit. Le sous-domaine est supprimé.

~~Récupérer une source Amazon suppose d'abord de **retrouver l'endpoint actuel**~~ — **fait le
2026-07-18** : `amz-oss.spotbye.qzz.io`, vivant (`has_wvd:true`, `has_login:true`). La question S4
n'est donc plus « sans objet » : elle redevient pertinente **si** on décide de construire le flux de
vérification humaine décrit dans l'encadré en tête de document. L'ordre reste : accès d'abord,
déchiffrement DRM ensuite, et seulement s'il s'avère nécessaire.

## 4. Question ouverte — l'écrasement du service est-il voulu ?

```go
// jobs_helpers.go:254-267
if serviceURL == "" && streamingURLs["isrc"] != "" {
    tidalID, tidalAPI, err := tidal.GetTidalIDFromISRC(...)
    if err == nil && tidalID > 0 {
        ...
        if service != "tidal" && service != "auto" {
            service = "tidal"        // ← écrase un choix explicite
            audioFormat = firstNonEmpty(s.TidalQuality, "LOSSLESS")
        }
    }
}
```

**Vérifié en direct :** un job soumis avec `service: "qobuz"` a été téléchargé via Tidal, sans une
seule ligne Qobuz dans les logs.

Deux lectures possibles, et le code ne dit pas laquelle est voulue :
- **Fallback délibéré** — « on préfère toujours Tidal s'il a la piste, quel que soit le réglage ».
  Alors le nom du champ (`service`) est trompeur : c'est une préférence, pas un choix.
- **Bug** — le réglage explicite de l'utilisateur devrait être respecté, et `auto` seul devrait
  arbitrer.

**Effet de bord dans les deux cas :** les pannes des autres providers deviennent invisibles. C'est
mesurable — les logs de prod ne contiennent aucun 401 Qobuz aujourd'hui alors que le bug est
reproductible à 100 %.

## 5. Le motif, et ce qu'il coûte

Ce ne sont pas des incidents indépendants : **toute la couche de services tiers non officiels
s'érode**, et chaque panne est silencieuse (aucune n'a produit d'alerte — elles ont toutes été
découvertes en cherchant autre chose).

Ce qui a permis de les voir : la page de statut (`/api/v1/apis/status`), qui fait de **vraies
sondes fonctionnelles** et non des pings. C'est le seul dispositif qui rende cette érosion visible —
d'où l'intérêt d'y brancher toute nouvelle source (fait pour Apple Music, cf. `pingAppleMusic`).

**Ce qui manque encore :** rien ne surveille `useSpotFetchAPI=True` pointant sur un service mort, ni
`tidal-uptime` qui échoue en boucle. Ces deux-là ne sont pas dans `coreServices`.
