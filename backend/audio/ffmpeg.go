package audio

import (
	"archive/tar"
	"archive/zip"

	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/meta"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	"github.com/ulikunitz/xz"
)

// setHideWindow is a no-op on Linux (Docker target). On Windows it would hide
// the console window of spawned subprocesses; that build target is not supported.
func setHideWindow(_ *exec.Cmd) {}

func IsFFprobeInstalled() (bool, error) {
	ffprobePath, err := util.GetFFprobePath()
	if err != nil {
		return false, nil
	}

	if err := util.ValidateExecutable(ffprobePath); err != nil {
		return false, nil
	}

	cmd := exec.Command(ffprobePath, "-version")
	setHideWindow(cmd)
	err = cmd.Run()
	return err == nil, nil
}

func IsFFmpegInstalled() (bool, error) {
	ffmpegPath, err := util.GetFFmpegPath()
	if err != nil {
		return false, err
	}

	if err := util.ValidateExecutable(ffmpegPath); err != nil {
		return false, nil
	}

	cmd := exec.Command(ffmpegPath, "-version")

	setHideWindow(cmd)
	err = cmd.Run()
	return err == nil, nil
}

// ⚠️ github.com/afkarxyz/ffmpeg-binaries 404s as of 2026-08-04 — the repository
// is gone, not just this release. Every URL below therefore fails, and the
// auto-install path they serve cannot succeed on any platform.
//
// Left in place rather than deleted because this is the LEGACY DESKTOP path and
// nothing in the served product reaches it: the Docker image bakes FFmpeg in
// from BtbN/FFmpeg-Builds at build time, and EnsureFFmpeg finds it on PATH.
// Removing the constants would mean removing the auto-install feature, which is
// a product decision, not a link fix. Anyone reviving that feature needs a new
// source first; this comment is the warning that they do.
const (
	ffmpegWindowsURL = "https://github.com/afkarxyz/ffmpeg-binaries/releases/download/v8.0/ffmpeg-windows-amd64.zip"
	ffmpegLinuxURL   = "https://github.com/afkarxyz/ffmpeg-binaries/releases/download/v8.0/ffmpeg-linux-amd64.tar.xz"
)

func DownloadFFmpeg(progressCallback func(int)) error {

	ffmpegDir, err := util.GetFFmpegDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(ffmpegDir, 0755); err != nil {
		return fmt.Errorf("failed to create ffmpeg directory: %w", err)
	}

	if runtime.GOOS == "darwin" {
		ffmpegInstalled, _ := IsFFmpegInstalled()
		ffprobeInstalled, _ := IsFFprobeInstalled()

		isARM := runtime.GOARCH == "arm64"

		var macFFmpegURLs []string
		var macFFprobeURLs []string

		if isARM {

			macFFmpegURLs = []string{"https://github.com/afkarxyz/ffmpeg-binaries/releases/download/v8.0/ffmpeg-macos-arm64.zip"}
			macFFprobeURLs = []string{"https://github.com/afkarxyz/ffmpeg-binaries/releases/download/v8.0/ffprobe-macos-arm64.zip"}
		} else {

			macFFmpegURLs = []string{"https://github.com/afkarxyz/ffmpeg-binaries/releases/download/v8.0/ffmpeg-macos-intel.zip"}
			macFFprobeURLs = []string{"https://github.com/afkarxyz/ffmpeg-binaries/releases/download/v8.0/ffprobe-macos-intel.zip"}
		}

		if !ffmpegInstalled && !ffprobeInstalled {
			if err := downloadWithFallback(macFFmpegURLs, ffmpegDir, progressCallback, 0, 50); err != nil {
				return err
			}
			if err := downloadWithFallback(macFFprobeURLs, ffmpegDir, progressCallback, 50, 100); err != nil {
				return err
			}
		} else if !ffmpegInstalled {
			if err := downloadWithFallback(macFFmpegURLs, ffmpegDir, progressCallback, 0, 100); err != nil {
				return err
			}
		} else if !ffprobeInstalled {
			if err := downloadWithFallback(macFFprobeURLs, ffmpegDir, progressCallback, 0, 100); err != nil {
				return err
			}
		}
		return nil
	}

	var url string
	switch runtime.GOOS {
	case "windows":
		url = ffmpegWindowsURL
	case "linux":
		url = ffmpegLinuxURL
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	slog.Info("[FFmpeg] Downloading", "url", url)
	if err := downloadAndExtract(url, ffmpegDir, progressCallback, 0, 100); err != nil {
		return err
	}

	return nil
}

func downloadWithFallback(urls []string, destDir string, progressCallback func(int), start, end int) error {
	var lastErr error
	for _, url := range urls {
		slog.Info("[FFmpeg] Trying to download", "url", url)
		err := downloadAndExtract(url, destDir, progressCallback, start, end)
		if err == nil {
			return nil
		}
		lastErr = err
		slog.Warn("[FFmpeg] Download attempt failed", "err", err)
	}
	return fmt.Errorf("all download attempts failed: %w", lastErr)
}

func downloadAndExtract(url, destDir string, progressCallback func(int), progressStart, progressEnd int) error {

	tmpFile, err := os.CreateTemp("", "ffmpeg-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	client := util.NewHTTPClient(30 * time.Second)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download: HTTP %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength
	var downloaded int64

	if totalSize > 0 {
		totalSizeMB := float64(totalSize) / (1024 * 1024)
		slog.Info("[FFmpeg] Total size", "mb", totalSizeMB)
	} else {
		slog.Info("[FFmpeg] Downloading (size unknown)")
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := tmpFile.Write(buf[:n])
			if writeErr != nil {
				return fmt.Errorf("failed to write to temp file: %w", writeErr)
			}
			downloaded += int64(n)

			if totalSize > 0 && progressCallback != nil {
				rawProgress := float64(downloaded) / float64(totalSize)
				scaledProgress := progressStart + int(rawProgress*float64(progressEnd-progressStart))
				progressCallback(scaledProgress)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}
	}

	tmpFile.Close()

	if totalSize > 0 {
		slog.Info("[FFmpeg] Download complete", "mb", float64(downloaded)/(1024*1024), "total_mb", float64(totalSize)/(1024*1024))
	} else {
		slog.Info("[FFmpeg] Download complete", "mb", float64(downloaded)/(1024*1024))
	}
	slog.Info("[FFmpeg] Extracting")

	if strings.HasSuffix(url, ".tar.xz") || runtime.GOOS == "linux" {
		return extractTarXz(tmpFile.Name(), destDir)
	}
	return extractZip(tmpFile.Name(), destDir)
}

func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	ffmpegName := "ffmpeg"
	ffprobeName := "ffprobe"
	if runtime.GOOS == "windows" {
		ffmpegName = "ffmpeg.exe"
		ffprobeName = "ffprobe.exe"
	}

	foundFFmpeg := false
	foundFFprobe := false

	for _, f := range r.File {
		baseName := filepath.Base(f.Name)
		if f.FileInfo().IsDir() {
			continue
		}

		var destPath string
		if baseName == ffmpegName {
			destPath = filepath.Join(destDir, ffmpegName)
			foundFFmpeg = true
		} else if baseName == ffprobeName {
			destPath = filepath.Join(destDir, ffprobeName)
			foundFFprobe = true
		} else {

			continue
		}

		slog.Debug("[FFmpeg] Found", "file", f.Name)

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open file in zip: %w", err)
		}

		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			rc.Close()
			return fmt.Errorf("failed to create output file: %w", err)
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()

		if err != nil {
			return fmt.Errorf("failed to extract file: %w", err)
		}

		slog.Debug("[FFmpeg] Extracted", "path", destPath)
	}

	if !foundFFmpeg && !foundFFprobe {
		return fmt.Errorf("neither ffmpeg nor ffprobe found in archive")
	}

	if foundFFmpeg {
		slog.Info("[FFmpeg] ffmpeg extracted successfully")
	}
	if foundFFprobe {
		slog.Info("[FFmpeg] ffprobe extracted successfully")
	}

	return nil
}

func extractTarXz(tarXzPath, destDir string) error {
	file, err := os.Open(tarXzPath)
	if err != nil {
		return fmt.Errorf("failed to open tar.xz: %w", err)
	}
	defer file.Close()

	xzReader, err := xz.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create xz reader: %w", err)
	}

	tarReader := tar.NewReader(xzReader)

	ffmpegName := "ffmpeg"
	ffprobeName := "ffprobe"
	foundFFmpeg := false
	foundFFprobe := false

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		baseName := filepath.Base(header.Name)
		var destPath string

		if baseName == ffmpegName {
			destPath = filepath.Join(destDir, ffmpegName)
			foundFFmpeg = true
		} else if baseName == ffprobeName {
			destPath = filepath.Join(destDir, ffprobeName)
			foundFFprobe = true
		} else {

			continue
		}

		slog.Debug("[FFmpeg] Found", "file", header.Name)

		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}

		_, err = io.Copy(outFile, tarReader)
		outFile.Close()

		if err != nil {
			return fmt.Errorf("failed to extract file: %w", err)
		}

		slog.Debug("[FFmpeg] Extracted", "path", destPath)
	}

	if !foundFFmpeg && !foundFFprobe {
		return fmt.Errorf("neither ffmpeg nor ffprobe found in archive")
	}

	if foundFFmpeg {
		slog.Info("[FFmpeg] ffmpeg extracted successfully")
	}
	if foundFFprobe {
		slog.Info("[FFmpeg] ffprobe extracted successfully")
	}

	return nil
}

type ConvertAudioRequest struct {
	InputFiles   []string `json:"input_files"`
	OutputFormat string   `json:"output_format"`
	Bitrate      string   `json:"bitrate"`
	Codec        string   `json:"codec"`
}

type ConvertAudioResult struct {
	InputFile  string `json:"input_file"`
	OutputFile string `json:"output_file"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
	// Warning is set when the audio conversion itself succeeded (the
	// output file is valid and playable, hence Success stays true) but a
	// secondary step — embedding metadata or lyrics into it — failed.
	// Previously that failure was only ever printed to the server log, so
	// Success=true with no Error told the caller everything went fine when
	// the converted file could actually be missing its genre/cover/lyrics.
	Warning string `json:"warning,omitempty"`
}

// allowedConvertOutputFormats mirrors the formats the frontend's Audio
// Converter page can request (frontend/src/components/AudioConverterPage.tsx).
// OutputFormat is otherwise concatenated straight into an output directory
// and file extension below — an unvalidated value like "../../../etc/cron.d"
// would let a caller write ffmpeg's output outside the input file's directory.
var allowedConvertOutputFormats = map[string]bool{
	"mp3": true,
	"m4a": true,
}

func ConvertAudio(req ConvertAudioRequest) ([]ConvertAudioResult, error) {
	if !allowedConvertOutputFormats[strings.ToLower(req.OutputFormat)] {
		return nil, fmt.Errorf("unsupported output format: %q", req.OutputFormat)
	}

	ffmpegPath, err := util.GetFFmpegPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get ffmpeg path: %w", err)
	}

	if err := util.ValidateExecutable(ffmpegPath); err != nil {
		return nil, fmt.Errorf("invalid ffmpeg executable: %w", err)
	}

	installed, err := IsFFmpegInstalled()
	if err != nil || !installed {
		return nil, fmt.Errorf("ffmpeg is not installed")
	}

	results := make([]ConvertAudioResult, len(req.InputFiles))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, inputFile := range req.InputFiles {
		wg.Add(1)
		go func(idx int, inputFile string) {
			defer wg.Done()

			result := ConvertAudioResult{
				InputFile: inputFile,
			}
			// wg.Done() above still fires on a panic (registered defers
			// run during unwind), so this wouldn't hang the batch even
			// unrecovered — but an unrecovered panic in any goroutine
			// still crashes the whole process, and would leave results[idx]
			// at its zero value for whichever caller reads it if it
			// somehow didn't. Recover and report the same way every other
			// per-file failure in this function already does.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("[PANIC] recovered converting file", "file", inputFile, "recover", r, "stack", string(debug.Stack()))
					mu.Lock()
					results[idx] = ConvertAudioResult{InputFile: inputFile, Success: false, Error: fmt.Sprintf("internal error: %v", r)}
					mu.Unlock()
				}
			}()

			inputExt := strings.ToLower(filepath.Ext(inputFile))
			baseName := strings.TrimSuffix(filepath.Base(inputFile), inputExt)
			inputDir := filepath.Dir(inputFile)

			outputFormatUpper := strings.ToUpper(req.OutputFormat)
			outputDir := filepath.Join(inputDir, outputFormatUpper)

			if err := os.MkdirAll(outputDir, 0755); err != nil {
				result.Error = fmt.Sprintf("failed to create output directory: %v", err)
				result.Success = false
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				return
			}

			outputExt := "." + strings.ToLower(req.OutputFormat)
			outputFile := filepath.Join(outputDir, baseName+outputExt)

			if inputExt == outputExt {
				result.Error = "Input and output formats are the same"
				result.Success = false
				mu.Lock()
				results[idx] = result
				mu.Unlock()
				return
			}

			result.OutputFile = outputFile

			var coverArtPath string
			var lyrics string
			var inputMetadata meta.Metadata

			inputMetadata, err = meta.ExtractFullMetadataFromFile(inputFile)
			if err != nil {
				slog.Warn("[FFmpeg] Failed to extract metadata", "file", inputFile, "err", err)
			}

			coverArtPath, _ = meta.ExtractCoverArt(inputFile)
			lyrics, err = meta.ExtractLyrics(inputFile)
			if err != nil {
				slog.Warn("[FFmpeg] Failed to extract lyrics", "file", inputFile, "err", err)
			} else if lyrics != "" {
				slog.Debug("[FFmpeg] Lyrics extracted", "file", inputFile, "chars", len(lyrics))
			} else {
				slog.Debug("[FFmpeg] No lyrics found", "file", inputFile)
			}

			inputMetadata.Lyrics = lyrics

			// Hardened: inputFile is any file in the library or the upload
			// staging dir — including bytes a proxy chose. See
			// util.FFmpegHardeningArgs.
			args := append(util.FFmpegHardeningArgs(),
				"-i", inputFile,
				"-y",
			)

			switch req.OutputFormat {
			case "mp3":
				args = append(args,
					"-codec:a", "libmp3lame",
					"-b:a", req.Bitrate,
					"-map", "0:a",
					"-id3v2_version", "3",
				)
			case "m4a":

				codec := req.Codec
				if codec == "" {
					codec = "aac"
				}

				if codec == "alac" {

					args = append(args,
						"-codec:a", "alac",
						"-map", "0:a",
					)
				} else {

					args = append(args,
						"-codec:a", "aac",
						"-b:a", req.Bitrate,
						"-map", "0:a",
					)
				}
			}

			args = append(args, outputFile)

			slog.Debug("[FFmpeg] Converting", "input", inputFile, "output", outputFile)

			cmd := exec.Command(ffmpegPath, args...)

			setHideWindow(cmd)
			output, err := cmd.CombinedOutput()
			if err != nil {
				result.Error = fmt.Sprintf("conversion failed: %s - %s", err.Error(), string(output))
				result.Success = false
				mu.Lock()
				results[idx] = result
				mu.Unlock()

				if coverArtPath != "" {
					os.Remove(coverArtPath)
				}
				return
			}

			var warnings []string
			if err := meta.EmbedMetadataToConvertedFile(outputFile, inputMetadata, coverArtPath); err != nil {
				slog.Warn("[FFmpeg] Failed to embed metadata", "err", err)
				warnings = append(warnings, fmt.Sprintf("failed to embed metadata: %v", err))
			} else {
				slog.Debug("[FFmpeg] Metadata embedded successfully")
			}

			if lyrics != "" {
				if err := meta.EmbedLyricsOnlyUniversal(outputFile, lyrics); err != nil {
					slog.Warn("[FFmpeg] Failed to embed lyrics", "err", err)
					warnings = append(warnings, fmt.Sprintf("failed to embed lyrics: %v", err))
				} else {
					slog.Debug("[FFmpeg] Lyrics embedded successfully")
				}
			}
			if len(warnings) > 0 {
				result.Warning = strings.Join(warnings, "; ")
			}

			if coverArtPath != "" {
				os.Remove(coverArtPath)
			}

			result.Success = true
			slog.Debug("[FFmpeg] Successfully converted", "output", outputFile)

			mu.Lock()
			results[idx] = result
			mu.Unlock()
		}(i, inputFile)
	}

	wg.Wait()
	return results, nil
}
