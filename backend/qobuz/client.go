package qobuz

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/community"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/providerutil"
	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

type QobuzDownloader struct {
	client        *http.Client
	SpeedCallback func(mbDownloaded, speedMBps float64)
}

type QobuzSearchResponse struct {
	Query  string `json:"query"`
	Tracks struct {
		Limit  int          `json:"limit"`
		Offset int          `json:"offset"`
		Total  int          `json:"total"`
		Items  []QobuzTrack `json:"items"`
	} `json:"tracks"`
}

type QobuzTrack struct {
	ID                  int64   `json:"id"`
	Title               string  `json:"title"`
	Version             string  `json:"version"`
	Duration            int     `json:"duration"`
	TrackNumber         int     `json:"track_number"`
	MediaNumber         int     `json:"media_number"`
	ISRC                string  `json:"isrc"`
	Copyright           string  `json:"copyright"`
	MaximumBitDepth     int     `json:"maximum_bit_depth"`
	MaximumSamplingRate float64 `json:"maximum_sampling_rate"`
	Hires               bool    `json:"hires"`
	HiresStreamable     bool    `json:"hires_streamable"`
	ReleaseDateOriginal string  `json:"release_date_original"`
	Performer           struct {
		Name string `json:"name"`
		ID   int64  `json:"id"`
	} `json:"performer"`
	Album struct {
		Title string `json:"title"`
		ID    string `json:"id"`
		Image struct {
			Small     string `json:"small"`
			Thumbnail string `json:"thumbnail"`
			Large     string `json:"large"`
		} `json:"image"`
		Artist struct {
			Name string `json:"name"`
			ID   int64  `json:"id"`
		} `json:"artist"`
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	} `json:"album"`
}

type QobuzStreamResponse struct {
	URL string `json:"url"`
}

func NewQobuzDownloader() *QobuzDownloader {
	return &QobuzDownloader{
		client: util.NewHTTPClient(60 * time.Second),
	}
}

func (q *QobuzDownloader) searchByISRC(isrc string) (*QobuzTrack, error) {
	url := signedQobuzURL("track/search", map[string]string{
		"query": isrc,
		"limit": "1",
	})

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", providerutil.ChromeUserAgent)
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search track: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// Names the exact call so this can't be confused with the identically
		// worded songlink errors — the mix-up that produced R12's misattribution.
		// A 401 here now means the *signed* search was rejected (the web-player
		// credential rotated, see signed_search.go), a distinct failure from a
		// download-URL 401 downstream.
		return nil, fmt.Errorf("qobuz: signed ISRC search returned status %d", resp.StatusCode)
	}

	var searchResp QobuzSearchResponse

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("API returned empty response")
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	if len(searchResp.Tracks.Items) == 0 {
		return nil, fmt.Errorf("track not found for ISRC: %s", isrc)
	}

	return &searchResp.Tracks.Items[0], nil
}

func buildQobuzAPIURL(apiBase string, trackID int64, quality string) string {
	return fmt.Sprintf("%s%d&quality=%s", apiBase, trackID, quality)
}

func (q *QobuzDownloader) DownloadFromStandard(apiBase string, trackID int64, quality string) (string, error) {
	apiURL := buildQobuzAPIURL(apiBase, trackID, quality)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", providerutil.ChromeUserAgent)
	resp, err := q.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if len(body) == 0 {
		return "", fmt.Errorf("empty body")
	}

	var streamResp QobuzStreamResponse
	if err := json.Unmarshal(body, &streamResp); err == nil && streamResp.URL != "" {
		return streamResp.URL, nil
	}

	var nestedResp struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &nestedResp); err == nil && nestedResp.Data.URL != "" {
		return nestedResp.Data.URL, nil
	}

	return "", fmt.Errorf("invalid response")
}

func (q *QobuzDownloader) GetDownloadURL(trackID int64, quality string, allowFallback bool) (string, error) {
	qualityCode := quality
	if qualityCode == "" || qualityCode == "5" {
		qualityCode = "6"
	}

	slog.Debug("[Qobuz] Getting download URL", "track_id", trackID, "quality", qualityCode)

	downloadFunc := func(qual string) (string, error) {
		// 1. Community service (session-authenticated). The only path that
		//    works out of the box, since every public Qobuz proxy is dead.
		u, err := q.getCommunityDownloadURL(trackID, qual)
		if err == nil {
			slog.Debug("[Qobuz] Provider succeeded", "provider", "community")
			return u, nil
		}
		communityErr := err
		slog.Debug("[Qobuz] community failed", "err", err)

		// 2. User-configured standard GET providers. Independent of the
		//    community service, so worth trying even if it is on cooldown.
		var lastErr error
		for _, api := range util.GetQobuzProviders() {
			slog.Debug("[Qobuz] Trying provider", "provider", api, "quality", qual)
			u, err := q.DownloadFromStandard(api, trackID, qual)
			if err == nil {
				slog.Debug("[Qobuz] Provider succeeded", "provider", api)
				return u, nil
			}
			slog.Debug("[Qobuz] Provider failed", "provider", api, "err", err)
			lastErr = err
		}
		if lastErr != nil {
			return "", lastErr
		}
		// Community was the only path; surface its error (which may be a typed
		// cooldown the quality loop below must respect).
		return "", communityErr
	}

	url, err := downloadFunc(qualityCode)
	if err == nil {
		return url, nil
	}

	// A cooldown closes the service for every quality alike. Retrying at 7 then
	// 6 would just spend two more 503s on a door that is shut — upstream's chain
	// makes exactly that mistake by only short-circuiting on cancellation.
	if community.IsCooldown(err) {
		return "", err
	}

	currentQuality := qualityCode

	if currentQuality == "27" && allowFallback {
		slog.Debug("[Qobuz] Quality 27 failed, trying fallback to 7 (24-bit Standard)")
		url, err := downloadFunc("7")
		if err == nil {
			slog.Debug("[Qobuz] Success with fallback quality 7")
			return url, nil
		}
		if community.IsCooldown(err) {
			return "", err
		}
		currentQuality = "7"
	}

	if currentQuality == "7" && allowFallback {
		slog.Debug("[Qobuz] Quality 7 failed, trying fallback to 6 (16-bit Lossless)")
		url, err := downloadFunc("6")
		if err == nil {
			slog.Debug("[Qobuz] Success with fallback quality 6")
			return url, nil
		}
	}

	return "", fmt.Errorf("all APIs and fallbacks failed. Last error: %v", err)
}

func (q *QobuzDownloader) DownloadFile(url, filepath string) error {
	slog.Debug("[Qobuz] Starting file download")

	downloadClient := util.NewHTTPClient(5 * time.Minute)
	written, err := providerutil.GetToFile(downloadClient, url, filepath, q.SpeedCallback)
	if err != nil {
		return err
	}

	slog.Debug("[Qobuz] Downloaded", "mb", float64(written)/(1024*1024))
	return nil
}

func (q *QobuzDownloader) DownloadCoverArt(coverURL, filepath string) error {
	if coverURL == "" {
		return fmt.Errorf("no cover URL provided")
	}

	resp, err := q.client.Get(coverURL)
	if err != nil {
		return fmt.Errorf("failed to download cover: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("cover download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create cover file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func buildQobuzFilename(title, artist, album, albumArtist, releaseDate string, trackNumber, discNumber int, format string, includeTrackNumber bool, position int, useAlbumTrackNumber bool) string {
	var filename string

	// One resolved value, used for BOTH the guard and the printed number. They
	// used to disagree: the guard tested the raw list position while the printed
	// value was the resolved one, so a track with position 0 and a valid album
	// number got no number at all — and the existence check, which resolved
	// properly, then looked for a numbered filename that was never written.
	numberToUse := util.ResolveTrackNumber(position, trackNumber, useAlbumTrackNumber)

	year := ""
	if len(releaseDate) >= 4 {
		year = releaseDate[:4]
	}

	if strings.Contains(format, "{") {
		filename = format
		filename = strings.ReplaceAll(filename, "{title}", title)
		filename = strings.ReplaceAll(filename, "{artist}", artist)
		filename = strings.ReplaceAll(filename, "{album}", album)
		filename = strings.ReplaceAll(filename, "{album_artist}", albumArtist)
		filename = strings.ReplaceAll(filename, "{year}", year)
		filename = strings.ReplaceAll(filename, "{date}", util.SanitizeFilename(releaseDate))

		if discNumber > 0 {
			filename = strings.ReplaceAll(filename, "{disc}", fmt.Sprintf("%d", discNumber))
		} else {
			filename = strings.ReplaceAll(filename, "{disc}", "")
		}

		if numberToUse > 0 {
			filename = strings.ReplaceAll(filename, "{track}", fmt.Sprintf("%02d", numberToUse))
		} else {

			filename = regexp.MustCompile(`\{track\}\.\s*`).ReplaceAllString(filename, "")
			filename = regexp.MustCompile(`\{track\}\s*-\s*`).ReplaceAllString(filename, "")
			filename = regexp.MustCompile(`\{track\}\s*`).ReplaceAllString(filename, "")
		}
	} else {

		switch format {
		case "artist-title":
			filename = fmt.Sprintf("%s - %s", artist, title)
		case "title":
			filename = title
		default:
			filename = fmt.Sprintf("%s - %s", title, artist)
		}

		if includeTrackNumber && numberToUse > 0 {
			filename = fmt.Sprintf("%02d. %s", numberToUse, filename)
		}
	}

	return filename + ".flac"
}

func (q *QobuzDownloader) DownloadTrack(p DownloadParams) (string, error) {
	if p.SpotifyID == "" {
		return "", fmt.Errorf("spotify ID is required for Qobuz download")
	}
	songlinkClient := songlink.GetSongLinkClient()
	isrc, err := songlinkClient.GetISRC(p.SpotifyID)
	if err != nil {
		return "", fmt.Errorf("failed to get ISRC: %v", err)
	}
	p.DeezerISRC = isrc
	return q.DownloadTrackWithISRC(p)
}

func (q *QobuzDownloader) DownloadTrackWithISRC(p DownloadParams) (string, error) {
	slog.Debug("[Qobuz] Fetching track info", "isrc", p.DeezerISRC)

	metaChan := providerutil.FetchGenreMetadataAsync(p.DeezerISRC, "", p.SpotifyTrackName, p.SpotifyArtistName, p.SpotifyAlbumName, p.UseSingleGenre, p.EmbedGenre)

	if p.OutputDir != "." {
		if err := os.MkdirAll(p.OutputDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	track, err := q.searchByISRC(p.DeezerISRC)
	if err != nil {
		return "", err
	}

	artists := p.SpotifyArtistName
	trackTitle := p.SpotifyTrackName
	albumTitle := p.SpotifyAlbumName

	slog.Debug("[Qobuz] Found track", "artist", artists, "title", trackTitle, "album", albumTitle)

	qualityInfo := "Standard"
	if track.Hires {
		qualityInfo = fmt.Sprintf("Hi-Res (%d-bit / %.1f kHz)", track.MaximumBitDepth, track.MaximumSamplingRate)
	}
	slog.Debug("[Qobuz] Quality", "quality", qualityInfo)

	slog.Debug("[Qobuz] Getting download URL")
	downloadURL, err := q.GetDownloadURL(track.ID, p.Quality, p.AllowFallback)
	if err != nil {
		return "", fmt.Errorf("failed to get download URL: %w", err)
	}

	if downloadURL == "" {
		return "", fmt.Errorf("received empty download URL")
	}

	urlPreview := downloadURL
	if len(downloadURL) > 60 {
		urlPreview = downloadURL[:60] + "..."
	}
	slog.Debug("[Qobuz] Download URL obtained", "url", urlPreview)

	safeArtist := util.SanitizeFilename(artists)
	safeAlbumArtist := util.SanitizeFilename(p.SpotifyAlbumArtist)

	if p.UseFirstArtistOnly {
		safeArtist = util.SanitizeFilename(util.GetFirstArtist(artists))
		safeAlbumArtist = util.SanitizeFilename(util.GetFirstArtist(p.SpotifyAlbumArtist))
	}

	safeTitle := util.SanitizeFilename(trackTitle)
	safeAlbum := util.SanitizeFilename(albumTitle)

	filename := buildQobuzFilename(safeTitle, safeArtist, safeAlbum, safeAlbumArtist, p.SpotifyReleaseDate, p.SpotifyTrackNumber, p.SpotifyDiscNumber, p.FilenameFormat, p.IncludeTrackNumber, p.Position, p.UseAlbumTrackNumber)
	filepath := filepath.Join(p.OutputDir, filename)

	if fileInfo, err := os.Stat(filepath); err == nil && fileInfo.Size() > 0 {
		slog.Debug("[Qobuz] File already exists", "path", filepath, "mb", float64(fileInfo.Size())/(1024*1024))
		return "EXISTS:" + filepath, nil
	}

	slog.Debug("[Qobuz] Downloading FLAC file", "path", filepath)
	if err := q.DownloadFile(downloadURL, filepath); err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}

	slog.Debug("[Qobuz] Downloaded", "path", filepath)

	coverPath := ""

	if p.SpotifyCoverURL != "" {
		coverPath = filepath + ".cover.jpg"
		coverClient := meta.NewCoverClient()
		if err := coverClient.DownloadCoverToPath(p.SpotifyCoverURL, coverPath, p.EmbedMaxQualityCover); err != nil {
			slog.Warn("[Qobuz] Failed to download Spotify cover", "err", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
			slog.Debug("[Qobuz] Spotify cover downloaded")
		}
	}

	var mbMeta meta.Metadata
	if p.DeezerISRC != "" {
		mbMeta = (<-metaChan).Metadata
	}

	slog.Debug("[Qobuz] Embedding metadata and cover art")

	trackNumberToEmbed := p.SpotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := meta.Metadata{
		Title:       trackTitle,
		Artist:      artists,
		Album:       albumTitle,
		AlbumArtist: p.SpotifyAlbumArtist,
		Date:        p.SpotifyReleaseDate,
		TrackNumber: trackNumberToEmbed,
		TotalTracks: p.SpotifyTotalTracks,
		DiscNumber:  p.SpotifyDiscNumber,
		TotalDiscs:  p.SpotifyTotalDiscs,
		URL:         p.SpotifyURL,
		Copyright:   p.SpotifyCopyright,
		Publisher:   p.SpotifyPublisher,
		ISRC:        p.DeezerISRC,
		Genre:       mbMeta.Genre,
		SpotifyID:   p.SpotifyID,
	}

	if err := meta.EmbedMetadata(filepath, metadata, coverPath); err != nil {
		return "", fmt.Errorf("failed to embed metadata: %w", err)
	}

	slog.Debug("[Qobuz] Metadata embedded successfully")
	return filepath, nil
}
