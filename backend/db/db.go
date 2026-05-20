// Package db wraps the SQLite catalog database used to track every Spotify
// track ever encountered, every audio file on disk, every download attempt,
// and every playlist snapshot.
//
// The catalog complements the existing BoltDB store (jobs.db). BoltDB stays
// the source of truth for the live download queue, watchlist definitions,
// users and API keys; SQLite stores the long-term history that survives the
// 24-hour cleanup loop.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// dbFileName is the basename of the SQLite catalog inside the config dir.
const dbFileName = "catalog.db"

// dsnTemplate enables WAL (concurrent readers + serialized writers),
// foreign keys, and a 5 s busy timeout so concurrent writers wait instead
// of returning SQLITE_BUSY immediately. NORMAL synchronous is the SQLite
// recommendation under WAL: durable enough for our use case (a track loss
// on power-fail re-downloads itself), faster than FULL.
const dsnTemplate = "file:%s?" +
	"_pragma=journal_mode(WAL)&" +
	"_pragma=foreign_keys(on)&" +
	"_pragma=busy_timeout(5000)&" +
	"_pragma=synchronous(NORMAL)"

// openTimeout caps the time we will wait for the database to respond on
// startup before giving up. Keeps boot deterministic.
const openTimeout = 10 * time.Second

// Open returns a *sql.DB pointing at the catalog database in dir, after
// applying any pending migrations. The caller owns the lifecycle and must
// call Close when done.
func Open(dir string) (*sql.DB, error) {
	path := filepath.Join(dir, dbFileName)
	dsn := fmt.Sprintf(dsnTemplate, path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite at %s: %w", path, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite at %s: %w", path, err)
	}

	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
