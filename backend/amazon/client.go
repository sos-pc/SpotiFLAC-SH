package amazon

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
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

// getAmazonDebugKey derives the X-Debug-Key for amazon.spotbye.qzz.io using
// AES-256-GCM decryption.  The result is cached after the first call.
func getAmazonDebugKey() (string, error) {
	amazonDebugKeyOnce.Do(func() {
		amazonDebugKey, amazonDebugKeyErr = deriveAESGCMKey(
			amazonDebugKeySeedParts,
			amazonDebugKeyNonce,
			amazonDebugKeyCiphertext,
			amazonDebugKeyTag,
			amazonDebugKeyAAD,
		)
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
	slog.Debug("[Amazon] Getting Amazon URL")

	client := songlink.GetSongLinkClient()
	urls, err := client.GetAllURLsFromSpotify(spotifyTrackID, "")
	if err != nil {
		return "", fmt.Errorf("failed to get Amazon URL: %w", err)
	}
	if urls.AmazonURL == "" {
		return "", fmt.Errorf("amazon Music link not found")
	}

	amazonURL := urls.AmazonURL
	if strings.Contains(amazonURL, "trackAsin=") {
		parts := strings.Split(amazonURL, "trackAsin=")
		if len(parts) > 1 {
			trackAsin := strings.Split(parts[1], "&")[0]
			amazonURL = fmt.Sprintf("https://music.amazon.com/tracks/%s?musicTerritory=US", trackAsin)
		}
	}

	slog.Debug("[Amazon] Found Amazon URL", "url", amazonURL)
	return amazonURL, nil
}

func (a *AmazonDownloader) getStreamResponse(base, asin string) (*AmazonStreamResponse, error) {
	apiURL := fmt.Sprintf("%s/api/track/%s", base, asin)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", providerutil.ChromeUserAgent)
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

	slog.Debug("[Amazon] Fetching from Amazon API", "asin", asin)
	var apiResp *AmazonStreamResponse
	var lastErr error
	for _, proxy := range util.GetAmazonProxies() {
		r, err := a.getStreamResponse(proxy, asin)
		if err == nil {
			apiResp = r
			break
		}
		lastErr = err
		slog.Debug("[Amazon] Proxy failed, trying next", "proxy", proxy, "err", err)
	}
	if apiResp == nil {
		// "All proxies failed" with no proxies configured reads as a network
		// problem and sends the reader hunting for one. Since 2026-07-20 the
		// default list is empty on purpose (every known host is dead), so this
		// is now the common case and must say what it actually is.
		if lastErr == nil {
			return "", fmt.Errorf("no Amazon proxy is configured — add one in Settings → APIs → Proxy Configuration")
		}
		return "", fmt.Errorf("all Amazon proxies failed: %v", lastErr)
	}

	downloadURL := apiResp.StreamURL
	fileName := fmt.Sprintf("%s.m4a", asin)
	filePath := filepath.Join(outputDir, fileName)

	dlReq, _ := http.NewRequest("GET", downloadURL, nil)
	dlReq.Header.Set("User-Agent", providerutil.ChromeUserAgent)

	dlResp, err := a.client.Do(dlReq)
	if err != nil {
		return "", err
	}
	defer dlResp.Body.Close()

	slog.Debug("[Amazon] Downloading track", "filename", fileName)
	written, err := providerutil.DownloadToFileAtomic(filePath, dlResp.Body, a.SpeedCallback)
	if err != nil {
		return "", err
	}

	slog.Debug("[Amazon] Downloaded", "mb", float64(written)/(1024*1024))

	if apiResp.DecryptionKey != "" {
		slog.Debug("[Amazon] Decrypting file")

		ffprobePath, err := util.GetFFprobePath()
		var codec string
		if err == nil {
			// Hardened: filePath is the still-encrypted payload a community
			// proxy just handed us — same trust level as the Tidal path. See
			// util.FFprobeHardeningArgs.
			probeArgs := append(util.FFprobeHardeningArgs(),
				"-v", "quiet",
				"-select_streams", "a:0",
				"-show_entries", "stream=codec_name",
				"-of", "default=noprint_wrappers=1:nokey=1",
				filePath,
			)
			cmdProbe := exec.Command(ffprobePath, probeArgs...)
			codecOutput, _ := cmdProbe.Output()
			codec = strings.TrimSpace(string(codecOutput))
			slog.Debug("[Amazon] Detected codec", "codec", codec)
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

		// Hardened: same untrusted payload as the probe above, now going through
		// the mov demuxer (CENC decryption happens inside it — -decryption_key
		// is a demuxer option, not the crypto: protocol, so restricting
		// protocols to `file` does not affect it). See util.FFmpegHardeningArgs.
		decryptArgs := append(util.FFmpegHardeningArgs(),
			"-decryption_key", key,
			"-i", filePath,
			"-c", "copy",
			"-y",
			decryptedPath,
		)
		cmd := exec.Command(ffmpegPath, decryptArgs...)

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
			slog.Warn("[Amazon] Failed to remove encrypted file", "err", err)
		}

		finalPath := filepath.Join(outputDir, strings.TrimPrefix(decryptedFilename, "dec_"))
		if err := os.Rename(decryptedPath, finalPath); err != nil {
			return "", fmt.Errorf("failed to rename decrypted file: %w", err)
		}
		filePath = finalPath

		slog.Debug("[Amazon] Decryption successful")
	}

	return filePath, nil
}

func (a *AmazonDownloader) DownloadByURL(p DownloadParams) (string, error) {

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
		expectedFilename := util.BuildExpectedFilename(p.SpotifyTrackName, filenameArtist, p.SpotifyAlbumName, filenameAlbumArtist, p.SpotifyReleaseDate, p.FilenameFormat, p.PlaylistName, p.PlaylistOwner, p.IncludeTrackNumber, p.Position, p.SpotifyDiscNumber)
		expectedPath := filepath.Join(p.OutputDir, expectedFilename)

		if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 0 {
			slog.Debug("[Amazon] File already exists", "path", expectedPath, "mb", float64(fileInfo.Size())/(1024*1024))
			return "EXISTS:" + expectedPath, nil
		}
	}

	metaChan := providerutil.FetchGenreMetadataAsync("", p.SpotifyURL, p.SpotifyTrackName, p.SpotifyArtistName, p.SpotifyAlbumName, p.UseSingleGenre, p.EmbedGenre)

	slog.Debug("[Amazon] Using URL", "url", p.URL)

	filePath, err := a.DownloadFromAfkarXYZ(p.URL, p.OutputDir, p.Quality)
	if err != nil {
		return "", err
	}

	var isrc string
	var mbMeta meta.Metadata
	if p.SpotifyURL != "" {
		result := <-metaChan
		isrc = result.ISRC
		mbMeta = result.Metadata
	}

	originalFileDir := filepath.Dir(filePath)
	originalFileBase := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	if p.SpotifyTrackName != "" && p.SpotifyArtistName != "" {
		safeArtist := util.SanitizeFilename(p.SpotifyArtistName)
		safeAlbumArtist := util.SanitizeFilename(p.SpotifyAlbumArtist)

		if p.UseFirstArtistOnly {
			safeArtist = util.SanitizeFilename(util.GetFirstArtist(p.SpotifyArtistName))
			safeAlbumArtist = util.SanitizeFilename(util.GetFirstArtist(p.SpotifyAlbumArtist))
		}

		safeTitle := util.SanitizeFilename(p.SpotifyTrackName)
		safeAlbum := util.SanitizeFilename(p.SpotifyAlbumName)

		year := ""
		if len(p.SpotifyReleaseDate) >= 4 {
			year = p.SpotifyReleaseDate[:4]
		}

		var newFilename string

		if strings.Contains(p.FilenameFormat, "{") {
			newFilename = p.FilenameFormat
			newFilename = strings.ReplaceAll(newFilename, "{title}", safeTitle)
			newFilename = strings.ReplaceAll(newFilename, "{artist}", safeArtist)
			newFilename = strings.ReplaceAll(newFilename, "{album}", safeAlbum)
			newFilename = strings.ReplaceAll(newFilename, "{album_artist}", safeAlbumArtist)
			newFilename = strings.ReplaceAll(newFilename, "{year}", year)
			newFilename = strings.ReplaceAll(newFilename, "{date}", util.SanitizeFilename(p.SpotifyReleaseDate))

			if p.SpotifyDiscNumber > 0 {
				newFilename = strings.ReplaceAll(newFilename, "{disc}", fmt.Sprintf("%d", p.SpotifyDiscNumber))
			} else {
				newFilename = strings.ReplaceAll(newFilename, "{disc}", "")
			}

			if p.Position > 0 {
				newFilename = strings.ReplaceAll(newFilename, "{track}", fmt.Sprintf("%02d", p.Position))
			} else {

				newFilename = regexp.MustCompile(`\{track\}\.\s*`).ReplaceAllString(newFilename, "")
				newFilename = regexp.MustCompile(`\{track\}\s*-\s*`).ReplaceAllString(newFilename, "")
				newFilename = regexp.MustCompile(`\{track\}\s*`).ReplaceAllString(newFilename, "")
			}
		} else {

			switch p.FilenameFormat {
			case "artist-title":
				newFilename = fmt.Sprintf("%s - %s", safeArtist, safeTitle)
			case "title":
				newFilename = safeTitle
			default:
				newFilename = fmt.Sprintf("%s - %s", safeTitle, safeArtist)
			}

			if p.IncludeTrackNumber && p.Position > 0 {
				newFilename = fmt.Sprintf("%02d. %s", p.Position, newFilename)
			}
		}

		ext := filepath.Ext(filePath)
		if ext == "" {
			ext = ".flac"
		}
		newFilename = newFilename + ext
		newFilePath := filepath.Join(p.OutputDir, newFilename)

		if err := os.Rename(filePath, newFilePath); err != nil {
			slog.Warn("[Amazon] Failed to rename file", "err", err)
		} else {
			filePath = newFilePath
			slog.Debug("[Amazon] Renamed", "filename", newFilename)
		}
	}

	slog.Debug("[Amazon] Embedding Spotify metadata")

	coverPath := ""

	if p.SpotifyCoverURL != "" {
		coverPath = filePath + ".cover.jpg"
		coverClient := meta.NewCoverClient()
		if err := coverClient.DownloadCoverToPath(p.SpotifyCoverURL, coverPath, p.EmbedMaxQualityCover); err != nil {
			slog.Warn("[Amazon] Failed to download Spotify cover", "err", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
			slog.Debug("[Amazon] Spotify cover downloaded")
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
		ISRC:        isrc,
		Genre:       mbMeta.Genre,
		SpotifyID:   p.SpotifyTrackID,
	}

	embedErr := meta.EmbedMetadataToConvertedFile(filePath, metadata, coverPath)
	if embedErr != nil {
		slog.Warn("[Amazon] Failed to embed metadata", "err", embedErr)
	} else {
		slog.Debug("[Amazon] Metadata embedded successfully")
	}

	if strings.HasSuffix(strings.ToLower(filePath), ".flac") {

		originalM4aPath := filepath.Join(originalFileDir, originalFileBase+".m4a")
		if _, err := os.Stat(originalM4aPath); err == nil {
			if err := os.Remove(originalM4aPath); err != nil {
				slog.Warn("[Amazon] Failed to remove M4A file", "err", err)
			} else {
				slog.Debug("[Amazon] Cleaned up original M4A file", "path", filepath.Base(originalM4aPath))
			}
		}
	}

	// Checked after the M4A cleanup above (which must run regardless) so a
	// failed embed still fails the job — matching Qobuz's existing
	// behavior — instead of silently leaving an untagged file that
	// ExecuteDownload's caller-side cleanup never gets a chance to catch.
	if embedErr != nil {
		return "", fmt.Errorf("failed to embed metadata: %w", embedErr)
	}

	slog.Debug("[Amazon] Done")
	slog.Debug("[Amazon] Downloaded successfully")
	return filePath, nil
}

func (a *AmazonDownloader) DownloadBySpotifyID(p DownloadParams) (string, error) {
	amazonURL, err := a.GetAmazonURLFromSpotify(p.SpotifyTrackID)
	if err != nil {
		return "", err
	}
	p.URL = amazonURL
	return a.DownloadByURL(p)
}
