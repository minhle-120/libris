package main

// The wiring between settings and the model servers, for a question.
//
// It lives here, in the wiring layer, because it is the only place that is allowed
// to know both: config knows the names and URLs, llm knows the wire format, and
// neither may import the other. What they cannot do is agree by accident — so
// somebody has to translate, and it is not either of them.

import (
	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/index"
)

// modelSource resolves the model side of one question from the settings saved when
// it started. Same discipline as the ingest job: a URL saved through the UI moves
// the socket for the next question, not the one under way.
//
// The probe embedder is resolved here rather than once at boot because it carries
// the model name and vector length the vectors are keyed on.
//
// The address and the model name come from the client cache's target, because that is what
// already asks "what is being served": a second copy of the question here would be a second
// answer, and under Auto the port can change between two lines of the same function.
func modelSource(clients *clientCache, embedders *index.Supplier) app.ModelsFor {
	return func(cfg config.Config) (app.Models, error) {
		// Resolved once, so the names and the URLs cannot come from different modes.
		target, err := clients.target(cfg)
		if err != nil {
			return app.Models{}, err
		}

		client, err := clients.For(cfg)
		if err != nil {
			return app.Models{}, err
		}

		probe, err := embedders.For(target.Apply(cfg))
		if err != nil {
			return app.Models{}, err
		}

		return app.Models{ChatModel: target.ChatModel, Chat: client, Probe: probe}, nil
	}
}
