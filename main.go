package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend"
	catalogdb "github.com/sos-pc/SpotiFLAC-SH/backend/db"
	"github.com/sos-pc/SpotiFLAC-SH/backend/isrclookup"
	bolt "go.etcd.io/bbolt"
)

const port = "6890"

func main() {
	// Capture stdout as early as possible so startup logs land in the
	// in-memory buffer the Debug Logs page reads from too.
	captureStdout()
	initLogger()

	// ── Config dir ────────────────────────────────────────────────────────
	configDir, err := getConfigDir()
	if err != nil {
		fprintReal("FATAL: cannot determine config dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		fprintReal("FATAL: cannot create config dir: %v\n", err)
		printPermissionHintIfNeeded(err, configDir)
		os.Exit(1)
	}
	slog.Info("[Main] Config dir", "path", configDir)

	// ── BoltDB ────────────────────────────────────────────────────────────
	dbPath := filepath.Join(configDir, dbFile)
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		fprintReal("FATAL: cannot open database: %v\n", err)
		printPermissionHintIfNeeded(err, configDir)
		os.Exit(1)
	}
	defer db.Close()

	// ── SQLite catalog ────────────────────────────────────────────────────
	// Long-term store for tracks/library_files/download_attempts/snapshots.
	// Survives BoltDB cleanup; source of truth for M3U8 generation once
	// populated. Deliberately non-fatal: the catalog is additive (BoltDB
	// still drives the live queue and watchlists), and every call site
	// (jobs_catalog.go, watcher_catalog.go, api_admin.go) already nil-checks
	// it and degrades gracefully — a permission/disk/WAL-corruption issue on
	// this one file shouldn't take down core Spotify->FLAC downloading.
	catalog, err := catalogdb.Open(configDir)
	if err != nil {
		slog.Warn("[Main] catalog database unavailable, continuing without it (M3U8 generation falls back to filesystem/BoltDB, dedup falls back to BoltDB-only)", "err", err)
		catalog = nil
	} else {
		defer catalog.Close()
	}

	// ── History buckets (partagés dans jobs.db) ───────────────────────────
	if err := backend.InitHistoryDBShared(db); err != nil {
		slog.Warn("[Main] failed to init history buckets", "err", err)
	}

	// ── ISRC cache bucket (partagé dans jobs.db) ──────────────────────────
	if err := isrclookup.InitCacheDB(db); err != nil {
		slog.Warn("[Main] failed to init ISRC cache bucket", "err", err)
	}

	// ── SSE hub ───────────────────────────────────────────────────────────
	// Owned here rather than inside the job manager: three different things
	// need it, each a different half. The manager only ever publishes, so it
	// receives it as an EventSink; the log buffer and the shutdown hook need the
	// concrete type for attachHub and closeAll. Constructing it in the manager
	// forced everyone to reach back through `jobs.hub`, and made the job layer
	// depend on its own transport.
	sseHub := newSSEHub()

	// ── Job manager (workers + cleanup) ───────────────────────────────────
	jobs, err := NewJobManager(configDir, db, catalog, sseHub)
	if err != nil {
		fprintReal("FATAL: cannot init job manager: %v\n", err)
		os.Exit(1)
	}
	defer jobs.Close()
	serverLogs.attachHub(sseHub)

	// ── Auth (Jellyfin + JWT) ─────────────────────────────────────────────
	auth, err := NewAuthManager(db)
	if err != nil {
		fprintReal("FATAL: cannot init auth: %v\n", err)
		os.Exit(1)
	}

	// ── Watcher (playlist sync) ───────────────────────────────────────────
	watcher := NewWatcher(jobs, auth)
	defer watcher.Close()

	// Connecter le Watcher comme handler d'événements du JobManager
	jobs.SetEventHandler(watcher)

	// ── Container (DI) ───────────────────────────────────────────────────
	ctr := &Container{
		DB:       db,
		Catalog:  catalog,
		Jobs:     jobs,
		Auth:     auth,
		Watcher:  watcher,
		SSE:      sseHub,
		System:   &SystemService{},
		Media:    &MediaService{},
		History:  NewHistoryService(jobs),
		Audio:    &AudioService{},
		Metadata: NewMetadataService(auth),
		Download: NewDownloadService(jobs, auth),
	}
	ctr.Files = NewFileService(catalog, jobs)

	// Proxy auto-discovery used to start here: a goroutine polling
	// tidal-uptime.geeked.wtf every 6 h to reorder the Tidal proxy list. That
	// domain has been NXDOMAIN for months, so every run failed and the merge it
	// fed never had data to merge. Removed with proxy_discovery.go; the
	// configured list LoadProxyConfig just restored is what was in use anyway.

	// No community-session refresh here any more. It signed requests for the
	// native Qobuz and Amazon downloaders, which are gone; the engine obtains its
	// own session, and the solver container now answers the engine rather than
	// us. Tidal, the one native path left, authenticates with its own token.
	server := NewServer(ctr)

	httpServer := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           server,
		ReadTimeout:       0, // pas de timeout — les downloads peuvent être longs
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second, // borne la lecture des headers (Slowloris) sans affecter les gros transferts de body
	}

	// Close live SSE streams when Shutdown starts. Without this, an open Debug
	// Logs or download-queue page keeps a streaming connection that never goes
	// idle, so Shutdown waits the full 30 s timeout every restart. Registered
	// here (not called inline) so it runs after the listeners are closed — no
	// new stream can subscribe in the gap.
	httpServer.RegisterOnShutdown(sseHub.closeAll)

	// ── Graceful shutdown ─────────────────────────────────────────────────
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		// A panic here (as opposed to ListenAndServe's normal error
		// return, already handled below) must still trigger the same
		// graceful-shutdown signal — recovering and just letting this
		// goroutine die would leave <-stop below blocked forever with the
		// process still running but no longer serving anything, which is
		// worse than a clean, restart-policy-visible exit.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[Main] PANIC recovered in HTTP server goroutine", "recover", r, "stack", string(debug.Stack()))
				stop <- syscall.SIGTERM
			}
		}()
		slog.Info("[Main] SpotiFLAC listening", "addr", "http://0.0.0.0:"+port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("[Main] server error", "err", err)
			// Signal propre au lieu de os.Exit pour respecter les defer
			stop <- syscall.SIGTERM
		}
	}()

	<-stop
	slog.Info("[Main] Shutting down gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)

	slog.Info("[Main] Bye.")
}

// printPermissionHintIfNeeded surfaces the most common cause of a
// permission-denied FATAL under Docker: when the host directory bind-mounted
// onto configDir didn't exist yet, Docker creates it as root before starting
// the container, but this image runs as non-root uid 1000 (see Dockerfile)
// and has no shell/entrypoint able to chown it. Without this hint the
// resulting Go error ("permission denied") gives no indication of the fix.
func printPermissionHintIfNeeded(err error, configDir string) {
	if !os.IsPermission(err) {
		return
	}
	fprintReal("HINT: this container runs as a non-root user (uid 1000, see 'user: \"1000:1000\"' in docker-compose.yaml).\n")
	fprintReal("      The host directory mounted at %s must be owned by that same uid. On the host, run:\n", configDir)
	fprintReal("      sudo chown -R 1000:1000 /path/to/your/host/config/directory\n")
}
