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

	"github.com/afkarxyz/SpotiFLAC/backend"
	bolt "go.etcd.io/bbolt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type SyncLog struct {
	Time       time.Time `json:"time"`
	NewTracks  int       `json:"new_tracks"`
	Downloaded int       `json:"downloaded"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	Deleted    int       `json:"deleted"`
}

type WatchedPlaylist struct {
	ID             string      `json:"id"`
	SpotifyURL     string      `json:"spotify_url"`
	Name           string      `json:"name"`
	IntervalHours  int         `json:"interval_hours"`
	Settings       JobSettings `json:"settings"`
	LastSync       time.Time   `json:"last_sync"`
	TrackIDs       []string    `json:"track_ids"`
	CreatedAt      time.Time   `json:"created_at"`
	SyncDeletions  bool        `json:"sync_deletions"`
	UpgradeQuality bool        `json:"upgrade_quality"`
	SyncLogs       []SyncLog   `json:"sync_logs,omitempty"`
	UserID         string      `json:"user_id,omitempty"`
}

type AddWatchlistRequest struct {
	SpotifyURL     string      `json:"spotify_url"`
	IntervalHours  int         `json:"interval_hours"`
	Settings       JobSettings `json:"settings"`
	SyncDeletions  bool        `json:"sync_deletions"`
	UpgradeQuality bool        `json:"upgrade_quality"`
	UserID         string      `json:"user_id,omitempty"`
}

type AddWatchlistResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Watcher — singleton
// ─────────────────────────────────────────────────────────────────────────────

type Watcher struct {
	jm     *JobManager
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex // FIX #2 — protège les écritures concurrentes sur les watchlists
}

var (
	globalWatcher *Watcher
	watcherOnce   sync.Once
)

func GetWatcher() *Watcher {
	return globalWatcher
}

// InitWatcher démarre le daemon de surveillance des playlists.
func InitWatcher(jm *JobManager) {
	watcherOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		globalWatcher = &Watcher{
			jm:     jm,
			ctx:    ctx,
			cancel: cancel,
		}
		go globalWatcher.daemon()
		fmt.Println("[Watcher] Daemon started")
	})
}

// CloseWatcher arrête le daemon.
func CloseWatcher() {
	if globalWatcher != nil {
		globalWatcher.cancel()
	}
}

// daemon tourne en permanence et vérifie toutes les 5 minutes
// si des playlists doivent être synchronisées.
// FIX #1 — cleanupTicker intégré dans le for/select (était créé mais jamais consommé)
func (w *Watcher) daemon() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// FIX #1 — cleanup toutes les 24h, intégré dans le select
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer cleanupTicker.Stop()

	// Vérifier immédiatement au démarrage
	w.checkAll()

	// Premier cleanup différé de 5 minutes (laisser le temps aux workers de démarrer)
	go func() {
		time.Sleep(5 * time.Minute)
		if jm := GetJobManager(); jm != nil {
			jm.CleanupOldJobs()
		}
	}()

	for {
		select {
		case <-w.ctx.Done():
			fmt.Println("[Watcher] Daemon stopped")
			return
		case <-ticker.C:
			w.checkAll()
		// FIX #1 — cleanupTicker.C maintenant consommé → cleanup tourne toutes les 24h
		case <-cleanupTicker.C:
			if jm := GetJobManager(); jm != nil {
				deleted, err := jm.CleanupOldJobs()
				if err != nil {
					fmt.Printf("[Watcher] Cleanup error: %v\n", err)
				} else if deleted > 0 {
					fmt.Printf("[Watcher] Cleanup: %d old jobs deleted\n", deleted)
				}
			}
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

// syncPlaylist récupère les métadonnées Spotify, compare avec les tracks déjà
// connus, et enqueue uniquement les nouveaux.
// FIX #2 — mu.Lock() autour des écritures sur TrackIDs + saveWatchlist
func (w *Watcher) syncPlaylist(pl WatchedPlaylist) {
	fmt.Printf("[Watcher] Syncing: %s\n", pl.SpotifyURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	data, err := backend.GetFilteredSpotifyData(ctx, pl.SpotifyURL, true, time.Second)
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
	if playlistName != pl.Name {
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
	if len(newTracks) > 0 {
		_, err := w.jm.EnqueueBatch(EnqueueBatchRequest{
			Tracks:      newTracks,
			Settings:    pl.Settings,
			WatchlistID: pl.ID,
			UserID:      pl.UserID,
		})
		if err != nil {
			fmt.Printf("[Watcher] EnqueueBatch failed for %s: %v\n", playlistName, err)
		}
	}

	// M3U8 généré après EnqueueBatch (les jobs existants sont déjà là)
	// maybeGenerateM3U8 dans jobs.go le regénère aussi à la fin de chaque job
	go w.generateM3U8ForPlaylist(pl)

	// ── Sync deletions ──
	deletedCount := 0
	if pl.SyncDeletions && len(currentTrackIDs) > 0 {
		currentSet := make(map[string]bool)
		for _, id := range currentTrackIDs {
			currentSet[id] = true
		}
		jm := GetJobManager()
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
							dir := filepath.Dir(job.FilePath)
							if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
								os.Remove(dir)
								fmt.Printf("[Watcher] Deleted empty dir: %s\n", dir)
							}
						}
					}
				}
			}
			deletedCount++
		}
		pl.TrackIDs = remainingIDs
	}

	// ── SyncLog ──
	syncLog := SyncLog{
		Time:      time.Now(),
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

	data, err := backend.GetFilteredSpotifyData(ctx, req.SpotifyURL, true, time.Second)
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
		ID:             fmt.Sprintf("watch-%d", time.Now().UnixNano()),
		SpotifyURL:     req.SpotifyURL,
		Name:           name,
		IntervalHours:  req.IntervalHours,
		Settings:       req.Settings,
		LastSync:       time.Now(),
		TrackIDs:       trackIDs,
		CreatedAt:      time.Now(),
		SyncDeletions:  req.SyncDeletions,
		UpgradeQuality: req.UpgradeQuality,
		UserID:         req.UserID,
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

// ForceSyncWatchlist force une synchronisation immédiate d'une playlist.
func (w *Watcher) ForceSyncWatchlist(id string) error {
	playlists, err := w.GetWatchlists()
	if err != nil {
		return err
	}
	for _, pl := range playlists {
		if pl.ID == id {
			go w.syncPlaylist(pl)
			return nil
		}
	}
	return fmt.Errorf("watchlist not found: %s", id)
}

// SyncWatchlist combine :
//  1. Nouveaux tracks Spotify (pas encore dans TrackIDs) → enqueue
//  2. Jobs StatusFailed de cette watchlist → reset + remise en queue
//
// C'est le seul bouton "Sync" exposé au frontend (remplace ForceSyncWatchlist + RedownloadWatchlist).
func (w *Watcher) SyncWatchlist(id string) error {
	pl, err := w.getWatchlistByID(id)
	if err != nil {
		return err
	}

	// ── 1. Nouveaux tracks depuis Spotify ────────────────────────────────
	go w.syncPlaylist(*pl)

	// ── 2. Retry des jobs failed ─────────────────────────────────────────
	jm := GetJobManager()
	if jm != nil {
		requeued, err := jm.RequeueFailedJobs(id)
		if err != nil {
			fmt.Printf("[Watcher] SyncWatchlist: RequeueFailedJobs error: %v\n", err)
		} else if requeued > 0 {
			fmt.Printf("[Watcher] SyncWatchlist: %d failed jobs requeued for %s\n", requeued, pl.Name)
		}
	}

	return nil
}

// FIX #3 — defer cancel() sorti de la boucle (pattern correct)
func (w *Watcher) RedownloadWatchlist(id string) error {
	playlists, err := w.GetWatchlists()
	if err != nil {
		return err
	}
	for _, pl := range playlists {
		if pl.ID != id {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel() // un seul defer, hors boucle effective car on return juste après

		data, err := backend.GetFilteredSpotifyData(ctx, pl.SpotifyURL, true, time.Second)
		if err != nil {
			return fmt.Errorf("failed to fetch playlist: %v", err)
		}
		tracks := extractTracksFromMetadata(data)
		playlistName := extractPlaylistName(data)
		if playlistName == "" {
			playlistName = pl.Name
		}
		for i := range tracks {
			tracks[i].PlaylistName = playlistName
			tracks[i].Position = i + 1
		}
		if len(tracks) > 0 {
			go w.jm.EnqueueBatch(EnqueueBatchRequest{
				Tracks:      tracks,
				Settings:    pl.Settings,
				WatchlistID: pl.ID,
				UserID:      pl.UserID,
			})
		}
		newIDs := make([]string, 0, len(tracks))
		for _, t := range tracks {
			if t.SpotifyID != "" {
				newIDs = append(newIDs, t.SpotifyID)
			}
		}
		pl.TrackIDs = newIDs
		pl.Name = playlistName
		if err := w.saveWatchlist(&pl); err != nil {
			return err
		}
		fmt.Printf("[Watcher] Re-download all triggered for %s (%d tracks)\n", pl.Name, len(tracks))
		return nil
	}
	return fmt.Errorf("watchlist not found: %s", id)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers — parsing de la réponse GetFilteredSpotifyData
// ─────────────────────────────────────────────────────────────────────────────

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
			Name  string `json:"name"`  // FIX #7 — nom de la playlist, pas Owner.Name
			Owner struct {
				Name string `json:"name"`
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
	if result.PlaylistInfo.Name != "" {
		return result.PlaylistInfo.Name
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
	Downloaded  int     `json:"downloaded"`
	Failed      int     `json:"failed"`
	Skipped     int     `json:"skipped"`
	TotalSizeMB float64 `json:"total_size_mb"`
}

func (w *Watcher) GetWatchlistStats(watchlistID string) (WatchlistStats, error) {
	jm := GetJobManager()
	stats := WatchlistStats{WatchlistID: watchlistID}
	if jm == nil {
		return stats, nil
	}
	jobs, err := jm.GetAllJobs()
	if err != nil {
		return stats, err
	}
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
	for _, j := range latest {
		switch j.Status {
		case StatusDone:
			stats.Downloaded++
			stats.TotalSizeMB += j.TotalSize
		case StatusFailed:
			stats.Failed++
		case StatusSkipped:
			stats.Skipped++
			if j.FilePath != "" {
				if info, err := os.Stat(j.FilePath); err == nil {
					stats.TotalSizeMB += float64(info.Size()) / 1024 / 1024
				}
			}
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
	jm := GetJobManager()
	if jm == nil {
		return nil, nil
	}
	jobs, err := jm.GetAllJobs()
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

// ─────────────────────────────────────────────────────────────────────────────
// M3U8 generation pour Jellyfin
// ─────────────────────────────────────────────────────────────────────────────

func (w *Watcher) generateM3U8ForPlaylist(pl WatchedPlaylist) {
	app := &App{}
	var settings map[string]interface{}
	if pl.UserID != "" {
		if auth := GetAuthManager(); auth != nil {
			if profile, err2 := auth.GetUser(pl.UserID); err2 == nil && profile != nil && len(profile.Settings) > 0 {
				settings = profile.Settings
			}
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

	jm := GetJobManager()
	if jm == nil {
		return
	}
	outputDir := pl.Settings.DownloadPath
	if outputDir == "" {
		outputDir = "/home/nonroot/Music"
	}

	type entry struct {
		pos  int
		path string
	}
	jobs, err := jm.GetAllJobs()
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
	var entries []entry
	for _, job := range latestJob {
		entries = append(entries, entry{pos: job.Position, path: job.FilePath})
	}
	if len(entries) == 0 {
		return
	}
	// FIX #6 — sort.Slice ici aussi
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
	if err := app.CreateM3U8File(pl.Name, playlistDir, paths, jellyfinPath); err != nil {
		fmt.Printf("[Watcher] M3U8: failed to create %s: %v\n", pl.Name, err)
	} else {
		fmt.Printf("[Watcher] M3U8: created %s.m3u8\n", pl.Name)
	}
}
