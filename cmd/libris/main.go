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
	"strconv"
	"syscall"
	"time"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/httpapi"
	"libris/internal/index"
	"libris/internal/ingest"
	"libris/internal/llm"
	"libris/internal/store/sqlite"
	"libris/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address; loopback only unless you mean otherwise")
	dataDir := flag.String("data", defaultDataDir(),
		"directory for the database, originals and derived files; defaults to Documents/libris")
	flag.Parse()

	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	log.Info("data directory", "path", *dataDir)
	if !given["data"] {
		// Builds before this default kept everything under the OS config
		// directory. Its originals are all still there, so say where they are
		// rather than let a returning user read an empty library as a deletion.
		if old := legacyDataDir(); old != "" && old != *dataDir {
			log.Info("a previous data directory is here; move its contents or pass -data",
				"previous", old)
		}
	}

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

	// One client for chat, embeddings and reranking: libris talks to llama.cpp
	// servers that speak the OpenAI wire format, and one API key covers all
	// three. Read from the settings store rather than the environment-only base,
	// or a key saved through the UI would be ignored.
	live := settings.Get()
	llmClient, err := llm.New(llm.Config{
		ChatURL:   live.ChatURL,
		EmbedURL:  live.EmbedURL,
		RerankURL: live.RerankURL,
		APIKey:    live.APIKey,
		Timeout:   time.Duration(live.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		fail(log, "llm client", err)
	}

	// The embedding server is probed the first time it is needed, and its vector
	// length is written back into the settings so the UI shows what the store is
	// keyed on. Re-pointing the settings at a different server re-probes it.
	embedders := index.NewSupplier(llmClient, log, func(dim int) {
		next := settings.Get()
		if next.EmbedDim == strconv.Itoa(dim) {
			return
		}
		next.EmbedDim = strconv.Itoa(dim)
		if err := settings.Save(next); err != nil {
			log.Warn("could not record the probed embedding dimensions", "dimensions", dim, "err", err)
		}
	})

	// The processor is built per job from the live settings, so a DPI change
	// applies to the next upload without a restart.
	service, err := app.New(app.Options{
		Store:    db,
		Settings: settings,
		Processor: func(cfg config.Config, sink ingest.Sink) (app.Ingestor, error) {
			return ingest.New(cfg, sink)
		},
		// Likewise the embedder: a job embeds against the server configured when
		// that job ran, not the one that was set when libris booted.
		Embedder: func(cfg config.Config) (app.Embedder, error) {
			return embedders.For(cfg)
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

func fail(log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	os.Exit(1)
}
