package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

var (
	jellyfinURL = getEnv("JELLYFIN_URL", "http://localhost:8096")
	jwtSecret   = loadOrGenerateJWTSecret()
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadOrGenerateJWTSecret returns the JWT secret from env, persisted file, or generates a new one.
func loadOrGenerateJWTSecret() []byte {
	// 1. Variable d'environnement → priorité absolue
	if v := os.Getenv("JWT_SECRET"); v != "" {
		return []byte(v)
	}
	// 2. Fichier persisté dans le dossier config
	configDir, err := getConfigDir()
	if err == nil {
		secretFile := configDir + "/jwt_secret"
		if data, err := os.ReadFile(secretFile); err == nil && len(data) > 0 {
			return data
		}
		// 3. Générer un nouveau secret et le persister
		secret := generateRandomSecret()
		_ = os.MkdirAll(configDir, 0700)
		_ = os.WriteFile(secretFile, secret, 0600)
		return secret
	}
	// 4. Fallback
	return generateRandomSecret()
}

func generateRandomSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate JWT secret: " + err.Error())
	}
	dst := make([]byte, base64.RawURLEncoding.EncodedLen(len(b)))
	base64.RawURLEncoding.Encode(dst, b)
	return dst
}

// ─────────────────────────────────────────────────────────────────────────────
// UserProfile
// ─────────────────────────────────────────────────────────────────────────────

var bucketUsers = []byte("users")

type UserProfile struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	DisplayName string                 `json:"display_name"`
	IsAdmin     bool                   `json:"is_admin"`
	AvatarURL   string                 `json:"avatar_url,omitempty"`
	Settings    map[string]interface{} `json:"settings,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	// TokenVersion is embedded in every JWT minted for this user and
	// checked live on each request (v1Auth). Bumping it — currently done
	// automatically when Jellyfin's admin flag for this user changes —
	// invalidates every token issued before the bump, even ones that
	// haven't hit their 24h expiry yet. JWTs are otherwise fully
	// stateless and unrevocable.
	TokenVersion int `json:"token_version,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// AuthManager
// ─────────────────────────────────────────────────────────────────────────────

type AuthManager struct {
	db *bolt.DB
}

func NewAuthManager(db *bolt.DB) (*AuthManager, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketUsers)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &AuthManager{db: db}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Jellyfin auth
// ─────────────────────────────────────────────────────────────────────────────

type jellyfinAuthRequest struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
}

type jellyfinAuthResponse struct {
	User struct {
		Id     string `json:"Id"`
		Name   string `json:"Name"`
		Policy struct {
			IsAdministrator bool `json:"IsAdministrator"`
		} `json:"Policy"`
	} `json:"User"`
	AccessToken string `json:"AccessToken"`
}

func (a *AuthManager) AuthenticateWithJellyfin(username, password string) (*UserProfile, error) {
	payload, _ := json.Marshal(jellyfinAuthRequest{Username: username, Pw: password})

	req, err := http.NewRequest("POST",
		jellyfinURL+"/Users/AuthenticateByName",
		strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization",
		`MediaBrowser Client="SpotiFLAC", Device="SpotiFLAC", DeviceId="spotiflac", Version="1.0"`)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin unreachable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("invalid credentials")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("jellyfin error: HTTP %d", resp.StatusCode)
	}

	var jResp jellyfinAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&jResp); err != nil {
		return nil, fmt.Errorf("failed to parse jellyfin response: %v", err)
	}

	// Upsert UserProfile dans BoltDB
	profile, err := a.GetOrCreateUser(jResp.User.Id, jResp.User.Name, jResp.User.Policy.IsAdministrator)
	if err != nil {
		return nil, err
	}
	return profile, nil
}

func (a *AuthManager) GetOrCreateUser(jellyfinID, name string, isAdmin bool) (*UserProfile, error) {
	var profile UserProfile
	err := a.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		data := b.Get([]byte(jellyfinID))
		if data != nil {
			if err := json.Unmarshal(data, &profile); err != nil {
				return err
			}
			// Mettre à jour nom + isAdmin
			profile.Name = jellyfinID
			profile.DisplayName = name
			if profile.IsAdmin != isAdmin {
				// Privilege change: any JWT already issued for this user
				// carries the old admin flag baked in and would otherwise
				// stay valid (with the stale privilege level) until its
				// 24h expiry. Bumping TokenVersion invalidates them all.
				profile.TokenVersion++
			}
			profile.IsAdmin = isAdmin
			profile.UpdatedAt = time.Now()
		} else {
			// Nouveau user
			profile = UserProfile{
				ID:          jellyfinID,
				Name:        jellyfinID,
				DisplayName: name,
				IsAdmin:     isAdmin,
				Settings:    make(map[string]interface{}),
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}
		}
		data, err := json.Marshal(profile)
		if err != nil {
			return err
		}
		return b.Put([]byte(jellyfinID), data)
	})
	return &profile, err
}

func (a *AuthManager) GetUser(userID string) (*UserProfile, error) {
	var profile UserProfile
	err := a.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return fmt.Errorf("users bucket not found")
		}
		data := b.Get([]byte(userID))
		if data == nil {
			return fmt.Errorf("user not found: %s", userID)
		}
		return json.Unmarshal(data, &profile)
	})
	return &profile, err
}

func (a *AuthManager) SaveUserSettings(userID string, settings map[string]interface{}) error {
	return a.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return fmt.Errorf("users bucket not found")
		}
		var profile UserProfile
		data := b.Get([]byte(userID))
		if data != nil {
			json.Unmarshal(data, &profile)
		}
		profile.Settings = settings
		profile.UpdatedAt = time.Now()
		data, err := json.Marshal(profile)
		if err != nil {
			return err
		}
		return b.Put([]byte(userID), data)
	})
}

func (a *AuthManager) GetAllUsers() ([]UserProfile, error) {
	var users []UserProfile
	err := a.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var u UserProfile
			if err := json.Unmarshal(v, &u); err == nil {
				users = append(users, u)
			}
			return nil
		})
	})
	return users, err
}

// ─────────────────────────────────────────────────────────────────────────────
// JWT (HMAC-SHA256, sans dépendance externe)
// ─────────────────────────────────────────────────────────────────────────────

type JWTClaims struct {
	UserID       string   `json:"uid"`
	DisplayName  string   `json:"name"`
	IsAdmin      bool     `json:"admin"`
	ExpiresAt    int64    `json:"exp"`
	Permissions  []string `json:"perms,omitempty"` // set only for API-key-derived claims; empty = full session, unrestricted
	IsAPIKey     bool     `json:"is_key,omitempty"`
	TokenVersion int      `json:"tv,omitempty"` // must match UserProfile.TokenVersion at validation time; see UserProfile.TokenVersion
	// Scope narrows what this token may be used for. Empty = normal
	// session, usable on any endpoint the user/key is otherwise allowed
	// to reach. "stream" = short-lived token minted for endpoints the
	// browser can't attach an Authorization header to — SSE connections
	// and the job-download link (see GenerateStreamToken, streamScopedPaths,
	// isJobDownloadPath) — v1Auth rejects it everywhere else.
	Scope string `json:"scope,omitempty"`
}

// streamTokenTTL bounds how long a token minted for an SSE connection or a
// browser download link stays valid. Short on purpose: this token ends up
// in the request URL (neither EventSource nor an <a href> download can set
// custom headers), so it's the one credential that routinely leaks into
// reverse-proxy access logs / browser history — unlike the 24h session JWT,
// a leaked stream token is worthless within a minute.
const streamTokenTTL = 60 * time.Second

func signClaims(claims JWTClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := hmacSign(header + "." + body)
	return header + "." + body + "." + sig, nil
}

func GenerateJWT(profile *UserProfile) (string, error) {
	return signClaims(JWTClaims{
		UserID:       profile.ID,
		DisplayName:  profile.DisplayName,
		IsAdmin:      profile.IsAdmin,
		ExpiresAt:    time.Now().Add(24 * time.Hour).Unix(),
		TokenVersion: profile.TokenVersion,
	})
}

// GenerateStreamToken mints a short-lived token carrying the same identity
// as an already-validated session/API-key claims, for use on the narrow set
// of endpoints in streamScopedPaths/isJobDownloadPath. See streamTokenTTL.
func GenerateStreamToken(claims *JWTClaims) (string, error) {
	return signClaims(JWTClaims{
		UserID:      claims.UserID,
		DisplayName: claims.DisplayName,
		IsAdmin:     claims.IsAdmin,
		ExpiresAt:   time.Now().Add(streamTokenTTL).Unix(),
		Scope:       "stream",
	})
}

func ValidateJWT(token string) (*JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}
	expected := hmacSign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, fmt.Errorf("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid token payload")
	}
	var claims JWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("invalid token claims")
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, fmt.Errorf("token expired")
	}
	return &claims, nil
}

func hmacSign(data string) string {
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ─────────────────────────────────────────────────────────────────────────────
// Middleware + contexte
// ─────────────────────────────────────────────────────────────────────────────

type contextKey string

const contextKeyUser contextKey = "user"

func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			token = auth[7:]
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		claims, err := ValidateJWT(token)
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), contextKeyUser, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GetUserFromContext(r *http.Request) *JWTClaims {
	claims, _ := r.Context().Value(contextKeyUser).(*JWTClaims)
	return claims
}
