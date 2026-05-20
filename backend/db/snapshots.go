package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	snapshotIDPrefix = "snap-"
	snapshotIDBytes  = 16
)

// PlaylistSnapshot freezes a watchlist's contents at a moment in time.
// Snapshots are created on syncs where the playlist actually changed
// (i.e. added_count + removed_count > 0) plus the very first sync.
//
// Renames are captured by storing the playlist_name as it was at
// taken_at; subsequent snapshots get the new name without rewriting old ones.
type PlaylistSnapshot struct {
	ID                string
	WatchlistID       string
	SpotifySnapshotID string
	PlaylistName      string
	TrackCount        int
	TakenAt           int64
	AddedCount        int
	RemovedCount      int
}

// CreatePlaylistSnapshot stores a snapshot together with its frozen
// ordered track list. Performs the parent INSERT and the junction INSERTs
// in a single transaction. Caller must have ensured every spotify_id in
// orderedSpotifyIDs exists in tracks (UpsertTrackStub at minimum).
//
// If snap.ID is empty, a fresh "snap-<hex>" identifier is generated.
// TakenAt defaults to now. TrackCount is normalised to len(orderedSpotifyIDs).
func CreatePlaylistSnapshot(
	ctx context.Context, db *sql.DB,
	snap *PlaylistSnapshot, orderedSpotifyIDs []string,
) error {
	if snap == nil {
		return errors.New("snapshot: nil")
	}
	if snap.WatchlistID == "" {
		return errors.New("snapshot: watchlist_id required")
	}
	if snap.ID == "" {
		id, err := newSnapshotID()
		if err != nil {
			return fmt.Errorf("generate snapshot id: %w", err)
		}
		snap.ID = id
	}
	if snap.TakenAt == 0 {
		snap.TakenAt = time.Now().Unix()
	}
	snap.TrackCount = len(orderedSpotifyIDs)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO playlist_snapshots (
			id, watchlist_id, spotify_snapshot_id, playlist_name,
			track_count, taken_at, added_count, removed_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		snap.ID, snap.WatchlistID, snap.SpotifySnapshotID, snap.PlaylistName,
		snap.TrackCount, snap.TakenAt, snap.AddedCount, snap.RemovedCount,
	); err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}

	for i, id := range orderedSpotifyIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO playlist_snapshot_tracks (snapshot_id, spotify_id, position)
			VALUES (?, ?, ?)
		`, snap.ID, id, i); err != nil {
			return fmt.Errorf("insert snapshot track %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	return nil
}

// LatestSnapshotID returns the most recent snapshot ID for a watchlist,
// or empty string if none exists yet. Cheap lookup used to decide whether
// the next sync should be considered "first sync" (always snapshotted).
func LatestSnapshotID(ctx context.Context, q Querier, watchlistID string) (string, error) {
	if watchlistID == "" {
		return "", errors.New("snapshot: watchlist_id required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT id FROM playlist_snapshots
		WHERE watchlist_id = ?
		ORDER BY taken_at DESC
		LIMIT 1
	`, watchlistID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest snapshot for %s: %w", watchlistID, err)
	}
	return id, nil
}

// ListPlaylistSnapshots returns the snapshots of a watchlist, newest first.
// Pagination is left to the caller (LIMIT/OFFSET) — typical UI shows the
// last 20 or 50.
func ListPlaylistSnapshots(
	ctx context.Context, q Querier,
	watchlistID string, limit int,
) ([]*PlaylistSnapshot, error) {
	if watchlistID == "" {
		return nil, errors.New("snapshot: watchlist_id required")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.QueryContext(ctx, `
		SELECT id, watchlist_id, spotify_snapshot_id, playlist_name,
		       track_count, taken_at, added_count, removed_count
		FROM playlist_snapshots
		WHERE watchlist_id = ?
		ORDER BY taken_at DESC
		LIMIT ?
	`, watchlistID, limit)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()

	out := make([]*PlaylistSnapshot, 0)
	for rows.Next() {
		var s PlaylistSnapshot
		if err := rows.Scan(
			&s.ID, &s.WatchlistID, &s.SpotifySnapshotID, &s.PlaylistName,
			&s.TrackCount, &s.TakenAt, &s.AddedCount, &s.RemovedCount,
		); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// GetSnapshotTrackIDs returns the ordered Spotify IDs of a snapshot,
// suitable for rendering the frozen playlist contents.
func GetSnapshotTrackIDs(ctx context.Context, q Querier, snapshotID string) ([]string, error) {
	if snapshotID == "" {
		return nil, errors.New("snapshot: id required")
	}
	rows, err := q.QueryContext(ctx, `
		SELECT spotify_id FROM playlist_snapshot_tracks
		WHERE snapshot_id = ?
		ORDER BY position
	`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("get snapshot tracks: %w", err)
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

// newSnapshotID produces a "snap-<hex>" identifier.
func newSnapshotID() (string, error) {
	buf := make([]byte, snapshotIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return snapshotIDPrefix + hex.EncodeToString(buf), nil
}
