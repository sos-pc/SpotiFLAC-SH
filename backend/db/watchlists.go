package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SetWatchlistTracks atomically replaces the track list of a watchlist
// with the supplied ordered Spotify IDs. Returns the diff vs the previous
// state so the caller (watcher) can decide whether to take a snapshot,
// log changes, etc.
//
// Caller is responsible for ensuring all referenced Spotify IDs already
// exist in the tracks table — UpsertTrackStub on each ID is the cheapest
// way to satisfy the FK without forcing a full metadata fetch first.
func SetWatchlistTracks(
	ctx context.Context, db *sql.DB,
	watchlistID string, orderedSpotifyIDs []string,
) (added, removed []string, err error) {
	if watchlistID == "" {
		return nil, nil, errors.New("watchlist: id required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := loadWatchlistTrackPositions(ctx, tx, watchlistID)
	if err != nil {
		return nil, nil, err
	}

	newSet := make(map[string]int, len(orderedSpotifyIDs))
	for i, id := range orderedSpotifyIDs {
		newSet[id] = i
	}

	for id := range existing {
		if _, kept := newSet[id]; !kept {
			removed = append(removed, id)
		}
	}
	for id := range newSet {
		if _, was := existing[id]; !was {
			added = append(added, id)
		}
	}

	// Clear-then-insert is simpler than a 3-way diff (insert/update/delete)
	// and the volume per watchlist is small (rarely > 200 tracks).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM watchlist_tracks WHERE watchlist_id = ?`,
		watchlistID,
	); err != nil {
		return nil, nil, fmt.Errorf("clear watchlist_tracks: %w", err)
	}

	now := time.Now().Unix()
	for id, pos := range newSet {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO watchlist_tracks (watchlist_id, spotify_id, position, added_at)
			VALUES (?, ?, ?, ?)
		`, watchlistID, id, pos, now); err != nil {
			return nil, nil, fmt.Errorf("insert watchlist_track %s/%s: %w", watchlistID, id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit watchlist_tracks: %w", err)
	}
	return added, removed, nil
}

// IsTrackInOtherWatchlists reports whether the given Spotify ID appears in
// at least one watchlist other than excludeWatchlistID. Used by the
// sync_deletions logic to preserve files that are still referenced by a
// different playlist (multi-playlist protection).
func IsTrackInOtherWatchlists(
	ctx context.Context, q Querier,
	spotifyID, excludeWatchlistID string,
) (bool, error) {
	if spotifyID == "" {
		return false, errors.New("watchlist: spotify_id required")
	}
	var count int
	err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM watchlist_tracks
		WHERE spotify_id = ? AND watchlist_id != ?
	`, spotifyID, excludeWatchlistID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("is track in other watchlists: %w", err)
	}
	return count > 0, nil
}

// RemoveWatchlistTracks deletes every junction row for a watchlist. Used
// when the user removes the watchlist itself.
func RemoveWatchlistTracks(ctx context.Context, q Querier, watchlistID string) error {
	if watchlistID == "" {
		return errors.New("watchlist: id required")
	}
	_, err := q.ExecContext(ctx,
		`DELETE FROM watchlist_tracks WHERE watchlist_id = ?`,
		watchlistID,
	)
	if err != nil {
		return fmt.Errorf("remove watchlist_tracks for %s: %w", watchlistID, err)
	}
	return nil
}

// ListWatchlistTrackIDs returns the Spotify IDs in a watchlist, ordered by
// position. Cheap query used as input to M3U8 generation.
func ListWatchlistTrackIDs(ctx context.Context, q Querier, watchlistID string) ([]string, error) {
	if watchlistID == "" {
		return nil, errors.New("watchlist: id required")
	}
	rows, err := q.QueryContext(ctx, `
		SELECT spotify_id FROM watchlist_tracks
		WHERE watchlist_id = ?
		ORDER BY position
	`, watchlistID)
	if err != nil {
		return nil, fmt.Errorf("list watchlist track ids: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// loadWatchlistTrackPositions returns spotify_id -> position for a
// watchlist. Internal helper for diff computation.
func loadWatchlistTrackPositions(ctx context.Context, q Querier, watchlistID string) (map[string]int, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT spotify_id, position FROM watchlist_tracks WHERE watchlist_id = ?`,
		watchlistID,
	)
	if err != nil {
		return nil, fmt.Errorf("load watchlist_tracks: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var (
			id  string
			pos int
		)
		if err := rows.Scan(&id, &pos); err != nil {
			return nil, fmt.Errorf("scan watchlist_track: %w", err)
		}
		out[id] = pos
	}
	return out, rows.Err()
}
