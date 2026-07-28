package main

// ─────────────────────────────────────────────────────────────────────────────
// Business helpers for JobManager:
//   streaming URL resolution, output path building, filename checking,
//   batch completion tracking, failed-job requeue
// ─────────────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
	"github.com/afkarxyz/SpotiFLAC/backend/isrclookup"
	"github.com/afkarxyz/SpotiFLAC/backend/tidal"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// -----------------------------------------------------------------------------
// ISRC resolution
// -----------------------------------------------------------------------------

// resolveTrackISRC resolves the track's ISRC, the one piece of cross-provider
// identity the download path still needs.
//
// This used to be getStreamingURLs and returned a map of tidal_url/amazon_url/isrc.
// Two of those three keys were already impossible to populate: amazon_url died
// with the native Amazon downloader, and tidal_url only ever came from Song.link
// - ResolveByName, despite its then-SongLinkURLs return type, never set it.
// Removing Song.link (item 7) left the map with exactly one live key, so it is a
// string now, and the dead branches reading the other two are gone with it.
func (jm *JobManager) resolveTrackISRC(job *Job) string {
	// Cheapest and most authoritative: Spotify's own catalog record for this
	// exact track, cached.
	if isrc := jm.resolveISRCDirect(job); isrc != "" {
		return isrc
	}

	// Deezer's public API as a name-search fallback - one call, no rate-limit,
	// but a name match, so it can land on the wrong edition/remaster. Gated on
	// Tidal being a live candidate: it is the only consumer that needs an ISRC
	// before the download starts (GetTidalIDFromISRC below). A delegated provider
	// is handed the Spotify URL and resolves internally.
	if !tidalMayRunNatively(job.Settings) {
		slog.Debug("[Jobs] Skipping the ISRC fallback - no native Tidal candidate",
			"track", job.TrackName, "service", job.Settings.Service)
		return ""
	}
	if job.TrackName == "" || job.ArtistName == "" {
		return ""
	}

	isrc, err := isrclookup.ResolveByName(job.TrackName, job.ArtistName)
	if err != nil {
		slog.Debug("[Jobs] Deezer ISRC fallback failed", "track", job.TrackName, "err", err)
		return ""
	}
	slog.Debug("[Jobs] Deezer ISRC fallback OK", "track", job.TrackName, "isrc", isrc)
	return isrc
}

// tidalMayRunNatively reports whether Tidal - the only provider left with a
// native path - could run for this job. Qobuz, Amazon and Deezer are engine-only,
// and the engine needs no ISRC from us.
//
// When the chain cannot be known here (auto with no configured order, which
// ExecuteDownload fills in later) the answer is yes: guessing wrong would
// silently strip an ISRC a native Tidal download depends on, and the cost of
// being wrong in that direction is only the round-trip we pay today.
func tidalMayRunNatively(s JobSettings) bool {
	isNativeTidal := func(svc string) bool {
		return strings.EqualFold(strings.TrimSpace(svc), "tidal") && !backend.EngineHandles("tidal")
	}

	svc := strings.TrimSpace(strings.ToLower(s.Service))
	if svc != "" && svc != "auto" {
		return isNativeTidal(svc)
	}

	if strings.TrimSpace(s.AutoOrder) == "" {
		return true
	}
	for _, candidate := range strings.Split(s.AutoOrder, "-") {
		if isNativeTidal(candidate) {
			return true
		}
	}
	return false
}

// resolveISRCDirect resolves job.SpotifyID's ISRC straight from Spotify's
// metadata, independent of the Deezer name search. Cached (see
// isrclookup.Resolve), so repeat downloads/retags of the same track
// don't pay for a fresh lookup.
func (jm *JobManager) resolveISRCDirect(job *Job) string {
	if job.SpotifyID == "" {
		return ""
	}

	isrc, err := jm.isrcClient.Resolve(job.SpotifyID)
	if err != nil {
		slog.Debug("[Jobs] ISRC-direct failed", "track", job.TrackName, "err", err)
		return ""
	}
	return isrc
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

func (jm *JobManager) buildDownloadRequest(job *Job, outputDir string, isrc string) DownloadRequest {
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
	// Only resolve Tidal's URL when Tidal can actually run — no point paying for
	// a Tidal ISRC lookup on an explicit qobuz/deezer/amazon download.
	tidalURL := ""
	if isrc != "" && (service == "tidal" || service == "auto") {
		if tidalID, _, err := tidal.GetTidalIDFromISRC(job.TrackName, job.ArtistName, isrc); err == nil && tidalID > 0 {
			tidalURL = fmt.Sprintf("https://tidal.com/track/%d", tidalID)
			slog.Debug("[Jobs] Tidal found via ISRC", "track", job.TrackName, "tidal_id", tidalID)
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
	trackNumberToPrint := util.ResolveTrackNumber(job.Position, job.TrackNumber, useAlbumTrackNumber)

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
		trackNumberToPrint,
		job.DiscNumber,
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

// maybeGenerateM3U8 checks whether the batch is complete, then hands it to the
// event handler: OnBatchComplete for a watchlist, OnManualBatchComplete for a
// manual one that asked for an M3U8.
//
// It used to bail whenever watchlistID was empty, so a manual batch never had a
// completion moment at all. That is why a freshly-downloaded playlist got no
// M3U8: the client wrote one right after ENQUEUE, from the existence check
// alone, so it only ever listed tracks that were already on disk.
//
// Fast path: in-memory counter O(1).
// Recovery path (after restart, counter unknown): BoltDB scan O(n).
func (jm *JobManager) maybeGenerateM3U8(watchlistID, batchID string) {
	if batchID == "" {
		return
	}

	jm.mu.Lock()
	m3u8Req, wantsM3U8 := jm.batchM3U8[batchID]
	// A manual batch that never asked for an M3U8 has nothing to do here, and
	// the work below is two full BoltDB scans. Leaving without them matters
	// after a restart, when the counters are gone and every completing job
	// would otherwise pay for both.
	if watchlistID == "" && !wantsM3U8 {
		jm.mu.Unlock()
		return
	}
	total, hasCounter := jm.batchTotals[batchID]
	if hasCounter {
		jm.batchDone[batchID]++
		done := jm.batchDone[batchID]
		if done < total {
			jm.mu.Unlock()
			return
		}
		jm.forgetBatch(batchID)
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

	if jm.eventHandler == nil {
		return
	}
	if watchlistID != "" {
		jm.eventHandler.OnBatchComplete(watchlistID, batchID, downloaded, skipped, failed)
		return
	}
	if !wantsM3U8 {
		return
	}
	// Only tracks that actually landed on disk go in, in playlist order. A
	// failed track has no file to point at, and a skipped one was already
	// there — both are legitimate entries only if they resolved to a path.
	//
	// The batch's own jobs are merged with the tracks the catalog dedup took out
	// at enqueue time: those never become jobs, so listing jobs alone would make
	// the file "the last batch" instead of "the playlist".
	entries := make([]M3U8Entry, 0, len(latest)+len(m3u8Req.Preexisting))
	for _, j := range latest {
		if (j.Status == StatusDone || j.Status == StatusSkipped) && j.FilePath != "" {
			entries = append(entries, M3U8Entry{Position: j.Position, Path: j.FilePath})
		}
	}
	entries = append(entries, m3u8Req.Preexisting...)
	sort.Slice(entries, func(a, b int) bool { return entries[a].Position < entries[b].Position })
	paths := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		paths = append(paths, e.Path)
	}
	jm.eventHandler.OnManualBatchComplete(m3u8Req, paths)
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
