package spotify

import (
	"regexp"
	"testing"
)

func TestSpotifyIDToGID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		want    string
		wantErr bool
	}{
		// '0' is index 0 in the base62 alphabet → value 0 → all-zero GID.
		{name: "all zero", id: "0", want: "00000000000000000000000000000000"},
		// '1' is index 1 → value 1 → 31 zeros + 1.
		{name: "one", id: "1", want: "00000000000000000000000000000001"},
		// "10" → 1*62 + 0 = 62 = 0x3e.
		{name: "sixty-two", id: "10", want: "0000000000000000000000000000003e"},
		{name: "empty", id: "", wantErr: true},
		{name: "invalid char dash", id: "abc-def", wantErr: true},
		{name: "invalid char punct", id: "!", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spotifyIDToGID(tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("spotifyIDToGID(%q) = %q, want error", tt.id, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("spotifyIDToGID(%q) unexpected error: %v", tt.id, err)
			}
			if got != tt.want {
				t.Fatalf("spotifyIDToGID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestSpotifyIDToGID_RealisticIDInvariants(t *testing.T) {
	// A real 22-char base62 track ID must map to exactly 32 lowercase hex
	// characters, even without a known reference GID to compare against.
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	got, err := spotifyIDToGID("4iV5W9uYEdYUVa79Axb7Rh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hex32.MatchString(got) {
		t.Fatalf("GID %q is not 32 lowercase hex chars", got)
	}
}

func TestParseTrackISRC(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		wantErr bool
	}{
		{
			name:    "valid isrc",
			payload: `{"external_id":[{"type":"isrc","id":"USRC17607839"}]}`,
			want:    "USRC17607839",
		},
		{
			name:    "lowercase is uppercased",
			payload: `{"external_id":[{"type":"isrc","id":"gbaye0601498"}]}`,
			want:    "GBAYE0601498",
		},
		{
			name:    "picks isrc among other ids",
			payload: `{"external_id":[{"type":"upc","id":"00602547"},{"type":"isrc","id":"USRC17607839"}]}`,
			want:    "USRC17607839",
		},
		{
			name:    "no isrc present",
			payload: `{"external_id":[{"type":"upc","id":"00602547"}]}`,
			wantErr: true,
		},
		{
			name:    "empty external_id",
			payload: `{"external_id":[]}`,
			wantErr: true,
		},
		{
			name:    "malformed isrc wrong length",
			payload: `{"external_id":[{"type":"isrc","id":"ABC"}]}`,
			wantErr: true,
		},
		{
			name:    "malformed isrc bad char",
			payload: `{"external_id":[{"type":"isrc","id":"US-RC1760783"}]}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			payload: `{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTrackISRC([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseTrackISRC(%s) = %q, want error", tt.payload, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTrackISRC(%s) unexpected error: %v", tt.payload, err)
			}
			if got != tt.want {
				t.Fatalf("parseTrackISRC(%s) = %q, want %q", tt.payload, got, tt.want)
			}
		})
	}
}
