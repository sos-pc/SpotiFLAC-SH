# Réglages — pas de source unique de vérité

> **🔍 Constat — 2026-07-17.** Relevé par lecture du code + observation en prod (session methammer,
> in-app browser) pendant la vérif de la refonte de sélection de service. **Problème d'architecture
> réel** signalé par l'utilisateur : « les settings ne sont pas appliqués partout, il n'y a pas une
> seule vérité ». Ce document décrit *pourquoi*, avec preuves, et les décisions à prendre. Rien n'est
> corrigé ici. Index : [README.md](README.md). Voisins :
> [service-selection-map.md](service-selection-map.md), [override-rework-plan.md](override-rework-plan.md).

## 0. Le constat en une phrase

Un même réglage existe dans **trois stockages** côté client/serveur, et atteint le backend par **deux
mécanismes différents selon le chemin de téléchargement**. Résultat : deux téléchargements peuvent
utiliser des réglages différents, et ce que l'UI affiche n'est pas toujours ce qui tourne.

## 1. Trois stockages de réglages

| # | Stockage | Écrit par | Lu par |
|---|----------|-----------|--------|
| A | **Serveur** (BoltDB, par utilisateur) | `PUT /api/v1/settings` (bouton *Save Changes*) | `ApplySettingsFallbacks` (backend, à chaque download) **et** `loadSettings()` (frontend au boot) |
| B | **localStorage** (`spotiflac-settings`) | le frontend (changement d'UI, et `loadSettings` recopie le serveur) | `getSettingsFromLocalStorage()` |
| C | **Cache mémoire** (`cachedSettings`) | `loadSettings()` (depuis A) ou fallback depuis B | `getSettings()` |

`getSettings()` = `cachedSettings ?? localStorage` ([settings.ts:259](../frontend/src/lib/settings.ts)).
`loadSettings()` va chercher le serveur et remplit C **et** B.

**Divergence observée en prod (2026-07-17)** : à un instant, `localStorage.downloader = "auto"` alors
que l'UI et le serveur montraient `"qobuz"`. Les trois n'étaient pas d'accord. Selon le *timing* (avant
ou après que `loadSettings` ait rempli le cache), `getSettings()` renvoie l'un ou l'autre.

## 2. Deux mécanismes de livraison au backend — la conséquence directe

| Chemin | Comment les réglages arrivent au backend |
|--------|-------------------------------------------|
| **Batch** (`POST /jobs`) | Le frontend **envoie** tous les réglages dans `jobSettings` (voir `useDownload.ts enqueueTracksBatch`). Le backend les prend tels quels. |
| **Mono-piste** (`POST /downloads/track`) | Le frontend envoie un **sous-ensemble** ; le backend **remplit le reste depuis les réglages SERVEUR** via [`ApplySettingsFallbacks`](../download_service.go) : `OutputDir`, `FilenameFormat`, `AudioFormat`, `AutoOrder`, `EmbedLyrics`, `EmbedMaxQualityCover`, `AllowFallback`, `UseFirstArtistOnly`, `UseSingleGenre`, `EmbedGenre`, `TrackNumber`. |

**Donc** : pour un même réglage (ex. `autoOrder`), le batch utilise la valeur **frontend** (cache/
localStorage) et le mono-piste utilise la valeur **serveur**. Si les deux divergent (§1 — ex. un
changement d'UI non sauvegardé), **les deux chemins de téléchargement se comportent différemment**.
C'est littéralement « les settings pas appliqués partout ».

> **Vérifié le 2026-07-17** : `Source=auto`, chaîne `deezer-qobuz-amazon-tidal` sauvegardée. Un
> download mono-piste envoie `service:"auto"` **sans** `auto_order` → le backend le remplit depuis le
> serveur → la chaîne **est** honorée (Teardrop est revenu en 16-bit, cohérent avec Deezer en tête).
> Le mécanisme *marche* quand serveur = UI ; il **casse** dès qu'ils divergent.

## 3. Autres problèmes voisins (constatés)

- **Défauts `autoOrder` divergents** (déjà en [service-selection-map §4](service-selection-map.md)) :
  UI placeholder `tidal-qobuz-amazon`, exécution front/back `tidal-amazon-qobuz`. Ce que l'utilisateur
  *voit* quand rien n'est réglé n'est pas ce qui *tourne*.
- **Champ `format` de l'historique trompeur** : un fichier 16-bit venu de Deezer s'affiche
  `format: "HI_RES_LOSSLESS"` — c'est l'`audio_format` **demandé** (qualité Tidal) stocké tel quel,
  pas le service ni la qualité réelle du fichier.
- **Statut mono-piste marqué « done » dès l'enqueue** (malhonnêteté préexistante, suivie en
  [override-rework-plan §8 phase 2b](override-rework-plan.md)).

## 4. La décision à prendre : un seul modèle

Il faut choisir **qui détient la vérité** et l'appliquer aux deux chemins :

1. **Frontend autoritaire** : le frontend envoie **tous** les réglages effectifs, le backend ne
   remplit **rien** depuis le serveur (ou seulement en dernier recours si un champ est absent *et*
   qu'aucun réglage utilisateur n'est joignable). C'est le modèle du **batch** aujourd'hui.
   - *Pour* : ce que l'UI montre est exactement ce qui tourne. Un seul endroit décide.
   - *Contre* : le frontend doit tout envoyer (surface plus large) ; un client tiers de l'API doit
     fournir tous les champs.
2. **Backend autoritaire** : le frontend n'envoie que l'essentiel (URL/piste + éventuels overrides
   ponctuels), le backend applique **toujours** les réglages serveur de l'utilisateur. C'est le modèle
   du **mono-piste** aujourd'hui.
   - *Pour* : les réglages vivent à un seul endroit (serveur) ; l'API est simple à appeler.
   - *Contre* : un changement d'UI n'a d'effet qu'après *Save* ; le frontend doit refléter fidèlement
     l'état serveur (le cache/localStorage ne doivent jamais faire autorité).

**Le point commun des deux** : supprimer la divergence en **ne laissant qu'une source décider** par
chemin, et rendre les deux chemins cohérents. Le patch d'un seul champ (le `auto_order` de `7d8eae4`,
reverté) va dans le sens inverse : il crée une source **mixte**, pire que l'un ou l'autre modèle.

## 5. Recommandation

**Backend autoritaire (option 2)** semble le plus proche de l'existant et le plus robuste : les
réglages sont déjà persistés par utilisateur côté serveur, `ApplySettingsFallbacks` existe déjà, et ça
donne une seule source (le serveur). Il resterait à :
- faire passer le **batch** aussi par un remplissage serveur (au lieu d'envoyer `jobSettings`), ou au
  moins garantir que ce que le batch envoie = les réglages serveur ;
- garantir côté frontend que `getSettings()` reflète **toujours** le serveur (jamais un localStorage
  périmé) — corriger la priorité cache/localStorage et le timing du boot ;
- aligner les défauts `autoOrder` (§3).

Mais c'est une **décision produit** : à trancher avant de coder. Ce document ne la tranche pas.

## 6. Note de méthode (l'erreur qui a mené ici)

Ce constat est né d'un **diagnostic raté** : j'avais conclu à un « bug de chaîne » depuis un log à 401
sans vérifier le réglage réel (`Source` était sur `Qobuz`, pas `auto` — le 401 était donc correct) ni
capturer le payload envoyé. J'ai poussé un correctif (`7d8eae4`) sur cette base fausse, puis reverté
(`76e96a7`) après avoir capturé la vérité terrain dans le navigateur. La leçon est celle de tout le
projet : **capturer ce qui se passe réellement avant de conclure** — ici, lire le réglage effectif et
le corps de la requête, pas interpréter un log ambigu.
