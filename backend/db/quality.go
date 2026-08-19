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
//
// Three of these are written; two are not, and that is a statement about the
// design rather than an omission. Checked 2026-08-16: no code writes or reads
// "moved" or "corrupt", and the schema comment in migration 0002 lists all
// five, which reads as if a row might carry any of them.
//
// They are kept, and named here, because deleting them would delete the
// question. What the reader needs to know is not that they are unused but WHY
// nothing produces them:
//
//   - moved    — the rebuild does detect a file that turned up at a new path
//                (api_admin.go, counted as result.Moved), and answers with
//                UpdateLibraryFilePath, which writes the new path AND
//                status="present" in the same statement. The row follows the
//                file; there is no interval during which it is "a moved
//                thing". A separate state would have to be entered and left by
//                the same UPDATE.
//   - corrupt  — integrity IS checked, but before the file ever reaches the
//                library: the engine's shim rejects a download whose bytes are
//                not the audio the name claims (_unplayable, added after a
//                Deezer stream died mid-transfer on 2026-08-04 and left its
//                partial response under a .flac name), and
//                backend/providerutil/atomicwrite.go stops a partial write
//                landing at the final path at all. A corrupt file therefore
//                never gets a catalog row to mark.
//
// The state that would need this is the one nothing does today: verifying the
// integrity of files ALREADY in the library, long after download. If that is
// ever built, "corrupt" is the state it should write, and this comment is the
// note saying nothing else already owns it.
const (
	StatusPresent = "present" // file exists on disk, last verified recently
	StatusMissing = "missing" // expected file not found at file_path
	StatusMoved   = "moved"   // NOT WRITTEN — see above; the rebuild updates file_path instead
	StatusCorrupt = "corrupt" // NOT WRITTEN — see above; corruption is caught before ingestion
	StatusDeleted = "deleted" // historical row, replaced by a newer/better one, or deliberately removed
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
