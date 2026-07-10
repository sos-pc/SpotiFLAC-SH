package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// requestWithClaimsAndBody mirrors requestWithClaims (security_test.go) but
// attaches a real JSON body, needed for handlers like v1CreateAPIKey that
// decode the request.
func requestWithClaimsAndBody(claims *JWTClaims, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/keys", strings.NewReader(body))
	ctx := context.WithValue(r.Context(), contextKeyUser, claims)
	return r.WithContext(ctx)
}

// TestV1CreateAPIKeyRejectsAdminEscalation is the regression test for the
// privilege-escalation bug found while debugging a user's "why doesn't my
// key have admin" report: v1CreateAPIKey took req.Permissions straight from
// the request body with no check that the CALLING session was itself
// admin. Any authenticated non-admin user could self-issue a key with
// permission "admin" and use it against every v1RequireAdmin-gated
// endpoint indefinitely (API keys never expire, unlike JWTs).
func TestV1CreateAPIKeyRejectsAdminEscalation(t *testing.T) {
	am := newTestAuthManager(t)
	s := &Server{ctr: &Container{Auth: am}}

	t.Run("session non-admin demandant la permission admin -> 403, aucune clé créée", func(t *testing.T) {
		claims := &JWTClaims{UserID: "user1", IsAdmin: false}
		r := requestWithClaimsAndBody(claims, `{"name":"escalation","permissions":["read","admin"]}`)
		w := httptest.NewRecorder()

		s.v1CreateAPIKey(w, r)

		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d — body: %s", w.Code, http.StatusForbidden, w.Body.String())
		}
		keys, err := am.ListAPIKeys("user1")
		if err != nil {
			t.Fatalf("ListAPIKeys: %v", err)
		}
		if len(keys) != 0 {
			t.Errorf("expected no key to have been created, got %d", len(keys))
		}
	})

	t.Run("session non-admin sans permission admin -> 201", func(t *testing.T) {
		claims := &JWTClaims{UserID: "user2", IsAdmin: false}
		r := requestWithClaimsAndBody(claims, `{"name":"normal","permissions":["read","download"]}`)
		w := httptest.NewRecorder()

		s.v1CreateAPIKey(w, r)

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d — body: %s", w.Code, http.StatusCreated, w.Body.String())
		}
	})

	t.Run("session admin demandant la permission admin -> 201", func(t *testing.T) {
		claims := &JWTClaims{UserID: "admin1", IsAdmin: true}
		r := requestWithClaimsAndBody(claims, `{"name":"admin key","permissions":["read","admin"]}`)
		w := httptest.NewRecorder()

		s.v1CreateAPIKey(w, r)

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d — body: %s", w.Code, http.StatusCreated, w.Body.String())
		}
		var resp struct {
			Permissions []string `json:"permissions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		found := false
		for _, p := range resp.Permissions {
			if p == "admin" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected admin permission on the created key, got %v", resp.Permissions)
		}
	})
}
