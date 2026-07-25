package backend

// ─────────────────────────────────────────────────────────────────────────────
// Engine route — delegate a provider's download to the sidecar engine, then
// ingest the result into our library on our own terms.
//
// The engine (a fork of the SpotiFLAC module, see engine/shim.py) resolves the
// Spotify track and fetches the audio through its multi-route provider chain.
// It deliberately does NOT tag: naming, tags (incl. SPOTIFY_ID, which M3U8
// regeneration depends on), cover and genre stay ours — applied here at
// ingestion. See docs/module-engine-migration.md Q2.
//
// Opt-in per provider via ENGINE_SERVICES, so this is inert until explicitly
// enabled and a single env var rolls it back.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/engine"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/providerutil"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// EngineBaseURL is where the shim lives on the internal compose network
// (e.g. http://spotiflac-engine:8080). Empty disables the engine entirely.
// Exported so the status probe (api_status.go) reads the engine's location from
// the same place the download path does — one definition of ENGINE_URL, so the
// two can't drift apart.
func EngineBaseURL() string { return strings.TrimSpace(os.Getenv("ENGINE_URL")) }

// engineStagingDir is the shared volume the engine writes into. It must be
// mounted at the SAME path in both containers, since the path the engine
// returns is resolved as-is on our side.
func engineStagingDir() string {
	if d := strings.TrimSpace(os.Getenv("ENGINE_STAGING_DIR")); d != "" {
		return d
	}
	return "/staging"
}

// engineHandles reports whether svc is delegated to the engine.
//
// Opt-in and comma-separated (ENGINE_SERVICES=deezer, then deezer,qobuz, …), so
// providers move over one at a time and only after each is proven in prod —
// the staged cutover in docs/module-engine-runbook.md. Unset = nothing
// delegated, every provider keeps its native Go path.
func engineHandles(svc string) bool {
	if EngineBaseURL() == "" {
		return false
	}
	for _, s := range strings.Split(os.Getenv("ENGINE_SERVICES"), ",") {
		if strings.EqualFold(strings.TrimSpace(s), svc) {
			return true
		}
	}
	return false
}

// downloadViaEngine runs one provider through the engine and ingests the file.
// Returns the final library path, or "EXISTS:<path>" when it's already there —
// same contract as the native downloaders, so runService's callers are unchanged.
func downloadViaEngine(req DownloadRequest, svc, spotifyURL string) (string, error) {
	if req.SpotifyID == "" {
		return "", fmt.Errorf("engine requires a Spotify ID")
	}

	// Name the file the way the rest of the app expects BEFORE fetching, so the
	// already-on-disk check matches ExecuteDownload's own dedup check — same
	// helpers, same arguments.
	trackNumberToPrint := util.ResolveTrackNumber(req.Position, req.SpotifyTrackNumber, req.UseAlbumTrackNumber)
	filename := util.BuildExpectedFilename(
		req.TrackName, req.ArtistName, req.AlbumName, req.AlbumArtist, req.ReleaseDate,
		req.FilenameFormat, req.PlaylistName, req.PlaylistOwner,
		req.TrackNumber, trackNumberToPrint, req.SpotifyDiscNumber,
	)
	finalPath := filepath.Join(req.OutputDir, filename)
	if fi, err := os.Stat(finalPath); err == nil && fi.Size() > 100*1024 {
		return "EXISTS:" + finalPath, nil
	}

	if req.OutputDir != "." {
		if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	// Genre lookup runs alongside the download, as the native providers do.
	// Deliberately NOT reading ExecuteDownload's isrcChan: that channel carries a
	// single value for a single reader (the qobuz case), so a second read here
	// would deadlock the auto chain. req.ISRC or the Spotify URL is enough for
	// the genre lookup.
	metaChan := providerutil.FetchGenreMetadataAsync(
		req.ISRC, spotifyURL, req.TrackName, req.ArtistName, req.AlbumName,
		req.UseSingleGenre, req.EmbedGenre,
	)
	drainGenre := func() meta.Metadata {
		if !req.EmbedGenre {
			return meta.Metadata{}
		}
		return (<-metaChan).Metadata
	}

	client := engine.NewClient(EngineBaseURL())
	res, err := client.Download(context.Background(), spotifyURL, []string{svc}, req.AudioFormat, engineStagingDir(), req.AllowFallback)

	// Hi-res is a request the catalogue often cannot honour: most older material
	// exists only in 16/44.1, and asking a stream endpoint for strict 24-bit on
	// such a track fails outright — observed as a bare HTTP 500, retried six
	// times, on Qobuz track 1930534 (hires=false, maximum_bit_depth=16) even
	// though the track is streamable at CD quality. Retrying once at LOSSLESS
	// turns those from a lost track into a normal download, mirroring the ladder
	// the native Qobuz path already walks (27→7→6). Gated on the user's own
	// AllowFallback so "hi-res or nothing" stays expressible.
	if err != nil && req.AllowFallback && isHiResRequest(req.AudioFormat) {
		slog.Info("[Engine] hi-res failed, retrying at CD quality", "service", svc, "err", err, "track", req.TrackName)
		res, err = client.Download(context.Background(), spotifyURL, []string{svc}, "LOSSLESS", engineStagingDir(), req.AllowFallback)
	}
	if err != nil {
		drainGenre() // never leave the genre goroutine's send blocked
		return "", err
	}
	logEngineOutput(res.Log, svc)

	// The engine wrote into a per-job dir on the shared volume; move it into the
	// library. A plain rename would fail across the two Docker volumes (EXDEV),
	// so stream it through the same atomic writer the native providers use
	// (temp file + rename inside the destination dir).
	defer os.RemoveAll(filepath.Dir(res.File))

	src, err := os.Open(res.File)
	if err != nil {
		drainGenre()
		return "", fmt.Errorf("engine file unreadable at %s: %w", res.File, err)
	}
	written, werr := providerutil.DownloadToFileAtomic(finalPath, src, req.SpeedCallback)
	src.Close()
	if werr != nil {
		drainGenre()
		return "", fmt.Errorf("failed to ingest engine file: %w", werr)
	}
	slog.Debug("[Engine] ingested", "service", svc, "mb", float64(written)/(1024*1024), "path", finalPath)

	// Cover + tags are ours: this is what makes an engine-sourced file
	// indistinguishable from a natively downloaded one in the catalog.
	coverPath := ""
	if req.CoverURL != "" {
		coverPath = finalPath + ".cover.jpg"
		if cerr := meta.NewCoverClient().DownloadCoverToPath(req.CoverURL, coverPath, req.EmbedMaxQualityCover); cerr != nil {
			slog.Warn("[Engine] cover download failed", "err", cerr)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
		}
	}

	mbMeta := drainGenre()

	trackNumberToEmbed := req.SpotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}
	metadata := meta.Metadata{
		Title:       req.TrackName,
		Artist:      req.ArtistName,
		Album:       req.AlbumName,
		AlbumArtist: req.AlbumArtist,
		Date:        req.ReleaseDate,
		TrackNumber: trackNumberToEmbed,
		TotalTracks: req.SpotifyTotalTracks,
		DiscNumber:  req.SpotifyDiscNumber,
		TotalDiscs:  req.SpotifyTotalDiscs,
		URL:         spotifyURL,
		Copyright:   req.Copyright,
		Publisher:   req.Publisher,
		ISRC:        req.ISRC,
		Genre:       mbMeta.Genre,
		SpotifyID:   req.SpotifyID, // what meta.BuildSpotifyIDIndex reads to rebuild M3U8s
	}
	if err := meta.EmbedMetadata(finalPath, metadata, coverPath); err != nil {
		return "", fmt.Errorf("failed to embed metadata: %w", err)
	}

	return finalPath, nil
}

// isHiResRequest reports whether the caller asked for better-than-CD audio, in
// any of the vocabularies AudioFormat carries (the canonical names the UI uses
// and the numeric Qobuz codes that reach us from older call sites).
func isHiResRequest(format string) bool {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "HI_RES_LOSSLESS", "HI_RES", "27", "7":
		return true
	}
	return false
}

// logEngineOutput surfaces the engine's own output in our logs (and therefore in
// the Debug Logs page over SSE), since it runs in a separate container whose
// stdout the UI never sees. Debug level: on failure the error already carries
// the reason, and a successful download's chatter shouldn't flood the feed.
func logEngineOutput(out, svc string) {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			slog.Debug("[Engine] "+line, "service", svc)
		}
	}
}
