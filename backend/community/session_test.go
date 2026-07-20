package community

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func newTestStore(t *testing.T) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "test.db"), 0600, nil)
	if err != nil {
		t.Fatalf("open bolt: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := InitStore(db); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		storeMu.Lock()
		storeDB = nil
		storeMu.Unlock()
	})
}

func rfc3339In(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

// Validity leaves the skew as margin: a session expiring in two minutes must
// not be handed out, or a download can die mid-flight.
func TestSessionValidity(t *testing.T) {
	tests := []struct {
		name string
		s    *Session
		want bool
	}{
		{"valide 6h", &Session{SessionID: "a", SessionSecret: "b", ExpiresAt: rfc3339In(6 * time.Hour)}, true},
		{"expire dans 2min (sous la marge)", &Session{SessionID: "a", SessionSecret: "b", ExpiresAt: rfc3339In(2 * time.Minute)}, false},
		{"expirée", &Session{SessionID: "a", SessionSecret: "b", ExpiresAt: rfc3339In(-time.Hour)}, false},
		{"sans secret", &Session{SessionID: "a", ExpiresAt: rfc3339In(6 * time.Hour)}, false},
		{"sans id", &Session{SessionSecret: "b", ExpiresAt: rfc3339In(6 * time.Hour)}, false},
		{"date illisible", &Session{SessionID: "a", SessionSecret: "b", ExpiresAt: "bientôt"}, false},
		{"date absente", &Session{SessionID: "a", SessionSecret: "b"}, false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.IsValid(); got != tc.want {
				t.Errorf("IsValid() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A Signer must never be built from an unusable session: signing with empty
// credentials produces a request the server rejects with a message that
// explains none of this.
func TestSignerRefusesAnInvalidSession(t *testing.T) {
	expired := &Session{SessionID: "a", SessionSecret: "b", ExpiresAt: rfc3339In(-time.Hour)}
	if _, err := expired.Signer("7.2.0"); err == nil {
		t.Error("built a Signer from an expired session")
	}
	valid := &Session{SessionID: "a", SessionSecret: "b", ExpiresAt: rfc3339In(6 * time.Hour)}
	signer, err := valid.Signer("7.2.0")
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	if signer.SessionID != "a" || signer.SessionSecret != "b" || signer.AppVersion != "7.2.0" {
		t.Errorf("signer carries the wrong values: %+v", signer)
	}
}

// The InstallID identifies the installation across verifications, so it must be
// generated once and survive everything else.
func TestInstallIDIsStableAndSurvivesClear(t *testing.T) {
	newTestStore(t)

	first, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if first.InstallID == "" {
		t.Fatal("no InstallID was generated")
	}

	second, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if second.InstallID != first.InstallID {
		t.Errorf("InstallID changed between loads: %q then %q", first.InstallID, second.InstallID)
	}

	first.SessionID, first.SessionSecret, first.ExpiresAt = "sid", "secret", rfc3339In(6*time.Hour)
	if err := Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := ClearCredentials(); err != nil {
		t.Fatalf("ClearCredentials: %v", err)
	}

	after, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.InstallID != first.InstallID {
		t.Error("ClearCredentials dropped the InstallID")
	}
	if after.SessionID != "" || after.SessionSecret != "" || after.ExpiresAt != "" {
		t.Errorf("credentials survived the clear: %+v", after)
	}
}

func TestSessionRoundTripsThroughStorage(t *testing.T) {
	newTestStore(t)

	stored := &Session{InstallID: "install-1", SessionID: "sid", SessionSecret: "secret", ExpiresAt: rfc3339In(6 * time.Hour)}
	if err := Save(stored); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *loaded != *stored {
		t.Errorf("round trip changed the session:\n got %+v\nwant %+v", loaded, stored)
	}
	if !loaded.IsValid() {
		t.Error("a session valid before storage is invalid after")
	}
}

func TestExchangeGrantStoresTheSession(t *testing.T) {
	newTestStore(t)

	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/exchange" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(exchangeResponse{
			SessionID: "new-sid", SessionSecret: "new-secret", ExpiresAt: rfc3339In(6 * time.Hour),
		})
	}))
	defer server.Close()

	session, err := exchangeGrantAt(server.URL, server.Client(), "the-grant", "7.2.0")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if session.SessionID != "new-sid" || session.SessionSecret != "new-secret" {
		t.Errorf("session not populated: %+v", session)
	}
	// The platform declared at exchange must match the one signed into every
	// request, or the server sees a client that contradicts itself.
	if gotBody["platform"] != Platform {
		t.Errorf("platform sent = %q, want %q", gotBody["platform"], Platform)
	}
	if gotBody["grant"] != "the-grant" {
		t.Errorf("grant sent = %q", gotBody["grant"])
	}
	if gotBody["install_id"] == "" {
		t.Error("install_id was not sent")
	}

	persisted, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if persisted.SessionID != "new-sid" {
		t.Error("the exchanged session was not persisted")
	}
}

// A failed exchange must not leave a half-written session behind, and must say
// what the server answered — an expired grant and an IP mismatch look the same
// otherwise.
func TestExchangeGrantFailuresAreExplicit(t *testing.T) {
	newTestStore(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"grant expired"}`))
	}))
	defer server.Close()

	_, err := exchangeGrantAt(server.URL, server.Client(), "stale-grant", "7.2.0")
	if err == nil {
		t.Fatal("a 403 exchange was reported as success")
	}
	if got := err.Error(); !strings.Contains(got, "403") || !strings.Contains(got, "grant expired") {
		t.Errorf("error %q carries neither the status nor the server's explanation", got)
	}

	after, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.SessionID != "" || after.SessionSecret != "" {
		t.Errorf("a failed exchange wrote credentials: %+v", after)
	}

	if _, err := ExchangeGrant(nil, "", "7.2.0"); err == nil {
		t.Error("an empty grant was accepted")
	}
}
