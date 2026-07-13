# Audit de refactoring — SpotiFLAC-SH (couche 2 : maintenabilité & structure)

> **Périmètre.** Cette couche est distincte du premier audit (`review-et-audit-le-dynamic-gem.md`,
> lots 1-8 + Q11), qui portait sur **sécurité / fiabilité / infra** et est intégralement livré.
> Ici on regarde le code **tel qu'il est aujourd'hui, après tous ces correctifs**, sous l'angle
> **maintenabilité** : code smells, duplication, méthodes trop longues, violations SOLID, nommage.
> Aucun de ces points n'est un bug ni une faille — ce sont des dettes structurelles qui rendent le
> code plus coûteux à faire évoluer. Chaque finding est **vérifié par lecture directe** (numéros de
> ligne réels), pas déduit de mémoire.
>
> **Contrainte directrice.** Le comportement doit être **strictement préservé**. On refactore par
> petits pas, chaque pas suivi de `go build ./... && go vet ./... && go test -race ./...` (+ `bun run
> build && bun run lint` côté front), et pour tout changement d'UI une vérification navigateur réelle.

---

## 0. Résumé exécutif

| ID | Zone | Smell | Impact | Effort | Priorité |
|----|------|-------|--------|--------|----------|
| **R1** | `frontend/…/SettingsPage.tsx` (2230 l.) | God component : 6 onglets inline, 26 `useState` | Élevé | Moyen-élevé | **1** |
| **R2** | `backend/spotify/*.go` (`FilterTrack` 312 l., etc.) | Primitive obsession : traversée de `map[string]interface{}` | Élevé | Élevé | **2** |
| **R3** | `app_core.go` — struct `App` (50+ méthodes, 1183 l.) | God object (façade Wails vestigiale) | Moyen | Élevé (mécanique) | 3 |
| **R4** | `watcher.go` — `syncPlaylist` (252 l.), fichier 1783 l. | Méthode longue + fichier fourre-tout | Moyen | Moyen | 3 |
| **R5** | `backend/downloader.go` dispatch providers | Pas d'interface `Provider` ; `switch` dupliqué ; entrées mal nommées | Moyen | Faible-moyen | 4 |
| **R6** | `backend/{tidal,qobuz,amazon,deezer}` `DownloadFile` | Boilerplate requête HTTP dupliqué 4× | Faible | Faible | 4 |
| **R7** | `watcher.go` — `DownloadPath`-or-default ×5 | Petite duplication | Faible | Très faible | 5 |
| **R8** | `app_core.go` — `SaveSettings`/`LoadSettings` `map[string]interface{}` | Réglages stringly-typed côté Go | Faible | Moyen (risque de drift) | 5 (optionnel) |
| **R9** | `frontend/…/useDownload.ts` (849 l.) | Gros hook (déjà partiellement découpé) | Faible | Faible | 5 |
| **R10** | retag `incomplete-metadata` (terrain) | Non-convergent : sur-sélection sur `genre` (99 % skip) | Moyen | Faible | **voir §6** |
| **R11** | Dépendances (ffmpeg/Go/front) | ffmpeg & front déjà à jour ; Dependabot **actif** (11 PR ouvertes non triées, dont sqlite/bbolt/x/text) | Faible-moyen | Faible | **voir §7** |
| **R12** | Résolution/proxy tierce (terrain) | Songlink (401 + 429) & proxies Tidal en échec en prod = I1/I2 en direct ; M3U8 `/all` OK à 2546/2547 | Externe | — | **voir §6** |

**Déjà propre — à ne PAS toucher** (pour équilibrer : tout n'est pas à refaire) :
`frontend/src/lib/rpc.ts` (helper `rest<T>()` partagé + wrappers d'une ligne), `backend/providerutil/*`
(boîte à outils partagée déjà extraite au lot 4 : `DownloadToFileAtomic`, `ChromeUserAgent`,
`genremeta`, `atomicwrite`), `frontend/src/lib/jobsStream.ts` (hub SSE ref-compté, une seule connexion).

**Recommandation de séquencement.** R1 seul apporte le plus de valeur de lisibilité pour un risque
contenu (découpage mécanique, pas de logique modifiée). R2 est le plus payant à long terme mais le
plus risqué (le parsing Spotify est du code chaud, peu testé) — à faire **incrémentalement, une
fonction `Filter*` à la fois, avec des tests de caractérisation posés AVANT**. R5-R7 sont des quick
wins DRY à faible risque, bons à grouper. R3/R8 sont optionnels et peuvent être différés
indéfiniment sans coût.

---

## 1. Évaluation qualité (code smells & anti-patterns vérifiés)

### R1 — `SettingsPage.tsx` : God component (⚠️ priorité 1)

**Constat vérifié.** 2230 lignes, **une seule** fonction composant `SettingsPage`, **26 `useState`**,
19 `useEffect`/`useCallback`, et un `return` JSX unique contenant six blocs `{activeTab === "…" && (…)}` :

| Onglet | Lignes | ~Taille |
|--------|--------|---------|
| `general` | 702-1283 | ~580 |
| `files` | 1284-1543 | ~260 |
| `keys` | 1544-1649 | ~105 |
| `tidal` | 1650-1771 | ~120 |
| `apis` | 1772-2041 | ~270 |
| `maintenance` | 2042-2230 | ~190 |

Les états sont déjà regroupés par bannière commentée (`// ── API Keys state`, `// ── Tidal Auth
state`, `// ── API Statuses state`, `// ── Library maintenance state`, `// ── Proxy config state`),
ce qui **révèle les coutures naturelles** : chaque groupe d'état ne sert qu'à un seul onglet.

**Pourquoi c'est un problème.**
- **Charge cognitive** : impossible de raisonner sur l'onglet « Maintenance » sans scroller à
  travers 2000 lignes d'états sans rapport.
- **Re-renders** : les 26 `useState` vivent dans un seul composant ; une frappe dans un champ de
  l'onglet General re-évalue tout le corps (y compris le JSX des 5 autres onglets, même non montés,
  côté réconciliation du closure).
- **Fusion de merge / revue** : toute modif d'onglet touche le même fichier géant → conflits.
- **SRP** : le composant a 6 responsabilités.

### R2 — `backend/spotify/{client,metadata}.go` : primitive obsession (⚠️ priorité 2)

**Constat vérifié.** `client.go` (1854 l.) et `metadata.go` (1669 l.) manipulent presque tout via
`map[string]interface{}` + une famille de casts non typés :
`getString`/`getMap`/`getSlice`/`getFloat64`/`getBool` (client.go:371-405). Les fonctions de
transformation sont énormes :

| Fonction | Lignes | Taille |
|----------|--------|--------|
| `FilterTrack` | 565-877 | **312** |
| `FilterAlbum` | 877-1018 | 141 |
| `FilterPlaylist` | 1018-1270 | 252 |
| `FilterArtist` | 1370-1499 | 129 |

**Pourquoi c'est un problème.**
- **Zéro sûreté au compile** : une clé JSON mal orthographiée ou un champ renommé côté Spotify passe
  silencieusement (`getString` renvoie `""`), au lieu d'échouer à la compilation ou au décodage.
- **Illisible** : une fonction de 312 lignes qui pioche des clés dans une `map` imbriquée est
  impossible à survoler ; la forme réelle de l'objet Spotify n'est documentée nulle part — elle est
  « devinée » clé par clé.
- **Non testable finement** : pas de type = pas de valeur à asserter proprement ; on ne peut tester
  que le tout via des fixtures JSON.

### R3 — `app_core.go` : God object `App` (façade Wails vestigiale)

**Constat vérifié.** La struct `App` porte **50+ méthodes** (1183 l.) couvrant des domaines sans
rapport : streaming (`GetStreamingURLs`), métadonnées (`GetSpotifyMetadata`, `SearchSpotify*`),
téléchargement (`DownloadTrack`, `ApplySettingsFallbacks`), historique (10 méthodes
`*History*`), analyse audio (`AnalyzeTrack`), téléchargements média (`DownloadLyrics/Cover/Header/
GalleryImage/Avatar`), FFmpeg (`IsFFmpegInstalled`…), fichiers (`ListDirectoryFiles`,
`RenameFileTo`, `UploadImage`…), réglages (`SaveSettings`/`LoadSettings`), OS (`GetOSInfo`).

C'est l'héritage direct du binding Wails (dans l'app desktop, **tout** était méthode de `App` pour
être exposé au front). Dans la version web, ce couplage n'a plus de raison d'être — les handlers
HTTP appellent déjà `a.ctr.*` directement à beaucoup d'endroits.

**Pourquoi c'est un problème.** Violation SRP massive ; `App` est un point de couplage central que
tout le backend référence, ce qui gêne le test unitaire (il faut un `App` complet pour tester une
méthode isolée) et brouille la carte mentale du code.

**Nuance honnête.** La plupart de ces méthodes sont des **délégations d'une ligne** vers `backend.*`
ou `a.ctr.*`. Le smell est réel mais le risque de le corriger est surtout mécanique, et le gain est
modéré (c'est une façade, pas de la logique enfouie). **À différer** sauf si un domaine précis
devient actif.

### R4 — `watcher.go` : méthode longue + fichier fourre-tout

**Constat vérifié.** 1783 lignes, 40 fonctions. `syncPlaylist` (196-448) fait **252 lignes** et
enchaîne plusieurs phases distinctes dans une seule fonction (verrou initial → fetch metadata →
diff des pistes → enqueue → génération M3U8 → verrou final). Les frontières sont visibles via les
`w.mu.Lock/Unlock` (198-209 au début, 409-420 à la fin).

Le fichier mélange : le daemon/scheduler (`daemon`, `checkAll`), le CRUD watchlist (`AddWatchlist`,
`RemoveWatchlist`, `UpdateWatchlist`, `saveWatchlist`), la sync (`syncPlaylist`, `OnBatchComplete`),
la génération M3U8 (`generateM3U8ForPlaylist`, `loadM3U8Settings`, `m3u8BaseName`…), les stats
(`GetWatchlistStats`, `CheckWatchlistFreshness`, `computeFreshnessReport`), et des helpers de parsing
(`extractTracksFromMetadata` 132 l., `convertTracks`, `extractPlaylistName`).

**Note concurrence (rappel, pas un nouveau bug).** `syncPlaylist` relâche `w.mu` pendant le corps de
sync (qui dure plusieurs minutes) — c'est **volontaire** (on ne veut pas tenir le verrou aussi
longtemps) mais ça laisse la fenêtre de lost-update décrite en Q3. Le premier audit a resserré le
verrouillage sur `RemoveWatchlist`/`OnBatchComplete`/`UpdateWatchlist` (tous en `defer
w.mu.Unlock()` désormais) ; le cas `syncPlaylist` reste un compromis assumé. **À documenter, pas à
« corriger » à l'aveugle.**

### R5 — Dispatch providers sans interface (`backend/downloader.go`)

**Constat vérifié.** La sélection de provider est un `switch req.Service` (downloader.go:374) avec
les cas `amazon/tidal/qobuz/deezer/auto`, et le cas `"auto"` (425) contient **un second `switch svc`**
(443) qui reconstruit et rappelle chaque provider — la logique de construction+appel est donc écrite
**deux fois**. Pire, chaque provider expose une **entrée nommée différemment** :

| Provider | Méthode d'entrée |
|----------|------------------|
| tidal | `Download(p DownloadParams)` |
| qobuz | `DownloadTrack(p)` / `DownloadTrackWithISRC(p)` |
| amazon | `DownloadByURL(p)` / `DownloadBySpotifyID(p)` |
| deezer | `Download(p)` |

**Pourquoi c'est un problème (OCP).** Ajouter un provider ⇒ éditer le `switch` à **deux endroits** +
retenir quelle méthode s'appelle comment. Il n'existe aucune abstraction `Provider` commune alors que
la signature converge déjà vers `func(DownloadParams) (string, error)`.

### R6 — Boilerplate `DownloadFile` dupliqué (4 providers)

**Constat vérifié.** `tidal.DownloadFile` (client.go:317) et `qobuz.DownloadFile` (client.go:418)
sont à ~90 % identiques : `http.NewRequest("GET", …)` → `req.Header.Set("User-Agent",
providerutil.ChromeUserAgent)` → `client.Do` → check `StatusCode != 200` → `providerutil.
DownloadToFileAtomic(...)` → log MB. Le **cœur** (écriture atomique + callback vitesse) est déjà
partagé (lot 4) ; il reste le **squelette requête/statut/erreur** copié-collé dans les 4 providers.

### R7 — `DownloadPath`-or-default répété 5× (`watcher.go`)

**Constat vérifié.** Le motif exact
```go
outputDir := pl.Settings.DownloadPath
if outputDir == "" { outputDir = util.GetDefaultMusicPath() }
```
apparaît aux lignes **340, 394, 525, 1259, 1500** (Q8 avait supprimé le chemin codé en dur
`/home/nonroot/Music`, mais pas centralisé ce fallback).

### R8 — Réglages `map[string]interface{}` côté Go (optionnel)

`App.SaveSettings`/`LoadSettings` (app_core.go:1061,1087) travaillent en `map[string]interface{}`
alors que le front a un type `Settings` complet (`frontend/src/lib/settings.ts`, 412 l.). Le contrat
réel des réglages n'est typé que d'un côté. **Attention** : introduire un struct Go typé crée un
risque de **drift** avec le type TS s'ils ne sont pas générés depuis une source unique → à ne faire
que si on met en place une génération de types partagée. Sinon, laisser tel quel.

### R9 — `useDownload.ts` : gros hook (déjà en partie découpé)

849 lignes, ~10 `useState` + refs. **Bon point** : trois helpers purs sont déjà extraits **au-dessus**
du hook (`buildExistenceCheckRequests`, `enqueueTracksBatch`, `maybeCreateM3U8`). Reste que le corps
du hook mêle le mode « download navigateur » (via SSE) et le mode « download serveur ». Découpage
possible d'un sous-hook `useBrowserDownloadMode`, mais gain modéré → basse priorité.

---

## 2. Plan de refactoring priorisé

### Phase A — Découpage `SettingsPage` (R1) · risque faible, gain élevé
1. Créer `frontend/src/components/settings/` avec un fichier par onglet : `GeneralTab.tsx`,
   `FilesTab.tsx`, `ApiKeysTab.tsx`, `TidalTab.tsx`, `ApisTab.tsx`, `MaintenanceTab.tsx`.
2. **Déplacer l'état avec son onglet** : chaque groupe `// ── … state` + ses `useCallback`/
   `useEffect` de chargement migrent dans le composant d'onglet correspondant. `SettingsPage` ne
   garde que `activeTab`, `savedSettings`/`tempSettings` (partagés General↔Files) et la barre
   d'onglets.
3. Faire une étape à la fois (un onglet extrait = un commit), en vérifiant l'UI dans le navigateur
   après chaque extraction (la procédure Playwright déjà éprouvée pour l'onglet Maintenance).

### Phase B — Quick wins DRY/SOLID backend (R5, R6, R7) · risque faible
4. **R7** : ajouter `func (pl *WatchedPlaylist) EffectiveDownloadPath() string` et remplacer les 5
   occurrences. Un seul commit, purement mécanique.
5. **R6** : extraire `providerutil.GetToFile(client *http.Client, url string, cb ProgressCallback)
   (int64, error)` regroupant requête+UA+statut+écriture atomique ; réécrire les 4 `DownloadFile`
   par-dessus (tidal garde son cas spécial `MANIFEST:` en amont).
6. **R5** : définir `type Provider interface { Download(DownloadParams) (string, error) }`, faire
   converger les entrées (garder les méthodes existantes, ajouter un adaptateur `Download` là où le
   nom diffère), puis remplacer les deux `switch` par une **fabrique** `providerFor(service) Provider`
   + une seule boucle pour le mode `auto`.

### Phase C — Décomposition `spotify` (R2) · risque élevé, gain élevé, incrémental
7. **Poser d'abord des tests de caractérisation** : fixtures JSON réelles (déjà présentes ?
   sinon, capturer une réponse `Query` par type) → asserter la sortie actuelle de chaque `Filter*`
   **avant** tout changement. Ces tests figent le comportement observable.
8. Extraire, **une `Filter*` à la fois**, des sous-fonctions par section (ex. `FilterTrack` →
   `extractTrackCore` / `extractTrackAlbum` / `extractTrackArtists`), sans changer les types encore.
9. (Optionnel, plus tard) introduire des structs typés + `json.Unmarshal` pour remplacer la traversée
   `map[string]interface{}` là où la forme est stable.

### Phase D — Splits de fichiers (R4) · risque faible, gain lisibilité
10. Scinder `watcher.go` par responsabilité en gardant le même package :
    `watcher_sync.go` / `watcher_crud.go` / `watcher_m3u8.go` / `watcher_stats.go` /
    `watcher_parsing.go`. Aucun changement de logique — juste déplacer des fonctions (le compilateur
    garantit l'exactitude). Extraire ensuite les phases de `syncPlaylist` en sous-méthodes nommées.

### Phase E — Optionnels (R3, R8, R9) · à différer
11. R3/R8/R9 ne sont **pas** planifiés tant qu'un besoin concret ne les rend pas rentables.

---

## 3. Code refactoré — extraits représentatifs

> Illustratif : la forme cible, pas le patch complet. Chaque extrait préserve le comportement.

### R7 — méthode `EffectiveDownloadPath` (le plus simple, à faire en premier)

```go
// EffectiveDownloadPath renvoie le dossier de sortie configuré pour cette
// watchlist, en retombant sur le dossier musique par défaut si aucun n'est
// défini. Centralise le fallback jusque-là dupliqué (watcher.go: 340, 394,
// 525, 1259, 1500).
func (pl *WatchedPlaylist) EffectiveDownloadPath() string {
	if pl.Settings.DownloadPath != "" {
		return pl.Settings.DownloadPath
	}
	return util.GetDefaultMusicPath()
}
```
Puis chaque site devient : `outputDir := pl.EffectiveDownloadPath()`.

### R6 — helper `providerutil.GetToFile`

```go
// GetToFile fait un GET simple et écrit le corps atomiquement dans dstPath,
// en signalant la vitesse via cb. Regroupe le squelette requête/UA/statut
// jusque-là copié dans chaque DownloadFile de provider.
func GetToFile(client *http.Client, url, dstPath string, cb ProgressCallback) (int64, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", ChromeUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	return DownloadToFileAtomic(dstPath, resp.Body, cb)
}
```
`qobuz.DownloadFile` se réduit alors à :
```go
func (q *QobuzDownloader) DownloadFile(url, filepath string) error {
	written, err := providerutil.GetToFile(util.NewHTTPClient(5*time.Minute), url, filepath, q.SpeedCallback)
	if err != nil {
		return err
	}
	slog.Debug("[Qobuz] Downloaded", "mb", float64(written)/(1024*1024))
	return nil
}
```
(tidal conserve le court-circuit `MANIFEST:` **avant** l'appel.)

### R5 — interface `Provider` + fabrique

```go
// Provider est l'abstraction commune que downloader.go dispatche. Toutes les
// implémentations téléchargent une piste et renvoient le chemin du fichier.
type Provider interface {
	Download(p DownloadParams) (string, error)
}

// providerFor renvoie le provider pour un service donné (nil si inconnu).
// Ajouter un provider = une ligne ici, plus aucun switch ailleurs.
func providerFor(service, apiURL string) Provider {
	switch service {
	case "tidal":
		return tidal.NewTidalDownloader(apiURL)
	case "qobuz":
		return qobuz.NewQobuzDownloader()
	case "amazon":
		return amazon.NewAmazonDownloader()
	case "deezer":
		return deezer.NewDeezerDownloader()
	default:
		return nil
	}
}
```
Le mode `auto` devient une **seule** boucle sur un ordre de priorité :
```go
for _, svc := range autoOrder { // ex. []string{"tidal","amazon","qobuz","deezer"}
	if p := providerFor(svc, ""); p != nil {
		if filename, err = p.Download(params); err == nil {
			break
		}
	}
}
```
(Là où l'entrée diffère — `qobuz.DownloadTrack`, `amazon.DownloadByURL` — on ajoute une méthode
`Download` d'adaptation d'une ligne sur le downloader concerné, sans supprimer l'existante.)

### R1 — squelette du découpage `SettingsPage`

```tsx
// SettingsPage.tsx — ne garde QUE la navigation et l'état partagé.
export function SettingsPage({ isAdmin }: SettingsPageProps) {
  const [activeTab, setActiveTab] = useState<SettingsTab>("general");
  const [savedSettings, setSavedSettings] = useState<SettingsType>(getSettings());
  const [tempSettings, setTempSettings] = useState<SettingsType>(savedSettings);

  return (
    <div>
      <SettingsTabBar active={activeTab} onChange={setActiveTab} isAdmin={isAdmin} />
      {activeTab === "general" && (
        <GeneralTab saved={savedSettings} temp={tempSettings} onChange={setTempSettings} />
      )}
      {activeTab === "files" && <FilesTab settings={tempSettings} onChange={setTempSettings} />}
      {activeTab === "keys" && <ApiKeysTab />}
      {activeTab === "tidal" && <TidalTab />}
      {activeTab === "apis" && <ApisTab />}
      {isAdmin && activeTab === "maintenance" && <MaintenanceTab />}
    </div>
  );
}
```
```tsx
// settings/MaintenanceTab.tsx — l'état maintenance (rebuild/retag) déménage ICI,
// avec ses handlers et ses écouteurs SSE, hors du composant géant.
export function MaintenanceTab() {
  const [rebuildLoading, setRebuildLoading] = useState(false);
  const [rebuildResult, setRebuildResult] = useState<LibraryRebuildResult | null>(null);
  // … runLibraryRebuild + useJobsStreamEvent("library_rebuild_done", …) tels quels …
  return (/* les cartes de l'onglet, inchangées */);
}
```

### R2 — caractérisation AVANT décomposition (le filet de sécurité)

```go
// spotify_filter_characterization_test.go — GÈLE la sortie actuelle avant tout
// refactoring de FilterTrack. On ne juge pas si la sortie est « correcte », on
// certifie juste qu'elle NE CHANGE PAS pendant qu'on découpe la fonction.
func TestFilterTrackCharacterization(t *testing.T) {
	raw := loadFixture(t, "testdata/track_query_response.json") // capture réelle
	got := FilterTrack(raw)
	want := loadGolden(t, "testdata/track_filtered.golden.json") // 1re exécution = référence
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("FilterTrack output changed (-want +got):\n%s", diff)
	}
}
```

---

## 4. Explication (ce qui change, et pourquoi c'est sûr)

- **R1 / R4 / R3** ne changent **aucune logique** : ce sont des déplacements de code. Le compilateur
  (Go) et le typecheck (TS) garantissent l'exactitude du déplacement ; la seule chose à vérifier
  manuellement est le câblage des props (R1) — d'où la vérif navigateur par étape.
- **R5 / R6 / R7** suppriment de la duplication en introduisant une abstraction que la signature
  appelait déjà : on ne fait que **nommer** un pattern existant. On garde les méthodes historiques
  (adaptateurs) pour ne pas casser d'appelants.
- **R2** est le seul qui touche du code chaud peu testé → il **exige** le filet de caractérisation
  (§3) posé en premier ; sans lui, on ne refactore pas. Décomposition d'abord (même types), typage
  ensuite (optionnel).
- **Aucune nouvelle dépendance** n'est requise (sauf éventuellement `github.com/google/go-cmp` en
  `testdata`, déjà courant en test Go — à confirmer dans `go.mod`).
- **Pas de breaking change d'API** externe : les contrats HTTP (`/api/v1/*`) et le format SSE
  restent identiques. R5/R6 sont internes au backend ; R1 est interne au front.

---

## 5. Recommandations de test

**Filet global (avant de commencer).** `go test -race ./...` et `bun run build && bun run lint`
doivent être verts au point de départ (ils le sont). Ce sont les gardes-fous de non-régression pour
**tous** les lots.

**Par finding :**
- **R1** : après chaque onglet extrait, rejouer le parcours Playwright (nav sidebar → onglet →
  action) et confirmer visuellement la carte de résultat / le toast. Vérifier spécifiquement que
  General↔Files partagent bien `tempSettings` (le bouton « Save Changes » doit refléter les deux).
- **R2** : tests de caractérisation golden **par type** (track / album / playlist / artist / search)
  posés avant, rejoués après chaque extraction. Cas limites à couvrir : champ absent (doit rester
  `""`/`0`, pas paniquer), artistes multiples, cover manquante, playlist vide, piste sans ISRC.
- **R5 / R6** : un test de dispatch qui, pour chaque `service` (`tidal/qobuz/amazon/deezer` + un
  inconnu), vérifie que `providerFor` renvoie le bon type concret (et `nil` sur inconnu). Pour
  `GetToFile` : test avec un `httptest.Server` renvoyant 200+corps (fichier écrit, octets comptés) et
  un renvoyant 404 (erreur propagée, pas de fichier partiel — l'atomicité de `DownloadToFileAtomic`
  le garantit déjà).
- **R7** : test unitaire trivial de `EffectiveDownloadPath` (chemin défini → renvoyé tel quel ;
  chemin vide → défaut).
- **R4** : purement mécanique ; la couverture existante de `watcher` + `-race` suffit. Après
  extraction des phases de `syncPlaylist`, relancer un scénario de sync réel (ajout d'une watchlist)
  en local.

**Approche régression générale.** Un pas = un commit = un cycle CI complet (déjà en place :
tests + vet + golangci-lint + govulncheck + lint front + build Docker + scan Trivy). On ne cumule
jamais deux refactorings non vérifiés dans un même commit.

---

## 6. Findings terrain (observés en production, hors analyse statique)

> Ces points ne viennent pas de la lecture statique du code mais de l'observation du
> comportement réel en production. Provenance différente → section séparée, pour rester honnête sur
> la façon dont ils ont été trouvés.

### R10 — Le retag « incomplete-metadata » est **non-convergent** (sur-sélection sur `genre`)

**Observé en prod** (run complet du 13/07) : `scanned=2556 filled=17 skipped=2534 failed=5`.
**99 % des pistes sélectionnées comme « incomplètes » n'ont rien reçu.**

**Cause vérifiée.** La requête de sélection (`backend/db/tracks.go:190-193`) retient une piste dès
qu'**un** des 12 champs est vide, dont `genre`. Or `genre` n'est renseigné **que** via
ISRC→MusicBrainz (`api_admin.go` `retagOneTrack` : `catalogTrack.Genre = freshGenre` seulement
`if freshGenre != ""`), et MusicBrainz n'a de genre que pour une minorité de pistes. Donc la majorité
des pistes sont sélectionnées sur `genre=''`, re-fetchées, puis re-skippées car le genre reste
introuvable en amont.

**Conséquence.** Chaque exécution re-scanne ~2556 pistes, refait ~2556 lookups externes throttlés
(~40 min de run) pour ~17 remplissages utiles, et l'ensemble « needs retag » **ne décroît jamais**.
C'est du travail répété sans convergence — et si un jour un indicateur UI « X pistes incomplètes »
est ajouté, il restera bloqué haut en permanence.

**Pistes (à décider, non tranché) :**
- **Retirer `genre` (et éventuellement `copyright`) de la clause de sélection** — le genre est déjà
  « best effort » au download ; en faire un critère d'incomplétude rend le set non-convergent. Le
  plus simple et probablement le bon.
- OU mémoriser un `last_retag_at` par piste et ne pas re-sélectionner une piste re-tentée récemment
  sans succès (convergence par « on a déjà essayé »).
- OU distinguer « champ indisponible en amont » de « champ pas encore tenté ».

**Note secondaire (mineur).** 5 échecs dont 2 `HTTP 503` (token client Spotify, transitoire) : le
retag ne réessaie pas les erreurs transitoires → ces pistes restent `failed` jusqu'au prochain run.
Un simple retry/backoff sur 5xx suffirait, mais l'impact est faible.

**Bon signe au passage.** Ce run — comme le `library-rebuild` de 2556 fichiers — s'est terminé
proprement (`done`, pas de `context canceled`), ce qui **confirme une nouvelle fois** que la
conversion async 202+SSE tient sous charge réelle prolongée.

### R12 — Couche de résolution/proxy tierce dégradée en prod (I1/I2 en direct)

**Observé** pendant le téléchargement du playlist `/all` (2547 pistes), sources vérifiées par `grep`
de chaque message :
- `API returned status 401` (nu) → **Songlink** (song.link/odesli, `backend/songlink/client.go:181/290/427`).
- `Songlink Rate limited, skipping calls for 5 minutes` (429) → coupe-circuit 5 min
  (`songlink/client.go:46`) qui casse **en cascade** les pistes suivantes nécessitant une résolution
  ISRC (`songlink rate limited, skipping call`).
- `failed to get download URL: … 401 and fallbacks failed` → API Tidal officielle 401 **par design**
  (tokens client-TV privés du scope playback, cf. commentaire `tidal/client.go:209-212`), **et** tous
  les proxies communautaires (`util.GetTidalProxiesEffective()`) en échec simultané.
- `no streaming URLs found` / `tidal link not found` → pistes réellement absentes de Tidal (attendu).

**Diagnostic.** Dénominateur commun : la **couche de résolution/proxy tierce non officielle**
(Songlink + proxies Tidal communautaires), dégradée sur la fenêtre observée. C'est **I1/I2 du premier
audit** matérialisé en direct. Impact borné : seuls les *nouveaux* téléchargements échouent ; les
pistes déjà en bibliothèque ne sont pas touchées.

**À surveiller.** Le **401 Songlink** — song.link ne requiert normalement pas d'auth. Si c'est un
**changement d'API** (auth désormais requise) et non un hoquet transitoire, la résolution
Spotify→Tidal/Deezer serait cassée **durablement** → à trancher par une investigation quand ce sera
utile.

**Piste (mineure).** Pacing plus doux des appels Songlink sur les gros batches, pour éviter de
déclencher le 429/coupe-circuit qui pénalise ensuite tout le reste du batch.

**Validation positive au passage (M3U8).** La génération M3U8 du playlist `/all` a résolu
**2546/2547** pistes (99,96 %) via catalogue + tag + job — la chaîne rebuild→dédup→M3U8 fonctionne de
bout en bout sur une bibliothèque réelle. La seule `unresolved=1` est un orphelin (ni catalogue, ni
tag, ni job), vraisemblablement un des downloads échoués ci-dessus. **Nuance :** le conseil affiché
par le watcher (« run retag-legacy then library-rebuild ») ne récupère un orphelin **que si son
fichier existe sur disque** ; pour un download échoué le fichier n'existe pas, donc ni retag ni
rebuild n'y changent rien — il faut d'abord un téléchargement réussi. Le message de remédiation est
donc légèrement trompeur dans le cas « download échoué » (petit item UX/wording).

---

## 7. Dépendances & cadence de mise à jour (R11)

> Ajouté suite à la question « mettre à jour des dépendances comme ffmpeg pour des gains de perf ou
> de fonctionnalité ». Réponse constatée par lecture des versions réelles (Dockerfile, `go list -m
> -u all`, `package.json`), pas supposée.

### Réponse directe sur ffmpeg : **pas de gain ici.**
- ffmpeg est **déjà** un build statique BtbN épinglé à `autobuild-2026-07-11-13-13` (asset
  `ffmpeg-N-125519-g300cac3078-linux64-lgpl.tar.xz`), soit un snapshot du **master FFmpeg du
  11/07/2026** — quasi bleeding-edge.
- Surtout, la charge ffmpeg de l'app est du transcodage basique : FLAC→MP3 (`libmp3lame`), →AAC
  (encodeur natif), →ALAC (natif), + ffprobe pour métadonnées/analyse (`backend/audio/ffmpeg.go`).
  **Aucun filtre, pas de hwaccel, pas de loudnorm.** Ces encodeurs sont stables depuis des années →
  une version plus récente n'apporte ni perf ni fonctionnalité mesurable pour ce cas d'usage.
- (Le chemin de dev macOS local télécharge `afkarxyz/ffmpeg-binaries v8.0` — FFmpeg 8.0 stable, hors
  Docker ; sans impact prod.)

### Front-end : rien à faire.
Tout est déjà sur les derniers majeurs : React 19.2, Vite 7.3, Tailwind 4.2, ESLint 10, TypeScript
5.9, `@types/node` 25, Radix/lucide/motion récents. Aucune mise à jour en attente à valeur.

### Go : trois bumps réels, un seul à intérêt tangible.
| Dép (directe) | Actuel | Dispo | Intérêt |
|---|---|---|---|
| `modernc.org/sqlite` | 1.40.0 | 1.53.0 | **Le plus intéressant.** Driver du catalogue (dédup, freshness, sélection retag). Embarque un SQLite upstream plus récent : corrections + améliorations du planner. Gain perf **réel mais probablement marginal** vu la simplicité des requêtes ; la vraie raison est correctness / rester à jour. |
| `go.etcd.io/bbolt` | 1.4.3 | 1.5.0 | Store KV (jobs/auth/watchlists). Minor release avec fixes. Faible risque. |
| `golang.org/x/text` | 0.31.0 | 0.40.0 | Normalisation Unicode (noms de fichiers). Bump de routine. |

Transitives notables très en retard : `golang.org/x/net` (0.38→0.57), `golang.org/x/crypto`,
`golang.org/x/sys`. `govulncheck` est **vert** en CI (aucune vuln connue atteignable aujourd'hui),
mais ce sont les libs où atterrissent les correctifs HTTP/2 et crypto ; un `go get -u ./... && go mod
tidy` les remonterait au passage.

### Le vrai finding : **la cadence existe déjà — ce sont ses PR qui traînent.**
> ⚠️ **Correction.** Une première version de ce finding affirmait « I5 (Dependabot) toujours non
> fait ». **C'est faux, vérifié après coup** : `.github/dependabot.yml` existe et couvre `gomod` +
> `npm` + `docker` + `github-actions` (hebdomadaire). I5 **est fait**. L'erreur venait d'une reprise
> de mémoire du premier audit au lieu d'une vérification — corrigé ici.

L'épinglage est excellent pour la reproductibilité (images Docker par digest SHA, ffmpeg par tag daté
+ vérif checksum, lockfiles gelés), **et** Dependabot les bumpe bien : **11 PR ouvertes** au moment
de l'audit, dont exactement les 3 bumps Go identifiés ci-dessus. Le vrai finding n'est donc pas
« rien ne met à jour » mais « **les PR de bump s'accumulent sans être triées** » :

| PR | Bump | Risque / action |
|----|------|-----------------|
| #14 | `modernc.org/sqlite` 1.40→1.53 | Faible — **mergeable** dès CI verte (couverture catalogue/jobs = garde-fou) |
| #13 | `go.etcd.io/bbolt` 1.4.3→1.5.0 | Faible — mergeable dès CI verte |
| #16 | `golang.org/x/text` 0.31→0.40 | Faible — mergeable dès CI verte |
| #11 #12 #15 #10 | actions gh-release 2→3, trivy 0.35→0.36, github-script 7→9, checkout 4→7 | Très faible (CI uniquement) |
| #18 | **TypeScript 5.9→7.0** (majeur) | À **tester** — saut de major, peut casser le typecheck |
| #19 | **Vite 7→8** (majeur) | À tester — saut de major du bundler |
| #17 #20 | `@types/node` 25→26, `@vitejs/plugin-react` 5→6 (majeurs) | À tester avec #18/#19 |

### Recommandation concrète (triage, pas « activer Dependabot »)
1. **Backend, à faible risque** : merger #13, #14, #16 après CI verte (idéalement groupés, un cycle CI
   complet chacun) — c'est le lot « deps » à valeur, `modernc.org/sqlite` en tête.
2. **CI/actions** : #10/#11/#12/#15, risque quasi nul.
3. **Frontend majeurs** (#17/#18/#19/#20) : à traiter ensemble, avec `bun run build` + `bun run lint`
   + vérif navigateur, car TypeScript 7 et Vite 8 sont des sauts de major.
4. **ffmpeg** reste **hors cadence auto** : l'ARG `FFMPEG_BUILD_TAG` est un `curl` dans un `RUN`, pas
   un `FROM`, donc l'écosystème `docker` de Dependabot ne le voit pas. Comme établi plus haut, ce
   n'est pas gênant (rien à y gagner) — un bump manuel occasionnel du tag daté suffit.

---

## Annexe — méthode de vérification de cet audit

Chaque finding a été établi par lecture directe (numéros de ligne réels au moment de l'audit) :
inventaire par `wc -l` trié, signatures par `grep -nE "^func "`, comptage d'états par `grep -c
useState`, et lecture des corps concernés (`DownloadFile` tidal/qobuz, `SaveSettings`, dispatch
`downloader.go`). Les zones jugées « déjà propres » (`rpc.ts`, `providerutil`, `jobsStream.ts`) ont
été inspectées avec le même soin pour éviter un audit à charge — l'objectif est une carte fiable de
la dette **restante**, pas un catalogue de reproches.
