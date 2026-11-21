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

`internal/llm` deliberately excludes spawning and supervising servers. A future
`internal/llamacpp` will probe or start them and hand plain base URLs to `llm`, so
"external OpenAI API" and "bundled llama.cpp" stay interchangeable.
