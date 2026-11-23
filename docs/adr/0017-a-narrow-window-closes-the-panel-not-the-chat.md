# 0017 — A narrow window closes the panel, not the chat

Date: 2026-11-23
Status: accepted, implemented (`narrowRule` and `MIN_CHAT` in `web/src/lib/panel.ts`, applied
by `web/src/App.svelte`, checked in `scripts/ui-nav.mjs`). It commits to the thing ADR 0012
left open — "a panel that can collapse costs nothing when it is not wanted — but it does not
commit to it either" — for a reason 0012 did not consider: not that the panel has outgrown
its content, but that the window has run out of room.

## Context

A notebook is a fixed sidebar, a fixed panel, and a middle column that is whatever is left.
Both fixed columns are user-draggable and remember their width, so at the defaults a 900px
window leaves the chat **272px** and a 640px window leaves it **40px** — measured, not
estimated, and the first number is the one people were typing into. Nothing in the layout
objected, because the middle column is the only one with no opinion about its own width. At
272px an answer's table cannot fit and gets a horizontal bar; at 40px an answer is a column
of single words.

Three ways out were on the table, and all three were measured first:

- **A minimum width on the chat, with the page scrolling sideways.** This moves the scrollbar
  rather than removing it, and it puts one on the whole application — a window that scrolls
  horizontally when its entire UI is one screen is worse than the bar under a table.
- **Breaking words inside table cells** (`overflow-wrap: anywhere` on `th`/`td`), which makes
  any table fit any column. It is the exact thing ADR 0016's fix removed: `Temperature` came
  out as `Temperatur/e`. Solving a layout problem by mangling typography is how the last
  round of this ended.
- **Leaving it.** Honest, and it leaves the reading column — the one thing the app is *for* —
  as the thing that loses.

## Decision

Below `MIN_CHAT` (480px) of reading column, **the panel becomes its 44px rail**. The sidebar
is not touched: it is the primary navigation and the tree of notebooks and conversations, and
if the app starts deciding which of the user's deliberate choices to undo, that is the one it
should not start with.

`narrowRule` is a pure function of the window's width and the two panels' remembered
geometry, and it returns **two** numbers, not one: `tooNarrow` is measured with the panel as
it stands now — including a panel the user dragged wide — and `hasRoom` is measured against
the narrowest panel worth opening (`SIDE.min`, 260) rather than the remembered width. With a
single threshold the rule oscillates, because the panel's own width is what makes the chat
narrow: closing it creates the room that reopening it immediately destroys.

`App` applies the rule **only when the answer changes**, i.e. on the edge of a resize, and
acts only on its own act:

- Closing by rule does not write `localStorage`. A preference the window's width overwrote is
  not the user's preference, and the next wide window would open on the app's idea of what
  they wanted. So the setting survives, and the panel returns at the width it closed at.
- A panel the user opened by hand in a narrow window stays open, through further resizes,
  until the window has been wide again. A rule that fires on every resize event takes the
  panel away a second after the user put it back, which is worse than the bar it removes.
- A panel the user closed is never opened at them on the way out of narrow.

## Consequences

- At 640px the chat goes from 40px to **308px**, and no part of the app overflows the window.
  The source list is one rail click away; `ui-drop` now opens it that way at that width,
  which is also what a person at 640px does.
- The middle column now has a defender, so the sharp edge in `AGENTS.md` about two fixed
  columns is a measurement lesson rather than an open bug. It stays written down, because the
  numbers are the reason this exists.
- Every browser harness names the window it runs in. Headless Chrome defaults to **800×600**,
  which is inside this rule's range — until now all four harnesses inherited it silently, so
  the panel was open and the chat was 172px wide in every one of them, and no check had ever
  measured that. A harness that does not set a viewport is a harness testing a layout nobody
  chose.
- `MIN_CHAT` is a design constant with one home. Changing it changes which windows get the
  rail, and the six checks in `ui-nav` are the ones that notice.
- The code fence keeps its sideways scroll. A line of code that folds can be read as two
  statements, which is a cost paid by whoever reads the code, unlike a table's bar, which is
  paid by everyone looking at the page. The bar itself is now thin, `--border`-coloured and
  invisible until the pointer is in the region that scrolls.
