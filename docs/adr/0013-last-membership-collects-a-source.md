# 0013 — A source dies with the last notebook that uses it

Date: 2026-11-22
Status: accepted, implemented (`internal/store/sqlite/collect.go`, `app.Service.collect`,
`web/src/components/SidePanel.svelte`) — narrows the non-negotiable *originals are always
kept* (`AGENTS.md` §2) and supersedes one consequence of ADR 0003 (*nothing
garbage-collects*). It does not reverse ADR 0003's model or ADR 0008's citations.

## Context

ADR 0003 made sources content-addressed and shared, and left their files immortal
because nothing had yet reasoned about when a file may be collected. So
deleting a notebook and detaching a source produce a data directory that only grows:
detaching the last notebook from a PDF leaves its row, its segments, its vectors and every
page raster it spawned, unreachable through any screen and re-made from scratch if the
file is ever offered again.

That is a leak the user cannot see or fix. It is also the easy half of the problem — the
hard half is that the same bytes may be cited by three notebooks, and a collector that
guesses takes an answer's evidence with it. ADR 0008 says a citation keeps its own copy of
the text and the locator, so a *stored answer* survives; what does not survive is the
promise underneath it, that a citation can open the original.

## Decision

**Membership count is the reference count, and it is the only one.** When a `DELETE`
drops the last `notebook_sources` row for a source — by deleting a notebook or by
detaching a source — the source's row, its segments, its vectors and its asset rows go in
the same transaction, and the files are unlinked afterwards.

- **Files are unlinked after the commit, never inside it.** A file deleted while its
  citations still resolve is a broken answer; a file left behind because the process died
  between the commit and the unlink is only disk, which is the failure mode this app has
  always had. The order is therefore not a preference.
- **The store answers with the paths** (`domain.Orphan`): the database is the only thing
  that knows which rasters a source owned, and after its own rows are deleted it knows
  nothing. `collectOrphans` runs inside the caller's transaction for the same reason —
  the memberships that just died and the rows they protected are decided in one commit.
- **The subtree is deleted, not only the listed files.** An ingest that died between
  writing a raster and storing its row leaves a file no query can name; the directory
  named after the source hash is the only thing that can find it.
- **A path is refused, not trusted.** Every unlink checks that the path lies inside the
  data directory it belongs in, and a source id that is not 64 hex characters gets no
  recursive delete. This is the only code in libris that removes bytes the user did not
  ask it to remove, so it obeys the rule `DirSink` already applies to names.
- **A shared source is untouched**, rows and rasters alike. This is the common case and it
  is why nothing visible changes for a file two notebooks use.
- **The UI says which of the two the click is**, from `notebooks_using` on the source
  read, and asks first only when the answer is "this one deletes it". Two of the three
  confirmation boxes that existed afterwards were folded into `lib/ConfirmDialog.svelte`,
  so that "the safe control is the focused one" is a rule written once.

## Consequences

- Deleting is honest about what it costs. The notebook confirmation no longer promises the
  files survive — it says which go and which stay, and the app is checked against that
  sentence at three levels: the reference count in the store, the bytes on disk in `app`,
  and the real binary's filesystem in `make e2e`.
- A source that a user re-offers after deleting its last notebook is re-uploaded,
  re-chunked and re-embedded from nothing. That is the price of collection, and it is paid
  only for a file no notebook wanted any more.
- The rule is *last membership*, not *no citations*. A stored answer keeps quoting a source
  that no longer exists — its chip opens nothing, because the answer holds its own copy of
  the text (ADR 0008). Making that legible is UI work, not schema work.
- `DetachSource` and `DeleteNotebook` now answer with what they orphaned. Any future
  transport that deletes must keep the same two-step shape, and a caller that ignores the
  orphans leaks exactly what this ADR set out to stop.
- Unsettled, and deliberately untouched here: an ingest interrupted at the wrong moment
  can still leave a directory behind, since the collector only runs when a *membership*
  dies. A sweep at startup for source directories with no row would catch it, and would
  need to tolerate a directory whose ingest is running right now.
