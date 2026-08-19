package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Embedded frontend
// ─────────────────────────────────────────────────────────────────────────────

//go:embed all:frontend/dist
var frontendFS embed.FS

// ─────────────────────────────────────────────────────────────────────────────
// Server
// ─────────────────────────────────────────────────────────────────────────────

type Server struct {
	ctr     *Container
	mux     *http.ServeMux
	loginRL *LoginRateLimiter
	uploads *uploadRegistry
}

func NewServer(ctr *Container) *Server {
	s := &Server{
		ctr:     ctr,
		mux:     http.NewServeMux(),
		loginRL: NewLoginRateLimiter(),
		uploads: newUploadRegistry(),
	}
	s.registerRoutes()
	s.registerV1Routes()
	return s
}

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

func (s *Server) checkWatchlistOwnership(watchlistID string, user *auth.JWTClaims) error {
	if user == nil {
		return fmt.Errorf("unauthorized")
	}
	pl, err := s.ctr.Watcher.GetWatchlistByID(watchlistID)
	if err != nil {
		return fmt.Errorf("watchlist not found")
	}
	if pl.UserID != user.UserID && !user.IsAdmin {
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

// writeV1JSON / writeV1Error live here rather than in api_v1.go because
// server.go and sse.go call them too: leaving the writers with the v1 handlers
// meant the server layer depended on the API layer for its own error
// responses, which is one half of an import cycle once these split.
// RequireAuth is a *Server method, so it belongs with the server rather than
// with the AuthManager it calls into. Living in authHeader.go made the authHeader layer
// name the server type — the other half of the authHeader<->server cycle.
// RequireAuth mirrors v1Auth's JWT check, including the live TokenVersion
// revocation comparison — without it, a demoted/disabled admin's existing
// JWT would keep working here up to its full 24h expiry even after
// GetOrCreateUser bumped TokenVersion specifically to invalidate it
// everywhere else.
func (s *Server) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = authHeader[7:]
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		claims, err := auth.ValidateJWT(token)
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		if !claims.IsAPIKey && s.ctr.Auth != nil {
			if profile, err := s.ctr.Auth.GetUser(claims.UserID); err == nil && profile.TokenVersion != claims.TokenVersion {
				http.Error(w, `{"error":"session revoked"}`, http.StatusUnauthorized)
				return
			}
		}
		ctx := auth.WithUser(r.Context(), claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeV1JSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeV1Error(w http.ResponseWriter, status int, msg string) {
	writeV1JSON(w, status, map[string]string{"error": msg})
}

func (s *Server) registerRoutes() {
	s.mux.Handle("/api/upload", corsMiddleware(s.RequireAuth(http.HandlerFunc(s.handleUpload))))

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
			// An unmatched /api/ path must not fall through to the SPA. It did
			// until 2026-07-19, so a typo'd endpoint answered 200 with an HTML
			// page: a client asking for JSON got "success" and a web page, and
			// had to parse the body to discover the route does not exist. Same
			// failure mode as silently ignoring an unknown query parameter —
			// answering plausibly and wrongly is worse than answering "no".
			//
			// Checked here rather than before the Stat so a static asset that
			// genuinely lives under api/ would still be served.
			if strings.HasPrefix(path, "api/") {
				writeV1Error(w, http.StatusNotFound, "no such endpoint: "+r.URL.Path)
				return
			}
			// SPA fallback → index.html
			r.URL.Path = "/"
			path = "index.html"
		}
		if strings.HasPrefix(path, "assets/") {
			// Vite content-hashes these filenames, so any code change
			// produces a new URL — safe to cache indefinitely.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// index.html (and every SPA-fallback route) must always be
			// revalidated. embed.FS files carry no Last-Modified/ETag, so
			// without this a browser can heuristically cache index.html
			// across a redeploy, keep referencing asset filenames that no
			// longer exist in the new image, and silently run stale
			// frontend code against the new backend — e.g. an old handler
			// reading a response field the new API no longer sends.
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// uploadRegistry tracks the temp paths handleUpload has handed back to
// clients, so analyze/convert (see cleanUploadOrLibraryPath in api_v1.go)
// can accept exactly those paths without reopening the music-library path
// confinement to arbitrary filesystem locations — membership is exact-match
// against a path this server itself generated, not a prefix/pattern guess.
type uploadRegistry struct {
	mu    sync.Mutex
	paths map[string]struct{}
}

func newUploadRegistry() *uploadRegistry {
	return &uploadRegistry{paths: make(map[string]struct{})}
}

func (u *uploadRegistry) add(path string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.paths[path] = struct{}{}
}

func (u *uploadRegistry) remove(path string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.paths, path)
}

func (u *uploadRegistry) contains(path string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, ok := u.paths[path]
	return ok
}

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

	// os.CreateTemp's "*" is replaced with a random string, so two concurrent
	// uploads of a file with the same original name can no longer collide on
	// the exact same path (the previous os.Create(fixed name) truncated and
	// reused whatever was there).
	dst, err := os.CreateTemp(os.TempDir(), "spotiflac_upload_*_"+safeFilename)
	if err != nil {
		http.Error(w, "failed to create temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpPath := dst.Name()
	defer dst.Close()

	// FIX #15 — io.Copy remplace la boucle manuelle (plus simple, gère toutes les erreurs)
	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "failed to write temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Registered so analyze/convert can recognize and accept this exact path
	// (see cleanUploadOrLibraryPath) even though it lives outside the music
	// library root. The consumer reads it within seconds of the upload
	// completing — no caller ever cleaned it up itself (this was a standing
	// TODO), so schedule a bounded cleanup here instead of leaking one file
	// per upload into the OS temp dir forever.
	s.uploads.add(tmpPath)
	time.AfterFunc(10*time.Minute, func() {
		s.uploads.remove(tmpPath)
		os.Remove(tmpPath)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": tmpPath})
}
