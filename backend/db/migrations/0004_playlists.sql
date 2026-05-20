-- Migration 0004: playlist tracking — current state + versioned snapshots.
--
-- watchlist_tracks  : current contents of each watchlist, source of M3U8.
-- playlist_snapshots: historical captures, one row per sync where the
--                     contents actually changed. Lets the user browse
--                     "what was in Discover Weekly 6 weeks ago".
-- playlist_snapshot_tracks: junction freezing the track list at snapshot
--                           time, with position so order is preserved.
--
-- The watchlists table itself stays in BoltDB for now: it carries
-- behaviour state (interval, sync_deletions, last_sync_at, sync_logs)
-- that the live daemon mutates frequently. SQLite-side junction tables
-- get the IDs only; integrity between the two stores is maintained by
-- application code (watcher.go).

-- Current state: which tracks live in which watchlist, in what order.
CREATE TABLE watchlist_tracks (
    watchlist_id TEXT NOT NULL,
    spotify_id   TEXT NOT NULL,
    position     INTEGER NOT NULL DEFAULT 0,  -- 0-based ordinal in the playlist
    added_at     INTEGER NOT NULL,            -- when this row was inserted

    PRIMARY KEY (watchlist_id, spotify_id),
    FOREIGN KEY (spotify_id) REFERENCES tracks(spotify_id) ON DELETE RESTRICT
    -- No FK on watchlist_id: watchlists table lives in BoltDB.
);

CREATE INDEX idx_watchlist_tracks_spotify_id ON watchlist_tracks(spotify_id);
CREATE INDEX idx_watchlist_tracks_position   ON watchlist_tracks(watchlist_id, position);

-- Historical snapshots: one row per sync where the contents changed.
-- We deliberately do NOT snapshot no-op syncs; track_count + diff fields
-- below carry the metadata needed to render a timeline.
CREATE TABLE playlist_snapshots (
    id                  TEXT PRIMARY KEY,             -- "snap-" + 32 hex chars
    watchlist_id        TEXT NOT NULL,
    spotify_snapshot_id TEXT NOT NULL DEFAULT '',     -- Spotify's own snapshot id when available
    playlist_name       TEXT NOT NULL DEFAULT '',     -- name AT THIS POINT IN TIME (renames preserved)
    track_count         INTEGER NOT NULL DEFAULT 0,
    taken_at            INTEGER NOT NULL,             -- Unix seconds

    -- Diff vs the immediately previous snapshot for this watchlist.
    added_count   INTEGER NOT NULL DEFAULT 0,
    removed_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_playlist_snapshots_watchlist_id ON playlist_snapshots(watchlist_id);
CREATE INDEX idx_playlist_snapshots_taken_at     ON playlist_snapshots(taken_at);

-- Frozen track list at snapshot time. ON DELETE CASCADE so dropping a
-- snapshot row drops the junction in one stroke.
CREATE TABLE playlist_snapshot_tracks (
    snapshot_id TEXT NOT NULL,
    spotify_id  TEXT NOT NULL,
    position    INTEGER NOT NULL DEFAULT 0,

    PRIMARY KEY (snapshot_id, spotify_id),
    FOREIGN KEY (snapshot_id) REFERENCES playlist_snapshots(id) ON DELETE CASCADE,
    FOREIGN KEY (spotify_id)  REFERENCES tracks(spotify_id)     ON DELETE RESTRICT
);

CREATE INDEX idx_psnap_tracks_snap_id    ON playlist_snapshot_tracks(snapshot_id, position);
CREATE INDEX idx_psnap_tracks_spotify_id ON playlist_snapshot_tracks(spotify_id);
