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
	app      *App
	ctr      *Container
	mux      *http.ServeMux
	loginRL  *LoginRateLimiter
}

func NewServer(app *App, ctr *Container) *Server {
	s := &Server{
		app:     app,
		ctr:     ctr,
		mux:     http.NewServeMux(),
		loginRL: NewLoginRateLimiter(),
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

func (s *Server) checkWatchlistOwnership(watchlistID string, user *JWTClaims) error {
	if user == nil {
		return fmt.Errorf("unauthorized")
	}
	pl, err := s.ctr.Watcher.getWatchlistByID(watchlistID)
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

func (s *Server) registerRoutes() {
	s.mux.Handle("/api/upload", corsMiddleware(localBypassMiddleware(RequireAuth(http.HandlerFunc(s.handleUpload)))))

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
