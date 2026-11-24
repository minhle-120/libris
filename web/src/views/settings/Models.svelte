<script lang="ts">
  import { onMount } from 'svelte';
  import * as api from '../../lib/api';
  import { errorMessage, formatBytes } from '../../lib/format';
  import type { ModelEntry, ModelsView } from '../../lib/types';
  import Badge from '../../lib/ui/Badge.svelte';
  import Button from '../../lib/ui/Button.svelte';
  import Modal from '../../lib/ui/Modal.svelte';

  /**
   * The weights a managed engine loads. The section above it fetches a program; this one
   * fetches the files that program reads — and they are two sections because a program can be
   * replaced by a newer one, while a model file is the thing a corpus was embedded *with*.
   *
   * Nothing here is a setting: a setting is something libris can apply, and downloading a file
   * is not one. The catalogue is pinned upstream to a commit and a digest, and this page offers
   * no choice of quantisation, because that choice was made by someone who could measure what
   * it does to an answer.
   */
  let { saved, onSet }: { saved: number; onSet: (installedCount: number) => void } = $props();

  let view = $state<ModelsView>();
  let error = $state('');
  let busy = $state(false);
  let doomed = $state<ModelEntry | null>(null);
  let modalError = $state('');

  const entries = $derived(view?.entries ?? []);
  const installed = $derived(entries.filter((e) => e.complete));
  const progress = $derived(
    entries.find((e) => e.downloading) ? view?.progress : undefined
  );
  const pct = $derived(
    progress?.total
      ? Math.min(100, Math.round((progress.done / progress.total) * 100))
      : 0
  );
  const summary = $derived(
    `${installed.length} of ${entries.length} installed · ${formatBytes(
      installed.reduce((n, e) => n + e.bytes, 0)
    )} of ${formatBytes(view?.bytesTotal ?? 0)} on disk` +
      (view?.partialBytes ? ` · ${formatBytes(view.partialBytes)} held by a stopped download` : '')
  );

  /** A poll answered after the card has been taken away — external mode takes it away, and a
   *  download poll is in flight whenever the mode is switched — has no card left to write on. */
  let alive = true;

  async function load() {
    try {
      const got = await api.getModels();
      if (!alive) return;
      view = got;
      onSet(installed.length);
      error = '';
    } catch (e) {
      if (alive) error = errorMessage(e);
    }
  }

  onMount(() => {
    void load();
    return () => {
      alive = false;
      if (timer) clearInterval(timer);
    };
  });

  let timer: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    const running = !!progress || view?.progress?.state === 'verifying';
    void saved;
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
    if (running) timer = setInterval(() => void load(), 1000);
  });

  async function act(fn: () => Promise<unknown>) {
    busy = true;
    error = '';
    try {
      await fn();
      await load();
    } catch (e) {
      if (alive) error = errorMessage(e);
    } finally {
      if (alive) busy = false;
    }
  }

  const missing = (e: ModelEntry) => Math.max(0, e.bytes - e.partialBytes);
  const failed = $derived(
    view?.progress?.state === 'failed' ? view.progress : undefined
  );
</script>

<section class="models">
  {#if error}
    <p class="line error" role="alert">{error}</p>
  {/if}

  <p class="summary">{summary}</p>

  {#if view?.needsRestart}
    <p class="line note">
      A model arrived while the server was running, and it reads its models at start — restart
      the server above to use it.
    </p>
  {/if}

  {#if failed?.error}
    <p class="line error" role="alert">
      {failed.error}
      <Button variant="ghost" size="sm" onclick={() => act(() => api.discardModel(failed.id))}
        >Throw the partial download away</Button
      >
    </p>
  {/if}

  {#if progress}
    <div class="running">
      <div
        class="bar"
        role="progressbar"
        aria-valuemin="0"
        aria-valuemax="100"
        aria-valuenow={pct}
        aria-label={progress.state}
      >
        <span style="width: {pct}%"></span>
      </div>
      <p class="line">
        {progress.state === 'verifying' ? 'Checking the digest' : `Downloading ${progress.part ?? ''}`}
        {#if progress.total}
          · {formatBytes(progress.done)} of {formatBytes(progress.total)} ({pct}%)
        {/if}
      </p>
    </div>
  {/if}

  <ul>
    {#each entries as e (e.id)}
      <li class="entry">
        <div class="head">
          <span class="role">{e.role}</span>
          <span class="name" title={e.label}>{e.label}</span>
          <span class="quant">{e.quant}</span>
          {#if e.complete}
            <Badge
              tone={e.verified ? 'success' : 'attention'}
              title={e.verified
                ? undefined
                : 'The files are the right size, but libris did not download them, so nothing has checked their digest.'}
            >
              {e.verified ? 'installed' : 'unverified'}
            </Badge>
          {:else if e.downloading}
            <Badge tone="accent">in flight</Badge>
          {:else if e.partialBytes}
            <Badge tone="neutral">part-fetched</Badge>
          {:else}
            <Badge tone="neutral">not here</Badge>
          {/if}
        </div>

        <p class="note-line">{e.note}</p>

        {#if e.complete}
          <ul class="files">
            {#each e.parts as p (p.name)}
              <li>
                <span class="fname" title="{e.dir}/{p.name}">{p.name}</span>
                <span>{formatBytes(p.bytes)}</span>
              </li>
            {/each}
          </ul>
        {:else if e.downloading}
          <p class="line">
            {progress?.part ?? 'a part'} · {formatBytes(progress?.done ?? 0)} of{' '}
            {formatBytes(progress?.total ?? e.bytes)}
          </p>
        {:else}
          <p class="line">
            {formatBytes(e.bytes)} in {e.parts.length} {e.parts.length === 1 ? 'file' : 'files'}
            {#if e.partialBytes}
              · {formatBytes(e.partialBytes)} already fetched
            {/if}
          </p>
        {/if}

        <div class="actions">
          {#if e.downloading}
            <Button
              size="sm"
              title="Stop this download. The bytes so far are kept, and the next attempt resumes where they stopped."
              disabled={busy}
              onclick={() => act(api.stopModelDownload)}
            >
              Stop
            </Button>
          {:else if e.complete}
            <Button
              variant="danger"
              size="sm"
              title="Delete these files from this machine"
              disabled={busy}
              onclick={() => {
                doomed = e;
                modalError = '';
              }}
            >
              Remove
            </Button>
          {:else}
            <Button
              variant="primary"
              size="sm"
              title={`From ${e.repo} at ${e.revision}`}
              disabled={busy}
              onclick={() => act(() => api.installModel(e.id))}
            >
              Download {formatBytes(missing(e))}
            </Button>
            {#if e.partialBytes}
              <Button variant="ghost" size="sm" onclick={() => act(() => api.discardModel(e.id))}>
                Discard {formatBytes(e.partialBytes)}
              </Button>
            {/if}
          {/if}
        </div>
      </li>
    {/each}
  </ul>

  <p class="footnote">
    Pinned to one commit of each repository, and checked against its digest on
    arrival, because a model that changed silently would invalidate every vector already stored.
    They live in {view?.dir ?? 'the model directory'}, beside the engine's own files.
  </p>
</section>

<Modal
  klass="remove-model"
  open={doomed !== null}
  onclosed={() => (doomed = null)}
  title="Remove “{doomed?.label ?? ''}”?"
  confirm="Remove model"
  variant="danger"
  error={modalError}
  onconfirm={async () => {
    if (!doomed) return;
    try {
      await api.removeModel(doomed.id);
      if (!alive) return;
      doomed = null;
      modalError = '';
      await load();
    } catch (e) {
      if (alive) modalError = errorMessage(e);
    }
  }}
  keeps="Your notebooks, sources and answers stay. Re-downloading takes as long as the download did — and a corpus embedded with this model would have to be re-embedded."
>
  <p class="lead">
    {doomed ? `${formatBytes(doomed.bytes)} of weights are deleted from this machine.` : ''} This
    is the one action here that pressing the button again does not undo.
  </p>
</Modal>

<style>
  .models {
    border-top: 1px solid var(--border);
    padding: 10px 16px 14px;
  }

  .summary {
    font-size: var(--fs-sm);
    color: var(--muted);
  }

  .note {
    color: var(--attention);
  }

  .line {
    font-size: var(--fs-xs);
    color: var(--muted);
    padding-top: 3px;
  }

  .line.error,
  .error {
    color: var(--danger);
  }

  .running {
    padding: 6px 0 2px;
  }

  .bar {
    height: 4px;
    border-radius: 2px;
    background: var(--chrome);
    overflow: hidden;
  }

  .bar > span {
    display: block;
    height: 100%;
    background: var(--accent);
  }

  ul {
    margin: 8px 0 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 8px;
  }

  .entry {
    padding: 9px 11px;
    border: 1px solid var(--border-soft);
    border-radius: var(--r);
    background: var(--raised);
  }

  .head {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  /* The role is a machine word: mono, and spelled once — the shared `.role` rule is in app.css,
     because the wizard's model rows say the same thing about the same file. */

  .name {
    font-size: var(--fs-sm);
    font-weight: 600;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .quant {
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .note-line {
    font-size: var(--fs-xs);
    color: var(--muted);
    padding-top: 2px;
    line-height: 1.5;
  }

  .files {
    margin: 6px 0 0;
    padding: 0;
    display: grid;
    gap: 1px;
  }

  .files li {
    display: flex;
    justify-content: space-between;
    gap: 10px;
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .fname {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
    padding-top: 7px;
  }

  .footnote {
    padding-top: 10px;
    font-size: var(--fs-xs);
    color: var(--muted);
    line-height: 1.55;
  }
</style>
