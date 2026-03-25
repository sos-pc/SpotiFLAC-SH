package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
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

// ─────────────────────────────────────────────────────────────────────────────
// FIX #12 — Helper CORS centralisé (évite la duplication dans chaque handler)
// ─────────────────────────────────────────────────────────────────────────────

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// ─────────────────────────────────────────────────────────────────────────────
// FIX #2 — Middleware CORS pour gérer les preflight OPTIONS
// (sans ça, RequireAuth renvoyait 401 sur toutes les requêtes OPTIONS)
// ─────────────────────────────────────────────────────────────────────────────

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Local bypass — DISABLE_AUTH_ON_LAN=true
// ─────────────────────────────────────────────────────────────────────────────

func isLocalIP(r *http.Request) bool {
	// Si la requête vient via un reverse proxy (SWAG), X-Forwarded-For est présent.
	// Dans ce cas on refuse le bypass même si RemoteAddr est une IP privée.
	// Accès direct (LAN, SSH tunnel localhost) → pas de X-Forwarded-For → on vérifie RemoteAddr.
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	parsed := net.ParseIP(host)
	if parsed == nil {
		return false
	}
	// Loopback + RFC-1918 + Docker bridge (172.16/12 couvre 172.17.0.1)
	privateRanges := []string{
		"127.0.0.0/8",
		"::1/128",
		"10.0.0.0/8",
		"192.168.0.0/16",
		"172.16.0.0/12",
	}
	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(parsed) {
			return true
		}
	}
	return false
}

func localBypassEnabled() bool {
	return os.Getenv("DISABLE_AUTH_ON_LAN") == "true"
}

// localBypassMiddleware injecte un JWT admin synthétique si la requête vient
// d'une IP locale et que DISABLE_AUTH_ON_LAN=true.
// Remplace RequireAuth sur les routes protégées dans ce cas.
func localBypassMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if localBypassEnabled() && isLocalIP(r) {
			// Pas de token dans la requête → injecter un user admin local
			if r.Header.Get("Authorization") == "" {
				profile := &UserProfile{
					ID:          "local-admin",
					DisplayName: "Local Admin",
					IsAdmin:     true,
				}
				token, err := GenerateJWT(profile)
				if err == nil {
					r.Header.Set("Authorization", "Bearer "+token)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleLocalAuth retourne un JWT admin si DISABLE_AUTH_ON_LAN=true et IP locale.
// Appelé par le frontend au démarrage pour bypass automatique.
func (s *Server) handleLocalAuth(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !localBypassEnabled() || !isLocalIP(r) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "local bypass not enabled"})
		return
	}
	profile := &UserProfile{ID: "local-admin", DisplayName: "Local Admin", IsAdmin: true}
	token, err := GenerateJWT(profile)
	if err != nil {
		http.Error(w, `{"error":"failed to generate token"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token": token,
		"user":  map[string]interface{}{"id": profile.ID, "display_name": profile.DisplayName, "is_admin": profile.IsAdmin},
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// FIX #12 — utilise le helper centralisé
	setCORSHeaders(w)
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

// FIX #11 — handleMe utilise désormais GetUserFromContext (plus de logique JWT dupliquée)
// La route est wrappée par corsMiddleware + RequireAuth dans registerRoutes
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	claims := GetUserFromContext(r)
	if claims == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
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
	s.mux.HandleFunc("/auth/local", s.handleLocalAuth)
	s.mux.Handle("/auth/me", corsMiddleware(localBypassMiddleware(RequireAuth(http.HandlerFunc(s.handleMe)))))
	s.mux.Handle("/api/rpc", corsMiddleware(localBypassMiddleware(RequireAuth(http.HandlerFunc(s.handleRPC)))))
	s.mux.Handle("/api/upload", corsMiddleware(localBypassMiddleware(RequireAuth(http.HandlerFunc(s.handleUpload)))))
	s.mux.Handle("/api/auth/tidal/url", corsMiddleware(localBypassMiddleware(RequireAuth(http.HandlerFunc(s.handleTidalAuthURL)))))
	s.mux.Handle("/api/auth/tidal/callback", corsMiddleware(localBypassMiddleware(RequireAuth(http.HandlerFunc(s.handleTidalAuthCallback)))))

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

func (s *Server) handleTidalAuthURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	url := backend.GenerateTidalAuthURL()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}

func (s *Server) handleTidalAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := backend.ExchangeTidalAuthCode(req.URL); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
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

	// FIX #13 — CleanupOldJobs restreint aux admins
	s.handle("CleanupOldJobs", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		if user == nil || !user.IsAdmin {
			return nil, fmt.Errorf("admin required")
		}
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

	// SyncWatchlist — nouveaux tracks Spotify + retry des jobs failed
	s.handle("SyncWatchlist", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		if err := checkWatchlistOwnership(params.ID, user); err != nil {
			return nil, err
		}
		return nil, GetWatcher().SyncWatchlist(params.ID)
	})

	// Gardés pour compatibilité ascendante (anciens clients)
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
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return a.GetDownloadHistory(userID)
	})

	s.handle("ClearDownloadHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return nil, a.ClearDownloadHistory(userID)
	})

	s.handle("DeleteDownloadHistoryItem", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return nil, a.DeleteDownloadHistoryItem(params.ID, userID)
	})

	s.handle("GetFetchHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return a.GetFetchHistory(userID)
	})

	s.handle("AddFetchHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var item backend.FetchHistoryItem
		if err := json.Unmarshal(p, &item); err != nil {
			return nil, err
		}
		// Forcer UserID depuis JWT, ignore ce que le frontend envoie
		if user != nil {
			item.UserID = user.UserID
		}
		return nil, a.AddFetchHistory(item)
	})

	s.handle("ClearFetchHistory", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return nil, a.ClearFetchHistory(userID)
	})

	s.handle("ClearFetchHistoryByType", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ItemType string `json:"item_type"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return nil, a.ClearFetchHistoryByType(params.ItemType, userID)
	})

	s.handle("DeleteFetchHistoryItem", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, err
		}
		userID := ""
		if user != nil {
			userID = user.UserID
		}
		return nil, a.DeleteFetchHistoryItem(params.ID, userID)
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

	// FIX #10 — Retourne une erreur explicite au lieu d'une chaîne vide silencieuse
	s.handle("SelectFolder", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return nil, fmt.Errorf("not supported in web mode")
	})

	s.handle("SelectFile", func(p json.RawMessage, user *JWTClaims) (interface{}, error) {
		return nil, fmt.Errorf("not supported in web mode")
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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

	// FIX #16 — filepath.Base() pour éviter tout path traversal via header.Filename
	safeFilename := filepath.Base(header.Filename)
	tmpPath := filepath.Join(os.TempDir(), "spotiflac_upload_"+safeFilename)

	dst, err := os.Create(tmpPath)
	if err != nil {
		http.Error(w, "failed to create temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// FIX #15 — io.Copy remplace la boucle manuelle (plus simple, gère toutes les erreurs)
	// FIX #1  — Le fichier temporaire sera nettoyé par le consommateur (app.go) après usage.
	//           Un nettoyage ici supprimerait le fichier avant que le handler appelant ne le lise.
	//           TODO : faire un defer os.Remove(tmpPath) côté consommateur après traitement.
	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "failed to write temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": tmpPath})
}
