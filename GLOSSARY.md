# Glossary

The words this codebase uses, in the sense they mean here. Code, test names, commit
messages, issues and ADRs should use them; when the glossary picks a term, do not drift
to a synonym it avoids. If a concept you need is missing, that is either language the
project does not use (reconsider) or a real gap (say so, and it gets added).

## Sources and what comes out of them

**Original** — the uploaded file, kept forever at `originals/<sha256><ext>`. Never
rewritten, never deleted. A citation's last resort: it must be able to open this.

**Source** — a file the library knows about, identified by the SHA-256 of its contents
and therefore shared between notebooks rather than copied into each.

**Derived document** — one of the two documents a PDF produces: its page text and its
page rasters. Always both, indexed and cited as if they were separate sources.

**Doc ID** — the identity of a derived document. A PDF yields `<source>` (text) and
`<source>#pages` (rasters).

**Ingest** — bytes → segments. An ingest never fails a batch: unreadable, unknown or
unsupported input comes back as `unsupported` with warnings; errors are reserved for
missing files and the size cap.

**Unsupported** — a source the pipeline recognised but will not read (a PDF with no
text layer, a `.docx`). It is a state with a reason in it, not a failure.

**Ready** — every segment of a source has a vector, and only ready sources are
retrieved. It is a promise about the whole path from bytes to searchable, not a label
for "the upload finished".

**Re-ingest** — put the kept original back through the whole pipeline: extract,
rasterise, chunk, embed. One action covering both "it failed" and "the world under it
changed" (new embedding model, new chunking). **Re-embed** is the narrower thing: new
vectors for chunks that are already split.

**Sink** — the thing that writes derived files. It refuses `..` and absolute names
rather than sanitising them.

## Chunks

**Segment** — the retrievable unit, and the ingest→embed contract. Called a **chunk**
in prose, in prompts, and in the citation grammar.

**Ordinal** — a segment's position within its doc ID. Segment IDs are
`<docID>:<ordinal padded to 4>`.

**Provenance** — where a chunk came from: page (and the page it straddles into),
character offsets, or a time range for audio. **Char offsets are page-local**, not
document-local; `PageEnd` is 0 unless the chunk crosses a page break.

**Span** — provenance as carried on a segment (`Segment.Span`). Not a DOM range.

**Asset** — a file a sink wrote: a page raster, an audio slice. Its ID is a path under
`assets/`, and it never crosses the API as a path.

## Retrieval

**Recall** — vector search: the nearest chunks by cosine, over a brute-force scan.

**Keywords / query** — the research model's two retrieval arguments, never swapped:
`keywords` is the embedding probe, `query` is the reranking objective.

**Rerank** — the second pass, scoring recall's candidates against the objective. The
embedding server and the rerank server are different servers, and either can be down.

**Evidence registry** — what the model was actually shown this turn, with the dense
`[[Chunk N]]` labels it may cite. Only what is in the registry can be cited, which is
what makes a salvaged answer honest instead of hopeful.

**Chunk label** — `[[Chunk N]]`, the citation grammar. Per turn, dense, and numbered
from the registry, not from segment IDs.

## Answers

**Research turn** — the first model call: plans retrieval with tools, is never read by a
user, and its output never reaches the transcript.

**Synthesis** — the second model call: writes the answer from the knowledge block. The
only prose that survives.

**Gate** — the mandatory-first-search refusal message. A model that tries to answer
without searching gets it; it does not unlock the gate.

**Salvage** — answering from everything found when the iteration budget runs out with
evidence in hand, as distinct from ending with nothing.

**Trace** — the research turn's tool calls. Stored on the assistant row
(`trace_json`), never listed as messages.

**Citation** — a chunk label resolved to what it pointed at, **copied out** of the
segment: quote, source, kind, page or time range, asset. Not a reference to a segment
row, because those are deleted and rewritten on re-ingest.

**Turn status** — how an assistant turn ended, in the reader's words: `answered`
(grounded, with citations), `no-info` (the library does not contain it), `ungrounded`
(the model answered in prose instead of searching — kept, but labelled unchecked),
`canceled` (the user stopped it), `interrupted` (it started and did not finish). Five
different claims about the sentence in front of the reader.

## Interface

Words the chrome is built from, so that "open the panel" means one thing.

**Panel** — the right-hand column of a notebook, holding **Sources** and **Activity** as
a segmented view. Both answer a question about the corpus *while* an answer is being
read, so they share a place with the chat rather than replacing it. Collapses to a rail,
resizes, and keeps its width per browser.

**Rail** — what either column becomes when collapsed: 44px, one button, no content. A
collapsed column is still a column, and clicking the rail opens it at its width.

**Breadcrumb** — `libris / <notebook> / <panel view>` in the header: the app's location,
named by the levels that can be climbed to. It does not name the open chat, which is the
ground under the feet rather than a step up.

**Inspector** — the panel's third view, reached from a source row or a citation: what
that source was cut into and why. It replaces the list rather than floating over it, so
the answer that cited it stays on screen beside it.

## Library

**Notebook** — a workspace: it owns its chats and its source memberships. Sources are
shared between notebooks, not duplicated.

**Chat** — one conversation inside a notebook. **Message** — one turn of it, `user` or
`assistant`.

**Data directory** — `-data`: `settings.json`, `libris.db`, `originals/`, `assets/`. The
only copy of the user's originals, which is why it defaults somewhere they can see
(ADR 0005).
