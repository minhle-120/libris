# 0002 — Settings are served and saved over HTTP

Status: accepted

## Context

`internal/config` is the single definition of a setting (ADR 0001), and the
frontend renders the generated schema. Values still had to reach the browser,
and the UI had to persist edits somewhere.

There is no document store yet, and settings are a few dozen scalars read on
every request path.

## Decision

- `config.FileStore` holds the live values in memory and mirrors them to
  `<data>/settings.json`, written atomically (temp + rename, mode 0600 because it
  can contain an API key). Load order: defaults → `LIBRIS_*` environment → file.
- `GET /api/settings` returns the effective configuration.
- `PUT /api/settings` applies a **partial** document: absent keys keep their
  value, unknown keys are rejected with 400, and every write is validated before
  it touches memory or disk.
- The frontend loads on mount, tracks a dirty snapshot, and saves explicitly.
  Schema, labels, ranges and enums still come from the generated file.

## Consequences

- Settings never drift between UI and backend: both are projections of the same
  struct, and a typo in a key is a 400 rather than a silent default.
- A failed write leaves the previous configuration in force.
- No database dependency for configuration; when the document store arrives it
  will not own settings.
- Single-user assumption: no locking across processes, which matches one binary
  owning the data directory.
