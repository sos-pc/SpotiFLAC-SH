package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/amazon"
	"github.com/afkarxyz/SpotiFLAC/backend/audio"
	"github.com/afkarxyz/SpotiFLAC/backend/deezer"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/qobuz"
	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
	"github.com/afkarxyz/SpotiFLAC/backend/tidal"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// defaultAutoOrder is the fallback chain used when "auto" is requested with no
// configured order. It mirrors DEFAULT_AUTO_ORDER in frontend/src/lib/settings.ts
// — keep the two in step.
//
// It used to read "tidal-amazon-qobuz", which matched neither the frontend
// default ("tidal-qobuz-amazon-deezer") nor the UI placeholder
// ("tidal-qobuz-amazon"). Since this is the value that actually *runs*, an
// unconfigured user got a chain that was displayed nowhere: Deezer never tried,
// Amazon and Qobuz in the opposite order to what the settings screen showed.
const defaultAutoOrder = "tidal-qobuz-amazon-deezer"

type DownloadRequest struct {
	Service              string `json:"service"`
	Query                string `json:"query,omitempty"`
	TrackName            string `json:"track_name,omitempty"`
	ArtistName           string `json:"artist_name,omitempty"`
	AlbumName            string `json:"album_name,omitempty"`
	AlbumArtist          string `json:"album_artist,omitempty"`
	ReleaseDate          string `json:"release_date,omitempty"`
	CoverURL             string `json:"cover_url,omitempty"`
	ApiURL               string `json:"api_url,omitempty"`
	OutputDir            string `json:"output_dir,omitempty"`
	AudioFormat          string `json:"audio_format,omitempty"`
	FilenameFormat       string `json:"filename_format,omitempty"`
	TrackNumber          bool   `json:"track_number,omitempty"`
	Position             int    `json:"position,omitempty"`
	UseAlbumTrackNumber  bool   `json:"use_album_track_number,omitempty"`
	SpotifyID            string `json:"spotify_id,omitempty"`
	EmbedLyrics          bool   `json:"embed_lyrics,omitempty"`
	EmbedMaxQualityCover bool   `json:"embed_max_quality_cover,omitempty"`
	ServiceURL           string `json:"service_url,omitempty"`
	// AmazonURL carries Amazon's own stream URL so the `auto` fallback chain can
	// hand Amazon the right URL instead of ServiceURL, which holds Tidal's (that
	// mismatch silently broke auto→amazon). The frontend's direct Amazon
	// download still sends its URL as service_url, so amazonParams falls back to
	// ServiceURL when this is empty.
	AmazonURL          string `json:"amazon_url,omitempty"`
	ISRC               string `json:"isrc,omitempty"`
	AutoOrder          string `json:"auto_order,omitempty"`
	Duration           int    `json:"duration,omitempty"`
	ItemID             string `json:"item_id,omitempty"`
	SpotifyTrackNumber int    `json:"spotify_track_number,omitempty"`
	SpotifyDiscNumber  int    `json:"spotify_disc_number,omitempty"`
	SpotifyTotalTracks int    `json:"spotify_total_tracks,omitempty"`
	SpotifyTotalDiscs  int    `json:"spotify_total_discs,omitempty"`
	Copyright          string `json:"copyright,omitempty"`
	Publisher          string `json:"publisher,omitempty"`
	PlaylistName       string `json:"playlist_name,omitempty"`
	PlaylistOwner      string `json:"playlist_owner,omitempty"`
	AllowFallback      bool   `json:"allow_fallback"`
	UseFirstArtistOnly bool   `json:"use_first_artist_only,omitempty"`
	UseSingleGenre     bool   `json:"use_single_genre,omitempty"`
	EmbedGenre         bool   `json:"embed_genre,omitempty"`
	UserID             string `json:"user_id,omitempty"`
	// SpeedCallback est appelé pendant le téléchargement avec (mbDownloaded, speedMBps).
	// Nil si non requis. Non sérialisé.
	SpeedCallback func(mbDownloaded, speedMBps float64) `json:"-"`
}

type DownloadResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	File          string `json:"file,omitempty"`
	Error         string `json:"error,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
	ItemID        string `json:"item_id,omitempty"`
}

func ExecuteDownload(req DownloadRequest) (DownloadResponse, error) {
	if req.Service == "qobuz" && req.SpotifyID == "" {
		return DownloadResponse{Success: false, Error: "Spotify ID is required for Qobuz"},
			fmt.Errorf("spotify ID is required for Qobuz")
	}
	if req.Service == "" {
		req.Service = "tidal"
	}
	if req.OutputDir == "" {
		req.OutputDir = "."
	} else {
		req.OutputDir = util.SanitizeFolderPath(req.OutputDir)
	}
	if req.AudioFormat == "" {
		req.AudioFormat = "LOSSLESS"
	}
	if req.FilenameFormat == "" {
		req.FilenameFormat = "title-artist"
	}

	var err error
	var filename string

	itemID := req.ItemID
	if itemID == "" {
		if req.SpotifyID != "" {
			itemID = fmt.Sprintf("%s-%d", req.SpotifyID, time.Now().UnixNano())
		} else {
			itemID = fmt.Sprintf("%s-%s-%d", req.TrackName, req.ArtistName, time.Now().UnixNano())
		}
	}

	spotifyURL := ""
	if req.SpotifyID != "" {
		spotifyURL = fmt.Sprintf("https://open.spotify.com/track/%s", req.SpotifyID)
	}

	// FIX double cancel — contextes nommés distinctement
	if req.SpotifyID != "" && (req.Copyright == "" || req.Publisher == "" || req.SpotifyTotalDiscs == 0 || req.ReleaseDate == "" || req.SpotifyTotalTracks == 0 || req.SpotifyTrackNumber == 0) {
		detailCtx, detailCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer detailCancel()

		trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", req.SpotifyID)
		trackData, err := spotify.GetFilteredSpotifyData(detailCtx, trackURL, false, 0)
		if err == nil {
			var trackResp struct {
				Track struct {
					Copyright   string `json:"copyright"`
					Publisher   string `json:"publisher"`
					TotalDiscs  int    `json:"total_discs"`
					TotalTracks int    `json:"total_tracks"`
					TrackNumber int    `json:"track_number"`
					ReleaseDate string `json:"release_date"`
				} `json:"track"`
			}
			if jsonData, jsonErr := json.Marshal(trackData); jsonErr == nil {
				if json.Unmarshal(jsonData, &trackResp) == nil {
					if req.Copyright == "" && trackResp.Track.Copyright != "" {
						req.Copyright = trackResp.Track.Copyright
					}
					if req.Publisher == "" && trackResp.Track.Publisher != "" {
						req.Publisher = trackResp.Track.Publisher
					}
					if req.SpotifyTotalDiscs == 0 && trackResp.Track.TotalDiscs > 0 {
						req.SpotifyTotalDiscs = trackResp.Track.TotalDiscs
					}
					if req.SpotifyTotalTracks == 0 && trackResp.Track.TotalTracks > 0 {
						req.SpotifyTotalTracks = trackResp.Track.TotalTracks
					}
					if req.SpotifyTrackNumber == 0 && trackResp.Track.TrackNumber > 0 {
						req.SpotifyTrackNumber = trackResp.Track.TrackNumber
					}
					if req.ReleaseDate == "" && trackResp.Track.ReleaseDate != "" {
						req.ReleaseDate = trackResp.Track.ReleaseDate
					}
				}
			}
		}
	}

	if req.TrackName != "" && req.ArtistName != "" {
		// Resolve the number first. This call used to hand the raw Position to a
		// parameter the builder ignored, so it looked for a filename the download
		// never wrote — the divergence that let this check report "already exists"
		// for a file the worker's own check had missed.
		trackNumberToPrint := util.ResolveTrackNumber(req.Position, req.SpotifyTrackNumber, req.UseAlbumTrackNumber)
		expectedFilename := util.BuildExpectedFilename(req.TrackName, req.ArtistName, req.AlbumName, req.AlbumArtist, req.ReleaseDate, req.FilenameFormat, req.PlaylistName, req.PlaylistOwner, req.TrackNumber, trackNumberToPrint, req.SpotifyDiscNumber)
		expectedPath := filepath.Join(req.OutputDir, expectedFilename)
		if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 100*1024 {
			return DownloadResponse{
				Success:       true,
				Message:       "File already exists",
				File:          expectedPath,
				AlreadyExists: true,
				ItemID:        itemID,
			}, nil
		}
	}

	lyricsChan := make(chan string, 1)
	isrcChan := make(chan string, 1)

	if req.SpotifyID != "" {
		if req.EmbedLyrics {
			// Whoever reads lyricsChan blocks until something arrives, so
			// a recovered panic must still send the same "not found"
			// fallback (empty string) the normal failure path already
			// sends — otherwise that read hangs forever instead of just
			// missing the lyrics for this one track.
			util.SafeGoOrElse("downloader.fetchLyrics", func() {
				client := meta.NewLyricsClient()
				resp, _, err := client.FetchLyricsAllSources(req.SpotifyID, req.TrackName, req.ArtistName, req.AlbumName, req.Duration)
				if err == nil && resp != nil && len(resp.Lines) > 0 {
					lrc := client.ConvertToLRC(resp, req.TrackName, req.ArtistName)
					lyricsChan <- lrc
				} else {
					lyricsChan <- ""
				}
			}, func() { lyricsChan <- "" })
		} else {
			close(lyricsChan)
		}
		// Same reasoning as fetchLyrics above: isrcChan's reader blocks
		// until something arrives, so a recovered panic must still send a
		// fallback ("" — the same value the normal all-methods-failed
		// path already sends).
		util.SafeGoOrElse("downloader.resolveISRC", func() {
			if req.ISRC != "" {
				slog.Debug("[ISRC] Using pre-fetched ISRC", "isrc", req.ISRC)
				isrcChan <- req.ISRC
				return
			}

			var finalIsrc string
			// 1. Tenter Deezer Fallback (rapide, sans compte, pas de rate-limit Songlink)
			if req.TrackName != "" && req.ArtistName != "" {
				if fallback, ferr := songlink.GetDeezerSearchFallback(req.TrackName, req.ArtistName); ferr == nil && fallback != nil && fallback.ISRC != "" {
					finalIsrc = fallback.ISRC
					slog.Debug("[ISRC] Found via Deezer API", "isrc", finalIsrc)
				}
			}

			// 2. Si Deezer a échoué, tenter Songlink (JSON ou HTML)
			if finalIsrc == "" {
				sl := songlink.GetSongLinkClient()
				if sl != nil {
					// L'appel GetAllURLs inclut le fallback HTML depuis la v1.3.6
					urls, err := sl.GetAllURLsFromSpotify(req.SpotifyID, "")
					if err == nil && urls != nil && urls.ISRC != "" {
						finalIsrc = urls.ISRC
						slog.Debug("[ISRC] Found via Songlink JSON", "isrc", finalIsrc)
					} else {
						// Fallback ultime : Scraping HTML (si l'appel global ne l'a pas déjà fait)
						if htmlURLs, hErr := sl.ScrapeSongLinkHTML(req.SpotifyID); hErr == nil && htmlURLs != nil && htmlURLs.ISRC != "" {
							finalIsrc = htmlURLs.ISRC
							slog.Debug("[ISRC] Found via Songlink HTML", "isrc", finalIsrc)
						}
					}
				}
			}

			if finalIsrc == "" {
				slog.Debug("[ISRC] All lookup methods failed", "track", req.TrackName)
			}

			isrcChan <- finalIsrc
		}, func() { isrcChan <- "" })
	} else {
		close(lyricsChan)
		close(isrcChan)
	}

	tidalFmt := TidalQualityFor(req.AudioFormat)
	qobuzFmt := QobuzQualityFor(req.AudioFormat)

	// Build the tidal.DownloadParams once; the same struct fits all three Tidal entry points
	// (Download, DownloadByURL, DownloadByURLWithFallback). The call site picks one method,
	// which then reads either p.URL or p.SpotifyTrackID.
	tidalParams := func(quality string) tidal.DownloadParams {
		return tidal.DownloadParams{
			URL:                  req.ServiceURL,
			SpotifyTrackID:       req.SpotifyID,
			OutputDir:            req.OutputDir,
			Quality:              quality,
			FilenameFormat:       req.FilenameFormat,
			Position:             req.Position,
			IncludeTrackNumber:   req.TrackNumber,
			UseAlbumTrackNumber:  req.UseAlbumTrackNumber,
			UseFirstArtistOnly:   req.UseFirstArtistOnly,
			SpotifyTrackName:     req.TrackName,
			SpotifyArtistName:    req.ArtistName,
			SpotifyAlbumName:     req.AlbumName,
			SpotifyAlbumArtist:   req.AlbumArtist,
			SpotifyReleaseDate:   req.ReleaseDate,
			SpotifyCoverURL:      req.CoverURL,
			SpotifyTrackNumber:   req.SpotifyTrackNumber,
			SpotifyDiscNumber:    req.SpotifyDiscNumber,
			SpotifyTotalTracks:   req.SpotifyTotalTracks,
			SpotifyTotalDiscs:    req.SpotifyTotalDiscs,
			SpotifyCopyright:     req.Copyright,
			SpotifyPublisher:     req.Publisher,
			SpotifyURL:           spotifyURL,
			AllowFallback:        req.AllowFallback,
			EmbedMaxQualityCover: req.EmbedMaxQualityCover,
			UseSingleGenre:       req.UseSingleGenre,
			EmbedGenre:           req.EmbedGenre,
		}
	}

	// Same idea for Qobuz. The ISRC is supplied separately, after being awaited
	// from isrcChan (we don't always know it at this point).
	qobuzParams := func(isrc string) qobuz.DownloadParams {
		return qobuz.DownloadParams{
			DeezerISRC:           isrc,
			SpotifyID:            req.SpotifyID,
			OutputDir:            req.OutputDir,
			Quality:              qobuzFmt,
			FilenameFormat:       req.FilenameFormat,
			Position:             req.Position,
			IncludeTrackNumber:   req.TrackNumber,
			UseAlbumTrackNumber:  req.UseAlbumTrackNumber,
			UseFirstArtistOnly:   req.UseFirstArtistOnly,
			SpotifyTrackName:     req.TrackName,
			SpotifyArtistName:    req.ArtistName,
			SpotifyAlbumName:     req.AlbumName,
			SpotifyAlbumArtist:   req.AlbumArtist,
			SpotifyReleaseDate:   req.ReleaseDate,
			SpotifyCoverURL:      req.CoverURL,
			SpotifyTrackNumber:   req.SpotifyTrackNumber,
			SpotifyDiscNumber:    req.SpotifyDiscNumber,
			SpotifyTotalTracks:   req.SpotifyTotalTracks,
			SpotifyTotalDiscs:    req.SpotifyTotalDiscs,
			SpotifyCopyright:     req.Copyright,
			SpotifyPublisher:     req.Publisher,
			SpotifyURL:           spotifyURL,
			AllowFallback:        req.AllowFallback,
			EmbedMaxQualityCover: req.EmbedMaxQualityCover,
			UseSingleGenre:       req.UseSingleGenre,
			EmbedGenre:           req.EmbedGenre,
		}
	}

	// Amazon parameters: includes PlaylistName/PlaylistOwner (used for filename
	// generation) but no AllowFallback (Amazon has no quality fallback yet).
	amazonParams := func() amazon.DownloadParams {
		// Amazon's own URL only. NOT ServiceURL: in the auto loop that holds
		// Tidal's URL (ensureTidalServiceURL), and feeding it to Amazon made it
		// try to parse an ASIN out of a tidal.com link ("failed to extract ASIN
		// from URL: https://tidal.com/track/…"). When there's no Amazon URL,
		// runService falls back to DownloadBySpotifyID instead.
		return amazon.DownloadParams{
			URL:                  req.AmazonURL,
			SpotifyTrackID:       req.SpotifyID,
			OutputDir:            req.OutputDir,
			Quality:              req.AudioFormat,
			FilenameFormat:       req.FilenameFormat,
			Position:             req.Position,
			IncludeTrackNumber:   req.TrackNumber,
			UseFirstArtistOnly:   req.UseFirstArtistOnly,
			PlaylistName:         req.PlaylistName,
			PlaylistOwner:        req.PlaylistOwner,
			SpotifyTrackName:     req.TrackName,
			SpotifyArtistName:    req.ArtistName,
			SpotifyAlbumName:     req.AlbumName,
			SpotifyAlbumArtist:   req.AlbumArtist,
			SpotifyReleaseDate:   req.ReleaseDate,
			SpotifyCoverURL:      req.CoverURL,
			SpotifyTrackNumber:   req.SpotifyTrackNumber,
			SpotifyDiscNumber:    req.SpotifyDiscNumber,
			SpotifyTotalTracks:   req.SpotifyTotalTracks,
			SpotifyTotalDiscs:    req.SpotifyTotalDiscs,
			SpotifyCopyright:     req.Copyright,
			SpotifyPublisher:     req.Publisher,
			SpotifyURL:           spotifyURL,
			EmbedMaxQualityCover: req.EmbedMaxQualityCover,
			UseSingleGenre:       req.UseSingleGenre,
			EmbedGenre:           req.EmbedGenre,
		}
	}

	// Deezer parameters: no Quality (Deezer always uses FLAC via Deezmate),
	// no AllowFallback, no genre flags (not consumed by Deezer client today).
	deezerParams := func() deezer.DownloadParams {
		return deezer.DownloadParams{
			SpotifyID:            req.SpotifyID,
			OutputDir:            req.OutputDir,
			FilenameFormat:       req.FilenameFormat,
			Position:             req.Position,
			IncludeTrackNumber:   req.TrackNumber,
			UseFirstArtistOnly:   req.UseFirstArtistOnly,
			PlaylistName:         req.PlaylistName,
			PlaylistOwner:        req.PlaylistOwner,
			SpotifyTrackName:     req.TrackName,
			SpotifyArtistName:    req.ArtistName,
			SpotifyAlbumName:     req.AlbumName,
			SpotifyAlbumArtist:   req.AlbumArtist,
			SpotifyReleaseDate:   req.ReleaseDate,
			SpotifyCoverURL:      req.CoverURL,
			SpotifyTrackNumber:   req.SpotifyTrackNumber,
			SpotifyDiscNumber:    req.SpotifyDiscNumber,
			SpotifyTotalTracks:   req.SpotifyTotalTracks,
			SpotifyTotalDiscs:    req.SpotifyTotalDiscs,
			SpotifyCopyright:     req.Copyright,
			SpotifyPublisher:     req.Publisher,
			SpotifyURL:           spotifyURL,
			EmbedMaxQualityCover: req.EmbedMaxQualityCover,
		}
	}

	// ensureTidalServiceURL resolves a Tidal URL by track/artist name when only
	// the name is known, populating req.ServiceURL. No-op if it's already set.
	ensureTidalServiceURL := func(logMsg string) {
		if req.ServiceURL == "" && req.TrackName != "" && req.ArtistName != "" {
			dl := tidal.NewTidalDownloader("")
			if tidalURL, serr := dl.SearchTidalByName(req.TrackName, req.ArtistName); serr == nil && tidalURL != "" {
				req.ServiceURL = tidalURL
				slog.Debug(logMsg, "url", tidalURL)
			}
		}
	}

	// runService builds the client for one provider and runs the download.
	// Previously this per-provider logic was written twice — once in the direct
	// switch below and once inside the "auto" fallback loop — which diverged
	// only in how Tidal's API URL was chosen. tidalApiURL captures that one
	// difference ("" / "auto" → public HiFi endpoints with fallback, otherwise
	// the specific proxy); it's ignored for the other providers. Returns the
	// downloaded filename and error from the underlying client.
	runService := func(svc, tidalApiURL string) (string, error) {
		switch svc {
		case "amazon":
			dl := amazon.NewAmazonDownloader()
			dl.SpeedCallback = req.SpeedCallback
			// Gate on Amazon's own URL, not ServiceURL (which is Tidal's in the
			// auto loop) — otherwise a Tidal URL gets sent to DownloadByURL.
			if req.AmazonURL != "" {
				return dl.DownloadByURL(amazonParams())
			}
			return dl.DownloadBySpotifyID(amazonParams())
		case "tidal":
			if tidalApiURL == "" || tidalApiURL == "auto" {
				dl := tidal.NewTidalDownloader("")
				dl.SpeedCallback = req.SpeedCallback
				p := tidalParams(tidalFmt)
				if req.ServiceURL != "" {
					return dl.DownloadByURLWithFallback(p)
				}
				return dl.Download(p)
			}
			dl := tidal.NewTidalDownloader(tidalApiURL)
			dl.SpeedCallback = req.SpeedCallback
			p := tidalParams(tidalFmt)
			if req.ServiceURL != "" {
				return dl.DownloadByURL(p)
			}
			return dl.Download(p)
		case "qobuz":
			isrc := <-isrcChan
			dl := qobuz.NewQobuzDownloader()
			dl.SpeedCallback = req.SpeedCallback
			return dl.DownloadTrackWithISRC(qobuzParams(isrc))
		case "deezer":
			dl := deezer.NewDeezerDownloader()
			dl.SpeedCallback = req.SpeedCallback
			return dl.Download(deezerParams())
		default:
			return "", fmt.Errorf("unknown service: %s", svc)
		}
	}

	// One always-on line per download so a prod log makes the decision path
	// visible without a debug build — which service was asked for, at what
	// quality. The per-service walk below completes the picture for "auto".
	slog.Info("[Download] Resolving",
		"track", req.TrackName, "requested_service", req.Service, "audio_format", req.AudioFormat)

	switch req.Service {
	case "amazon", "qobuz", "deezer":
		filename, err = runService(req.Service, "")

	case "tidal":
		ensureTidalServiceURL("[DownloadTrack] Found Tidal URL via fallback search")
		filename, err = runService("tidal", req.ApiURL)

	case "auto":
		// Respecter l'ordre configuré par l'user (AutoOrder)
		orderStr := req.AutoOrder
		if orderStr == "" {
			orderStr = defaultAutoOrder
		}
		order := strings.Split(orderStr, "-")
		slog.Info("[Auto] Walking chain", "track", req.TrackName, "order", orderStr)

		ensureTidalServiceURL("[DownloadTrack/Auto] Found Tidal URL via fallback search")

		var lastErr error
		for _, svc := range order {
			switch svc {
			case "tidal", "amazon", "qobuz", "deezer":
			default:
				continue
			}
			slog.Info("[Auto] Trying", "service", svc, "track", req.TrackName)
			filename, err = runService(svc, "")
			if err == nil {
				slog.Info("[Auto] Success", "service", svc, "track", req.TrackName)
				break
			}
			lastErr = err
			// Info, not Debug: knowing *why* each provider was skipped is the
			// whole point of being able to read an auto download in prod.
			slog.Info("[Auto] Service failed, trying next", "service", svc, "err", err, "track", req.TrackName)
		}
		if err != nil {
			err = lastErr
		}

	default:
		return DownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("Unknown service: %s", req.Service),
		}, fmt.Errorf("unknown service: %s", req.Service)
	}

	if err != nil {
		if filename != "" && !strings.HasPrefix(filename, "EXISTS:") {
			if _, statErr := os.Stat(filename); statErr == nil {
				slog.Warn("[Downloader] Removing corrupted/partial file after failed download", "file", filename)
				if removeErr := os.Remove(filename); removeErr != nil {
					slog.Warn("[Downloader] Failed to remove corrupted file", "file", filename, "err", removeErr)
				}
			}
		}
		return DownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("Download failed: %v", err),
			ItemID:  itemID,
		}, err
	}

	alreadyExists := false
	if strings.HasPrefix(filename, "EXISTS:") {
		alreadyExists = true
		filename = strings.TrimPrefix(filename, "EXISTS:")
	}

	if !alreadyExists && req.SpotifyID != "" && req.EmbedLyrics && (strings.HasSuffix(filename, ".flac") || strings.HasSuffix(filename, ".mp3") || strings.HasSuffix(filename, ".m4a")) {
		slog.Debug("[Downloader] Waiting for lyrics fetch to complete")
		lyrics := <-lyricsChan
		if lyrics != "" {
			slog.Debug("[Downloader] Embedding lyrics", "file", filename, "content", lyrics)
			if err := meta.EmbedLyricsOnlyUniversal(filename, lyrics); err != nil {
				slog.Warn("[Downloader] Failed to embed lyrics", "err", err)
			} else {
				slog.Debug("[Downloader] Lyrics embedded successfully")
			}
		} else {
			slog.Debug("[Downloader] No lyrics found to embed")
		}
	} else {
		select {
		case <-lyricsChan:
		default:
		}
	}

	message := "Download completed successfully"
	if alreadyExists {
		message = "File already exists"
	} else {
		if _, statErr := os.Stat(filename); statErr != nil {
			slog.Warn("[Downloader] Could not stat completed file", "file", filename, "err", statErr)
		}

		// FIX #4 — capture req.UserID pour le taguer dans l'item d'historique
		fPath, track, artist, album, sID, cover, format, userID := filename, req.TrackName, req.ArtistName, req.AlbumName, req.SpotifyID, req.CoverURL, req.AudioFormat, req.UserID
		util.SafeGo("downloader.recordHistory", func() {
			quality := "Unknown"
			durationStr := "--:--"

			meta, err := audio.GetTrackMetadata(fPath)
			if err == nil && meta != nil {
				if meta.BitsPerSample > 0 {
					quality = fmt.Sprintf("%d-bit/%.1fkHz", meta.BitsPerSample, float64(meta.SampleRate)/1000.0)
				} else if meta.Bitrate > 0 {
					quality = fmt.Sprintf("%dkbps/%.1fkHz", meta.Bitrate/1000, float64(meta.SampleRate)/1000.0)
				} else if meta.SampleRate > 0 {
					quality = fmt.Sprintf("%.1fkHz", float64(meta.SampleRate)/1000.0)
				}
				d := int(meta.Duration)
				durationStr = fmt.Sprintf("%d:%02d", d/60, d%60)
			} else {
				slog.Warn("[History] Failed to get metadata for file", "file", fPath, "err", err)
			}

			item := HistoryItem{
				SpotifyID:   sID,
				Title:       track,
				Artists:     artist,
				Album:       album,
				DurationStr: durationStr,
				CoverURL:    cover,
				Quality:     quality,
				Format:      strings.ToUpper(format),
				Path:        fPath,
				UserID:      userID,
			}

			if item.Format == "" || item.Format == "LOSSLESS" {
				ext := filepath.Ext(fPath)
				if len(ext) > 1 {
					item.Format = strings.ToUpper(ext[1:])
				}
			}
			switch item.Format {
			case "6", "7", "27":
				item.Format = "FLAC"
			}

			if err := AddHistoryItem(item); err != nil {
				slog.Warn("[History] Failed to record history for file", "file", fPath, "err", err)
			}
		})
	}

	return DownloadResponse{
		Success:       true,
		Message:       message,
		File:          filename,
		AlreadyExists: alreadyExists,
		ItemID:        itemID,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Quality mapping helpers
// ─────────────────────────────────────────────────────────────────────────────

// TidalQualityFor converts any quality string to the nearest valid Tidal quality.
func TidalQualityFor(format string) string {
	switch format {
	case "27", "7":
		return "HI_RES_LOSSLESS"
	case "6", "flac":
		return "LOSSLESS"
	case "LOSSLESS", "HI_RES_LOSSLESS", "HI_RES":
		return format
	default:
		return "LOSSLESS"
	}
}

// QobuzQualityFor converts any quality string to the nearest valid Qobuz quality.
func QobuzQualityFor(format string) string {
	switch format {
	case "HI_RES_LOSSLESS", "HI_RES", "27":
		return "27"
	case "7":
		return "7"
	default:
		return "6"
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Misc
// ─────────────────────────────────────────────────────────────────────────────
