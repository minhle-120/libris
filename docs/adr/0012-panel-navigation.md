# 0012 — Sources and Activity are a panel beside the chat, not pages instead of it

Date: 2026-11-22
Status: accepted, implemented (`web/src/components/SidePanel.svelte`,
`web/src/lib/panel.ts`, `web/src/components/NotebookView.svelte`) — reverses the tab row
that §7 of `AGENTS.md` recorded, and takes the chunk inspector out of the top layer. No
numbered ADR is reversed.

*Amended 2026-11-22 by ADR 0014, which supersedes the breadcrumb clauses below — the
panel view left the header and the chat's title took its place. The Context, the three
columns, `lib/panel.ts` and the inspector-as-panel-step all stand as written.*

## Context

A notebook was one column with three tabs — Sources, Chat, Activity — and an inspector
that opened over all of it. Three things about that shape only showed up once the app was
used rather than described:

- **The tabs were not peers.** Sources and Activity are answers about the corpus; Chat is
  the thing you are doing. Switching between them threw away the source list the question
  was asked next to, which is the exact moment a user wants both.
- **The inspector took the answer with it.** A citation opens the source it cites. As a
  modal, that also dimmed and blocked the answer being checked, so reading a citation
  meant losing your place, and the view that existed to build trust in an answer made the
  answer harder to read.
- **The tab row read as chrome.** A strip of words across the top of a panel is what a
  file manager and a settings page both do; here it separated the menu from the thing it
  menus, and looked like it belonged to neither.

Activity was, at that point, an empty state for an ingest-progress stream that is not
built. Its stated future was wider than a tab: see what is happening under the hood —
ingest jobs, the research loop, the retrieval behind an answer.

## Decision

**A notebook is three columns and never changes page.** Notebook list on the left, chat
in the middle, and one **panel** on the right holding Sources and Activity as a segmented
view. The chunk inspector is a step inside that panel.

- The panel repeats the left column's geometry: 260..640 px dragged from its inner edge,
  a 44 px rail when collapsed, its width in `localStorage` under the same key shape. The
  rule is shared (`lib/panel.ts`), the numbers are not — mirror geometry, never values.
- The breadcrumb (`libris / <notebook> / <panel view>`) sheds the level a column names
  when that column collapses, and gains one when the inspector opens. Sources stays
  non-clickable while the chat is open underneath it; it is the ground, not a step up.
- **The chat is not in the breadcrumb.** A breadcrumb names where you are by the levels
  you can climb to; a chat is not one of them, and it has a title and a picker of its own
  where it is actually switched.
- **The inspector replaces the panel's content** rather than floating over the app, so the
  answer that cited it stays on screen, and the way back is the crumb that became a link.
- Activity ships as a frame for the trace view, not a trace: the research loop's tool
  calls render there now, because a page of nothing plus a promise reads worse than one
  check arriving late. Ingest progress lands in the same list when the stream exists.

## Consequences

- **Sources is one column, about 340 px wide.** A source, its derived documents and their
  actions have to read as one list, so hierarchy is shown by the one means that width
  allows — indentation — and the second level is quiet: smaller, one button. The count a
  PDF row carries says `2 rasters`, not `2 × 459×459` and not a size: it is true of the
  derived documents rather than of the bytes on disk, and those bytes live in an asset the
  user did not ask to size.
- **Escape no longer closes the inspector.** It belonged to `showModal()`. The way out is
  the Back button, the Sources crumb, or a different source's chip, and the last of those
  is an upgrade the modal could not offer.
- Two resizable columns is now a real cost rather than a theoretical one, and it is
  measured, not guessed: at the default 288 + 340 the middle is 812 px at 1440, 552 at
  1180, 272 at 900 and **12 px at 640**, because neither column gives way and the chat is
  the only flexible thing between them. Nothing overflows sideways — the harnesses assert
  that — it simply becomes unreadable. **Decided 2026-11-22: nothing gives way.** The
  target is a desktop window, and a responsive mode with two layout modes is a second
  thing to build and to test at both ends of its breakpoint, for a window size nobody
  uses this app at. The alternatives (a floor under the chat with the columns shrinking
  first; auto-collapsing the panel; an overlay drawer under a breakpoint) are recorded
  here so the question is answered rather than re-asked, and the numbers so the cliff is
  visible without measuring again.
- The breadcrumb's rules became testable things worth asserting: `ui.crumbs()` in the
  harness, plus checks that a collapsed panel shortens it, that it never names a chat, and
  that Sources is not a link while the chat is open below it.
- If the under-the-hood view grows past a list — retrieval scores beside the answer, a
  diff of what re-ingest changed — it may want to be a second document in the middle
  column with a rail between them. This decision does not close that off — a panel that
  can collapse costs nothing when it is not wanted — but it does not commit to it either,
  because the view that would fill it is the ingest stream, which is not built.
