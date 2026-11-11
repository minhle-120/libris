# AGENTS.md

Working notes for any agent (or human) picking this repo up. Everything here is
either a decision that has been made or a fact that has been verified. Things
that are still open are listed at the end under **Open decisions** — do not
silently decide them.

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
| Go | 1.27.3, module name `libris` |
| Go dependencies | `github.com/ncruces/go-sqlite3` (cgo-free SQLite) — one, and it is deliberate |
| Node | 26 / npm 12 |
| Frontend | Svelte 5.57 (runes), Vite 8.3, TypeScript. `npx svelte-check` is the type gate |
| External binaries | `pdftotext`, `pdftoppm` (poppler), `ffmpeg`/`ffprobe` — optional, probed at startup |

```bash
make build      # npm run build, then go build -o libris ./cmd/libris
make web-check  # svelte-check + build (the frontend equivalent of make check)
make ui         # build, run, then drive the real UI in headless Chrome
make e2e        # build, run, then upload against a fake embeddings server
make run        # go run ./cmd/libris
make test       # go test ./... -race -shuffle=on
make cover      # coverage profile + total
make check      # fmt + vet + test
make generate   # regenerate web/src/lib/config.gen.ts after changing config
./libris                                  # data goes to Documents/libris
./libris -addr 127.0.0.1:8080 -data /mnt/nvme/libris
```

The binary is ~21 MB and self-contained apart from the model servers and
poppler/ffmpeg. It was 10.5 MB before SQLite: `go-sqlite3` embeds the compiled
WASM build, which is the price of staying cgo-free (and cross-compilable).

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
  store/sqlite/        migrations + every query (sources, segments, notebooks)
  ingest/              bytes → segments (+ pdf, audio, sink, chunker, detect)
  index/               embed (text/image/audio → vectors via llm); lexical not built
  retrieve/            not built: recall → rerank → evidence registry
  chat/                not built: research loop, prompts, synthesis
  app/                 use cases shared by httpapi and mcpserver (+ ingest queue)
  events/              not built: status/token/job events
  httpapi/             browser transport: JSON, files, embedded SPA (SSE later)
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

Two rules that the layout exists to enforce:

- `app` declares the interfaces it needs (`Store`, `Settings`, `Ingestor`) and
  knows nothing about SQLite or the pipeline's internals. `httpapi` declares
  `API`. Both are consumer-side (§2 rule 11).
- `httpapi` handlers do not touch the store. `Options.App` is nil-able, so a
  build without a database still serves settings and the UI.

---

## 5. Data model (`internal/domain`)

```go
Notebook{ID, Name, CreatedAt}
Source{ID, Name, Kind, SHA256, Bytes, OriginalPath, Status, Detail, Warnings, AddedAt, SegmentCount}
Provenance{Page, PageEnd, CharStart, CharEnd, T0, T1}
Asset{ID, Path, MIME, Bytes}                     // a file written by a sink
Segment{ID, SourceID, DocID, Kind, Ordinal, Text, Asset, Span}
```

Every domain type marshals snake_case. `domain.ErrNotFound` is the shared
sentinel, so the transport maps 404 without importing the store's error values.

A **notebook** owns its chats and its memberships; sources are shared, not
copied (ADR 0003). `Source.ID` is the SHA-256 of the contents, so the same file
offered to two notebooks is stored, rasterised and extracted once.

`Status` is the async ingest state machine: `processing → ready | unsupported |
failed`. Only `ready` sources are retrieved, so a half-ingested file cannot be
cited. A process that died mid-flight leaves `processing`; `app.Service.Start`
sweeps those to `failed` at boot so the UI can offer a retry.

**Data directory** (`-data`), created on demand:

```
settings.json   0600, atomic temp+rename — may hold an API key
libris.db       SQLite (+ -wal/-shm while running)
originals/      <sha256><ext> — the kept copy, never rewritten or deleted
assets/         <sha256>/pages/page-0001.png, /slices/slice-0001.wav, …
```

The default location is `Documents/libris` — the registry's `Personal` folder on
Windows, `XDG_DOCUMENTS_DIR` on Linux, each with a fallback (ADR 0005). It is a
place the user can see and back up, because this directory holds the only copy of
their originals. `-data` overrides it; the resolved path is logged on every start,
and an older default's directory is only *mentioned*, never touched.

`Provenance` char offsets are **page-local**: `CharStart` counts from the start of
`Page`, `CharEnd` from the start of `PageEnd`. `PageEnd` is 0 unless the chunk
straddles a page break. Since a page holds far more words than a chunk, a chunk
can never span more than two pages.

**Chunking** (`ingest.SplitText` + `ingest/pagespan.go`): text is chunked by
words — `chunkWords` 100, `chunkOverlap` 50 — over the **whole document's** text,
not page by page, so a paragraph that crosses a page break stays one chunk.
`JoinPages` builds the concatenation plus each page's byte range; `MapSpan`
resolves a chunk back onto page-local offsets. For a plain text file there is no
page, so offsets are into the file itself.

| Modality | Chunk granularity | Why |
|---|---|---|
| Text | 100 words, 50 overlap | the reference spec's numbers; ~300 tokens worst case, far under the batch ceiling |
| PDF page raster | one segment per page | a raster is already a coherent unit; cites at page level (~280 tokens) |
| Image | one segment per file | ditto |
| Audio | `ingest.AudioSliceSeconds` = 30 s | ~25 tokens/s ⇒ ~750 tokens, near the 1022-token single-ubatch ceiling. Longer slices need a larger `-ub` |

`Segment` is the ingest→embed contract:

| Kind | embed | text is |
|---|---|---|
| `KindText` | `Text` | the passage |
| `KindImage` | bytes at `Asset.Path` (`file://` URL for llama.cpp) | rendition/caption (may be empty) |
| `KindAudio` | slice at `Asset.Path` | transcript |

Segment IDs are `"<docID>:<ordinal padded to 4>"`. A PDF yields two doc IDs:
`<source>` (text) and `<source>#pages` (rasters).

`Source.Status` describes the whole path from bytes to retrievable, so **`ready`
means every segment has a vector** — ADR 0004, implemented: ingest ends by
embedding, and a source whose embedding fails is `failed` with a detail naming
the server URL, keeping its chunks so that retry has something to work with. The
migration did not backfill, so a source some older build marked `ready` can still
have no vectors — and `RetrySource` accepts only `failed` and `unsupported`, so
nothing in the UI can currently fix that row.

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
- Multimodal content parts (`/v1/chat/completions` and `/v1/embeddings` share the
  tokenizer path `tokenize_oai_content_array`): `text`, `image_url.url`,
  `input_audio.data|url`, `input_video.data|url` (alias `video_url`). A media part
  is swapped for an internal `media_marker` text part and the bytes are handed to
  the mtmd pipeline. Without `--mmproj` each media type raises
  `"<type> input is not supported - hint: … you may need to provide the mmproj"`.
- Media URLs accept exactly four forms (`handle_media` in `server-common.cpp`):
  `http(s)://` (fetched by the server, 10 MB / 10 s cap), `file://`,
  `data:<image|audio|video>/…;base64,…`, or **raw base64** (fallback decode).
  `file://` requires `--media-path`, and the path is *concatenated after it*, then
  checked by `fs_validate_filename(path, /*allow_subdirs*/ true)`: rejects `..`
  anywhere, `.` as the whole path, `: * ? " < > | \`, control chars, non-canonical
  UTF-8, leading/trailing spaces and a trailing dot, and components over 255 bytes.
  So `file://` is a jailed read — it cannot escape `--media-path`. Asset names we
  generate (hex + `-` + extension) satisfy it.
- Embeddings: one vector per input object. `input` may be a string, an array of
  strings, or an array of `{content:[parts]}` objects — the last keeps the
  OpenAI `data[]` response shape. A top-level `content` key on the request is also
  accepted but **downgrades the response** to llama.cpp's bare
  `[{embedding, index}]` form, so always nest under `input`.
- `--embd-normalize` defaults to **2 (Euclidean)**, so the server returns unit
  vectors — do not re-normalise. A per-request `embd_normalize` override exists;
  we never send it (§2 rule 1).
- Rerank: `documents` selects the Jina response format
  `{results:[{index, relevance_score}]}`, already sorted descending. The `texts`
  key selects TEI's bare array, which we never send. Each query/document item may
  also be `{"text": str|[str], "image": url|data-uri|base64|[urls]}` — reranking
  page rasters is supported, but video and `fps`/`max_frames` are rejected, and it
  needs `--mmproj` plus a rerank chat template.
- `503` means all inference slots are busy (llama.cpp refuses instead of
  queueing) → `llm.IsBusy(err)`, with `Retry-After` parsed.
- **Oversized embedding input is rejected, never truncated** — HTTP 400
  `exceed_context_size_error`, or HTTP 500 asking for a larger physical batch.
  Truncation was removed from the server on purpose ("context truncation is the
  job of the client"). An embedding model must fit each input in **one ubatch**,
  so the usable ceiling is `min(-c, -ub) − 2` tokens (BOS + EOS are added by the
  server). `-c` costs no VRAM; `-ub` does. EmbeddingGemma 2's window is 8192
  tokens with a 1024-token internal sliding window, and recommended launches use
  `-c 8192 -ub 1024` → ~1022 tokens per input. Cost in that budget: text 1
  token/subword, an image ≈ 280, audio ≈ 25 per second.
- `EmbeddingGemma 2` (`ggml-org/embeddinggemma-2-GGUF`) ships an `mmproj`, and is
  genuinely multimodal: text, images, audio, video in one vector space.
- `/v1/embeddings` takes the same content parts as chat: each element of `input`
  may be `{"content":[{"type":"image_url",…},{"type":"text",…}]}`, and one element
  still yields exactly one vector. **A base64 `data:` URI needs no `--media-path`**
  — only a `file://` path does. So `index` inlines the raster it just wrote
  (`ImageBase64`, `AudioBase64`) rather than sending a path: one less flag for the
  user to get right, at the cost of re-reading and re-encoding each asset once per
  batch (`maxAssetBytes` bounds it).
- Multimodal embedding is recent and experimental upstream. A build without
  `--mmproj`, or older than that work, accepts the request shape and fails on the
  image part — which is why a failed source keeps its chunks and offers retry.

`internal/llm` deliberately excludes process management. A future
`internal/llamacpp` (control) will spawn/probe servers and hand plain base URLs
to `llm`, so "external OpenAI API" and "bundled llama.cpp" stay interchangeable.

---

## 8. HTTP surface

```
GET    /api/health                        {"status":"ok"}
GET    /api/settings                      23 values, effective configuration
PUT    /api/settings                      partial document; 400 on unknown key / bad range
POST   /api/notebooks                     {"name":…} → 201
GET    /api/notebooks                     ·  GET /api/notebooks/{id}
PATCH  /api/notebooks/{id}                rename ·  DELETE → 204
POST   /api/notebooks/{id}/sources        multipart field "file" → 202 while processing
GET    /api/notebooks/{id}/sources        ·  DELETE …/sources/{sourceID} → 204
GET    /api/sources/{id}                  status, for the UI to poll
POST   /api/sources/{id}/retry            re-queue a failed source → 202
GET    /api/sources/{id}/segments         chunks + provenance + assets
GET    /api/sources/{id}/original         the kept copy
GET    /api/assets/{id...}                derived files; id contains slashes
GET    /                                  embedded SPA, index.html fallback
```

Status mapping is one `fail` function: `domain.ErrNotFound` → 404,
`app.ErrInvalid` → 400, `app.ErrTooLarge` → 413, `app.ErrBusy` → 429 with
`Retry-After`, everything else → 500 and a log line. Nothing else in the
transport guesses at statuses.

The frontend consumes all of the above: sidebar counts come from
`GET /api/notebooks`, the notebook view polls that source list while anything is
`processing`, and the chunk inspector reads `…/segments` then fetches each page
raster from `/api/assets/`.

**No absolute path crosses the API.** `Source.OriginalPath` and `Asset.Path` are
`json:"-"`: the client asks for `/api/sources/{id}/original` or
`/api/assets/{id}` and never learns where the data directory is. Files are looked
up by ID, which is also why the asset route is `{id...}` — the ID *is* a path, and
`DirSink` already refuses `..` and absolute names.

Both file endpoints stream through `http.ServeContent`, so Range works — that is
what lets the browser seek inside an audio slice and a PDF viewer page without
downloading the file.

Planned: SSE progress instead of polling, `POST /api/notebooks/{id}/ask`
(SSE: status → citations → tokens), `/mcp`.

---

## 9. Frontend conventions

- Primer/GitHub design language: 6px radii, `#1f883d` primary, `#d1d9e0` borders,
  `#fd8c73` active-tab underline, 56px header, 288px sidebar. Dark mode is
  `#0d1117` / `#3d444d` / `#4493f8`. Tokens live in `web/src/app.css`.
- `#app` is exactly viewport height with `overflow:hidden`, so `<main>` is the
  only scroller. Do not go back to `min-height:100%` — it scrolls the sidebar away.
- Overlays are native `<dialog>` + `showModal()`, not positioned divs: Esc, focus
  trapping and the top layer come from the browser, and a hand-rolled scrim fails
  the a11y checks honestly. Backdrop-click-to-close is the `onclick` on the dialog
  comparing `e.target === dlg`.
- `ChunkViewer.svelte` is the ingest debugger: one section per derived document,
  every chunk with its ordinal, page span, word count and text, and page rasters as
  tiles labelled with their measured pixel size. It shades the region where a chunk
  repeats the previous chunk's tail — computed in the viewer from the two texts, so
  it shows what the chunker actually did rather than what it is configured to do.
- `SettingsView.svelte` renders `settingGroups` generically; it defines no labels,
  defaults or ranges. Reads go through `str()/num()/bool()` accessors and writes
  through `set()` because `Settings` is a union-typed record.
- `web/dist/` is **committed** (it is what `go:embed` bakes in). Rebuild it in the
  same commit as any frontend change.
- `lib/api.ts` is the only place that talks to `/api`. Multipart uploads must not
  set a Content-Type — the browser has to add the boundary itself.
- `lib/types.ts` mirrors the **JSON**, not the Go: field names are snake_case
  because that is what `internal/domain` marshals. `format.ts` holds the only
  display formatting rules, so byte and duration strings cannot diverge.
- The UI holds no mock data. Notebooks, sources and settings all load from the
  backend on mount; a notebook's source list refetches while anything is
  `processing` (an effect that destroys itself when idle).
- `NotebookView` and `SettingsView` define no labels, defaults or ranges for
  settings, and no fixtures for anything else.

---

## 10. Testing

- Tests live beside the code (`foo.go` → `foo_test.go`); per-package goldens in
  `testdata/`. No separate tests tree.
- Hermetic by default: `httptest` for HTTP, `fakeRunner` for external processes,
  `t.TempDir()` for files, `fstest.MapFS` for assets. Tests that need real poppler
  skip themselves when the binaries are missing.
- Coverage today: `domain` 100%, `llm` 100%, `index` 95%, `config` 97%, `ingest`
  98%, `httpapi` 89%, `app` 88%, `store/sqlite` 81%. The gaps are DB-error guards
  and unreachable IO/reflect branches; do not contort production code to reach them.
- `make e2e` (`scripts/e2e-embed.mjs`) runs the **real binary** against a fake
  embeddings server speaking llama.cpp's shape, and reads the database with
  `sqlite3` rather than trusting the API: unit tests pin each layer's shape, this
  pins that the settings a user saves actually reach the pipeline, that the probe
  writes the dimension back, and that `ready` is backed by rows.
- `app` tests use the **real** SQLite store with a fake pipeline: the interesting
  failures are how ingest's output lands in the schema (foreign keys, id
  idempotency, the ready gate), not what a mock would agree to.
- `httpapi` tests use a fake `API` and assert only the HTTP mapping.
- Async ingest is tested without sleeps-as-synchronisation: `waitUntil` polls a
  condition with a deadline, and blocking processors release on a channel so a
  bounded queue and a graceful close are both observable.
- Add a test with every feature. `TestNoSamplingOrInferenceConfig` exists to stop
  sampling parameters creeping back into requests.
- No sleeps for synchronisation, no network in unit tests.
- **Browser checks**: `make ui` builds, starts the binary against a throwaway
  data directory, and runs `scripts/ui-upload.mjs` and `scripts/ui-drop.mjs`
  over CDP (`scripts/cdp.mjs`, no dependencies). They assert on DOM state —
  computed text and counts — not screenshots. 51 checks cover create-notebook,
  file-picker upload, `processing → ready` polling, dedupe, drag-and-drop,
  unsupported files, filtering, detach, and console/page errors. They skip
  successfully when Chrome is missing. They caught a 10× byte-formatting bug
  that no amount of staring at the component would have found.

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
  source, 0002 settings API, 0003 notebooks and content-addressed sources, 0004
  `ready` means searchable (accepted, implemented), 0005 the data directory
  defaults to `Documents/libris`.
- User-caused mistakes are `app.ErrInvalid` / `ErrTooLarge` / `ErrBusy` or
  `domain.ErrNotFound`; a bare `errors.New` from a use case means 500, so pick a
  sentinel deliberately.
- Commit messages describe behaviour and rationale; run `make check` first.

---

## 12. Open decisions (do not assume)

1. **Per-notebook naming of a shared source.** Sources are content-addressed and
   shared, so the same file appears under the name it was first offered with.
   Labelling it "Smith2020" in one notebook and "paper.pdf" in another needs an
   alias on `notebook_sources`, which is not built.
2. **Whole-notebook-in-context vs retrieval.** Deferred twice. The reference spec
   is 100% retrieval-based; a frozen-prefix design would change the answer path,
   prompt assembly, chunking and citation format. Prefix stability matters for KV
   cache reuse, so this is a load-bearing choice.
3. **Citation granularity inside a page.** Settled: text cites a char range
   within a page (or the two pages a chunk spans), audio cites a timestamp range,
   page rasters cite the page. Unsettled: a region/bbox inside an image, which
   would let a raster cite a figure rather than a whole page.
   The viewer contract is a hash route per modality —
   `#/source/<id>?page=7&c=1200-1640`, `?page=7`, `?t=84.2-104.6` — and a
   straddling chunk should anchor on the page it *starts* on.
4. **Multimodal retrieval strategy.** Native audio/image embeddings, text
   renditions (captions/transcripts), or both. `Segment.Text` currently stays
   empty for images.
5. **Whether the research/synthesis split survives**, and whether `tool_choice:
   "required"` behaves on real local models (verified in llama.cpp's parser, not
   yet against a live model under load).
6. ~~Storage schema~~ — built: SQLite via `ncruces/go-sqlite3`, embedded
   migrations, `embedding BLOB` reserved and NULL. Still open: brute-force
   cosine in Go versus `sqlite-vec` once the corpus is big enough to care.
11. **Garbage collection.** Detaching a source and deleting a notebook never
    remove files, so re-ingesting after a delete costs nothing but disk. Nothing
    yet decides when an unreferenced source or asset may be collected, and a
    wrong guess deletes a cited file.
7. **MCP shape**: same process at `/mcp`, Loopback, bearer token, read-only by
   default; `cmd/libris-mcp` as a stdio shim. Details deferred.
8. **Packaging the model servers** (supervise/spawn `llama-server`, or require it
   pre-installed).
9. **Office formats** (docx/pptx/xlsx/epub) still land in `Unsupported` with a
   reason. **Audio transcription** is the bigger gap: slicing works, so audio is
   retrievable by embedding, but `Segment.Text` stays empty — the synthesiser
   cannot quote an audio chunk and a citation can only offer to play it.
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
- `json:"…,omitempty"` deletes meaningful zeros. `Provenance.T0` is the first
  audio slice's timestamp and it is `0.0`; it must stay on the wire. Zero is a
  value here, not an absence — check that before tagging a field.
- `PathValue("id...")` returns nothing. For a `{id...}` wildcard the value is
  read with the bare name, `PathValue("id")`, slashes intact.
- SQLite foreign keys are enforced by this driver, and silently so: an ignored
  error in a test reads as a passing test. Insert assets before the segments that
  reference them.
- A migration file that fails on its *second* statement cannot prove rollback —
  it fails identically every time. Make it do work, then fail on something
  unrelated (`SELECT * FROM no_such_table`), and read the file back with a raw
  `sql.Open` since a failed `Open` hands you nothing.
- `Close()` must not cancel the context the in-flight job writes with. It did:
  the rasterising finished and then its results were discarded, leaving the
  source "processing" forever. Stopping the queue and cancelling the job are two
  different acts.
- `ingest.Detect` reads magic bytes first for PDF/image/audio, extension for the
  rest. A PNG named `notes.txt` must not become text (mojibake chunks); a `.md`
  stays text whatever the sniffer says. Do not "simplify" this to one order.
- Go has no "Documents folder" call. `os.UserHomeDir()` plus a hardcoded
  `Documents` is wrong twice over: Windows moves that folder into OneDrive with
  Known Folder redirection, and a non-English Linux desktop names it `Dokumente`
  or `文档` and records the real name in `~/.config/user-dirs.dirs`. Read those
  instead — `HKCU\…\User Shell Folders\Personal` through `syscall` is enough, no
  new dependency — and reject a resolved value that is relative, unanchored
  (`\Users\name` is `IsAbs` on Windows), the filesystem root, or home itself.
- Two sources must never emit the same `Segment.ID` — it is derived from the
  source's doc id, and a test fixture that hardcodes `"doc:0000"` for both fails
  on the primary key in a way that looks like a storage bug.
- `formatBytes` said 12 KB for a 1.2 KB file: `bytes / 10 ** (2 * i)` is decimal,
  `bytes / 1024 ** i` is binary. The index was chosen by log₂₀₄₈ — write the
  divisor to match and add an exact-value check, not a `/KB/` one.
- Svelte applies a `$state` write on a microtask. A test that dispatches
  `dragenter` and reads `classList.contains('dragging')` on the next line sees
  the *old* class, and reports a working feature as broken. Await a tick first.
- A test helper that takes an **expression** must not be handed a statement
  block. `until("const r = …; return r")` compiles to `const value = const r = …`
  and fails with a syntax error inside the page, which looks like an app bug.
- `Runtime.evaluate` returns the object handle nested under `result`: reading
  `{ objectId }` off the response gives `undefined`, and the error says "no
  element matches", which is a lie about the selector.
- A `check(name, ok, detail)` helper that prints `detail` on success will happily
  print "rows never reached 0" next to a PASS. Make `detail` a thunk.
- Page code is built by an **untagged template literal**, so `\s` in it becomes
  `s`: `text.split(/\s+/)` reached the page as `/s+/` and reported every chunk as
  one word. Regexes and unicode escapes handed to the page need doubling (`\\s`).
- Both `eval` and `until` wrap their argument in a function body, so it must
  `return`. A bare expression returns `undefined`, which reads as "the app did not
  do the thing"; an object literal is worse — `({…})` parses as a block and fails
  with a syntax error inside the page.
- A test that waits for `.dropzone` proves only that *some* drop zone is on screen.
  With a shared server the previous notebook's is already there, the drop lands in
  it, and the click that was supposed to switch notebooks wins later. Wait for the
  thing you created, by name.
- Positional selectors are silent breakage: `.source .row-actions button` was the
  remove button until the inspect button was added in front of it, and the detach
  test "passed" by opening a dialog. Select by title.
- A synthetic `KeyboardEvent('keydown', {key:'Escape'})` cannot close a `<dialog>`:
  Chrome honours Escape only for trusted input. Use `Input.dispatchKeyEvent`
  (`ui.press`), which is the behaviour worth testing anyway.
- Truthiness in a poll must mean "rendered", not "present": a dialog that is open
  while its fetch is in flight satisfies `dialog && dialog.open` and the first
  measurement reads the loading text.
- Ingest dispatches on the filename, so a fixture called `x.pdf` whose bytes are
  text is genuinely `unsupported` ("PDF produced no text layer"), not `failed`.
  When a test wants to exercise the embedding path, name the file honestly.
- A 202 body is not decoration. The UI decides whether to keep polling from the
  status in the response, so `RetrySource` answering with the row it read *before*
  flipping it to `processing` left the row saying `failed`, stopped the poll, and the
  source looked stuck until reload. Return the state you just caused.
- `{source.status}` inside an element renders as one text node with the template's
  whitespace: `.textContent` is `" ready"`. Compare trimmed — or a check passes on
  a regex and fails on `===`, which reads like the feature is broken.
- An empty `[]domain.Segment` serialises to `null`, not `[]`. Every list reader
  has to tolerate it — `api.ts` routes all lists through `list()` for that reason
  — and a script that asserts `segs.length` on a fresh source will crash.
- Degenerate fixtures hide logic bugs. A fixture of 20 identical lines has a
  period-10 word sequence, so *every* 100-word window is identical and full-chunk
  shading is correct — assert the overlap on unique words, where 50 words is a
  number that can be wrong.
