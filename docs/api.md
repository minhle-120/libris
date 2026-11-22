# HTTP surface

`internal/httpapi` is the browser's transport and nothing else: JSON, files, the
embedded SPA, and the one streaming endpoint. Handlers do not touch the store — they
call the `API` interface they declare. Routes are registered in `server.go`,
`sources.go` and `chat.go`; that registration is the canonical list, and this page is
the readable version of it.

```
GET    /api/health                        {"status":"ok"}
GET    /api/settings                      23 values, effective configuration
PUT    /api/settings                      partial document; 400 on unknown key / bad range
POST   /api/notebooks                     {"name":…} → 201
GET    /api/notebooks                     ·  GET /api/notebooks/{id}
PATCH  /api/notebooks/{id}                rename ·  DELETE → 204 (its chats too; a source
                                          with no notebook left is collected, ADR 0013)
POST   /api/notebooks/{id}/sources        multipart field "file" → 202 while processing
GET    /api/notebooks/{id}/sources        carries notebooks_using, the count a row's trash
                                          can is worded from ·  DELETE …/sources/{sourceID}
                                          → 204, and deletes the source if it was the last
                                          notebook using it (ADR 0013)
GET    /api/sources/{id}                  status, for the UI to poll
POST   /api/sources/{id}/reingest         rebuild from the original, any state → 202
GET    /api/sources/{id}/segments         chunks + provenance + assets
GET    /api/sources/{id}/original         the kept copy
GET    /api/assets/{id...}                derived files; id contains slashes
POST   /api/notebooks/{id}/chats          {"title":…} → 201
GET    /api/notebooks/{id}/chats          ·  DELETE …/chats/{chatID} → 204
GET    /api/notebooks/{id}/chats/{chatID}/messages
POST   /api/notebooks/{id}/chats/{chatID}/ask   SSE, see below
GET    /                                  embedded SPA, index.html fallback
```

Planned, not built: SSE progress for ingest instead of the UI's polling, and `/mcp`.

## Status codes are decided in one place

`Server.fail` maps `domain.ErrNotFound` → 404, `app.ErrInvalid` → 400,
`app.ErrTooLarge` → 413, `app.ErrBusy` → 429 with `Retry-After`, everything else → 500
plus a log line. Nothing else in the transport guesses at statuses, which is why a use
case picking the wrong sentinel is a visible bug: a bare `errors.New` from `app` is a
500 about a user's typo.

A 202 body is not decoration: the UI decides whether to keep polling from the status in
the response, so an action must return the state it just caused, not the row it read
before causing it.

## Files

**No absolute path crosses the API.** `Source.OriginalPath` and `Asset.Path` are
`json:"-"`: the client asks for `/api/sources/{id}/original` or `/api/assets/{id}` and
never learns where the data directory is. Files are looked up by ID, which is also why
the asset route is `{id...}` — the ID *is* a path, and `DirSink` already refuses `..`
and absolute names.

Both file endpoints stream through `http.ServeContent`, so Range works — that is what
lets the browser seek inside an audio slice or a PDF page without downloading the file.

## A question streams

`POST …/chats/{chatID}/ask` answers `text/event-stream` with `status`, then
`citations`, then `delta`s, then `done` — in that order, because the order is the
contract: the citations arrive before the prose that rests on them, so the UI can show
what an answer is grounded in while it is still being written.

| Frame | Body | |
|---|---|---|
| `status` | `{"text":…}` | what the loop is doing; also carries reasoning |
| `citations` | `[domain.Citation]` | frozen, before any prose |
| `delta` | `{"text":…,"reasoning":…}` | answer text and reasoning travel apart; reasoning is never appended to the answer |
| `done` | `domain.Message` | the stored row, so the client reconciles with the server rather than with its own accumulation |
| `error` | `{"text":…}` | after the stream opened, there is no status code left to set |

Before the stream opens, the `fail` mapping applies as everywhere else. The request
context is the cancellation — hang up and the turn ends, which is why there is no stop
endpoint to fall out of sync with.

## What the frontend does with it

Sidebar counts come from `GET /api/notebooks`; the notebook view polls that notebook's
source list while anything is `processing`; the chunk inspector reads `…/segments` then
fetches each page raster from `/api/assets/`; `lib/api.ts` is the only module that
talks to `/api`, and multipart uploads must not set a Content-Type because the browser
has to add the boundary itself.
