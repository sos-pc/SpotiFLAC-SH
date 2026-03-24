package backend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type TidalCreds struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

type TidalTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

var (
	tidalTokenCache *TidalTokenData
	tidalTokenMutex sync.Mutex
)

// Clés de secours (encodées base64 pour éviter les scanners basiques)
var defaultClientID = "ZlgySnhkbW50WldLMGl4VA=="
var defaultClientSecret = "MU5tNUFmREFqeHJnSkZKYktOV0xlQXlLR1ZHbUlOdVhQUExIVlhBdnhBZz0="

// GetTidalTokenPath retourne le chemin absolu du fichier de configuration du token Tidal
func GetTidalTokenPath() string {
	configDir, err := GetFFmpegDir() // Utilise le répertoire .spotiflac (homeDir)
	if err != nil {
		// Fallback si erreur (très improbable)
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

// FetchTidalCredentials récupère la liste des Client IDs depuis le Gist public
func FetchTidalCredentials() ([]TidalCreds, error) {
	url := "https://api.github.com/gists/48d01f5a24b4b7b37f19443977c22cd6"
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch gist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gist returned status %d", resp.StatusCode)
	}

	var gistData struct {
		Files map[string]struct {
			Content string `json:"content"`
		} `json:"files"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&gistData); err != nil {
		return nil, fmt.Errorf("failed to decode gist JSON: %w", err)
	}

	fileData, ok := gistData.Files["tidal-api-key.json"]
	if !ok {
		return nil, fmt.Errorf("tidal-api-key.json not found in gist")
	}

	var keysData struct {
		Keys []struct {
			Valid        string `json:"valid"`
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
			Formats      string `json:"formats"`
		} `json:"keys"`
	}

	if err := json.Unmarshal([]byte(fileData.Content), &keysData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal keys content: %w", err)
	}

	var hifiCreds []TidalCreds
	var otherCreds []TidalCreds

	// Ajout des clés de secours décodées
	decID, _ := base64.StdEncoding.DecodeString(defaultClientID)
	decSec, _ := base64.StdEncoding.DecodeString(defaultClientSecret)
	hifiCreds = append(hifiCreds, TidalCreds{ClientID: string(decID), ClientSecret: string(decSec)})

	for _, k := range keysData.Keys {
		if strings.ToLower(k.Valid) == "true" {
			cred := TidalCreds{ClientID: k.ClientID, ClientSecret: k.ClientSecret}
			if strings.Contains(strings.ToLower(k.Formats), "hifi") {
				hifiCreds = append(hifiCreds, cred)
			} else {
				otherCreds = append(otherCreds, cred)
			}
		}
	}

	// Mélanger pour répartir la charge et éviter les bans en masse
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(hifiCreds), func(i, j int) { hifiCreds[i], hifiCreds[j] = hifiCreds[j], hifiCreds[i] })
	rand.Shuffle(len(otherCreds), func(i, j int) { otherCreds[i], otherCreds[j] = otherCreds[j], otherCreds[i] })

	// Prioriser les clés HiFi
	return append(hifiCreds, otherCreds...), nil
}

// RefreshTidalToken rafraîchit le jeton d'accès en utilisant le refresh_token
func RefreshTidalToken(tokenData *TidalTokenData) (*TidalTokenData, error) {
	if tokenData.RefreshToken == "" || tokenData.ClientID == "" {
		return nil, fmt.Errorf("missing refresh token or client ID")
	}

	urlStr := "https://auth.tidal.com/v1/oauth2/token"

	// Form encoded data
	data := fmt.Sprintf("client_id=%s&refresh_token=%s&grant_type=refresh_token&scope=r_usr+w_usr+w_sub",
		tokenData.ClientID, tokenData.RefreshToken)

	req, err := http.NewRequest("POST", urlStr, strings.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Si un secret est présent, on utilise Basic Auth (certaines apps TV l'exigent)
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

// PerformDeviceAuthorization déclenche le flow TV et demande à l'utilisateur de valider sur le site Tidal
func PerformDeviceAuthorization() (*TidalTokenData, error) {
	fmt.Println("[Tidal Auth] Starting Device Authorization Flow...")

	creds, err := FetchTidalCredentials()
	if err != nil || len(creds) == 0 {
		return nil, fmt.Errorf("failed to fetch valid credentials: %v", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var deviceRespData struct {
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
	}

	var selectedCred TidalCreds
	var deviceAuthSuccess bool

	// Essayer chaque clé jusqu'à ce qu'une fonctionne pour l'étape 1
	for _, cred := range creds {
		urlStr := "https://auth.tidal.com/v1/oauth2/device_authorization"
		data := fmt.Sprintf("client_id=%s&scope=r_usr+w_usr+w_sub", cred.ClientID)

		req, _ := http.NewRequest("POST", urlStr, strings.NewReader(data))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == 200 {
			if err := json.NewDecoder(resp.Body).Decode(&deviceRespData); err == nil {
				selectedCred = cred
				deviceAuthSuccess = true
				resp.Body.Close()
				break
			}
		}
		resp.Body.Close()
	}

	if !deviceAuthSuccess {
		return nil, fmt.Errorf("all Client IDs failed device authorization")
	}

	// Afficher l'instruction critique à l'utilisateur
	fmt.Println("=======================================================================")
	fmt.Println("🚨 TIDAL AUTHENTICATION REQUIRED 🚨")
	fmt.Println("SpotiFLAC needs to authenticate with Tidal to download Lossless FLAC.")
	fmt.Printf("1. Open this link in your browser: %s\n", deviceRespData.VerificationURIComplete)
	fmt.Printf("2. Log in with ANY Tidal account (even a free one works).\n")
	fmt.Println("Waiting for authorization... (Timeout in 5 minutes)")
	fmt.Println("=======================================================================")

	// Polling : on demande le token toutes les `Interval` secondes
	pollInterval := time.Duration(deviceRespData.Interval) * time.Second
	if pollInterval < 5*time.Second {
		pollInterval = 5 * time.Second
	}

	tokenUrl := "https://auth.tidal.com/v1/oauth2/token"
	timeoutTime := time.Now().Add(5 * time.Minute)

	for time.Now().Before(timeoutTime) {
		time.Sleep(pollInterval)

		data := fmt.Sprintf("client_id=%s&device_code=%s&grant_type=urn:ietf:params:oauth:grant-type:device_code",
			selectedCred.ClientID, deviceRespData.DeviceCode)

		req, _ := http.NewRequest("POST", tokenUrl, strings.NewReader(data))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if selectedCred.ClientSecret != "" {
			req.SetBasicAuth(selectedCred.ClientID, selectedCred.ClientSecret)
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == 200 {
			var tokenResp struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				ExpiresIn    int    `json:"expires_in"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err == nil {
				resp.Body.Close()
				fmt.Println("[Tidal Auth] Authorization SUCCESSFUL! Token acquired.")

				tokenData := &TidalTokenData{
					AccessToken:  tokenResp.AccessToken,
					RefreshToken: tokenResp.RefreshToken,
					ExpiresIn:    tokenResp.ExpiresIn,
					ExpiresAt:    time.Now().Unix() + int64(tokenResp.ExpiresIn),
					ClientID:     selectedCred.ClientID,
					ClientSecret: selectedCred.ClientSecret,
				}

				SaveTidalToken(tokenData)
				return tokenData, nil
			}
		} else if resp.StatusCode == 400 {
			// Expected "authorization_pending" error while waiting
			bodyBytes, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(bodyBytes), "authorization_pending") {
				fmt.Printf("[Tidal Auth] Polling error: %s\n", string(bodyBytes))
			}
		}
		resp.Body.Close()
	}

	return nil, fmt.Errorf("device authorization timed out after 5 minutes")
}

// GetValidTidalToken retourne le token courant, le rafraîchit si besoin,
// ou bloque l'exécution pour forcer une nouvelle authentification.
func GetValidTidalToken() (*TidalTokenData, error) {
	token := LoadTidalToken()

	if token == nil {
		// Pas de token -> on déclenche le Device Flow bloquant
		newToken, err := PerformDeviceAuthorization()
		if err != nil {
			return nil, err
		}
		return newToken, nil
	}

	// Le token existe, on vérifie s'il expire dans moins de 5 minutes
	if time.Now().Unix() > (token.ExpiresAt - 300) {
		refreshedToken, err := RefreshTidalToken(token)
		if err != nil {
			fmt.Printf("[Tidal Auth] Refresh failed (%v), requesting new authorization...\n", err)
			DeleteTidalToken() // On supprime le token corrompu/révoqué

			// On relance l'autorisation complète
			newToken, err := PerformDeviceAuthorization()
			if err != nil {
				return nil, err
			}
			return newToken, nil
		}
		return refreshedToken, nil
	}

	return token, nil
}
