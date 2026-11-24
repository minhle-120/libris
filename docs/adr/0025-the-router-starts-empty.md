# 0025 — The router starts empty

Date: 2026-11-24
Status: implemented (`internal/engine/serve.go` Preset + startOnce, `internal/app/host.go`)

## Context

Managed mode came up with every catalogued model resident. The preset libris writes carried
`load-on-startup = true` for each section, so opening the application meant loading a 1.3 GB
chat model and an embedder before anything had been asked of either — the router's own log
calling it `(startup) loading model libris-chat`.

Two costs and one accident:

- **Opening libris was slow and the wait was invisible.** The port and the pid were ready in a
  second; the models took the rest, and the settings page had no way to say "still loading" for
  something nobody asked for yet.
- **Most questions need one model.** A chat turn asks for `libris-chat`; an ingest job asks for
  `libris-embed`. Paying for both, every start, is a cost paid for a case that does not
  happen.
- **On a 4 GB card the two of them together did not survive.** The log of a start that died
  ended at `make_room: waiting for the memory measurement of model libris-chat`, with
  `measure process for model name=libris-chat exited with status 1 before reporting` above it.
  That measurement pass is `--models-memory-margin`'s own machinery — ADR 0020's consequence
  that "three models share one card" — so the flag written to protect a small card was
  implicated in a router that stopped.

## Decision

**The router runs, and holds nothing.**

- The preset writes **`load-on-startup = false`** for every model section. Written out rather
  than omitted, so the file states the choice: llama.cpp reads the key with a truthiness that
  is a whitelist of `on|enabled|true|1` (`common/arg.cpp`, `is_truthy`), so `false` — and any
  spelling upstream stops understanding — means do not load.
- **`--models-memory-margin` is not passed.** libris stops asking llama.cpp to keep a margin
  free, which stops it running the per-model measurement subprocess that gates every load.
  This supersedes that part of ADR 0020's consequences. The cap that remains is
  `--models-max 3`, which needs no measurement: at most three instances, least recently used
  unloaded first.
- `--models-autoload` is **not** passed either, which is the point: its default is enabled, so
  a request naming an unloaded model loads it. Per-request control exists
  (`?autoload=false`) and is not used.
- Autostart stays: managed mode still brings the **router** up at boot. It is now cheap,
  because an empty router is a socket and a table of what could be loaded.

## Consequences

- **The first question pays for a model load.** A chat turn that finds `libris-chat` unloaded
  waits for it — tens of seconds on a cold cache — and every later turn does not. This is the
  deliberate trade: one slow first answer per session, instead of a slow start for every
  session and a startup that can fail outright.
- **A model that cannot load fails at first use, not at start.** Per-model status is still the
  router's to report (`GET /models` carries a per-model `status`), so the settings page is
  where a bad GGUF is seen, and the sentence about it arrives with the request that needed it.
- **Nothing may assume a model is resident.** Anything new that wants to know whether the
  corpus is embeddable asks `GET /models` (or asks the machine for the file), never "is the
  port answering", because a port can answer to an empty router.
- **`--models-max` is now the only bound on what is resident.** If two models genuinely do not
  fit, the one being loaded fails and says so, rather than evicting the other quietly. That is
  a worse-looking failure than thrashing and a more honest one: the eviction was invisible, and
  the reload it caused was billed to the next question anyway.
- The margin number is gone from `internal/app`, and `ServeOptions` lost the field with it: a
  knob nothing sets is a claim that libris can apply something it no longer does (ADR 0011).
- The startup-failure surface got smaller, which is why the reaper work (ADR 0024's neighbour)
  matters less often: fewer things can be wrong at the moment the app opens.
