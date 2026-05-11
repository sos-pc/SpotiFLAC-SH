package qobuz

import (
	"fmt"
	"strings"
	"testing"
)

// ─── buildQobuzAPIURL ─────────────────────────────────────────────────────────

func TestBuildQobuzAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		apiBase string
		trackID int64
		quality string
	}{
		{
			name:    "proxy standard → séparateur &",
			apiBase: "https://other.proxy.example/track?track_id=",
			trackID: 111222333,
			quality: "7",
		},
		{
			name:    "URL standard → séparateur &",
			apiBase: "https://api.qobuz.com/track/getFileUrl?track_id=",
			trackID: 42,
			quality: "6",
		},
		{
			name:    "proxy dab.yeet.su → séparateur &",
			apiBase: "https://dab.yeet.su/api/stream?trackId=",
			trackID: 20882393,
			quality: "6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildQobuzAPIURL(tt.apiBase, tt.trackID, tt.quality)

			// L'URL doit contenir l'ID de track
			idStr := fmt.Sprintf("%d", tt.trackID)
			if !strings.Contains(got, idStr) {
				t.Errorf("URL %q ne contient pas l'ID %s", got, idStr)
			}

			// L'URL doit contenir la qualité
			if !strings.Contains(got, tt.quality) {
				t.Errorf("URL %q ne contient pas la qualité %s", got, tt.quality)
			}

			// Tous les proxies utilisent désormais le séparateur &
			qualityPart := "&quality=" + tt.quality
			if !strings.Contains(got, qualityPart) {
				t.Errorf("URL %q : attendu séparateur & avant quality=", got)
			}
		})
	}
}

func TestBuildQobuzAPIURL_IDEmbedded(t *testing.T) {
	t.Run("l'ID est bien inclus dans l'URL", func(t *testing.T) {
		url := buildQobuzAPIURL("https://base.example/", 999, "6")
		if !strings.Contains(url, "999") {
			t.Errorf("URL %q ne contient pas l'ID 999", url)
		}
	})

	t.Run("la qualité est bien incluse dans l'URL", func(t *testing.T) {
		url := buildQobuzAPIURL("https://base.example/", 1, "27")
		if !strings.Contains(url, "27") {
			t.Errorf("URL %q ne contient pas la qualité 27", url)
		}
	})

	t.Run("tous les proxies utilisent & avant quality", func(t *testing.T) {
		url := buildQobuzAPIURL("https://any.proxy.example/", 1, "6")
		if !strings.Contains(url, "&quality=") {
			t.Errorf("attendu &quality= dans l'URL : %q", url)
		}
	})
}
