# 0024 — The port the model server binds is chosen at start

Date: 2026-11-24
Status: implemented (`internal/engine/port.go`, `internal/config` `enginePort`, `app.Host.ServePort`)

## Context

Managed mode ran llama.cpp on port 8080 because that is the number in llama.cpp's own
documentation, and `enginePort` existed as a setting so a person could move it. Two things
went wrong with that, one ordinary and one embarrassing.

The ordinary one: 8080 is the most contested port on a developer machine. Vite's fallback, a
thousand sample apps, half of Docker Hub's quick starts. When something else held it, libris
said *"Something else is already using port 8080. Change the port in Settings"* — correct
advice, but only after a start had failed, and the user had to go and find a number to type.

The embarrassing one is in this repo's own documentation: `AGENTS.md` shows
`./libris -addr 127.0.0.1:8080` as the example of running libris on a chosen address. libris
and its own model server were both configured for 8080 by default, in the same process, on
the same loopback interface. It worked only because the engine's bind loses and reports the
loss.

Capacity has always been llama.cpp's to decide (ADR 0011) and the preset its configuration
file (ADR 0022, 0023). The port is not capacity and not model configuration: it is which
socket this process owns, and libris is the only party that knows what else it is running.

## Decision

**The port is a fact about the running process, so the process answers for it.**

- `enginePort` defaults to **0 = Auto**. libris asks the kernel for a free port (bind `:0`,
  read the number back, let go), hands it to llama.cpp as `--port`, and records it.
- A **non-zero** port is honoured exactly, tried **once**, and a collision fails with that
  number named.
- `port` is **not a key in the preset**. It is a command-line argument, so a start on Auto
  never rewrites the file — which keeps ADR 0023's promise that hand edits survive until a
  model or the device moves.
- Managed mode's three addresses are derived from **the port the router reports**, not from
  `enginePort` (`Config.Served(port)` takes it as a parameter; `Host.ServePort` reads the
  record). With nothing running and the port on Auto there is no address, and the answer is
  `app.ErrEngineDown` — a sentence — rather than a client aimed at a port nobody holds.
- `deviceinfo` remains the only device probe (ADR 0019); this changes nothing about how the
  machine is asked, only what it is asked to run on.

## Why not `--port 0`

llama.cpp supports it: `server_http_context::start()` calls `bind_to_any_port()` and adopts
the number, so the kernel would do the collision avoidance directly. It is still the wrong
choice here, because the chosen number then exists **only in that build's startup log**, and
libris downloads unpinned builds (ADR 0018). Parsing a log line to learn the address every
model request is aimed at makes a wire claim about a format upstream is free to change, and
the failure mode is a libris that cannot find its own server.

Probing the port locally costs one socket, is understood on Windows as well as Linux, and
keeps the number on this side of the process boundary.

## The race, stated rather than hidden

Binding `:0` and letting go is not atomic with llama.cpp binding the same number, so a
collision is possible in the millisecond between. That is not a reason to avoid the approach
— every "is this port free?" check on every machine has this race — it is a reason to keep
the retry: **Auto tries up to four ports**, and a start that loses all four says so in the
sentence "no free port could be found", which is a different fact from "8080 is taken" and
carries different advice.

A pinned port is never retried. The reason to name a port is usually that something else was
already told that address; binding a different one quietly would break the thing the pin was
for, and report success while doing it.

## Consequences

- **The address is not stable across starts.** It is on the settings page, next to the
  running badge, and in the pid record. Anything that needs to *dial* the router asks libris,
  and libris asks the process. A person who wants a fixed address still has `enginePort`.
- **Anything new that needs an endpoint asks `Host.ServePort`** — the extension of the rule
  that managed mode's addresses are derived and never stored (ADR 0020). A port saved once
  now goes stale twice: when the port changes, and when the process is restarted on another.
- The port moving changes the llm client's cache key, so a restart on a new port rebuilds the
  client on the next job rather than needing a restart. Same discipline as every other
  derived connection fact (AGENTS §6).
- Managed mode with the server down is now a *named* state on the ask and ingest paths
  (`ErrEngineDown`) instead of a socket error naming a port that was never bound. This closed
  a long-standing sharp edge in the same place the embedding base URL once had it.
- `view.port` is 0 when Auto, and the page says **"a free port, chosen at start"** rather than
  printing `http://127.0.0.1:0`. `view.endpoint` is the running address when there is one, and
  empty when there is nothing to address.
- The settings page's `Server port` field says what the number does, and the engine section
  shows the consequence. The generated form gains no special case: a number whose zero means
  "ask me" is still a number, and `autoZero` in the struct is what lets it keep a floor of
  1024 — ports below that need root on Linux — while allowing the zero through.
