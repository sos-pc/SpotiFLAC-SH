package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Search, Tracks, Files, Audio, Media, Settings, System
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
)

// ─────────────────────────────────────────────────────────────────────────────
// Per-track media placement (D4)
// ─────────────────────────────────────────────────────────────────────────────

// trackMediaPlacement is where a per-track sidecar file (a cover, a .lrc) must
// be written, and under what name, for it to sit beside its audio file.
type trackMediaPlacement struct {
	OutputDir           string
	FilenameFormat      string
	TrackNumber         bool
	UseAlbumTrackNumber bool
	// The artist names to write, after the server's first-artist rule. They are
	// returned rather than applied in place because the caller's request struct
	// owns them, and because outputSubfolder needs the UNTRIMMED names (it
	// applies the rule itself).
	ArtistName  string
	AlbumArtist string
}

// trackNumberFor picks between the list index and the album track number the
// same way file_service.go does for the existence check, so a sidecar file is
// numbered exactly like the audio file it accompanies.
func (p trackMediaPlacement) trackNumberFor(position, albumTrackNumber int) int {
	if p.UseAlbumTrackNumber && albumTrackNumber > 0 {
		return albumTrackNumber
	}
	return position
}

// mediaPlacement resolves that placement from the requesting user's settings,
// reusing outputSubfolder — the very function buildOutputDir uses for the audio
// file. Sharing the implementation is the point: a cover computed by a second,
// slightly different rule is a cover in the wrong folder.
//
// Every subfolder part is sanitised by outputSubfolder, so the result stays
// confined to the library root without needing cleanLibraryPath on client input
// — there is no client input left in the path.
func (s *Server) mediaPlacement(
	r *http.Request,
	artistName, albumName, albumArtist, releaseDate, playlistName string,
) trackMediaPlacement {
	settings := EffectiveDownloadSettings(s.ctr.Auth, userIDFromContext(r))
	sub := outputSubfolder(
		settings.FolderTemplate, settings.CreatePlaylistFolder, settings.UseFirstArtistOnly,
		artistName, albumName, albumArtist, releaseDate, playlistName,
	)
	p := trackMediaPlacement{
		OutputDir:      filepath.Join(s.libraryRootFor(r), sub),
		FilenameFormat: settings.FilenameTemplate,
		TrackNumber:    settings.TrackNumber,
		// Derived from the folder template exactly as buildDownloadRequest does.
		UseAlbumTrackNumber: strings.Contains(settings.FolderTemplate, "{album}") ||
			strings.Contains(settings.FolderTemplate, "{album_artist}"),
		ArtistName:  artistName,
		AlbumArtist: albumArtist,
	}
	if settings.UseFirstArtistOnly {
		p.ArtistName = getFirstArtistStatic(p.ArtistName)
		if p.AlbumArtist != "" {
			p.AlbumArtist = getFirstArtistStatic(p.AlbumArtist)
		}
	}
	return p
}

// ─────────────────────────────────────────────────────────────────────────────
// Route registration — everything not in auth / jobs / watchlists
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerFileRoutes() {

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
		result, err := s.ctr.Metadata.GetSpotifyMetadata(SpotifyMetadataRequest{URL: url, Batch: batch}, userIDFromContext(r))
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
		// See sse.go's v1JobsStream for why "Connection: keep-alive" is
		// deliberately not set here — it's an HTTP/2 protocol violation
		// that can surface as net::ERR_HTTP2_PROTOCOL_ERROR through a
		// reverse proxy.
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
			result, err := s.ctr.Metadata.SearchSpotifyByType(SpotifySearchByTypeRequest{
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
			result, err := s.ctr.Metadata.SearchSpotify(SpotifySearchRequest{Query: q, Limit: limit})
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
		url, err := s.ctr.Metadata.GetPreviewURL(id)
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
		result, err := s.ctr.Metadata.CheckTrackAvailability(id)
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
		result, err := s.ctr.Metadata.GetStreamingURLs(id, region)
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
		settings, err := s.ctr.System.LoadSettings()
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
		if !decodeV1JSON(w, r, &settings) {
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
		if err := s.ctr.System.SaveSettings(settings); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	// ── Files ─────────────────────────────────────────────────────────────
	// Every route below that accepts a client-supplied filesystem path uses
	// cleanLibraryPath(s.libraryRootFor(r), ...) instead of the bare
	// cleanAbsPath — cleanAbsPath only rejects relative paths, it does not
	// stop a caller from naming any absolute path on the host (S3).
	// libraryRootFor resolves the authenticated caller's own downloadPath
	// override if they have one, else the operator's global default (R8) —
	// unlike the plain global-only libraryRoot() that api_admin.go's
	// rebuild-scan uses for its library-wide walk (collectScanRoots).
	s.mux.Handle("GET /api/v1/files", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		path, err := cleanLibraryPath(s.libraryRootFor(r), r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := s.ctr.Files.ListDirectoryFiles(path)
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
		path, err := cleanLibraryPath(s.libraryRootFor(r), r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := s.ctr.Files.ListAudioFilesInDir(path)
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
		path, err := cleanLibraryPath(s.libraryRootFor(r), r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := s.ctr.Files.ReadFileMetadata(path)
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		if _, err := cleanLibraryPath(s.libraryRootFor(r), params.OldPath); err != nil {
			writeV1Error(w, http.StatusBadRequest, "old_path: "+err.Error())
			return
		}
		if err := s.ctr.Files.RenameFileTo(params.OldPath, params.NewName); err != nil {
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		cleaned, err := cleanLibraryPaths(s.libraryRootFor(r), params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, s.ctr.Files.GetFileSizes(cleaned))
	}))

	// Admin-only: returns raw image as base64.
	s.mux.Handle("GET /api/v1/files/image", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		path, err := cleanLibraryPath(s.libraryRootFor(r), r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := s.ctr.Files.ReadImageAsBase64(path)
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		path, err := cleanLibraryPath(s.libraryRootFor(r), params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		content, err := s.ctr.Files.ReadTextFile(path)
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		cleaned, err := cleanLibraryPaths(s.libraryRootFor(r), params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, s.ctr.Files.RenameFilesByMetadata(cleaned, params.Format))
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		cleaned, err := cleanLibraryPaths(s.libraryRootFor(r), params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, s.ctr.Files.PreviewRenameFiles(cleaned, params.Format))
	}))

	// Uploads client-supplied bytes (not a server-side path) to the
	// configured cover-art host — no path-confinement concern here, unlike
	// upload/path below.
	s.mux.Handle("POST /api/v1/files/upload/image", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		// Outbound: uploads client-supplied bytes to a third-party public host and
		// returns a public URL. Not something a read-only key should be able to do
		// with the instance's network identity.
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var params struct {
			Filename   string `json:"filename"`
			Base64Data string `json:"base64_data"`
		}
		if !decodeV1JSON(w, r, &params) {
			return
		}
		url, err := s.ctr.Files.UploadImageBytes(params.Filename, params.Base64Data)
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		path, err := cleanLibraryPath(s.libraryRootFor(r), params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		url, err := s.ctr.Files.UploadImage(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		// Backend-authoritative (docs/settings-source-of-truth.md D2): the
		// client's output_dir/root_dir are ignored. The base is the user's
		// server download path, and each track's subfolder is derived from the
		// server folder template with outputSubfolder — the very function
		// buildOutputDir uses — so this check targets exactly where a download
		// would land. Every subfolder part is sanitised (no "../"), so the
		// result stays confined to base without needing cleanLibraryPath on any
		// client input.
		base := s.libraryRootFor(r)
		settings := EffectiveDownloadSettings(s.ctr.Auth, userIDFromContext(r))
		for i := range params.Tracks {
			t := &params.Tracks[i]
			// Folder first: outputSubfolder applies the first-artist rule itself,
			// so it needs the untrimmed names.
			t.RelativePath = outputSubfolder(
				settings.FolderTemplate, settings.CreatePlaylistFolder, settings.UseFirstArtistOnly,
				t.ArtistName, t.AlbumName, t.AlbumArtist, t.ReleaseDate, "",
			)
			// Then the filename, which must match what a download actually writes:
			// same template, same track-number rule, and the same first-artist
			// trimming buildDownloadRequest applies before BuildExpectedFilename.
			// Without this the client still decided the filename while the server
			// decided the folder — half-authoritative, and a mismatch the moment
			// the two disagreed.
			t.FilenameFormat = settings.FilenameTemplate
			t.IncludeTrackNumber = settings.TrackNumber
			// Derived from the folder template exactly as buildDownloadRequest does.
			t.UseAlbumTrackNumber = strings.Contains(settings.FolderTemplate, "{album}") ||
				strings.Contains(settings.FolderTemplate, "{album_artist}")
			if settings.UseFirstArtistOnly {
				t.ArtistName = getFirstArtistStatic(t.ArtistName)
				if t.AlbumArtist != "" {
					t.AlbumArtist = getFirstArtistStatic(t.AlbumArtist)
				}
			}
		}
		// rootDir "" — the secondary "match by filename anywhere under the root"
		// walk is dropped: with the path now computed server-side it can only
		// produce false positives (a same-named track in another album).
		writeV1JSON(w, http.StatusOK, s.ctr.Files.CheckFilesExistence(base, "", params.Tracks))
	}))

	// ── Audio ─────────────────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/audio/analyze", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		var params struct {
			FilePath string `json:"file_path"`
		}
		if !decodeV1JSON(w, r, &params) {
			return
		}
		path, err := s.cleanUploadOrLibraryPath(s.libraryRootFor(r), params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := s.ctr.Audio.AnalyzeTrack(path)
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
		if !decodeV1JSON(w, r, &params) {
			return
		}
		cleaned, err := s.cleanUploadOrLibraryPaths(s.libraryRootFor(r), params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := s.ctr.Audio.AnalyzeMultipleTracks(cleaned)
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
		if !decodeV1JSON(w, r, &req) {
			return
		}
		cleaned, err := s.cleanUploadOrLibraryPaths(s.libraryRootFor(r), req.InputFiles)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		req.InputFiles = cleaned
		result, err := s.ctr.Audio.ConvertAudio(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── Media (lyrics, cover, header, gallery, avatar) ────────────────────
	// The lyrics and cover routes place their file beside the track, so they
	// derive that placement from the user's settings rather than trusting the
	// request (see mediaPlacement). The artist/header/gallery/avatar routes
	// below still take an output_dir from the client, validated with
	// cleanLibraryPath — they are not per-track and have no track to sit next to.
	s.mux.Handle("POST /api/v1/media/lyrics", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req LyricsDownloadRequest
		if !decodeV1JSON(w, r, &req) {
			return
		}
		// Backend-authoritative (D4): the client's output_dir/filename_format/
		// track-number flags are discarded and recomputed here, so the .lrc lands
		// beside the track instead of wherever the client thought it would.
		//
		// The client used to resolve this path itself, with a rule that did not
		// match the server's: it appended a playlist folder whenever the view was
		// not an album, even when the folder template already contained {album} —
		// outputSubfolder never does. With createPlaylistFolder on, the lyrics
		// landed one directory away from their track.
		p := s.mediaPlacement(r, req.ArtistName, req.AlbumName, req.AlbumArtist,
			req.ReleaseDate, req.PlaylistName)
		req.OutputDir = p.OutputDir
		req.FilenameFormat = p.FilenameFormat
		req.TrackNumber = p.TrackNumber
		req.ArtistName = p.ArtistName
		req.AlbumArtist = p.AlbumArtist
		req.Position = p.trackNumberFor(req.Position, req.AlbumTrackNumber)
		result, err := s.ctr.Media.DownloadLyrics(req)
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
		if !decodeV1JSON(w, r, &req) {
			return
		}
		// Same server-side placement as the lyrics route above (D4).
		p := s.mediaPlacement(r, req.ArtistName, req.AlbumName, req.AlbumArtist,
			req.ReleaseDate, req.PlaylistName)
		req.OutputDir = p.OutputDir
		req.FilenameFormat = p.FilenameFormat
		req.TrackNumber = p.TrackNumber
		req.ArtistName = p.ArtistName
		req.AlbumArtist = p.AlbumArtist
		req.Position = p.trackNumberFor(req.Position, req.AlbumTrackNumber)
		result, err := s.ctr.Media.DownloadCover(req)
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
		if !decodeV1JSON(w, r, &req) {
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRootFor(r), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := s.ctr.Media.DownloadHeader(req)
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
		if !decodeV1JSON(w, r, &req) {
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRootFor(r), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := s.ctr.Media.DownloadGalleryImage(req)
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
		if !decodeV1JSON(w, r, &req) {
			return
		}
		outputDir, err := cleanLibraryPath(s.libraryRootFor(r), req.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		req.OutputDir = outputDir
		result, err := s.ctr.Media.DownloadAvatar(req)
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
		osInfo, _ := s.ctr.System.GetOSInfo()
		configPath, _ := s.ctr.System.GetConfigPath()
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
		installed, err := s.ctr.Audio.IsFFmpegInstalled()
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		ffprobeInstalled, _ := s.ctr.Audio.IsFFprobeInstalled()
		ffmpegPath, _ := s.ctr.Audio.GetFFmpegPath()
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
		writeV1JSON(w, http.StatusOK, s.ctr.System.GetDefaults())
	}))
}
