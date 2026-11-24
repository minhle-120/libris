package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/index"
	"libris/internal/llm"
)

func recordingServer(t *testing.T, response string, seen *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// installed answers the only question managed mode asks about a reranker: is the file on this
// disk? Tests that do not care answer true; the one that cares says no.
func installed(onDisk bool) func() bool {
	return func() bool { return onDisk }
}

// Managed mode with the port on Auto has no address until something is running: the number
// comes from the kernel at start and is in no settings file. Building a client anyway would
// aim a request at a port nobody holds, which fails as a socket error blaming a server that
// was never asked — the mistake libris already made once about an embedding base URL.
func TestNothingIsDialedForAServerThatIsNotRunning(t *testing.T) {
	cfg := config.Default() // managed, and the port is Auto: nothing stored names a server

	var dark clientCache
	if _, err := dark.For(cfg); !errors.Is(err, app.ErrEngineDown) {
		t.Fatalf("client for a router that is not running: %v, want ErrEngineDown", err)
	}

	lit := clientCache{servePort: func() int { return 41234 }}
	if _, err := lit.For(cfg); err != nil {
		t.Fatalf("client for a running router: %v", err)
	}
	// The same fact reaches a question through the cache, so nothing else has to be told:
	// one place answers "where does a request go", including for reranking.
	if _, err := modelSource(&lit, index.NewSupplier(lit.For, testLogger(), nil), installed(false))(cfg); err != nil {
		t.Fatalf("models for a running router: %v", err)
	}

	// The question sees the same answer, since a failed question deserves a sentence rather
	// than a port number.
	var clients clientCache
	supplier := index.NewSupplier(clients.For, testLogger(), nil)
	if _, err := modelSource(&clients, supplier, installed(false))(cfg); !errors.Is(err, app.ErrEngineDown) {
		t.Fatalf("models for a router that is not running: %v, want ErrEngineDown", err)
	}
}

func configured(t *testing.T) (config.Config, *clientCache, *index.Supplier) {
	t.Helper()

	var embedBody, rerankBody []byte
	embed := recordingServer(t, `{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"eg"}`, &embedBody)
	rerank := recordingServer(t, `{"results":[{"index":0,"relevance_score":0.5}]}`, &rerankBody)
	var clients clientCache
	supplier := index.NewSupplier(clients.For, testLogger(), nil)

	cfg := config.Default()
	// External mode: these tests name the servers and the model names on purpose, which is
	// exactly what managed mode derives instead (ADR 0020).
	cfg.EngineMode = config.EngineExternal
	cfg.ChatURL = "http://127.0.0.1:1" // never dialled by these tests
	cfg.EmbedURL = embed.URL
	cfg.EmbedModel = "embeddinggemma-2"
	cfg.EmbedDim = "3"
	cfg.RerankURL = rerank.URL
	cfg.RerankEnabled = true
	cfg.RerankModel = "bge-reranker-v2-m3"

	return cfg, &clients, supplier
}

// The reranker name the user typed is what the server is told. Sent without it, the
// setting is decoration: the UI would say one reranker is scoring the corpus while
// the server answers as whatever it loaded, and no response would ever disagree.
func TestModelsPassTheRerankerName(t *testing.T) {
	var rerankBody []byte
	rerank := recordingServer(t, `{"results":[{"index":0,"relevance_score":0.5}]}`, &rerankBody)

	cfg, clients, supplier := configured(t)
	cfg.RerankURL = rerank.URL

	m, err := modelSource(clients, supplier, installed(true))(cfg)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if m.Rerank == nil {
		t.Fatal("rerank is nil, want the configured reranker wired in")
	}

	if _, err := m.Rerank.Score(context.Background(), "what did he measure?",
		[]llm.RerankDoc{llm.TextDoc("a passage")}); err != nil {
		t.Fatalf("Score: %v", err)
	}

	var sent struct {
		Model     string `json:"model"`
		Query     string `json:"query"`
		Documents []any  `json:"documents"`
	}
	if err := json.Unmarshal(rerankBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "bge-reranker-v2-m3" {
		t.Errorf("request model = %q, want the reranker from Settings", sent.Model)
	}
	if sent.Query != "what did he measure?" || len(sent.Documents) != 1 {
		t.Errorf("request = %+v", sent)
	}
}

// Reranking is off in three different ways — the switch is off, no server is set, or managed
// mode has no reranker on disk — and all of them mean vector order rather than an error on
// every question.
func TestModelsRerankIsOptional(t *testing.T) {
	cases := map[string]struct {
		mut      func(*config.Config)
		reranker bool // whether a reranker is on this disk
	}{
		"switch off": {func(c *config.Config) { c.RerankEnabled = false }, true},
		"no server":  {func(c *config.Config) { c.RerankURL = "" }, true},
		"both absent": {func(c *config.Config) {
			c.RerankURL = ""
			c.RerankEnabled = false
		}, true},
		// In managed mode the address is derived from the port, so it is never blank and
		// the setting is never what stands in the way — only the file that is not there.
		"managed, nothing installed": {
			func(c *config.Config) { c.EngineMode = config.EngineManaged }, false,
		},
	}
	for name, tc := range cases {
		cfg, clients, supplier := configured(t)
		tc.mut(&cfg)

		if cfg.EngineMode == config.EngineManaged {
			// Managed mode asks what the running process holds, and under Auto that number is
			// nowhere in the settings. This case is about a reranker that is not on disk, so the
			// router is simply said to be up — on the port the cache is told to ask for.
			clients.servePort = func() int { return 41234 }
		}

		m, err := modelSource(clients, supplier, installed(tc.reranker))(cfg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.Rerank != nil {
			t.Errorf("%s: rerank wired anyway, want vector order", name)
		}
	}
}

// The probe carries the model name the vectors are keyed on, and it comes from the
// settings handed in — not from whatever was current when libris started. Two
// questions with different embeddingModel settings must not search the same space.
func TestProbeIdentityFollowsTheSettings(t *testing.T) {
	cfg, clients, supplier := configured(t)

	first, err := modelSource(clients, supplier, installed(true))(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Probe == nil {
		t.Fatal("probe is nil, want the configured embedding server")
	}
	if !strings.Contains(first.Probe.Identity(), "embeddinggemma-2") {
		t.Errorf("identity = %q, want the model name in it", first.Probe.Identity())
	}

	cfg.EmbedModel = "something-else"
	second, err := modelSource(clients, supplier, installed(true))(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Probe.Identity() == first.Probe.Identity() {
		t.Errorf("both probes claim %q, but the settings name two models",
			first.Probe.Identity())
	}

	// The chat model travels per request, so it is a field rather than part of any
	// connection identity.
	if first.ChatModel != cfg.ChatModel {
		t.Errorf("chat model = %q, want %q", first.ChatModel, cfg.ChatModel)
	}
}
