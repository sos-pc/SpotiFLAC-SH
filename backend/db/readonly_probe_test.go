package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

// Phase 3 (the read-only SQL console) rests on one claim: opening a second
// connection with mode=ro makes "SELECT only" a property of the connection
// rather than the result of inspecting the query text. A verb blacklist is a
// string-matching exercise and loses to the first construct nobody thought of;
// a read-only handle either refuses a write or it does not.
//
// This file measures whether that claim holds for modernc.org/sqlite, including
// the escapes worth worrying about — before any console is built on top of it.

// openROProbe opens a writable catalog, seeds it, then returns a second handle
// on the same file opened read-only, mirroring what the console would do.
func openROProbe(t *testing.T) (rw *sql.DB, ro *sql.DB, path string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "catalog.db")

	rw, err := sql.Open("sqlite", fmt.Sprintf(dsnTemplate, path))
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	t.Cleanup(func() { rw.Close() })
	if _, err := rw.Exec(`CREATE TABLE tracks (spotify_id TEXT PRIMARY KEY, name TEXT);
		INSERT INTO tracks VALUES ('id1', 'Yadnus');`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Same pragmas as the live catalog, plus mode=ro.
	roDSN := fmt.Sprintf(dsnTemplate, path) + "&mode=ro"
	ro, err = sql.Open("sqlite", roDSN)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	if err := ro.Ping(); err != nil {
		t.Fatalf("ping ro (the journal_mode pragma may need dropping for ro): %v", err)
	}
	return rw, ro, path
}

func TestReadOnlyHandleRefusesEveryWrite(t *testing.T) {
	_, ro, _ := openROProbe(t)

	// Reads must still work, or the whole thing is pointless.
	var name string
	if err := ro.QueryRow(`SELECT name FROM tracks WHERE spotify_id = 'id1'`).Scan(&name); err != nil {
		t.Fatalf("SELECT failed on a read-only handle: %v", err)
	}
	if name != "Yadnus" {
		t.Fatalf("SELECT returned %q", name)
	}

	writes := map[string]string{
		"INSERT":            `INSERT INTO tracks VALUES ('id2', 'X')`,
		"UPDATE":            `UPDATE tracks SET name = 'X'`,
		"DELETE":            `DELETE FROM tracks`,
		"DROP":              `DROP TABLE tracks`,
		"CREATE TABLE":      `CREATE TABLE evil (x TEXT)`,
		"ALTER TABLE":       `ALTER TABLE tracks ADD COLUMN evil TEXT`,
		"CREATE INDEX":      `CREATE INDEX evil ON tracks(name)`,
		"REPLACE":           `REPLACE INTO tracks VALUES ('id1', 'X')`,
		"INSERT via SELECT": `INSERT INTO tracks SELECT 'id3', name FROM tracks`,
		"VACUUM":            `VACUUM`,
		// A write hidden behind a leading comment and odd casing — the shape a
		// verb blacklist is most likely to miss, and which the connection
		// refuses without needing to understand it at all.
		"write déguisée en commentaire": "/* SELECT */ iNsErT INTO tracks VALUES ('id4', 'X')",
	}
	for label, stmt := range writes {
		t.Run(label, func(t *testing.T) {
			if _, err := ro.Exec(stmt); err == nil {
				t.Errorf("a read-only handle accepted: %s", stmt)
			} else if !strings.Contains(strings.ToLower(err.Error()), "readonly") &&
				!strings.Contains(strings.ToLower(err.Error()), "read-only") {
				// Still refused, but for another reason — worth knowing.
				t.Logf("refused, but not as read-only: %v", err)
			}
		})
	}

	// The row count must be untouched after all of the above.
	var n int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("table holds %d rows after the write attempts, want 1", n)
	}
}

// ATTACH is the escape that matters: if a read-only connection can attach a
// second database, "read-only" would only describe the original file. A console
// would then need to police ATTACH itself, which is exactly the query-inspection
// game mode=ro is meant to avoid.
func TestReadOnlyHandleAndATTACH(t *testing.T) {
	_, ro, path := openROProbe(t)
	other := filepath.Join(filepath.Dir(path), "other.db")

	_, err := ro.Exec(fmt.Sprintf(`ATTACH DATABASE '%s' AS other`, other))
	if err != nil {
		t.Logf("ATTACH refused outright: %v", err)
		return
	}

	// Measured 2026-07-19: it attaches, the attached database is WRITABLE, and
	// a new file appears on disk. So mode=ro on its own is not the structural
	// guarantee it looks like — it constrains the original file, not the
	// connection's ability to reach a different one. This is not asserted as a
	// failure because it is the driver's behaviour, not a defect in our code;
	// what our code must do about it is asserted in the next test.
	t.Logf("ATTACH accepted on a read-only handle")
	if _, err := ro.Exec(`CREATE TABLE other.evil (x TEXT)`); err == nil {
		t.Logf("→ and the attached database is writable: mode=ro alone is NOT enough")
	} else {
		t.Logf("→ but writing to it was refused: %v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Logf("→ and a file was created at %s", other)
	}
}

// sqliteLimitAttached is SQLITE_LIMIT_ATTACHED. The constant lives in the
// driver's per-GOARCH lib package, which is awkward to import portably; its
// value is part of SQLite's stable C API and is 7 on every platform.
const sqliteLimitAttached = 7

// Closing the ATTACH hole: sqlite3_limit(SQLITE_LIMIT_ATTACHED, 0) on the
// connection. The catch is that limits bind to one connection, and a *sql.DB
// hands out a pool — so a console built on this must hold a single dedicated
// *sql.Conn rather than letting the pool open fresh, unlimited ones.
func TestAttachedLimitZeroClosesTheHole(t *testing.T) {
	_, ro, path := openROProbe(t)
	other := filepath.Join(filepath.Dir(path), "blocked.db")

	ro.SetMaxOpenConns(1)
	conn, err := ro.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()

	prev, err := sqlite.Limit(conn, sqliteLimitAttached, 0)
	if err != nil {
		t.Fatalf("Limit: %v", err)
	}
	t.Logf("SQLITE_LIMIT_ATTACHED: %d -> 0", prev)

	if _, err := conn.ExecContext(context.Background(),
		fmt.Sprintf(`ATTACH DATABASE '%s' AS other`, other)); err == nil {
		t.Error("ATTACH still accepted after setting the limit to 0")
	} else {
		t.Logf("ATTACH now refused: %v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("a file was still created on disk")
	}

	// Reads must survive the limit.
	var n int
	if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tracks`).Scan(&n); err != nil {
		t.Fatalf("SELECT broke after setting the limit: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}

	// NOTE for whoever builds the console on this: do NOT query `ro` (the pool)
	// while holding this dedicated conn. With SetMaxOpenConns(1) the pool has
	// no second connection to hand out and the call blocks forever — this test
	// hung exactly that way when first written. The console must route every
	// query through the one limited conn, which also means serialising them
	// behind a mutex. That is acceptable here (admin-only, rare queries) but it
	// is a design constraint, not an implementation detail.
}
