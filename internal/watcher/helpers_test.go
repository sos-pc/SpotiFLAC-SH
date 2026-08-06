package watcher

import (
	"database/sql"
	"os"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	bolt "go.etcd.io/bbolt"
)

// newTestWatcher builds a Watcher wired the way main.go wires it: its own BoltDB
// and catalog handles, plus a job manager sharing them.
//
// The root package has a near-identical helper. Splitting the packages split
// this too, for the same reason the others were duplicated — test scaffolding
// does not cross a package boundary without being exported, and exporting it
// would put test-only construction in the API the binary links against.
func newTestWatcher(t *testing.T, withCatalog bool) (*Watcher, *jobs.JobManager) {
	t.Helper()

	f, err := os.CreateTemp("", "spotiflac-test-*.db")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	boltDB, err := bolt.Open(f.Name(), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { boltDB.Close() })

	var catalog *sql.DB
	if withCatalog {
		handle, err := db.Open(t.TempDir())
		if err != nil {
			t.Fatalf("db.Open: %v", err)
		}
		t.Cleanup(func() { handle.Close() })
		catalog = handle
	}

	jm, err := jobs.NewJobManager(t.TempDir(), boltDB, catalog, nil)
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	t.Cleanup(jm.Close)

	return &Watcher{db: boltDB, catalog: catalog, jm: jm}, jm
}

// openTestCatalogDB opens a throwaway SQLite catalog.
func openTestCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}
