# The router's address is derived in one module

"Where does a model request go, and what does the thing at the other end answer to?" had
three answers. `config.Served(port)` turned a stored config into a live address; the client
cache in the wiring layer decided when that client was stale; the app's host decided what the
settings page was told. Twenty-one places touched a port, each with a comment explaining its
own staleness rule, because the rule had been copied rather than placed.

They had already diverged. The page reported a router as stopped unless its status said
running; the wiring reported a port whether or not it was running. So a client could be built
for a process that had died, and the same screen could show an endpoint from the running
process beside a port number read from the settings. The fact is one thing; it is now asked
once, in `internal/serving`, and both the client and the page read the same answer.

## Considered options

- **Derivation only, liveness left to the callers** — rejected: the arithmetic was never the
  expensive part. Three copies of "is the router up" is what produced the bug, and a shared
  function that each caller feeds a port it fetched itself keeps every fetch rule separate.
- **Let the module own the llama.cpp process too** — rejected for now: it would merge the
  supervisor and its preset into the module that reads them, which is a larger question than
  "who answers where". If a second reader of that process appears, the boundary gets revisited.

## Consequences

Managed mode with nothing running is a value, not an empty string: a client fails with
`serving.ErrNotRunning` instead of dialling a hole and reporting the socket error as a server
that refused it, and the settings page reads the same result to say "stopped, and it would
live here". That is why the module returns a usable answer alongside the error.

The settings struct no longer has a method that quietly becomes a live address. A caller must
ask serving and then apply what it got — which is the same shape as before at the call site,
and a different claim about who knows what.

A future model role, or a third address, is one module to change. `config`'s URL and name
fields are external-mode settings and nothing else; `serving.Aliases()` is the list the page
shows, so it cannot drift from the preset that was written.
