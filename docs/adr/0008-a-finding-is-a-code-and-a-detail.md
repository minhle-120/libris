# A finding is a code and a detail, and there are only two codes

A source's non-fatal findings travelled as `[]string`. The pipeline appended sentences —
`"pdftoppm not found: PDF page images skipped"` — the service passed them on, the store marshalled
them, and the UI joined them with `·` under the row. Nothing in that path could tell a person what
to *do*, and the worst case was not cosmetic: a PDF on a machine without poppler ended up
`unsupported — PDF produced no segments`, which reads as a verdict about the file when the file was
fine and the machine was short of a program.

Typing the finding is what lets the row say whose fault it was, and lets one sentence point at the
fix.

## Decision

`domain.Finding{Code, Detail}`, with two codes and no third:

- **`tool_missing`** — a program libris shells out to is not on PATH. The only kind whose fix
  lives outside this program, and the only kind where re-running the file is guaranteed to be worth
  doing. The UI renders it as the action: *install pdftoppm, then run it again*.
- `stage_failed` — a stage gave up on this one file while the rest of the run stood. Re-running is
  a guess, so the row says what was lost (`page images: …`) and stops there.

Anything that would need a third code to explain is not a finding, it is the outcome, and it
arrives as `Detail` beside a status. So three sites that appended a warning *and* set `Unsupported`
are deleted outright — a row does not say the same event twice.

Because the kind is known, a PDF that produced nothing now blames the right thing: if every finding
is `tool_missing`, the row says `no PDF tools on this machine`; otherwise `PDF produced no
segments`. The findings underneath name which programs.

`Detail` stays free text — which program, or what the stage reported. It is for a person reading a
row. A program switches on `Code` or not at all.

Re-running a source stays offered for every source that is not processing, findings or none. The
pipeline changes underneath a corpus, so "run it again" is a real action for a healthy row too;
what the code changes is that the row now says which kind of redo it is asking for.

## Considered Alternatives

- **One code per tool (`no_pdftoppm`, `no_pdftotext`) plus `no_ocr`, `no_text_layer`…** Rejected:
  each new tool or stage adds a code, and every consumer has to know the whole list to answer the
  one question anyone asks — "can I fix this?". The tool's name is data, and it is already in
  `Detail`.
- **`Retryable bool` on the finding.** Rejected: it is a second, derived vocabulary that can
  disagree with the first, and today it is exactly `code == tool_missing`. Add it when a second
  code genuinely retries.
- **Keep prose and pattern-match it in the UI.** Rejected: it puts a Go error format inside a
  product contract, and the matching would live in the one place least able to notice the producer
  changing.
- **A migration to rename the column and re-shape the JSON.** Rejected: migrations here are
  append-only, the column name is not something anyone reads, and findings are re-derivable by
  re-running. A row written before this change therefore decodes to *no findings* rather than to
  invented codes — the strict read is deliberate: guessing a code for a stored sentence would tell
  the UI to offer an action nobody verified.

## Consequences

`internal/ingest` no longer decides what the sentence will be; it decides which kind of thing
happened. The sentences a person reads live in one place in the UI, which is where a copy change
belongs. And a pre-existing row loses its findings line on upgrade — nothing else about it changes,
and "run it again" gets the line back.
