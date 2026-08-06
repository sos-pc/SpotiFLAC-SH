package main

import (
	"database/sql"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	"github.com/sos-pc/SpotiFLAC-SH/internal/service"
	"github.com/sos-pc/SpotiFLAC-SH/internal/watcher"

	bolt "go.etcd.io/bbolt"
)

// Container regroupe toutes les dépendances du service layer.
// Il est câblé dans main.go et passé explicitement à App et Server.
type Container struct {
	DB      *bolt.DB // BoltDB: live queue, watchlists, users, api keys, fetch history
	Catalog *sql.DB  // SQLite: long-term track/file/playlist history (catalog.db)
	Jobs    *jobs.JobManager
	Auth    *auth.AuthManager
	Watcher *watcher.Watcher

	// SSE is the event transport. It sits here, beside the components that use
	// it, rather than inside JobManager: the manager only publishes and takes it
	// as an EventSink, while the stream handler needs subscribe/unsubscribe.
	// Reaching it through `Jobs.hub` made every consumer traverse an unexported
	// field of an unexported field, and tied the job layer to its own transport.
	SSE *SSEHub

	// Domain services carved out of the former App god-object (R3). Each holds
	// only the dependencies it actually needs; stateless ones (System, Media,
	// Audio) have none.
	System   *service.SystemService
	Media    *service.MediaService
	History  *service.HistoryService
	Files    *service.FileService
	Audio    *service.AudioService
	Metadata *service.MetadataService
	Download *service.DownloadService
}
