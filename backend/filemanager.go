package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

type FileInfo struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"is_dir"`
	Size     int64      `json:"size"`
	Children []FileInfo `json:"children,omitempty"`
}

// AudioMetadata is what the File Manager shows for a file (GET
// /api/v1/files/metadata).
//
// Genre and ISRC are read here as well as in meta.ReadFullTrackTags, which the
// retag pass uses. The duplication predates this field: the two readers exist
// for different callers (one browses arbitrary paths, one backfills catalogued
// tracks) and they had drifted, so the File Manager could not show the two tags
// most likely to be missing — which is precisely what you want to look at after
// a retag.
type AudioMetadata struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtist string `json:"album_artist"`
	TrackNumber int    `json:"track_number"`
	DiscNumber  int    `json:"disc_number"`
	Year        string `json:"year"`
	Genre       string `json:"genre"`
	ISRC        string `json:"isrc"`
	// Added 2026-07-19. meta.ReadFullTrackTags already read both; the File
	// Manager's own reader simply never exposed them, so every investigation
	// needing a copyright or a Spotify ID meant another field, another release.
	SpotifyID string `json:"spotify_id"`
	Copyright string `json:"copyright"`
}

type RenamePreview struct {
	OldPath  string        `json:"old_path"`
	OldName  string        `json:"old_name"`
	NewName  string        `json:"new_name"`
	NewPath  string        `json:"new_path"`
	Error    string        `json:"error,omitempty"`
	Metadata AudioMetadata `json:"metadata"`
}

type RenameResult struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func ListDirectory(dirPath string) ([]FileInfo, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var result []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fileInfo := FileInfo{
			Name:  entry.Name(),
			Path:  filepath.Join(dirPath, entry.Name()),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		}

		if entry.IsDir() {
			children, err := ListDirectory(fileInfo.Path)
			if err == nil {
				fileInfo.Children = children
			}
		}

		result = append(result, fileInfo)
	}

	return result, nil
}

func ListAudioFiles(dirPath string) ([]FileInfo, error) {
	var result []FileInfo

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".flac" || ext == ".mp3" || ext == ".m4a" {
			result = append(result, FileInfo{
				Name:  info.Name(),
				Path:  path,
				IsDir: false,
				Size:  info.Size(),
			})
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	return result, nil
}

// ReadAudioMetadata reads a file's tags for the File Manager API.
//
// It delegates to meta.ReadFullTrackTags rather than keeping its own per-format
// readers. There used to be TWO independent tag readers with their own
// .flac/.mp3/.m4a dispatch, and they had drifted: the retag one knew SPOTIFY_ID
// and COPYRIGHT, this one knew neither, while this one tolerated more ffprobe
// key spellings on M4A than the retag one did. Converging on the richer reader
// required teaching it those spellings first (see meta.firstTag) — otherwise
// this "cleanup" would have quietly lost M4A data.
func ReadAudioMetadata(filePath string) (*AudioMetadata, error) {
	if !util.FileExists(filePath) {
		return nil, fmt.Errorf("file does not exist")
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".flac", ".mp3", ".m4a":
	default:
		return nil, fmt.Errorf("unsupported file format: %s", ext)
	}

	t := meta.ReadFullTrackTags(filePath)
	return &AudioMetadata{
		Title:       t.Title,
		Artist:      t.Artist,
		Album:       t.Album,
		AlbumArtist: t.AlbumArtist,
		TrackNumber: t.TrackNumber,
		DiscNumber:  t.DiscNumber,
		// FullTrackTags calls it ReleaseDate; the File Manager field has always
		// been "year" and carries whatever the DATE tag holds, so this is a
		// rename, not a truncation.
		Year:      t.ReleaseDate,
		Genre:     t.Genre,
		ISRC:      t.ISRC,
		SpotifyID: t.SpotifyID,
		Copyright: t.Copyright,
	}, nil
}

func GenerateFilename(metadata *AudioMetadata, format string, ext string) string {
	if metadata == nil {
		return ""
	}

	result := format

	year := metadata.Year
	if len(year) >= 4 {
		year = year[:4]
	}

	result = strings.ReplaceAll(result, "{title}", sanitizeFilenameForRename(metadata.Title))
	result = strings.ReplaceAll(result, "{artist}", sanitizeFilenameForRename(metadata.Artist))
	result = strings.ReplaceAll(result, "{album}", sanitizeFilenameForRename(metadata.Album))
	result = strings.ReplaceAll(result, "{album_artist}", sanitizeFilenameForRename(metadata.AlbumArtist))
	result = strings.ReplaceAll(result, "{year}", sanitizeFilenameForRename(year))
	result = strings.ReplaceAll(result, "{date}", sanitizeFilenameForRename(metadata.Year))

	if metadata.TrackNumber > 0 {
		result = strings.ReplaceAll(result, "{track}", fmt.Sprintf("%02d", metadata.TrackNumber))
	} else {
		result = strings.ReplaceAll(result, "{track}", "")
	}

	if metadata.DiscNumber > 0 {
		result = strings.ReplaceAll(result, "{disc}", fmt.Sprintf("%d", metadata.DiscNumber))
	} else {
		result = strings.ReplaceAll(result, "{disc}", "")
	}

	result = strings.TrimSpace(result)
	result = strings.Join(strings.Fields(result), " ")

	result = strings.Trim(result, " -._")

	if result == "" {
		return ""
	}

	return result + ext
}

func sanitizeFilenameForRename(name string) string {

	invalid := []string{"<", ">", ":", "\"", "/", "\\", "|", "?", "*"}
	result := name
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "")
	}
	return strings.TrimSpace(result)
}

func PreviewRename(files []string, format string) []RenamePreview {
	var previews []RenamePreview

	for _, filePath := range files {
		preview := RenamePreview{
			OldPath: filePath,
			OldName: filepath.Base(filePath),
		}

		metadata, err := ReadAudioMetadata(filePath)
		if err != nil {
			preview.Error = err.Error()
			previews = append(previews, preview)
			continue
		}

		preview.Metadata = *metadata

		ext := filepath.Ext(filePath)
		newName := GenerateFilename(metadata, format, ext)

		if newName == "" {
			preview.Error = "Could not generate filename (missing metadata)"
			previews = append(previews, preview)
			continue
		}

		preview.NewName = newName
		preview.NewPath = filepath.Join(filepath.Dir(filePath), newName)

		previews = append(previews, preview)
	}

	return previews
}

func GetFileSizes(files []string) map[string]int64 {
	result := make(map[string]int64)
	for _, filePath := range files {
		info, err := os.Stat(filePath)
		if err == nil {
			result[filePath] = info.Size()
		}
	}
	return result
}

func RenameFiles(files []string, format string) []RenameResult {
	var results []RenameResult

	for _, filePath := range files {
		result := RenameResult{
			OldPath: filePath,
		}

		metadata, err := ReadAudioMetadata(filePath)
		if err != nil {
			result.Error = err.Error()
			result.Success = false
			results = append(results, result)
			continue
		}

		ext := filepath.Ext(filePath)
		newName := GenerateFilename(metadata, format, ext)

		if newName == "" {
			result.Error = "Could not generate filename (missing metadata)"
			result.Success = false
			results = append(results, result)
			continue
		}

		newPath := filepath.Join(filepath.Dir(filePath), newName)
		result.NewPath = newPath

		if newPath != filePath {
			if _, err := os.Stat(newPath); err == nil {
				result.Error = "File already exists"
				result.Success = false
				results = append(results, result)
				continue
			}
		}

		if err := os.Rename(filePath, newPath); err != nil {
			result.Error = err.Error()
			result.Success = false
			results = append(results, result)
			continue
		}

		result.Success = true
		results = append(results, result)
	}

	return results
}
