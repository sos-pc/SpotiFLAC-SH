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

| Doc | Type | Statut | Vérifié | Pour quoi |
|---|---|---|---|---|
| [third-party-layer-status.md](third-party-layer-status.md) | 🌍 | ⚠️ **périssable** | **2026-07-15**, sondes live | L'état réel des services tiers. **Commencer ici** avant tout travail touchant un provider. |
| [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md) | 🔍 | 🟢 **clos** | **2026-07-16, exécution réelle en prod** (+ ELF parsé, couches listées) | ffmpeg/ffprobe ne démarraient pas (`scratch` sans loader ELF). Corrigé via distroless/cc + smoke test CI ; durcissement **complet** (9 sites) vérifié en prod, chemin Tidal compris. **§4bis : ce que le durcissement ne protège pas — toujours valable.** |
| [deployment-hardening.md](deployment-hardening.md) | 🔍 | 🟡 **3 corrigés, 1 ouvert** | **2026-07-16**, sondes HTTP/2 + tests de ports + lecture du code | Compose, SWAG/nginx, conteneur. DoS login (compteur partagé) et port LAN ouvert **corrigés et vérifiés** ; `mem_limit` rejeté par le noyau **ouvert**. A fait surgir 2 bugs applicatifs dormants (upload, SSE). **§7 : les tâches ouvertes.** |
| [upstream-catchup.md](upstream-catchup.md) | 🔍 | 🟡 en cours (S1–S16) | 2026-07-15 | Rattrapage de l'upstream. Le tableau §0 porte le statut par sujet. |
| [audit-refactoring-couche2.md](audit-refactoring-couche2.md) | 🔍 | 🟢 clos (R1–R12) | 07-13 · **§R12 corrigé le 07-15** | Audit couche 2. ⚠️ R12 contenait une attribution fausse — voir l'encadré. |
| [api-redesign-plan.md](api-redesign-plan.md) | 🧭 | ⏸️ non commencé | 2026-07-15 | Cohérence read/manage/admin + accès DB. Décisions ouvertes en §3. |
| [EXTERNAL_APIS.md](EXTERNAL_APIS.md) | 🌍 | ⚠️ **partiellement périmé** | Amazon corrigé 07-15 ; **le reste non re-vérifié** | Les API externes utilisées. Croiser avec `third-party-layer-status.md`. |
| [deployment.md](deployment.md) | 📘+🌍 | ⚠️ §Docker inexact | ffmpeg corrigé 07-15 | Déploiement. L'archi Docker décrite est l'intention, pas la réalité (cf. régression ffmpeg). |
| [troubleshooting.md](troubleshooting.md) | 📘 | ⚠️ §FFmpeg corrigé 07-15 | — | Pannes courantes. Disait « should never happen » sur ce qui arrive. |
| [api-reference.md](api-reference.md) | 📘 | ❔ **non re-vérifié** | — | Référence REST (~70 routes). Généré en lisant les fichiers de routes. |
| [authentication.md](authentication.md) | 📘 | ❔ non re-vérifié | — | JWT, clés API, Jellyfin. |
| [settings-reference.md](settings-reference.md) | 📘 | ❔ non re-vérifié | — | Les réglages et leur effet. |
| [tidal-auth.md](tidal-auth.md) | 📘+🌍 | ❔ non re-vérifié | — | Flow device-code Tidal. |
| [watchlist.md](watchlist.md) | 📘 | ❔ non re-vérifié | — | Watchlists et synchronisation. |

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
