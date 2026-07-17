package qobuz

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/providerutil"
	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// ─── musicdl.me X-Debug-Key derivation (AES-256-GCM) ─────────────────────────
// Ported from spotbye/SpotiFLAC backend/qobuz.go.

var (
	qobuzMusicDLKeyOnce sync.Once
	qobuzMusicDLKey     string
	qobuzMusicDLKeyErr  error
)

var qobuzMusicDLKeySeedParts = [][]byte{
	{0x73, 0x70, 0x6f, 0x74, 0x69, 0x66},
	{0x6c, 0x61, 0x63, 0x3a, 0x71, 0x6f},
	{0x62, 0x75, 0x7a, 0x3a, 0x6d, 0x75, 0x73, 0x69, 0x63, 0x64, 0x6c, 0x3a, 0x76, 0x31},
}

var qobuzMusicDLKeyAAD = []byte{
	0x71, 0x6f, 0x62, 0x75, 0x7a, 0x7c, 0x6d, 0x75, 0x73, 0x69, 0x63, 0x64,
	0x6c, 0x7c, 0x64, 0x65, 0x62, 0x75, 0x67, 0x7c, 0x76, 0x31,
}

var qobuzMusicDLKeyNonce = []byte{
	0x91, 0x2a, 0x5c, 0x77, 0x0f, 0x33, 0xa8, 0x14, 0x62, 0x9d, 0xce, 0x41,
}

var qobuzMusicDLKeyCiphertext = []byte{
	0xf3, 0x4a, 0x83, 0x45, 0x24, 0xb6, 0x22, 0xaf, 0xd6, 0xc3, 0x6e, 0x2d,
	0x56, 0xd1, 0xbb, 0x0b, 0xe9, 0x1b, 0x4f, 0x1c, 0x5f, 0x41, 0x55, 0xc2,
	0xc6, 0xdf, 0xad, 0x21, 0x58, 0xfe, 0xd5, 0xb8, 0x2d, 0x29, 0xf9, 0x9e,
	0x6f, 0xd6,
}

var qobuzMusicDLKeyTag = []byte{
	0x69, 0x0c, 0x42, 0x70, 0x14, 0x83, 0xff, 0x14, 0xc8, 0xbe, 0x17, 0x00,
	0x69, 0xb1, 0xfe, 0xbb,
}

// deriveAESGCMKey derives a plaintext key by hashing seedParts with SHA-256
// and decrypting ciphertext+tag with AES-256-GCM using the given nonce and aad.
func deriveAESGCMKey(seedParts [][]byte, nonce, ciphertext, tag, aad []byte) (string, error) {
	hasher := sha256.New()
	for _, part := range seedParts {
		hasher.Write(part)
	}
	block, err := aes.NewCipher(hasher.Sum(nil))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)
	plaintext, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func getQobuzMusicDLDebugKey() (string, error) {
	qobuzMusicDLKeyOnce.Do(func() {
		qobuzMusicDLKey, qobuzMusicDLKeyErr = deriveAESGCMKey(
			qobuzMusicDLKeySeedParts,
			qobuzMusicDLKeyNonce,
			qobuzMusicDLKeyCiphertext,
			qobuzMusicDLKeyTag,
			qobuzMusicDLKeyAAD,
		)
	})
	if qobuzMusicDLKeyErr != nil {
		return "", qobuzMusicDLKeyErr
	}
	return qobuzMusicDLKey, nil
}

type QobuzDownloader struct {
	client        *http.Client
	appID         string
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

type qobuzMusicDLRequest struct {
	URL     string `json:"url"`
	Quality string `json:"quality"`
}

type qobuzMusicDLResponse struct {
	Success     bool   `json:"success"`
	Type        string `json:"type"`
	URLType     string `json:"url_type"`
	TrackID     string `json:"track_id"`
	Quality     string `json:"quality_label"`
	DownloadURL string `json:"download_url"`
	Message     string `json:"message"`
	Error       string `json:"error"`
}

func NewQobuzDownloader() *QobuzDownloader {
	return &QobuzDownloader{
		client: util.NewHTTPClient(60 * time.Second),
		appID:  "798273057",
	}
}

func (q *QobuzDownloader) searchByISRC(isrc string) (*QobuzTrack, error) {
	apiBase := "https://www.qobuz.com/api.json/0.2/track/search?query="
	url := fmt.Sprintf("%s%s&limit=1&app_id=%s", apiBase, isrc, q.appID)

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
		// worded songlink errors — the mix-up that produced R12's misattribution
		// and that blocks validating the S6 Qobuz-signing fix. A 401 here means
		// the unsigned public app_id was rejected (see docs/upstream-catchup.md
		// §S6), which is a different failure from a download-URL 401 downstream.
		return nil, fmt.Errorf("qobuz: unsigned ISRC search returned status %d", resp.StatusCode)
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

// DownloadFromMusicDL fetches a Qobuz download URL from the musicdl.me API
// using a POST request with X-Debug-Key authentication.
// quality: "6" (FLAC 16-bit), "7" (Hi-Res 24-bit), "27" (Hi-Res Max)
func (q *QobuzDownloader) DownloadFromMusicDL(trackID int64, quality string) (string, error) {
	if strings.TrimSpace(quality) == "" {
		quality = "6"
	}

	debugKey, err := getQobuzMusicDLDebugKey()
	if err != nil {
		return "", fmt.Errorf("failed to derive musicdl.me debug key: %w", err)
	}

	openURL := fmt.Sprintf("https://open.qobuz.com/track/%d", trackID)
	payload, err := json.Marshal(qobuzMusicDLRequest{
		URL:     openURL,
		Quality: strings.TrimSpace(quality),
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode musicdl.me request: %w", err)
	}

	apiURL := util.GetQobuzMusicDLURL()
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("failed to create musicdl.me request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", providerutil.ChromeUserAgent)
	req.Header.Set("X-Debug-Key", debugKey)

	resp, err := q.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach musicdl.me: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read musicdl.me response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 256 {
			preview = preview[:256] + "..."
		}
		return "", fmt.Errorf("musicdl.me returned status %d: %s", resp.StatusCode, preview)
	}

	var dlResp qobuzMusicDLResponse
	if err := json.Unmarshal(body, &dlResp); err != nil {
		return "", fmt.Errorf("failed to decode musicdl.me response: %w", err)
	}

	if !dlResp.Success {
		msg := strings.TrimSpace(dlResp.Error)
		if msg == "" {
			msg = strings.TrimSpace(dlResp.Message)
		}
		if msg == "" {
			msg = "musicdl.me reported failure"
		}
		return "", fmt.Errorf("%s", msg)
	}

	downloadURL := strings.TrimSpace(dlResp.DownloadURL)
	if downloadURL == "" {
		return "", fmt.Errorf("musicdl.me response missing download_url")
	}
	return downloadURL, nil
}

func (q *QobuzDownloader) GetDownloadURL(trackID int64, quality string, allowFallback bool) (string, error) {
	qualityCode := quality
	if qualityCode == "" || qualityCode == "5" {
		qualityCode = "6"
	}

	slog.Debug("[Qobuz] Getting download URL", "track_id", trackID, "quality", qualityCode)

	downloadFunc := func(qual string) (string, error) {
		// 1. Try musicdl.me first (primary provider — POST + X-Debug-Key)
		if musicDLURL := util.GetQobuzMusicDLURL(); musicDLURL != "" {
			slog.Debug("[Qobuz] Trying provider", "provider", "musicdl.me", "quality", qual)
			u, err := q.DownloadFromMusicDL(trackID, qual)
			if err == nil {
				slog.Debug("[Qobuz] Provider succeeded", "provider", "musicdl.me")
				return u, nil
			}
			slog.Debug("[Qobuz] musicdl.me failed", "err", err)
		}

		// 2. Fall back to standard GET-based providers (user-configurable)
		var lastErr error
		for _, api := range util.GetQobuzProviders() {
			currentAPI := api
			slog.Debug("[Qobuz] Trying provider", "provider", currentAPI, "quality", qual)
			u, err := q.DownloadFromStandard(currentAPI, trackID, qual)
			if err == nil {
				slog.Debug("[Qobuz] Provider succeeded", "provider", currentAPI)
				return u, nil
			}
			slog.Debug("[Qobuz] Provider failed", "provider", currentAPI, "err", err)
			lastErr = err
		}

		if lastErr != nil {
			return "", lastErr
		}
		return "", fmt.Errorf("no Qobuz providers configured")
	}

	url, err := downloadFunc(qualityCode)
	if err == nil {
		return url, nil
	}

	currentQuality := qualityCode

	if currentQuality == "27" && allowFallback {
		slog.Debug("[Qobuz] Quality 27 failed, trying fallback to 7 (24-bit Standard)")
		url, err := downloadFunc("7")
		if err == nil {
			slog.Debug("[Qobuz] Success with fallback quality 7")
			return url, nil
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

	numberToUse := position
	if useAlbumTrackNumber && trackNumber > 0 {
		numberToUse = trackNumber
	}

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

		if includeTrackNumber && position > 0 {
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
