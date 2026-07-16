# État des lieux — la couche de services tiers (2026-07-15)

> **🌍 Observation — vérifié le 2026-07-15** (sondes live : DNS, `/api/v1/apis/status` en prod,
> requêtes directes aux API). **C'est le document le plus périssable du repo** : il décrit des
> services tiers qui meurent sans prévenir — c'est tout son sujet. **Re-tester avant de citer.**
> Index : [README.md](README.md).

> **Ce document existe parce que R12 s'est trompé de temporalité.** L'audit couche 2
> ([`audit-refactoring-couche2.md`](audit-refactoring-couche2.md) §R12) a constaté le 13/07 une
> « couche de résolution/proxy tierce **dégradée sur la fenêtre observée** » et conclu à un incident
> borné. Trois jours plus tard, rien n'est revenu — et plusieurs de ces services sont morts
> **définitivement** (sous-domaine DNS supprimé, pas en panne). Ce n'est pas un hoquet, c'est une
> érosion.

## 1. L'état, vérifié en direct le 2026-07-15

| Service | Rôle | État | Nature de la panne |
|---|---|---|---|
| **Amazon proxy** (`amazon.spotbye.qzz.io`) | seule source Amazon | 🔴 **mort** | **DNS supprimé** — le domaine parent `spotbye.qzz.io` résout (Cloudflare), le sous-domaine `amazon.` non. Retiré, pas en panne. |
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

Récupérer une source Amazon suppose d'abord de **retrouver l'endpoint actuel**, que l'upstream a
déplacé dans sa config chiffrée (`community_endpoints.go`, AES-GCM — voir §S1). L'ordre logique est
donc : endpoint d'abord, déchiffrement DRM ensuite (et seulement s'il s'avère nécessaire).

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
