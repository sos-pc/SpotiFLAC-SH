package backend

import (
	"bytes"
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

// DownloadFromDeezmate — télécharge directement via api.deezmate.com avec l'ID Deezer
func (d *DeezerDownloader) DownloadFromDeezmate(deezerTrackID, outputDir string) (string, error) {
	apiURL := fmt.Sprintf("https://api.deezmate.com/dl/%s", deezerTrackID)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("deezmate: failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")

	fmt.Printf("[Deezer] Fetching from deezmate.com (ID: %s)...\n", deezerTrackID)
	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("deezmate: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("deezmate: returned status %d", resp.StatusCode)
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
		return "", fmt.Errorf("deezmate: failed to decode response: %w", err)
	}
	if !deezResp.Success || deezResp.Links.FLAC == "" {
		errMsg := deezResp.Error
		if errMsg == "" {
			errMsg = "no FLAC link in response"
		}
		return "", fmt.Errorf("deezmate: %s", errMsg)
	}

	fmt.Printf("[Deezer] Downloading FLAC from deezmate CDN...\n")

	dlReq, err := http.NewRequest("GET", deezResp.Links.FLAC, nil)
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

	// api.deezmate.com et yoinkify.lol sont tous les deux morts (domaines expirés).
	// TODO: remplacer par un nouveau provider Deezer quand disponible.
	fmt.Printf("[Deezer] No working download provider available (deezmate + yoinkify both dead)\n")
	return "", fmt.Errorf("deezer download unavailable: no working provider (deezmate and yoinkify domains expired)")

}
