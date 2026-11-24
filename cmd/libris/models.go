package main

// The wiring between settings and the model servers, for a question.
//
// It lives here, in the wiring layer, because it is the only place that is allowed
// to know both: config knows the names and URLs, llm knows the wire format, and
// neither may import the other. What they cannot do is agree by accident — so
// somebody has to translate, and it is not either of them.

import (
	"context"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/index"
	"libris/internal/llm"
)

// modelSource resolves the model side of one question from the settings saved when
// it started. Same discipline as the ingest job: a URL saved through the UI moves
// the socket for the next question, not the one under way.
//
// The probe embedder is resolved here rather than once at boot because it carries
// the model name and vector length the vectors are keyed on.
//
// The port comes from the client cache, because that is what already owns "where a request
// goes": a second copy of it here would be a second answer to that question, and under Auto
// the number can move between two lines of the same function.
func modelSource(clients *clientCache, embedders *index.Supplier, rerankerInstalled func() bool) app.ModelsFor {
	return func(cfg config.Config) (app.Models, error) {
		// Resolved once, so the names and the URLs cannot come from different modes
		// (ADR 0020).
		cfg = clients.Served(cfg)

		client, err := clients.For(cfg)
		if err != nil {
			return app.Models{}, err
		}

		probe, err := embedders.For(cfg)
		if err != nil {
			return app.Models{}, err
		}

		m := app.Models{ChatModel: cfg.ChatModel, Chat: client, Probe: probe}
		// Reranking is optional by configuration and by capability: the setting is
		// off, or no server is set, and retrieval stays in vector order. That is a
		// documented degradation, so it is not reported as a failure per question.
		if cfg.RerankEnabled && cfg.RerankURL != "" && rerankReady(cfg, rerankerInstalled) {
			m.Rerank = reranker{client: client, model: cfg.RerankModel}
		}
		return m, nil
	}
}

// rerankReady is whether the reranker a configuration names can answer. External mode's
// answer is the address the person typed. Managed mode's is whether the file is on this disk:
// the address there is derived from the port, so it is always well-formed, and a well-formed
// address for a model nobody downloaded makes every question pay a 400 for a feature that was
// never installed (ADR 0021). Retrieval staying in vector order is the documented fallback.
func rerankReady(cfg config.Config, installed func() bool) bool {
	if cfg.EngineMode != config.EngineManaged {
		return true
	}
	return installed != nil && installed()
}

// reranker is the adapter between the name the consumer chose for this method and
// the name the producer gave it, holding the model name the request needs.
//
// The model is closed over rather than passed per call because retrieve — the
// package that declares the interface — has no business knowing that models have
// names. It asks for "score these documents against this question"; which reranker
// answers is a decision about the deployment, which is exactly what this file is.
type reranker struct {
	client *llm.Client
	model  string
}

func (r reranker) Score(ctx context.Context, question string, docs []llm.RerankDoc) ([]float64, error) {
	return r.client.RerankScores(ctx, r.model, question, docs)
}
