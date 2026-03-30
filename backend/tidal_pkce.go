package backend

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	pkceVerifier      string
	pkceClientID      string // client_id utilisé pour générer l'URL — doit être réutilisé à l'échange
	pkceVerifierMutex sync.Mutex
)

func generateRandomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// GenerateTidalAuthURL génère le lien de connexion PKCE pour le client Web Tidal.
// Le client_id est auto-découvert depuis listen.tidal.com (mis en cache 24h).
func GenerateTidalAuthURL() string {
	clientID := GetTidalClientID()

	pkceVerifierMutex.Lock()
	defer pkceVerifierMutex.Unlock()

	pkceVerifier = generateRandomString(32)
	pkceClientID = clientID
	hash := sha256.Sum256([]byte(pkceVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	redirectURI := "https://listen.tidal.com/login/auth"

	params := url.Values{}
	params.Add("client_id", clientID)
	params.Add("response_type", "code")
	params.Add("redirect_uri", redirectURI)
	params.Add("code_challenge", challenge)
	params.Add("code_challenge_method", "S256")
	params.Add("lang", "en")
	params.Add("appMode", "web")
	params.Add("scope", "r_usr w_usr w_sub")

	return "https://login.tidal.com/authorize?" + params.Encode()
}

// ExchangeTidalAuthCode échange le code d'autorisation contre un token.
// Utilise le même client_id que lors de la génération de l'URL.
func ExchangeTidalAuthCode(redirectedURL string) error {
	pkceVerifierMutex.Lock()
	verifier := pkceVerifier
	clientID := pkceClientID
	pkceVerifierMutex.Unlock()

	if verifier == "" {
		return fmt.Errorf("no pending authentication found. Please generate a login URL first")
	}
	if clientID == "" {
		clientID = GetTidalClientID()
	}

	redirectedURL = strings.TrimSpace(redirectedURL)

	var code string
	if strings.Contains(redirectedURL, "code=") {
		parsedURL, err := url.Parse(redirectedURL)
		if err == nil {
			code = parsedURL.Query().Get("code")
		} else {
			parts := strings.Split(redirectedURL, "code=")
			if len(parts) > 1 {
				code = strings.Split(parts[1], "&")[0]
			}
		}
	} else {
		code = redirectedURL
	}

	if code == "" {
		return fmt.Errorf("no 'code' parameter found in the provided string")
	}

	tokenURL := "https://auth.tidal.com/v1/oauth2/token"
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", "https://listen.tidal.com/login/auth")
	v.Set("code_verifier", verifier)

	req, _ := http.NewRequest("POST", tokenURL, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		// Si le client_id est refusé, invalider le cache pour forcer un re-scraping
		if resp.StatusCode == 400 || resp.StatusCode == 401 {
			InvalidateTidalClientIDCache()
		}
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	tokenData := &TidalTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    time.Now().Unix() + int64(tokenResp.ExpiresIn),
		ClientID:     clientID,
	}

	if err := SaveTidalToken(tokenData); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	pkceVerifierMutex.Lock()
	pkceVerifier = ""
	pkceClientID = ""
	pkceVerifierMutex.Unlock()

	return nil
}
