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
	apiURL := "https://yoinkify.lol/api/download"

	payload := YoinkifyRequest{
		URL:         spotifyURL,
		Format:      "flac",
		GenreSource: "spotify",
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	fmt.Printf("Fetching from Deezer API (Yoinkify)...\n")
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Deezer API returned status %d", resp.StatusCode)
	}

	tempFileName := fmt.Sprintf("deezer_%d.flac", time.Now().UnixNano())
	filePath := filepath.Join(outputDir, tempFileName)

	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	fmt.Printf("Downloading track from Deezer...\n")
	pw := NewProgressWriter(out)
	_, err = io.Copy(pw, resp.Body)
	if err != nil {
		out.Close()
		os.Remove(filePath)
		return "", err
	}

	fmt.Printf("\rDownloaded: %.2f MB (Complete)\n", float64(pw.GetTotal())/(1024*1024))
	return filePath, nil
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

	type mbResult struct {
		ISRC     string
		Metadata Metadata
	}

	metaChan := make(chan mbResult, 1)
	if (embedGenre || true) && spotifyURL != "" {
		go func() {
			res := mbResult{}
			var isrc string
			parts := strings.Split(spotifyURL, "/")
			if len(parts) > 0 {
				sID := strings.Split(parts[len(parts)-1], "?")[0]
				if sID != "" {
					client := GetSongLinkClient()
					if val, err := client.GetISRC(sID); err == nil {
						isrc = val
					}
				}
			}
			res.ISRC = isrc
			if isrc != "" && embedGenre {
				fmt.Println("Fetching MusicBrainz metadata...")
				if fetchedMeta, err := FetchMusicBrainzMetadata(isrc, spotifyTrackName, spotifyArtistName, spotifyAlbumName, useSingleGenre, embedGenre); err == nil {
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

	// Tentative 1 : deezmate (Deezer ID → CDN direct, plus fiable)
	var filePath string
	if spotifyTrackName != "" && spotifyArtistName != "" {
		if deezerID, idErr := d.getDeezerTrackID(spotifyTrackName, spotifyArtistName); idErr == nil {
			var deezErr error
			filePath, deezErr = d.DownloadFromDeezmate(deezerID, outputDir)
			if deezErr != nil {
				fmt.Printf("[Deezer] deezmate failed (%v) — falling back to yoinkify\n", deezErr)
				filePath = ""
			}
		} else {
			fmt.Printf("[Deezer] Deezer ID lookup failed (%v) — falling back to yoinkify\n", idErr)
		}
	}

	// Tentative 2 : yoinkify (fallback Spotify URL direct)
	if filePath == "" {
		var yErr error
		filePath, yErr = d.DownloadFromYoinkify(spotifyURL, outputDir)
		if yErr != nil {
			return "", yErr
		}
	}

	var isrc string
	var mbMeta Metadata
	if spotifyURL != "" {
		result := <-metaChan
		isrc = result.ISRC
		mbMeta = result.Metadata
	}

	if spotifyTrackName != "" && spotifyArtistName != "" {
		safeArtist := sanitizeFilename(spotifyArtistName)
		safeAlbumArtist := sanitizeFilename(spotifyAlbumArtist)

		if useFirstArtistOnly {
			safeArtist = sanitizeFilename(GetFirstArtist(spotifyArtistName))
			safeAlbumArtist = sanitizeFilename(GetFirstArtist(spotifyAlbumArtist))
		}

		safeTitle := sanitizeFilename(spotifyTrackName)
		safeAlbum := sanitizeFilename(spotifyAlbumName)

		year := ""
		if len(spotifyReleaseDate) >= 4 {
			year = spotifyReleaseDate[:4]
		}

		var newFilename string

		if strings.Contains(filenameFormat, "{") {
			newFilename = filenameFormat
			newFilename = strings.ReplaceAll(newFilename, "{title}", safeTitle)
			newFilename = strings.ReplaceAll(newFilename, "{artist}", safeArtist)
			newFilename = strings.ReplaceAll(newFilename, "{album}", safeAlbum)
			newFilename = strings.ReplaceAll(newFilename, "{album_artist}", safeAlbumArtist)
			newFilename = strings.ReplaceAll(newFilename, "{year}", year)
			newFilename = strings.ReplaceAll(newFilename, "{date}", SanitizeFilename(spotifyReleaseDate))

			if spotifyDiscNumber > 0 {
				newFilename = strings.ReplaceAll(newFilename, "{disc}", fmt.Sprintf("%d", spotifyDiscNumber))
			} else {
				newFilename = strings.ReplaceAll(newFilename, "{disc}", "")
			}

			if position > 0 {
				newFilename = strings.ReplaceAll(newFilename, "{track}", fmt.Sprintf("%02d", position))
			} else {
				newFilename = strings.ReplaceAll(newFilename, "{track}", "")
			}
		} else {
			switch filenameFormat {
			case "artist-title":
				newFilename = fmt.Sprintf("%s - %s", safeArtist, safeTitle)
			case "title":
				newFilename = safeTitle
			default:
				newFilename = fmt.Sprintf("%s - %s", safeTitle, safeArtist)
			}

			if includeTrackNumber && position > 0 {
				newFilename = fmt.Sprintf("%02d. %s", position, newFilename)
			}
		}

		ext := ".flac"
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
		ISRC:        isrc,
		Genre:       mbMeta.Genre,
	}

	if err := EmbedMetadataToConvertedFile(filePath, metadata, coverPath); err != nil {
		fmt.Printf("Warning: Failed to embed metadata: %v\n", err)
	} else {
		fmt.Println("Metadata embedded successfully")
	}

	fmt.Println("Done")
	fmt.Println("✓ Downloaded successfully from Deezer")
	return filePath, nil
}
