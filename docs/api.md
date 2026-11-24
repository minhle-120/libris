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
PATCH  …/chats/{chatID}                   {"title":…} → 204
GET    /api/notebooks/{id}/chats/{chatID}/messages
POST   /api/notebooks/{id}/ask            SSE, see below
GET    /                                  embedded SPA, index.html fallback
GET    /api/host/engine                   installed + newest + progress; `?check=1` asks
                                          GitHub again now
POST   /api/host/engine                   {"backend"?,"tag"?} → 202 with the tag it chose;
                                          409 while the update check is still running, 502
                                          with the reason when there is no answer
POST   /api/host/engine/stop              ends a download and keeps its bytes to resume
DELETE /api/host/engine                   the engine, unless a server is running on it
DELETE /api/host/engine/parts             kept download bytes, and their resume records
GET    /api/host/devices                  what the machine has, cached for a night
GET    /api/host/engine/serve             libris's own router: running, port, models, device
POST   /api/host/engine/serve             start it, and wait until the router answers — so a
                                          200 is a server that replied, not a process that
                                          exists. 409 already running (autostart makes this
                                          ordinary), or did not start: port taken, a model
                                          that would not load. The sentence is the router's own,
                                          read off its log
DELETE /api/host/engine/serve             stop it. Not running is the state the caller wanted,
                                          so it is a 200 and not a 404
GET    /api/host/models                   the catalogue: every entry, its bytes, whether the
                                          files are on disk and verified, and the download
                                          progress folded in
POST   /api/host/models                   {"id":…} → 202 with the whole view; 409 another
                                          download is running; 422 an id the catalogue
                                          does not have
POST   /api/host/models/stop              end a download and keep its bytes to resume
POST   /api/host/models/discard           end it and throw those bytes away
DELETE /api/host/models/{id}              the entry's whole folder. 409 while the router has the
                                          model open — nothing deletes what something is using
                                          (ADR 0013's rule, said about a process)
```

Managed mode's five endpoints share one rule with the rest of the API: **a call that answers
returns the state it just caused**, so a 202 carries the whole view and the page does not have
to ask again to find out whether its own request worked.

Neither host endpoint waits on GitHub. `GET /api/host/engine` answers from the local record
in microseconds and starts the lookup behind the response, saying `{"checking":true}`; the
page polls and the answer lands in a later view. This is not a performance choice — a
measured `api.github.com` round trip here is seven seconds, which is longer than the browser
waits for a settings page and longer than anyone will stand looking at one.

Planned, not built: SSE progress for ingest instead of the UI's polling, and `/mcp`.

## Status codes are decided in one place

`Server.fail` maps `domain.ErrNotFound` → 404, `app.ErrInvalid` → 400,
`app.ErrTooLarge` → 413, `app.ErrBusy` → 429 with `Retry-After`, everything else → 500
plus a log line. Nothing else in the transport guesses at statuses, which is why a use
case picking the wrong sentinel is a visible bug: a bare `errors.New` from `app` is a
500 about a user's typo.

The `/api/host/*` handlers map their own sentinels, because the states they report are not
retryable-in-a-moment and 429 would be a lie: `app.ErrEngineMode` and `app.ErrInvalid` → 422
(the fix is a setting, or a different id), and `app.ErrEngineRunning`, `app.ErrEngineStart`
and the model-in-use refusal → 409, which is what "the world is not in the state this request
assumed" means. Each carries a sentence a person can act on — a port to change, a server to
stop, an id that exists — because the page shows the message and not the status.

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

`POST /api/notebooks/{id}/ask` takes `{"question":…, "chat_id":…}` and answers
`text/event-stream` with `status`, then
`citations`, then `delta`s, then `done` — in that order, because the order is the
contract: the citations arrive before the prose that rests on them, so the UI can show
what an answer is grounded in while it is still being written.

| Frame | Body | |
|---|---|---|
| `status` | `{"text":…}` | what the loop is doing; also carries reasoning |
| `citations` | `[domain.Citation]` | frozen, before any prose |
| `delta` | `{"text":…,"reasoning":…}` | answer text and reasoning travel apart; reasoning is never appended to the answer |
| `done` | `domain.Message` | the stored row, so the client reconciles with the server rather than with its own accumulation — including its `chat_id`, which is how a question asked with no chat finds out which one it started |
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
