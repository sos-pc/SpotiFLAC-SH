package qobuz

import "testing"

func TestMapQualityToCommunity(t *testing.T) {
	tests := map[string]string{
		"27":    "24", // hi-res max
		"7":     "24", // hi-res standard
		"6":     "16", // lossless
		"5":     "16", // lossy → treated as 16
		"":      "16", // unset → 16
		"  7  ": "24",
	}
	for in, want := range tests {
		if got := mapQualityToCommunity(in); got != want {
			t.Errorf("mapQualityToCommunity(%q) = %q, want %q", in, got, want)
		}
	}
}

// The service has been seen to name the URL four different ways; the adapter
// must find it in all of them, and prefer download_url over a plain url, and
// top-level over nested, so the most specific value wins.
func TestExtractStreamingURL(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"top-level url", `{"url":"https://x/a.flac"}`, "https://x/a.flac"},
		{"top-level download_url", `{"download_url":"https://x/b.flac"}`, "https://x/b.flac"},
		{"nested url", `{"data":{"url":"https://x/c.flac"}}`, "https://x/c.flac"},
		{"nested download_url", `{"data":{"download_url":"https://x/d.flac"}}`, "https://x/d.flac"},
		{"download_url wins over url", `{"url":"https://x/plain","download_url":"https://x/dl"}`, "https://x/dl"},
		{"empty body", ``, ""},
		{"whitespace only", "   \n", ""},
		{"no url anywhere", `{"status":"ok"}`, ""},
		{"blank url is not a match", `{"url":"  ","data":{"url":"https://x/real"}}`, "https://x/real"},
		{"invalid json", `not json`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractStreamingURL([]byte(tc.body)); got != tc.want {
				t.Errorf("extractStreamingURL(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}
