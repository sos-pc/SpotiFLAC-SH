package spotifyoauth

import (
	"net/url"
	"os"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	f, err := os.CreateTemp("", "spotifyoauth-*.db")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	db, err := bolt.Open(f.Name(), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// The verifier must never appear in the URL the browser follows — that is the
// whole of what PKCE buys, and it is one typo away from being lost.
func TestAuthorizeURLCarriesTheChallengeAndNotTheVerifier(t *testing.T) {
	s := newTestStore(t)

	raw, err := s.AuthorizeURL("client-1", "https://example.test/api/v1/spotify/callback", "u1")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()

	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("challenge method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Error("no code_challenge in the authorize URL")
	}
	if q.Get("state") == "" {
		t.Error("no state: the callback would have nothing to validate against")
	}
	if strings.Contains(raw, "code_verifier") {
		t.Error("the verifier is in the authorize URL — PKCE buys nothing if it travels")
	}

	// And the challenge must actually be the hash of the stored verifier.
	s.mu.Lock()
	p, ok := s.pending[q.Get("state")]
	s.mu.Unlock()
	if !ok {
		t.Fatal("no pending authorization recorded for the state")
	}
	if challengeFor(p.verifier) != q.Get("code_challenge") {
		t.Error("the challenge is not the hash of the verifier that was kept")
	}
	if p.userID != "u1" {
		t.Errorf("pending userID = %q, want u1", p.userID)
	}
}

// Only the scopes the feature needs. Asking for more because it might be useful
// later is how a consent screen starts frightening people.
func TestAuthorizeURLAsksForReadScopesOnly(t *testing.T) {
	s := newTestStore(t)
	raw, err := s.AuthorizeURL("client-1", "https://example.test/cb", "u1")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	got := strings.Fields(mustQuery(t, raw).Get("scope"))
	for _, sc := range got {
		if !strings.HasPrefix(sc, "playlist-read") {
			t.Errorf("scope %q is not a playlist read scope", sc)
		}
	}
	if len(got) == 0 {
		t.Error("no scopes requested")
	}
}

// A state is single-use. Replaying one that has been redeemed must fail, or the
// protection it provides is decorative.
func TestStateIsSingleUse(t *testing.T) {
	s := newTestStore(t)
	raw, err := s.AuthorizeURL("client-1", "https://example.test/cb", "u1")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	state := mustQuery(t, raw).Get("state")

	if _, ok := s.take(state); !ok {
		t.Fatal("first take failed")
	}
	if _, ok := s.take(state); ok {
		t.Error("the same state was accepted twice")
	}
}

func TestUnknownStateIsRejected(t *testing.T) {
	s := newTestStore(t)
	if _, ok := s.take("never-issued"); ok {
		t.Error("a state nobody issued was accepted")
	}
}

// Reconnecting is how an expired refresh token is replaced, so the second
// connection has to win rather than being refused as a duplicate.
func TestSaveOverwritesAndDeleteRemoves(t *testing.T) {
	s := newTestStore(t)

	if err := s.Save(Connection{UserID: "u1", RefreshToken: "old"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Save(Connection{UserID: "u1", RefreshToken: "new"}); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	c, err := s.Get("u1")
	if err != nil || c == nil {
		t.Fatalf("Get: %v, %v", c, err)
	}
	if c.RefreshToken != "new" {
		t.Errorf("refresh token = %q, want the reconnection's", c.RefreshToken)
	}

	if err := s.Delete("u1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if c, err := s.Get("u1"); err != nil || c != nil {
		t.Errorf("after Delete: %v, %v", c, err)
	}
}

func TestGetOnAnUnconnectedUser(t *testing.T) {
	s := newTestStore(t)
	c, err := s.Get("nobody")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if c != nil {
		t.Errorf("got %+v for a user who never connected", c)
	}
}

// The exact-match redirect URI is the step everyone gets wrong, so it is
// computed rather than typed.
func TestRedirectURI(t *testing.T) {
	for in, want := range map[string]string{
		"https://spotiflac.example.fr":  "https://spotiflac.example.fr/api/v1/spotify/callback",
		"https://spotiflac.example.fr/": "https://spotiflac.example.fr/api/v1/spotify/callback",
	} {
		if got := RedirectURI(in); got != want {
			t.Errorf("RedirectURI(%q) = %q, want %q", in, got, want)
		}
	}
}

// No application configured is a different answer from a failed authorization,
// and the operator needs to be able to tell them apart.
func TestAuthorizeURLNeedsAClientID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AuthorizeURL("", "https://example.test/cb", "u1"); err == nil {
		t.Error("an empty client id was accepted")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Query()
}

// The first real connection stored spotify_id and display_name empty, with
// nothing anywhere saying why, because currentUser swallowed its own failure.
// EnsureIdentity is what reaches those records without anyone knowing to
// reconnect — so it must leave an already-known identity alone, and must not
// invent one when the lookup fails.
func TestEnsureIdentityKeepsWhatIsAlreadyKnown(t *testing.T) {
	s := newTestStore(t)
	c := Connection{UserID: "u1", SpotifyID: "already-known", DisplayName: "Me"}
	if err := s.Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// An empty token would fail any lookup; it must not be attempted at all.
	if got := s.EnsureIdentity(t.Context(), &c, ""); got != "already-known" {
		t.Errorf("EnsureIdentity = %q, want the stored id untouched", got)
	}
}

func TestEnsureIdentityReportsNothingRatherThanGuessing(t *testing.T) {
	s := newTestStore(t)
	c := Connection{UserID: "u1"} // the shape the first connection produced

	// No token, so /v1/me cannot answer. The account must stay unidentified
	// rather than acquire a made-up id — every playlist reading as "followed"
	// is recoverable, a wrong owner is not.
	if got := s.EnsureIdentity(t.Context(), &c, ""); got != "" {
		t.Errorf("EnsureIdentity = %q, want empty when the lookup cannot answer", got)
	}
	if c.SpotifyID != "" {
		t.Errorf("SpotifyID = %q, want it left empty", c.SpotifyID)
	}
}
