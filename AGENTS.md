# AGENTS.md

Working notes for any agent (or human) picking this repo up. Everything here is either a
decision that has been made or a fact that has been verified. Things that are still open
are at the end under **Open decisions** — do not silently decide them.

Detail that describes rather than constrains lives next to the code it describes. Read
the one you need before you start:

| Before you touch | Read |
|---|---|
| naming, status words, "chunk" vs "segment" — anything a reader sees | `GLOSSARY.md` |
| `internal/domain`, migrations, chunking, provenance, the data directory | `docs/data-model.md` |
| routes, status codes, SSE frames, file endpoints | `docs/api.md` |
| `internal/llm`, any llama.cpp flag, endpoint or claim | `docs/llamacpp.md` |
| tests, `make e2e`, `make ui`, the CDP harness | `docs/testing.md` |
| the notebook's three columns, the panel, the breadcrumb, a conversation's place in the UI | `docs/adr/0012-panel-navigation.md`, `docs/adr/0014-the-breadcrumb-ends-at-the-chat.md`, `docs/adr/0015-conversations-under-their-notebook.md` |
| a decision someone says was already made | `docs/adr/` |

---

## Agent skills

### Issue tracker

Issues and specs for this repo live in **GitHub Issues** on `minhle-120/libris`, driven
with the `gh` CLI from inside this clone (it infers the repo from `git remote`).

- Create: `gh issue create --title "…" --body "…"` (heredoc for multi-line bodies).
  Read: `gh issue view <n> --json number,title,body,labels,comments`. List:
  `gh issue list --state open`. Label: `gh issue edit <n> --add-label "…"`. Close: post
  the reason with `gh issue comment`, then `gh issue close <n>`.
- Blocking uses GitHub's native issue dependencies (`gh issue edit <child>
  --add-blocked-by <blocker>`); a ticket is unblocked when every blocker is closed.
- Before the first `gh issue create` of a session, confirm the target with
  `gh repo view --json nameWithOwner`. Never file into a repo nobody here owns.
- **Nothing writes to GitHub unless it is asked to.** No push, no issue, no label, as a
  side effect of a task. Six local commits sitting unpushed is normal here.

### Triage labels

Five state roles, each label string equal to its name — `needs-triage`, `needs-info`,
`ready-for-agent`, `ready-for-human`, `wontfix` — plus the category roles `bug` and
`enhancement`. A triaged issue carries exactly one of each. `bug`, `enhancement` and
`wontfix` exist on the remote; the four state labels do not yet, so create them the
first time triage runs, not before.

### Domain docs

Single context. `GLOSSARY.md` at the root defines the vocabulary and `docs/adr/` holds
one file per accepted decision (0001–0005). Read both before exploring an area, use the
glossary's words in issue titles, test names and commit messages, and if what you are
about to write contradicts an ADR, say so out loud rather than overriding it quietly.

---

## 1. What Libris is

A local-first NotebookLM-style app: you add sources (PDFs, images, audio), ask questions
across them, and get answers with citations that open the original at the right page or
timestamp. One machine runs the UI, the database and the models; nothing leaves
localhost. Target scale: one user, ~10³ sources, 10⁴–10⁵ segments, sharing RAM with the
model servers.

`references/research-loop-spec.md` is the behaviour spec for the answer path, ported
from a different project (Sift) and implemented in `internal/chat`. It is input, not
gospel: the two-call loop, the `[[Chunk N]]` grammar, the evidence registry and its
acceptance tests are the parts worth keeping; sections marked `[PORT: drop]` concern a
plugin/sub-agent system that does not exist here. `references/llama.cpp/` is a clone of
the server, kept so wire claims can be checked rather than remembered.

---

## 2. Non-negotiables

Argued through. Breaking one needs a conversation, not a commit.

1. **Sampling and inference config never travel per request, and are not settings
   either.** Temperature, top_k/p, min_p, mirostat, repeat penalties,
   `n_predict`/`max_tokens`, `seed`, `stop`, grammar and KV-cache behaviour are fixed on
   the `llama-server` command line; the client sends structure only
   (`internal/llm/wire.go`, `docs/llamacpp.md`). Capacity is the same rule one step
   earlier: `n_ctx`, the GPU layer count and batch sizes are not in `internal/config`
   at all, because a setting implies the app can apply it, and it cannot (ADR 0011).
2. **One definition per setting.** `internal/config/config.go` is the only place settings
   exist — values, labels, help, ranges, enums — and the frontend is *generated* from it
   (ADR 0001).
3. **An uploaded original is never rewritten, and is deleted only when nothing uses it
   any more.** A citation must be able to open the source file — which is the same fact
   seen from the answer: while any notebook lists a source, its file is untouchable. The
   last notebook to let go is the one act that may remove it (ADR 0013).
4. **A PDF produces two derived documents** — page text and page rasters — always both,
   indexed and cited as if they were separate sources.
5. **An ingest never fails a batch.** Unreadable, unknown or unsupported input returns a
   `Result` with `Unsupported` plus `Warnings`. Errors are reserved for missing files,
   missing directories, and the size cap.
6. **One process owns the data directory.** No cross-process locking; the MCP server
   lives inside the same binary, not beside it.
7. **No TTS, no audio output.** Audio is an input modality only.
8. **No video.** Explicitly dropped.
9. **Loopback only by default.** `127.0.0.1`; anything else must be deliberate.
10. **No `utils`, `common`, `models` packages.** That is where modularity dies.
11. **Interfaces are declared by the consumer**, not the producer, and kept narrow.

Every one of those, and several things this file does not repeat, has an ADR behind it.
They are short, and the index is `ls docs/adr/` — a number here would be stale by the
next one, so what each one settles, in the order an agent is likely to need it:

| Deciding | Read | It says |
|---|---|---|
| a setting, or a field in the UI | 0001, 0011 | config is defined once in Go and the frontend is generated from it; a setting is an ability libris can apply, never capacity or a model name |
| a request to a model server | 0006 | sampling and inference config never travel per request |
| the shape of an answer | 0007 | a question is two model calls: research, then synthesis |
| citations, re-ingest, what a chunk points at | 0008 | a citation is copied out of the segment, not referenced |
| `Source.Status`, embeddings, "done" | 0004 | `ready` means searchable, so embedding is part of ingest |
| cancellation, Stop, a dropped client | 0009 | one POST, one stream, and hanging up *is* the cancellation |
| retrieval at scale, a vector index | 0010 | vectors are BLOBs in SQLite, scanned in Go — for now |
| notebooks, dedupe, a shared file | 0003 | notebooks own memberships; sources are content-addressed and shared |
| where files go on disk | 0005 | the data directory defaults to Documents/libris, resolved per platform |
| the notebook's columns, the panel, the breadcrumb, a conversation | 0012, 0014, 0015, 0017 | Sources and Activity are a panel beside the chat, not pages instead of it; the header ends at the chat you are reading; conversations are the second level of the notebook list, not a control in the composer; a window too narrow for both fixed columns closes the panel, never the chat |
| how settings reach the browser | 0002 | settings are served and saved over HTTP |

---

## 3. Toolchain and commands

| | |
|---|---|
| Go | 1.27.3, module `libris`. One dependency: `github.com/ncruces/go-sqlite3` (cgo-free SQLite), and that is deliberate |
| Node | 26 / npm 12. Svelte 5.57 (runes), Vite 8.3, TypeScript; `make web-check` is the type gate |
| External binaries | `pdftotext`, `pdftoppm` (poppler), `ffmpeg`/`ffprobe` — optional, probed at startup |

```bash
make check      # fmt + vet + test                       ← run before committing
make web-check  # svelte-check + build
make test       # go test ./... -race -shuffle=on
make cover      # coverage profile + total
make generate   # regenerate web/src/lib/config.gen.ts after changing config
make build      # npm run build, then go build -o libris ./cmd/libris
make run        # go run ./cmd/libris
make ui         # build, run, drive the real UI in headless Chrome
make e2e        # build, run, upload against a fake embeddings server
./libris                                  # data goes to Documents/libris
./libris -addr 127.0.0.1:8080 -data /mnt/nvme/libris
./libris -open=false                      # headless: no browser is opened
```

On start it hands the address to the desktop (`open` / `xdg-open` / `rundll32`, in
`cmd/libris/open*.go`) — a courtesy, never a start-up requirement, so a machine with no
session logs the URL instead and serves anyway. The address is derived from the *bound*
listener, which is why `-addr 127.0.0.1:0` opens the port the kernel picked. Both harness
launches pass `-open=false`.

The binary is ~23 MB and self-contained apart from the model servers and poppler/ffmpeg.
It was 10.5 MB before SQLite: `go-sqlite3` embeds the compiled WASM build, which is the
price of staying cgo-free and cross-compilable. `libris -version` prints the revision a
binary was built from, and `+dirty` means the tree it came from had uncommitted edits —
the stamps are the compiler's (`runtime/debug.ReadBuildInfo`), so there is no version
string anywhere to keep in sync.

A release is cut by a tag, never by a push: `git tag -a v0.1.0 -m "…" && git push origin
v0.1.0` runs `.github/workflows/release.yml`, which compiles static `linux/amd64` and
`windows/amd64` binaries (`CGO_ENABLED=0 -ldflags "-s -w"`, 14 MB) and publishes them with
`SHA256SUMS.txt` as one GitHub Release. **The workflow runs no tests** — they run locally,
before the tag (`make check`, and `make e2e`/`make ui` where they reach) — and it builds
no frontend either, because `web/dist` is committed and `go:embed` reads the tree, so a
release ships the UI in the tag and keeping that current is a local-commit responsibility.
Releases carry no notes — a body written once goes stale on the second release nobody
remembered to edit, and the binary names itself with `-version`. This workflow, and the
issue tracker, are the only things here that write to GitHub.

---

## 4. Layout, direction, state of play

```
cmd/libris/            entry point: flags → config → wire → serve
internal/
  config/              the single definition of every setting (+ store, generator)
  domain/              plain types, zero imports
  llm/                 the only package that knows the OpenAI wire format
  store/sqlite/        migrations + every query (collect.go: the orphan sweep)
  ingest/              bytes → segments (+ pdf, audio, sink, chunker, detect)
  index/               text/image/audio → vectors via llm
  retrieve/            recall → rerank → evidence registry
  chat/                research loop, its three tools, both prompts, synthesis call
  app/                 use cases shared by both transports (+ ingest queue)
  httpapi/             browser transport: JSON, files, embedded SPA, the SSE ask stream
  events/              not built: status/token/job events
  mcpserver/           not built: MCP transport
web/                   Svelte source, embed.go, and dist/ — committed, because go:embed bakes it in
docs/                  adr/ plus the reference pages in the table above
```

Dependencies point one way:

```
httpapi ─┐
         ├─> app ─> retrieve/ingest/chat ─> index ─> llm
mcpserver┘                 └────────────> store
                    everything ─> domain (leaf)
```

Two rules the layout exists to enforce:

- `app` declares the interfaces it needs (`Store`, `Settings`, `Ingestor`) and knows
  nothing about SQLite or the pipeline's internals; `httpapi` declares `API`. Both are
  consumer-side (§2.11).
- `httpapi` handlers do not touch the store. `Options.App` is nil-able, so a build
  without a database still serves settings and the UI.

`chat` takes evidence as *input* and must not call `retrieve` itself. Orchestration lives
in `app` so both transports share one implementation.

**Built:** settings and the generated settings UI; ingest of text, PDFs (text +
rasters), images and audio slices; embedding; recall, rerank and the registry; the
research loop and synthesis; turn persistence in a chat that a question finds, starts, or
deletes; the SSE ask stream; the web UI including the conversation tree, the chunk
inspector, and answers rendered as markdown with LaTeX (ADR 0016); `make check`,
`make e2e`, `make ui`.

**Not built:** `/mcp` and `cmd/libris-mcp` (`internal/events` and `internal/mcpserver`
are empty directories, untracked); ingest progress events; audio transcription — slicing
works, so audio is retrievable by embedding, but `Segment.Text` stays empty and the
synthesiser cannot quote it; lexical recall; vector ANN (retrieval is a brute-force
cosine scan); an eval set; model-server packaging; Office formats.

---

## 5. The answer path

Two model calls, never one: a tool-calling **research** turn that plans retrieval and is
never read by a user, then a **synthesis** call that writes the answer and is the only
prose that survives.

- The research model has exactly three tools, named as the spec names them because the
  gate message and the evaluation note quote them: `query_knowledge_base`,
  `finalize_research_response`, `no_info_found`. Their JSON Schema is *generated* from
  the structs their arguments decode into — §2.2 applies to tool arguments too.
- `keywords` is the embedding probe, `query` is the reranking objective. Never swapped.
- `tool_choice: "required"` on every research call, the mandatory-first-search gate (a
  refusal-shaped tool message that does not unlock the gate), and a prose fallback for
  models that ignore it. The reference insists on all three, and a raw answer is reported
  as `ungrounded` rather than dressed up as a cited one.
- Six ways a turn ends: `finalize`, `no_info_found`, budget-exhausted-with-evidence
  (salvage: answer from everything found), budget-exhausted-with-nothing, prose, cancel.
- Chunk ids are per turn and dense, and the registry holds exactly what the model was
  shown — so a salvage cannot cite a chunk nobody read, and a hallucinated `[[Chunk 99]]`
  is dropped rather than fatal.
- The synthesis prompt is system + real chat history + knowledge block + `### User
  Query:`. The research trace is never in it.
- The two histories are built differently on purpose: flattened text for the research
  model, so prior tool-shaped turns cannot influence it; real role messages for the
  synthesiser. `[[Chunk N]]` is stripped from history, and a no-info turn is excluded
  entirely — user message included.
- The turn is **persisted as it happens** (`app.Ask`): chat, user message, then the
  assistant row *with its citations* before a word of the answer streams, then that row
  closed by the final text. A crash mid-answer therefore leaves a UI that can still say
  what the answer rested on, and `interrupted` is a status a reader is told rather than a
  message that went missing. The `done` frame carries the stored row, so the client
  cannot disagree with the database about what was said.

`docs/api.md` has the stream contract; `GLOSSARY.md` has the five turn statuses and why
rendering them alike is a lie; ADR 0007 records why the split is two calls and what it
costs, and ADR 0008 why an answer keeps its evidence when the corpus moves.

---

## 6. Settings

`internal/config/config.go` is a flat struct; every field carries its UI metadata in
tags, so values, labels, help, ranges and enums exist once:

```go
PDFDPI int `key:"pdfDPI" group:"ingest" label:"PDF raster DPI" help:"…"
           min:"50" max:"600" unit:"dpi"`
```

- `Default()` holds the values; `Validate()` reads `min`/`max`/`required`/`kind`/`enum`
  from the same tags.
- `schema.go` reflects the struct into `web/src/lib/config.gen.ts`.
  `TestGeneratedSchemaIsCurrent` fails the build when that file is stale — run
  `make generate`. **Never hand-edit it, never add a setting to the frontend.** 21
  settings exist today — capacity is deliberately absent (rule 1 of the
  non-negotiables), and the three model names default to blank because a prefilled name
  is a guess about someone else's server that looks configured and fails anyway.
- Env var names derive from keys (`pdfDPI` → `LIBRIS_PDF_DPI`); load order is defaults →
  env → `<data>/settings.json`. `MarshalJSON`/`ApplyJSON` use the same `key` tags, so the
  API and the schema cannot disagree; `ApplyJSON` merges partials and rejects unknown
  keys — loudly, because a request naming a setting that does not exist is a live bug.
  The settings *file* goes through `ApplyStoredJSON`, which drops such keys, reports them
  once at startup (`FileStore.Dropped()`) and lets the next save rewrite the file: an
  older build's vocabulary must never be a reason the app will not boot (ADR 0011).
- `FileStore` writes `<data>/settings.json` atomically at `0600` (it can hold an API key);
  a failed write leaves the old config in force. `MaxFileBytes()` is derived from
  `MaxFileMB`, so size has one unit.
- **A saved setting takes effect on the next job, not the next restart.** `app` reads
  `Settings.Get()` per job, and `cmd/libris/clients.go` keeps one `llm` client keyed by
  what it *connects with* (three URLs, API key, timeout) and rebuilds it when that
  changes — model names are not part of the key, they travel per request. Anything new
  that caches a client, a pool or a path from settings must go through the same
  discipline, or the settings UI lies.

---

## 7. Frontend

- Primer/GitHub design language: 6px radii, `#1f883d` primary, `#d1d9e0` borders,
  `#fd8c73` accent underline, 56px header, 288px default sidebar; dark mode `#0d1117` /
  `#3d444d` / `#4493f8`. Tokens live in `web/src/app.css`, and a `var()` naming a token
  that does not exist fails *silently* — the chat carried nine of them, which is why its
  citation chips rendered in the body colour and its streaming caret was invisible.
- Three planes, not one: `--canvas` is the reading surface (the chat), `--canvas-subtle`
  is the chrome around it (header, both columns), `--elevated` is anything the user acts
  on (source cards, the composer). Depth comes from that order plus a shadow, never from
  a louder colour. `--canvas-inset` is `#010409`, a code block's well, not a message
  bubble.
- Google borrows cheaply here and stops there: filled borderless fields, tonal badges and
  chips instead of outlined ones, a round send control inside the composer card, row
  actions that arrive on hover. What stays Primer is the 6px control radius, the green
  primary, the mono wordmark, and the accent bar marking a selection in both columns.
- No dashed frames. Empty states are icon + sentence + button on the open canvas; a
  dotted box around "nothing here" is two boxes competing with the message. The one
  dashed affordance that earned its keep was drag-over, now a solid 2px accent outline.
  The same reasoning took the ring off the empty-state icon — an icon in a bordered
  circle is another box, and one per column made every empty screen look like the same
  placeholder.
- A scrollbar is chrome, not content: thin, coloured from `--border`, and transparent until
   the pointer is inside the region that scrolls (`*:hover`, `*:focus-within`). The gutter
   stays reserved, so an invisible bar cannot shift text when it arrives. In a notebook the
   scroller is `.messages`, and the harness asserts its computed `scrollbar-width`/
   `scrollbar-color` rather than a screenshot, because a bar that only exists under the
   pointer is exactly the thing a still image gets wrong in both directions.
- A scrollbar is chrome, not content: thin, coloured from `--border`, and transparent until
  the pointer is inside the region that scrolls (`*:hover`, `*:focus-within`). The gutter
  stays reserved, so an invisible bar cannot shift text when it arrives. In a notebook the
  scroller is `.messages`, and the harness asserts its computed `scrollbar-width` and
  `scrollbar-color` rather than a screenshot, because a bar that exists only under the
  pointer is exactly what a still image gets wrong in both directions.
- A notebook is three columns until the window says otherwise. Below `MIN_CHAT` (480px of
  reading column, `lib/panel.ts`) the **panel** becomes its 44px rail, automatically — the
  sidebar is never touched, because it is the navigation. The rule fires on the *edge* of a
  resize, never on every event; an automatic close does not write `localStorage`, so the
  user's width and their open/closed choice survive it; and a panel opened by hand in a
  narrow window stays open. ADR 0017 argues the three alternatives and why each lost.
- A row's actions arrive on hover or focus, and they arrive *over* the card corner rather
  than in its flow: four 32px buttons in flow cost a filename 128px of width and left
  seven characters to identify the file by. Hidden with `opacity`, never `display:none` —
  they stay tab-reachable, and a harness clicking one programmatically is a user who
  found it.
- `#app` is exactly viewport height with `overflow:hidden`, so `<main>` is the page
  scroller for anything that wants one. A notebook takes `height:100%` and scrolls its
  two columns itself — the chat and the panel — because a transcript that grows the page
  scrolls the composer off the bottom. Do not go back to `min-height:100%`: it scrolls
  the sidebar away.
- The chat is a reading column, not the pane: one `--measure` (`min(100%, 42rem)`) on
  `.chat` governs the bar, the thread and the composer together, so an answer and the box
  under it end at the same edge. `76ch` on a turn is a second wrap and disagrees with it.
  Centred, not left-anchored — a 672px column against one edge of a 1468px pane reads as
  a layout that failed to fill; the same column in the middle reads as a line of text.
- A notebook is three columns, not three pages: the notebook list on the left, the chat
  in the middle, and one **panel** on the right holding Sources and Activity as a
  segmented view (ADR 0012). The panel repeats the left column's geometry on purpose —
  260..640 drag from its inner edge, 44px rail, its width in `localStorage` — and
  `lib/panel.ts` is the single rule that keeps them clamped and persisted. Do not
  split it into two components and
  do not make the sizes equal: mirrored geometry, independent numbers.
- The breadcrumb is the app's location and nothing else: `libris / <notebook>`, plus
  the chat's title when a chat is open (ADR 0014). A panel view is not a level — Sources
  and Activity are things you open beside the chat, and the segment control says which
  one is showing. Only `libris` is a button, because only it is somewhere else.
- Conversations live under the notebook that owns them, in the left column, because that
  is the one place with the width to show several titles side by side and the room to
  rename one in place. They are a second list (`ul.chats`) inside the notebook's `li`, not
  rows of the notebook list: a chat is not a notebook and must not borrow its geometry.
  Only the open notebook expands, so the list is the tree it describes rather than a
  hundred rows to scroll. `ChatPanel` has no conversation switcher — one control, one
  home — and no header at all: the title the open chat has is on screen in the breadcrumb,
  and a second copy of a fact the header already states is how the two start disagreeing.
  Renaming a chat is the notebook's inline rule (Enter or blur commits, Escape abandons),
  and deleting one goes through `ConfirmDialog` naming the conversation, because a chat is
  an answer someone needed.
- Overlays are native `<dialog>` + `showModal()`, not positioned divs: Esc, focus
  trapping and the top layer come from the browser, and a hand-rolled scrim fails the
  accessibility checks honestly. Backdrop-click-to-close is the `onclick` on the dialog
  comparing `e.target === dlg`. The chunk inspector is *not* one: it is a step inside the
  panel, because an inspector that dims the screen takes the answer with it.
- Every confirmation is `lib/ConfirmDialog.svelte`, opened by the parent flipping a
  `$bindable` boolean. Its geometry is in `app.css` and its Cancel is first in the markup
  on purpose — `showModal()` focuses the first control it finds, so Enter on a freshly
  opened confirmation cannot do the destructive thing. Adding a second hand-written
  `<dialog class="modal">` re-opens that argument in a place nobody is looking.
- A source row's trash can means two different things, and `notebooks_using` on the source
  read says which: with other notebooks using the file the tooltip says it stays, with one
  it says "no other notebook uses this file" and the click asks first (ADR 0013).
- A destructive action fires on the second click, never the first, and its button is
  `.btn-danger` — red on a red tint, so it is never the green one the eye is trained to
  press. `showModal()` focuses the first control it finds, which is why Cancel is first:
  Enter on a freshly opened confirmation cannot delete anything. The notebook dialog says
  what goes (chats, answers, memberships — the last two by cascade) and what stays (the
  files, because sources are shared and nothing re-derives a raster cheaply), because the
  thing a user fears losing is the file.
- `lib/api.ts` is the only module that talks to `/api`. `lib/types.ts` mirrors the
  **JSON**, not the Go — snake_case, because that is what `internal/domain` marshals.
  `lib/format.ts` holds the only display formatting rules, so byte and duration strings
  cannot diverge; `citationLocator` there is the whole rule for what a citation chip
  says (a time range for audio, a page for everything else).
- An answer body is rendered by `lib/markdown.ts` — marked, then DOMPurify, then KaTeX and
  highlight.js — and it is the only markdown in the app (ADR 0016). Source text, chunk
  text and warnings stay verbatim, because they quote a file and formatting them would
  reinterpret it. A question is never rendered either: it is what the person typed.
- Citation chips are attached to the finished DOM by `attachChips`, after markdown and
  math, so a code block that quotes the grammar cites nothing without the pass trying to
  be clever about it. `lib/chunks.ts` still owns the grammar; `chunkPieces` is gone.
- Anything inside `.md` must be styled in `app.css`. Svelte's scoped styles cannot reach
  nodes the component did not build, which is why `.inline` and `.caret` left
  `ChatPanel.svelte` — a rule added to the component for a chip is a rule that does nothing.
- An image in an answer becomes a link with its alt text, and a fence whose language is
  not registered stays plain monospace. Both refusals are the same one: no request the
  user did not make, no language guessed at. `vite.config.ts`'s `woff2Only()` is not
  decoration either — `dist/` is committed, so the build's leftovers ship.
- `lib/chunks.ts` is the only module that knows the `[[Chunk N]]` grammar. A label with
  no citation behind it is dropped rather than rendered as a dead button: the registry
  holds exactly what the model was shown, so an invented `[[Chunk 99]]` means a model
  made one up and the honest response is to remove it and let the sentence stand. A
  *malformed* label is left as the text it is, because silently deleting it would hide a
  prompt bug. `trimPartialLabel` holds back a label that is still arriving.
- A citation chip is built from the frozen `Citation` the server sent — never from a
  segment id looked up at render time.
- `ChatPanel.svelte` holds no model of a turn: when the stream ends it re-reads the
  thread, so the server's rows are what is on screen. The one thing it adds itself is the
  question at the moment it is sent — a half-minute turn that shows only a spinner reads
  as a hang.
- A notebook is never unnamed: New opens a `<dialog>` whose Create is disabled until
  the name is non-blank, and the sidebar renames in place (Enter commits, Escape
  abandons, blur commits what changed) rather than opening a second dialog for it.
  The old one-click path hard-coded "Untitled notebook", a name the server refuses.
- `App` owns the panel tab (`Panel` in `lib/types.ts`), whether the panel is open, the
  notebook's chats, and which one is being read — because the list that shows them is in
  the sidebar, and a component cannot show the open row of a selection it does not own.
  `NotebookView` does not own `tab`, and `ChatPanel` owns no chat at all: `chatId` is a
  prop, and the one thing it still reports upward is which chat a question *started*.
  The title is the server's, the first question, so a chat with no question has no name
  and no crumb.
- The panel's tabs are Sources · Activity. The chat is not one of them: it is the middle
  column, and the composer's send button is still titled Ask because there it is a verb,
  not a place.
- One New control per screen. The header's New button is gone: with the sidebar's `+`
  and the landing empty state already creating notebooks, a third was three ways to
  find the same dialog and three ways for one to be wired wrong — the landing button
  was, for exactly one session, pointing at the old one-click `create`.
  so they live in `localStorage` and the component, not in `app.css` — that is why
  `--sidebar-w` is gone. A stylesheet cannot hold a per-user value it never reads back. and defines no labels,
  defaults or ranges; reads go through `str()/num()/bool()` and writes through `set()`,
  because `Settings` is a union-typed record. `NotebookView.svelte` likewise defines no
  fixtures for anything.
- The UI holds no mock data. Notebooks, sources and settings load from the backend on
  mount; a notebook's source list refetches while anything is `processing` (an effect
  that destroys itself when idle).
- `ChunkViewer.svelte` is the ingest debugger — one section per derived document, every
  chunk with ordinal, page span and word count, rasters as tiles at their measured pixel
  size, and the repeated tail between consecutive chunks shaded. The shading is computed
  in the viewer from the two texts, so it shows what the chunker *did*, not what it is
  configured to do.
- `web/dist/` is **committed**. Rebuild it in the same commit as any frontend change.

---

## 8. Testing

Tests live beside the code, are hermetic, and no unit test touches the network or sleeps
for synchronisation. Every feature comes with a test; the research loop is driven by a
scripted model so all six termination paths are asserted with no model in the room;
`app` tests use the real SQLite store with a fake pipeline, and `httpapi` tests use a
fake `API` and assert only the HTTP mapping.

Three levels: `make check` (unit), `make e2e` (real binary against a fake embedding
server, database read back with `sqlite3`), `make ui` (209 browser checks over CDP
against the real UI and a fake that answers all three llama.cpp endpoints). Coverage is
88% overall; the gaps are DB-error guards and unreachable IO branches, and production
code should not be contorted to reach them.

### Run the smallest gate that implies the rest

Pick the test that *entails* the change, not the whole matrix. The cost of running a
level nobody needed is not the seconds, it is the ten-minute habit that makes an agent
stop testing small.

**Test the thing you changed, at the level where only that level can see it.** Two
levels are per-change, not per-suite: one harness covering the behaviour, not the suite
that contains it. `make ui UI_HARNESS=ui-nav` and `make ui-nav` are the same thing; the
other three harnesses running because three exist is noise, and a failure in the two you
did not need costs the same reading time as one you did.

| the change is | gate | ≈ |
|---|---|---|
| one behaviour, while iterating | `go test ./internal/<pkg>/ -run <TestName>` | <1 s |
| any Go change, before commit | `make check` | 4 s |
| a `web/` change, while iterating | `make web-check`, then the one harness that covers it | 10 s + 30 s |
| a `web/` change, before commit | that one harness again — `make ui` only if the change is in shared chrome (`App.svelte`, `api.ts`, the header, sidebar, panel, `app.css`) | 30 s |
| a stored row, the ingest pipeline, or bytes on disk | `make e2e` once, because nothing below it can see the filesystem | 1 min |
| a wire shape, an endpoint, the SSE frame contract | `make check` + `make e2e`, and re-check the claim against `docs/llamacpp.md` | 1 min |

`make check` (4 s) always runs before a commit — it compiles every package, so it is
what notices that a signature change in `app` reached `httpapi`, which a package-scoped
run cannot see. That is cheap enough to be a rule and not a judgement call. Everything
above it is a judgement: `TestGeneratedSchemaIsCurrent` already proves the generated
TypeScript cannot disagree with the struct, so a new setting does not need a browser to
know the field exists — only to learn it renders wrong. Nothing runs *for reassurance*: if
the change is visible at a lower level, the higher level is not evidence, it is reading.

`docs/testing.md` has the detail — the fixture traps, what each level proves, and the
CDP harness's many ways of reporting a working feature as broken.

---

## 9. Conventions

- Errors, not panics, on anything user- or file-shaped. Wrap with context
  (`fmt.Errorf("ingest: …: %w", err)`) and expose typed errors (`llm.Error`,
  `llm.IsBusy`, `ingest.ErrNoPDFText`).
- User-caused mistakes are `app.ErrInvalid` / `ErrTooLarge` / `ErrBusy` or
  `domain.ErrNotFound`. A bare `errors.New` from a use case means 500, so pick a sentinel
  deliberately.
- Atomic writes: temp + rename, temp removed on every failure path.
- Reject unsafe paths rather than sanitising them (`DirSink` refuses `..` and absolute
  names).
- Comments explain *why*, especially where the choice looks odd — no `Client.Timeout`
  because it would cap streams; no base64 auto-detection because `path/to/file` is
  ambiguous.
- One ADR per accepted decision, one file in `docs/adr/`: `# 000N — Title`, then `Date:`,
  `Status:` (naming where it is implemented), then Context, Decision, Consequences. Numbers
  are allocated as they are written, so a backfilled ADR can predate its neighbours; the
  index is `ls docs/adr/`, not this file. A reversal is a new ADR that names the old one —
  an ADR's Context is never rewritten to match today, because it describes the world the
  decision was made in. Cross-references from anywhere into this file name the rule
  ("*an original is never rewritten*"), never a section number.
- Commit messages describe behaviour and rationale, and `make check` runs first.

---

## 10. Open decisions (do not assume)

New work goes to the issue tracker; this list is for design forks that are not yet worth
a ticket, or that a ticket would only restate.

1. **Per-notebook naming of a shared source.** Sources are content-addressed and shared,
   so a file appears under the name it was first offered with. "Smith2020" here and
   "paper.pdf" there needs an alias on `notebook_sources`, which is not built.
2. **Whole-notebook-in-context vs retrieval.** Deferred twice. The spec is 100%
   retrieval-based; a frozen-prefix design would change the answer path, prompt assembly,
   chunking and citation format — and prefix stability is what KV-cache reuse costs, so it
   is load-bearing.
3. **Citation granularity inside a page.** Settled: text cites a char range within a page
   (or the two pages a chunk spans), audio a time range, rasters the page. Unsettled: a
   region inside an image, which would let a raster cite a figure rather than a page.
4. **The viewer's contract.** Hash route per modality — `#/source/<id>?page=7&c=1200-1640`
   (the character range of a text chunk), `?page=7` (a raster), `?t=84.2-104.6` (audio) —
   and a straddling chunk anchors on the page it *starts* on.
5. **Multimodal retrieval strategy.** Native audio/image embeddings, text renditions
   (captions, transcripts), or both. `Segment.Text` is empty for images today.
6. **Whether the research/synthesis split survives.** Built as the spec has it, with
   `tool_choice: "required"` and the prose fallback, so it can be re-litigated against
   real transcripts rather than in the abstract. Still unverified: whether local models
   obey `tool_choice: "required"` under load, and whether the spec's iteration budget and
   phrasing suit the models installed here — `maxIterations` and `historyMessages` are
   settings, so both are measurable without a rebuild.
7. **Vector search.** Brute-force cosine in Go for now; `sqlite-vec` once the corpus is
   big enough to care.
8. **Collection of files left by an interrupted ingest.** ADR 0013 settled the case that
   matters — the last notebook letting go takes the source's files with it — but a raster
   written by a job that died before its rows exist is still found by nothing, because no
   query can name it. A startup sweep for source directories with no row would catch it,
   and has to tolerate a directory whose ingest is running right now.
9. **MCP shape.** Same process at `/mcp`, loopback, bearer token, read-only by default;
   `cmd/libris-mcp` as a stdio shim. Details deferred.
10. **Packaging the model servers** — supervise `llama-server`, or require it
    pre-installed.
11. **Eval set**: ~30 golden questions per corpus, recall@k and citation accuracy. Not
    started, and needed before tuning retrieval.

---

## 11. Sharp edges

Found the hard way. Browser-harness and fixture traps are in `docs/testing.md`.

- `go:embed` cannot reach above its package directory, so migrations live under
  `internal/store/sqlite/` and the frontend embed lives in `web/embed.go`.
- Guessing base64 vs path by character set is wrong: `path/to/file` is all `[A-Za-z/]`.
  Use explicit constructors (`AudioPart` vs `AudioBase64`).
- `json:"…,omitempty"` deletes meaningful zeros. `Provenance.T0` is the first audio
  slice's timestamp and it is `0.0`; it must stay on the wire. Zero is a value here, not
  an absence — check that before tagging a field.
- `strings.TrimSpace` uses Unicode spaces; word splitting uses ASCII spaces. A string of
  only U+2003 has no words.
- `PathValue("id...")` returns nothing. For a `{id...}` wildcard the value is read with
  the bare name, `PathValue("id")`, slashes intact.
- SQLite foreign keys are enforced by this driver, and silently so: an ignored error in a
  test reads as a passing test. Insert assets before the segments that reference them.
- A migration that fails on its *second* statement cannot prove rollback — it fails
  identically every time. Make it do work, then fail on something unrelated
  (`SELECT * FROM no_such_table`), and read the file back with a raw `sql.Open`, since a
  failed `Open` hands you nothing.
- `Close()` must not cancel the context an in-flight job writes with. It did: the
  rasterising finished and its results were discarded, leaving the source `processing`
  forever. Stopping the queue and cancelling the job are two different acts.
- `ingest.Detect` reads magic bytes first for PDF/image/audio, extension for the rest. A
  PNG named `notes.txt` must not become text (mojibake chunks); a `.md` stays text
  whatever the sniffer says. Do not "simplify" this to one order.
- A failure message can be newer than the socket it describes. `Embedder` carried a
  `baseURL` "only for error messages" while the `llm` client carried the URL it was built
  with at boot, so changing the embedding URL in Settings produced `…through
  http://localhost:3333: Post "http://127.0.0.1:8082/…"` and told the user to fix a server
  that had never been asked. If two places hold the same address, build both from one
  snapshot and assert it in a test with two servers.
- Go has no "Documents folder" call. `os.UserHomeDir()` plus a hardcoded `Documents` is
  wrong twice over: Windows moves that folder into OneDrive with Known Folder
  redirection, and a non-English Linux desktop names it `Dokumente` or `文档` and records
  the real name in `~/.config/user-dirs.dirs`. Read those instead —
  `HKCU\…\User Shell Folders\Personal` through `syscall` is enough, no new dependency —
  and reject a resolved value that is relative, unanchored (`\Users\name` is `IsAbs` on
  Windows), the filesystem root, or home itself.
- An empty `[]domain.Segment` serialises to `null`, not `[]`. Every list reader has to
  tolerate it — `api.ts` routes all lists through `list()` for that reason.
- DOMPurify's `ALLOWED_URI_REGEXP` is not only a link filter. It runs over the value of
  every *allowed* attribute that is not on DOMPurify's own URI-safe list, and one that
  fails it is deleted without a word. So `lib/markdown.ts` lost `align="right"` and
  `colspan="2"` — a model's `|---:|` arrived left-aligned — while `class` and `title`
  survived, because those happen to be on the list. A non-address attribute that has to
  survive goes in `ADD_URI_SAFE_ATTR`; `NOT_ADDRESSES` says why.
- `overflow-wrap: anywhere` takes part in min-content sizing and `break-word` does not. On
  the markdown body, `anywhere` let a table column under pressure collapse to four
  characters and break every word in it ("Temperatur/e", "Wh/ere") — broken at 900px,
  perfectly fine at 1468, which is how it went unnoticed into a commit.
- An `<img>` starts fetching as soon as it belongs to a rendered document, whether or not
  anything appends it. Converting an answer's image into a link *after* importing the node
  is therefore a request the user never made: do it in the parser document, where nothing
  can load. `make ui` counts what the page fetched, which is how this was found.
- A 202 body is not decoration: return the state you just caused. `ReingestSource`
  answering with the row it read *before* flipping it to `processing` left the row saying
  `failed`, stopped the poll, and the source looked stuck until reload.
- `Render` returns one content part per text chunk and **two** for a raster — label, then
  pixels. A blank line inserted between *parts* detaches a page's label from its image,
  and llama.cpp pairs images with prompt markers by position, so an answer cites one page
  while the model read another's picture. Separate chunks, never parts.
- Stripping a chunk label out of an answer is not a regex replace: the space a label stood
  in has to go with it *when a full stop follows* (`1990 [[Chunk 2]].` → `1990.`) and
  survive when a word does (`a [[Chunk 1]] b` → `a b`, never `ab`).
- `formatBytes` said 12 KB for a 1.2 KB file: `bytes / 10 ** (2 * i)` is decimal,
  `bytes / 1024 ** i` is binary, and the index was chosen by log₂₀₄₈. Write the divisor to
  match the base and assert an exact value, not `/KB/`.
- Two fixed columns either side of one flexible middle means the middle dies first: at
  the default 288 + 340 the chat is 272 px at a 900 px window and 12 px at 640, with no
  horizontal scroll to show for it. Measure the columns, do not eyeball them (ADR 0012).
  ADR 0017 closed it: the panel now becomes its rail below 480px of chat, which takes
  the chat from 40px to 308px at 640.
- A table is shrink-to-fit already. Adding `width: max-content; max-width: 100%` to
  `.md table` measured byte-for-byte identical at 1468 / 1180 / 900 / 640, so the bar under a
  narrow table means the chat column is narrower than the table's minimum content width —
  a layout fact about the column, which CSS on the table cannot fix. Two of these fixes were
  written before the measurement; only the scrollbar one survived it.
- A probe run after a viewport loop measures whatever the last screenshot left behind. The
  first per-width numbers here were taken at the 640px pass and reported a 40px chat column
  as if it were the layout; per-width numbers need the viewport set *inside* the loop.
- Grid items with `align-items: start` do not stretch, so a sticky sub-nav needs its
  containing row to be tall. Check layout at 640 / 900 / 1180 px before declaring a
  settings-style page done.
