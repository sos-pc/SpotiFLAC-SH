package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Admin-only read access to the SQLite catalog
// ─────────────────────────────────────────────────────────────────────────────
//
// Why this exists: answering "why did this track fail three times last night?"
// used to mean writing Go and redeploying, or shelling into the container.
// These two routes let the catalog be read over HTTP instead.
//
// The whole security argument rests on one distinction. A SQL statement holds
// *values* and *identifiers*, and they are not interchangeable:
//
//	SELECT * FROM download_attempts WHERE status = 'failed' ORDER BY started_at
//	                   table               column    value            column
//
// Values are passed as bound parameters — the driver sends them outside the SQL
// text, so no value from a client can ever be executed, whatever it contains.
// Identifiers cannot be bound (SQLite rejects `FROM ?`), so they must end up
// concatenated into the statement. That is the only injection surface.
//
// Hence: a client-supplied identifier is never inserted into SQL. It is
// *compared* against what the database itself reports, and on a match the
// server uses its own copy of the name. The client picks from a closed set; it
// never writes one. The set is read from the live schema rather than written by
// hand, because migration 0005 already added five columns to `tracks` — a
// hand-maintained list would have gone stale on that migration alone, and a
// forgotten column becomes silently invisible rather than an error.

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// catalogSchema is the set of tables and columns the catalog actually has,
// read once from SQLite and then used as the whitelist for every request.
type catalogSchema struct {
	// columns maps a table name to its column names, in schema order.
	columns map[string][]string
	// textColumns maps a table name to the subset of its columns that hold
	// text, which are the only ones the `q` substring search may touch.
	// Matching a substring against an INTEGER column is not dangerous, it is
	// just meaningless, and allowing it would invite confusing empty results.
	textColumns map[string][]string
}

var (
	catalogSchemaOnce  sync.Once
	catalogSchemaCache *catalogSchema
	catalogSchemaErr   error
)

// loadCatalogSchema reads the table and column names from SQLite itself.
//
// sqlite_master is filtered to type='table' so indexes and views never appear,
// and to exclude SQLite's own internal sqlite_* bookkeeping tables. Everything
// the migrations create is exposed, including schema_migrations: it carries no
// data of its own beyond which migrations ran, and that is exactly the kind of
// thing one wants to check when a deployment behaves unexpectedly.
func loadCatalogSchema(dbh *sql.DB) (*catalogSchema, error) {
	rows, err := dbh.Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tables: %w", err)
	}

	schema := &catalogSchema{
		columns:     make(map[string][]string, len(tables)),
		textColumns: make(map[string][]string, len(tables)),
	}
	for _, table := range tables {
		cols, textCols, err := readTableColumns(dbh, table)
		if err != nil {
			return nil, err
		}
		schema.columns[table] = cols
		schema.textColumns[table] = textCols
	}
	return schema, nil
}

// readTableColumns runs PRAGMA table_info for one table.
//
// The table name is interpolated here, which is the one place it must be. It is
// safe precisely because it did not come from a client: it came from
// sqlite_master a moment ago. No request-scoped data reaches this function.
func readTableColumns(dbh *sql.DB, table string) (all []string, text []string, err error) {
	rows, err := dbh.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, nil, fmt.Errorf("describe %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			declType   string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &declType, &notNull, &defaultVal, &pk); err != nil {
			return nil, nil, fmt.Errorf("scan column of %s: %w", table, err)
		}
		all = append(all, name)
		// SQLite type affinity: anything containing "CHAR", "CLOB" or "TEXT"
		// has TEXT affinity. The migrations only ever declare TEXT and
		// INTEGER, but deriving this keeps it correct if that changes.
		if strings.Contains(strings.ToUpper(declType), "CHAR") ||
			strings.Contains(strings.ToUpper(declType), "CLOB") ||
			strings.Contains(strings.ToUpper(declType), "TEXT") {
			text = append(text, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate columns of %s: %w", table, err)
	}
	return all, text, nil
}

// schema returns the cached schema, loading it on first use.
//
// The schema is read once per process. Migrations run at startup, before any
// request can arrive, so the schema cannot change under a running server —
// caching it costs nothing in correctness and keeps every request from paying
// for eight PRAGMA round-trips.
func (s *Server) catalogSchemaFor() (*catalogSchema, error) {
	catalogSchemaOnce.Do(func() {
		if s.ctr == nil || s.ctr.Catalog == nil {
			catalogSchemaErr = fmt.Errorf("catalog database is not available")
			return
		}
		catalogSchemaCache, catalogSchemaErr = loadCatalogSchema(s.ctr.Catalog)
	})
	return catalogSchemaCache, catalogSchemaErr
}

// resolveColumn maps a client-supplied column name to the server's own copy of
// that name, or reports that it is unknown. The returned string is the one that
// may be used in SQL; the input never is.
func (cs *catalogSchema) resolveColumn(table, requested string) (string, bool) {
	for _, col := range cs.columns[table] {
		if col == requested {
			return col, true
		}
	}
	return "", false
}

func (cs *catalogSchema) hasTable(table string) bool {
	_, ok := cs.columns[table]
	return ok
}

// ─────────────────────────────────────────────────────────────────────────────

// catalogTableInfo describes one table in the GET /admin/db/tables listing.
type catalogTableInfo struct {
	Name    string   `json:"name"`
	Rows    int64    `json:"rows"`
	Columns []string `json:"columns"`
}

// catalogRowsResponse is the payload of GET /admin/db/{table}.
type catalogRowsResponse struct {
	Table string `json:"table"`
	// Total is the number of rows matching the filters, ignoring pagination,
	// so a caller can tell "20 of 3000" from "20 of 20".
	Total   int64                    `json:"total"`
	Limit   int                      `json:"limit"`
	Offset  int                      `json:"offset"`
	Order   string                   `json:"order"`
	Dir     string                   `json:"dir"`
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
}

const (
	catalogDefaultLimit = 50
	catalogMaxLimit     = 500
)

// registerCatalogRoutes wires the /api/v1/admin/db/* endpoints.
//
// Admin-only, and that is a deliberate call rather than an inherited default:
// these tables carry user_id and downloaded_by, so they say who downloaded
// what. They hold no secrets — tokens and passwords live in BoltDB, not here —
// but on a multi-user instance this is still other people's listening history.
func (s *Server) registerCatalogRoutes() {
	s.mux.Handle("GET /api/v1/admin/db/tables", s.v1Auth(s.v1CatalogTables))
	s.mux.Handle("GET /api/v1/admin/db/{table}", s.v1Auth(s.v1CatalogRows))
}

// v1CatalogTables lists every catalog table with its row count and columns.
func (s *Server) v1CatalogTables(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	schema, err := s.catalogSchemaFor()
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	names := make([]string, 0, len(schema.columns))
	for name := range schema.columns {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]catalogTableInfo, 0, len(names))
	for _, name := range names {
		var count int64
		// name came from sqlite_master, never from the request.
		if err := s.ctr.Catalog.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %q", name)).Scan(&count); err != nil {
			writeV1Error(w, http.StatusInternalServerError, fmt.Sprintf("count %s: %v", name, err))
			return
		}
		out = append(out, catalogTableInfo{Name: name, Rows: count, Columns: schema.columns[name]})
	}
	writeV1JSON(w, http.StatusOK, out)
}

// v1CatalogRows reads one table with pagination, equality filters and a text
// search.
//
// Reserved query parameters are limit, offset, order, dir and q. Every other
// parameter is read as an equality filter on the column of that name, which is
// why an unknown parameter is rejected rather than ignored: silently dropping
// ?statuz=failed would return every row and look like a successful query.
func (s *Server) v1CatalogRows(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	schema, err := s.catalogSchemaFor()
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	table := r.PathValue("table")
	if !schema.hasTable(table) {
		writeV1Error(w, http.StatusNotFound, fmt.Sprintf("unknown table %q", table))
		return
	}
	// Past this point `table` is only ever used via the schema's own key, and
	// the identifiers below are the server's copies, not the request's.
	tableName, _ := schema.resolveTable(table)

	where, args, err := buildCatalogFilters(schema, tableName, r)
	if err != nil {
		writeV1Error(w, http.StatusBadRequest, err.Error())
		return
	}

	orderCol, dir, err := parseCatalogOrder(schema, tableName, r)
	if err != nil {
		writeV1Error(w, http.StatusBadRequest, err.Error())
		return
	}

	limit, offset, err := parseCatalogPaging(r)
	if err != nil {
		writeV1Error(w, http.StatusBadRequest, err.Error())
		return
	}

	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %q%s", tableName, where)
	if err := s.ctr.Catalog.QueryRow(countSQL, args...).Scan(&total); err != nil {
		writeV1Error(w, http.StatusInternalServerError, fmt.Sprintf("count: %v", err))
		return
	}

	cols := schema.columns[tableName]
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = fmt.Sprintf("%q", c)
	}

	// LIMIT and OFFSET are bound rather than formatted: they are values, and
	// treating them as such means the parsing above is a usability guard, not
	// the thing standing between a client and the query.
	rowsSQL := fmt.Sprintf("SELECT %s FROM %q%s ORDER BY %q %s LIMIT ? OFFSET ?",
		strings.Join(quoted, ", "), tableName, where, orderCol, dir)
	rows, err := s.ctr.Catalog.Query(rowsSQL, append(append([]interface{}{}, args...), limit, offset)...)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, fmt.Sprintf("query: %v", err))
		return
	}
	defer rows.Close()

	out := make([]map[string]interface{}, 0, limit)
	for rows.Next() {
		scanTargets := make([]interface{}, len(cols))
		holders := make([]interface{}, len(cols))
		for i := range cols {
			scanTargets[i] = &holders[i]
		}
		if err := rows.Scan(scanTargets...); err != nil {
			writeV1Error(w, http.StatusInternalServerError, fmt.Sprintf("scan: %v", err))
			return
		}
		row := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			// The driver hands TEXT back as []byte, which encoding/json would
			// render as base64. Every text column would arrive unreadable.
			if b, ok := holders[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = holders[i]
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeV1Error(w, http.StatusInternalServerError, fmt.Sprintf("iterate: %v", err))
		return
	}

	writeV1JSON(w, http.StatusOK, catalogRowsResponse{
		Table: tableName, Total: total, Limit: limit, Offset: offset,
		Order: orderCol, Dir: dir, Columns: cols, Rows: out,
	})
}

// resolveTable mirrors resolveColumn for table names.
func (cs *catalogSchema) resolveTable(requested string) (string, bool) {
	for name := range cs.columns {
		if name == requested {
			return name, true
		}
	}
	return "", false
}

// catalogReservedParams are the query parameters that control the read itself
// rather than filtering it.
var catalogReservedParams = map[string]bool{
	"limit": true, "offset": true, "order": true, "dir": true, "q": true,
}

// buildCatalogFilters turns the query string into a WHERE clause and its bound
// arguments. Every column name in the returned SQL comes from the schema; every
// client-supplied value goes into args.
func buildCatalogFilters(schema *catalogSchema, table string, r *http.Request) (string, []interface{}, error) {
	var clauses []string
	var args []interface{}

	// Sorted for a deterministic clause order, which keeps the generated SQL
	// stable across requests and therefore testable.
	params := make([]string, 0, len(r.URL.Query()))
	for key := range r.URL.Query() {
		params = append(params, key)
	}
	sort.Strings(params)

	for _, key := range params {
		if catalogReservedParams[key] {
			continue
		}
		col, ok := schema.resolveColumn(table, key)
		if !ok {
			return "", nil, fmt.Errorf("unknown column %q on table %q", key, table)
		}
		value := r.URL.Query().Get(key)
		if value == "" {
			// Asking for an empty value means "rows with nothing here", and a
			// plain `= ''` answers that wrongly: SQL says NULL = '' is never
			// true, so a nullable column full of NULLs reports zero matches —
			// the exact opposite of the truth. Measured on prod: all 2619
			// tracks have a NULL album_id, and ?album_id= returned 0.
			clauses = append(clauses, fmt.Sprintf("(%q IS NULL OR %q = '')", col, col))
			continue
		}
		clauses = append(clauses, fmt.Sprintf("%q = ?", col))
		args = append(args, value)
	}

	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		textCols := schema.textColumns[table]
		if len(textCols) == 0 {
			return "", nil, fmt.Errorf("table %q has no text column to search", table)
		}
		var likes []string
		for _, col := range textCols {
			likes = append(likes, fmt.Sprintf("%q LIKE ? ESCAPE '\\'", col))
			args = append(args, "%"+escapeLikePattern(q)+"%")
		}
		clauses = append(clauses, "("+strings.Join(likes, " OR ")+")")
	}

	if len(clauses) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

// escapeLikePattern neutralises the LIKE wildcards inside a user's search term.
// Without it, searching for a literal "100%" would match far more than intended
// and "_" would match any character — surprising rather than unsafe, but the
// kind of surprise that makes a debugging tool untrustworthy.
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// parseCatalogOrder resolves the sort column and direction.
//
// The default is rowid, which every one of these tables has (none is declared
// WITHOUT ROWID). It matters: SQLite gives no ordering guarantee without an
// ORDER BY, so paginating without one can repeat or skip rows between pages.
func parseCatalogOrder(schema *catalogSchema, table string, r *http.Request) (string, string, error) {
	orderCol := "rowid"
	if requested := r.URL.Query().Get("order"); requested != "" {
		col, ok := schema.resolveColumn(table, requested)
		if !ok {
			return "", "", fmt.Errorf("unknown order column %q on table %q", requested, table)
		}
		orderCol = col
	}

	dir := "ASC"
	switch strings.ToLower(r.URL.Query().Get("dir")) {
	case "", "asc":
	case "desc":
		dir = "DESC"
	default:
		return "", "", fmt.Errorf("dir must be asc or desc")
	}
	return orderCol, dir, nil
}

// parseCatalogPaging resolves limit and offset, capping the page size so a
// single request cannot try to serialise the whole catalog.
func parseCatalogPaging(r *http.Request) (limit int, offset int, err error) {
	limit = catalogDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, fmt.Errorf("limit must be an integer")
		}
		if limit < 1 {
			return 0, 0, fmt.Errorf("limit must be at least 1")
		}
		if limit > catalogMaxLimit {
			limit = catalogMaxLimit
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, fmt.Errorf("offset must be an integer")
		}
		if offset < 0 {
			return 0, 0, fmt.Errorf("offset must not be negative")
		}
	}
	return limit, offset, nil
}
