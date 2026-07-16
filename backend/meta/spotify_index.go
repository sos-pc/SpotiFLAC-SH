package meta

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
	id3v2 "github.com/bogem/id3v2/v2"
	"github.com/go-flac/flacvorbis"
	"github.com/go-flac/go-flac"
)

// flacMagic is the 4-byte signature at the start of every valid FLAC stream.
const flacMagic = "fLaC"

// hasFlacMagic returns true if the file's first 4 bytes equal "fLaC". Used to
// short-circuit go-flac.ParseFile, which panics with index-out-of-range when
// the buffer is shorter than 4 bytes (truncated, empty, or mis-named files).
// See go-flac@v1.0.0 util.go:38 readFLACStream.
func hasFlacMagic(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var buf [4]byte
	n, _ := io.ReadFull(f, buf[:])
	if n < 4 {
		return false, nil
	}
	return string(buf[:]) == flacMagic, nil
}

// safeParseFlac wraps go-flac.ParseFile in a panic recovery so a malformed
// stream that survives the magic-byte check (e.g. truncated mid-header) still
// turns into a regular error instead of crashing the goroutine.
func safeParseFlac(path string) (file *flac.File, err error) {
	defer func() {
		if r := recover(); r != nil {
			file = nil
			err = fmt.Errorf("malformed FLAC stream (recovered panic): %v", r)
		}
	}()
	return flac.ParseFile(path)
}

// saveFlacAtomic writes f's marshaled bytes to a temp file next to path,
// then renames it into place. go-flac's own File.Save does
// os.WriteFile(path, ...) directly, which opens with O_TRUNC — it
// truncates the existing file before writing the new bytes, so a crash,
// OOM-kill, disk-full, or concurrent writer to the same path mid-write
// leaves a truncated/corrupted FLAC behind, not just an untagged one. Every
// FLAC save in this package (initial metadata embed, lyrics-only update,
// SPOTIFY_ID retag) goes through this instead, matching the atomic
// temp-file+rename pattern already used for MP3 (id3v2.Tag.Save) and M4A
// (ffmpeg-based tagging writes to a temp file and renames over the
// original).
func saveFlacAtomic(f *flac.File, path string) error {
	tmp := path + ".tagtmp"
	if err := os.WriteFile(tmp, f.Marshal(), 0644); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// supportedAudioExtensions is the set of audio file extensions scanned by
// BuildSpotifyIDIndex.
var supportedAudioExtensions = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  true,
}

// IsSupportedAudioExt reports whether ext (with leading dot, any case) is
// one of the audio formats this package can read SPOTIFY_ID tags from.
// Exposed so callers walking the filesystem (e.g. library-rebuild) can
// filter consistently with BuildSpotifyIDIndex.
func IsSupportedAudioExt(ext string) bool {
	return supportedAudioExtensions[strings.ToLower(ext)]
}

// ReadSpotifyID returns the SPOTIFY_ID tag value of the audio file at
// path, or empty string if the tag is absent or the extension is not a
// supported audio format. The format dispatch matches BuildSpotifyIDIndex
// (native readers for FLAC/MP3, ffprobe for M4A).
func ReadSpotifyID(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if !supportedAudioExtensions[ext] {
		return "", nil
	}
	return readSpotifyIDFromFile(path, ext)
}

// BuildSpotifyIDIndex walks rootDir recursively, reads the SPOTIFY_ID tag from
// every supported audio file it finds, and returns a map from Spotify track ID
// to absolute file path. Files without the tag are silently skipped.
//
// This is the source of truth for M3U8 generation: it answers "for this
// SpotifyID, where is the file on disk?" without depending on BoltDB job state.
//
// If two files share the same SPOTIFY_ID, the path encountered last during
// the walk wins. Walk errors on individual entries are logged then skipped so
// one unreadable file never aborts the whole index.
func BuildSpotifyIDIndex(rootDir string) (map[string]string, error) {
	index := make(map[string]string)
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("[SpotifyIndex] Skipping path", "path", path, "err", walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !supportedAudioExtensions[ext] {
			return nil
		}
		spotifyID, readErr := readSpotifyIDFromFile(path, ext)
		if readErr != nil || spotifyID == "" {
			return nil
		}
		index[spotifyID] = path
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", rootDir, err)
	}
	return index, nil
}

// readSpotifyIDFromFile returns the SPOTIFY_ID tag value, or empty string if
// the tag is absent. The native FLAC and MP3 readers avoid forking ffprobe
// for the dominant case; M4A falls back to ffprobe (no native reader here).
func readSpotifyIDFromFile(path, ext string) (string, error) {
	switch ext {
	case ".flac":
		return readSpotifyIDFromFlac(path)
	case ".mp3":
		return readSpotifyIDFromMp3(path)
	case ".m4a":
		return readSpotifyIDFromFFprobe(path)
	}
	return "", nil
}

func readSpotifyIDFromFlac(path string) (string, error) {
	ok, err := hasFlacMagic(path)
	if err != nil {
		return "", err
	}
	if !ok {
		// Mis-named or truncated file — silently skip in the index walk.
		return "", nil
	}
	f, err := safeParseFlac(path)
	if err != nil {
		return "", err
	}
	for _, block := range f.Meta {
		if block.Type != flac.VorbisComment {
			continue
		}
		cmt, err := flacvorbis.ParseFromMetaDataBlock(*block)
		if err != nil {
			continue
		}
		for _, comment := range cmt.Comments {
			parts := strings.SplitN(comment, "=", 2)
			if len(parts) != 2 {
				continue
			}
			if strings.EqualFold(parts[0], SpotifyIDTagKey) {
				return parts[1], nil
			}
		}
	}
	return "", nil
}

func readSpotifyIDFromMp3(path string) (string, error) {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return "", err
	}
	defer tag.Close()
	for _, frame := range tag.GetFrames("TXXX") {
		txxx, ok := frame.(id3v2.UserDefinedTextFrame)
		if !ok {
			continue
		}
		if strings.EqualFold(txxx.Description, SpotifyIDTagKey) {
			return txxx.Value, nil
		}
	}
	return "", nil
}

func readSpotifyIDFromFFprobe(path string) (string, error) {
	tags, err := util.ReadFFprobeTags(path)
	if err != nil {
		return "", err
	}
	return tags[strings.ToLower(SpotifyIDTagKey)], nil
}

// FullTrackTags is every tag SpotiFLAC embeds into a downloaded file,
// read back in one pass. Used to backfill the SQLite catalog — both right
// after a download (jobs_catalog.go) and when a filesystem scan discovers
// a file with no live Job at all (library-rebuild/repair). Every field is
// best-effort: empty/zero if absent, unreadable, or (Genre on MP3/M4A
// files written before genre embedding covered those formats) simply
// never written in the first place.
type FullTrackTags struct {
	SpotifyID   string
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	ReleaseDate string
	TrackNumber int
	DiscNumber  int
	ISRC        string
	Genre       string
	Copyright   string
}

// ReadFullTrackTags reads every tag in FullTrackTags from the file at path
// in a single parse pass. Deliberately one pass, not "call ReadSpotifyID
// then read the rest separately": for FLAC in particular, both would
// re-run hasFlacMagic + a full safeParseFlac, doubling the I/O/CPU cost of
// a library-rebuild scan across every file in the library — the exact
// scan that was already found to be too slow for a multi-thousand-track
// collection.
func ReadFullTrackTags(path string) FullTrackTags {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		return readFullTrackTagsFromFlac(path)
	case ".mp3":
		return readFullTrackTagsFromMp3(path)
	case ".m4a":
		return readFullTrackTagsFromFFprobe(path)
	}
	return FullTrackTags{}
}

func readFullTrackTagsFromFlac(path string) FullTrackTags {
	var out FullTrackTags
	ok, err := hasFlacMagic(path)
	if err != nil || !ok {
		return out
	}
	f, err := safeParseFlac(path)
	if err != nil {
		return out
	}
	for _, block := range f.Meta {
		if block.Type != flac.VorbisComment {
			continue
		}
		cmt, err := flacvorbis.ParseFromMetaDataBlock(*block)
		if err != nil {
			continue
		}
		for _, comment := range cmt.Comments {
			parts := strings.SplitN(comment, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key, val := parts[0], parts[1]
			switch {
			case strings.EqualFold(key, SpotifyIDTagKey):
				out.SpotifyID = val
			case strings.EqualFold(key, "TITLE"):
				out.Title = val
			case strings.EqualFold(key, "ARTIST"):
				out.Artist = val
			case strings.EqualFold(key, "ALBUM"):
				out.Album = val
			case strings.EqualFold(key, "ALBUMARTIST"):
				out.AlbumArtist = val
			case strings.EqualFold(key, "DATE"):
				out.ReleaseDate = val
			case strings.EqualFold(key, "TRACKNUMBER"):
				out.TrackNumber = parseLeadingInt(val)
			case strings.EqualFold(key, "DISCNUMBER"):
				out.DiscNumber = parseLeadingInt(val)
			case strings.EqualFold(key, "ISRC"):
				out.ISRC = val
			case strings.EqualFold(key, "GENRE"):
				out.Genre = val
			case strings.EqualFold(key, "COPYRIGHT"):
				out.Copyright = val
			}
		}
	}
	return out
}

func readFullTrackTagsFromMp3(path string) FullTrackTags {
	var out FullTrackTags
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return out
	}
	defer tag.Close()
	for _, frame := range tag.GetFrames("TXXX") {
		txxx, ok := frame.(id3v2.UserDefinedTextFrame)
		if ok && strings.EqualFold(txxx.Description, SpotifyIDTagKey) {
			out.SpotifyID = txxx.Value
			break
		}
	}
	out.Title = tag.Title()
	out.Artist = tag.Artist()
	out.Album = tag.Album()
	out.AlbumArtist = tag.GetTextFrame("TPE2").Text
	out.ReleaseDate = tag.Year()
	out.TrackNumber = parseLeadingInt(tag.GetTextFrame(tag.CommonID("Track number/Position in set")).Text)
	out.DiscNumber = parseLeadingInt(tag.GetTextFrame(tag.CommonID("Part of a set")).Text)
	out.ISRC = tag.GetTextFrame("TSRC").Text
	out.Genre = tag.GetTextFrame("TCON").Text
	out.Copyright = tag.GetTextFrame("TCOP").Text
	return out
}

func readFullTrackTagsFromFFprobe(path string) FullTrackTags {
	tags, err := util.ReadFFprobeTags(path)
	if err != nil {
		return FullTrackTags{}
	}
	return FullTrackTags{
		SpotifyID:   tags[strings.ToLower(SpotifyIDTagKey)],
		Title:       tags["title"],
		Artist:      tags["artist"],
		Album:       tags["album"],
		AlbumArtist: tags["album_artist"],
		ReleaseDate: tags["date"],
		TrackNumber: parseLeadingInt(tags["track"]),
		DiscNumber:  parseLeadingInt(tags["disk"]),
		ISRC:        tags["isrc"],
		Genre:       tags["genre"],
		Copyright:   tags["copyright"],
	}
}

// parseLeadingInt parses the leading integer of s, tolerating the "N/total"
// format used for track/disc numbers on MP3/M4A (e.g. "3/12" -> 3) and
// returning 0 for anything unparseable rather than erroring — every
// caller here is a best-effort tag read.
func parseLeadingInt(s string) int {
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}



// WriteSpotifyIDTag writes the SPOTIFY_ID tag to an existing audio file
// without touching any other metadata. Used to retro-fit legacy files
// downloaded before the tag was systematically embedded.
//
// Returns true if the tag was written, false if it was already present
// with the same value (no-op).
//
// Locked per-path (see tagWriteLocks) so a retag-legacy run can't race
// against a worker's concurrent metadata embed for the same file — both
// read-modify-write the same tag block, and without this a stale read on
// either side could silently discard the other's changes on write.
func WriteSpotifyIDTag(path, spotifyID string) (bool, error) {
	if spotifyID == "" {
		return false, fmt.Errorf("spotifyID is required")
	}
	unlock := lockTagWrite(path)
	defer unlock()
	ext := strings.ToLower(filepath.Ext(path))
	existing, _ := readSpotifyIDFromFile(path, ext)
	if existing == spotifyID {
		return false, nil
	}
	switch ext {
	case ".flac":
		return true, writeSpotifyIDToFlac(path, spotifyID)
	case ".mp3":
		return true, writeSpotifyIDToMp3(path, spotifyID)
	case ".m4a":
		return true, writeSpotifyIDToM4A(path, spotifyID)
	}
	return false, fmt.Errorf("unsupported file extension: %s", ext)
}

func writeSpotifyIDToFlac(path, spotifyID string) error {
	ok, err := hasFlacMagic(path)
	if err != nil {
		return fmt.Errorf("read FLAC header: %w", err)
	}
	if !ok {
		// Loud failure on the write path: the caller asked us to tag a file
		// that isn't actually FLAC — surfacing this lets the retag-legacy
		// handler record it in failed_ids instead of crashing.
		return fmt.Errorf("not a valid FLAC file (missing %q magic): %s", flacMagic, path)
	}
	f, err := safeParseFlac(path)
	if err != nil {
		return fmt.Errorf("parse FLAC: %w", err)
	}

	cmtIdx := -1
	var cmt *flacvorbis.MetaDataBlockVorbisComment
	for idx, block := range f.Meta {
		if block.Type == flac.VorbisComment {
			cmtIdx = idx
			cmt, _ = flacvorbis.ParseFromMetaDataBlock(*block)
			break
		}
	}
	if cmt == nil {
		cmt = flacvorbis.New()
	}

	// Drop any existing SPOTIFY_ID entries (case-insensitive) before re-adding.
	filtered := make([]string, 0, len(cmt.Comments))
	for _, comment := range cmt.Comments {
		parts := strings.SplitN(comment, "=", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], SpotifyIDTagKey) {
			continue
		}
		filtered = append(filtered, comment)
	}
	cmt.Comments = filtered
	if err := cmt.Add(SpotifyIDTagKey, spotifyID); err != nil {
		return fmt.Errorf("add tag: %w", err)
	}

	block := cmt.Marshal()
	if cmtIdx < 0 {
		f.Meta = append(f.Meta, &block)
	} else {
		f.Meta[cmtIdx] = &block
	}
	if err := saveFlacAtomic(f, path); err != nil {
		return fmt.Errorf("save FLAC: %w", err)
	}
	return nil
}

// WriteMissingTags fills in FLAC vorbis comments that are currently empty
// (per current, normally a fresh ReadFullTrackTags(path) call) using fresh
// (values just fetched from Spotify/Deezer/MusicBrainz) — mirrors
// WriteSpotifyIDTag's surgical approach, extended to every field
// retag-incomplete-metadata knows how to backfill. A tag already present in
// the file is never touched, even if fresh disagrees with it.
//
// FLAC only: the rest of retag-incomplete-metadata's target library is
// exclusively FLAC in practice, and extending the same surgical merge to
// MP3 (ID3 TXXX/standard frames) and M4A (custom atoms) is straightforward
// but separate work — this errors instead of silently doing nothing for
// those formats.
//
// Returns false if every field fresh could offer was already present in
// the file (no write performed).
func WriteMissingTags(path string, current, fresh FullTrackTags) (bool, error) {
	if strings.ToLower(filepath.Ext(path)) != ".flac" {
		return false, fmt.Errorf("WriteMissingTags: unsupported extension %s (FLAC only)", filepath.Ext(path))
	}

	unlock := lockTagWrite(path)
	defer unlock()

	ok, err := hasFlacMagic(path)
	if err != nil {
		return false, fmt.Errorf("read FLAC header: %w", err)
	}
	if !ok {
		return false, fmt.Errorf("not a valid FLAC file (missing %q magic): %s", flacMagic, path)
	}
	f, err := safeParseFlac(path)
	if err != nil {
		return false, fmt.Errorf("parse FLAC: %w", err)
	}

	cmtIdx := -1
	var cmt *flacvorbis.MetaDataBlockVorbisComment
	for idx, block := range f.Meta {
		if block.Type == flac.VorbisComment {
			cmtIdx = idx
			cmt, _ = flacvorbis.ParseFromMetaDataBlock(*block)
			break
		}
	}
	if cmt == nil {
		cmt = flacvorbis.New()
	}

	type fillCandidate struct {
		key      string
		curVal   string
		freshVal string
	}
	candidates := []fillCandidate{
		{"ISRC", current.ISRC, fresh.ISRC},
		{"TITLE", current.Title, fresh.Title},
		{"ARTIST", current.Artist, fresh.Artist},
		{"ALBUM", current.Album, fresh.Album},
		{"ALBUMARTIST", current.AlbumArtist, fresh.AlbumArtist},
		{"DATE", current.ReleaseDate, fresh.ReleaseDate},
		{"GENRE", current.Genre, fresh.Genre},
		{"COPYRIGHT", current.Copyright, fresh.Copyright},
	}
	if current.TrackNumber == 0 && fresh.TrackNumber > 0 {
		candidates = append(candidates, fillCandidate{"TRACKNUMBER", "", strconv.Itoa(fresh.TrackNumber)})
	}
	if current.DiscNumber == 0 && fresh.DiscNumber > 0 {
		candidates = append(candidates, fillCandidate{"DISCNUMBER", "", strconv.Itoa(fresh.DiscNumber)})
	}

	changed := false
	for _, c := range candidates {
		if c.curVal != "" || c.freshVal == "" {
			continue // already present in the file, or fresh has nothing to offer
		}
		filtered := make([]string, 0, len(cmt.Comments))
		for _, comment := range cmt.Comments {
			parts := strings.SplitN(comment, "=", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], c.key) {
				continue
			}
			filtered = append(filtered, comment)
		}
		cmt.Comments = filtered
		if err := cmt.Add(c.key, c.freshVal); err != nil {
			return changed, fmt.Errorf("add %s tag: %w", c.key, err)
		}
		changed = true
	}
	if !changed {
		return false, nil
	}

	block := cmt.Marshal()
	if cmtIdx < 0 {
		f.Meta = append(f.Meta, &block)
	} else {
		f.Meta[cmtIdx] = &block
	}
	if err := saveFlacAtomic(f, path); err != nil {
		return false, fmt.Errorf("save FLAC: %w", err)
	}
	return true, nil
}

func writeSpotifyIDToMp3(path, spotifyID string) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("open MP3: %w", err)
	}
	defer tag.Close()

	// Remove any pre-existing TXXX:SPOTIFY_ID frames before adding ours.
	kept := []id3v2.Framer{}
	for _, frame := range tag.GetFrames("TXXX") {
		txxx, ok := frame.(id3v2.UserDefinedTextFrame)
		if ok && strings.EqualFold(txxx.Description, SpotifyIDTagKey) {
			continue
		}
		kept = append(kept, frame)
	}
	tag.DeleteFrames("TXXX")
	for _, f := range kept {
		tag.AddFrame("TXXX", f)
	}
	tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
		Encoding:    id3v2.EncodingUTF8,
		Description: SpotifyIDTagKey,
		Value:       spotifyID,
	})
	if err := tag.Save(); err != nil {
		return fmt.Errorf("save MP3: %w", err)
	}
	return nil
}

func writeSpotifyIDToM4A(path, spotifyID string) error {
	ffmpegPath, err := util.GetFFmpegPath()
	if err != nil {
		return fmt.Errorf("ffmpeg not found: %w", err)
	}
	if err := util.ValidateExecutable(ffmpegPath); err != nil {
		return fmt.Errorf("invalid ffmpeg: %w", err)
	}

	tmpOut := strings.TrimSuffix(path, filepath.Ext(path)) + ".retag" + filepath.Ext(path)
	defer func() {
		if _, statErr := os.Stat(tmpOut); statErr == nil {
			_ = os.Remove(tmpOut)
		}
	}()

	// Hardened: path is an existing library file being rewritten in place. See
	// util.FFmpegHardeningArgs.
	args := append(util.FFmpegHardeningArgs(),
		"-i", path,
		"-map", "0",
		"-codec", "copy",
		"-metadata", SpotifyIDTagKey+"="+spotifyID,
		"-f", "ipod",
		"-y",
		tmpOut,
	)
	cmd := exec.Command(ffmpegPath, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg retag: %s - %w", string(output), err)
	}
	if err := os.Rename(tmpOut, path); err != nil {
		return fmt.Errorf("rename retagged file: %w", err)
	}
	return nil
}
