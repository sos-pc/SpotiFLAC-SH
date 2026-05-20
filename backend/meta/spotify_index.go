package meta

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
	id3v2 "github.com/bogem/id3v2/v2"
	"github.com/go-flac/flacvorbis"
	"github.com/go-flac/go-flac"
)

// supportedAudioExtensions is the set of audio file extensions scanned by
// BuildSpotifyIDIndex.
var supportedAudioExtensions = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  true,
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
			fmt.Printf("[SpotifyIndex] skip %s: %v\n", path, walkErr)
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
	f, err := flac.ParseFile(path)
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



// WriteSpotifyIDTag writes the SPOTIFY_ID tag to an existing audio file
// without touching any other metadata. Used to retro-fit legacy files
// downloaded before the tag was systematically embedded.
//
// Returns true if the tag was written, false if it was already present
// with the same value (no-op).
func WriteSpotifyIDTag(path, spotifyID string) (bool, error) {
	if spotifyID == "" {
		return false, fmt.Errorf("spotifyID is required")
	}
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
	f, err := flac.ParseFile(path)
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
	if err := f.Save(path); err != nil {
		return fmt.Errorf("save FLAC: %w", err)
	}
	return nil
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

	cmd := exec.Command(ffmpegPath,
		"-i", path,
		"-map", "0",
		"-codec", "copy",
		"-metadata", SpotifyIDTagKey+"="+spotifyID,
		"-f", "ipod",
		"-y",
		tmpOut,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg retag: %s - %w", string(output), err)
	}
	if err := os.Rename(tmpOut, path); err != nil {
		return fmt.Errorf("rename retagged file: %w", err)
	}
	return nil
}
