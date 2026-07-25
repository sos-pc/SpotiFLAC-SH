// Package engine is the Go app's client to the download engine's HTTP shim
// (the forked SpotiFLAC module, run as a sidecar — see engine/shim.py and
// docs/module-engine-runbook.md).
//
// From the Go app's point of view the engine "downloads": we POST a Spotify URL
// and a service priority, the engine resolves + fetches the FLAC into a per-job
// dir under the shared /staging volume, and we get back the file path to ingest
// (re-tag with SPOTIFY_ID, catalog, move into the library).
//
// The contract here is engine-AGNOSTIC on purpose: nothing names the underlying
// engine, so swapping the fork for another downloader is a shim change only
// (docs/module-version-integration.md §6).
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client talks to the engine shim over the internal compose network.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient targets the shim base URL (e.g. http://spotiflac-engine:8080).
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		// Downloads can be long (hi-res files, multi-route fallback); no short timeout.
		http: &http.Client{Timeout: 10 * time.Minute},
	}
}

type downloadRequest struct {
	SpotifyURL    string   `json:"spotify_url"`
	Services      []string `json:"services"`
	Quality       string   `json:"quality"`
	OutDir        string   `json:"out_dir"`
	AllowFallback bool     `json:"allow_fallback"`
}

type downloadResponse struct {
	Status string `json:"status"`
	File   string `json:"file"`
	Error  string `json:"error"`
	Log    string `json:"log"`
}

// Result is what the worker needs after a delegated download.
type Result struct {
	File string // absolute path in the shared /staging volume
	Log  string // engine output, forward into serverLogs (Debug Logs)
}

// Download asks the engine to fetch spotifyURL into outDir using the given service
// priority. For the anonymous path, services must lead with real-FLAC sources
// (qobuz/deezer/amazon) — never tidal-first, which is previews-only without a
// token. Tidal is handled by the Go BYOT path before the engine is ever called.
func (c *Client) Download(ctx context.Context, spotifyURL string, services []string, quality, outDir string, allowFallback bool) (*Result, error) {
	payload, err := json.Marshal(downloadRequest{
		SpotifyURL:    spotifyURL,
		Services:      services,
		Quality:       quality,
		OutDir:        outDir,
		AllowFallback: allowFallback,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/download", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("engine unreachable: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("engine HTTP %d: %s", resp.StatusCode, truncate(raw, 256))
	}

	var dr downloadResponse
	if err := json.Unmarshal(raw, &dr); err != nil {
		return nil, fmt.Errorf("engine bad response: %w", err)
	}
	if dr.Status != "ok" {
		// All routes exhausted / no match / provider down. The log carries detail.
		return nil, fmt.Errorf("engine download failed: %s", dr.Error)
	}
	if dr.File == "" {
		return nil, fmt.Errorf("engine reported ok but returned no file path")
	}
	slog.Debug("[Engine] download ok", "file", dr.File)
	return &Result{File: dr.File, Log: dr.Log}, nil
}

// Health probes the engine's /health (for api_status.go).
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("engine health HTTP %d", resp.StatusCode)
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
