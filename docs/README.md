# Index de la documentation

**Point d'entrée. Lire ceci avant d'ouvrir quoi que ce soit d'autre.**

## Comment lire ce tableau

La colonne qui compte est **Type** — elle dit *à quelle vitesse un document pourrit* :

| Type | Ce que le doc affirme | Pourrit… |
|---|---|---|
| 📘 **Référence** | notre code | quand le code change — **vérifiable** contre `git`, jamais silencieusement faux sur le fond |
| 🌍 **Observation** | le monde extérieur (services tiers, état de la prod) | **silencieusement, avec le temps.** Un service meurt sans prévenir. **Sans date de vérification, une observation ne vaut rien.** |
| 🧭 **Plan** | une décision à prendre / un chantier non commencé | ne pourrit pas — se fait, ou s'abandonne |
| 🔍 **Constat** | un audit, une trouvaille datée | c'est un instantané : ouvert ou clos |

**La règle qui découle de cette session :** une 🌍 observation de plus de quelques jours doit être
**re-vérifiée avant d'être citée**, jamais reprise de confiance.

## L'index

Rangé par **état**. Si tu reprends le travail : §A.

### A. Chantiers EN COURS

| Chantier | Docs | Où ça en est | Prochain pas |
|---|---|---|---|
| **Retrait de la couche provider superseded** | [dead-code-removal-plan.md](dead-code-removal-plan.md) | 🧭 **planifié, rien de supprimé** — analyse de dépendances mesurée. ~2200 LOC retirables (Qobuz, Deezer, community), ~1000 de plus quand Amazon sera prouvé. Tidal **conservé** (BYOT). | Étape 2 du plan : retirer l'appel au repli natif (0 succès en 3 invocations) |
| **Durcissement du déploiement** | [deployment-hardening.md](deployment-hardening.md) | 🟡 5 corrigés+vérifiés ; 3 points ouverts | `grep memory /proc/cgroups` ; logs nginx SWAG (502 `stream-token`) ; rotations d'identifiants |
| **Refonte API** | [api-redesign-plan.md](api-redesign-plan.md) | 🟢 phases 1-4 faites+vérifiées prod ; audit 76 routes (1 élévation corrigée) | 2 décisions posées, non tranchées (catalogue `admin`→`read`, explorateur BoltDB) |

### B. Références du code — « comment ça marche »

| Doc | Type | Statut |
|---|---|---|
| **⭐ [module-engine.md](module-engine.md)** | 📘 | **vérifié en prod 07-25/26** — le moteur de download en sidecar : archi, contrat, activation, flux, limites, exploitation. Inclut §8 « ce qu'on a eu faux », car les plans archivés affirment encore le contraire. |
| [api-reference.md](api-reference.md) | 📘 | ⚠️ `/downloads/track`, `/files/exists`, `/files/m3u8` corrigés 07-18 ; le reste non re-vérifié |
| [authentication.md](authentication.md) | 📘 | ❔ non re-vérifié — JWT, clés API, Jellyfin |
| [settings-reference.md](settings-reference.md) | 📘 | ⚠️ à relire après la migration backend-autoritaire |
| [tidal-auth.md](tidal-auth.md) | 📘+🌍 | ❔ non re-vérifié — device-code Tidal (toujours utile : c'est le chemin **BYOT**) |
| [watchlist.md](watchlist.md) | 📘 | ⚠️ à relire — les watchlists suivent les réglages **globaux** |
| [deployment.md](deployment.md) | 📘+🌍 | ⚠️ minimal — lire `deployment-hardening.md` avant mise en ligne |
| [troubleshooting.md](troubleshooting.md) | 📘 | ⚠️ §FFmpeg corrigé 07-15 |

### C. Observations du monde extérieur — à re-vérifier avant de citer

| Doc | Vérifié | Note |
|---|---|---|
| [third-party-layer-status.md](third-party-layer-status.md) | corrigé 2026-07-18 | État réel des services tiers. **Encore valable pendant la transition** (tant que l'engine n'est pas live et que notre app utilise les proxies). Devient caduc une fois les providers passés à l'engine. |
| [EXTERNAL_APIS.md](EXTERNAL_APIS.md) | Amazon corrigé 07-15 | Les API externes utilisées. Sera fortement réduit post-engine (les proxies Qobuz/Amazon/Deezer partent ; voir la carte de migration). |

### D. Archive — [archive/](archive/)

Tout ce qui est **fait** ou **superseded** est déplacé dans [archive/](archive/) (voir son index).
N'y cherche pas l'état courant — seulement le « pourquoi ». **11 docs** : refontes terminées
(sélection de service, réglages, rattrapage amont), constats clos (ffmpeg, audit couche 2, carte de
sélection historique), les deux plans que l'engine remplace (couche API externe, MusicBrainz→Qobuz),
et les **trois plans du moteur** eux-mêmes — livrés, donc remplacés par la référence
[module-engine.md](module-engine.md).

⚠️ Ces trois-là contiennent des affirmations que la prod a **démenties** (solver supprimable, Deezer
mort, Qobuz natif devenu inutile). Le tableau §8 de la référence les corrige une par une.

## L'en-tête à mettre sur un doc 🌍 ou 🔍

Un bandeau d'une ligne, en tête de fichier, qui répond aux seules questions qui comptent :

```markdown
> **🌍 Observation — vérifié le AAAA-MM-JJ** (comment : sondes live / lecture du code / logs prod).
> Une observation non re-vérifiée depuis plus de quelques jours **doit être re-testée avant d'être citée**.
```

## Ce que ce système ne fait pas

Il ne détecte pas une affirmation **jamais vérifiée** (le cas ffmpeg : « statically-linked », écrit
avec aplomb, jamais testé). Aucune métadonnée ne rattrape ça — seule la discipline du moment
d'écriture le peut : **distinguer ce qu'on a mesuré de ce qu'on a déduit, et le dire dans le texte.**
