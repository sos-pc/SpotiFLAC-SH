package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These routes had no level check at all until 2026-07-19. Measured in prod:
// a read-only API key reached them while being correctly refused on manage and
// admin routes (docs/api-redesign-plan.md phase 4).
//
// The two things that must hold, and that these cover:
//   - a read-only key can no longer reach the destructive ones;
//   - a browser session is unaffected, because permission scoping applies to
//     API keys only (v1RequirePermission returns early for !IsAPIKey).
func TestAuthRouteGuards(t *testing.T) {
	readOnlyKey := &JWTClaims{
		UserID: "u1", IsAPIKey: true, Permissions: []string{"read"},
	}
	manageKey := &JWTClaims{
		UserID: "u1", IsAPIKey: true, Permissions: []string{"read", "manage"},
	}
	adminKey := &JWTClaims{
		UserID: "u1", IsAPIKey: true, IsAdmin: true, Permissions: []string{"read", "manage", "admin"},
	}
	browser := &JWTClaims{UserID: "u1", IsAPIKey: false}
	adminBrowser := &JWTClaims{UserID: "u1", IsAPIKey: false, IsAdmin: true}

	tests := []struct {
		name    string
		claims  *JWTClaims
		perm    string // "" means the route uses v1RequireAdmin
		allowed bool
	}{
		// The Tidal account is a process-global singleton: disconnecting or
		// rebinding it affects every user of the instance.
		{"read key cannot disconnect Tidal", readOnlyKey, "", false},
		{"manage key cannot disconnect Tidal", manageKey, "", false},
		{"admin key can disconnect Tidal", adminKey, "", true},
		// BEHAVIOUR CHANGE, deliberate: before this guard any authenticated user
		// could disconnect the instance's Tidal account. Now it takes an admin
		// account — connecting Tidal from a non-admin account no longer works.
		{"non-admin browser session cannot disconnect Tidal", browser, "", false},
		{"admin browser session can disconnect Tidal", adminBrowser, "", true},

		// Revoking a credential is destructive, even scoped to one's own account.
		{"read key cannot revoke a key", readOnlyKey, "manage", false},
		{"manage key can revoke a key", manageKey, "manage", true},

		// Reads stay reachable at the nominal level.
		{"read key can read Tidal status", readOnlyKey, "read", true},
		{"browser session can read Tidal status", browser, "read", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/whatever", nil)
			req = req.WithContext(context.WithValue(req.Context(), contextKeyUser, tc.claims))
			rec := httptest.NewRecorder()

			var got bool
			if tc.perm == "" {
				got = v1RequireAdmin(rec, req)
			} else {
				got = v1RequirePermission(rec, req, tc.perm)
			}

			if got != tc.allowed {
				t.Errorf("allowed = %v, want %v (status %d)", got, tc.allowed, rec.Code)
			}
		})
	}
}

// Measured on prod 2026-07-19: a read-only API key created a ["read","manage"]
// key and wrote settings with it. POST /auth/keys had no level check at all,
// and its only guard tested the *account*'s admin flag — "manage" is not
// "admin", so nothing stopped the escalation. API keys never expire, so it was
// permanent: a leaked read-only key could grant itself write access for good.
//
// The rule now: no API key may mint a key stronger than itself.
func TestAnAPIKeyCannotMintAStrongerKey(t *testing.T) {
	readKey := &JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: []string{"read"}}
	manageKey := &JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: []string{"read", "manage"}}
	legacyKey := &JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: []string{"read", "download"}}
	adminKey := &JWTClaims{UserID: "u1", IsAPIKey: true, IsAdmin: true,
		Permissions: []string{"read", "manage", "admin"}}
	browser := &JWTClaims{UserID: "u1", IsAPIKey: false}

	tests := []struct {
		name   string
		caller *JWTClaims
		grant  string
		want   bool
	}{
		{"read ne peut pas accorder manage", readKey, "manage", false},
		{"read ne peut pas accorder admin", readKey, "admin", false},
		{"read peut accorder read", readKey, "read", true},

		{"manage peut accorder manage", manageKey, "manage", true},
		{"manage ne peut pas accorder admin", manageKey, "admin", false},

		// "download" is the pre-rename name for "manage"; a key holding it
		// really does have that power, so it may grant it. This is the case
		// that would break first if the two membership rules ever drift.
		{"download hérité vaut manage", legacyKey, "manage", true},

		{"admin peut tout accorder", adminKey, "admin", true},

		// A browser session is the human, bounded by the account's admin flag
		// rather than by key permissions.
		{"session navigateur non contrainte", browser, "manage", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := callerHasPermission(tc.caller, tc.grant); got != tc.want {
				t.Errorf("callerHasPermission(%v, %q) = %v, want %v",
					tc.caller.Permissions, tc.grant, got, tc.want)
			}
		})
	}

	t.Run("les deux règles d'appartenance restent d'accord", func(t *testing.T) {
		// callerHasPermission duplicates v1RequirePermission's rule without the
		// HTTP response. If they drift, a key could be granted a permission it
		// cannot itself use — pin them together.
		for _, caller := range []*JWTClaims{readKey, manageKey, legacyKey, adminKey, browser} {
			for _, perm := range []string{"read", "manage", "admin"} {
				req := httptest.NewRequest(http.MethodGet, "/x", nil)
				req = req.WithContext(context.WithValue(req.Context(), contextKeyUser, caller))
				enforced := v1RequirePermission(httptest.NewRecorder(), req, perm)
				if asked := callerHasPermission(caller, perm); asked != enforced {
					t.Errorf("perms=%v perm=%q: asked %v but enforced %v",
						caller.Permissions, perm, asked, enforced)
				}
			}
		}
	})
}
