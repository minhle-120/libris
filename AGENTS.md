# libris

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
