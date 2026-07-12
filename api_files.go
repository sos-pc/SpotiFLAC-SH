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
		if !v1RequirePermission(w, r, "read") {
			return
		}
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
		id := r.PathValue("id")
		url, err := a.GetPreviewURL(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
	}))

	s.mux.Handle("GET /api/v1/tracks/{id}/availability", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		id := r.PathValue("id")
		result, err := a.CheckTrackAvailability(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	s.mux.Handle("GET /api/v1/tracks/{id}/links", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
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
		if !v1RequirePermission(w, r, "manage") {
			return
		}
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
	// Every route below that accepts a client-supplied filesystem path uses
	// cleanLibraryPath(s.libraryRoot(), ...) instead of the bare cleanAbsPath
	// — cleanAbsPath only rejects relative paths, it does not stop a caller
	// from naming any absolute path on the host (S3). Confining to the
	// configured music library root is what api_admin.go's rebuild-scan
	// already does for watchlist paths (collectScanRoots); this makes every
	// other file-management/download surface consistent with it.
	s.mux.Handle("GET /api/v1/files", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		path, err := cleanLibraryPath(s.libraryRoot(), r.URL.Query().Get("path"))
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
		path, err := cleanLibraryPath(s.libraryRoot(), r.URL.Query().Get("path"))
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

	// Admin-only: File Manager is the only caller (hidden from non-admins in
	// the sidebar); reading arbitrary-path tag metadata has no other
	// legitimate non-admin use.
	s.mux.Handle("GET /api/v1/files/metadata", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		path, err := cleanLibraryPath(s.libraryRoot(), r.URL.Query().Get("path"))
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

	// Admin-only: File Manager is the only caller. Renaming an arbitrary
	// absolute path is a data-tampering primitive, not just disclosure.
	s.mux.Handle("POST /api/v1/files/rename", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		var params struct {
			OldPath string `json:"old_path"`
			NewName string `json:"new_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if _, err := cleanLibraryPath(s.libraryRoot(), params.OldPath); err != nil {
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanLibraryPaths(s.libraryRoot(), params.FilePaths)
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
		path, err := cleanLibraryPath(s.libraryRoot(), r.URL.Query().Get("path"))
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
		path, err := cleanLibraryPath(s.libraryRoot(), params.FilePath)
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

	// Admin-only: File Manager is the only caller.
	s.mux.Handle("POST /api/v1/files/rename/batch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanLibraryPaths(s.libraryRoot(), params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.RenameFilesByMetadata(cleaned, params.Format))
	}))

	// Admin-only: File Manager is the only caller.
	s.mux.Handle("POST /api/v1/files/rename/preview", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanLibraryPaths(s.libraryRoot(), params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.PreviewRenameFiles(cleaned, params.Format))
	}))

	// Uploads client-supplied bytes (not a server-side path) to the
	// configured cover-art host — no path-confinement concern here, unlike
	// upload/path below.
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

	// Admin-only (N1): this reads an arbitrary server-side path and uploads
	// its contents to a third-party host, returning a public URL — without
	// the admin gate, any authenticated non-admin user could exfiltrate any
	// file the server process can read. cleanLibraryPath additionally
	// confines it to the music library, consistent with every other route
	// here (defense in depth even for an admin-triggered action).
	s.mux.Handle("POST /api/v1/files/upload/path", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
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
		path, err := cleanLibraryPath(s.libraryRoot(), params.FilePath)
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
		if !v1RequirePermission(w, r, "manage") {
			return
		}
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
		root := s.libraryRoot()
		outputDir, err := cleanLibraryPath(root, params.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		cleanedPaths, err := cleanLibraryPaths(root, params.FilePaths)
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
		var params struct {
			OutputDir string                      `json:"output_dir"`
			RootDir   string                      `json:"root_dir"`
			Tracks    []CheckFileExistenceRequest `json:"tracks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		root := s.libraryRoot()
		outputDir, err := cleanLibraryPath(root, params.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		// root_dir is optional (only used to also match against a second
		// directory, e.g. a legacy layout) — filepath.Walk on it below makes
		// it just as much a real I/O target as output_dir, and an unconfined
		// value would let a caller enumerate .flac/.mp3 filenames anywhere
		// on the host, so it's only accepted when non-empty.
		var rootDir string
		if params.RootDir != "" {
			rootDir, err = cleanLibraryPath(root, params.RootDir)
			if err != nil {
				writeV1Error(w, http.StatusBadRequest, "root_dir: "+err.Error())
				return
			}
		}
		writeV1JSON(w, http.StatusOK, a.CheckFilesExistence(outputDir, rootDir, params.Tracks))
	}))

	// ── Audio ─────────────────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/audio/analyze", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		path, err := s.cleanUploadOrLibraryPath(s.libraryRoot(), params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.AnalyzeTrack(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/audio/analyze/batch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := s.cleanUploadOrLibraryPaths(s.libraryRoot(), params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.AnalyzeMultipleTracks(cleaned)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	s.mux.Handle("POST /api/v1/audio/convert", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req ConvertAudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := s.cleanUploadOrLibraryPaths(s.libraryRoot(), req.InputFiles)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		req.InputFiles = cleaned
		result, err := a.ConvertAudio(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── Media (lyrics, cover, header, gallery, avatar) ────────────────────
	// Each of these takes an output_dir the server writes a downloaded file
	// into — previously accepted straight from the request body with no
	// validation at all (not even cleanAbsPath), the same gap as the
	// Files/Audio routes above.
	s.mux.Handle("POST /api/v1/media/lyrics", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req LyricsDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRoot(), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := a.DownloadLyrics(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/cover", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req CoverDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRoot(), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := a.DownloadCover(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/header", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req HeaderDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRoot(), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := a.DownloadHeader(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/gallery", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req GalleryImageDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRoot(), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := a.DownloadGalleryImage(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/avatar", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req AvatarDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRoot(), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := a.DownloadAvatar(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── System ────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/system/info", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
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
		if !v1RequirePermission(w, r, "read") {
			return
		}
		writeV1JSON(w, http.StatusOK, a.GetDefaults())
	}))
}
