<script lang="ts">
  import {
    app,
    deleteChat,
    deleteNotebook,
    newConversation,
    openChat,
    openNotebook,
    renameChat,
    renameNotebook,
    setOpen,
    NOTEBOOKS,
    setWidth
  } from '../lib/store.svelte';
  import { dragHandle } from '../lib/panel';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Modal from '../lib/ui/Modal.svelte';

  /**
   * The notebook list, and the conversations of the one notebook you are in.
   *
   * Conversations are a second level under the open notebook rather than a chevron per row:
   * which other notebook's chats would you want to see right now? The level is indented to the
   * notebook's *title* and carries a vertical rule — the rule is the tree, the alignment is the
   * sentence.
   */
  const open = $derived(app.sidebarOpen);
  const width = $derived(app.sidebarOpen ? app.sidebarW : NOTEBOOKS.rail);

  let editing = $state<string | null>(null);
  let draft = $state('');
  let doomed = $state<string | null>(null);
  let doomingChat = $state<string | null>(null);
  let error = $state('');

  const target = $derived(app.notebooks.find((n) => n.id === doomed));
  const chatTarget = $derived(app.chats.find((c) => c.id === doomingChat));

  function startRename(id: string, name: string) {
    editing = id;
    draft = name;
  }

  async function commit(id: string) {
    if (editing !== id) return;
    editing = null;
    const next = draft.trim();
    const current = app.notebooks.find((n) => n.id === id)?.name ?? '';
    // A blank or unchanged draft reverts silently rather than letting the server refuse it.
    if (!next || next === current) return;
    try {
      await renameNotebook(id, next);
    } catch (e) {
      app.error = e instanceof Error ? e.message : String(e);
    }
  }

  async function commitChat(id: string) {
    if (editing !== `chat:${id}`) return;
    editing = null;
    const next = draft.trim();
    const current = app.chats.find((c) => c.id === id)?.title ?? '';
    if (!next || next === current) return;
    try {
      await renameChat(id, next);
    } catch (e) {
      app.error = e instanceof Error ? e.message : String(e);
    }
  }

  /** Selects the contents on open, so typing over the name is the whole gesture. */
  function editingInput(node: HTMLInputElement) {
    node.focus();
    node.select();
  }

  async function confirmDelete() {
    if (!doomed) return;
    error = '';
    try {
      await deleteNotebook(doomed);
      doomed = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function confirmDeleteChat() {
    if (!doomingChat) return;
    error = '';
    try {
      await deleteChat(doomingChat);
      doomingChat = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  /** Set while a drag has over-dragged past the floor: the column fades to say it is about to go. */
  let folding = $state(false);

  const handle = (node: HTMLElement) =>
    dragHandle(node, {
      spec: NOTEBOOKS,
      get: () => app.sidebarW,
      set: (n) => setWidth(NOTEBOOKS, n),
      open: () => app.sidebarOpen,
      setOpen: (v) => setOpen(NOTEBOOKS, v),
      pending: (armed) => (folding = armed)
    });
</script>

<aside
  class="sidebar"
  class:closed={!open}
  class:pending={folding}
  style="width: {width}px"
  aria-label="Notebooks"
>
  <div class="section-head">
    {#if open}<span class="title">Notebooks</span>{/if}
    <Button
      variant="ghost"
      shape="icon"
      title="New notebook"
      ariaLabel="New notebook"
      onclick={() => (app.newNotebook = true)}
    >
      <Icon name="plus" />
    </Button>
  </div>

  {#if open}
    <ul class="list">
      {#each app.notebooks as nb (nb.id)}
        <li class="row" class:active={nb.id === app.notebookId}>
          {#if editing === nb.id}
            <!-- svelte-ignore a11y_label_has_associated_control -->
            <input
              class="rename"
              type="text"
              aria-label="Rename {nb.name}"
              use:editingInput
              bind:value={draft}
              onblur={() => commit(nb.id)}
              onkeydown={(e) => {
                if (e.key === 'Enter') commit(nb.id);
                else if (e.key === 'Escape') editing = null;
                else return;
                e.stopPropagation();
              }}
            />
          {:else}
            <button class="item" onclick={() => openNotebook(nb.id)}>
              <Icon name="book" />
              <span class="name">{nb.name}</span>
              {#if nb.source_count}<span class="count">{nb.source_count}</span>{/if}
            </button>
          {/if}

          <span class="row-actions">
            <Button
              variant="ghost"
              shape="icon"
              klass="rename-open"
              size="sm"
              title="Rename {nb.name}"
              ariaLabel="Rename {nb.name}"
              onclick={() => startRename(nb.id, nb.name)}
            >
              <Icon name="pencil" size={14} />
            </Button>
            <Button
              variant="ghost"
              shape="icon"
              klass="delete-open"
              size="sm"
              title="Delete {nb.name}"
              ariaLabel="Delete {nb.name}"
              onclick={() => {
                doomed = nb.id;
                error = '';
              }}
            >
              <Icon name="trash" size={14} />
            </Button>
          </span>

          {#if nb.id === app.notebookId}
            <ul class="chats">
              {#if app.chats.length}
                <li>
                  <button class="chat-new" onclick={newConversation}>New conversation</button>
                </li>
              {/if}
              {#each app.chats as c (c.id)}
                <li class="chat-row" class:active={c.id === app.chatId}>
                  {#if editing === `chat:${c.id}`}
                    <!-- svelte-ignore a11y_label_has_associated_control -->
                    <input
                      class="chat-rename"
                      type="text"
                      aria-label="Rename {c.title}"
                      use:editingInput
                      bind:value={draft}
                      onblur={() => commitChat(c.id)}
                      onkeydown={(e) => {
                        if (e.key === 'Enter') commitChat(c.id);
                        else if (e.key === 'Escape') editing = null;
                        else return;
                        e.stopPropagation();
                      }}
                    />
                  {:else}
                    <button class="chat-open" onclick={() => openChat(c.id)}>{c.title}</button>
                  {/if}
                  <span class="row-actions">
                    <Button
                      variant="ghost"
                      shape="icon"
                      klass="chat-rename-open"
                      size="sm"
                      title="Rename {c.title}"
                      ariaLabel="Rename {c.title}"
                      onclick={() => startRename(`chat:${c.id}`, c.title)}
                    >
                      <Icon name="pencil" size={13} />
                    </Button>
                    <Button
                      variant="ghost"
                      shape="icon"
                      klass="chat-delete-open"
                      size="sm"
                      title="Delete {c.title}"
                      ariaLabel="Delete {c.title}"
                      onclick={() => {
                        doomingChat = c.id;
                        error = '';
                      }}
                    >
                      <Icon name="trash" size={13} />
                    </Button>
                  </span>
                </li>
              {/each}
            </ul>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <ul class="rail">
      {#each app.notebooks as nb (nb.id)}
        <li>
          <button
            class="rail-item"
            class:active={nb.id === app.notebookId}
            title={nb.name}
            aria-label={nb.name}
            onclick={() => openNotebook(nb.id)}
          >
            <Icon name="book" />
          </button>
        </li>
      {/each}
    </ul>
  {/if}

  <!-- The edge is the whole control: drag it to resize, over-drag past the floor and the column
       folds away, pull it out of the rail and it returns at the width it had. A button of its own
       would have to sit either in a 44px head — where it overflowed the border onto the panel
       beside it — or on the border, floating over what it covers.
       Arrows mean wider and narrower, never "move the border right"; Enter folds and opens. -->
  <button
    class="resizer"
    style="right: -4px"
    tabindex="0"
    aria-expanded={open}
    title={open ? 'Notebook list width — over-drag left to hide' : 'Notebooks — drag right to show'}
    aria-label={open ? 'Notebook list width' : 'Show notebooks'}
    use:handle
  ></button>
</aside>

<Modal
  klass="delete-notebook"
  open={doomed !== null}
  onclosed={() => (doomed = null)}
  title="Delete “{target?.name ?? ''}”?"
  confirm="Delete notebook"
  variant="danger"
  {error}
  onconfirm={confirmDelete}
>
  <p class="lead">
    <!-- The empty branch has to be empty of both. A notebook can hold no files and still hold
         conversations, and deleting the notebook deletes them with it — so the promise that
         nothing else is touched is only allowed once both counts are checked. -->
    {#if target?.source_count || target?.chat_count}
      The notebook and everything asked in it are deleted.
    {:else}
      It is empty, so nothing else is touched.
    {/if}
  </p>
</Modal>

<Modal
  klass="delete-chat"
  open={doomingChat !== null}
  onclosed={() => (doomingChat = null)}
  title="Delete “{chatTarget?.title ?? ''}”?"
  confirm="Delete chat"
  variant="danger"
  {error}
  onconfirm={confirmDeleteChat}
  keeps="The sources it cites are untouched, and every one of them is askable again in a new conversation."
>
  <p class="lead">Every question asked in it, and every answer, is deleted.</p>
</Modal>

<style>
  .sidebar {
    position: relative;
    flex: none;
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: var(--chrome);
    border-right: 1px solid var(--border);
  }

  .section-head {
    display: flex;
    align-items: center;
    gap: 2px;
    height: 34px;
    flex: none;
    padding: 0 4px 0 10px;
  }

  .title {
    flex: 1;
    font-size: var(--fs-xs);
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }

  /* Closed, the head holds the one thing the rail can still use — a new notebook — so it is
     simply centred in what the rail gives it. */
  .closed .section-head {
    justify-content: center;
    padding: 0 4px;
  }

  /* Held at the floor of a drag: the handle keeps travelling and the column fades, so the fold
     reads as the end of the resize rather than as something that fell off. */
  .sidebar > :not(.resizer) {
    transition: opacity 120ms ease;
  }

  .sidebar.pending > :not(.resizer) {
    opacity: 0.4;
  }

  .list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    margin: 0;
    padding: 0 6px 12px;
    list-style: none;
  }

  .row {
    position: relative;
    margin: 1px 0;
  }

  .item {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    padding: 5px 8px;
    border: 1px solid transparent;
    border-radius: var(--r);
    background: none;
    text-align: left;
    color: var(--text);
  }

  .item:hover {
    background: var(--hover);
  }

  .item .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* A selected row comes forward rather than getting louder — the column behind it is already
     tinted — and the count flips to the accent tint with it. */
  .row.active > .item {
    background: var(--raised);
    border-color: var(--border);
    box-shadow: var(--shadow);
    font-weight: 600;
  }

  .row.active > .item::before {
    content: '';
    position: absolute;
    left: 0;
    top: 6px;
    bottom: 6px;
    width: 2px;
    border-radius: 2px;
    background: var(--accent);
  }

  .row.active :global(.count) {
    background: var(--accent-bg);
    color: var(--accent-text);
  }

  .rename,
  .chat-rename {
    width: calc(100% - 4px);
    margin: 1px 2px;
    height: 26px;
    padding: 0 6px;
    border: 1px solid var(--accent);
    border-radius: var(--r);
    background: var(--bg);
    font: inherit;
  }

  .row-actions {
    position: absolute;
    top: 3px;
    right: 4px;
    display: flex;
    gap: 1px;
    opacity: 0;
    pointer-events: none;
  }

  /* Hover-revealed, but still in the DOM and tab-reachable; destructive keeps its colour at
     rest so a trash can never read as a pencil. */
  .row:hover .row-actions,
  .row:focus-within .row-actions,
  .row-actions:focus-within {
    opacity: 1;
    pointer-events: auto;
  }

  .row-actions :global(.delete-open),
  .row-actions :global(.chat-delete-open) {
    color: var(--danger);
  }

  @media (hover: none) {
    .row-actions {
      opacity: 1;
      pointer-events: auto;
    }
  }

  .chats {
    margin: 2px 0 4px 20px;
    padding: 0 0 0 8px;
    list-style: none;
    border-left: 1px solid var(--border);
  }

  .chat-row {
    position: relative;
  }

  .chat-new,
  .chat-open {
    display: block;
    width: 100%;
    padding: 4px 8px;
    border: 1px solid transparent;
    border-radius: var(--r);
    background: none;
    text-align: left;
    color: var(--text);
    font-size: var(--fs-sm);
  }

  .chat-new {
    color: var(--muted);
  }

  .chat-new:hover,
  .chat-open:hover {
    background: var(--hover);
  }

  /* Two lines, so a list of them stays tellable apart where one clipped line does not. */
  .chat-open {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    white-space: normal;
  }

  .chat-row.active > .chat-open {
    background: var(--raised);
    border-color: var(--border);
    box-shadow: var(--shadow);
    font-weight: 600;
  }

  .chat-row .row-actions {
    top: 2px;
  }

  .rail {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    margin: 0;
    padding: 0 5px;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .rail-item {
    position: relative;
    display: grid;
    place-items: center;
    width: 32px;
    height: 30px;
    border: 1px solid transparent;
    border-radius: var(--r);
    background: none;
    color: var(--text);
  }

  .rail-item:hover {
    background: var(--hover);
  }

  .rail-item.active {
    background: var(--raised);
    border-color: var(--border);
    box-shadow: var(--shadow);
  }

  .rail-item.active::before {
    content: '';
    position: absolute;
    left: -5px;
    top: 5px;
    bottom: 5px;
    width: 2px;
    border-radius: 2px;
    background: var(--accent);
  }
</style>
