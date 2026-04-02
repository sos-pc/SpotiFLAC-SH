package util

import (
	"testing"
	"unicode/utf8"
)

// ─── SanitizeFilename ────────────────────────────────────────────────────────

func TestSanitizeFilename_Normal(t *testing.T) {
	got := SanitizeFilename("Hello World")
	if got != "Hello World" {
		t.Errorf("expected %q, got %q", "Hello World", got)
	}
}

func TestSanitizeFilename_ForbiddenChars(t *testing.T) {
	// < > : " \ | ? * must be replaced by a space
	got := SanitizeFilename(`AC/DC: "Back in Black" <live>`)
	for _, ch := range []rune{'<', '>', ':', '"', '\\', '|', '?', '*'} {
		for _, r := range got {
			if r == ch {
				t.Errorf("forbidden char %q found in result %q", ch, got)
			}
		}
	}
}

func TestSanitizeFilename_SlashReplaced(t *testing.T) {
	got := SanitizeFilename("feat/remix")
	for _, r := range got {
		if r == '/' {
			t.Errorf("slash should be replaced, got %q", got)
		}
	}
}

func TestSanitizeFilename_ControlChars(t *testing.T) {
	// Null byte and other control chars should be stripped
	got := SanitizeFilename("title\x00name")
	for _, r := range got {
		if r < 0x20 {
			t.Errorf("control char %U still present in %q", r, got)
		}
	}
}

func TestSanitizeFilename_LeadingTrailingDots(t *testing.T) {
	got := SanitizeFilename("...name...")
	if len(got) > 0 && (got[0] == '.' || got[len(got)-1] == '.') {
		t.Errorf("leading/trailing dots not stripped, got %q", got)
	}
}

func TestSanitizeFilename_Empty(t *testing.T) {
	got := SanitizeFilename("")
	if got != "Unknown" {
		t.Errorf("empty input should return %q, got %q", "Unknown", got)
	}
}

func TestSanitizeFilename_OnlyForbidden(t *testing.T) {
	// Only forbidden chars → collapses to spaces → trimmed → "Unknown"
	got := SanitizeFilename(`<>:"|?*`)
	if got != "Unknown" {
		t.Errorf("only-forbidden input should return %q, got %q", "Unknown", got)
	}
}

func TestSanitizeFilename_MultipleSpaces(t *testing.T) {
	got := SanitizeFilename("hello   world")
	if got != "hello world" {
		t.Errorf("multiple spaces should collapse to one, got %q", got)
	}
}

func TestSanitizeFilename_InvalidUTF8(t *testing.T) {
	// Must not panic; the valid prefix and suffix must be preserved.
	input := "bad\xff\xfestring"
	got := SanitizeFilename(input)
	if !utf8.ValidString(got) {
		t.Errorf("result is not valid UTF-8: %q", got)
	}
	if !containsSubstring(got, "bad") || !containsSubstring(got, "string") {
		t.Errorf("valid parts of input should be preserved, got %q", got)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

// ─── GetFirstArtist ──────────────────────────────────────────────────────────

func TestGetFirstArtist_Single(t *testing.T) {
	got := GetFirstArtist("Adele")
	if got != "Adele" {
		t.Errorf("expected %q, got %q", "Adele", got)
	}
}

func TestGetFirstArtist_CommaSeparated(t *testing.T) {
	got := GetFirstArtist("Drake, Future")
	if got != "Drake" {
		t.Errorf("expected %q, got %q", "Drake", got)
	}
}

func TestGetFirstArtist_Ampersand(t *testing.T) {
	got := GetFirstArtist("Jay-Z & Kanye West")
	if got != "Jay-Z" {
		t.Errorf("expected %q, got %q", "Jay-Z", got)
	}
}

func TestGetFirstArtist_Feat(t *testing.T) {
	got := GetFirstArtist("Eminem feat. Rihanna")
	if got != "Eminem" {
		t.Errorf("expected %q, got %q", "Eminem", got)
	}
}

func TestGetFirstArtist_Ft(t *testing.T) {
	got := GetFirstArtist("Post Malone ft. Swae Lee")
	if got != "Post Malone" {
		t.Errorf("expected %q, got %q", "Post Malone", got)
	}
}

func TestGetFirstArtist_Featuring(t *testing.T) {
	got := GetFirstArtist("Kendrick Lamar featuring SZA")
	if got != "Kendrick Lamar" {
		t.Errorf("expected %q, got %q", "Kendrick Lamar", got)
	}
}

func TestGetFirstArtist_Empty(t *testing.T) {
	got := GetFirstArtist("")
	if got != "" {
		t.Errorf("empty input should return %q, got %q", "", got)
	}
}
