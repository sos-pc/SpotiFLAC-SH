package watcher

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	"github.com/sos-pc/SpotiFLAC-SH/internal/m3u8"
	"github.com/sos-pc/SpotiFLAC-SH/internal/settings"
	bolt "go.etcd.io/bbolt"
)

// bucketWatchlist is the watcher's own BoltDB bucket. It was declared in
// jobs.go, where the only use was creating it at startup — every read and write
// is here.
var bucketWatchlist = []byte("watchlist")

// maxSyncLogs caps the per-watchlist sync history. Two writers trim it —
// applySyncResult and OnBatchComplete — and they have to agree, so the bound is
// named rather than repeated.
const maxSyncLogs = 20

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type SyncLog struct {
	Time       time.Time `json:"time"`
	BatchID    string    `json:"batch_id,omitempty"`
	NewTracks  int       `json:"new_tracks"`
	Downloaded int       `json:"downloaded"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	Deleted    int       `json:"deleted"`
}

type WatchedPlaylist struct {
	ID            string           `json:"id"`
	SpotifyURL    string           `json:"spotify_url"`
	Name          string           `json:"name"`
	IntervalHours int              `json:"interval_hours"`
	Settings      jobs.JobSettings `json:"settings"`
	LastSync      time.Time        `json:"last_sync"`
	TrackIDs      []string         `json:"track_ids"`
	CreatedAt     time.Time        `json:"created_at"`
	SyncDeletions bool             `json:"sync_deletions"`
	SyncLogs      []SyncLog        `json:"sync_logs,omitempty"`
	UserID        string           `json:"user_id,omitempty"`
	// M3U8File is the playlist file this watchlist currently owns, as a bare
	// filename inside <downloadPath>/Playlists/. Empty means none has been
	// written yet, or the record predates this field.
	//
	// It exists because every other place that needed to find that file was
	// *recomputing* the name from the playlist's name and ID, and a recomputed
	// name is only right while nothing it derives from has changed. A rename on
	// Spotify, and later a change in how the name is built, both move the target
	// while the file on disk stays where it was — the cleanup then deletes
	// nothing and the old file is orphaned with no record that it ever existed.
	// Storing what was written turns "guess where it probably is" into "look".
	//
	// Costs nothing to add: watchlists are already persisted as JSON in BoltDB,
	// so existing records simply decode with an empty value and fill it on their
	// next write.
	M3U8File string `json:"m3u8_file,omitempty"`
	// CustomName overrides Name for anything a human reads: the playlist file,
	// and the label in the UI. Empty means "use whatever Spotify calls it".
	//
	// It cannot live in Name, because syncPlaylist overwrites that from Spotify
	// on every cycle (see the rename detection in syncPlaylist) — a name typed
	// here would survive until the next sync and no longer.
	CustomName string `json:"custom_name,omitempty"`
}

// EffectiveName is the name to show and to build the playlist filename from:
// what the user chose, or what Spotify calls it.
func (pl *WatchedPlaylist) EffectiveName() string {
	if pl.CustomName != "" {
		return pl.CustomName
	}
	return pl.Name
}

// watchlistJobSettings returns the JobSettings a watchlist's downloads run with.
// Watchlists now follow the user's GLOBAL server settings, not the per-watchlist
// WatchedPlaylist.Settings copy — that copy is legacy and was never exposed in
// the UI, so keeping it as a separate source of truth only created drift (the
// M3U8 CreateM3u8File check already read global settings while the path came
// from the copy). Service too follows the global downloader. See
// docs/settings-source-of-truth.md.
func (w *Watcher) watchlistJobSettings(pl *WatchedPlaylist) jobs.JobSettings {
	s := settings.EffectiveDownloadSettings(w.auth, pl.UserID)
	return settings.ServerJobSettings(s, s.Downloader)
}

// watchlistOutputRoot is the base download directory for a watchlist's M3U8 and
// scan operations — the user's global download path (default music dir if unset).
func (w *Watcher) watchlistOutputRoot(pl *WatchedPlaylist) string {
	if p := settings.EffectiveDownloadSettings(w.auth, pl.UserID).DownloadPath; p != "" {
		return p
	}
	return util.GetDefaultMusicPath()
}

type AddWatchlistRequest struct {
	SpotifyURL    string           `json:"spotify_url"`
	IntervalHours int              `json:"interval_hours"`
	Settings      jobs.JobSettings `json:"settings"`
	SyncDeletions bool             `json:"sync_deletions"`
	UserID        string           `json:"user_id,omitempty"`
}

type AddWatchlistResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Watcher
// ─────────────────────────────────────────────────────────────────────────────

type Watcher struct {
	// The watcher owns its own storage handles rather than borrowing the job
	// manager's. bucketWatchlist below is read and written here and nowhere
	// else, and the three catalog queries in watcher_catalog.go are the
	// watcher's own work — reaching through w.jm.db and w.jm.catalog to reach
	// them made the watchlist feature look like part of the job manager.
	db      *bolt.DB
	catalog *sql.DB

	jm      *jobs.JobManager
	auth    *auth.AuthManager
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex      // protège les écritures concurrentes sur les watchlists + syncing
	syncing map[string]bool // playlists en cours de sync (protégé par mu)
}

// NewWatcher crée et démarre le daemon de surveillance des playlists.
func NewWatcher(db *bolt.DB, catalog *sql.DB, jm *jobs.JobManager, auth *auth.AuthManager) *Watcher {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{
		db:      db,
		catalog: catalog,
		jm:      jm,
		auth:    auth,
		ctx:     ctx,
		cancel:  cancel,
		syncing: make(map[string]bool),
	}
	// Injecter le callback de résolution des settings actuels d'une watchlist.
	// processJob l'utilise pour ignorer les settings obsolètes figés dans le job.
	jm.WatchlistSettingsFunc = func(watchlistID string) (jobs.JobSettings, bool) {
		pl, err := w.GetWatchlistByID(watchlistID)
		if err != nil || pl == nil {
			return jobs.JobSettings{}, false
		}
		// Global settings, not pl.Settings — watchlists follow the user's
		// current global settings (see watchlistJobSettings).
		return w.watchlistJobSettings(pl), true
	}
	// Vérifier l'intégrité des M3U8 au démarrage (recovery après crash/redémarrage)
	util.SafeGo("watcher.startupM3U8Integrity", func() {
		playlists, err := w.GetWatchlists()
		if err != nil {
			return
		}
		for _, pl := range playlists {
			// Recovered per playlist so one bad playlist can't abort the
			// integrity check for every other one in this one-time
			// startup pass.
			func(pl WatchedPlaylist) {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("[Watcher] PANIC recovered checking M3U8 integrity", "playlist", pl.Name, "recover", r, "stack", string(debug.Stack()))
					}
				}()
				w.checkM3U8Integrity(pl)
			}(pl)
		}
	})
	util.SafeGo("watcher.daemon", w.daemon)
	slog.Info("[Watcher] Daemon started")
	return w
}

// Close arrête le daemon.
func (w *Watcher) Close() {
	w.cancel()
}

// daemon tourne en permanence et vérifie toutes les 5 minutes
// si des playlists doivent être synchronisées.
// Le cleanup périodique est délégué au JobManager (cleanupLoop).
func (w *Watcher) daemon() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Vérifier immédiatement au démarrage
	w.checkAllSafely()

	for {
		select {
		case <-w.ctx.Done():
			slog.Info("[Watcher] Daemon stopped")
			return
		case <-ticker.C:
			w.checkAllSafely()
		}
	}
}

// checkAllSafely recovers a panic from a single checkAll pass so it only
// skips that pass instead of permanently killing this goroutine — without
// it, an unrecovered panic here would silently stop watchlist syncing for
// the rest of the process's lifetime (nothing restarts this loop).
func (w *Watcher) checkAllSafely() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[Watcher] PANIC recovered in checkAll", "recover", r, "stack", string(debug.Stack()))
		}
	}()
	w.checkAll()
}

// checkAll parcourt toutes les playlists et lance une sync si nécessaire.
func (w *Watcher) checkAll() {
	playlists, err := w.GetWatchlists()
	if err != nil {
		return
	}

	if len(playlists) == 0 {
		return
	}

	for _, pl := range playlists {
		interval := time.Duration(pl.IntervalHours) * time.Hour
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		if time.Since(pl.LastSync) >= interval {
			util.SafeGo("watcher.syncPlaylist["+pl.ID+"]", func() { w.syncPlaylist(pl) })
		}
	}
}

// checkM3U8Integrity is a startup-only recovery hook: regenerate every
// watchlist M3U8 to absorb any drift accumulated while the server was
// offline (manual file moves, settings changes, partial crashes mid-batch).
// The write itself is atomic, but resolution can legitimately fail for some
// tracks (no catalog entry, no SPOTIFY_ID tag, no BoltDB job record left —
// exactly the state of a pre-existing library downloaded before tag
// embedding existed) — GenerateM3U8ForPlaylist guards against silently
// shrinking an already-good file when that happens, so running this
// unconditionally on every boot is safe.
func (w *Watcher) checkM3U8Integrity(pl WatchedPlaylist) {
	_, _ = w.GenerateM3U8ForPlaylist(pl.ID, false)
}

// syncPlaylist récupère les métadonnées Spotify, compare avec les tracks déjà
// connus, et enqueue uniquement les nouveaux.
//
// pl is a snapshot taken by the caller; see the merge under w.mu near the end
// for what that means and which fields it is allowed to write back.
func (w *Watcher) syncPlaylist(pl WatchedPlaylist) {
	// Empêcher les exécutions concurrentes pour la même playlist
	w.mu.Lock()
	if w.syncing[pl.ID] {
		w.mu.Unlock()
		slog.Debug("[Watcher] Sync already in progress, skipping", "playlist", pl.Name)
		return
	}
	w.syncing[pl.ID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.syncing, pl.ID)
		w.mu.Unlock()
	}()

	slog.Info("[Watcher] Syncing", "url", pl.SpotifyURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	data, err := spotify.GetFilteredSpotifyData(ctx, pl.SpotifyURL, true, time.Second)
	if err != nil {
		slog.Warn("[Watcher] Failed to fetch metadata", "url", pl.SpotifyURL, "err", err)
		return
	}

	tracks := ExtractTracksFromMetadata(data)
	if len(tracks) == 0 {
		slog.Warn("[Watcher] No tracks found", "url", pl.SpotifyURL)
		return
	}

	playlistName := extractPlaylistName(data)
	if playlistName == "" {
		playlistName = pl.Name
	}
	oldName := ""
	if playlistName != pl.Name {
		oldName = pl.Name // sauvegarder l'ancien nom pour nettoyer l'ancien M3U8
		pl.Name = playlistName
	}

	currentTrackIDs := make([]string, 0, len(tracks))
	for _, t := range tracks {
		if t.SpotifyID != "" {
			currentTrackIDs = append(currentTrackIDs, t.SpotifyID)
		}
	}

	knownIDs := make(map[string]bool, len(pl.TrackIDs))
	for _, id := range pl.TrackIDs {
		knownIDs[id] = true
	}

	// Vérifier que les fichiers considérés comme téléchargés existent encore sur disque.
	// Source unique : jobs BoltDB (StatusDone/StatusSkipped avec FilePath).
	w.recoverMissingFiles(&pl)
	// Reconstruire knownIDs après recoverMissingFiles (peut avoir modifié pl.TrackIDs)
	knownIDs = make(map[string]bool, len(pl.TrackIDs))
	for _, id := range pl.TrackIDs {
		knownIDs[id] = true
	}

	var newTracks []jobs.JobTrack
	var newIDs []string

	for i, track := range tracks {
		if track.SpotifyID == "" || knownIDs[track.SpotifyID] {
			continue
		}
		track.Position = i + 1
		track.PlaylistName = playlistName
		newTracks = append(newTracks, track)
		newIDs = append(newIDs, track.SpotifyID)
	}

	slog.Info("[Watcher] New tracks to download", "playlist", playlistName, "count", len(newTracks))

	// Before the SyncLog below, which records this batch's ID: enqueuing is what
	// produces it.
	var batchID string
	if len(newTracks) > 0 {
		result, err := w.jm.EnqueueBatch(jobs.EnqueueBatchRequest{
			Tracks:      newTracks,
			Settings:    w.watchlistJobSettings(&pl),
			WatchlistID: pl.ID,
			UserID:      pl.UserID,
		})
		if err != nil {
			slog.Error("[Watcher] EnqueueBatch failed", "playlist", playlistName, "err", err)
		} else {
			batchID = result.BatchID
		}
	}

	// Retry des jobs failed pour cette watchlist (après fetch Spotify réussi)
	// NOTE : intentionnellement absent du daemon — seulement sur refresh manuel (SyncWatchlist)

	// ── Sync deletions ──
	deletedCount := w.syncDeletions(&pl, currentTrackIDs)

	// ── SyncLog ──
	// Built here, appended under the lock below onto the record as it exists
	// by then — never onto this copy. OnBatchComplete writes the download
	// counters into SyncLogs while this sync is still draining its queue, and
	// appending here would carry those counters away at save time. That loss is
	// what the "standalone entry" fallback in OnBatchComplete was written to
	// paper over; with the save below merging instead of replacing, the entry it
	// looks for is still there.
	syncLog := SyncLog{
		Time:      time.Now(),
		BatchID:   batchID,
		NewTracks: len(newTracks),
		Deleted:   deletedCount,
	}

	// Rename Spotify : supprimer l'ancien M3U8 (le nouveau sera créé juste
	// après). Doit tourner AVANT saveWatchlist ci-dessous : ce dernier
	// persiste le nouveau pl.Name, donc si le process crash entre les deux,
	// le prochain sync ne verra plus playlistName != pl.Name (BoltDB a déjà
	// le nouveau nom) et oldName ne sera plus jamais recalculé — l'ancien
	// M3U8 resterait orphelin indéfiniment. Dans cet ordre, un crash avant
	// saveWatchlist refait détecter le rename au prochain sync (retry
	// naturel) ; un crash après ce bloc mais avant saveWatchlist ne fait
	// qu'un os.Remove redondant et sans danger sur un fichier déjà absent.
	w.mu.Lock()
	// This sync started from a snapshot of pl taken possibly hours ago — the
	// Spotify fetch and EnqueueBatch sit between the copy and here, and with one
	// job worker a large playlist drains slowly. Anything the user changed in
	// the meantime is newer than what this copy holds, so the record is re-read
	// and only the fields this sync owns are carried onto it.
	//
	// Saving the snapshot wholesale is what put `tout.m3u8` back on 2026-08-10:
	// a rename had moved the file and recorded the new name, and the sync
	// running at the time wrote the old name over it, orphaning the renamed file
	// and making the next generation recreate the old one. Same mechanism
	// reverted CustomName, IntervalHours, SyncDeletions and the SyncLogs
	// counters OnBatchComplete had just written.
	//
	// The existence check this replaced guarded one consequence of the same
	// staleness — resurrecting a watchlist deleted mid-sync — and a nil record
	// still covers it: there is nothing to merge onto, so nothing is written.
	fresh, err := w.GetWatchlistByID(pl.ID)
	if err != nil || fresh == nil {
		w.mu.Unlock()
		return
	}
	applySyncResult(fresh, &pl, newIDs, syncLog)

	// Spotify renamed the playlist: the third and last moment a filename is
	// decided. Only when the user has not chosen a name of their own — theirs
	// wins, and Spotify renaming something must not overwrite a deliberate
	// choice. Read from `fresh`, so a custom name typed during this sync is
	// seen; reading it from the snapshot would decide against a name the user
	// has already replaced.
	//
	// This replaced a delete-then-wait: the old file was removed here and the
	// new one only appeared at the next generation, leaving a window where the
	// playlist was simply absent from Jellyfin. applyM3U8Name renames instead.
	if oldName != "" && fresh.CustomName == "" {
		if all, err := w.GetWatchlists(); err == nil {
			w.applyM3U8Name(fresh, all)
		}
	}
	w.saveWatchlist(fresh)
	// Everything below this lock reads pl; hand it the record that was actually
	// persisted rather than the snapshot that was not.
	pl = *fresh
	w.mu.Unlock()

	// Mirror current state into the SQLite catalog: track stubs,
	// watchlist_tracks junction, and a snapshot when the contents have
	// changed (or this is the first sync). Best-effort.
	w.mirrorWatchlistToCatalog(&pl)

	// Régénérer le M3U8 systématiquement à chaque sync. La fonction est
	// idempotente (rename atomique) : si OnBatchComplete tourne ensuite à
	// la fin du batch, le dernier write gagne et le M3U8 reste cohérent.
	_, _ = w.GenerateM3U8ForPlaylist(pl.ID, false)

	// Notifier le frontend que la sync est terminée
	w.jm.Publish(jobs.JobEvent{
		Type: "watchlist_synced",
		Data: map[string]interface{}{
			"watchlist_id": pl.ID,
			"new_tracks":   len(newTracks),
			"deleted":      deletedCount,
			// The name a human reads, so the toast this feeds says what the UI
			// list says rather than reverting to Spotify's on every sync.
			"name": pl.EffectiveName(),
		},
	})
}

// syncDeletions handles tracks that have left this watchlist's Spotify
// playlist since the last sync. Such a track always drops out of pl.TrackIDs
// (pl is mutated in place); its downloaded file is deleted too, unless another
// watchlist still references the same Spotify ID. Returns the number of files
// deleted. No-op returning 0 when deletion-sync is disabled or the freshly
// fetched playlist came back empty. Extracted from syncPlaylist (R4).
func (w *Watcher) syncDeletions(pl *WatchedPlaylist, currentTrackIDs []string) int {
	if !pl.SyncDeletions || len(currentTrackIDs) == 0 {
		return 0
	}

	currentSet := make(map[string]bool)
	for _, id := range currentTrackIDs {
		currentSet[id] = true
	}
	// A read failure here used to be discarded, and an empty map reads exactly
	// like "no other watchlist wants any of these tracks" — so a transient
	// BoltDB error turned into deleting audio files that another watchlist was
	// still using. There is no safe way to proceed without this answer: refuse
	// the whole deletion pass and let the next sync retry it.
	allPlaylists, err := w.GetWatchlists()
	if err != nil {
		slog.Warn("[Watcher] Cannot read watchlists, skipping deletion pass rather than risk deleting shared files",
			"playlist", pl.Name, "err", err)
		return 0
	}
	otherWatchlistIDs := make(map[string]bool)
	for _, other := range allPlaylists {
		if other.ID == pl.ID {
			continue
		}
		for _, id := range other.TrackIDs {
			otherWatchlistIDs[id] = true
		}
	}

	// The same lookup generation uses, resolved once ahead of the loop.
	//
	// It used to read job.FilePath directly, which is how a File Manager rename
	// leaked files: the rename updated the catalog, the job kept the old path,
	// and os.Remove on a path nothing is at fails with ErrNotExist — deletion
	// reported success while the real file stayed on disk forever. Sharing one
	// resolver is what makes that impossible rather than patched.
	files := w.resolveTrackFiles(pl)
	// Invariant across the loop: it reads pl, which nothing here changes. It was
	// resolved inside the innermost branch, so every deleted file re-read the
	// user's settings to compute the same path.
	outputRoot := w.watchlistOutputRoot(pl)

	deletedCount := 0
	remainingIDs := make([]string, 0, len(pl.TrackIDs))
	for _, knownID := range pl.TrackIDs {
		if currentSet[knownID] {
			remainingIDs = append(remainingIDs, knownID)
			continue
		}
		// Track left THIS watchlist's Spotify playlist — it always
		// drops out of OUR TrackIDs (note: no append to remainingIDs
		// below, in either branch). Whether to physically delete the
		// underlying file is a separate question, gated by whether
		// another watchlist still wants it. The old code kept the ID
		// in remainingIDs when another watchlist still had it — which
		// meant it never actually left this watchlist's list, and if
		// the other watchlist later dropped it too, THAT watchlist's
		// own "is it in another playlist" check would see this stale
		// retention and also keep it — a permanent mutual deadlock
		// where a once-shared track could never be purged from either
		// watchlist again.
		inOtherPlaylist := otherWatchlistIDs[knownID]
		if inOtherPlaylist {
			slog.Debug("[Watcher] Track removed from playlist but present in another watchlist, skipping file deletion", "spotify_id", knownID, "playlist", pl.Name)
		} else if path := files[knownID]; path != "" {
			if err := os.Remove(path); err != nil {
				if !os.IsNotExist(err) {
					slog.Warn("[Watcher] Failed to delete file", "path", path, "err", err)
				}
				continue
			}
			slog.Info("[Watcher] Deleted file", "spotify_id", knownID, "path", path)
			removeEmptyParents(filepath.Dir(path), outputRoot)
			deletedCount++
			// The job record keeps pointing at where it put the file, and that
			// stays true: it did put it there. Clearing it was necessary while
			// job.FilePath was consulted as a location; it no longer is, and
			// every read of it stat-checks, so a path to a deleted file resolves
			// to nothing on its own.
		}
	}
	pl.TrackIDs = remainingIDs
	return deletedCount
}

// ─────────────────────────────────────────────────────────────────────────────
// CRUD watchlist
// ─────────────────────────────────────────────────────────────────────────────

func (w *Watcher) AddWatchlist(req AddWatchlistRequest) (AddWatchlistResponse, error) {
	if req.SpotifyURL == "" {
		return AddWatchlistResponse{}, fmt.Errorf("spotify URL is required")
	}

	if existing, err := w.findWatchlistBySource(req.SpotifyURL, req.UserID); err == nil && existing != nil {
		return AddWatchlistResponse{}, fmt.Errorf(
			"already watching this: %q, added %s — edit or remove it instead of adding a second copy",
			existing.Name, existing.CreatedAt.Format("2006-01-02"),
		)
	}

	if req.IntervalHours <= 0 {
		req.IntervalHours = 24
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	data, err := spotify.GetFilteredSpotifyData(ctx, req.SpotifyURL, true, time.Second)
	if err != nil {
		return AddWatchlistResponse{}, fmt.Errorf("failed to fetch playlist: %v", err)
	}

	name := extractPlaylistName(data)
	if name == "" {
		name = req.SpotifyURL
	}

	tracks := ExtractTracksFromMetadata(data)
	trackIDs := make([]string, 0, len(tracks))
	for _, t := range tracks {
		if t.SpotifyID != "" {
			trackIDs = append(trackIDs, t.SpotifyID)
		}
	}

	pl := &WatchedPlaylist{
		ID:            fmt.Sprintf("watch-%d", time.Now().UnixNano()),
		SpotifyURL:    req.SpotifyURL,
		Name:          name,
		IntervalHours: req.IntervalHours,
		Settings:      req.Settings,
		LastSync:      time.Now(),
		TrackIDs:      trackIDs,
		CreatedAt:     time.Now(),
		SyncDeletions: req.SyncDeletions,
		UserID:        req.UserID,
	}

	// Decide the filename now, while the set of watchlists is in hand and a
	// collision can be resolved once. Nothing recomputes it afterwards, so a
	// watchlist added later cannot silently move this one's file.
	//
	// No refusal here: the name came from Spotify and the caller did not choose
	// it, so the ladder disambiguates quietly. Refusing would block an add that
	// the user has no way to fix.
	// Failing the add is deliberate. Swallowing the error left M3U8File empty,
	// and an empty M3U8File is not "decide it later" — nothing recomputes it, so
	// m3u8FileFor falls back to the legacy scheme and the playlist wears a hash
	// suffix for the rest of its life over one transient read. Nothing has been
	// written at this point, so the user simply adds it again.
	existing, err := w.GetWatchlists()
	if err != nil {
		return AddWatchlistResponse{}, fmt.Errorf("cannot read watchlists to name this one: %w", err)
	}
	pl.M3U8File = decideM3U8Name(pl.EffectiveName(), pl.UserID, pl.ID,
		existing, w.watchlistOutputRoot(pl), w.watchlistOutputRoot, w.userLabel) + ".m3u8"

	if err := w.saveWatchlist(pl); err != nil {
		return AddWatchlistResponse{}, fmt.Errorf("failed to save watchlist: %v", err)
	}

	if len(tracks) > 0 {
		for i := range tracks {
			tracks[i].PlaylistName = name
			tracks[i].Position = i + 1
		}
		batchReq := jobs.EnqueueBatchRequest{
			Tracks:      tracks,
			Settings:    w.watchlistJobSettings(pl),
			WatchlistID: pl.ID,
			UserID:      pl.UserID,
		}
		util.SafeGo("watcher.enqueueBatch["+pl.ID+"]", func() { w.jm.EnqueueBatch(batchReq) })
	}

	slog.Info("[Watcher] Added watchlist", "name", name, "tracks", len(tracks), "interval_hours", req.IntervalHours)

	return AddWatchlistResponse{
		ID:      pl.ID,
		Name:    name,
		Message: fmt.Sprintf("Watching '%s' — %d tracks enqueued", name, len(tracks)),
	}, nil
}

// RemoveWatchlist deletes the record first, then cleans up the files it owned.
//
// That order is the point. Cleaning up first left a window in which a sync or a
// batch completion — both of which regenerate the M3U8 from the record — could
// rewrite the file between its deletion and the record's, leaving a playlist in
// Jellyfin that nothing owned any more. Deleting the record first closes the
// window structurally rather than by widening the lock: every producer resolves
// the watchlist before writing, and none of them find it.
//
// Only the record deletion holds w.mu. The cleanup below can remove thousands of
// audio files, and holding the watchlist lock across that would stall every
// other watchlist operation for its duration.
func (w *Watcher) RemoveWatchlist(id string) error {
	w.mu.Lock()
	playlists, err := w.GetWatchlists()
	if err != nil {
		w.mu.Unlock()
		// Deleting the record while unable to read it would strand its files
		// and its M3U8 with nothing left that knows they existed.
		return fmt.Errorf("cannot read watchlists to remove %s: %w", id, err)
	}
	var pl *WatchedPlaylist
	otherIDs := make(map[string]bool)
	for i := range playlists {
		if playlists[i].ID == id {
			pl = &playlists[i]
			continue
		}
		for _, tid := range playlists[i].TrackIDs {
			otherIDs[tid] = true
		}
	}
	if pl == nil {
		w.mu.Unlock()
		return nil // already gone: deleting twice is not an error
	}
	if err := w.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchlist)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(id))
	}); err != nil {
		w.mu.Unlock()
		return err
	}
	w.mu.Unlock()

	outputRoot := w.watchlistOutputRoot(pl)

	// ── Audio files, only when the watchlist was set to sync deletions ──
	//
	// Resolved the same way generation and syncDeletions resolve, rather than by
	// walking jobs for their FilePath: one lookup, one answer, so removing a
	// watchlist cannot delete a different file from the one its playlist listed.
	//
	// Keyed on pl.TrackIDs rather than on every job that ever named this
	// watchlist. Those are the tracks it owns at the moment it is removed; a
	// track dropped earlier already went through syncDeletions, which either
	// deleted its file or deliberately kept it for another watchlist.
	if pl.SyncDeletions {
		files := w.resolveTrackFiles(pl)
		for _, spotifyID := range pl.TrackIDs {
			path := files[spotifyID]
			if path == "" {
				continue
			}
			if otherIDs[spotifyID] {
				slog.Debug("[Watcher] Track in another watchlist, skipping file deletion", "spotify_id", spotifyID)
				continue
			}
			if err := os.Remove(path); err != nil {
				if !os.IsNotExist(err) {
					slog.Warn("[Watcher] Failed to delete file (watchlist removed)", "path", path, "err", err)
				}
				continue
			}
			slog.Info("[Watcher] Deleted file (watchlist removed)", "spotify_id", spotifyID, "path", path)
			removeEmptyParents(filepath.Dir(path), outputRoot)
		}
	}

	// ── The M3U8, always, regardless of SyncDeletions ──
	//
	// Deliberately NOT gated on CreateM3u8File, for the same reason as the
	// rename cleanup: the setting governs writing, not tidying up. Gating it
	// is how `all [957f2ab0].m3u8` survived its watchlist by 27 days —
	// removed while the setting was off, then nothing knew about it again.
	playlistsDir := filepath.Join(outputRoot, m3u8.PlaylistsDirName)
	// The recorded name when there is one — it is the only name that is
	// certainly right. Otherwise the two computed forms, for watchlists that
	// predate M3U8File: the current rule and the pre-disambiguation one, for a
	// watchlist removed before it ever re-synced after that migration. Removing
	// a file that does not exist is free, so trying both costs nothing.
	//
	// Not three candidates: m3u8FileFor() returns M3U8File verbatim when it is
	// set, so listing both asked the filesystem to delete the same path twice.
	var candidates []string
	if pl.M3U8File != "" {
		candidates = []string{filepath.Join(playlistsDir, pl.M3U8File)}
	} else {
		candidates = []string{
			filepath.Join(playlistsDir, pl.m3u8FileFor()),
			filepath.Join(playlistsDir, legacyM3U8BaseName(pl.Name)+".m3u8"),
		}
	}
	for _, m3u8Path := range candidates {
		if err := os.Remove(m3u8Path); err == nil {
			slog.Info("[Watcher] Deleted M3U8 (watchlist removed)", "path", m3u8Path)
		} else if !os.IsNotExist(err) {
			slog.Warn("[Watcher] Failed to delete M3U8 (watchlist removed)", "path", m3u8Path, "err", err)
		}
	}
	if entries, err := os.ReadDir(playlistsDir); err == nil && len(entries) == 0 {
		if err := os.Remove(playlistsDir); err == nil {
			slog.Info("[Watcher] Deleted empty Playlists dir", "path", playlistsDir)
		}
	}
	return nil
}

func (w *Watcher) GetWatchlists() ([]WatchedPlaylist, error) {
	var playlists []WatchedPlaylist
	err := w.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchlist)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var pl WatchedPlaylist
			if err := json.Unmarshal(v, &pl); err != nil {
				// Skipping is right — one unreadable record must not hide every
				// other watchlist — but doing it in silence is not. A watchlist
				// that stops syncing, vanishes from the UI and leaves its files
				// behind, with nothing anywhere saying why, is indistinguishable
				// from one the user deleted.
				slog.Error("[Watcher] Unreadable watchlist record, skipped",
					"key", string(k), "err", err)
				return nil
			}
			playlists = append(playlists, pl)
			return nil
		})
	})
	return playlists, err
}

// GetWatchlistByID reads one record by key.
//
// It used to load every watchlist and scan the slice, which meant decoding all
// of them — 73 KB on the reference deployment, most of it one playlist's 2561
// track IDs — to return one. That is invisible at three watchlists and stops
// being invisible as they accumulate, and this sits on hot paths: the job
// manager resolves a watchlist's settings through it once per download.
//
// A record that fails to decode now surfaces its error instead of reading as
// "not found", which is what the slice scan did: GetWatchlists skips undecodable
// rows silently, so a corrupted watchlist looked exactly like an absent one.
func (w *Watcher) GetWatchlistByID(id string) (*WatchedPlaylist, error) {
	var pl WatchedPlaylist
	found := false
	err := w.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchlist)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(id))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &pl)
	})
	if err != nil {
		return nil, fmt.Errorf("watchlist %s: %w", id, err)
	}
	if !found {
		return nil, fmt.Errorf("watchlist not found: %s", id)
	}
	return &pl, nil
}

func (w *Watcher) GetWatchlistsByUser(userID string) ([]WatchedPlaylist, error) {
	all, err := w.GetWatchlists()
	if err != nil {
		return nil, err
	}
	if userID == "" {
		return all, nil
	}
	var filtered []WatchedPlaylist
	for _, pl := range all {
		if pl.UserID == userID || pl.UserID == "" {
			filtered = append(filtered, pl)
		}
	}
	return filtered, nil
}

// applySyncResult carries onto `fresh` the fields a sync owns, and nothing else.
//
// It exists because syncPlaylist works from a snapshot taken before a Spotify
// fetch and a batch enqueue — hours, on a large playlist with one job worker —
// and used to save that snapshot whole. Everything the user or another handler
// changed in between was reverted: the filename and custom name (which is how
// `tout.m3u8` came back on 2026-08-10), the interval, the deletion toggle, and
// the download counters OnBatchComplete writes into SyncLogs.
//
// Three fields transfer, and only because the sync recomputed them from the two
// sources that decide them:
//
//   - Name: what Spotify calls the playlist now.
//   - TrackIDs: recoverMissingFiles and syncDeletions have already rebuilt this
//     from the disk and from Spotify's current contents, so the snapshot's copy
//     is the newer one despite being older. newIDs are the tracks just enqueued.
//   - LastSync: this sync is the event being recorded.
//
// The SyncLog is appended to `fresh`'s list rather than the snapshot's, so an
// entry OnBatchComplete updated while this sync ran survives to be found.
//
// Nothing here reads `fresh` before overwriting it, so it is safe to call with
// the two pointing at the same record.
func applySyncResult(fresh, snapshot *WatchedPlaylist, newIDs []string, log SyncLog) {
	fresh.Name = snapshot.Name
	fresh.TrackIDs = append(snapshot.TrackIDs, newIDs...)
	fresh.LastSync = time.Now()

	fresh.SyncLogs = append(fresh.SyncLogs, log)
	if len(fresh.SyncLogs) > maxSyncLogs {
		fresh.SyncLogs = fresh.SyncLogs[len(fresh.SyncLogs)-maxSyncLogs:]
	}
}

// setM3U8File records which playlist file this watchlist owns, touching only
// that field.
//
// Read-modify-write inside one transaction rather than saveWatchlist, because
// the caller is GenerateM3U8ForPlaylist: it works on a copy it loaded itself,
// and runs from four places including a startup hook and a batch-completion
// handler, either of which can overlap a running sync. Writing the whole struct
// back from there would persist a snapshot that is stale in TrackIDs, LastSync
// and everything else the sync just updated. BoltDB serialises writers, so
// re-reading inside the Update is enough to be safe.
//
// A watchlist removed between the read and this call is left alone rather than
// resurrected — the same hazard saveWatchlist's caller guards against.
func (w *Watcher) setM3U8File(watchlistID, filename string) error {
	return w.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchlist)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(watchlistID))
		if raw == nil {
			return nil
		}
		var cur WatchedPlaylist
		if err := json.Unmarshal(raw, &cur); err != nil {
			return err
		}
		if cur.M3U8File == filename {
			return nil
		}
		cur.M3U8File = filename
		data, err := json.Marshal(&cur)
		if err != nil {
			return err
		}
		return b.Put([]byte(watchlistID), data)
	})
}

func (w *Watcher) saveWatchlist(pl *WatchedPlaylist) error {
	return w.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketWatchlist)
		if err != nil {
			return err
		}
		data, err := json.Marshal(pl)
		if err != nil {
			return err
		}
		return b.Put([]byte(pl.ID), data)
	})
}

// SyncWatchlist déclenche une synchronisation manuelle de la watchlist :
//  1. Nouveaux tracks Spotify → EnqueueBatch
//  2. Retry des jobs failed avec les settings actuels de la watchlist
//     (corrige les jobs créés avec d'anciens settings obsolètes)
func (w *Watcher) SyncWatchlist(id string) error {
	pl, err := w.GetWatchlistByID(id)
	if err != nil {
		return err
	}
	util.SafeGo("watcher.syncPlaylist["+pl.ID+"]", func() { w.syncPlaylist(*pl) })
	// Retry des failed uniquement sur refresh manuel, avec les settings à jour
	if requeued, err := w.jm.RequeueFailedJobs(id, w.watchlistJobSettings(pl)); err == nil && requeued > 0 {
		slog.Info("[Watcher] SyncWatchlist: failed jobs requeued", "count", requeued, "playlist", pl.Name)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// JobEventHandler — implémentation
// ─────────────────────────────────────────────────────────────────────────────

// OnPermanentFailure implémente JobEventHandler.
// Retire le track des TrackIDs pour qu'il soit réessayé au prochain sync.
func (w *Watcher) OnPermanentFailure(watchlistID, spotifyID string) {
	w.RemoveTrackID(watchlistID, spotifyID)
}

// OnManualBatchComplete implémente JobEventHandler pour les lots NON-watchlist.
//
// C'est ici que se ferme le trou de l'étape 5 : le client écrivait le M3U8 juste
// après l'ENQUEUE, à partir du seul contrôle d'existence, donc il ne listait que
// les pistes déjà présentes — une playlist entièrement neuve n'en obtenait
// aucun. Le lot a maintenant un vrai moment de fin, côté serveur, et le fichier
// est écrit à partir des pistes réellement arrivées sur le disque. Ça marche
// aussi quand l'onglet a été fermé entre-temps.
//
// Même écriture partagée que les watchlists (m3u8.WriteToPlaylistsDir), donc même
// dossier et même convention de nommage. Le garde anti-rétrécissement est
// toujours actif ici : un lot manuel est par nature un sous-ensemble, il ne doit
// jamais remplacer le fichier complet par une version partielle.
func (w *Watcher) OnManualBatchComplete(req jobs.BatchM3U8Request, paths []string) {
	if len(paths) == 0 {
		return
	}
	// Same rule as the watchlist path: an album already exists as an album in
	// the library, so a playlist listing exactly its tracks is the same content
	// filed a second time. This is the producer the operator was actually
	// looking at — three of the albums in their Playlists tab came from here,
	// not from watchlists, which the first attempt at this missed entirely.
	//
	// SourceID carries the Spotify URL the download started from: App.tsx passes
	// `spotifyUrl` down as the batch's identity, so the same detection works on
	// both producers with no new plumbing. An artist discography keeps its
	// playlist, for the reason given on isAlbumSource.
	if isAlbumSource(req.SourceID) {
		slog.Info("[M3U8] manual batch is an album, no playlist written", "name", req.Name)
		return
	}

	// A playlist that is already watched has an owner, and it is not this batch.
	// The watchlist's own generation resolves every track it knows about; a
	// manual batch only ever holds the subset just downloaded, so writing from
	// here produced a second, poorer file beside the first — two entries in
	// Jellyfin for one playlist, which is what "why are there two /all?" was.
	// Regenerating the watchlist's file is the useful thing to do instead: the
	// tracks that just landed are exactly what it was missing.
	if existing, err := w.findWatchlistBySource(req.SourceID, req.UserID); err != nil {
		slog.Warn("[M3U8] cannot tell whether this source is watched, writing nothing",
			"name", req.Name, "err", err)
		return
	} else if existing != nil {
		slog.Info("[M3U8] source already watched, refreshing its playlist instead of writing a second one",
			"name", req.Name, "watchlist", existing.Name)
		_, _ = w.GenerateM3U8ForPlaylist(existing.ID, false)
		return
	}

	settings := settings.EffectiveDownloadSettings(w.auth, req.UserID)
	if !settings.CreateM3u8File {
		return
	}
	root := settings.DownloadPath
	if root == "" {
		root = util.GetDefaultMusicPath()
	}

	// The same escalation ladder the watchlists use, instead of an unconditional
	// hash suffix. m3u8BaseName always appended one, which is why every playlist
	// downloaded from the search bar arrived in Jellyfin wearing eight hex digits
	// — the very thing the watchlist path was changed to stop doing, left in
	// place on the other producer.
	//
	// The key is the Spotify entity, not req.SourceID verbatim: the raw URL
	// carries a `?si=…` that changes between shares, and hashing that would give
	// the same playlist a different name each time it collided.
	_, entityID := spotify.ParseEntityRef(req.SourceID)
	selfKey := "manual-" + entityID
	all, err := w.GetWatchlists()
	if err != nil {
		slog.Warn("[M3U8] cannot read watchlists to check for a name collision, writing nothing",
			"name", req.Name, "err", err)
		return
	}
	// root as-is, not filepath.Clean'd: decideM3U8Name compares it against what
	// watchlistOutputRoot returns for the other watchlists, and that is the raw
	// setting. Cleaning one side only would make "/music/" and "/music" look
	// like different roots and let a real collision through. The write below
	// still cleans it, which is where it matters.
	baseName := decideM3U8Name(req.Name, req.UserID, selfKey,
		all, root, w.watchlistOutputRoot, w.userLabel)
	result, err := m3u8.WriteToPlaylistsDir(
		filepath.Clean(root), baseName, settings.JellyfinMusicPath, paths, len(paths), true,
	)
	if err != nil {
		slog.Error("[M3U8] manual batch write failed", "playlist", req.Name, "err", err)
		return
	}
	if result.Written {
		slog.Info("[M3U8] manual batch written", "file", baseName+".m3u8", "entries", len(paths))
	}
}

// OnBatchComplete implémente JobEventHandler.
// Trouve le SyncLog par BatchID, met à jour ses compteurs, génère le M3U8.
func (w *Watcher) OnBatchComplete(watchlistID, batchID string, downloaded, skipped, failed int) {
	// Locked: same reasoning as UpdateWatchlist — this read-modify-write
	// of the watchlist record must not interleave with syncPlaylist's own
	// end-of-sync save. Scoped to just the BoltDB read/save, same as
	// syncPlaylist itself, so the slower M3U8 regeneration below doesn't
	// hold up other watchlist operations.
	w.mu.Lock()
	playlists, err := w.GetWatchlists()
	if err != nil {
		w.mu.Unlock()
		return
	}
	var matchedID string
	for _, pl := range playlists {
		if pl.ID != watchlistID {
			continue
		}
		// Trouver le SyncLog correspondant au batchID plutôt que le dernier.
		found := false
		if batchID != "" {
			for i := range pl.SyncLogs {
				if pl.SyncLogs[i].BatchID == batchID {
					pl.SyncLogs[i].Downloaded = downloaded
					pl.SyncLogs[i].Skipped = skipped
					pl.SyncLogs[i].Failed = failed
					found = true
					break
				}
			}
		}
		if !found {
			// jobWorkers=1 serializes every download across every
			// watchlist through one shared queue, so a large batch can
			// take far longer to drain than 20 sync cycles' worth of
			// time — long enough for the SyncLogs cap to have already
			// evicted this batch's original entry by the time it
			// finishes. Append a standalone entry instead of silently
			// dropping these counts: a slightly duplicated-looking log
			// line beats losing the result entirely.
			pl.SyncLogs = append(pl.SyncLogs, SyncLog{
				Time:       time.Now(),
				BatchID:    batchID,
				Downloaded: downloaded,
				Skipped:    skipped,
				Failed:     failed,
			})
			if len(pl.SyncLogs) > maxSyncLogs {
				pl.SyncLogs = pl.SyncLogs[len(pl.SyncLogs)-maxSyncLogs:]
			}
		}
		if saveErr := w.saveWatchlist(&pl); saveErr != nil {
			slog.Error("[Watcher] Failed to save sync log", "err", saveErr)
		}
		matchedID = pl.ID
		break
	}
	w.mu.Unlock()

	if matchedID != "" {
		_, _ = w.GenerateM3U8ForPlaylist(matchedID, false)
	}
}

// RemoveTrackID retire un spotify_id des TrackIDs d'une watchlist (appelé après échec permanent).
func (w *Watcher) RemoveTrackID(watchlistID, spotifyID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	playlists, err := w.GetWatchlists()
	if err != nil {
		return
	}
	var pl *WatchedPlaylist
	for i := range playlists {
		if playlists[i].ID == watchlistID {
			pl = &playlists[i]
			break
		}
	}
	if pl == nil {
		return
	}
	newIDs := pl.TrackIDs[:0]
	for _, id := range pl.TrackIDs {
		if id != spotifyID {
			newIDs = append(newIDs, id)
		}
	}
	pl.TrackIDs = newIDs
	// The whole point of this function is that the track comes back on the next
	// sync. If the save fails it does not, and the caller — OnPermanentFailure —
	// has no return value to check, so an unlogged failure means a track quietly
	// never retried again.
	if err := w.saveWatchlist(pl); err != nil {
		slog.Error("[Watcher] Could not drop the failed track, it will not be retried",
			"spotify_id", spotifyID, "playlist", pl.Name, "err", err)
		return
	}
	slog.Debug("[Watcher] Track removed from TrackIDs, will retry next sync", "spotify_id", spotifyID, "playlist", pl.Name)
}

func toRawBytes(data interface{}) []byte {
	if s, ok := data.(string); ok {
		return []byte(s)
	}
	raw, _ := json.Marshal(data)
	return raw
}

// removeEmptyParents remonte l'arborescence depuis dir et supprime chaque
// répertoire vide rencontré, jusqu'à stopAt (exclus).
// Sécurité : ne remonte jamais au-delà de stopAt ni à la racine du FS.
func removeEmptyParents(dir, stopAt string) {
	dir = filepath.Clean(dir)
	stopAt = filepath.Clean(stopAt)
	for {
		// Ne pas remonter au-delà de stopAt ni à la racine
		if dir == stopAt || dir == filepath.Dir(dir) {
			break
		}
		if !strings.HasPrefix(dir, stopAt+string(filepath.Separator)) {
			break
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		slog.Debug("[Watcher] Deleted empty dir", "path", dir)
		dir = filepath.Dir(dir)
	}
}

func ExtractTracksFromMetadata(data interface{}) []jobs.JobTrack {
	raw := toRawBytes(data)
	if raw == nil {
		return nil
	}

	var playlistPayload struct {
		TrackList []struct {
			SpotifyID   string `json:"spotify_id"`
			Name        string `json:"name"`
			Artists     string `json:"artists"`
			AlbumName   string `json:"album_name"`
			AlbumArtist string `json:"album_artist"`
			ReleaseDate string `json:"release_date"`
			Images      string `json:"images"`
			TrackNumber int    `json:"track_number"`
			DiscNumber  int    `json:"disc_number"`
			DurationMs  int    `json:"duration_ms"`
		} `json:"track_list"`
	}
	if err := json.Unmarshal(raw, &playlistPayload); err == nil && len(playlistPayload.TrackList) > 0 {
		return convertTracks(playlistPayload.TrackList)
	}

	var flatTracks []struct {
		SpotifyID   string `json:"spotify_id"`
		Name        string `json:"name"`
		Artists     string `json:"artists"`
		AlbumName   string `json:"album_name"`
		AlbumArtist string `json:"album_artist"`
		ReleaseDate string `json:"release_date"`
		Images      string `json:"images"`
		TrackNumber int    `json:"track_number"`
		DiscNumber  int    `json:"disc_number"`
		DurationMs  int    `json:"duration_ms"`
	}
	if err := json.Unmarshal(raw, &flatTracks); err == nil && len(flatTracks) > 0 {
		return convertTracks(flatTracks)
	}

	var playlist struct {
		Playlist struct {
			Name   string `json:"name"`
			Tracks []struct {
				SpotifyID   string `json:"spotify_id"`
				Name        string `json:"name"`
				Artists     string `json:"artists"`
				AlbumName   string `json:"album_name"`
				AlbumArtist string `json:"album_artist"`
				ReleaseDate string `json:"release_date"`
				Images      string `json:"images"`
				TrackNumber int    `json:"track_number"`
				DiscNumber  int    `json:"disc_number"`
				TotalTracks int    `json:"total_tracks"`
				TotalDiscs  int    `json:"total_discs"`
				Copyright   string `json:"copyright"`
				Publisher   string `json:"publisher"`
				DurationMs  int    `json:"duration_ms"`
			} `json:"tracks"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(raw, &playlist); err == nil && len(playlist.Playlist.Tracks) > 0 {
		return convertTracks(playlist.Playlist.Tracks)
	}

	var album struct {
		Album struct {
			Name   string `json:"name"`
			Tracks []struct {
				SpotifyID   string `json:"spotify_id"`
				Name        string `json:"name"`
				Artists     string `json:"artists"`
				AlbumName   string `json:"album_name"`
				AlbumArtist string `json:"album_artist"`
				ReleaseDate string `json:"release_date"`
				Images      string `json:"images"`
				TrackNumber int    `json:"track_number"`
				DiscNumber  int    `json:"disc_number"`
				TotalTracks int    `json:"total_tracks"`
				TotalDiscs  int    `json:"total_discs"`
				Copyright   string `json:"copyright"`
				Publisher   string `json:"publisher"`
				DurationMs  int    `json:"duration_ms"`
			} `json:"tracks"`
		} `json:"album"`
	}
	if err := json.Unmarshal(raw, &album); err == nil && len(album.Album.Tracks) > 0 {
		return convertTracks(album.Album.Tracks)
	}

	var single struct {
		Track struct {
			SpotifyID   string `json:"spotify_id"`
			Name        string `json:"name"`
			Artists     string `json:"artists"`
			AlbumName   string `json:"album_name"`
			AlbumArtist string `json:"album_artist"`
			ReleaseDate string `json:"release_date"`
			Images      string `json:"images"`
			TrackNumber int    `json:"track_number"`
			DiscNumber  int    `json:"disc_number"`
			TotalTracks int    `json:"total_tracks"`
			TotalDiscs  int    `json:"total_discs"`
			Copyright   string `json:"copyright"`
			Publisher   string `json:"publisher"`
			DurationMs  int    `json:"duration_ms"`
		} `json:"track"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.Track.SpotifyID != "" {
		t := single.Track
		return []jobs.JobTrack{{
			SpotifyID:   t.SpotifyID,
			TrackName:   t.Name,
			ArtistName:  t.Artists,
			AlbumName:   t.AlbumName,
			AlbumArtist: t.AlbumArtist,
			ReleaseDate: t.ReleaseDate,
			CoverURL:    t.Images,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.DiscNumber,
			TotalTracks: t.TotalTracks,
			TotalDiscs:  t.TotalDiscs,
			Copyright:   t.Copyright,
			Publisher:   t.Publisher,
			DurationMs:  t.DurationMs,
		}}
	}

	return nil
}

// extractPlaylistName returns the playlist's own name, never its owner's.
// The distinction needs stating because the field it ends up reading is called
// Owner.Name and holds the playlist name anyway — see the fallback below.
func extractPlaylistName(data interface{}) string {
	raw := toRawBytes(data)
	if raw == nil {
		return ""
	}

	var result struct {
		PlaylistInfo struct {
			Owner struct {
				DisplayName string `json:"display_name"`
				Name        string `json:"name"`
			} `json:"owner"`
		} `json:"playlist_info"`
		AlbumInfo struct {
			Name string `json:"name"`
		} `json:"album_info"`
		ArtistInfo struct {
			Name string `json:"name"`
		} `json:"artist_info"`
		Playlist struct {
			Name string `json:"name"`
		} `json:"playlist"`
		Album struct {
			Name string `json:"name"`
		} `json:"album"`
		Track struct {
			Name string `json:"name"`
		} `json:"track"`
	}

	if err := json.Unmarshal(raw, &result); err != nil {
		return ""
	}

	// PlaylistInfo.Owner.Name contient le nom de la playlist (pas PlaylistInfo.Name)
	if result.PlaylistInfo.Owner.Name != "" {
		return result.PlaylistInfo.Owner.Name
	}
	if result.AlbumInfo.Name != "" {
		return result.AlbumInfo.Name
	}
	if result.ArtistInfo.Name != "" {
		return result.ArtistInfo.Name
	}
	if result.Playlist.Name != "" {
		return result.Playlist.Name
	}
	if result.Album.Name != "" {
		return result.Album.Name
	}
	return result.Track.Name
}

// convertTracks est un helper générique pour convertir n'importe quelle slice
// de structs anonymes en []JobTrack via JSON round-trip.
func convertTracks(tracks interface{}) []jobs.JobTrack {
	raw, err := json.Marshal(tracks)
	if err != nil {
		return nil
	}

	var items []struct {
		SpotifyID   string `json:"spotify_id"`
		Name        string `json:"name"`
		Artists     string `json:"artists"`
		AlbumName   string `json:"album_name"`
		AlbumArtist string `json:"album_artist"`
		ReleaseDate string `json:"release_date"`
		Images      string `json:"images"`
		TrackNumber int    `json:"track_number"`
		DiscNumber  int    `json:"disc_number"`
		TotalTracks int    `json:"total_tracks"`
		TotalDiscs  int    `json:"total_discs"`
		Copyright   string `json:"copyright"`
		Publisher   string `json:"publisher"`
		DurationMs  int    `json:"duration_ms"`
	}

	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}

	result := make([]jobs.JobTrack, 0, len(items))
	for _, t := range items {
		if t.SpotifyID == "" {
			continue
		}
		artistName := strings.TrimSpace(t.Artists)
		result = append(result, jobs.JobTrack{
			SpotifyID:   t.SpotifyID,
			TrackName:   t.Name,
			ArtistName:  artistName,
			AlbumName:   t.AlbumName,
			AlbumArtist: t.AlbumArtist,
			ReleaseDate: t.ReleaseDate,
			CoverURL:    t.Images,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.DiscNumber,
			TotalTracks: t.TotalTracks,
			TotalDiscs:  t.TotalDiscs,
			Copyright:   t.Copyright,
			Publisher:   t.Publisher,
			DurationMs:  t.DurationMs,
		})
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateWatchlist
// ─────────────────────────────────────────────────────────────────────────────

type UpdateWatchlistRequest struct {
	ID            string `json:"id"`
	IntervalHours int    `json:"interval_hours"`
	SyncDeletions bool   `json:"sync_deletions"`
	// CustomName is a pointer so the three states stay distinct: absent leaves
	// the name alone, "" clears it back to Spotify's, and a value sets it. A
	// plain string could not express "clear" without meaning "unchanged".
	CustomName *string `json:"custom_name,omitempty"`
}

// ErrNameTaken is returned when a rename would give two watchlists in the same
// directory the same filename. Mapped to 409 by the HTTP layer, because it is
// the caller's input that is wrong, not the server.
var ErrNameTaken = errors.New("a watchlist with that name already exists here — pick another")

func (w *Watcher) UpdateWatchlist(req UpdateWatchlistRequest) error {
	// Locked: without this, a settings change landing mid-sync could be
	// silently overwritten by syncPlaylist's own end-of-sync save of its
	// stale in-memory copy of this same record.
	w.mu.Lock()
	defer w.mu.Unlock()

	playlists, err := w.GetWatchlists()
	if err != nil {
		return err
	}
	for _, pl := range playlists {
		if pl.ID == req.ID {
			if req.CustomName != nil && strings.TrimSpace(*req.CustomName) != pl.CustomName {
				pl.CustomName = strings.TrimSpace(*req.CustomName)

				// Refuse rather than silently disambiguate. At creation the name
				// comes from Spotify and nobody chose it, so the ladder resolves
				// it quietly; here a human just typed it and can be told.
				//
				// Compared against the names already decided, not against names
				// recomputed for every other watchlist: those are stored, so
				// this is one pass over a list already in hand.
				want := decideM3U8Name(pl.EffectiveName(), pl.UserID, pl.ID,
					playlists, w.watchlistOutputRoot(&pl), w.watchlistOutputRoot, w.userLabel) + ".m3u8"
				root := w.watchlistOutputRoot(&pl)
				for i := range playlists {
					other := &playlists[i]
					if other.ID == pl.ID || isAlbumSource(other.SpotifyURL) {
						continue
					}
					if w.watchlistOutputRoot(other) == root && other.m3u8FileFor() == want {
						return ErrNameTaken
					}
				}
				// Moves the file straight away. The name is what Jellyfin shows,
				// so a rename that only takes effect at the next sync — hours
				// away — reads as having done nothing at all.
				w.applyM3U8Name(&pl, playlists)
			}
			if req.IntervalHours > 0 {
				pl.IntervalHours = req.IntervalHours
			}
			pl.SyncDeletions = req.SyncDeletions
			return w.saveWatchlist(&pl)
		}
	}
	return fmt.Errorf("watchlist not found: %s", req.ID)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetWatchlistStats
// ─────────────────────────────────────────────────────────────────────────────

type WatchlistStats struct {
	WatchlistID string  `json:"watchlist_id"`
	TotalTracks int     `json:"total_tracks"`
	Downloaded  int     `json:"downloaded"`
	Skipped     int     `json:"skipped"`
	Failed      int     `json:"failed"`
	Pending     int     `json:"pending"`
	TotalSizeMB float64 `json:"total_size_mb"`
}

// GetWatchlistStats reports per-track counts and total size for a
// watchlist. Downloaded/size is resolved primarily against the SQLite
// catalog (library_files, status='present') rather than BoltDB jobs:
// CleanupOldJobs prunes job rows every 24h, but the catalog row for a
// track survives indefinitely and holds its real on-disk file_size — a
// job's TotalSize can also go stale after a later quality-upgrade
// re-download landed at the same path. Without the catalog fallback, a
// playlist whose jobs had mostly aged out reported both a plausible-ish
// downloaded count (via the jobless-track-is-skipped fallback below) and
// a wildly understated total_size_mb, since size can only ever be summed
// from a job that still exists.
func (w *Watcher) GetWatchlistStats(watchlistID string) (WatchlistStats, error) {
	jm := w.jm
	stats := WatchlistStats{WatchlistID: watchlistID}

	pl, err := w.GetWatchlistByID(watchlistID)
	if err != nil {
		return stats, err
	}
	stats.TotalTracks = len(pl.TrackIDs)

	trackIDSet := make(map[string]bool, len(pl.TrackIDs))
	for _, id := range pl.TrackIDs {
		trackIDSet[id] = true
	}

	jobList, err := jm.GetAllJobs()
	if err != nil {
		return stats, err
	}

	// Dédupliquer par SpotifyID : garder le job le plus récent par track
	// encore présente dans la watchlist.
	latest := make(map[string]jobs.Job)
	for _, j := range jobList {
		if j.WatchlistID != watchlistID || j.SpotifyID == "" || !trackIDSet[j.SpotifyID] {
			continue
		}
		if prev, ok := latest[j.SpotifyID]; !ok || j.UpdatedAt.After(prev.UpdatedAt) {
			latest[j.SpotifyID] = j
		}
	}

	catalogSizes := w.catalogFileSizesForWatchlist(pl)

	for _, id := range pl.TrackIDs {
		if size, ok := catalogSizes[id]; ok {
			stats.Downloaded++
			stats.TotalSizeMB += float64(size) / (1024 * 1024)
			continue
		}
		j, hasJob := latest[id]
		if !hasJob {
			if w.catalog != nil {
				// Le catalogue est actif et n'a rien pour cette track :
				// elle n'a vraiment pas encore été téléchargée.
				stats.Pending++
			} else {
				// Pas de catalogue pour trancher : on garde l'ancien
				// comportement (track sans job = déjà téléchargée avant
				// l'activation du tracking, ou job nettoyé par
				// CleanupOldJobs).
				stats.Skipped++
			}
			continue
		}
		switch j.Status {
		case jobs.StatusDone:
			stats.Downloaded++
			stats.TotalSizeMB += j.TotalSize
		case jobs.StatusSkipped:
			stats.Skipped++
			stats.TotalSizeMB += j.TotalSize
		case jobs.StatusFailed:
			stats.Failed++
		case jobs.StatusPending, jobs.StatusDownloading:
			stats.Pending++
		}
	}

	return stats, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CheckWatchlistFreshness
// ─────────────────────────────────────────────────────────────────────────────

// WatchlistFreshnessReport answers "is this watchlist actually up to date?"
// — comparing the live Spotify playlist against what's locally tracked,
// verifying every locally-known track still has a real file backing it
// (not just a catalog/job row that might be stale), and checking the M3U8
// file itself isn't lagging behind what's resolvable. Read-only: unlike
// SyncWatchlist, this never enqueues downloads, deletes files, or mutates
// pl.TrackIDs/pl.LastSync — it only reports.
type WatchlistFreshnessReport struct {
	UpToDate           bool      `json:"up_to_date"`
	TotalTracks        int       `json:"total_tracks"`
	NewOnSpotify       int       `json:"new_on_spotify"`       // on Spotify now, not yet tracked locally
	RemovedFromSpotify int       `json:"removed_from_spotify"` // tracked locally, no longer on Spotify
	MissingFiles       int       `json:"missing_files"`        // tracked locally, but no verified file resolves on disk
	Pending            int       `json:"pending"`              // currently queued/downloading (from GetWatchlistStats)
	Failed             int       `json:"failed"`               // failed downloads (from GetWatchlistStats)
	M3U8Enabled        bool      `json:"m3u8_enabled"`
	M3U8EntryCount     int       `json:"m3u8_entry_count,omitempty"`
	M3U8Stale          bool      `json:"m3u8_stale,omitempty"` // M3U8 on disk has fewer entries than are actually resolvable right now
	CheckedAt          time.Time `json:"checked_at"`
}

// CheckWatchlistFreshness fetches the current Spotify playlist (same call
// syncPlaylist makes) and gathers everything computeFreshnessReport needs
// to diff it against pl.TrackIDs — including file presence verified the
// same way M3U8 generation does (resolveTrackPaths: catalog → filesystem
// tag scan → BoltDB, each stat-checked), so a catalog row pointing at a
// file deleted outside SpotiFLAC is correctly reported as missing rather
// than trusted at face value.
func (w *Watcher) CheckWatchlistFreshness(id string) (WatchlistFreshnessReport, error) {
	pl, err := w.GetWatchlistByID(id)
	if err != nil {
		return WatchlistFreshnessReport{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := spotify.GetFilteredSpotifyData(ctx, pl.SpotifyURL, true, time.Second)
	if err != nil {
		return WatchlistFreshnessReport{}, fmt.Errorf("fetch spotify metadata: %w", err)
	}
	spotifyTracks := ExtractTracksFromMetadata(data)
	spotifyTrackIDs := make([]string, 0, len(spotifyTracks))
	for _, t := range spotifyTracks {
		spotifyTrackIDs = append(spotifyTrackIDs, t.SpotifyID)
	}

	outputDir := w.watchlistOutputRoot(pl)
	resolved, _ := w.resolveTrackPaths(pl, outputDir)

	var pending, failed int
	if stats, statsErr := w.GetWatchlistStats(id); statsErr == nil {
		pending, failed = stats.Pending, stats.Failed
	}

	m3u8Enabled := w.loadM3U8Settings(pl) != nil
	var m3u8Count int
	var m3u8Exists bool
	if m3u8Enabled {
		playlistDir := filepath.Join(outputDir, "Playlists")
		m3u8Path := filepath.Join(playlistDir, pl.m3u8FileFor())
		m3u8Count, m3u8Exists = m3u8.CountEntries(m3u8Path)
	}

	return computeFreshnessReport(
		pl.TrackIDs, spotifyTrackIDs, len(resolved),
		pending, failed, m3u8Enabled, m3u8Count, m3u8Exists,
	), nil
}

// computeFreshnessReport is the pure comparison logic behind
// CheckWatchlistFreshness, factored out so it's unit-testable without a
// live Spotify fetch: given what CheckWatchlistFreshness already gathered
// (current Spotify track IDs, how many local tracks resolved to a real
// file, download stats, and the M3U8's on-disk entry count), it computes
// the diff counts and the final up-to-date verdict.
func computeFreshnessReport(
	localTrackIDs, spotifyTrackIDs []string,
	resolvedCount int,
	pending, failed int,
	m3u8Enabled bool,
	m3u8EntryCount int,
	m3u8Exists bool,
) WatchlistFreshnessReport {
	spotifyIDs := make(map[string]bool, len(spotifyTrackIDs))
	for _, id := range spotifyTrackIDs {
		if id != "" {
			spotifyIDs[id] = true
		}
	}
	localIDs := make(map[string]bool, len(localTrackIDs))
	for _, id := range localTrackIDs {
		localIDs[id] = true
	}

	report := WatchlistFreshnessReport{
		TotalTracks: len(localTrackIDs),
		Pending:     pending,
		Failed:      failed,
		M3U8Enabled: m3u8Enabled,
		CheckedAt:   time.Now(),
	}
	for sid := range spotifyIDs {
		if !localIDs[sid] {
			report.NewOnSpotify++
		}
	}
	for lid := range localIDs {
		if !spotifyIDs[lid] {
			report.RemovedFromSpotify++
		}
	}
	// resolveTrackPaths skips (never pads) unresolved IDs, so the shortfall
	// vs len(localTrackIDs) is exactly the count of tracks with no
	// verified file — never negative.
	report.MissingFiles = len(localTrackIDs) - resolvedCount

	if m3u8Enabled {
		if m3u8Exists {
			report.M3U8EntryCount = m3u8EntryCount
			report.M3U8Stale = m3u8EntryCount < resolvedCount
		} else {
			// No M3U8 on disk yet despite having resolvable tracks — also
			// stale, just reported via M3U8Stale rather than a separate flag.
			report.M3U8Stale = resolvedCount > 0
		}
	}

	report.UpToDate = report.NewOnSpotify == 0 && report.RemovedFromSpotify == 0 &&
		report.MissingFiles == 0 && report.Pending == 0 && report.Failed == 0 && !report.M3U8Stale

	return report
}

// ─────────────────────────────────────────────────────────────────────────────
// GetWatchlistHistory
// ─────────────────────────────────────────────────────────────────────────────

type WatchlistHistoryItem struct {
	TrackName  string  `json:"track_name"`
	ArtistName string  `json:"artist_name"`
	AlbumName  string  `json:"album_name"`
	Status     string  `json:"status"`
	TotalSize  float64 `json:"total_size"`
	UpdatedAt  int64   `json:"updated_at"`
	FilePath   string  `json:"file_path"`
	Error      string  `json:"error,omitempty"`
}

// GetWatchlistHistory lists this watchlist's download attempts, newest first.
func (w *Watcher) GetWatchlistHistory(watchlistID string) ([]WatchlistHistoryItem, error) {
	jobList, err := w.jm.GetAllJobs()
	if err != nil {
		return nil, err
	}
	var items []WatchlistHistoryItem
	for _, j := range jobList {
		if j.WatchlistID != watchlistID {
			continue
		}
		items = append(items, WatchlistHistoryItem{
			TrackName:  j.TrackName,
			ArtistName: j.ArtistName,
			AlbumName:  j.AlbumName,
			Status:     string(j.Status),
			TotalSize:  j.TotalSize,
			UpdatedAt:  j.UpdatedAt.Unix(),
			FilePath:   j.FilePath,
			Error:      j.Error,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	return items, nil
}

// recoverMissingFiles vérifie que les fichiers considérés comme téléchargés existent
// encore sur disque. Source : jobs BoltDB (StatusDone/StatusSkipped avec FilePath).
// Les tracks dont le fichier a disparu sont retirés de pl.TrackIDs pour être
// re-téléchargés au prochain sync.
func (w *Watcher) recoverMissingFiles(pl *WatchedPlaylist) {
	if w.jm == nil {
		return
	}
	jobList, err := w.jm.GetAllJobs()
	if err != nil {
		return
	}

	// Garder le job le plus récent par SpotifyID pour cette watchlist
	latestJob := make(map[string]jobs.Job)
	for _, job := range jobList {
		if job.WatchlistID != pl.ID || job.FilePath == "" {
			continue
		}
		if job.Status != jobs.StatusDone && job.Status != jobs.StatusSkipped {
			continue
		}
		key := job.SpotifyID
		if key == "" {
			continue
		}
		if prev, ok := latestJob[key]; !ok || job.UpdatedAt.After(prev.UpdatedAt) {
			latestJob[key] = job
		}
	}

	// Set des TrackIDs pour filtrer les jobs qui appartiennent encore à cette playlist
	trackIDSet := make(map[string]bool, len(pl.TrackIDs))
	for _, id := range pl.TrackIDs {
		trackIDSet[id] = true
	}

	var missingIDs []string
	for spotifyID, job := range latestJob {
		if !trackIDSet[spotifyID] {
			continue
		}
		if _, err := os.Stat(job.FilePath); err != nil {
			slog.Info("[Watcher] File missing, will re-download", "spotify_id", spotifyID, "path", job.FilePath)
			missingIDs = append(missingIDs, spotifyID)
		}
	}

	if len(missingIDs) > 0 {
		missingSet := make(map[string]bool, len(missingIDs))
		for _, id := range missingIDs {
			missingSet[id] = true
		}
		filtered := make([]string, 0, len(pl.TrackIDs))
		for _, id := range pl.TrackIDs {
			if !missingSet[id] {
				filtered = append(filtered, id)
			}
		}
		pl.TrackIDs = filtered
		slog.Info("[Watcher] Missing file(s) will be re-queued", "count", len(missingIDs), "playlist", pl.Name)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// M3U8 generation pour Jellyfin
// ─────────────────────────────────────────────────────────────────────────────

// GenerateM3U8ForPlaylist writes the M3U8 file for a watchlist by resolving
// each Spotify ID in pl.TrackIDs against the filesystem, via the SPOTIFY_ID
// tag embedded in audio files at download time. BoltDB jobs are used only
// as a fallback for legacy files that don't yet carry the tag.
//
// The filesystem is the source of truth: the M3U8 reflects what is actually
// on disk, in the order Spotify reports the playlist. Files moved, renamed,
// or copied keep working as long as the tag is preserved.
//
// force=true bypasses the shrink-guard (see m3u8.ShouldSkipShrinkingWrite) and
// always writes the freshly-resolved list, even if it's smaller than what's
// currently on disk. Used by the explicit per-watchlist repair action,
// where the user has just asked to reconcile the M3U8 with reality — unlike
// the automatic triggers (sync, batch-complete, startup), which must never
// silently destroy a better existing file, an explicit repair should show
// the true current state even if that state got worse (e.g. a file was
// deleted outside SpotiFLAC).
func (w *Watcher) GenerateM3U8ForPlaylist(watchlistID string, force bool) (m3u8.GenerationResult, error) {
	pl, err := w.GetWatchlistByID(watchlistID)
	if err != nil || pl == nil {
		return m3u8.GenerationResult{}, fmt.Errorf("watchlist not found: %s", watchlistID)
	}

	// An album is not a playlist, and it is already an album. Downloads land in
	// <Artist>/<Album>/, which Jellyfin indexes as an album on its own, so an
	// M3U8 for an album watchlist put the identical content in the Playlists tab
	// a second time — three of the eight playlists on the reference deployment
	// (2026-08-08) were albums duplicating albums. Tracking, syncing and
	// downloading are unaffected; only the redundant playlist file stops being
	// written. Files written before this rule existed stay until their watchlist
	// is removed — there is no sweep to collect them, see the note above
	// legacyM3U8BaseName for why.
	//
	// Note this covers album *watchlists* only. Albums downloaded from the search
	// bar go through OnManualBatchComplete, which is a different producer and
	// still writes a playlist for them.
	if isAlbumSource(pl.SpotifyURL) {
		return m3u8.GenerationResult{Skipped: true, SkipReason: "album watchlist"}, nil
	}

	settings := w.loadM3U8Settings(pl)
	if settings == nil {
		return m3u8.GenerationResult{}, fmt.Errorf("M3U8 generation is disabled (createM3u8File setting)")
	}

	outputDir := w.watchlistOutputRoot(pl)

	paths, unresolvedIDs := w.resolveTrackPaths(pl, outputDir)
	result := m3u8.GenerationResult{
		Total:      len(pl.TrackIDs),
		Resolved:   len(paths),
		Unresolved: len(unresolvedIDs),
	}
	if len(paths) == 0 {
		if result.Total > 0 {
			slog.Warn("[Watcher] M3U8: 0 tracks resolved, nothing written; run POST /api/v1/admin/retag-legacy then POST /api/v1/admin/library-rebuild, or use the Repair button on this watchlist",
				"playlist", pl.Name, "total", result.Total)
		}
		return result, nil
	}

	// resolveTrackPaths can legitimately fail to resolve some of pl.TrackIDs
	// — no catalog entry, no SPOTIFY_ID tag, and no BoltDB job record left
	// (exactly the state of a pre-existing library downloaded before tag
	// embedding existed). sync_deletions already removed any genuinely
	// deleted track from pl.TrackIDs before this function runs, so
	// len(paths) < len(pl.TrackIDs) here always means a resolution gap, not
	// an intentional shrink. Refuse to overwrite a bigger existing file with
	// a smaller one in that case (unless force) — every automatic call site
	// (startup integrity check, post-sync, post-batch) used to regenerate
	// unconditionally, so a single unresolved track anywhere in the
	// fallback chain would silently clobber an otherwise-complete M3U8 on
	// the very next event, and the startup hook meant every container
	// restart re-triggered it.
	//
	// baseName includes a watchlist-ID-derived suffix (m3u8BaseName) so two
	// watchlists whose names collide after sanitization (e.g. "AC/DC Hits"
	// and "AC:DC Hits") get distinct files instead of silently overwriting
	// each other every sync.
	baseName := strings.TrimSuffix(pl.m3u8FileFor(), ".m3u8")

	// If the name this watchlist should write to has moved since last time —
	// Spotify renamed the playlist, or the naming rule changed under it — the
	// file it used to own is now stale and nothing else will ever look for it
	// again. Remove it before writing the new one, using the recorded name
	// rather than a recomputed guess, which is the whole point of recording it.
	if desired := baseName + ".m3u8"; pl.M3U8File != "" && pl.M3U8File != desired {
		old := filepath.Join(outputDir, m3u8.PlaylistsDirName, pl.M3U8File)
		if err := os.Remove(old); err == nil {
			slog.Info("[Watcher] M3U8: playlist file moved, removed the previous one",
				"from", pl.M3U8File, "to", desired)
		} else if !os.IsNotExist(err) {
			slog.Warn("[Watcher] M3U8: cannot remove the previous file",
				"file", pl.M3U8File, "err", err)
		}
	}

	if result.Unresolved > 0 {
		// The IDs, not just how many. A count says a problem exists and gives
		// nothing to act on: "1 unresolved" sat in this deployment's logs for
		// days while the track behind it went unidentified, and until this
		// commit it also triggered a full library walk on every generation.
		// Capped, because a library that lost its catalog should not print
		// thousands of lines.
		sample := unresolvedIDs
		if len(sample) > unresolvedSampleLimit {
			sample = sample[:unresolvedSampleLimit]
		}
		slog.Warn("[Watcher] M3U8: tracks unresolved (no catalog row, no job record); run POST /api/v1/admin/retag-legacy then POST /api/v1/admin/library-rebuild to recover them",
			"playlist", pl.Name, "unresolved", result.Unresolved, "total", len(pl.TrackIDs),
			"spotify_ids", sample)
	}

	// The shrink guard applies only when tracks failed to resolve: a
	// fully-resolved shorter list means the playlist genuinely shrank, and
	// sync_deletions has already pruned pl.TrackIDs before we get here.
	result, err = m3u8.WriteToPlaylistsDir(outputDir, baseName, settings.JellyfinPath,
		paths, len(pl.TrackIDs), result.Unresolved > 0 && !force)
	if err != nil {
		return result, fmt.Errorf("failed to create %s: %w", pl.Name, err)
	}
	if result.Skipped {
		return result, nil
	}
	slog.Info("[Watcher] M3U8 written", "file", baseName+".m3u8", "entries", len(paths))

	// Backfill only, for records written before M3U8File existed: their name
	// came from m3u8FileFor's fallback, and recording it turns the next
	// cleanup's guess into a lookup. Only this field is touched — see
	// setM3U8File for why the whole record must not be saved from here.
	//
	// Deliberately not a write in the general case. Generating does not DECIDE
	// the name — creation, an explicit rename and a Spotify rename do, which is
	// what m3u8FileFor documents — and baseName above is simply what those
	// decided, read back. Writing it unconditionally made generation a fourth
	// writer of a field with three deciders, and one that works from a value
	// read before the file was written: a rename landing in between was
	// reverted here. Equivalent whenever the record already has a name, since
	// setM3U8File returns early on an unchanged value; the difference is only
	// that the stale case can no longer overwrite a newer decision.
	if pl.M3U8File == "" {
		if err := w.setM3U8File(pl.ID, baseName+".m3u8"); err != nil {
			slog.Warn("[Watcher] M3U8: written but not recorded, cleanup will fall back to guessing",
				"file", baseName+".m3u8", "err", err)
		}
	}

	playlistDir := filepath.Join(outputDir, m3u8.PlaylistsDirName)
	m3u8Path := filepath.Join(playlistDir, baseName+".m3u8")

	// One-time migration cleanup: remove the pre-disambiguation file (no ID
	// suffix) now that the new-format one has been written successfully.
	// Safe even if it was shared with another colliding watchlist — that
	// content was unreliable anyway (whichever watchlist synced last had
	// silently overwritten it), and every affected watchlist gets a fresh,
	// correctly-attributed file on its own next sync.
	legacyPath := filepath.Join(playlistDir, legacyM3U8BaseName(pl.Name)+".m3u8")
	if legacyPath != m3u8Path {
		if err := os.Remove(legacyPath); err == nil {
			slog.Info("[Watcher] M3U8: migrated legacy file, old file removed", "playlist", pl.Name)
		}
	}
	return result, nil
}

// m3u8BaseName returns the .m3u8-free base filename SpotiFLAC uses for a
// watchlist's Jellyfin playlist file: the sanitized playlist name plus a
// short, watchlist-ID-derived suffix. The suffix exists because two
// watchlists can have names that collide after SanitizeFilename strips
// forbidden characters (e.g. "AC/DC Hits" and "AC:DC Hits" both sanitize
// to "AC DC Hits") — without it, both watchlists would write to the same
// file, and whichever synced last would silently overwrite the other's
// playlist on every cycle.
func m3u8BaseName(playlistName, watchlistID string) string {
	safeName := util.SanitizeFilename(playlistName)
	if safeName == "" {
		safeName = "playlist"
	}
	return fmt.Sprintf("%s [%s]", safeName, watchlistIDSuffix(watchlistID))
}

// watchlistIDSuffix derives a short (8 hex char), stable, filename-safe
// suffix from a watchlist ID via SHA-256, rather than truncating the ID
// directly (format "watch-<unix-nano>") — a hash spreads collisions evenly
// instead of concentrating them on IDs created close together in time.
func watchlistIDSuffix(watchlistID string) string {
	sum := sha256.Sum256([]byte(watchlistID))
	return hex.EncodeToString(sum[:4])
}

// sameSpotifySource reports whether two references point at the same Spotify
// entity, comparing extracted IDs rather than the strings.
//
// The stored URL is whatever was pasted, and the same playlist arrives in
// several shapes: with or without `?si=…` (production carries it), as a
// `spotify:` URI, or localised. Comparing raw strings would call two of those
// different.
//
// Unrecognised references are never a match. Failing open here costs a
// duplicate watchlist, which is recoverable; failing closed would refuse a
// legitimate one for a URL shape nobody anticipated, which reads as the feature
// being broken.
func sameSpotifySource(a, b string) bool {
	kindA, idA := spotify.ParseEntityRef(a)
	kindB, idB := spotify.ParseEntityRef(b)
	if idA == "" || idB == "" {
		return false
	}
	return kindA == kindB && idA == idB
}

// findWatchlistBySource returns this user's watchlist tracking the same Spotify
// entity, or nil.
//
// Scoped per user on purpose: two accounts tracking the same playlist are two
// legitimate watchlists with their own settings, download paths and files.
// AddWatchlist had no duplicate check of any kind, which is how a deployment
// ended up with two entries for one playlist, two IDs, and two M3U8 files —
// one of which then stopped syncing and sat stale for weeks.
func (w *Watcher) findWatchlistBySource(spotifyURL, userID string) (*WatchedPlaylist, error) {
	all, err := w.GetWatchlists()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].UserID != userID {
			continue
		}
		if sameSpotifySource(all[i].SpotifyURL, spotifyURL) {
			return &all[i], nil
		}
	}
	return nil, nil
}

// sanitizedEmpty is whatever SanitizeFilename produces for nothing at all.
// Read from the function rather than written out, so it cannot drift from it.
var sanitizedEmpty = util.SanitizeFilename("")

// decideM3U8Name returns the .m3u8-free filename for a playlist, given every
// watchlist that exists and a way to resolve a user ID to a display label.
//
// It takes the three values it needs rather than a *WatchedPlaylist, because
// both M3U8 producers have to reach the same answer and only one of them has a
// watchlist: a manual batch has a display name, an owner and a Spotify entity,
// and nothing else. Passing a struct forced that caller to fabricate one, or —
// as it did until now — to skip this function entirely and name its files by a
// different rule.
//
// selfKey identifies the caller so it does not collide with itself: a
// watchlist's ID, or a stable key derived from the manual batch's entity. It is
// also what the hash suffix is derived from, so the same playlist keeps the same
// name across runs.
//
// The suffix is now applied only when it is needed, in escalating order:
//
//	Release Radar                    nothing else claims that name
//	Release Radar (methammer)        another account's watchlist claims it
//	Release Radar [830f8305]         the same account claims it twice
//
// It used to be unconditional, which is why every filename carried eight hex
// digits nobody could read. The suffix was never noise though — Spotify's
// personalised playlists (Release Radar, Discover Weekly) are per-account, so
// two accounts genuinely produce two different playlists with one name, and
// without disambiguation one would silently overwrite the other every sync. The
// account label says which is which; the hash only appears where no readable
// distinction exists.
//
// Only watchlists sharing this one's output directory can collide with it: the
// download path is per-user, so two accounts writing to different roots are not
// in each other's way. Album watchlists write no file at all and are skipped.
//
// Making the name depend on the live set of watchlists is safe only because
// each watchlist records the file it owns (M3U8File): when the set changes and
// a name moves, the write path removes the file it used to have. Without that
// record this would strand a file on every collision.
func decideM3U8Name(displayName, ownerID, selfKey string, all []WatchedPlaylist, root string,
	rootOf func(*WatchedPlaylist) string, labelOf func(userID string) string) string {

	safe := util.SanitizeFilename(displayName)
	if safe == "" {
		safe = "playlist"
	}

	sameName, sameNameAndUser := false, false
	for i := range all {
		other := &all[i]
		if other.ID == selfKey || isAlbumSource(other.SpotifyURL) {
			continue
		}
		if rootOf(other) != root {
			continue
		}
		otherSafe := util.SanitizeFilename(other.EffectiveName())
		if otherSafe == "" {
			otherSafe = "playlist"
		}
		if otherSafe != safe {
			continue
		}
		sameName = true
		if other.UserID == ownerID {
			sameNameAndUser = true
		}
	}

	switch {
	case sameNameAndUser:
		return fmt.Sprintf("%s [%s]", safe, watchlistIDSuffix(selfKey))
	case sameName:
		// Both halves matter. SanitizeFilename never returns an empty string —
		// it substitutes a placeholder — so testing its output alone would hand
		// every unlabelled user the same word and collide the very files this
		// is disambiguating. Caught by TestM3U8NameEscalation, which is why the
		// raw label is checked first and the sanitised one is compared against
		// that placeholder rather than against "".
		if raw := labelOf(ownerID); raw != "" {
			if label := util.SanitizeFilename(raw); label != sanitizedEmpty {
				return fmt.Sprintf("%s (%s)", safe, label)
			}
		}
		// No usable label — fall back rather than collide.
		return fmt.Sprintf("%s [%s]", safe, watchlistIDSuffix(selfKey))
	default:
		return safe
	}
}

// m3u8FileFor is the file this watchlist owns.
//
// The name is decided at three moments — creation, an explicit rename, and
// Spotify renaming the playlist — and stored. Everywhere else just reads it.
// It used to be recomputed from the live set of watchlists on every write,
// which made one watchlist's filename a function of what the others were doing
// and cost a full scan per call.
//
// The fallback covers watchlists that predate the field: the old scheme was
// deterministic, so what their file is called is recoverable rather than lost.
func (pl *WatchedPlaylist) m3u8FileFor() string {
	if pl.M3U8File != "" {
		return pl.M3U8File
	}
	return m3u8BaseName(pl.Name, pl.ID) + ".m3u8"
}

// applyM3U8Name decides this watchlist's filename, moves the file on disk if it
// changed, and records it. Returns the name in use.
//
// os.Rename rather than delete-and-rewrite: the content is already correct, and
// renaming keeps the playlist present in Jellyfin throughout instead of making
// it vanish until the next generation.
//
// Callers hold w.mu, and must also have read `pl` under that same lock. Holding
// the lock is not on its own enough: it serialises writers, it does not refresh
// a record read before it was taken. syncPlaylist satisfied the first half and
// not the second for months — decided a name against a snapshot hours old, and
// the save that followed reverted a rename the user had made in between.
//
// It records the decision on pl but does not persist it; the caller's save does.
func (w *Watcher) applyM3U8Name(pl *WatchedPlaylist, all []WatchedPlaylist) string {
	want := decideM3U8Name(pl.EffectiveName(), pl.UserID, pl.ID,
		all, w.watchlistOutputRoot(pl), w.watchlistOutputRoot, w.userLabel) + ".m3u8"
	current := pl.m3u8FileFor()
	if current == want {
		pl.M3U8File = want
		return want
	}

	dir := filepath.Join(w.watchlistOutputRoot(pl), m3u8.PlaylistsDirName)
	if err := os.Rename(filepath.Join(dir, current), filepath.Join(dir, want)); err == nil {
		slog.Info("[Watcher] M3U8 renamed", "from", current, "to", want)
	} else if !os.IsNotExist(err) {
		// Not fatal: the next generation writes to the new name regardless. Say
		// so, because the old file is then left behind and someone has to know.
		slog.Warn("[Watcher] M3U8: could not rename, the previous file may remain",
			"from", current, "to", want, "err", err)
	}
	pl.M3U8File = want
	return want
}

// userLabel resolves a user ID to something readable for a filename, or "" when
// it cannot — an unknown user must not become the string "unknown" in a name.
func (w *Watcher) userLabel(userID string) string {
	if userID == "" || w.auth == nil {
		return ""
	}
	u, err := w.auth.GetUser(userID)
	if err != nil || u == nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.DisplayName
}

// isAlbumSource reports whether a Spotify URL points at an album rather than a
// playlist. Used by both M3U8 producers — watchlist syncs and manual batches —
// which is why it is named for the source and not for either caller.
//
// Derived from the URL because WatchedPlaylist has never stored a source type.
// Adding one would need a migration for every existing watchlist, and the URL is
// already authoritative and immutable — it is what the watchlist was created
// from and cannot drift from it.
//
// Through spotify.ParseEntityRef, not a substring match. This was
// `strings.Contains(url, "/album/")` until the parser existed; two ways to read
// a Spotify URL in one package is how they drift, and the loose one matched any
// URL with that text anywhere in it.
//
// Artist watchlists (`/artist/`) are deliberately NOT covered. The same "an
// album is not a playlist" argument would apply, but an artist is a growing
// collection spanning many releases, which is the one case where a flat playlist
// file shows something the <Artist>/<Album>/ tree does not. Recorded as a
// decision so it does not read as an oversight later.
func isAlbumSource(spotifyURL string) bool {
	kind, _ := spotify.ParseEntityRef(spotifyURL)
	return kind == "album"
}

// A reconcilePlaylistDirs pass lived here: every check cycle it deleted any
// suffixed .m3u8 in a Playlists/ directory that no live watchlist owned. It was
// unsound, and it destroyed real files on the reference deployment within
// minutes of shipping.
//
// The premise was that watchlists are the only producer of these files. They are
// not. OnManualBatchComplete writes one for any non-watchlist batch that asks —
// a download started from the search bar — using the same m3u8BaseName naming.
// Those files have no watchlist by construction, so "no watchlist owns it" is
// not evidence of an orphan, and the sweep deleted five of them on the first
// run. Nothing on disk distinguishes the two producers, so no filename rule can
// fix this.
//
// Orphan cleanup needs a record of what we wrote and who wrote it. Until that
// exists, the two places that create orphans are fixed at the source instead:
// RemoveWatchlist no longer gates its cleanup on
// the CreateM3u8File setting, which is how the observed orphan survived its
// watchlist by 27 days.

// legacyM3U8BaseName is the pre-disambiguation filename (sanitized playlist
// name only, no ID suffix) SpotiFLAC used before m3u8BaseName. Used only to
// find and clean up leftover files from before the naming-collision fix.
func legacyM3U8BaseName(playlistName string) string {
	safeName := util.SanitizeFilename(playlistName)
	if safeName == "" {
		safeName = "playlist"
	}
	return safeName
}

// m3u8Settings holds the user settings relevant to M3U8 generation.
type m3u8Settings struct {
	JellyfinPath string
}

// loadM3U8Settings returns the user (or global) settings if M3U8 generation is
// enabled, or nil if it is disabled.
func (w *Watcher) loadM3U8Settings(pl *WatchedPlaylist) *m3u8Settings {
	settings := settings.EffectiveDownloadSettings(w.auth, pl.UserID)
	if !settings.CreateM3u8File {
		return nil
	}
	return &m3u8Settings{JellyfinPath: settings.JellyfinMusicPath}
}

// resolveTrackPaths returns the ordered list of file paths matching pl.TrackIDs,
// and the IDs it could not place. Resolution order per ID:
//
//  1. Catalog active library_file, stat-checked. The index of what is on disk,
//     maintained by recordCatalogDone and by the daily verification pass.
//  2. BoltDB job FilePath, stat-checked. Legacy fallback for files downloaded
//     before the catalog existed, or whose row was cleaned up.
//
// It no longer walks the library reading SPOTIFY_ID tags. That walk was the
// third source, and it cost ~15 s cold over 2744 files on the reference
// deployment — on *every* generation, because it fired whenever any track was
// unresolved, and a single track that was never downloaded is unresolved
// forever. After each sync, after each batch, once per watchlist at startup.
//
// It was doing almost nothing for that price. Measured on the reference
// deployment before removing it: of 2621 tracks across three watchlists, 12 had
// no catalog row, 11 of those were covered by a job record, and the twelfth
// resolved to nothing at all — so the walk placed zero tracks and the M3U8 keeps
// exactly the entries it had.
//
// What it did cover, and now does not: a file present on disk with a
// SPOTIFY_ID tag but with neither a catalog row nor a job record — a library
// imported from outside SpotiFLAC. That is what library-rebuild is for, and the
// warning on an unresolved track says so. Recovering it belongs to an explicit
// repair, not to every write of a playlist file. See
// docs/watchlist-consistency-plan.md §6.
func (w *Watcher) resolveTrackPaths(pl *WatchedPlaylist, outputDir string) (paths []string, unresolved []string) {
	files := w.resolveTrackFiles(pl)
	paths = make([]string, 0, len(pl.TrackIDs))
	for _, spotifyID := range pl.TrackIDs {
		if path := files[spotifyID]; path != "" {
			paths = append(paths, path)
			continue
		}
		unresolved = append(unresolved, spotifyID)
	}
	return paths, unresolved
}

// resolveTrackFiles answers "where is this watchlist's file for this track",
// for every track it holds. Absent from the map means no existing file could be
// found; the value is always a path that stat'd successfully at lookup time.
//
// This is the single answer everything is supposed to share. It was not:
// generation resolved catalog-then-jobs while deletion read job.FilePath alone,
// so the two could name different files for one track — and the codebase already
// paid for that. UpdateJobFilePathsForRename exists because a File Manager
// rename updated only the catalog, leaving os.Remove(job.FilePath) pointing at a
// path that no longer existed: the deletion silently failed and "leaked the
// actual (renamed) file on disk forever" (internal/jobs/storage.go). The answer
// then was to write the new path into every store; the answer here is to stop
// having stores that can disagree. See docs/watchlist-consistency-plan.md §4.
//
// Sources, in order, each stat-checked:
//
//  1. The catalog — the index of what is on disk, maintained by
//     recordCatalogDone and by the daily verification pass.
//  2. BoltDB job paths — for files downloaded before the catalog existed, or
//     whose row was cleaned up. Not an authority on location; a leftover.
func (w *Watcher) resolveTrackFiles(pl *WatchedPlaylist) map[string]string {
	catalogPaths := w.catalogPathsForWatchlist(pl)
	legacy := w.legacyJobPaths(pl.ID)

	files := make(map[string]string, len(pl.TrackIDs))
	for _, spotifyID := range pl.TrackIDs {
		if path := catalogPaths[spotifyID]; path != "" {
			if _, statErr := os.Stat(path); statErr == nil {
				files[spotifyID] = path
				continue
			}
			// Stale row — the file is no longer where it says. Fall through
			// rather than hand back a path nothing is at: writing it into an
			// M3U8 makes a broken entry, and handing it to os.Remove deletes
			// nothing while reporting success. The daily verification pass
			// marks the row missing; library-rebuild repairs a moved file.
		}
		if path := legacy[spotifyID]; path != "" {
			if _, statErr := os.Stat(path); statErr == nil {
				files[spotifyID] = path
			}
		}
	}
	return files
}

// unresolvedSampleLimit caps how many unresolved Spotify IDs a generation logs.
const unresolvedSampleLimit = 10

// legacyJobPaths returns the SpotifyID→FilePath map from BoltDB jobs for a
// given watchlist, used as fallback for files that don't yet carry the
// SPOTIFY_ID tag (downloaded before this change).
func (w *Watcher) legacyJobPaths(watchlistID string) map[string]string {
	if w.jm == nil {
		return map[string]string{}
	}
	jobList, err := w.jm.GetAllJobs()
	if err != nil {
		return map[string]string{}
	}
	type jobRef struct {
		path      string
		updatedAt time.Time
	}
	latest := make(map[string]jobRef)
	for _, job := range jobList {
		if job.WatchlistID != watchlistID || job.FilePath == "" || job.SpotifyID == "" {
			continue
		}
		if job.Status != jobs.StatusDone && job.Status != jobs.StatusSkipped {
			continue
		}
		prev, ok := latest[job.SpotifyID]
		if !ok || job.UpdatedAt.After(prev.updatedAt) {
			latest[job.SpotifyID] = jobRef{path: job.FilePath, updatedAt: job.UpdatedAt}
		}
	}
	out := make(map[string]string, len(latest))
	for id, ref := range latest {
		out[id] = ref.path
	}
	return out
}
