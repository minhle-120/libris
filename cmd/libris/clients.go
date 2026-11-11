package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"libris/internal/config"
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
}

// For returns a client built from cfg. Only the fields that change *how libris
// connects* are part of the key: model names and the like travel per request.
func (c *clientCache) For(cfg config.Config) (*llm.Client, error) {
	key := strings.Join([]string{
		cfg.ChatURL, cfg.EmbedURL, cfg.RerankURL, cfg.APIKey,
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
		APIKey:    cfg.APIKey,
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
