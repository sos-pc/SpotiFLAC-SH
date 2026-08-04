package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/meta"
	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	"github.com/sos-pc/SpotiFLAC-SH/internal/m3u8"
	bolt "go.etcd.io/bbolt"
)

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
	ID            string      `json:"id"`
	SpotifyURL    string      `json:"spotify_url"`
	Name          string      `json:"name"`
	IntervalHours int         `json:"interval_hours"`
	Settings      JobSettings `json:"settings"`
	LastSync      time.Time   `json:"last_sync"`
	TrackIDs      []string    `json:"track_ids"`
	CreatedAt     time.Time   `json:"created_at"`
	SyncDeletions bool        `json:"sync_deletions"`
	SyncLogs      []SyncLog   `json:"sync_logs,omitempty"`
	UserID        string      `json:"user_id,omitempty"`
}

// watchlistJobSettings returns the JobSettings a watchlist's downloads run with.
// Watchlists now follow the user's GLOBAL server settings, not the per-watchlist
// WatchedPlaylist.Settings copy — that copy is legacy and was never exposed in
// the UI, so keeping it as a separate source of truth only created drift (the
// M3U8 CreateM3u8File check already read global settings while the path came
// from the copy). Service too follows the global downloader. See
// docs/settings-source-of-truth.md.
func (w *Watcher) watchlistJobSettings(pl *WatchedPlaylist) JobSettings {
	s := EffectiveDownloadSettings(w.auth, pl.UserID)
	return serverJobSettings(s, s.Downloader)
}

// watchlistOutputRoot is the base download directory for a watchlist's M3U8 and
// scan operations — the user's global download path (default music dir if unset).
func (w *Watcher) watchlistOutputRoot(pl *WatchedPlaylist) string {
	if p := EffectiveDownloadSettings(w.auth, pl.UserID).DownloadPath; p != "" {
		return p
	}
	return util.GetDefaultMusicPath()
}

type AddWatchlistRequest struct {
	SpotifyURL    string      `json:"spotify_url"`
	IntervalHours int         `json:"interval_hours"`
	Settings      JobSettings `json:"settings"`
	SyncDeletions bool        `json:"sync_deletions"`
	UserID        string      `json:"user_id,omitempty"`
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
	jm      *JobManager
	auth    *AuthManager
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex      // protège les écritures concurrentes sur les watchlists + syncing
	syncing map[string]bool // playlists en cours de sync (protégé par mu)
}

// NewWatcher crée et démarre le daemon de surveillance des playlists.
func NewWatcher(jm *JobManager, auth *AuthManager) *Watcher {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{
		jm:      jm,
		auth:    auth,
		ctx:     ctx,
		cancel:  cancel,
		syncing: make(map[string]bool),
	}
	// Injecter le callback de résolution des settings actuels d'une watchlist.
	// processJob l'utilise pour ignorer les settings obsolètes figés dans le job.
	jm.getWatchlistSettings = func(watchlistID string) (JobSettings, bool) {
		pl, err := w.getWatchlistByID(watchlistID)
		if err != nil || pl == nil {
			return JobSettings{}, false
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
	if err != nil || len(playlists) == 0 {
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
// embedding existed) — generateM3U8ForPlaylist guards against silently
// shrinking an already-good file when that happens, so running this
// unconditionally on every boot is safe.
func (w *Watcher) checkM3U8Integrity(pl WatchedPlaylist) {
	_, _ = w.generateM3U8ForPlaylist(pl.ID, false)
}

// syncPlaylist récupère les métadonnées Spotify, compare avec les tracks déjà
// connus, et enqueue uniquement les nouveaux.
// FIX #2 — mu.Lock() autour des écritures sur TrackIDs + saveWatchlist
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

	tracks := extractTracksFromMetadata(data)
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

	var newTracks []JobTrack
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

	// FIX #4 — EnqueueBatch avant generateM3U8 (était inversé)
	var batchID string
	if len(newTracks) > 0 {
		result, err := w.jm.EnqueueBatch(EnqueueBatchRequest{
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
	syncLog := SyncLog{
		Time:      time.Now(),
		BatchID:   batchID,
		NewTracks: len(newTracks),
		Deleted:   deletedCount,
	}
	pl.SyncLogs = append(pl.SyncLogs, syncLog)
	if len(pl.SyncLogs) > 20 {
		pl.SyncLogs = pl.SyncLogs[len(pl.SyncLogs)-20:]
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
	w.deleteStaleM3U8OnRename(&pl, oldName)

	// FIX #2 — verrou autour de la mise à jour de TrackIDs + save
	w.mu.Lock()
	// Q3: this sync started from a snapshot of pl taken possibly minutes
	// ago — if the watchlist was removed in the meantime (RemoveWatchlist
	// is locked too, see above), saving it back here would resurrect it.
	if _, err := w.getWatchlistByID(pl.ID); err != nil {
		w.mu.Unlock()
		return
	}
	pl.TrackIDs = append(pl.TrackIDs, newIDs...)
	pl.LastSync = time.Now()
	w.saveWatchlist(&pl)
	w.mu.Unlock()

	// Mirror current state into the SQLite catalog: track stubs,
	// watchlist_tracks junction, and a snapshot when the contents have
	// changed (or this is the first sync). Best-effort.
	w.mirrorWatchlistToCatalog(&pl)

	// Régénérer le M3U8 systématiquement à chaque sync. La fonction est
	// idempotente (rename atomique) : si OnBatchComplete tourne ensuite à
	// la fin du batch, le dernier write gagne et le M3U8 reste cohérent.
	_, _ = w.generateM3U8ForPlaylist(pl.ID, false)

	// Notifier le frontend que la sync est terminée
	w.jm.Publish(JobEvent{
		Type: "watchlist_synced",
		Data: map[string]interface{}{
			"watchlist_id": pl.ID,
			"new_tracks":   len(newTracks),
			"deleted":      deletedCount,
			"name":         pl.Name,
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
	jm := w.jm
	allPlaylists, _ := w.GetWatchlists()
	otherWatchlistIDs := make(map[string]bool)
	for _, other := range allPlaylists {
		if other.ID == pl.ID {
			continue
		}
		for _, id := range other.TrackIDs {
			otherWatchlistIDs[id] = true
		}
	}

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
		} else if jm != nil {
			jobs, _ := jm.GetAllJobs()
			for _, job := range jobs {
				if job.SpotifyID == knownID && job.WatchlistID == pl.ID && job.FilePath != "" {
					if err := os.Remove(job.FilePath); err == nil {
						slog.Info("[Watcher] Deleted file", "path", job.FilePath)
						outputRoot := w.watchlistOutputRoot(pl)
						removeEmptyParents(filepath.Dir(job.FilePath), outputRoot)
						// Nettoyer le FilePath dans BoltDB (le fichier n'existe plus)
						job.FilePath = ""
						job.UpdatedAt = time.Now()
						_ = jm.saveJob(&job)
						deletedCount++
					} else if !os.IsNotExist(err) {
						slog.Warn("[Watcher] Failed to delete file", "path", job.FilePath, "err", err)
					}
				}
			}
		}
	}
	pl.TrackIDs = remainingIDs
	return deletedCount
}

// deleteStaleM3U8OnRename removes the old M3U8 file after a playlist was
// renamed on Spotify (oldName is the previous name, "" when there was no
// rename). Must run before saveWatchlist persists the new name — see the call
// site in syncPlaylist for the crash-ordering rationale. Extracted from
// syncPlaylist (R4).
func (w *Watcher) deleteStaleM3U8OnRename(pl *WatchedPlaylist, oldName string) {
	if oldName == "" {
		return
	}
	if !EffectiveDownloadSettings(w.auth, pl.UserID).CreateM3u8File {
		return
	}
	outputDir := w.watchlistOutputRoot(pl)
	oldM3u8Path := filepath.Join(outputDir, "Playlists", m3u8BaseName(oldName, pl.ID)+".m3u8")
	if err := os.Remove(oldM3u8Path); err == nil {
		slog.Info("[Watcher] Playlist renamed, old M3U8 deleted", "old_name", oldName, "new_name", pl.Name)
	} else if !os.IsNotExist(err) {
		slog.Warn("[Watcher] Playlist renamed, failed to delete old M3U8", "old_name", oldName, "new_name", pl.Name, "err", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CRUD watchlist
// ─────────────────────────────────────────────────────────────────────────────

func (w *Watcher) AddWatchlist(req AddWatchlistRequest) (AddWatchlistResponse, error) {
	if req.SpotifyURL == "" {
		return AddWatchlistResponse{}, fmt.Errorf("spotify URL is required")
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

	tracks := extractTracksFromMetadata(data)
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

	if err := w.saveWatchlist(pl); err != nil {
		return AddWatchlistResponse{}, fmt.Errorf("failed to save watchlist: %v", err)
	}

	if len(tracks) > 0 {
		for i := range tracks {
			tracks[i].PlaylistName = name
			tracks[i].Position = i + 1
		}
		batchReq := EnqueueBatchRequest{
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

func (w *Watcher) RemoveWatchlist(id string) error {
	playlists, _ := w.GetWatchlists()
	for _, pl := range playlists {
		if pl.ID != id {
			continue
		}

		outputRoot := w.watchlistOutputRoot(&pl)

		// ── Suppression des fichiers audio (seulement si SyncDeletions) ────────
		if pl.SyncDeletions {
			otherIDs := make(map[string]bool)
			for _, other := range playlists {
				if other.ID == id {
					continue
				}
				for _, tid := range other.TrackIDs {
					otherIDs[tid] = true
				}
			}
			jobs, _ := w.jm.GetAllJobs()
			for _, job := range jobs {
				if job.WatchlistID != id || job.FilePath == "" {
					continue
				}
				if otherIDs[job.SpotifyID] {
					slog.Debug("[Watcher] Track in another watchlist, skipping file deletion", "spotify_id", job.SpotifyID)
					continue
				}
				if err := os.Remove(job.FilePath); err == nil {
					slog.Info("[Watcher] Deleted file (watchlist removed)", "path", job.FilePath)
					removeEmptyParents(filepath.Dir(job.FilePath), outputRoot)
					// Nettoyer le FilePath dans BoltDB
					job.FilePath = ""
					job.UpdatedAt = time.Now()
					_ = w.jm.saveJob(&job)
				}
			}
		}

		// ── Suppression du fichier M3U8 (toujours, indépendamment de SyncDeletions) ──
		if EffectiveDownloadSettings(w.auth, pl.UserID).CreateM3u8File {
			playlistsDir := filepath.Join(outputRoot, "Playlists")
			// Try both the current (ID-suffixed) and legacy (pre-migration,
			// no suffix) filenames — a watchlist removed before ever
			// re-syncing after the naming-collision fix could still only
			// have the legacy file on disk.
			for _, m3u8Path := range []string{
				filepath.Join(playlistsDir, m3u8BaseName(pl.Name, pl.ID)+".m3u8"),
				filepath.Join(playlistsDir, legacyM3U8BaseName(pl.Name)+".m3u8"),
			} {
				if err := os.Remove(m3u8Path); err == nil {
					slog.Info("[Watcher] Deleted M3U8 (watchlist removed)", "path", m3u8Path)
				} else if !os.IsNotExist(err) {
					slog.Warn("[Watcher] Failed to delete M3U8 (watchlist removed)", "path", m3u8Path, "err", err)
				}
			}
			// Nettoyer le dossier Playlists/ s'il est vide
			if entries, err := os.ReadDir(playlistsDir); err == nil && len(entries) == 0 {
				if err := os.Remove(playlistsDir); err == nil {
					slog.Info("[Watcher] Deleted empty Playlists dir", "path", playlistsDir)
				}
			}
		}

		break
	}

	// Locked (Q3): if a sync for this same watchlist is mid-flight, its
	// end-of-sync save (also locked, see syncPlaylist) would otherwise be
	// able to land after this delete and resurrect the record.
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchlist)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(id))
	})
}

func (w *Watcher) GetWatchlists() ([]WatchedPlaylist, error) {
	var playlists []WatchedPlaylist
	err := w.jm.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchlist)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var pl WatchedPlaylist
			if err := json.Unmarshal(v, &pl); err != nil {
				return nil
			}
			playlists = append(playlists, pl)
			return nil
		})
	})
	return playlists, err
}

func (w *Watcher) getWatchlistByID(id string) (*WatchedPlaylist, error) {
	playlists, err := w.GetWatchlists()
	if err != nil {
		return nil, err
	}
	for _, pl := range playlists {
		if pl.ID == id {
			return &pl, nil
		}
	}
	return nil, fmt.Errorf("watchlist not found: %s", id)
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

func (w *Watcher) saveWatchlist(pl *WatchedPlaylist) error {
	return w.jm.db.Update(func(tx *bolt.Tx) error {
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
	pl, err := w.getWatchlistByID(id)
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
// Helpers — parsing de la réponse GetFilteredSpotifyData
// ─────────────────────────────────────────────────────────────────────────────

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
func (w *Watcher) OnManualBatchComplete(req BatchM3U8Request, paths []string) {
	if len(paths) == 0 {
		return
	}
	settings := EffectiveDownloadSettings(w.auth, req.UserID)
	if !settings.CreateM3u8File {
		return
	}
	root := settings.DownloadPath
	if root == "" {
		root = util.GetDefaultMusicPath()
	}
	baseName := m3u8BaseName(req.Name, req.SourceID)
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
	// Locked (Q3): same reasoning as UpdateWatchlist — this read-modify-write
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
			if len(pl.SyncLogs) > 20 {
				pl.SyncLogs = pl.SyncLogs[len(pl.SyncLogs)-20:]
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
		_, _ = w.generateM3U8ForPlaylist(matchedID, false)
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
	_ = w.saveWatchlist(pl)
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

func extractTracksFromMetadata(data interface{}) []JobTrack {
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
		return []JobTrack{{
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

// FIX #7 — extractPlaylistName retourne le nom de la playlist, pas le owner
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

	// FIX #7 — priorité au nom de la playlist sur le nom du owner
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
func convertTracks(tracks interface{}) []JobTrack {
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

	result := make([]JobTrack, 0, len(items))
	for _, t := range items {
		if t.SpotifyID == "" {
			continue
		}
		artistName := strings.TrimSpace(t.Artists)
		result = append(result, JobTrack{
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
}

func (w *Watcher) UpdateWatchlist(req UpdateWatchlistRequest) error {
	// Locked (Q3): without this, a settings change landing mid-sync could be
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

	pl, err := w.getWatchlistByID(watchlistID)
	if err != nil {
		return stats, err
	}
	stats.TotalTracks = len(pl.TrackIDs)

	trackIDSet := make(map[string]bool, len(pl.TrackIDs))
	for _, id := range pl.TrackIDs {
		trackIDSet[id] = true
	}

	jobs, err := jm.GetAllJobs()
	if err != nil {
		return stats, err
	}

	// Dédupliquer par SpotifyID : garder le job le plus récent par track
	// encore présente dans la watchlist.
	latest := make(map[string]Job)
	for _, j := range jobs {
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
			if jm.catalog != nil {
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
		case StatusDone:
			stats.Downloaded++
			stats.TotalSizeMB += j.TotalSize
		case StatusSkipped:
			stats.Skipped++
			stats.TotalSizeMB += j.TotalSize
		case StatusFailed:
			stats.Failed++
		case StatusPending, StatusDownloading:
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
	pl, err := w.getWatchlistByID(id)
	if err != nil {
		return WatchlistFreshnessReport{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := spotify.GetFilteredSpotifyData(ctx, pl.SpotifyURL, true, time.Second)
	if err != nil {
		return WatchlistFreshnessReport{}, fmt.Errorf("fetch spotify metadata: %w", err)
	}
	spotifyTracks := extractTracksFromMetadata(data)
	spotifyTrackIDs := make([]string, 0, len(spotifyTracks))
	for _, t := range spotifyTracks {
		spotifyTrackIDs = append(spotifyTrackIDs, t.SpotifyID)
	}

	outputDir := w.watchlistOutputRoot(pl)
	resolved := w.resolveTrackPaths(pl, outputDir)

	var pending, failed int
	if stats, statsErr := w.GetWatchlistStats(id); statsErr == nil {
		pending, failed = stats.Pending, stats.Failed
	}

	m3u8Enabled := w.loadM3U8Settings(pl) != nil
	var m3u8Count int
	var m3u8Exists bool
	if m3u8Enabled {
		playlistDir := filepath.Join(outputDir, "Playlists")
		m3u8Path := filepath.Join(playlistDir, m3u8BaseName(pl.Name, pl.ID)+".m3u8")
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

// FIX #6 — sort.Slice à la place du tri O(n²)
func (w *Watcher) GetWatchlistHistory(watchlistID string) ([]WatchlistHistoryItem, error) {
	jobs, err := w.jm.GetAllJobs()
	if err != nil {
		return nil, err
	}
	var items []WatchlistHistoryItem
	for _, j := range jobs {
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
	// FIX #6 — O(n log n) au lieu de O(n²)
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
	jobs, err := w.jm.GetAllJobs()
	if err != nil {
		return
	}

	// Garder le job le plus récent par SpotifyID pour cette watchlist
	latestJob := make(map[string]Job)
	for _, job := range jobs {
		if job.WatchlistID != pl.ID || job.FilePath == "" {
			continue
		}
		if job.Status != StatusDone && job.Status != StatusSkipped {
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

// generateM3U8ForPlaylist writes the M3U8 file for a watchlist by resolving
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
func (w *Watcher) generateM3U8ForPlaylist(watchlistID string, force bool) (m3u8.GenerationResult, error) {
	pl, err := w.getWatchlistByID(watchlistID)
	if err != nil || pl == nil {
		return m3u8.GenerationResult{}, fmt.Errorf("watchlist not found: %s", watchlistID)
	}

	settings := w.loadM3U8Settings(pl)
	if settings == nil {
		return m3u8.GenerationResult{}, fmt.Errorf("M3U8 generation is disabled (createM3u8File setting)")
	}

	outputDir := w.watchlistOutputRoot(pl)

	paths := w.resolveTrackPaths(pl, outputDir)
	result := m3u8.GenerationResult{
		Total:      len(pl.TrackIDs),
		Resolved:   len(paths),
		Unresolved: len(pl.TrackIDs) - len(paths),
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
	baseName := m3u8BaseName(pl.Name, pl.ID)
	if result.Unresolved > 0 {
		slog.Warn("[Watcher] M3U8: tracks unresolved (no catalog entry, no SPOTIFY_ID tag, no BoltDB job record); run POST /api/v1/admin/retag-legacy then POST /api/v1/admin/library-rebuild to recover them",
			"playlist", pl.Name, "unresolved", result.Unresolved, "total", len(pl.TrackIDs))
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
	settings := EffectiveDownloadSettings(w.auth, pl.UserID)
	if !settings.CreateM3u8File {
		return nil
	}
	return &m3u8Settings{JellyfinPath: settings.JellyfinMusicPath}
}

// resolveTrackPaths returns the ordered list of file paths matching pl.TrackIDs.
// Resolution order per ID:
//  1. Catalog active library_file (fast SQL JOIN, source of truth once
//     populated by recordCatalogDone or future library-rebuild). Survives
//     BoltDB cleanup and reflects manual file moves once the rescan flow
//     has updated the row.
//  2. Filesystem index built from SPOTIFY_ID tags under outputDir.
//     Covers files just downloaded but not yet mirrored to the catalog,
//     and tagged legacy files the catalog has not learned about yet.
//  3. BoltDB job FilePath (legacy fallback for files without the tag).
//
// Tracks that resolve to no existing file are skipped silently. We only
// build the filesystem index lazily — when at least one ID needs it,
// because its catalog entry is either missing OR stale (fails os.Stat) — to
// avoid a recursive walk on populated libraries. Gating this on stale-ness
// as well as absence matters: a single renamed/moved file whose catalog row
// still exists but points at a dead path must still trigger the fallback
// scan, even if every OTHER track in the playlist resolves fine via the
// catalog (a count-only check would leave exactly that one track
// unresolved forever, since "catalog row count == TrackIDs count" looks
// satisfied even though one of those rows is dead).
func (w *Watcher) resolveTrackPaths(pl *WatchedPlaylist, outputDir string) []string {
	catalogPaths := w.catalogPathsForWatchlist(pl)

	validCatalog := make(map[string]string, len(catalogPaths))
	for _, spotifyID := range pl.TrackIDs {
		path := catalogPaths[spotifyID]
		if path == "" {
			continue
		}
		if _, statErr := os.Stat(path); statErr == nil {
			validCatalog[spotifyID] = path
		}
		// Else: stale catalog row — file no longer at recorded path. Left
		// out of validCatalog, which is what needsFilesystemIndexFallback
		// below checks for, rather than emitting a broken M3U8 entry.
	}

	var index map[string]string
	if needsFilesystemIndexFallback(pl.TrackIDs, validCatalog) {
		var err error
		index, err = meta.BuildSpotifyIDIndex(outputDir)
		if err != nil {
			slog.Warn("[Watcher] M3U8: index build failed", "playlist", pl.Name, "err", err)
			index = map[string]string{}
		}
	}

	legacy := w.legacyJobPaths(pl.ID)

	paths := make([]string, 0, len(pl.TrackIDs))
	for _, spotifyID := range pl.TrackIDs {
		if path := validCatalog[spotifyID]; path != "" {
			paths = append(paths, path)
			continue
		}
		if path := index[spotifyID]; path != "" {
			paths = append(paths, path)
			continue
		}
		if path := legacy[spotifyID]; path != "" {
			if _, statErr := os.Stat(path); statErr == nil {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// needsFilesystemIndexFallback reports whether at least one ID in trackIDs
// has no valid (already stat-checked) entry in validCatalog — meaning the
// filesystem SPOTIFY_ID-tag index must be built as a fallback source.
// Comparing lengths this way correctly catches BOTH a missing catalog row
// and a stale one (absent from validCatalog either way), unlike comparing
// raw catalog row counts against trackIDs, which stays satisfied even when
// one of those rows points at a dead path.
func needsFilesystemIndexFallback(trackIDs []string, validCatalog map[string]string) bool {
	return len(validCatalog) < len(trackIDs)
}

// legacyJobPaths returns the SpotifyID→FilePath map from BoltDB jobs for a
// given watchlist, used as fallback for files that don't yet carry the
// SPOTIFY_ID tag (downloaded before this change).
func (w *Watcher) legacyJobPaths(watchlistID string) map[string]string {
	jobs, err := w.jm.GetAllJobs()
	if err != nil {
		return map[string]string{}
	}
	type jobRef struct {
		path      string
		updatedAt time.Time
	}
	latest := make(map[string]jobRef)
	for _, job := range jobs {
		if job.WatchlistID != watchlistID || job.FilePath == "" || job.SpotifyID == "" {
			continue
		}
		if job.Status != StatusDone && job.Status != StatusSkipped {
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
