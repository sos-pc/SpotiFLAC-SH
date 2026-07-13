package tidal

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/providerutil"
	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

type TidalDownloader struct {
	client        *http.Client
	timeout       time.Duration
	maxRetries    int
	apiURL        string
	SpeedCallback func(mbDownloaded, speedMBps float64)
}

type TidalAPIResponse struct {
	OriginalTrackURL string `json:"OriginalTrackUrl"`
}

type TidalAPIResponseV2 struct {
	Version string `json:"version"`
	Data    struct {
		TrackID           int64  `json:"trackId"`
		AssetPresentation string `json:"assetPresentation"`
		AudioMode         string `json:"audioMode"`
		AudioQuality      string `json:"audioQuality"`
		ManifestMimeType  string `json:"manifestMimeType"`
		ManifestHash      string `json:"manifestHash"`
		Manifest          string `json:"manifest"`
		BitDepth          int    `json:"bitDepth"`
		SampleRate        int    `json:"sampleRate"`
	} `json:"data"`
}

type TidalBTSManifest struct {
	MimeType       string   `json:"mimeType"`
	Codecs         string   `json:"codecs"`
	EncryptionType string   `json:"encryptionType"`
	URLs           []string `json:"urls"`
}

func NewTidalDownloader(apiURL string) *TidalDownloader {
	if apiURL == "" {
		downloader := &TidalDownloader{
			client:     util.NewHTTPClient(5 * time.Second),
			timeout:    5 * time.Second,
			maxRetries: 3,
			apiURL:     "",
		}

		apis, err := downloader.GetAvailableAPIs()
		if err == nil && len(apis) > 0 {
			apiURL = apis[0]
		}
	}

	return &TidalDownloader{
		client:     util.NewHTTPClient(5 * time.Second),
		timeout:    5 * time.Second,
		maxRetries: 3,
		apiURL:     apiURL,
	}
}

func (t *TidalDownloader) GetAvailableAPIs() ([]string, error) {
	apis := []string{
		"https://api.tidal.com",
	}
	return apis, nil
}

func (t *TidalDownloader) SearchTidalByName(trackName, artistName string) (string, error) {
	cleanArtist := artistName
	for _, sep := range []string{", ", " & ", " feat.", " ft.", " featuring "} {
		if idx := strings.Index(cleanArtist, sep); idx > 0 {
			cleanArtist = strings.TrimSpace(cleanArtist[:idx])
			break
		}
	}
	cleanTrack := trackName
	for _, sep := range []string{" - ", " (", " ["} {
		if idx := strings.Index(cleanTrack, sep); idx > 0 {
			cleanTrack = strings.TrimSpace(cleanTrack[:idx])
			break
		}
	}

	query := url.QueryEscape(cleanTrack + " " + cleanArtist)
	apiURL := fmt.Sprintf("https://api.tidal.com/v1/search/tracks?query=%s&limit=1&countryCode=%s", query, GetTidalCountryCode())

	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("x-tidal-token", PublicTidalToken)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		slog.Debug("[Tidal Search] Failed", "status", resp.StatusCode, "body", string(bodyBytes))
		return "", fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	var searchResp struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
		slog.Debug("[Tidal Search] Failed to decode JSON", "err", err, "body", string(bodyBytes))
		return "", err
	}
	if len(searchResp.Items) == 0 {
		return "", fmt.Errorf("no tracks found")
	}

	return fmt.Sprintf("https://tidal.com/track/%d", searchResp.Items[0].ID), nil
}

func (t *TidalDownloader) GetTidalURLFromSpotify(spotifyTrackID string) (string, error) {
	slog.Debug("[Tidal] Getting Tidal URL from Spotify")

	client := songlink.GetSongLinkClient()
	urls, err := client.GetAllURLsFromSpotify(spotifyTrackID, "")
	if err != nil {
		return "", fmt.Errorf("failed to get Tidal URL: %w", err)
	}
	if urls.TidalURL == "" {
		return "", fmt.Errorf("tidal link not found")
	}

	slog.Debug("[Tidal] Found Tidal URL", "url", urls.TidalURL)
	return urls.TidalURL, nil
}

func (t *TidalDownloader) GetTrackIDFromURL(tidalURL string) (int64, error) {

	parts := strings.Split(tidalURL, "/track/")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid tidal URL format")
	}

	trackIDStr := strings.Split(parts[1], "?")[0]
	trackIDStr = strings.TrimSpace(trackIDStr)

	var trackID int64
	_, err := fmt.Sscanf(trackIDStr, "%d", &trackID)
	if err != nil {
		return 0, fmt.Errorf("failed to parse track ID: %w", err)
	}

	return trackID, nil
}

func (t *TidalDownloader) GetDownloadURL(trackID int64, quality string) (string, error) {
	slog.Debug("[Tidal] Fetching download URL")

	var body []byte
	var respStatusCode int
	success := false

	token, err := GetValidTidalToken()
	if err != nil {
		slog.Debug("[Tidal] Authentication failed, falling back to public HiFi APIs", "err", err)
	}

	if token != nil {
		countryCode := token.CountryCode
		if countryCode == "" {
			countryCode = "US"
		}
		url := fmt.Sprintf("https://api.tidal.com/v1/tracks/%d/playbackinfopostpaywall?countryCode=%s&audioquality=%s&playbackmode=STREAM&assetpresentation=FULL", trackID, countryCode, quality)
		slog.Debug("[Tidal] API URL", "url", url)

		req, err := http.NewRequest("GET", url, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)
			req.Header.Set("User-Agent", providerutil.ChromeUserAgent)

			resp, err := t.client.Do(req)
			if err == nil {
				respStatusCode = resp.StatusCode
				if resp.StatusCode == 200 {
					body, _ = io.ReadAll(resp.Body)
					success = true
				} else {
					bodyBytes, _ := io.ReadAll(resp.Body)
					slog.Debug("[Tidal] API returned non-200 status", "status", resp.StatusCode, "body", string(bodyBytes))
					if resp.StatusCode == 401 || resp.StatusCode == 403 {
						// Ne plus supprimer le token sur une erreur 401/403 de streaming.
						// Les anciens clients TV (utilisés ici) se voient refuser le scope playback
						// même avec un compte valide. La suppression forcerait une boucle de reconnexion inutile.
						_, _ = RefreshTidalToken(token)
					} else if resp.StatusCode == 404 && (quality == "HI_RES_LOSSLESS" || quality == "HI_RES") {
						slog.Debug("[Tidal] Quality unavailable for track, retrying with LOSSLESS", "quality", quality, "track_id", trackID)
						losslessURL := fmt.Sprintf("https://api.tidal.com/v1/tracks/%d/playbackinfopostpaywall?countryCode=%s&audioquality=LOSSLESS&playbackmode=STREAM&assetpresentation=FULL", trackID, countryCode)
						if lreq, lerr := http.NewRequest("GET", losslessURL, nil); lerr == nil {
							lreq.Header.Set("Authorization", "Bearer "+token.AccessToken)
							lreq.Header.Set("User-Agent", providerutil.ChromeUserAgent)
							if lresp, lerr := t.client.Do(lreq); lerr == nil {
								if lresp.StatusCode == 200 {
									body, _ = io.ReadAll(lresp.Body)
									success = true
								}
								lresp.Body.Close()
							}
						}
					}
				}
				resp.Body.Close()
			} else {
				slog.Debug("[Tidal] API request failed", "err", err)
			}
		}
	}

	if !success {
		slog.Debug("[Tidal] Falling back to public HiFi APIs")
		apis := util.GetTidalProxiesEffective()
		for _, apiBase := range apis {
			fallbackURL := fmt.Sprintf("%s/track/?id=%d&audioquality=%s", apiBase, trackID, quality)
			slog.Debug("[Tidal] Trying fallback API", "url", fallbackURL)
			req, err := http.NewRequest("GET", fallbackURL, nil)
			if err != nil {
				continue
			}
			resp, err := t.client.Do(req)
			if err != nil {
				continue
			}
			if resp.StatusCode == 200 {
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				bodyStr := string(bodyBytes)
				if strings.Contains(bodyStr, "Upstream API error") || strings.Contains(bodyStr, "\"detail\"") {
					slog.Debug("[Tidal] Fallback failed (upstream error)", "api", apiBase)
				} else if strings.Contains(bodyStr, "\"PREVIEW\"") {
					slog.Debug("[Tidal] Fallback returned a preview snippet, skipping", "api", apiBase)
				} else {
					slog.Debug("[Tidal] Fallback API succeeded", "api", apiBase)
					body = bodyBytes
					success = true
					break
				}
			} else {
				slog.Debug("[Tidal] Fallback returned non-200 status", "api", apiBase, "status", resp.StatusCode)
				resp.Body.Close()
			}
		}
	}

	if !success {
		return "", fmt.Errorf("failed to get download URL: API returned status code %d and fallbacks failed", respStatusCode)
	}

	var v2Response TidalAPIResponseV2
	if err := json.Unmarshal(body, &v2Response); err == nil && v2Response.Data.Manifest != "" {
		slog.Debug("[Tidal] Manifest found (v2 API)")
		return "MANIFEST:" + v2Response.Data.Manifest, nil
	}

	var officialResp struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(body, &officialResp); err == nil && officialResp.Manifest != "" {
		slog.Debug("[Tidal] Manifest found (Official API)")
		return "MANIFEST:" + officialResp.Manifest, nil
	}

	var apiResponses []TidalAPIResponse
	if err := json.Unmarshal(body, &apiResponses); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		slog.Debug("[Tidal] Failed to decode API response", "err", err, "response", bodyStr)
		return "", fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	if len(apiResponses) == 0 {
		slog.Debug("[Tidal] API returned empty response")
		return "", fmt.Errorf("no download URL in response")
	}

	for _, item := range apiResponses {
		if item.OriginalTrackURL != "" {
			slog.Debug("[Tidal] Download URL found")
			return item.OriginalTrackURL, nil
		}
	}

	slog.Debug("[Tidal] No valid download URL in API response")
	return "", fmt.Errorf("download URL not found in response")
}

func (t *TidalDownloader) DownloadFile(url, filepath string) error {

	if strings.HasPrefix(url, "MANIFEST:") {
		return t.DownloadFromManifest(strings.TrimPrefix(url, "MANIFEST:"), filepath)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", providerutil.ChromeUserAgent)

	resp, err := t.client.Do(req)

	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	written, err := providerutil.DownloadToFileAtomic(filepath, resp.Body, t.SpeedCallback)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	slog.Debug("[Tidal] Downloaded", "mb", float64(written)/(1024*1024))
	slog.Debug("[Tidal] Download complete")
	return nil
}

func (t *TidalDownloader) DownloadFromManifest(manifestB64, outputPath string) error {
	directURL, initURL, mediaURLs, mimeType, err := parseManifest(manifestB64)
	if err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	client := util.NewHTTPClient(120 * time.Second)

	doRequest := func(url string) (*http.Response, error) {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", providerutil.ChromeUserAgent)
		return client.Do(req)
	}

	if directURL != "" && (strings.Contains(strings.ToLower(mimeType), "flac") || mimeType == "") {
		slog.Debug("[Tidal] Downloading file")

		resp, err := doRequest(directURL)
		if err != nil {
			return fmt.Errorf("failed to download file: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return fmt.Errorf("download failed with status %d", resp.StatusCode)
		}

		written, err := providerutil.DownloadToFileAtomic(outputPath, resp.Body, t.SpeedCallback)
		if err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}

		slog.Debug("[Tidal] Downloaded", "mb", float64(written)/(1024*1024))
		slog.Debug("[Tidal] Download complete")
		return nil
	}

	tempPath := outputPath + ".m4a.tmp"

	if directURL != "" {
		slog.Debug("[Tidal] Downloading non-FLAC file", "mime_type", mimeType)

		resp, err := doRequest(directURL)
		if err != nil {
			return fmt.Errorf("failed to download file: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return fmt.Errorf("download failed with status %d", resp.StatusCode)
		}

		out, err := os.Create(tempPath)
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}

		pw := util.NewProgressWriterWithCallback(out, t.SpeedCallback)
		_, err = io.Copy(pw, resp.Body)
		out.Close()

		if err != nil {
			os.Remove(tempPath)
			return fmt.Errorf("failed to write temp file: %w", err)
		}

		slog.Debug("[Tidal] Downloaded", "mb", float64(pw.GetTotal())/(1024*1024))

	} else {

		slog.Debug("[Tidal] Downloading segments", "count", len(mediaURLs)+1)

		out, err := os.Create(tempPath)
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}

		slog.Debug("[Tidal] Downloading init segment")
		resp, err := doRequest(initURL)
		if err != nil {
			out.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to download init segment: %w", err)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			out.Close()
			os.Remove(tempPath)
			return fmt.Errorf("init segment download failed with status %d", resp.StatusCode)
		}
		_, err = io.Copy(out, resp.Body)
		resp.Body.Close()
		if err != nil {
			out.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to write init segment: %w", err)
		}
		slog.Debug("[Tidal] Init segment downloaded")

		var totalBytes int64
		lastTime := time.Now()
		var lastBytes int64
		for i, mediaURL := range mediaURLs {
			resp, err := doRequest(mediaURL)
			if err != nil {
				out.Close()
				os.Remove(tempPath)
				return fmt.Errorf("failed to download segment %d: %w", i+1, err)
			}
			if resp.StatusCode != 200 {
				resp.Body.Close()
				out.Close()
				os.Remove(tempPath)
				return fmt.Errorf("segment %d download failed with status %d", i+1, resp.StatusCode)
			}
			n, err := io.Copy(out, resp.Body)
			totalBytes += n
			resp.Body.Close()
			if err != nil {
				out.Close()
				os.Remove(tempPath)
				return fmt.Errorf("failed to write segment %d: %w", i+1, err)
			}

			mbDownloaded := float64(totalBytes) / (1024 * 1024)
			now := time.Now()
			timeDiff := now.Sub(lastTime).Seconds()
			var speedMBps float64
			if timeDiff > 0.1 {
				bytesDiff := float64(totalBytes - lastBytes)
				speedMBps = (bytesDiff / (1024 * 1024)) / timeDiff
				lastTime = now
				lastBytes = totalBytes
			}
			if t.SpeedCallback != nil {
				t.SpeedCallback(mbDownloaded, speedMBps)
			}
		}

		out.Close()

		tempInfo, _ := os.Stat(tempPath)
		slog.Debug("[Tidal] Downloaded", "mb", float64(tempInfo.Size())/(1024*1024))
	}

	slog.Debug("[Tidal] Converting to FLAC")
	ffmpegPath, err := util.GetFFmpegPath()
	if err != nil {
		return fmt.Errorf("ffmpeg not found: %w", err)
	}

	if err := util.ValidateExecutable(ffmpegPath); err != nil {
		return fmt.Errorf("invalid ffmpeg executable: %w", err)
	}

	cmd := exec.Command(ffmpegPath, "-y", "-i", tempPath, "-vn", "-c:a", "flac", outputPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {

		m4aPath := strings.TrimSuffix(outputPath, ".flac") + ".m4a"
		os.Rename(tempPath, m4aPath)
		return fmt.Errorf("ffmpeg conversion failed (M4A saved as %s): %w - %s", m4aPath, err, stderr.String())
	}

	os.Remove(tempPath)
	slog.Debug("[Tidal] Download complete")

	return nil
}

func (t *TidalDownloader) DownloadByURL(p DownloadParams) (string, error) {
	if p.OutputDir != "." {
		if err := os.MkdirAll(p.OutputDir, 0755); err != nil {
			return "", fmt.Errorf("directory error: %w", err)
		}
	}

	slog.Debug("[Tidal] Using URL", "url", p.URL)

	trackID, err := t.GetTrackIDFromURL(p.URL)
	if err != nil {
		return "", err
	}

	if trackID == 0 {
		return "", fmt.Errorf("no track ID found")
	}

	artistName := p.SpotifyArtistName
	trackTitle := p.SpotifyTrackName
	albumTitle := p.SpotifyAlbumName

	artistNameForFile := util.SanitizeFilename(artistName)
	albumArtistForFile := util.SanitizeFilename(p.SpotifyAlbumArtist)

	if p.UseFirstArtistOnly {
		artistNameForFile = util.SanitizeFilename(util.GetFirstArtist(artistName))
		albumArtistForFile = util.SanitizeFilename(util.GetFirstArtist(p.SpotifyAlbumArtist))
	}

	trackTitleForFile := util.SanitizeFilename(trackTitle)
	albumTitleForFile := util.SanitizeFilename(albumTitle)

	filename := buildTidalFilename(trackTitleForFile, artistNameForFile, albumTitleForFile, albumArtistForFile, p.SpotifyReleaseDate, p.SpotifyTrackNumber, p.SpotifyDiscNumber, p.FilenameFormat, p.IncludeTrackNumber, p.Position, p.UseAlbumTrackNumber)
	outputFilename := filepath.Join(p.OutputDir, filename)

	if fileInfo, err := os.Stat(outputFilename); err == nil && fileInfo.Size() > 0 {
		slog.Debug("[Tidal] File already exists", "path", outputFilename, "mb", float64(fileInfo.Size())/(1024*1024))
		return "EXISTS:" + outputFilename, nil
	}

	downloadURL, err := t.GetDownloadURL(trackID, p.Quality)
	if err != nil {
		if (p.Quality == "HI_RES" || p.Quality == "HI_RES_LOSSLESS") && p.AllowFallback {
			slog.Debug("[Tidal] Quality unavailable/failed, falling back to LOSSLESS", "quality", p.Quality)
			downloadURL, err = t.GetDownloadURL(trackID, "LOSSLESS")
			if err != nil {
				return "", fmt.Errorf("failed to get download URL (%s & LOSSLESS both failed): %w", p.Quality, err)
			}
		} else {
			return "", err
		}
	}

	metaChan := providerutil.FetchGenreMetadataAsync("", p.SpotifyURL, trackTitle, artistName, albumTitle, p.UseSingleGenre, p.EmbedGenre)

	slog.Debug("[Tidal] Downloading to", "path", outputFilename)
	if err := t.DownloadFile(downloadURL, outputFilename); err != nil {
		return "", err
	}

	var isrc string
	var mbMeta meta.Metadata
	if p.SpotifyURL != "" {
		result := <-metaChan
		isrc = result.ISRC
		mbMeta = result.Metadata
	}

	slog.Debug("[Tidal] Adding metadata")

	coverPath := ""

	if p.SpotifyCoverURL != "" {
		coverPath = outputFilename + ".cover.jpg"
		coverClient := meta.NewCoverClient()
		if err := coverClient.DownloadCoverToPath(p.SpotifyCoverURL, coverPath, p.EmbedMaxQualityCover); err != nil {
			slog.Warn("[Tidal] Failed to download Spotify cover", "err", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
			slog.Debug("[Tidal] Spotify cover downloaded")
		}
	}

	trackNumberToEmbed := p.SpotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := meta.Metadata{
		Title:       trackTitle,
		Artist:      artistName,
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
		ISRC:        isrc,
		Genre:       mbMeta.Genre,
		SpotifyID:   p.SpotifyTrackID,
	}

	if err := meta.EmbedMetadata(outputFilename, metadata, coverPath); err != nil {
		return "", fmt.Errorf("failed to embed metadata: %w", err)
	}
	slog.Debug("[Tidal] Metadata saved")

	slog.Debug("[Tidal] Done")
	slog.Debug("[Tidal] Downloaded successfully")
	return outputFilename, nil
}

func (t *TidalDownloader) DownloadByURLWithFallback(p DownloadParams) (string, error) {
	apis, err := t.GetAvailableAPIs()
	if err != nil {
		return "", fmt.Errorf("no APIs available for fallback: %w", err)
	}

	if p.OutputDir != "." {
		if err := os.MkdirAll(p.OutputDir, 0755); err != nil {
			return "", fmt.Errorf("directory error: %w", err)
		}
	}

	slog.Debug("[Tidal] Using URL", "url", p.URL)

	trackID, err := t.GetTrackIDFromURL(p.URL)
	if err != nil {
		return "", err
	}

	if trackID == 0 {
		return "", fmt.Errorf("no track ID found")
	}

	artistName := p.SpotifyArtistName
	trackTitle := p.SpotifyTrackName
	albumTitle := p.SpotifyAlbumName

	artistNameForFile := util.SanitizeFilename(artistName)
	albumArtistForFile := util.SanitizeFilename(p.SpotifyAlbumArtist)

	if p.UseFirstArtistOnly {
		artistNameForFile = util.SanitizeFilename(util.GetFirstArtist(artistName))
		albumArtistForFile = util.SanitizeFilename(util.GetFirstArtist(p.SpotifyAlbumArtist))
	}

	trackTitleForFile := util.SanitizeFilename(trackTitle)
	albumTitleForFile := util.SanitizeFilename(albumTitle)

	filename := buildTidalFilename(trackTitleForFile, artistNameForFile, albumTitleForFile, albumArtistForFile, p.SpotifyReleaseDate, p.SpotifyTrackNumber, p.SpotifyDiscNumber, p.FilenameFormat, p.IncludeTrackNumber, p.Position, p.UseAlbumTrackNumber)
	outputFilename := filepath.Join(p.OutputDir, filename)

	if fileInfo, err := os.Stat(outputFilename); err == nil && fileInfo.Size() > 0 {
		slog.Debug("[Tidal] File already exists", "path", outputFilename, "mb", float64(fileInfo.Size())/(1024*1024))
		return "EXISTS:" + outputFilename, nil
	}

	successAPI, downloadURL, err := getDownloadURLRotated(apis, trackID, p.Quality)
	if err != nil {
		if (p.Quality == "HI_RES" || p.Quality == "HI_RES_LOSSLESS") && p.AllowFallback {
			slog.Debug("[Tidal] Quality unavailable/failed on all APIs, falling back to LOSSLESS", "quality", p.Quality)
			successAPI, downloadURL, err = getDownloadURLRotated(apis, trackID, "LOSSLESS")
			if err != nil {
				return "", fmt.Errorf("failed to get download URL (%s & LOSSLESS both failed): %w", p.Quality, err)
			}
		} else {
			return "", err
		}
	}

	metaChan := providerutil.FetchGenreMetadataAsync("", p.SpotifyURL, trackTitle, artistName, albumTitle, p.UseSingleGenre, p.EmbedGenre)

	slog.Debug("[Tidal] Downloading to", "path", outputFilename)
	downloader := NewTidalDownloader(successAPI)
	if err := downloader.DownloadFile(downloadURL, outputFilename); err != nil {
		return "", err
	}

	var isrc string
	var mbMeta meta.Metadata
	if p.SpotifyURL != "" {
		result := <-metaChan
		isrc = result.ISRC
		mbMeta = result.Metadata
	}

	slog.Debug("[Tidal] Adding metadata")

	coverPath := ""

	if p.SpotifyCoverURL != "" {
		coverPath = outputFilename + ".cover.jpg"
		coverClient := meta.NewCoverClient()
		if err := coverClient.DownloadCoverToPath(p.SpotifyCoverURL, coverPath, p.EmbedMaxQualityCover); err != nil {
			slog.Warn("[Tidal] Failed to download Spotify cover", "err", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
			slog.Debug("[Tidal] Spotify cover downloaded")
		}
	}

	trackNumberToEmbed := p.SpotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := meta.Metadata{
		Title:       trackTitle,
		Artist:      artistName,
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
		ISRC:        isrc,
		Genre:       mbMeta.Genre,
		SpotifyID:   p.SpotifyTrackID,
	}

	if err := meta.EmbedMetadata(outputFilename, metadata, coverPath); err != nil {
		return "", fmt.Errorf("failed to embed metadata: %w", err)
	}
	slog.Debug("[Tidal] Metadata saved")

	slog.Debug("[Tidal] Done")
	slog.Debug("[Tidal] Downloaded successfully")
	return outputFilename, nil
}

func (t *TidalDownloader) Download(p DownloadParams) (string, error) {

	var tidalURL string
	var err error
	// Try direct Tidal search first (no rate-limit, ~200 ms)
	if p.SpotifyTrackName != "" && p.SpotifyArtistName != "" {
		tidalURL, err = t.SearchTidalByName(p.SpotifyTrackName, p.SpotifyArtistName)
		if err != nil {
			slog.Debug("[Tidal] Direct search failed, falling back to song.link", "err", err)
		}
	}
	// Fall back to song.link when direct search fails
	if tidalURL == "" {
		tidalURL, err = t.GetTidalURLFromSpotify(p.SpotifyTrackID)
		if err != nil {
			return "", fmt.Errorf("could not find track on Tidal: %w", err)
		}
	}

	p.URL = tidalURL
	return t.DownloadByURLWithFallback(p)
}

type SegmentTemplate struct {
	Initialization string `xml:"initialization,attr"`
	Media          string `xml:"media,attr"`
	Timeline       struct {
		Segments []struct {
			Duration int64 `xml:"d,attr"`
			Repeat   int   `xml:"r,attr"`
		} `xml:"S"`
	} `xml:"SegmentTimeline"`
}

type MPD struct {
	XMLName xml.Name `xml:"MPD"`
	Period  struct {
		AdaptationSets []struct {
			MimeType        string `xml:"mimeType,attr"`
			Codecs          string `xml:"codecs,attr"`
			Representations []struct {
				ID              string           `xml:"id,attr"`
				Codecs          string           `xml:"codecs,attr"`
				Bandwidth       int              `xml:"bandwidth,attr"`
				SegmentTemplate *SegmentTemplate `xml:"SegmentTemplate"`
			} `xml:"Representation"`
			SegmentTemplate *SegmentTemplate `xml:"SegmentTemplate"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

func parseManifest(manifestB64 string) (directURL string, initURL string, mediaURLs []string, mimeType string, err error) {
	manifestBytes, err := base64.StdEncoding.DecodeString(manifestB64)
	if err != nil {
		return "", "", nil, "", fmt.Errorf("failed to decode manifest: %w", err)
	}

	manifestStr := string(manifestBytes)

	if strings.HasPrefix(strings.TrimSpace(manifestStr), "{") {
		var btsManifest TidalBTSManifest
		if err := json.Unmarshal(manifestBytes, &btsManifest); err != nil {
			return "", "", nil, "", fmt.Errorf("failed to parse BTS manifest: %w", err)
		}

		if len(btsManifest.URLs) == 0 {
			return "", "", nil, "", fmt.Errorf("no URLs in BTS manifest")
		}

		slog.Debug("[Tidal] Manifest format: BTS", "mime_type", btsManifest.MimeType, "codecs", btsManifest.Codecs)
		return btsManifest.URLs[0], "", nil, btsManifest.MimeType, nil
	}

	slog.Debug("[Tidal] Manifest format: DASH")

	var mpd MPD
	var segTemplate *SegmentTemplate

	if err := xml.Unmarshal(manifestBytes, &mpd); err == nil {
		var selectedBandwidth int
		var selectedCodecs string

		for _, as := range mpd.Period.AdaptationSets {

			if as.SegmentTemplate != nil {

				if segTemplate == nil {
					segTemplate = as.SegmentTemplate
					selectedCodecs = as.Codecs
				}
			}

			for _, rep := range as.Representations {
				if rep.SegmentTemplate != nil {
					if rep.Bandwidth > selectedBandwidth {
						selectedBandwidth = rep.Bandwidth
						segTemplate = rep.SegmentTemplate

						if rep.Codecs != "" {
							selectedCodecs = rep.Codecs
						} else {
							selectedCodecs = as.Codecs
						}
					}
				}
			}
		}

		if selectedBandwidth > 0 {
			slog.Debug("[Tidal] Selected stream", "codec", selectedCodecs, "bandwidth_bps", selectedBandwidth)
		}
	}

	var mediaTemplate string
	segmentCount := 0

	if segTemplate != nil {
		initURL = segTemplate.Initialization
		mediaTemplate = segTemplate.Media

		for _, seg := range segTemplate.Timeline.Segments {
			segmentCount += seg.Repeat + 1
		}
	}

	if segmentCount > 0 && initURL != "" && mediaTemplate != "" {
		initURL = strings.ReplaceAll(initURL, "&amp;", "&")
		mediaTemplate = strings.ReplaceAll(mediaTemplate, "&amp;", "&")

		slog.Debug("[Tidal] Parsed manifest via XML", "segments", segmentCount)

		for i := 1; i <= segmentCount; i++ {
			mediaURL := strings.ReplaceAll(mediaTemplate, "$Number$", fmt.Sprintf("%d", i))
			mediaURLs = append(mediaURLs, mediaURL)
		}
		return "", initURL, mediaURLs, "", nil
	}

	slog.Debug("[Tidal] Using regex fallback for DASH manifest")

	initRe := regexp.MustCompile(`initialization="([^"]+)"`)
	mediaRe := regexp.MustCompile(`media="([^"]+)"`)

	if match := initRe.FindStringSubmatch(manifestStr); len(match) > 1 {
		initURL = match[1]
	}
	if match := mediaRe.FindStringSubmatch(manifestStr); len(match) > 1 {
		mediaTemplate = match[1]
	}

	if initURL == "" {
		return "", "", nil, "", fmt.Errorf("no initialization URL found in manifest")
	}

	initURL = strings.ReplaceAll(initURL, "&amp;", "&")
	mediaTemplate = strings.ReplaceAll(mediaTemplate, "&amp;", "&")

	segmentCount = 0

	segTagRe := regexp.MustCompile(`<S\s+[^>]*>`)
	matches := segTagRe.FindAllString(manifestStr, -1)

	for _, match := range matches {
		repeat := 0
		rRe := regexp.MustCompile(`r="(\d+)"`)
		if rMatch := rRe.FindStringSubmatch(match); len(rMatch) > 1 {
			fmt.Sscanf(rMatch[1], "%d", &repeat)
		}
		segmentCount += repeat + 1
	}

	if segmentCount == 0 {
		return "", "", nil, "", fmt.Errorf("no segments found in manifest (XML: %d, Regex: 0)", len(matches))
	}

	slog.Debug("[Tidal] Parsed manifest via regex", "segments", segmentCount)

	for i := 1; i <= segmentCount; i++ {
		mediaURL := strings.ReplaceAll(mediaTemplate, "$Number$", fmt.Sprintf("%d", i))
		mediaURLs = append(mediaURLs, mediaURL)
	}

	return "", initURL, mediaURLs, "", nil
}

func getDownloadURLRotated(apis []string, trackID int64, quality string) (string, string, error) {
	downloader := NewTidalDownloader("")
	url, err := downloader.GetDownloadURL(trackID, quality)
	if err != nil {
		return "", "", err
	}
	return "https://api.tidal.com", url, nil
}

func buildTidalFilename(title, artist, album, albumArtist, releaseDate string, trackNumber, discNumber int, format string, includeTrackNumber bool, position int, useAlbumTrackNumber bool) string {
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

// GetTidalIDFromISRC cherche un track Tidal via ISRC sur l'API officielle
func GetTidalIDFromISRC(trackName, artistName, isrc string) (int64, string, error) {
	apiURL := fmt.Sprintf("https://api.tidal.com/v1/tracks?countryCode=%s&isrc=%s&limit=1", GetTidalCountryCode(), isrc)

	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("x-tidal-token", PublicTidalToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var searchResp struct {
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(bodyBytes, &searchResp); err == nil && len(searchResp.Items) > 0 {
			return searchResp.Items[0].ID, "https://api.tidal.com", nil
		} else if err != nil {
			slog.Debug("[Tidal ISRC] Failed to decode JSON", "err", err, "body", string(bodyBytes))
		}
	} else {
		bodyBytes, _ := io.ReadAll(resp.Body)
		slog.Debug("[Tidal ISRC] API returned non-200 status", "status", resp.StatusCode, "body", string(bodyBytes))
	}

	return 0, "", fmt.Errorf("ISRC not found on Tidal")
}
