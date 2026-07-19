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
**re-vérifiée avant d'être citée**, jamais reprise de confiance. Trois docs affirmaient des choses
fausses (`amazon.spotbye.qzz.io` vivant, ffmpeg statique, le 401 attribué à Songlink) — aucune n'était
fausse à l'écriture ; deux ont pourri, une n'avait jamais été vérifiée.

## L'index

Rangé par **état** d'abord, par chantier ensuite. Si tu reprends le travail : §A.

### A. Chantiers EN COURS

| Chantier | Doc | Où ça en est | Prochain pas concret |
|---|---|---|---|
| **Durcissement du déploiement** | [deployment-hardening.md](deployment-hardening.md) | 🟡 5 corrigés+vérifiés ; **3 points ouverts** | `grep memory /proc/cgroups` (mem_limit) ; logs nginx SWAG (502 `stream-token`) ; **rotations d'identifiants** |
| **Couche API externe** (Qobuz, Amazon, DRM) | [external-api-layer.md](external-api-layer.md) | 🧭 **ouvert le 07-18**, non commencé | Décision produit : construire ou non le flux de vérification humaine (§5). Recommandé : **ne rien faire tant que Tidal tient** |
| **Refonte API** | [api-redesign-plan.md](api-redesign-plan.md) | 🟡 **phases 1-3 faites**, vérifiées en prod ; **phase 4 entière** | Audit des ~70 endpoints (§4). 2 décisions posées, non tranchées : catalogue `admin`→`read`, explorateur BoltDB |

### B. Chantiers TERMINÉS (2026-07-18/19) — vérifiés en prod

| Chantier | Doc | Ce qui a été obtenu |
|---|---|---|
| **Source unique des réglages** | [settings-source-of-truth.md](settings-source-of-truth.md) | 🟢 **terminé** — étapes 1→6 + watchlists, toutes vérifiées en prod. Plus aucun calcul de chemin côté client, **un seul override** (`service`). Inclut la fermeture du trou M3U8 (lot manuel) et le ménage du code mort. |
| **Refonte de la sélection de service** | [override-rework-plan.md](override-rework-plan.md) | 🟢 **terminé** — phases 1, 2a, 2b vérifiées navigateur ; phase 3 soldée **sans code** (défauts déjà alignés, libellé déjà exact, Deezer à ne PAS réactiver car mort). |
| **Rattrapage amont** | [upstream-catchup.md](upstream-catchup.md) | 🟢 **terminé pour tout ce qui était indépendant** — S8 (client Spotify) et S2 (validation post-téléchargement) implémentés et vérifiés. S6/S7/S4/S1 → chantier **API externe**. S11/S12/S13 **écartés avec argument**, pas oubliés. |

### C. Constats clos — pour comprendre le « pourquoi », pas l'état actuel

| Doc | Type | Statut | Pour quoi |
|---|---|---|---|
| [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md) | 🔍 | 🟢 clos, vérifié prod 07-16 | ffmpeg ne démarrait pas (`scratch` sans loader ELF). **§4bis : ce que le durcissement ne protège pas — toujours valable.** |
| [audit-refactoring-couche2.md](audit-refactoring-couche2.md) | 🔍 | 🟢 clos (R1–R12) | Audit couche 2. ⚠️ R12 contenait une attribution fausse — voir l'encadré. |
| [service-selection-map.md](service-selection-map.md) | 🕰️ **historique** | décrit l'état **AVANT** la refonte | ⚠️ **Ne décrit plus le code actuel.** Utile pour comprendre *pourquoi* la refonte. |

### D. Observations du monde extérieur — à re-vérifier avant de citer

| Doc | Type | Vérifié | Pour quoi |
|---|---|---|---|
| [third-party-layer-status.md](third-party-layer-status.md) | 🌍 | **corrigé le 2026-07-18** | L'état réel des services tiers. **Commencer ici** avant tout travail touchant un provider. ⚠️ **Correction majeure : « Amazon est mort » était faux** — le service a déménagé (`amz-oss`), c'est notre config qui est périmée. |
| [EXTERNAL_APIS.md](EXTERNAL_APIS.md) | 🌍 | Amazon corrigé 07-15 ; le reste non re-vérifié | Les API externes utilisées. Croiser avec le doc ci-dessus. |

### E. Références du code — la doc « comment ça marche »

| Doc | Type | Statut | Pour quoi |
|---|---|---|---|
| [api-reference.md](api-reference.md) | 📘 | ⚠️ `/downloads/track`, `/files/exists` et `/files/m3u8` **corrigés le 07-18** ; le reste non re-vérifié | Référence REST (~70 routes). |
| [deployment.md](deployment.md) | 📘+🌍 | ⚠️ minimal, pas durci — lire `deployment-hardening.md` avant de mettre en ligne | Déploiement de base. |
| [troubleshooting.md](troubleshooting.md) | 📘 | ⚠️ §FFmpeg corrigé 07-15 | Pannes courantes. |
| [authentication.md](authentication.md) | 📘 | ❔ non re-vérifié | JWT, clés API, Jellyfin. |
| [settings-reference.md](settings-reference.md) | 📘 | ❔ non re-vérifié | ⚠️ **à relire** après la migration backend-autoritaire. |
| [tidal-auth.md](tidal-auth.md) | 📘+🌍 | ❔ non re-vérifié | Flow device-code Tidal. |
| [watchlist.md](watchlist.md) | 📘 | ❔ non re-vérifié | ⚠️ **à relire** : les watchlists suivent désormais les réglages **globaux**. |

**« ❔ non re-vérifié »** est une information, pas un aveu : ces docs n'ont pas été relus lors des
travaux de juillet 2026. Ils ne sont pas présumés faux — ils sont **présumés non testés**.

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
C'est pourquoi les docs de juillet 2026 portent des mentions explicites du type « prouvé en
téléchargeant l'asset et en parsant l'ELF » vs « hypothèse non vérifiée ».
