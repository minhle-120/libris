# 0020 — Managed mode: one router process, and the settings that follow it

Date: 2026-11-23
Status: accepted, and implemented. The mode switch, the router, its preset and the derived
endpoints are in `internal/config`, `internal/engine` (`serve.go`), `internal/models` and the
generated settings page. The download that was left open here — which models, at what
quantisation, and where their bytes come from — is decided by **ADR 0021**, which also
supersedes the `pooling = mean` line in the preset sketch below.
It follows ADR 0018 (the engine is fetched, not shipped) and ADR 0019 (the engine is the only
device probe), and it is the point where libris stops downloading a program and starts
running it.

## Context

ADR 0018 downloads llama.cpp and stops there. `internal/llm` still talks to three base URLs
the person configures by hand, and the group's help text claims libris "downloads and runs"
the engine — which nothing does. So the download button buys files that nothing launches, and
the settings page asks a user to type `http://127.0.0.1:8081` for a program libris has the
binary for sitting in its own data directory.

Two things follow from that gap, and they are the same decision:

- **Where the servers come from** is one question with two mutually exclusive answers. Either
  the person runs model servers and points libris at them, or libris does it. Today the page
  offers both at once, so every screen shows a user fields that are irrelevant in the other
  mode — and a machine section next to three URL boxes is genuinely hard to read.
- **Which build to download** was a setting (`engineBackend`: Vulkan, CUDA, CPU…) and should
  not have been. One Vulkan build covers NVIDIA, AMD and Intel, and it runs on pure CPU when
  there is no Vulkan device to open. Choosing a backend is asking the user to know what
  ADR 0019's device report exists to find out.

## Decision

**Managed mode and external mode are one switch, and they are exclusive.**

- **External** is what exists today: model servers run somewhere else, and the three URLs and
  the three model names are typed by hand. Two fields went with the switch: the **API key**
  (libris now sends no `Authorization` header at all — its servers are loopback ones it starts
  or points at, and a server that demands a key is a server it cannot be told about) and the
  **download mirror** (a prefix on the file download only, while the digest is checked against
  the release the *unproxied* API described: two sources of truth about which bytes are
  current, which is worse than no mirror).
- **Managed** is new. libris downloads the **Vulkan** build (ADR 0018, minus the choice),
  starts **one** `llama-server` in **router mode**, and derives everything the connection
  group used to ask for: one loopback base URL, and model names that are libris's own aliases.
  In managed mode the three URL fields and the three name fields are not shown — a field that
  the app fills in is a field that can disagree with the app.

**One process, not three.** Router mode is `llama-server` launched without a model: it routes
by the `"model"` field in each request (`tools/server/README.md`, "Using multiple models"), so
chat, embedding and reranking share a port and a process. That makes the start/stop control
one control — starting one of three servers is a half-configured app — and it makes the
process libris supervises a single thing with one pid, one log and one state.

**The models are configured by a preset file libris writes**, not by command-line flags it
assembles at the call site:

Written by `engine.Preset`, which is where the keys were checked against `common/arg.cpp`
(`LLAMA_ARG_EMBEDDINGS`, `LLAMA_ARG_POOLING`, `LLAMA_ARG_RERANKING`) rather than remembered:

```ini
version = 1
[*]              flash-attn, jinja, no-warmup, fit = on, cache-type-k/v = q8_0
[libris-chat]    model = <data>/models/<file>.gguf   mmproj = …   ctx-size = 8192
[libris-embed]   model = …   embeddings = on   pooling = mean    cache-ram = 0
[libris-rerank]  model = …   reranking = on                      cache-ram = 0
```

- Section names are the aliases libris auto-fills into the model-name settings.
- Capacity and sampling still do not travel per request, and still are not settings
  (§2.1, ADR 0006, ADR 0011). They live in this file. **This is the point where the rule has
  to be stated precisely**: a preset is a file in the data directory that libris writes and a
  person may edit with an editor; it is not a field in the settings UI, because the UI may
  only offer what libris can apply at a moment's notice, and these arguments belong to a
  process that has to be restarted to change them.
- `--models-memory-margin` and `--models-max` are how three models share one card, and they
  are the reason ADR 0019's device report matters: on a 4 GB card, all three loaded at once
  is not a configuration, it is a thrash.

**Model files are downloaded into the data directory** — the same tree ADR 0005 resolved —
behind a menu offering three preconfigured models. Which three, and at what quantisation, is
deliberately **not settled here**: it is a judgement about somebody's hardware and about
answer quality, not an engineering fact, and it wants the device report in front of whoever
picks. The plumbing (verified digest, resume, remove-when-unused) is the same ADR 0018 already
built for the engine binary.

**The device report shows up only once there is something to show it with** — the row and its
warning are absent until an engine is installed, since ADR 0019 made the engine the only
source and the card already says "Nothing downloaded yet".

## Consequences

- **libris now owns a running model server**, with everything that costs: start, stop, a
  crash, a port already taken, a model file deleted underneath a running instance, and a
  person quitting the app while inference is in flight. Each of those needs a sentence the
  settings page can show, and a state that is read from the process rather than remembered.
- **One port becomes shared state.** Loopback only still (§2.9), and a port already in use is
  a failure with a name, not a hang.
- **A settings file written before this decision means external mode.** `ApplyStoredJSON` can
  tell, because the file does not name a mode, and defaulting such a file to managed would
  point a working setup at a port nothing listens on with its URLs hidden behind a switch. A
  brand-new install has no file and gets managed.
- **`make e2e` and `make ui` keep their fakes** — the fake server already answers all three
  endpoints on one port, which is now also what the real one looks like.
- **A bearer token can no longer be sent to any server, and a download cannot be redirected.**
  Both abilities were deleted rather than hidden, so `llm.Config` has no credential field and
  a key-protected or mirrored endpoint needs a rebuild to reach.
- **Managed mode makes the three model-name settings derived rather than typed**, so
  `config` grows a mode and the generated frontend changes shape (ADR 0001). External mode
  keeps every field exactly where it is.
- **Deferred, and named so:** which three models ship in the menu, and the exact preset
  contents per model. Both are plan-later by decision, not by omission.
