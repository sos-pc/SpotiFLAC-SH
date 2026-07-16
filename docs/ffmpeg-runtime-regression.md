# Régression — ffmpeg/ffprobe inexécutables dans l'image `scratch`

> **🔍 Constat — diagnostiqué, prouvé, et corrigé le 2026-07-15 (option (a), voir §4bis).**
> Découvert en investiguant tout autre chose (le 401 Qobuz de S6). Régression introduite le
> 2026-07-12, en production trois jours. **Le correctif attend un déploiement pour être confirmé en
> prod** — la CI le vérifie désormais à chaque build. Lire §4bis pour ce que le durcissement ne
> protège pas. Index : [README.md](README.md).

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

## 4bis. Ce qui a été fait (2026-07-15) — et ce que ça ne protège pas

**Option (a) retenue et implémentée.**

| Changement | Où | Effet |
|---|---|---|
| `FROM scratch` → `gcr.io/distroless/cc-debian12@sha256:7ee09f…` | `Dockerfile` | ffmpeg/ffprobe démarrent enfin. Base **vérifiée** en listant ses 18 couches : les 8 `DT_NEEDED` mesurées y sont, y compris `/lib64/ld-linux-x86-64.so.2` (le `PT_INTERP` exact) et `libgcc_s.so.1` — que `distroless/base` **n'a pas**. |
| Smoke test | `.github/workflows/docker.yml` | Exécute réellement les binaires + **l'argv exact de production** sur un vrai fichier. C'est le garde-fou qui manquait : un `COPY` réussi ne prouve rien. |
| `-protocol_whitelist file` + `-nostdin` | `backend/util/ffmpeg_hardening.go`, appliqué à `tidal/client.go` et `util/ffprobetags.go` | Coupe la jambe réseau : un fichier forgé ne peut plus faire ouvrir d'URL à ffmpeg (SSRF / exfiltration via `dref` mp4 ou entrées de playlist). |

### Les limites, explicitement

**Le durcissement ne réduit pas la probabilité d'une RCE dans ffmpeg.** Il retire deux facilités
bon marché ; il ne rend pas le décodeur sûr. La surface reste : démuxeur mov/mp4, décodeurs AAC/FLAC,
sur des octets **choisis par des proxies tiers**.

**Le choix de l'image de base n'y change rien.** Même binaire, mêmes codecs, sur `scratch` comme sur
distroless. La base décide seulement s'il *démarre*. Aujourd'hui la surface était nulle — parce que
rien ne s'exécutait. « Sécurisé par la panne » n'est pas une stratégie.

**Durcissement partiel, assumé.** Seuls 2 des ~8 points d'appel portent les flags : le chemin de
téléchargement Tidal (entrée la moins fiable) et la lecture de tags (toute la bibliothèque). **Non
couverts** : `audio/analysis.go`, `audio/ffmpeg.go` (ConvertAudio), `meta/metadata.go`,
`amazon/client.go`. Raison : ils n'ont pas pu être testés sur un vrai fichier ici, et casser une
conversion coûterait plus que le gain marginal. À étendre **une fois le smoke test vert** — il
prouvera que les flags passent.

**Le rayon de souffle d'une RCE reste entier**, et il est plus large qu'il n'y paraît :

| Atteint | Pourquoi c'est sérieux |
|---|---|
| `configDir/jwt_secret` | **forge de JWT admin indéfiniment** ; persiste dans le volume nommé, donc **survit au correctif et au redéploiement**. Le mettre en variable d'env ne protège pas (`/proc/self/environ`). |
| `tidal_token.json` | contient le **`refresh_token`** → accès **durable** au compte, pas un jeton éphémère |
| bibliothèque (bind mount, écriture) | milliers de fichiers, suppression/chiffrement possibles |
| multi-utilisateur | watchlists et historiques de tous les comptes |

**Probabilité vs impact.** Exploiter ceci demande un 0-day ciblé contre un ffmpeg de 4 jours
(master `N-125519`, 2026-07-11), livré par un proxy compromis. **Peu probable — mais l'impact est
sérieux.** Ne pas confondre les deux.

**Le seul levier qui réduirait la probabilité** est de se méfier de ce que ffmpeg avale, donc des
proxies communautaires — voir [`third-party-layer-status.md`](third-party-layer-status.md).

### Durcissement conteneur — non fait, car c'est le déploiement, pas le code

Le compose de référence s'arrête à `user: "1000:1000"`. Ces trois blocs réduiraient ce qu'une RCE
*permet ensuite* (pas sa probabilité), sans coût : le binaire est non-root et n'a besoin d'aucune
capability (port 6890 > 1024).

```yaml
security_opt: [ "no-new-privileges:true" ]
cap_drop: [ ALL ]
read_only: true          # les volumes Music/.SpotiFLAC restent inscriptibles
tmpfs: [ /tmp ]
```

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
