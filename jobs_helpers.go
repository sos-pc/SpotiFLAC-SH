package main

// ─────────────────────────────────────────────────────────────────────────────
// Business helpers for JobManager:
//   streaming URL resolution, output path building, filename checking,
//   batch completion tracking, failed-job requeue
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/tidal"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// ─────────────────────────────────────────────────────────────────────────────
// Streaming URL resolution
// ─────────────────────────────────────────────────────────────────────────────

// getStreamingURLs resolves Tidal/Amazon/ISRC URLs for a job.
// Priority: ISRC-direct (Spotify's own catalog record) enriches whatever the
// URL chain below finds → Deezer (no rate-limit) → Songlink → HTML scraping
// fallbacks.
// FIX #2 — respects cancellation context while waiting on the semaphore.
func (jm *JobManager) getStreamingURLs(job *Job) map[string]string {
	s := job.Settings

	if s.Service == "deezer" {
		return nil
	}

	result := jm.getStreamingURLsViaFallbackChain(job)

	// ISRC-direct is name-match-free (reads Spotify's own catalog record for
	// this exact track), so it's more trustworthy than whatever the chain
	// above found via Deezer/Song.link name search — it overrides rather
	// than just fills a gap. Tidal/Amazon URLs from the chain are kept as-is.
	if directISRC := jm.resolveISRCDirect(job); directISRC != "" {
		if result == nil {
			result = make(map[string]string)
		}
		result["isrc"] = directISRC
	}

	return result
}

// resolveISRCDirect resolves job.SpotifyID's ISRC straight from Spotify's
// metadata, independent of the Deezer/Song.link URL chain. Cached (see
// songlink.GetISRCDirect), so repeat downloads/retags of the same track
// don't pay for a fresh lookup.
func (jm *JobManager) resolveISRCDirect(job *Job) string {
	if job.SpotifyID == "" {
		return ""
	}

	isrc, err := jm.songLinkClient.GetISRCDirect(job.SpotifyID)
	if err != nil {
		slog.Debug("[Jobs] ISRC-direct failed", "track", job.TrackName, "err", err)
		return ""
	}
	return isrc
}

// getStreamingURLsViaFallbackChain is getStreamingURLs' pre-existing
// Deezer/Song.link URL resolution, factored out so getStreamingURLs can
// enrich its result with the ISRC-direct lookup above without duplicating
// this logic.
func (jm *JobManager) getStreamingURLsViaFallbackChain(job *Job) map[string]string {
	s := job.Settings

	// Amazon URLs only come from Songlink.
	if s.Service == "amazon" {
		return jm.getStreamingURLsViaSonglink(job)
	}

	// 1. Deezer public API (no rate-limit, ~fast)
	if job.TrackName != "" && job.ArtistName != "" {
		if fallback, ferr := songlink.GetDeezerSearchFallback(job.TrackName, job.ArtistName); ferr == nil && fallback != nil {
			result := make(map[string]string)
			if fallback.ISRC != "" {
				result["isrc"] = fallback.ISRC
			}
			if fallback.TidalURL != "" {
				result["tidal_url"] = fallback.TidalURL
			}
			if fallback.AmazonURL != "" {
				result["amazon_url"] = fallback.AmazonURL
			}
			if len(result) > 0 {
				slog.Debug("[Jobs] Deezer OK", "track", job.TrackName, "isrc", result["isrc"])
				return result
			}
		} else if ferr != nil {
			slog.Debug("[Jobs] Deezer failed, trying Songlink", "track", job.TrackName, "err", ferr)
		}
	}

	// 2. Songlink as last resort
	return jm.getStreamingURLsViaSonglink(job)
}

// getStreamingURLsViaSonglink calls Songlink with rate-limiting semaphore.
func (jm *JobManager) getStreamingURLsViaSonglink(job *Job) map[string]string {
	select {
	case jm.songLinkSem <- struct{}{}:
	case <-jm.ctx.Done():
		slog.Debug("[Jobs] song.link skipped (shutdown)", "track", job.TrackName)
		return nil
	}

	defer func() {
		time.Sleep(time.Duration(songLinkDelay) * time.Millisecond)
		<-jm.songLinkSem
	}()

	client := jm.songLinkClient

	if !client.IsRateLimited() {
		urls, err := client.GetAllURLsFromSpotify(job.SpotifyID, job.Settings.Region)
		if err == nil && urls != nil {
			result := make(map[string]string)
			data, _ := json.Marshal(urls)
			json.Unmarshal(data, &result)
			if result["tidal_url"] != "" || result["amazon_url"] != "" || result["isrc"] != "" {
				return result
			}
		}
		if err != nil {
			slog.Debug("[Jobs] song.link failed", "track", job.TrackName, "err", err)
		}
	} else {
		slog.Debug("[Jobs] Songlink rate-limited, trying HTML scraping", "track", job.TrackName)
	}

	// Fallback 1: iTunes Search + song.link /i/{appleMusicID}
	if job.TrackName != "" && job.ArtistName != "" {
		amURLs, amErr := client.ScrapeSongLinkViaAppleMusic(job.TrackName, job.ArtistName, job.AlbumName, job.Settings.Region, job.DurationMs)
		if amErr == nil && amURLs != nil {
			result := make(map[string]string)
			data, _ := json.Marshal(amURLs)
			json.Unmarshal(data, &result)
			if result["tidal_url"] != "" || result["amazon_url"] != "" || result["isrc"] != "" {
				slog.Debug("[Jobs] AppleMusic scraping OK", "track", job.TrackName)
				return result
			}
		} else if amErr != nil {
			slog.Debug("[Jobs] AppleMusic scraping failed", "track", job.TrackName, "err", amErr)
		}
	}

	// Fallback 2: HTML scraping song.link /s/{spotifyID}
	if job.SpotifyID != "" {
		htmlURLs, hErr := client.ScrapeSongLinkHTML(job.SpotifyID)
		if hErr == nil && htmlURLs != nil {
			result := make(map[string]string)
			data, _ := json.Marshal(htmlURLs)
			json.Unmarshal(data, &result)
			if result["tidal_url"] != "" || result["amazon_url"] != "" || result["isrc"] != "" {
				slog.Debug("[Jobs] HTML scraping OK", "track", job.TrackName)
				return result
			}
		} else if hErr != nil {
			slog.Debug("[Jobs] HTML scraping failed", "track", job.TrackName, "err", hErr)
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Output path
// ─────────────────────────────────────────────────────────────────────────────

func (jm *JobManager) buildOutputDir(job *Job) string {
	s := job.Settings
	base := s.DownloadPath
	if base == "" {
		base = util.GetDefaultMusicPath()
	}
	sub := outputSubfolder(s.FolderTemplate, s.CreatePlaylistFolder, s.UseFirstArtistOnly,
		job.ArtistName, job.AlbumName, job.AlbumArtist, job.ReleaseDate, job.PlaylistName)
	if sub != "" {
		base = filepath.Join(base, sub)
	}
	return util.SanitizeFolderPath(base)
}

// outputSubfolder computes the per-track subfolder RELATIVE to the base download
// path, from the folder template and the track's own fields. Extracted from
// buildOutputDir so the /files/exists check derives the exact same directory a
// download would land in — the two must agree, and having one implementation is
// how they stay in sync (docs/settings-source-of-truth.md D1/D2).
func outputSubfolder(folderTemplate string, createPlaylistFolder, useFirstArtistOnly bool, artist, album, albumArtist, releaseDate, playlistName string) string {
	var parts []string

	if createPlaylistFolder && playlistName != "" {
		if !strings.Contains(folderTemplate, "{album}") &&
			!strings.Contains(folderTemplate, "{album_artist}") &&
			!strings.Contains(folderTemplate, "{playlist}") {
			parts = append(parts, util.SanitizeFilename(playlistName))
		}
	}

	if folderTemplate != "" {
		releaseYear := ""
		if len(releaseDate) >= 4 {
			releaseYear = releaseDate[:4]
		}
		if useFirstArtistOnly {
			artist = getFirstArtistStatic(artist)
		}
		if albumArtist == "" {
			albumArtist = artist
		}
		if useFirstArtistOnly {
			albumArtist = getFirstArtistStatic(albumArtist)
		}

		tpl := folderTemplate
		tpl = strings.ReplaceAll(tpl, "{artist}", util.SanitizeFilename(artist))
		tpl = strings.ReplaceAll(tpl, "{album}", util.SanitizeFilename(album))
		tpl = strings.ReplaceAll(tpl, "{album_artist}", util.SanitizeFilename(albumArtist))
		tpl = strings.ReplaceAll(tpl, "{year}", releaseYear)
		tpl = strings.ReplaceAll(tpl, "{playlist}", util.SanitizeFilename(playlistName))

		for _, part := range strings.Split(tpl, "/") {
			part = strings.TrimSpace(part)
			if part != "" {
				parts = append(parts, part)
			}
		}
	}

	return filepath.Join(parts...)
}

// ─────────────────────────────────────────────────────────────────────────────
// Download request assembly
// ─────────────────────────────────────────────────────────────────────────────

func (jm *JobManager) buildDownloadRequest(job *Job, outputDir string, streamingURLs map[string]string) DownloadRequest {
	s := job.Settings

	service := s.Service
	if service == "" {
		service = "tidal"
	}

	audioFormat := resolveAudioFormat(service, s)

	// URL resolution only — this never rewrites `service`. Rewriting it here was
	// the legacy override that made an explicit service choice unreachable (it
	// forced tidal/qobuz whenever it found a Tidal URL or an ISRC). Removed per
	// docs/override-rework-plan.md: each service now carries its own URL, and
	// backend.ExecuteDownload dispatches — an explicit service runs alone, `auto`
	// iterates the AutoOrder chain. The Tidal name-search fallback is not
	// duplicated here; ExecuteDownload's ensureTidalServiceURL owns it.
	tidalURL := ""
	amazonURL := ""
	isrc := ""
	if streamingURLs != nil {
		isrc = streamingURLs["isrc"]
		amazonURL = streamingURLs["amazon_url"]

		// Only resolve Tidal's URL when Tidal can actually run — no point paying
		// for a Tidal ISRC lookup on an explicit qobuz/deezer/amazon download.
		if service == "tidal" || service == "auto" {
			tidalURL = streamingURLs["tidal_url"]
			if tidalURL == "" && isrc != "" {
				if tidalID, _, err := tidal.GetTidalIDFromISRC(job.TrackName, job.ArtistName, isrc); err == nil && tidalID > 0 {
					tidalURL = fmt.Sprintf("https://tidal.com/track/%d", tidalID)
					slog.Debug("[Jobs] Tidal found via ISRC", "track", job.TrackName, "tidal_id", tidalID)
				}
			}
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
		ISRC:                 isrc,
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
		ServiceURL:           tidalURL,
		AmazonURL:            amazonURL,
		AutoOrder:            s.AutoOrder,
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

// resolveAudioFormat picks the right format string for a given service.
func resolveAudioFormat(service string, s JobSettings) string {
	switch service {
	case "tidal":
		return firstNonEmpty(s.TidalQuality, "LOSSLESS")
	case "qobuz":
		return firstNonEmpty(s.QobuzQuality, "6")
	case "deezer":
		return "flac"
	case "auto":
		if s.AutoQuality == "24" {
			return "HI_RES_LOSSLESS"
		}
		return "LOSSLESS"
	default:
		return "LOSSLESS"
	}
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// File existence check
// ─────────────────────────────────────────────────────────────────────────────

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

	expectedFilename := util.BuildExpectedFilename(
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

// ─────────────────────────────────────────────────────────────────────────────
// Batch completion tracking
// ─────────────────────────────────────────────────────────────────────────────

// maybeGenerateM3U8 checks whether the batch is complete and notifies Watcher
// via OnBatchComplete.
// Fast path: in-memory counter O(1).
// Recovery path (after restart, counter unknown): BoltDB scan O(n).
func (jm *JobManager) maybeGenerateM3U8(watchlistID, batchID string) {
	if batchID == "" || watchlistID == "" {
		return
	}

	jm.mu.Lock()
	total, hasCounter := jm.batchTotals[batchID]
	if hasCounter {
		jm.batchDone[batchID]++
		done := jm.batchDone[batchID]
		if done < total {
			jm.mu.Unlock()
			return
		}
		delete(jm.batchTotals, batchID)
		delete(jm.batchDone, batchID)
		delete(jm.batchWatchID, batchID)
		jm.mu.Unlock()
	} else {
		jm.mu.Unlock()
		jobs, err := jm.GetAllJobs()
		if err != nil {
			return
		}
		for _, j := range jobs {
			if j.WatchlistID != watchlistID || j.BatchID != batchID {
				continue
			}
			if j.Status == StatusPending || j.Status == StatusDownloading {
				return
			}
		}
	}

	jobs, err := jm.GetAllJobs()
	if err != nil {
		return
	}
	latest := make(map[string]Job)
	for _, j := range jobs {
		if j.WatchlistID != watchlistID || j.BatchID != batchID {
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

	var downloaded, skipped, failed int
	for _, j := range latest {
		switch j.Status {
		case StatusDone:
			downloaded++
		case StatusSkipped:
			skipped++
		case StatusFailed:
			failed++
		}
	}

	if jm.eventHandler != nil {
		jm.eventHandler.OnBatchComplete(watchlistID, batchID, downloaded, skipped, failed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Static helpers
// ─────────────────────────────────────────────────────────────────────────────

// getFirstArtistStatic returns the first artist name from a comma/ampersand/feat
// delimited string, without accessing any JobManager state.
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

// RequeueFailedJobs re-enqueues all StatusFailed jobs for a watchlist,
// applying currentSettings so that stale job configs are corrected.
func (jm *JobManager) RequeueFailedJobs(watchlistID string, currentSettings JobSettings) (int, error) {
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
		job.Settings = currentSettings
		job.Error = ""
		job.Progress = 0
		job.UpdatedAt = time.Now()
		if err := jm.saveJob(&job); err != nil {
			slog.Error("[Jobs] RequeueFailed: failed to save job", "job_id", job.ID, "err", err)
			continue
		}
		jm.notifyJob(&job)

		select {
		case jm.queue <- job.ID:
			requeued++
		default:
			slog.Warn("[Jobs] Queue full, failed job will be picked up later", "job_id", job.ID)
			requeued++
		}
	}
	if requeued > 0 {
		slog.Info("[Jobs] Requeued failed jobs", "count", requeued, "watchlist_id", watchlistID)
	}
	return requeued, nil
}
