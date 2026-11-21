# 0010 — Vectors are BLOBs in SQLite, scanned in Go

Date: 2026-11-12
Recorded: 2026-11-21, after the fact.
Status: accepted, implemented (`store/sqlite`: `PutEmbeddings`, `SearchSegments`)

## Context

At the target scale — 10³ sources, 10⁴–10⁵ segments, one machine, RAM shared with the
model servers — vector search has an obvious industry answer: an ANN index, usually a
SQLite extension like `sqlite-vec`, sometimes a separate server.

A separate server is out on the facts: this app is one process that owns one directory,
and a second daemon would have to be installed, started, secured and backed up alongside
it (non-negotiable 6). An extension was the live alternative, and it is not free either:
an index is a second representation of the vectors that every ingest, re-ingest and delete
has to keep in sync, and a corpus where the embedding model changed mid-flight makes an
approximate index a place for stale neighbours to hide.

## Decision

Vectors live beside the rows they describe: `segments.embedding BLOB`, with `dim` and
`model` written by `PutEmbeddings`. Recall is a brute-force cosine scan in Go, bounded by
a heap rather than by a sort.

Three things are settled by that choice and worth stating:

- **The scan is filtered before it scores.** Only sources this notebook holds, only
  `status = 'ready'` (ADR 0004). Membership and readiness remove far more rows than any
  index would have skipped.
- **One model identity, one dimension.** A search takes `model` and `dim` and compares
  nothing else. Vectors from another embedding model, or another prompt scheme, live in a
  different space; averaging their scores would look like relevance and mean nothing. So
  stale rows are *invisible* to a search rather than wrong inside one, and re-ingest is
  what brings them back.
- **Cosine is computed, not assumed.** llama.cpp normalises embeddings by default, which
  would make a dot product enough — but that is a command-line flag, and a dot product of
  unnormalised vectors silently prefers long passages.

## Consequences

- One file, no extension, no daemon. Backing up the corpus is copying the directory, which
  is a promise this app makes about the originals and should keep for the vectors.
- Recall is O(vectors in scope) per question, materialising that slice of the corpus
  through Go. At the target scale a single pass with a bounded result set beats keeping a
  second structure in sync.
- The escape hatch is bounded: swapping in `sqlite-vec` or any ANN index is a change to
  `SearchSegments`' body, not to its signature, and every caller already passes the query
  vector rather than its text. ADR 0003 kept the driver cgo-free specifically so an
  extension remained possible.
- The revisit condition is stated so this is a decision with an expiry rather than a
  preference: when recall measurably dominates answer latency at a corpus the user actually
  has, not when the row count feels large. That is the vector-search open decision in
  `AGENTS.md` §10.
- `dim` is stored per row so a truncated or mismatched blob is detectable at read time —
  cosine over ragged vectors still computes, and is meaningless (ADR 0004).
