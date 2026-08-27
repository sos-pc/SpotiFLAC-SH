package providerutil

import (
	"fmt"
	"net/http"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// GetToFile performs a plain GET (with the shared Chrome User-Agent), checks
// for a 200, and streams the body atomically into dstPath via
// DownloadToFileAtomic. It collapses the request/UA/status boilerplate that
// each provider's file-download step repeated verbatim (R6). speedCallback
// may be nil.
//
// Providers that don't 200-check their download response (currently Amazon)
// deliberately don't use this, so adopting it can't silently change their
// behavior.
func GetToFile(
	client *http.Client,
	url, dstPath string,
	speedCallback func(mbDownloaded, speedMBps float64),
) (int64, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", util.ChromeUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	return DownloadToFileAtomic(dstPath, resp.Body, speedCallback)
}
