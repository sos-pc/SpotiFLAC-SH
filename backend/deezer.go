package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DeezerDownloader struct {
	client *http.Client
}

func NewDeezerDownloader() *DeezerDownloader {
	return &DeezerDownloader{
		client: NewHTTPClient(300 * time.Second),
	}
}

type YoinkifyRequest struct {
	URL         string `json:"url"`
	Format      string `json:"format"`
	GenreSource string `json:"genreSource"`
}

func (d *DeezerDownloader) DownloadFromYoinkify(spotifyURL, outputDir string) (string, error) {
	// yoinkify.lol — domaine expiré/mort depuis 2025, fail fast
	return "", fmt.Errorf("yoinkify.lol unavailable (domain expired)")
}

// getDeezerTrackID — recherche l'ID Deezer via l'API publique (pas de clé requise)
func (d *DeezerDownloader) getDeezerTrackID(trackName, artistName string) (string, error) {
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
	searchURL := fmt.Sprintf("https://api.deezer.com/search?q=%s&limit=3", query)

	resp, err := d.client.Get(searchURL)
	if err != nil {
		return "", fmt.Errorf("deezer search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("deezer search returned status %d", resp.StatusCode)
	}

	var searchResp struct {
		Data []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return "", fmt.Errorf("deezer search decode failed: %w", err)
	}
	if len(searchResp.Data) == 0 {
		return "", fmt.Errorf("deezer: no results for %s - %s", trackName, artistName)
	}

	return fmt.Sprintf("%d", searchResp.Data[0].ID), nil
}

func (d *DeezerDownloader) getFlacURL(base, deezerTrackID string) (string, error) {
	apiURL := fmt.Sprintf("%s/dl/%s", base, deezerTrackID)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var deezResp struct {
		Success bool `json:"success"`
		Links   struct {
			FLAC string `json:"flac"`
			MP3  string `json:"mp3"`
		} `json:"links"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deezResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	if !deezResp.Success || deezResp.Links.FLAC == "" {
		if deezResp.Error != "" {
			return "", fmt.Errorf("%s", deezResp.Error)
		}
		return "", fmt.Errorf("no FLAC link in response")
	}
	return deezResp.Links.FLAC, nil
}

// DownloadFromDeezmate — télécharge directement via un proxy compatible avec l'ID Deezer
func (d *DeezerDownloader) DownloadFromDeezmate(deezerTrackID, outputDir string) (string, error) {
	fmt.Printf("[Deezer] Fetching FLAC URL for ID: %s...\n", deezerTrackID)
	var flacURL string
	var lastErr error
	for _, proxy := range GetDeezerProxies() {
		u, err := d.getFlacURL(proxy, deezerTrackID)
		if err == nil {
			flacURL = u
			break
		}
		lastErr = err
		fmt.Printf("[Deezer] Proxy %s failed: %v, trying next...\n", proxy, err)
	}
	if flacURL == "" {
		return "", fmt.Errorf("all Deezer proxies failed: %v", lastErr)
	}

	fmt.Printf("[Deezer] Downloading FLAC from deezmate CDN...\n")

	dlReq, err := http.NewRequest("GET", flacURL, nil)
	if err != nil {
		return "", fmt.Errorf("deezmate: failed to create download request: %w", err)
	}
	dlReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")

	dlResp, err := d.client.Do(dlReq)
	if err != nil {
		return "", fmt.Errorf("deezmate: download failed: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != 200 {
		return "", fmt.Errorf("deezmate: CDN returned status %d", dlResp.StatusCode)
	}

	tempFileName := fmt.Sprintf("deezer_%d.flac", time.Now().UnixNano())
	filePath := filepath.Join(outputDir, tempFileName)

	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	pw := NewProgressWriter(out)
	if _, err = io.Copy(pw, dlResp.Body); err != nil {
		out.Close()
		os.Remove(filePath)
		return "", fmt.Errorf("deezmate: download stream failed: %w", err)
	}

	fmt.Printf("\r[Deezer] deezmate downloaded: %.2f MB\n", float64(pw.GetTotal())/(1024*1024))
	return filePath, nil
}

func (d *DeezerDownloader) Download(spotifyID, outputDir, filenameFormat, playlistName, playlistOwner string, includeTrackNumber bool, position int, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate, spotifyCoverURL string, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks int, embedMaxQualityCover bool, spotifyTotalDiscs int, spotifyCopyright, spotifyPublisher, spotifyURL string, useFirstArtistOnly bool, useSingleGenre bool, embedGenre bool) (string, error) {

	if outputDir != "." {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	if spotifyTrackName != "" && spotifyArtistName != "" {
		filenameArtist := spotifyArtistName
		filenameAlbumArtist := spotifyAlbumArtist
		if useFirstArtistOnly {
			filenameArtist = GetFirstArtist(spotifyArtistName)
			filenameAlbumArtist = GetFirstArtist(spotifyAlbumArtist)
		}
		expectedFilename := BuildExpectedFilename(spotifyTrackName, filenameArtist, spotifyAlbumName, filenameAlbumArtist, spotifyReleaseDate, filenameFormat, playlistName, playlistOwner, includeTrackNumber, position, spotifyDiscNumber, false)
		expectedPath := filepath.Join(outputDir, expectedFilename)

		if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 0 {
			fmt.Printf("File already exists: %s (%.2f MB)\n", expectedPath, float64(fileInfo.Size())/(1024*1024))
			return "EXISTS:" + expectedPath, nil
		}
	}

	deezerTrackID, err := d.getDeezerTrackID(spotifyTrackName, spotifyArtistName)
	if err != nil {
		return "", fmt.Errorf("deezer: track lookup failed: %w", err)
	}

	filePath, err := d.DownloadFromDeezmate(deezerTrackID, outputDir)
	if err != nil {
		return "", err
	}

	if spotifyTrackName != "" && spotifyArtistName != "" {
		filenameArtist := spotifyArtistName
		filenameAlbumArtist := spotifyAlbumArtist
		if useFirstArtistOnly {
			filenameArtist = GetFirstArtist(spotifyArtistName)
			filenameAlbumArtist = GetFirstArtist(spotifyAlbumArtist)
		}
		newFilename := BuildExpectedFilename(spotifyTrackName, filenameArtist, spotifyAlbumName, filenameAlbumArtist, spotifyReleaseDate, filenameFormat, playlistName, playlistOwner, includeTrackNumber, position, spotifyDiscNumber, false)
		ext := filepath.Ext(filePath)
		if ext == "" {
			ext = ".flac"
		}
		newFilename = newFilename + ext
		newFilePath := filepath.Join(outputDir, newFilename)
		if err := os.Rename(filePath, newFilePath); err != nil {
			fmt.Printf("Warning: Failed to rename file: %v\n", err)
		} else {
			filePath = newFilePath
			fmt.Printf("Renamed to: %s\n", newFilename)
		}
	}

	fmt.Println("Embedding Spotify metadata...")

	coverPath := ""
	if spotifyCoverURL != "" {
		coverPath = filePath + ".cover.jpg"
		coverClient := NewCoverClient()
		if err := coverClient.DownloadCoverToPath(spotifyCoverURL, coverPath, embedMaxQualityCover); err != nil {
			fmt.Printf("Warning: Failed to download cover: %v\n", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
		}
	}

	trackNumberToEmbed := spotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := Metadata{
		Title:       spotifyTrackName,
		Artist:      spotifyArtistName,
		Album:       spotifyAlbumName,
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
	}
	if err := EmbedMetadataToConvertedFile(filePath, metadata, coverPath); err != nil {
		fmt.Printf("Warning: Failed to embed metadata: %v\n", err)
	} else {
		fmt.Println("Metadata embedded successfully")
	}

	fmt.Println("✓ Downloaded successfully from Deezer")
	return filePath, nil
}
