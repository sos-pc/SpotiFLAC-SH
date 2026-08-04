package main

// ─────────────────────────────────────────────────────────────────────────────
// FileService — file-management operations (list, metadata, rename, upload,
// existence checks) carved out of the former App god-object (R3).
//
// Holds the catalog handle and the job manager, not the Container: its two
// rename methods delegate to syncCatalogPathOnRename, and those two stores are
// all it needs. Taking the whole container made this service name a server-layer
// type for nothing.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

type FileService struct {
	catalog *sql.DB
	jobs    *JobManager
}

func NewFileService(catalog *sql.DB, jobs *JobManager) *FileService {
	return &FileService{catalog: catalog, jobs: jobs}
}

func (f *FileService) OpenFolder(path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	return backend.OpenFolderInExplorer(path)
}

func (f *FileService) GetFileSizes(files []string) map[string]int64 {
	return backend.GetFileSizes(files)
}

func (f *FileService) ListDirectoryFiles(dirPath string) ([]backend.FileInfo, error) {
	if dirPath == "" {
		return nil, fmt.Errorf("directory path is required")
	}
	return backend.ListDirectory(dirPath)
}

func (f *FileService) ListAudioFilesInDir(dirPath string) ([]backend.FileInfo, error) {
	if dirPath == "" {
		return nil, fmt.Errorf("directory path is required")
	}
	return backend.ListAudioFiles(dirPath)
}

func (f *FileService) ReadFileMetadata(filePath string) (*backend.AudioMetadata, error) {
	if filePath == "" {
		return nil, fmt.Errorf("file path is required")
	}
	return backend.ReadAudioMetadata(filePath)
}

func (f *FileService) PreviewRenameFiles(files []string, format string) []backend.RenamePreview {
	return backend.PreviewRename(files, format)
}

func (f *FileService) RenameFilesByMetadata(files []string, format string) []backend.RenameResult {
	results := backend.RenameFiles(files, format)
	for _, r := range results {
		if r.Success && r.NewPath != "" {
			syncCatalogPathOnRename(f.catalog, f.jobs, r.OldPath, r.NewPath)
		}
	}
	return results
}

func (f *FileService) ReadTextFile(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// RenameFileTo renames a single file to newName (extension preserved),
// keeping it in the same directory. newName is user-supplied free text (the
// manual "rename" text field in File Manager, not a metadata-derived
// filename like GenerateFilename produces), so — unlike RenameFiles/
// GenerateFilename, which already sanitize every field they build a name
// from — it must be sanitized here too: an unsanitized value like
// "../../../etc/cron.d/x" would survive filepath.Join and escape dir
// entirely. Also mirrors RenameFiles' collision check: os.Rename silently
// replaces an existing destination file on POSIX, so without this check a
// name collision would destroy another track with no warning.
func (f *FileService) RenameFileTo(oldPath, newName string) error {
	dir := filepath.Dir(oldPath)
	ext := filepath.Ext(oldPath)
	safeName := util.SanitizeFilename(newName)
	newPath := filepath.Join(dir, safeName+ext)
	if newPath != oldPath {
		if _, err := os.Stat(newPath); err == nil {
			return fmt.Errorf("a file named %q already exists", filepath.Base(newPath))
		}
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	syncCatalogPathOnRename(f.catalog, f.jobs, oldPath, newPath)
	return nil
}

func (f *FileService) UploadImage(filePath string) (string, error) {
	return backend.UploadToSendNow(filePath)
}

func (f *FileService) UploadImageBytes(filename string, base64Data string) (string, error) {
	if idx := strings.Index(base64Data, ","); idx != -1 {
		base64Data = base64Data[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %v", err)
	}
	return backend.UploadBytesToSendNow(filename, data)
}

func (f *FileService) ReadImageAsBase64(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	var mimeType string
	switch ext {
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".png":
		mimeType = "image/png"
	case ".gif":
		mimeType = "image/gif"
	case ".webp":
		mimeType = "image/webp"
	default:
		mimeType = "image/jpeg"
	}
	encoded := base64.StdEncoding.EncodeToString(content)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CheckFilesExistence
// ─────────────────────────────────────────────────────────────────────────────

type CheckFileExistenceRequest struct {
	SpotifyID           string `json:"spotify_id"`
	TrackName           string `json:"track_name"`
	ArtistName          string `json:"artist_name"`
	AlbumName           string `json:"album_name,omitempty"`
	AlbumArtist         string `json:"album_artist,omitempty"`
	ReleaseDate         string `json:"release_date,omitempty"`
	TrackNumber         int    `json:"track_number,omitempty"`
	DiscNumber          int    `json:"disc_number,omitempty"`
	Position            int    `json:"position,omitempty"`
	UseAlbumTrackNumber bool   `json:"use_album_track_number,omitempty"`
	FilenameFormat      string `json:"filename_format,omitempty"`
	IncludeTrackNumber  bool   `json:"include_track_number,omitempty"`
	AudioFormat         string `json:"audio_format,omitempty"`
	RelativePath        string `json:"relative_path,omitempty"`
}

type CheckFileExistenceResult struct {
	SpotifyID  string `json:"spotify_id"`
	Exists     bool   `json:"exists"`
	FilePath   string `json:"file_path,omitempty"`
	TrackName  string `json:"track_name,omitempty"`
	ArtistName string `json:"artist_name,omitempty"`
}

func (f *FileService) CheckFilesExistence(outputDir string, rootDir string, tracks []CheckFileExistenceRequest) []CheckFileExistenceResult {
	if len(tracks) == 0 {
		return []CheckFileExistenceResult{}
	}

	outputDir = util.NormalizePath(outputDir)
	if rootDir != "" {
		rootDir = util.NormalizePath(rootDir)
	}

	defaultFilenameFormat := "title-artist"

	type result struct {
		index  int
		result CheckFileExistenceResult
	}

	resultsChan := make(chan result, len(tracks))

	var rootDirFiles map[string]string
	rootDirFilesOnce := false
	getRootDirFiles := func() map[string]string {
		if rootDirFilesOnce {
			return rootDirFiles
		}
		rootDirFiles = make(map[string]string)
		if rootDir != "" && rootDir != outputDir {
			filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !info.IsDir() {
					if strings.EqualFold(filepath.Ext(path), ".flac") || strings.EqualFold(filepath.Ext(path), ".mp3") {
						rootDirFiles[info.Name()] = path
					}
				}
				return nil
			})
		}
		rootDirFilesOnce = true
		return rootDirFiles
	}

	for i, track := range tracks {
		go func(idx int, t CheckFileExistenceRequest) {
			res := CheckFileExistenceResult{
				SpotifyID:  t.SpotifyID,
				TrackName:  t.TrackName,
				ArtistName: t.ArtistName,
				Exists:     false,
			}
			// The collection loop below reads exactly len(tracks) results
			// off resultsChan, so every goroutine here must send exactly
			// one — an unrecovered panic wouldn't just crash the process
			// (as any unrecovered panic in any goroutine does), it would
			// also leave that read permanently blocked if the process
			// somehow kept running. Recovering and still sending the
			// zero-value "not found" res satisfies both.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("[PANIC] recovered checking file existence", "artist", t.ArtistName, "track", t.TrackName, "recover", r, "stack", string(debug.Stack()))
					resultsChan <- result{index: idx, result: res}
				}
			}()
			if t.TrackName == "" || t.ArtistName == "" {
				resultsChan <- result{index: idx, result: res}
				return
			}
			filenameFormat := t.FilenameFormat
			if filenameFormat == "" {
				filenameFormat = defaultFilenameFormat
			}
			trackNumberToPrint := util.ResolveTrackNumber(t.Position, t.TrackNumber, t.UseAlbumTrackNumber)
			fileExt := ".flac"
			if t.AudioFormat == "mp3" {
				fileExt = ".mp3"
			}
			expectedFilenameBase := util.BuildExpectedFilename(
				t.TrackName, t.ArtistName, t.AlbumName, t.AlbumArtist, t.ReleaseDate,
				filenameFormat, "", "", t.IncludeTrackNumber, trackNumberToPrint, t.DiscNumber,
			)
			expectedFilename := strings.TrimSuffix(expectedFilenameBase, ".flac") + fileExt
			targetDir := outputDir
			if t.RelativePath != "" {
				targetDir = filepath.Join(outputDir, t.RelativePath)
			}
			expectedPath := filepath.Join(targetDir, expectedFilename)
			if fileInfo, err := os.Stat(expectedPath); err == nil && fileInfo.Size() > 100*1024 {
				res.Exists = true
				res.FilePath = expectedPath
			} else {
				res.FilePath = expectedFilename
			}
			resultsChan <- result{index: idx, result: res}
		}(i, track)
	}

	results := make([]CheckFileExistenceResult, len(tracks))
	missingIndices := []int{}
	for i := 0; i < len(tracks); i++ {
		r := <-resultsChan
		results[r.index] = r.result
		if !results[r.index].Exists {
			missingIndices = append(missingIndices, r.index)
		}
	}

	if len(missingIndices) > 0 && rootDir != "" {
		filesMap := getRootDirFiles()
		if len(filesMap) > 0 {
			for _, idx := range missingIndices {
				expectedFilename := results[idx].FilePath
				baseName := filepath.Base(expectedFilename)
				if path, ok := filesMap[baseName]; ok {
					results[idx].Exists = true
					results[idx].FilePath = path
				} else {
					results[idx].FilePath = ""
				}
			}
		} else {
			for _, idx := range missingIndices {
				results[idx].FilePath = ""
			}
		}
	} else {
		for _, idx := range missingIndices {
			results[idx].FilePath = ""
		}
	}

	return results
}
