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

Rangé par **ce que tu cherches**, pas alphabétiquement. Si tu reprends le travail : commence par §A.

### A. Chantiers actifs — le travail en cours

| Doc | Type | Où ça en est | Pour quoi |
|---|---|---|---|
| [settings-source-of-truth.md](settings-source-of-truth.md) | 🔍+🧭 | **étapes 1, 2, 3, 6 + watchlists ✅ vérifiées prod** (6 par test d'empoisonnement du localStorage) ; **4 (cover/paroles) et 5 (M3U8) codées**, vérif navigateur en attente. Migration terminée côté code : plus aucun calcul de chemin côté client. | Les réglages n'avaient pas de source unique (3 stockages, 2 mécanismes). **Décidé : backend autoritaire**, un seul override (`service`). §7 = carte exhaustive + plan phasé, §7.5 = avancement. |
| [override-rework-plan.md](override-rework-plan.md) | 🧭 | **phases 1 + 2a ✅ vérifiées** ; reste 2b (statut SSE honnête), 3 (UI : Deezer, défauts `autoOrder`) | Refonte de la sélection de service vers le modèle UI (`auto`+chaîne honorée / explicite = forcer). La boucle de fallback **existait déjà** (`ExecuteDownload`) — réutilisée, pas recodée. |
| [upstream-catchup.md](upstream-catchup.md) | 🔍 | 🟡 en cours (S1–S16) · **S6 validé en prod le 07-18, prêt à porter** | Rattrapage de l'upstream. Le tableau §0 porte le statut par sujet. Prochain morceau concret : porter `qobuz_api.go` (recherche signée). |
| [deployment-hardening.md](deployment-hardening.md) | 🔍 | 🟡 5 corrigés+vérifiés · **`mem_limit` + 502 `stream-token` ouverts** | Compose, SWAG/nginx, conteneur. DoS login et port LAN **corrigés** ; a fait surgir 2 bugs dormants (upload, SSE) et une anomalie non expliquée (§5.3 : 502 au premier appel du boot). §7 : reste `mem_limit`, le 502, et les rotations d'identifiants. |
| [api-redesign-plan.md](api-redesign-plan.md) | 🧭 | ⏸️ non commencé | Cohérence read/manage/admin + accès DB. Décisions ouvertes en §3. |

### B. Constats clos — pour comprendre le « pourquoi », pas l'état actuel

| Doc | Type | Statut | Pour quoi |
|---|---|---|---|
| [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md) | 🔍 | 🟢 **clos**, vérifié en prod 07-16 | ffmpeg ne démarrait pas (`scratch` sans loader ELF). Corrigé (distroless/cc + smoke test CI), durcissement complet (9 sites). **§4bis : ce que le durcissement ne protège pas — toujours valable.** |
| [audit-refactoring-couche2.md](audit-refactoring-couche2.md) | 🔍 | 🟢 clos (R1–R12) | Audit couche 2. ⚠️ R12 contenait une attribution fausse — voir l'encadré. |
| [service-selection-map.md](service-selection-map.md) | 🕰️ **historique** | décrit l'état **AVANT** la refonte | ⚠️ **Ne décrit plus le code actuel** (l'override et la boucle client ont été supprimés). Utile pour comprendre *pourquoi* la refonte : les 4 couches, les pièges de nommage, la dérive des défauts. |

### C. Observations du monde extérieur — à re-vérifier avant de citer

| Doc | Type | Vérifié | Pour quoi |
|---|---|---|---|
| [third-party-layer-status.md](third-party-layer-status.md) | 🌍 | relevé 07-15, **complété le 2026-07-18** (logs de downloads réels) | L'état réel des services tiers. **Commencer ici** avant tout travail touchant un provider. **Constat 07-18 : Deezer cassé (HTML au lieu de JSON), proxy Amazon mort, Songlink en 429 — la chaîne `auto` finit presque toujours sur Tidal.** |
| [EXTERNAL_APIS.md](EXTERNAL_APIS.md) | 🌍 | Amazon corrigé 07-15 ; le reste non re-vérifié | Les API externes utilisées. Croiser avec `third-party-layer-status.md`. |

### D. Références du code — la doc « comment ça marche »

| Doc | Type | Statut | Pour quoi |
|---|---|---|---|
| [deployment.md](deployment.md) | 📘+🌍 | ⚠️ minimal, pas durci — lire `deployment-hardening.md` avant de mettre en ligne | Déploiement de base. |
| [troubleshooting.md](troubleshooting.md) | 📘 | ⚠️ §FFmpeg corrigé 07-15 | Pannes courantes. |
| [api-reference.md](api-reference.md) | 📘 | ❔ non re-vérifié | Référence REST (~70 routes). |
| [authentication.md](authentication.md) | 📘 | ❔ non re-vérifié | JWT, clés API, Jellyfin. |
| [settings-reference.md](settings-reference.md) | 📘 | ❔ non re-vérifié | Les réglages et leur effet. ⚠️ à relire après la migration backend-autoritaire. |
| [tidal-auth.md](tidal-auth.md) | 📘+🌍 | ❔ non re-vérifié | Flow device-code Tidal. |
| [watchlist.md](watchlist.md) | 📘 | ❔ non re-vérifié | Watchlists et synchronisation. ⚠️ à relire : les watchlists suivent désormais les réglages **globaux**. |

**« ❔ non re-vérifié »** est une information, pas un aveu : ces docs n'ont pas été relus lors des
travaux de juillet 2026. Ils ne sont pas présumés faux — ils sont **présumés non testés**. À traiter
comme tels si on s'appuie dessus.

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
