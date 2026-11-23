# Data model

The shapes, the identifiers and the invariants that are not visible in the type
declaration. Vocabulary is in `GLOSSARY.md`; `internal/domain` is the source of truth
for fields.

```go
Notebook{ID, Name, CreatedAt}
Source{ID, Name, Kind, SHA256, Bytes, OriginalPath, Status, Detail, Warnings, AddedAt, SegmentCount}
Provenance{Page, PageEnd, CharStart, CharEnd, T0, T1}
Asset{ID, Path, MIME, Bytes}                     // a file written by a sink
Segment{ID, SourceID, DocID, Kind, Ordinal, Text, Asset, Span}

Chat{ID, NotebookID, Title, CreatedAt}
Message{ID, ChatID, Role, Content, Status, Citations, CreatedAt}
Citation{Chunk, Segment, DocID, SourceID, SourceName, Kind, Ordinal, Quote, Asset, Span}
```

Every domain type marshals snake_case. `domain.ErrNotFound` is the shared sentinel, so
the transport maps 404 without importing the store's error values.

## Identifiers

- `Source.ID` is the SHA-256 of the contents. Sources are shared, not copied (ADR
  0003): the same file offered to two notebooks is stored, rasterised and extracted
  once, and a rebuild refreshes the one copy every notebook citing it reads from.
- A PDF yields **two doc IDs**: `<source>` (page text) and `<source>#pages` (rasters).
- `Segment.ID` is `"<docID>:<ordinal padded to 4>"`, so it is derived — two test
  fixtures that both hardcode `"doc:0000"` collide on the primary key in a way that
  looks like a storage bug.
- An `Asset.ID` *is* a path under `assets/<sha256>/…`, which is why the asset route is
  `{id...}` and why `DirSink` refuses `..` and absolute names.

## `Source.Status`: bytes → retrievable

`processing → ready | unsupported | failed`. Only `ready` sources are retrieved, so a
half-ingested file cannot be cited. A process that died mid-flight leaves `processing`;
`app.Service.Start` sweeps those to `failed` at boot so the UI can offer a retry.

**`ready` means every segment has a vector** — ADR 0004, implemented: ingest ends by
embedding, and a source whose embedding fails is `failed` with a detail naming the
server URL, keeping its chunks so that retry has something to work with. The migration
did not backfill, so a source some older build marked `ready` can still have no
vectors; `ReingestSource` accepts every state except `processing`, so every row has a
door.

## Re-ingest

Every source is re-runnable from its kept original. One action, `ReingestSource`, puts
the file back through the pipeline: text extracted, pages rasterised, chunks split and
vectors written again. It covers both reasons a row gets redone — it failed the first
time, and the world under it changed (a new embedding model or `--pooling`, a new
`index.PromptScheme`, new chunk settings). The store records the model and dimension
per row, so a corpus can still be read back and asked which sources predate a change;
re-running them is the fix.

Only a `processing` source is refused, because a job is already writing its rows and
the queue cannot stop a second one interleaving. `PutSegments` deletes the source's old
rows before inserting the new ones, which deletes their vectors with them — so a source
is never half from one chunker and half from another, and it spends the whole rebuild
`processing`, which is out of retrieval scope.

## Chunks and provenance

Text is chunked by words — `chunkWords` 100, `chunkOverlap` 50 — over the **whole
document's** text, not page by page, so a paragraph that crosses a page break stays one
chunk (`ingest.SplitText`). `JoinPages` builds the concatenation plus each page's byte
range; `MapSpan` resolves a chunk back onto page-local offsets (`ingest/pagespan.go`).
For a plain text file there is no page, so offsets are into the file itself.

| Modality | Chunk granularity | Why |
|---|---|---|
| Text | 100 words, 50 overlap | the reference spec's numbers; ~300 tokens worst case, far under the batch ceiling |
| PDF page raster | one segment per page | a raster is already a coherent unit; cites at page level (~280 tokens) |
| Image | one segment per file | ditto |
| Audio | `ingest.AudioSliceSeconds` = 30 s | ~25 tokens/s ⇒ ~750 tokens, near the 1022-token single-ubatch ceiling. Longer slices need a larger `-ub` |

The token budget behind that table is `docs/llamacpp.md`; changing a chunk size without
it is how embedding starts failing at 400.

`Provenance` char offsets are **page-local**: `CharStart` counts from the start of
`Page`, `CharEnd` from the start of `PageEnd`. `PageEnd` is 0 unless the chunk straddles
a page break. Since a page holds far more words than a chunk, a chunk can never span
more than two pages.

`Segment` is the ingest→embed contract:

| Kind | embed | text is |
|---|---|---|
| `KindText` | `Text` | the passage |
| `KindImage` | bytes at `Asset.Path` (`file://` URL for llama.cpp) | rendition/caption (may be empty) |
| `KindAudio` | slice at `Asset.Path` | transcript |

Vectors live in SQLite as an `embedding BLOB` with a `dim` column, and retrieval is a
brute-force cosine scan in Go (`store/sqlite/search.go`). Scores are computed rather
than assumed, because the normalisation the server applies is a setting on that server.

## Chats and turns

A chat is one conversation inside a notebook; a message is one turn of it. Only `user`
and `assistant` messages are ever listed — the research model's tool calls are a trace,
stored in `trace_json` on the assistant row and never selected by a list query, because
a transcript of tool calls is not something a user should read.

`Message.Status` is the reader's vocabulary, not the loop's: `answered`, `no-info`,
`ungrounded`, `canceled`, `interrupted`. They are different claims about the sentence in
front of the reader, so a UI that renders them identically is lying. A `no-info` turn is
excluded from later history via `exclude_from_history`: an earlier "I could not find
that" reads, to the next turn, as evidence that the fact is absent from the world rather
than absent from the library.

A `Citation` is **copied out** of the segment, never referenced — `CitationFrom` clones
the asset and drops its `Path`. Re-ingest deletes and rewrites a source's segment rows,
and an answer written before that must not have its evidence rewritten along with them.
That is also why a stored message never relies on a segment id as its only link.

## The data directory

`-data`, created on demand:

```
settings.json   0600, atomic temp+rename — whatever a person typed, so never world readable
libris.db       SQLite (+ -wal/-shm while running)
originals/      <sha256><ext> — the kept copy, never rewritten; deleted only when the
                  last notebook using it lets go (ADR 0013)
assets/         <sha256>/pages/page-0001.png, /slices/slice-0001.wav, …
```

The default is `Documents/libris` — the registry's `Personal` folder on Windows,
`XDG_DOCUMENTS_DIR` on Linux, each with a fallback (ADR 0005). It is a place the user
can see and back up, because this directory holds the only copy of their originals.
`-data` overrides it; the resolved path is logged on every start, and an older
default's directory is only *mentioned*, never touched.

Migrations are embedded and ordered (`internal/store/sqlite/migrations/`): `0001_init`
(sources, segments, embeddings), `0002_vector_model` (which model and dimension each
vector came from), `0003_chat_turns` (chats, messages, citations, status, trace).
