package meta

import "sync"

// tagWriteLocks serializes concurrent tag-write operations against the
// same file path. Without this, a normal download's metadata embed
// (EmbedMetadata / EmbedMetadataToConvertedFile) and an admin
// retag-legacy run (WriteSpotifyIDTag) — or two admin runs, or a
// lyrics-only re-embed — can independently parse the same file, each
// modify their own in-memory copy, and write back in an unpredictable
// order: the atomic write itself (saveFlacAtomic, id3v2's own temp+rename
// Save, ffmpeg's temp+rename) prevents a torn/corrupted file, but not a
// lost update, where whichever write lands last silently discards the
// other's changes.
//
// Locks are per-path and never released from the map (a live *sync.Mutex
// per path touched over the process lifetime) — acceptable here since tag
// writes happen at most a few times per downloaded track, so the map never
// grows anywhere near a size where that matters.
var tagWriteLocks sync.Map // path string -> *sync.Mutex

// lockTagWrite acquires the per-path lock for path and returns a function
// that releases it. Callers must defer the returned function.
//
// Reentrancy note: this is NOT safe to call twice for the same path on the
// same call stack (sync.Mutex isn't reentrant) — that's why EmbedMetadata's
// FLAC logic is factored into the unlocked embedMetadataFlac, called
// directly (bypassing EmbedMetadata's lock) by EmbedMetadataToConvertedFile,
// which holds its own lock for the whole dispatch. Same pattern for
// EmbedLyricsOnly/EmbedLyricsOnlyMP3 under EmbedLyricsOnlyUniversal, and
// writeSpotifyIDToFlac/writeSpotifyIDToMp3 under WriteSpotifyIDTag — those
// inner functions are unexported and only ever called from their already-
// locked dispatcher, never independently.
func lockTagWrite(path string) func() {
	v, _ := tagWriteLocks.LoadOrStore(path, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
