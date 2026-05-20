package meta

import (
	"fmt"
	"io/fs"
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
