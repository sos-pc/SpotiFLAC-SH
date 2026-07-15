# Régression — ffmpeg/ffprobe inexécutables dans l'image `scratch`

> **Statut : diagnostiqué et prouvé, correctif non appliqué — décision requise (§4).**
> Découvert le 2026-07-15 en investiguant tout autre chose (le 401 Qobuz de S6). Régression
> introduite le 2026-07-12, en production depuis.

## 1. Le symptôme

```
GET /api/v1/system/ffmpeg
{"ffmpeg_path": "/usr/local/bin/ffmpeg", "ffprobe_installed": false, "installed": false}

WARN [History] Failed to get metadata for file file=/home/nonroot/Music/….flac
     err=ffprobe failed: fork/exec /usr/local/bin/ffprobe: no such file or directory
```

**Le message est trompeur** : le fichier `/usr/local/bin/ffprobe` **existe bel et bien**.

`GetFFprobePath()` ([backend/util/ffmpegpath.go:82](../backend/util/ffmpegpath.go)) renvoie ce chemin
**sans erreur**, ce qui prouve qu'il vient d'`exec.LookPath` — et `LookPath` vérifie l'existence *et*
le bit exécutable. C'est l'`exec` suivant qui échoue en `ENOENT`.

**Un fichier que `LookPath` trouve mais que `exec` refuse en ENOENT = interpréteur ELF manquant.**
Linux renvoie ENOENT quand c'est le *loader* du binaire qui est introuvable, pas le binaire lui-même.

## 2. La cause — une prémisse fausse devenue porteuse

**Prouvé** en téléchargeant l'asset exact épinglé par le Dockerfile
(`ffmpeg-N-125519-g300cac3078-linux64-lgpl.tar.xz`) et en lisant l'en-tête ELF :

```
ffprobe : interp '/lib64/ld-linux-x86-64.so.2'  → PRÉSENT
          DT_NEEDED: libc.so.6, libm.so.6, libpthread.so.0, libstdc++
ffmpeg  : identique
```

**Les builds BtbN/FFmpeg-Builds ne sont PAS statiques.** Ils sont dynamiquement liés à la glibc et à
libstdc++. Dans une image `scratch` (aucune libc, aucun `ld-linux`), ils ne peuvent pas s'exécuter.

### L'enchaînement, par bisect git

| Date | Commit | Base runtime | État |
|---|---|---|---|
| 07-11 | `4787220` | `bookworm-slim` | ✅ fonctionnait — la glibc de Debian fournissait le loader |
| **07-12** | **`25b5d42`** | **`scratch`** | ❌ **cassé ici** |

- `4787220` affirme : *« A statically-linked ffmpeg has no runtime library dependencies of its own »*
  — **jamais vérifié**, mais **sans conséquence** : la base `bookworm-slim` fournissait le loader.
- `25b5d42` s'appuie dessus : *« **Now that ffmpeg is a static binary** […] nothing in the runtime
  image actually needs a Linux distro »* → `FROM scratch`. **La prémisse fausse devient porteuse, et
  tout casse.**

C'est le schéma classique : une affirmation non vérifiée, inoffensive dans son contexte d'origine,
réutilisée plus tard comme fondation.

### Pourquoi la CI n'a rien vu

Le `COPY` du Dockerfile réussit (les fichiers existent bien dans le stage de build), l'image se
construit, et **rien n'exécute ffmpeg pendant la CI**. Un simple `ffmpeg -version` dans l'image
finale aurait suffi à l'attraper.

## 3. Rayon de souffle (points d'appel vérifiés)

| Fonction | État | Détail |
|---|---|---|
| Tidal LOSSLESS via URL directe / manifest BTS | ✅ **fonctionne** | sortie anticipée sans ffmpeg ([tidal/client.go:350](../backend/tidal/client.go)) |
| **Tidal HI_RES / DASH segmenté / non-FLAC** | ❌ cassé | conversion ffmpeg ([tidal/client.go:491](../backend/tidal/client.go)) |
| Conversion audio (`POST /api/v1/audio/convert`) | ❌ cassé | [audio/ffmpeg.go:409](../backend/audio/ffmpeg.go) |
| Analyse audio / spectre | ❌ cassé | [audio/analysis.go:174](../backend/audio/analysis.go) |
| Lecture de tags M4A (File Manager) | ❌ cassé | [filemanager.go:250](../backend/filemanager.go) → ffprobe |
| `GetAudioDuration` **hors FLAC** | ❌ cassé | [meta/metadata.go:596](../backend/meta/metadata.go) — le FLAC passe par un parsing natif, tout le reste par ffprobe |
| Déchiffrement Amazon | ❌ cassé | [amazon/client.go:262](../backend/amazon/client.go) — sans objet en pratique : le proxy `amazon.spotbye.qzz.io` ne résout plus (DNS mort, vérifié) |
| Métadonnées d'historique | ❌ cassé | le WARN observé ci-dessus |

**Impact réel modéré aujourd'hui** parce que le réglage de prod est `tidalQuality = LOSSLESS`, qui
emprunte la sortie anticipée sans ffmpeg. Un téléchargement Tidal a été déclenché en prod pour le
vérifier : il a réussi.

**Conséquence pour S2 (`upstream-catchup.md`)** : le portage de la validation de durée
post-téléchargement, recommandé comme « petit et sûr », serait **silencieusement inopérant hors
FLAC** tant que ffprobe ne s'exécute pas. À corriger avant S2.

## 4. Options de correction — décision requise

| Option | Principe | Pour | Contre |
|---|---|---|---|
| **(a) `gcr.io/distroless/cc`** | base fournissant glibc + libstdc++ | petit diff, garde l'esprit « pas de distro », maintenu par Google | réintroduit une (petite) surface OS |
| **(b) build réellement statique** | ex. johnvansickle.com/ffmpeg (statique confirmé) | conserve `FROM scratch` | à vérifier : licence (GPL vs LGPL), codecs disponibles, politique de pinning/checksum |
| **(c) copier les libs depuis le stage Debian** | `COPY` de la glibc + libstdc++ | conserve `scratch` | fragile, à re-vérifier à chaque bump — **déconseillé** |

**Réserve à assumer.** Tout l'argumentaire CVE du Dockerfile (*« zéro paquet OS, donc zéro surface
OS pour un scanner »*) repose sur cette prémisse erronée. Les options (a) et (c) réintroduisent une
surface OS réelle ; seule (b) préserve l'argument — à condition que le build statique existe avec la
licence et les codecs voulus (`libmp3lame` + aac/alac natifs, cf. `backend/audio/ffmpeg.go`).

**Quelle que soit l'option retenue : ajouter un `RUN ffmpeg -version` (ou équivalent) sur l'image
finale en CI.** C'est ce qui manquait, et c'est ce qui empêchera la prochaine régression du même
type.

## 5. Documentation à corriger une fois l'option choisie

L'affirmation « statique » s'est propagée — ces passages sont **faux en l'état** :

- [`docs/deployment.md:240`](deployment.md) — *« downloads a **statically-linked** FFmpeg/FFprobe build »*
- [`docs/deployment.md:241`](deployment.md) — *« the two **static** FFmpeg binaries »*
- [`docs/troubleshooting.md:117`](troubleshooting.md) — *« In Docker deployments this **should never
  happen** »* : activement trompeur, il décourage d'enquêter sur exactement ce qui se produit
- [`README.md:290`](../README.md) — *« static ffmpeg fetch »*
- Le commentaire du `Dockerfile` (stage 3) — le raisonnement CVE complet

*(Un avertissement pointant vers ce document a été ajouté dans `troubleshooting.md` en attendant le
correctif.)*
