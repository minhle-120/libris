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
