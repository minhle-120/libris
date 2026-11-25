# libris — the UI, described

Written from the UI that existed just before it was deleted, so the UI can be rebuilt once,
coherently, from this document. **No code in here, and no measurements of individual controls** —
this describes what is on each page, where it sits, what it says, and how it behaves. Pick the
numbers.

## 0. How to use this document

**Order:** §1 what the program is → §2 the look → §3 the handful of shared controls → §4 rules that
apply across screens → §5 the shell → §6 each screen → §7 how state behaves → §8 voice → §9 what the
automated checks demand → §10 the mistakes not to repeat.

**Read these too, and they are not being rebuilt:**

- `web/src/lib/types.ts`, `api.ts`, `config.gen.ts` — the whole server contract: endpoints, fields,
  status values, exactly as sent. The only source of truth about data; **do not change them**.
- `scripts/*.mjs` — browser checks. Kept; they are the judge. Edit one only where it pins wording you can
  improve (§9.2C) — never where it pins structure, behaviour or a count.
- **Four pieces of infrastructure, provided rather than rebuilt** — no design in them, and checks assert on
  their output. Use them; do not reimplement or edit them: `lib/markdown.ts` + `lib/Rich.svelte` (the answer
  renderer, §4.3) · `lib/chunks.ts` (the citation-grammar DOM pass, §4.2) · `lib/format.ts` (§4.1's wording of
  numbers, dates and citation labels) · `lib/panel.ts` (§4.4's geometry, narrow rule, storage and drag).
  Everything else in `web/src` is deleted.
- `GLOSSARY.md` — the vocabulary the UI must use.
- `docs/adr/` — 0002 no rerank stage · 0003 no audio input · 0005 the "can it answer yet" rule is the
  server's, never re-derived in the browser · 0008 a finding is a code plus a detail.

**Do not** go and read the deleted markup or CSS to copy an implementation. If something here is
ambiguous, note it in §11 and choose the simplest thing that satisfies the checks.

**The plan:** one authoring pass over the whole UI → review against **screenshots** of every screen
and state → a gap pass per screen (loading · empty · error · narrow · keyboard).

**The current state, so nothing is surprising:** the old UI is deleted. `web/src/main.ts`,
`index.html`, the contract layer and the four infrastructure modules above survive, plus a
`App.svelte` that says "no screens yet" and contains no design worth imitating. `make web-check` is
green (0 errors) and `make check` is green. **`make ui` is red by construction** — measured at
3/8 on the wizard harness, and the three that pass are "the app behind it is not mounted", "no page
errors" and "no console errors". Treat every harness failure as "this screen does not exist yet"
until you build the screen that answers it.

**No sentence of the old UI is quoted here.** Its copy had grown bloated and developer-facing, so each
screen below states what it must *communicate* and leaves the words to you, to the rules in §8. Two kinds of
word are genuinely fixed — the §9.2(A) names a check selects on, and the §9.2(B) status vocabulary.
**Everything else is yours, including where a check happens to match on the old sentence: improve the
sentence and update that assertion.** Wording is never a reason to keep wording.

**Done means:** every check passes — unchanged, except a wording assertion you updated in the same commit as
the better copy it now matches (§9.2C) · every §9.2(A)/(B) name appears where it must · every screen
in §6 renders in every state in §7 · the screenshots look like one program in the language of §2 · and
reading the app aloud, nobody would guess it was written by an engineer apologising.

---

## 1. What this program is, and what that does to the UI

A local-first research notebook: one person's own documents on their own machine, with a model
answering from them and citing the page. Everything else follows from that.

- **One user.** No accounts, no sessions, no sharing. Nothing here is about identity.
- **The server is on loopback, started by the same person.** A request never shows a spinner. A
  *job* does — downloads, ingestion, a model answering.
- **Offline is normal.** Only two things need the internet: "is there a newer build", and the model
  catalogue. A machine with no network is a fully working libris and must not look broken.
- **Long jobs are the heart of it.** A model download is gigabytes. The page can be closed and
  reopened and the job is still there. **The UI never owns a job; it observes one.**
- **Deleting is the one irreversible thing.** A file and everything derived from it go when the last
  notebook listing it lets go. That earns a confirmation, a red control, and a sentence naming what
  *stays*.
- **It is a reading tool.** The chat column is a measure of prose. When width runs out, the reading
  column keeps it and the chrome loses it (§4.4).
- **It is a keyboard tool.** Every repeated action is reachable without the mouse (§7.5).
- **Light and dark are both first-class.** No screen may be legible in only one.

---

## 2. The look

A mix of **GitHub** and **Google**: GitHub's *chrome* (planes, hairline borders, tonal chips,
compact density, no gradient) and Google's *interaction habits* (a filled action button, tonal
status pills instead of outlined ones, a round send control, a thumb-slide switch, focus as a ring).
The result should feel calm, slightly dense, and **bordered rather than shadowy** — things are
separated by hairlines and by which plane they sit on, never by drop-shadow drama.

Five habits that define it:

1. **Three planes, and that is the whole depth system.** A reading surface; a subtly tinted chrome
   around it (header, both side columns); and raised surfaces for anything you act on. A raised
   surface is distinguished by a hairline border and a whisper of shadow, not by colour.
2. **Colour means state, and only state.** Blue is for links, focus and citations; green is the
   primary action and "ready"; amber is in-flight; red is destructive and failed; purple is a
   completion distinct from an action. Text is near-black on light, near-white on dark, with one
   muted grey for secondary. If a screen needs a new hue, it is using colour wrong.
3. **Status is a tinted pill, never an outlined one.** A hairline of the status colour around a
   four-letter word reads as a form error; a soft tint of it reads as a state. The pill's text and
   its background are always the same colour, at different strengths — so a badge can never be green
   text on a red field.
4. **Type is quiet.** Compact body text for chrome; the only place type gets large is a heading
   inside a model's answer, and even those stay close to the body size — a `# Heading` from a model
   is a marker inside prose, not a page title. One monospace face for identifiers, byte counts,
   flags, and code.
5. **Motion is minimal and only on colour.** Hover and press change fill, quickly. The only
   perpetual animation is the caret on an answer that is still arriving, and it stops for people who
   ask for reduced motion. Scrollbars are chrome: thin, drawn in the border colour, and invisible
   until the pointer is inside what they scroll — without ever moving the text when they appear.

**Dark mode** is a single switch on the document, chosen in settings and remembered; every colour
comes from a token so both themes are equally first-class. Syntax colours in code reuse the app's
own palette, so a code block cannot disagree with its page in either theme.

---

## 3. The handful of shared controls

Every screen composes from these and **nothing else**. A screen defining its own button, pill, empty
state or modal is the single most likely way this rebuild fails.

- **Button** — four variants and they mean different things: *default* (everything ordinary),
  *primary* (the one forward action in a view — solid green, white text), *ghost* (icon-only row
  actions, and Cancel), *danger* (the one irreversible action — red text on a soft red tint, never
  inverted on hover). Sizes: a normal one, and a small one for inside rows. Two special shapes:
  icon-only square, and a **round** one used only by the composer's send/stop, which share a single
  slot so the control cannot move when a turn starts.
- **Badge** — a state, as a tinted pill (neutral · accent · success · attention · danger · done).
- **Chip** — the same shape language but for *facts* rather than states (a source's kind, a count, a
  filter). If it describes how something is doing, it is a badge; if it describes what something is,
  it is a chip.
- **Input**, **card** (a raised container for things you act on), **switch** (real checkbox under the
  hood so the keyboard works), **setting row** (label + optional one-line description on the left,
  control right-aligned, hairline between rows).
- **The one dialog** — a native `<dialog>`, so the browser owns Esc, the focus trap and the top
  layer. Anatomy: title → the plain-text sentence saying **what is about to be lost** → optional
  boxed note saying **what is *not* going** (it is set apart rather than merely spoken, because it is
  the sentence people reread) → actions right-aligned, with **Cancel first in the DOM on purpose**:
  the dialog focuses its first control, so Enter on a freshly opened confirmation cannot do the
  destructive thing. Both confirmations in the app — a notebook, a file — are this one dialog, and
  closing always travels upward (Esc, backdrop click, Cancel), because a parent that still believes a
  dialog is open can never open it again.
- **Empty state** — an icon, a short title, one sentence, and usually a way out. Vertically centred.
  The icon is **not** ringed or boxed: two ringed icons in two columns at the same size is what made
  every empty screen look like the same placeholder.
- **Icons** — one set, one style (line icons, no fill, current colour, uniform stroke), `aria-hidden`
  because a label is always nearby. Names in use: `search · plus · sun · moon · book · upload · inbox
   · chat · pulse · settings · file · image · trash · x · alert · clock · open · layers · retry · stop
   · download · send · pencil · panel-collapse · panel-expand · check`. A new glyph needs a reason.

---

## 4. Rules that apply across screens

**All four of these are already implemented**, and described here so you know what to ask of them rather
than so you can rebuild them: §4.1 in `lib/format.ts` · §4.2 in `lib/chunks.ts` · §4.3 in `lib/markdown.ts`
with `lib/Rich.svelte` · §4.4 in `lib/panel.ts`.

### 4.1 How numbers and dates are said

One set of rules, applied everywhere; no screen spells out a byte count or an age by hand.

| fact | form |
|---|---|
| size | one unit, integer above ten, one decimal below → `512 B`, `1.5 MB`, `35 MB`, `2.1 GB` |
| age of a thing | `just now` · `4m ago` · `3h ago` · `5d ago` · then a plain date. Coarse on purpose |
| age of a machine's answer | `moments ago` · `12 min ago` · `3 h ago` · `2 d ago` — used **only** where the app displays a fact it did not compute just now (the device probe), so the reader knows how stale it is |
| a day | locale date; **missing or unreadable says the word `unknown`**, never blank, never `Invalid Date` |
| pages | `page 7`, and `pages 7–9` when a chunk straddles a break |
| a citation | `Chunk 12 · page 7` — the number always, the place when there is one. The row above an answer and the button inside its sentences use this one form |
| an error | the server's own sentence, verbatim. Never "something went wrong" |

### 4.2 The citation grammar — the app's most important mechanic

A model's answer arrives containing `[[Chunk 12]]` inside its prose. That grammar is what makes an
answer checkable and what a reader must never see, so the rendered answer is walked and every
well-formed label becomes a **small tinted chip showing only the number**; its tooltip is
`<source name> — Chunk 12 · page 7`; clicking opens the source at that place.

The rules that make this trustworthy:

- Substitution happens **after** markdown and math, on the live DOM. That is deliberate: an answer
  that *quotes* the grammar cannot provoke it, and the walk is blind inside code and math — so a PDF
  containing the literal text `[[Chunk 3]]` in a code block cites nothing.
- A label with no citation behind it is **removed**, leaving the sentence standing. The registry holds
  only what the model was actually shown, so `[[Chunk 99]]` means a model invented a source, and the
  honest thing to do with an invented citation is to take it out.
- A **malformed** label (`[[chunk 1]]`) is left as literal text. Silently deleting it would hide a
  prompt bug.
- While an answer is arriving, a half-written label at the end of the buffer is **held back** until
  its closing bracket lands, so no fragment of grammar flickers at the reader. This applies only to
  text still arriving — a finished answer is shown exactly as it was stored, and dropping a stray
  from it would be a lie.

### 4.3 A rendered answer must be a real document

The chat is the app's reason to exist, so an answer is rendered as a full document, not as plain
text with newlines:

- **Markdown in full**: headings, lists, task lists, tables, blockquotes, rules, links, inline code
  and fenced code with **syntax highlighting**.
- **LaTeX in full**: inline `$…$`, display `$$…$$`, both in prose and inside tables.
- Code blocks are **wells cut into the page** (the deepest surface, a hairline border, scrolls
  sideways) rather than bubbles — a code block is not something the assistant "said".
- Tables, wide equations and wide code **scroll inside the reading column; they never widen it**.
- A blockquote reads like a citation (a rule and a muted voice), because in this app a quote usually
  *is* the model quoting a source.
- An image the model could not show becomes a muted italic link — dressed as what it is.
- The answer's prose is set at a larger, more comfortable size and leading than the rest of the UI.
  This is the only place in the app that gets generous.

### 4.4 The three columns, and who loses when width runs out

Two draggable panels around a reading column, and both panels obey **one** geometry rule — two
panels that drag differently teach the user twice.

- Each panel has a minimum, a maximum, a default, and a **collapsed rail** (a strip of icons).
  The notebook list starts narrower than the source panel. Storage keys and bounds live in one place.
- A handle is an invisible strip over the panel's border that shows only on hover. **Dragging
  resizes, double-click resets, arrow keys nudge, Home resets.** On *both* panels the arrows mean
  *wider* and *narrower*, never "move the border right" — opposite edges would otherwise mean
  opposite things. The width is stored when the interaction ends, not on every frame. While dragging,
  the cursor is `col-resize` everywhere and text selection is off.
- **The narrow rule**: when the reading column would fall below a readable width, the **right panel
  collapses to its rail**, and returns when a real panel would fit again. The way in and the way out
  are deliberately different widths, because the panel's own width is what makes the chat narrow —
  one threshold oscillates.
- The rule fires **only when its answer changes**, not on every resize event: a rule that runs on
  every event takes the panel away again a second after the user put it back, which is worse than the
  bar it removes.
- **Closing by rule never overwrites the user's stored preference.** A preference the window's width
  overwrote is not their preference, and the next wide window would open on the app's idea of what
  they wanted.
- Below a phone-ish width the header drops its search box and breadcrumb; the shell never becomes a
  hamburger and never scrolls as a whole document.

### 4.5 What is remembered, and where

Everything remembered is a local preference and goes nowhere. Only these: the **theme**; each
**panel's width and open/closed** state. Everything else is *not* remembered — which notebook, which
conversation, which source is open. The app opens on the newest notebook and its newest conversation.

Theme is applied **before the first paint** (saved choice, else the operating system's preference) so
a dark machine never sees a light flash. That tiny inline script in the page shell is the only
reason it exists.

### 4.6 Errors, and drops

- A failed **request** (not a failed job) shows **one** pattern: a tinted banner at the top of the
  main column with an alert icon and the server's own sentence, announced to assistive tech. Any
  successful load clears it. No screen invents its own error treatment, and no error is only a
  console message.
- A **file dropped anywhere** in the shell is an instruction to add it to the open notebook. The
  shell swallows file drags at the window level so the browser never opens a PDF in place of the app;
  a drop carrying **no files** is left alone, so dropping text into an input still works.

### 4.7 Hover and focus

- Row actions (rename, delete) appear **only** when the row is hovered or focused, or when one of
  them has focus — permanently visible icons are noise, and click-to-reveal is a different action.
- A **destructive action keeps its colour at rest**, even while hidden: a trash can shaped like a
  pencil is how a notebook gets deleted while reaching for rename.
- Everything focusable shows the accent ring; nothing removes it. Hover always changes something
  visible — fill, and usually the border too. A hover state that changes nothing is treated as a bug.
EOF
wc -l docs/ui-spec.md && grep -c '' docs/ui-spec.md
---

## 5. The shell

Two views — **notebook** and **settings** — inside one fixed frame. The header never scrolls, the
side columns never scroll the page, and the main column is the only scroller. The app is exactly the
viewport tall: nothing outside the main column ever moves.

### 5.1 The gate: nothing is behind the wizard

If the server says this machine cannot answer a question yet, **the wizard is the app** — no header,
no sidebar, no chat column. Not a tour and not a modal: a question and an ingest both need the
embedder, so there is no screen behind it worth showing. It re-reads the server's answer on a short
loop and disappears the moment that answer changes. It is never dismissible, never "snoozed", and
never re-derives the rule in the browser. Anatomy: §6.6.

### 5.2 Header

One row, left to right:

- **Brand tile** — a small filled square with the book glyph. It is the home button (deselects the
  notebook). It carries **no wordmark**: the word `libris` is already the first breadcrumb, and
  saying it twice made the header read `libris libris / test`.
- **Breadcrumb** — `libris` → notebook → conversation. **Only `libris` is a link**; the other levels
  are plain, because you are on both of them. The separator is air with a slash in it, kept behind
  the labels. In settings the second level is the word `settings`. A conversation with no title yet
  is not in the breadcrumb at all — the server names it from the first question, so until then there
  is nothing to show.
- **Search**, pushed right — a filled box with **no border until hover** (the one Google-flavoured
  element here, and the reason the header stopped reading as a row of outlined boxes), with a `/`
  key hint. ⚠️ *It currently searches nothing and `/` does not focus it.* Wire it or remove it — see
  §10.
- **Right edge** — two icon buttons: **settings** (shows as active while in settings, and leaves
  settings just as readily as it enters) and **theme** (moon titled *Switch to dark theme*, sun
  titled *Switch to light theme*).

At a narrow width the search box and breadcrumb disappear; the tile and the two buttons stay.

### 5.3 The notebook list (left)

- A section headed **NOTEBOOKS** (small, uppercase, muted) with **New notebook** and a
  **Hide notebooks** collapse toggle at its right.
- **One row per notebook**: book glyph, name, and a small **source count** shown only when it holds
  sources. Selected rows come *forward* (raised surface + a short accent bar on the inner edge +
  bolder name) rather than getting louder, because the column behind them is already tinted; the
  count flips to the accent tint with them.
- **Row actions**: rename, then delete, revealed on hover/focus, delete permanently coloured (§4.7).
  Tooltips and accessible names name the thing: `Rename <name>`, `Delete <name>`.
- **Conversations are the second level, shown only under the notebook you are in.** A chevron per
  notebook would be a control for a question nobody asks (*which other notebook's chats do I want to
  see right now?*). The level is indented to the notebook's **title** rather than its row, and carries
  a vertical rule: the rule is the tree, the alignment is the sentence.
  - First item: **New conversation** — which creates nothing. A conversation begins at its first
    question, so this only puts the middle column back to an empty thread; an abandoned question
    leaves no row called "New chat" behind.
  - A conversation row shows its title — the first question, up to seventy characters — **clamped to
    two lines** so a list of them stays tellable apart where one clipped line does not.
- **Renaming happens in the row.** The field selects its contents on open; **Enter commits, blur
  commits** (a click elsewhere is an implied "that is the name"), **Escape cancels**, and a blank or
  unchanged draft reverts silently rather than letting the server refuse it.
- **Collapsed**, the column becomes a rail of book icons, each titled with the name it cannot show;
  the selected one keeps its accent edge bar.

### 5.4 The main column

| when | what |
|---|---|
| settings | the settings page — and **the notebook list unmounts**. Settings is a *place*, not a column beside one: the list has nothing to select there, and the page is better for the width. Both ways out stay in the header: `libris` in the breadcrumb, and the gear |
| a notebook selected | the notebook view (§6.1), freshly mounted when the notebook changes |
| still loading | one quiet centred line, nothing else |
| nothing selected | the landing empty state |

The landing empty state says, in a title and one line: no notebook is picked; what a notebook *is* (a set
of sources you ask questions about); that PDFs, images and text are what go in; and that nothing leaves the
machine — that last clause is a promise, not decoration, and belongs here rather than buried in settings.
The action is the one forward button.

### 5.5 Dialogs the shell owns

Three, all built from the one dialog (§3.5). Each must do three things in order: **name the thing**
(the notebook, the conversation), **name what is lost**, **name what survives** — the last of these is
why the boxed note exists, because a source is content-addressed and shared between notebooks, so
deleting one notebook must never read as deleting a file.

- **New notebook.** One field for the name. Enter creates; the primary button is disabled while the
  name is blank; the server refuses an unnamed notebook too, and refusing here keeps that from being
  the first thing anyone learns about the feature.
- **Delete notebook.** Lost: its conversations and every answer in them, and — if it holds sources —
  this notebook's *list* of them, with the count. Survives: any source another notebook also lists,
  with its file, page images and chunks, ready without a second upload or indexing pass.
- **Delete conversation.** Lost: every question asked in it and every answer. Survives: every source it
  cited, untouched and askable again.

---

## 6. The screens

### 6.1 A notebook: two columns and nothing else

The middle column **is the conversation**, and the right column is the source panel (§6.3). No tab
strip across the middle, no notebook landing page: the chat got the whole column because it is the page,
and the things you open *beside* it (sources, activity) live in the right panel with their own switcher.

The error banner here and the chat's are the **same** component as the shell's (§4.6) — the code being
deleted had three near-identical copies, which is a defect to fix, not a pattern (§10).

**Sources are loaded when the notebook opens and re-read while work is in flight** — a file still
uploading, or any source still processing. The loop exists only while something is busy and stops the
moment it is not; a settled notebook asks for nothing. Uploads are **serial**, one file at a time,
because the backend ingests one at a time anyway: a second file's row appearing while the first is still
being read would be a lie about what has happened.

**A citation is an instruction to look.** Clicking one opens the source in the right panel, scrolled to
the chunk the answer came from — not the top of a 400-page run. The source is *looked up* rather than
taken on trust from the answer, because it may have been detached from this notebook since the question
was asked, and an old citation should still open. Opening a citation forces the panel open even if it was
shut.

Detaching a source from the notebook takes any open viewer of it back with it — the app cannot serve
segments for a document the notebook no longer has, and a viewer left open would keep rendering something
that is gone.

### 6.2 The conversation

No header. A conversation is chosen in the notebook list — the one place that can show several at a
readable width — so this column is the thread and the box to ask the next question in.

**One width, held by the column and not by the things in it.** Banner, thread and composer share a single
reading measure, centred. The old thread wrapped narrower than the composer spanned, so an answer ended
six inches short of the box beneath it; and a narrow column pushed against one edge of a wide pane reads
as a layout that failed to fill, where the same column in the middle reads as a line of text — which is
what it is. **A check measures this**: the answer's width must equal the composer's.

**The thread scrolls inside itself; the page never does.** The composer stays put, because a page that
scrolls would move the input out from under the keyboard mid-question.

#### The thread

- **A question** — right-aligned, on a soft raised bubble, **never rendered as markdown**: it is what the
  person typed, and formatting their `# headings` or `$math$` would reinterpret what they said. Capped
  narrower than the column, so a short question is visibly short.
- **An answer** — left-aligned, unboxed, full reading measure, rendered as a real document (§4.3), with
  its **citation chips in a row above it**: one per cited chunk, a file or image glyph and the label from
  §4.1, tooltip adding the source name, click opens the source at that place. No citations, no row.
- **A verdict line under an answer that is anything but a clean one** — a badge and one short sentence,
  and the sentence's job is to say what the app did *not* do. Badge words are contract (§9.2):

  | status | badge | what the sentence must convey |
  |---|---|---|
  | no evidence | `No evidence` | nothing in the sources answered this, and nothing was invented in its place |
  | ungrounded | `Uncited` | the model answered in prose instead of searching, so this has no citations and was not checked |
  | cancelled | `Stopped` | part of an answer, cut short |
  | errored | `Interrupted` | libris stopped while the answer was being written |

  A clean answer shows nothing: the absence of a verdict is the good news.
- **Reasoning** a model emits is **never appended to the answer**. It is stored beside it and shown above
  the prose in a disclosure, **collapsed by default**, labelled with how long the model thought:
  `Thought for 6s`. A turn read back from the database opens to the same text — the thinking is stored,
  not remembered — and a row that has no measurement says `Thought` rather than inventing a length.
  It is neither the answer nor the evidence for it (the citation chips are the evidence), and the panel
  says so in its own caption. Nothing feeds it back to a model.

  What is disclosed here is the **answer's** thinking, and by default the only thinking there is: the
  research loop is asked not to think, because whatever it wrote to itself before naming a search is
  thrown away the moment the call lands. Settings → Retrieval → **Think before searching** puts the loop
  back to deliberating, and the screen does not change either way — that thinking is never drawn, only
  paid for.

#### The turn in flight

The moment a question is sent it appears on screen. A turn can take half a minute and a spinner for that
long reads as a hang — particularly as the answer arrives *under* the question it answers. Then:

1. a status line where the answer will be, replaced by the research loop's own words as they arrive; and
   while a model is thinking out loud, its thinking streaming into the disclosure above — **open**, with
   the label `Thinking…`, until the answer's first sentence arrives and closes it;
2. the citations the loop is working from, above the answer, as soon as they are known — **disabled**,
   because they are not yet the frozen set the answer was written against;
3. the answer streaming in as real markdown, forming as it arrives (a list that forms *is* the answer
   being written), caret after the last character;
4. **stopping is a client-side hang-up** — there is no stop endpoint to fall out of sync with.

Then the thread is re-read from the server, because **the stored row is the last word**: it carries the
id, the status and the frozen citations, and the turn may have ended in a way the client did not witness
(a model that refused, a budget that ran out). Two rules about that moment:

- The live answer is the only copy of a stopped answer's text until the stored row arrives, so the turn
  comes down **all at once, after** the row arrives. Blanking it first blinks the answer out for a frame
  and back, and a reader is left guessing which of the two the server saved.
- If the refresh fails, **keep what is on screen** rather than blanking a real answer over a failed poll.

Switching conversations clears the half-finished turn first: a streaming answer belongs to the chat it was
asked in, not the one now on screen. And a thread fetch that comes back after the reader switched again is
**discarded**, rather than leaving them looking at one chat under another chat's name.

**Auto-follow is polite.** The thread sticks to the bottom while an answer arrives — unless the reader has
scrolled up to re-read something, in which case nothing moves. Yanking a paragraph out of reach
mid-stream is the fastest way to make an answer unreadable.

#### The composer

One raised card at the foot of the thread: a growing field, and a **round control at its trailing edge
that is send when idle and stop while a turn runs — the same slot in both states**, so what is under the
thumb while an answer arrives is the thing that stopped it. The field grows as the question gets longer
and then stops, scrolling inside the composer, so a long question cannot push the composer off the bottom
of the column.

- **Enter sends; Shift+Enter is a newline** — the convention inherited from every chat product, and the
  one exception worth making; it must not fire mid-composition of an accented character.
- No rule above the card: a line over an input makes it read as a footer of the window rather than where
  the next question starts.
- **Nothing to answer from is said before it fails, not after.** While no source is ready: the field's
  placeholder says to add a source first, a small amber line inside the card says there is nothing to
  answer from, and send is disabled. Send also needs a non-empty question and no turn already running.
- Under the card, one quiet line, shown only when there is something to search: the promise that every
  answer comes from the user's own sources and says where it came from.
- A brand-new conversation shows an empty state instead of a thread, and its words change with whether
  there is anything to ask about — ready: that a question runs a research loop over the ready sources and
  cites the page, and a citation opens the original there; none ready: that answers come only from
  sources that finished ingesting, so add a file or retry the failed ones.
- Loading a thread is one quiet line, never a skeleton, and a **draft** (no conversation chosen) never
  shows a loader for a visible frame.

### 6.3 The source panel (right)

A chrome-plane column whose contents are chosen by a **segmented switcher** at its top — not page tabs: it
switches what is inside one column, and it is the only thing that says which, since the breadcrumb names
the conversation, not the panel. Two segments, **Sources** and **Activity**, each allowed a small count or
a pulse dot, plus a collapse toggle. When shut it is a rail: expand toggle, source count, pulse dot if
something is running.

Inside Sources, top to bottom: a **filter field** (the same borderless-until-hover box as the header's)
and an **Add sources** button; a one-line status; then the list. The status line says what is true right
now and never more: how many are uploading → that something is processing → how many there are.

**The whole panel body is the drop target.** While a file is over it the panel takes an accent outline and
a wash saying to drop. The rail is *not* a drop target — a drop has one meaning here, and it is not
available where there is nothing to drop onto.

#### A source is a card, not a row in a table

One raised card per source; a bordered group of rows reads as a table of nothing. The card:

- a kind tile (file · image · inbox for something that cannot be ingested),
- the **name**, truncated, the only loud thing in the card,
- a **fact line**: status badge, kind, size, — when ready — chunk count, — when added. The status lives
  *on this line*, not beside the name: with four action icons in the corner and a badge in the name row,
  about seven characters of filename survived, which is a worse way to say "this is your file" than a
  second line,
- **detail or findings**, when there are any,
- **action buttons arriving over the top-right corner on hover or focus**: still in the DOM, still
  tab-reachable, and the tail of the name is the one part of the card empty when the pointer gets there.
  **On touch devices, where there is no hover, always visible.**

Which actions exist depends on the state, and **the tooltip is the documentation** — it must make the
consequence legible before the click:

| action | when | what its tooltip must say |
|---|---|---|
| **Run again** | any state except processing | that everything derived is replaced: chunks, rasters, vectors |
| **Inspect** | ready, or failed | that this shows how the file was chunked |
| **Open the original** | ready, unsupported, or failed | opens the kept file in a new tab — this is why a re-run is free |
| **Remove / Delete** | always | **which of the two it is**: several notebooks use this file so it stays, or this was the last so it goes |

That last pair is the one place the same icon means two different things, decided by a number in the row.
Asking is required only for the part that cannot be undone — the delete-file confirmation, whose lead names
the chunk count going with it and whose note repeats that a shared file is never deleted here.

#### Findings — how a source says what went wrong

The server sends each problem as a **code plus a detail** (ADR 0008); the UI turns them into one clause
per finding, in the order the run noticed them, separated visibly. The rule that matters:

- A **missing external tool** becomes the action it implies — install it, then run the file again — rather
  than the tool's bare name. A name leaves the reader to work out that installing it and re-running is the
  point, and re-running is free here because the original is kept.
- Anything else says **what was lost** and stops there: re-running a file that has no more pages to give
  gains nothing, so the row must not imply that it would.
- A hard failure is rendered in the destructive colour; a soft finding stays quiet.

A **processing** card is dimmed at the name rather than showing a percentage — there is no progress stream
to show, and inventing one is the worst kind of lie this app could tell.

### 6.4 The chunk viewer — how a source was actually cut up

Opened from a source card or a citation, and it **replaces the list inside the panel**. A view with a way
back, *not* a top layer: it fills the column and scrolls inside itself, so the answer that cited the source
stays readable beside it. It therefore has **no Escape handler on purpose** — Escape belongs to the
composer while this is open. The way back is a close button and the Sources segment.

Header: that this is how *this file* is chunked, naming it; beneath it a plain-words summary (chunks, how
many text and image, kind, size) — which is also where a load failure appears.

**A PDF yields two documents, and the viewer says so in plain words**: **page text** and **page rasters**,
each its own group with a heading, a chunk count and a provenance note. Text groups render as a list of
chunks; an image group as a grid of page thumbnails that open the full raster in a new tab, each captioned
with its padded ordinal, its page or page range, and — once loaded — its real pixel size and file size.

Per text chunk, in a narrow provenance column beside the text: **padded ordinal**, page or range (or a dash
when there is none), word count, and the character range inside the source. Then the text.

Two honesty features that must survive the rebuild:

- **Overlap is shown, not hidden.** The chunker deliberately repeats the tail of the previous chunk; the
  repeated opening of each chunk is detected (longest common tail, snapped back to a word boundary so
  shading never starts mid-word) and rendered **shaded**. A legend explains the shading in one line — and
  when nothing overlaps, the legend says the opposite rather than going quiet.
- **A citation is honoured, or admitted to be impossible.** Opening from a citation scrolls that chunk to
  the middle of the column and rings it with an accent outline (a ring, not a fill — it must read in both
  themes and not fight the shading). When the chunk no longer exists that is a **normal outcome, not a
  bug**: ids are `doc:ordinal`, so a source rebuilt with different settings keeps them only while it
  produces the same number of chunks. The citation still quotes what was cited; it just cannot point at
  something gone. Say nothing about it.

Long chunks collapse behind a control that says how much more there is, so a run of them stays scannable; a
chunk with no text says so rather than showing an empty box; a source that yielded nothing says that.

### 6.5 Settings — a page, not a column

Full width (capped, generous), a title, and a one-line note about where the values come from and where they
go. A **sticky section nav** down the left (active item marked with a short accent bar) that scrolls to a
section; on a narrow page it becomes a wrapping row above the content.

**Nothing in this file defines a setting.** Labels, help, kinds, ranges, enums and units all come from the
schema the backend generates; the page renders what it is told. A control that the schema did not ask for is
a defect.

- **One save bar, sticky under the header** — the page is several screens of fields, and a change made in
  the first section should not have to hunt for its Save button. It states the truth of the moment and only
  that: loading · an error · unsaved work · saved · nothing to do. Both buttons are disabled until something
  changes, so **the bar's own edge** is what signals unsaved work (accent) or failure (red) — a tint behind
  the buttons would compete with the primary itself. Buttons: discard, and the primary save.
- **One card per section** — heading plus its help sentence, then **setting rows**. Control type follows the
  schema: switch for a boolean, select for an enum, otherwise text / number / password, with a unit spelled
  beside it. Numbers right-aligned and narrow; text and selects wide, full width on a narrow page. A
  **read-only** field with no value says that its value is probed at startup — it is the machine's, not the
  user's.
- **A field can be scoped to one engine mode** (managed vs external), **and hidden from the form.** Hidden
  means still defined, saved, validated and generated like every other setting, but with its control living
  beside the facts it needs — for the device, the list the installed engine printed — rather than as a text
  box asking someone to type a name they can only learn from a command line. **A group whose every field is
  hidden is a heading with nothing under it**, so such groups disappear (one named group excepted: it hosts
  the machine section).
- **Managed-vs-external is one switch, not two toggles that can both be on** — and the fields it hides are the
  ones libris computes for itself. A form that asks for an address it is about to derive is how this page
  became confusing once already.
- **Saving re-reads the machine section afterwards**, because port and mode are settings: the page must not
  go on showing the port from before the one just typed.

#### The machine section — facts, not settings

Lives under the group that names it, separated by rules rather than a second frame (a box inside a box is
the thing this app stopped doing), and reads the host endpoints rather than the settings ones. Its controls
are **verbs**. That distinction is the whole design: everything else on the page is a choice libris can
apply; what is installed is a fact, what is new is somebody else's fact, and what the box can run is a fact
about the installed engine.

Facts, as label/value pairs. Where a word below is marked **(s)** it is a *state word* the checks match on
(§9.2) — the phrasing around it is yours.

| row | must convey |
|---|---|
| **On this machine** | the installed build's tag and size, or that nothing is downloaded yet **(s)** |
| **Latest build** | the newest tag, the asset for this box with size and file name, plus one of: an update is available **(s)** · it is current · a check is running **(s)** · it is not known right now **(s)** · unknown **(s)**. **A check in flight is not a failure and must not be said as one** — collapsing those two is how an app teaches people to press "check again" repeatedly while it is already working |
| **Stored in** | the directory, as a path, titled in full |
| **Model server** | below |
| **Run models on** | a **picker**, never a text field: the automatic choice, one entry per adapter the installed build printed (name, description, size), and CPU-only. Below it, the truth about *now*: when a server runs, which device **the process** is on — not what the form is holding — and if those differ, that saving and restarting switches; either way, that it applies on the next start and is saved with the rest of the page |
| **Detected** | the adapters as chips (description and total size; free size in the tooltip) · that there is no graphics device and so it runs on the CPU · or that the engine will be asked once it is downloaded. **The report always carries a CPU row, so "this box has no GPU" is the *absence* of a non-CPU row, never an empty list** — and with no engine installed the row must point at the download, **not** deny that the machine has a GPU. Plus a link to re-probe skipping the cache, and how stale the answer is |

**The model server row is the verb of the section and must sit in the row whose state it changes** — it used
to live at the bottom of the page beside the download button, where pressing it meant scrolling away to find
out what happened, and where *stop the server* shared a row with *stop* a download. It shows the address a
request would reach (or that a free port is chosen at start — never a URL pointing at nothing), one of
running with its pid **(s)** · it did not start **(s)** · stopped **(s)**, then **start / stop the server**
(primary when stopped). Start is disabled with a tooltip saying why when there is no model to run. Beneath
it, the models the running server holds — alias and size, titled with the path — or that nothing is loaded
yet, linking to the models section **(s)**. If the server failed, its own message verbatim (a port somebody
else holds, a model that will not load), and a line naming the log file.

Progress, while downloading: a labelled bar naming which phase **(s)**, the tag, that it is continuing where
it stopped when resuming, and bytes and percentage with a real progressbar role. Below it the verbs: while
busy **stop**, titled that the bytes so far are kept and the next attempt resumes **(s tooltip)**, plus how
much is downloaded; otherwise **download this build** / **update to that one** / a checking state **(s)**,
or that it is up to date with the date beside a check-for-updates button, plus discarding a stopped
download's bytes, and **remove** (danger) when something is installed.

**Remove asks first**: the files are deleted from this machine; nothing else is touched — notebooks, sources
and answers stay, and re-downloading takes a minute. A refusal because a server holds the files is **shown,
not retried**: the fix belongs to the person running the server.

A closing note, in the section's own words, that the engine is a separate program libris downloads and runs,
and that sampling and context sizes are fixed by it rather than by these settings.

**Polling**: while downloading, verifying, unpacking, or a release check is in flight, once a second — and
**stops the moment the state is final**, so an open settings page is not quietly asking the server forever.
The device report is re-asked only when the *installed build's tag* changes, never on first paint; and if
the probe fails the section says nothing about devices — an unavailable list is not a failure worth shouting
about, whereas an empty list with no sentence **is**.

#### The models card, inside the Models section

The other half of managed mode. The section above fetches a *program*; this one fetches the *weights* that
program loads — and they are two sections for a reason: a program can be replaced by a newer one, while a
model file is the thing a corpus was embedded **with**. So the catalogue is pinned on the server to a commit
and a digest, and **the page never offers a choice of quantisation** — that choice was made when the
catalogue was written, by somebody who could measure what it does to an answer. Nothing here is a setting: a
setting is something libris can apply, and downloading a file is not one.

One summary line: how many of the catalogue are installed, how much disk they take of the whole set, and —
when bytes are sitting half-fetched — that a stopped download is holding them. Then, in order:

- **A restart note**, shown only when it is true: a model arrived while the server was running, it reads its
  models at start, so restarting the engine above is what uses it.
- A failure, with the server's sentence and a link to throw the partial bytes away.
- **One card per model**: its **role** (`chat` / `embed`) as a small monospace tag **(contract: the role
  words)**, its name, its quantisation, and a state badge — installed, or **unverified** (titled: the files
  are the right size but libris did not download them, so nothing has checked their digest). Then the
  model's own one line of prose. Then either the files it is made of (name, size, the name titled with the
  full path), or what it would cost to fetch it (bytes and file count, plus anything already fetched), or
  the download in progress: its phase, the part name, bytes and percentage, a bar.
- **The verb follows the state**: download (titled with where it comes from) → **stop** (titled that the
  bytes so far are kept and it resumes) → **remove** (danger, titled that the files are deleted from this
  machine), with a link beside a half-done one to discard the partial download.
- A closing note in its own words: pinned to one commit of each repository and checked against its digest on
  arrival, because a model that changed silently would invalidate every vector already stored — and where
  the files live.

**Removing a model asks first** — it is the one act here that cannot be undone by pressing the button again:
gigabytes of download, and the corpus may hold vectors made with it. Lead names the size going; the note
repeats that notebooks, sources and answers stay, and that re-downloading takes as long as the download.

The card tells its parent when the set on disk changes, because the engine section's start button is disabled
while it can see no model to run — and that button used to stay dead until the app was reopened, which read
as "the app has to be restarted to notice what it just did". Downloads are **polled once a second only while
one is running**.

### 6.6 The setup wizard — the app before the app

Replaces the whole app while the machine cannot answer yet. The steps are **the facts the server reports, not
a script to be followed**: if the models are already on disk it opens on the third row.

Layout: a slim bar with the wordmark `libris` **(s)**, what this screen is **(s)**, and an `n of 3` counter
pushed right. A single reading-width column beneath, holding a **step list** and the **work panel** for the
current step.

The step list is three rows — **Engine**, **Inference device**, **Models** — each with a numbered dot that
becomes a tick, a name, and a one-line detail that carries the real state:

| step | the detail must distinguish |
|---|---|
| Engine | installed (with its tag) · known but not fetched (tag and size) · merely what it is |
| Inference device | that a device has been chosen and how many exist · that choices exist but none is chosen · that the engine is being asked · that it waits on the engine. **The first two are different sentences**, and this row is the only place the difference shows before the step opens |
| Models | ready · on disk but nothing serving them · how many bytes are left to fetch · waiting |

The active step is raised; done steps are ticks. The work panel asks one thing at a time:

1. **Get the engine** — that libris runs one local model server, that it is fetched from upstream when needed
   rather than installed by hand, and that it is checked against the digest upstream publishes; then the
   download action (**named by what it downloads** — the checks match on that label, §9.2).
2. **Where does it run?** — that these are the slots the engine itself reported, that libris picks one when
   you do not, and that it can be changed later in Settings; one row per reported device (slot name,
   description, size) with an action to use it, and an action to let libris choose underneath. If nothing has
   been reported, one quiet row says the engine has reported no accelerator and the CPU will be used.
   Choosing writes the pick **and** the acknowledgement token the server said it would accept, sent back
   unchanged. *The page does not compute what "confirmed for this engine on this machine" means — a page that
   computes it is a second copy of a rule the server owns* (ADR 0005). A blank pick means libris chooses,
   which is exactly why the acknowledgement is stored beside it rather than inferred from it.
3. **Get the models** — that one model answers and one embeds, that both are pinned downloads checked as
   they arrive, and that a different embedder would silently invalidate every vector already stored; then
   **one button for both** (or for the one that is missing), naming the size. Below it both models listed
   with quantisation and size, each reading installed, in flight, verifying, or waiting. Under the button,
   that a stopped download keeps its bytes and resumes where it left off.
   When the files are on disk but nothing is serving them, this panel becomes the **server start** action
   with one line saying the server loads a model when a question first asks for one.

Below the panel: a progress bar while downloading or verifying, saying the percentage or that the digest is
being checked, and any failure as one sentence — the server's own.

**Two things this wizard must not do, learned the hard way:**

- **One button means both models, so the queue of what was asked for has to outlive the page.** A reload does
  not cancel the running download — the fetcher keeps going on purpose — so a queue held only in component
  state comes back as one model installed and the other waiting forever. Store it beside the other per-user
  view state, and **do not** re-derive it from "what is missing": that would fetch gigabytes nobody asked for
  on a page load.
- **A queue must not run while the screen is showing an earlier step.** A stale queue from a previous session
  once fired the moment the first poll arrived — while the person was still downloading the engine, with no
  engine and the models not even on screen. A button that appears to fetch gigabytes it never mentioned is
  worse than a queue that has to be started again. Likewise a *refused* request (a busy fetcher, a failed
  write) is not a download in progress: drop the queue, rather than leaving the button saying "downloading"
  and the row saying "waiting" about a thing nobody is doing. And a queue does **not** retry a failure on its
  own — the reason is already on the page, and a queue that retries looks like a download that never ends.

The wizard's own reads are short and cheap: engine and models on a two-second loop while it is open and the
machine is not ready (panels elsewhere use job-scoped timers instead), and the device list follows the
**engine's tag**, never the clock — asked when a build lands or is removed, not every few seconds.

---

## 7. How state behaves

The single biggest source of inconsistency in the old UI was screens disagreeing about this. One answer each.

### 7.1 Jobs are polled; requests are not

| thing | asked | stops when |
|---|---|---|
| a notebook's sources | on open, then on a short loop | nothing is uploading and nothing is processing |
| the engine section | on open, then once a second | the download state is final **and** no release check is in flight |
| the models card | same | no download is running |
| the wizard | engine and models, two-second loop | the machine is ready, or the wizard is closed |
| the device report | when the installed build's **tag** changes | — not on a timer at all; it follows the engine, never the clock |
| anything at rest | **nothing** | — |

No timer may run while nothing is in flight. The old app had panels whose timers outlived their jobs, and that
is the difference between "an app that polls" and "an app quietly asking the server forever". Nothing is
streamed: the app has one polling discipline, and a second way to be out of date is not worth a websocket.

### 7.2 The server has the last word, and the UI must not be slow about it

- **Upload** — the response arrives with the source already processing, so **the row appears immediately**,
  before any poll would have revealed it.
- **A question** — on screen the instant it is sent, as a local row, replaced by the stored row at the end of
  the turn.
- **A conversation** — the row appears **only when the server has made one**. `+` creates nothing; the title is
  the server's; the list is refetched rather than guessed. An abandoned question leaves no empty conversation
  behind.
- **A rename** — committed to the server, then reflected in the list and the breadcrumb **from the same
  object**, so the two cannot disagree about which conversation is open.
- **A notebook's source count** — the sidebar's numbers come from the notebook list, so anything that changes
  sources asks for that list again rather than editing a number.
- **Never invent a row, a count, or a status.** If the app cannot see it, it says what it cannot see.

### 7.3 What a late response is allowed to do

- A response for a selection the reader left is **discarded** — they must not end up looking at one
  conversation under another conversation's name.
- A failed *refresh* of something already on screen **keeps the screen**.
- A turn started elsewhere is handed over, not fetched twice.
- A switch **clears** in-flight content first.

### 7.4 "Unknown" is a state, and it has words

Before an answer arrives, a screen must assert nothing. Not decoration:

| never | instead |
|---|---|
| `Nothing downloaded yet` before the read completes | say that this machine is still being read |
| a placeholder zero, a skeleton count, a fake thumbnail | nothing until there is a number |
| `Invalid Date`, or an empty cell | the word for unknown |
| a red failure while a check is still running | that it is checking |
| a stale device list presented as current | how stale it is, and since when |
| silence when a probe failed | a sentence naming which probe failed — an empty list with no sentence **is** the failure |
| a percentage of something with no progress stream | a dimmed row and the fact that it is processing |

### 7.5 Keyboard

- **Enter sends** in the composer; **Shift+Enter is a newline**; neither fires while an input method is
  composing.
- In a rename field: **Enter commits, Escape cancels**, blur commits.
- **Enter on a freshly opened confirmation cannot do the destructive thing** (§3.5).
- Dialogs close on **Escape**; the chunk viewer deliberately has none, because Escape belongs to whatever is
  behind it.
- Panel edges respond to **arrow keys and Home** once focused (§4.4).
- Hover-revealed actions stay **tab-reachable**, and on touch devices are simply always visible.
- Breadcrumb, section nav, panel switcher and rail toggles are real buttons, so they are in the tab order with
  real names.
- If the header advertises a key, **the key must work** (§10).

### 7.6 Assistive technology, and reduced motion

Announce failures (`role="alert"` on banners and error lines). Give icon-only controls real names — an
`aria-label`, plus a `title` that explains the consequence rather than the glyph. Mark icons `aria-hidden`.
Make the panel switcher a real tablist with `aria-selected`. Make progress bars real progressbars with a
current value. Keep the breadcrumb a labelled navigation. Label dialogs by purpose. Do not steal focus or
scroll while the reader has scrolled away (§6.2). Honour `prefers-reduced-motion` for the caret and every
pulse.

### 7.7 A screen must never

…show a spinner for a request. …claim progress it is not being given. …render a count it has not been sent.
…paraphrase a server's error. …write its own button, pill, empty state or modal. …block the page on a probe
(a long operation is a job: start it, then read its state). …put a second frame inside a frame. …offer a
choice it computes for itself.

---

## 8. Voice

**The old wording is deliberately not reproduced here.** It had grown bloated and developer-facing —
sentences explaining internals to someone who only wanted to press a button. Write your own, to these rules.

- **Short.** If a line needs a comma and a semicolon, it is two lines or none.
- **Plain.** Everyday words. No "utilize", "leverage", "please note", "successfully".
- **Direct.** Say what is true or what to do. A label is a noun or a verb, not a sentence.
- **Never apologetic, never chirpy.** No exclamation marks, no "oops", no "something went wrong", no
  reassuring filler when the news is bad.
- **Name the thing**, then the consequence: which file, which notebook, how many chunks, whether it can be
  undone.
- **Say what survives** whenever something is destroyed — that is the sentence people are actually looking
  for.
- **Explain a rule in one clause, only when the rule would otherwise look arbitrary** (why a download is
  pinned; why a re-run is free; why a choice takes effect on restart).
- **Errors are the server's sentence**, trimmed of anything a reader cannot act on. Never a wrapper.
- **No invented friendliness, no hedging.** "There is nothing to answer from" beats "It looks like there
  aren't any sources available just yet".
- Consistent grammar between siblings: every tooltip a verb phrase, every badge a state word, every empty
  state a title plus one line.
- **Word choice outranks a test.** If a check matches on a sentence and you can write it better, the sentence
  is yours: change the app's words and the assertion, in the same commit, and say so in the message (§9.2C).
  A check that pins old copy is pinning *that something is said here*, not that it is said well.
- **Only two kinds of word are fixed**, and only because they are the app's vocabulary rather than its prose:
  the names a check selects on, and the status words (§9.2A, §9.2B). Nothing else is.

---

## 9. The contract the automated checks hold you to

`make ui` drives a real Chromium against a real binary (harnesses `ui-upload ui-drop ui-ask ui-nav`;
`make ui-setup` runs the wizard against a fresh data directory and an `EXPECT_STEP`). They are the only
external evidence that a from-scratch UI still works, so **they are not to be edited to fit whatever you
happen to build** — not the structure, not the behaviour, not the counts, not the hooks below.

**One exception, and it is a rule rather than a loophole: wording.** Where a check matches on words the old
UI used, it asserts *that the app communicates something here* — never that the old sentence was good. If
you can write it better, change the app's wording and update that assertion: the same commit, one assertion,
and say in the message what you reworded. What never changes is **what the assertion proves**. It must still
be a real check against real text, and the same number of checks must pass afterwards. Making an assertion
`true`, deleting it, or loosening it until nothing could fail it is not rewording — it is deleting the test,
which is the one thing this section exists to prevent.

### 9.1 Class and structure hooks

Anything not listed here may be renamed freely. Names are given as the harness spells them.

**Shell** — `.header` · `.brand .mark` · `.crumbs .crumb` and `.crumbs .crumb.link` · `header .right
button` · `header p` · `main` · `.landing` (+ its button) · `button`

**Sidebar** — `.sidebar` (and `.closed` on the aside when collapsed) · `.sidebar .list .item` ·
`.sidebar .row.active` and `.row.active .item` · `.sidebar .row.active .count` · `.rename-open` ·
`.delete-open` · `.chat-rename-open` · `.chat-delete-open` · `.sidebar .chats` · `.sidebar .chat-open` ·
`.sidebar .chat-row.active .chat-open` · `.sidebar .chat-new` · `.sidebar .rail` and `.rail-item` ·
`.sidebar .section-head .rail-toggle` · `.rail-toggle` · `input.rename` · `input.chat-rename` ·
`.resizer`

**Dialogs** — native `dialog` elements carrying `class="new-notebook"`, `delete-notebook`,
`delete-chat`, `delete-source` (plus the shared modal class), and a `.actions .status` for the settings
bar.

**Chat** — `.chat` · `.messages` · `.turn.user` · `.turn.assistant` · `.turn.live` (the in-flight turn) ·
`.turn.live .answer .md` and `.turn.live .answer:not(.pulse)` · `.chips .cite` · `.answer` ·
`.answer .md` · `.answer .inline` (the citation button inside a sentence) · `.note` (the verdict line) ·
`.badge` · `.banner` · `.composer` · `.composer textarea` · `.composer button` · `.empty h2` ·
`.center` · `.status`

**Rendered answer** — `.md` plus real `h*`, `ul > li`, `table`, `th`, `tr`, `code`, `p code, li code`,
`pre`, `pre .hljs-keyword, pre .hljs-title` (highlight.js classes must survive), `.katex` (and **no
`.katex-error` may appear**), `.math`, `.table-wrap`, `img`, and links that stay links (`a[href=…]`).

**Source panel** — `.sidepanel` · `.sidepanel .seg` with `.seg-btn` and
`.seg-btn[aria-selected="true"]` · `.sidepanel .dropzone` · `.drop-hint` · `.sidepanel .list` ·
`.source` · `.source .name` · `.source .badge` · `.source .facts` · `.source .row-actions button` ·
`.iconlink` · `.filter input` · `.note` · `.sidepanel .empty h2` · `.sidepanel .resizer`

**Chunk viewer** — `.viewer` · `.viewer [aria-label="Back to sources"]` · `.viewer img` · `.viewer .cap`
· `.viewer .more` · `.doc-head h3` · `.chunk` and `.tile` · `.chunk .text` · `.text` · `.ord` ·
`.prov span:first-child` · `mark` · `.chunk.highlighted` / `.tile.highlighted` · `footer .small` ·
and every chunk carrying **`data-seg="<segment id>"`** (that is how a citation's target is found).

**Settings** — each generated group renders as a section whose **id is the group's own id**, so
`#servers` and `#models` exist; inside them `.engine` and `.models` · `.engine .facts > div` with `dt` /
`dd` · `.card` · `.panels > .actions` with `.actions .status` · `.subnav .nav-item` · `.panels input` ·
`#servers select` · `.label` · `.role` · `.summary` · `.footnote` · `.warnings li`

**Wizard** — `.setup` · `.steps li` (with `.active` / `.done`) and `.steps li.active .name` ·
`.devices .slot` · `.devices button`

### 9.2 The words that are contract

Three kinds, and they behave differently. **(A) and (B) are the app's vocabulary — fixed. (C) is everything
else, and the assertion moves with the copy, not the other way round.**

**(A) Attribute names used as selectors.** Match these exactly, or a check cannot find the control:
`title="Ask"` on the send control, and a `title` beginning `Stop` while a turn runs · `title="Add sources"`
· `title="See how this source was chunked"` and a `title` beginning `Run this file` · a source's remove
control titled with the words **`no other notebook`** when this was the last notebook using the file ·
`title="Hide panel"` · `aria-label="Back to sources"` · an `aria-label` on the viewer naming the source it
shows.

**(B) Vocabulary — these words, in these places.** This is the app's status language, not its style:

- a source's state, as the badge text exactly as the server sends it: `processing` · `ready` ·
  `unsupported` · `failed`
- a turn's verdict: `No evidence` · `Uncited` · `Stopped` (and `Interrupted` for the fourth)
- a model's role: `chat` · `embed`
- the citation label's **shape**: `Chunk 12 · page 7` — the word `Chunk`, the number, ` · `, then `page N`
  or `pages A–B` (§4.1). A check matches the shape, and so does every reader
- the two legend sentences the chunk viewer prints about overlap: grep `shaded text repeats` and
  `no chunk repeats` in `ui-drop.mjs` / `ui-upload.mjs`
- the wizard's wordmark `libris`, its screen title (grep `Set up Libris`) and its engine action
  (grep `Download llama`)
- byte sizes always in **binary units**, never decimal; and a ready source's fact line always naming its
  chunk count

**(C) All other wording is yours — update the check with it.** Any assertion that matches on words rather
than structure may be brought in line with better copy, under the rule at the head of this section. The table
is a **survey of what is pinned today, not a permission limited to these rows**: if you hit a wording
assertion that is not listed, it is the same case — fix it the same way, and add a line here so the next
person knows it moved. Each fragment below appears in **exactly one** check; grep it in the harness before
editing, and touch no other line of that harness:

| fragment | the check that pins it |
|---|---|
| `no other notebook uses goes with it` | `ui-nav.mjs` — delete-notebook confirmation |
| `sources it cites are untouched` | `ui-nav.mjs` — delete-chat confirmation |
| `No other notebook uses it` · `page image` | `ui-upload.mjs` — delete-file confirmation |
| `Nothing in this notebook yet` | `ui-upload.mjs` — empty source list |
| `No source matches` | `ui-upload.mjs` — filter with no match |
| `Nothing recorded yet` | `ui-nav.mjs` — activity panel |
| `Saved settings loaded` · `Unsaved changes` · `^Save changes$` | `ui-nav.mjs` — settings save bar |
| `Nothing downloaded yet` · `nothing to run until the engine` | `ui-nav.mjs` — machine facts |
| `Run models on` · `Start the server\|Stop the server` | `ui-nav.mjs` — machine verbs |
| `Pinned to one commit` | `ui-nav.mjs` — models footnote |
| `N of M installed · … on disk` · `^Download <size>$` | `ui-nav.mjs` — models summary and button |
| `no graphics`, **negated**: with no engine installed the row must *not* deny that the box has a GPU | `ui-nav.mjs` |

What those sentences have to *do* is described in §5.5, §6.3 and §6.5, and that part is load-bearing: name
the thing, name the loss, name what survives.

### 9.3 Behaviours the checks actually prove

Worth knowing before building, because they pin decisions that look cosmetic:

- the notebook list opens at its default width; **dragging widens it, the width is stored, arrow keys
  resize without a mouse, double-click resets**;
- `New` opens a dialog with the **name field focused** and `Create` disabled while blank; typing a name
  makes the notebook that name;
- rename **selects the current name**, Enter commits, Escape abandons, and the change reaches the server;
- delete asks first; **the confirmation says which files go and which stay**; the destructive button is
  red **and Cancel has the focus**; Escape closes it and the notebook is still there;
- after deleting, **another notebook is open in its place and the collapsed rail forgets it too**;
- the header shows **nothing** about the chunk viewer, open or shut; the viewer gives the panel back to
  the list; nothing is left open over the app;
- uploads: a `202` row appears at once, starts `processing`, and **polling reaches `ready`**; sizes are
  **binary units**; identical bytes are **deduped, not duplicated**; an unsupported upload is still
  stored, says so, gives its reason visibly, and can still be opened;
- a 200-word text becomes three chunks stepping by 100 words, and **the marked overlap is literally the
  previous chunk's tail**, with the legend explaining the shading;
- drops: dragging highlights the zone, dropping clears the overlay, a file dropped **elsewhere on the
  page is not a navigation**, dragging over the chrome is allowed but **inert**, and a drop outside the
  panel uploads nothing;
- ask: the composer opens, `Ask` is disabled with nothing typed and enabled by typing; **the question
  appears immediately**; a live turn is shown while it runs; **a citation label never reaches the reader
  as text**; the answer carries a clickable citation, the label says where inside the source it points,
  and the label is also a button inside the sentence; an answered turn carries **no** warning; the
  question starts the conversation, the sidebar lists it, and the breadcrumb ends with it; a citation
  opens its source **ringed on the cited chunk**, and **the chat is still there when the source closes**;
- after a reload the thread and the composer share **one measure** — the check compares their widths —
  and a reloaded answer keeps its citations;
- the wizard: it opens, **the app behind it is not mounted**, it says what it is, lists three steps,
  opens on the expected step, offers the engine download or the slots the engine reported, and choosing a
  slot lets go of the step; a ready machine goes straight in;
- **every harness fails on an uncaught page error or a console error.** A single React-style warning from
  a stray attribute, or an unused-var lint escaping into the console, is a broken build.

---

## 10. The mistakes not to repeat

The reason this rebuild exists. Each was observed in the code being deleted.

1. **Three copies of the error banner** — the shell, the notebook and the chat each had their own, with
   different margins, different radii, and only one of them dismissible. There is one pattern (§4.6):
   build it once and put it in three places.
2. **Three pill implementations** — a `Badge` component, a global `.chip` class, and hand-rolled `.badge`
   spans inside the machine sections. Plus four *more* pill-ish classes (`.cite`, `.inline`, `.device`,
   `.tag`, `.role`, `.count`). Decide once: badge = state, chip = fact, count = number, inline = citation
   inside prose — and no screen invents a sixth.
3. **Form controls styled four times** (the new-notebook field, the settings inputs, the rename input, the
   filter box), each slightly different, one of them reaching for an accent border that the global focus
   ring already draws.
4. **Two panel geometries**: `panel.ts` unified the *behaviour*, but both components still copied the
   handle's CSS and the `html.resizing` global rules into their own styles. Shared behaviour with
   duplicated looks drifts just as surely.
5. **Non-existent design tokens.** The models card styled itself with `--text-muted`, `--primary`,
   `--success-subtle` and `--attention-subtle` — **none of which are defined anywhere**. Two of them
   carried hard-coded light-mode hex fallbacks (`#dafbe1`, `#fff8c5`), so those badges were wrong in dark
   mode; the other two silently did nothing at all, and the muted text was inheriting. An undefined token
   is a screen that is quietly wrong in one theme, and no check catches it — screenshots do (§0).
6. **A stale document reference** in a stylesheet comment, citing an ADR number that does not exist.
   Comments that point at decisions must be able to be opened.
7. **A search box that searches nothing**, with a `/` hint inside it — a control that promises a feature
   the app does not have, in the header, forever. Either wire it or delete the box and the key.
8. **The wizard bypassed the primitives**, writing raw button classes instead of using the Button
   component — which is exactly how the same control ends up with two hover states.
9. **A raw global class threaded through a component's API** (the composer's round shape was passed in as
   a CSS string). A component's variants should be its own vocabulary, not global CSS leaking through.
10. **Two implementations of one choice** — the wizard's device step and the settings device picker, with
    slightly different wording for the same decision. Two renderers are fine (different space), one copy
    rule is not met by copy-paste.
11. **Pulse/blink keyframes defined three times** (the caret, the panel dot, the chat's loading line) at
    three different speeds. Work has one tempo.
12. **`role="presentation"` on the drop zone** while it was the only thing telling sighted users files
    could be dropped there. A region that takes an action deserves a name.

---

## 11. Open, to be decided at build time

- **Header search** — there is no search endpoint. Ship the header **without** the box, or wire it to a
  client-side filter of what is already loaded (notebooks and conversations are both in memory). Do not
  ship a dead input. If it stays, the `/` hint must work.
- **The Activity panel** is a placeholder by design (the app does not yet record a log). Keep the empty
  state, do not fake a timeline.
- **Where the reading measure is enforced** — one custom property on the column, used by every block in
  it. The check that compares the answer's width with the composer's will tell you if you got this wrong.
