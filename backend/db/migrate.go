package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

//go:embed migrations
var migrationsFS embed.FS

// migrationsDir is the embed path holding the .sql files. Single source of
// truth so the path appears once in this file.
const migrationsDir = "migrations"

// createMigrationsTable bootstraps the version-tracking table itself. Run
// before any user migration so the runner can record what it has applied.
const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    TEXT PRIMARY KEY,
	applied_at INTEGER NOT NULL
)`

// Migrate applies any pending migrations from the embedded migrations/ folder
// in lexicographic order. Each migration runs in its own transaction so a
// failure rolls back cleanly without leaving the schema half-applied.
//
// Migrations are forward-only by design: there is no down direction. To
// revert a change, write a new forward migration. This trade simplicity
// against flexibility — repeatable, no surprise ALTERs in production.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	pending, err := pendingMigrations(ctx, db)
	if err != nil {
		return err
	}

	for _, m := range pending {
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
		slog.Info("[DB] Migration applied", "version", m.version)
	}
	return nil
}

// migration is a parsed entry from migrationsFS: filename version (without
// extension) and the raw SQL body.
type migration struct {
	version string
	sql     string
}

// pendingMigrations returns the list of migrations present on disk that
// have not yet been recorded in schema_migrations, sorted lexicographically.
func pendingMigrations(ctx context.Context, db *sql.DB) ([]migration, error) {
	all, err := readEmbeddedMigrations()
	if err != nil {
		return nil, err
	}
	applied, err := loadAppliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	pending := make([]migration, 0)
	for _, m := range all {
		if !applied[m.version] {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// readEmbeddedMigrations loads every .sql file under migrationsDir and
// returns them sorted by filename. The convention is "NNNN_description.sql"
// (zero-padded numeric prefix), so lexicographic order == application order.
func readEmbeddedMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir(migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations dir: %w", err)
	}
	migs := make([]migration, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		body, err := migrationsFS.ReadFile(migrationsDir + "/" + name)
		if err != nil {
			return nil, fmt.Errorf("read embedded migration %s: %w", name, err)
		}
		migs = append(migs, migration{
			version: strings.TrimSuffix(name, ".sql"),
			sql:     string(body),
		})
	}
	sort.Slice(migs, func(i, j int) bool {
		return migs[i].version < migs[j].version
	})
	return migs, nil
}

// loadAppliedVersions reads schema_migrations and returns a set of versions
// already recorded as applied.
func loadAppliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs every statement in m.sql inside a single transaction
// and records the version on success. modernc.org/sqlite (via database/sql)
// only accepts one statement per Exec, so we split on ";\n" — convention
// for our DDL-only migrations. Triggers or stored bodies that contain inner
// ";\n" should keep them on the same logical line.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	for i, stmt := range splitStatements(m.sql) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply %s statement #%d: %w", m.version, i+1, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("record %s: %w", m.version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", m.version, err)
	}
	return nil
}

// splitStatements separates a migration body into individual SQL statements
// using ";\n" as the delimiter. Trailing whitespace and empty fragments are
// dropped so an extra newline at end of file does not produce a blank Exec.
func splitStatements(sql string) []string {
	raw := strings.Split(sql, ";\n")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}
