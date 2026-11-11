package index

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/llm"
)

// embedServer answers /v1/embeddings with vectors of one fixed length, and counts
// how many times it was asked. The count is the point: probing is one request per
// server, not one per job.
func embedServer(t *testing.T, dim int, probes *atomic.Int64) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Decoded loosely on purpose: EmbedInput is write-only (a response never
		// carries inputs), so the shape that matters is asserted here rather than
		// round-tripped through the client's own type.
		var req struct {
			Model string            `json:"model"`
			Input []json.RawMessage `json:"input"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad embed request: %v\n%s", err, body)
		}
		if req.Model == "" {
			t.Error("the embed request must name the model")
		}
		probes.Add(1)

		data := make([]struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}, len(req.Input))
		for i := range data {
			vec := make([]float32, dim)
			for j := range vec {
				vec[j] = 0.1
			}
			data[i].Embedding = vec
			data[i].Index = i
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// supplierClients builds a client out of the settings it is handed, which is what
// the wiring does. The whole point is that the client and the URL libris reports
// come from the same snapshot.
func supplierClients(t *testing.T) ClientSource {
	t.Helper()
	return func(cfg config.Config) (*llm.Client, error) {
		return llm.New(llm.Config{ChatURL: cfg.EmbedURL, EmbedURL: cfg.EmbedURL})
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig(url, model, dim string) config.Config {
	cfg := config.Default()
	cfg.EmbedURL = url
	cfg.EmbedModel = model
	cfg.EmbedDim = dim
	return cfg
}

// The dimension belongs to the server, so it is asked for once per server and
// reported to whoever records it.
func TestSupplierProbesEachServerOnce(t *testing.T) {
	var probes atomic.Int64
	srv := embedServer(t, 4, &probes)

	var recorded []int
	sup := NewSupplier(supplierClients(t), quietLogger(), func(dim int) {
		recorded = append(recorded, dim)
	})

	for range 3 {
		if _, err := sup.For(testConfig(srv.URL, "model-a", "")); err != nil {
			t.Fatal(err)
		}
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("%d probes for three jobs on one server, want the first to pay for all", got)
	}
	if len(recorded) != 1 || recorded[0] != 4 {
		t.Errorf("recorded %v, want the server's length reported once", recorded)
	}

	// A different model on the same URL is a different server as far as vectors
	// are concerned, so it gets asked too.
	if _, err := sup.For(testConfig(srv.URL, "model-b", "")); err != nil {
		t.Fatal(err)
	}
	if got := probes.Load(); got != 2 {
		t.Errorf("%d probes after switching model, want 2", got)
	}
}

// The probed length is the one enforced. A setting that disagrees is a stale
// number from a previous server, not a reason to reject good vectors.
func TestSupplierEnforcesTheServerLengthNotTheSetting(t *testing.T) {
	var probes atomic.Int64
	srv := embedServer(t, 4, &probes)
	sup := NewSupplier(supplierClients(t), quietLogger(), nil)

	emb, err := sup.For(testConfig(srv.URL, "model-a", "768"))
	if err != nil {
		t.Fatal(err)
	}

	vecs, err := emb.Embed(context.Background(), []domain.Segment{
		{ID: "d:0000", DocID: "d", Kind: domain.KindText, Text: "hello"},
	})
	if err != nil {
		t.Fatalf("a stale configured length rejected a valid vector: %v", err)
	}
	if len(vecs) != 1 || vecs[0].Dim != 4 {
		t.Errorf("vectors = %+v, want one of length 4", vecs)
	}
}

// No server, no problem: ingest still runs, and vectors are accepted at whatever
// length the server turns out to use. A refused connection must not be able to
// stop a file from being read.
func TestSupplierToleratesAnUnreachableServer(t *testing.T) {
	srv := embedServer(t, 4, new(atomic.Int64))
	url := srv.URL
	srv.Close() // now a port nobody listens on

	var probed bool
	sup := NewSupplier(supplierClients(t), quietLogger(), func(int) { probed = true })

	emb, err := sup.For(testConfig(url, "model-a", "1024"))
	if err != nil {
		t.Fatalf("unreachable server should not fail the wiring: %v", err)
	}
	if emb == nil {
		t.Fatal("no embedder returned")
	}
	if probed {
		t.Error("onProbe called for a server that never answered")
	}
	// The setting's number is used as a fallback, so the moment the server comes
	// back the store is still guarded against a mixed dimension.
	if emb.wantDim != 1024 {
		t.Errorf("enforcing %d, want the configured 1024 as a fallback", emb.wantDim)
	}
}

// A server that answers with a length libris has never recorded is worth one log
// line, because the vectors it produces cannot be compared with anything already
// in the store.
func TestSupplierKeepsGoingWhenTheProbeDisagreesWithSettings(t *testing.T) {
	var probes atomic.Int64
	srv := embedServer(t, 4, &probes)
	sup := NewSupplier(supplierClients(t), quietLogger(), nil)

	if _, err := sup.For(testConfig(srv.URL, "model-a", "768")); err != nil {
		t.Fatal(err)
	}
	// The disagreement is reported by the embedder that rejects the vector, so
	// the failure lands on the source rather than in a setting nobody reads.
	_, err := sup.For(testConfig(srv.URL, "model-a", "768"))
	if err != nil {
		t.Fatalf("disagreement should not stop ingest: %v", err)
	}
}

// port is the "127.0.0.1:53123" part of a server URL — the only part of it that
// distinguishes two local fakes from each other.
func port(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// The bug this pins is the one a user sees: they repoint the embedding URL in
// Settings, the next upload fails, and the failure names the server they just
// typed while the request went to the one libris booted with. So the dial target
// and the URL in the message must come from one settings snapshot, always.
func TestSupplierFollowsARepointedURL(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	a := embedServer(t, 4, &hitsA)
	b := embedServer(t, 7, &hitsB)

	sup := NewSupplier(supplierClients(t), quietLogger(), nil)
	ctx := context.Background()
	one := []domain.Segment{{ID: "d:0000", DocID: "d", Kind: domain.KindText, Text: "hello"}}

	first, err := sup.For(testConfig(a.URL, "model-a", ""))
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := first.Embed(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	if vecs[0].Dim != 4 {
		t.Errorf("first job produced a vector of length %d, want server A's 4", vecs[0].Dim)
	}
	if n := hitsB.Load(); n != 0 {
		t.Fatalf("server B was asked %d times while the settings still named A", n)
	}

	// The user edits Settings. The next job follows, including its vectors.
	second, err := sup.For(testConfig(b.URL, "model-a", ""))
	if err != nil {
		t.Fatal(err)
	}
	vecs, err = second.Embed(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	if vecs[0].Dim != 7 {
		t.Errorf("after the change, vector length %d, want server B's 7", vecs[0].Dim)
	}
	if n := hitsB.Load(); n != 2 {
		t.Errorf("server B got %d requests, want 2 (one probe, one batch)", n)
	}

	// And when that server goes away, the message may not fall back to naming the
	// old one. This is the exact assertion the reported error string fails.
	bURL, aHost := b.URL, port(t, a.URL)
	b.Close()

	dead, err := sup.For(testConfig(bURL, "model-a", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.Embed(ctx, one); err == nil {
		t.Fatal("a server that stopped listening produced no error")
	} else if msg := err.Error(); !strings.Contains(msg, port(t, bURL)) || strings.Contains(msg, aHost) {
		t.Errorf("the failure must name only the server it dialled:\n%s", msg)
	}
}
