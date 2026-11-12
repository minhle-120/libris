<script lang="ts">
  import { ask, createChat, deleteChat, listChats, listMessages } from '../lib/api';
  import { chunkPieces, trimPartialLabel } from '../lib/chunks';
  import { citationLocator, errorMessage } from '../lib/format';
  import type { Chat, Citation, Message, Notebook, Source } from '../lib/types';
  import Badge from '../lib/Badge.svelte';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';
  import EmptyState from '../lib/EmptyState.svelte';

  let {
    notebook,
    sources,
    oncite = undefined
  }: {
    notebook: Notebook;
    sources: Source[];
    /** Opens the source at the cited page, character range or timestamp. */
    oncite?: (citation: Citation) => void;
  } = $props();

  let chats = $state<Chat[]>([]);
  let chatId = $state('');
  let messages = $state<Message[]>([]);
  let loading = $state(true);
  let error = $state('');

  // The turn in flight. `streaming` is the answer arriving; `status` is what the
  // research loop says it is doing; `reasoning` is a model thinking out loud, shown
  // with the status and never appended to the answer.
  let running = $state(false);
  let streaming = $state('');
  let status = $state('');
  let reasoning = $state('');
  let live = $state<Citation[]>([]);
  let stopped = $state(false);
  let controller: AbortController | undefined = $state();

  let draft = $state('');
  let panel: HTMLDivElement | undefined = $state();
  let input: HTMLTextAreaElement | undefined = $state();

  const chat = $derived(chats.find((c) => c.id === chatId) ?? null);
  const searchable = $derived(sources.filter((s) => s.status === 'ready').length);
  const canSend = $derived(draft.trim().length > 0 && searchable > 0 && !running);

  // Opening a notebook loads its threads. The effect re-runs only when the notebook
  // actually changes, which is also the case that used to leave the previous
  // notebook's messages on screen.
  $effect(() => {
    const id = notebook.id;
    if (!id) return;
    void loadChats(id);
  });

  // Follow the answer as it arrives, unless the reader has scrolled up to re-read
  // something — yanking a paragraph back out of reach mid-stream is the fastest way
  // to make an answer unreadable.
  $effect(() => {
    void messages.length;
    void streaming;
    void status;
    const el = panel;
    if (!el) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 160) el.scrollTop = el.scrollHeight;
  });

  async function loadChats(notebookId: string) {
    error = '';
    try {
      chats = await listChats(notebookId);
      const keep = chats.some((c) => c.id === chatId) ? chatId : (chats.at(-1)?.id ?? '');
      await selectChat(keep);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  async function selectChat(id: string) {
    chatId = id;
    messages = [];
    streaming = '';
    status = '';
    reasoning = '';
    live = [];
    if (!id) return;
    try {
      messages = await listMessages(notebook.id, id);
    } catch (e) {
      error = errorMessage(e);
    }
  }

  async function newChat() {
    error = '';
    try {
      const chat = await createChat(notebook.id, 'New chat');
      chats = [...chats, chat];
      await selectChat(chat.id);
      input?.focus();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  async function removeChat(id: string) {
    error = '';
    try {
      await deleteChat(notebook.id, id);
      chats = chats.filter((c) => c.id !== id);
      if (chatId === id) await selectChat(chats.at(-1)?.id ?? '');
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function send() {
    const question = draft.trim();
    if (!question || running || searchable === 0) return;

    draft = '';
    // A thread with no chat yet is named after the first question asked in it, which
    // is what the sidebar will show and the only label a one-question thread gets.
    start(question).catch(() => {
      /* reported through `error`; nothing else to do from a text box */
    });
  }

  /**
   * One turn, start to finish.
   *
   * The message the server stores is authoritative: the optimistic row is replaced
   * with it when the stream ends, because the stored row carries the id, the status
   * and the frozen citations, and the turn may have ended in a way the client did not
   * witness (a model that refused, a budget that ran out).
   */
  async function start(question: string) {
    error = '';
    stopped = false;
    running = true;
    streaming = '';
    reasoning = '';
    status = 'Starting research…';
    live = [];

    // The question goes on screen the instant it is sent. A turn can take half a
    // minute, and a screen that shows only a spinner for that long reads as a hang —
    // especially since the answer arrives under the question it answers. A negative id
    // keeps it distinct from a stored row, and the reload at the end replaces it.
    const sent = Math.floor(Date.now() / 1000);
    messages = [
      ...messages,
      { id: -sent, chat_id: chatId, role: 'user', content: question, created_at: sent }
    ];

    let id = chatId;
    try {
      if (!id) {
        const created = await createChat(notebook.id, question.slice(0, 120));
        chats = [...chats, created];
        id = created.id;
        chatId = id;
      }

      controller = new AbortController();
      const events = {
        onStatus: (text: string) => {
          if (text) status = text;
        },
        onCitations: (citations: Citation[]) => {
          live = citations;
        },
        onDelta: (text: string, think: string) => {
          if (text) streaming += text;
          if (think) reasoning = reasoning + think;
        },
        onDone: (message: Message) => {
          messages = [...messages, message];
          streaming = '';
          live = [];
          status = '';
        }
      };

      await ask(notebook.id, id, question, events, controller.signal);
    } catch (e) {
      if (!stopped) error = errorMessage(e);
    } finally {
      // Whatever happened, the server has the last word on what the turn was.
      controller = undefined;
      running = false;
      streaming = '';
      status = '';
      reasoning = '';
      if (id) {
        try {
          messages = await listMessages(notebook.id, id);
        } catch {
          /* keep what is on screen rather than blanking it over a failed refresh */
        }
        void loadChats(notebook.id); // the thread may have just been named
      }
    }
  }

  /** Hanging up is the stop button. There is no stop endpoint to fall out of sync with. */
  function stop() {
    stopped = true;
    status = 'Stopping…';
    controller?.abort();
  }

  function onKeydown(e: KeyboardEvent) {
    // Enter sends, Shift+Enter is a newline — the convention the composer inherits
    // from every chat product, and worth the one exception in the key handler.
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      send();
    }
  }

  /** The label on a citation chip: the chunk's number, then where it points. */
  function chipLabel(c: Citation): string {
    const where = citationLocator(c);
    return where ? `Chunk ${c.chunk} · ${where}` : `Chunk ${c.chunk}`;
  }
</script>

<section class="chat">
  <header class="bar">
    <div class="thread">
      {#if chats.length > 1}
        <select
          class="picker"
          aria-label="Chat"
          bind:value={chatId}
          onchange={(e) => selectChat((e.target as HTMLSelectElement).value)}
        >
          {#each chats as c (c.id)}
            <option value={c.id}>{c.title}</option>
          {/each}
        </select>
      {:else}
        <span class="title">{chat?.title || 'New chat'}</span>
      {/if}
    </div>
    <div class="actions">
      {#if chat}
        <Button size="sm" variant="ghost" title="Delete this chat" onclick={() => removeChat(chat.id)}>
          <Icon name="trash" size={15} />
        </Button>
      {/if}
      <Button size="sm" variant="ghost" title="Start a new chat" onclick={newChat}>
        <Icon name="plus" size={15} /> New
      </Button>
    </div>
  </header>

  {#if error}
    <div class="banner">
      <Icon name="alert" size={15} />
      <span>{error}</span>
      <button class="dismiss" aria-label="Dismiss" onclick={() => (error = '')}>
        <Icon name="x" size={14} />
      </button>
    </div>
  {/if}

  {#if loading}
    <div class="empty"><span class="pulse">Loading…</span></div>
  {:else if messages.length === 0 && !running}
    <EmptyState
      icon="chat"
      title={searchable ? 'Ask across the sources' : 'Nothing to ask yet'}
      description={searchable
        ? 'Every question runs a research loop over the ready sources, then answers with citations back to the page or timestamp. Click a citation to open the original there.'
        : 'A question is answered only from sources that finished ingesting. Add a file, or retry the ones marked failed.'}
    />
  {:else}
    <div class="messages" bind:this={panel}>
      {#each messages as m (m.id)}
        {#if m.role === 'user'}
          <div class="turn user">
            <p>{m.content}</p>
          </div>
        {:else}
          <article class="turn assistant">
            {#if m.citations?.length}
              <div class="chips">
                {#each m.citations as c (c.chunk)}
                  <button
                    class="cite"
                    type="button"
                    title="{c.source_name} — {chipLabel(c)}"
                    onclick={() => oncite?.(c)}
                  >
                    <Icon name={c.kind === 'audio' ? 'audio' : c.kind === 'image' ? 'image' : 'file'} size={13} />
                    <span>{chipLabel(c)}</span>
                  </button>
                {/each}
              </div>
            {/if}

            <p class="answer">
              {#each chunkPieces(m.content, m.citations ?? []) as piece, i (i)}
                {#if piece.kind === 'text'}{piece.text}{:else}<button
                    class="inline"
                    type="button"
                    title="{piece.citation?.source_name} — {chipLabel(piece.citation!)}"
                    onclick={() => piece.citation && oncite?.(piece.citation)}
                  >{piece.citation?.chunk}</button>{/if}
              {/each}
            </p>

            {#if m.status && m.status !== 'answered'}
              <p class="note">
                {#if m.status === 'no-info'}
                  <Badge tone="neutral">No evidence</Badge>
                  <span>Nothing in the sources answered this. Nothing was invented in its place.</span>
                {:else if m.status === 'ungrounded'}
                  <Badge tone="attention">Uncited</Badge>
                  <span>The model replied in prose instead of using the research tools, so this answer
                    has no citations and has not been checked against the sources.</span>
                {:else if m.status === 'canceled'}
                  <Badge tone="neutral">Stopped</Badge>
                  <span>Part of an answer, cut short.</span>
                {:else}
                  <Badge tone="danger">Interrupted</Badge>
                  <span>Libris stopped while this answer was being written.</span>
                {/if}
              </p>
            {/if}
          </article>
        {/if}
      {/each}

      {#if running}
        <article class="turn assistant live">
          {#if live.length}
            <div class="chips">
              {#each live as c (c.chunk)}
                <button class="cite" type="button" title="{c.source_name} — {chipLabel(c)}" disabled>
                  <Icon name="file" size={13} />
                  <span>{chipLabel(c)}</span>
                </button>
              {/each}
            </div>
          {/if}

          {#if streaming}
            <p class="answer">{trimPartialLabel(streaming)}<span class="caret"></span></p>
          {:else}
            <p class="answer pulse">{status || 'Thinking…'}</p>
          {/if}

          {#if reasoning && !streaming}
            <p class="reasoning">{reasoning}</p>
          {/if}
        </article>
      {/if}
    </div>
  {/if}

  <footer class="composer">
    {#if searchable === 0 && !running}
      <p class="hint">No ready sources — nothing to answer from.</p>
    {/if}
    <div class="entry">
      <textarea
        bind:this={input}
        bind:value={draft}
        onkeydown={onKeydown}
        rows="1"
        placeholder={searchable ? 'Ask a question about your sources' : 'Add a source first'}
        aria-label="Question"
      ></textarea>
      {#if running}
        <Button variant="default" size="md" title="Stop this question" onclick={stop}>
          <Icon name="stop" size={15} /> Stop
        </Button>
      {:else}
        <Button variant="primary" size="md" title="Ask" disabled={!canSend} onclick={send}>
          <Icon name="send" size={15} /> Ask
        </Button>
      {/if}
    </div>
  </footer>
</section>

<style>
  .chat {
    display: flex;
    flex-direction: column;
    gap: 10px;
    /* The panel is a scroller of its own: the page owns the header and the composer,
       the thread owns the middle. Letting the page scroll instead would move the
       composer out from under the keyboard mid-question. */
    min-height: 0;
    height: 100%;
  }

  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .thread {
    min-width: 0;
    flex: 1;
  }

  .title {
    font-size: 14px;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
  }

  .picker {
    max-width: 100%;
    font: inherit;
    font-size: 13px;
    color: var(--ink);
    background: var(--canvas-inset);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 4px 6px;
  }

  .actions {
    display: flex;
    gap: 4px;
  }

  .messages {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 4px 2px;
  }

  .turn {
    max-width: 76ch;
  }

  .turn.user {
    align-self: flex-end;
    background: var(--canvas-inset);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 8px 12px;
  }

  .turn.user p {
    margin: 0;
    font-size: 14px;
    white-space: pre-wrap;
  }

  .turn.assistant p {
    margin: 0;
    font-size: 14px;
    line-height: 1.55;
    white-space: pre-wrap;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 6px;
  }

  .cite,
  .inline {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font: inherit;
    color: var(--link);
    background: var(--canvas-inset);
    border: 1px solid var(--border);
    border-radius: 999px;
    cursor: pointer;
  }

  .cite {
    font-size: 12px;
    padding: 2px 8px;
  }

  /* The inline form sits inside a sentence, so it stays small and does not lift the
     line the way a padded button would. */
  .inline {
    font-size: 11px;
    line-height: 1;
    padding: 2px 6px;
    vertical-align: 0.15em;
  }

  .cite:hover:not(:disabled),
  .inline:hover {
    border-color: var(--border-emphasis);
    background: var(--canvas);
  }

  .cite:disabled {
    cursor: default;
    opacity: 0.7;
  }

  .note {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 6px !important;
    font-size: 12px;
    color: var(--ink-muted);
  }

  .reasoning {
    font-size: 12px;
    color: var(--ink-muted);
    border-left: 2px solid var(--border);
    padding-left: 8px;
    margin-top: 6px !important;
  }

  .caret {
    display: inline-block;
    width: 7px;
    height: 1em;
    margin-left: 2px;
    background: var(--ink-muted);
    vertical-align: -0.15em;
    animation: blink 1s steps(1) infinite;
  }

  @keyframes blink {
    50% {
      opacity: 0;
    }
  }

  .composer {
    border-top: 1px solid var(--border);
    padding-top: 8px;
  }

  .entry {
    display: flex;
    gap: 8px;
    align-items: flex-end;
  }

  textarea {
    flex: 1;
    resize: none;
    font: inherit;
    font-size: 14px;
    color: var(--ink);
    background: var(--canvas);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 8px 10px;
    max-height: 12rem;
  }

  textarea:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -1px;
  }

  .hint {
    margin: 0 0 6px;
    font-size: 12px;
    color: var(--ink-muted);
  }

  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    font-size: 13px;
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: var(--radius);
  }

  .dismiss {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    margin-left: auto;
    padding: 0;
    color: inherit;
    background: none;
    border: none;
    border-radius: var(--radius-sm);
    cursor: pointer;
  }

  .empty {
    flex: 1;
    display: grid;
    place-items: center;
  }

  .pulse {
    animation: pulse 1.6s ease-in-out infinite;
    color: var(--ink-muted);
    font-size: 14px;
  }

  @keyframes pulse {
    0%,
    100% {
      opacity: 0.35;
    }
    50% {
      opacity: 1;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .pulse,
    .caret {
      animation: none;
    }
  }
</style>
