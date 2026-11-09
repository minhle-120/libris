# AGENTS.md

Working notes for any agent (or human) picking this repo up. Everything here is
either a decision that has been made or a fact that has been verified. Things
that are still open are listed at the end under **Open decisions** — do not
silently decide them.

Last updated at commit `4781b19`.

---

## 1. What Libris is

A local-first NotebookLM-style app: you add sources (PDFs, images, audio), ask
questions across them, and get answers with citations that open the original at
the right page or timestamp. Everything runs on one machine: the UI, the
database, and the models. Nothing leaves localhost.

The reference workflow is `references/research-loop-spec.md`, a behaviour spec
ported from a different project (Sift). Treat it as input, not gospel: the
two-call research/synthesis loop, the `[[Chunk N]]` citation grammar, the
evidence registry and the acceptance tests are the parts worth keeping. Sections
marked `[PORT: drop]` concern a plugin/sub-agent system that does not exist here.

Target scale: one user, one machine, ~10³ sources, 10⁴–10⁵ segments, sharing RAM
with the model servers.

---

## 2. Non-negotiables

These have been argued through. Breaking one needs a conversation, not a commit.

1. **Sampling and inference config never travel per request.** Temperature, top_k/p,
   min_p, mirostat, repeat penalties, `n_predict`/`max_tokens`, `seed`, `stop`,
   grammar and KV-cache behaviour are fixed on the `llama-server` command line.
   The client sends structure only. See §7 and `internal/llm/wire.go`.
2. **One definition per setting.** `internal/config/config.go` is the only place
   settings exist (values, labels, help, ranges, enums). The frontend is
   *generated* from it. See ADR 0001.
3. **Originals are always kept.** Citations must be able to open the source file.
   Never delete or rewrite an uploaded original.
4. **A PDF produces two derived documents**: page text and page rasters, always
   both, indexed and cited as if they were separate sources.
5. **An ingest never fails a batch.** Unreadable, unknown or unsupported input
   returns a `Result` with `Unsupported` + `Warnings`. Errors are reserved for
   missing files, directories, and the size cap.
6. **One process owns the data directory.** No cross-process locking; the MCP
   server will live inside the same binary, not beside it.
7. **No TTS, no audio output.** Audio is an input modality only.
8. **No video.** Explicitly dropped.
9. **Loopback only by default.** `127.0.0.1`; anything else must be deliberate.
10. **No `utils`, `common`, `models` packages.** They are where modularity dies.
11. **Interfaces are declared by the consumer**, not the producer, and kept narrow.

---

## 3. Toolchain

| | version |
|---|---|
| Go | 1.27.3, module name `libris` (stdlib only, no dependencies) |
| Node | 26 / npm 12 |
| Frontend | Svelte 5.57 (runes), Vite 8.3, TypeScript |
| External binaries | `pdftotext`, `pdftoppm` (poppler), `ffmpeg`/`ffprobe` — optional, probed at startup |

```bash
make build      # npm run build, then go build -o libris ./cmd/libris
make run        # go run ./cmd/libris
make test       # go test ./... -race -shuffle=on
make cover      # coverage profile + total
make check      # fmt + vet + test
make generate   # regenerate web/src/lib/config.gen.ts after changing config
./libris -addr 127.0.0.1:8080 -data ~/.config/libris
```

The binary is ~10.5 MB and self-contained apart from the model servers and
poppler/ffmpeg.

---

## 4. Layout and dependency direction

```
cmd/libris/            entry point: flags → config → wire → serve
cmd/libris-mcp/        later: stdio shim that proxies to the local /mcp
internal/
  config/              the single definition of every setting (+ store, generator)
  domain/              plain types, zero imports: Source, Segment, Provenance, Asset
  llm/                 the only package that knows the OpenAI wire format
    fake/              replays recorded responses (used by later stages)
  store/{sqlite,blob}/ not built yet
  ingest/              bytes → segments (+ pdf, sink, chunker)
  index/               not built: embed + vector + lexical
  retrieve/            not built: recall → rerank → evidence registry
  chat/                not built: research loop, prompts, synthesis
  app/                 not built: use cases shared by httpapi and mcpserver
  events/              not built: status/token/job events
  httpapi/             browser transport: JSON, SSE, embedded SPA
  mcpserver/           not built: MCP transport
web/                   Svelte source + embed.go + dist/ (committed)
docs/adr/              one file per accepted decision
```

Dependencies point one way:

```
httpapi ─┐
         ├─> app ─> retrieve/ingest/chat ─> index ─> llm
mcpserver┘                 └────────────> store
                    everything ─> domain (leaf)
```

`chat` takes evidence as *input*; it must not call `retrieve` itself. Orchestration
lives in `app` so both transports share one implementation.

---

## 5. Data model (`internal/domain`)

```go
Source{ID, Name, Kind, SHA256, Bytes}          // ID = sha256 of contents
Provenance{Page, CharStart, CharEnd, T0, T1}   // page is 1-based, 0 = not paginated
Asset{ID, Path, MIME, Bytes}                   // a file written by a sink
Segment{ID, SourceID, DocID, Kind, Ordinal, Text, Asset, Span}
```

`Segment` is the ingest→embed contract:

| Kind | embed | text is |
|---|---|---|
| `KindText` | `Text` | the passage |
| `KindImage` | bytes at `Asset.Path` (`file://` URL for llama.cpp) | rendition/caption (may be empty) |
| `KindAudio` | slice at `Asset.Path` | transcript |

Segment IDs are `"<docID>:<ordinal padded to 4>"`. A PDF yields two doc IDs:
`<source>` (text) and `<source>#pages` (rasters).

---

## 6. Configuration

`internal/config/config.go` is a flat struct; every field carries UI metadata in
tags:

```go
PDFDPI int `key:"pdfDPI" group:"ingest" label:"PDF raster DPI" help:"…"
           min:"50" max:"600" unit:"dpi"`
```

- `Default()` holds the values. `Validate()` reads `min`/`max`/`required`/`kind`/`enum`
  from the **same tags**, so ranges exist once.
- `schema.go` reflects the struct into `web/src/lib/config.gen.ts`
  (`Settings`, `defaultSettings()`, `settingGroups`). `TestGeneratedSchemaIsCurrent`
  fails the build when that file is stale — run `make generate`.
- Env var names derive from keys: `pdfDPI` → `LIBRIS_PDF_DPI`. Load order is
  defaults → env → `<data>/settings.json`.
- `MarshalJSON`/`ApplyJSON` use the same `key` tags, so API and schema cannot
  disagree. `ApplyJSON` merges partial documents and rejects unknown keys.
- `FileStore` mirrors memory to `<data>/settings.json`: atomic temp+rename, mode
  `0600` (it can hold an API key); a failed write leaves the old config in force.
- `MaxFileBytes()` is a method derived from `MaxFileMB`, so size has one unit.

**Never hand-edit `web/src/lib/config.gen.ts`.** Never add a setting to the
frontend. 23 settings exist today.

---

## 7. llama.cpp facts verified against source/docs

Verified against the llama.cpp server README and `common/chat.cpp`,
`tools/server/server-context.cpp`, `tools/server/server-task.cpp`.

- Endpoints used: `POST /v1/chat/completions`, `POST /v1/embeddings`,
  `POST /v1/rerank` (alias of `/reranking`). Auth is `Authorization: Bearer <key>`.
- `tool_choice` accepts exactly `auto`, `none`, `required`; anything else throws.
- Streaming: chunks carry `choices[].delta`; the final chunk has an empty delta and
  `finish_reason` ∈ `stop | tool_calls | length`; then `data: [DONE]` — but only for
  chat completions. `/v1/responses` and `/v1/messages` close without a sentinel,
  so the client treats clean EOF as success too.
- Reasoning is returned in `reasoning_content`.
- Multimodal parts: `image_url.url`, `input_audio.data|url`,
  `input_video.data|url`. Local files need a `file://` prefix **and** the server
  started with `--media-path`.
- Embeddings: one vector per input object; input may be a string or
  `{content:[parts]}` (llama.cpp's non-OpenAI shape, mirrored by `EmbedInput`).
  **The server normalises vectors** (Euclidean norm) — do not re-normalise.
- Rerank: `documents` selects the Jina response format
  `{results:[{index, relevance_score}]}`, already sorted descending. The `texts`
  key selects TEI's bare array, which we never send.
- `503` means all inference slots are busy (llama.cpp refuses instead of
  queueing) → `llm.IsBusy(err)`, with `Retry-After` parsed.
- `EmbeddingGemma 2` (`ggml-org/embeddinggemma-2-GGUF`) ships an `mmproj`, and is
  genuinely multimodal: text, images, audio, video in one vector space.

`internal/llm` deliberately excludes process management. A future
`internal/llamacpp` (control) will spawn/probe servers and hand plain base URLs
to `llm`, so "external OpenAI API" and "bundled llama.cpp" stay interchangeable.

---

## 8. HTTP surface

```
GET  /api/health     {"status":"ok"}
GET  /api/settings   23 values, effective configuration
PUT  /api/settings   partial document; 400 on unknown key / bad range / bad JSON
GET  /               embedded SPA, index.html fallback (client-side routing)
```

Planned: notebooks/sources/segments CRUD, ingest jobs + SSE progress,
`POST /api/notebooks/{id}/ask` (SSE: status → citations → tokens), `/mcp`.

---

## 9. Frontend conventions

- Primer/GitHub design language: 6px radii, `#1f883d` primary, `#d1d9e0` borders,
  `#fd8c73` active-tab underline, 56px header, 288px sidebar. Dark mode is
  `#0d1117` / `#3d444d` / `#4493f8`. Tokens live in `web/src/app.css`.
- `#app` is exactly viewport height with `overflow:hidden`, so `<main>` is the
  only scroller. Do not go back to `min-height:100%` — it scrolls the sidebar away.
- `SettingsView.svelte` renders `settingGroups` generically; it defines no labels,
  defaults or ranges. Reads go through `str()/num()/bool()` accessors and writes
  through `set()` because `Settings` is a union-typed record.
- `web/dist/` is **committed** (it is what `go:embed` bakes in). Rebuild it in the
  same commit as any frontend change.
- `lib/api.ts` is the only place that talks to `/api`.

---

## 10. Testing

- Tests live beside the code (`foo.go` → `foo_test.go`); per-package goldens in
  `testdata/`. No separate tests tree.
- Hermetic by default: `httptest` for HTTP, `fakeRunner` for external processes,
  `t.TempDir()` for files, `fstest.MapFS` for assets. Tests that need real poppler
  skip themselves when the binaries are missing.
- Coverage today: `domain` 100%, `httpapi` 100%, `llm` 100%, `config` 97%, `ingest`
  97%. The uncovered lines are unreachable IO/reflect guards; do not contort
  production code to reach them.
- Add a test with every feature. `TestNoSamplingOrInferenceConfig` exists to stop
  sampling parameters creeping back into requests.
- No sleeps for synchronisation, no network in unit tests.

---

## 11. Conventions

- Errors, not panics, on anything user- or file-shaped. Wrap with context
  (`fmt.Errorf("ingest: …: %w", err)`), and expose typed errors (`llm.Error`,
  `llm.IsBusy`, `ingest.ErrNoPDFText`).
- Atomic writes: temp + rename; remove the temp on every failure path.
- Reject unsafe paths rather than sanitising them (`DirSink` refuses `..` and
  absolute names).
- Keep comments about *why*, especially where a choice looks odd (no
  `Client.Timeout` because it would cap streams; no base64 auto-detection because
  `path/to/file` is ambiguous).
- One ADR per accepted decision in `docs/adr/`. Current: 0001 config single
  source, 0002 settings API.
- Commit messages describe behaviour and rationale; run `make check` first.

---

## 12. Open decisions (do not assume)

1. **Notebook ↔ collection ↔ sources.** Is a notebook one collection? Can sources
   be shared? Undecided.
2. **Whole-notebook-in-context vs retrieval.** Deferred twice. The reference spec
   is 100% retrieval-based; a frozen-prefix design would change the answer path,
   prompt assembly, chunking and citation format. Prefix stability matters for KV
   cache reuse, so this is a load-bearing choice.
3. **Citation granularity for images and audio.** `Provenance` has `Page` and
   `T0/T1` placeholders but nothing has exercised them yet; region/bbox for images
   is not designed.
4. **Multimodal retrieval strategy.** Native audio/image embeddings, text
   renditions (captions/transcripts), or both. `Segment.Text` currently stays
   empty for images.
5. **Whether the research/synthesis split survives**, and whether `tool_choice:
   "required"` behaves on real local models (verified in llama.cpp's parser, not
   yet against a live model under load).
6. **Storage schema**: SQLite via `ncruces/go-sqlite3` (cgo-free, cross-compiles,
   keeps `sqlite-vec` available). Brute-force cosine in Go is the starting plan;
   vectors as `BLOB`. Not implemented.
7. **MCP shape**: same process at `/mcp`, Loopback, bearer token, read-only by
   default; `cmd/libris-mcp` as a stdio shim. Details deferred.
8. **Packaging the model servers** (supervise/spawn `llama-server`, or require it
   pre-installed).
9. **Office formats** (docx/pptx/xlsx/epub) and **audio transcription**: both
   currently land in `Unsupported` with a reason.
10. **Eval set**: ~30 golden questions per corpus, recall@k and citation accuracy.
    Not started; needed before tuning retrieval.

---

## 13. Sharp edges encountered (so you don't repeat them)

- `go:embed` cannot reach above its package directory → migrations must live under
  `internal/store/sqlite/`, and the frontend embed lives in `web/embed.go`.
- Guessing base64 vs path by charset is wrong: `path/to/file` is all `[A-Za-z/]`.
  Use explicit constructors (`AudioPart` vs `AudioBase64`).
- `pkill -f "<pattern>"` matches the invoking shell's own command line. Use
  `pkill -x libris` or an obfuscated pattern.
- Headless-Chrome screenshots of >~1000px wide images come back rescaled and look
  ghosted/doubled in review. Verify DOM state (`getComputedStyle`, counts) rather
  than trusting the picture; capture small viewports when pixels matter.
- Grid items with `align-items: start` do not stretch, so a sticky sub-nav needs its
  containing row to be tall; check layout at 640 / 900 / 1180 px before declaring
  a settings-style page done.
- `strings.TrimSpace` uses Unicode spaces; word splitting uses ASCII spaces. A
  string of only U+2003 has no words.
