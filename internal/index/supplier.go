package index

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"libris/internal/config"
	"libris/internal/llm"
)

// probeTimeout bounds a startup probe. Long enough that a cold model still
// loading gets a chance, short enough that a wrong URL is a log line rather than
// a stalled upload.
var probeTimeout = 20 * time.Second

// Supplier hands out an embedder for the settings currently in force.
//
// It exists because the embedding dimension is not something the user types: it
// belongs to whichever server the settings point at. So the first time a Supplier
// sees a URL/model pair it asks that server how long its vectors are, records the
// answer through onProbe (which writes it into the settings the UI shows), and
// from then on rejects any vector that length. A second server that disagrees gets
// probed too — enforcing the old server's length against the new one would refuse
// perfectly good vectors.
//
// A probe that fails does not stop ingest. The embedder still runs with no
// enforced length, and the model name is recorded with every vector, so a mixed
// store can still be identified and rebuilt.
type Supplier struct {
	client  *llm.Client
	log     *slog.Logger
	onProbe func(dim int) // may be nil

	mu     sync.Mutex
	probed map[string]int // "url\x00model" -> dimension
}

// NewSupplier builds embedders over one client. onProbe, if given, is called with
// the dimension of a newly seen server.
func NewSupplier(client *llm.Client, log *slog.Logger, onProbe func(dim int)) *Supplier {
	return &Supplier{client: client, log: log, onProbe: onProbe, probed: map[string]int{}}
}

// For returns an embedder matching cfg. It is safe to call from several ingest
// jobs at once: only the first job for a given server pays for the probe.
func (s *Supplier) For(cfg config.Config) (*Embedder, error) {
	url, model := cfg.EmbedURL, cfg.EmbedModel

	s.mu.Lock()
	defer s.mu.Unlock()

	key := url + "\x00" + model
	dim, seen := s.probed[key]
	if !seen {
		dim = s.discover(cfg, url, model)
		s.probed[key] = dim
	}

	if want := atoi(cfg.EmbedDim); want != 0 && dim != 0 && want != dim {
		s.log.Warn("configured embedding dimensions disagree with the server",
			"model", model, "configured", want, "server", dim)
	}
	if dim == 0 {
		// Fall back to whatever the settings claim, so a server we could not
		// reach still gets a length enforced once we do reach it.
		dim = atoi(cfg.EmbedDim)
	}
	return NewEmbedder(s.client, url, model, dim), nil
}

// discover asks the server for one vector. Split out so tests do not have to wait
// behind a dead port.
func (s *Supplier) discover(cfg config.Config, url, model string) int {
	probe := NewEmbedder(s.client, url, model, 0)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	n, err := probe.Probe(ctx)
	if err != nil {
		s.log.Warn("embedding server not reachable; vectors will be accepted at any length",
			"url", url, "model", model, "err", err)
		return 0
	}

	s.log.Info("embedding server probed", "url", url, "model", model, "dimensions", n)
	if s.onProbe != nil {
		s.onProbe(n)
	}
	return n
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
