<script lang="ts">
  import { onMount } from 'svelte';
  import {
    getSource,
    detachSource,
    listSources,
    reingestSource,
    uploadSource
  } from '../lib/api';
  import { errorMessage } from '../lib/format';
  import type { Citation, Notebook, Panel, Source } from '../lib/types';
  import Icon from '../lib/Icon.svelte';
  import ChatPanel from './ChatPanel.svelte';
  import SidePanel from './SidePanel.svelte';

  let {
    notebook,
    panel,
    open,
    inspecting,
    onpanel,
    onopen,
    oninspect,
    onchange = undefined
  }: {
    notebook: Notebook;
    panel: Panel;
    open: boolean;
    /** Owned by App, because the breadcrumb has to say which source is open. */
    inspecting: Source | null;
    onpanel: (panel: Panel) => void;
    onopen: (open: boolean) => void;
    oninspect: (source: Source | null) => void;
    onchange?: () => void;
  } = $props();

  let sources = $state<Source[]>([]);
  let loading = $state(true);
  let error = $state('');
  let uploading = $state(0);

  // The chunk an answer cited, carried into the viewer so it opens on the place the
  // answer came from rather than at the top of a 400-page raster run.
  let focus: { segment_id: string; chunk?: number } | null = $state(null);

  const busy = $derived(uploading > 0 || sources.some((s) => s.status === 'processing'));

  onMount(load);

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
      oninspect(source);
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
      // A viewer left open on a detached source would keep rendering a document
      // the notebook no longer has, and the app cannot serve its segments twice.
      if (inspecting?.id === source.id) back();
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

  function back() {
    focus = null;
    oninspect(null);
  }
</script>

<div class="nb">
  <section class="body">
    {#if error}
      <div class="banner" role="alert">
        <Icon name="alert" size={14} />
        {error}
        <button type="button" class="dismiss" aria-label="Dismiss" onclick={() => (error = '')}>
          <Icon name="x" size={14} />
        </button>
      </div>
    {/if}

    <ChatPanel notebook={notebook} {sources} oncite={openCitation} />
  </section>

  <SidePanel
    {panel}
    {open}
    {sources}
    {loading}
    {uploading}
    {busy}
    {inspecting}
    focus={focus ?? undefined}
    {onpanel}
    {onopen}
    oninspect={(source) => (source ? oninspect(source) : back())}
    onupload={upload}
    onrebuild={rebuild}
    onremove={remove}
  />
</div>

<style>
  /* height:100% rather than flex:1 because main is the page's scroller and is not a
     flex container — flex:1 would do nothing here and the two columns would size to
     their content, so the chat would grow the page instead of scrolling itself. */
  .nb {
    display: flex;
    align-items: stretch;
    height: 100%;
    min-height: 0;
  }

  /* The chat is the page now, not one tab among three, so it gets the column that is
     left after the two panels and it scrolls inside itself. */
  .body {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
    min-height: 0;
  }
  /* Tinted rather than outlined in red: a full 1px red box around a sentence makes the
     page look broken, when what happened is one request failed. */
  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 12px 20px 0;
    padding: 10px 12px;
    font-size: 13px;
    color: var(--fg);
    border: 1px solid color-mix(in srgb, var(--danger) 35%, transparent);
    border-radius: 8px;
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
</style>
