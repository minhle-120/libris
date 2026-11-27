package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/engine"
	"libris/internal/index"
	"libris/internal/llm"
	"libris/internal/serving"
)

// clientCache hands out one llm client per connection settings and rebuilds it
// when any of them change.
//
// It exists because a saved setting has to take effect on the next job, not at the
// next restart. The obvious alternative — let the client read the URLs per request
// — would put a settings dependency inside llm, which is the one package meant to
// know only the wire format. So the wiring layer keeps a client keyed by the
// settings it was built from, and everything downstream keeps taking a plain
// client.
type clientCache struct {
	mu     sync.Mutex
	key    string
	client *llm.Client

	// server is the router libris might be running. The addresses managed mode dials
	// come from that process rather than from the setting, because a port on Auto is a
	// number the kernel chose at start and the settings file never knew it. A port
	// change therefore rebuilds the client like any other address change.
	//
	// A cache built without one is a machine with no engine to run, which serving reads
	// as "nothing is answering" — the honest reading rather than a fallback address.
	server serving.Server
}

// target is what a model request should dial and name, resolved from the settings in
// force right now.
//
// It is a method rather than something buried inside For because For can only fix what
// For reads — the addresses. Anything that reads a model *name* out of a config first is
// reading the stored one, and `internal/app` hands out the settings it saved: an ingest
// came to dial the correct router with `cfg.EmbedModel`, which in managed mode is either
// blank or a name from the days of pointing libris at somebody else's server.
func (c *clientCache) target(cfg config.Config) (serving.Target, error) {
	return serving.Endpoints(c.server, cfg)
}

// servedEmbedder is the embedder factory libris hands to the app, with the derivation the
// supplier needs and does not do for itself: the supplier reads a model *name* out of the
// config it is given, while clientCache.For can only derive the address on the way past it.
// Without this, an ingest in managed mode asked the running router for `cfg.EmbedModel` — blank
// in a fresh settings file, or a name typed for a different machine once — and the router had
// no reason to recognise it. The same shape as the research call's model name, one seam further
// down.
func servedEmbedder(clients *clientCache, embedders *index.Supplier) func(config.Config) (app.Embedder, error) {
	return func(cfg config.Config) (app.Embedder, error) {
		target, err := clients.target(cfg)
		if err != nil {
			return nil, err
		}
		return embedders.For(target.Apply(cfg))
	}
}

// settingsSource is the part of the settings this file reads: the values in force right now.
// Named here rather than taken as *config.Store so the wiring does not depend on how they are
// stored — the same reason the app's own Settings interface is one method wide.
type settingsSource interface {
	Get() config.Config
}

// embedderReleaser is what the app calls when the uploads stop: a chance for the model server to
// give back what the embedding model was holding on the card, which is the memory a question's
// own model would rather have.
func embedderReleaser(clients *clientCache, settings settingsSource) func(context.Context) error {
	return modelReleaser(clients, settings, string(engine.AliasEmbed))
}

// ingestRoomFor is what the app asks before it starts reading a file: the model that answers
// questions is off the card, so the ffmpeg, poppler and embedding passes that are about to run
// have somewhere to go.
//
// It is the same exchange as the release after the uploads stop, aimed at the other model, and
// the two together are what keep one machine's memory moving to where the work is: the answering
// model out of the way while a file is being read, and the embedding model out of the way once
// nothing is being read. Nothing reloads either one — the router loads a model on the request
// that names it — so what this buys is memory and what it costs is the first answer after an
// upload, which pays for the load it would not otherwise have paid for.
func ingestRoomFor(clients *clientCache, settings settingsSource) func(context.Context) error {
	return modelReleaser(clients, settings, string(engine.AliasChat))
}

// modelReleaser is that exchange for one named model: look, then stop it if it is up.
//
// It asks before it tells. The router is asked what is resident, and the unload is sent only for
// an instance that is actually up — because "stop the thing that is already stopped" is an error
// at the wire, and a job that starts by making room would otherwise log a message about a model
// that was never loaded.
//
// External mode does nothing at all. An address a person typed is somebody else's process, and
// putting one of their models to sleep is not a thing this app should do to a server it does not
// run.
func modelReleaser(clients *clientCache, settings settingsSource, name string) func(context.Context) error {
	return func(ctx context.Context) error {
		cfg := settings.Get()
		if cfg.EngineMode != config.EngineManaged {
			return nil
		}

		// No target means no router of ours is running, which is the same outcome as releasing:
		// nothing is being held. The error is swallowed for that reason and no other — the client
		// cache and the router's own answers are reported to the caller.
		if _, err := clients.target(cfg); err != nil {
			return nil
		}

		client, err := clients.For(cfg)
		if err != nil {
			return err
		}

		residents, err := client.RouterModels(ctx)
		if err != nil {
			return err
		}
		for _, m := range residents {
			if m.Name != name {
				continue
			}
			if !m.Running() {
				return nil
			}
			return client.UnloadModel(ctx, name)
		}
		// Not in the list at all: nothing to stop, and nothing to say about it.
		return nil
	}
}

func (c *clientCache) For(cfg config.Config) (*llm.Client, error) {
	// The address a request goes to is decided here and nowhere else: serving answers it
	// from the router libris runs in managed mode, and from the settings in external.
	target, err := c.target(cfg)
	if err != nil {
		// Managed mode with nothing running. There is no address to build a client for,
		// and inventing one would produce a socket error blaming a server that was never
		// asked — the same mistake the embedding base URL made once, in a different decade.
		return nil, err
	}

	key := strings.Join([]string{
		target.ChatURL, target.EmbedURL,
		strconv.Itoa(cfg.TimeoutSeconds),
	}, "\x00")

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil && c.key == key {
		return c.client, nil
	}

	client, err := llm.New(llm.Config{
		ChatURL:  target.ChatURL,
		EmbedURL: target.EmbedURL,
		Timeout:  time.Duration(cfg.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		// Uncached: a half-saved URL must not be remembered for the rest of the
		// process's life, or the next correct save would keep using the broken one.
		return nil, err
	}

	previous := c.client
	c.key, c.client = key, client
	// The reason a user changes a URL is usually that the old server is gone, so
	// do not keep its sockets half open.
	if previous != nil {
		previous.CloseIdleConnections()
	}
	return client, nil
}
