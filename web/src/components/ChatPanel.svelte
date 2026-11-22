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
    oncite = undefined,
    onchat = undefined
  }: {
    notebook: Notebook;
    sources: Source[];
    /** Opens the source at the cited page, character range or timestamp. */
    oncite?: (citation: Citation) => void;
    /** The open chat's title, for the breadcrumb. Empty when there is no chat. */
    onchat?: (title: string) => void;
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

  // The breadcrumb names the chat you are reading, so the title has to leave this
  // component — and it is the server's title, not a local guess, because a chat is
  // only named once its first question is stored.
  $effect(() => onchat?.(chat?.title ?? ''));

  const searchable = $derived(sources.filter((s) => s.status === 'ready').length);
  const canSend = $derived(draft.trim().length > 0 && searchable > 0 && !running);

  // The field grows with the question and stops at 12rem, so a long one scrolls inside
  // the composer instead of pushing it off the bottom of the column.
  $effect(() => {
    void draft;
    const el = input;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 192)}px`;
  });

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
    <!-- One card: the field, its caption and the round control that sends or stops. The
         control keeps its place between the two states, so the thing under the thumb
         while an answer is arriving is the thing that stopped it. -->
    <div class="entry">
      {#if searchable === 0 && !running}
        <p class="hint"><Icon name="alert" size={13} /> No ready sources — nothing to answer from.</p>
      {/if}
      <div class="row">
        <textarea
          bind:this={input}
          bind:value={draft}
          onkeydown={onKeydown}
          rows="1"
          placeholder={searchable ? 'Ask a question about your sources' : 'Add a source first'}
          aria-label="Question"
        ></textarea>
        {#if running}
          <Button class="btn-round" variant="default" iconOnly title="Stop this question" onclick={stop}>
            <Icon name="stop" size={15} />
          </Button>
        {:else}
          <Button class="btn-round" variant="primary" iconOnly title="Ask" disabled={!canSend} onclick={send}>
            <Icon name="send" size={17} />
          </Button>
        {/if}
      </div>
    </div>
    {#if searchable > 0}
      <p class="credit">Every answer comes from your sources, with the page or timestamp it came from.</p>
    {/if}
  </footer>
</section>

<style>
  .chat {
    --measure: min(100%, 42rem);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 0 20px;
    /* The panel is a scroller of its own: the page owns the header and the composer,
       the thread owns the middle. Letting the page scroll instead would move the
       composer out from under the keyboard mid-question. */
    min-height: 0;
    height: 100%;
  }

  /* One measure, held by the column and not by each thing in it. The thread wrapped at
     76ch while the composer spanned the whole pane, so an answer ended six inches short
     of the box under it — and with the panel shut the composer was a metre of empty
     field. Centred, not left-anchored: a narrow column against one edge of a wide pane
     reads as a layout that failed to fill, and the same column in the middle reads as a
     line of text, which is what it is. */
  .bar,
  .banner,
  .messages,
  .composer,
  .empty {
    width: 100%;
    max-width: var(--measure);
  }

  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 44px;
    flex-shrink: 0;
  }

  .thread {
    min-width: 0;
    flex: 1;
  }

  /* The thread's name is context, not a heading: it used to shout as loudly as the
     question underneath it. */
  .title {
    font-size: 13px;
    font-weight: 500;
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
  }

  .picker {
    max-width: 100%;
    font: inherit;
    font-size: 13px;
    color: var(--fg);
    background: var(--canvas-inset);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 4px 6px;
  }

  .actions {
    display: flex;
    gap: 4px;
    flex-shrink: 0;
  }

  /* Deleting a thread is not a first-class action, so it waits for the pointer the way
     the source rows' actions do. `New` stays: it is the one thing to press here. */
  .actions :global(.btn[title='Delete this chat']) {
    opacity: 0;
  }
  .bar:hover :global(.btn[title='Delete this chat']),
  .bar:focus-within :global(.btn[title='Delete this chat']) {
    opacity: 1;
  }
  @media (hover: none) {
    .actions :global(.btn[title='Delete this chat']) {
      opacity: 1;
    }
  }

  .messages {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 20px;
    padding: 4px 2px 16px;
  }

  .turn {
    max-width: 100%;
  }

  /* --canvas-inset is #010409, near black: a code block's well. The user's own message
     is not a well, so it sits on the subtle plane with a soft corner instead. */
  .turn.user {
    align-self: flex-end;
    max-width: min(100%, 82%);
    background: var(--canvas-subtle);
    border: 1px solid var(--border-muted);
    border-radius: 14px 14px 4px 14px;
    padding: 10px 14px;
  }

  .turn.user p {
    margin: 0;
    font-size: 14px;
    white-space: pre-wrap;
  }

  .turn.assistant p {
    margin: 0;
    font-size: 15px;
    line-height: 1.65;
    white-space: pre-wrap;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 6px;
  }

  /* Tonal, not outlined: a chip is a thing to press, and a 1px box around fourteen
     characters reads as a label. `--link` never existed, so until now these rendered in
     the body colour and looked like part of the sentence. */
  .cite,
  .inline {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font: inherit;
    color: var(--accent);
    background: color-mix(in srgb, var(--accent) 13%, transparent);
    border: none;
    border-radius: 999px;
    cursor: pointer;
    transition: background 80ms ease;
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
    background: color-mix(in srgb, var(--accent) 24%, transparent);
    text-decoration: none;
  }

  .cite:disabled {
    cursor: default;
    opacity: 0.7;
  }

  .note {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 10px !important;
    font-size: 13px;
    color: var(--fg-muted);
  }

  .reasoning {
    font-size: 12px;
    color: var(--fg-muted);
    border-left: 2px solid var(--border);
    padding-left: 8px;
    margin-top: 6px !important;
  }

  .caret {
    display: inline-block;
    width: 7px;
    height: 1em;
    margin-left: 2px;
    background: var(--fg-muted);
    vertical-align: -0.15em;
    animation: blink 1s steps(1) infinite;
  }

  @keyframes blink {
    50% {
      opacity: 0;
    }
  }

  .composer {
    flex-shrink: 0;
    padding: 4px 0 14px;
  }

  /* The field is a card the whole width of the thread, and the page's bottom rule is
     gone: a line above an input makes it look like a footer of the window rather than
     the place where the next question starts. */
  .entry {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 6px 6px 6px 14px;
    background: var(--elevated);
    border: 1px solid var(--border);
    border-radius: 14px;
    box-shadow: var(--shadow-pop);
    transition: border-color 100ms ease;
  }
  .entry:focus-within {
    border-color: var(--accent);
  }

  .row {
    display: flex;
    align-items: flex-end;
    gap: 10px;
  }

  textarea {
    flex: 1;
    resize: none;
    font: inherit;
    font-size: 15px;
    line-height: 1.5;
    color: var(--fg);
    background: none;
    border: none;
    padding: 8px 0;
    max-height: 12rem;
  }
  textarea::placeholder {
    color: var(--fg-muted);
  }

  textarea:focus {
    outline: none;
    box-shadow: none;
  }

  .hint {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 8px 0 0;
    font-size: 12px;
    color: var(--attention);
  }

  .credit {
    margin: 8px 0 0;
    font-size: 11px;
    text-align: center;
    color: var(--fg-muted);
    opacity: 0.7;
  }

  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 9px 12px;
    font-size: 13px;
    color: var(--fg);
    border: 1px solid color-mix(in srgb, var(--danger) 35%, transparent);
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--danger) 10%, transparent);
  }
  .banner :global(svg) {
    flex-shrink: 0;
    color: var(--danger);
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
    color: var(--fg-muted);
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
