package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/index"
	"libris/internal/llm"
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

	// servePort is where the router libris runs answers, or zero when it is not running.
	// Managed mode's addresses are derived from that rather than from the setting, because a
	// port on Auto is a number the kernel chose at start and the settings file never knew it
	// (ADR 0020). A port change therefore rebuilds the client like any other address change.
	servePort func() int
}

// For returns a client built from cfg. Only the fields that change *how libris
// connects* are part of the key: model names and the like travel per request.
// port is the live router's port, or zero. A cache built without a server to ask is treated
// as one with nothing running, which is the honest reading rather than a fallback address.
func (c *clientCache) port() int {
	if c.servePort == nil {
		return 0
	}
	return c.servePort()
}

// Served is the configuration as a model request should read it: in managed mode the three
// addresses *and* the three model names are facts about the router libris runs, and the port is
// a fact about that process (ADR 0020, ADR 0024).
//
// It is a method a caller can apply, rather than something buried inside For, because For can
// only fix what For reads — the addresses. Anything that reads a model *name* out of a config
// first is reading the stored one, and `internal/app` hands out the settings it saved: an
// ingest came to dial the correct router with `cfg.EmbedModel`, which in managed mode is either
// blank or a name from the days of pointing libris at somebody else's server.
func (c *clientCache) Served(cfg config.Config) config.Config {
	return cfg.Served(c.port())
}

// servedEmbedder is the embedder factory libris hands to the app, with the derivation the
// supplier needs and does not do for itself: the supplier reads a model *name* out of the
// config it is given, while clientCache.For can only derive the address on the way past it.
// Without this, an ingest in managed mode asked the running router for `cfg.EmbedModel` — blank
// in a fresh settings file, or a name typed for a different machine once — and the router had
// no reason to recognise it. The same shape as the research call's model name, one seam further
// down (ADR 0020).
func servedEmbedder(clients *clientCache, embedders *index.Supplier) func(config.Config) (app.Embedder, error) {
	return func(cfg config.Config) (app.Embedder, error) {
		return embedders.For(clients.Served(cfg))
	}
}

func (c *clientCache) For(cfg config.Config) (*llm.Client, error) {
	// The address a request goes to is decided here and nowhere else. In managed mode it is
	// the router libris runs; in external mode it is what the settings say (ADR 0020).
	cfg = c.Served(cfg)
	if cfg.EngineMode == config.EngineManaged && cfg.ChatURL == "" {
		// Managed mode with nothing running. There is no address to build a client for, and
		// inventing one would produce a socket error blaming a server that was never asked —
		// the same mistake the embedding base URL made once, in a different decade.
		return nil, app.ErrEngineDown
	}

	key := strings.Join([]string{
		cfg.ChatURL, cfg.EmbedURL, cfg.RerankURL,
		strconv.Itoa(cfg.TimeoutSeconds),
	}, "\x00")

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil && c.key == key {
		return c.client, nil
	}

	client, err := llm.New(llm.Config{
		ChatURL:   cfg.ChatURL,
		EmbedURL:  cfg.EmbedURL,
		RerankURL: cfg.RerankURL,
		Timeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
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
