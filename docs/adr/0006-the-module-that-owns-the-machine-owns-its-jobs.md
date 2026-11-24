# The module that owns the machine owns its jobs, and one table maps a machine failure to a status

`internal/httpapi/engine.go` started the engine download itself: it validated the tag against
its own copy of the nightly pattern, resolved "the newest" by reading the view another endpoint
returns, wrote the three sentences for "no answer yet", then spawned

    go func() { s.host.EngineInstall(context.Background(), req.Tag) }()

because the fetch must outlive the request that asked for it — a closed tab, a proxy
idle timeout or a laptop lid must not be the act that stops the bytes. That reason is the
download's. Sitting in the transport meant the transport also owned the half-written engine, and
had to be consulted about it on shutdown. The model downloads already worked the other way:
`models.Fetcher.Start` answers at once and the fetcher owns the job, so the machine had two long
jobs owned by two different modules.

Seven handlers each carried their own `switch errors.Is(...)` mapping a failure to a status, and
they had drifted: a malformed tag was 400 in one handler and what `app.ErrInvalid` means
elsewhere was 422; "the newest build could not be determined" was written three times; and the
transport's tag pattern (`^b[0-9]{4,7}$`) disagreed with the engine's (`^b\d{4,6}$`), so a
seven-digit nightly was offered by one module and refused by the other.

## Decision

`Host.EngineInstallStart(ctx, tag) (string, error)` owns the decision and the job. It refuses a
malformed tag with `ErrInvalid`, resolves an empty tag to the newest it knows (or answers
`ErrUpdateChecking` when a lookup is in flight, `ErrUpdateUnknown` with the upstream sentence
when it cannot be had), refuses to stack on a running download with `ErrBusy`, and only then
spawns the fetch with `context.WithoutCancel(ctx)` — the request's values travel, its
cancellation does not. It answers with the tag it settled on. On completion it invalidates the
device report, which is a step only this module can take: the cached answer describes the build
that was just replaced.

`EngineServeStop` returns the state it left the process in, so the page that asked does not send
a second request to learn what its own request did; `ServeStatus` is gone from the seam.

The HTTP layer keeps the part that is the transport's: which status a failure deserves. That is
`failMachine`, one table for the machine endpoints, written once, passing through the sentence
the app module composed. It is deliberately not shared with the document endpoints, where
`ErrInvalid` means a body that could not be read (400) rather than a request that read fine and
names something that cannot happen (422).

## Considered Alternatives

- **Split `Host` into a report module and an acting module, two packages or two interfaces.**
  Declined after the deletion test. The report assembly (`Engine`, `Devices`, `ModelCatalog`) is
  already deep — one document, one round trip, five sources behind it — and the acting methods
  delegate to modules that own their own state. Cutting the interface in two would move no
  complexity: it would add a second seam with one adapter on each side. What was actually shallow
  got deleted instead: the goroutine, the seven switches, the duplicated pattern, the redundant
  status call. Narrow the interface where a call is genuinely redundant, not where a file is long.
- **Move the job into `engine.Manager` as `StartInstall(tag)`.** Rejected: a finished install has
  to invalidate the device report, and `engine` has no idea that a report exists. The completion
  hook is what decides who owns the goroutine.
- **Let the handler keep resolving "the newest" from the published view.** Rejected because the
  view is a rendering of the release cache, and reading your own output to decide what to fetch
  is how a module ends up with a second, staler copy of a rule.
- **One status table across the whole API.** Rejected for now: the document endpoints answer
  `ErrBusy` with 429 plus `Retry-After`, which is right for an ingest queue and wrong for "you
  pressed the model button while a model was downloading". Two groups, two answers, two tables —
  and the difference is written where it is decided rather than hidden in a switch that has to
  guess the caller.

## Consequences

Starting a download answers before the bytes move, so `internal/app`'s tests wait for the fetch
to be recorded (`awaitTags`) or give a wrongly-started one a moment to show up (`settledTags`)
rather than reading the record immediately. A malformed tag is now 422 rather than 400; the web
client displays the message either way. `engine.ValidTag` is exported so both callers share one
shape for a nightly — do not write the pattern out again anywhere else.
