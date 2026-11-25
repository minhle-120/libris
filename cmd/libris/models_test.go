package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"libris/internal/config"
	"libris/internal/engine"
	"libris/internal/index"
	"libris/internal/llm"
	"libris/internal/serving"
)

// ── what the serving model can be shown ──────────────────────────

func capsServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func capsClient(t *testing.T, url string) *llm.Client {
	t.Helper()
	c, err := llm.New(llm.Config{ChatURL: url})
	if err != nil {
		t.Fatalf("llm.New: %v", err)
	}
	return c
}

// The engine libris runs answers /props, so the question starts knowing whether the
// page images in its evidence can be shown at all — and a model that can see them keeps
// them.
func TestTheServingModelIsAskedWhatItCanBeShown(t *testing.T) {
	srv := capsServer(t, http.StatusOK,
		`{"model_alias":"libris-chat","modalities":{"vision":true,"video":false,"audio":false}}`)

	caps := chatCaps(capsClient(t, srv.URL), "libris-chat")
	if caps == nil || !caps.Vision || caps.Model != "libris-chat" {
		t.Fatalf("caps = %+v", caps)
	}
	if caps.Audio || caps.Video {
		t.Errorf("capabilities the engine denied were carried through: %+v", caps)
	}
}

// A server with no /props — one libris does not manage, or an older llama.cpp — is not
// a question that has gone wrong, and guessing "no vision" from here would withhold
// every page in the notebook from a model that could read them. nil is the answer that
// says nobody replied, and retrieve then lets the server speak for itself.
func TestNothingIsGuessedFromAServerThatDoesNotAnswer(t *testing.T) {
	for _, srv := range []*httptest.Server{
		capsServer(t, http.StatusNotFound, `{"error":{"message":"not found"}}`),
		capsServer(t, http.StatusOK, `<html>not a llama.cpp server</html>`),
		// The router answering about itself, which is what an unqualified /props is
		// worth: nothing, and least of all a denial.
		capsServer(t, http.StatusOK, `{"role":"router","model_alias":"llama-server","model_path":"none"}`),
	} {
		if caps := chatCaps(capsClient(t, srv.URL), "libris-chat"); caps != nil {
			t.Errorf("a server that never answered produced caps: %+v", caps)
		}
	}
}

// fakeRouter is the supervisor as the wiring sees it: a status, on demand.
type fakeRouter struct{ st engine.Serving }

func (f fakeRouter) Status() engine.Serving { return f.st }

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

// Managed mode with the port on Auto has no address until something is running: the number
// comes from the kernel at start and is in no settings file. Building a client anyway would
// aim a request at a port nobody holds, which fails as a socket error blaming a server that
// was never asked — the mistake libris already made once about an embedding base URL.
func TestNothingIsDialedForAServerThatIsNotRunning(t *testing.T) {
	cfg := config.Default() // managed, and the port is Auto: nothing stored names a server

	var dark clientCache
	if _, err := dark.For(cfg); !errors.Is(err, serving.ErrNotRunning) {
		t.Fatalf("client for a router that is not running: %v, want serving.ErrNotRunning", err)
	}

	lit := clientCache{server: fakeRouter{engine.Serving{Running: true, Port: 41234}}}
	if _, err := lit.For(cfg); err != nil {
		t.Fatalf("client for a running router: %v", err)
	}
	// The same fact reaches a question through the cache, so nothing else has to be told:
	// one place answers "where does a request go", for the probe and the chat call alike.
	if _, err := modelSource(&lit, index.NewSupplier(lit.For, testLogger(), nil))(cfg); err != nil {
		t.Fatalf("models for a running router: %v", err)
	}

	// The question sees the same answer, since a failed question deserves a sentence rather
	// than a port number.
	var clients clientCache
	supplier := index.NewSupplier(clients.For, testLogger(), nil)
	if _, err := modelSource(&clients, supplier)(cfg); !errors.Is(err, serving.ErrNotRunning) {
		t.Fatalf("models for a router that is not running: %v, want serving.ErrNotRunning", err)
	}
}

func configured(t *testing.T) (config.Config, *clientCache, *index.Supplier) {
	t.Helper()

	var embedBody []byte
	embed := recordingServer(t, `{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"eg"}`, &embedBody)
	var clients clientCache
	supplier := index.NewSupplier(clients.For, testLogger(), nil)

	cfg := config.Default()
	// External mode: these tests name the servers and the model names on purpose, which is
	// exactly what managed mode derives instead.
	cfg.EngineMode = config.EngineExternal
	cfg.ChatURL = "http://127.0.0.1:1" // never dialled by these tests
	cfg.EmbedURL = embed.URL
	cfg.EmbedModel = "embeddinggemma-2"
	cfg.EmbedDim = "3"

	return cfg, &clients, supplier
}

// In managed mode the embedder has to be named for the router it is dialling, not for the
// settings file. `embedModel` there is blank on a fresh install, and after a move off external
// mode it is a name from somebody else's server; the router libris runs knows its embedder as
// `libris-embed`, the preset's section name. The address is derived inside clientCache.For,
// which is precisely the trap: the name is read *upstream* of For, by the supplier, so deriving
// only the address produced an ingest that dialled the right process and asked it for a model it
// had never heard of. Identity() is the name the vectors are keyed on, so it is the wire.
func TestTheEmbedderIsNamedForTheRouterItDials(t *testing.T) {
	cfg, clients, supplier := configured(t)
	cfg.EngineMode = config.EngineManaged
	cfg.EmbedModel = "LFM3-VL-2B"
	clients.server = fakeRouter{engine.Serving{Running: true, Port: 41234}}

	got, err := servedEmbedder(clients, supplier)(cfg)
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}
	ident, ok := got.(interface{ Identity() string })
	if !ok {
		t.Fatal("the embedder reports no identity, so nothing keys the vectors it returns")
	}
	if !strings.Contains(ident.Identity(), "libris-embed") {
		t.Errorf("identity = %q, want the alias this router answers to", ident.Identity())
	}
	if strings.Contains(ident.Identity(), "LFM3-VL-2B") {
		t.Errorf("identity = %q, still carries the stored name", ident.Identity())
	}
}

// The probe carries the model name the vectors are keyed on, and it comes from the
// settings handed in — not from whatever was current when libris started. Two
// questions with different embeddingModel settings must not search the same space.
func TestProbeIdentityFollowsTheSettings(t *testing.T) {
	cfg, clients, supplier := configured(t)

	first, err := modelSource(clients, supplier)(cfg)
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
	second, err := modelSource(clients, supplier)(cfg)
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
