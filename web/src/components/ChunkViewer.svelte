<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { assetURL, listSegments } from '../lib/api';
  import { formatBytes } from '../lib/format';
  import type { Segment, Source } from '../lib/types';
  import Icon from '../lib/Icon.svelte';
  import Badge from '../lib/Badge.svelte';
  import Button from '../lib/Button.svelte';

  let {
    source,
    focus = undefined,
    onback
  }: {
    source: Source;
    /** A chunk a citation points at: scrolled to and ringed once the chunks load. */
    focus?: { segment_id: string; chunk?: number };
    /** Back to the source list. The viewer is a step off Sources, not a modal over
     * the whole app, so it leaves the answer behind it readable. */
    onback: () => void;
  } = $props();

  let segments = $state<Segment[]>([]);
  let loading = $state(true);
  let error = $state('');

  /** Natural sizes, learned as page rasters load. Keyed by asset id. */
  let dims = $state<Record<string, string>>({});
  let open = $state<Record<string, boolean>>({});
  let root: HTMLElement | undefined = $state();

  /** The segment a citation pointed at, if one did. */
  let highlighted = $state('');

  // A chunk longer than this collapses, so a 100-word default stays scannable.
  const PREVIEW = 420;

  onMount(() => {
    load().then(() => {
      if (focus) reveal(focus);
    });
  });

  /**
   * Scroll to the chunk an answer cited, and say which one it was.
   *
   * Absent is a normal outcome, not a bug: the segment ids are `doc:ordinal`, so a
   * re-ingested source keeps them only while its chunking produces the same number of
   * chunks. If a source has been rebuilt with different settings since the answer was
   * written, the citation still quotes what was cited — it just cannot point at a
   * chunk that no longer exists.
   */
  async function reveal(where: { segment_id: string; chunk?: number }) {
    highlighted = where.segment_id;
    await tick(); // the chunks render after `segments` lands
    root?.querySelector<HTMLElement>(`[data-seg="${where.segment_id}"]`)?.scrollIntoView({
      block: 'center'
    });
  }

  async function load() {
    try {
      segments = await listSegments(source.id);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  /**
   * Longest tail of the previous chunk that the current one repeats — the
   * overlap the chunker inserts on purpose. Snapped back to a word boundary so
   * the shading never starts mid-word.
   */
  function overlap(prev: string, cur: string): number {
    const max = Math.min(prev.length, cur.length);
    for (let n = max; n > 0; n--) {
      if (prev.slice(-n) === cur.slice(0, n)) {
        let cut = n;
        while (cut > 0 && !/\s/.test(cur[cut] ?? ' ')) cut--;
        return cut;
      }
    }
    return 0;
  }

  function words(text: string): number {
    const trimmed = text.trim();
    return trimmed ? trimmed.split(/\s+/).length : 0;
  }

  /** A PDF yields two documents, and the viewer should say so in plain words. */
  function docLabel(docID: string): string {
    if (docID === source.id) return 'page text';
    if (docID.endsWith('#pages')) return 'page rasters';
    return docID;
  }

  /** Provenance, in the unit that document actually has. */
  function spanLabel(seg: Segment): string {
    const { page, page_end: pageEnd } = seg.span;
    if (page) return pageEnd && pageEnd !== page ? `p${page}–${pageEnd}` : `p${page}`;
    return '—';
  }

  type Chunk = { seg: Segment; head: string; body: string; words: number };

  // Segments arrive ordered by doc then ordinal, so a single pass groups them and
  // compares each chunk with the one before it in the same document.
  const groups = $derived.by(() => {
    const built: { docID: string; chunks: Chunk[]; prev: string }[] = [];
    for (const seg of segments) {
      let last = built[built.length - 1];
      if (!last || last.docID !== seg.doc_id) {
        last = { docID: seg.doc_id, chunks: [], prev: '' };
        built.push(last);
      }
      const cut = overlap(last.prev, seg.text);
      last.chunks.push({
        seg,
        head: seg.text.slice(0, cut),
        body: seg.text.slice(cut),
        words: words(seg.text)
      });
      last.prev = seg.text;
    }
    return built.map((g) => ({
      docID: g.docID,
      label: docLabel(g.docID),
      chunks: g.chunks
    }));
  });

  const summary = $derived.by(() => {
    const text = segments.filter((s) => s.kind === 'text').length;
    const images = segments.filter((s) => s.kind === 'image').length;
    const parts = [`${segments.length} chunk${segments.length === 1 ? '' : 's'}`];
    if (text) parts.push(`${text} text`);
    if (images) parts.push(`${images} image${images === 1 ? '' : 's'}`);
    return parts.join(' · ');
  });

  const hasOverlap = $derived(groups.some((g) => g.chunks.some((c) => c.head.length > 0)));

  function measure(assetID: string, img: HTMLImageElement) {
    dims[assetID] = `${img.naturalWidth}×${img.naturalHeight}`;
  }
</script>

<!-- No Escape handler, which the dialog version did not need written because
     showModal() gave it one. This is a view with a way back — the button here and the
     Sources crumb above it — not a top layer, so Escape would be a key with no claim
     on it: the composer keeps Escape while a chunk viewer is open beside it. -->
<section class="viewer" aria-label="Chunks of {source.name}" bind:this={root}>
    <header>
      <div class="head-text">
        <h2>How “{source.name}” is chunked</h2>
        <p class="muted small">
          {#if loading}loading…{:else if error}{error}{:else}{summary} · {source.kind} · {formatBytes(source.bytes)}{/if}
        </p>
      </div>
      <button type="button" class="close" aria-label="Back to sources" onclick={onback}>
        <Icon name="x" size={16} />
      </button>
    </header>

    <div class="body">
      {#if !loading && !error && segments.length === 0}
        <p class="muted center">Nothing was extracted from this source.</p>
      {/if}

      {#each groups as group (group.docID)}
        <div class="doc">
          <div class="doc-head">
            <h3>{group.label}</h3>
            <span class="muted small" title={group.docID}>
              {group.chunks.length} chunk{group.chunks.length === 1 ? '' : 's'} · doc {group.docID === source.id
                ? 'same as source'
                : group.docID.slice(group.docID.indexOf('#') + 1)}
            </span>
          </div>

          {#if group.chunks[0].seg.kind === 'image'}
            <div class="grid">
              {#each group.chunks as chunk (chunk.seg.id)}
                <a
                  class="tile"
                  class:highlighted={chunk.seg.id === highlighted}
                  data-seg={chunk.seg.id}
                  href={assetURL(chunk.seg.asset!)}
                  target="_blank"
                  rel="noreferrer"
                  title="Open the full raster"
                >
                  <img
                    src={assetURL(chunk.seg.asset!)}
                    alt="Page {chunk.seg.span.page}"
                    loading="lazy"
                    onload={(e) =>
                      measure(chunk.seg.asset!.id, e.currentTarget as HTMLImageElement)
                    }
                  />
                  <span class="cap">
                    <code>{String(chunk.seg.ordinal).padStart(4, '0')}</code>
                    <span>{spanLabel(chunk.seg)}</span>
                    <span class="muted">
                      {dims[chunk.seg.asset!.id] ?? '…'} · {formatBytes(chunk.seg.asset!.bytes)}
                    </span>
                  </span>
                </a>
              {/each}
            </div>
          {:else}
            <ul class="chunks">
              {#each group.chunks as chunk (chunk.seg.id)}
                <li
                  class="chunk"
                  class:highlighted={chunk.seg.id === highlighted}
                  data-seg={chunk.seg.id}
                  title={chunk.seg.id}
                >
                  <div class="chip">
                    <div class="ord">{String(chunk.seg.ordinal).padStart(4, '0')}</div>
                    <div class="prov">
                      <span>{spanLabel(chunk.seg)}</span>
                      <span>{chunk.words} words</span>
                      {#if chunk.seg.span.char_end}
                        <span class="muted">
                          chars {chunk.seg.span.char_start ?? 0}–{chunk.seg.span.char_end}
                        </span>
                      {/if}
                    </div>
                  </div>

                  {#if chunk.seg.text}
                    <div class="text">
                      {#if chunk.head}
                        <mark title="Repeated from the previous chunk">{chunk.head}</mark>
                      {/if}
                      {#if open[chunk.seg.id] || chunk.body.length <= PREVIEW}
                        {chunk.body}
                      {:else}
                        {chunk.body.slice(0, PREVIEW)}<button
                          type="button"
                          class="more"
                          onclick={() => (open[chunk.seg.id] = true)}>… +{chunk.body.length - PREVIEW} chars</button>
                      {/if}
                    </div>
                  {:else}
                    <p class="muted small empty-text">no text rendition</p>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </div>
      {/each}
    </div>

    <footer>
      <span class="muted small">
        {#if hasOverlap}
          <span class="swatch"></span> shaded text repeats the tail of the previous chunk
        {:else}
          no chunk repeats any part of the one before it
        {/if}
      </span>
      <Badge tone="neutral">{source.segment_count} segments</Badge>
    </footer>
</section>

<style>
  /* Panel content, not a top layer: it fills the column it was opened in and scrolls
     inside itself, so the answer that cited it stays on screen behind the edge. */
  .viewer {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    padding: 0;
    overflow: hidden;
  }
  header {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 16px;
    border-bottom: 1px solid var(--border);
  }
  .head-text {
    flex: 1;
    min-width: 0;
  }
  header h2 {
    margin: 0;
    font-size: 15px;
  }
  header p {
    margin: 4px 0 0;
  }
  .close {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    flex-shrink: 0;
    color: var(--fg-muted);
    border: 1px solid transparent;
    border-radius: var(--radius);
    background: none;
    cursor: pointer;
  }
  .close:hover {
    border-color: var(--border);
    background: var(--canvas-subtle);
  }
  .body {
    flex: 1;
    padding: 16px;
    overflow: auto;
  }
  .doc {
    margin-bottom: 24px;
  }
  .doc:last-child {
    margin-bottom: 0;
  }
  .doc-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    padding-bottom: 8px;
    border-bottom: 1px solid var(--border-muted);
  }
  .doc-head h3 {
    margin: 0;
    font-size: 13px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-muted);
  }
  .small {
    font-size: 12px;
  }
  .center {
    padding: 24px 0;
    text-align: center;
  }
  .chunks {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .chunk {
    display: flex;
    gap: 12px;
    padding: 10px 0;
    border-bottom: 1px solid var(--border-muted);
  }
  .chunk:last-child {
    border-bottom: none;
  }
  /* The chunk an answer pointed at. The rule is a ring rather than a background so it
     still reads against both themes and does not fight the overlap shading. */
  .chunk.highlighted,
  .tile.highlighted {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: var(--radius-sm);
  }
  .chip {
    display: flex;
    flex-direction: column;
    gap: 2px;
    width: 108px;
    flex-shrink: 0;
  }
  .ord {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg-muted);
  }
  .prov {
    display: flex;
    flex-direction: column;
    gap: 1px;
    font-size: 11px;
  }
  .text {
    flex: 1;
    min-width: 0;
    font-size: 13px;
    line-height: 1.55;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .text mark {
    color: inherit;
    background: color-mix(in srgb, var(--attention) 20%, transparent);
    border-radius: 2px;
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--attention) 35%, transparent);
  }
  .more {
    border: none;
    padding: 0;
    font: inherit;
    color: var(--accent);
    background: none;
    cursor: pointer;
  }
  .empty-text {
    margin: 0;
    font-style: italic;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(148px, 1fr));
    gap: 12px;
    padding-top: 12px;
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--canvas-subtle);
    text-decoration: none;
    color: inherit;
  }
  .tile:hover {
    border-color: var(--accent);
  }
  .tile img {
    width: 100%;
    height: 150px;
    object-fit: contain;
    border-radius: var(--radius-sm);
    background: var(--canvas);
  }
  .cap {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 6px;
    font-size: 11px;
  }
  .cap code {
    font-family: var(--font-mono);
    color: var(--fg-muted);
  }
  footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 16px;
    border-top: 1px solid var(--border);
    background: var(--canvas-subtle);
  }
  .swatch {
    display: inline-block;
    width: 10px;
    height: 10px;
    margin-right: 6px;
    vertical-align: -1px;
    background: color-mix(in srgb, var(--attention) 20%, transparent);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--attention) 35%, transparent);
  }
</style>
