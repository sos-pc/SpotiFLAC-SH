package backend

// ─────────────────────────────────────────────────────────────────────────────
// Engine route — delegate a provider's download to the sidecar engine, then
// ingest the result into our library on our own terms.
//
// The engine (a fork of the SpotiFLAC module, see engine/shim.py) resolves the
// Spotify track and fetches the audio through its multi-route provider chain.
// It deliberately does NOT tag: naming, tags (incl. SPOTIFY_ID, which M3U8
// regeneration depends on), cover and genre stay ours — applied here at
// ingestion. See docs/module-engine.md §4.
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

	"github.com/sos-pc/SpotiFLAC-SH/backend/engine"
	"github.com/sos-pc/SpotiFLAC-SH/backend/meta"
	"github.com/sos-pc/SpotiFLAC-SH/backend/providerutil"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// EngineBaseURL is where the shim lives on the internal compose network
// (e.g. http://spotiflac-engine:8080). Empty disables the engine entirely.
// Exported so the status probe (api_status.go) reads the engine's location from
// the same place the download path does — one definition of ENGINE_URL, so the
// two can't drift apart.
func EngineBaseURL() string { return strings.TrimSpace(os.Getenv("ENGINE_URL")) }

// EngineStagingDir is the shared volume the engine writes into. It must be
// mounted at the SAME path in both containers, since the path the engine
// returns is resolved as-is on our side.
func EngineStagingDir() string {
	if d := strings.TrimSpace(os.Getenv("ENGINE_STAGING_DIR")); d != "" {
		return d
	}
	return "/staging"
}

// EngineHandles reports whether svc is delegated to the engine.
//
// Opt-in and comma-separated (ENGINE_SERVICES=deezer, then deezer,qobuz, …), so
// providers move over one at a time and only after each is proven in prod —
// the staged cutover in docs/module-engine.md §3. Unset = nothing
// delegated, every provider keeps its native Go path.
//
// Exported because the job pipeline needs the same answer before deciding
// whether to resolve provider URLs at all (jobs_helpers.go): a delegated
// provider takes the Spotify URL and resolves internally, so paying for that
// lookup would be pure waste. One definition, so the two cannot disagree.
func EngineHandles(svc string) bool {
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
	quality := engineQualityFor(req.AudioFormat)
	res, err := client.Download(context.Background(), spotifyURL, []string{svc}, quality, EngineStagingDir(), req.AllowFallback)

	// Hi-res is a request the catalogue often cannot honour: most older material
	// exists only in 16/44.1, and asking a stream endpoint for strict 24-bit on
	// such a track fails outright — observed as a bare HTTP 500, retried six
	// times, on Qobuz track 1930534 (hires=false, maximum_bit_depth=16) even
	// though the track is streamable at CD quality. Retrying once at LOSSLESS
	// turns those from a lost track into a normal download, mirroring the ladder
	// the native Qobuz path already walks (27→7→6). Gated on the user's own
	// AllowFallback so "hi-res or nothing" stays expressible.
	if err != nil && req.AllowFallback && isHiResRequest(quality) && providerHasQualityTiers(svc) {
		slog.Info("[Engine] hi-res failed, retrying at CD quality", "service", svc, "err", err, "track", req.TrackName)
		res, err = client.Download(context.Background(), spotifyURL, []string{svc}, "LOSSLESS", EngineStagingDir(), req.AllowFallback)
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
		// Delete what we just wrote. Tagging parses the container, so a failure
		// here usually means the payload is not the audio file it claims to be —
		// observed when a Deezer route answered 502 and the engine still produced
		// a ".flac" mutagen refused to open. Returning an error alone would leave
		// that garbage in the library: ExecuteDownload's own cleanup keys off the
		// filename we return, and on the error path we return "".
		if rmErr := os.Remove(finalPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Warn("[Engine] could not remove unusable file", "path", finalPath, "err", rmErr)
		}
		return "", fmt.Errorf("failed to embed metadata (file discarded): %w", err)
	}

	return finalPath, nil
}

// providerHasQualityTiers reports whether asking svc for a different quality can
// change the outcome. Deezer serves one tier ("FLAC Best Available"), so a
// hi-res retry there re-runs the exact same request — pure latency on a download
// that is already failing (observed: a 502 retried at LOSSLESS, failing again).
func providerHasQualityTiers(svc string) bool {
	switch strings.ToLower(strings.TrimSpace(svc)) {
	case "qobuz", "tidal", "amazon":
		return true
	}
	return false
}

// engineQualityFor translates our per-provider quality vocabulary into the
// canonical names the engine works in — the same job TidalQualityFor does for
// the native Tidal path, done once, at the boundary.
//
// req.AudioFormat arrives in whichever dialect its provider speaks: Tidal's
// LOSSLESS/HI_RES_LOSSLESS, Qobuz's numeric format IDs, and the literal "flac"
// that resolveAudioFormat still returns for Deezer — a leftover from the native
// Deezer downloader, which no longer exists.
//
// Read upstream (SpotiFLAC/core/quality.py, 2026-07-28) rather than assumed:
// normalize_quality() maps every input to one of HI_RES_LOSSLESS / HI_RES /
// LOSSLESS / HIGH / LOW / DOLBY_ATMOS. Two things that changes about the plan's
// §7.3 diagnosis —
//
//   - The Qobuz numerics are NOT accidental compatibility. "27", "7", "6", "5"
//     and "4" are listed aliases in that table, twice over (the alias lists and
//     an isdigit() branch below them). They were always going to work.
//   - "flac" IS accidental. It matches no alias, is not a digit, and contains
//     none of the substrings the heuristics look for ("HI", "24", "96", "LOSS",
//     "LOW", "MP3"), so it reaches the function's final `return "LOSSLESS"`.
//     Right answer, reached by falling off the end.
//
// So this exists to stop depending on that table at all: it emits only the
// canonical names, which are the keys of the mapping rather than entries in it.
// Aliases upstream may add, rename or reinterpret; the canonical set is the
// stable part of the contract. No download changes quality as a result — every
// value below normalizes today to exactly what it normalized to before.
func engineQualityFor(format string) string {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "27", "HI_RES_LOSSLESS", "HIRES_LOSSLESS", "HI-RES-LOSSLESS":
		return "HI_RES_LOSSLESS"
	case "7", "HI_RES", "HIRES", "HI-RES":
		return "HI_RES"
	case "5", "HIGH":
		return "HIGH"
	case "4", "LOW":
		return "LOW"
	case "DOLBY_ATMOS", "ATMOS":
		return "DOLBY_ATMOS"
	default:
		// "6", "LOSSLESS", "flac", "" and anything unrecognised. Matching
		// upstream's own default: CD quality is the safe floor, not an error.
		return "LOSSLESS"
	}
}

// isHiResRequest reports whether the caller asked for better-than-CD audio.
// Callers pass the canonical value from engineQualityFor; the numeric Qobuz
// codes stay accepted because req.AudioFormat still reaches other call sites in
// that dialect.
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
