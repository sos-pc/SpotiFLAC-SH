package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Track represents the Spotify identity of a track. It carries everything
// we need for filename generation, tag embedding and matching to lossless
// providers, but it does NOT carry a file path: physical files live in the
// LibraryFile entity (added in a later migration).
type Track struct {
	SpotifyID   string
	ISRC        string
	Name        string
	ArtistName  string // joined display string ("A, B feat. C")
	AlbumID     string // empty if unknown (column stored as NULL)
	TrackNumber int
	DiscNumber  int
	DurationMs  int
	Explicit    bool
	Genre       string // from MusicBrainz lookup; empty until fetched
	ReleaseDate string // "YYYY-MM-DD" or "YYYY", from Spotify — album-level, denormalized (see migration 0005)
	AlbumName   string // denormalized; not linked to the albums table (see migration 0005)
	AlbumArtist string // denormalized
	CoverURL    string // denormalized
	Copyright   string // denormalized
	FirstSeenAt int64  // preserved on update
	LastSeenAt  int64  // bumped on every upsert
}

// UpsertTrack inserts or refreshes a track. If AlbumID is non-empty, an
// album stub is created (no-op if the album exists) before the track row
// is written, so the FK is always satisfied.
//
// FirstSeenAt is preserved across updates; LastSeenAt is set to time.Now().
func UpsertTrack(ctx context.Context, q Querier, t *Track) error {
	if t == nil || t.SpotifyID == "" {
		return errors.New("track: spotify_id required")
	}
	if t.AlbumID != "" {
		if err := UpsertAlbumStub(ctx, q, t.AlbumID); err != nil {
			return fmt.Errorf("ensure album for track %s: %w", t.SpotifyID, err)
		}
	}
	now := time.Now().Unix()
	_, err := q.ExecContext(ctx, `
		INSERT INTO tracks (
			spotify_id, isrc, name, artist_name, album_id,
			track_number, disc_number, duration_ms, explicit, genre,
			release_date, album_name, album_artist, cover_url, copyright,
			first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (spotify_id) DO UPDATE SET
			-- isrc/genre are only ever known when the caller could read them
			-- back from a downloaded file (see meta.ReadTrackTags); a call
			-- from a failed job has no file to read and would otherwise
			-- clobber a previously-good value with empty on every retry.
			isrc         = COALESCE(NULLIF(excluded.isrc, ''), tracks.isrc),
			name         = excluded.name,
			artist_name  = excluded.artist_name,
			album_id     = excluded.album_id,
			track_number = excluded.track_number,
			disc_number  = excluded.disc_number,
			duration_ms  = excluded.duration_ms,
			explicit     = excluded.explicit,
			genre        = COALESCE(NULLIF(excluded.genre, ''), tracks.genre),
			release_date = excluded.release_date,
			album_name   = excluded.album_name,
			album_artist = excluded.album_artist,
			cover_url    = excluded.cover_url,
			copyright    = excluded.copyright,
			last_seen_at = excluded.last_seen_at
	`,
		t.SpotifyID, t.ISRC, t.Name, t.ArtistName, nullableString(t.AlbumID),
		t.TrackNumber, t.DiscNumber, t.DurationMs, boolToInt(t.Explicit), t.Genre,
		t.ReleaseDate, t.AlbumName, t.AlbumArtist, t.CoverURL, t.Copyright,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert track %s: %w", t.SpotifyID, err)
	}
	if t.FirstSeenAt == 0 {
		t.FirstSeenAt = now
	}
	t.LastSeenAt = now
	return nil
}

// GetTrack returns the track with the given Spotify ID, or (nil, nil) if it
// does not exist. Matches the convention used by GetAlbum.
func GetTrack(ctx context.Context, q Querier, spotifyID string) (*Track, error) {
	row := q.QueryRowContext(ctx, `
		SELECT spotify_id, isrc, name, artist_name, COALESCE(album_id, ''),
		       track_number, disc_number, duration_ms, explicit, genre,
		       release_date, album_name, album_artist, cover_url, copyright,
		       first_seen_at, last_seen_at
		FROM tracks
		WHERE spotify_id = ?
	`, spotifyID)

	var (
		t        Track
		explicit int
	)
	err := row.Scan(
		&t.SpotifyID, &t.ISRC, &t.Name, &t.ArtistName, &t.AlbumID,
		&t.TrackNumber, &t.DiscNumber, &t.DurationMs, &explicit, &t.Genre,
		&t.ReleaseDate, &t.AlbumName, &t.AlbumArtist, &t.CoverURL, &t.Copyright,
		&t.FirstSeenAt, &t.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get track %s: %w", spotifyID, err)
	}
	t.Explicit = explicit != 0
	return &t, nil
}

// GetTrackByISRC returns the first track found with the given ISRC, or
// (nil, nil) if none. Useful when the catch-up path resolves a downloaded
// file via its tag and we want to look up our internal ID.
//
// In rare cases multiple tracks share an ISRC (regional editions, remasters);
// the result then depends on insert order. Callers needing exhaustive
// matches should query the table directly.
func GetTrackByISRC(ctx context.Context, q Querier, isrc string) (*Track, error) {
	if isrc == "" {
		return nil, nil
	}
	row := q.QueryRowContext(ctx, `
		SELECT spotify_id, isrc, name, artist_name, COALESCE(album_id, ''),
		       track_number, disc_number, duration_ms, explicit, genre,
		       release_date, album_name, album_artist, cover_url, copyright,
		       first_seen_at, last_seen_at
		FROM tracks
		WHERE isrc = ?
		ORDER BY first_seen_at
		LIMIT 1
	`, isrc)

	var (
		t        Track
		explicit int
	)
	err := row.Scan(
		&t.SpotifyID, &t.ISRC, &t.Name, &t.ArtistName, &t.AlbumID,
		&t.TrackNumber, &t.DiscNumber, &t.DurationMs, &explicit, &t.Genre,
		&t.ReleaseDate, &t.AlbumName, &t.AlbumArtist, &t.CoverURL, &t.Copyright,
		&t.FirstSeenAt, &t.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get track by isrc %s: %w", isrc, err)
	}
	t.Explicit = explicit != 0
	return &t, nil
}

// UpsertTrackStub creates a placeholder track row with only spotify_id set
// if no row exists yet. Used by the watcher when it receives a list of
// track IDs from a Spotify playlist sync but has not yet fetched the full
// metadata for each. Subsequent UpsertTrack calls fill in real data.
//
// No-op if the track already exists. Like UpsertAlbumStub, this keeps FK
// invariants satisfiable across the catalog without forcing a strict
// "fetch full metadata before any junction insert" workflow on callers.
func UpsertTrackStub(ctx context.Context, q Querier, spotifyID string) error {
	if spotifyID == "" {
		return errors.New("track stub: spotify_id required")
	}
	now := time.Now().Unix()
	_, err := q.ExecContext(ctx, `
		INSERT INTO tracks (spotify_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT (spotify_id) DO NOTHING
	`, spotifyID, now, now)
	if err != nil {
		return fmt.Errorf("upsert track stub %s: %w", spotifyID, err)
	}
	return nil
}
