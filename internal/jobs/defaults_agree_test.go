package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// A job whose stored settings are empty — a recovered pending job, or one
// enqueued before a template was ever set — must be LOOKED FOR under the same
// name it would be WRITTEN under.
//
// checkFileExists and buildDownloadRequest each substitute a default when
// FilenameTemplate is empty. They used to do it with their own copy of the
// literal "title-artist", in the same file but forty lines apart, and there
// were four more copies elsewhere: two naming cover and lyrics sidecars, one in
// the downloader, one in the file service. Six chances to disagree, and the
// symptom of disagreement is not a crash — it is every already-downloaded track
// being downloaded again, forever, because the skip check looks somewhere the
// writer never wrote.
//
// This asserts the property rather than the constant: point one of them at a
// different default and the test fails, whatever the values are.
func TestEmptySettingsLookUpMatchesWriteName(t *testing.T) {
	jm := newTestJobManager(t, false)

	job := &Job{
		ID:         "j1",
		TrackName:  "Windowlicker",
		ArtistName: "Aphex Twin",
		AlbumName:  "Windowlicker",
		Settings:   JobSettings{}, // the whole point: nothing set
	}

	outputDir := t.TempDir()

	// What the writer would use.
	req := jm.buildDownloadRequest(job, outputDir, "")
	if req.FilenameFormat == "" {
		t.Fatal("buildDownloadRequest left FilenameFormat empty for a job with no settings")
	}
	if req.FilenameFormat != util.DefaultFilenameTemplate {
		t.Errorf("write format = %q, want the shared default %q",
			req.FilenameFormat, util.DefaultFilenameTemplate)
	}

	// Create the file the writer would have produced, then ask the skip check
	// to find it. If the two defaults ever diverge, this is the assertion that
	// notices.
	written := util.BuildExpectedFilename(
		job.TrackName, job.ArtistName, job.AlbumName, job.AlbumArtist,
		job.ReleaseDate, req.FilenameFormat, job.PlaylistName, "",
		job.Settings.TrackNumber,
		util.ResolveTrackNumber(job.Position, job.TrackNumber, false),
		job.DiscNumber,
	)
	// BuildExpectedFilename already carries the extension, and checkFileExists
	// only counts a file over 100 KB — a deliberate guard against a
	// half-written download being mistaken for a finished one.
	path := filepath.Join(outputDir, written)
	if err := os.WriteFile(path, make([]byte, 200*1024), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := jm.checkFileExists(job, outputDir)
	if got == "" {
		t.Fatalf("checkFileExists did not find %q — the lookup default has drifted from the write default", path)
	}
	if filepath.Clean(got) != filepath.Clean(path) {
		t.Errorf("checkFileExists found %q, want %q", got, path)
	}
}

// The service default has the same shape of duplication: internal/jobs guards a
// stored job, backend.Download guards a request an internal caller built. They
// are not redundant, but they must agree — a job that stored no service would
// otherwise be routed to one provider and have its file named by another's
// conventions.
func TestEmptySettingsServiceDefault(t *testing.T) {
	jm := newTestJobManager(t, false)

	job := &Job{
		ID: "j2", TrackName: "t", ArtistName: "a",
		Settings: JobSettings{},
	}
	req := jm.buildDownloadRequest(job, t.TempDir(), "")
	if req.Service != util.DefaultService {
		t.Errorf("service = %q, want the shared default %q", req.Service, util.DefaultService)
	}
}
