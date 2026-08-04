package main

// ─────────────────────────────────────────────────────────────────────────────
// AudioService — audio analysis, format conversion and FFmpeg availability
// carved out of the former App god-object (R3). Stateless: every method is a
// thin wrapper over backend/audio and backend/util, so the struct holds no
// dependencies.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"fmt"

	"github.com/sos-pc/SpotiFLAC-SH/backend/audio"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

type AudioService struct{}

func (s *AudioService) AnalyzeTrack(filePath string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("file path is required")
	}
	result, err := audio.AnalyzeTrack(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to analyze track: %v", err)
	}
	jsonData, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
}

func (s *AudioService) AnalyzeMultipleTracks(filePaths []string) (string, error) {
	if len(filePaths) == 0 {
		return "", fmt.Errorf("at least one file path is required")
	}
	results := make([]*audio.AnalysisResult, 0, len(filePaths))
	for _, filePath := range filePaths {
		result, err := audio.AnalyzeTrack(filePath)
		if err != nil {
			continue
		}
		results = append(results, result)
	}
	jsonData, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
}

func (s *AudioService) IsFFmpegInstalled() (bool, error)  { return audio.IsFFmpegInstalled() }
func (s *AudioService) IsFFprobeInstalled() (bool, error) { return audio.IsFFprobeInstalled() }
func (s *AudioService) GetFFmpegPath() (string, error)    { return util.GetFFmpegPath() }

type ConvertAudioRequest struct {
	InputFiles   []string `json:"input_files"`
	OutputFormat string   `json:"output_format"`
	Bitrate      string   `json:"bitrate"`
	Codec        string   `json:"codec"`
}

func (s *AudioService) ConvertAudio(req ConvertAudioRequest) ([]audio.ConvertAudioResult, error) {
	return audio.ConvertAudio(audio.ConvertAudioRequest{
		InputFiles:   req.InputFiles,
		OutputFormat: req.OutputFormat,
		Bitrate:      req.Bitrate,
		Codec:        req.Codec,
	})
}
