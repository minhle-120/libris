# 4. `ready` means searchable

Date: 2026-11-11
Status: accepted, implemented (`internal/index`, `internal/app`); verified end to
end by `make e2e`

## Context

`Source.Status` is `processing | ready | unsupported | failed`, and ingest today
ends at segmentation: hash, store the original, extract, chunk, write segments.
The first time the UI was used with real files, a 702-byte PDF reached `ready` in
41 ms on a machine with no model server running at all, which looked — correctly —
like the pipeline had been skipped.

That is a naming problem, not a bug: `ready` currently means "segmented". But the
sentence a user reads as they upload is "this source is ready", and what they mean
by that is "I can ask about it". Those two must be the same state, or every later
feature (Ask, citations, the eval set) has to re-derive whether a source is
actually usable.

## Decision

**`ready` means the source's segments all have vectors.** A source with segments
and no embeddings is not `ready`; it is still working. The embedding step becomes
part of ingest rather than a stage the caller opts into.

The status the user reads is the status of the whole path from bytes to
retrievable.

## Consequences

- Ingest becomes two phases in one job: extract+chunk, then embed. The
  `processing` window grows from milliseconds to whatever the embedding server
  needs, so `GET /api/sources/{id}` polling stays the mechanism, and a progress
  stream (SSE) moves up the priority list.
- Retrieval can then trust its scope query. `SegmentsForNotebook` already filters
  `status = 'ready'`; once this holds, that filter is also "has a vector", and a
  missing vector becomes a bug rather than a normal state.
- **A source must not be silently under-embedded.** If 3 of 100 segments fail to
  embed, answering from that source would quietly return less than it contains —
  the same failure mode as `MaxFileMB` skipping a 2 MB PDF. The state machine has
  to distinguish it.
- Changing `embeddingModel` (or its base URL) invalidates every vector. The
  stored vector needs the model recorded alongside it, and "re-embed everything"
  becomes a real operation. `segments.model` and `segments.dim` are written with
  every vector; what is not built yet is the sweep that re-embeds on change.
- **A source is embedded all at once or not at all.** `index.Embedder` returns an
  error when it produces fewer vectors than it was given, and ingest treats any
  embedding error as the source failing. That is not the same as choosing between
  `failed` and `degraded` below: it removes the situation, so a source is either
  fully retrievable or explicitly broken. `POST /api/sources/{id}/reingest` re-runs
  ingest from the start, which is what makes "the server was not up yet" recoverable
  without re-finding the file.
- The embedding **dimension** is a property of the server, not a number to type.
  `embedDimensions` became a read-only setting that libris fills by probing the
  server once per URL/model pair; a vector of another length is rejected rather
  than stored beside the others, because cosine over ragged vectors still computes
  and is meaningless.
- The chunk inspector added with this decision (`See how this source was
  chunked`) shows segments before any of this exists, so it is deliberately
  labelled by chunk counts only, never by retrievability.

## Still open

1. Whether a partially embedded source is `failed`, or a distinct state
   (`degraded`?) that still permits asking. Not decided; do not pick one in code.
   This is now likely to arrive rather than stay theoretical: llama.cpp's
   multimodal embeddings are recent and need the model loaded with `--mmproj`, so
   a server that embeds a PDF's text but refuses its page rasters is a normal
   configuration. Today that fails the whole source.
2. Whether re-embedding is automatic on model change or an explicit user action.
3. Whether `ready` should also require the *lexical* index once one exists, or
   whether vectors are sufficient on their own.
