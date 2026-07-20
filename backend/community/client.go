package community

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Observed behaviour of the community service, 2026-07-20:
//
//	503 + "taking a short break, try again in about N minute(s)"
//
// seen twice within two hours, announcing 108 minutes then 16. The delay varies
// and does not track our own request count, so this is the service pausing
// itself rather than rate-limiting us. Anything built on it has to treat
// "temporarily closed" as a normal state, not an incident.
//
// Deliberately NOT ported from upstream: its SetRateLimitCooldown /
// SetCommunityCooldown globals in progress.go. Those are process-wide mutable
// state feeding a single-user desktop progress bar — the pattern this project
// removed in v3.4.0. A cooldown here is returned as a typed error and surfaces
// through the job that hit it.

const (
	// maxRetries bounds transient-error retries (429, 502, 504).
	maxRetries = 4

	// fallbackWait applies when a 429 carries no usable Retry-After.
	fallbackWait = 30 * time.Second

	// maxWait caps a server-requested delay. The service has asked for 108
	// minutes; no download job should sit blocked that long. Past this we give
	// up and report, letting the caller decide when to come back.
	maxWait = 2 * time.Minute
)

// CooldownError means the service is deliberately closed for a while. It is a
// distinct type because it warrants a different response from a real failure:
// nothing is wrong with the request, and retrying sooner will not help.
type CooldownError struct {
	Service string
	Retry   time.Duration
	Message string
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("%s community service is on cooldown for %s: %s",
		e.Service, e.Retry.Round(time.Minute), e.Message)
}

// IsCooldown reports whether err is a service cooldown.
//
// Two things must consult this when the community path is wired in, both
// learned from reading upstream:
//
//  1. The quality fallback chain must STOP on a cooldown. Upstream's
//     qobuz.go tries 27 → 7 → 6 and only short-circuits on a cancelled
//     download, so a paused service costs three identical 503s instead of
//     one. Our qobuz/client.go has the same shape and the same trap waiting.
//
//  2. A cooldown deserves to be announced instance-wide, not just to the job
//     that met it: it closes the service for everyone, and another user should
//     not queue a whole playlist against a door that is shut for 108 minutes.
//     Upstream does this with a global progress field feeding CooldownBanner;
//     the equivalent here is an SSE event — a notification, not the mutable
//     process state removed in v3.4.0. Worth reusing their event-id trick: the
//     banner is dismissible, and a NEW cooldown bumps the id so it reappears
//     instead of staying hidden.
func IsCooldown(err error) bool {
	var c *CooldownError
	return errors.As(err, &c)
}

// AuthError means the service rejected our credentials: the session expired,
// was revoked, or does not match this IP. The stored credentials have already
// been cleared when this is returned, so the next attempt starts clean.
//
// The service answers 401 for all of these without distinguishing them, and
// that ambiguity has cost real debugging time — hence the deliberately
// non-committal wording.
type AuthError struct {
	Service string
	Status  int
	Body    string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("%s community request was rejected (HTTP %d): the session is expired, "+
		"revoked, or bound to a different IP — re-verification required. %s",
		e.Service, e.Status, e.Body)
}

// IsAuth reports whether err is a credential rejection.
func IsAuth(err error) bool {
	var a *AuthError
	return errors.As(err, &a)
}

// Do sends a signed request, retrying transient failures.
//
// buildRequest is called afresh for each attempt because a request cannot be
// signed twice: the body has been consumed and the nonce must be new.
//
// On 401/428 the stored credentials are cleared and an AuthError is returned
// without retrying. Upstream retries once after clearing, which only works
// because it can pop a browser open; here re-verification needs a human, so
// retrying immediately would just produce a second identical rejection.
func Do(client *http.Client, service string, signer Signer, buildRequest func() (*http.Request, error)) (*http.Response, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := buildRequest()
		if err != nil {
			return nil, err
		}
		if err := signer.Sign(req); err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s community request failed: %w", service, err)
		}

		switch {
		case resp.StatusCode == http.StatusServiceUnavailable:
			return nil, newCooldownError(service, resp)

		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusPreconditionRequired:
			preview := readPreview(resp)
			if err := ClearCredentials(); err != nil {
				slog.Warn("[Community] could not clear rejected credentials", "err", err)
			}
			return nil, &AuthError{Service: service, Status: resp.StatusCode, Body: preview}

		case resp.StatusCode == http.StatusTooManyRequests,
			resp.StatusCode == http.StatusBadGateway,
			resp.StatusCode == http.StatusGatewayTimeout:
			wait := retryDelay(resp, attempt)
			lastErr = fmt.Errorf("%s community service returned %d", service, resp.StatusCode)
			resp.Body.Close()
			if attempt == maxRetries {
				return nil, lastErr
			}
			slog.Info("[Community] transient error, backing off",
				"service", service, "status", resp.StatusCode,
				"wait", wait, "attempt", attempt+1, "of", maxRetries)
			time.Sleep(wait)

		default:
			// Every other status, including 4xx about the payload, belongs to
			// the caller: a 404 for an unknown track is an answer, not a
			// failure of this layer.
			return resp, nil
		}
	}
	return nil, lastErr
}

// newCooldownError reads the service's own explanation, which is user-facing
// and better than anything we would write.
func newCooldownError(service string, resp *http.Response) *CooldownError {
	message := ""
	if body, err := io.ReadAll(io.LimitReader(resp.Body, 4096)); err == nil {
		var parsed struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			message = strings.TrimSpace(parsed.Error)
		}
	}
	resp.Body.Close()

	retry := fallbackWait
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			retry = time.Duration(secs) * time.Second
		}
	}
	if message == "" {
		message = "no explanation given"
	}
	return &CooldownError{Service: service, Retry: retry, Message: message}
}

// retryDelay honours Retry-After, then X-RateLimit-Reset, then backs off
// linearly — capped, so a server asking for an hour cannot pin a worker.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			return capWait(time.Duration(secs)*time.Second + 250*time.Millisecond)
		}
	}
	if reset := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			if wait := time.Until(time.Unix(epoch, 0)); wait > 0 {
				return capWait(wait + 250*time.Millisecond)
			}
		}
	}
	return capWait(time.Duration(attempt+1) * 5 * time.Second)
}

func capWait(d time.Duration) time.Duration {
	if d > maxWait {
		return maxWait
	}
	if d < time.Second {
		return time.Second
	}
	return d
}

func readPreview(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	return strings.TrimSpace(string(body))
}
