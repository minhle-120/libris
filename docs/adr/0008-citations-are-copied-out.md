# 0008 — A citation is copied out, not referenced

Date: 2026-11-12
Recorded: 2026-11-21, after the fact.
Status: accepted, implemented (`domain.CitationFrom`, `store/sqlite/chat.go`)

## Context

`messages.citations` has existed since the first migration, so a stored answer needed a
representation of what it cited. The normalised answer is obvious: segments already carry
the quote, the span, the kind and the asset, so store `{chunk, segment_id}` and join at
read time. One copy of the truth, no duplication, and a source that gets re-ingested
automatically refreshes every answer that cites it.

That last clause is the trap. Re-ingest is not an edge case here — ADR 0004 made embedding
part of ingest, and every source must be re-runnable because the embedding model, its
`--pooling`, the prompt scheme or the chunk settings will all change sometime after the
corpus exists. `PutSegments` therefore deletes every segment row of a source and writes
new ones, with different ids whenever chunking changed. A citation by reference does not
survive that: it either dangles, or worse, resolves to a *different* passage that happens
to share an ordinal — an answer citing a sentence the model never read, with total
confidence.

There is a second, quieter constraint. An answer is a record of a claim made at a moment.
`"It reports 4.2 K [[Chunk 2]]"` is a fact about last Tuesday's corpus, and rewriting what
that citation points at, because the corpus moved, is silently rewriting history.

## Decision

**A citation is copied out.** `domain.CitationFrom` clones what a reader and a viewer
need — chunk label, segment id, doc id, source id and name, kind, ordinal, the quote, the
span, and the asset struct — and stores that whole value in the message's JSON.

The asset's on-disk `Path` is dropped on the way, since a citation is a record of what was
cited rather than a handle for opening files; anything that needs bytes fetches
`/api/assets/{id}` by id. The segment id stays, but as provenance — where this came from —
and not as the lookup key the UI renders from.

## Consequences

- An answer keeps its evidence across a re-ingest, a re-rasterisation, a changed chunk
  window, and a source detached from the notebook it was cited in. The chunk inspector can
  highlight the exact span without touching the segment table.
- An answer can quote text the current corpus no longer contains. That is correct, and it
  is the point: the reader is looking at what was said, and the source row still says when
  it was last rebuilt.
- The frontend inherits the rule: a citation chip is built from the frozen `Citation` in
  the message, never from a segment fetched at render time. Recorded in `AGENTS.md` §7
  because it looks like an optimisation to break.
- Duplication is bounded by citations per message times quote length — the quote is the
  segment text, so a heavily cited answer roughly re-stores what the model read. Accepted
  as the price of a citation that cannot rot, at a scale where messages are hundreds, not
  millions.
- Nothing validates an old citation against the current corpus. If a future feature wants
  "this source has changed since that answer", it can compare `Segment` ids; the id is
  still recorded precisely so that question can be asked.
