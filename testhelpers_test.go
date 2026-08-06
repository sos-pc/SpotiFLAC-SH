package main

import (
	"database/sql"
	"os"
	"testing"

	"github.com/go-flac/flacvorbis"
	"github.com/go-flac/go-flac"
	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
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
