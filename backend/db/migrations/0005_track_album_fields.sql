-- Migration 0005: denormalized album-level fields directly on tracks.
--
-- A proper `albums` table (with a real Spotify album ID as its key)
-- already exists from migration 0001, but linking tracks.album_id to it
-- requires threading Spotify's per-track album_id through five different
-- JSON payload shapes in watcher.go plus the manual single-track download
-- path — a much larger change than populating what's already sitting on
-- the in-memory Job struct at catalog-write time.
--
-- These columns are the pragmatic middle ground: everything below is
-- already fetched from Spotify for every track (used for tag embedding),
-- just never persisted to the catalog. Revisit the real albums-table
-- linkage if/when album-level browsing (grouping many tracks under one
-- shared row) becomes an actual feature.

ALTER TABLE tracks ADD COLUMN release_date TEXT NOT NULL DEFAULT '';
ALTER TABLE tracks ADD COLUMN album_name   TEXT NOT NULL DEFAULT '';
ALTER TABLE tracks ADD COLUMN album_artist TEXT NOT NULL DEFAULT '';
ALTER TABLE tracks ADD COLUMN cover_url    TEXT NOT NULL DEFAULT '';
ALTER TABLE tracks ADD COLUMN copyright    TEXT NOT NULL DEFAULT '';
