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
	pkceVerifierMutex sync.Mutex
)

func generateRandomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// GenerateTidalAuthURL génère le lien de connexion PKCE pour le client Web Tidal
func GenerateTidalAuthURL() string {
	pkceVerifierMutex.Lock()
	defer pkceVerifierMutex.Unlock()

	pkceVerifier = generateRandomString(32)
	hash := sha256.Sum256([]byte(pkceVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	redirectURI := "https://listen.tidal.com/login/auth"

	params := url.Values{}
	params.Add("client_id", "txNoH4kkV41MfH25")
	params.Add("response_type", "code")
	params.Add("redirect_uri", redirectURI)
	params.Add("code_challenge", challenge)
	params.Add("code_challenge_method", "S256")
	params.Add("lang", "en")
	params.Add("appMode", "web")
	// On demande les scopes basiques (le backend Tidal Web OIDC gère les droits via l'abonnement du compte)
	params.Add("scope", "r_usr w_usr w_sub")

	return "https://login.tidal.com/authorize?" + params.Encode()
}

// ExchangeTidalAuthCode échange le code d'autorisation (extrait de l'URL de redirection) contre un vrai token
func ExchangeTidalAuthCode(redirectedURL string) error {
	pkceVerifierMutex.Lock()
	verifier := pkceVerifier
	pkceVerifierMutex.Unlock()

	if verifier == "" {
		return fmt.Errorf("no pending authentication found. Please generate a login URL first")
	}

	// Nettoyer l'URL si l'utilisateur a collé toute la barre d'adresse
	redirectedURL = strings.TrimSpace(redirectedURL)

	var code string
	if strings.Contains(redirectedURL, "code=") {
		parsedURL, err := url.Parse(redirectedURL)
		if err == nil {
			code = parsedURL.Query().Get("code")
		} else {
			// Fallback parsing manuel si URL.Parse échoue sur des formats bizarres
			parts := strings.Split(redirectedURL, "code=")
			if len(parts) > 1 {
				code = strings.Split(parts[1], "&")[0]
			}
		}
	} else {
		// L'utilisateur a peut-être juste collé le code brut
		code = redirectedURL
	}

	if code == "" {
		return fmt.Errorf("no 'code' parameter found in the provided string")
	}

	tokenUrl := "https://auth.tidal.com/v1/oauth2/token"
	v := url.Values{}
	v.Set("client_id", "txNoH4kkV41MfH25")
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", "https://listen.tidal.com/login/auth")
	v.Set("code_verifier", verifier)

	req, _ := http.NewRequest("POST", tokenUrl, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
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
		ClientID:     "txNoH4kkV41MfH25",
	}

	if err := SaveTidalToken(tokenData); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	// Succès : on nettoie le verifier en attente
	pkceVerifierMutex.Lock()
	pkceVerifier = ""
	pkceVerifierMutex.Unlock()

	return nil
}
