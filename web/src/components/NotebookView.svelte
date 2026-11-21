<script lang="ts">
  import { onMount } from 'svelte';
  import {
    detachSource,
    getSource,
    listSources,
    originalURL,
    reingestSource,
    uploadSource
  } from '../lib/api';
  import { formatAge, formatBytes, errorMessage } from '../lib/format';
  import type {
    Citation,
    Notebook,
    Panel,
    Source,
    SourceKind,
    SourceStatus,
    Tab
  } from '../lib/types';
  import Tabs from '../lib/Tabs.svelte';
  import Badge from '../lib/Badge.svelte';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';
  import EmptyState from '../lib/EmptyState.svelte';
  import ChunkViewer from './ChunkViewer.svelte';
  import ChatPanel from './ChatPanel.svelte';

  let {
    notebook,
    tab,
    ontab,
    onchat = undefined,
    onchange = undefined
  }: {
    notebook: Notebook;
    tab: Panel;
    ontab: (tab: Panel) => void;
    /** Carries the open chat's title up to the breadcrumb. */
    onchat?: (title: string) => void;
    onchange?: () => void;
  } = $props();

  let sources = $state<Source[]>([]);
  let loading = $state(true);
  let error = $state('');
  let filter = $state('');
  let uploading = $state(0);
  let dragging = $state(false);
  let inspecting: Source | null = $state(null);
  // The chunk an answer cited, carried into the viewer so it opens on the place the
  // answer came from rather than at the top of a 400-page raster run.
  let focus: { segment_id: string; chunk?: number } | null = $state(null);

  let fileInput: HTMLInputElement | undefined = $state();

  const visible = $derived(
    sources
      .filter((s) => s.name.toLowerCase().includes(filter.trim().toLowerCase()))
      .slice()
      .sort((a, b) => b.added_at - a.added_at)
  );

  // Anything still in flight is polled. The backend has no progress stream yet,
  // so a refetch of the list is the cheapest honest signal.
  const busy = $derived(uploading > 0 || sources.some((s) => s.status === 'processing'));

  const tabs = $derived<Tab[]>([
    { id: 'sources', label: 'Sources', count: sources.length },
    { id: 'chat', label: 'Chat' },
    { id: 'activity', label: 'Activity' }
  ]);

  const kindIcon: Record<SourceKind, string> = {
    pdf: 'file',
    image: 'image',
    audio: 'audio',
    text: 'file',
    unsupported: 'inbox'
  };

  const statusTone: Record<SourceStatus, 'neutral' | 'success' | 'attention' | 'danger'> = {
    processing: 'attention',
    ready: 'success',
    unsupported: 'neutral',
    failed: 'danger'
  };

  onMount(() => {
    load();
  });

  // The effect is created only while something is in flight, and destroyed by
  // Svelte when it stops being busy or the notebook changes.
  $effect(() => {
    if (!busy) return;
    const timer = setInterval(load, 1500);
    return () => clearInterval(timer);
  });

  async function load() {
    try {
      sources = await listSources(notebook.id);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  /** Uploads are serial: the backend ingests one file at a time anyway. */
  async function upload(files: File[]) {
    if (!files.length) return;

    error = '';
    const failures: string[] = [];

    for (const file of files) {
      uploading += 1;
      try {
        // 202 with the source in `processing`: show the row immediately rather
        // than waiting for the next poll to reveal it.
        sources = upsert(sources, await uploadSource(notebook.id, file));
      } catch (e) {
        failures.push(`${file.name}: ${errorMessage(e)}`);
      } finally {
        uploading -= 1;
      }
    }

    if (failures.length) error = failures.join(' · ');
    onchange?.(); // the sidebar's counts come from the notebook list
  }

  /**
   * Open a citation's source on the chunk the answer came from.
   *
   * The source is looked up rather than taken on trust from the answer: it may have
   * been detached from this notebook since the question was asked, and segments are
   * served by source id, so an old citation still opens.
   */
  async function openCitation(c: Citation) {
    error = '';
    try {
      const source = sources.find((s) => s.id === c.source_id) ?? (await getSource(c.source_id));
      focus = { segment_id: c.segment_id, chunk: c.chunk };
      inspecting = source;
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function upsert(list: Source[], next: Source): Source[] {
    const rest = list.filter((s) => s.id !== next.id);
    return [next, ...rest];
  }

  async function remove(source: Source) {
    error = '';
    try {
      await detachSource(notebook.id, source.id);
      sources = sources.filter((s) => s.id !== source.id);
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  /**
   * Rebuilds a source from its kept original: text, page rasters, chunks and
   * vectors all derived again. One action for both reasons a row gets redone — it
   * failed, or the pipeline changed under it.
   *
   * The answer is the row back in `processing`, and the poller already running
   * takes it from there.
   */
  async function rebuild(source: Source) {
    error = '';
    try {
      sources = upsert(sources, await reingestSource(source.id));
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function onDrop(e: DragEvent) {
    dragging = false;
    const files = Array.from(e.dataTransfer?.files ?? []);
    if (files.length) upload(files);
  }

  function pick() {
    fileInput?.click();
  }
</script>

<div class="page">
  <div class="title-row">
    <div class="title">
      <Icon name="book" size={16} />
      <h1 class="truncate">{notebook.name}</h1>
      <Badge tone={sources.length === 0 ? 'neutral' : 'accent'}>local</Badge>
    </div>
    <div class="actions">
      <input
        bind:this={fileInput}
        type="file"
        multiple
        hidden
        onchange={(e) => upload(Array.from(e.currentTarget.files ?? []))}
      />
      <Button variant="primary" size="sm" onclick={pick}>
        <Icon name="plus" size={14} />
        Add sources
      </Button>
    </div>
  </div>

  <Tabs items={tabs} active={tab} onselect={(id) => ontab(id as Panel)} />

  <div class="panel">
    {#if error}
      <div class="banner error" role="alert">
        <Icon name="alert" size={14} />
        {error}
        <button type="button" class="dismiss" aria-label="Dismiss" onclick={() => (error = '')}>
          <Icon name="x" size={14} />
        </button>
      </div>
    {/if}

    {#if tab === 'sources'}
      <div
        class="dropzone"
        class:dragging
        role="presentation"
        ondragenter={(e) => {
          e.preventDefault();
          dragging = true;
        }}
        ondragover={(e) => {
          e.preventDefault();
          dragging = true;
        }}
        ondragleave={() => (dragging = false)}
        ondrop={onDrop}
      >
        <div class="toolbar">
          <label class="filter">
            <Icon name="search" size={14} />
            <input placeholder="Filter sources" bind:value={filter} />
          </label>
          <span class="muted small">
            {#if uploading > 0}
              uploading {uploading}…
            {:else if busy}
              <Icon name="clock" size={12} /> processing…
            {:else}
              {sources.length} source{sources.length === 1 ? '' : 's'}
            {/if}
          </span>
        </div>

        {#if loading}
          <p class="muted center">Loading…</p>
        {:else if sources.length === 0}
          <EmptyState
            icon="upload"
            title="Nothing in this notebook yet"
            description="Drop PDFs, images, or audio here. Text and page images are indexed separately, and the original file is always kept."
          >
            <Button variant="primary" size="sm" onclick={pick}>Choose files</Button>
          </EmptyState>
        {:else if visible.length === 0}
          <p class="muted center">No source matches “{filter}”.</p>
        {:else}
          <ul class="list">
            {#each visible as source (source.id)}
              <li class="source" class:processing={source.status === 'processing'}>
                <span class="kind"><Icon name={kindIcon[source.kind]} size={16} /></span>

                <div class="meta">
                  <div class="name-row">
                    <span class="name truncate" title={source.name}>{source.name}</span>
                    <Badge tone={statusTone[source.status]}>
                      {#if source.status === 'processing'}<span class="pulse"></span>{/if}
                      {source.status}
                    </Badge>
                  </div>

                  <div class="facts muted small">
                    {source.kind} · {formatBytes(source.bytes)}
                    {#if source.status === 'ready'}
                      · {source.segment_count} chunk{source.segment_count === 1 ? '' : 's'}
                    {/if}
                    · added {formatAge(source.added_at)}
                  </div>

                  {#if source.detail}
                    <p class="note {source.status === 'failed' ? 'bad' : ''}">{source.detail}</p>
                  {/if}
                  {#if source.warnings?.length}
                    <p class="note">{source.warnings.join(' · ')}</p>
                  {/if}
                </div>

                <div class="row-actions">
                  {#if source.status !== 'processing'}
                    <Button
                      variant="ghost"
                      size="sm"
                      iconOnly
                      title="Run this file through the pipeline again — every chunk, raster and vector it has is replaced"
                      onclick={() => rebuild(source)}
                    >
                      <Icon name="retry" size={14} />
                    </Button>
                  {/if}
                  {#if source.status === 'ready' || source.status === 'failed'}
                    <Button
                      variant="ghost"
                      size="sm"
                      iconOnly
                      title="See how this source was chunked"
                      onclick={() => (inspecting = source)}
                    >
                      <Icon name="layers" size={14} />
                    </Button>
                  {/if}
                  {#if source.status === 'ready' || source.status === 'unsupported' || source.status === 'failed'}
                    <a
                      class="iconlink"
                      href={originalURL(source.id)}
                      target="_blank"
                      rel="noreferrer"
                      title="Open the original"
                    >
                      <Icon name="open" size={14} />
                    </a>
                  {/if}
                  <Button
                    variant="ghost"
                    size="sm"
                    iconOnly
                    title="Remove from this notebook (the file is kept)"
                    onclick={() => remove(source)}
                  >
                    <Icon name="trash" size={14} />
                  </Button>
                </div>
              </li>
            {/each}
          </ul>
        {/if}

        {#if dragging}
          <div class="drop-hint"><Icon name="upload" size={20} /> Drop to add</div>
        {/if}
      </div>
    {:else if tab === 'chat'}
      <ChatPanel notebook={notebook} {sources} oncite={openCitation} {onchat} />
    {:else}
      <EmptyState
        icon="pulse"
        title="No activity"
        description="Ingest stages, research iterations, and model calls will show up here."
      />
    {/if}
  </div>
</div>

{#if inspecting}
  <ChunkViewer
    source={inspecting}
    focus={focus ?? undefined}
    onclose={() => {
      inspecting = null;
      focus = null;
    }}
  />
{/if}

<style>
  .page {
    max-width: 1080px;
    padding: 24px;
  }
  .title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    margin-bottom: 16px;
  }
  .title {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    color: var(--fg-muted);
  }
  .title h1 {
    color: var(--fg);
  }
  .actions {
    display: flex;
    gap: 8px;
    flex-shrink: 0;
  }
  .panel {
    margin-top: 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .dropzone {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .dropzone.dragging {
    outline: 2px dashed var(--accent);
    outline-offset: 6px;
  }
  .drop-hint {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    gap: 8px;
    font-size: 14px;
    color: var(--accent);
    background: color-mix(in srgb, var(--accent-subtle) 80%, transparent);
    pointer-events: none;
  }
  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 0;
  }
  .filter {
    display: flex;
    align-items: center;
    gap: 8px;
    width: min(320px, 50vw);
    height: 30px;
    padding: 0 10px;
    color: var(--fg-muted);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--canvas);
  }
  .filter input {
    flex: 1;
    min-width: 0;
    font: inherit;
    color: var(--fg);
    background: none;
    border: none;
    outline: none;
  }
  .small {
    font-size: 12px;
  }
  .center {
    padding: 24px 0;
    text-align: center;
  }
  .list {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--canvas);
  }
  .source {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border-muted);
  }
  .source:last-child {
    border-bottom: none;
  }
  /* Ingest has no progress stream yet, so a working row is dimmed rather than
     showing a percentage we do not have. */
  .source.processing .name {
    color: var(--fg-muted);
  }
  .kind {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    flex-shrink: 0;
    color: var(--fg-muted);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--canvas-subtle);
  }
  .meta {
    flex: 1;
    min-width: 0;
  }
  .name-row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .name {
    font-size: 14px;
    font-weight: 500;
  }
  .facts {
    margin-top: 2px;
  }
  .note {
    margin: 4px 0 0;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .note.bad {
    color: var(--danger);
  }
  .row-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    flex-shrink: 0;
  }
  .iconlink {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    color: var(--fg-muted);
    border-radius: var(--radius);
  }
  .iconlink:hover {
    color: var(--accent);
    background: var(--canvas-subtle);
  }
  .pulse {
    width: 6px;
    height: 6px;
    margin-right: 5px;
    border-radius: 50%;
    background: currentColor;
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    0%,
    100% {
      opacity: 0.25;
    }
    50% {
      opacity: 1;
    }
  }
  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    font-size: 13px;
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    background: var(--canvas);
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
  @media (prefers-reduced-motion: reduce) {
    .pulse {
      animation: none;
    }
  }
</style>
