<script lang="ts">
  import type { Chat, Notebook } from '../lib/types';
  import { dragHandle, NOTEBOOKS, readOpen, readW, writeOpen } from '../lib/panel';
  import { renameChat, renameNotebook } from '../lib/api';
  import { errorMessage } from '../lib/format';
  import Icon from '../lib/Icon.svelte';
  import Button from '../lib/Button.svelte';

  let {
    notebooks,
    activeId,
    chats,
    chatId,
    onselect,
    onnew,
    onrenamed,
    ondelete,
    onchat,
    onnewchat,
    onchatrenamed,
    onchatdelete,
    onerror
  }: {
    notebooks: Notebook[];
    activeId: string | null;
    /** The active notebook's conversations, newest first. */
    chats: Chat[];
    /** Which one is open. Empty is a draft: a thread no question has started yet. */
    chatId: string;
    onselect: (id: string) => void;
    onnew: () => void;
    /** The rename is already committed on the server; this only updates the list. */
    onrenamed: (id: string, name: string) => void;
    /** Deletion is asked for here and decided by the shell, which owns the selection. */
    ondelete: (notebook: Notebook) => void;
    onchat: (id: string) => void;
    /** Back to an empty thread. Nothing is created until something is asked in it. */
    onnewchat: () => void;
    onchatrenamed: (id: string, title: string) => void;
    onchatdelete: (chat: Chat) => void;
    onerror: (message: string) => void;
  } = $props();

  // Width and open state are the user's, not the stylesheet's, so they live in
  // localStorage and are shared with the right-hand panel through lib/panel.ts.
  let width = $state(readW(NOTEBOOKS));
  let open = $state(readOpen(NOTEBOOKS));
  let editingId = $state<string | null>(null);
  let draft = $state('');
  // A conversation renames the same way a notebook does, so it gets its own pair of
  // these: one row is being edited at a time, in one of the two lists.
  let editingChatId = $state<string | null>(null);
  let chatDraft = $state('');

  const handle = (node: HTMLElement) =>
    dragHandle(node, { spec: NOTEBOOKS, get: () => width, set: (n) => (width = n) });

  function toggle() {
    open = !open;
    writeOpen(NOTEBOOKS, open);
  }

  function beginRename(nb: Notebook) {
    editingId = nb.id;
    draft = nb.name;
  }

  function edit(el: HTMLInputElement) {
    el.focus();
    el.select();
  }

  /**
   * Committed on Enter and on blur, cancelled on Escape — a click elsewhere is an
   * implied "that is the name". A blank or unchanged draft reverts rather than
   * asking the server to reject it: an unnamed notebook is not a state worth
   * having, and the name is the only thing the row says.
   */
  async function commit(nb: Notebook) {
    if (editingId !== nb.id) return;
    editingId = null;
    const name = draft.trim();
    if (!name || name === nb.name) return;
    try {
      await renameNotebook(nb.id, name);
      onrenamed(nb.id, name);
    } catch (e) {
      onerror(errorMessage(e));
    }
  }

  function beginChatRename(chat: Chat) {
    editingChatId = chat.id;
    chatDraft = chat.title;
  }

  /** The same rule as a notebook's, for the same reason: a conversation with no name
     is the thing the list exists to prevent, so a blank draft reverts. */
  async function commitChat(chat: Chat) {
    if (editingChatId !== chat.id || !activeId) return;
    editingChatId = null;
    const title = chatDraft.trim();
    if (!title || title === chat.title) return;
    try {
      await renameChat(activeId, chat.id, title);
      onchatrenamed(chat.id, title);
    } catch (e) {
      onerror(errorMessage(e));
    }
  }
</script>

<aside class="sidebar" class:closed={!open} style:width={open ? `${width}px` : `${NOTEBOOKS.rail}px`}>
  {#if !open}
    <button
      type="button"
      class="rail-toggle"
      title="Show notebooks"
      aria-label="Show notebooks"
      onclick={toggle}
    >
      <Icon name="panel-expand" size={16} />
    </button>
    <ul class="rail">
      {#each notebooks as nb (nb.id)}
        <li>
          <button
            type="button"
            class="rail-item"
            class:active={nb.id === activeId}
            title={nb.name}
            aria-label={nb.name}
            onclick={() => onselect(nb.id)}
          >
            <Icon name="book" size={15} />
          </button>
        </li>
      {/each}
    </ul>
  {:else}
    <div class="section">
      <div class="section-head">
        <h2>Notebooks</h2>
        <span class="head-actions">
          <Button variant="ghost" size="sm" iconOnly title="New notebook" onclick={onnew}>
            <Icon name="plus" size={14} />
          </Button>
          <button
            type="button"
            class="rail-toggle"
            title="Hide notebooks"
            aria-label="Hide notebooks"
            onclick={toggle}
          >
            <Icon name="panel-collapse" size={16} />
          </button>
        </span>
      </div>

      {#if notebooks.length === 0}
        <p class="empty-hint">No notebooks yet.</p>
      {:else}
        <ul class="list">
          {#each notebooks as nb (nb.id)}
            <li>
              {#if editingId === nb.id}
                <input
                  class="rename"
                  type="text"
                  use:edit
                  aria-label="Notebook name"
                  maxlength="200"
                  bind:value={draft}
                  onkeydown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      commit(nb);
                    } else if (e.key === 'Escape') {
                      e.preventDefault();
                      editingId = null;
                    }
                  }}
                  onblur={() => commit(nb)}
                />
              {:else}
                <div class="row" class:active={nb.id === activeId}>
                  <button type="button" class="item" onclick={() => onselect(nb.id)}>
                    <Icon name="book" size={14} />
                    <span class="label truncate">{nb.name}</span>
                    {#if nb.source_count > 0}
                      <span class="count">{nb.source_count}</span>
                    {/if}
                  </button>
                  <button
                    type="button"
                    class="row-act rename-open"
                    title="Rename {nb.name}"
                    aria-label="Rename {nb.name}"
                    onclick={() => beginRename(nb)}
                  >
                    <Icon name="pencil" size={13} />
                  </button>
                  <button
                    type="button"
                    class="row-act delete-open"
                    title="Delete {nb.name}"
                    aria-label="Delete {nb.name}"
                    onclick={() => ondelete(nb)}
                  >
                    <Icon name="trash" size={13} />
                  </button>
                </div>
              {/if}

              <!-- Only the notebook you are in expands. Its conversations are the
                   second level of the one tree the breadcrumb reads, and a chevron
                   per row would be a control for a question nobody asks: which other
                   notebook's chats do I want to see right now? -->
              {#if nb.id === activeId && chats.length > 0}
                <ul class="chats">
                  <li>
                    <button type="button" class="chat-new" onclick={onnewchat}>
                      <Icon name="plus" size={13} />
                      <span>New chat</span>
                    </button>
                  </li>
                  {#each chats as c (c.id)}
                    <li>
                      {#if editingChatId === c.id}
                        <input
                          class="rename chat-rename"
                          type="text"
                          use:edit
                          aria-label="Chat title"
                          maxlength="200"
                          bind:value={chatDraft}
                          onkeydown={(e) => {
                            if (e.key === 'Enter') {
                              e.preventDefault();
                              commitChat(c);
                            } else if (e.key === 'Escape') {
                              e.preventDefault();
                              editingChatId = null;
                            }
                          }}
                          onblur={() => commitChat(c)}
                        />
                      {:else}
                        <div class="row chat-row" class:active={c.id === chatId}>
                          <button
                            type="button"
                            class="item chat-open"
                            title={c.title}
                            aria-label={c.title}
                            onclick={() => onchat(c.id)}
                          >
                            <span class="label clamp">{c.title}</span>
                          </button>
                          <button
                            type="button"
                            class="row-act chat-rename-open"
                            title="Rename {c.title}"
                            aria-label="Rename {c.title}"
                            onclick={() => beginChatRename(c)}
                          >
                            <Icon name="pencil" size={12} />
                          </button>
                          <button
                            type="button"
                            class="row-act delete-open chat-delete-open"
                            title="Delete {c.title}"
                            aria-label="Delete {c.title}"
                            onclick={() => onchatdelete(c)}
                          >
                            <Icon name="trash" size={12} />
                          </button>
                        </div>
                      {/if}
                    </li>
                  {/each}
                </ul>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </div>

    <button
      type="button"
      class="resizer"
      title="Drag to resize · double-click to reset"
      aria-label="Notebook list width. Drag, or use arrow keys."
      use:handle
    ></button>
  {/if}
</aside>

<style>
  .sidebar {
    position: relative;
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    border-right: 1px solid var(--border);
    background: var(--canvas-subtle);
  }
  .sidebar.closed {
    align-items: stretch;
    padding-top: 8px;
  }
  .section {
    padding: 12px 8px;
  }
  .section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 4px 8px 8px;
  }
  .section-head h2 {
    font-size: 12px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.4px;
    color: var(--fg-muted);
  }
  .head-actions {
    display: flex;
    align-items: center;
    gap: 2px;
  }
  .list {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 2px;
    border-radius: var(--radius);
  }
  .row:hover,
  .row:focus-within {
    background: var(--hover);
  }
  /* Raised, not tinted: on a subtle column the selected notebook has to come forward,
     and the accent bar on its edge is the same mark the panel rail uses. */
  .row.active {
    background: var(--elevated);
    box-shadow: inset 2px 0 0 var(--accent), var(--shadow-sm);
  }
  .item {
    display: flex;
    align-items: center;
    gap: 9px;
    flex: 1;
    min-width: 0;
    height: 32px;
    padding: 0 8px;
    font: inherit;
    font-size: 14px;
    color: var(--fg);
    text-align: left;
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .row.active .item {
    font-weight: 600;
  }
  .label {
    flex: 1;
  }
  .count {
    display: inline-grid;
    place-items: center;
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    font-size: 11px;
    font-weight: 600;
    line-height: 1;
    color: var(--fg-muted);
    background: color-mix(in srgb, var(--fg-muted) 18%, transparent);
    border-radius: 999px;
  }
  .row.active .count {
    background: color-mix(in srgb, var(--accent) 16%, transparent);
    color: var(--accent);
  }
  /* A row's actions wait for the pointer, and the destructive one is coloured for what
     it does even at rest — a trash can shaped like a pencil is how you get a notebook
     deleted by accident while reaching for the rename. */
  .row-act {
    display: none;
    padding: 4px;
    color: var(--fg-muted);
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .row:hover .row-act,
  .row:focus-within .row-act,
  .row-act:focus-visible {
    display: inline-flex;
  }
  .row-act:hover {
    color: var(--fg);
    background: var(--canvas);
  }
  .delete-open {
    color: var(--danger);
  }
  .delete-open:hover {
    color: var(--danger);
    background: color-mix(in srgb, var(--danger) 14%, transparent);
  }
  /* The second level. A rule rather than an indent says what it is: these belong to
     the row above, and two spaces of padding would read as a mistake in the list.
     Quoted rather than coloured when open, because a notebook and one of its chats are
     active together and two accent bars would have to be told apart at a glance. */
  .chats {
    margin: 2px 0 4px 15px;
    padding: 0 0 0 9px;
    list-style: none;
    border-left: 1px solid var(--border-muted);
  }
  .chat-open {
    height: auto;
    padding: 5px 8px;
    font-size: 13px;
    line-height: 1.35;
    color: var(--fg-muted);
  }
  .chat-row:hover .chat-open,
  .chat-row.active .chat-open {
    color: var(--fg);
  }
  .chat-row.active .chat-open {
    font-weight: 600;
  }
  /* Titles are the first question, up to seventy characters. Two lines and an ellipsis
     keep them tellable apart, and a whole row of them stays readable as a list instead
     of a column of clipped fragments. */
  .clamp {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    white-space: normal;
    overflow-wrap: anywhere;
  }
  .chat-new {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    padding: 5px 8px;
    font: inherit;
    font-size: 12.5px;
    color: var(--fg-muted);
    text-align: left;
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .chat-new:hover,
  .chat-new:focus-visible {
    color: var(--accent);
    background: var(--hover);
  }
  .chat-rename {
    font-size: 13px;
  }

  .rename {
    flex: 1;
    min-width: 0;
    width: 100%;
    padding: 5px 8px;
    margin: 1px 0;
    font: inherit;
    font-size: 14px;
    color: var(--fg);
    background: var(--canvas);
    border: 1px solid var(--accent);
    border-radius: var(--radius);
    outline: none;
  }
  .empty-hint {
    padding: 4px 8px;
    font-size: 13px;
    color: var(--fg-muted);
  }

  /* Collapsed: a rail of book icons, each titled by the name it cannot show. */
  .rail {
    margin: 0;
    padding: 4px 6px;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 2px;
    overflow: hidden;
  }
  .rail-item {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    color: var(--fg-muted);
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .rail-item:hover {
    color: var(--fg);
    background: var(--hover);
  }
  .rail-item.active {
    color: var(--fg);
    background: var(--elevated);
    box-shadow: inset 2px 0 0 var(--accent), var(--shadow-sm);
  }
  .rail-toggle {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    margin: 0;
    color: var(--fg-muted);
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .rail-toggle:hover {
    color: var(--fg);
    background: var(--hover);
  }
  .closed .rail-toggle {
    align-self: center;
    margin-bottom: 4px;
  }

  /* The grip is invisible until hovered: 1px of border is not grabbable, but a
     visible bar would read as content in an otherwise flat column. */
  .resizer {
    position: absolute;
    padding: 0;
    border: none;
    top: 0;
    right: -3px;
    z-index: 11;
    width: 7px;
    height: 100%;
    cursor: col-resize;
    background: transparent;
  }
  .resizer:hover,
  .resizer:focus-visible {
    background: var(--accent);
    opacity: 0.35;
    outline: none;
  }

  :global(html.resizing) {
    cursor: col-resize;
  }
  :global(html.resizing) * {
    user-select: none;
  }
</style>
