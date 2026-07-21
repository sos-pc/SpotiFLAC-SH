package qobuz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/community"
)

// mapQualityToCommunity collapses our quality codes to the two the community
// download endpoint understands: 24-bit for hi-res tiers, 16-bit otherwise.
// Same mapping as upstream's mapQobuzQualityToCommunity.
func mapQualityToCommunity(quality string) string {
	switch strings.TrimSpace(quality) {
	case "27", "7":
		return "24"
	default:
		return "16"
	}
}

// getCommunityDownloadURL resolves a streaming URL through the shared community
// service, which requires a signed, session-authenticated request.
//
// It returns a plain error (not community's typed AuthError/CooldownError) so
// GetDownloadURL can treat "no session yet" the same as any other provider
// miss and fall through — but the underlying cause is preserved with %w so a
// caller that cares can still inspect it via community.IsAuth / IsCooldown.
func (q *QobuzDownloader) getCommunityDownloadURL(trackID int64, quality string) (string, error) {
	signer, err := community.SignerFromStore()
	if err != nil {
		return "", fmt.Errorf("qobuz community: %w", err)
	}
	endpoint, err := community.QobuzDownloadURL()
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]string{
		"id":      fmt.Sprintf("%d", trackID),
		"quality": mapQualityToCommunity(quality),
	})
	if err != nil {
		return "", err
	}

	resp, err := community.Do(q.client, "Qobuz", signer, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qobuz community API returned status %d", resp.StatusCode)
	}

	url := extractStreamingURL(body)
	if url == "" {
		return "", fmt.Errorf("qobuz community response carried no streamable URL")
	}
	return url, nil
}

// extractStreamingURL pulls the download URL out of a community response,
// tolerating the shapes the service has been seen to use: a URL at the top
// level or nested under "data", named either "url" or "download_url".
func extractStreamingURL(body []byte) string {
	if strings.TrimSpace(string(body)) == "" {
		return ""
	}
	var parsed struct {
		URL         string `json:"url"`
		DownloadURL string `json:"download_url"`
		Data        struct {
			URL         string `json:"url"`
			DownloadURL string `json:"download_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	for _, candidate := range []string{
		parsed.DownloadURL, parsed.URL, parsed.Data.DownloadURL, parsed.Data.URL,
	} {
		if u := strings.TrimSpace(candidate); u != "" {
			return u
		}
	}
	return ""
}
