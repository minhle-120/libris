<script lang="ts">
  import { onMount } from 'svelte';
  import {
    discardModel,
    getModels,
    installModel,
    removeModel,
    stopModelDownload
  } from '../lib/api';
  import type { ModelEntry, ModelsView } from '../lib/types';
  import { formatBytes } from '../lib/format';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';
  import ConfirmDialog from '../lib/ConfirmDialog.svelte';

  /**
   * The models libris runs, and the download of the ones it does not have yet.
   *
   * This is the other half of managed mode. The engine card above it fetches a program; this
   * one fetches the weights that program loads, and the difference is why they are two cards:
   * a program can be replaced by a newer one, while a model file is the thing a corpus was
   * embedded *with*. So the catalogue here is pinned to a commit and a digest on the server
   * and the page never offers a choice of quantisation — the choice was made when
   * the catalogue was written, by somebody who could measure what it does to an answer.
   *
   * Nothing here is a setting. The only fields on this page that describe a model are in the
   * server libris starts, and those are not a form either.
   */
  let {
    saved = 0,
    onModels = () => {}
  }: {
    /** Bumped by the parent on every save. The models card does not read settings — but the
     *  mode does decide whether it is mounted at all, and a save that moved the port moves
     *  the "restart to load it" answer with it. */
    saved?: number;
    /** Called when the set of models on this disk changes: a download landed, an entry was
     *  discarded or removed. The engine card gates its Start button on there being a model to
     *  run, and the engine view carries the engine's download progress rather than the model
     *  downloads', so that card cannot see this by itself. */
    onModels?: () => void;
  } = $props();

  let view = $state<ModelsView | null>(null);
  let loadError = $state('');
  let actionError = $state('');
  let confirming = $state(false);
  let pending = $state<ModelEntry | null>(null);
  let removing = $state(false);

  const entries = $derived(view?.entries ?? []);
  const running = $derived(entries.find((e) => e.downloading) ?? null);
  const progress = $derived(view?.progress);
  const failed = $derived(
    !running && progress && progress.state === 'failed' ? progress : null
  );
  const complete = $derived(entries.filter((e) => e.complete).length);
  const installedBytes = $derived(
    entries
      .filter((e) => e.complete)
      .reduce((sum, e) => sum + e.bytes, 0)
  );

  onMount(() => void refresh());

  // A save can change the port, and the port is what "the server has to be restarted" is
  // about. Same rule as the engine card: watch the counter, not the draft, so the card does
  // not reload on every keystroke somewhere else on the page.
  let first = true;
  $effect(() => {
    void saved;
    if (first) {
      first = false;
      return;
    }
    void refresh();
  });

  // A model landing is a fact the engine card needs and cannot read for itself: the engine
  // view reports the llama.cpp download, never these files, and its Start button is disabled
  // while it can see no model to run. So a download that finished while the page was open
  // left that button dead until the app was restarted — which read as "the app has to be
  // reopened to notice what it just did". Reported upward rather than reached across, because
  // the parent owns the wiring between its two cards.
  let lastComplete: number | undefined;
  $effect(() => {
    if (!view) return;
    const n = complete;
    if (lastComplete !== undefined && n !== lastComplete) onModels();
    lastComplete = n;
  });

  // Polled, not streamed — the same arrangement as the engine download and a source that is
  // processing. The timer goes away the moment nothing is running, so an open settings page
  // is not quietly asking the server every second forever.
  $effect(() => {
    if (!running) return;
    const timer = setInterval(() => void refresh(), 1000);
    return () => clearInterval(timer);
  });

  async function refresh() {
    try {
      view = await getModels();
      actionError = '';
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
    }
  }

  async function act(fn: () => Promise<ModelsView>) {
    try {
      view = await fn();
      actionError = '';
    } catch (e) {
      actionError = e instanceof Error ? e.message : String(e);
    }
  }

  const start = (e: ModelEntry) => () => act(() => installModel(e.id));
  const stop = () => act(stopModelDownload);
  const discard = (e: ModelEntry) => () => act(() => discardModel(e.id));

  // Remove is the one act here that cannot be undone by pressing the button again, so it
  // asks first and the parent decides which row the box is about.
  const askRemove = (e: ModelEntry) => () => {
    pending = e;
    confirming = true;
  };

  async function confirmRemove() {
    if (!pending) return;
    const entry = pending;
    removing = true;
    try {
      view = await removeModel(entry.id);
      actionError = '';
    } catch (e) {
      actionError = e instanceof Error ? e.message : String(e);
    } finally {
      removing = false;
      pending = null;
    }
  }

  /** What the row says about a model that is not here yet: the two files and what they cost. */
  function missingWords(e: ModelEntry) {
    const files = e.parts.filter((p) => !p.present);
    const bytes = files.reduce((sum, p) => sum + p.bytes, 0);
    return `${formatBytes(bytes)} · ${files.length} file${files.length === 1 ? '' : 's'}`;
  }

  function pct(e: ModelEntry) {
    if (!progress || progress.id !== e.id || progress.total <= 0) return 0;
    return Math.min(100, Math.round((progress.done / progress.total) * 100));
  }
</script>

<div class="models">
  {#if loadError}
    <p class="error">{loadError}</p>
  {:else if view === null}
    <p class="muted">Reading the models folder…</p>
  {:else}
    <p class="summary">
      {complete} of {entries.length} installed · {formatBytes(installedBytes)} of{' '}
      {formatBytes(view.bytesTotal)} on disk
      {#if view.partialBytes > 0 && !running}
        · <span class="muted">{formatBytes(view.partialBytes)} in a stopped download</span>
      {/if}
    </p>

    {#if view.needsRestart}
      <p class="note">
        <Icon name="retry" size={14} />
        A model arrived while the server was running. It reads its models at start, so
        restart the engine above to use it.
      </p>
    {/if}

    {#if failed?.error}
      <p class="warn">
        <Icon name="alert" />
        {failed.error}
      </p>
      {#if failed.id}
        <button class="link" onclick={() => void act(() => discardModel(failed.id!))}>
          throw away the partial bytes
        </button>
      {/if}
    {/if}

    <ul>
      {#each entries as entry (entry.id)}
        <li class:complete={entry.complete}>
          <div class="head">
            <span class="role">{entry.role}</span>
            <strong>{entry.label}</strong>
            <span class="muted">{entry.quant}</span>
            {#if entry.complete && !entry.verified}
              <span class="badge warn" title="The files are the right size, but libris did not download them, so nothing has checked their digest.">
                unverified
              </span>
            {/if}
            {#if entry.complete && entry.verified}
              <span class="badge">installed</span>
            {/if}
          </div>

          <p class="note">{entry.note}</p>

          {#if running?.id === entry.id && progress}
            <div class="progress" class:failed={progress.state === 'failed'}>
              <div class="line">
                <span>
                  {progress.state === 'verifying' ? 'checking the digest' : 'downloading'}
                  {#if progress.part}
                    <span class="tag">{progress.part}</span>
                  {/if}
                </span>
                <span class="muted">
                  {formatBytes(progress.done)} of {formatBytes(progress.total)} · {pct(entry)}%
                </span>
              </div>
              <div class="bar"><span style:width="{pct(entry)}%"></span></div>
            </div>
          {:else if entry.complete}
            <ul class="files">
              {#each entry.parts as part (part.name)}
                <li>
                  <span class="name" title="{entry.dir}/{part.name}">{part.name}</span>
                  <span class="muted">{formatBytes(part.bytes)}</span>
                </li>
              {/each}
            </ul>
          {:else}
            <p class="muted">
              {missingWords(entry)}
              {#if entry.partialBytes > 0}
                · <span class="muted">{formatBytes(entry.partialBytes)} already fetched</span>
              {/if}
            </p>
          {/if}

          <div class="actions">
            {#if running?.id === entry.id}
              <Button onclick={stop} title="Keeps the bytes downloaded so far; the next attempt resumes">
                <Icon name="stop" size={14} /> Stop
              </Button>
            {:else if entry.complete}
              <Button variant="danger" onclick={askRemove(entry)} title="Deletes these files from this machine">
                <Icon name="trash" size={14} /> Remove
              </Button>
            {:else}
              <Button variant="primary" onclick={start(entry)} title="Downloads from {entry.repo}">
                <Icon name="download" size={14} /> Download {formatBytes(entry.bytes)}
              </Button>
              {#if entry.partialBytes > 0}
                <button class="link" onclick={discard(entry)}>
                  throw away the partial download
                </button>
              {/if}
            {/if}
          </div>
        </li>
      {/each}
    </ul>

    <p class="muted footnote">
      Pinned to one commit of each repository, and checked against its digest as it arrives —
      a model that changed silently would invalidate every vector already stored. Files live
      in <code>{view.dir}</code>.
    </p>

    {#if actionError}
      <p class="error">{actionError}</p>
    {/if}
  {/if}
</div>

<!-- Deleting a model is the one act here that cannot be undone by pressing the same button
     again: it is 2.4 GB of download, and the corpus may hold vectors made with it. -->
<ConfirmDialog
  bind:open={confirming}
  cls="model-remove"
  label="Remove model"
  title="Remove {pending?.label ?? 'this model'}?"
  lead="The {pending ? formatBytes(pending.bytes) : ''} of files are deleted from this machine."
  note="Your notebooks, sources and answers stay. Re-downloading takes as long as the download took."
  confirmLabel="Remove"
  busy={removing}
  onconfirm={confirmRemove}
/>

<style>
  /* Inside a settings card already, so this block is separated by a rule and inset to the
     row padding rather than put in a second frame — the same arrangement the engine card
     uses above it. The inset is what keeps the entries off the card's own border. */
  .models {
    display: grid;
    gap: 12px;
    border-top: 1px solid var(--border-muted);
    padding: 16px;
  }

  .summary {
    margin: 0;
    color: var(--text-muted);
    font-size: 13px;
  }

  ul {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 10px;
  }

  li {
    display: grid;
    gap: 6px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--elevated);
  }

  /* Installed and not-installed share a plane. Depth here is the badge, not a background:
     --canvas-subtle is chrome, and a card that still has a button in it is not chrome. */

  .head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    flex-wrap: wrap;
  }

  .role {
    font: 500 11px/1.4 ui-monospace, SFMono-Regular, monospace;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 2px 6px;
  }

  .badge {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 999px;
    background: var(--success-subtle, #dafbe1);
    color: var(--success);
  }

  .badge.warn {
    background: var(--attention-subtle, #fff8c5);
    color: var(--attention);
  }

  .note {
    margin: 0;
    font-size: 13px;
    color: var(--text-muted);
  }

  .files {
    gap: 2px;
  }

  .files li {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 0;
    border: 0;
    background: none;
    font-size: 12px;
  }

  .files .muted {
    white-space: nowrap;
  }

  .files .name {
    font-family: ui-monospace, SFMono-Regular, monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .progress {
    display: grid;
    gap: 6px;
  }

  .line {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    font-size: 12px;
  }

  /* The count is the thing being read off a moving bar; it must not be the part that wraps
     into a third line at a narrow width. */
  .line > .muted {
    white-space: nowrap;
  }

  .tag {
    font-family: ui-monospace, SFMono-Regular, monospace;
    color: var(--text-muted);
  }

  .bar {
    height: 6px;
    border-radius: 999px;
    background: var(--border);
    overflow: hidden;
  }

  .bar span {
    display: block;
    height: 100%;
    background: var(--primary);
    transition: width 0.3s ease;
  }

  .progress.failed .bar span {
    background: var(--danger);
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }

  .link {
    border: 0;
    background: none;
    padding: 0;
    color: var(--accent);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }

  .muted {
    color: var(--text-muted);
  }

  .footnote {
    font-size: 12px;
    margin: 0;
  }

  .footnote code {
    font-family: ui-monospace, SFMono-Regular, monospace;
  }

  .warn {
    margin: 0;
    color: var(--attention);
    font-size: 13px;
  }

  .error {
    margin: 0;
    color: var(--danger);
    font-size: 13px;
  }
</style>
