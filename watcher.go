package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
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
	// Vérifier l'intégrité des M3U8 au démarrage (recovery après crash/redémarrage)
	go func() {
		playlists, err := w.GetWatchlists()
		if err != nil {
			return
		}
		for _, pl := range playlists {
			w.checkM3U8Integrity(pl)
		}
	}()
	go w.daemon()
	fmt.Println("[Watcher] Daemon started")
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
	w.checkAll()

	for {
		select {
		case <-w.ctx.Done():
			fmt.Println("[Watcher] Daemon stopped")
			return
		case <-ticker.C:
			w.checkAll()
		}
	}
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
			go w.syncPlaylist(pl)
		}
	}
}

// checkM3U8Integrity vérifie que le M3U8 sur disque correspond aux fichiers
// réellement présents parmi les jobs done/skipped de cette watchlist.
// Appelée uniquement au démarrage du serveur (NewWatcher) pour réparer d'éventuels
// écarts après un crash ou redémarrage. Corrige en appelant generateM3U8ForPlaylist.
func (w *Watcher) checkM3U8Integrity(pl WatchedPlaylist) {
	app := &App{}
	var settings map[string]interface{}
	if pl.UserID != "" && w.auth != nil {
		if profile, err2 := w.auth.GetUser(pl.UserID); err2 == nil && profile != nil && len(profile.Settings) > 0 {
			settings = profile.Settings
		}
	}
	if settings == nil {
		var err error
		settings, err = app.LoadSettings()
		if err != nil || settings == nil {
			return
		}
	}
	createM3u8, _ := settings["createM3u8File"].(bool)
	if !createM3u8 {
		return
	}

	outputDir := pl.Settings.DownloadPath
	if outputDir == "" {
		outputDir = "/home/nonroot/Music"
	}
	safeName := util.SanitizeFilename(pl.Name)
	if safeName == "" {
		safeName = "playlist"
	}
	m3u8Path := filepath.Join(outputDir, "Playlists", safeName+".m3u8")

	// Collecter les jobs done/skipped avec FilePath pour cette watchlist.
	// Dédupliquer par SpotifyID (garder le plus récent).
	jobs, err := w.jm.GetAllJobs()
	if err != nil {
		return
	}
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
			key = job.ID
		}
		if prev, ok := latestJob[key]; !ok || job.UpdatedAt.After(prev.UpdatedAt) {
			latestJob[key] = job
		}
	}

	// Compter les fichiers effectivement présents sur disque.
	validCount := 0
	for _, job := range latestJob {
		if _, err := os.Stat(job.FilePath); err == nil {
			validCount++
		}
	}

	// Lire le M3U8 existant et compter ses entrées.
	m3u8Count := -1 // -1 = fichier absent
	if data, err := os.ReadFile(m3u8Path); err == nil {
		m3u8Count = 0
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				m3u8Count++
			}
		}
	}

	switch {
	case validCount == 0:
		// Rien de téléchargé : pas de M3U8 à vérifier.
	case m3u8Count == -1:
		fmt.Printf("[Watcher] Integrity %s: M3U8 absent, %d fichiers valides → régénération\n", pl.Name, validCount)
		w.generateM3U8ForPlaylist(pl.ID)
	case m3u8Count != validCount:
		fmt.Printf("[Watcher] Integrity %s: M3U8=%d entrées, fichiers valides=%d → régénération\n", pl.Name, m3u8Count, validCount)
		w.generateM3U8ForPlaylist(pl.ID)
	default:
		fmt.Printf("[Watcher] Integrity %s: OK (%d/%d)\n", pl.Name, m3u8Count, validCount)
	}
}

// syncPlaylist récupère les métadonnées Spotify, compare avec les tracks déjà
// connus, et enqueue uniquement les nouveaux.
// FIX #2 — mu.Lock() autour des écritures sur TrackIDs + saveWatchlist
func (w *Watcher) syncPlaylist(pl WatchedPlaylist) {
	// Empêcher les exécutions concurrentes pour la même playlist
	w.mu.Lock()
	if w.syncing[pl.ID] {
		w.mu.Unlock()
		fmt.Printf("[Watcher] Sync already in progress for %s — skipping\n", pl.Name)
		return
	}
	w.syncing[pl.ID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.syncing, pl.ID)
		w.mu.Unlock()
	}()

	fmt.Printf("[Watcher] Syncing: %s\n", pl.SpotifyURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	data, err := spotify.GetFilteredSpotifyData(ctx, pl.SpotifyURL, true, time.Second)
	if err != nil {
		fmt.Printf("[Watcher] Failed to fetch metadata for %s: %v\n", pl.SpotifyURL, err)
		return
	}

	tracks := extractTracksFromMetadata(data)
	if len(tracks) == 0 {
		fmt.Printf("[Watcher] No tracks found for %s\n", pl.SpotifyURL)
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

	fmt.Printf("[Watcher] %s — %d new tracks to download\n", playlistName, len(newTracks))

	// FIX #4 — EnqueueBatch avant generateM3U8 (était inversé)
	var batchID string
	if len(newTracks) > 0 {
		result, err := w.jm.EnqueueBatch(EnqueueBatchRequest{
			Tracks:      newTracks,
			Settings:    pl.Settings,
			WatchlistID: pl.ID,
			UserID:      pl.UserID,
		})
		if err != nil {
			fmt.Printf("[Watcher] EnqueueBatch failed for %s: %v\n", playlistName, err)
		} else {
			batchID = result.BatchID
		}
	}

	// Retry des jobs failed pour cette watchlist (après fetch Spotify réussi)
	if requeued, err := w.jm.RequeueFailedJobs(pl.ID); err == nil && requeued > 0 {
		fmt.Printf("[Watcher] Requeued %d failed jobs for %s\n", requeued, pl.Name)
	}

	// ── Sync deletions ──
	deletedCount := 0
	if pl.SyncDeletions && len(currentTrackIDs) > 0 {
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

		remainingIDs := make([]string, 0, len(pl.TrackIDs))
		for _, knownID := range pl.TrackIDs {
			if currentSet[knownID] {
				remainingIDs = append(remainingIDs, knownID)
				continue
			}
			inOtherPlaylist := otherWatchlistIDs[knownID]
			if inOtherPlaylist {
				fmt.Printf("[Watcher] Track %s removed from %s but present in another watchlist — skipping file deletion\n", knownID, pl.Name)
				remainingIDs = append(remainingIDs, knownID)
			} else if jm != nil {
				jobs, _ := jm.GetAllJobs()
				for _, job := range jobs {
					if job.SpotifyID == knownID && job.WatchlistID == pl.ID && job.FilePath != "" {
						if err := os.Remove(job.FilePath); err == nil {
							fmt.Printf("[Watcher] Deleted file: %s\n", job.FilePath)
							outputRoot := pl.Settings.DownloadPath
							if outputRoot == "" {
								outputRoot = "/home/nonroot/Music"
							}
							removeEmptyParents(filepath.Dir(job.FilePath), outputRoot)
							// Nettoyer le FilePath dans BoltDB (le fichier n'existe plus)
							job.FilePath = ""
							job.UpdatedAt = time.Now()
							_ = jm.saveJob(&job)
							deletedCount++
						}
					}
				}
			}
		}
		pl.TrackIDs = remainingIDs
	}

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

	// FIX #2 — verrou autour de la mise à jour de TrackIDs + save
	w.mu.Lock()
	pl.TrackIDs = append(pl.TrackIDs, newIDs...)
	pl.LastSync = time.Now()
	w.saveWatchlist(&pl)
	w.mu.Unlock()

	// Rename Spotify : supprimer l'ancien M3U8 (le nouveau sera créé juste après)
	if oldName != "" {
		appInst := &App{}
		var renameSettings map[string]interface{}
		if pl.UserID != "" && w.auth != nil {
			if profile, err2 := w.auth.GetUser(pl.UserID); err2 == nil && profile != nil && len(profile.Settings) > 0 {
				renameSettings = profile.Settings
			}
		}
		if renameSettings == nil {
			renameSettings, _ = appInst.LoadSettings()
		}
		if renameSettings != nil {
			if createM3u8, _ := renameSettings["createM3u8File"].(bool); createM3u8 {
				outputDir := pl.Settings.DownloadPath
				if outputDir == "" {
					outputDir = "/home/nonroot/Music"
				}
				oldSafeName := util.SanitizeFilename(oldName)
				if oldSafeName != "" {
					oldM3u8Path := filepath.Join(outputDir, "Playlists", oldSafeName+".m3u8")
					if err := os.Remove(oldM3u8Path); err == nil {
						fmt.Printf("[Watcher] Playlist renommée '%s' → '%s' : ancien M3U8 supprimé\n", oldName, pl.Name)
					}
				}
			}
		}
	}

	// Régénérer le M3U8 à chaque sync quand aucun nouveau batch n'est en cours.
	// Si len(newTracks) > 0, OnBatchComplete s'en chargera après les téléchargements.
	if len(newTracks) == 0 {
		w.generateM3U8ForPlaylist(pl.ID)
	}

	// Notifier le frontend que la sync est terminée
	w.jm.hub.publish(JobEvent{
		Type: "watchlist_synced",
		Data: map[string]interface{}{
			"watchlist_id": pl.ID,
			"new_tracks":   len(newTracks),
			"deleted":      deletedCount,
			"name":         pl.Name,
		},
	})
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
		go w.jm.EnqueueBatch(EnqueueBatchRequest{
			Tracks:      tracks,
			Settings:    req.Settings,
			WatchlistID: pl.ID,
			UserID:      pl.UserID,
		})
	}

	fmt.Printf("[Watcher] Added watchlist: %s (%d tracks, every %dh)\n",
		name, len(tracks), req.IntervalHours)

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

		outputRoot := pl.Settings.DownloadPath
		if outputRoot == "" {
			outputRoot = "/home/nonroot/Music"
		}

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
					fmt.Printf("[Watcher] Track %s in another watchlist — skipping file deletion\n", job.SpotifyID)
					continue
				}
				if err := os.Remove(job.FilePath); err == nil {
					fmt.Printf("[Watcher] Deleted file (watchlist removed): %s\n", job.FilePath)
					removeEmptyParents(filepath.Dir(job.FilePath), outputRoot)
					// Nettoyer le FilePath dans BoltDB
					job.FilePath = ""
					job.UpdatedAt = time.Now()
					_ = w.jm.saveJob(&job)
				}
			}
		}

		// ── Suppression du fichier M3U8 (toujours, indépendamment de SyncDeletions) ──
		app := &App{}
		var settings map[string]interface{}
		if pl.UserID != "" && w.auth != nil {
			if profile, err2 := w.auth.GetUser(pl.UserID); err2 == nil && profile != nil && len(profile.Settings) > 0 {
				settings = profile.Settings
			}
		}
		if settings == nil {
			settings, _ = app.LoadSettings()
		}
		if settings != nil {
			if createM3u8, _ := settings["createM3u8File"].(bool); createM3u8 {
				safeName := util.SanitizeFilename(pl.Name)
				if safeName != "" {
					m3u8Path := filepath.Join(outputRoot, "Playlists", safeName+".m3u8")
					if err := os.Remove(m3u8Path); err == nil {
						fmt.Printf("[Watcher] Deleted M3U8 (watchlist removed): %s\n", m3u8Path)
					}
					// Nettoyer le dossier Playlists/ s'il est vide
					playlistsDir := filepath.Join(outputRoot, "Playlists")
					if entries, err := os.ReadDir(playlistsDir); err == nil && len(entries) == 0 {
						if err := os.Remove(playlistsDir); err == nil {
							fmt.Printf("[Watcher] Deleted empty Playlists dir: %s\n", playlistsDir)
						}
					}
				}
			}
		}

		break
	}

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
//  2. Retry des jobs failed (déplacé dans syncPlaylist, après fetch Spotify réussi)
func (w *Watcher) SyncWatchlist(id string) error {
	pl, err := w.getWatchlistByID(id)
	if err != nil {
		return err
	}
	go w.syncPlaylist(*pl)
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

// OnBatchComplete implémente JobEventHandler.
// Trouve le SyncLog par BatchID, met à jour ses compteurs, génère le M3U8.
func (w *Watcher) OnBatchComplete(watchlistID, batchID string, downloaded, skipped, failed int) {
	playlists, err := w.GetWatchlists()
	if err != nil {
		return
	}
	for _, pl := range playlists {
		if pl.ID != watchlistID {
			continue
		}
		// Trouver le SyncLog correspondant au batchID plutôt que le dernier.
		if batchID != "" {
			for i := range pl.SyncLogs {
				if pl.SyncLogs[i].BatchID == batchID {
					pl.SyncLogs[i].Downloaded = downloaded
					pl.SyncLogs[i].Skipped = skipped
					pl.SyncLogs[i].Failed = failed
					if saveErr := w.saveWatchlist(&pl); saveErr != nil {
						fmt.Printf("[Watcher] Failed to save sync log: %v\n", saveErr)
					}
					break
				}
			}
		}
		w.generateM3U8ForPlaylist(pl.ID)
		return
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
	fmt.Printf("[Watcher] Track %s removed from %s TrackIDs (will retry next sync)\n", spotifyID, pl.Name)
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
		fmt.Printf("[Watcher] Deleted empty dir: %s\n", dir)
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

func (w *Watcher) GetWatchlistStats(watchlistID string) (WatchlistStats, error) {
	jm := w.jm
	stats := WatchlistStats{WatchlistID: watchlistID}

	pl, err := w.getWatchlistByID(watchlistID)
	if err != nil {
		return stats, err
	}
	stats.TotalTracks = len(pl.TrackIDs)

	// Set des SpotifyIDs actuellement dans la watchlist
	trackIDSet := make(map[string]bool, len(pl.TrackIDs))
	for _, id := range pl.TrackIDs {
		trackIDSet[id] = true
	}

	jobs, err := jm.GetAllJobs()
	if err != nil {
		return stats, err
	}

	// Dédupliquer par SpotifyID : garder le job le plus récent par track.
	latest := make(map[string]Job)
	for _, j := range jobs {
		if j.WatchlistID != watchlistID {
			continue
		}
		key := j.SpotifyID
		if key == "" {
			key = j.ID
		}
		if prev, ok := latest[key]; !ok || j.UpdatedAt.After(prev.UpdatedAt) {
			latest[key] = j
		}
	}

	// Compter par statut et cumuler la taille.
	tracksWithJob := make(map[string]bool)
	for _, j := range latest {
		// Ne compter que les jobs dont le track est encore dans la watchlist
		// (OnPermanentFailure peut avoir retiré des tracks de TrackIDs)
		if j.SpotifyID != "" && !trackIDSet[j.SpotifyID] {
			continue
		}
		if j.SpotifyID != "" {
			tracksWithJob[j.SpotifyID] = true
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

	// Tracks présentes dans TrackIDs mais sans job : téléchargées avant
	// l'activation du tracking ou dont le job a été nettoyé (CleanupOldJobs).
	// On les considère présentes et on les ajoute aux skipped.
	for _, id := range pl.TrackIDs {
		if !tracksWithJob[id] {
			stats.Skipped++
		}
	}

	return stats, nil
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
			fmt.Printf("[Watcher] File missing for %s (%s) — will re-download\n", spotifyID, job.FilePath)
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
		fmt.Printf("[Watcher] %d missing file(s) will be re-queued for %s\n", len(missingIDs), pl.Name)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// M3U8 generation pour Jellyfin
// ─────────────────────────────────────────────────────────────────────────────

func (w *Watcher) generateM3U8ForPlaylist(watchlistID string) {
	pl, err := w.getWatchlistByID(watchlistID)
	if err != nil || pl == nil {
		return
	}

	app := &App{}
	var settings map[string]interface{}
	if pl.UserID != "" && w.auth != nil {
		if profile, err2 := w.auth.GetUser(pl.UserID); err2 == nil && profile != nil && len(profile.Settings) > 0 {
			settings = profile.Settings
		}
	}
	if settings == nil {
		var err error
		settings, err = app.LoadSettings()
		if err != nil || settings == nil {
			return
		}
	}

	createM3u8, _ := settings["createM3u8File"].(bool)
	if !createM3u8 {
		return
	}
	jellyfinPath, _ := settings["jellyfinMusicPath"].(string)

	outputDir := pl.Settings.DownloadPath
	if outputDir == "" {
		outputDir = "/home/nonroot/Music"
	}

	// Construire la map position depuis TrackIDs (ordre de la playlist Spotify)
	posMap := make(map[string]int, len(pl.TrackIDs))
	for i, id := range pl.TrackIDs {
		posMap[id] = i
	}

	// Source unique : jobs BoltDB StatusDone/StatusSkipped avec FilePath.
	// Même source que checkM3U8Integrity — cohérence garantie.
	jobs, err := w.jm.GetAllJobs()
	if err != nil {
		return
	}
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
			key = job.ID
		}
		if prev, ok := latestJob[key]; !ok || job.UpdatedAt.After(prev.UpdatedAt) {
			latestJob[key] = job
		}
	}

	type entry struct {
		pos  int
		path string
	}
	var entries []entry
	for _, job := range latestJob {
		if _, err := os.Stat(job.FilePath); err != nil {
			continue // fichier supprimé ou déplacé
		}
		pos, ok := posMap[job.SpotifyID]
		if !ok {
			pos = len(pl.TrackIDs) // track hors playlist → à la fin
		}
		entries = append(entries, entry{pos: pos, path: job.FilePath})
	}
	if len(entries) == 0 {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].pos < entries[j].pos
	})
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}

	playlistDir := outputDir + "/Playlists"
	if err := os.MkdirAll(playlistDir, 0755); err != nil {
		fmt.Printf("[Watcher] M3U8: failed to create Playlists dir: %v\n", err)
		return
	}
	if err := app.CreateM3U8File(pl.Name, playlistDir, paths, jellyfinPath, outputDir); err != nil {
		fmt.Printf("[Watcher] M3U8: failed to create %s: %v\n", pl.Name, err)
	} else {
		fmt.Printf("[Watcher] M3U8: created %s.m3u8\n", pl.Name)
	}
}
