package community

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Session lifetime facts, measured against the live service on 2026-07-20 by
// completing a real verification. They are not configurable by us — the server
// decides — but they drive every design choice in this file:
//
//   - a session lasts ~6 hours (expires_at came back at now+5.98h)
//   - it is bound to the IP that solved the challenge (ip_hash is sealed into
//     the session token, identical to the one in the grant)
//   - the server may revoke it early, signalled by 401 or 428
//
// Sessions are refreshed automatically via the Turnstile solver integration
// (see solver.go). A fresh session is obtained before expiry without user
// intervention.

const (
	// expirySkew is how long before the stated expiry we stop trusting a
	// session. A download that starts at expiry-minus-one-second would fail
	// mid-flight; five minutes gives a request time to finish.
	expirySkew = 5 * time.Minute

	// exchangeTimeout bounds the grant-for-session call.
	exchangeTimeout = 15 * time.Second
)

var (
	sessionBucket = []byte("community_session")
	sessionKey    = []byte("current")
)

// Session is the persisted credential set.
//
// It is a single instance-wide record, not one per user. The session
// authenticates *this installation* to the community service; an admin
// verifies once and every user of the instance benefits. Storing one per user
// would multiply verification challenges for no gain, and could not work anyway since
// the service ties a session to one IP.
type Session struct {
	// InstallID identifies this installation across verifications. Generated
	// once and kept, so the service sees a stable client rather than a new one
	// every six hours.
	InstallID string `json:"install_id"`

	SessionID string `json:"session_id,omitempty"`
	// SessionSecret signs every request. It is a live credential for ~6h and
	// must never be logged or returned by any API.
	SessionSecret string `json:"session_secret,omitempty"`
	// ExpiresAt is RFC3339 as sent by the server.
	ExpiresAt string `json:"expires_at,omitempty"`
}

// IsValid reports whether the session can still sign requests, leaving the
// skew as margin. A malformed or absent expiry counts as invalid: refusing to
// guess is the safe direction here.
func (s *Session) IsValid() bool {
	if s == nil || s.SessionID == "" || s.SessionSecret == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, s.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Until(expiresAt) > expirySkew
}

// ExpiresIn returns the remaining lifetime, or 0 once past expiry. Meant for
// showing the user when they will next have to verify.
func (s *Session) ExpiresIn() time.Duration {
	if s == nil || s.ExpiresAt == "" {
		return 0
	}
	expiresAt, err := time.Parse(time.RFC3339, s.ExpiresAt)
	if err != nil {
		return 0
	}
	if d := time.Until(expiresAt); d > 0 {
		return d
	}
	return 0
}

// Signer builds a Signer from this session. Returns an error rather than an
// unusable Signer when the session cannot sign.
func (s *Session) Signer(appVersion string) (Signer, error) {
	if !s.IsValid() {
		return Signer{}, fmt.Errorf("community: no valid session (verification required)")
	}
	return Signer{
		SessionID:     s.SessionID,
		SessionSecret: s.SessionSecret,
		AppVersion:    appVersion,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Storage
// ─────────────────────────────────────────────────────────────────────────────

var (
	storeMu sync.Mutex
	storeDB *bolt.DB
)

// InitStore registers the session bucket in the app's shared BoltDB, following
// the same pattern as songlink.InitISRCCacheDBShared.
//
// BoltDB rather than upstream's community_session.json: it is where the rest of
// our state already lives, it is already backed up with the app, and it avoids
// a second file with its own permission handling. Note this does make BoltDB
// hold a live secret for the first time — docs/api-redesign-plan.md notes that
// the store held none, and that note now needs qualifying.
func InitStore(db *bolt.DB) error {
	if db == nil {
		return fmt.Errorf("community: nil database")
	}
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(sessionBucket)
		return err
	})
	if err != nil {
		return fmt.Errorf("community: session bucket init failed: %w", err)
	}
	storeMu.Lock()
	storeDB = db
	storeMu.Unlock()
	return nil
}

func db() (*bolt.DB, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if storeDB == nil {
		return nil, fmt.Errorf("community: session store is not initialised")
	}
	return storeDB, nil
}

// Load returns the stored session, creating one with a fresh InstallID on first
// use. It never returns nil without an error.
func Load() (*Session, error) {
	database, err := db()
	if err != nil {
		return nil, err
	}
	session := &Session{}
	err = database.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return nil
		}
		if data := b.Get(sessionKey); data != nil {
			return json.Unmarshal(data, session)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("community: load session: %w", err)
	}
	if session.InstallID == "" {
		id, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		session.InstallID = id
		if err := Save(session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

// Save persists the session.
func Save(session *Session) error {
	if session == nil {
		return fmt.Errorf("community: nil session")
	}
	database, err := db()
	if err != nil {
		return err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("community: encode session: %w", err)
	}
	if err := database.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(sessionBucket)
		if err != nil {
			return err
		}
		return b.Put(sessionKey, data)
	}); err != nil {
		return fmt.Errorf("community: save session: %w", err)
	}
	return nil
}

// ClearCredentials drops the session but keeps the InstallID.
//
// Called when the server answers 401 or 428: it has stopped honouring these
// credentials, so keeping them only produces more rejected requests. The
// InstallID survives because it identifies the installation, not the session.
func ClearCredentials() error {
	session, err := Load()
	if err != nil {
		return err
	}
	session.SessionID = ""
	session.SessionSecret = ""
	session.ExpiresAt = ""
	return Save(session)
}

// ─────────────────────────────────────────────────────────────────────────────
// Grant exchange
// ─────────────────────────────────────────────────────────────────────────────

// exchangeResponse is what /session/exchange returns.
type exchangeResponse struct {
	SessionID     string `json:"session_id"`
	SessionSecret string `json:"session_secret"`
	ExpiresAt     string `json:"expires_at"`
}

// ExchangeGrant trades a verification grant for a session and persists it.
//
// The grant is obtained by solving a Turnstile challenge via the solver
// integration (see solver.go).
//
// Grants are short-lived — about a minute, measured — and single-use, so this
// must run immediately after the grant is captured.
func ExchangeGrant(client *http.Client, grant, appVersion string) (*Session, error) {
	verifyURL, err := VerifyBaseURL()
	if err != nil {
		return nil, err
	}
	return exchangeGrantAt(verifyURL, client, grant, appVersion)
}

// exchangeGrantAt is ExchangeGrant with the base URL injected, so the exchange
// can be exercised against a test server without reaching the real service.
func exchangeGrantAt(verifyURL string, client *http.Client, grant, appVersion string) (*Session, error) {
	if grant == "" {
		return nil, fmt.Errorf("community: empty grant")
	}
	session, err := Load()
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]string{
		"grant":       grant,
		"install_id":  session.InstallID,
		"app_version": appVersion,
		"platform":    Platform,
	})
	if err != nil {
		return nil, fmt.Errorf("community: encode exchange request: %w", err)
	}

	if client == nil {
		client = &http.Client{Timeout: exchangeTimeout}
	}
	resp, err := client.Post(verifyURL+"/session/exchange", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("community: session exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The body often explains (expired grant, IP mismatch); include a
		// bounded slice of it so the caller is not left with a bare status.
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("community: session exchange returned HTTP %d: %s",
			resp.StatusCode, bytes.TrimSpace(preview))
	}

	var result exchangeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("community: decode exchange response: %w", err)
	}
	if result.SessionID == "" || result.SessionSecret == "" || result.ExpiresAt == "" {
		return nil, fmt.Errorf("community: session exchange response is incomplete")
	}

	session.SessionID = result.SessionID
	session.SessionSecret = result.SessionSecret
	session.ExpiresAt = result.ExpiresAt
	if err := Save(session); err != nil {
		return nil, err
	}
	return session, nil
}
