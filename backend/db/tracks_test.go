package db

import (
	"context"
	"testing"
)

// TestUpsertTrackPopulatesNewMetadataFields covers the fields added in
// migration 0005 (release_date/album_name/album_artist/cover_url/copyright)
// plus isrc/genre, confirming a full round trip through UpsertTrack/GetTrack.
func TestUpsertTrackPopulatesNewMetadataFields(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	track := &Track{
		SpotifyID:   "spotify:track:abc",
		ISRC:        "USRC17607839",
		Name:        "Some Track",
		ArtistName:  "Some Artist",
		Genre:       "Synthwave",
		ReleaseDate: "2024-03-15",
		AlbumName:   "Some Album",
		AlbumArtist: "Some Album Artist",
		CoverURL:    "https://example.com/cover.jpg",
		Copyright:   "2024 Some Label",
	}
	if err := UpsertTrack(ctx, database, track); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}

	got, err := GetTrack(ctx, database, track.SpotifyID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if got == nil {
		t.Fatal("GetTrack returned nil")
	}
	if got.ISRC != track.ISRC {
		t.Errorf("ISRC = %q, want %q", got.ISRC, track.ISRC)
	}
	if got.Genre != track.Genre {
		t.Errorf("Genre = %q, want %q", got.Genre, track.Genre)
	}
	if got.ReleaseDate != track.ReleaseDate {
		t.Errorf("ReleaseDate = %q, want %q", got.ReleaseDate, track.ReleaseDate)
	}
	if got.AlbumName != track.AlbumName {
		t.Errorf("AlbumName = %q, want %q", got.AlbumName, track.AlbumName)
	}
	if got.AlbumArtist != track.AlbumArtist {
		t.Errorf("AlbumArtist = %q, want %q", got.AlbumArtist, track.AlbumArtist)
	}
	if got.CoverURL != track.CoverURL {
		t.Errorf("CoverURL = %q, want %q", got.CoverURL, track.CoverURL)
	}
	if got.Copyright != track.Copyright {
		t.Errorf("Copyright = %q, want %q", got.Copyright, track.Copyright)
	}
}

// TestUpsertTrackDoesNotClobberISRCOrGenreWithEmpty is the regression test
// for a real risk introduced by reading isrc/genre back from the
// downloaded file: those two fields are only ever known when a file was
// actually readable (a successful or skipped job). A later UpsertTrack
// call for the SAME track from a failed job has no file to read and would
// otherwise silently erase a previously-recorded isrc/genre on every
// subsequent failed retry.
func TestUpsertTrackDoesNotClobberISRCOrGenreWithEmpty(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	first := &Track{
		SpotifyID: "spotify:track:xyz",
		ISRC:      "USRC17607839",
		Name:      "Some Track",
		Genre:     "Synthwave",
	}
	if err := UpsertTrack(ctx, database, first); err != nil {
		t.Fatalf("UpsertTrack (initial): %v", err)
	}

	// Simulate a later failed job for the same track: no file was ever
	// read, so isrc/genre come back empty on this call.
	second := &Track{
		SpotifyID: "spotify:track:xyz",
		Name:      "Some Track",
	}
	if err := UpsertTrack(ctx, database, second); err != nil {
		t.Fatalf("UpsertTrack (empty isrc/genre): %v", err)
	}

	got, err := GetTrack(ctx, database, "spotify:track:xyz")
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if got.ISRC != "USRC17607839" {
		t.Errorf("ISRC = %q, want the previously-recorded value to survive an empty update", got.ISRC)
	}
	if got.Genre != "Synthwave" {
		t.Errorf("Genre = %q, want the previously-recorded value to survive an empty update", got.Genre)
	}

	// A later call WITH a real (different) value must still be able to
	// update it — this isn't a permanent lock-in, just "don't overwrite
	// with nothing."
	third := &Track{
		SpotifyID: "spotify:track:xyz",
		Name:      "Some Track",
		ISRC:      "GBUM71029604",
		Genre:     "Vaporwave",
	}
	if err := UpsertTrack(ctx, database, third); err != nil {
		t.Fatalf("UpsertTrack (real update): %v", err)
	}
	got, err = GetTrack(ctx, database, "spotify:track:xyz")
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if got.ISRC != "GBUM71029604" {
		t.Errorf("ISRC = %q, want the new value to apply", got.ISRC)
	}
	if got.Genre != "Vaporwave" {
		t.Errorf("Genre = %q, want the new value to apply", got.Genre)
	}
}
