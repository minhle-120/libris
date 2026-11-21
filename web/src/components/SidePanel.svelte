<script lang="ts">
  import { originalURL } from '../lib/api';
  import { formatAge, formatBytes } from '../lib/format';
  import { dragHandle, readW, SIDE } from '../lib/panel';
  import type { Panel, Source } from '../lib/types';
  import Icon from '../lib/Icon.svelte';
  import Badge from '../lib/Badge.svelte';
  import Button from '../lib/Button.svelte';
  import EmptyState from '../lib/EmptyState.svelte';
  import ChunkViewer from './ChunkViewer.svelte';

  let {
    panel,
    open,
    sources,
    loading,
    uploading,
    busy,
    inspecting,
    focus = undefined,
    onpanel,
    onopen,
    oninspect,
    onupload,
    onrebuild,
    onremove
  }: {
    panel: Panel;
    open: boolean;
    sources: Source[];
    loading: boolean;
    uploading: number;
    busy: boolean;
    /** The source whose chunks are being shown, which sits above Sources in the breadcrumb. */
    inspecting: Source | null;
    /** A chunk a citation pointed at, carried into the viewer. */
    focus?: { segment_id: string; chunk?: number } | undefined;
    onpanel: (panel: Panel) => void;
    onopen: (open: boolean) => void;
    oninspect: (source: Source | null) => void;
    onupload: (files: File[]) => void;
    onrebuild: (source: Source) => void;
    onremove: (source: Source) => void;
  } = $props();

  let width = $state(readW(SIDE));
  let filter = $state('');
  let dragging = $state(false);
  let fileInput: HTMLInputElement | undefined = $state();

  const handle = (node: HTMLElement) =>
    dragHandle(node, { spec: SIDE, get: () => width, set: (n) => (width = n) });

  const visible = $derived(
    sources
      .filter((s) => s.name.toLowerCase().includes(filter.trim().toLowerCase()))
      .slice()
      .sort((a, b) => b.added_at - a.added_at)
  );

  const kindIcon: Record<string, string> = {
    pdf: 'file',
    image: 'image',
    audio: 'audio',
    text: 'file',
    unsupported: 'inbox'
  };

  const statusTone: Record<string, 'neutral' | 'success' | 'attention' | 'danger'> = {
    processing: 'attention',
    ready: 'success',
    unsupported: 'neutral',
    failed: 'danger'
  };

  function pick() {
    fileInput?.click();
  }

  /** The rail is the drop target while it is open, so a drop has one meaning here. */
  function onDrop(e: DragEvent) {
    e.preventDefault();
    dragging = false;
    const files = Array.from(e.dataTransfer?.files ?? []);
    if (files.length) onupload(files);
  }
</script>

<aside class="sidepanel" class:closed={!open} style:width={open ? `${width}px` : `${SIDE.rail}px`}>
  {#if !open}
    <button
      type="button"
      class="rail-toggle"
      title="Show sources"
      aria-label="Show sources"
      onclick={() => onopen(true)}
    >
      <Icon name="panel-expand" size={16} />
    </button>
    <span class="rail-count" title="{sources.length} sources">{sources.length}</span>
    {#if busy}<span class="pulse" title="Something is running"></span>{/if}
  {:else}
    <div class="head">
      <div class="seg" role="tablist" aria-label="Notebook panels">
        <button
          type="button"
          role="tab"
          class="seg-btn"
          class:active={panel === 'sources'}
          aria-selected={panel === 'sources'}
          onclick={() => onpanel('sources')}
        >
          Sources
          {#if sources.length > 0}<span class="count">{sources.length}</span>{/if}
        </button>
        <button
          type="button"
          role="tab"
          class="seg-btn"
          class:active={panel === 'activity'}
          aria-selected={panel === 'activity'}
          onclick={() => onpanel('activity')}
        >
          Activity
          {#if busy}<span class="pulse"></span>{/if}
        </button>
      </div>
      <button
        type="button"
        class="rail-toggle"
        title="Hide panel"
        aria-label="Hide panel"
        onclick={() => onopen(false)}
      >
        <Icon name="panel-collapse" size={16} />
      </button>
    </div>

    {#if inspecting}
      <ChunkViewer source={inspecting} {focus} onback={() => oninspect(null)} />
    {:else if panel === 'activity'}
      <EmptyState
        icon="pulse"
        title="Nothing recorded yet"
        description="What the app did — ingest stages, research iterations, model calls — will be listed here."
      />
    {:else}
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
        <input
          bind:this={fileInput}
          type="file"
          multiple
          hidden
          onchange={(e) => onupload(Array.from(e.currentTarget.files ?? []))}
        />
        <div class="toolbar">
          <label class="filter">
            <Icon name="search" size={14} />
            <input placeholder="Filter sources" bind:value={filter} />
          </label>
          <Button variant="ghost" size="sm" iconOnly title="Add sources" onclick={pick}>
            <Icon name="plus" size={14} />
          </Button>
        </div>

        <p class="muted small status">
          {#if uploading > 0}
            uploading {uploading}…
          {:else if busy}
            <Icon name="clock" size={12} /> processing…
          {:else}
            {sources.length} source{sources.length === 1 ? '' : 's'}
          {/if}
        </p>

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
                      onclick={() => onrebuild(source)}
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
                      onclick={() => oninspect(source)}
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
                    onclick={() => onremove(source)}
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
    {/if}

    <button
      type="button"
      class="resizer"
      title="Drag to resize · double-click to reset"
      aria-label="Panel width. Drag, or use arrow keys."
      use:handle
    ></button>
  {/if}
</aside>

<style>
  .sidepanel {
    position: relative;
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    min-height: 0;
    border-left: 1px solid var(--border);
    background: var(--canvas);
  }
  .sidepanel.closed {
    align-items: center;
    gap: 8px;
    padding-top: 8px;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 8px 0 12px;
    border-bottom: 1px solid var(--border);
  }

  /* A segmented control, not page tabs: it switches the contents of one column, and
     it is not navigation — the breadcrumb above is. Same words, different job. */
  .seg {
    display: flex;
    gap: 2px;
    padding: 2px;
    background: var(--canvas-subtle);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .seg-btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 9px;
    font: inherit;
    font-size: 12px;
    font-weight: 500;
    color: var(--fg-muted);
    background: none;
    border: none;
    border-radius: var(--radius-sm);
    cursor: pointer;
  }
  .seg-btn:hover {
    color: var(--fg);
  }
  .seg-btn.active {
    color: var(--fg);
    background: var(--canvas);
    box-shadow: var(--shadow-sm, 0 1px 0 rgb(0 0 0 / 8%));
  }
  .count {
    font-size: 11px;
    color: var(--fg-muted);
  }

  .rail-toggle {
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
  .rail-toggle:hover {
    color: var(--fg);
    background: var(--canvas-subtle);
  }
  .rail-count {
    font-size: 12px;
    color: var(--fg-muted);
  }

  .dropzone {
    position: relative;
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 0 12px 12px;
  }
  .dropzone.dragging {
    outline: 2px solid var(--accent);
    outline-offset: -4px;
  }
  .drop-hint {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    gap: 8px;
    font-size: 13px;
    color: var(--accent);
    background: color-mix(in srgb, var(--accent-subtle) 80%, transparent);
    pointer-events: none;
  }
  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 10px 0 6px;
  }
  .filter {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1;
    min-width: 0;
    height: 28px;
    padding: 0 8px;
    color: var(--fg-muted);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--canvas);
  }
  .filter input {
    flex: 1;
    min-width: 0;
    font: inherit;
    font-size: 13px;
    color: var(--fg);
    background: none;
    border: none;
    outline: none;
  }
  .status {
    display: flex;
    align-items: center;
    gap: 5px;
    margin: 0 0 8px;
  }
  .small {
    font-size: 12px;
  }
  .center {
    padding: 16px 0;
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
    align-items: flex-start;
    gap: 8px;
    padding: 8px;
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
    width: 26px;
    height: 26px;
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
    gap: 6px;
    min-width: 0;
  }
  .name {
    font-size: 13px;
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
    gap: 0;
    flex-shrink: 0;
  }
  .iconlink {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
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

  .resizer {
    position: absolute;
    top: 0;
    left: -3px;
    z-index: 11;
    width: 7px;
    height: 100%;
    padding: 0;
    border: none;
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

  @media (prefers-reduced-motion: reduce) {
    .pulse {
      animation: none;
    }
  }
</style>
