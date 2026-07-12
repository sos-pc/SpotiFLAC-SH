package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
	catalogdb "github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
	bolt "go.etcd.io/bbolt"
)

const port = "6890"

func main() {
	// Capture stdout as early as possible so startup logs land in the
	// in-memory buffer the Debug Logs page reads from too.
	captureStdout()

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
	fmt.Printf("[Main] Config dir: %s\n", configDir)

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
		fmt.Printf("[Main] WARNING: catalog database unavailable, continuing without it (M3U8 generation falls back to filesystem/BoltDB, dedup falls back to BoltDB-only): %v\n", err)
		catalog = nil
	} else {
		defer catalog.Close()
	}

	// ── History buckets (partagés dans jobs.db) ───────────────────────────
	if err := backend.InitHistoryDBShared(db); err != nil {
		fmt.Printf("[Main] Warning: failed to init history buckets: %v\n", err)
	}

	// ── Job manager (workers + cleanup) ───────────────────────────────────
	jobs, err := NewJobManager(configDir, db, catalog)
	if err != nil {
		fprintReal("FATAL: cannot init job manager: %v\n", err)
		os.Exit(1)
	}
	defer jobs.Close()
	serverLogs.attachHub(jobs.hub)

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
		DB:      db,
		Catalog: catalog,
		Jobs:    jobs,
		Auth:    auth,
		Watcher: watcher,
	}

	// ── App + HTTP server ─────────────────────────────────────────────────
	app := NewApp(ctr)
	app.startup(context.Background())

	LoadProxyConfig(db)

	// Restore last discovery result so GetTidalProxiesEffective() is correct
	// immediately, before the first scheduled run of the discovery goroutine.
	loadSavedDiscovery(db)

	// Start background proxy auto-discovery (tidal-uptime.geeked.wtf, every 6h).
	discoveryCtx, cancelDiscovery := context.WithCancel(context.Background())
	defer cancelDiscovery()
	util.SafeGo("proxyDiscovery", func() { startProxyDiscovery(discoveryCtx, db) })

	server := NewServer(app, ctr)

	httpServer := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           server,
		ReadTimeout:       0, // pas de timeout — les downloads peuvent être longs
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second, // borne la lecture des headers (Slowloris) sans affecter les gros transferts de body
	}

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
				fmt.Printf("[PANIC] recovered in HTTP server goroutine: %v\n%s\n", r, debug.Stack())
				stop <- syscall.SIGTERM
			}
		}()
		fmt.Printf("[Main] SpotiFLAC listening on http://0.0.0.0:%s\n", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("FATAL: server error: %v\n", err)
			// Signal propre au lieu de os.Exit pour respecter les defer
			stop <- syscall.SIGTERM
		}
	}()

	<-stop
	fmt.Println("[Main] Shutting down gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)

	app.shutdown(ctx)
	fmt.Println("[Main] Bye.")
}

// getConfigDir retourne le dossier de config SpotiFLAC.
// Sous Docker : /home/nonroot/.SpotiFLAC
// En local    : ~/.SpotiFLAC
func getConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".SpotiFLAC"), nil
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
