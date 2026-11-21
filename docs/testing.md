# Testing

How this repo is tested, and the traps found while writing the tests. `AGENTS.md` keeps
the rules that apply to every change; this file is the detail plus the browser-harness
notes that only bite when you are writing a check.

## Rules

- Tests live beside the code (`foo.go` → `foo_test.go`); per-package goldens in
  `testdata/`. No separate tests tree.
- Add a test with every feature. `TestNoSamplingOrInferenceConfig` exists to stop
  sampling parameters creeping back into requests.
- Hermetic by default: `httptest` for HTTP, `fakeRunner` for external processes,
  `t.TempDir()` for files, `fstest.MapFS` for assets. Tests that need real poppler skip
  themselves when the binaries are missing. No network in unit tests.
- No sleeps for synchronisation. Async ingest uses `waitUntil`, which polls a condition
  with a deadline, and blocking processors release on a channel — so a bounded queue and
  a graceful close are both observable without anyone guessing how long to wait.
- The research loop is driven by a scripted model — a slice of canned completions — so
  every termination path, the gate, malformed arguments and the transcript invariant are
  asserted with no model in the room. The scripted fake fails the test if the loop asks
  for a reply it was not given, which is what pins the iteration budget.
- `app` tests use the **real** SQLite store with a fake pipeline: the interesting
  failures are how ingest's output lands in the schema (foreign keys, id idempotency,
  the ready gate), not what a mock would agree to.
- `httpapi` tests use a fake `API` and assert only the HTTP mapping.
- Degenerate fixtures hide logic bugs. A fixture of 20 identical lines has a period-10
  word sequence, so *every* 100-word window is identical and full-chunk overlap looks
  correct — assert the overlap on unique words, where 50 words is a number that can be
  wrong. The same trap from the other side: ten `domain.Hit`s all reading `"chunk"` are
  **one** chunk to a content-keyed registry, so a "takes FinalTopK" test written with
  them proves only that dedupe works.
- Ingest dispatches on the filename, so a fixture called `x.pdf` whose bytes are text is
  genuinely `unsupported` ("PDF produced no text layer"), not `failed`. When a test wants
  to exercise the embedding path, name the file honestly.

Coverage today: `domain` 100%, `llm` 99%, `ingest` 98%, `config` 97%, `index` 94%,
`chat` 92%, `retrieve` 88%, `httpapi` 88%, `app` 85%, `store/sqlite` 81%; 89% overall.
The gaps are DB-error guards and unreachable IO/reflect branches; do not contort
production code to reach them.

## `make e2e`

`scripts/e2e-embed.mjs` runs the **real binary** against a fake embeddings server
speaking llama.cpp's shape, and reads the database with `sqlite3` rather than trusting
the API. Unit tests pin each layer's shape; this pins that the settings a user saves
actually reach the pipeline, that the probe writes the dimension back, that repointing
the embedding server *while the binary runs* moves the socket and not only the error
text, and that `ready` is backed by rows.

## `make ui`: the browser checks

Builds, starts the binary against a throwaway data directory, and runs
`scripts/ui-upload.mjs`, `scripts/ui-drop.mjs` and `scripts/ui-ask.mjs` over CDP
(`scripts/cdp.mjs`, no dependencies). They assert on DOM state — computed text and
counts — not screenshots. 100 checks cover create-notebook, file-picker upload,
`processing → ready` polling, dedupe, drag-and-drop, unsupported files, filtering,
detach, and a whole question: streaming, citations arriving before the answer, a chip
opening the source on the cited chunk, a reload, the model refusing, the model answering
in prose, a dead server, and Stop. They skip successfully when Chrome is missing. One
harness runs alone with `make ui-upload`, `make ui-drop` or `make ui-ask` — same fake
and same fixture, so a frontend tweak does not require reading three suites — and a
harness name that does not exist is an error rather than an empty pass. They
caught a 10× byte-formatting bug that no amount of staring at the component would have
found — and two bugs in the answer path that only appeared once a real browser drove it.

`scripts/fake-embed.mjs` is one port standing in for llama.cpp: `/v1/embeddings`,
`/v1/rerank` and `/v1/chat/completions` (non-streaming for the research turn, SSE for
synthesis, with the answer split into 9-character pieces so the client has to reassemble
frames — including one that splits a `[[Chunk 1]]` label in half). The research reply is
derived from the request rather than scripted, so one fake answers a question about any
corpus; `/control {"script":…,"hold":…}` pins an exact sequence when a check needs a
specific way to end. Assert against it through `/stats`, which reports what the *server*
saw, not what the client intended.

## Traps in the CDP harness

- Page code is built by an **untagged template literal**, so `\s` in it becomes `s`:
  `text.split(/\s+/)` reached the page as `/s+/` and reported every chunk as one word.
  Regexes and unicode escapes handed to the page need doubling (`\\s`).
- Page code inside a `ui.eval` template literal cannot contain a **nested** template
  literal: the inner backtick ends the outer one, and the syntax error surfaces dozens of
  lines later as "missing ) after argument list" at an innocent `if`. Build those strings
  with `+`.
- Both `eval` and `until` wrap their argument in a function body, so it must `return`. A
  bare expression returns `undefined`, which reads as "the app did not do the thing"; an
  object literal is worse — `({…})` parses as a block and fails with a syntax error
  inside the page.
- A test helper that takes an **expression** must not be handed a statement block.
  `until("const r = …; return r")` compiles to `const value = const r = …` and fails with
  a syntax error inside the page, which looks like an app bug.
- `Runtime.evaluate` returns the object handle nested under `result`: reading
  `{ objectId }` off the response gives `undefined`, and the error says "no element
  matches", which is a lie about the selector.
- `until("the last assistant turn")` returns the turn from the *previous* question the
  first time it runs, because that turn is also a finished row. Take the count before the
  click and wait for the count to grow — and give the poll longer than the thing takes:
  an answer streamed a few characters at a time outran a 6-second wait and reported a
  working Stop button as broken.
- Truthiness in a poll must mean "rendered", not "present": a dialog that is open while
  its fetch is in flight satisfies `dialog && dialog.open`, and the first measurement
  reads the loading text.
- A live bubble shows its status in the same slot as the answer (`p.answer.pulse`), so
  "`.answer` has more than ten characters" measures *"Searching library for …"* and lets
  a check click Stop on a turn that has written nothing yet. Select `.answer:not(.pulse)`
  when what is being asserted is prose.
- `{source.status}` inside an element renders as one text node with the template's
  whitespace: `.textContent` is `" ready"`. Compare trimmed — or a check passes on a regex
  and fails on `===`, which reads like the feature is broken.
- An empty `[]domain.Segment` serialises to `null`, not `[]`. Every list reader has to
  tolerate it — `api.ts` routes all lists through `list()` for that reason — and a script
  that asserts `segs.length` on a fresh source will crash.
- Positional selectors are silent breakage: `.source .row-actions button` was the remove
  button until the inspect button was added in front of it, and the detach test "passed"
  by opening a dialog. Select by title. And `button[title="Stop"]` does not match
  `title="Stop this question"` — match the prefix (`title^=`), and have the failure
  message print the titles that were there instead.
- A test that waits for `.dropzone` proves only that *some* drop zone is on screen. With a
  shared server the previous notebook's is already there, the drop lands in it, and the
  click that was supposed to switch notebooks wins later. Wait for the thing you created,
  by name.
- A synthetic `KeyboardEvent('keydown', {key:'Escape'})` cannot close a `<dialog>`:
  Chrome honours Escape only for trusted input. Use `Input.dispatchKeyEvent`
  (`ui.press`), which is the behaviour worth testing anyway.
- Svelte applies a `$state` write on a microtask. A test that dispatches `dragenter` and
  reads `classList.contains('dragging')` on the next line sees the *old* class, and
  reports a working feature as broken. Await a tick first.
- In the fake server, `/control` applies `reset` **first**: a caller that says "start
  over, and say this" means the script it named, and a reset processed afterwards deleted
  the script, the hold flag and the dead-server flag in the same request — which showed up
  as four unrelated checks failing with the previous answer.
- A wait on the DOM cannot prove work reached the pipeline. The rebuild check polled a row
  until it said `ready` — which it already did — and then counted embedding requests,
  sometimes before the job had run. Wait on the socket (`/stats`), then read the DOM.
- A `check(name, ok, detail)` helper that prints `detail` on success will happily print
  "rows never reached 0" next to a PASS. Make `detail` a thunk.
- A browser check that never calls `checks.report()` prints its FAIL lines and exits 0, so
  `make ui` goes green around a broken suite. The failure count is the exit status.
- The editor's svelte language service keeps the old shape of a `.ts` module after it is
  rewritten and reports real exports as missing. `make web-check` is the gate; the inline
  findings are not.
- Headless-Chrome screenshots of >~1000px wide images come back rescaled and look
  ghosted/doubled in review. Verify DOM state (`getComputedStyle`, counts) rather than
  trusting the picture; capture small viewports when pixels matter.
- `pkill -f "<pattern>"` matches the invoking shell's own command line. Use `pkill -x
  libris` or an obfuscated pattern.
