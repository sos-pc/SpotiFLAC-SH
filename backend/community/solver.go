package community

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// solverDefaultTimeout bounds how long we wait for a Turnstile challenge to be
// solved and the grant to be returned. The solver itself has internal timeouts;
// this is the outer bound from the caller's perspective.
const solverDefaultTimeout = 90 * time.Second

// TurnstileSolver talks to a Turnstile-Solver instance (theyka/turnstile_solver
// or a compatible image running grant_solver.py) to solve Cloudflare Turnstile
// challenges and capture the resulting grant token.
type TurnstileSolver struct {
	// BaseURL is the root URL of the solver service, e.g.
	// "http://turnstile-solver:5000". Read from TURNSTILE_SOLVER_URL at init
	// time.
	BaseURL string

	// Client is the HTTP client. If nil, a default with solverDefaultTimeout
	// is used.
	Client *http.Client
}

// grantRequest is the JSON body sent to POST /grant.
type grantRequest struct {
	ChallengeURL string `json:"challenge_url"`
}

// grantResponse is the JSON returned on success.
type grantResponse struct {
	Grant   string  `json:"grant"`
	Elapsed float64 `json:"elapsed"`
}

// grantErrorResponse is returned on solver errors.
type grantErrorResponse struct {
	Error   string  `json:"error"`
	Elapsed float64 `json:"elapsed"`
}

// SolverFromEnv reads TURNSTILE_SOLVER_URL and returns a configured
// TurnstileSolver, or nil if the env var is empty (callers should treat nil as
// "solver not configured" and fall back gracefully).
func SolverFromEnv() *TurnstileSolver {
	u := os.Getenv("TURNSTILE_SOLVER_URL")
	if u == "" {
		return nil
	}
	return &TurnstileSolver{BaseURL: u}
}

// GetGrant sends a challenge URL to the solver and returns the grant token.
func (s *TurnstileSolver) GetGrant(ctx context.Context, challengeURL string) (string, error) {
	if s.BaseURL == "" {
		return "", fmt.Errorf("community: solver URL not configured")
	}

	body, err := json.Marshal(grantRequest{ChallengeURL: challengeURL})
	if err != nil {
		return "", fmt.Errorf("community: encode grant request: %w", err)
	}

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: solverDefaultTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.BaseURL+"/grant", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("community: create solver request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("community: solver unreachable: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 64*1024)
	if resp.StatusCode == http.StatusOK {
		var result grantResponse
		if err := json.NewDecoder(limited).Decode(&result); err != nil {
			return "", fmt.Errorf("community: decode grant response: %w", err)
		}
		if result.Grant == "" {
			return "", fmt.Errorf("community: solver returned empty grant")
		}
		return result.Grant, nil
	}

	var errResp grantErrorResponse
	preview, _ := io.ReadAll(limited)
	if json.Unmarshal(preview, &errResp) == nil && errResp.Error != "" {
		return "", fmt.Errorf("community: solver error (HTTP %d): %s",
			resp.StatusCode, errResp.Error)
	}
	return "", fmt.Errorf("community: solver returned HTTP %d: %s",
		resp.StatusCode, string(bytes.TrimSpace(preview)))
}
