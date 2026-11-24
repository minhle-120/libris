# libris

## What this is

A local-first RAG notebook. Sources — PDFs, images, text — go into a notebook, get
chunked and embedded, and a question about them is answered by a model call that cites the
segments it was given. One machine runs the UI, the database and the model servers; nothing
leaves localhost. `cmd/libris` wires it, `internal/` holds the modules, `web/` is the Svelte
UI that `web/embed.go` bakes into the binary.

## Agent skills

### Issue tracker

Issues live in GitHub Issues for `minhle-120/libris`, via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary — `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: root `GLOSSARY.md` + `docs/adr/`. See `docs/agents/domain.md`.

## Documentation

Nothing is written ahead of the thing it describes. `GLOSSARY.md` and `docs/adr/` appear only
when a term or a decision actually gets resolved — in the conversation where it is argued, not
in a write-up afterwards — and it is fine for years to pass with neither file existing.

- A comment that wants to cite a decision restates the reason instead. Cite no document that
  does not exist yet: a pointer to something nobody will write starts decaying the day it is
  written. (The repo carried a 4,500-line spec set until 2026-11-24; the code outlived it and
  was left pointing at nothing.)
- This file stays a list of pointers. Explanation belongs beside the code it explains.

## Where to read (in this order)

- **A decision and its reason** — `docs/adr/`, one file per accepted decision, numbered as
  they are written. `ls docs/adr/` is the index; a reversal is a new file that names the old.
- **How a module works, and why it is shaped that way** — the package and the file it is in.
  Start at `cmd/libris/main.go` for the wiring, `internal/app/app.go` for the use cases both
  transports share, `internal/chat/loop.go` for the two-call answer path.
- **A setting** — `internal/config/config.go` is the only place settings exist, values and UI
  metadata together. `web/src/lib/config.gen.ts` is generated from it by `make generate`, and a
  test fails the build when it is stale: never hand-edit it, never add a setting in the frontend.
- **What a model server is promised over the wire** — `internal/llm/wire.go`, checked against
  the clone in `references/llama.cpp/` rather than remembered. That directory is a 224 MB
  upstream checkout, ignored by git and excluded from greps.
- **How the repo is checked** — the `Makefile` targets, and `docs/adr/0001` for why the tests
  are not in CI.

## Running it

`make build` builds the UI and compiles it into `./libris`, so `./libris serve` runs what the
tree says. `go build` and `go run` read `web/dist` through `//go:embed` directly, so they need
`make web` once beforehand — with that directory absent the embed fails as a pattern error,
which reads like a missing file rather than a missing build.

`make check` before a commit. `make e2e` and `make ui` where the change reaches the filesystem
or the browser, and the smallest gate that covers it while iterating — see `docs/adr/0001`.
