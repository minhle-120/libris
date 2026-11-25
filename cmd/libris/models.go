package main

// The wiring between settings and the model servers, for a question.
//
// It lives here, in the wiring layer, because it is the only place that is allowed
// to know both: config knows the names and URLs, llm knows the wire format, and
// neither may import the other. What they cannot do is agree by accident — so
// somebody has to translate, and it is not either of them.

import (
	"context"
	"time"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/index"
	"libris/internal/llm"
)

// capsTimeout bounds how long a question waits for its server to describe itself. On
// loopback /props answers in microseconds; the wait is only ever spent on a server
// that is not answering, and that question is about to fail on the same socket anyway.
const capsTimeout = 2 * time.Second

// chatCaps asks the chat server what it can be shown, so an evidence set of page
// images is refused in words somebody can act on rather than by the server throwing on
// a request it cannot serve.
//
// Asked per question and not cached, because the answer belongs to whichever model is
// loaded right now and a serving restart changes it with no notice.
//
// A server that does not answer is not a failure and must not become one: nil means
// "it did not say", and retrieve then sends the media and lets a server that cannot
// take it speak for itself. That is the case for a server libris does not manage, where
// /props may not exist at all — guessing no from here would withhold pages the model
// could read.
// It is named in the ask because the engine is a router until it is told otherwise:
// asked about nothing, /props describes the router — no model, no modalities — and that
// silence used to be read as "no vision" here, which refused a question about a page in
// front of a chat model started with the projector for it.
func chatCaps(client *llm.Client, model string) *llm.Caps {
	ctx, cancel := context.WithTimeout(context.Background(), capsTimeout)
	defer cancel()

	caps, err := client.Props(ctx, model)
	if err != nil {
		return nil
	}
	return caps
}

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

		return app.Models{
			ChatModel: target.ChatModel,
			Chat:      client,
			Probe:     probe,
			Caps:      chatCaps(client, target.ChatModel),
		}, nil
	}
}
