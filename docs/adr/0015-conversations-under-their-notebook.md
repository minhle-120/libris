# 0015 — A conversation is chosen where notebooks are chosen

Date: 2026-11-22
Status: accepted, implemented (`web/src/App.svelte`, `web/src/components/Sidebar.svelte`,
`web/src/components/ChatPanel.svelte`, `internal/app/chats.go`, `internal/httpapi/chat.go`)
— supersedes the chat-picker clauses of ADR 0014, which superseded 0012's breadcrumb. 0014's
two levels, its rule that only `libris` is a button, and its rule that a chat with no
question has no crumb all stand.

## Context

0014 left the choice of conversation in the composer's header, because the composer was the
only component that knew which chat the server had open. In use, that header was a
forty-character-wide control asked to name a thing whose title is the first question anyone
asked in it, up to two hundred characters long: one conversation legible at a time, the rest
truncated without even an ellipsis, no rename, no delete. The rename endpoint it needed
(`PATCH …/chats/{chatID}`) existed and had nothing that could reach it.

Two things made the left column the honest home. The notebook list is the only place with the
width to show several titles side by side and the room to rename one in place. And the
collapsed rail already carries buttons whose whole job is to reach the hidden column, so a
sidebar control for conversations is not a new kind of affordance — it is the one this app
already has.

## Decision

A notebook's conversations are the second level of the notebook list. `ChatPanel` has no
conversation switcher, and no header at all.

- The list is `ul.chats` inside the open notebook's `li`, indented to the notebook's *title*
  rather than to its row, and only the open notebook expands. A chat is not a notebook and
  does not borrow its geometry.
- `App` owns `chatId` and the fetched list. `ChatPanel` takes `chatId` as a prop and reports
  upward only the chat a question *started* — the one thing it can know before `App` does.
- The HTTP shape follows the screen: `/api/notebooks/{id}/chats` is the only chat path,
  `POST …/ask` carries `chat_id`, and the `done` frame carries the stored row including its
  `chat_id`, which is how a question asked with none selected finds out which conversation it
  started. A notebook owns its chats, so deleting a notebook takes them by cascade and no
  conversation outlives the notebook that names it.
- A question asked with no conversation selected starts one. The tree must never be the thing
  standing between a reader and a question — and there is deliberately no "New conversation"
  button in the sidebar, because a conversation is a thing that happens.

## Consequences

- One more request per notebook switch: the list is fetched with the notebook, not with the
  chat, because a notebook with no chats is a normal thing to click on.
- `App` holds both the breadcrumb's last level and the list it came from, so the header and
  the open row cannot drift apart; `ui-nav` asserts the crumb equals the open row's text.
- `ChatPanel` is driven entirely by props, so renaming a conversation in the sidebar has one
  place to be true.
- The collapsed rail cannot reach a conversation. Chats are not tabs, and a rail item would
  be a third way to say what the row already says; a conversation appears the moment the
  sidebar is open.
- Deleting a conversation is a row action through `ConfirmDialog`, naming the conversation,
  and its copy says that the sources it cites are untouched. That sentence is the difference
  between this and deleting a notebook, and the box is where a reader learns it (ADR 0013).
