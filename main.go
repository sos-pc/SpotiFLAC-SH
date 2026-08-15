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
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	"github.com/sos-pc/SpotiFLAC-SH/internal/applog"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	"github.com/sos-pc/SpotiFLAC-SH/internal/service"
	"github.com/sos-pc/SpotiFLAC-SH/internal/settings"
	"github.com/sos-pc/SpotiFLAC-SH/internal/watcher"
	bolt "go.etcd.io/bbolt"
)

const port = "6890"

func main() {
	// Capture stdout as early as possible so startup logs land in the
	// in-memory buffer the Debug Logs page reads from too.
	applog.CaptureStdout()
	applog.InitLogger()

	// ── Config dir ────────────────────────────────────────────────────────
	configDir, err := util.AppDir()
	if err != nil {
		applog.FprintReal("FATAL: cannot determine config dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		applog.FprintReal("FATAL: cannot create config dir: %v\n", err)
		printPermissionHintIfNeeded(err, configDir)
		os.Exit(1)
	}
	slog.Info("[Main] Config dir", "path", configDir)

	// ── BoltDB ────────────────────────────────────────────────────────────
	dbPath := filepath.Join(configDir, jobs.DBFile)
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		applog.FprintReal("FATAL: cannot open database: %v\n", err)
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

	// ── Retired buckets ───────────────────────────────────────────────────
	if err := dropRetiredBuckets(db); err != nil {
		slog.Warn("[Main] failed to drop retired buckets", "err", err)
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
	jobMgr, err := jobs.NewJobManager(configDir, db, catalog, sseHub)
	if err != nil {
		applog.FprintReal("FATAL: cannot init job manager: %v\n", err)
		os.Exit(1)
	}
	defer jobMgr.Close()
	applog.ServerLogs.AttachSink(sseHub)

	// ── Auth (Jellyfin + JWT) ─────────────────────────────────────────────
	auth, err := auth.NewAuthManager(db)
	if err != nil {
		applog.FprintReal("FATAL: cannot init auth: %v\n", err)
		os.Exit(1)
	}

	// Instance-scoped settings used to live in whatever user profile happened
	// to save them; this moves them where they belong, once. Not fatal on
	// error — a deployment that cannot migrate should still start, and the
	// layered read falls back to defaults — but loud, because the failure mode
	// it prevents is silent: M3U8 files quietly stop being written where
	// Jellyfin reads them.
	if err := settings.PromoteInstanceSettings(auth); err != nil {
		slog.Error("[Settings] Scope migration failed; instance settings may be missing", "err", err)
	}

	// ── Watcher (playlist sync) ───────────────────────────────────────────
	wtch := watcher.NewWatcher(db, catalog, jobMgr, auth)
	defer wtch.Close()

	// Connecter le Watcher comme handler d'événements du JobManager
	jobMgr.SetEventHandler(wtch)

	// ── Container (DI) ───────────────────────────────────────────────────
	ctr := &Container{
		DB:       db,
		Catalog:  catalog,
		Jobs:     jobMgr,
		Auth:     auth,
		Watcher:  wtch,
		SSE:      sseHub,
		System:   &service.SystemService{},
		Media:    &service.MediaService{},
		History:  service.NewHistoryService(jobMgr),
		Audio:    &service.AudioService{},
		Metadata: service.NewMetadataService(auth),
		Download: service.NewDownloadService(jobMgr, auth),
	}
	ctr.Files = service.NewFileService(catalog, jobMgr)

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
	applog.FprintReal("HINT: this container runs as a non-root user (uid 1000, see 'user: \"1000:1000\"' in docker-compose.yaml).\n")
	applog.FprintReal("      The host directory mounted at %s must be owned by that same uid. On the host, run:\n", configDir)
	applog.FprintReal("      sudo chown -R 1000:1000 /path/to/your/host/config/directory\n")
}

// retiredBuckets are BoltDB buckets no code opens any more.
//
// spotify_tokens held one OAuth refresh token per account, for the per-account
// Spotify connection removed in #92. Nothing reads them now, and a credential
// nobody reads is still a credential: it stays valid at Spotify until it is
// revoked there, and it sits in a file that gets copied into backups. Dropping
// the bucket is the difference between "the feature is gone" and "the feature
// is gone and so are its secrets".
var retiredBuckets = [][]byte{
	[]byte("spotify_tokens"),
}

// dropRetiredBuckets deletes them, once. Idempotent: bolt.ErrBucketNotFound on
// a second run is the expected outcome, not a failure.
func dropRetiredBuckets(db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		for _, name := range retiredBuckets {
			if tx.Bucket(name) == nil {
				continue
			}
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
			slog.Info("[Main] Dropped a retired bucket", "bucket", string(name))
		}
		return nil
	})
}
