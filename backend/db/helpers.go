package db

import (
	"context"
	"database/sql"
)

// Querier is the subset of *sql.DB and *sql.Tx used by DAO operations.
// Accepting this interface lets callers compose multiple DAO calls inside
// a single transaction without code duplication.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// nullableString maps an empty Go string to SQL NULL. Useful for FK columns
// where "" is not a valid foreign-key value but Go zero-values produce "".
func nullableString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// boolToInt encodes a Go bool as 0 or 1 for SQLite storage. SQLite has no
// native bool type; the project convention is INTEGER columns named
// like a predicate (e.g. "explicit").
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
