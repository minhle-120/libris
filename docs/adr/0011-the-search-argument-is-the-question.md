# The search argument is the question, not a keyword list

`query_knowledge_base` takes one argument, `query`, and it is the whole request as a sentence.
The keyword list is gone. Retrieval embeds that sentence and nothing else.

ADR 0002 removed the rerank stage, and with it the only route a complete question had into
retrieval — its `query` argument existed to be the reranker's objective. That left the embedding
probe as the single place a request can enter the path, and the argument the schema asked for was
two to six terms. So the system prompt instructs the research model to resolve pronouns and the
broader goal out of the history, and the schema then asks it to compress what it resolved back
into terms: the subject gets recovered and thrown away before the search sees it. The embedding
model is given one string with none of the conversation around it, which is the whole reason the
sentence has to travel with the search.

## Considered options

- **Keep the keyword probe** — rejected, but not on the evidence. The rule rested on a mechanism
  rather than a measurement: mean pooling averages a probe, so a long sentence drifts toward the
  centre of everything it mentions. That mechanism is real and it cuts both ways — the same
  averaging pulls a two-term probe toward whatever those terms usually appear near. Neither shape
  has been measured on this corpus, and the term list was never the measured half of the trade.
- **Ask for both, as the tool used to** — rejected: two arguments where one retrieval stage reads
  one string is how 0002 described the old schema, an objective stated and then discarded.

## Consequences

This reverses one consequence of ADR 0002, not its decision. Cosine order remains the only order
in the path: no reranker returns, and the measurement 0002 asked for before one is still owed.

The query-side prefix is unchanged — `task: search result | query: ` — because the model card's
own retrieval example puts a complete question under that prefix, so nothing about the stored
vectors moves. `index.QueryAnswer` stays unused, and moving a question to it is a separate
unmeasured knob, not a correction this change implies.

The owed measurement is small and has a shape: take a dozen real questions from a notebook, embed
each one as the sentence and as the terms a model would write for it, and count which form puts
the chunk you would have cited in the top ten. Query vectors are never stored, so it costs no
re-embedding and can be reversed as easily as it was made.
