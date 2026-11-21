# 0009 — One POST, one stream, and hanging up is the cancellation

Date: 2026-11-12
Recorded: 2026-11-21, after the fact.
Status: accepted, implemented (`internal/httpapi/chat.go`, `internal/app/chat.go`)

## Context

A question takes tens of seconds (ADR 0007: several research rounds, then synthesis), and
over that period four different kinds of thing become true: the loop is doing something
legible, the evidence is known, prose exists, the turn is over. The previous transport —
upload returns 202 and the browser polls — cannot carry prose, and gives the UI a clock
that is not the server's.

What was considered:

- **Poll a job id.** Needs a job resource, a retention policy for finished jobs, and it
  still cannot stream deltas; you end up animating text locally and pretending.
- **WebSocket.** Bidirectional, which is the only argument for it, and the client sends
  nothing after the request body. It costs an upgrade path, a session identity to route a
  cancel to, and reconnection logic in a browser page that is already re-fetching a thread
  when it reloads.
- **SSE on the POST itself.** HTTP, no new protocol, ends when the connection ends.

A stop control was also needed, and the reflex is a second endpoint.

## Decision

`POST /api/notebooks/{id}/chats/{chatID}/ask` answers `text/event-stream` and that is the
whole transport. **There is no stop endpoint**: the request context is the cancellation, so
closing the stream ends the turn.

Frames arrive in a fixed order and the order is the contract, not an optimisation:
`status` → `citations` → `delta`* → `done`. Citations precede the prose that rests on them,
so the UI can show what an answer is grounded in while it is still being written. `done`
carries the stored `domain.Message`, so the client reconciles with the database instead of
with its own accumulation of deltas.

Two consequences of using a stream rather than a request:

- Once the stream is open there is no status code left to set. Before it opens, the single
  `fail` mapping applies as everywhere else; after, a failure is an `error` frame.
- Cancel is a disconnection, so it cannot be told apart from a network drop. That is
  accepted, because both mean the same thing to the server, and the row records what
  happened either way: `canceled` carries whatever was streamed plus the citations written
  before the stream, and `interrupted` carries the same for a failure the user did not
  cause. A turn's final writes deliberately run on `context.WithoutCancel` with a short
  timeout — the context that ended is exactly the one whose record the user stopped to
  look at.

## Consequences

- One transport for the entire answer path, and a Stop button that cannot fall out of sync
  with the server because it does not tell the server anything.
- `internal/app` must treat a dead context as a normal outcome with a row to write, which
  is why the cancellation logic lives there and not in the handler.
- The client has to handle a stream that ends without `done`, and an `error` frame that
  arrives after some deltas. `ChatPanel` sidesteps the disagreement by re-reading the
  thread when the stream ends.
- Ingest keeps its 202-and-poll shape. A progress stream for ingest is planned and will
  reuse this pattern rather than invent a second one.
