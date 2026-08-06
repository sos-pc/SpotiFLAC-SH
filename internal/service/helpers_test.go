package service

import (
	"database/sql"
	"os"
	"testing"

	"github.com/go-flac/flacvorbis"
	"github.com/go-flac/go-flac"
	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	bolt "go.etcd.io/bbolt"
)

// The fourth home for these. Each package boundary the split crossed needed its
// own copy: test scaffolding is unexported by nature, and exporting it would put
// construction helpers in the API the binary links against.

func openTestCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

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

func newTestJobManager(t *testing.T, withCatalog bool) *jobs.JobManager {
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
	return jm
}
