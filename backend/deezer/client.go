package deezer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/providerutil"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

type DeezerDownloader struct {
	client        *http.Client
	SpeedCallback func(mbDownloaded, speedMBps float64)
}

func NewDeezerDownloader() *DeezerDownloader {
	return &DeezerDownloader{
		client: util.NewHTTPClient(300 * time.Second),
	}
}

type YoinkifyRequest struct {
	URL         string `json:"url"`
	Format      string `json:"format"`
	GenreSource string `json:"genreSource"`
}

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
	req.Header.Set("User-Agent", providerutil.ChromeUserAgent)
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
	slog.Debug("[Deezer] Fetching FLAC URL", "track_id", deezerTrackID)
	var flacURL string
	var lastErr error
	for _, proxy := range util.GetDeezerProxies() {
		u, err := d.getFlacURL(proxy, deezerTrackID)
		if err == nil {
			flacURL = u
			break
		}
		lastErr = err
		slog.Debug("[Deezer] Proxy failed, trying next", "proxy", proxy, "err", err)
	}
	if flacURL == "" {
		return "", fmt.Errorf("all Deezer proxies failed: %v", lastErr)
	}

	slog.Debug("[Deezer] Downloading FLAC from deezmate CDN")

	dlReq, err := http.NewRequest("GET", flacURL, nil)
	if err != nil {
		return "", fmt.Errorf("deezmate: failed to create download request: %w", err)
	}
	dlReq.Header.Set("User-Agent", providerutil.ChromeUserAgent)

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

	written, err := providerutil.DownloadToFileAtomic(filePath, dlResp.Body, d.SpeedCallback)
	if err != nil {
		return "", fmt.Errorf("deezmate: download stream failed: %w", err)
	}

	slog.Debug("[Deezer] deezmate downloaded", "mb", float64(written)/(1024*1024))
	return filePath, nil
}

func (d *DeezerDownloader) Download(p DownloadParams) (string, error) {

	if p.OutputDir != "." {
		if err := os.MkdirAll(p.OutputDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	if p.SpotifyTrackName != "" && p.SpotifyArtistName != "" {
		filenameArtist := p.SpotifyArtistName
		filenameAlbumArtist := p.SpotifyAlbumArtist
		if p.UseFirstArtistOnly {
			filenameArtist = util.GetFirstArtist(p.SpotifyArtistName)
			filenameAlbumArtist = util.GetFirstArtist(p.SpotifyAlbumArtist)
		}
		expectedFilename := util.BuildExpectedFilename(p.SpotifyTrackName, filenameArtist, p.SpotifyAlbumName, filenameAlbumArtist, p.SpotifyReleaseDate, p.FilenameFormat, p.PlaylistName, p.PlaylistOwner, p.IncludeTrackNumber, p.Position, p.SpotifyDiscNumber, false)
		expectedPath := filepath.Join(p.OutputDir, expectedFilename)

		if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 0 {
			slog.Debug("[Deezer] File already exists", "path", expectedPath, "mb", float64(fileInfo.Size())/(1024*1024))
			return "EXISTS:" + expectedPath, nil
		}
	}

	deezerTrackID, err := d.getDeezerTrackID(p.SpotifyTrackName, p.SpotifyArtistName)
	if err != nil {
		return "", fmt.Errorf("deezer: track lookup failed: %w", err)
	}

	filePath, err := d.DownloadFromDeezmate(deezerTrackID, p.OutputDir)
	if err != nil {
		return "", err
	}

	if p.SpotifyTrackName != "" && p.SpotifyArtistName != "" {
		filenameArtist := p.SpotifyArtistName
		filenameAlbumArtist := p.SpotifyAlbumArtist
		if p.UseFirstArtistOnly {
			filenameArtist = util.GetFirstArtist(p.SpotifyArtistName)
			filenameAlbumArtist = util.GetFirstArtist(p.SpotifyAlbumArtist)
		}
		newFilename := util.BuildExpectedFilename(p.SpotifyTrackName, filenameArtist, p.SpotifyAlbumName, filenameAlbumArtist, p.SpotifyReleaseDate, p.FilenameFormat, p.PlaylistName, p.PlaylistOwner, p.IncludeTrackNumber, p.Position, p.SpotifyDiscNumber, false)
		ext := filepath.Ext(filePath)
		if ext == "" {
			ext = ".flac"
		}
		newFilename = newFilename + ext
		newFilePath := filepath.Join(p.OutputDir, newFilename)
		if err := os.Rename(filePath, newFilePath); err != nil {
			slog.Warn("[Deezer] Failed to rename file", "err", err)
		} else {
			filePath = newFilePath
			slog.Debug("[Deezer] Renamed", "filename", newFilename)
		}
	}

	slog.Debug("[Deezer] Embedding Spotify metadata")

	coverPath := ""
	if p.SpotifyCoverURL != "" {
		coverPath = filePath + ".cover.jpg"
		coverClient := meta.NewCoverClient()
		if err := coverClient.DownloadCoverToPath(p.SpotifyCoverURL, coverPath, p.EmbedMaxQualityCover); err != nil {
			slog.Warn("[Deezer] Failed to download cover", "err", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
		}
	}

	trackNumberToEmbed := p.SpotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := meta.Metadata{
		Title:       p.SpotifyTrackName,
		Artist:      p.SpotifyArtistName,
		Album:       p.SpotifyAlbumName,
		AlbumArtist: p.SpotifyAlbumArtist,
		Date:        p.SpotifyReleaseDate,
		TrackNumber: trackNumberToEmbed,
		TotalTracks: p.SpotifyTotalTracks,
		DiscNumber:  p.SpotifyDiscNumber,
		TotalDiscs:  p.SpotifyTotalDiscs,
		URL:         p.SpotifyURL,
		Copyright:   p.SpotifyCopyright,
		Publisher:   p.SpotifyPublisher,
		SpotifyID:   p.SpotifyID,
	}
	if err := meta.EmbedMetadataToConvertedFile(filePath, metadata, coverPath); err != nil {
		return "", fmt.Errorf("failed to embed metadata: %w", err)
	}
	slog.Debug("[Deezer] Metadata embedded successfully")

	slog.Debug("[Deezer] Downloaded successfully")
	return filePath, nil
}
