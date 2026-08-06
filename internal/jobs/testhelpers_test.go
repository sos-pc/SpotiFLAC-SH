package jobs

import (
	"database/sql"
	"os"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
	bolt "go.etcd.io/bbolt"
)

// recordingSink collects every event the manager publishes.
//
// This is why EventSink exists. Before it, a test that wanted to assert on
// emitted events had to build the real SSE hub — an HTTP transport — because
// JobManager constructed one internally and published straight into it. The hub
// lives in package main and could not follow this package here; a channel with
// one method can.
type recordingSink struct{ events chan JobEvent }

func newRecordingSink() *recordingSink {
	// Buffered generously: Publish must never block the manager, and a test
	// that stops reading should not deadlock the code under test.
	return &recordingSink{events: make(chan JobEvent, 64)}
}

func (r *recordingSink) Publish(ev JobEvent) {
	select {
	case r.events <- ev:
	default:
	}
}

// newTestJobManager builds a manager on throwaway databases.
//
// The root package has a helper of the same shape for tests that need a Watcher
// or a Server alongside. They are deliberately separate rather than shared: this
// one cannot reach package main, and making the root's version importable would
// mean exporting test scaffolding from the binary's own package.
func newTestJobManager(t *testing.T, withCatalog bool) *JobManager {
	t.Helper()
	jm, _ := newTestJobManagerSink(t, withCatalog, nil)
	return jm
}

// newTestJobManagerWithSink also hands back the sink, for tests asserting on
// the events a call emits.
func newTestJobManagerWithSink(t *testing.T, withCatalog bool) (*JobManager, *recordingSink) {
	t.Helper()
	sink := newRecordingSink()
	jm, _ := newTestJobManagerSink(t, withCatalog, sink)
	return jm, sink
}

func newTestJobManagerSink(t *testing.T, withCatalog bool, sink EventSink) (*JobManager, *sql.DB) {
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

	jm, err := NewJobManager(t.TempDir(), boltDB, catalog, sink)
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	t.Cleanup(jm.Close)
	return jm, catalog
}
