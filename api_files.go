package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Search, Tracks, Files, Audio, Media, Settings, System
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
)

// ─────────────────────────────────────────────────────────────────────────────
// Route registration — everything not in auth / jobs / watchlists
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerFileRoutes() {
	a := s.app

	// ── Search ────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/search", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			writeV1Error(w, http.StatusBadRequest, "url query param required")
			return
		}
		batchStr := r.URL.Query().Get("batch")
		batch := batchStr == "true" || batchStr == "1"
		result, err := a.GetSpotifyMetadata(SpotifyMetadataRequest{URL: url, Batch: batch})
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	// SSE — streams artist discography progressively.
	//   event: artist_info   → artist metadata + album list (emitted first, ~1-2s)
	//   event: album_tracks  → tracks for one album (emitted N times as they complete)
	//   event: done          → all albums processed
	//   event: stream_error  → fatal error
	s.mux.Handle("GET /api/v1/search/stream", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeV1Error(w, http.StatusInternalServerError, "streaming not supported")
			return
		}
		spotifyURL := r.URL.Query().Get("url")
		if spotifyURL == "" {
			writeV1Error(w, http.StatusBadRequest, "url parameter required")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		emit := func(eventType string, data interface{}) error {
			select {
			case <-r.Context().Done():
				return r.Context().Err()
			default:
			}
			sendSSEEvent(w, flusher, eventType, data)
			return nil
		}

		if err := spotify.StreamArtistDiscography(r.Context(), spotifyURL, emit); err != nil {
			select {
			case <-r.Context().Done():
			default:
				sendSSEEvent(w, flusher, "stream_error", map[string]string{"message": err.Error()})
			}
		}
	}))

	s.mux.Handle("GET /api/v1/search/query", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		searchType := r.URL.Query().Get("type")
		if q == "" {
			writeV1Error(w, http.StatusBadRequest, "q query param required")
			return
		}
		limit := 10
		if l := r.URL.Query().Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil && v > 0 {
				limit = v
			}
		}
		offset := 0
		if o := r.URL.Query().Get("offset"); o != "" {
			if v, err := strconv.Atoi(o); err == nil && v >= 0 {
				offset = v
			}
		}
		if searchType != "" {
			result, err := a.SearchSpotifyByType(SpotifySearchByTypeRequest{
				Query:      q,
				SearchType: searchType,
				Limit:      limit,
				Offset:     offset,
			})
			if err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeV1JSON(w, http.StatusOK, result)
		} else {
			result, err := a.SearchSpotify(SpotifySearchRequest{Query: q, Limit: limit})
			if err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeV1JSON(w, http.StatusOK, result)
		}
	}))

	// ── Tracks ────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/tracks/{id}/preview", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		url, err := a.GetPreviewURL(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
	}))

	s.mux.Handle("GET /api/v1/tracks/{id}/availability", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		result, err := a.CheckTrackAvailability(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	s.mux.Handle("GET /api/v1/tracks/{id}/links", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		region := r.URL.Query().Get("region")
		result, err := a.GetStreamingURLs(id, region)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]json.RawMessage{"urls": json.RawMessage(result)})
	}))

	// ── Settings ──────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/settings", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r)
		if user != nil && s.ctr.Auth != nil {
			if profile, err := s.ctr.Auth.GetUser(user.UserID); err == nil && len(profile.Settings) > 0 {
				writeV1JSON(w, http.StatusOK, profile.Settings)
				return
			}
		}
		settings, err := a.LoadSettings()
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, settings)
	}))

	s.mux.Handle("PUT /api/v1/settings", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var settings map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		user := GetUserFromContext(r)
		if user != nil && s.ctr.Auth != nil {
			if err := s.ctr.Auth.SaveUserSettings(user.UserID, settings); err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if err := a.SaveSettings(settings); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	// ── Files ─────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/files", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.ListDirectoryFiles(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("GET /api/v1/files/audio", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.ListAudioFilesInDir(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("GET /api/v1/files/metadata", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.ReadFileMetadata(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/files/rename", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			OldPath string `json:"old_path"`
			NewName string `json:"new_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if _, err := cleanAbsPath(params.OldPath); err != nil {
			writeV1Error(w, http.StatusBadRequest, "old_path: "+err.Error())
			return
		}
		if err := a.RenameFileTo(params.OldPath, params.NewName); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("POST /api/v1/files/sizes", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanAbsPaths(params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.GetFileSizes(cleaned))
	}))

	// Admin-only: returns raw image as base64.
	s.mux.Handle("GET /api/v1/files/image", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := a.ReadImageAsBase64(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"data": data})
	}))

	// Admin-only: returns raw file contents.
	s.mux.Handle("POST /api/v1/files/read", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		path, err := cleanAbsPath(params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		content, err := a.ReadTextFile(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"content": content})
	}))

	s.mux.Handle("POST /api/v1/files/rename/batch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanAbsPaths(params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.RenameFilesByMetadata(cleaned, params.Format))
	}))

	s.mux.Handle("POST /api/v1/files/rename/preview", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanAbsPaths(params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.PreviewRenameFiles(cleaned, params.Format))
	}))

	s.mux.Handle("POST /api/v1/files/upload/image", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Filename   string `json:"filename"`
			Base64Data string `json:"base64_data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		url, err := a.UploadImageBytes(params.Filename, params.Base64Data)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
	}))

	s.mux.Handle("POST /api/v1/files/upload/path", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		path, err := cleanAbsPath(params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		url, err := a.UploadImage(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
	}))

	s.mux.Handle("POST /api/v1/files/m3u8", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			M3U8Name          string   `json:"m3u8_name"`
			OutputDir         string   `json:"output_dir"`
			FilePaths         []string `json:"file_paths"`
			JellyfinMusicPath string   `json:"jellyfin_music_path"`
			MusicRoot         string   `json:"music_root"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanAbsPath(params.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		cleanedPaths, err := cleanAbsPaths(params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.CreateM3U8File(params.M3U8Name, outputDir, cleanedPaths, params.JellyfinMusicPath, params.MusicRoot); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("POST /api/v1/files/exists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			OutputDir string                      `json:"output_dir"`
			RootDir   string                      `json:"root_dir"`
			Tracks    []CheckFileExistenceRequest `json:"tracks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanAbsPath(params.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		rootDir, err := cleanAbsPath(params.RootDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "root_dir: "+err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.CheckFilesExistence(outputDir, rootDir, params.Tracks))
	}))

	// ── Audio ─────────────────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/audio/analyze", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.AnalyzeTrack(params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/audio/analyze/batch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.AnalyzeMultipleTracks(params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	s.mux.Handle("POST /api/v1/audio/convert", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req ConvertAudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.ConvertAudio(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── Media (lyrics, cover, header, gallery, avatar) ────────────────────
	s.mux.Handle("POST /api/v1/media/lyrics", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req LyricsDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadLyrics(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/cover", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req CoverDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadCover(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/header", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req HeaderDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadHeader(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/gallery", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req GalleryImageDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadGalleryImage(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/avatar", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req AvatarDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadAvatar(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── System ────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/system/info", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		osInfo, _ := a.GetOSInfo()
		configPath, _ := a.GetConfigPath()
		homeDir, _ := os.UserHomeDir()
		writeV1JSON(w, http.StatusOK, map[string]string{
			"os":          osInfo,
			"config_path": configPath,
			"home_dir":    homeDir,
			"version":     "v1",
		})
	}))

	s.mux.Handle("GET /api/v1/system/ffmpeg", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		installed, err := a.IsFFmpegInstalled()
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		ffprobeInstalled, _ := a.IsFFprobeInstalled()
		ffmpegPath, _ := a.GetFFmpegPath()
		writeV1JSON(w, http.StatusOK, map[string]interface{}{
			"installed":         installed,
			"ffprobe_installed": ffprobeInstalled,
			"ffmpeg_path":       ffmpegPath,
		})
	}))

	s.mux.Handle("GET /api/v1/system/defaults", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		writeV1JSON(w, http.StatusOK, a.GetDefaults())
	}))
}
