package community

import (
	"context"
	"log/slog"
	"time"
)

// refreshInterval is how often we check whether the session needs renewal.
// At ~6h session lifetime, checking every 30 min means we miss at most one
// download window worth of expiry.
const refreshInterval = 30 * time.Minute

// RefreshLoop periodically checks the community session and triggers a fresh
// verification when the session is expired or about to expire.
//
// It is designed to run as a background goroutine for the lifetime of the
// process. On the first iteration (t=0) it verifies immediately if no valid
// session exists; subsequent checks are driven by the ticker.
//
// Errors are logged but do not stop the loop — a transient solver failure or
// a cooldown from the community service should not prevent future retries.
func RefreshLoop(ctx context.Context, solver *TurnstileSolver, appVersion string) {
	verify := func() {
		session, err := Load()
		if err != nil {
			slog.Warn("[Community] cannot load session, skipping refresh",
				"err", err)
			return
		}
		if session.IsValid() {
			remaining := session.ExpiresIn()
			slog.Info("[Community] session valid",
				"remaining", remaining.Round(time.Minute).String())
			return
		}

		slog.Info("[Community] session missing or expired, starting verification")
		verifyCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
		defer cancel()

		newSession, err := Verify(verifyCtx, solver, appVersion)
		if err != nil {
			slog.Error("[Community] verification failed",
				"err", err)
			return
		}
		slog.Info("[Community] session obtained",
			"remaining", newSession.ExpiresIn().Round(time.Minute).String())
	}

	// First check immediately — if there is no session, don't wait 30 min.
	verify()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			verify()
		case <-ctx.Done():
			slog.Info("[Community] refresh loop stopped")
			return
		}
	}
}
