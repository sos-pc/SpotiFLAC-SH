-- Migration 0003: download_attempts — permanent log of every download.
--
-- Every time the queue processes a track, a download_attempts row is
-- created. It progresses through pending -> downloading -> {done | failed
-- | skipped | cancelled} in place. Once terminal, the row is never again
-- mutated; aggregate operations (clear-history) flip a status to a
-- "hidden" state in the future, never DELETE.
--
-- This table replaces the historical role of BoltDB jobs: jobs.db keeps
-- the live queue state (workers polling pending items), download_attempts
-- keeps the audit trail forever. The 24h jobs cleanup loop will be
-- relaxed in a later commit once the catalog is the source of truth.

CREATE TABLE download_attempts (
    id              TEXT PRIMARY KEY,             -- "att-" + 32 hex chars
    spotify_id      TEXT NOT NULL,                -- FK -> tracks(spotify_id)
    library_file_id TEXT,                         -- FK -> library_files(id), NULL until success

    user_id         TEXT NOT NULL DEFAULT '',
    watchlist_id    TEXT NOT NULL DEFAULT '',     -- empty for manual one-off downloads
    batch_id        TEXT NOT NULL DEFAULT '',     -- groups attempts from one EnqueueBatch

    provider        TEXT NOT NULL DEFAULT '',     -- last provider tried (may be empty for skipped)
    quality         TEXT NOT NULL DEFAULT '',     -- quality requested (or actual on success)

    status          TEXT NOT NULL,
    -- status: 'pending' | 'downloading' | 'done' | 'failed' | 'skipped' | 'cancelled'
    error           TEXT NOT NULL DEFAULT '',     -- last error message; non-empty only when status='failed'
    attempt_count   INTEGER NOT NULL DEFAULT 1,   -- bumped if the same row is retried in-place

    started_at      INTEGER NOT NULL,             -- Unix seconds, when row was created
    completed_at    INTEGER NOT NULL DEFAULT 0,   -- Unix seconds, set when status becomes terminal

    FOREIGN KEY (spotify_id)      REFERENCES tracks(spotify_id)   ON DELETE RESTRICT,
    FOREIGN KEY (library_file_id) REFERENCES library_files(id)    ON DELETE SET NULL
);

CREATE INDEX idx_download_attempts_spotify_id   ON download_attempts(spotify_id);
CREATE INDEX idx_download_attempts_status       ON download_attempts(status);
CREATE INDEX idx_download_attempts_user_id      ON download_attempts(user_id)      WHERE user_id != '';
CREATE INDEX idx_download_attempts_watchlist_id ON download_attempts(watchlist_id) WHERE watchlist_id != '';
CREATE INDEX idx_download_attempts_batch_id     ON download_attempts(batch_id)     WHERE batch_id != '';
CREATE INDEX idx_download_attempts_started_at   ON download_attempts(started_at);
