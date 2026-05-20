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

// downloadAttemptIDPrefix and downloadAttemptIDBytes mirror the convention
// used by library_files: "att-" + 32 hex chars.
const (
	downloadAttemptIDPrefix = "att-"
	downloadAttemptIDBytes  = 16
)

// AttemptStatus values used in download_attempts.status.
const (
	AttemptStatusPending     = "pending"
	AttemptStatusDownloading = "downloading"
	AttemptStatusDone        = "done"
	AttemptStatusFailed      = "failed"
	AttemptStatusSkipped     = "skipped"
	AttemptStatusCancelled   = "cancelled"
)

// DownloadAttempt is one record of a queue processing event for a Spotify
// track. Created on enqueue, updated as the worker progresses, and frozen
// on terminal status. Never deleted in normal operation.
type DownloadAttempt struct {
	ID            string
	SpotifyID     string
	LibraryFileID string // empty until status='done'

	UserID      string
	WatchlistID string
	BatchID     string

	Provider string
	Quality  string

	Status       string
	Error        string
	AttemptCount int

	StartedAt   int64
	CompletedAt int64 // 0 until terminal
}

// IsTerminal reports whether the attempt has reached an end state.
// pending and downloading are non-terminal; the rest are terminal.
func (a *DownloadAttempt) IsTerminal() bool {
	switch a.Status {
	case AttemptStatusDone, AttemptStatusFailed,
		AttemptStatusSkipped, AttemptStatusCancelled:
		return true
	}
	return false
}

// CreateDownloadAttempt inserts a new attempt row. Required: SpotifyID,
// Status. ID is generated if empty. StartedAt defaults to now. Caller is
// responsible for ensuring the referenced track exists.
func CreateDownloadAttempt(ctx context.Context, q Querier, a *DownloadAttempt) error {
	if a == nil {
		return errors.New("download_attempt: nil")
	}
	if a.SpotifyID == "" {
		return errors.New("download_attempt: spotify_id required")
	}
	if a.Status == "" {
		return errors.New("download_attempt: status required")
	}
	if a.ID == "" {
		id, err := newDownloadAttemptID()
		if err != nil {
			return fmt.Errorf("generate id: %w", err)
		}
		a.ID = id
	}
	if a.AttemptCount == 0 {
		a.AttemptCount = 1
	}
	now := time.Now().Unix()
	if a.StartedAt == 0 {
		a.StartedAt = now
	}
	if a.IsTerminal() && a.CompletedAt == 0 {
		a.CompletedAt = now
	}

	_, err := q.ExecContext(ctx, `
		INSERT INTO download_attempts (
			id, spotify_id, library_file_id,
			user_id, watchlist_id, batch_id,
			provider, quality,
			status, error, attempt_count,
			started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		a.ID, a.SpotifyID, nullableString(a.LibraryFileID),
		a.UserID, a.WatchlistID, a.BatchID,
		a.Provider, a.Quality,
		a.Status, a.Error, a.AttemptCount,
		a.StartedAt, a.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("create download_attempt for %s: %w", a.SpotifyID, err)
	}
	return nil
}

// MarkDownloadAttemptDone transitions an attempt to status='done', sets
// the linked library_file_id, and stamps completed_at. provider/quality
// fields can be filled with the values that actually succeeded (may differ
// from what was requested when fallback chains run).
func MarkDownloadAttemptDone(
	ctx context.Context, q Querier,
	id, libraryFileID, provider, quality string,
) error {
	if id == "" {
		return errors.New("download_attempt: id required")
	}
	if libraryFileID == "" {
		return errors.New("download_attempt: library_file_id required for done status")
	}
	res, err := q.ExecContext(ctx, `
		UPDATE download_attempts
		SET status = ?, library_file_id = ?, provider = ?, quality = ?,
		    error = '', completed_at = ?
		WHERE id = ?
	`, AttemptStatusDone, libraryFileID, provider, quality, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("mark done %s: %w", id, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("download_attempt not found: %s", id)
	}
	return nil
}

// MarkDownloadAttemptFailed transitions to status='failed' and stamps the
// error message. The row keeps its prior provider/quality so the caller
// can see what was last tried.
func MarkDownloadAttemptFailed(ctx context.Context, q Querier, id, errMsg string) error {
	if id == "" {
		return errors.New("download_attempt: id required")
	}
	res, err := q.ExecContext(ctx, `
		UPDATE download_attempts
		SET status = ?, error = ?, completed_at = ?
		WHERE id = ?
	`, AttemptStatusFailed, errMsg, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("mark failed %s: %w", id, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("download_attempt not found: %s", id)
	}
	return nil
}

// MarkDownloadAttemptSkipped transitions to 'skipped'. Used when a dedup
// check determined we already have the track at equal-or-better quality,
// so no provider was actually contacted.
func MarkDownloadAttemptSkipped(ctx context.Context, q Querier, id, reason string) error {
	if id == "" {
		return errors.New("download_attempt: id required")
	}
	res, err := q.ExecContext(ctx, `
		UPDATE download_attempts
		SET status = ?, error = ?, completed_at = ?
		WHERE id = ?
	`, AttemptStatusSkipped, reason, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("mark skipped %s: %w", id, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("download_attempt not found: %s", id)
	}
	return nil
}

// SetDownloadAttemptDownloading flips status to 'downloading'. Cheap UPDATE
// used by the worker as it picks up a pending attempt.
func SetDownloadAttemptDownloading(ctx context.Context, q Querier, id string) error {
	if id == "" {
		return errors.New("download_attempt: id required")
	}
	_, err := q.ExecContext(ctx, `
		UPDATE download_attempts SET status = ? WHERE id = ?
	`, AttemptStatusDownloading, id)
	if err != nil {
		return fmt.Errorf("set downloading %s: %w", id, err)
	}
	return nil
}

// GetDownloadAttempt returns the row by id or (nil, nil) if missing.
func GetDownloadAttempt(ctx context.Context, q Querier, id string) (*DownloadAttempt, error) {
	if id == "" {
		return nil, errors.New("download_attempt: id required")
	}
	row := q.QueryRowContext(ctx, attemptSelectColumns+" WHERE id = ?", id)
	return scanDownloadAttempt(row)
}

// ListDownloadAttemptsBySpotifyID returns all attempts for a track ordered
// by started_at descending (most recent first). Useful to render a track's
// download history (e.g. "tried Tidal, failed; tried Qobuz, succeeded").
func ListDownloadAttemptsBySpotifyID(ctx context.Context, q Querier, spotifyID string) ([]*DownloadAttempt, error) {
	if spotifyID == "" {
		return nil, errors.New("download_attempt: spotify_id required")
	}
	rows, err := q.QueryContext(ctx,
		attemptSelectColumns+" WHERE spotify_id = ? ORDER BY started_at DESC",
		spotifyID,
	)
	if err != nil {
		return nil, fmt.Errorf("list attempts for %s: %w", spotifyID, err)
	}
	defer rows.Close()

	out := make([]*DownloadAttempt, 0)
	for rows.Next() {
		a, err := scanDownloadAttemptRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// attemptSelectColumns is the canonical column list used by every query
// that hydrates a DownloadAttempt. Centralising it keeps GET/LIST in sync.
const attemptSelectColumns = `
	SELECT id, spotify_id, COALESCE(library_file_id, ''),
	       user_id, watchlist_id, batch_id,
	       provider, quality,
	       status, error, attempt_count,
	       started_at, completed_at
	FROM download_attempts
`

// scanDownloadAttempt scans a *sql.Row produced by attemptSelectColumns.
// Returns (nil, nil) on sql.ErrNoRows so callers can use the
// "nil pointer = not found" convention.
func scanDownloadAttempt(row *sql.Row) (*DownloadAttempt, error) {
	var a DownloadAttempt
	err := row.Scan(
		&a.ID, &a.SpotifyID, &a.LibraryFileID,
		&a.UserID, &a.WatchlistID, &a.BatchID,
		&a.Provider, &a.Quality,
		&a.Status, &a.Error, &a.AttemptCount,
		&a.StartedAt, &a.CompletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan download_attempt: %w", err)
	}
	return &a, nil
}

// scanDownloadAttemptRow is the *sql.Rows variant used by list queries.
// Splitting the two avoids a Row vs Rows interface that would buy us
// nothing: only two callers, each clearly typed.
func scanDownloadAttemptRow(row *sql.Rows) (*DownloadAttempt, error) {
	var a DownloadAttempt
	err := row.Scan(
		&a.ID, &a.SpotifyID, &a.LibraryFileID,
		&a.UserID, &a.WatchlistID, &a.BatchID,
		&a.Provider, &a.Quality,
		&a.Status, &a.Error, &a.AttemptCount,
		&a.StartedAt, &a.CompletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan download_attempt: %w", err)
	}
	return &a, nil
}

// newDownloadAttemptID produces an "att-<hex>" identifier. Same shape as
// library_files IDs for log-readability.
func newDownloadAttemptID() (string, error) {
	buf := make([]byte, downloadAttemptIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return downloadAttemptIDPrefix + hex.EncodeToString(buf), nil
}
