-- Migration 0001: tracks + albums core entities.
--
-- These represent the Spotify-side identity of music. They are independent
-- of any download or watchlist; a track exists in the catalog as soon as
-- we have seen its Spotify URL, even if no file was ever downloaded.
--
-- spotify_id is the stable identifier from Spotify; it never changes
-- once assigned. album_id on tracks references albums.spotify_id.
-- first_seen_at / last_seen_at let us track when metadata was first
-- collected and when it was last refreshed by a fetch or sync.

CREATE TABLE albums (
    spotify_id    TEXT PRIMARY KEY,
    name          TEXT NOT NULL DEFAULT '',
    album_artist  TEXT NOT NULL DEFAULT '',
    release_date  TEXT NOT NULL DEFAULT '',  -- "YYYY-MM-DD" or "YYYY"
    total_tracks  INTEGER NOT NULL DEFAULT 0,
    total_discs   INTEGER NOT NULL DEFAULT 1,
    cover_url     TEXT NOT NULL DEFAULT '',
    label         TEXT NOT NULL DEFAULT '',
    copyright     TEXT NOT NULL DEFAULT '',
    first_seen_at INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL
);

CREATE INDEX idx_albums_name ON albums(name);

CREATE TABLE tracks (
    spotify_id    TEXT PRIMARY KEY,
    isrc          TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    artist_name   TEXT NOT NULL DEFAULT '',  -- joined display string ("A, B feat. C")
    album_id      TEXT,                       -- nullable: track may exist before album is upserted
    track_number  INTEGER NOT NULL DEFAULT 0,
    disc_number   INTEGER NOT NULL DEFAULT 0,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    explicit      INTEGER NOT NULL DEFAULT 0, -- bool as 0/1
    genre         TEXT NOT NULL DEFAULT '',   -- from MusicBrainz when fetched
    first_seen_at INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    FOREIGN KEY (album_id) REFERENCES albums(spotify_id) ON DELETE SET NULL
);

CREATE INDEX idx_tracks_isrc        ON tracks(isrc) WHERE isrc != '';
CREATE INDEX idx_tracks_album_id    ON tracks(album_id);
CREATE INDEX idx_tracks_artist_name ON tracks(artist_name);
