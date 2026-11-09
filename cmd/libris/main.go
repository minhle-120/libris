// Command libris serves the app: the embedded UI, the settings API, and (later)
// ingest, retrieval and MCP.
package main

import (
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"libris/internal/config"
	"libris/internal/httpapi"
	"libris/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address; loopback only unless you mean otherwise")
	dataDir := flag.String("data", defaultDataDir(), "directory for settings and derived files")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Environment provides defaults; the settings file overrides them.
	base, err := config.Load()
	if err != nil {
		fail(log, "load config", err)
	}

	store, err := config.NewFileStore(filepath.Join(*dataDir, "settings.json"), base)
	if err != nil {
		fail(log, "open settings", err)
	}

	assets, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		fail(log, "locate embedded frontend", err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.New(httpapi.Options{Settings: store, Assets: assets, Logger: log}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("libris listening", "addr", "http://"+*addr, "data", *dataDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fail(log, "serve", err)
	}
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
