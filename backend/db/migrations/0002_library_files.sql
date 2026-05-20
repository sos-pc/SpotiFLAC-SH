-- Migration 0002: library_files — physical files on disk.
--
-- A library_files row is the bridge between a Spotify track identity and
-- a real file on the filesystem. It carries the source provider, the
-- quality tier (with a numeric quality_rank to allow "keep best" upgrades),
-- the format, the path, and a status field tracking the file's health.
--
-- The "keep best quality" rule is enforced by application code via
-- quality_rank comparison + UPDATE-in-place; the database itself permits
-- any number of historical "deleted" rows for the same spotify_id (full
-- audit trail), but only one active row at a time per track. The partial
-- unique index below enforces that invariant.

CREATE TABLE library_files (
    id              TEXT PRIMARY KEY,             -- "lib-" + 32 hex chars
    spotify_id      TEXT NOT NULL,                -- FK -> tracks(spotify_id)

    provider        TEXT NOT NULL,                -- "tidal" | "qobuz" | "amazon" | "deezer"
    quality         TEXT NOT NULL,                -- "LOSSLESS" | "HI_RES_LOSSLESS" | "HI_RES" | "HIGH"
    quality_rank    INTEGER NOT NULL,             -- numeric for comparison (higher = better)
    format          TEXT NOT NULL,                -- "flac" | "m4a" | "mp3"

    file_path       TEXT NOT NULL,
    file_size       INTEGER NOT NULL DEFAULT 0,   -- bytes

    downloaded_at   INTEGER NOT NULL,             -- Unix seconds
    downloaded_by   TEXT NOT NULL DEFAULT '',     -- user_id who triggered the DL

    status          TEXT NOT NULL DEFAULT 'present',
    -- status values: 'present' | 'missing' | 'moved' | 'corrupt' | 'deleted'
    last_verified_at INTEGER NOT NULL,

    FOREIGN KEY (spotify_id) REFERENCES tracks(spotify_id) ON DELETE RESTRICT
);

-- At most one non-deleted file per Spotify track. Replacing a file means
-- marking the old row 'deleted' and inserting a new one — preserves audit
-- trail.
CREATE UNIQUE INDEX idx_library_files_active_per_track
    ON library_files(spotify_id)
    WHERE status != 'deleted';

CREATE INDEX idx_library_files_path     ON library_files(file_path);
CREATE INDEX idx_library_files_status   ON library_files(status);
CREATE INDEX idx_library_files_provider ON library_files(provider);
