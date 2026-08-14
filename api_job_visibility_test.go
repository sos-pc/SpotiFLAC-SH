package main

import (
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
)

// The operator reported another account's playlist appearing under their own
// downloads. The filter exempted admins, so their queue carried everyone's
// jobs — and since the panel groups by batch, other people's batches showed up
// with nothing saying whose they were.
//
// Watchlists already filtered unconditionally, so the same account saw "my
// watchlists" beside "everybody's queue". These pin the rule that resolves it.
func TestJobVisibility(t *testing.T) {
	admin := &auth.JWTClaims{UserID: "admin-1", IsAdmin: true}
	regular := &auth.JWTClaims{UserID: "u1"}

	cases := []struct {
		name      string
		user      *auth.JWTClaims
		jobUserID string
		want      bool
	}{
		{"a user sees their own", regular, "u1", true},
		{"a user does not see another's", regular, "u2", false},

		// The one that changed.
		{"an admin sees their own", admin, "admin-1", true},
		{"an admin does NOT see another's in their personal queue", admin, "u1", false},

		// Pre-authentication records have no owner. Hiding them would hide them
		// from the person they actually belong to.
		{"ownerless stays visible to a user", regular, "", true},
		{"ownerless stays visible to an admin", admin, "", true},

		// The SSE snapshot resolves the user before filtering; a nil one means
		// there is nobody to filter against.
		{"no authenticated user filters nothing", nil, "u1", true},
	}

	for _, tc := range cases {
		if got := jobVisibleTo(tc.user, tc.jobUserID); got != tc.want {
			t.Errorf("%s: jobVisibleTo(...) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
