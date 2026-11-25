<script lang="ts">
  import {
    app,
    ask,
    canAsk,
    draftKey,
    openCitation,
    stopTurn
  } from '../lib/store.svelte';
  import { chipLabel } from '../lib/format';
  import type { MessageStatus } from '../lib/types';
  import Rich from '../lib/Rich.svelte';
  import Reasoning from '../lib/Reasoning.svelte';
  import Badge from '../lib/ui/Badge.svelte';
  import Banner from '../lib/ui/Banner.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Empty from '../lib/ui/Empty.svelte';

  /**
   * The conversation: a thread and the box to ask the next question in, at one width held by
   * the column and not by the things in it. An answer that ended six inches short of the box
   * beneath it was the bug this file exists to avoid.
   */
  const readyCount = $derived(app.sources.filter((s) => s.status === 'ready').length);
  const nothingToAnswerFrom = $derived(readyCount === 0);
  const draft = $derived(app.drafts[draftKey()] ?? '');

  const live = $derived(app.live);
  const running = $derived(!!live);
  const sendTitle = $derived(running ? 'Stop this answer' : 'Ask');

  let thread = $state<HTMLElement>();
  let box = $state<HTMLTextAreaElement | undefined>();
  let stick = true;

  /** Follow the answer only while the reader is at the bottom of it. */
  function onscroll() {
    const el = thread;
    if (!el) return;
    stick = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
  }

  $effect(() => {
    const el = thread;
    const rows = app.messages.length;
    const streaming = live?.answer.length ?? 0;
    const turn = !!live;
    void rows;
    void streaming;
    void turn;
    if (el && stick) el.scrollTop = el.scrollHeight;
  });

  /** The field grows as the question gets longer and then stops, so a long question cannot
   *  push the composer off the bottom of the column. */
  function grow(node: HTMLTextAreaElement) {
    box = node;
    const fit = () => {
      node.style.height = 'auto';
      node.style.height = `${Math.min(node.scrollHeight, 210)}px`;
    };
    fit();
    node.addEventListener('input', fit);
    return {
      destroy() {
        box = undefined;
        node.removeEventListener('input', fit);
      }
    };
  }

  function setDraft(value: string) {
    app.drafts[draftKey()] = value;
  }

  function send() {
    if (running) {
      stopTurn();
      return;
    }
    if (!canAsk(draft)) return;
    void ask(draft);
    // A growing field keeps its own DOM height; the value is the store's, so clear both.
    if (box) {
      box.value = '';
      box.style.height = 'auto';
    }
  }

  const VERDICT: Record<
    Exclude<MessageStatus, 'answered'>,
    { word: string; tone: 'neutral' | 'attention' | 'danger'; says: string }
  > = {
    'no-info': {
      word: 'No evidence',
      tone: 'neutral',
      says: 'Nothing in your sources answered this, and nothing was invented in its place.'
    },
    ungrounded: {
      word: 'Uncited',
      tone: 'attention',
      says: 'The model answered in prose instead of searching, so this has no citations and was not checked.'
    },
    canceled: {
      word: 'Stopped',
      tone: 'attention',
      says: 'Cut short. What was written is kept.'
    },
    interrupted: {
      word: 'Interrupted',
      tone: 'danger',
      says: 'libris stopped while the answer was being written.'
    }
  };

  /** The verdict is decided once per row, so the template never asks a question twice. */
  const rows = $derived(
    app.messages.map((m) => ({
      m,
      v: m.status && m.status !== 'answered' ? VERDICT[m.status] : null
    }))
  );

  const empty = $derived(app.messages.length === 0 && !live);
</script>

<section class="chat">
  {#if app.error}
    <div class="wrap">
      <Banner message={app.error} ondismiss={() => (app.error = '')} />
    </div>
  {/if}

  <div class="messages" bind:this={thread} onscroll={onscroll}>
    <div class="column">
      {#if app.messagesLoading && !app.messages.length}
        <p class="center">Loading…</p>
      {:else if empty}
        <div class="empty">
          <Icon name="chat" size={26} />
          <h2>
            {nothingToAnswerFrom ? 'Nothing to answer from yet' : 'Ask about these sources'}
          </h2>
          {#if nothingToAnswerFrom}
            <p>
              Answers come only from sources that finished ingesting. Add a file, or run the
              failed ones again.
            </p>
          {:else}
            <p>
              A question searches {readyCount} ready {readyCount === 1 ? 'source' : 'sources'} and
              cites the page it came from. A citation opens the original there.
            </p>
          {/if}
        </div>
      {:else}
        {#each rows as row (row.m.id)}
          {#if row.m.role === 'user'}
            <!-- What the person typed, never rendered as markdown: formatting their `#headings`
                 would reinterpret what they said. -->
            <div class="turn user">{row.m.content}</div>
          {:else}
            {@const m = row.m}
            <div class="turn assistant">
              {#if m.citations?.length}
                <div class="chips">
                  {#each m.citations as c (c.segment_id + c.chunk)}
                    <button
                      class="cite"
                      type="button"
                      title="{c.source_name} — {chipLabel(c)}"
                      onclick={() => openCitation(c)}
                    >
                      <Icon name={c.kind === 'image' ? 'image' : 'file'} size={13} />
                      {chipLabel(c)}
                    </button>
                  {/each}
                </div>
              {/if}
              {#if m.reasoning}
                <Reasoning text={m.reasoning} ms={m.reasoning_ms ?? 0} />
              {/if}
              <div class="answer">
                <Rich
                  text={m.content}
                  citations={m.citations ?? []}
                  oncite={openCitation}
                />
              </div>
              {#if row.v}
                <p class="note">
                  <Badge tone={row.v.tone}>{row.v.word}</Badge>
                  <span>{row.v.says}</span>
                </p>
              {/if}
            </div>
          {/if}
        {/each}

        {#if live}
          <div class="turn user">{live.question}</div>
          <div class="turn assistant live">
            {#if live.citations.length}
              <div class="chips">
                {#each live.citations as c (c.segment_id + c.chunk)}
                  <!-- Disabled: not yet the frozen set the answer is being written against. -->
                  <button class="cite" type="button" disabled title="{c.source_name} — {chipLabel(c)}">
                    <Icon name={c.kind === 'image' ? 'image' : 'file'} size={13} />
                    {chipLabel(c)}
                  </button>
                {/each}
              </div>
            {/if}
            {#if live.reasoning}
              <!-- Open while the model is still thinking, and closed by the stream itself when
                   the first sentence arrives: watching the work and reading the result are two
                   different things to do with the same screen. -->
              <Reasoning bind:open={live.thinkingOpen} text={live.reasoning} live={!live.answer} />
            {/if}
            <div class="answer" class:pulse={!live.answer}>
              {#if live.answer}
                <Rich
                  text={live.answer}
                  citations={live.citations}
                  oncite={openCitation}
                  streaming={live.streaming}
                />
              {:else}
                <p class="status">
                  <span class="pulse">{live.status || 'Searching your sources'}</span>
                </p>
              {/if}
            </div>
          </div>
        {/if}
      {/if}
    </div>
  </div>

  <div class="wrap">
    <div class="composer">
      <div class="entry">
        <textarea
          class="input field-quiet"
          rows="1"
          placeholder={nothingToAnswerFrom ? 'Add a source first' : 'Ask a question'}
          aria-label="Ask a question"
          use:grow
          value={draft}
          oninput={(e) => setDraft(e.currentTarget.value)}
          onkeydown={(e) => {
            // Enter sends, Shift+Enter is a newline — and neither fires mid-composition of an
            // accented character.
            if (e.key !== 'Enter' || e.isComposing) return;
            e.preventDefault();
            if (e.shiftKey) setDraft(`${draft}\n`);
            else send();
          }}
        ></textarea>
        <Button
          variant={running ? 'danger' : 'primary'}
          shape="round"
          title={sendTitle}
          ariaLabel={sendTitle}
          disabled={running ? false : !canAsk(draft)}
          onclick={send}
        >
          <Icon name={running ? 'stop' : 'send'} />
        </Button>
      </div>
      {#if nothingToAnswerFrom}
        <p class="warn">There is nothing to answer from.</p>
      {/if}
    </div>
    {#if !nothingToAnswerFrom}
      <p class="promise">
        Every answer comes from your own sources and says where it came from.
      </p>
    {/if}
  </div>
</section>

<style>
  .chat {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }

  /* One reading measure, held by the column and used by every block in it. */
  .wrap,
  .column {
    width: min(100%, var(--measure));
    margin-inline: auto;
  }

  .wrap {
    flex: none;
    padding: 8px 12px 0;
  }

  .messages {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overflow-x: hidden;
    padding: 8px 12px 0;
  }

  .column {
    padding-bottom: 16px;
    display: grid;
    gap: 14px;
    align-content: start;
  }

  .turn {
    min-width: 0;
  }

  .turn.user {
    justify-self: end;
    max-width: min(100%, 34rem);
    padding: 8px 12px;
    border: 1px solid var(--border-soft);
    border-radius: 10px 10px 3px 10px;
    background: var(--chrome);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .turn.assistant {
    display: grid;
    gap: 6px;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
  }

  .cite {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 21px;
    padding: 0 8px;
    border: 0;
    border-radius: 11px;
    background: var(--accent-bg);
    color: var(--accent-text);
    font-size: var(--fs-xs);
    font-weight: 500;
  }

  .cite:hover:not(:disabled) {
    background: var(--accent);
    /* The reading surface, not white: on a dark theme a filled accent wants the page's own
       colour back, and `#fff` is one of the two hexes this app is meant to have none of. */
    color: var(--bg);
  }

  .cite:disabled {
    opacity: 0.55;
  }

  .answer {
    min-width: 0;
  }

  .note {
    display: flex;
    align-items: baseline;
    gap: 8px;
    font-size: var(--fs-sm);
    color: var(--muted);
  }

  .status {
    display: flex;
    align-items: baseline;
    gap: 8px;
    font-size: var(--fs-sm);
    color: var(--muted);
  }

  .empty {
    height: auto;
    padding: 48px 12px;
  }

  .empty :global(.glyph) {
    color: var(--border);
  }

  .composer {
    display: grid;
    gap: 4px;
    /* The card is `.entry`, not this box: the composer and the thread must measure the same,
       and a border here would put two pixels between the two widths. */
    padding: 0;
  }

  .entry {
    display: flex;
    align-items: flex-end;
    gap: 8px;
    padding: 6px 6px 6px 10px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--raised);
    box-shadow: var(--shadow);
  }

  .entry textarea {
    flex: 1;
    min-width: 0;
    border: 0;
    background: none;
    max-height: 210px;
    overflow-y: auto;
  }

  .entry textarea:hover {
    border-color: transparent;
  }

  .warn {
    font-size: var(--fs-xs);
    color: var(--attention);
    padding-left: 2px;
  }

  .promise {
    font-size: var(--fs-xs);
    color: var(--muted);
    text-align: center;
    padding: 6px 0 8px;
  }
</style>
