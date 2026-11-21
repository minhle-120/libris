# 0001 — Configuration is defined once, in Go

Date: 2026-11-09
Status: accepted, implemented (`internal/config`, `web/src/lib/config.gen.ts`)

## Context

Settings were written twice: a Go struct, and a hand-maintained TypeScript
`Settings` type with its own `defaultSettings()` and hand-built settings rows.
Two copies of the same default (DPI 150, chunk size 100, max file 512 MB) means
two places to forget, and the UI had no way to know the backend's real ranges.

## Decision

`internal/config` is the only definition of a setting. A field is added there
once, with UI metadata in struct tags:

```go
PDFDPI int `key:"pdfDPI" group:"ingest" label:"PDF raster DPI"
           help:"Resolution for page images." min:"50" max:"600" unit:"dpi"`
```

`internal/config/schema.go` reflects over those tags and emits
`web/src/lib/config.gen.ts` (types, defaults, groups, field metadata).
`SettingsView.svelte` renders that metadata generically; it contains no labels,
defaults, ranges or enum options of its own.

Validation also reflects over the same tags, so ranges exist once and are
enforced by `Validate()`.

`make generate` regenerates; `TestGeneratedSchemaIsCurrent` fails the build when
the generated file is stale, so drift is impossible rather than discouraged.

## Consequences

- Adding a setting is one edit plus `make generate`.
- The frontend cannot invent a setting the backend does not have.
- Enum options (currently only PDF mode) live in `config.Enums()`.
- Environment variable names are derived from the key: `pdfDPI` →
  `LIBRIS_PDF_DPI`. Renaming a key renames its env var.
- Values came from the generated file until ADR 0002 put them on the network. The
  schema came from the generated file either way, which is the part this ADR is about.
