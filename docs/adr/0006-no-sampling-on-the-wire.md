# 0006 — Sampling and inference config never travel per request

Date: 2026-11-09
Recorded: 2026-11-21, after the fact.
Status: accepted, implemented (`internal/llm/wire.go`); pinned by
`TestNoSamplingOrInferenceConfig`

## Context

An OpenAI-shaped client can send `temperature`, `top_p`, `max_tokens`, `seed`, `stop`
and a dozen more keys on every call, and the obvious move is to surface them: the user
picks a temperature for answers, another for brainstorming, and libris grows a settings
row per knob llama.cpp happens to expose.

Two things are wrong with that, and they are the same two things ADR 0001 was about.

These are not per-request qualities in practice. On llama.cpp they are properties of how
a model was loaded and quantised, and they interact with the sampler chain and the KV
cache. A client that sends them produces answers that cannot be reproduced from the
transcript, because the transcript does not contain what actually shaped the sentence.

And every key added to the request is a value the user has to reason about while the
person who started the server already knows better. libris runs whatever the user
launched; it is a client, and a client with opinions about sampling is a client that
quietly overrides the deployment.

## Decision

Requests carry **structure only**: `model`, `messages`, `tools`, `tool_choice`, `stream`,
`input`. Nothing that changes how a token is chosen or how much work the server does.

Specifically excluded, and asserted by name in `TestNoSamplingOrInferenceConfig`:
`temperature`, `top_p`, `top_k`, `min_p`, `typ_p`, `max_tokens`, `n_predict`, `seed`,
`stop`, `repeat_penalty`, `presence_penalty`, `frequency_penalty`, `mirostat`, `grammar`,
`cache_prompt`, `parallel_tool_calls`, `n_keep`, `ignore_eos`. That belongs on the
`llama-server` command line, where the person running the model put it.

Two keys look like sampling and are not: `tool_choice`, which is control flow (and
`"required"` is what makes the research turn a research turn — ADR 0007), and `stream`,
which is transport. llama.cpp also accepts a per-request `embd_normalize`; it is never
sent, and `llm` treats the server's output as already normalised but computes cosine
anyway rather than assuming it (ADR 0010).

## Consequences

- There is no temperature in the settings page, and no plan to add one. Answer quality is
  a deployment decision here; `docs/llamacpp.md` is where the flags and what they cost are
  written down.
- Reproducing an answer means the same server flags and the same transcript. The
  transcript is stored (`trace_json`), and the flags are the user's command line, so both
  halves are reachable.
- llama.cpp's own defaults are effectively libris's defaults, including for embedding
  normalisation and the KV cache.
- The test is the load-bearing part. A feature that wants "a bit more creativity" will
  reach for a `temperature` field, and the only defence is a build failure that says where
  it belongs.
- The predictable pressure point is the eval set (still open in `AGENTS.md` §10), which wants
  determinism and will want `seed`. The honest resolution is to measure the deployment
  the user actually runs, not to smuggle a sampler through the API.
