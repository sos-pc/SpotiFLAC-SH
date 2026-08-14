package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Spotify profiles, for the playlist picker
// ─────────────────────────────────────────────────────────────────────────────
//
// The picker offers three sources: your own playlists (OAuth, not built yet),
// someone's public profile, and a URL. These two routes serve the middle one,
// which needs no account at all — the existing anonymous web-player token
// reaches both, so a household member can watch a friend's public playlists
// without anyone declaring a Spotify application.
//
// Read-only, and "read" rather than "manage": nothing here downloads anything.

import (
	"net/http"
	"strconv"

	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
)

func (s *Server) registerSpotifyRoutes() {
	// Finding SOMEONE ELSE. It is a poor way to find yourself — display names
	// are not unique and ids are opaque, so "marc" answers with ten profiles
	// most of which are literally named Marc — and it is unnecessary for that
	// anyway once an account is connected.
	s.mux.Handle("GET /api/v1/spotify/profiles", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		q := r.URL.Query().Get("q")
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
		profiles, err := spotify.SearchProfiles(r.Context(), q, limit)
		if err != nil {
			writeV1Error(w, http.StatusBadGateway, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]interface{}{"profiles": profiles})
	}))

	// Every public playlist on one profile, walked to exhaustion. The walk can
	// take several calls against a profile with hundreds, so this is one
	// request that blocks rather than a paginated endpoint the client would
	// have to drive: the pagination rules are subtle enough (see
	// listProfilePlaylists) that duplicating them in TypeScript is how they
	// would drift.
	s.mux.Handle("GET /api/v1/spotify/profiles/{id}/playlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeV1Error(w, http.StatusBadRequest, "profile id required")
			return
		}
		entries, err := spotify.ListProfilePlaylists(r.Context(), id)
		if err != nil {
			// Upstream's failure, not the caller's: a profile that does not
			// exist, or Spotify refusing. 502 rather than 500 so it is not
			// mistaken for a bug on this side.
			writeV1Error(w, http.StatusBadGateway, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]interface{}{"playlists": entries})
	}))
}
