package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Album represents the Spotify identity of an album. It is independent of
// any download — an Album row exists as soon as Spotify metadata has been
// fetched, regardless of whether any track of the album was downloaded.
type Album struct {
	SpotifyID    string
	Name         string
	AlbumArtist  string
	ReleaseDate  string // "YYYY-MM-DD" or "YYYY"
	TotalTracks  int
	TotalDiscs   int
	CoverURL     string
	Label        string
	Copyright    string
	FirstSeenAt  int64 // Unix seconds, set on first insert, never updated
	LastSeenAt   int64 // Unix seconds, bumped on every upsert
}

// UpsertAlbum inserts a new album or refreshes an existing one. FirstSeenAt
// is preserved on update (the row keeps its original value); LastSeenAt is
// always set to time.Now(). Existing field values are overwritten with the
// supplied ones — caller is responsible for not passing zero values they
// did not intend to overwrite.
func UpsertAlbum(ctx context.Context, q Querier, a *Album) error {
	if a == nil || a.SpotifyID == "" {
		return errors.New("album: spotify_id required")
	}
	now := time.Now().Unix()
	_, err := q.ExecContext(ctx, `
		INSERT INTO albums (
			spotify_id, name, album_artist, release_date,
			total_tracks, total_discs, cover_url, label, copyright,
			first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (spotify_id) DO UPDATE SET
			name         = excluded.name,
			album_artist = excluded.album_artist,
			release_date = excluded.release_date,
			total_tracks = excluded.total_tracks,
			total_discs  = excluded.total_discs,
			cover_url    = excluded.cover_url,
			label        = excluded.label,
			copyright    = excluded.copyright,
			last_seen_at = excluded.last_seen_at
	`,
		a.SpotifyID, a.Name, a.AlbumArtist, a.ReleaseDate,
		a.TotalTracks, a.TotalDiscs, a.CoverURL, a.Label, a.Copyright,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert album %s: %w", a.SpotifyID, err)
	}
	if a.FirstSeenAt == 0 {
		a.FirstSeenAt = now
	}
	a.LastSeenAt = now
	return nil
}

// UpsertAlbumStub creates a minimal album row with only spotify_id set, if
// no row exists yet. Used when a track references an album whose full
// metadata hasn't been fetched: keeps FK validity until UpsertAlbum is
// called with real data. No-op if the album already exists.
func UpsertAlbumStub(ctx context.Context, q Querier, spotifyID string) error {
	if spotifyID == "" {
		return errors.New("album stub: spotify_id required")
	}
	now := time.Now().Unix()
	_, err := q.ExecContext(ctx, `
		INSERT INTO albums (spotify_id, first_seen_at, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT (spotify_id) DO NOTHING
	`, spotifyID, now, now)
	if err != nil {
		return fmt.Errorf("upsert album stub %s: %w", spotifyID, err)
	}
	return nil
}

// GetAlbum returns the album with the given Spotify ID, or (nil, nil) if it
// does not exist. The "not found is not an error" convention keeps callers
// simple: `if album == nil { … }`.
func GetAlbum(ctx context.Context, q Querier, spotifyID string) (*Album, error) {
	row := q.QueryRowContext(ctx, `
		SELECT spotify_id, name, album_artist, release_date,
		       total_tracks, total_discs, cover_url, label, copyright,
		       first_seen_at, last_seen_at
		FROM albums
		WHERE spotify_id = ?
	`, spotifyID)

	var a Album
	err := row.Scan(
		&a.SpotifyID, &a.Name, &a.AlbumArtist, &a.ReleaseDate,
		&a.TotalTracks, &a.TotalDiscs, &a.CoverURL, &a.Label, &a.Copyright,
		&a.FirstSeenAt, &a.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get album %s: %w", spotifyID, err)
	}
	return &a, nil
}
