package db

// Quality tier names used in library_files.quality.
//
// These match the strings produced by the existing downloaders
// (backend/tidal, backend/qobuz, backend/amazon, backend/deezer) so the
// catalog stores values the rest of the codebase already manipulates.
const (
	QualityHigh          = "HIGH"            // lossy high-bitrate (e.g. Spotify, fallback)
	QualityLossless      = "LOSSLESS"        // 16-bit FLAC
	QualityHiRes         = "HI_RES"          // 24-bit FLAC <96 kHz
	QualityHiResLossless = "HI_RES_LOSSLESS" // 24-bit FLAC ≥96 kHz
)

// Status values used in library_files.status.
const (
	StatusPresent = "present" // file exists on disk, last verified recently
	StatusMissing = "missing" // expected file not found at file_path
	StatusMoved   = "moved"   // file content found elsewhere via SPOTIFY_ID tag
	StatusCorrupt = "corrupt" // file fails integrity check
	StatusDeleted = "deleted" // historical row, replaced by a newer/better one
)

// QualityRank ranks the four supported quality tiers for "keep best" logic.
// Higher value = better quality. Unknown tiers return 0 so any known tier
// beats them.
//
// We compare on rank only — format (flac vs m4a) is not part of the order.
// The user opts out of any specific provider/format by configuring downloader
// or autoOrder, not by relying on the catalog's preference.
func QualityRank(quality string) int {
	switch quality {
	case QualityHiResLossless:
		return 4
	case QualityHiRes:
		return 3
	case QualityLossless:
		return 2
	case QualityHigh:
		return 1
	}
	return 0
}
