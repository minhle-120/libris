# 0007 — A question is two model calls, never one

Date: 2026-11-12
Recorded: 2026-11-21, after the fact.
Status: accepted, implemented (`internal/chat`, `internal/app`)

## Context

The cheap design is one call with tools attached: give the model the question, give it
`query_knowledge_base`, let it search and answer in the same turn. It is a third fewer
round trips and every tutorial does it that way.

A one-shot design was also available — embed the question, take the top-k chunks, ask for
an answer — which is cheaper still and what most "local NotebookLM" attempts ship.

Three things made both unacceptable:

- **A turn that plans and writes at once has no moment at which the evidence is known.**
  By the time the model stops calling tools, the prose that a user will read is already
  half written, from whatever it had read at the time. Refusals, half-answers and
  citations of chunks it never fetched all arrive in the same stream, indistinguishable.
- **Citations have to point at something the reader can check.** That requires a fixed,
  known set of chunks — not whatever the model glanced at across N tool calls.
- **The reference spec is emphatic**, and its acceptance tests are written against the
  two-call shape (`references/research-loop-spec.md`). Deviating from it in the first
  build would have made the ported tests useless.

## Decision

**Research, then synthesis.** A tool-calling research turn plans retrieval and is never
read by anyone; a synthesis call writes the answer and is the only prose that survives.

- The research model gets exactly three tools, and `tool_choice: "required"`, plus a
  mandatory-first-search gate for models that try to answer first, plus a prose fallback
  for models that ignore `tool_choice` outright. All three because the spec insists, and
  because they fail differently: the gate catches intent, the fallback catches capability.
- A turn ends six ways: `finalize`, `no_info_found`, budget-exhausted-with-evidence
  (salvage), budget-exhausted-with-nothing, prose, cancel. Each maps to a distinct reader
  status, and "prose" and "no_info_found" are different sentences about the library.
- The knowledge block handed to synthesis is rendered from the evidence registry, which
  holds exactly what the model was shown. Chunk labels are per turn and dense. So an
  answer cannot cite a chunk nobody read, and a hallucinated `[[Chunk 99]]` is dropped
  rather than fatal.
- The two histories are built differently on purpose: flattened text for the research
  model, so prior tool-shaped turns cannot teach it to imitate them; real role messages
  for the synthesiser, which needs to know what it already said.
- Only the synthesiser's output is persisted. The research trace is stored beside the
  answer (`trace_json`) and never listed as a message.

## Consequences

- Every question costs at least two calls and up to `maxIterations` research rounds
  (default 5) before a word of user-visible prose exists. That is the tens of seconds the
  UI streams status through.
- Synthesis can fail with evidence in hand. That is `interrupted`, and it is survivable
  because the turn is persisted as it happens: the row and its citations exist before the
  first delta, so a dead answer stream costs the prose and not the evidence (ADR 0008).
- The split is what makes the UI's honesty possible — `no-info`, `ungrounded` and
  `answered` are statements about which of these two calls produced what. A single call
  collapses them into "text appeared".
- Cost of being wrong: if the split turns out not to earn its latency, removing it touches
  prompt assembly, the registry, the status vocabulary and the frontend's frame handling.
  Whether it survives is an open decision (`AGENTS.md` §10), deliberately measurable: `maxIterations` and
  `historyMessages` are settings, so the shape can be probed without a rebuild.
