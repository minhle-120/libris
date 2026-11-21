# 0003 — Notebooks own memberships; sources are content-addressed and shared

Date: 2026-11-10
Status: accepted, implemented (`internal/store/sqlite`, `internal/app`)

## Context

Notebooks had to be built before anything could be stored, and they answer the
question the retrieval design depends on: what is the scope of an answer.

The obvious shape is a folder — each notebook holds its own copies of the files
you put in it. The alternative is a library with links: one copy of each file,
and notebooks pointing at it.

The difference is not cosmetic here. A PDF costs two derived documents: page
text and page rasters at `pdfDPI`. Duplicating a 200-page scan across four
notebooks quadruples poppler's work and the raster directory, and it makes
"re-upload this file here" a multi-minute wait for work that is already done.

Citations also constrain deletion. A chunk in notebook A may cite page 7 of a
file that notebook B also holds. If the file is per-notebook, deleting B is safe;
if it is shared, deleting B must not remove anything A can still cite.

## Decision

- **A notebook owns its chats and its source memberships.** Sources are not
  notebook children. `notebook_sources` is the join, and deleting a notebook
  cascades to its chats and memberships only.
- **A source is identified by the SHA-256 of its contents**, and that hash names
  the stored original: `<data>/originals/<sha256><ext>`. Same bytes means the
  same row, the same file, the same rasters, the same segments.
- **The original is never rewritten or deleted.** Dedupe relies on the path being
  stable, and the non-negotiable *originals are always kept* (`AGENTS.md` §2) says a
  citation must be able to open its source.
- **Re-offering a ready file costs one row insert.** `AddSource` reports whether
  it added anything; if the source is already `ready` nothing is re-run. A source
  that `failed` is re-queued — re-offering the file is the retry affordance.
- **Ingest is asynchronous and serial.** Upload answers 202 with the source in
  `processing`; the browser polls. One worker because poppler, ffmpeg and the
  model server each want most of the machine.
- **SQLite via `ncruces/go-sqlite3`.** cgo-free, so cross-compiling survives, and
  it keeps `sqlite-vec` a possible later addition. Migrations are embedded files,
  one transaction per file, each recorded by version.

## Consequences

- Uploading the same file to a second notebook is instant, and costs nothing on
  disk. Verified against the running binary: three uploads of two distinct files
  produced two originals and two ingest log lines.
- A source's `Name` is the name it was first offered with, because the row is
  shared. Naming the same file differently per notebook needs an alias column on
  the membership — recorded as an open decision, not built.
- Nothing garbage-collects. Deleting a notebook leaves its sources and assets
  behind on purpose; when collection is added it must respect every remaining
  citation, so the safe default (do nothing) stays until it can be reasoned about.
- `Status` is now load-bearing for correctness, not just display: retrieval
  selects `ready` sources only, so a half-ingested file can never be cited. A
  crash mid-ingest leaves `processing`, which the next start sweeps to `failed`.
- The binary roughly doubled (10.5 → 21 MB) because the SQLite driver embeds its
  WASM build. That is the cost of staying cgo-free, and it is the only external
  Go dependency in the project.
