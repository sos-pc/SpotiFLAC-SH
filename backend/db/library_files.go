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

// libraryFileIDPrefix is the human-readable prefix on every library_files.id.
// Same convention as APIKey ("key-...") so IDs are searchable across logs.
const libraryFileIDPrefix = "lib-"

// libraryFileIDBytes is the entropy budget for the random suffix. 16 bytes
// encoded as hex = 32 chars + "lib-" prefix = 36-char identifier.
const libraryFileIDBytes = 16

// LibraryFile is a physical audio file on disk linked to a Spotify track.
//
// Lifecycle:
//   - Created when a download succeeds with status="present".
//   - Marked "missing" by the rescan task when stat() fails on file_path.
//   - Updated to a new file_path with status="present" when the SPOTIFY_ID
//     tag scan finds the same content elsewhere on disk.
//   - Replaced by a new row when a higher-quality download lands: the old
//     row is set to status="deleted" (audit trail), a new row inserted.
//
// At most one non-deleted row per spotify_id at any time.
type LibraryFile struct {
	ID          string
	SpotifyID   string

	Provider    string
	Quality     string
	QualityRank int
	Format      string

	FilePath string
	FileSize int64

	DownloadedAt int64
	DownloadedBy string

	Status         string
	LastVerifiedAt int64
}

// CreateLibraryFile inserts a new library_files row. Caller must:
//   - Have ensured the corresponding track exists (UpsertTrack).
//   - Have either no current active row for this spotify_id, or have just
//     marked the existing one as deleted (otherwise the partial unique
//     index will reject the insert — propagated as an error to the caller).
//
// If lf.ID is empty, a fresh random ID is generated. Status defaults to
// "present" and QualityRank is computed from Quality if zero.
func CreateLibraryFile(ctx context.Context, q Querier, lf *LibraryFile) error {
	if lf == nil {
		return errors.New("library_file: nil")
	}
	if lf.SpotifyID == "" {
		return errors.New("library_file: spotify_id required")
	}
	if lf.FilePath == "" {
		return errors.New("library_file: file_path required")
	}
	if lf.Provider == "" {
		return errors.New("library_file: provider required")
	}
	if lf.Quality == "" {
		return errors.New("library_file: quality required")
	}
	if lf.Format == "" {
		return errors.New("library_file: format required")
	}

	if lf.ID == "" {
		id, err := newLibraryFileID()
		if err != nil {
			return fmt.Errorf("generate library_file id: %w", err)
		}
		lf.ID = id
	}
	if lf.Status == "" {
		lf.Status = StatusPresent
	}
	if lf.QualityRank == 0 {
		lf.QualityRank = QualityRank(lf.Quality)
	}
	now := time.Now().Unix()
	if lf.DownloadedAt == 0 {
		lf.DownloadedAt = now
	}
	if lf.LastVerifiedAt == 0 {
		lf.LastVerifiedAt = now
	}

	_, err := q.ExecContext(ctx, `
		INSERT INTO library_files (
			id, spotify_id, provider, quality, quality_rank, format,
			file_path, file_size, downloaded_at, downloaded_by,
			status, last_verified_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		lf.ID, lf.SpotifyID, lf.Provider, lf.Quality, lf.QualityRank, lf.Format,
		lf.FilePath, lf.FileSize, lf.DownloadedAt, lf.DownloadedBy,
		lf.Status, lf.LastVerifiedAt,
	)
	if err != nil {
		return fmt.Errorf("create library_file for %s: %w", lf.SpotifyID, err)
	}
	return nil
}

// GetActiveLibraryFile returns the current non-deleted file for a track,
// or (nil, nil) if there is none. The partial unique index guarantees at
// most one row matches.
func GetActiveLibraryFile(ctx context.Context, q Querier, spotifyID string) (*LibraryFile, error) {
	if spotifyID == "" {
		return nil, errors.New("library_file: spotify_id required")
	}
	row := q.QueryRowContext(ctx, `
		SELECT id, spotify_id, provider, quality, quality_rank, format,
		       file_path, file_size, downloaded_at, downloaded_by,
		       status, last_verified_at
		FROM library_files
		WHERE spotify_id = ? AND status != 'deleted'
		LIMIT 1
	`, spotifyID)

	var lf LibraryFile
	err := row.Scan(
		&lf.ID, &lf.SpotifyID, &lf.Provider, &lf.Quality, &lf.QualityRank, &lf.Format,
		&lf.FilePath, &lf.FileSize, &lf.DownloadedAt, &lf.DownloadedBy,
		&lf.Status, &lf.LastVerifiedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active library_file for %s: %w", spotifyID, err)
	}
	return &lf, nil
}

// MarkLibraryFileDeleted flips status to "deleted" without removing the row.
// Used when replacing with a higher-quality version (audit trail) or when
// sync_deletions has removed the underlying file.
func MarkLibraryFileDeleted(ctx context.Context, q Querier, id string) error {
	if id == "" {
		return errors.New("library_file: id required")
	}
	res, err := q.ExecContext(ctx, `
		UPDATE library_files
		SET status = ?, last_verified_at = ?
		WHERE id = ?
	`, StatusDeleted, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("mark deleted %s: %w", id, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("library_file not found: %s", id)
	}
	return nil
}

// UpdateLibraryFileStatus changes the status field (e.g. to "missing" or
// "corrupt") and bumps last_verified_at. Use MarkLibraryFileDeleted for
// the deleted state to keep that path explicit.
func UpdateLibraryFileStatus(ctx context.Context, q Querier, id, status string) error {
	if id == "" {
		return errors.New("library_file: id required")
	}
	if status == StatusDeleted {
		return errors.New("library_file: use MarkLibraryFileDeleted for deleted state")
	}
	_, err := q.ExecContext(ctx, `
		UPDATE library_files
		SET status = ?, last_verified_at = ?
		WHERE id = ?
	`, status, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("update status %s -> %s: %w", id, status, err)
	}
	return nil
}

// UpdateLibraryFileQuality refreshes provider/quality/quality_rank/file_size
// and bumps downloaded_at + last_verified_at on an existing row. Used when a
// fresh download lands at the *same* file_path as the current active row
// (e.g. a quality-upgrade download resolves to the same templated filename,
// since the naming template doesn't encode bitrate) — without this, the
// catalog would keep reporting the original quality forever, and dedup
// checks that compare against it would re-trigger the "upgrade" download on
// every subsequent sync. Also resets status to "present" since a fresh
// write means the file demonstrably exists now.
func UpdateLibraryFileQuality(ctx context.Context, q Querier, id, provider, quality string, fileSize int64) error {
	if id == "" {
		return errors.New("library_file: id required")
	}
	now := time.Now().Unix()
	_, err := q.ExecContext(ctx, `
		UPDATE library_files
		SET provider = ?, quality = ?, quality_rank = ?, file_size = ?,
		    downloaded_at = ?, last_verified_at = ?, status = ?
		WHERE id = ?
	`, provider, quality, QualityRank(quality), fileSize, now, now, StatusPresent, id)
	if err != nil {
		return fmt.Errorf("update quality for %s: %w", id, err)
	}
	return nil
}

// UpdateLibraryFilePath rewrites the file_path of an existing row and
// resets status to "present". Used by the rescan flow when a file was
// moved manually and rediscovered via its SPOTIFY_ID tag.
func UpdateLibraryFilePath(ctx context.Context, q Querier, id, newPath string) error {
	if id == "" {
		return errors.New("library_file: id required")
	}
	if newPath == "" {
		return errors.New("library_file: new_path required")
	}
	_, err := q.ExecContext(ctx, `
		UPDATE library_files
		SET file_path = ?, status = ?, last_verified_at = ?
		WHERE id = ?
	`, newPath, StatusPresent, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("update path for %s: %w", id, err)
	}
	return nil
}

// newLibraryFileID generates a fresh "lib-<hex>" identifier with
// libraryFileIDBytes bytes of randomness.
func newLibraryFileID() (string, error) {
	buf := make([]byte, libraryFileIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return libraryFileIDPrefix + hex.EncodeToString(buf), nil
}
