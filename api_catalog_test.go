package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newCatalogTestDB builds a throwaway catalog shaped like the real one: a text
// primary key, a text column, an integer column, and rows to page through.
func newCatalogTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbh, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })

	_, err = dbh.Exec(`
		CREATE TABLE tracks (
			spotify_id  TEXT PRIMARY KEY,
			name        TEXT NOT NULL DEFAULT '',
			artist_name TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX idx_tracks_name ON tracks(name);
		CREATE TABLE download_attempts (
			id      INTEGER PRIMARY KEY,
			status  TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT ''
		);`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	rows := []struct {
		id, name, artist string
		ms               int
	}{
		{"id1", "Yadnus", "!!!", 100},
		{"id2", "Even When The Water's Cold", "!!!", 200},
		{"id3", "Tour De France", "Powerplant", 300},
		{"id4", "100% Pure Love", "Crystal Waters", 400},
	}
	for _, r := range rows {
		if _, err := dbh.Exec(
			`INSERT INTO tracks (spotify_id, name, artist_name, duration_ms) VALUES (?, ?, ?, ?)`,
			r.id, r.name, r.artist, r.ms); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return dbh
}

func loadTestSchema(t *testing.T, dbh *sql.DB) *catalogSchema {
	t.Helper()
	schema, err := loadCatalogSchema(dbh)
	if err != nil {
		t.Fatalf("loadCatalogSchema: %v", err)
	}
	return schema
}

// The whitelist is read from the database rather than written by hand, so that
// a migration adding a column cannot leave it silently invisible — migration
// 0005 added five columns to `tracks`, which a hand-written list would have missed.
func TestCatalogSchemaComesFromTheDatabase(t *testing.T) {
	dbh := newCatalogTestDB(t)
	schema := loadTestSchema(t, dbh)

	if !schema.hasTable("tracks") || !schema.hasTable("download_attempts") {
		t.Fatalf("tables not discovered: %v", schema.columns)
	}
	// An index is not a table.
	if schema.hasTable("idx_tracks_name") {
		t.Error("an index was exposed as a table")
	}
	want := []string{"spotify_id", "name", "artist_name", "duration_ms"}
	if got := schema.columns["tracks"]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("columns = %v, want %v", got, want)
	}
	// q must only reach text columns: matching a substring against an INTEGER
	// is meaningless and would just produce confusing empty results.
	if got := schema.textColumns["tracks"]; strings.Join(got, ",") != "spotify_id,name,artist_name" {
		t.Errorf("text columns = %v, want the three TEXT ones", got)
	}

	// A newly added column must appear without touching this code.
	if _, err := dbh.Exec(`ALTER TABLE tracks ADD COLUMN copyright TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("alter: %v", err)
	}
	if _, ok := loadTestSchema(t, dbh).resolveColumn("tracks", "copyright"); !ok {
		t.Error("a column added by migration did not appear in the whitelist")
	}
}

// The security claim in one test: an identifier supplied by a client is never
// put into SQL. It is compared against the schema, and a non-match is refused.
func TestCatalogRejectsIdentifiersItDidNotIssue(t *testing.T) {
	dbh := newCatalogTestDB(t)
	schema := loadTestSchema(t, dbh)

	hostile := []string{
		`name"; DROP TABLE tracks; --`,
		`name) OR 1=1 --`,
		"rowid",      // real pseudo-column, but not one PRAGMA lists
		"NAME",       // column matching is exact, not case-folded
		"sqlite_",    // no reaching internal tables
		"other.name", // no crossing into another table
		"1",          // ordinal references are not accepted either
		"",           // an empty column name is not a column
	}
	for _, attempt := range hostile {
		t.Run(attempt, func(t *testing.T) {
			if col, ok := schema.resolveColumn("tracks", attempt); ok {
				t.Errorf("accepted %q, resolved to %q", attempt, col)
			}
		})
	}
	for _, attempt := range []string{`tracks"; DROP TABLE tracks; --`, "sqlite_master", "TRACKS"} {
		if _, ok := schema.resolveTable(attempt); ok {
			t.Errorf("accepted table %q", attempt)
		}
	}
}

// Filters must produce bound placeholders, never an inlined value: the whole
// point is that no client string is ever parsed as SQL.
func TestCatalogFiltersBindTheirValues(t *testing.T) {
	dbh := newCatalogTestDB(t)
	schema := loadTestSchema(t, dbh)

	t.Run("equality", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/?artist_name=%21%21%21", nil)
		where, args, err := buildCatalogFilters(schema, "tracks", r)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if where != ` WHERE "artist_name" = ?` {
			t.Errorf("where = %q", where)
		}
		if len(args) != 1 || args[0] != "!!!" {
			t.Errorf("args = %v", args)
		}
	})

	t.Run("une valeur hostile reste une valeur", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/?name=x%27%3B+DROP+TABLE+tracks%3B+--", nil)
		where, args, err := buildCatalogFilters(schema, "tracks", r)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if strings.Contains(where, "DROP") {
			t.Fatalf("a value reached the SQL text: %q", where)
		}
		if args[0] != "x'; DROP TABLE tracks; --" {
			t.Errorf("value was mangled instead of bound: %v", args[0])
		}
	})

	t.Run("colonne inconnue refusée, pas ignorée", func(t *testing.T) {
		// Silently ignoring ?statuz=failed would return every row and look
		// like a successful query — the worst outcome for a debugging tool.
		r := httptest.NewRequest(http.MethodGet, "/?statuz=failed", nil)
		if _, _, err := buildCatalogFilters(schema, "tracks", r); err == nil {
			t.Error("an unknown column was accepted")
		}
	})

	t.Run("q couvre les colonnes texte seulement", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/?q=daft", nil)
		where, args, err := buildCatalogFilters(schema, "tracks", r)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if strings.Contains(where, "duration_ms") {
			t.Errorf("q reached an integer column: %q", where)
		}
		if n := strings.Count(where, "LIKE ?"); n != 3 {
			t.Errorf("expected 3 LIKE clauses, got %d in %q", n, where)
		}
		for _, a := range args {
			if a != "%daft%" {
				t.Errorf("arg = %v, want %%daft%%", a)
			}
		}
	})
}

// A search for "100%" must find the track called "100% Pure Love" and nothing
// else. Unescaped, the % would be a wildcard and match every row — surprising
// rather than unsafe, but the kind of surprise that makes a tool untrustworthy.
func TestCatalogSearchTreatsWildcardsAsLiterals(t *testing.T) {
	dbh := newCatalogTestDB(t)
	schema := loadTestSchema(t, dbh)

	r := httptest.NewRequest(http.MethodGet, "/?q=100%25", nil)
	where, args, err := buildCatalogFilters(schema, "tracks", r)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	var count int
	if err := dbh.QueryRow(`SELECT COUNT(*) FROM "tracks"`+where, args...).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("searching for a literal %% matched %d rows, want 1", count)
	}
}

// The generated statement must actually run. In particular ORDER BY "rowid":
// SQLite resolves a quoted identifier that matches no column as a string
// literal instead of failing, which would silently sort every row by the same
// constant and make pagination repeat and skip rows.
func TestCatalogGeneratedSQLRunsAndPaginatesStably(t *testing.T) {
	dbh := newCatalogTestDB(t)
	schema := loadTestSchema(t, dbh)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	orderCol, dir, err := parseCatalogOrder(schema, "tracks", r)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if orderCol != "rowid" {
		t.Fatalf("default order = %q, want rowid", orderCol)
	}

	// The sharp test for the concern above: ask for DESC and check the order
	// actually reverses. A quoted identifier resolved as the string literal
	// 'rowid' would sort every row by the same constant, so DESC would return
	// the same sequence as ASC and pagination would quietly repeat and skip
	// rows. Merely checking that pages do not overlap would not catch it,
	// because SQLite's scan order stays stable under a constant sort.
	readOrder := func(dir string) []string {
		q := fmt.Sprintf(`SELECT "spotify_id" FROM "tracks" ORDER BY %q %s`, orderCol, dir)
		rows, err := dbh.Query(q)
		if err != nil {
			t.Fatalf("query %s: %v", dir, err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	asc, desc := readOrder("ASC"), readOrder("DESC")
	if len(asc) != 4 {
		t.Fatalf("got %d rows, want 4", len(asc))
	}
	for i := range asc {
		if asc[i] != desc[len(desc)-1-i] {
			t.Fatalf("DESC did not reverse ASC (asc=%v desc=%v) — the quoted "+
				"identifier is not resolving to the rowid pseudo-column", asc, desc)
		}
	}

	seen := map[string]bool{}
	for offset := 0; offset < 4; offset += 2 {
		q := fmt.Sprintf(`SELECT "spotify_id" FROM "tracks" ORDER BY %q %s LIMIT ? OFFSET ?`, orderCol, dir)
		rows, err := dbh.Query(q, 2, offset)
		if err != nil {
			t.Fatalf("query at offset %d: %v", offset, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if seen[id] {
				t.Errorf("row %s returned on two different pages — ordering is not stable", id)
			}
			seen[id] = true
		}
		rows.Close()
	}
	if len(seen) != 4 {
		t.Errorf("paged through %d distinct rows, want 4", len(seen))
	}
}

// Both routes match /api/v1/admin/db/tables. Go's ServeMux is documented to
// prefer the more specific pattern, so the literal should win over the
// wildcard — but if it ever went the other way, "tables" would be read as a
// table name and the listing endpoint would answer 404 unknown table. Cheap to
// pin down, and it fails loudly if the routing rules shift under us.
func TestCatalogListingRouteBeatsTheWildcard(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/db/tables", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("listing"))
	})
	mux.HandleFunc("GET /api/v1/admin/db/{table}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("rows:" + r.PathValue("table")))
	})

	for _, tc := range []struct{ path, want string }{
		{"/api/v1/admin/db/tables", "listing"},
		{"/api/v1/admin/db/tracks", "rows:tracks"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s routed to %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestCatalogPagingBounds(t *testing.T) {
	tests := []struct {
		query      string
		wantLimit  int
		wantOffset int
		wantErr    bool
	}{
		{"/", catalogDefaultLimit, 0, false},
		{"/?limit=10&offset=5", 10, 5, false},
		{"/?limit=99999", catalogMaxLimit, 0, false}, // capped, not refused
		{"/?limit=0", 0, 0, true},
		{"/?limit=-1", 0, 0, true},
		{"/?offset=-1", 0, 0, true},
		{"/?limit=abc", 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			limit, offset, err := parseCatalogPaging(httptest.NewRequest(http.MethodGet, tc.query, nil))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && (limit != tc.wantLimit || offset != tc.wantOffset) {
				t.Errorf("limit/offset = %d/%d, want %d/%d", limit, offset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}
