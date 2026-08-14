// Package spotifyoauth connects a user's own Spotify account, so the playlist
// picker can offer their private and collaborative playlists as well as the
// public ones a profile exposes.
//
// Authorization Code + PKCE, and no client secret anywhere. A self-hosted
// deployment has nowhere good to keep one — it would sit in the same config
// file the operator pastes into a chat when asking for help — and PKCE removes
// the need entirely: the proof is a random verifier this process generates per
// attempt and never transmits until the exchange.
//
// What IS stored, per user, is a refresh token. That is a credential, so it
// lives in its own Bolt bucket rather than in the settings blob that the
// settings screen round-trips through the browser.
//
// Two constraints the operator meets, not us. A Spotify application stays in
// development mode unless it passes a review this project would fail — the
// developer policy forbids using the API in connection with downloading — so
// it is capped at 25 users, each added by hand in the dashboard with the email
// of their Spotify account. And refresh tokens can be given a finite lifetime
// (180 days on the reference deployment), after which reconnecting is the only
// remedy; the status endpoint reports when the connection was made so a screen
// can say so before it expires rather than after.
package spotifyoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketTokens = []byte("spotify_tokens")

// Scopes we ask for, and nothing more. Reading playlists is the whole feature;
// asking for modify or library scopes because they might be useful later is how
// a consent screen starts frightening people.
const scopes = "playlist-read-private playlist-read-collaborative"

const (
	authorizeEndpoint = "https://accounts.spotify.com/authorize"
	tokenEndpoint     = "https://accounts.spotify.com/api/token"
)

// Connection is what the app knows about one user's link to Spotify.
type Connection struct {
	UserID       string    `json:"user_id"`
	SpotifyID    string    `json:"spotify_id"`
	DisplayName  string    `json:"display_name"`
	RefreshToken string    `json:"refresh_token"`
	ConnectedAt  time.Time `json:"connected_at"`
}

// pending is one authorization in flight: the PKCE verifier that will prove the
// exchange belongs to the request that started it, and who started it.
//
// Held in memory, not in Bolt. It lives for the seconds between "Connect" and
// the browser coming back; persisting it would mean writing a secret to disk to
// protect a window that a restart closes anyway.
type pending struct {
	userID   string
	verifier string
	created  time.Time
}

// Store keeps connections, and the handful of authorizations in flight.
type Store struct {
	db *bolt.DB

	mu      sync.Mutex
	pending map[string]pending // keyed by the state parameter
}

func NewStore(db *bolt.DB) (*Store, error) {
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketTokens)
		return err
	}); err != nil {
		return nil, fmt.Errorf("spotify token bucket: %w", err)
	}
	return &Store{db: db, pending: map[string]pending{}}, nil
}

// pendingTTL bounds how long a half-finished authorization is remembered. Long
// enough for a user to read Spotify's consent screen and think about it, short
// enough that an abandoned attempt does not sit in memory for the life of the
// process.
const pendingTTL = 15 * time.Minute

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// challengeFor is the S256 transformation PKCE specifies: the verifier is sent
// only at the exchange, and only its hash travels in the URL the browser
// follows. Anyone who intercepts the redirect gets a code they cannot spend.
func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL starts an authorization for userID and returns where to send the
// browser.
func (s *Store) AuthorizeURL(clientID, redirectURI, userID string) (string, error) {
	if clientID == "" {
		return "", fmt.Errorf("no Spotify application configured for this instance")
	}
	if userID == "" {
		return "", fmt.Errorf("no authenticated user")
	}
	verifier, err := randomURLSafe(64)
	if err != nil {
		return "", err
	}
	state, err := randomURLSafe(24)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.sweepLocked()
	s.pending[state] = pending{userID: userID, verifier: verifier, created: time.Now()}
	s.mu.Unlock()

	q := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"scope":                 {scopes},
		"code_challenge_method": {"S256"},
		"code_challenge":        {challengeFor(verifier)},
	}
	return authorizeEndpoint + "?" + q.Encode(), nil
}

func (s *Store) sweepLocked() {
	cutoff := time.Now().Add(-pendingTTL)
	for k, p := range s.pending {
		if p.created.Before(cutoff) {
			delete(s.pending, k)
		}
	}
}

// take consumes a pending authorization. Single use: a state that has been
// redeemed cannot be replayed, which is the other half of what it is for.
func (s *Store) take(state string) (pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	p, ok := s.pending[state]
	if ok {
		delete(s.pending, state)
	}
	return p, ok
}

// Save writes a connection. Overwrites any previous one for that user:
// reconnecting is how an expired refresh token is replaced, so the second
// connection must win.
func (s *Store) Save(c Connection) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTokens)
		if b == nil {
			return fmt.Errorf("spotify token bucket missing")
		}
		return b.Put([]byte(c.UserID), data)
	})
}

// Get returns the connection for userID, or nil if there is none.
func (s *Store) Get(userID string) (*Connection, error) {
	var out *Connection
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTokens)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(userID))
		if raw == nil {
			return nil
		}
		var c Connection
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		out = &c
		return nil
	})
	return out, err
}

// Delete removes a connection. Disconnecting here does not revoke anything at
// Spotify — only the user can do that, from their account page — so the screen
// should say as much rather than implying a clean break.
func (s *Store) Delete(userID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTokens)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(userID))
	})
}

// RedirectURI builds the callback address from the origin the admin is actually
// on, which is the value they must register in the Spotify dashboard.
//
// Computing it rather than asking for it is the point: an exact-match redirect
// URI typed by hand is the step everyone gets wrong, and a mismatch surfaces as
// Spotify's own INVALID_CLIENT page with nothing pointing back here.
func RedirectURI(origin string) string {
	return strings.TrimRight(origin, "/") + "/api/v1/spotify/callback"
}

// tokenResponse is Spotify's answer at the token endpoint, for both the initial
// exchange and a refresh.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("spotify token response: %w", err)
	}
	if tr.Error != "" {
		// Spotify's own words. Its errors here are specific and actionable —
		// "invalid_grant", "redirect_uri mismatch" — and paraphrasing them into
		// "authorization failed" is how an operator ends up guessing.
		return nil, fmt.Errorf("spotify: %s: %s", tr.Error, tr.ErrorDesc)
	}
	return &tr, nil
}

// Exchange completes an authorization: it validates the state, spends the code
// with the verifier that started it, and stores the refresh token.
//
// Returns the user this authorization belonged to, so the caller does not have
// to trust the browser about whose it was.
func (s *Store) Exchange(ctx context.Context, clientID, redirectURI, code, state string) (string, error) {
	p, ok := s.take(state)
	if !ok {
		// Expired, already used, or forged. All three deserve the same answer:
		// telling them apart would confirm which states exist.
		return "", fmt.Errorf("this authorization is no longer valid — start again")
	}

	tr, err := postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {p.verifier},
	})
	if err != nil {
		return "", err
	}
	if tr.RefreshToken == "" {
		return "", fmt.Errorf("spotify returned no refresh token")
	}

	id, name, meErr := currentUser(ctx, tr.AccessToken)
	if meErr != nil {
		// Not fatal — the authorization worked and the refresh token is worth
		// keeping — but no longer silent, and EnsureIdentity will retry.
		slog.Warn("[Spotify] Connected, but could not read the account identity",
			"user", p.userID, "err", meErr)
	}
	if err := s.Save(Connection{
		UserID:       p.userID,
		SpotifyID:    id,
		DisplayName:  name,
		RefreshToken: tr.RefreshToken,
		ConnectedAt:  time.Now(),
	}); err != nil {
		return "", err
	}
	return p.userID, nil
}

// AccessToken exchanges a stored refresh token for a usable access token.
//
// Not cached: an access token lives an hour, the picker is opened rarely, and a
// cache here would be a second place for a credential to live for the sake of
// saving one request nobody is waiting on.
//
// Spotify may return a NEW refresh token on refresh; when it does, the old one
// stops working, so it is stored immediately rather than at the end of whatever
// the caller is doing.
func (s *Store) AccessToken(ctx context.Context, clientID, userID string) (string, error) {
	c, err := s.Get(userID)
	if err != nil {
		return "", err
	}
	if c == nil {
		return "", fmt.Errorf("this account is not connected to Spotify")
	}
	tr, err := postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
		"client_id":     {clientID},
	})
	if err != nil {
		return "", err
	}
	if tr.RefreshToken != "" && tr.RefreshToken != c.RefreshToken {
		c.RefreshToken = tr.RefreshToken
		if err := s.Save(*c); err != nil {
			return "", err
		}
	}
	return tr.AccessToken, nil
}

// currentUser asks who the token belongs to, so the screen can say "connected
// as X" rather than "connected", and — the part that actually matters — so
// ListMyPlaylists can tell the account's own playlists from the ones it merely
// follows.
//
// It used to swallow every failure and return two empty strings, described as
// "best-effort: a failure here must not fail an authorization that otherwise
// worked". The first real connection produced exactly that: a stored record
// with spotify_id and display_name empty, no line anywhere saying why, and a
// picker that would have shown every playlist as "followed".
//
// Still non-fatal, because an authorization that worked must not be thrown away
// over a profile lookup. But it says so now, and the caller can retry.
func currentUser(ctx context.Context, accessToken string) (id, name string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.spotify.com/v1/me", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("spotify /v1/me: HTTP %d", resp.StatusCode)
	}
	var me struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return "", "", fmt.Errorf("spotify /v1/me: %w", err)
	}
	return me.ID, me.DisplayName, nil
}

// EnsureIdentity fills in a connection's Spotify identity when the exchange
// could not. Returns the account id.
//
// Self-healing rather than a migration: connections stored before this existed
// carry an empty id, and the fix has to reach them without anyone knowing to
// reconnect. Called where the id is needed, which is the only place its absence
// has a consequence.
func (s *Store) EnsureIdentity(ctx context.Context, c *Connection, accessToken string) string {
	if c.SpotifyID != "" {
		return c.SpotifyID
	}
	id, name, err := currentUser(ctx, accessToken)
	if err != nil || id == "" {
		// A 403 here is the same cause as on /v1/me/playlists: the account is
		// not on the application's allowlist. Worth naming in the log, because
		// the visible symptom is an empty "Connected as" that looks like a
		// rendering bug rather than a permission.
		slog.Warn("[Spotify] Could not resolve the account identity; own playlists will read as followed",
			"user", c.UserID, "err", err)
		return ""
	}
	c.SpotifyID, c.DisplayName = id, name
	if err := s.Save(*c); err != nil {
		slog.Warn("[Spotify] Could not store the resolved identity", "user", c.UserID, "err", err)
	}
	return id
}
