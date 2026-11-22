<script lang="ts">
  import { originalURL } from '../lib/api';
  import { formatAge, formatBytes } from '../lib/format';
  import ConfirmDialog from '../lib/ConfirmDialog.svelte';
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

  // The last membership is a deletion, so that trash can does two different things
  // depending on a number the row knows and the click does not. Asking is only the
  // part that cannot be undone silently.
  let doomed = $state<Source | null>(null);
  let confirming = $state(false);

  function askRemove(source: Source) {
    doomed = source;
    confirming = true;
  }

  function confirmRemove() {
    if (!doomed) return;
    // `doomed` is cleared by the box closing, which keeps its title readable right up
    // until the moment it is gone.
    onremove(doomed);
    confirming = false;
  }

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
          <Button variant="default" size="sm" iconOnly title="Add sources" onclick={pick}>
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
                  </div>

                  <div class="facts muted small">
                    <Badge tone={statusTone[source.status]}>
                      {#if source.status === 'processing'}<span class="pulse"></span>{/if}
                      {source.status}
                    </Badge>
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
                  {#if source.notebooks_using > 1}
                    <Button
                      variant="ghost"
                      size="sm"
                      iconOnly
                      title={`Remove from this notebook — ${source.notebooks_using} notebooks use this file, so it stays`}
                      onclick={() => onremove(source)}
                    >
                      <Icon name="trash" size={14} />
                    </Button>
                  {:else}
                    <Button
                      variant="ghost"
                      size="sm"
                      iconOnly
                      title="Delete — no other notebook uses this file"
                      onclick={() => askRemove(source)}
                    >
                      <Icon name="trash" size={14} />
                    </Button>
                  {/if}
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

<ConfirmDialog
  label="Delete source"
  cls="delete-source"
  bind:open={confirming}
  title={`Delete “${doomed?.name ?? ''}”?`}
  lead={`No other notebook uses it, so its original, its ${doomed?.segment_count ?? 0} chunk${
    doomed?.segment_count === 1 ? '' : 's'
  } and every page image made from it are deleted. This cannot be undone.`}
  note="A file another notebook also uses is never deleted here: it stays ready, rasters and all, for the notebooks that cite it."
  confirmLabel="Delete file"
  onconfirm={confirmRemove}
  onclose={() => (doomed = null)}
/>

<style>
  /* The chrome plane. The chat is the deep one and the list items are raised cards on
     this one, so a source row reads as an object and not as a slice of a table. */
  .sidepanel {
    position: relative;
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    min-height: 0;
    border-left: 1px solid var(--border);
    background: var(--canvas-subtle);
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
    padding: 10px 8px 10px 14px;
    border-bottom: 1px solid var(--border-muted);
  }

  /* A segmented control, not page tabs: it switches the contents of one column, and
     it is not navigation — the breadcrumb above is. Same words, different job. */
  .seg {
    display: flex;
    gap: 2px;
    padding: 3px;
    background: var(--canvas);
    border: 1px solid var(--border-muted);
    border-radius: 999px;
  }
  .seg-btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 24px;
    padding: 0 11px;
    font: inherit;
    font-size: 12px;
    font-weight: 500;
    color: var(--fg-muted);
    background: none;
    border: none;
    border-radius: 999px;
    cursor: pointer;
    transition: background 80ms ease, color 80ms ease;
  }
  .seg-btn:hover {
    color: var(--fg);
  }
  .seg-btn.active {
    color: var(--fg);
    background: var(--elevated);
    box-shadow: var(--shadow-md);
  }
  .count {
    display: inline-grid;
    place-items: center;
    min-width: 15px;
    height: 15px;
    padding: 0 4px;
    font-size: 10px;
    font-weight: 600;
    line-height: 1;
    color: var(--fg-muted);
    background: color-mix(in srgb, var(--fg-muted) 18%, transparent);
    border-radius: 999px;
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
    background: var(--hover);
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
    gap: 6px;
    padding: 12px 0 8px;
  }
  .filter {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1;
    min-width: 0;
    height: 30px;
    padding: 0 10px;
    color: var(--fg-muted);
    border: 1px solid transparent;
    border-radius: 8px;
    background: var(--canvas);
    transition: border-color 80ms ease;
  }
  .filter:hover {
    border-color: var(--border);
  }
  .filter:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
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
  /* One card per source, not one box with hairlines through it: the row is the object
     a user aims at, and a bordered group of four rows reads as a table of nothing. */
  .list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .source {
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px;
    background: var(--elevated);
    border: 1px solid var(--border-muted);
    border-radius: 10px;
    transition: border-color 100ms ease, box-shadow 100ms ease;
  }
  .source:hover {
    border-color: var(--border);
    box-shadow: var(--shadow-md);
  }
  /* Ingest has no progress stream yet, so a working row is dimmed rather than
     showing a percentage we do not have. */
  .source.processing .name {
    color: var(--fg-muted);
  }
  .kind {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    flex-shrink: 0;
    color: var(--fg-muted);
    border: none;
    border-radius: 8px;
    background: color-mix(in srgb, var(--fg-muted) 14%, transparent);
  }
  .meta {
    flex: 1;
    min-width: 0;
  }
  .name-row {
    display: flex;
    align-items: center;
    min-width: 0;
  }
  .name {
    font-size: 13px;
    font-weight: 600;
  }
  /* The status lives on the fact line rather than beside the name: four action buttons
     in the corner plus a badge in the same row left about seven characters of filename,
     which is a worse way to say "this is your file" than a second line. */
  .facts {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 4px;
  }
  .note {
    margin: 4px 0 0;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .note.bad {
    color: var(--danger);
  }
  /* Four always-on icons per row is four decisions the user did not ask to make, and in
     flow they cost the filename 128px of width. So they arrive over the corner of the
     card — still in the DOM, still tab-reachable, and the name's tail is the one part of
     a card that is empty when the pointer gets there. */
  .row-actions {
    position: absolute;
    top: 6px;
    right: 6px;
    display: flex;
    align-items: center;
    gap: 0;
    padding: 1px;
    background: var(--elevated);
    border: 1px solid var(--border-muted);
    border-radius: 9px;
    box-shadow: var(--shadow-md);
    opacity: 0;
    transition: opacity 100ms ease;
  }
  .source:hover .row-actions,
  .source:focus-within .row-actions {
    opacity: 1;
  }
  @media (hover: none) {
    .row-actions {
      opacity: 1;
    }
  }
  .iconlink {
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    color: var(--fg-muted);
    border-radius: var(--radius);
  }
  .iconlink:hover {
    color: var(--accent);
    background: var(--hover);
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
