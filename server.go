package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend"
)

// ─────────────────────────────────────────────────────────────────────────────
// Embedded frontend
// ─────────────────────────────────────────────────────────────────────────────

//go:embed all:frontend/dist
var frontendFS embed.FS

// ─────────────────────────────────────────────────────────────────────────────
// RPC
// ─────────────────────────────────────────────────────────────────────────────

type rpcRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcResponse struct {
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

type HandlerFunc func(params json.RawMessage, user *JWTClaims) (interface{}, error)

// ─────────────────────────────────────────────────────────────────────────────
// Server
// ─────────────────────────────────────────────────────────────────────────────

type Server struct {
	app      *App
	registry map[string]HandlerFunc
	mux      *http.ServeMux
}

func NewServer(app *App) *Server {
	s := &Server{
		app:      app,
		registry: make(map[string]HandlerFunc),
		mux:      http.NewServeMux(),
	}
	s.registerHandlers()
	s.registerRoutes()
	return s
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	auth := GetAuthManager()
	if auth == nil {
		http.Error(w, `{"error":"auth not initialized"}`, http.StatusInternalServerError)
		return
	}
	profile, err := auth.AuthenticateWithJellyfin(req.Username, req.Password)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	token, err := GenerateJWT(profile)
	if err != nil {
		http.Error(w, `{"error":"failed to generate token"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token": token,
		"user": map[string]interface{}{
			"id":           profile.ID,
			"display_name": profile.DisplayName,
			"is_admin":     profile.IsAdmin,
		},
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	token := ""
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token = auth[7:]
	}
	if token == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	claims, err := ValidateJWT(token)
	if err != nil {
		http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           claims.UserID,
		"display_name": claims.DisplayName,
		"is_admin":     claims.IsAdmin,
	})
}

func checkWatchlistOwnership(watchlistID string, user *JWTClaims) error {
	if user == nil {
		return fmt.Errorf("unauthorized")
	}
	pl, err := GetWatcher().getWatchlistByID(watchlistID)
	if err != nil {
		return fmt.Errorf("watchlist not found")
	}
	if pl.UserID != "" && pl.UserID != user.UserID && !user.IsAdmin {
		return fmt.Errorf("access denied")
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ─────────────────────────────────────────────────────────────────────────────
// Routes
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/auth/login", s.handleLogin)
	s.mux.HandleFunc("/auth/me", s.handleMe)
	s.mux.Handle("/api/rpc", RequireAuth(http.HandlerFunc(s.handleRPC)))
	s.mux.Handle("/api/upload", RequireAuth(http.HandlerFunc(s.handleUpload)))

	distFS, err := fs.Sub(frontendFS, "frontend/dist")
	if err != nil {
		panic(fmt.Sprintf("failed to get frontend/dist: %v", err))
	}
	fileServer := http.FileServer(http.FS(distFS))

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(distFS, path); err != nil {
			// SPA fallback → index.html
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler RPC
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	handler, ok := s.registry[req.Method]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown method: %s", req.Method))
		return
	}

	claims := GetUserFromContext(r)
	result, err := handler(req.Params, claims)
	if err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, rpcResponse{Result: result})
}

// ─────────────────────────────────────────────────────────────────────────────
// Registry
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerHandlers() {
	a := s.app

	// ── Background download ───────────────────────────────────────────────
	s.handle("EnqueueBatch", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req EnqueueBatchRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		if user != nil {
			req.UserID = user.UserID
		}
		return GetJobManager().EnqueueBatch(req)
	})

	// ── Watchlist ─────────────────────────────────────────────────────────
	s.handle("AddToWatchlist", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req AddWatchlistRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		if user != nil {
			req.UserID = user.UserID
		}
		return GetWatcher().AddWatchlist(req)
	})

	s.handle("RemoveFromWatchlist", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		if err := checkWatchlistOwnership(params.ID, user); err != nil {
			return nil, err
		}
		return nil, GetWatcher().RemoveWatchlist(params.ID)
	})

	s.handle("GetWatchlists", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return GetWatcher().GetWatchlistsByUser(userID)
	})

	s.handle("MigrateWatchlistUser", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req struct {
			ID     string `json:"id"`
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		// Seulement admin peut migrer
		if user == nil || !user.IsAdmin {
			return nil, fmt.Errorf("admin required")
		}
		playlists, err := GetWatcher().GetWatchlists()
		if err != nil {
			return nil, err
		}
		for _, pl := range playlists {
			if pl.ID == req.ID {
				pl.UserID = req.UserID
				return map[string]bool{"ok": true}, GetWatcher().saveWatchlist(&pl)
			}
		}
		return nil, fmt.Errorf("watchlist not found")
	})

	s.handle("UpdateWatchlist", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req UpdateWatchlistRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		if err := checkWatchlistOwnership(req.ID, user); err != nil {
			return nil, err
		}
		return nil, GetWatcher().UpdateWatchlist(req)
	})

	s.handle("GetWatchlistStats", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		if err := checkWatchlistOwnership(params.ID, user); err != nil {
			return nil, err
		}
		return GetWatcher().GetWatchlistStats(params.ID)
	})

	s.handle("GetWatchlistHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		if err := checkWatchlistOwnership(params.ID, user); err != nil {
			return nil, err
		}
		return GetWatcher().GetWatchlistHistory(params.ID)
	})

	s.handle("CleanupOldJobs", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		jm := GetJobManager()
		if jm == nil {
			return map[string]int{"deleted": 0}, nil
		}
		deleted, err := jm.CleanupOldJobs()
		if err != nil {
			return nil, err
		}
		return map[string]int{"deleted": deleted}, nil
	})

	s.handle("RedownloadWatchlist", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		if err := checkWatchlistOwnership(params.ID, user); err != nil {
			return nil, err
		}
		return nil, GetWatcher().RedownloadWatchlist(params.ID)
	})

	s.handle("ForceSyncWatchlist", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		if err := checkWatchlistOwnership(params.ID, user); err != nil {
			return nil, err
		}
		return nil, GetWatcher().ForceSyncWatchlist(params.ID)
	})

	// ── Queue / Progress ──────────────────────────────────────────────────
	s.handle("GetDownloadQueue", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		queue := a.GetDownloadQueue()
		if user == nil {
			return queue, nil
		}
		// Filtrer les items par userID
		jm := GetJobManager()
		if jm == nil {
			return queue, nil
		}
		jobs, err := jm.GetAllJobs()
		if err != nil {
			return queue, nil
		}
		allowedIDs := make(map[string]bool)
		for _, j := range jobs {
			if j.UserID == "" || j.UserID == user.UserID {
				allowedIDs[j.ID] = true
			}
		}
		filtered := queue.Queue[:0]
		for _, item := range queue.Queue {
			if allowedIDs[item.ID] {
				filtered = append(filtered, item)
			}
		}
		queue.Queue = filtered
		return queue, nil
	})

	s.handle("GetDownloadProgress", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.GetDownloadProgress(), nil
	})

	s.handle("ClearCompletedDownloads", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		a.ClearCompletedDownloads()
		return nil, nil
	})

	s.handle("ClearAllDownloads", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		a.ClearAllDownloads()
		return nil, nil
	})

	s.handle("CancelAllQueuedItems", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		a.CancelAllQueuedItems()
		return nil, nil
	})

	s.handle("AddToDownloadQueue", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			SpotifyID  string `json:"spotify_id"`
			TrackName  string `json:"track_name"`
			ArtistName string `json:"artist_name"`
			AlbumName  string `json:"album_name"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.AddToDownloadQueue(params.SpotifyID, params.TrackName, params.ArtistName, params.AlbumName), nil
	})

	s.handle("SkipDownloadItem", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ItemID   string `json:"item_id"`
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		a.SkipDownloadItem(params.ItemID, params.FilePath)
		return nil, nil
	})

	s.handle("MarkDownloadItemFailed", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ItemID   string `json:"item_id"`
			ErrorMsg string `json:"error_msg"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		a.MarkDownloadItemFailed(params.ItemID, params.ErrorMsg)
		return nil, nil
	})

	// ── Spotify metadata ──────────────────────────────────────────────────
	s.handle("GetSpotifyMetadata", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req SpotifyMetadataRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.GetSpotifyMetadata(req)
	})

	s.handle("GetStreamingURLs", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			SpotifyTrackID string `json:"spotify_track_id"`
			Region         string `json:"region"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.GetStreamingURLs(params.SpotifyTrackID, params.Region)
	})

	s.handle("CheckTrackAvailability", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			SpotifyTrackID string `json:"spotify_track_id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.CheckTrackAvailability(params.SpotifyTrackID)
	})

	s.handle("SearchSpotify", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req SpotifySearchRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.SearchSpotify(req)
	})

	s.handle("SearchSpotifyByType", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req SpotifySearchByTypeRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.SearchSpotifyByType(req)
	})

	s.handle("GetPreviewURL", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			TrackID string `json:"track_id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.GetPreviewURL(params.TrackID)
	})

	// ── Download direct (track unique, conservé pour compatibilité) ───────
	s.handle("DownloadTrack", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req DownloadRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.DownloadTrack(req)
	})

	// ── Lyrics / Cover / Media ────────────────────────────────────────────
	s.handle("DownloadLyrics", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req LyricsDownloadRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.DownloadLyrics(req)
	})

	s.handle("DownloadCover", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req CoverDownloadRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.DownloadCover(req)
	})

	s.handle("DownloadHeader", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req HeaderDownloadRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.DownloadHeader(req)
	})

	s.handle("DownloadGalleryImage", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req GalleryImageDownloadRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.DownloadGalleryImage(req)
	})

	s.handle("DownloadAvatar", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req AvatarDownloadRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.DownloadAvatar(req)
	})

	// ── History ───────────────────────────────────────────────────────────
	s.handle("GetDownloadHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.GetDownloadHistory()
	})

	s.handle("ClearDownloadHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return nil, a.ClearDownloadHistory()
	})

	s.handle("DeleteDownloadHistoryItem", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return nil, a.DeleteDownloadHistoryItem(params.ID)
	})

	s.handle("GetFetchHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.GetFetchHistory()
	})

	s.handle("AddFetchHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var item backend.FetchHistoryItem
		if err := json.Unmarshal(p, &item); err != nil {
			return nil, err
		}
		return nil, a.AddFetchHistory(item)
	})

	s.handle("ClearFetchHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return nil, a.ClearFetchHistory()
	})

	s.handle("ClearFetchHistoryByType", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ItemType string `json:"item_type"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return nil, a.ClearFetchHistoryByType(params.ItemType)
	})

	s.handle("DeleteFetchHistoryItem", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return nil, a.DeleteFetchHistoryItem(params.ID)
	})

	// ── Settings ──────────────────────────────────────────────────────────
	s.handle("LoadSettings", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		// Settings par user si authentifié
		if user != nil {
			auth := GetAuthManager()
			if auth != nil {
				profile, err := auth.GetUser(user.UserID)
				if err == nil && profile != nil && len(profile.Settings) > 0 {
					return profile.Settings, nil
				}
			}
		}
		return a.LoadSettings()
	})

	s.handle("SaveSettings", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			Settings map[string]interface{} `json:"settings"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		// Sauver dans user profile si authentifié
		if user != nil {
			auth := GetAuthManager()
			if auth != nil {
				return nil, auth.SaveUserSettings(user.UserID, params.Settings)
			}
		}
		// Fallback global config.json
		return nil, a.SaveSettings(params.Settings)
	})

	s.handle("GetDefaults", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.GetDefaults(), nil
	})

	s.handle("GetConfigPath", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.GetConfigPath()
	})

	s.handle("GetOSInfo", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.GetOSInfo()
	})

	// ── FFmpeg ────────────────────────────────────────────────────────────
	s.handle("IsFFmpegInstalled", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.IsFFmpegInstalled()
	})

	s.handle("IsFFprobeInstalled", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.IsFFprobeInstalled()
	})

	s.handle("CheckFFmpegInstalled", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.CheckFFmpegInstalled()
	})

	s.handle("GetFFmpegPath", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.GetFFmpegPath()
	})

	s.handle("DownloadFFmpeg", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.DownloadFFmpeg(), nil
	})

	// ── Audio / File tools ────────────────────────────────────────────────
	s.handle("ConvertAudio", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var req ConvertAudioRequest
		if err := json.Unmarshal(p, &req); err != nil {
			return nil, err
		}
		return a.ConvertAudio(req)
	})

	s.handle("AnalyzeTrack", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.AnalyzeTrack(params.FilePath)
	})

	s.handle("AnalyzeMultipleTracks", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.AnalyzeMultipleTracks(params.FilePaths)
	})

	s.handle("GetFileSizes", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.GetFileSizes(params.FilePaths), nil
	})

	s.handle("ListDirectoryFiles", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			DirPath string `json:"dir_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.ListDirectoryFiles(params.DirPath)
	})

	s.handle("ListAudioFilesInDir", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			DirPath string `json:"dir_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.ListAudioFilesInDir(params.DirPath)
	})

	s.handle("ReadFileMetadata", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.ReadFileMetadata(params.FilePath)
	})

	s.handle("ReadImageAsBase64", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.ReadImageAsBase64(params.FilePath)
	})

	s.handle("ReadTextFile", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.ReadTextFile(params.FilePath)
	})

	s.handle("RenameFileTo", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			OldPath string `json:"old_path"`
			NewName string `json:"new_name"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return nil, a.RenameFileTo(params.OldPath, params.NewName)
	})

	s.handle("RenameFilesByMetadata", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.RenameFilesByMetadata(params.Files, params.Format), nil
	})

	s.handle("PreviewRenameFiles", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.PreviewRenameFiles(params.Files, params.Format), nil
	})

	s.handle("UploadImage", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.UploadImage(params.FilePath)
	})

	s.handle("UploadImageBytes", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			Filename   string `json:"filename"`
			Base64Data string `json:"base64_data"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.UploadImageBytes(params.Filename, params.Base64Data)
	})

	s.handle("CreateM3U8File", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			M3U8Name          string   `json:"m3u8_name"`
			OutputDir         string   `json:"output_dir"`
			FilePaths         []string `json:"file_paths"`
			JellyfinMusicPath string   `json:"jellyfin_music_path"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return nil, a.CreateM3U8File(params.M3U8Name, params.OutputDir, params.FilePaths, params.JellyfinMusicPath)
	})

	s.handle("CheckFilesExistence", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			OutputDir string                      `json:"output_dir"`
			RootDir   string                      `json:"root_dir"`
			Tracks    []CheckFileExistenceRequest `json:"tracks"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		return a.CheckFilesExistence(params.OutputDir, params.RootDir, params.Tracks), nil
	})

	s.handle("ExportFailedDownloads", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return a.ExportFailedDownloads()
	})

	// ── Path helpers (pas de dialog natif en mode web) ────────────────────
	s.handle("GetUserHomeDir", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return os.UserHomeDir()
	})

	s.handle("GetPathSeparator", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return string(filepath.Separator), nil
	})

	s.handle("SelectFolder", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return "", nil
	})

	s.handle("SelectFile", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return "", nil
	})
}

func (s *Server) handle(method string, fn HandlerFunc) {
	s.registry[method] = fn
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP helpers
// ─────────────────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, rpcResponse{Error: msg})
}

// ─────────────────────────────────────────────────────────────────────────────
// Upload handler
// ─────────────────────────────────────────────────────────────────────────────
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		http.Error(w, "failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	tmpPath := filepath.Join(os.TempDir(), "spotiflac_upload_"+header.Filename)
	dst, err := os.Create(tmpPath)
	if err != nil {
		http.Error(w, "failed to create temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()
	buf := make([]byte, 32*1024)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				http.Error(w, "write error: "+writeErr.Error(), http.StatusInternalServerError)
				return
			}
		}
		if readErr != nil { break }
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": tmpPath})
}
