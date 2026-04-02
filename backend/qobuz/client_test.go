package qobuz

import (
	"strings"
	"testing"
)

// ─── buildQobuzAPIURL ─────────────────────────────────────────────────────────

func TestBuildQobuzAPIURL(t *testing.T) {
	tests := []struct {
		name        string
		apiBase     string
		trackID     int64
		quality     string
		wantSep     string // "?" ou "&"
	}{
		{
			name:    "proxy qbz.afkarxyz.qzz.io → séparateur ?",
			apiBase: "https://qbz.afkarxyz.qzz.io/track/getFileUrl?track_id=",
			trackID: 123456789,
			quality: "27",
			wantSep: "?",
		},
		{
			name:    "proxy qbz.afkarxyz.fun → séparateur ?",
			apiBase: "https://qbz.afkarxyz.fun/track/getFileUrl?track_id=",
			trackID: 987654321,
			quality: "6",
			wantSep: "?",
		},
		{
			name:    "autre proxy → séparateur &",
			apiBase: "https://other.proxy.example/track?track_id=",
			trackID: 111222333,
			quality: "7",
			wantSep: "&",
		},
		{
			name:    "URL standard → séparateur &",
			apiBase: "https://api.qobuz.com/track/getFileUrl?track_id=",
			trackID: 42,
			quality: "6",
			wantSep: "&",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildQobuzAPIURL(tt.apiBase, tt.trackID, tt.quality)

			// L'URL doit contenir l'ID de track
			idStr := "123456789"
			switch tt.trackID {
			case 987654321:
				idStr = "987654321"
			case 111222333:
				idStr = "111222333"
			case 42:
				idStr = "42"
			}
			if !strings.Contains(got, idStr) {
				t.Errorf("URL %q ne contient pas l'ID %s", got, idStr)
			}

			// L'URL doit contenir la qualité
			if !strings.Contains(got, tt.quality) {
				t.Errorf("URL %q ne contient pas la qualité %s", got, tt.quality)
			}

			// Vérifier le bon séparateur entre l'ID et quality=
			qualityPart := tt.wantSep + "quality=" + tt.quality
			if !strings.Contains(got, qualityPart) {
				t.Errorf("URL %q : attendu séparateur %q avant quality=, got URL complète", got, tt.wantSep)
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

	t.Run("proxy afkarxyz ne doit pas utiliser &", func(t *testing.T) {
		url := buildQobuzAPIURL("https://qbz.afkarxyz.fun/", 1, "6")
		// quality= doit être précédé de ? et non de &
		if strings.Contains(url, "&quality=") {
			t.Errorf("proxy afkarxyz ne doit pas utiliser & : %q", url)
		}
	})

	t.Run("proxy non-afkarxyz ne doit pas utiliser ? avant quality", func(t *testing.T) {
		url := buildQobuzAPIURL("https://other.example/", 1, "6")
		if strings.Contains(url, "?quality=") {
			t.Errorf("proxy standard ne doit pas utiliser ? avant quality= : %q", url)
		}
	})
}
