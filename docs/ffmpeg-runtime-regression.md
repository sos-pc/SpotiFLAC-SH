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
(`ffmpeg-N-125519-g300cac3078-linux64-lgpl.tar.xz`) et en parsant sa table ELF (`PT_INTERP` +
`DT_NEEDED` résolus via `DT_STRTAB` — pas une recherche de chaînes) :

```
ffprobe / ffmpeg (identiques) :
  PT_INTERP  : /lib64/ld-linux-x86-64.so.2
  DT_NEEDED  : libm.so.6, libdl.so.2, librt.so.1, libpthread.so.0,
               libmvec.so.1, libc.so.6, ld-linux-x86-64.so.2, libgcc_s.so.1
```

**Les builds BtbN/FFmpeg-Builds ne sont pas *entièrement* statiques** — la nuance compte :

- ✅ **Les bibliothèques de codecs SONT embarquées.** Aucune `libav*.so` en `DT_NEEDED`, et le binaire
  pèse **115 Mo** (un `ffprobe` liant `libavcodec` dynamiquement ferait quelques centaines de Ko).
  **L'argument CVE du Dockerfile — éviter les ~30 paquets apt transitifs — reste donc valide.**
- ❌ **Mais la glibc et `libgcc_s` restent dynamiques.** Dans `scratch` (aucune libc, aucun
  `ld-linux`), le binaire ne peut pas démarrer.

C'est le saut logique « statique **donc** aucune distro nécessaire » qui était faux, pas tout
l'argumentaire. La dépendance réelle est **étroite et parfaitement identifiée** : glibc + `libgcc_s`,
rien d'autre. Notamment **pas** `libstdc++`.

> **Note de méthode.** Une première version de ce document affirmait que le binaire dépendait de
> `libstdc++`. **C'était faux** : le constat venait d'une recherche de sous-chaîne dans 115 Mo
> (`data.find(b"libstdc++")`), qui matche du bruit (symboles, chemins de debug). Seul le parsing réel
> de `DT_NEEDED` fait foi — et il ne liste pas `libstdc++`. Ironie utile : c'est exactement la même
> erreur de méthode (affirmer sans vérifier) que celle qui a causé le bug documenté ici.

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
| **(a) `gcr.io/distroless/cc`** ⭐ | base fournissant **exactement** glibc + `libgcc1` | **couvre au poil près les `DT_NEEDED` mesurées** ; diff d'une ligne ; pas de shell ni gestionnaire de paquets, donc l'esprit « pas de distro » est conservé ; maintenu par Google | réintroduit une petite surface OS (glibc/libgcc), à assumer côté scan CVE |
| **(b) build réellement statique** | johnvansickle.com/ffmpeg | conserve `FROM scratch` | **GPL v3** (le Dockerfile a délibérément choisi LGPL) ; et **BtbN n'offre aucune variante entièrement statique** — vérifié : le tag épinglé ne propose que `-shared` (libav externes) vs non-`-shared` (libav embarquées, glibc dynamique). Changer de fournisseur = changer de licence, de cadence de publication et de discipline de pinning |
| **(c) copier les libs depuis le stage Debian** | `COPY` de la glibc + `libgcc_s` | conserve `scratch` | fragile, à re-vérifier à chaque bump — **déconseillé** |

**Pourquoi (a) est le candidat naturel :** la dépendance mesurée est **glibc + `libgcc_s`, rien
d'autre**. `distroless/cc` = « minimal Linux, glibc runtime » **+ `libgcc1` et ses dépendances » —
c'est précisément l'ensemble requis, ni plus ni moins. (`distroless/base` **ne suffit pas** : il
fournit la glibc mais pas `libgcc_s`.)

**Réserve à assumer.** L'argument CVE du Dockerfile reste largement valide (les ~30 paquets de codecs
sont bel et bien embarqués — voir §2), mais sa formulation absolue — *« zéro paquet OS, donc zéro
surface OS pour un scanner »* — ne tiendra plus avec (a) ou (c). C'est un arbitrage réel :
quelques paquets OS de base contre des fonctions qui marchent.

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
