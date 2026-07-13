package main

import (
	"database/sql"

	bolt "go.etcd.io/bbolt"
)

// Container regroupe toutes les dépendances du service layer.
// Il est câblé dans main.go et passé explicitement à App et Server.
type Container struct {
	DB      *bolt.DB // BoltDB: live queue, watchlists, users, api keys, fetch history
	Catalog *sql.DB  // SQLite: long-term track/file/playlist history (catalog.db)
	Jobs    *JobManager
	Auth    *AuthManager
	Watcher *Watcher

	// Domain services carved out of the former App god-object (R3). Each holds
	// only the dependencies it actually needs; stateless ones (System) have
	// none.
	System  *SystemService
	Media   *MediaService
	History *HistoryService
}
