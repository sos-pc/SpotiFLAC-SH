# Chantier — couche API externe (Qobuz, Amazon, DRM)

> **🧭 Plan — ouvert le 2026-07-18.** Regroupe ce qui était éparpillé dans le rattrapage amont (S6
> Qobuz, S7 Amazon, S4 DRM, S1 infra communautaire) une fois mesuré que ces sujets ne sont **pas des
> portages de code** mais un même problème d'accès aux services tiers. Index : [README.md](README.md).
> Voisins : [third-party-layer-status.md](third-party-layer-status.md) (l'état des services),
> [upstream-catchup.md](upstream-catchup.md) (§S6/§S7/§S4, l'analyse d'origine).

## 0. Pourquoi ce chantier existe

Trois sujets du rattrapage amont butaient sur la même chose. Les traiter séparément menait chaque
fois à la même impasse, alors les voici ensemble.

**Le constat qui a tout réorienté (mesuré le 2026-07-18) :** ce n'est pas que les services tiers sont
morts. C'est que **notre accès l'est**.

## 1. L'état réel, mesuré

| Route | Mesure du 2026-07-18 |
|---|---|
| `qbz-oss.spotbye.qzz.io/health` (Qobuz, amont) | 🟢 **200** — `{"success":true,"data":{"status":"ok"}}` |
| `amz-oss.spotbye.qzz.io/health` (Amazon, amont) | 🟢 **200** — `{"ok":true,"has_wvd":true,"has_login":true}` |
| `amazon.spotbye.qzz.io` (**notre config**) | 🔴 DNS échoue — l'hôte a changé, le service n'a pas disparu |
| `musicdl.me` (**notre** remplaçant Qobuz de mai 2026) | 🔴 **500**, corps chiffré, **identique avec bonne clé / fausse clé / sans clé** |
| Recherche Qobuz non signée (notre code) | 🔴 **401** sur 3 ISRC |
| Recherche Qobuz **signée** (paire publique du web player) | 🟢 **200**, résultats réels |

**Deux corrections d'affirmations antérieures :**
- « Le proxy Amazon est mort » était **faux** : il a déménagé (`amazon.` → `amz-oss.`).
- « S6 = porter `qobuz_api.go` » était **incomplet** : Qobuz a **deux** maillons cassés (recherche
  *et* téléchargement). Porter les 407 lignes réparerait une recherche qui ne débouche sur rien.

## 2. Le vrai obstacle

L'accès communautaire exige une **session obtenue par vérification humaine dans un navigateur** :

1. amorçage → le serveur renvoie une `challenge_url` ;
2. **ouverture dans un navigateur**, avec une URL de rappel ;
3. un humain complète le défi → *grant* ;
4. échange contre une session signée (`X-Sig-Platform: desktop`, `install_id` par installation,
   délai d'attente 5 min).

Une sonde réelle, montée avec les fichiers amont, s'arrête sur `browser integration is not ready`.

**C'est une barrière anti-automatisation délibérée, pas un détail d'implémentation.**

## 3. Contrainte posée d'emblée

> **Aucun contournement ni résolution automatique du défi ne sera implémenté.** La seule forme
> acceptable est un flux où **l'utilisateur** complète la vérification lui-même dans son navigateur —
> ce qui reste un usage humain du service. La distinction n'est pas cosmétique.

À noter aussi : l'amont se déclare `platform: desktop`. Nous sommes un serveur auto-hébergé. S'annoncer
comme un client de bureau est un choix à assumer explicitement, pas à glisser sans le dire.

## 4. Ce que ça coûterait

| Élément | Taille |
|---|---|
| `community_session.go` (session, signature, enrôlement) | 323 lignes |
| `community_endpoints.go` (endpoints chiffrés — **déchiffrement déjà reproduit et vérifié**) | 278 lignes |
| `community_apikey.go` | 28 lignes |
| Adaptateur Qobuz | 114 lignes |
| Adaptateur Amazon | ~100 lignes (dans `amazon.go`) |
| **Plus, côté produit** | exposer le défi dans l'UI, endpoint de rappel, gestion de l'expiration et des re-vérifications |

Soit ~850 lignes **plus** une fonctionnalité produit — et un engagement récurrent : le protocole peut
changer sous nous, et chaque expiration redemande une action humaine.

## 5. Options

1. **Ne rien faire.** Tidal fonctionne et couvre les besoins actuels. Qobuz/Amazon restent désactivés
   comme Deezer. Coût nul. **Recommandé tant que Tidal tient.**
2. **Recherche Qobuz signée seule** (~40 lignes, mesurée fonctionnelle). Ne rend rien téléchargeable —
   supprime seulement un 401 trompeur. Faible valeur isolément.
3. **Flux de vérification complet.** Ressusciterait Qobuz **et** Amazon. À ouvrir avec du temps devant
   soi, pas en fin de session.

## 6. Ce qui est déjà acquis pour le jour J

- Le **déchiffrement des endpoints** est reproduit et vérifié (les URLs du §1 en sortent).
- La **recherche Qobuz signée** est comprise et mesurée : payload de signature = chemin normalisé sans
  `/` + params triés (hors `app_id`/`request_ts`/`request_sig`) + timestamp + secret, en MD5.
- La paire publique (`712109809` / `589be88e…`) **fonctionne aujourd'hui** — le scraping du bundle JS
  n'est qu'une mesure de **durabilité**, sans objet tant que le téléchargement ne marche pas.
- S4 (DRM mp4ff) **redevient pertinent** si le chantier se fait : `has_wvd:true` côté Amazon indique un
  service Widevine complet. Sans le chantier, la question reste sans objet.
