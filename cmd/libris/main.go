// Command libris serves the app: the embedded UI, the settings API, ingest, and
// (later) retrieval and MCP.
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/httpapi"
	"libris/internal/ingest"
	"libris/internal/store/sqlite"
	"libris/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address; loopback only unless you mean otherwise")
	dataDir := flag.String("data", defaultDataDir(), "directory for the database, originals and derived files")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Ctrl-C stops a long ingest between files rather than in the middle of one.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Environment provides defaults; the settings file overrides them.
	base, err := config.Load()
	if err != nil {
		fail(log, "load config", err)
	}

	settings, err := config.NewFileStore(filepath.Join(*dataDir, "settings.json"), base)
	if err != nil {
		fail(log, "open settings", err)
	}

	db, err := sqlite.Open(filepath.Join(*dataDir, "libris.db"))
	if err != nil {
		fail(log, "open database", err)
	}
	defer db.Close()

	// The processor is built per job from the live settings, so a DPI change
	// applies to the next upload without a restart.
	service, err := app.New(app.Options{
		Store:    db,
		Settings: settings,
		Processor: func(cfg config.Config, sink ingest.Sink) (app.Ingestor, error) {
			return ingest.New(cfg, sink)
		},
		Originals: filepath.Join(*dataDir, "originals"),
		Assets:    filepath.Join(*dataDir, "assets"),
		Logger:    log,
	})
	if err != nil {
		fail(log, "wire app", err)
	}
	if err := service.Start(ctx); err != nil {
		fail(log, "start app", err)
	}

	assets, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		fail(log, "locate embedded frontend", err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.New(httpapi.Options{Settings: settings, App: service, Assets: assets, Logger: log}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fail(log, "serve", err)
		}
	}()

	log.Info("libris listening", "addr", "http://"+*addr, "data", *dataDir)

	<-ctx.Done()
	log.Info("shutting down")

	// Stop accepting requests, let in-flight ones finish, then let the ingest
	// worker finish the file it is on. Anything still queued is picked up as
	// interrupted on the next start.
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	service.Close()
}

func defaultDataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "data"
	}
	return filepath.Join(dir, "libris")
}

func fail(log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	os.Exit(1)
}
