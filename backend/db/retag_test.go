package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

func completeTrack(spotifyID string) *Track {
	return &Track{
		SpotifyID:   spotifyID,
		ISRC:        "USRC17607839",
		Name:        "Some Track",
		ArtistName:  "Some Artist",
		TrackNumber: 3,
		DiscNumber:  1,
		DurationMs:  210000,
		Genre:       "Synthwave",
		ReleaseDate: "2024-03-15",
		AlbumName:   "Some Album",
		AlbumArtist: "Some Album Artist",
		CoverURL:    "https://example.com/cover.jpg",
		Copyright:   "2024 Some Label",
	}
}

func presentLibraryFile(t *testing.T, ctx context.Context, database *sql.DB, spotifyID, path string) {
	t.Helper()
	lf := &LibraryFile{
		SpotifyID: spotifyID,
		Provider:  "tidal",
		Quality:   QualityLossless,
		Format:    "flac",
		FilePath:  path,
		FileSize:  1000,
	}
	if err := CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}
}

// TestGetTracksNeedingRetagFindsIncompleteTrack is the core case: a track
// missing a field the retag pass can fill (isrc here), with a present file,
// must be returned along with its file path.
func TestGetTracksNeedingRetagFindsIncompleteTrack(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	track := completeTrack("spotify:track:incomplete")
	track.ISRC = ""
	if err := UpsertTrack(ctx, database, track); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	presentLibraryFile(t, ctx, database, track.SpotifyID, "/music/Artist/Track.flac")

	got, err := GetTracksNeedingRetag(ctx, database)
	if err != nil {
		t.Fatalf("GetTracksNeedingRetag: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].SpotifyID != track.SpotifyID {
		t.Errorf("SpotifyID = %q, want %q", got[0].SpotifyID, track.SpotifyID)
	}
	if got[0].FilePath != "/music/Artist/Track.flac" {
		t.Errorf("FilePath = %q, want %q", got[0].FilePath, "/music/Artist/Track.flac")
	}
}

// TestGetTracksNeedingRetagSkipsCompleteTrack confirms a track with every
// checked field already filled is not returned — otherwise every retag run
// would refetch the entire library forever.
func TestGetTracksNeedingRetagSkipsCompleteTrack(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	track := completeTrack("spotify:track:complete")
	if err := UpsertTrack(ctx, database, track); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	presentLibraryFile(t, ctx, database, track.SpotifyID, "/music/Artist/Complete.flac")

	got, err := GetTracksNeedingRetag(ctx, database)
	if err != nil {
		t.Fatalf("GetTracksNeedingRetag: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d track(s), want 0 (track is already complete): %+v", len(got), got)
	}
}

// TestGetTracksNeedingRetagIgnoresFilesNotPresent confirms a track without
// a currently-on-disk file is excluded — there would be nowhere to write
// the recovered tags.
func TestGetTracksNeedingRetagIgnoresFilesNotPresent(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	track := completeTrack("spotify:track:missingfile")
	track.Genre = ""
	if err := UpsertTrack(ctx, database, track); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	lf := &LibraryFile{
		SpotifyID: track.SpotifyID,
		Provider:  "tidal",
		Quality:   QualityLossless,
		Format:    "flac",
		FilePath:  "/music/Artist/Gone.flac",
		FileSize:  1000,
	}
	if err := CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}
	if err := UpdateLibraryFileStatus(ctx, database, lf.ID, StatusMissing); err != nil {
		t.Fatalf("UpdateLibraryFileStatus: %v", err)
	}

	got, err := GetTracksNeedingRetag(ctx, database)
	if err != nil {
		t.Fatalf("GetTracksNeedingRetag: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d track(s), want 0 (file is not status=present): %+v", len(got), got)
	}
}

// TestGetTracksNeedingRetagStillSelectsUnknownGenreSentinel confirms a track
// tagged util.UnknownGenre (every genre source was asked and none knew this
// recording — see backend/providerutil.genreForWriting) is still selected for
// retry, exactly like a blank genre. This value is not a permanent
// give-up flag: a source's catalog can gain the data later, so the pass must
// keep re-attempting it — same reasoning as never blacklisting a download
// that failed. A regression here would silently freeze every "unknown
// genre" track out of all future retries.
func TestGetTracksNeedingRetagStillSelectsUnknownGenreSentinel(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	track := completeTrack("spotify:track:genreunknown")
	track.Genre = util.UnknownGenre
	if err := UpsertTrack(ctx, database, track); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	presentLibraryFile(t, ctx, database, track.SpotifyID, "/music/Artist/Unknown.flac")

	got, err := GetTracksNeedingRetag(ctx, database)
	if err != nil {
		t.Fatalf("GetTracksNeedingRetag: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 — a track marked %q must still be retried, not treated as complete", len(got), util.UnknownGenre)
	}
}

// TestGetTracksNeedingRetagIgnoresAlbumID confirms album_id being empty
// (which it always is — see migration 0005's comment, it's deliberately
// never populated) does not by itself make an otherwise-complete track a
// retag candidate. Without this, every track in the catalog would be
// selected on every run, forever.
func TestGetTracksNeedingRetagIgnoresAlbumID(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	track := completeTrack("spotify:track:noalbumid")
	// AlbumID intentionally left empty, everything else filled.
	if err := UpsertTrack(ctx, database, track); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	presentLibraryFile(t, ctx, database, track.SpotifyID, "/music/Artist/NoAlbumID.flac")

	got, err := GetTracksNeedingRetag(ctx, database)
	if err != nil {
		t.Fatalf("GetTracksNeedingRetag: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d track(s), want 0 (empty album_id must not trigger retag): %+v", len(got), got)
	}
}
