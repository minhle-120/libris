# Research Loop Specification

A language- and framework-agnostic spec for porting Sift's agentic RAG research loop.
It describes **behaviour and contracts**, not file layout, DI, or UI. Anything marked
`[PORT: drop]` is specific to Sift's plugin/agent system and is intentionally excluded.

Source of truth: `lib/src/features/orchestrator/domain/research_orchestrator.dart`,
`lib/core/tools/{rag_tool,delegate_to_synthesizer_tool,no_info_found_tool}.dart`,
`lib/core/models/ai_models.dart`, `lib/src/features/orchestrator/domain/chat_orchestrator.dart`,
`lib/src/features/chat/presentation/controllers/chat_controller.dart`.

---

## 1. Core idea

A user question is answered by **two separate LLM calls with different roles**:

| | Research Agent | Synthesizer Agent |
|---|---|---|
| Purpose | Decide *what to retrieve*, iteratively | Write the *final prose answer* |
| Transport | **Non-streaming** `chat/completions` | **Streaming** `chat/completions` |
| Tools | Yes, `tool_choice = "required"` | None |
| Output to user | **Never.** Emits only tool calls | Streams visible answer text |
| Ends when | it calls `finalize_research_response` / `no_info_found` / budget exhausted | stream completes |

The decisive design choice: **the research model never writes the answer.** It can only
say *"these retrieved chunks are the answer's evidence"* by returning chunk indices.
Consequences worth preserving:

- The research model may be small / tool-calling-optimised / non-verbal; its prose is
  discarded anyway, so the "ONLY output tool calls" rule costs nothing.
- The user never sees a research-model draft; visible output starts only when the
  synthesis stream starts, so perceived latency is one retrieval loop + one first token.
- The answer's evidence set is an explicit, validated, persistable list of chunk IDs
  → citations are exact and can be rendered *during* streaming.
- Evidence can be re-resolved from the DB after the loop ends; the research transcript
  never has to carry chunk text into the answer call.

Everything below exists to support that split.

---

## 2. External contracts

Model these as abstract data types; names/prefixes are yours.

```
Role        = system | user | assistant | tool

Message {
  role            : Role
  content         : string | ContentPart[]   // parts only needed for multimodal
  toolCallId      : string?                  // set iff role == tool
  toolName        : string?                  // informational only
  toolCalls       : ToolCall[]?              // set iff assistant requested tools
  reasoning       : string?                  // reasoning_content passthrough, optional
}

ToolCall      { id, name, argumentsJson /* raw string, NOT pre-parsed */ }
ToolDef       { name, description, parametersJsonSchema }
ToolOutcome   = { kind: CONTINUE | FINALIZE | NO_INFO | STOP,
                  toolMessage: Message?,     // omitted for terminal kinds
                  payload: any? }

// Retrieval side
Chunk         { id: DocId, docId: DocId, chunkIndex: int, content: string, sourceTitle: string }
Evidence      { index: int,   // stable 1-based session id, the ONLY thing the model sees
                chunk: Chunk, score: float? }
EvidenceStore { register(chunk) -> index; resolve(index) -> Evidence?; all() -> Evidence[]; reset() }

ResearchPackage { indices: int[] }            // chosen evidence, in citation order
ResearchTrace   = Message[]                   // loop transcript, for debug/replay only
ResearchOutcome {
  status        : SYNTHESIZE | NO_INFO | RAW_ANSWER | CANCELED
  package       : ResearchPackage?            // set when status == SYNTHESIZE
  reason        : string?                     // set when status == NO_INFO
  rawAnswer     : string?                     // set when status == RAW_ANSWER
  trace         : ResearchTrace
}

// The orchestrator's public surface
runTurn(collectionId, historyText, userQuery, events, isCanceled) -> ResearchOutcome
synthesize(history, package, evidenceStore, userQuery, isCanceled) -> stream of (textDelta | reasoningDelta)
```

Hard requirements on `Message` ↔ OpenAI-compatible wire format:
`tool_calls[].function.name` + `.arguments` (string), `tool_call_id` on tool messages,
`tool_choice` only sent when tools are present. `arguments` must stay a raw string
end-to-end; parse lazily, and **a parse failure must not throw** — treat as `{}`.

---

## 3. Evidence store (chunk registry)

A per-turn, in-memory, deduplicating map. It is what lets the model refer to chunks
without ever copying their text.

```
register(content, sourceTitle, docId, chunkIndex, score?) -> int:
    key = content.trim()
    if key in contentToIndex: return contentToIndex[key]          // dedupe, same id back
    id = nextId++                                                 // 1, 2, 3, ... never reused
    store[id] = Evidence(id, ..., score)
    return id
```

Invariants:
- `reset()` at the top of every turn; `nextId` restarts at 1.
- Ids are **dense, 1-based, monotonic** and stable for the whole turn, so `[[Chunk 3]]`
  in iteration 1 means the same chunk in iteration 4 and in the final answer.
- Dedupe is on trimmed content only → the same chunk found via two different searches
  yields one id, so the model's citation list stays short and the context isn't doubled.
- Exposing `all()` matters: it is the fallback evidence set (see §5.6).

---

## 4. Tools exposed to the research model

Exactly three. `parametersJsonSchema` is standard JSON Schema.

### 4.1 `query_knowledge_base`
```
description: "Search the local knowledge base for relevant information using keywords
              for retrieval and a query for reranking. Always start by calling this
              tool first!"
args: {
  keywords: string  (required) "Search keywords used for the initial vector retrieval stage."
  query   : string  (required) "A more detailed question or context used to rerank the
                                retrieved results for best relevance."
}
```
**Two-argument split is load-bearing.** `keywords` is the *embedding probe* (short,
term-like — better vector hit rate); `query` is the *reranking objective* (a full
question — better relevance ordering). Never embed `query`, never rerank with `keywords`.

### 4.2 `finalize_research_response`
```
description: "Call this when you have finished your research and want to provide the
              final textual answer. Use this ONLY for summaries and analysis."
args: { indices: int[] (required)
        "The indices of the chunks (e.g. [[Chunk 1]]) that are most relevant to the user query." }
```
This tool has **no side effects and returns no content.** Its `execute()` is just
`ResearchPackage(indices: args.indices)`, and the loop returns immediately. The answer is
produced later by the Synthesizer from the resolved chunks. Do not "improve" this into a
tool that returns text — that would collapse the two-call design.

### 4.3 `no_info_found`
```
description: "Call this when you have searched the knowledge base and found NO relevant
              information to answer the user query. This stops the research process and
              informs the user."
args: { reason: string (required)
        "A brief explanation of what was searched and why no info was found." }
```
`execute()` is a no-op; only the trigger matters. `reason` becomes the user-visible
answer for that turn.

---

## 5. The loop

```
MAX_ITERATIONS   = 5      // assistant turns, not tool calls
RECALL_TOP_K     = 10     // vector stage
FINAL_TOP_K      = 5      // post-rerank
HISTORY_MESSAGES = 8      // most recent persisted messages

runTurn(collectionId, historyText, userQuery, events, isCanceled):
  store.reset()
  events.status("Starting research...")

  # ── 1. assemble ────────────────────────────────────────────────
  system  = RESEARCH_SYSTEM_PROMPT
  context = historyText.isEmpty ? userQuery
                                : historyText + "\n\nCurrent query: " + userQuery
  messages = [ Message(system, system), Message(user, context) ]
  traceFrom = len(messages)          # trace excludes system + seeded user prompt
  gateOpen  = false                  # "has queried at least once"

  # ── 2. iterate ─────────────────────────────────────────────────
  for iter in 1..MAX_ITERATIONS:
    if isCanceled(): return Outcome(CANCELED, trace(messages[traceFrom:]))
    events.status("Analyzing context (Iteration $iter)...")

    reply = llm.chat(messages, tools = ALL_TOOLS, toolChoice = "required")
    messages.append(reply)                       # keep the full transcript, incl. prose

    # 5.1 escape hatch: model answered in prose instead of calling a tool
    if reply.toolCalls is empty:
      return Outcome(RAW_ANSWER, rawAnswer = reply.content,
                     trace = messages[traceFrom:])

    # 5.2 execute every tool call in the reply, in order
    for call in reply.toolCalls:
      args = tryParseJson(call.argumentsJson) or {}     # never throws

      # 5.3 mandatory-first-search gate
      if not gateOpen and call.name != "query_knowledge_base":
        messages.append(toolMsg(call,
          "ERROR: You must call `query_knowledge_base` first to gather information from "
          "the library before using other tools. Please call `query_knowledge_base` now."))
        continue                                     # no side effect; same iteration

      if call.name == "query_knowledge_base":
        gateOpen = true
        events.status('Searching library for "${args.keywords or 'relevant info'}"...')
        messages.append(toolMsg(call, retrieve(collectionId, args, userQuery)))

      elif call.name == "finalize_research_response":
        pkg = ResearchPackage((args.indices or []).asInts())
        return Outcome(SYNTHESIZE, package = pkg, trace = messages[traceFrom:])
                                             # NOTE: return immediately — remaining
                                             # toolCalls in this reply are dropped

      elif call.name == "no_info_found":
        return Outcome(NO_INFO, reason = args.reason, trace = messages[traceFrom:])

      else:
        # 5.4 unknown/unhandled tool → tell the model, do not crash
        messages.append(toolMsg(call, "Error: Unknown tool ${call.name}"))

  # ── 3. budget exhausted ───────────────────────────────────────
  all = store.all()
  if all is not empty:
    # salvage: cite everything found, in registration order
    return Outcome(SYNTHESIZE, package = ResearchPackage(all.map(e => e.index)),
                   trace = messages[traceFrom:])
  return Outcome(NO_INFO,
    reason = "I have reached the maximum research depth without finding specific details "
             "in the library to answer your query.",
    trace = messages[traceFrom:])
```

`toolMsg(call, text)` = `Message(role=tool, content=text, toolCallId=call.id, name=call.name)`.
Every appended tool message **must** carry the matching `toolCallId`.

### 5.1 Termination paths (all six)

| # | Trigger | Status | Evidence set | User sees |
|---|---|---|---|---|
| 1 | `finalize_research_response` | `SYNTHESIZE` | model-chosen indices | streamed answer |
| 2 | `no_info_found` | `NO_INFO` | none | `reason` |
| 3 | Budget hit, chunks exist | `SYNTHESIZE` | **all** registered chunks | streamed answer |
| 4 | Budget hit, no chunks | `NO_INFO` | none | canned depth message |
| 5 | Prose instead of tool call | `RAW_ANSWER` | none | that prose verbatim |
| 6 | `isCanceled()` at loop top | `CANCELED` | — | nothing / partial |

Path 3 is the important safety net: a model that keeps refining until the budget runs out
still produces a grounded answer rather than an error. Path 5 must be handled even though
`tool_choice="required"` should prevent it — local models under load ignore it.

### 5.2 Context growth (port this carefully)

`messages` is **append-only across iterations**: iteration *n*'s request contains the
system prompt, the seeded user prompt, and every prior assistant reply + tool result.
After 4 searches, the request carries ~4 × 5 chunks of verbatim chunk text plus the
evaluation notes. This is what gives the model cross-iteration memory ("don't re-search
that, I already have it; drill deeper instead") — it is a feature, not an accident.

Budget it before porting: roughly
`prompt + MAX_ITERATIONS × (assistant turn + FINAL_TOP_K × avg chunk tokens)`.
If your model's window is tight, the two cheapest levers, in order: lower `FINAL_TOP_K`,
then lower `MAX_ITERATIONS`. Truncating *older tool results* is possible but breaks the
reason-and-refine behaviour — do it last and log it.

### 5.3 The gate, and why it is a tool message

The gate exists because small models habitually answer from memory or call a specialist
tool first, producing ungrounded answers. It is implemented as a **refusal-shaped tool
message**, not a hard abort: the model sees the scolding text on the next iteration and
complies. It never sets `gateOpen`, so the model must still perform a real search. Costs
one extra iteration, which is the intended price.

Related invariant: the gate makes "`has evidence` ⇒ `called query_knowledge_base`" true,
which is why path 3 (chunks exist ⇒ synthesize anyway) is safe.

### 5.4 Multiple tool calls in one assistant turn

They run **sequentially in array order**, all appended before the next LLM call. A terminal
call (`finalize` / `no_info_found`) returns instantly, discarding later calls in that
array. Don't parallelise retrieval "for speed" unless you also preserve ordering, since the
chunk ids embedded in a `finalize` depend on registration order.

### 5.5 Cancellation

Checked **once per iteration at the top**. Long in-iteration waits (an LLM turn, a search)
are not interruptible in the reference. Upgrade if your UX needs it: race the await against
the cancel signal and return `CANCELED`. A cancel *after* the loop, during the synthesis
stream, is handled in §9.

### 5.6 Error handling

No exception is allowed to escape the loop for model-shaped problems. Bad JSON args → `{}`;
missing keys → `''`/`[]`/defaults; unknown tool → error-shaped tool message; empty search →
a plain-text "nothing found" tool result (§6) that nudges the model to re-search or give up.
Only infrastructure failures (embedding server down, HTTP errors) should raise, and the
caller maps them to an error state.

---

## 6. `query_knowledge_base` implementation

```
retrieve(collectionId, args, userQuery) -> string:
  keywords = args.keywords or ""
  query    = args.query    or ""
  if keywords.isEmpty: return "No keywords provided."

  # 1. embed the KEYWORDS, not the query
  v = embed([keywords])[0]

  # 2. brute-force cosine top-10 within this collection
  hits = vectorSearch(collectionId, v, limit = RECALL_TOP_K)
  if hits.isEmpty: return "No relevant information found in the library."

  # 3. rerank with the QUERY, if a reranker is configured
  scores = rerank(query, hits.map(h => h.content))       # [] of len(hits), higher = better
  ranked = sortByDesc(hits zip scores, by = score)

  # 4. register the best 5 → stable ids
  evidences = ranked.take(FINAL_TOP_K)
                  .map(h => store.resolve(store.register(h.content, h.sourceTitle,
                                                         h.docId, h.chunkIndex, h.score)))
  # 5. re-sort chronologically for display (document order, then position in document)
  evidences.sort(by = (docId, chunkIndex))

  body = evidences.map(e => "[[Chunk ${e.index}]]\n${e.content}").join("\n\n")
  return body + "\n\n"
       + "**Assistant Evaluation Note**: Review the background knowledge retrieved above. "
       + "1. Is this sufficient to answer the User Query (\"$userQuery\") fully? If yes, proceed with your tasks. "
       + "2. Do you need more specific details, numbers, or dates that might be in other parts of the library? "
       + "If yes, call `query_knowledge_base` again with refined keywords."
```

Notes on the non-obvious parts:

- **Registration order ≠ display order.** Chunks are registered in *relevance* order (so the
  ids reflect what the search found most useful) but presented in *document* order (so the
  model reads a coherent narrative instead of shuffled fragments). Keep both.
- **The evaluation note is the loop's engine.** No code forces iteration 2 — this injected
  nudge does, and it is what turns a one-shot retriever into a research loop. It lives in
  the tool result rather than the system prompt so it can quote the live `userQuery`.
  Keep it per-call. Treat the trailing note as part of the tool contract.
- Retrieval is recall→rerank two-stage with different inputs per stage; that asymmetry is
  the whole point of the two-argument schema.
- `rerank()` degrades gracefully: unconfigured reranker → uniform `1.0`s (i.e. pure vector
  order); reranker error → uniform `0.0`s. Both must return `len(documents)` values so the
  zip stays aligned. Failures must never abort a turn.
- `vectorSearch` is exhaustive (cosine over every chunk in the collection, batched/pipelined
  and multi-core in the original). At ~10⁴–10⁵ chunks it's fine; past that, swap in an ANN
  index (sqlite-vec / HNSW / pgvector) behind the same interface. Recall must stay ≥
  `RECALL_TOP_K`.
- Embedding storage format is a JSON array of floats in a text column, compared as
  `Float32List`. Fine as a starting point; a binary/vector column is a cheap later win.

**Chunking at ingest** (not part of the loop, but the loop's output quality depends on it):
recursive split by priority *paragraph → newline → sentence → word*, target `chunkSize = 100`
words with `overlap = 50` words carried over by backtracking. Word counts are
`split(/\s+/)`. Tune these per corpus, not per prompt.

---

## 7. Prompts

### 7.1 `RESEARCH_SYSTEM_PROMPT`

Port verbatim minus the `[PORT: drop]` lines. Verbatim matters — local models are
noticeably more obedient to this exact phrasing.

```
You are a Research Specialist. Your task is to locate relevant information in the database to answer user queries.
You have access to the conversation history. Use this context to resolve pronouns (e.g., "he", "they", "that project") and understand the broader goal of the user's current request.

### Core Objectives:
1. **Understand Context**: Analyze history to rephrase the query into standalone search terms if necessary.
2. **Search**: Use `query_knowledge_base` to find relevant document chunks.
3. **Reason & Refine**: After each search, analyze the chunks. If the information is incomplete, broaden your knowledge by calling `query_knowledge_base` again with different keywords. Repeat this cycle until you have a nuanced and complete understanding.
4. **No Information Found**: If you have searched multiple times and found no relevant info, call `no_info_found`. **CRITICAL**: You MUST attempt at least one search first.
5. [PORT: drop — specialist delegation]
6. **Synthesis Strategy**: Once research is complete (and any specialists called), you MUST provide a final textual overview via `finalize_research_response`.

### Rules:
- **ONLY output Tool Calls**. Do not provide any conversational text, explanations, or reasoning.
- **Cycle of Knowledge**: Do not settle for the first result. Use subsequent searches to drill down into specifics or clarify points found in earlier results.
- [PORT: drop — specialist flow]
- **Consistency**: Use the [[Chunk X]] citation format in all tool arguments if referencing specific sources.

### Flow:
1. Rephrase the query if context is complex.
2. **Knowledge Cycle**: Call `query_knowledge_base`, analyze, and repeat as needed.
3. [PORT: drop — specialist tool]
4. Call `finalize_research_response` to end the session.
```

Renumber objectives/flow after dropping the specialist lines. If you also drop
`no_info_found`, keep objective 4's "MUST attempt at least one search first" spirit
somewhere — it is the anti-hallucination backstop.

The three prompt-level behaviours that actually show up in metrics, if you must compress:
(a) resolve pronouns from history into standalone search terms, (b) never stop at the
first search, (c) tool calls only, no prose.

### 7.2 `SYNTHESIS_SYSTEM_PROMPT` (the Synthesizer)

```
You are Sift, a helpful AI assistant. Your goal is to answer the user's question accurately using the provided background knowledge chunks.

### Citation Rules:
1. **Strict Format**: EVERY piece of information from the background context MUST be cited using exactly this format: [[Chunk X]] where X is the chunk number.
2. **Immediate Placement**: Place citations immediately after the sentence or claim they support, not just at the end of a paragraph.
3. **No Alternatives**: NEVER use formats like "(Chunk 1)", "Source 1", or "according to xyz". ONLY use the [[Chunk X]] tag.
4. **Multiple Sources**: If multiple chunks support a claim, list them all: [[Chunk 1]][[Chunk 2]].

### Math Formatting:
- **Use LaTeX** for any mathematical expressions, formulas or equations (note: do not apply LaTeX for simple measurements like 1m, 1kg, etc.. Just write them as they are).
- **Inline Math**: Use single dollar signs: $ E=mc^2 $.
- **Block Math**: Use double dollar signs for complex or standalone equations: $$ P(A|B) = \frac{P(A \cap B)}{P(B)} $$.
- **Consistency**: Do not mix LaTeX with plain text symbols (like using * instead of \times).

### Instructions:
- Answer the user's latest query accurately using the provided context.
- [PORT: drop — specialist-artifact acknowledgement]
- Be honest: If the context doesn't contain the answer, state that "The provided documents do not contain information about [topic]."
- Maintain a professional, objective, and helpful tone.
```

The rigid citation grammar is not cosmetic: `[[Chunk N]]` is a machine-parseable key into
the persisted citation map (§9), and the strict "never `(Chunk 1)`" rule is what makes the
regex `/\[\[Chunk \d+\]\]/` sufficient for both rendering and history sanitisation.

### 7.3 Where history goes

Two different history renderings for the two agents. Do not merge them.

**Research agent** gets history as **flattened text inside one user message** (not as
alternating role messages) — cheaper, and it keeps `tool_choice="required"` from being
"helpfully" influenced by prior tool-shaped turns:

```
buildResearchHistory(persistedMessages):        # only the last HISTORY_MESSAGES = 8
  out = []
  for m in tail(persistedMessages, 8):
    if m.metadata.exclude_from_history: continue
    if m.role == user:      out.push("Query: " + m.text)
    if m.role == assistant and m.text.trim() != "":
                            out.push("Synthesizer answer: " + stripChunks(m.text))
  return out.join("\n\n" before queries, "\n" before answers)   # see note
```
Then: `context = history == "" ? query : history + "\n\nCurrent query: " + query`.

- `"Synthesizer answer:"` (not `"Assistant:"`) is a deliberate framing cue: it tells the
  research model that prior answers were evidence-derived, so it should trust and refine
  them rather than re-derive from scratch.
- `stripChunks` = remove `[[Chunk N]]` — the research agent has no use for citation tags
  and they only pollute its search-term choices.
- **Note on the original's join:** user turns are separated by `\n\n`, assistant turns by
  `\n` (the separator is written *before* each turn based on that turn's role). Preserve it
  only if you're chasing output parity; a uniform `\n\n` is equivalent for the model.

**Synthesizer** gets history as **real role messages** (`user`/`assistant`), last 8,
`[[Chunk N]]` stripped from assistant text, empty assistant texts dropped,
`exclude_from_history` skipped. Multimodal attachments, if you have them, are re-expanded
into content parts here (images base64, documents as `--- Previous File: <name> ---\n<text>`).

---

## 8. Synthesis call

```
synthesize(collectionId, history, package, store, userQuery, isCanceled) -> stream:
  # 1. resolve + order: model's index order is irrelevant, document order wins
  evidences = package.indices.map(store.resolve).filter(nonNull)
                               .sortBy((docId, chunkIndex))

  # 2. single user message: evidence block + query
  userContent = "### Knowledge Chunks:\n"
              + evidences.map(e => "[[Chunk ${e.index}]]\n${e.content}").join("\n\n")
              + "\n\n### User Query:\n" + userQuery

  messages = [ Message(system, SYNTHESIS_SYSTEM_PROMPT),
               ...history,                       # real role messages, §7.3
               Message(user, userContent) ]

  return llm.streamChat(messages, isCanceled)    # { textDelta?, reasoningDelta? }
```

Key points:
- Only the **model-selected** chunks are pasted here, not everything retrieved. A 4-search
  turn may register 20 chunks and answer from 6. This is the context economy that makes the
  loop affordable.
- Indices survive unchanged, so the answer's `[[Chunk N]]` tags still resolve against the
  evidence store even though display was re-sorted.
- Indices that don't resolve are silently dropped. If the model hallucinates an index, you
  get a shorter context, not a crash. If *zero* resolve, the answer degrades to
  "no chunks" — acceptable; the honesty instruction in §7.2 covers it.
- The research **trace** is deliberately **not** in this call. The Synthesizer sees a clean
  prompt (system + chat history + evidence + question), never the search reasoning.

---

## 9. Caller-side orchestration, persistence, citations

The loop is only half of a turn. The reference caller (`chat_controller`) does this, and the
ordering is load-bearing:

```
onUserQuery(query, collectionId, persistedMessages, placeholderId):
  history            = exclude(persistedMessages, currentUserId, placeholderId)
  researchHistoryTxt = buildResearchHistory(history)          # §7.3
  synthHistory       = buildSynthesisHistory(history, limit=8)

  outcome = runTurn(collectionId, researchHistoryTxt, query, events, isCanceled)

  if outcome.status == CANCELED:  mark canceled; return

  if outcome.status == NO_INFO:                          # 9.2 turn pruning
    markUserTurn(exclude_from_history = true)
    write(placeholderId, outcome.reason)
    meta(placeholderId, { exclude_from_history: true, research_steps: outcome.trace })
    return

  if outcome.status == RAW_ANSWER:                       # 5.1 path 5
    write(placeholderId, outcome.rawAnswer
              ?? "I couldn't find enough specific information to answer that accurately.")
    return

  # SYNTHESIZE
  citationMap = {}
  for i in outcome.package.indices:
    e = store.resolve(i)
    if e: citationMap[stringify(i)] = {documentId, sourceTitle, chunkIndex}
  writeCitations(placeholderId, citationMap)             # 9.1 BEFORE streaming

  for delta in synthesize(collectionId, synthHistory, outcome.package, store,
                          query, isCanceled):
    if isCanceled(): break
    if firstDelta: write(placeholderId, "")              # 9.3 reveal
    if delta.reasoning: appendReasoning(placeholderId, delta.reasoning)
    if delta.text:      appendContent(placeholderId, delta.text)   # throttled writes OK

  # 9.4 close the synthetic tool call in the persisted trace
  trace = outcome.trace + Message(tool, content = finalAnswerText,
                                  toolCallId = idOfFinalizeCallIn(trace),
                                  name = "finalize_research_response")
  meta(placeholderId, { research_steps: trace })
```

### 9.1 Citations before streaming
Citations are persisted from `package.indices` **before** the answer stream starts, so the
UI can render "Sources" while text is still arriving and can survive a mid-stream crash.
Shape: `{ "<chunkIndex>": {documentId, sourceTitle, chunkIndex} }`. The answer body keeps
the tags; the map is what turns a tag into a clickable source.

### 9.2 Turn pruning (`exclude_from_history`)
On `NO_INFO`, both the user message and the assistant turn are flagged
`exclude_from_history`, so failed turns never re-enter either agent's context. Without
this, a "no information found" turn poisons later research: the model reads its own past
failure as a signal and gives up earlier. Every history builder must honour the flag.
This is the mechanism behind path 2's "stop and prune this turn".

### 9.3 Reveal timing
A placeholder assistant message exists before the loop runs (for the typing indicator).
It stays **empty until the first synthesis delta**, which is when the indicator is replaced
by text. Research progress must be communicated via `events.status()` on a separate channel,
never by writing into the answer. Status strings in the reference:
`Starting research...` → `Analyzing context (Iteration N)...` →
`Searching library for "<keywords>"...` → *(synthesizing: indicator)* → clear.
Surface these; they are the main transparency the user gets during a multi-search turn.

### 9.4 Trace persistence
Persist `research_steps` = the loop transcript **excluding** system and seeded user prompt,
and append a synthetic `tool` message whose `content` is the final answer and whose
`toolCallId` matches the `finalize_research_response` call in the transcript. This keeps the
stored trace a valid OpenAI-shaped conversation (every tool call has a matching result), so
you can replay, debug, or show a "how it researched" panel without special-casing. The trace
is never fed back to either model.

---

## 10. Tunables

| Knob | Default | Effect of raising |
|---|---|---|
| `MAX_ITERATIONS` | 5 | Deeper multi-hop research; cost ≈ +`FINAL_TOP_K × chunk` tokens per extra turn; diminishing returns past 3 for most corpora |
| `RECALL_TOP_K` | 10 | Better reranker input coverage; more rerank cost & scoring time |
| `FINAL_TOP_K` | 5 | More evidence per turn; bigger context in *both* the tool result and the synthesis prompt |
| `HISTORY_MESSAGES` | 8 | Longer conversational memory for follow-ups; more prompt; research history also grows linearly |
| `chunkSize` / `chunkOverlap` (words) | 100 / 50 | Bigger chunks = more per-hit context, coarser citations, fewer rows to scan |
| `tool_choice` | `"required"` | `"auto"` lets the model answer from memory — do not relax; it defeats grounding |
| temperature | unset (server default) | Unset in the original. Low it (≤0.3) for the research agent if your model rambles or re-searches in circles; leave the synthesizer's server default |
| HTTP timeouts | 5 min receive / 30 s send | Sized for slow local CPU inference, not for an API. Lower aggressively for hosted models |

---

## 11. Porting checklist (non-obvious decisions)

1. Two LLM calls, not one: tool-calling non-streaming → generative streaming.
2. Research agent's prose is discarded; it is a retrieval planner only.
3. `finalize_research_response` returns **indices only** — evidence is resolved later, out-of-band.
4. `tool_choice="required"` + "ONLY output tool calls" + prose fallback (path 5). All three.
5. Mandatory-first-search gate: refusal-shaped tool message, not an abort.
6. `keywords` embeds, `query` reranks. Never swap.
7. Registration order = relevance; display order = `(docId, chunkIndex)`. Both.
8. Stable, dense, 1-based per-turn chunk ids; dedupe on trimmed content; reset per turn.
9. Evaluation note appended to every search result — it is what drives iteration.
10. Research history = flattened text in one user message; synthesis history = real role messages. Different builders.
11. `exclude_from_history` on no-info turns (user *and* assistant).
12. Citations persisted before streaming; `[[Chunk N]]` is a key, hence the strict grammar.
13. Salvage-on-exhaustion: chunks found + budget spent ⇒ synthesize anyway (path 3).
14. Never throw on model-shaped errors: bad args → `{}`, unknown tool → tool message, empty hit → nudging text.
15. Trace persisted as a valid OpenAI transcript with the finalize call closed by the final answer.
16. Reranker is optional and degrades to uniform scores; the loop must work without it.
17. Cancellation checked at iteration top; add mid-await cancellation if your UX demands it.
18. Context growth over iterations is the design, not a bug — budget `MAX_ITERATIONS` accordingly.

---

## 12. Acceptance tests

Drive with a scripted/fake LLM returning canned tool calls, so the loop is testable
without a model. Assert on `ResearchOutcome.status`, `package.indices`, tool-message
contents, and the synthesis prompt.

| # | Given | Then |
|---|---|---|
| A1 | iter1 `query_knowledge_base` → iter2 `finalize([1,3])` | `SYNTHESIZE`, indices `[1,3]` |
| A2 | first call is `finalize` | tool message is the gate ERROR; store untouched; search follows |
| A3 | only `no_info_found(reason)` after one search | `NO_INFO`, `reason` preserved verbatim |
| A4 | search ×5, never finalizes | `SYNTHESIZE` with **all** registered indices (path 3) |
| A5 | 5 iterations, every search empty | `NO_INFO` + depth message |
| A6 | reply with no tool calls | `RAW_ANSWER` with that content |
| A7 | `finalize` with a hallucinated index (e.g. `99`) | answer synthesizes; `99` silently dropped |
| A8 | same chunk returned by two different searches | one id, one registration, `[[Chunk N]]` stable in both tool results |
| A9 | unknown tool name | error-shaped tool message; loop continues; no exception |
| A10 | malformed `arguments` JSON | treated as `{}`; loop continues |
| A11 | cancel true at loop top | `CANCELED`, nothing written |
| A12 | reranker endpoint returns 500 | search still returns `FINAL_TOP_K` results in vector order |
| A13 | inspect synthesis prompt | contains exactly `package.indices`' chunks, `(docId, chunkIndex)` order, `[[Chunk N]]` labels, `### User Query:` block, and **no** research trace |
| A14 | inspect persisted trace | every tool_call has a matching tool message, incl. the synthetic finalize→answer pair |
| A15 | no-info turn, then a new query | `buildResearchHistory` emits neither the user query nor the failed answer |
| A16 | follow-up "and what did *he* do then?" | history present in the seeded user message; keywords reflect resolved pronouns (needs a real model) |
| A17 | two turns in one session | ids restart at 1 for turn 2 (no leakage between turns) |

---

## 13. Deliberately excluded

**Specialist/sub-agent layer**: `AgentPlugin` / `plugins_provider` and the five specialists
(graph, code, flashcard, quiz, canvas), `delegate_to_*` tools, `PluginMode`/`pluginModes`,
`PluginResult`/`ArtifactContent`, the `### ARTIFACT_CONTENT:` injection in the synthesis
prompt, the mandate-appending/`replaceAll`-stripping dance on the user query, per-plugin
workbench tabs, the Riverpod/`Ref` coupling inside `ChunkRegistry`, and the `Syncable`/sync
layer. Their only touchpoints on the loop are the `else` branch at 5.4 (unknown tool →
error) and two `[PORT: drop]` prompt lines. Nothing else needs to know they existed.

**Single-pass (`lite`) path**: the reference's cheaper one-call fallback (`AiMode.lite`,
`_handleLiteRag`, and the forced mobile-internal branch) — it skips the loop entirely,
embeds the raw query, takes top-3 with no rerank, allocates positional chunk labels, and
inlines a 2-message history block into the user message. Out of scope here: implement the
full loop only. The one thing worth reusing from it if you ever need a degraded path is
that it shares every retrieval component in §6.
