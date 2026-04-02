package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

type TidalTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	CountryCode  string `json:"country_code,omitempty"`
}

// FetchTidalCountryCode appelle GET /v1/sessions pour récupérer le pays du compte.
func FetchTidalCountryCode(accessToken string) string {
	req, _ := http.NewRequest("GET", "https://api.tidal.com/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Tidal-Token", tidalDeviceClientID)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var data struct {
		CountryCode string `json:"countryCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}
	return data.CountryCode
}

var (
	tidalTokenCache *TidalTokenData
	tidalTokenMutex sync.Mutex
)

// GetTidalTokenPath retourne le chemin absolu du fichier de configuration du token Tidal
func GetTidalTokenPath() string {
	configDir, err := util.GetFFmpegDir() // Utilise le répertoire .spotiflac (homeDir)
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".spotiflac", "tidal_token.json")
	}
	return filepath.Join(configDir, "tidal_token.json")
}

// LoadTidalToken charge le token depuis le disque s'il n'est pas en cache
func LoadTidalToken() *TidalTokenData {
	tidalTokenMutex.Lock()
	defer tidalTokenMutex.Unlock()

	if tidalTokenCache != nil {
		return tidalTokenCache
	}

	path := GetTidalTokenPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var tokenData TidalTokenData
	if err := json.Unmarshal(data, &tokenData); err != nil {
		return nil
	}

	tidalTokenCache = &tokenData
	return &tokenData
}

// SaveTidalToken sauvegarde le token sur le disque et met à jour le cache
func SaveTidalToken(tokenData *TidalTokenData) error {
	tidalTokenMutex.Lock()
	defer tidalTokenMutex.Unlock()

	tidalTokenCache = tokenData
	path := GetTidalTokenPath()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(tokenData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// DeleteTidalToken supprime le token (en cas d'expiration/révocation)
func DeleteTidalToken() {
	tidalTokenMutex.Lock()
	defer tidalTokenMutex.Unlock()
	tidalTokenCache = nil
	os.Remove(GetTidalTokenPath())
}

// RefreshTidalToken rafraîchit le jeton d'accès en utilisant le refresh_token
func RefreshTidalToken(tokenData *TidalTokenData) (*TidalTokenData, error) {
	if tokenData.RefreshToken == "" || tokenData.ClientID == "" {
		return nil, fmt.Errorf("missing refresh token or client ID")
	}

	urlStr := "https://auth.tidal.com/v1/oauth2/token"

	// Form encoded data
	v := url.Values{}
	v.Set("client_id", tokenData.ClientID)
	v.Set("refresh_token", tokenData.RefreshToken)
	v.Set("grant_type", "refresh_token")

	req, err := http.NewRequest("POST", urlStr, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Si un secret est présent (legacy TV flow), on utilise Basic Auth
	if tokenData.ClientSecret != "" {
		req.SetBasicAuth(tokenData.ClientID, tokenData.ClientSecret)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var respData struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respData); err != nil {
		return nil, fmt.Errorf("failed to decode refresh response: %w", err)
	}

	// Mise à jour du token
	tokenData.AccessToken = respData.AccessToken
	tokenData.ExpiresIn = respData.ExpiresIn
	tokenData.ExpiresAt = time.Now().Unix() + int64(respData.ExpiresIn)

	SaveTidalToken(tokenData)
	fmt.Println("[Tidal Auth] Token refreshed successfully.")
	return tokenData, nil
}

// GetTidalCountryCode retourne le pays du compte Tidal connecté, ou "US" si absent.
func GetTidalCountryCode() string {
	token := LoadTidalToken()
	if token != nil && token.CountryCode != "" {
		return token.CountryCode
	}
	return "US"
}

// GetValidTidalToken retourne le token courant, le rafraîchit si besoin.
func GetValidTidalToken() (*TidalTokenData, error) {
	token := LoadTidalToken()

	if token == nil {
		return nil, fmt.Errorf("tidal authentication required: no token found. Configure it in settings")
	}

	// Le token existe, on vérifie s'il expire dans moins de 5 minutes
	if time.Now().Unix() > (token.ExpiresAt - 300) {
		refreshedToken, err := RefreshTidalToken(token)
		if err != nil {
			fmt.Printf("[Tidal Auth] Refresh failed (%v), token is invalid.\n", err)
			DeleteTidalToken() // On supprime le token corrompu/révoqué
			return nil, fmt.Errorf("tidal token expired and refresh failed")
		}
		return refreshedToken, nil
	}

	// Fetch country lazily pour les tokens sauvegardés avant l'ajout du champ
	if token.CountryCode == "" {
		if cc := FetchTidalCountryCode(token.AccessToken); cc != "" {
			token.CountryCode = cc
			SaveTidalToken(token)
			fmt.Printf("[Tidal Auth] Country code fetched: %s\n", cc)
		}
	}

	return token, nil
}
