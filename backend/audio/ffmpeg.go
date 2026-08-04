package audio

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/sos-pc/SpotiFLAC-SH/backend/meta"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
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
