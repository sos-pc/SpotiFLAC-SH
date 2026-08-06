package service

import (
	"fmt"
	"strings"

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

func (h *HistoryService) ClearCompletedDownloads(userID string, isAdmin bool) {
	if jm := h.jobs; jm != nil {
		jm.ClearCompletedJobs(userID, isAdmin)
	}
}

func (h *HistoryService) ClearAllDownloads(userID string, isAdmin bool) {
	if jm := h.jobs; jm != nil {
		jm.ClearAllJobs(userID, isAdmin)
	}
}

func (h *HistoryService) ExportFailedDownloads(userID string, isAdmin bool) (string, error) {
	jm := h.jobs
	if jm == nil {
		return "No failed downloads", nil
	}
	jobList, err := jm.GetAllJobs()
	if err != nil {
		return "", err
	}
	var failedItems []string
	hasFailed := false
	for _, job := range jobList {
		if !isAdmin && job.UserID != userID {
			continue
		}
		if job.Status == jobs.StatusFailed {
			hasFailed = true
			break
		}
	}
	if !hasFailed {
		return "No failed downloads to export", nil
	}
	failedItems = append(failedItems, "Track,Artist,Album,Error")
	for _, job := range jobList {
		if !isAdmin && job.UserID != userID {
			continue
		}
		if job.Status == jobs.StatusFailed {
			row := fmt.Sprintf("%q,%q,%q,%q",
				job.TrackName, job.ArtistName, job.AlbumName, job.Error)
			failedItems = append(failedItems, row)
		}
	}
	csvContent := strings.Join(failedItems, "\n")
	return "EXPORT:" + csvContent, nil
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
