package amazon

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// ─── X-Debug-Key derivation (AES-256-GCM) ────────────────────────────────────
// The key is derived from a SHA-256 hash of a seed string, then used to decrypt
// a hardcoded ciphertext.  The result is the X-Debug-Key header value required
// by amazon.spotbye.qzz.io.  Ported from spotbye/SpotiFLAC backend/amazon.go.

var (
	amazonDebugKeyOnce sync.Once
	amazonDebugKey     string
	amazonDebugKeyErr  error
)

var amazonDebugKeySeedParts = [][]byte{
	[]byte("spotif"),
	[]byte("lac:am"),
	[]byte("azon:spotbye:api:v1"),
}

var amazonDebugKeyAAD = []byte{
	0x61, 0x6d, 0x61, 0x7a, 0x6f, 0x6e, 0x7c, 0x73, 0x70, 0x6f, 0x74, 0x62,
	0x79, 0x65, 0x7c, 0x64, 0x65, 0x62, 0x75, 0x67, 0x7c, 0x76, 0x31,
}

var amazonDebugKeyNonce = []byte{
	0x52, 0x1f, 0xa4, 0x9c, 0x13, 0x77, 0x5b, 0xe2, 0x81, 0x44, 0x90, 0x6d,
}

var amazonDebugKeyCiphertext = []byte{
	0x5b, 0xf9, 0xc1, 0x2e, 0x58, 0xf8, 0x5b, 0xc0, 0x04, 0x68, 0x7e, 0xff,
	0x3d, 0xd6, 0x8b, 0xe3, 0x86, 0x49, 0x6c, 0xfd, 0xc1, 0x49, 0x0b, 0xfb,
}

var amazonDebugKeyTag = []byte{
	0x6c, 0x21, 0x98, 0x51, 0xf2, 0x38, 0x4b, 0x4a, 0x23, 0xe1, 0xc6, 0xd7,
	0x65, 0x7f, 0xfb, 0xa1,
}

// getAmazonDebugKey derives the X-Debug-Key for amazon.spotbye.qzz.io using
// AES-256-GCM decryption.  The result is cached after the first call.
func getAmazonDebugKey() (string, error) {
	amazonDebugKeyOnce.Do(func() {
		hasher := sha256.New()
		for _, part := range amazonDebugKeySeedParts {
			hasher.Write(part)
		}

		block, err := aes.NewCipher(hasher.Sum(nil))
		if err != nil {
			amazonDebugKeyErr = err
			return
		}

		gcm, err := cipher.NewGCM(block)
		if err != nil {
			amazonDebugKeyErr = err
			return
		}

		sealed := make([]byte, 0, len(amazonDebugKeyCiphertext)+len(amazonDebugKeyTag))
		sealed = append(sealed, amazonDebugKeyCiphertext...)
		sealed = append(sealed, amazonDebugKeyTag...)

		plaintext, err := gcm.Open(nil, amazonDebugKeyNonce, sealed, amazonDebugKeyAAD)
		if err != nil {
			amazonDebugKeyErr = err
			return
		}

		amazonDebugKey = string(plaintext)
	})

	if amazonDebugKeyErr != nil {
		return "", amazonDebugKeyErr
	}
	return amazonDebugKey, nil
}

type AmazonDownloader struct {
	client        *http.Client
	regions       []string
	SpeedCallback func(mbDownloaded, speedMBps float64)
}

type SongLinkResponse struct {
	LinksByPlatform map[string]struct {
		URL string `json:"url"`
	} `json:"linksByPlatform"`
}

type AmazonStreamResponse struct {
	StreamURL     string `json:"streamUrl"`
	DecryptionKey string `json:"decryptionKey"`
}

func NewAmazonDownloader() *AmazonDownloader {
	return &AmazonDownloader{
		client:  util.NewHTTPClient(120 * time.Second),
		regions: []string{"us", "eu"},
	}
}

func (a *AmazonDownloader) GetAmazonURLFromSpotify(spotifyTrackID string) (string, error) {

	spotifyBase := "https://open.spotify.com/track/"
	spotifyURL := fmt.Sprintf("%s%s", spotifyBase, spotifyTrackID)

	apiBase := "https://api.song.link/v1-alpha.1/links?url="
	apiURL := fmt.Sprintf("%s%s", apiBase, url.QueryEscape(spotifyURL))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	fmt.Println("Getting Amazon URL...")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get Amazon URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return "", fmt.Errorf("API returned empty response")
	}

	var songLinkResp SongLinkResponse
	if err := json.Unmarshal(body, &songLinkResp); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return "", fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	amazonLink, ok := songLinkResp.LinksByPlatform["amazonMusic"]
	if !ok || amazonLink.URL == "" {
		return "", fmt.Errorf("amazon Music link not found")
	}

	amazonURL := amazonLink.URL

	if strings.Contains(amazonURL, "trackAsin=") {
		parts := strings.Split(amazonURL, "trackAsin=")
		if len(parts) > 1 {
			trackAsin := strings.Split(parts[1], "&")[0]
			amazonURL = fmt.Sprintf("https://music.amazon.com/tracks/%s?musicTerritory=US", trackAsin)
		}
	}

	fmt.Printf("Found Amazon URL: %s\n", amazonURL)
	return amazonURL, nil
}

func (a *AmazonDownloader) getStreamResponse(base, asin string) (*AmazonStreamResponse, error) {
	apiURL := fmt.Sprintf("%s/api/track/%s", base, asin)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")
	if debugKey, keyErr := getAmazonDebugKey(); keyErr == nil && debugKey != "" {
		req.Header.Set("X-Debug-Key", debugKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var apiResp AmazonStreamResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if apiResp.StreamURL == "" {
		return nil, fmt.Errorf("no stream URL in response")
	}
	return &apiResp, nil
}

func (a *AmazonDownloader) DownloadFromAfkarXYZ(amazonURL, outputDir, quality string) (string, error) {

	asinRegex := regexp.MustCompile(`(B[0-9A-Z]{9})`)
	asin := asinRegex.FindString(amazonURL)
	if asin == "" {
		return "", fmt.Errorf("failed to extract ASIN from URL: %s", amazonURL)
	}

	fmt.Printf("Fetching from Amazon API (ASIN: %s)...\n", asin)
	var apiResp *AmazonStreamResponse
	var lastErr error
	for _, proxy := range util.GetAmazonProxies() {
		r, err := a.getStreamResponse(proxy, asin)
		if err == nil {
			apiResp = r
			break
		}
		lastErr = err
		fmt.Printf("[Amazon] Proxy %s failed: %v, trying next...\n", proxy, err)
	}
	if apiResp == nil {
		return "", fmt.Errorf("all Amazon proxies failed: %v", lastErr)
	}

	downloadURL := apiResp.StreamURL
	fileName := fmt.Sprintf("%s.m4a", asin)
	filePath := filepath.Join(outputDir, fileName)

	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	dlReq, _ := http.NewRequest("GET", downloadURL, nil)
	dlReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	dlResp, err := a.client.Do(dlReq)
	if err != nil {
		return "", err
	}
	defer dlResp.Body.Close()

	fmt.Printf("Downloading track: %s\n", fileName)
	pw := util.NewProgressWriterWithCallback(out, a.SpeedCallback)
	_, err = io.Copy(pw, dlResp.Body)
	if err != nil {
		out.Close()
		os.Remove(filePath)
		return "", err
	}

	fmt.Printf("\rDownloaded: %.2f MB (Complete)\n", float64(pw.GetTotal())/(1024*1024))

	if apiResp.DecryptionKey != "" {
		fmt.Printf("Decrypting file...\n")

		ffprobePath, err := util.GetFFprobePath()
		var codec string
		if err == nil {
			cmdProbe := exec.Command(ffprobePath,
				"-v", "quiet",
				"-select_streams", "a:0",
				"-show_entries", "stream=codec_name",
				"-of", "default=noprint_wrappers=1:nokey=1",
				filePath,
			)
			codecOutput, _ := cmdProbe.Output()
			codec = strings.TrimSpace(string(codecOutput))
			fmt.Printf("Detected codec: %s\n", codec)
		}

		targetExt := ".m4a"
		if codec == "flac" {
			targetExt = ".flac"
		}

		decryptedFilename := "dec_" + fileName + targetExt

		if targetExt == ".flac" && strings.HasSuffix(fileName, ".m4a") {
			decryptedFilename = "dec_" + strings.TrimSuffix(fileName, ".m4a") + ".flac"
		}

		decryptedPath := filepath.Join(outputDir, decryptedFilename)

		ffmpegPath, err := util.GetFFmpegPath()
		if err != nil {
			return "", fmt.Errorf("ffmpeg not found for decryption: %w", err)
		}

		if err := util.ValidateExecutable(ffmpegPath); err != nil {
			return "", fmt.Errorf("invalid ffmpeg executable: %w", err)
		}

		key := strings.TrimSpace(apiResp.DecryptionKey)

		cmd := exec.Command(ffmpegPath,
			"-decryption_key", key,
			"-i", filePath,
			"-c", "copy",
			"-y",
			decryptedPath,
		)

		output, err := cmd.CombinedOutput()
		if err != nil {

			outStr := string(output)
			if len(outStr) > 500 {
				outStr = outStr[len(outStr)-500:]
			}
			return "", fmt.Errorf("ffmpeg decryption failed: %v\nTail Output: %s", err, outStr)
		}

		if info, err := os.Stat(decryptedPath); err != nil || info.Size() == 0 {
			return "", fmt.Errorf("decrypted file missing or empty")
		}

		if err := os.Remove(filePath); err != nil {
			fmt.Printf("Warning: Failed to remove encrypted file: %v\n", err)
		}

		finalPath := filepath.Join(outputDir, strings.TrimPrefix(decryptedFilename, "dec_"))
		if err := os.Rename(decryptedPath, finalPath); err != nil {
			return "", fmt.Errorf("failed to rename decrypted file: %w", err)
		}
		filePath = finalPath

		fmt.Println("Decryption successful")
	}

	return filePath, nil
}

func (a *AmazonDownloader) DownloadFromService(amazonURL, outputDir, quality string) (string, error) {
	return a.DownloadFromAfkarXYZ(amazonURL, outputDir, quality)
}

func (a *AmazonDownloader) DownloadByURL(amazonURL, outputDir, quality, filenameFormat, playlistName, playlistOwner string, includeTrackNumber bool, position int, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate, spotifyCoverURL string, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks int, embedMaxQualityCover bool, spotifyTotalDiscs int, spotifyCopyright, spotifyPublisher, spotifyURL string, useFirstArtistOnly bool, useSingleGenre bool, embedGenre bool) (string, error) {

	if outputDir != "." {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	if spotifyTrackName != "" && spotifyArtistName != "" {
		filenameArtist := spotifyArtistName
		filenameAlbumArtist := spotifyAlbumArtist
		if useFirstArtistOnly {
			filenameArtist = util.GetFirstArtist(spotifyArtistName)
			filenameAlbumArtist = util.GetFirstArtist(spotifyAlbumArtist)
		}
		expectedFilename := util.BuildExpectedFilename(spotifyTrackName, filenameArtist, spotifyAlbumName, filenameAlbumArtist, spotifyReleaseDate, filenameFormat, playlistName, playlistOwner, includeTrackNumber, position, spotifyDiscNumber, false)
		expectedPath := filepath.Join(outputDir, expectedFilename)

		if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 0 {
			fmt.Printf("File already exists: %s (%.2f MB)\n", expectedPath, float64(fileInfo.Size())/(1024*1024))
			return "EXISTS:" + expectedPath, nil
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
				if fetchedMeta, err := meta.FetchMusicBrainzMetadata(isrc, spotifyTrackName, spotifyArtistName, spotifyAlbumName, useSingleGenre, embedGenre); err == nil {
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

	fmt.Printf("Using Amazon URL: %s\n", amazonURL)

	filePath, err := a.DownloadFromService(amazonURL, outputDir, quality)
	if err != nil {
		return "", err
	}

	var isrc string
	var mbMeta meta.Metadata
	if spotifyURL != "" {
		result := <-metaChan
		isrc = result.ISRC
		mbMeta = result.Metadata
	}

	originalFileDir := filepath.Dir(filePath)
	originalFileBase := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	if spotifyTrackName != "" && spotifyArtistName != "" {
		safeArtist := util.SanitizeFilename(spotifyArtistName)
		safeAlbumArtist := util.SanitizeFilename(spotifyAlbumArtist)

		if useFirstArtistOnly {
			safeArtist = util.SanitizeFilename(util.GetFirstArtist(spotifyArtistName))
			safeAlbumArtist = util.SanitizeFilename(util.GetFirstArtist(spotifyAlbumArtist))
		}

		safeTitle := util.SanitizeFilename(spotifyTrackName)
		safeAlbum := util.SanitizeFilename(spotifyAlbumName)

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
			newFilename = strings.ReplaceAll(newFilename, "{date}", util.SanitizeFilename(spotifyReleaseDate))

			if spotifyDiscNumber > 0 {
				newFilename = strings.ReplaceAll(newFilename, "{disc}", fmt.Sprintf("%d", spotifyDiscNumber))
			} else {
				newFilename = strings.ReplaceAll(newFilename, "{disc}", "")
			}

			if position > 0 {
				newFilename = strings.ReplaceAll(newFilename, "{track}", fmt.Sprintf("%02d", position))
			} else {

				newFilename = regexp.MustCompile(`\{track\}\.\s*`).ReplaceAllString(newFilename, "")
				newFilename = regexp.MustCompile(`\{track\}\s*-\s*`).ReplaceAllString(newFilename, "")
				newFilename = regexp.MustCompile(`\{track\}\s*`).ReplaceAllString(newFilename, "")
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

	if err := meta.EmbedMetadataToConvertedFile(filePath, metadata, coverPath); err != nil {
		fmt.Printf("Warning: Failed to embed metadata: %v\n", err)
	} else {
		fmt.Println("Metadata embedded successfully")
	}

	if strings.HasSuffix(strings.ToLower(filePath), ".flac") {

		originalM4aPath := filepath.Join(originalFileDir, originalFileBase+".m4a")
		if _, err := os.Stat(originalM4aPath); err == nil {
			if err := os.Remove(originalM4aPath); err != nil {
				fmt.Printf("Warning: Failed to remove M4A file: %v\n", err)
			} else {
				fmt.Printf("Cleaned up original M4A file: %s\n", filepath.Base(originalM4aPath))
			}
		}
	}

	fmt.Println("Done")
	fmt.Println("✓ Downloaded successfully from Amazon Music")
	return filePath, nil
}

func (a *AmazonDownloader) DownloadBySpotifyID(spotifyTrackID, outputDir, quality, filenameFormat, playlistName, playlistOwner string, includeTrackNumber bool, position int, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate, spotifyCoverURL string, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks int, embedMaxQualityCover bool, spotifyTotalDiscs int, spotifyCopyright, spotifyPublisher, spotifyURL string,
	useFirstArtistOnly bool, useSingleGenre bool, embedGenre bool,
) (string, error) {

	amazonURL, err := a.GetAmazonURLFromSpotify(spotifyTrackID)
	if err != nil {
		return "", err
	}

	return a.DownloadByURL(amazonURL, outputDir, quality, filenameFormat, playlistName, playlistOwner, includeTrackNumber, position, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate, spotifyCoverURL, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks, embedMaxQualityCover, spotifyTotalDiscs, spotifyCopyright, spotifyPublisher, spotifyURL, useFirstArtistOnly, useSingleGenre, embedGenre)
}
