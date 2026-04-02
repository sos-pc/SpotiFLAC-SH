package spotify

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func makeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func makeRespWithHeader(status int, body, headerKey, headerVal string) *http.Response {
	resp := makeResp(status, body)
	resp.Header.Set(headerKey, headerVal)
	return resp
}

// fakeInitTransport répond aux sous-requêtes d'Initialize() avec des données valides.
// Utilisé pour simuler un re-auth réussi après 401.
func fakeInitTransport(queryResp func() *http.Response) roundTripperFunc {
	// HTML de session avec clientVersion encodé en base64
	cfgJSON := `{"clientVersion":"1.2.3.456"}`
	cfgB64 := base64.StdEncoding.EncodeToString([]byte(cfgJSON))
	sessionHTML := fmt.Sprintf(`<script id="appServerConfig" type="text/plain">%s</script>`, cfgB64)

	tokenBody, _ := json.Marshal(map[string]interface{}{
		"accessToken": "fresh-access-token",
		"clientId":    "test-client-id",
	})
	clientTokenBody, _ := json.Marshal(map[string]interface{}{
		"response_type": "RESPONSE_GRANTED_TOKEN_RESPONSE",
		"granted_token": map[string]interface{}{"token": "fresh-client-token"},
	})

	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		host := req.URL.Host
		path := req.URL.Path

		switch {
		case host == "open.spotify.com" && (path == "/" || path == ""):
			resp := makeResp(200, sessionHTML)
			resp.Header.Set("Set-Cookie", "sp_t=fake-device-id; Path=/")
			return resp, nil

		case host == "open.spotify.com" && strings.HasPrefix(path, "/api/token"):
			resp := makeResp(200, string(tokenBody))
			resp.Header.Set("Set-Cookie", "sp_t=fake-device-id; Path=/")
			return resp, nil

		case host == "clienttoken.spotify.com":
			return makeResp(200, string(clientTokenBody)), nil

		case host == "api-partner.spotify.com":
			return queryResp(), nil
		}

		return makeResp(500, "unexpected URL: "+req.URL.String()), nil
	})
}

// newPreAuthedClient crée un SpotifyClient avec tokens pré-remplis.
// Initialize() ne sera pas appelé tant que les tokens restent valides.
func newPreAuthedClient(transport http.RoundTripper) *SpotifyClient {
	c := NewSpotifyClient()
	c.client = &http.Client{Transport: transport}
	c.accessToken = "pre-filled-access"
	c.clientToken = "pre-filled-client"
	c.clientVersion = "1.2.3"
	return c
}

// ─── TestQuery ────────────────────────────────────────────────────────────────

func TestQuerySuccess(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{"data": "ok"})
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return makeResp(200, string(body)), nil
	})

	c := newPreAuthedClient(transport)
	result, err := c.Query(map[string]interface{}{"key": "val"})
	if err != nil {
		t.Fatalf("Query : erreur inattendue: %v", err)
	}
	if result["data"] != "ok" {
		t.Errorf("result[\"data\"] = %v, want \"ok\"", result["data"])
	}
}

func TestQuery401ThenSuccess(t *testing.T) {
	// Premier appel → 401; Initialize() récupère des tokens frais; deuxième appel → 200.
	var queryCallCount int32
	successBody, _ := json.Marshal(map[string]interface{}{"data": "after-refresh"})

	queryResp := func() *http.Response {
		n := atomic.AddInt32(&queryCallCount, 1)
		if n == 1 {
			return makeResp(401, "Unauthorized")
		}
		return makeResp(200, string(successBody))
	}

	c := newPreAuthedClient(fakeInitTransport(queryResp))
	result, err := c.Query(map[string]interface{}{"key": "val"})
	if err != nil {
		t.Fatalf("Query : erreur inattendue: %v", err)
	}
	if result["data"] != "after-refresh" {
		t.Errorf("result[\"data\"] = %v, want \"after-refresh\"", result["data"])
	}
	if queryCallCount < 2 {
		t.Errorf("queryCallCount = %d, attendu ≥ 2 (retry après 401)", queryCallCount)
	}
}

func TestQueryPermanentError(t *testing.T) {
	// 500 → erreur immédiate sans retry.
	var callCount int32
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&callCount, 1)
		return makeResp(500, "Internal Server Error"), nil
	})

	c := newPreAuthedClient(transport)
	_, err := c.Query(map[string]interface{}{})
	if err == nil {
		t.Fatal("Query : erreur attendue pour HTTP 500")
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, attendu 1 (pas de retry sur 5xx)", callCount)
	}
}

func TestQueryExhausted401(t *testing.T) {
	// Toutes les tentatives retournent 401 → "failed after N attempts".
	var queryCallCount int32
	queryResp := func() *http.Response {
		atomic.AddInt32(&queryCallCount, 1)
		return makeResp(401, "Unauthorized")
	}

	c := newPreAuthedClient(fakeInitTransport(queryResp))
	_, err := c.Query(map[string]interface{}{})
	if err == nil {
		t.Fatal("Query : erreur attendue après épuisement des tentatives")
	}
	if !strings.Contains(err.Error(), "after") {
		t.Errorf("erreur %q ne mentionne pas 'after' (attempts)", err.Error())
	}
	// 3 appels Query (1 initial + 2 retries après re-auth)
	if queryCallCount != 3 {
		t.Errorf("queryCallCount = %d, attendu 3", queryCallCount)
	}
}

func TestQuery429RespectsRetryAfter(t *testing.T) {
	// Vérifie que Retry-After est bien lu — sans attendre réellement.
	// On remplace time.Sleep dans l'implémentation : non possible sans refacto.
	// À la place, on vérifie que 429 → retry (le 2ème appel reçoit 200).
	var queryCallCount int32
	successBody, _ := json.Marshal(map[string]interface{}{"data": "after-ratelimit"})

	// Utilise Retry-After: 1 pour un sleep minimal (1s au lieu du backoff 10/30/60s).
	queryResp := func() *http.Response {
		n := atomic.AddInt32(&queryCallCount, 1)
		if n == 1 {
			resp := makeResp(429, "Too Many Requests")
			resp.Header.Set("Retry-After", "1")
			return resp
		}
		return makeResp(200, string(successBody))
	}

	c := newPreAuthedClient(fakeInitTransport(queryResp))
	result, err := c.Query(map[string]interface{}{})
	if err != nil {
		t.Fatalf("Query : erreur inattendue: %v", err)
	}
	if result["data"] != "after-ratelimit" {
		t.Errorf("result[\"data\"] = %v, want \"after-ratelimit\"", result["data"])
	}
	if queryCallCount < 2 {
		t.Errorf("queryCallCount = %d, attendu ≥ 2 (retry après 429)", queryCallCount)
	}
}
