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
	"log/slog"
	"net/http"
	"strconv"

	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
	"github.com/sos-pc/SpotiFLAC-SH/internal/settings"
	"github.com/sos-pc/SpotiFLAC-SH/internal/spotifyoauth"
)

// requestOrigin is the address the caller actually reached this app on, which
// is what the redirect URI has to be built from - Spotify matches it exactly,
// against what the operator registered.
//
// Behind the reference deployment's nginx, r.TLS is nil and r.Host is the
// public name, so the scheme has to come from the proxy's header. That header
// is only trusted when TRUST_PROXY_HEADERS says so, for the same reason the
// rate limiter does not trust it by default: anything a client can set is
// something a client can lie about.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if trustProxyHeaders() {
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host
}

// instanceClientID reads the Spotify application this deployment authenticates
// against. Instance-scoped, so userID is deliberately empty: a household member
// cannot point the connection at an application of their own.
func (s *Server) instanceClientID() string {
	v, _ := settings.EffectiveBlob(s.ctr.Auth, "")["spotifyClientId"].(string)
	return v
}

func (s *Server) registerSpotifyRoutes() {

	// ── Connecting an account ─────────────────────────────────────────────

	s.mux.Handle("GET /api/v1/spotify/connection", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		out := map[string]interface{}{
			// Whether the OPERATOR has configured an application at all. The
			// screen needs to tell "your administrator has not set this up"
			// apart from "you have not connected yet" — a greyed button with
			// neither explanation is the failure this avoids.
			"configured":   s.instanceClientID() != "",
			"redirect_uri": spotifyoauth.RedirectURI(requestOrigin(r)),
			"connected":    false,
		}
		if c, err := s.ctr.SpotifyOAuth.Get(userIDFromContext(r)); err == nil && c != nil {
			out["connected"] = true
			out["display_name"] = c.DisplayName
			out["spotify_id"] = c.SpotifyID
			// The screen can warn before a finite refresh-token lifetime runs
			// out rather than after, when the only symptom is an empty list.
			out["connected_at"] = c.ConnectedAt
		}
		writeV1JSON(w, http.StatusOK, out)
	}))

	s.mux.Handle("POST /api/v1/spotify/connection", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		url, err := s.ctr.SpotifyOAuth.AuthorizeURL(
			s.instanceClientID(), spotifyoauth.RedirectURI(requestOrigin(r)), userIDFromContext(r))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		// The URL rather than a redirect: this is called by fetch(), and a 302
		// answered to XHR would be followed by the browser without ever
		// reaching the address bar.
		writeV1JSON(w, http.StatusOK, map[string]string{"authorize_url": url})
	}))

	s.mux.Handle("DELETE /api/v1/spotify/connection", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		if err := s.ctr.SpotifyOAuth.Delete(userIDFromContext(r)); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// The callback is deliberately NOT behind v1Auth.
	//
	// It arrives as a top-level navigation from accounts.spotify.com, so it
	// carries no Authorization header — the app's token lives in localStorage
	// and a redirect cannot reach it. Identity comes from the state parameter
	// instead, which is what state is for: it was minted for one user, it is
	// single-use, and an unknown one is refused.
	//
	// It answers with a redirect rather than JSON because a person is looking
	// at it, not a script.
	s.mux.HandleFunc("GET /api/v1/spotify/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			// The user pressed Cancel on Spotify's consent screen. Not a
			// failure of ours, and not something to show a stack trace for.
			http.Redirect(w, r, "/?spotify=declined", http.StatusSeeOther)
			return
		}
		userID, err := s.ctr.SpotifyOAuth.Exchange(r.Context(),
			s.instanceClientID(), spotifyoauth.RedirectURI(requestOrigin(r)),
			q.Get("code"), q.Get("state"))
		if err != nil {
			slog.Warn("[Spotify] Authorization failed", "err", err)
			http.Redirect(w, r, "/?spotify=failed", http.StatusSeeOther)
			return
		}
		slog.Info("[Spotify] Account connected", "user", userID)
		http.Redirect(w, r, "/?spotify=connected", http.StatusSeeOther)
	})

	// The connected account's own playlists — private and collaborative
	// included, and the only source that can report a track count.
	//
	// Every failure here is 502 rather than 500 for the same reason as the
	// profile walk, plus one specific to this route: a 401 from Spotify means
	// the refresh produced a token it will not accept, which is the account
	// having been disconnected on their side. That is upstream's answer, and
	// the screen should offer to reconnect rather than report a bug.
	s.mux.Handle("GET /api/v1/spotify/me/playlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		userID := userIDFromContext(r)
		conn, err := s.ctr.SpotifyOAuth.Get(userID)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		if conn == nil {
			// Not an error state to be logged: it is simply what a user who has
			// not connected looks like, and the picker asks before it knows.
			writeV1Error(w, http.StatusPreconditionRequired, "this account is not connected to Spotify")
			return
		}
		token, err := s.ctr.SpotifyOAuth.AccessToken(r.Context(), s.instanceClientID(), userID)
		if err != nil {
			writeV1Error(w, http.StatusBadGateway, err.Error())
			return
		}
		entries, err := spotify.ListMyPlaylists(r.Context(), token, conn.SpotifyID)
		if err != nil {
			writeV1Error(w, http.StatusBadGateway, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]interface{}{"playlists": entries})
	}))

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
