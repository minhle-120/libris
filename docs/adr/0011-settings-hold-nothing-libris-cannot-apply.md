# 0011 — A setting is an ability; capacity and model names are not settings

Date: 2026-11-21
Status: accepted, implemented (`internal/config/config.go`, `internal/config/json.go`,
`config.gen.ts`) — supersedes the two capacity fields that 0001's reflection shipped.

## Context

`internal/config` is the single definition of everything the app can be told (§2 rule 2),
and the settings UI is generated from it, so a field in that struct is indistinguishable
to a user from a promise. Two fields were promises Libris could not keep:

- `contextSize` (`n_ctx` per slot) and `gpuLayers` (`--n-gpu-layers`). Grep for their
  readers: nothing outside `config.go` and the generated TypeScript reads them. The app
  has never applied them — `llama-server` is started by the user, and those numbers are
  fixed when it loads a model. `docs/llamacpp.md` has always said the same about
  temperature and `n_predict` (rule 1 of the non-negotiables, ADR 0006): inference config
  belongs to the server's command line. Capacity is that rule one step earlier, and
  leaving it in Settings was worse than a stray sampling knob, because a knob that changes
  nothing is how a user ends up believing a 6 GB KV cache is theirs to shrink.

Three more fields were half-promises:

- `chatModel`, `embedModel`, `rerankModel` defaulted to `qwen3-vl-8b-instruct-q4_k_m`,
  `embeddinggemma-2-q8_0` and `bge-reranker-v2-m3-q8_0`. Those are guesses about files on
  someone else's disk. A first run shows three configured fields, sends a name the running
  server was not loaded with, and the failure arrives as a model error naming a file the
  user never chose. A blank field is at least an honest question.

## Decision

- Delete `contextSize` and `gpuLayers`. If a deployment wants a different context window
  or offload, it changes the `llama-server` command line, which is where it always
  worked.
- Default the three model names to `""`. They stay settable, stay un-`required`, and stay
  the only way Libris names a model in a request.
- Retire keys **leniently on disk, strictly on the wire**. A `settings.json` written by an
  older build still carries `contextSize` and `gpuLayers`, and `ApplyJSON` rejects unknown
  keys — which would have made this change a failed boot (`fail(log, "open settings", …)`)
  for anyone who had ever opened the settings page, since the page PUTs the whole object.
  So the file goes through `ApplyStoredJSON`, which drops unrecognised keys, returns them,
  and validates everything it does recognise exactly as before. `FileStore` keeps the list
  (`Dropped()`), `cmd/libris` logs it once at startup, and the next `Save` — which writes
  `MarshalJSON` of the current struct, not the old bytes — removes them for good.
  `ApplyJSON` stays strict: a request naming a setting that does not exist is a frontend
  typo, and that should still fail loudly.

## Consequences

- Settings went 23 → 21 keys, and the count is no longer asserted literally anywhere:
  `TestGetSettings` counts off `config.Default()`, so retiring a setting cannot leave a
  test defending a number that stopped existing.
- Nothing is inherited silently. A server started with `-c 4096` answers within 4096
  tokens and Libris gets that server's error back — `llm` surfaces a non-2xx as
  `llm.Error` and does not retry, so the user reads the real ceiling instead of a
  setting that appeared to own it. The fix is now somewhere they can act on it: the
  server's command line, not a field that did nothing.
- Blank is refused where a name is needed and tolerated where it is not, and both are
  deliberate. A question with no chat model does not reach the wire: `app.usable()`
  answers `invalid request: Settings ▸ Models ▸ chat model is empty`, which names the
  place to fix it — the sentence a blank default exists to produce. The embedding model
  is optional, because ingest succeeds without one; what it costs is quieter, and
  documented. This llama.cpp clone's completion path does not route by the request's
  `model` at all — the only name-based lookups in `tools/server/` are the `/models`
  management handlers, keyed by index — so the server answers anyway. But
  `index.ModelIdentity` degrades: `""` collapses every stored vector's stamp from
  `<model>+tp1` to `tp1`, so a changed embedding model stops being detectable and
  `ReembedNotebook` loses its basis. That is in `docs/llamacpp.md` and in the setting's
  own help text, because "blank is allowed" is not the honest statement unless what
  blank costs comes with it.
- The UI harness previously inherited a chat model from the defaults. It writes one now
  (`make ui`'s `settings.json`), so the browser checks exercise the answer path instead
  of correctly reporting an unconfigured app.
- Removing a setting is now a known, cheap operation with one hazard, and the hazard has a
  test (`TestFileStoreSurvivesRetiredSettings`): the file on disk is allowed to be older
  than the binary.
