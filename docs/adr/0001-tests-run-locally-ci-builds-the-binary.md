# Tests run locally; CI only builds the binary

The suite is 557 tests and no workflow runs them: `.github/workflows/release.yml` builds and
ships the binary, and `make check` (`fmt`, `vet`, `go test -race -shuffle=on`) plus
`make web-check` are the developer's loop. This is deliberate rather than an oversight — the
cases that actually catch regressions drive `pdftotext`, `pdftoppm`, `ffmpeg`, a 37 MB
llama.cpp download and a real browser over CDP, so a runner would have to install all of that
to test anything that matters, on a single-developer local-first app whose developer already
runs them locally.

## Considered options

- **Full test job on push/PR** — rejected: needs poppler, ffmpeg, a browser driver and
  network access to GitHub's release API in CI, and mostly re-runs what the developer already
  ran minutes earlier.
- **Smoke-only job (build + `go vet` + a few packages)** — rejected: a green subset teaches
  future readers the suite is checked in CI when it is not.

## Consequences

Green is claimed by hand, so `make check` is the thing that must actually be run before a
commit, not after. A future review should not propose a test workflow without making the
toolchain argument above.
