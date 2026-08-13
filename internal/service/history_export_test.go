package service

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
)

// The export existed for a year and nobody noticed it produced a file no
// spreadsheet could read, because the rows were built with
// fmt.Sprintf("%q,%q,%q,%q"). Go's %q escapes an embedded quote as \" ; CSV
// requires "". Engine failures quote the path they are about, so the Error
// column — the only reason to run this export — was the broken one.
//
// This asserts the output parses, not that it matches some expected string: the
// property that matters is that a CSV reader gets back exactly what went in.
func TestFailedDownloadsExportSurvivesRoundTrip(t *testing.T) {
	const nastyError = `"/staging/Foo.flac" is not a valid FLAC file`

	jobList := []jobs.Job{
		{
			UserID: "u1", Status: jobs.StatusFailed,
			TrackName: `Say "Yes"`, ArtistName: "A, B & C",
			AlbumName: "メガロボックス", Error: nastyError,
		},
		{UserID: "u1", Status: jobs.StatusDone, TrackName: "fine"},
	}

	got, err := failedDownloadsExport(jobList, "u1", false)
	if err != nil {
		t.Fatalf("failedDownloadsExport: %v", err)
	}
	if got.Message != "" {
		t.Fatalf("got a message alongside a CSV: %q", got.Message)
	}

	// The BOM is deliberate (Excel on Windows), and is not part of the CSV.
	body := strings.TrimPrefix(got.CSV, "\uFEFF")
	if body == got.CSV {
		t.Error("no BOM: Excel will render the non-ASCII titles as mojibake")
	}

	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("output is not parseable CSV: %v\n%s", err, body)
	}
	if len(rows) != 2 {
		t.Fatalf("want a header and one failed row, got %d rows: %q", len(rows), rows)
	}
	if got, want := rows[0], []string{"Track", "Artist", "Album", "Error"}; !equal(got, want) {
		t.Errorf("header = %q, want %q", got, want)
	}
	want := []string{`Say "Yes"`, "A, B & C", "メガロボックス", nastyError}
	if !equal(rows[1], want) {
		t.Errorf("row = %q, want %q", rows[1], want)
	}
}

// Only the caller's own failures, unless they are an admin. The filter predates
// this change; it is asserted here because splitting the function out is exactly
// the kind of edit that could drop it silently.
func TestFailedDownloadsExportScope(t *testing.T) {
	jobList := []jobs.Job{
		{UserID: "u1", Status: jobs.StatusFailed, TrackName: "mine"},
		{UserID: "u2", Status: jobs.StatusFailed, TrackName: "theirs"},
	}

	own, err := failedDownloadsExport(jobList, "u1", false)
	if err != nil {
		t.Fatalf("failedDownloadsExport: %v", err)
	}
	if strings.Contains(own.CSV, "theirs") {
		t.Error("non-admin export leaked another user's failure")
	}

	admin, err := failedDownloadsExport(jobList, "u1", true)
	if err != nil {
		t.Fatalf("failedDownloadsExport: %v", err)
	}
	if !strings.Contains(admin.CSV, "theirs") {
		t.Error("admin export dropped another user's failure")
	}
}

// No failures means a message and no file, rather than a CSV with only a header
// that the browser would happily save as an empty download.
func TestFailedDownloadsExportEmpty(t *testing.T) {
	got, err := failedDownloadsExport([]jobs.Job{
		{UserID: "u1", Status: jobs.StatusDone, TrackName: "fine"},
	}, "u1", false)
	if err != nil {
		t.Fatalf("failedDownloadsExport: %v", err)
	}
	if got.CSV != "" {
		t.Errorf("want no CSV, got %q", got.CSV)
	}
	if got.Message != noFailedDownloads {
		t.Errorf("Message = %q, want %q", got.Message, noFailedDownloads)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
