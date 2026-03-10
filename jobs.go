package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
	bolt "go.etcd.io/bbolt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constantes
// ─────────────────────────────────────────────────────────────────────────────

const (
	jobWorkers    = 3         // workers de téléchargement en parallèle
	songLinkDelay = 600       // ms entre deux requêtes song.link
	dbFile        = "jobs.db" // chemin relatif au configDir
)

var (
	bucketJobs      = []byte("jobs")
	bucketWatchlist = []byte("watchlist")
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type JobStatus string

const (
	StatusPending     JobStatus = "pending"
	StatusDownloading JobStatus = "downloading"
	StatusDone        JobStatus = "done"
	StatusFailed      JobStatus = "failed"
	StatusSkipped     JobStatus = "skipped"
)

// JobSettings contient tous les paramètres de téléchargement envoyés par le frontend.
type JobSettings struct {
	Service              string `json:"service"`
	DownloadPath         string `json:"downloadPath"`
	FilenameTemplate     string `json:"filenameTemplate"`
	FolderTemplate       string `json:"folderTemplate"`
	TrackNumber          bool   `json:"trackNumber"`
	EmbedLyrics          bool   `json:"embedLyrics"`
	EmbedMaxQualityCover bool   `json:"embedMaxQualityCover"`
	TidalQuality         string `json:"tidalQuality"`
	QobuzQuality         string `json:"qobuzQuality"`
	AutoOrder            string `json:"autoOrder"`
	AutoQuality          string `json:"autoQuality"`
	UseFirstArtistOnly   bool   `json:"useFirstArtistOnly"`
	UseSingleGenre       bool   `json:"useSingleGenre"`
	EmbedGenre           bool   `json:"embedGenre"`
	CreatePlaylistFolder bool   `json:"createPlaylistFolder"`
	AllowFallback        bool   `json:"allowFallback"`
	Region               string `json:"region"`
}

// Job représente un téléchargement individuel persisté en BoltDB.
type Job struct {
	ID           string      `json:"id"`
	SpotifyID    string      `json:"spotify_id"`
	TrackName    string      `json:"track_name"`
	ArtistName   string      `json:"artist_name"`
	AlbumName    string      `json:"album_name"`
	AlbumArtist  string      `json:"album_artist"`
	ReleaseDate  string      `json:"release_date"`
	CoverURL     string      `json:"cover_url"`
	TrackNumber  int         `json:"track_number"`
	DiscNumber   int         `json:"disc_number"`
	TotalTracks  int         `json:"total_tracks"`
	TotalDiscs   int         `json:"total_discs"`
	Copyright    string      `json:"copyright"`
	Publisher    string      `json:"publisher"`
	Position     int         `json:"position"`
	PlaylistName string      `json:"playlist_name"`
	DurationMs   int         `json:"duration_ms"`
	Settings     JobSettings `json:"settings"`
	WatchlistID  string      `json:"watchlist_id,omitempty"`
	UserID       string      `json:"user_id,omitempty"`
	Status       JobStatus   `json:"status"`
	FilePath     string      `json:"file_path,omitempty"`
	TotalSize    float64     `json:"total_size,omitempty"`
	Progress     float64     `json:"progress,omitempty"`
	Error        string      `json:"error,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	StartedAt    time.Time   `json:"started_at"`
}

// EnqueueBatchRequest est reçu depuis le frontend.
type EnqueueBatchRequest struct {
	Tracks      []JobTrack  `json:"tracks"`
	Settings    JobSettings `json:"settings"`
	WatchlistID string      `json:"watchlist_id,omitempty"`
	UserID      string      `json:"user_id,omitempty"`
}

// JobTrack est la représentation d'un titre dans la requête EnqueueBatch.
type JobTrack struct {
	SpotifyID    string `json:"spotify_id"`
	TrackName    string `json:"track_name"`
	ArtistName   string `json:"artist_name"`
	AlbumName    string `json:"album_name"`
	AlbumArtist  string `json:"album_artist"`
	ReleaseDate  string `json:"release_date"`
	CoverURL     string `json:"cover_url"`
	TrackNumber  int    `json:"track_number"`
	DiscNumber   int    `json:"disc_number"`
	TotalTracks  int    `json:"total_tracks"`
	TotalDiscs   int    `json:"total_discs"`
	Copyright    string `json:"copyright"`
	Publisher    string `json:"publisher"`
	Position     int    `json:"position"`
	PlaylistName string `json:"playlist_name"`
	DurationMs   int    `json:"duration_ms"`
}

type EnqueueBatchResponse struct {
	Enqueued int    `json:"enqueued"`
	Skipped  int    `json:"skipped"`
	Message  string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// JobManager — singleton
// ─────────────────────────────────────────────────────────────────────────────

type JobManager struct {
	db          *bolt.DB
	queue       chan string // job IDs à traiter
	songLinkSem chan struct{}
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.RWMutex
	// FIX #1 — guard contre double CloseJobManager
	closed     bool
	closedOnce sync.Once
}

var (
	globalJobManager *JobManager
	jobManagerOnce   sync.Once
)

func GetJobManager() *JobManager {
	return globalJobManager
}

// InitJobManager initialise la BoltDB et démarre les workers.
func InitJobManager(configDir string) error {
	var initErr error
	jobManagerOnce.Do(func() {
		dbPath := filepath.Join(configDir, dbFile)

		db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 2 * time.Second})
		if err != nil {
			initErr = fmt.Errorf("failed to open jobs DB: %v", err)
			return
		}

		err = db.Update(func(tx *bolt.Tx) error {
			if _, err := tx.CreateBucketIfNotExists(bucketJobs); err != nil {
				return err
			}
			if _, err := tx.CreateBucketIfNotExists(bucketWatchlist); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			initErr = fmt.Errorf("failed to init DB buckets: %v", err)
			return
		}

		ctx, cancel := context.WithCancel(context.Background())

		jm := &JobManager{
			db:          db,
			queue:       make(chan string, 10000),
			songLinkSem: make(chan struct{}, 1),
			ctx:         ctx,
			cancel:      cancel,
		}

		globalJobManager = jm

		jm.recoverPendingJobs()

		for i := 0; i < jobWorkers; i++ {
			jm.wg.Add(1)
			go jm.worker(i)
		}

		fmt.Printf("[Jobs] Manager started (%d workers, db: %s)\n", jobWorkers, dbPath)
	})
	return initErr
}

// CloseJobManager arrête proprement les workers et ferme la DB.
// FIX #1 — closedOnce garantit qu'on ne ferme jamais le canal deux fois.
func CloseJobManager() {
	if globalJobManager == nil {
		return
	}
	globalJobManager.closedOnce.Do(func() {
		fmt.Println("[Jobs] Shutting down...")
		globalJobManager.closed = true
		globalJobManager.cancel()
		close(globalJobManager.queue)
		globalJobManager.wg.Wait()
		globalJobManager.db.Close()
		fmt.Println("[Jobs] Shutdown complete")
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// EnqueueBatch — appelé depuis l'API HTTP
// FIX #4 — vérification des doublons actifs avant d'enqueuer
// ─────────────────────────────────────────────────────────────────────────────

func (jm *JobManager) EnqueueBatch(req EnqueueBatchRequest) (EnqueueBatchResponse, error) {
	if len(req.Tracks) == 0 {
		return EnqueueBatchResponse{}, fmt.Errorf("no tracks provided")
	}

	// FIX #4 — charger les jobs actifs pour détecter les doublons
	existingJobs, _ := jm.GetAllJobs()
	activeJobs := make(map[string]bool) // clé: spotifyID+watchlistID
	for _, j := range existingJobs {
		if j.Status == StatusPending || j.Status == StatusDownloading {
			activeJobs[j.SpotifyID+"|"+j.WatchlistID] = true
		}
	}

	enqueued := 0
	skipped := 0

	for _, track := range req.Tracks {
		if track.SpotifyID == "" {
			skipped++
			continue
		}

		// FIX #4 — ignorer si déjà actif en queue pour cette watchlist
		dupKey := track.SpotifyID + "|" + req.WatchlistID
		if activeJobs[dupKey] {
			skipped++
			continue
		}

		job := &Job{
			ID:           fmt.Sprintf("%s-%d", track.SpotifyID, time.Now().UnixNano()),
			SpotifyID:    track.SpotifyID,
			TrackName:    track.TrackName,
			ArtistName:   track.ArtistName,
			AlbumName:    track.AlbumName,
			AlbumArtist:  track.AlbumArtist,
			ReleaseDate:  track.ReleaseDate,
			CoverURL:     track.CoverURL,
			TrackNumber:  track.TrackNumber,
			DiscNumber:   track.DiscNumber,
			TotalTracks:  track.TotalTracks,
			TotalDiscs:   track.TotalDiscs,
			Copyright:    track.Copyright,
			Publisher:    track.Publisher,
			Position:     track.Position,
			PlaylistName: track.PlaylistName,
			DurationMs:   track.DurationMs,
			Settings:     req.Settings,
			WatchlistID:  req.WatchlistID,
			UserID:       req.UserID,
			Status:       StatusPending,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}

		backend.AddToQueue(job.ID, job.TrackName, job.ArtistName, job.AlbumName, job.SpotifyID)

		if err := jm.saveJob(job); err != nil {
			fmt.Printf("[Jobs] Failed to persist job %s: %v\n", job.ID, err)
			skipped++
			continue
		}

		select {
		case jm.queue <- job.ID:
			enqueued++
		default:
			fmt.Printf("[Jobs] Queue full, job %s will be picked up on next poll\n", job.ID)
			enqueued++
		}
	}

	return EnqueueBatchResponse{
		Enqueued: enqueued,
		Skipped:  skipped,
		Message:  fmt.Sprintf("%d tracks enqueued for background download", enqueued),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Worker
// ─────────────────────────────────────────────────────────────────────────────

func (jm *JobManager) worker(id int) {
	defer jm.wg.Done()
	fmt.Printf("[Jobs] Worker %d started\n", id)

	for {
		select {
		case <-jm.ctx.Done():
			fmt.Printf("[Jobs] Worker %d stopped\n", id)
			return
		case jobID, ok := <-jm.queue:
			if !ok {
				fmt.Printf("[Jobs] Worker %d queue closed\n", id)
				return
			}
			jm.processJob(jobID)
		}
	}
}

func (jm *JobManager) processJob(jobID string) {
	job, err := jm.loadJob(jobID)
	if err != nil {
		fmt.Printf("[Jobs] Failed to load job %s: %v\n", jobID, err)
		return
	}

	if job.Status == StatusDone || job.Status == StatusSkipped {
		return
	}

	fmt.Printf("[Jobs] Processing: %s - %s\n", job.TrackName, job.ArtistName)

	job.Status = StatusDownloading
	job.UpdatedAt = time.Now()
	job.StartedAt = time.Now()
	jm.saveJob(job)
	backend.StartDownloadItem(job.ID)

	outputDir := jm.buildOutputDir(job)

	if existingPath := jm.checkFileExists(job, outputDir); existingPath != "" {
		fmt.Printf("[Jobs] Already exists: %s\n", existingPath)
		job.Status = StatusSkipped
		job.FilePath = existingPath
		job.UpdatedAt = time.Now()
		jm.saveJob(job)
		backend.SkipDownloadItem(job.ID, existingPath)
		return
	}

	streamingURLs := jm.getStreamingURLs(job)

	req := jm.buildDownloadRequest(job, outputDir, streamingURLs)

	app := &App{}
	resp, err := app.DownloadTrack(req)
	if err != nil || !resp.Success {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		} else {
			errMsg = resp.Error
		}
		fmt.Printf("[Jobs] Failed: %s - %v\n", job.TrackName, errMsg)
		job.Status = StatusFailed
		job.Error = errMsg
		job.UpdatedAt = time.Now()
		jm.saveJob(job)
		if job.WatchlistID != "" && job.SpotifyID != "" {
			isPermanentFailure := true
			temporaryPatterns := []string{"429", "rate limit", "timeout", "connection refused", "context deadline"}
			for _, pattern := range temporaryPatterns {
				if strings.Contains(strings.ToLower(errMsg), strings.ToLower(pattern)) {
					isPermanentFailure = false
					break
				}
			}
			if isPermanentFailure {
				GetWatcher().RemoveTrackID(job.WatchlistID, job.SpotifyID)
			}
		}
		return
	}

	job.Status = StatusDone
	job.FilePath = resp.File
	job.Progress = 1.0
	if resp.File != "" {
		if info, err := os.Stat(resp.File); err == nil {
			job.TotalSize = float64(info.Size()) / 1024 / 1024
		}
	}
	job.UpdatedAt = time.Now()
	jm.saveJob(job)
	fmt.Printf("[Jobs] Done: %s\n", job.TrackName)

	if job.WatchlistID != "" {
		jm.maybeGenerateM3U8(job.WatchlistID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// FIX #2 — getStreamingURLs respecte le contexte d'annulation pendant l'attente du semaphore
func (jm *JobManager) getStreamingURLs(job *Job) map[string]string {
	s := job.Settings

	if s.Service != "auto" && s.Service != "tidal" && s.Service != "amazon" {
		return nil
	}

	// FIX #2 — select avec ctx.Done() pour ne pas bloquer le shutdown
	select {
	case jm.songLinkSem <- struct{}{}:
		// slot acquis
	case <-jm.ctx.Done():
		fmt.Printf("[Jobs] song.link skipped for %s (shutdown)\n", job.TrackName)
		return nil
	}

	defer func() {
		time.Sleep(time.Duration(songLinkDelay) * time.Millisecond)
		<-jm.songLinkSem
	}()

	client := backend.NewSongLinkClient()
	urls, err := client.GetAllURLsFromSpotify(job.SpotifyID, s.Region)
	if err != nil {
		fmt.Printf("[Jobs] song.link failed for %s: %v\n", job.TrackName, err)
		return nil
	}

	result := make(map[string]string)
	data, _ := json.Marshal(urls)
	json.Unmarshal(data, &result)
	return result
}

func (jm *JobManager) buildOutputDir(job *Job) string {
	s := job.Settings
	outputDir := s.DownloadPath
	if outputDir == "" {
		outputDir = backend.GetDefaultMusicPath()
	}

	if s.CreatePlaylistFolder && job.PlaylistName != "" {
		if !strings.Contains(s.FolderTemplate, "{album}") &&
			!strings.Contains(s.FolderTemplate, "{album_artist}") &&
			!strings.Contains(s.FolderTemplate, "{playlist}") {
			outputDir = filepath.Join(outputDir, backend.SanitizeFilename(job.PlaylistName))
		}
	}

	if s.FolderTemplate != "" {
		releaseYear := ""
		if len(job.ReleaseDate) >= 4 {
			releaseYear = job.ReleaseDate[:4]
		}
		artist := job.ArtistName
		if s.UseFirstArtistOnly {
			artist = getFirstArtistStatic(artist)
		}
		albumArtist := job.AlbumArtist
		if albumArtist == "" {
			albumArtist = artist
		}
		if s.UseFirstArtistOnly {
			albumArtist = getFirstArtistStatic(albumArtist)
		}

		tpl := s.FolderTemplate
		tpl = strings.ReplaceAll(tpl, "{artist}", backend.SanitizeFilename(artist))
		tpl = strings.ReplaceAll(tpl, "{album}", backend.SanitizeFilename(job.AlbumName))
		tpl = strings.ReplaceAll(tpl, "{album_artist}", backend.SanitizeFilename(albumArtist))
		tpl = strings.ReplaceAll(tpl, "{year}", releaseYear)
		tpl = strings.ReplaceAll(tpl, "{playlist}", backend.SanitizeFilename(job.PlaylistName))

		for _, part := range strings.Split(tpl, "/") {
			part = strings.TrimSpace(part)
			if part != "" {
				outputDir = filepath.Join(outputDir, part)
			}
		}
	}

	return backend.SanitizeFolderPath(outputDir)
}

func (jm *JobManager) buildDownloadRequest(job *Job, outputDir string, streamingURLs map[string]string) DownloadRequest {
	s := job.Settings

	service := s.Service
	if service == "" {
		service = "tidal"
	}

	audioFormat := ""
	switch service {
	case "tidal":
		audioFormat = s.TidalQuality
		if audioFormat == "" {
			audioFormat = "LOSSLESS"
		}
	case "qobuz":
		audioFormat = s.QobuzQuality
		if audioFormat == "" {
			audioFormat = "6"
		}
	case "deezer":
		audioFormat = "flac"
	case "auto":
		if s.AutoQuality == "24" {
			audioFormat = "HI_RES_LOSSLESS"
		} else {
			audioFormat = "LOSSLESS"
		}
	}

	serviceURL := ""
	if streamingURLs != nil {
		if service == "tidal" || service == "auto" {
			serviceURL = streamingURLs["tidal_url"]
		} else if service == "amazon" {
			serviceURL = streamingURLs["amazon_url"]
		}
	}

	artist := job.ArtistName
	albumArtist := job.AlbumArtist
	if s.UseFirstArtistOnly {
		artist = getFirstArtistStatic(artist)
		if albumArtist != "" {
			albumArtist = getFirstArtistStatic(albumArtist)
		}
	}

	useAlbumTrackNumber := strings.Contains(s.FolderTemplate, "{album}") ||
		strings.Contains(s.FolderTemplate, "{album_artist}")

	filenameFormat := s.FilenameTemplate
	if filenameFormat == "" {
		filenameFormat = "title-artist"
	}

	durationSeconds := 0
	if job.DurationMs > 0 {
		durationSeconds = job.DurationMs / 1000
	}

	return DownloadRequest{
		Service:              service,
		TrackName:            job.TrackName,
		ArtistName:           artist,
		AlbumName:            job.AlbumName,
		AlbumArtist:          albumArtist,
		ReleaseDate:          job.ReleaseDate,
		CoverURL:             job.CoverURL,
		OutputDir:            outputDir,
		AudioFormat:          audioFormat,
		FilenameFormat:       filenameFormat,
		TrackNumber:          s.TrackNumber,
		Position:             job.Position,
		UseAlbumTrackNumber:  useAlbumTrackNumber,
		SpotifyID:            job.SpotifyID,
		EmbedLyrics:          s.EmbedLyrics,
		EmbedMaxQualityCover: s.EmbedMaxQualityCover,
		ServiceURL:           serviceURL,
		Duration:             durationSeconds,
		ItemID:               job.ID,
		SpotifyTrackNumber:   job.TrackNumber,
		SpotifyDiscNumber:    job.DiscNumber,
		SpotifyTotalTracks:   job.TotalTracks,
		SpotifyTotalDiscs:    job.TotalDiscs,
		Copyright:            job.Copyright,
		Publisher:            job.Publisher,
		PlaylistName:         job.PlaylistName,
		UserID:               job.UserID,
		AllowFallback:        s.AllowFallback,
		UseFirstArtistOnly:   s.UseFirstArtistOnly,
		UseSingleGenre:       s.UseSingleGenre,
		EmbedGenre:           s.EmbedGenre,
	}
}

func (jm *JobManager) checkFileExists(job *Job, outputDir string) string {
	if job.TrackName == "" || job.ArtistName == "" {
		return ""
	}

	s := job.Settings
	filenameFormat := s.FilenameTemplate
	if filenameFormat == "" {
		filenameFormat = "title-artist"
	}

	artist := job.ArtistName
	if s.UseFirstArtistOnly {
		artist = getFirstArtistStatic(artist)
	}
	albumArtist := job.AlbumArtist
	if s.UseFirstArtistOnly && albumArtist != "" {
		albumArtist = getFirstArtistStatic(albumArtist)
	}

	useAlbumTrackNumber := strings.Contains(s.FolderTemplate, "{album}")
	trackNumber := job.Position
	if useAlbumTrackNumber && job.TrackNumber > 0 {
		trackNumber = job.TrackNumber
	}

	expectedFilename := backend.BuildExpectedFilename(
		job.TrackName,
		artist,
		job.AlbumName,
		albumArtist,
		job.ReleaseDate,
		filenameFormat,
		job.PlaylistName,
		"",
		s.TrackNumber,
		trackNumber,
		job.DiscNumber,
		useAlbumTrackNumber,
	)

	expectedPath := filepath.Join(outputDir, expectedFilename)
	if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 100*1024 {
		return expectedPath
	}
	return ""
}

// recoverPendingJobs remet dans la queue les jobs interrompus au dernier démarrage.
// FIX #6 — Progress remis à 0 pour les jobs qui n'ont pas fini
func (jm *JobManager) recoverPendingJobs() {
	recovered := 0
	var toRecover []Job
	jm.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var job Job
			if err := json.Unmarshal(v, &job); err != nil {
				return nil
			}
			if job.Status == StatusPending || job.Status == StatusDownloading {
				job.Status = StatusPending
				job.Progress = 0 // FIX #6 — reset progress pour éviter affichage incorrect
				job.UpdatedAt = time.Now()
				toRecover = append(toRecover, job)
			}
			return nil
		})
	})
	for _, job := range toRecover {
		jobCopy := job
		jm.saveJob(&jobCopy)
		backend.AddToQueue(jobCopy.ID, jobCopy.TrackName, jobCopy.ArtistName, jobCopy.AlbumName, jobCopy.SpotifyID)
		select {
		case jm.queue <- jobCopy.ID:
			recovered++
		default:
		}
	}
	if recovered > 0 {
		fmt.Printf("[Jobs] Recovered %d interrupted jobs\n", recovered)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BoltDB helpers
// ─────────────────────────────────────────────────────────────────────────────

func (jm *JobManager) saveJob(job *Job) error {
	return jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}
		data, err := json.Marshal(job)
		if err != nil {
			return err
		}
		return b.Put([]byte(job.ID), data)
	})
}

func (jm *JobManager) loadJob(id string) (*Job, error) {
	var job Job
	err := jm.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}
		data := b.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("job not found: %s", id)
		}
		return json.Unmarshal(data, &job)
	})
	return &job, err
}

func (jm *JobManager) GetAllJobs() ([]Job, error) {
	var jobs []Job
	err := jm.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var job Job
			if err := json.Unmarshal(v, &job); err != nil {
				return nil
			}
			jobs = append(jobs, job)
			return nil
		})
	})
	return jobs, err
}

func (jm *JobManager) CleanupOldJobs() (int, error) {
	jobs, err := jm.GetAllJobs()
	if err != nil {
		return 0, err
	}

	type key struct{ spotifyID, watchlistID string }
	latest := make(map[key]Job)
	noSpotifyID := []Job{}
	for _, j := range jobs {
		if j.SpotifyID == "" {
			noSpotifyID = append(noSpotifyID, j)
			continue
		}
		k := key{j.SpotifyID, j.WatchlistID}
		if prev, ok := latest[k]; !ok || j.UpdatedAt.After(prev.UpdatedAt) {
			latest[k] = j
		}
	}

	keepIDs := make(map[string]bool)
	for _, j := range latest {
		keepIDs[j.ID] = true
	}
	cutoff := time.Now().AddDate(0, 0, -7)
	for _, j := range noSpotifyID {
		if j.UpdatedAt.After(cutoff) {
			keepIDs[j.ID] = true
		}
	}
	// Toujours garder les jobs actifs
	for _, j := range jobs {
		if j.Status == StatusPending || j.Status == StatusDownloading {
			keepIDs[j.ID] = true
		}
	}

	deleted := 0
	err = jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		b.ForEach(func(k, v []byte) error {
			if !keepIDs[string(k)] {
				toDelete = append(toDelete, k)
			}
			return nil
		})
		for _, k := range toDelete {
			b.Delete(k)
			deleted++
		}
		return nil
	})

	if err == nil && deleted > 0 {
		jm.db.Update(func(tx *bolt.Tx) error { return nil })
		fmt.Printf("[Jobs] Cleanup: deleted %d duplicate/old jobs\n", deleted)
	}
	return deleted, err
}

func (jm *JobManager) ClearCompletedJobs() error {
	return jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		b.ForEach(func(k, v []byte) error {
			var job Job
			if err := json.Unmarshal(v, &job); err != nil {
				return nil
			}
			if job.Status == StatusDone || job.Status == StatusSkipped {
				toDelete = append(toDelete, k)
			}
			return nil
		})
		for _, k := range toDelete {
			b.Delete(k)
		}
		return nil
	})
}

// FIX #5 — ClearAllJobs : itère et supprime clé par clé au lieu de DeleteBucket
// (même pattern que ClearHistory corrigé en v1.2.1)
func (jm *JobManager) ClearAllJobs() error {
	return jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		b.ForEach(func(k, v []byte) error {
			toDelete = append(toDelete, k)
			return nil
		})
		for _, k := range toDelete {
			b.Delete(k)
		}
		return nil
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers statiques
// ─────────────────────────────────────────────────────────────────────────────

func getFirstArtistStatic(artistString string) string {
	if artistString == "" {
		return ""
	}
	delimiters := []string{", ", " & ", " feat. ", " ft. ", " featuring "}
	for _, d := range delimiters {
		if idx := strings.Index(strings.ToLower(artistString), d); idx != -1 {
			return strings.TrimSpace(artistString[:idx])
		}
	}
	return artistString
}

// RequeueFailedJobs remet en queue tous les jobs StatusFailed d'une watchlist.
// Appelé par SyncWatchlist pour combiner nouveaux tracks + retry des échecs.
func (jm *JobManager) RequeueFailedJobs(watchlistID string) (int, error) {
	jobs, err := jm.GetAllJobs()
	if err != nil {
		return 0, err
	}
	requeued := 0
	for _, job := range jobs {
		if job.WatchlistID != watchlistID || job.Status != StatusFailed {
			continue
		}
		job.Status = StatusPending
		job.Error = ""
		job.Progress = 0
		job.UpdatedAt = time.Now()
		if err := jm.saveJob(&job); err != nil {
			fmt.Printf("[Jobs] RequeueFailed: failed to save job %s: %v\n", job.ID, err)
			continue
		}
		backend.AddToQueue(job.ID, job.TrackName, job.ArtistName, job.AlbumName, job.SpotifyID)
		select {
		case jm.queue <- job.ID:
			requeued++
		default:
			fmt.Printf("[Jobs] Queue full, failed job %s will be picked up later\n", job.ID)
			requeued++
		}
	}
	if requeued > 0 {
		fmt.Printf("[Jobs] Requeued %d failed jobs for watchlist %s\n", requeued, watchlistID)
	}
	return requeued, nil
}

// maybeGenerateM3U8 génère le M3U8 si tous les jobs de la watchlist sont terminés.
// FIX #3 — whitelist des statuts terminaux au lieu de blacklist (plus robuste)
func (jm *JobManager) maybeGenerateM3U8(watchlistID string) {
	jobs, err := jm.GetAllJobs()
	if err != nil {
		return
	}

	// FIX #3 — un job est "en cours" seulement s'il est Pending ou Downloading
	// (évite qu'un état inconnu/corrompu bloque indéfiniment la génération)
	for _, j := range jobs {
		if j.WatchlistID != watchlistID {
			continue
		}
		if j.Status == StatusPending || j.Status == StatusDownloading {
			return // encore des jobs en cours
		}
	}

	var downloaded, skipped, failed int
	for _, j := range jobs {
		if j.WatchlistID != watchlistID {
			continue
		}
		switch j.Status {
		case StatusDone:
			downloaded++
		case StatusSkipped:
			skipped++
		case StatusFailed:
			failed++
		}
	}

	watcher := GetWatcher()
	if watcher == nil {
		return
	}
	playlists, err := watcher.GetWatchlists()
	if err != nil {
		return
	}
	for _, pl := range playlists {
		if pl.ID == watchlistID {
			if len(pl.SyncLogs) > 0 {
				last := &pl.SyncLogs[len(pl.SyncLogs)-1]
				last.Downloaded = downloaded
				last.Skipped = skipped
				last.Failed = failed
				if saveErr := watcher.saveWatchlist(&pl); saveErr != nil {
					fmt.Printf("[Watcher] Failed to save sync log: %v\n", saveErr)
				}
			}
			go watcher.generateM3U8ForPlaylist(pl)
			return
		}
	}
}
