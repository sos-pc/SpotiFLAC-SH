package amazon

//nolint:unused // called from client.go cross-file

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/community"
	"github.com/afkarxyz/SpotiFLAC/backend/providerutil"
)

// amazonCommunityResponse is the JSON shape returned by the community Amazon
// download endpoint (POST /api/dl).
type amazonCommunityResponse struct {
	ASIN      string   `json:"asin"`
	Codec     string   `json:"codec"`
	BitDepth  int      `json:"bit_depth"`
	StreamURL string   `json:"stream_url"`
	Key       string   `json:"key"`
	KeySpecs  []string `json:"key_specs"`
	Captcha   string   `json:"captcha"`
}

// downloadFromCommunity fetches keys from the signed community proxy,
// downloads the encrypted stream, decrypts with mp4ff, and remuxes.
func (a *AmazonDownloader) downloadFromCommunity(amazonURL, outputDir, quality string) (string, error) {
	asin := extractASIN(amazonURL)
	if asin == "" {
		return "", fmt.Errorf("amazon: failed to extract ASIN from URL: %s", amazonURL)
	}

	signer, err := community.SignerFromStore()
	if err != nil {
		return "", fmt.Errorf("amazon community: %w", err)
	}
	endpoint, err := community.AmazonDownloadURL()
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]string{
		"id":      asin,
		"quality": normalizeAmazonQuality(quality),
		"country": "US",
	})
	if err != nil {
		return "", err
	}

	slog.Debug("[Amazon] Fetching from community API", "asin", asin, "quality", quality)
	resp, err := community.Do(a.client, "Amazon", signer, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		return "", fmt.Errorf("amazon community API: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		preview := bodyBytes
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return "", fmt.Errorf("amazon community API returned HTTP %d: %s",
			resp.StatusCode, string(preview))
	}

	var apiResp amazonCommunityResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return "", fmt.Errorf("amazon: decode community response: %w", err)
	}

	streamURL := strings.TrimSpace(apiResp.StreamURL)
	if streamURL == "" {
		return "", fmt.Errorf("amazon: no stream URL in community response")
	}

	keySpecs := apiResp.KeySpecs
	if len(keySpecs) == 0 {
		if k := strings.TrimSpace(apiResp.Key); k != "" {
			keySpecs = []string{k}
		}
	}

	// Download encrypted stream.
	encPath := filepath.Join(outputDir, fmt.Sprintf("%s.encrypted.mp4", asin))
	f, err := os.Create(encPath)
	if err != nil {
		return "", err
	}
	dlOK := false
	defer func() {
		f.Close()
		if !dlOK {
			os.Remove(encPath)
		}
	}()

	dlReq, err := http.NewRequest(http.MethodGet, streamURL, nil)
	if err != nil {
		return "", err
	}
	dlReq.Header.Set("User-Agent", providerutil.ChromeUserAgent)
	if captcha := strings.TrimSpace(apiResp.Captcha); captcha != "" {
		dlReq.Header.Set("x-captcha-token", captcha)
	}

	dlResp, err := a.client.Do(dlReq)
	if err != nil {
		return "", err
	}
	defer dlResp.Body.Close()

	slog.Debug("[Amazon] Downloading encrypted stream", "asin", asin)
	written, err := providerutil.DownloadToFileAtomic(encPath, dlResp.Body, a.SpeedCallback)
	if err != nil {
		return "", err
	}
	slog.Debug("[Amazon] Encrypted stream downloaded", "mb", float64(written)/(1024*1024))

	// Decrypt.
	decPath := filepath.Join(outputDir, fmt.Sprintf("%s.decrypted.mp4", asin))
	if len(keySpecs) > 0 {
		slog.Debug("[Amazon] Decrypting with mp4ff", "keys", len(keySpecs))
		if err := decryptWithMP4FF(keySpecs, encPath, decPath); err != nil {
			return "", fmt.Errorf("amazon: mp4ff decryption: %w", err)
		}
		defer os.Remove(decPath)
		slog.Debug("[Amazon] Decryption successful")
	} else {
		decPath = encPath
	}

	// Remux.
	targetExt := targetExtForCodec(apiResp.Codec, quality)
	finalPath := filepath.Join(outputDir, asin+targetExt)
	slog.Debug("[Amazon] Remuxing", "ext", targetExt)
	if err := remuxWithFFmpeg(decPath, finalPath, targetExt); err != nil {
		return "", fmt.Errorf("amazon: remux: %w", err)
	}

	if info, err := os.Stat(finalPath); err != nil || info.Size() == 0 {
		return "", fmt.Errorf("amazon: remuxed file missing or empty")
	}

	dlOK = true
	return finalPath, nil
}

// extractASIN pulls the Amazon Standard Identification Number from a URL.
//
//nolint:unused // called cross-file, golangci-lint v2 false positive
func extractASIN(url string) string {
	re := regexp.MustCompile(`(B[0-9A-Z]{9})`)
	return re.FindString(url)
}
