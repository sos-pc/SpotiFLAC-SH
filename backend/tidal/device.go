package tidal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Credentials de l'application Tidal — identiques à ceux utilisés par les clients
// communautaires (tiddl, etc.). N'appartiennent pas à un compte utilisateur.
const (
	tidalDeviceClientID     = "4N3n6Q1x95LL5K7p"
	tidalDeviceClientSecret = "oKOXfJW371cX6xaZ0PyhgGNBdNLlBZd4AKKYougMjik="
)

// DeviceAuthResponse est la réponse de l'endpoint device_authorization.
// Tidal retourne du camelCase (deviceCode, verificationUriComplete…).
// Les tags JSON correspondent à la réponse Tidal ; on renomme en snake_case
// pour la sérialisation vers le frontend.
type DeviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DevicePollResult est le résultat d'un sondage du token endpoint.
type DevicePollResult struct {
	Status string `json:"status"` // "pending" | "authorized" | "expired" | "denied" | "error"
	Error  string `json:"error,omitempty"`
}

// StartTidalDeviceAuth démarre le flow Device Code.
// Retourne les informations nécessaires pour que l'utilisateur puisse authoriser.
func StartTidalDeviceAuth() (*DeviceAuthResponse, error) {
	v := url.Values{}
	v.Set("client_id", tidalDeviceClientID)
	v.Set("scope", "r_usr w_usr w_sub")

	req, _ := http.NewRequest("POST", "https://auth.tidal.com/v1/oauth2/device_authorization", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Tidal API returned %d: %s", resp.StatusCode, string(body))
	}

	// Logger le body brut pour diagnostiquer le format exact retourné par Tidal
	fmt.Printf("[Tidal] device_authorization raw response: %s\n", string(body))

	// Parser en map générique pour gérer camelCase ET snake_case
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	getString := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				if s, ok := v.(string); ok {
					return s
				}
			}
		}
		return ""
	}
	getInt := func(keys ...string) int {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				switch n := v.(type) {
				case float64:
					return int(n)
				case int:
					return n
				}
			}
		}
		return 0
	}

	result := &DeviceAuthResponse{
		DeviceCode:              getString("device_code", "deviceCode"),
		UserCode:                getString("user_code", "userCode"),
		VerificationURI:         getString("verification_uri", "verificationUri"),
		VerificationURIComplete: getString("verification_uri_complete", "verificationUriComplete"),
		ExpiresIn:               getInt("expires_in", "expiresIn"),
		Interval:                getInt("interval"),
	}

	if result.Interval == 0 {
		result.Interval = 5
	}
	if result.VerificationURIComplete == "" {
		result.VerificationURIComplete = result.VerificationURI
	}
	// Tidal omet parfois le schème (retourne "link.tidal.com/XXXX" au lieu de "https://...")
	ensureHTTPS := func(u string) string {
		if u != "" && !strings.HasPrefix(u, "http") {
			return "https://" + u
		}
		return u
	}
	result.VerificationURI = ensureHTTPS(result.VerificationURI)
	result.VerificationURIComplete = ensureHTTPS(result.VerificationURIComplete)

	fmt.Printf("[Tidal] Device auth started — user_code=%q verification_uri_complete=%q device_code=%q\n",
		result.UserCode, result.VerificationURIComplete, result.DeviceCode)

	return result, nil
}

// PollTidalDeviceAuth tente d'échanger le device_code contre un token.
// À appeler toutes les `interval` secondes jusqu'à obtenir "authorized" ou "expired"/"denied".
func PollTidalDeviceAuth(deviceCode string) DevicePollResult {
	v := url.Values{}
	v.Set("client_id", tidalDeviceClientID)
	v.Set("device_code", deviceCode)
	v.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	v.Set("scope", "r_usr w_usr w_sub")

	req, _ := http.NewRequest("POST", "https://auth.tidal.com/v1/oauth2/token", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(tidalDeviceClientID, tidalDeviceClientSecret)

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return DevicePollResult{Status: "error", Error: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 200 {
		var tokenResp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &tokenResp); err != nil {
			return DevicePollResult{Status: "error", Error: "failed to decode token: " + err.Error()}
		}

		countryCode := FetchTidalCountryCode(tokenResp.AccessToken)
		if countryCode != "" {
			fmt.Printf("[Tidal] Country code: %s\n", countryCode)
		}
		tokenData := &TidalTokenData{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ExpiresIn:    tokenResp.ExpiresIn,
			ExpiresAt:    time.Now().Unix() + int64(tokenResp.ExpiresIn),
			ClientID:     tidalDeviceClientID,
			CountryCode:  countryCode,
		}
		if err := SaveTidalToken(tokenData); err != nil {
			return DevicePollResult{Status: "error", Error: "failed to save token: " + err.Error()}
		}

		return DevicePollResult{Status: "authorized"}
	}

	// Erreurs attendues pendant le polling
	if resp.StatusCode == 400 {
		var errResp struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &errResp)
		switch errResp.Error {
		case "authorization_pending":
			return DevicePollResult{Status: "pending"}
		case "slow_down":
			return DevicePollResult{Status: "pending"} // ralentir le poll côté client
		case "expired_token":
			return DevicePollResult{Status: "expired", Error: "Authorization expired. Please start again."}
		case "access_denied":
			return DevicePollResult{Status: "denied", Error: "Access denied by user."}
		}
	}

	return DevicePollResult{Status: "error", Error: fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))}
}
