# Retrieval is ordered by the embedding model, and by nothing else

Recall used to have a second stage: candidates from the vector scan could be re-scored by a
reranker against the whole question, and the research tool's `query` argument existed to be
that objective. The stage is gone, along with the settings that configured it, the wire types
that carried it, the router alias that served it and the argument that fed it.

It had one adapter and no way to be used by the mode most installs run in. The reranker was
never in the pinned model catalogue, so managed mode derived a well-formed address for a model
nothing could download; the switch defaulted to off precisely because of that. In other words
the code supported a retrieval stage the product could not offer, and the tool schema asked the
model to state an objective the code then discarded when the switch was off — which it was, by
default, forever.

## Considered options

- **Keep the seam for a future adapter** — rejected: an interface with one implementation,
  reachable only in the mode where a person already runs their own servers, is a hypothetical
  seam. Its cost was not idle code but a schema and a settings group that promised a stage the
  install cannot run.
- **Pin a reranker and finish it** — rejected for now, not forever: it buys an unmeasured
  ordering win, one more pinned download, a third router section and a role to fill. The
  evidence to justify it is a measured improvement over cosine order on this corpus, and nobody
  has measured anything.

## Consequences

`query_knowledge_base` takes `keywords` only, so the research model is no longer asked to
restate the question before searching; retrieval embeds the keyword probe and presents the
scan's order, which is now the only order in the path. A `settings.json` left by an older build
still carrying `rerankURL`, `rerankModel` or `rerankEnabled` loads anyway: `FileStore.Dropped()`
names the retired keys once at startup and the next save rewrites them away.

A future review looking at a RAG retrieval path with no rerank stage should read this as
"decided", not "forgotten" — and re-adding it is a feature that arrives with a catalogue entry
and a measurement, not as a revival of half a seam.
