# The setup rule lives in one Go package, and the device confirmation is a setting

The settings screen and the wizard answered "can this machine answer a question yet" from
three polls — engine, models, devices — with the combination written in `web/src/lib/setup.ts`.
`EngineAutostart` ran a shorter version of the same question in Go. The page's version had grown
to carry the reasons for each of its clauses in comments, because each had been a bug: a device
step that passed before anyone saw the row, a router that came up holding an embedder and no chat
model. None of those reasons were fixable there. The page had the same facts the program did, and
still disagreed with it, because the two rules were written twice.

The confirmation that stopped the device step passing early was stored in `localStorage`, which
gave it the wrong life: it belonged to a browser profile rather than to a machine, so logging out
of the browser re-asked and copying a data directory to another machine did not. Worse, it could
not hold the state that mattered — "nobody has been asked yet" — because the field it inferred
from was blank for both that and "libris picks a device".

## Decision

`internal/setup` holds the rule: `For(facts)` answers with the first step to act on and everything
still blocking; `Startable(facts)` answers whether start-up may bring the router up, which is yes
only when the model server is the one thing missing. The page renders what `/api/host/engine`
returns; it composes nothing. `EngineAutostart` calls the same functions and logs the rule's own
answer when it stands down.

The confirmation is a setting (`engineDeviceAck`), stored beside the device it followed, holding
the key the server publishes: the engine build's tag and the machine's fingerprint. The page sends
that key back unchanged rather than computing it, so "confirmed against what" is written once.
Blank means nobody was asked, and no rule reads the device setting to guess.

## Considered Alternatives

- **Keep the composition in TypeScript and fix its bugs again.** Rejected because the disagreement
  is not a bug to fix: with the rule in two languages there is no place to fix it once, and the
  program that acts on it cannot see the copy that was wrong.
- **Let start-up read the page's answer over HTTP.** Rejected because it makes boot depend on a
  render, and the answer would still be computed in a language the caller cannot call.
- **Keep the ack in the browser and fix what it is keyed to.** Rejected because it stayed
  per-profile: the fact is about this machine's engine build, and the data directory is what a
  person moves when they change machine.
- **Infer "asked" from a settings timestamp.** Rejected because a timestamp says something happened,
  not that a person saw the row — the ambiguity the whole field exists to remove.

## Consequences

`engineDeviceAck` is a settings field nobody types, so the page hides it and the record contains a
machine-specific key — copying `settings.json` to another machine re-asks, which is the correct
answer and not the one a portable settings file suggests. `config_test.go` asserts the setting's
shape, including that blank answers "nobody was asked". The web harness that drives the wizard
(`scripts/ui-setup.mjs`) needs an installed engine to reach the device step; point it at a data
directory where libris has downloaded the upstream Vulkan build, not at a locally built
`llama-server`, which reports the devices that build was compiled with.
