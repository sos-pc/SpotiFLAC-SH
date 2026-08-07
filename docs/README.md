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
| **Durcissement du déploiement** | [deployment.md § Durcissement](deployment.md#durcissement) | 🟡 décisions vivantes reprises dans `deployment.md` le 2026-08-07 ; l'analyse d'origine est [archivée](archive/deployment-hardening.md), antérieure au moteur. 2 points ouverts : `mem_limit` ignoré par le noyau, 502 `stream-token` au boot | `grep memory /proc/cgroups` ; logs nginx SWAG ; durcir le service moteur |
| **Refonte API** | [api-redesign-plan.md](api-redesign-plan.md) | 🟢 phases 1-4 faites+vérifiées prod | 2 décisions posées, non tranchées (catalogue `admin`→`read`, explorateur BoltDB) |

Le **retrait de la couche provider superseded** est terminé : `backend/{qobuz,amazon,deezer,community,songlink}`
et `util/proxy_config.go` n'existent plus — vérifié fichier par fichier le 2026-08-04. Le plan est
[passé en archive](archive/dead-code-removal-plan.md).

### B. Références du code — « comment ça marche »

| Doc | Type | Statut |
|---|---|---|
| **⭐ [module-engine.md](module-engine.md)** | 📘 | ✅ **vérifié contre le code 2026-08-04**, engine 1.6.0 — le moteur en sidecar : archi, contrat, activation, flux, limites, exploitation. §8 « ce qu'on a eu faux » liste aussi les erreurs de ce document lui-même. |
| [upstream-tracking-plan.md](upstream-tracking-plan.md) | 📘 | ✅ **en service** — pourquoi il n'y a plus de fork, et comment la CI suit les releases amont (comparaison PyPI ↔ label de notre image). |
| [api-reference.md](api-reference.md) | 📘 | ✅ **vérifié 2026-08-04** — les 72 routes enregistrées sont documentées, aucune route documentée n'est absente du code. Comparaison automatisée route par route. |
| [settings-reference.md](settings-reference.md) | 📘 | ✅ **vérifié 2026-08-04** — toutes les clés documentées existent côté frontend (les réglages sont un blob `map[string]interface{}`, le schéma appartient au frontend). |
| [deployment.md](deployment.md) | 📘+🌍 | ✅ **corrigé 2026-08-04** — décrivait une installation mono-conteneur qui ne télécharge plus rien. ✅ **complété 2026-08-07** — porte désormais le durcissement et le mécanisme de mise à jour des deux images. |
| [authentication.md](authentication.md) | 📘 | ❔ non re-vérifié — JWT, clés API, Jellyfin |
| [tidal-auth.md](tidal-auth.md) | 📘+🌍 | ❔ non re-vérifié — device-code Tidal (toujours utile : c'est le chemin **BYOT**) |
| [watchlist.md](watchlist.md) | 📘 | ⚠️ à relire — les watchlists suivent les réglages **globaux** |
| [troubleshooting.md](troubleshooting.md) | 📘 | ⚠️ §FFmpeg corrigé 07-15 ; le vocabulaire « proxy » y survit par endroits |

### C. Observations du monde extérieur — à re-vérifier avant de citer

| Doc | Vérifié | Note |
|---|---|---|
| [EXTERNAL_APIS.md](EXTERNAL_APIS.md) | Amazon corrigé 07-15 | Les API externes utilisées. À relire : la couche proxy Qobuz/Amazon/Deezer qu'il décrit est partie avec la v4.0.0. |

Le relevé de l'ancienne couche de proxies (`third-party-layer-status.md`) est
[passé en archive](archive/third-party-layer-status.md) : cette couche n'existe
plus. Les endpoints que le moteur utilise à sa place viennent d'un registre
chiffré qu'il récupère **à l'exécution**, donc aucun document ne peut en tenir
l'état à jour — voir `module-engine.md` §5.

### D. Archive — [archive/](archive/)

Tout ce qui est **fait** ou **superseded** est déplacé dans [archive/](archive/) (voir son index).
N'y cherche pas l'état courant — seulement le « pourquoi ». **13 docs** : refontes terminées
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
