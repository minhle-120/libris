// Command libris serves the app: the embedded UI, the settings API, ingest, and
// (later) retrieval and MCP.
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/engine"
	"libris/internal/httpapi"
	"libris/internal/index"
	"libris/internal/ingest"
	"libris/internal/models"
	"libris/internal/store/sqlite"
	"libris/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address; loopback only unless you mean otherwise")
	dataDir := flag.String("data", defaultDataDir(),
		"directory for the database, originals and derived files; defaults to Documents/libris")
	openUI := flag.Bool("open", true,
		"show the address in a browser once the port is listening; -open=false for a headless run")
	showVersion := flag.Bool("version", false,
		"print the revision this binary was built from, and exit")
	flag.Parse()

	if *showVersion {
		writeVersion(os.Stdout)
		return
	}

	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Bound before anything is opened, so that a port someone else holds — nearly
	// always another libris — fails here rather than after the database has been
	// opened, the migrations run and the ingest worker started.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fail(log, "listen", err)
	}

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
	// A file written by an older build may name settings this one no longer has —
	// they were ignored, not fatal. Say which, so the user is not hunting for a
	// setting that quietly stopped existing.
	if dropped := settings.Dropped(); len(dropped) > 0 {
		log.Warn("settings.json holds settings this version does not use; they will be dropped on the next save",
			"keys", strings.Join(dropped, ", "))
	}

	db, err := sqlite.Open(filepath.Join(*dataDir, "libris.db"))
	if err != nil {
		fail(log, "open database", err)
	}
	defer db.Close()

	// One client for chat, embeddings and reranking: libris talks to llama.cpp
	// servers that speak the OpenAI wire format. Read from the settings store
	// rather than the environment-only base, so a change saved through the UI
	// takes effect on the next call rather than the next restart.
	// One client for chat, embeddings and reranking, rebuilt whenever the settings
	// that decide how libris connects stop matching the one it built. Resolved per
	// job rather than once at boot: a URL or API key saved through the UI has to
	// reach the pipeline on the next upload, and a client frozen at startup makes
	// the failure text name a server libris is no longer configured to use.
	// The process that would answer is built before the client that dials it, because under
	// Auto the port is whatever that process was given at start: the client has to ask it, and
	// it cannot ask something that does not exist yet.
	serve := engine.NewSupervisor(filepath.Join(*dataDir, "bin"), engine.Seams{Logger: log})
	servePort := func() int { return serve.Status().Port }

	clients := clientCache{servePort: servePort}
	warmClient(log, &clients, settings.Get())

	// The embedding server is probed the first time it is needed, and its vector
	// length is written back into the settings so the UI shows what the store is
	// keyed on. Re-pointing the settings at a different server re-probes it.
	embedders := index.NewSupplier(clients.For, log, func(dim int) {
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
	// The machine itself: which llama.cpp build is on this disk, and what the box has to run
	// one on. It reads settings per call, so changing the build selection takes effect on the
	// next check rather than the next restart — the same discipline as every other cached
	// thing in this process.
	modelsDir := models.Dir(*dataDir)
	host, err := app.NewHost(app.HostOptions{
		Engine:      engine.Open(filepath.Join(*dataDir, "bin"), log),
		Serve:       serve,
		ModelsDir:   modelsDir,
		PresetPath:  engine.PresetPath(*dataDir, engine.RouterID),
		Downloads:   models.NewFetcher(modelsDir, models.Seams{}),
		Settings:    settings,
		DeviceCache: filepath.Join(*dataDir, "devices.json"),
		Logger:      log,
	})
	if err == nil {
		// Managed mode means libris runs the servers, so the question is asked here rather
		// than in the answer path: a chat that fails with "connection refused" because nobody
		// pressed a Start button is not a local-first app. Everything about the decision is
		// the supervisor's (installed? models? already up?), and a failure is a log line and a
		// settings page that says why — never a reason not to serve the UI.
		go host.EngineAutostart(context.Background())
	}

	if err != nil {
		// Not fatal: the app answers questions without knowing what its own machine has. The
		// settings page loses the download section and says so, rather than the app refusing
		// to start on a machine where something in this feature is misconfigured.
		log.Error("engine manager unavailable; the settings page will not offer downloads", "err", err)
		host = nil
	}

	service, err := app.New(app.Options{
		Store:    db,
		Settings: settings,
		Processor: func(cfg config.Config, sink ingest.Sink) (app.Ingestor, error) {
			return ingest.New(cfg, sink)
		},
		// Likewise the embedder: a job embeds against the server configured when
		// that job ran, not the one that was set when libris booted.
		Embedder: servedEmbedder(&clients, embedders),
		// The answer path. Chats and Search are the same database the sources live
		// in — one process owns the directory — and the models are resolved per
		// question for the same reason the embedder is.
		Chats:  db,
		Search: db,
		Models: modelSource(&clients, embedders, func() bool {
			return models.HasRole(models.Statuses(modelsDir), models.RoleRerank)
		}),
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
		Addr: *addr,
		Handler: httpapi.New(httpapi.Options{
			Settings: settings,
			App:      service,
			Host:     host,
			Assets:   assets,
			Logger:   log,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Serve on the listener that was already bound: the backlog has been accepting
	// since startup, so a browser that arrives during the launch loses a request to
	// nobody, and nothing has to poll for a port it already owns.
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fail(log, "serve", err)
		}
	}()

	url := browseURL(ln.Addr())
	log.Info("libris listening", "url", url, "data", *dataDir)

	if *openUI {
		showInBrowser(log, url)
	}

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

	// The router is a process, not a connection, so an HTTP shutdown does not reach it: a
	// libris that exits with llama-server holding a GPU leaves an orphan the settings page
	// cannot stop and the next start cannot claim — a cost of owning the process rather than
	// borrowing one. Stop runs after srv.Shutdown, because an ask still streaming would write
	// to a store that is about to close. Stop waits for the group, children included.
	if host != nil {
		if err := host.EngineServeStop(shutdown); err != nil {
			log.Warn("engine stop", "err", err)
		}
	}
	service.Close()
}

// warmClient builds the client the first job would use, so a settings file that cannot
// produce one is found out here rather than inside an upload.
//
// Managed mode with nothing running is not that discovery — it is what a first run looks
// like, before an engine or a model has been downloaded — so it is said out loud and boot
// continues. Refusing to boot because a server a person has not started yet is not running
// would be a strange way to introduce the app.
func warmClient(log *slog.Logger, clients *clientCache, cfg config.Config) {
	if _, err := clients.For(cfg); err != nil {
		if errors.Is(err, app.ErrEngineDown) {
			log.Info("no model server is running yet; ask one from Settings")
			return
		}
		fail(log, "llm client", err)
	}
}

func fail(log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	os.Exit(1)
}
