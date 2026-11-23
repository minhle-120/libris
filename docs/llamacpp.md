# llama.cpp: facts verified against the server

Reference for anything that touches `internal/llm`. Verified against the llama.cpp
server README and `common/chat.cpp`, `tools/server/server-context.cpp`,
`tools/server/server-task.cpp` — not against a blog post or an older version of the
same code. Re-verify against the local clone (`references/llama.cpp/`) before
"correcting" anything here.

The rule this file exists under: **sampling and inference config never travel per
request** (`AGENTS.md`, non-negotiable 1). Those belong to the `llama-server` command line; what
follows is what the wire actually accepts, and what it does when we get it wrong.

Capacity is the same rule one step earlier, and it is not in `internal/config` either:
no `n_ctx`, no GPU layer count, no batch size (ADR 0011). A setting implies Libris can
apply it; it can only ask. What Libris does send is a model *name*, and the defaults are
blank — see the embeddings section for what a blank name costs.

## Endpoints and auth

- `POST /v1/chat/completions`, `POST /v1/embeddings`, `POST /v1/rerank` (alias of
  `/reranking`). Auth is `Authorization: Bearer <key>`.
- `503` means all inference slots are busy — llama.cpp refuses instead of queueing.
  That is `llm.IsBusy(err)`, with `Retry-After` parsed into `app.ErrBusy` → HTTP 429.

## Chat completions

- `tool_choice` accepts exactly `auto`, `none`, `required`; anything else throws.
- Streaming: chunks carry `choices[].delta`; the final chunk has an empty delta and
  `finish_reason` ∈ `stop | tool_calls | length`; then `data: [DONE]` — but only for
  chat completions. `/v1/responses` and `/v1/messages` close without a sentinel, so
  the client treats clean EOF as success too.
- Reasoning is returned in `reasoning_content`.

## Multimodal content

Chat and embeddings share the tokenizer path `tokenize_oai_content_array`, so the
same content parts work on both: `text`, `image_url.url`, `input_audio.data|url`,
`input_video.data|url` (alias `video_url`). A media part is swapped for an internal
`media_marker` text part and the bytes go to the mtmd pipeline. Without `--mmproj`,
each media type raises `"<type> input is not supported - hint: … you may need to
provide the mmproj"`.

Multimodal embedding is recent and experimental upstream. A build without `--mmproj`,
or older than that work, accepts the request shape and fails on the image part — which
is why a failed source keeps its chunks and offers a retry rather than reporting the
corpus as unreadable.

Media URLs accept exactly four forms (`handle_media` in `server-common.cpp`):

| Form | Notes |
|---|---|
| `http(s)://` | fetched by the server, 10 MB / 10 s cap |
| `file://` | requires `--media-path`; see the jail below |
| `data:<image\|audio\|video>/…;base64,…` | needs no `--media-path` |
| raw base64 | fallback decode |

A `file://` path is *concatenated after* `--media-path`, then checked by
`fs_validate_filename(path, /*allow_subdirs*/ true)`, which rejects `..` anywhere, `.`
as the whole path, `: * ? " < > | \`, control chars, non-canonical UTF-8,
leading/trailing spaces, a trailing dot, and components over 255 bytes. So `file://`
is a jailed read — it cannot escape `--media-path`. Asset names we generate (hex + `-`
+ extension) satisfy it.

## Embeddings

- One vector per input object. `input` may be a string, an array of strings, or an
  array of `{content:[parts]}` objects — the last keeps the OpenAI `data[]` response
  shape and is what lets a raster go through the same call. A top-level `content` key
  on the request is also accepted but **downgrades the response** to llama.cpp's bare
  `[{embedding, index}]` form, so always nest under `input`.
- **A base64 `data:` URI needs no `--media-path`** — only a `file://` path does. So
  `index` inlines the raster it just wrote (`ImageBase64`, `AudioBase64`) rather than
  sending a path: one less flag for the user to get right, at the cost of re-reading
  and re-encoding each asset once per batch (`maxAssetBytes` bounds it).
- A model *name* is a label, not a selector, in this clone: the completion path never
  routes by the request's `model` — the only name lookups in `tools/server/` are the
  `/models` management handlers and the multi-model scheduler, both keyed by index. So a
  blank `embedModel` still gets vectors. Libris nonetheless refuses a blank
  `chatModel` before sending anything (`app.usable`, ADR 0011) rather than let a
  half-configured app blame the server. What a blank embedding name costs is on our
  side: every stored vector is stamped
  with `index.ModelIdentity` = `<model>+tp1`, and that stamp is how `SearchSegments`
  filters and `ReembedNotebook` decides a corpus came from a different model. A blank
  name collapses every identity to `tp1`, so changing the embedding model stops
  invalidating the old vectors. Name it once, then it is free.
- `--embd-normalize` defaults to **2 (Euclidean)**, so the server returns unit
  vectors — do not re-normalise. A per-request `embd_normalize` override exists; we
  never send it.
- **Oversized input is rejected, never truncated**: HTTP 400
  `exceed_context_size_error`, or HTTP 500 asking for a larger physical batch.
  Truncation was removed from the server on purpose ("context truncation is the job of
  the client"). An embedding model must fit each input in **one ubatch**, so the usable
  ceiling is `min(-c, -ub) − 2` tokens (BOS + EOS are added by the server). `-c` costs
  no VRAM; `-ub` does.
- EmbeddingGemma 2 (`ggml-org/embeddinggemma-2-GGUF`) ships an `mmproj` and is
  genuinely multimodal: text, images, audio, video in one vector space. Its window is
  8192 tokens with a 1024-token internal sliding window, and recommended launches use
  `-c 8192 -ub 1024` → ~1022 tokens per input. Cost in that budget: text 1
  token/subword, an image ≈ 280, audio ≈ 25 per second. That budget is what sets the
  chunk sizes in the table in `docs/data-model.md`.

## Rerank

- `documents` selects the Jina response format `{results:[{index, relevance_score}]}`,
  already sorted descending. The `texts` key selects TEI's bare array, which we never
  send.
- Each query/document item may also be `{"text": str|[str], "image": url|data-uri|
  base64|[urls]}` — reranking page rasters is supported, but video and
  `fps`/`max_frames` are rejected, and it needs `--mmproj` plus a rerank chat template.

## Process management is not here

`internal/llm` deliberately excludes spawning and supervising servers. The binaries
themselves are not shipped either: `internal/engine` downloads upstream's prebuilt release
into the data directory on demand, and everything in `llm` still talks to a base URL, so
"external OpenAI API" and "downloaded llama.cpp" stay interchangeable.

## One process, many models: router mode

Verified against `tools/server/README.md` ("Using multiple models"), and what ADR 0020 builds
on. Start `llama-server` **without** a model and it becomes a router: it reads the `"model"`
field of every request and keeps an instance per model behind one port.

| flag | what it does |
|---|---|
| `--models-preset file.ini` | one `[section]` per model, keys are the CLI arguments without their dashes; a `[*]` section is shared by all |
| `--models-dir DIR` | instead: every GGUF in a directory, by filename |
| `--models-max N` | how many instances the router keeps (default 4) |
| `--models-memory-margin N` | MiB kept free on each device; the least recently used model is unloaded until a new one fits |
| `--no-ui` | no browser UI; the one thing a supervised process should not open |

`GET /models` at router level lists every model with `status.value` (`measuring`, `loaded`,
`unloaded`) and a memory measurement, and `POST /models/load|unload` take it from there. The
preset keys that matter here are `embeddings = on`, `pooling = mean`, `reranking = on` and
`mmproj` — checked against `common/arg.cpp` (`LLAMA_ARG_EMBEDDINGS`, `LLAMA_ARG_POOLING`,
`LLAMA_ARG_RERANKING`), because a wrong key is a router that refuses to start and the only
place it says so is its own log.

The router answers `GET /models` before any model has finished loading, which is what makes a
readiness probe short and honest: a model that will not load is a fact about that model, not a
reason the process cannot report its state.

## Asking it what it can run on: `--list-devices`

This is libris's only device probe (ADR 0019), so the flag is a wire contract like any
other. Verified by running `llama-b12651-bin-ubuntu-vulkan-x64` — digest checked against
GitHub's, stdout and stderr captured separately — and read from
`common_print_available_devices` (`common/arg.cpp:1149`).

| | |
|---|---|
| The output is **not JSON** | `common/arg.cpp` prints lines. A spec that said otherwise was written from the flag's name, and `ParseDevices` parses text |
| `Available devices:` then two spaces, `NAME: description (TOTAL MiB, FREE MiB free)` | the exact printf is `"  %s: %s (%zu MiB, %zu MiB free)\n"` over `ggml_backend_dev_name` and `ggml_backend_dev_description` |
| `description` may itself contain parentheses | `Vulkan0: AMD Radeon 660M (RADV REMBRANDT) (16142 MiB, 14663 MiB free)`. The memory suffix has to be anchored to the **end of the line**, not matched as "the last parenthesised group", or the card's own name is cut off at the first bracket |
| `name` is a backend slot: `CUDA0`, `HIP0`, `Vulkan1`, `SYCL0`, `CPU` | the backend word says the vendor for CUDA/HIP/SYCL, and says nothing at all for Vulkan and Metal — on this laptop the Vulkan rows are an AMD iGPU and an NVIDIA dGPU, told apart only by reading the description |
| One card can appear twice, once per loaded backend | so "how many rows" is not "how many GPUs", and a report that counts rows warns a user to choose between one GPU and itself. `mergeBackends` folds rows whose description and size agree, keeping both slot names — they are what `-dev` takes |
| CPU devices are **skipped** (`common/arg.cpp:1157`) | so the flag cannot say that a CPU exists, which is the one row `deviceinfo` states itself |
| `  (none)` when nothing but a CPU backend was found | and it is an answer, not a failure: it is how a build without GPU support says so |
| Everything goes to **stdout**, stderr is empty, exit 0, 1.7 s | so `cmd.Output()` is enough — nothing has to be read off a log — and a run that takes longer than a few seconds is a broken driver rather than a slow one |

The `(none)` shape is read from the source, not run: the machine this was checked on has two
adapters, and the CPU-only build that would print it was not downloaded.

`TestDetectAgainstARealEngine` runs the flag for real and is skipped unless
`LIBRIS_ENGINE_BIN` names an installed `llama-server`.

## Release binaries (verified against the live API, 2026-11-23)

Everything here was read off `api.github.com` or run from the downloaded tree, because the
first version of `internal/engine` was written from memory and got three of it wrong.

| | |
|---|---|
| The builds are the `b<digits>` releases, and every one is `prerelease: true` | so `/releases/latest` — which skips prereleases — returns `v1.0.0`, whose only asset is `nightly-tag.txt`. Asking that endpoint for an engine binary never gets one. `internal/engine` lists releases and picks the newest `b*` that carries an asset for this platform |
| `llama-b12650-bin-ubuntu-vulkan-x64.tar.gz` is 37.0 MB, `…-bin-ubuntu-x64.tar.gz` 23.3 MB | Linux's CPU build has **no backend word in its name at all**, which is why the asset name is a candidate table tried against the release's own list rather than a format string |
| Windows is `…-bin-win-vulkan-x64.zip` (38.9 MB) and `…-bin-win-cpu-x64.zip` | `win`, not `windows`, and `.zip`, not `.tar.gz` |
| Every asset publishes `digest: "sha256:<hex>"` | GitHub's own digest, so a download is verified against upstream rather than against nothing |
| The tar is **flat** | one directory, `llama-b12650/`, with `llama-server`, the tools and every `.so` side by side. No `bin/`, no `lib/` — `findServer` walks for that reason |
| The tar contains **symbolic links** | `libllama.so -> libllama.so.1 -> libllama.so.1.0.0`. Refusing them is refusing to install the server |
| `llama-server` has `RUNPATH: [$ORIGIN]` | so it finds its libraries wherever the directory is put, and needs no `LD_LIBRARY_PATH`. `…/llama-server --version` → `build 12650` |
| Asset URLs redirect to a signed CDN host | which does not honour a suffix range (`curl -r -65536`) at all |
| A stopped download resumes with `Range: bytes=<n>-` | asserted by `TestStoppedDownloadResumesRatherThanRestarting`, which is the one part of this table the live download did *not* exercise — it downloaded once, whole |
