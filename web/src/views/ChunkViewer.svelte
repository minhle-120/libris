<script lang="ts">
  import { tick } from 'svelte';
  import { app, chunkLoaded, closeViewer, loadMoreViewerChunks } from '../lib/store.svelte';
  import { chunksOf, repeatOf } from '../lib/chunks';
  import { formatBytes, formatSpan } from '../lib/format';
  import { assetURL } from '../lib/api';
  import { hasTimeline, type Segment } from '../lib/types';
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
  const clips = $derived(all.filter((s) => hasTimeline(s.kind)));
  const texts = $derived(all.filter((s) => s.kind === 'text'));

  /** Two kinds of clip, or one: a video is frames and a soundtrack, and a recording is only
   *  ever the second. They are listed apart because they are different documents of the same
   *  file and are numbered apart, and put together the list reads as if one window were two
   *  moments. */
  const sounds = $derived(clips.filter((s) => s.kind === 'audio'));
  const pictures = $derived(clips.filter((s) => s.kind === 'video'));

  /** A text chunk and a page raster both come from a PDF; a text file has one document, and
   *  naming it "page text" would be a lie about something nobody split into pages. */
  const textTitle = $derived(images.length ? 'page text' : 'text');

  /** Where a clip sits — in a recording, or in a video — in the shape the player under it
   *  shows, and the shape the model reads in a citation. */
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

  /** The repeat is found per pair, in reading order, and only for text.
   *
   *  A long source's chunks arrive a window at a time, and the pairs are found once rather
   *  than over the whole file on every arrival: a chunk's text never changes, and neither
   *  does the chunk before it, because the windows extend the list without disturbing it. */
  let reps = new Map<string, string>();
  let repOfSource = '';
  let repFrom = 0;
  const marked = $derived.by(() => {
    const list = texts;
    if (repOfSource !== (app.viewer ?? '')) {
      repOfSource = app.viewer ?? '';
      reps = new Map();
      repFrom = 0;
    }
    for (let i = repFrom; i < list.length; i++) {
      const rep = i ? repeatOf(list[i - 1].text ?? '', list[i].text ?? '') : '';
      if (rep) reps.set(list[i].id, rep);
    }
    repFrom = list.length;
    // A fresh wrapper around the cache, every time. The pairs live in one Map that is
    // extended in place, and a derived that hands back the same object tells Svelte nothing
    // changed — the shading, and the sentence about it, would be written from a half-filled
    // cache and never corrected.
    return { reps, checked: list.length };
  });

  /** What the file holds, as the server counted it, rather than what has reached the page. A
   *  heading that counted the fetch would tell a reader halfway down a nine-hundred-page PDF
   *  that the file contains no pictures. */
  const fileCount = (kind?: string) =>
    kind
      ? (app.viewerKinds[kind] ?? 0)
      : Object.values(app.viewerKinds).reduce((n, k) => n + k, 0);

  const legend = $derived(
    marked.reps.size
      ? 'The shaded text repeats the tail of the chunk before it, so a sentence is never cut in half where the search reads it.'
      : 'One chunk, or none of them overlaps: no chunk repeats any other.'
  );

  let list = $state<HTMLElement>();

  /** The next window is asked for as the reader runs out of page, and not before. Four
   *  screens of clearance is about the distance a fast flick covers while a fetch is in the
   *  air, so the list arrives under the scroll instead of stopping short of it. */
  const NEAR = 4000;
  function onScroll() {
    if (!list) return;
    if (list.scrollHeight - list.scrollTop - list.clientHeight >= NEAR) return;
    void loadMoreViewerChunks();
  }

  /** One player for the whole clip list.
   *
   *  A clip is thirty seconds of a recording, so a long one holds hundreds, and each used to
   *  carry its own media element — which is what made opening a recording slow. A media
   *  element costs far more to create than the row around it, and the reader hears one of
   *  them at a time. The row keeps its number and its window in the recording, and asks the
   *  player below for the sound. */
  let playerEl = $state<HTMLAudioElement | HTMLVideoElement>();
  let clip = $state<Segment | null>(null);
  let playingId = $state('');

  async function playClip(s: Segment) {
    if (!s.asset) return;
    if (playingId === s.id && playerEl && !playerEl.paused) {
      playerEl.pause();
      return;
    }
    // The element a clip of a video plays in is not the element a clip of sound plays in, and
    // it only exists once `clip` has been read by the template — so the element is looked up
    // after the state changes rather than captured before it.
    const fresh = clip?.id !== s.id;
    if (fresh) clip = s;
    await tick();
    const el = playerEl;
    if (!el) return;
    if (fresh) el.src = assetURL(s.asset);
    try {
      await el.play();
    } catch {
      // A file the server cannot open any more. The list is still the list, and a reader
      // who moves on needs no message about it.
      clip = null;
      playingId = '';
    }
  }

  /** A recording keeps sounding when the reader opens a different source in the same viewer,
   *  because the player outlives the change. It is stopped when its file is no longer the
   *  one on screen. */
  let listeningTo = '';
  $effect(() => {
    const id = app.viewer ?? '';
    if (listeningTo && listeningTo !== id) {
      playerEl?.pause();
      clip = null;
      playingId = '';
    }
    listeningTo = id;
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
        {fileCount()} {fileCount() === 1 ? 'chunk' : 'chunks'}
        · {fileCount('text')} text
        · {fileCount('image')} {fileCount('image') === 1 ? 'image' : 'images'}
        {#if source}
          · {source.kind}
          · {formatBytes(source.bytes)}
        {/if}
      {/if}
    </p>
  </header>

  <div class="body" bind:this={list} onscroll={onScroll}>
    {#if !all.length && !app.viewerError}
      <p class="center">Nothing was stored from this file.</p>
    {/if}

    {#if texts.length}
      <div class="doc-head">
        <h3>{textTitle}</h3>
        <span class="small">{fileCount('text')}</span>
      </div>
      <ol class="chunks">
        {#each texts as s (s.id)}
          {@const full = s.text ?? ''}
          {@const rep = marked.reps.get(s.id) ?? ''}
          {@const shown = openSet[s.id] || full.length <= CAP}
          <li class="chunk">
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

    {#if pictures.length}
      <div class="doc-head">
        <h3>clips of the pictures</h3>
        <span class="small">{fileCount('video')}</span>
      </div>
      <ul class="clips">
        {#each pictures as s (s.id)}
          <li class="clip">
            <div class="cmeta">
              <span class="ord">{ord(s)}</span>
              <span class="prov">
                <span>{when(s)}</span>
                <span>{s.asset ? formatBytes(s.asset.bytes) : ''}</span>
              </span>
            </div>
            {#if s.asset}
              <!-- Same one player for the whole list, and the same reason: a media element
                   costs far more to make than the row around it, and a reader watches one of
                   them at a time. -->
              <button class="play" type="button" onclick={() => playClip(s)}>
                {playingId === s.id ? 'Pause' : 'Play'}
              </button>
            {:else}
              <p class="center">No clip stored for this chunk.</p>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if sounds.length}
      <div class="doc-head">
        <h3>{pictures.length ? 'clips of the soundtrack' : 'clips'}</h3>
        <span class="small">{fileCount('audio')}</span>
      </div>
      <ul class="clips">
        {#each sounds as s (s.id)}
          <li class="clip">
            <div class="cmeta">
              <span class="ord">{ord(s)}</span>
              <span class="prov">
                <span>{when(s)}</span>
                <span>{s.asset ? formatBytes(s.asset.bytes) : ''}</span>
              </span>
            </div>
            {#if s.asset}
              <!-- One player serves the whole list, so a hundred clips are a hundred rows
                   rather than a hundred media elements. Nothing is fetched until the reader
                   presses this. -->
              <button class="play" type="button" onclick={() => playClip(s)}>
                {playingId === s.id ? 'Pause' : 'Play'}
              </button>
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
        <span class="small">{fileCount('image')}</span>
      </div>
      <ul class="tiles">
        {#each images as s (s.id)}
          <li class="tile">
            {#if s.asset}
              <!-- The thumbnail is for finding the page; the page itself is what a reader
                   clicks for, and a raster is only worth squinting at at full size. -->
              <a href={assetURL(s.asset)} target="_blank" rel="noopener"
                title="Open page {s.span?.page ?? s.ordinal + 1} at full size">
                <img
                  src={assetURL(s.asset)}
                  loading="lazy"
                  decoding="async"
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
    {#if !app.viewerError && fileCount() > chunkLoaded()}
      <p class="small">
        {#if app.viewerLoading}
          fetching more chunks…
        {:else}
          {chunkLoaded()} of {fileCount()} chunks · keep scrolling for the rest
        {/if}
      </p>
    {/if}
  </footer>

  <!-- The sound of whichever clip the reader pressed. Kept in the tree while empty so the
       element is here to be aimed at, and hidden rather than unmounted so a player cannot
       vanish mid-sentence with no way back to where it stopped. -->
  <div class="player" class:shown={!!clip}>
    {#if clip}
      <span class="small truncate">
        clip {ord(clip)} · {when(clip)}
      </span>
    {/if}
    {#if clip?.kind === 'video'}
      <video
        bind:this={playerEl}
        controls
        preload="none"
        onplay={() => (playingId = clip?.id ?? '')}
        onpause={() => (playingId = '')}
      ></video>
    {:else}
      <audio
        bind:this={playerEl}
        controls
        preload="none"
        onplay={() => (playingId = clip?.id ?? '')}
        onpause={() => (playingId = '')}
      ></audio>
    {/if}
  </div>
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

  /* Asking for the sound is the content of a clip row, so it gets the full width of the
     column — and it is a row of the list rather than a widget, so it is styled as one. */
  .clip .play {
    width: 100%;
    font: inherit;
    font-size: var(--fs-xs);
    color: var(--ink);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-2);
    padding: var(--sp-2);
    cursor: pointer;
  }

  .clip .play:hover {
    border-color: var(--border);
    background: var(--surface-2);
  }

  .player {
    display: none;
    flex: none;
    align-items: center;
    gap: var(--sp-2);
    padding: 6px 10px;
    border-top: 1px solid var(--border-soft);
    background: var(--surface);
  }

  .player.shown {
    display: flex;
  }

  .player audio {
    flex: 1;
    min-width: 0;
    height: 32px;
  }

  /* A clip of a video is watched rather than heard, so it is given the width of the column
     and a height worth seeing, instead of the height of a control bar. */
  .player video {
    flex: 1;
    min-width: 0;
    max-height: 45vh;
    background: #000;
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
