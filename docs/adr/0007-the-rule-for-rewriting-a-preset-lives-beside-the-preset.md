# The rule for rewriting a preset lives beside the preset's format

`Supervisor.Start` held the whole rule inline: read the preset file, compare it with what libris
derives, write a new one if they differ, then read the file back so the report and the process
agree. Two of its tests pinned that rule by starting a fake `llama-server` and reading the file
afterwards, so the assertions described how a process is spawned rather than when a user's file
may be regenerated — and a change to spawning could break them for no reason.

The comparison is deliberately narrow, because the file is the person's own: only which models
exist, where their files are, and which device to offload to are libris's to know. Every other key
is llama.cpp's to obey, and a `cache-ram` edited by hand is meant to survive. That is a rule about
the file's format, and it was sitting in the module that knows least about it.

## Decision

`refreshPreset(path, device, models) (*PresetFile, error)` in `preset.go`, next to `Preset` (write)
and `ReadPreset` (read) — the three things the format implies, in one file. It answers with what
the file now says, which is what the router is about to serve, so a caller reporting the running
set cannot report a derivation the server never got. `Start` asks that one question.

It is unexported. `Supervisor` is its only caller, and exporting it would put a second way to
regenerate a user's configuration on the package's interface for no caller. Its tests are in
package `engine` for the same reason, and they need a file and a string: missing is written, an
agreeing file is left byte-for-byte with its hand edit, each of the four derived moves rewrites,
and a file that cannot be read is an error rather than an absence, because overwriting it would
delete an edit nobody has been told about.

The two `Start` tests that duplicated the rule are deleted. One remains — a model that downloaded
while the router was stopped is in the running set — because that is the join between the two
modules, and it is a fact about starting.

## Considered Alternatives

- **`(*PresetFile).Drift(device, models) bool`, leaving the write-and-reread in `Start`.**
  Rejected: the ordering is the rule. A predicate moves half of it beside the format and leaves the
  other half — and the "read back, don't trust what you built" step — in the caller.
- **Split `internal/engine` into a release module and a process module.** Rejected for now: the
  preset module has exactly one consumer, so a package seam would hold one adapter on each side,
  which is indirection rather than a seam. The two modules are named where a reader looks —
  `preset.go` for the file, `serve.go`/`running.go`/`port.go` for the process — and if a second
  consumer ever wants the format, the package split is then a move, not a redesign.

## Consequences

`Preset` and `ReadPreset` stay exported only because their tests are in the external test package;
nothing outside `engine` calls them. Unexporting them and moving those tests in is open cleanup,
and the reason it is not done here is that it is a rename with no decision in it.
