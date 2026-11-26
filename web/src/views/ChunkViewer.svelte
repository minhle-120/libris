<script lang="ts">
  import { app, closeViewer } from '../lib/store.svelte';
  import { chunksOf, repeatOf } from '../lib/chunks';
  import { formatBytes, formatSpan } from '../lib/format';
  import { assetURL } from '../lib/api';
  import type { Segment } from '../lib/types';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';

  /**
   * How a file was actually cut up, read straight off the stored segments.
   *
   * It replaces the panel's body rather than floating over it. This column is 380px wide; a
   * view inside a view inside it is three scroll containers deep, and the reader ends up
   * scrolling the wrapper of the thing they came to read.
   *
   * A segment *is* a chunk. A PDF yields two documents from one file — the text, and the page
   * rasters — so the list is grouped by document and numbered per document, because a chunk of
   * text and an image of a page are not two of the same thing.
   */
  let { sourceId }: { sourceId: string } = $props();

  const source = $derived(app.sources.find((s) => s.id === sourceId));
  const all = $derived(chunksOf(app.viewerSegments, sourceId));
  const images = $derived(all.filter((s) => s.kind === 'image'));
  const clips = $derived(all.filter((s) => s.kind === 'audio'));
  const texts = $derived(all.filter((s) => s.kind === 'text'));

  /** A text chunk and a page raster both come from a PDF; a text file has one document, and
   *  naming it "page text" would be a lie about something nobody split into pages. */
  const textTitle = $derived(images.length ? 'page text' : 'text');

  /** Where a clip sits in the recording, in the shape the player under it shows. */
  const when = (s: Segment) =>
    s.span?.t0 === undefined ? '\u2014' : formatSpan(s.span.t0, s.span.t1);

  const openSet = $state<Record<string, boolean>>({});
  const dims = $state<Record<string, string>>({});

  /** Only the preview cap is at issue: the whole text is in the DOM either way, so expanding
   *  never re-fetches, and the overlap the shading marks is never hidden from a check. */
  const CAP = 900;

  const ord = (s: Segment) => String(s.ordinal).padStart(4, '0');
  const pages = (s: Segment) => {
    const a = s.span?.page;
    // A dash, not a blank: silence in the one column that says where a chunk came from reads
    // as a rendering bug rather than as "this file has no pages".
    if (a === undefined) return '\u2014';
    const b = s.span?.page_end;
    return b && b > a ? `p${a}\u2013${b}` : `p${a}`;
  };

  /** Arithmetic over text that is already on the page, so it cannot disagree with what the
   *  reader is looking at. */
  const words = (t: string) => (t.trim() ? t.trim().split(/\s+/).length : 0);

  /** Where this chunk sits in the file it came from, in characters. */
  const range = (s: Segment) => {
    const a = s.span?.char_start;
    if (a === undefined) return '';
    return `${a}\u2013${s.span?.char_end ?? a}`;
  };

  /** The repeat is found per pair, in reading order, and only for text. */
  const marked = $derived.by(() => {
    const out = new Map<string, string>();
    for (let i = 1; i < texts.length; i++) {
      const rep = repeatOf(texts[i - 1].text ?? '', texts[i].text ?? '');
      if (rep) out.set(texts[i].id, rep);
    }
    return out;
  });

  const legend = $derived(
    marked.size
      ? 'The shaded text repeats the tail of the chunk before it, so a sentence is never cut in half where the search reads it.'
      : 'One chunk, or none of them overlaps: no chunk repeats any other.'
  );

  let list = $state<HTMLElement>();

  /** The citation that opened this view is brought into sight, and ringed, rather than left
   *  somewhere below the fold. */
  $effect(() => {
    const want = app.viewerSeg;
    const n = all.length;
    void n;
    if (!want || !list) return;
    // The middle, not the top edge: the reader came to read this chunk, and a chunk pinned
    // under the viewer's own header is half out of reach.
    list.querySelector(`[data-seg="${CSS.escape(want)}"]`)?.scrollIntoView({ block: 'center' });
  });
</script>

<section class="viewer" aria-label="Chunks of {source?.name ?? 'source'}">
  <header>
    <div class="top">
      <Button
        variant="ghost"
        shape="icon"
        title="Back to sources"
        ariaLabel="Back to sources"
        onclick={closeViewer}
      >
        <Icon name="back" size={14} />
      </Button>
      <h2 class="truncate" title={source?.name}>{source?.name ?? 'Chunks'}</h2>
    </div>
    <!-- What is in here, in plain words — and where a load failure says so. Above the list
         rather than inside it: the failure replaces the list, so a sentence buried in the
         empty column below reads as part of the emptiness. -->
    <p class="sum" class:bad={!!app.viewerError} role={app.viewerError ? 'alert' : undefined}>
      {#if app.viewerError}
        {app.viewerError}
      {:else}
        {all.length} {all.length === 1 ? 'chunk' : 'chunks'}
        · {texts.length} text
        · {images.length} {images.length === 1 ? 'image' : 'images'}
        {#if source}
          · {source.kind}
          · {formatBytes(source.bytes)}
        {/if}
      {/if}
    </p>
  </header>

  <div class="body" bind:this={list}>
    {#if !all.length && !app.viewerError}
      <p class="center">Nothing was stored from this file.</p>
    {/if}

    {#if texts.length}
      <div class="doc-head">
        <h3>{textTitle}</h3>
        <span class="small">{texts.length}</span>
      </div>
      <ol class="chunks">
        {#each texts as s (s.id)}
          {@const full = s.text ?? ''}
          {@const rep = marked.get(s.id) ?? ''}
          {@const shown = openSet[s.id] || full.length <= CAP}
          <li class="chunk" class:highlighted={s.id === app.viewerSeg} data-seg={s.id}>
            <div class="cmeta">
              <span class="ord">{ord(s)}</span>
              <span class="prov">
                <span>{pages(s)}</span>
                <span>{words(full)} words</span>
                <span>{range(s)}</span>
              </span>
            </div>
            <div class="text">
              {#if !full}
                <span class="none">No text in this chunk.</span>
              {:else}
                {#if rep}<mark>{rep}</mark>{/if}{full.slice(rep.length)}
              {/if}
            </div>
            {#if !shown}
              <Button variant="ghost" size="sm" klass="more" onclick={() => (openSet[s.id] = true)}>
                … +{full.length - CAP} chars
              </Button>
            {/if}
          </li>
        {/each}
      </ol>
    {/if}

    {#if clips.length}
      <div class="doc-head">
        <h3>clips</h3>
        <span class="small">{clips.length}</span>
      </div>
      <ul class="clips">
        {#each clips as s (s.id)}
          <li class="clip" class:highlighted={s.id === app.viewerSeg} data-seg={s.id}>
            <div class="cmeta">
              <span class="ord">{ord(s)}</span>
              <span class="prov">
                <span>{when(s)}</span>
                <span>{s.asset ? formatBytes(s.asset.bytes) : ''}</span>
              </span>
            </div>
            {#if s.asset}
              <!-- preload="none": a source can hold hundreds of these, and a viewer that
                   fetched every one to draw a play button would read the whole recording off
                   the disk to show its list. -->
              <audio controls preload="none" src={assetURL(s.asset)}></audio>
            {:else}
              <p class="center">No clip stored for this chunk.</p>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if images.length}
      <div class="doc-head">
        <h3>page rasters</h3>
        <span class="small">{images.length}</span>
      </div>
      <ul class="tiles">
        {#each images as s (s.id)}
          <li class="tile" class:highlighted={s.id === app.viewerSeg} data-seg={s.id}>
            {#if s.asset}
              <!-- The thumbnail is for finding the page; the page itself is what a reader
                   clicks for, and a raster is only worth squinting at at full size. -->
              <a href={assetURL(s.asset)} target="_blank" rel="noopener"
                title="Open page {s.span?.page ?? s.ordinal + 1} at full size">
                <img
                  src={assetURL(s.asset)}
                  alt="Page {s.span?.page ?? s.ordinal + 1}"
                  onload={(e) => {
                    const img = e.currentTarget as HTMLImageElement;
                    dims[s.id] = `${img.naturalWidth}\u00d7${img.naturalHeight}`;
                  }}
                />
              </a>
            {:else}
              <p class="center">No page image for this page.</p>
            {/if}
            <p class="cap">
              {ord(s)} {pages(s)} {dims[s.id] ?? ''} {s.asset ? formatBytes(s.asset.bytes) : ''}
            </p>
          </li>
        {/each}
      </ul>
    {/if}
  </div>

  <footer>
    <p class="small">{legend}</p>
  </footer>
</section>

<style>
  .viewer {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
  }

  header {
    flex: none;
    padding: 6px 10px 8px;
    border-bottom: 1px solid var(--border-soft);
  }

  .top {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  h2 {
    flex: 1;
    min-width: 0;
    font-size: var(--fs);
    font-weight: 600;
  }

  header p {
    font-size: var(--fs-xs);
    color: var(--muted);
    padding-top: 2px;
  }

  header p.bad {
    color: var(--danger);
  }

  .body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 0 10px 12px;
  }

  .doc-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
    padding: 10px 0 4px;
    border-bottom: 1px solid var(--border-soft);
  }

  .doc-head h3 {
    font-size: var(--fs-xs);
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .small {
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .chunks {
    margin: 0;
    padding: 8px 0 0;
    list-style: none;
    display: grid;
    gap: 10px;
  }

  /* Each chunk carries its own left edge, so a run of them reads as one text cut into pieces
     rather than a list of equal boxes. */
  .chunk {
    border-left: 2px solid var(--border);
    padding-left: 9px;
    scroll-margin-top: 8px;
  }

  .chunk.highlighted {
    /* A ring, not a fill. The shading inside a chunk is already colour — the tail the chunker
       repeated — and a tint laid over it would hide exactly what that shading says. */
    border-left-color: var(--accent);
    outline: 1px solid var(--accent);
    outline-offset: -1px;
    border-radius: 0 var(--r) var(--r) 0;
  }

  .cmeta {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
    padding-bottom: 2px;
  }

  .ord {
    font-family: var(--mono);
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  /* Page, words, and where in the file — the three facts that make a chunk findable, in a
     column narrow enough that they stack rather than crowd one line. */
  .prov {
    display: flex;
    flex-wrap: wrap;
    gap: 2px 8px;
    font-family: var(--mono);
    font-size: var(--fs-xs);
    color: var(--muted);
    text-align: right;
  }

  .text {
    font-size: var(--fs-sm);
    line-height: 1.55;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }

  /* The repeat, which is the previous chunk's own tail. */
  .text :global(mark) {
    background: var(--accent-bg);
    color: var(--accent-text);
    padding: 0 1px;
    border-radius: 2px;
  }

  .clips {
    list-style: none;
    margin: 0 0 var(--sp-4);
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }

  .clip {
    border: 1px solid var(--line);
    border-radius: var(--r-2);
    padding: var(--sp-2);
    display: grid;
    gap: var(--sp-2);
  }

  /* The player is the content of a clip, so it gets the full width of the column. */
  .clip audio {
    width: 100%;
    height: 32px;
  }

  .tiles {
    margin: 0;
    padding: 8px 0 0;
    list-style: none;
    display: grid;
    gap: 10px;
  }

  .tile {
    scroll-margin-top: 8px;
    padding: 4px;
    border: 1px solid var(--border);
    border-radius: var(--r);
    background: var(--raised);
  }

  .tile.highlighted {
    border-color: var(--accent);
    box-shadow: 0 0 0 1px var(--accent);
  }

  .tile a {
    display: block;
  }

  .tile img {
    display: block;
    width: 100%;
    border-radius: 3px;
  }

  .none {
    color: var(--muted);
  }

  .cap {
    padding-top: 4px;
    font-family: var(--mono);
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  footer {
    flex: none;
    padding: 8px 10px;
    border-top: 1px solid var(--border-soft);
  }
</style>
