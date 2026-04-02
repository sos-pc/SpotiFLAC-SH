package main

import (
	"context"
	"net/url"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
)

type App struct {
	ctx context.Context
	ctr *Container
}

func NewApp(ctr *Container) *App {
	return &App{ctr: ctr}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type SpotifyMetadataRequest struct {
	URL     string  `json:"url"`
	Batch   bool    `json:"batch"`
	Delay   float64 `json:"delay"`
	Timeout float64 `json:"timeout"`
}

type DownloadRequest = backend.DownloadRequest
type DownloadResponse = backend.DownloadResponse

// ─────────────────────────────────────────────────────────────────────────────
// Spotify metadata
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetStreamingURLs(spotifyTrackID string, region string) (string, error) {
	if spotifyTrackID == "" {
		return "", fmt.Errorf("spotify track ID is required")
	}
	fmt.Printf("[GetStreamingURLs] Called for track ID: %s, Region: %s\n", spotifyTrackID, region)
	jm := a.ctr.Jobs
	if jm == nil {
		return "", fmt.Errorf("job manager not initialized")
	}
	urls, err := jm.songLinkClient.GetAllURLsFromSpotify(spotifyTrackID, region)

	// Si Songlink échoue ou ne trouve rien (ex: 429), on tente une recherche directe sur l'API Tidal
	if (err != nil || urls == nil || (urls.TidalURL == "" && urls.AmazonURL == "")) {
		fmt.Printf("[GetStreamingURLs] Songlink failed/empty (%v), falling back to direct Tidal Search for ID: %s\n", err, spotifyTrackID)

		// 1. Récupérer le nom de la piste et de l'artiste depuis Spotify via l'ID
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)
		trackData, sErr := backend.GetFilteredSpotifyData(ctx, trackURL, false, 0)

		if sErr == nil {
			var trackResp struct {
				Track struct {
					Name    string `json:"name"`
					Artists []struct {
						Name string `json:"name"`
					} `json:"artists"`
				} `json:"track"`
			}

			if jsonData, jsonErr := json.Marshal(trackData); jsonErr == nil {
				if json.Unmarshal(jsonData, &trackResp) == nil && trackResp.Track.Name != "" && len(trackResp.Track.Artists) > 0 {
					artistName := trackResp.Track.Artists[0].Name
					trackName := trackResp.Track.Name

					// 2. Lancer la recherche sur l'API Tidal
					dl := backend.NewTidalDownloader("")
					if tidalURL, tErr := dl.SearchTidalByName(trackName, artistName); tErr == nil && tidalURL != "" {
						if urls == nil {
							urls = &backend.SongLinkURLs{}
						}
						urls.TidalURL = tidalURL
						fmt.Printf("[GetStreamingURLs] ✓ Fallback successful: Found Tidal URL %s\n", tidalURL)
						err = nil // On efface l'erreur Songlink pour le frontend
					} else {
						fmt.Printf("[GetStreamingURLs] Tidal direct search failed: %v\n", tErr)
					}
				}
			}
		}
	}

	if err != nil {
		return "", err
	}
	jsonData, err := json.Marshal(urls)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
}


// normalizeSpotifyURL supprime le préfixe intl-xx/ et le paramètre ?si=
// ex: https://open.spotify.com/intl-fr/album/ID?si=xxx → https://open.spotify.com/album/ID
func normalizeSpotifyURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	// Supprimer query params (si=, etc.)
	parsed.RawQuery = ""
	// Supprimer segment intl-xx
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if !strings.HasPrefix(p, "intl-") {
			filtered = append(filtered, p)
		}
	}
	parsed.Path = "/" + strings.Join(filtered, "/")
	return parsed.String()
}

func (a *App) GetSpotifyMetadata(req SpotifyMetadataRequest) (string, error) {
	if req.URL == "" {
		return "", fmt.Errorf("URL parameter is required")
	}
	// Normaliser l'URL : supprimer intl-xx/ et ?si=... pour compatibilité API externe
	req.URL = normalizeSpotifyURL(req.URL)
	if req.Delay == 0 {
		req.Delay = 1.0
	}
	if req.Timeout == 0 {
		req.Timeout = 300.0
	}

	metaCtx, metaCancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout*float64(time.Second)))
	defer metaCancel()

	var spotFetchAPIURL string
	settings, err := a.LoadSettings()
	if err == nil && settings != nil {
		if apiURL, ok := settings["spotFetchAPIUrl"].(string); ok {
			spotFetchAPIURL = apiURL
		}
	}

	// Client natif Spotify (TOTP) — avec fallback automatique vers SpotFetch si échec
	data, nativeErr := backend.GetFilteredSpotifyData(metaCtx, req.URL, req.Batch, time.Duration(req.Delay*float64(time.Second)))
	if nativeErr == nil {
		jsonData, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to encode response: %v", err)
		}
		return string(jsonData), nil
	}

	// Fallback automatique vers SpotFetch si disponible
	if spotFetchAPIURL != "" {
		data, err := backend.GetSpotifyDataWithAPI(metaCtx, req.URL, true, spotFetchAPIURL, req.Batch, time.Duration(req.Delay*float64(time.Second)))
		if err != nil {
			return "", fmt.Errorf("failed to fetch metadata (native: %v, spotfetch: %v)", nativeErr, err)
		}
		jsonData, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to encode response: %v", err)
		}
		return string(jsonData), nil
	}

	return "", fmt.Errorf("failed to fetch metadata: %v", nativeErr)
}

type SpotifySearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (a *App) SearchSpotify(req SpotifySearchRequest) (*backend.SearchResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return backend.SearchSpotify(ctx, req.Query, req.Limit)
}

type SpotifySearchByTypeRequest struct {
	Query      string `json:"query"`
	SearchType string `json:"search_type"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

func (a *App) SearchSpotifyByType(req SpotifySearchByTypeRequest) ([]backend.SearchResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if req.SearchType == "" {
		return nil, fmt.Errorf("search type is required")
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return backend.SearchSpotifyByType(ctx, req.Query, req.SearchType, req.Limit, req.Offset)
}

// ─────────────────────────────────────────────────────────────────────────────
// Download
// ─────────────────────────────────────────────────────────────────────────────

// ApplySettingsFallbacks fills zero-value fields in req with the user's saved
// global settings. Intended for REST API callers that send minimal payloads;
// the Wails frontend always provides all fields explicitly.
func (a *App) ApplySettingsFallbacks(req *DownloadRequest) {
	settings, err := a.LoadSettings()
	if err != nil || settings == nil {
		return
	}
	getBool := func(key string) (bool, bool) {
		if v, ok := settings[key]; ok {
			if b, ok := v.(bool); ok {
				return b, true
			}
		}
		return false, false
	}
	getString := func(key string) string {
		if v, ok := settings[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	if req.OutputDir == "" {
		req.OutputDir = getString("downloadPath")
	}
	if req.FilenameFormat == "" {
		req.FilenameFormat = getString("filenameTemplate")
	}
	if req.AudioFormat == "" {
		switch req.Service {
		case "qobuz":
			req.AudioFormat = getString("qobuzQuality")
		default:
			req.AudioFormat = getString("tidalQuality")
		}
	}
	if req.AutoOrder == "" {
		req.AutoOrder = getString("autoOrder")
	}
	if !req.EmbedLyrics {
		if v, ok := getBool("embedLyrics"); ok {
			req.EmbedLyrics = v
		}
	}
	if !req.EmbedMaxQualityCover {
		if v, ok := getBool("embedMaxQualityCover"); ok {
			req.EmbedMaxQualityCover = v
		}
	}
	if !req.AllowFallback {
		if v, ok := getBool("allowFallback"); ok {
			req.AllowFallback = v
		}
	}
	if !req.UseFirstArtistOnly {
		if v, ok := getBool("useFirstArtistOnly"); ok {
			req.UseFirstArtistOnly = v
		}
	}
	if !req.UseSingleGenre {
		if v, ok := getBool("useSingleGenre"); ok {
			req.UseSingleGenre = v
		}
	}
	if !req.EmbedGenre {
		if v, ok := getBool("embedGenre"); ok {
			req.EmbedGenre = v
		}
	}
	if !req.TrackNumber {
		if v, ok := getBool("trackNumber"); ok {
			req.TrackNumber = v
		}
	}
}

func (a *App) DownloadTrack(req DownloadRequest) (DownloadResponse, error) {
	if req.Service == "" {
		req.Service = "auto"
	}

	itemID := req.ItemID
	if itemID == "" {
		if req.SpotifyID != "" {
			itemID = fmt.Sprintf("%s-%d", req.SpotifyID, time.Now().UnixNano())
		} else {
			itemID = fmt.Sprintf("%s-%s-%d", req.TrackName, req.ArtistName, time.Now().UnixNano())
		}
	}

	jm := a.ctr.Jobs
	if jm == nil {
		return DownloadResponse{Success: false, Error: "JobManager not initialized"}, fmt.Errorf("job manager not initialized")
	}

	// Récupération des métadonnées Spotify manquantes (AlbumArtist, Duration, etc.) si possible
	if req.SpotifyID != "" && (req.AlbumArtist == "" || req.Duration == 0) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", req.SpotifyID)
		if trackData, err := backend.GetFilteredSpotifyData(ctx, trackURL, false, 0); err == nil {
			var trackResp struct {
				Track struct {
					Album struct {
						Artists []struct{ Name string `json:"name"` } `json:"artists"`
					} `json:"album"`
					DurationMs int `json:"duration_ms"`
				} `json:"track"`
			}
			jsonData, _ := json.Marshal(trackData)
			if json.Unmarshal(jsonData, &trackResp) == nil {
				if req.AlbumArtist == "" && len(trackResp.Track.Album.Artists) > 0 {
					req.AlbumArtist = trackResp.Track.Album.Artists[0].Name
				}
				if req.Duration == 0 && trackResp.Track.DurationMs > 0 {
					req.Duration = trackResp.Track.DurationMs / 1000
				}
			}
		}
	}

	// Création du Job
	job := &Job{
		ID:          itemID,
		SpotifyID:   req.SpotifyID,
		TrackName:   req.TrackName,
		ArtistName:  req.ArtistName,
		AlbumName:   req.AlbumName,
		AlbumArtist: req.AlbumArtist,
		ReleaseDate: req.ReleaseDate,
		CoverURL:    req.CoverURL,
		TrackNumber: req.SpotifyTrackNumber,
		DiscNumber:  req.SpotifyDiscNumber,
		TotalTracks: req.SpotifyTotalTracks,
		TotalDiscs:  req.SpotifyTotalDiscs,
		Copyright:   req.Copyright,
		Publisher:   req.Publisher,
		Position:    req.Position,
		PlaylistName: req.PlaylistName,
		DurationMs:  req.Duration * 1000,
		UserID:      req.UserID,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Settings: JobSettings{
			Service:              req.Service,
			DownloadPath:         req.OutputDir,
			FilenameTemplate:     req.FilenameFormat,
			FolderTemplate:       "", // outputDir is pre-built by the frontend (folder template already applied)
			TrackNumber:          req.TrackNumber,
			EmbedLyrics:          req.EmbedLyrics,
			EmbedMaxQualityCover: req.EmbedMaxQualityCover,
			AutoOrder:            req.AutoOrder,
			TidalQuality:         tidalQualityFromFormat(req.AudioFormat),
			QobuzQuality:         qobuzQualityFromFormat(req.AudioFormat),
			AutoQuality: func() string {
				if req.AudioFormat == "HI_RES_LOSSLESS" || req.AudioFormat == "HI_RES" {
					return "24"
				}
				return ""
			}(),
			UseFirstArtistOnly: req.UseFirstArtistOnly,
			UseSingleGenre:     req.UseSingleGenre,
			EmbedGenre:         req.EmbedGenre,
			AllowFallback:      req.AllowFallback,
			Region:             "", // Region is rarely used in manual download
		},
	}

	// Ajout à la base de données et à la queue via les méthodes thread-safe de JobManager
	jm.saveJob(job)

	select {
	case jm.queue <- job.ID:
		fmt.Printf("[Download] Job %s added to queue\n", job.ID)
	default:
		fmt.Printf("[Download] Queue full, job %s will be picked up later\n", job.ID)
	}

	// Informe le frontend (qui gère l'état via l'historique/queue)
	return DownloadResponse{
		Success: true,
		Message: "Added to download queue",
		ItemID:  itemID,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Misc
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) OpenFolder(path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	return backend.OpenFolderInExplorer(path)
}

func (a *App) GetDefaults() map[string]string {
	return map[string]string{
		"downloadPath": util.GetDefaultMusicPath(),
	}
}

func (a *App) GetDownloadProgress() util.ProgressInfo {
	jm := a.ctr.Jobs
	if jm == nil {
		return util.GetDownloadProgress()
	}
	jobs, _ := jm.GetAllJobs()
	var total, done int
	for _, j := range jobs {
		total++
		if j.Status == StatusDone || j.Status == StatusSkipped {
			done++
		}
	}
	return util.ProgressInfo{IsDownloading: total > 0 && done < total}
}

func (a *App) GetDownloadQueue() util.DownloadQueueInfo {
	jm := a.ctr.Jobs
	if jm == nil {
		return util.GetDownloadQueue()
	}
	jobs, err := jm.GetAllJobs()
	if err != nil {
		return util.DownloadQueueInfo{}
	}
	items := make([]util.DownloadItem, 0, len(jobs))
	var queued, completed, failed, skipped int
	isDownloading := false
	for _, job := range jobs {
		ds := jobStatusToDownloadStatus(job.Status)
		switch ds {
		case util.StatusQueued:
			queued++
		case util.StatusDownloading:
			isDownloading = true
		case util.StatusCompleted:
			completed++
		case util.StatusFailed:
			failed++
		case util.StatusSkipped:
			skipped++
		}
		liveProgress, liveSpeed := util.GetItemProgress(job.ID)
		progress := job.Progress
		speed := liveSpeed
		if ds == util.StatusDownloading && liveProgress > 0 {
			progress = liveProgress
		}
		items = append(items, util.DownloadItem{
			ID:           job.ID,
			TrackName:    job.TrackName,
			ArtistName:   job.ArtistName,
			AlbumName:    job.AlbumName,
			SpotifyID:    job.SpotifyID,
			Status:       ds,
			ErrorMessage: job.Error,
			FilePath:     job.FilePath,
			StartTime:    func() int64 { if job.CreatedAt.IsZero() { return 0 }; return job.CreatedAt.Unix() }(),
			EndTime:      func() int64 { if job.UpdatedAt.IsZero() { return 0 }; return job.UpdatedAt.Unix() }(),
			StartedAt:    func() int64 { if job.StartedAt.IsZero() { return 0 }; return job.StartedAt.Unix() }(),
			TotalSize:    job.TotalSize,
			Progress:     progress,
			Speed:        speed,
		})
	}
	progInfo := util.GetDownloadProgress()
	var totalDL float64
	for _, item := range items {
		if item.Status == util.StatusCompleted && item.TotalSize > 0 {
			totalDL += item.TotalSize
		}
	}
	var sessionStart int64
	for _, item := range items {
		if sessionStart == 0 || item.StartTime < sessionStart {
			sessionStart = item.StartTime
		}
	}
	return util.DownloadQueueInfo{
		IsDownloading:    isDownloading,
		Queue:            items,
		QueuedCount:      queued,
		CompletedCount:   completed,
		FailedCount:      failed,
		SkippedCount:     skipped,
		CurrentSpeed:     progInfo.SpeedMBps,
		TotalDownloaded:  totalDL,
		SessionStartTime: sessionStart,
	}
}

func (a *App) ClearCompletedDownloads() {
	jm := a.ctr.Jobs
	if jm == nil {
		util.ClearDownloadQueue()
		return
	}
	jm.ClearCompletedJobs()
}

func (a *App) ClearAllDownloads() {
	jm := a.ctr.Jobs
	if jm == nil {
		util.ClearAllDownloads()
		return
	}
	jm.ClearAllJobs()
}

func (a *App) AddToDownloadQueue(spotifyID, trackName, artistName, albumName string) string {
	itemID := fmt.Sprintf("%s-%d", spotifyID, time.Now().UnixNano())
	util.AddToQueue(itemID, trackName, artistName, albumName, "")
	return itemID
}

func (a *App) MarkDownloadItemFailed(itemID, errorMsg string) {
	util.FailDownloadItem(itemID, errorMsg)
}

func (a *App) CancelAllQueuedItems() {
	util.CancelAllQueuedItems()
}

// FIX ExportFailedDownloads — utilise a.GetDownloadQueue() (lit le JobManager BoltDB)
// au lieu de util.GetDownloadQueue() (queue in-memory uniquement)
func (a *App) ExportFailedDownloads() (string, error) {
	queueInfo := a.GetDownloadQueue()
	var failedItems []string

	hasFailed := false
	for _, item := range queueInfo.Queue {
		if item.Status == util.StatusFailed {
			hasFailed = true
			break
		}
	}

	if !hasFailed {
		return "No failed downloads to export.", nil
	}

	failedItems = append(failedItems, fmt.Sprintf("Failed Downloads Report - %s", time.Now().Format("2006-01-02 15:04:05")))
	failedItems = append(failedItems, strings.Repeat("-", 50))
	failedItems = append(failedItems, "")

	count := 0
	for _, item := range queueInfo.Queue {
		if item.Status == util.StatusFailed {
			count++
			line := fmt.Sprintf("%d. %s - %s", count, item.TrackName, item.ArtistName)
			if item.AlbumName != "" {
				line += fmt.Sprintf(" (%s)", item.AlbumName)
			}
			failedItems = append(failedItems, line)
			failedItems = append(failedItems, fmt.Sprintf("   Error: %s", item.ErrorMessage))
			if item.SpotifyID != "" {
				failedItems = append(failedItems, fmt.Sprintf("   ID: %s", item.SpotifyID))
				failedItems = append(failedItems, fmt.Sprintf("   URL: https://open.spotify.com/track/%s", item.SpotifyID))
			}
			failedItems = append(failedItems, "")
		}
	}

	content := strings.Join(failedItems, "\n")
	return fmt.Sprintf("EXPORT:%s", content), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// History — FIX #4 : toutes les méthodes acceptent userID
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetDownloadHistory(userID string) ([]backend.HistoryItem, error) {
	return backend.GetHistoryItems("SpotiFLAC", userID)
}

func (a *App) ClearDownloadHistory(userID string) error {
	return backend.ClearHistory("SpotiFLAC", userID)
}

func (a *App) DeleteDownloadHistoryItem(id string, userID string) error {
	return backend.DeleteHistoryItem(id, "SpotiFLAC", userID)
}

func (a *App) GetFetchHistory(userID string) ([]backend.FetchHistoryItem, error) {
	return backend.GetFetchHistoryItems("SpotiFLAC", userID)
}

func (a *App) AddFetchHistory(item backend.FetchHistoryItem) error {
	return backend.AddFetchHistoryItem(item, "SpotiFLAC")
}

func (a *App) ClearFetchHistory(userID string) error {
	return backend.ClearFetchHistory("SpotiFLAC", userID)
}

func (a *App) ClearFetchHistoryByType(itemType string, userID string) error {
	return backend.ClearFetchHistoryByType(itemType, "SpotiFLAC", userID)
}

func (a *App) DeleteFetchHistoryItem(id string, userID string) error {
	return backend.DeleteFetchHistoryItem(id, "SpotiFLAC", userID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Audio analysis
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) AnalyzeTrack(filePath string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("file path is required")
	}
	result, err := backend.AnalyzeTrack(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to analyze track: %v", err)
	}
	jsonData, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
}

func (a *App) AnalyzeMultipleTracks(filePaths []string) (string, error) {
	if len(filePaths) == 0 {
		return "", fmt.Errorf("at least one file path is required")
	}
	results := make([]*backend.AnalysisResult, 0, len(filePaths))
	for _, filePath := range filePaths {
		result, err := backend.AnalyzeTrack(filePath)
		if err != nil {
			continue
		}
		results = append(results, result)
	}
	jsonData, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Lyrics / Cover / Media
// ─────────────────────────────────────────────────────────────────────────────

type LyricsDownloadRequest struct {
	SpotifyID           string `json:"spotify_id"`
	TrackName           string `json:"track_name"`
	ArtistName          string `json:"artist_name"`
	AlbumName           string `json:"album_name"`
	AlbumArtist         string `json:"album_artist"`
	ReleaseDate         string `json:"release_date"`
	OutputDir           string `json:"output_dir"`
	FilenameFormat      string `json:"filename_format"`
	TrackNumber         bool   `json:"track_number"`
	Position            int    `json:"position"`
	UseAlbumTrackNumber bool   `json:"use_album_track_number"`
	DiscNumber          int    `json:"disc_number"`
}

func (a *App) DownloadLyrics(req LyricsDownloadRequest) (meta.LyricsDownloadResponse, error) {
	if req.SpotifyID == "" {
		return meta.LyricsDownloadResponse{Success: false, Error: "Spotify ID is required"},
			fmt.Errorf("spotify ID is required")
	}
	client := meta.NewLyricsClient()
	backendReq := meta.LyricsDownloadRequest{
		SpotifyID:           req.SpotifyID,
		TrackName:           req.TrackName,
		ArtistName:          req.ArtistName,
		AlbumName:           req.AlbumName,
		AlbumArtist:         req.AlbumArtist,
		ReleaseDate:         req.ReleaseDate,
		OutputDir:           req.OutputDir,
		FilenameFormat:      req.FilenameFormat,
		TrackNumber:         req.TrackNumber,
		Position:            req.Position,
		UseAlbumTrackNumber: req.UseAlbumTrackNumber,
		DiscNumber:          req.DiscNumber,
	}
	resp, err := client.DownloadLyrics(backendReq)
	if err != nil {
		return meta.LyricsDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

type CoverDownloadRequest struct {
	CoverURL       string `json:"cover_url"`
	TrackName      string `json:"track_name"`
	ArtistName     string `json:"artist_name"`
	AlbumName      string `json:"album_name"`
	AlbumArtist    string `json:"album_artist"`
	ReleaseDate    string `json:"release_date"`
	OutputDir      string `json:"output_dir"`
	FilenameFormat string `json:"filename_format"`
	TrackNumber    bool   `json:"track_number"`
	Position       int    `json:"position"`
	DiscNumber     int    `json:"disc_number"`
}

func (a *App) DownloadCover(req CoverDownloadRequest) (meta.CoverDownloadResponse, error) {
	if req.CoverURL == "" {
		return meta.CoverDownloadResponse{Success: false, Error: "Cover URL is required"},
			fmt.Errorf("cover URL is required")
	}
	client := meta.NewCoverClient()
	backendReq := meta.CoverDownloadRequest{
		CoverURL:       req.CoverURL,
		TrackName:      req.TrackName,
		ArtistName:     req.ArtistName,
		AlbumName:      req.AlbumName,
		AlbumArtist:    req.AlbumArtist,
		ReleaseDate:    req.ReleaseDate,
		OutputDir:      req.OutputDir,
		FilenameFormat: req.FilenameFormat,
		TrackNumber:    req.TrackNumber,
		Position:       req.Position,
		DiscNumber:     req.DiscNumber,
	}
	resp, err := client.DownloadCover(backendReq)
	if err != nil {
		return meta.CoverDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

type HeaderDownloadRequest struct {
	HeaderURL  string `json:"header_url"`
	ArtistName string `json:"artist_name"`
	OutputDir  string `json:"output_dir"`
}

func (a *App) DownloadHeader(req HeaderDownloadRequest) (meta.HeaderDownloadResponse, error) {
	if req.HeaderURL == "" {
		return meta.HeaderDownloadResponse{Success: false, Error: "Header URL is required"},
			fmt.Errorf("header URL is required")
	}
	if req.ArtistName == "" {
		return meta.HeaderDownloadResponse{Success: false, Error: "Artist name is required"},
			fmt.Errorf("artist name is required")
	}
	client := meta.NewCoverClient()
	resp, err := client.DownloadHeader(meta.HeaderDownloadRequest{
		HeaderURL:  req.HeaderURL,
		ArtistName: req.ArtistName,
		OutputDir:  req.OutputDir,
	})
	if err != nil {
		return meta.HeaderDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

type GalleryImageDownloadRequest struct {
	ImageURL   string `json:"image_url"`
	ArtistName string `json:"artist_name"`
	ImageIndex int    `json:"image_index"`
	OutputDir  string `json:"output_dir"`
}

func (a *App) DownloadGalleryImage(req GalleryImageDownloadRequest) (meta.GalleryImageDownloadResponse, error) {
	if req.ImageURL == "" {
		return meta.GalleryImageDownloadResponse{Success: false, Error: "Image URL is required"},
			fmt.Errorf("image URL is required")
	}
	if req.ArtistName == "" {
		return meta.GalleryImageDownloadResponse{Success: false, Error: "Artist name is required"},
			fmt.Errorf("artist name is required")
	}
	client := meta.NewCoverClient()
	resp, err := client.DownloadGalleryImage(meta.GalleryImageDownloadRequest{
		ImageURL:   req.ImageURL,
		ArtistName: req.ArtistName,
		ImageIndex: req.ImageIndex,
		OutputDir:  req.OutputDir,
	})
	if err != nil {
		return meta.GalleryImageDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

type AvatarDownloadRequest struct {
	AvatarURL  string `json:"avatar_url"`
	ArtistName string `json:"artist_name"`
	OutputDir  string `json:"output_dir"`
}

func (a *App) DownloadAvatar(req AvatarDownloadRequest) (meta.AvatarDownloadResponse, error) {
	if req.AvatarURL == "" {
		return meta.AvatarDownloadResponse{Success: false, Error: "Avatar URL is required"},
			fmt.Errorf("avatar URL is required")
	}
	if req.ArtistName == "" {
		return meta.AvatarDownloadResponse{Success: false, Error: "Artist name is required"},
			fmt.Errorf("artist name is required")
	}
	client := meta.NewCoverClient()
	resp, err := client.DownloadAvatar(meta.AvatarDownloadRequest{
		AvatarURL:  req.AvatarURL,
		ArtistName: req.ArtistName,
		OutputDir:  req.OutputDir,
	})
	if err != nil {
		return meta.AvatarDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

func (a *App) CheckTrackAvailability(spotifyTrackID string) (string, error) {
	if spotifyTrackID == "" {
		return "", fmt.Errorf("spotify track ID is required")
	}
	jm := a.ctr.Jobs
	if jm == nil {
		return "", fmt.Errorf("job manager not initialized")
	}
	availability, err := jm.songLinkClient.CheckTrackAvailability(spotifyTrackID)
	if err != nil {
		return "", err
	}
	jsonData, err := json.Marshal(availability)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// FFmpeg
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) IsFFmpegInstalled() (bool, error)  { return backend.IsFFmpegInstalled() }
func (a *App) IsFFprobeInstalled() (bool, error) { return backend.IsFFprobeInstalled() }
func (a *App) GetFFmpegPath() (string, error)    { return util.GetFFmpegPath() }
func (a *App) CheckFFmpegInstalled() (bool, error) { return backend.IsFFmpegInstalled() }

// ─────────────────────────────────────────────────────────────────────────────
// Audio / File tools
// ─────────────────────────────────────────────────────────────────────────────

type ConvertAudioRequest struct {
	InputFiles   []string `json:"input_files"`
	OutputFormat string   `json:"output_format"`
	Bitrate      string   `json:"bitrate"`
	Codec        string   `json:"codec"`
}

func (a *App) ConvertAudio(req ConvertAudioRequest) ([]backend.ConvertAudioResult, error) {
	return backend.ConvertAudio(backend.ConvertAudioRequest{
		InputFiles:   req.InputFiles,
		OutputFormat: req.OutputFormat,
		Bitrate:      req.Bitrate,
		Codec:        req.Codec,
	})
}

func (a *App) GetFileSizes(files []string) map[string]int64  { return backend.GetFileSizes(files) }
func (a *App) ListDirectoryFiles(dirPath string) ([]backend.FileInfo, error) {
	if dirPath == "" {
		return nil, fmt.Errorf("directory path is required")
	}
	return backend.ListDirectory(dirPath)
}
func (a *App) ListAudioFilesInDir(dirPath string) ([]backend.FileInfo, error) {
	if dirPath == "" {
		return nil, fmt.Errorf("directory path is required")
	}
	return backend.ListAudioFiles(dirPath)
}
func (a *App) ReadFileMetadata(filePath string) (*backend.AudioMetadata, error) {
	if filePath == "" {
		return nil, fmt.Errorf("file path is required")
	}
	return backend.ReadAudioMetadata(filePath)
}
func (a *App) PreviewRenameFiles(files []string, format string) []backend.RenamePreview {
	return backend.PreviewRename(files, format)
}
func (a *App) RenameFilesByMetadata(files []string, format string) []backend.RenameResult {
	return backend.RenameFiles(files, format)
}
func (a *App) ReadTextFile(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
func (a *App) RenameFileTo(oldPath, newName string) error {
	dir := filepath.Dir(oldPath)
	ext := filepath.Ext(oldPath)
	newPath := filepath.Join(dir, newName+ext)
	return os.Rename(oldPath, newPath)
}
func (a *App) UploadImage(filePath string) (string, error) {
	return backend.UploadToSendNow(filePath)
}
func (a *App) UploadImageBytes(filename string, base64Data string) (string, error) {
	if idx := strings.Index(base64Data, ","); idx != -1 {
		base64Data = base64Data[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %v", err)
	}
	return backend.UploadBytesToSendNow(filename, data)
}
func (a *App) ReadImageAsBase64(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	var mimeType string
	switch ext {
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".png":
		mimeType = "image/png"
	case ".gif":
		mimeType = "image/gif"
	case ".webp":
		mimeType = "image/webp"
	default:
		mimeType = "image/jpeg"
	}
	encoded := base64.StdEncoding.EncodeToString(content)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CheckFilesExistence
// ─────────────────────────────────────────────────────────────────────────────

type CheckFileExistenceRequest struct {
	SpotifyID           string `json:"spotify_id"`
	TrackName           string `json:"track_name"`
	ArtistName          string `json:"artist_name"`
	AlbumName           string `json:"album_name,omitempty"`
	AlbumArtist         string `json:"album_artist,omitempty"`
	ReleaseDate         string `json:"release_date,omitempty"`
	TrackNumber         int    `json:"track_number,omitempty"`
	DiscNumber          int    `json:"disc_number,omitempty"`
	Position            int    `json:"position,omitempty"`
	UseAlbumTrackNumber bool   `json:"use_album_track_number,omitempty"`
	FilenameFormat      string `json:"filename_format,omitempty"`
	IncludeTrackNumber  bool   `json:"include_track_number,omitempty"`
	AudioFormat         string `json:"audio_format,omitempty"`
	RelativePath        string `json:"relative_path,omitempty"`
}

type CheckFileExistenceResult struct {
	SpotifyID  string `json:"spotify_id"`
	Exists     bool   `json:"exists"`
	FilePath   string `json:"file_path,omitempty"`
	TrackName  string `json:"track_name,omitempty"`
	ArtistName string `json:"artist_name,omitempty"`
}

func (a *App) CheckFilesExistence(outputDir string, rootDir string, tracks []CheckFileExistenceRequest) []CheckFileExistenceResult {
	if len(tracks) == 0 {
		return []CheckFileExistenceResult{}
	}

	outputDir = util.NormalizePath(outputDir)
	if rootDir != "" {
		rootDir = util.NormalizePath(rootDir)
	}

	defaultFilenameFormat := "title-artist"

	type result struct {
		index  int
		result CheckFileExistenceResult
	}

	resultsChan := make(chan result, len(tracks))

	var rootDirFiles map[string]string
	rootDirFilesOnce := false
	getRootDirFiles := func() map[string]string {
		if rootDirFilesOnce {
			return rootDirFiles
		}
		rootDirFiles = make(map[string]string)
		if rootDir != "" && rootDir != outputDir {
			filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !info.IsDir() {
					if strings.EqualFold(filepath.Ext(path), ".flac") || strings.EqualFold(filepath.Ext(path), ".mp3") {
						rootDirFiles[info.Name()] = path
					}
				}
				return nil
			})
		}
		rootDirFilesOnce = true
		return rootDirFiles
	}

	for i, track := range tracks {
		go func(idx int, t CheckFileExistenceRequest) {
			res := CheckFileExistenceResult{
				SpotifyID:  t.SpotifyID,
				TrackName:  t.TrackName,
				ArtistName: t.ArtistName,
				Exists:     false,
			}
			if t.TrackName == "" || t.ArtistName == "" {
				resultsChan <- result{index: idx, result: res}
				return
			}
			filenameFormat := t.FilenameFormat
			if filenameFormat == "" {
				filenameFormat = defaultFilenameFormat
			}
			trackNumber := t.Position
			if t.UseAlbumTrackNumber && t.TrackNumber > 0 {
				trackNumber = t.TrackNumber
			}
			fileExt := ".flac"
			if t.AudioFormat == "mp3" {
				fileExt = ".mp3"
			}
			expectedFilenameBase := util.BuildExpectedFilename(
				t.TrackName, t.ArtistName, t.AlbumName, t.AlbumArtist, t.ReleaseDate,
				filenameFormat, "", "", t.IncludeTrackNumber, trackNumber, t.DiscNumber, t.UseAlbumTrackNumber,
			)
			expectedFilename := strings.TrimSuffix(expectedFilenameBase, ".flac") + fileExt
			targetDir := outputDir
			if t.RelativePath != "" {
				targetDir = filepath.Join(outputDir, t.RelativePath)
			}
			expectedPath := filepath.Join(targetDir, expectedFilename)
			if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 100*1024 {
				res.Exists = true
				res.FilePath = expectedPath
			} else {
				res.FilePath = expectedFilename
			}
			resultsChan <- result{index: idx, result: res}
		}(i, track)
	}

	results := make([]CheckFileExistenceResult, len(tracks))
	missingIndices := []int{}
	for i := 0; i < len(tracks); i++ {
		r := <-resultsChan
		results[r.index] = r.result
		if !results[r.index].Exists {
			missingIndices = append(missingIndices, r.index)
		}
	}

	if len(missingIndices) > 0 && rootDir != "" {
		filesMap := getRootDirFiles()
		if len(filesMap) > 0 {
			for _, idx := range missingIndices {
				expectedFilename := results[idx].FilePath
				baseName := filepath.Base(expectedFilename)
				if path, ok := filesMap[baseName]; ok {
					results[idx].Exists = true
					results[idx].FilePath = path
				} else {
					results[idx].FilePath = ""
				}
			}
		} else {
			for _, idx := range missingIndices {
				results[idx].FilePath = ""
			}
		}
	} else {
		for _, idx := range missingIndices {
			results[idx].FilePath = ""
		}
	}

	return results
}

// ─────────────────────────────────────────────────────────────────────────────
// Settings
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetConfigPath() (string, error) {
	dir, err := util.GetFFmpegDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func (a *App) SaveSettings(settings map[string]interface{}) error {
	configPath, err := a.GetConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(configPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

func (a *App) LoadSettings() (map[string]interface{}, error) {
	configPath, err := a.GetConfigPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func (a *App) GetOSInfo() (string, error) { return util.GetOSInfo() }

func (a *App) CreateM3U8File(m3u8Name string, outputDir string, filePaths []string, jellyfinMusicPath string) error {
	if len(filePaths) == 0 {
		return nil
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	safeName := util.SanitizeFilename(m3u8Name)
	if safeName == "" {
		safeName = "playlist"
	}
	m3u8Path := filepath.Join(outputDir, safeName+".m3u8")
	f, err := os.Create(m3u8Path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString("#EXTM3U\n"); err != nil {
		return err
	}
	for _, path := range filePaths {
		if path == "" {
			continue
		}
		var entry string
		if jellyfinMusicPath != "" {
			entry = strings.Replace(path, "/home/nonroot/Music", strings.TrimRight(jellyfinMusicPath, "/"), 1)
			entry = filepath.ToSlash(entry)
		} else {
			relPath, err := filepath.Rel(outputDir, path)
			if err != nil {
				relPath = path
			}
			entry = filepath.ToSlash(relPath)
		}
		if _, err := f.WriteString(entry + "\n"); err != nil {
			return err
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Misc helpers
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) SkipDownloadItem(itemID, filePath string) { util.SkipDownloadItem(itemID, filePath) }
func (a *App) GetPreviewURL(trackID string) (string, error) { return backend.GetPreviewURL(trackID) }

func jobStatusToDownloadStatus(s JobStatus) util.DownloadStatus {
	switch s {
	case StatusPending:
		return util.StatusQueued
	case StatusDownloading:
		return util.StatusDownloading
	case StatusDone:
		return util.StatusCompleted
	case StatusFailed:
		return util.StatusFailed
	case StatusSkipped:
		return util.StatusSkipped
	default:
		return util.StatusQueued
	}
}

func (a *App) EnqueueBatch(req EnqueueBatchRequest) (EnqueueBatchResponse, error) {
	jm := a.ctr.Jobs
	if jm == nil {
		return EnqueueBatchResponse{}, fmt.Errorf("job manager not initialized")
	}
	return jm.EnqueueBatch(req)
}

// shutdown est appelé par main.go à l'arrêt (no-op en mode web)
func (a *App) shutdown(ctx context.Context) {}
