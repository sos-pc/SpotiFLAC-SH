package community

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// verifyTimeout bounds the full verification pipeline (bootstrap + solve +
// exchange). The solver itself may take up to ~60s; this gives it headroom.
const verifyTimeout = 120 * time.Second

// bootstrapResponse is what GET /bootstrap returns.
type bootstrapResponse struct {
	ChallengeURL string `json:"challenge_url"`
}

// Verify runs the full headless verification pipeline and returns a valid
// session, or an error. Callers should treat any non-nil error as "verification
// failed; retry later or notify the operator".
//
// Flow:
//  1. Load stored session (for InstallID).
//  2. GET {verifyURL}/bootstrap → challenge_url.
//  3. Add cb= parameter to challenge_url so the solver can detect the grant.
//  4. Call solver.GetGrant(challengeURL) → grant.
//  5. Call ExchangeGrant(grant) → session saved to BoltDB.
func Verify(ctx context.Context, solver *TurnstileSolver, appVersion string) (*Session, error) {
	verifyURL, err := VerifyBaseURL()
	if err != nil {
		return nil, fmt.Errorf("community: verify: %w", err)
	}

	session, err := Load()
	if err != nil {
		return nil, fmt.Errorf("community: verify: load session: %w", err)
	}

	// 1. Bootstrap
	bootstrapURL, err := url.Parse(verifyURL + "/bootstrap")
	if err != nil {
		return nil, fmt.Errorf("community: verify: parse bootstrap URL: %w", err)
	}
	q := bootstrapURL.Query()
	q.Set("install_id", session.InstallID)
	q.Set("app_version", appVersion)
	q.Set("platform", Platform)
	bootstrapURL.RawQuery = q.Encode()

	bootstrapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	bootstrapReq, err := http.NewRequestWithContext(bootstrapCtx,
		http.MethodGet, bootstrapURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("community: verify: bootstrap request: %w", err)
	}

	resp, err := http.DefaultClient.Do(bootstrapReq)
	if err != nil {
		return nil, fmt.Errorf("community: verify: bootstrap failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("community: verify: bootstrap HTTP %d: %s",
			resp.StatusCode, bytes.TrimSpace(preview))
	}

	var boot bootstrapResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32*1024)).Decode(&boot); err != nil {
		return nil, fmt.Errorf("community: verify: decode bootstrap: %w", err)
	}

	challengeURL, err := url.Parse(boot.ChallengeURL)
	if err != nil {
		return nil, fmt.Errorf("community: verify: invalid challenge URL: %w", err)
	}

	// 2. Inject callback path into challenge URL.
	//    The solver will watch for this pattern in the final URL to know it has
	//    been redirected after a successful solve. The actual host does not need
	//    to exist — Cloudflare just redirects the browser there.
	challengeQuery := challengeURL.Query()
	challengeQuery.Set("cb", "http://localhost/session-grant")
	challengeURL.RawQuery = challengeQuery.Encode()

	solveCtx, cancelSolve := context.WithTimeout(ctx, verifyTimeout)
	defer cancelSolve()

	// 3. Solve challenge → grant
	if solver == nil {
		return nil, fmt.Errorf("community: verify: no solver configured (set TURNSTILE_SOLVER_URL)")
	}
	grant, err := solver.GetGrant(solveCtx, challengeURL.String())
	if err != nil {
		return nil, fmt.Errorf("community: verify: solver: %w", err)
	}

	// 4. Exchange grant for session (exchangeGrantAt persists it)
	exchangeClient := &http.Client{Timeout: exchangeTimeout}

	sess, err := exchangeGrantAt(verifyURL, exchangeClient, grant, appVersion)
	if err != nil {
		return nil, fmt.Errorf("community: verify: exchange: %w", err)
	}

	return sess, nil
}
