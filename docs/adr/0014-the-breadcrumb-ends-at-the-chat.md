# 0014 — The breadcrumb ends at the chat you are reading

Date: 2026-11-22
Status: accepted, implemented (`web/src/App.svelte`, `web/src/components/ChatPanel.svelte`,
`web/src/components/NotebookView.svelte`) — supersedes the breadcrumb clauses of ADR 0012
(*Sources and Activity are a panel beside the chat*). It does not touch that ADR's three
columns, its shared `lib/panel.ts` rule, or the inspector replacing the panel's content.

## Context

0012 put the panel view in the header — `libris / <notebook> / Sources` — and excluded the
chat on the grounds that a breadcrumb names levels you can climb to, and the chat is the
ground under your feet. In use that read as a lie twice a day.

Sources is not somewhere you go. It is a thing you open beside an answer, the segment
control already says which one is open, and the header repeating it bought a third crumb
whose only behaviour was to change when a column collapsed. Meanwhile the one thing the
header left out is the thing actually being read: *which* of a notebook's chats this
answer belongs to. Nothing else on screen says so — a chat is a `<select>` with no title
beside it, and since the server titles a chat from its first question, a notebook with
five chats looked like the same page five times.

## Decision

`libris / <notebook>`, plus the chat's title as the last level whenever a chat is open.

- `libris` is the only button. Both remaining levels are where you are, and the page you
  are on is not a link to itself.
- No chat, no third level. The title is the server's, from the first question, so before
  a question is asked there is nothing to say and the breadcrumb is two levels long.
- The panel leaves the breadcrumb entirely: it is never named there, a collapse no longer
  shortens the list, and an open inspector adds no level.

`App` still owns the panel tab and whether the panel is open, because the collapsed rail's
buttons name a tab rather than toggle a box. `ChatPanel` reports the open chat's title up,
because only it knows which chat the server has open.

## Consequences

- The header stops reporting the panel. `ui-nav` asserts the crumb list is *unchanged*
  across a Sources↔Activity switch, a panel collapse and an inspector open, so the two
  concepts cannot quietly reattach.
- The inspector loses the breadcrumb exit 0012 gave it (Sources became a link while it was
  open). Its own `Back to sources` button is now the only way out, which is what
  `ui-upload` and `ui-ask` click.
- A chat title became load-bearing chrome. A chat with a blank title shows two crumbs
  rather than a dangling separator; `New chat` is a real title, so the header will show
  the words `New chat` until a rename gives it something better.
- Nothing about 0012's geometry moves: the panel, its rail, its mirrored drag range and
  the shared clamp rule are untouched. What changes is that the segment control is now the
  only thing that says which view is open — so it must keep saying it.
