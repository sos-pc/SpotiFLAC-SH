package main

import "testing"

// TestAudioContentType is the regression test for Q9: the browser-download
// handler used to claim Content-Type: audio/flac unconditionally, even
// though Amazon Music downloads land as .m4a and some fallback paths
// produce .mp3 (see filemanager.go's own .flac/.mp3/.m4a handling).
func TestAudioContentType(t *testing.T) {
	cases := []struct {
		filename string
		want     string
	}{
		{"track.flac", "audio/flac"},
		{"track.mp3", "audio/mpeg"},
		{"track.m4a", "audio/mp4"},
		{"track.MP3", "audio/mpeg"},
		{"track.M4A", "audio/mp4"},
		{"track", "audio/flac"},
		{"track.wav", "audio/flac"},
	}
	for _, c := range cases {
		if got := audioContentType(c.filename); got != c.want {
			t.Errorf("audioContentType(%q) = %q, want %q", c.filename, got, c.want)
		}
	}
}
