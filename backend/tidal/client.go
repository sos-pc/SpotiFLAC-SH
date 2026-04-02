package tidal

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
)

type TidalDownloader struct {
	client     *http.Client
	timeout    time.Duration
	maxRetries int
	apiURL     string
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
	req.Header.Set("x-tidal-token", GetPublicTidalToken())

	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Tidal Search] Failed with status %d: %s\n", resp.StatusCode, string(bodyBytes))
		return "", fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	var searchResp struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
		fmt.Printf("[Tidal Search] Failed to decode JSON: %v\nBody: %s\n", err, string(bodyBytes))
		return "", err
	}
	if len(searchResp.Items) == 0 {
		return "", fmt.Errorf("no tracks found")
	}

	return fmt.Sprintf("https://tidal.com/track/%d", searchResp.Items[0].ID), nil
}

func (t *TidalDownloader) GetTidalURLFromSpotify(spotifyTrackID string) (string, error) {

	spotifyBase := "https://open.spotify.com/track/"
	spotifyURL := fmt.Sprintf("%s%s", spotifyBase, spotifyTrackID)

	apiBase := "https://api.song.link/v1-alpha.1/links?url="
	apiURL := fmt.Sprintf("%s%s", apiBase, url.QueryEscape(spotifyURL))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	fmt.Println("Getting Tidal URL...")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get Tidal URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var songLinkResp struct {
		LinksByPlatform map[string]struct {
			URL string `json:"url"`
		} `json:"linksByPlatform"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&songLinkResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	tidalLink, ok := songLinkResp.LinksByPlatform["tidal"]
	if !ok || tidalLink.URL == "" {
		return "", fmt.Errorf("tidal link not found")
	}

	tidalURL := tidalLink.URL
	fmt.Printf("Found Tidal URL: %s\n", tidalURL)
	return tidalURL, nil
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
	fmt.Println("Fetching URL...")

	var body []byte
	var respStatusCode int
	success := false

	token, err := GetValidTidalToken()
	if err != nil {
		fmt.Printf("✗ Tidal authentication failed: %v. Falling back to public HiFi APIs...\n", err)
	}

	if token != nil {
		countryCode := token.CountryCode
		if countryCode == "" {
			countryCode = "US"
		}
		url := fmt.Sprintf("https://api.tidal.com/v1/tracks/%d/playbackinfopostpaywall?countryCode=%s&audioquality=%s&playbackmode=STREAM&assetpresentation=FULL", trackID, countryCode, quality)
		fmt.Printf("Tidal API URL: %s\n", url)

		req, err := http.NewRequest("GET", url, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token.AccessToken)
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

			resp, err := t.client.Do(req)
			if err == nil {
				respStatusCode = resp.StatusCode
				if resp.StatusCode == 200 {
					body, _ = io.ReadAll(resp.Body)
					success = true
				} else {
					bodyBytes, _ := io.ReadAll(resp.Body)
					fmt.Printf("✗ Tidal API returned status code: %d - %s\n", resp.StatusCode, string(bodyBytes))
					if resp.StatusCode == 401 || resp.StatusCode == 403 {
						// Ne plus supprimer le token sur une erreur 401/403 de streaming.
						// Les anciens clients TV (utilisés ici) se voient refuser le scope playback
						// même avec un compte valide. La suppression forcerait une boucle de reconnexion inutile.
						_, _ = RefreshTidalToken(token)
					} else if resp.StatusCode == 404 && (quality == "HI_RES_LOSSLESS" || quality == "HI_RES") {
						fmt.Printf("⚠ Tidal personal API: %s unavailable for track %d, retrying with LOSSLESS...\n", quality, trackID)
						losslessURL := fmt.Sprintf("https://api.tidal.com/v1/tracks/%d/playbackinfopostpaywall?countryCode=%s&audioquality=LOSSLESS&playbackmode=STREAM&assetpresentation=FULL", trackID, countryCode)
						if lreq, lerr := http.NewRequest("GET", losslessURL, nil); lerr == nil {
							lreq.Header.Set("Authorization", "Bearer "+token.AccessToken)
							lreq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")
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
				fmt.Printf("✗ Tidal API request failed: %v\n", err)
			}
		}
	}

	if !success {
		fmt.Println("Falling back to public HiFi APIs...")
		apis := util.GetTidalProxies()
		for _, apiBase := range apis {
			fallbackURL := fmt.Sprintf("%s/track/?id=%d&audioquality=%s", apiBase, trackID, quality)
			fmt.Printf("Trying fallback API: %s\n", fallbackURL)
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
					fmt.Printf("✗ Fallback %s failed (upstream error)\n", apiBase)
				} else if strings.Contains(bodyStr, "\"PREVIEW\"") {
					fmt.Printf("✗ Fallback %s returned a PREVIEW (30s snippet), skipping\n", apiBase)
				} else {
					fmt.Printf("✓ Fallback API %s succeeded\n", apiBase)
					body = bodyBytes
					success = true
					break
				}
			} else {
				fmt.Printf("✗ Fallback %s returned status %d\n", apiBase, resp.StatusCode)
				resp.Body.Close()
			}
		}
	}

	if !success {
		return "", fmt.Errorf("failed to get download URL: API returned status code %d and fallbacks failed", respStatusCode)
	}

	var v2Response TidalAPIResponseV2
	if err := json.Unmarshal(body, &v2Response); err == nil && v2Response.Data.Manifest != "" {
		fmt.Println("✓ Tidal manifest found (v2 API)")
		return "MANIFEST:" + v2Response.Data.Manifest, nil
	}

	var officialResp struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(body, &officialResp); err == nil && officialResp.Manifest != "" {
		fmt.Println("✓ Tidal manifest found (Official API)")
		return "MANIFEST:" + officialResp.Manifest, nil
	}

	var apiResponses []TidalAPIResponse
	if err := json.Unmarshal(body, &apiResponses); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		fmt.Printf("✗ Failed to decode Tidal API response: %v (response: %s)\n", err, bodyStr)
		return "", fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	if len(apiResponses) == 0 {
		fmt.Println("✗ Tidal API returned empty response")
		return "", fmt.Errorf("no download URL in response")
	}

	for _, item := range apiResponses {
		if item.OriginalTrackURL != "" {
			fmt.Println("✓ Tidal download URL found")
			return item.OriginalTrackURL, nil
		}
	}

	fmt.Println("✗ No valid download URL in Tidal API response")
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

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	resp, err := t.client.Do(req)

	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	pw := util.NewProgressWriter(out)
	_, err = io.Copy(pw, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Printf("\rDownloaded: %.2f MB (Complete)\n", float64(pw.GetTotal())/(1024*1024))

	fmt.Println("Download complete")
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
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")
		return client.Do(req)
	}

	if directURL != "" && (strings.Contains(strings.ToLower(mimeType), "flac") || mimeType == "") {
		fmt.Println("Downloading file...")

		resp, err := doRequest(directURL)
		if err != nil {
			return fmt.Errorf("failed to download file: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return fmt.Errorf("download failed with status %d", resp.StatusCode)
		}

		out, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create file: %w", err)
		}
		defer out.Close()

		pw := util.NewProgressWriter(out)
		_, err = io.Copy(pw, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}

		fmt.Printf("\rDownloaded: %.2f MB (Complete)\n", float64(pw.GetTotal())/(1024*1024))
		fmt.Println("Download complete")
		return nil
	}

	tempPath := outputPath + ".m4a.tmp"

	if directURL != "" {
		fmt.Printf("Downloading non-FLAC file (%s)...\n", mimeType)

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

		pw := util.NewProgressWriter(out)
		_, err = io.Copy(pw, resp.Body)
		out.Close()

		if err != nil {
			os.Remove(tempPath)
			return fmt.Errorf("failed to write temp file: %w", err)
		}

		fmt.Printf("\rDownloaded: %.2f MB (Complete)\n", float64(pw.GetTotal())/(1024*1024))

	} else {

		fmt.Printf("Downloading %d segments...\n", len(mediaURLs)+1)

		out, err := os.Create(tempPath)
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}

		fmt.Print("Downloading init segment... ")
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
		fmt.Println("OK")

		totalSegments := len(mediaURLs)
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
				util.SetDownloadSpeed(speedMBps)
				lastTime = now
				lastBytes = totalBytes
			}
			util.SetDownloadProgress(mbDownloaded)

			fmt.Printf("\rDownloading: %.2f MB (%d/%d segments)", mbDownloaded, i+1, totalSegments)
		}

		out.Close()

		tempInfo, _ := os.Stat(tempPath)
		fmt.Printf("\rDownloaded: %.2f MB (Complete)          \n", float64(tempInfo.Size())/(1024*1024))
	}

	fmt.Println("Converting to FLAC...")
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
	fmt.Println("Download complete")

	return nil
}

func (t *TidalDownloader) DownloadByURL(tidalURL, outputDir, quality, filenameFormat string, includeTrackNumber bool, position int, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate string, useAlbumTrackNumber bool, spotifyCoverURL string, embedMaxQualityCover bool, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks int, spotifyTotalDiscs int, spotifyCopyright, spotifyPublisher, spotifyURL string, allowFallback bool, useFirstArtistOnly bool, useSingleGenre bool, embedGenre bool) (string, error) {
	if outputDir != "." {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return "", fmt.Errorf("directory error: %w", err)
		}
	}

	fmt.Printf("Using Tidal URL: %s\n", tidalURL)

	trackID, err := t.GetTrackIDFromURL(tidalURL)
	if err != nil {
		return "", err
	}

	if trackID == 0 {
		return "", fmt.Errorf("no track ID found")
	}

	artistName := spotifyArtistName
	trackTitle := spotifyTrackName
	albumTitle := spotifyAlbumName

	artistNameForFile := util.SanitizeFilename(artistName)
	albumArtistForFile := util.SanitizeFilename(spotifyAlbumArtist)

	if useFirstArtistOnly {
		artistNameForFile = util.SanitizeFilename(util.GetFirstArtist(artistName))
		albumArtistForFile = util.SanitizeFilename(util.GetFirstArtist(spotifyAlbumArtist))
	}

	trackTitleForFile := util.SanitizeFilename(trackTitle)
	albumTitleForFile := util.SanitizeFilename(albumTitle)

	filename := buildTidalFilename(trackTitleForFile, artistNameForFile, albumTitleForFile, albumArtistForFile, spotifyReleaseDate, spotifyTrackNumber, spotifyDiscNumber, filenameFormat, includeTrackNumber, position, useAlbumTrackNumber)
	outputFilename := filepath.Join(outputDir, filename)

	if fileInfo, err := os.Stat(outputFilename); err == nil && fileInfo.Size() > 0 {
		fmt.Printf("File already exists: %s (%.2f MB)\n", outputFilename, float64(fileInfo.Size())/(1024*1024))
		return "EXISTS:" + outputFilename, nil
	}

	downloadURL, err := t.GetDownloadURL(trackID, quality)
	if err != nil {
		if (quality == "HI_RES" || quality == "HI_RES_LOSSLESS") && allowFallback {
			fmt.Printf("⚠ %s unavailable/failed, falling back to LOSSLESS...\n", quality)
			downloadURL, err = t.GetDownloadURL(trackID, "LOSSLESS")
			if err != nil {
				return "", fmt.Errorf("failed to get download URL (%s & LOSSLESS both failed): %w", quality, err)
			}
		} else {
			return "", err
		}
	}

	type mbResult struct {
		ISRC     string
		Metadata meta.Metadata
	}

	metaChan := make(chan mbResult, 1)
	if embedGenre && spotifyURL != "" {
		go func() {
			res := mbResult{}
			var isrc string
			parts := strings.Split(spotifyURL, "/")
			if len(parts) > 0 {
				sID := strings.Split(parts[len(parts)-1], "?")[0]
				if sID != "" {
					client := songlink.GetSongLinkClient()
					if val, err := client.GetISRC(sID); err == nil {
						isrc = val
					}
				}
			}
			res.ISRC = isrc
			if isrc != "" {
				fmt.Println("Fetching MusicBrainz metadata...")
				if fetchedMeta, err := meta.FetchMusicBrainzMetadata(isrc, trackTitle, artistName, albumTitle, useSingleGenre, embedGenre); err == nil {
					res.Metadata = fetchedMeta
					fmt.Println("✓ MusicBrainz metadata fetched")
				} else {
					fmt.Printf("Warning: Failed to fetch MusicBrainz metadata: %v\n", err)
				}
			}
			metaChan <- res
		}()
	} else {
		close(metaChan)
	}

	fmt.Printf("Downloading to: %s\n", outputFilename)
	if err := t.DownloadFile(downloadURL, outputFilename); err != nil {
		return "", err
	}

	var isrc string
	var mbMeta meta.Metadata
	if spotifyURL != "" {
		result := <-metaChan
		isrc = result.ISRC
		mbMeta = result.Metadata
	}

	fmt.Println("Adding metadata...")

	coverPath := ""

	if spotifyCoverURL != "" {
		coverPath = outputFilename + ".cover.jpg"
		coverClient := meta.NewCoverClient()
		if err := coverClient.DownloadCoverToPath(spotifyCoverURL, coverPath, embedMaxQualityCover); err != nil {
			fmt.Printf("Warning: Failed to download Spotify cover: %v\n", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
			fmt.Println("Spotify cover downloaded")
		}
	}

	trackNumberToEmbed := spotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := meta.Metadata{
		Title:       trackTitle,
		Artist:      artistName,
		Album:       albumTitle,
		AlbumArtist: spotifyAlbumArtist,
		Date:        spotifyReleaseDate,
		TrackNumber: trackNumberToEmbed,
		TotalTracks: spotifyTotalTracks,
		DiscNumber:  spotifyDiscNumber,
		TotalDiscs:  spotifyTotalDiscs,
		URL:         spotifyURL,
		Copyright:   spotifyCopyright,
		Publisher:   spotifyPublisher,
		Description: "https://github.com/afkarxyz/SpotiFLAC",
		ISRC:        isrc,
		Genre:       mbMeta.Genre,
	}

	if err := meta.EmbedMetadata(outputFilename, metadata, coverPath); err != nil {
		fmt.Printf("Tagging failed: %v\n", err)
	} else {
		fmt.Println("Metadata saved")
	}

	fmt.Println("Done")
	fmt.Println("✓ Downloaded successfully from Tidal")
	return outputFilename, nil
}

func (t *TidalDownloader) DownloadByURLWithFallback(tidalURL, outputDir, quality, filenameFormat string, includeTrackNumber bool, position int, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate string, useAlbumTrackNumber bool, spotifyCoverURL string, embedMaxQualityCover bool, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks int, spotifyTotalDiscs int, spotifyCopyright, spotifyPublisher, spotifyURL string, allowFallback bool, useFirstArtistOnly bool, useSingleGenre bool, embedGenre bool) (string, error) {
	apis, err := t.GetAvailableAPIs()
	if err != nil {
		return "", fmt.Errorf("no APIs available for fallback: %w", err)
	}

	if outputDir != "." {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return "", fmt.Errorf("directory error: %w", err)
		}
	}

	fmt.Printf("Using Tidal URL: %s\n", tidalURL)

	trackID, err := t.GetTrackIDFromURL(tidalURL)
	if err != nil {
		return "", err
	}

	if trackID == 0 {
		return "", fmt.Errorf("no track ID found")
	}

	artistName := spotifyArtistName
	trackTitle := spotifyTrackName
	albumTitle := spotifyAlbumName

	artistNameForFile := util.SanitizeFilename(artistName)
	albumArtistForFile := util.SanitizeFilename(spotifyAlbumArtist)

	if useFirstArtistOnly {
		artistNameForFile = util.SanitizeFilename(util.GetFirstArtist(artistName))
		albumArtistForFile = util.SanitizeFilename(util.GetFirstArtist(spotifyAlbumArtist))
	}

	trackTitleForFile := util.SanitizeFilename(trackTitle)
	albumTitleForFile := util.SanitizeFilename(albumTitle)

	filename := buildTidalFilename(trackTitleForFile, artistNameForFile, albumTitleForFile, albumArtistForFile, spotifyReleaseDate, spotifyTrackNumber, spotifyDiscNumber, filenameFormat, includeTrackNumber, position, useAlbumTrackNumber)
	outputFilename := filepath.Join(outputDir, filename)

	if fileInfo, err := os.Stat(outputFilename); err == nil && fileInfo.Size() > 0 {
		fmt.Printf("File already exists: %s (%.2f MB)\n", outputFilename, float64(fileInfo.Size())/(1024*1024))
		return "EXISTS:" + outputFilename, nil
	}

	successAPI, downloadURL, err := getDownloadURLRotated(apis, trackID, quality)
	if err != nil {
		if (quality == "HI_RES" || quality == "HI_RES_LOSSLESS") && allowFallback {
			fmt.Printf("⚠ %s unavailable/failed on all APIs, falling back to LOSSLESS...\n", quality)
			successAPI, downloadURL, err = getDownloadURLRotated(apis, trackID, "LOSSLESS")
			if err != nil {
				return "", fmt.Errorf("failed to get download URL (%s & LOSSLESS both failed): %w", quality, err)
			}
		} else {
			return "", err
		}
	}

	type mbResultFallback struct {
		ISRC     string
		Metadata meta.Metadata
	}

	metaChan := make(chan mbResultFallback, 1)
	if embedGenre && spotifyURL != "" {
		go func() {
			res := mbResultFallback{}
			var isrc string
			parts := strings.Split(spotifyURL, "/")
			if len(parts) > 0 {
				sID := strings.Split(parts[len(parts)-1], "?")[0]
				if sID != "" {
					client := songlink.GetSongLinkClient()
					if val, err := client.GetISRC(sID); err == nil {
						isrc = val
					}
				}
			}
			res.ISRC = isrc
			if isrc != "" {
				fmt.Println("Fetching MusicBrainz metadata...")
				if fetchedMeta, err := meta.FetchMusicBrainzMetadata(isrc, trackTitle, artistName, albumTitle, useSingleGenre, embedGenre); err == nil {
					res.Metadata = fetchedMeta
					fmt.Println("✓ MusicBrainz metadata fetched")
				} else {
					fmt.Printf("Warning: Failed to fetch MusicBrainz metadata: %v\n", err)
				}
			}
			metaChan <- res
		}()
	} else {
		close(metaChan)
	}

	fmt.Printf("Downloading to: %s\n", outputFilename)
	downloader := NewTidalDownloader(successAPI)
	if err := downloader.DownloadFile(downloadURL, outputFilename); err != nil {
		return "", err
	}

	var isrc string
	var mbMeta meta.Metadata
	if spotifyURL != "" {
		result := <-metaChan
		isrc = result.ISRC
		mbMeta = result.Metadata
	}

	fmt.Println("Adding metadata...")

	coverPath := ""

	if spotifyCoverURL != "" {
		coverPath = outputFilename + ".cover.jpg"
		coverClient := meta.NewCoverClient()
		if err := coverClient.DownloadCoverToPath(spotifyCoverURL, coverPath, embedMaxQualityCover); err != nil {
			fmt.Printf("Warning: Failed to download Spotify cover: %v\n", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
			fmt.Println("Spotify cover downloaded")
		}
	}

	trackNumberToEmbed := spotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := meta.Metadata{
		Title:       trackTitle,
		Artist:      artistName,
		Album:       albumTitle,
		AlbumArtist: spotifyAlbumArtist,
		Date:        spotifyReleaseDate,
		TrackNumber: trackNumberToEmbed,
		TotalTracks: spotifyTotalTracks,
		DiscNumber:  spotifyDiscNumber,
		TotalDiscs:  spotifyTotalDiscs,
		URL:         spotifyURL,
		Copyright:   spotifyCopyright,
		Publisher:   spotifyPublisher,
		Description: "https://github.com/afkarxyz/SpotiFLAC",
		ISRC:        isrc,
		Genre:       mbMeta.Genre,
	}

	if err := meta.EmbedMetadata(outputFilename, metadata, coverPath); err != nil {
		fmt.Printf("Tagging failed: %v\n", err)
	} else {
		fmt.Println("Metadata saved")
	}

	fmt.Println("Done")
	fmt.Println("✓ Downloaded successfully from Tidal")
	return outputFilename, nil
}

func (t *TidalDownloader) Download(spotifyTrackID, outputDir, quality, filenameFormat string, includeTrackNumber bool, position int, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate string, useAlbumTrackNumber bool, spotifyCoverURL string, embedMaxQualityCover bool, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks int, spotifyTotalDiscs int, spotifyCopyright, spotifyPublisher, spotifyURL string, allowFallback bool, useFirstArtistOnly bool, useSingleGenre bool, embedGenre bool) (string, error) {

	var tidalURL string
	var err error
	// Essayer la recherche directe Tidal en premier (pas de rate-limit, ~200ms)
	if spotifyTrackName != "" && spotifyArtistName != "" {
		tidalURL, err = t.SearchTidalByName(spotifyTrackName, spotifyArtistName)
		if err != nil {
			fmt.Printf("Direct Tidal search failed, falling back to song.link: %v\n", err)
		}
	}
	// Fallback sur song.link si la recherche directe échoue
	if tidalURL == "" {
		tidalURL, err = t.GetTidalURLFromSpotify(spotifyTrackID)
		if err != nil {
			return "", fmt.Errorf("could not find track on Tidal: %w", err)
		}
	}

	return t.DownloadByURLWithFallback(tidalURL, outputDir, quality, filenameFormat, includeTrackNumber, position, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate, useAlbumTrackNumber, spotifyCoverURL, embedMaxQualityCover, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks, spotifyTotalDiscs, spotifyCopyright, spotifyPublisher, spotifyURL, allowFallback, useFirstArtistOnly, useSingleGenre, embedGenre)
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

		fmt.Printf("Manifest: BTS format (%s, %s)\n", btsManifest.MimeType, btsManifest.Codecs)
		return btsManifest.URLs[0], "", nil, btsManifest.MimeType, nil
	}

	fmt.Println("Manifest: DASH format")

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
			fmt.Printf("Selected stream: Codec=%s, Bandwidth=%d bps\n", selectedCodecs, selectedBandwidth)
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

		fmt.Printf("Parsed manifest via XML: %d segments\n", segmentCount)

		for i := 1; i <= segmentCount; i++ {
			mediaURL := strings.ReplaceAll(mediaTemplate, "$Number$", fmt.Sprintf("%d", i))
			mediaURLs = append(mediaURLs, mediaURL)
		}
		return "", initURL, mediaURLs, "", nil
	}

	fmt.Println("Using regex fallback for DASH manifest...")

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

	fmt.Printf("Parsed manifest via Regex: %d segments\n", segmentCount)

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
	req.Header.Set("x-tidal-token", GetPublicTidalToken())

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
			fmt.Printf("[Tidal ISRC] Failed to decode JSON: %v\nBody: %s\n", err, string(bodyBytes))
		}
	} else {
		bodyBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Tidal ISRC] API returned status %d: %s\n", resp.StatusCode, string(bodyBytes))
	}

	return 0, "", fmt.Errorf("ISRC not found on Tidal")
}

