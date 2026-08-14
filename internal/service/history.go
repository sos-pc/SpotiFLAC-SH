package service

import (
	"bytes"
	"encoding/csv"

	"github.com/sos-pc/SpotiFLAC-SH/backend"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
)

// HistoryService groups everything about past downloads: clearing/exporting
// the live job queue (needs the JobManager) and the persistent download/fetch
// history DB (stateless backend calls). Extracted from the former App
// god-object (R3).
type HistoryService struct {
	jobs *jobs.JobManager
}

func NewHistoryService(jobs *jobs.JobManager) *HistoryService {
	return &HistoryService{jobs: jobs}
}

// These three serve the download-queue panel, which shows the caller their own
// work and nobody else's. So they act on their own work too: isAdmin is passed
// as false deliberately, and the parameter is gone from these signatures.
//
// An operator clearing every account's history is a legitimate thing to want,
// but not from a screen that shows them three rows and deletes thirty.
// JobManager keeps the capability for the administration screen that will
// need it.
func (h *HistoryService) ClearCompletedDownloads(userID string) {
	if jm := h.jobs; jm != nil {
		jm.ClearCompletedJobs(userID, false)
	}
}

func (h *HistoryService) ClearAllDownloads(userID string) {
	if jm := h.jobs; jm != nil {
		jm.ClearAllJobs(userID, false)
	}
}

// FailedDownloadsExport is what the export endpoint answers with: either a CSV
// to save, or a message to show. Exactly one is ever set.
//
// It replaces a single string carrying both meanings, told apart by an
// "EXPORT:" prefix that the client stripped with slice(7). Two fields cost
// nothing here — the response was already a JSON object — and remove a
// discriminator that lived inside the payload it was discriminating.
type FailedDownloadsExport struct {
	CSV     string `json:"csv,omitempty"`
	Message string `json:"message,omitempty"`
}

const noFailedDownloads = "No failed downloads to export"

func (h *HistoryService) ExportFailedDownloads(userID string) (FailedDownloadsExport, error) {
	jm := h.jobs
	if jm == nil {
		return FailedDownloadsExport{Message: noFailedDownloads}, nil
	}
	jobList, err := jm.GetAllJobs()
	if err != nil {
		return FailedDownloadsExport{}, err
	}
	return failedDownloadsExport(jobList, userID)
}

// failedDownloadsExport is the whole of the above except the DB read, split out
// so it can be tested against a []jobs.Job literal instead of a Bolt file.
//
// It took an isAdmin flag that let it aggregate every account's failures. The
// only caller is the download-queue panel's Export button, which now acts on
// the caller's own rows like everything else on that screen, so the flag was
// always false. An instance-wide export is a reasonable thing to want from an
// administration screen; it can grow an owner column then, which it would need
// anyway to be readable.
func failedDownloadsExport(jobList []jobs.Job, userID string) (FailedDownloadsExport, error) {
	records := [][]string{}
	for _, job := range jobList {
		if job.UserID != userID {
			continue
		}
		if job.Status != jobs.StatusFailed {
			continue
		}
		records = append(records, []string{
			job.TrackName, job.ArtistName, job.AlbumName, job.Error,
		})
	}
	if len(records) == 0 {
		return FailedDownloadsExport{Message: noFailedDownloads}, nil
	}

	var buf bytes.Buffer
	// A BOM, because this file exists to be opened in a spreadsheet: Excel on
	// Windows reads a BOM-less file as the system codepage and turns every
	// non-ASCII title into mojibake. This library is largely non-ASCII.
	buf.WriteString("\uFEFF")

	// encoding/csv rather than fmt.Sprintf("%q,%q,%q,%q"), which is what this
	// did. %q is Go's quoting, not CSV's: it escapes an embedded quote as \"
	// where CSV requires "". Engine failures quote the path they are about —
	//   "/staging/Foo.flac" is not a valid FLAC file
	// — so the Error column, the only reason to export at all, was the one
	// that came out unparseable.
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"Track", "Artist", "Album", "Error"}); err != nil {
		return FailedDownloadsExport{}, err
	}
	if err := w.WriteAll(records); err != nil {
		return FailedDownloadsExport{}, err
	}
	return FailedDownloadsExport{CSV: buf.String()}, nil
}
func (h *HistoryService) GetDownloadHistory(userID string) ([]backend.HistoryItem, error) {
	return backend.GetHistoryItems(userID)
}

func (h *HistoryService) ClearDownloadHistory(userID string) error {
	return backend.ClearHistory(userID)
}

func (h *HistoryService) DeleteDownloadHistoryItem(id string, userID string) error {
	return backend.DeleteHistoryItem(id, userID)
}

func (h *HistoryService) GetFetchHistory(userID string) ([]backend.FetchHistoryItem, error) {
	return backend.GetFetchHistoryItems(userID)
}

func (h *HistoryService) AddFetchHistory(item backend.FetchHistoryItem) error {
	return backend.AddFetchHistoryItem(item)
}

func (h *HistoryService) ClearFetchHistory(userID string) error {
	return backend.ClearFetchHistory(userID)
}

func (h *HistoryService) ClearFetchHistoryByType(itemType string, userID string) error {
	return backend.ClearFetchHistoryByType(itemType, userID)
}

func (h *HistoryService) DeleteFetchHistoryItem(id string, userID string) error {
	return backend.DeleteFetchHistoryItem(id, userID)
}
