package main

import (
	"database/sql"
	"os"
	"testing"

	"github.com/go-flac/flacvorbis"
	"github.com/go-flac/go-flac"
	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	bolt "go.etcd.io/bbolt"
)

// openTestCatalogDB opens a throwaway SQLite catalog.
//
// internal/jobs has one of these too. They are four lines each and cannot be
// shared: this package cannot import test helpers from another, and exporting
// scaffolding from internal/jobs to serve the binary's tests would put it in
// the shipped API. Duplicating four lines is the cheaper of the two.
func openTestCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// writeTestFlacWithTags writes a minimal FLAC carrying the given Vorbis
// comments. Duplicated from internal/jobs for the same reason as
// openTestCatalogDB above.
func writeTestFlacWithTags(t *testing.T, path, isrc, genre string) {
	t.Helper()
	cmt := flacvorbis.New()
	if isrc != "" {
		if err := cmt.Add("ISRC", isrc); err != nil {
			t.Fatalf("add ISRC comment: %v", err)
		}
	}
	if genre != "" {
		if err := cmt.Add("GENRE", genre); err != nil {
			t.Fatalf("add GENRE comment: %v", err)
		}
	}
	block := cmt.Marshal()
	f := &flac.File{Meta: []*flac.MetaDataBlock{&block}, Frames: []byte{0xFF, 0xF8}}
	if err := os.WriteFile(path, f.Marshal(), 0644); err != nil {
		t.Fatalf("write test FLAC: %v", err)
	}
}

// newTestAuthManager builds an AuthManager on a throwaway BoltDB.
//
// internal/auth has its own copy, for the same reason as the two helpers above:
// test scaffolding cannot be shared across packages without exporting it.
func newTestAuthManager(t *testing.T) *auth.AuthManager {
	t.Helper()
	f, err := os.CreateTemp("", "spotiflac-test-*.db")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	database, err := bolt.Open(f.Name(), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	am, err := auth.NewAuthManager(database)
	if err != nil {
		t.Fatalf("NewAuthManager: %v", err)
	}
	return am
}

// newTestJobManager creates a JobManager backed by a temp BoltDB and,
// when withCatalog is true, a temp SQLite catalog. Mirrors
// newTestAuthManager (api_keys_test.go)'s pattern.
// The handles the most recent newTestJobManagerSink built. Package-level, so
// these tests must not run in parallel — none call t.Parallel, and the
// alternative (threading two more return values through three helpers) buys
// nothing here. A Watcher now holds
// its own db/catalog rather than borrowing the job manager's, so a test that
// wants one has to be given the same two — see newTestWatcher.
// lastTestCatalog is the catalog handle of the most recent newTestJobManagerSink.
// Package-level, so these tests must not run in parallel — none call t.Parallel.
// api_admin_test.go needs it to seed the catalog behind a manager it did not
// build itself.
var lastTestCatalog *sql.DB

func newTestJobManager(t *testing.T, withCatalog bool) *jobs.JobManager {
	t.Helper()
	return newTestJobManagerSink(t, withCatalog, nil)
}

// newTestJobManagerSink builds the manager with an explicit sink. nil is a
// supported value: a test that never inspects events does not need a transport.
func newTestJobManagerSink(t *testing.T, withCatalog bool, hub jobs.EventSink) *jobs.JobManager {
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
		catalogHandle, err := db.Open(t.TempDir())
		if err != nil {
			t.Fatalf("db.Open: %v", err)
		}
		t.Cleanup(func() { catalogHandle.Close() })
		catalog = catalogHandle
	}

	lastTestCatalog = catalog

	jm, err := jobs.NewJobManager(t.TempDir(), boltDB, catalog, hub)
	if err != nil {
		t.Fatalf("jobs.NewJobManager: %v", err)
	}
	t.Cleanup(jm.Close)
	return jm
}

// newTestJobManagerWithHub is newTestJobManager for the tests that assert on
// emitted events. The hub is returned rather than reachable through the manager
// because the manager no longer owns it: it holds an EventSink, which has no
// subscribe. Handing back the concrete hub is the whole point of the split —
// a test that wants to observe events says so, instead of reaching into a
// field.
func newTestJobManagerWithHub(t *testing.T, withCatalog bool) (*jobs.JobManager, *SSEHub) {
	t.Helper()
	hub := newSSEHub()
	return newTestJobManagerSink(t, withCatalog, hub), hub
}
