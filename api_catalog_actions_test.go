package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
)

// The one distinction the check-deleted pass rests on: "the file is not there" and
// "I could not tell" must not be the same answer. util.FileExists returns a
// bare bool and folds a permission error into "absent"; used here, an
// unreadable mount would be recorded as a library that lost every file.
func TestStatLibraryFileSeparatesAbsentFromUnreadable(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "track.flac")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Run("fichier présent", func(t *testing.T) {
		ok, err := statLibraryFile(present)
		if err != nil || !ok {
			t.Errorf("got (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("fichier absent", func(t *testing.T) {
		// Absent is a finding, not an error: the pass must act on it.
		ok, err := statLibraryFile(filepath.Join(dir, "gone.flac"))
		if err != nil {
			t.Errorf("absence reported as an error: %v", err)
		}
		if ok {
			t.Error("a missing file was reported as present")
		}
	})

	t.Run("un répertoire n'est pas un fichier", func(t *testing.T) {
		ok, err := statLibraryFile(dir)
		if err != nil || ok {
			t.Errorf("got (%v, %v), want (false, nil)", ok, err)
		}
	})

	t.Run("chemin vide", func(t *testing.T) {
		// An empty path cannot be checked, so it must be an error rather than
		// "absent" — otherwise a row with a blank file_path would be marked
		// missing on every pass.
		if _, err := statLibraryFile(""); err == nil {
			t.Error("an empty path was accepted as a real answer")
		}
	})
}

// seedLibraryFile creates a track + library_files row pointing at path.
func seedLibraryFile(t *testing.T, database *sql.DB, spotifyID, path string) {
	t.Helper()
	ctx := context.Background()
	if err := db.UpsertTrackStub(ctx, database, spotifyID); err != nil {
		t.Fatalf("UpsertTrackStub: %v", err)
	}
	lf := &db.LibraryFile{
		SpotifyID: spotifyID, Provider: "tidal", Quality: "LOSSLESS",
		Format: "flac", FilePath: path,
	}
	if err := db.CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}
}

// statusOf reads a row's current status straight from the table.
func statusOf(t *testing.T, database *sql.DB, spotifyID string) string {
	t.Helper()
	lf, err := db.GetActiveLibraryFile(context.Background(), database, spotifyID)
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if lf == nil {
		t.Fatalf("no active library file for %s", spotifyID)
	}
	return lf.Status
}

func callCheckDeleted(t *testing.T, s *Server, body string) checkDeletedResult {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/admin/library-check-deleted", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/v1/admin/library-check-deleted", strings.NewReader(body))
	}
	r = r.WithContext(context.WithValue(r.Context(), contextKeyUser,
		&JWTClaims{UserID: "u1", IsAdmin: true}))
	rec := httptest.NewRecorder()
	s.v1CheckDeletedFiles(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out checkDeletedResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// Prod could not verify this: all 2589 rows are present, so a dry run and an
// apply produce identical output there. This exercises the part that only
// shows itself when a file is actually gone.
func TestCheckDeletedDetectsAndOnlyWritesWhenApplied(t *testing.T) {
	database := openTestCatalogDB(t)
	s := &Server{ctr: &Container{Catalog: database}}
	dir := t.TempDir()

	kept := filepath.Join(dir, "kept.flac")
	if err := os.WriteFile(kept, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	vanished := filepath.Join(dir, "vanished.flac")
	if err := os.WriteFile(vanished, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	seedLibraryFile(t, database, "spotify:track:kept", kept)
	seedLibraryFile(t, database, "spotify:track:vanished", vanished)

	// The file disappears from disk behind the catalog's back — the exact
	// situation nothing could detect before this action existed.
	if err := os.Remove(vanished); err != nil {
		t.Fatalf("remove: %v", err)
	}

	t.Run("la simulation détecte mais n'écrit pas", func(t *testing.T) {
		got := callCheckDeleted(t, s, "")
		if got.Applied {
			t.Error("applied = true on an empty body; the safe default must be a dry run")
		}
		if got.Checked != 2 || got.WentMissing != 1 || got.Unchanged != 1 {
			t.Errorf("checked=%d went_missing=%d unchanged=%d, want 2/1/1",
				got.Checked, got.WentMissing, got.Unchanged)
		}
		if len(got.MissingSample) != 1 || got.MissingSample[0] != vanished {
			t.Errorf("missing_sample = %v, want [%s]", got.MissingSample, vanished)
		}
		// The point of a dry run: the table is untouched.
		if s := statusOf(t, database, "spotify:track:vanished"); s != db.StatusPresent {
			t.Errorf("the dry run wrote to the table: status = %q, want still %q", s, db.StatusPresent)
		}
	})

	t.Run("apply écrit", func(t *testing.T) {
		got := callCheckDeleted(t, s, `{"apply": true}`)
		if !got.Applied || got.WentMissing != 1 {
			t.Fatalf("applied=%v went_missing=%d", got.Applied, got.WentMissing)
		}
		if s := statusOf(t, database, "spotify:track:vanished"); s != db.StatusMissing {
			t.Errorf("status = %q, want %q", s, db.StatusMissing)
		}
		// The file that is still there must not have been touched.
		if s := statusOf(t, database, "spotify:track:kept"); s != db.StatusPresent {
			t.Errorf("a present file was changed to %q", s)
		}
	})

	t.Run("une seconde passe ne recompte pas", func(t *testing.T) {
		// Already marked missing, so it is now "unchanged" — the pass must be
		// idempotent, or a scheduled run would report the same loss forever.
		got := callCheckDeleted(t, s, `{"apply": true}`)
		if got.WentMissing != 0 || got.Unchanged != 2 {
			t.Errorf("went_missing=%d unchanged=%d, want 0/2", got.WentMissing, got.Unchanged)
		}
	})

	t.Run("le fichier revient", func(t *testing.T) {
		if err := os.WriteFile(vanished, []byte("x"), 0o644); err != nil {
			t.Fatalf("restore: %v", err)
		}
		got := callCheckDeleted(t, s, `{"apply": true}`)
		if got.CameBack != 1 {
			t.Errorf("came_back = %d, want 1", got.CameBack)
		}
		if s := statusOf(t, database, "spotify:track:vanished"); s != db.StatusPresent {
			t.Errorf("status = %q, want %q", s, db.StatusPresent)
		}
	})
}

// The repair half of the pair. check-deleted marks a row missing; this one is
// the only thing that can bring the file back, because a watchlist sync drops
// every track already in knownIDs before reaching the enqueue path — and the
// enqueue path is the only place that stats the disk.
//
// The enqueue itself needs a live JobManager (BoltDB and workers), so this
// covers the selection and the dry-run gate, which is everything that happens
// before EnqueueBatch is reached.
func TestRedownloadMissingSelectsOnlyMissingAndRespectsDryRun(t *testing.T) {
	database := openTestCatalogDB(t)
	// A non-nil JobManager satisfies the handler's guard; the dry-run path
	// returns before ever using it.
	s := &Server{ctr: &Container{Catalog: database, Jobs: &JobManager{}}}
	ctx := context.Background()
	dir := t.TempDir()

	present := filepath.Join(dir, "here.flac")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	seedLibraryFile(t, database, "spotify:track:here", present)
	seedLibraryFile(t, database, "spotify:track:gone", filepath.Join(dir, "gone.flac"))

	// Give the missing one real metadata, so the handler can describe a
	// download for it.
	if err := db.UpsertTrack(ctx, database, &db.Track{
		SpotifyID: "spotify:track:gone", Name: "Even When The Water's Cold",
		ArtistName: "!!!", AlbumName: "Thr!!!er", TrackNumber: 1, DurationMs: 1000,
	}); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}

	t.Run("rien n'est missing tant qu'on n'a pas vérifié", func(t *testing.T) {
		// Both rows were created "present": nothing has checked disk yet.
		ids, err := db.ListMissingSpotifyIDs(ctx, database)
		if err != nil {
			t.Fatalf("ListMissingSpotifyIDs: %v", err)
		}
		if len(ids) != 0 {
			t.Errorf("got %v, want none — no check has run yet", ids)
		}
	})

	// Now run the detector, which is the real prerequisite.
	if got := callCheckDeleted(t, s, `{"apply": true}`); got.WentMissing != 1 {
		t.Fatalf("check-deleted found %d missing, want 1", got.WentMissing)
	}

	t.Run("seule la piste manquante est sélectionnée", func(t *testing.T) {
		ids, err := db.ListMissingSpotifyIDs(ctx, database)
		if err != nil {
			t.Fatalf("ListMissingSpotifyIDs: %v", err)
		}
		if len(ids) != 1 || ids[0] != "spotify:track:gone" {
			t.Errorf("got %v, want [spotify:track:gone] — the present file must not be requeued", ids)
		}
	})

	t.Run("la simulation ne met rien en file", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/library-redownload-missing", nil)
		r = r.WithContext(context.WithValue(r.Context(), contextKeyUser,
			&JWTClaims{UserID: "u1", IsAdmin: true}))
		rec := httptest.NewRecorder()
		s.v1RedownloadMissing(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var got redownloadMissingResult
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Applied {
			t.Error("applied = true on an empty body")
		}
		if got.Missing != 1 || got.Queued != 0 {
			t.Errorf("missing=%d queued=%d, want 1/0", got.Missing, got.Queued)
		}
		if len(got.Tracks) != 1 || !strings.Contains(got.Tracks[0], "Water") {
			t.Errorf("tracks = %v, want the missing track named", got.Tracks)
		}
	})
}
