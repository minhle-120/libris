<script lang="ts">
  import {
    app,
    openViewer,
    removeSource,
    reingest,
    resume,
    stop,
    setOpen,
    setPanel,
    setWidth,
    SIDE,
    uploadFiles
  } from '../lib/store.svelte';
  import { dragHandle } from '../lib/panel';
  import { formatBytes } from '../lib/format';
  import { hasTimeline, type Source } from '../lib/types';
  import Badge from '../lib/ui/Badge.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Empty from '../lib/ui/Empty.svelte';
  import Modal from '../lib/ui/Modal.svelte';
  import ChunkViewer from './ChunkViewer.svelte';
  import { originalURL, playerURL } from '../lib/api';
  import type { FindingCode } from '../lib/types';

  /**
   * A finding is a code and a detail (ADR 0008). A missing external tool becomes the action it
   * implies, because a bare name leaves the reader to work that out — and which action that is
   * depends on the machine rather than the file. A download carries its programs in a folder
   * beside libris, so there "install it" sends the reader after something that was never theirs
   * to install: what happened is that the folder got left behind when libris was moved. Anything
   * else says what was lost and stops there — re-running a file that has no more pages to give
   * gains nothing, so the row must not imply it would.
   */
  function finding(code: FindingCode, detail: string) {
    if (code !== 'tool_missing') return detail;
    if (app.tools?.bundled) {
      return `${detail} is missing from the tools folder beside libris. If libris was moved on its own, move that folder with it, then run this file again.`;
    }
    return `${detail} is not installed here. Install it, then run this file again.`;
  }

  /**
   * The right column: what is inside it is chosen by the segmented switcher at its top, because
   * the breadcrumb names the conversation, not the panel. The whole body is the drop target.
   */
  const open = $derived(app.sideOpen);
  const width = $derived(app.sideOpen ? app.sideW : SIDE.rail);
  const sources = $derived(app.sources);
  const running = $derived(sources.some((s) => s.status === 'processing'));
  const uploading = $derived(app.live === null && running);

  const filter = $state({ value: '' });
  const shown = $derived(
    filter.value.trim()
      ? sources.filter((s) => s.name.toLowerCase().includes(filter.value.trim().toLowerCase()))
      : sources
  );

  const status = $derived.by(() => {
    const n = sources.length;
    if (uploading) return 'Adding files, one at a time.';
    if (running) return 'A source is still being read.';
    if (!n) return 'Nothing here yet.';
    return `${n} ${n === 1 ? 'source' : 'sources'}.`;
  });

  let dragging = $state(false);
  let picker = $state<HTMLInputElement>();
  let doomed = $state<Source | null>(null);
  let error = $state('');

  const filesIn = (e: DragEvent) =>
    Array.from(e.dataTransfer?.files ?? []).filter((f) => f.size >= 0 || f.name);

  function ondrop(e: DragEvent) {
    e.preventDefault();
    dragging = false;
    const files = filesIn(e);
    if (files.length) uploadFiles(files);
  }

  function ondragleave(e: DragEvent) {
    const zone = e.currentTarget as HTMLElement;
    if (!zone.contains(e.relatedTarget as Node | null)) dragging = false;
  }

  const isLast = (s: Source) => s.notebooks_using <= 1;

  async function confirmDelete() {
    if (!doomed) return;
    error = '';
    try {
      await removeSource(doomed);
      doomed = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  /** Set while a drag has over-dragged past the floor: the column fades to say it is about to go. */
  let folding = $state(false);

  const handle = (node: HTMLElement) =>
    dragHandle(node, {
      spec: SIDE,
      get: () => app.sideW,
      set: (n) => setWidth(SIDE, n),
      open: () => app.sideOpen,
      setOpen: (v) => setOpen(SIDE, v),
      pending: (armed) => (folding = armed)
    });
</script>

<aside
  class="sidepanel"
  class:closed={!open}
  class:pending={folding}
  style="width: {width}px"
  aria-label="Sources and activity"
>
  {#if open}
    <div class="seg" role="tablist" aria-label="Panel">
      <button
        class="seg-btn"
        role="tab"
        tabindex="0"
        aria-selected={app.panel === 'sources'}
        onclick={() => setPanel('sources')}
      >
        Sources{#if sources.length}<span class="count">{' '}{sources.length}</span>{/if}
      </button>
      <button
        class="seg-btn"
        role="tab"
        tabindex="0"
        aria-selected={app.panel === 'activity'}
        onclick={() => setPanel('activity')}
      >
        Activity{#if running}<span class="dot pulse" title="Work in progress"></span>{/if}
      </button>
    </div>

    {#if app.viewer}
      <ChunkViewer sourceId={app.viewer} />
    {:else if app.panel === 'activity'}
      <Empty icon="clock" title="Nothing recorded yet" body="Libris does not keep a log of what it has done." />
    {:else}
      <div class="tools">
        <div class="filter">
          <!-- svelte-ignore a11y_label_has_associated_control -->
          <input
            class="input field-quiet"
            type="text"
            placeholder="Filter"
            aria-label="Filter sources"
            bind:value={filter.value}
          />
        </div>
        <Button
          variant="primary"
          title="Add sources"
          ariaLabel="Add sources"
          onclick={() => picker?.click()}
        >
          <Icon name="upload" />
          Add sources
        </Button>
        <input
          type="file"
          multiple
          hidden
          bind:this={picker}
          onchange={(e) => {
            const input = e.currentTarget;
            uploadFiles(Array.from(input.files ?? []));
            input.value = '';
          }}
        />
      </div>

      <p class="state">{status}</p>

      <!-- A region that takes an action deserves a name: role="presentation" here once told
           sighted users nothing while it was the only thing saying files could be dropped. -->
      <div
        class="dropzone"
        class:dragging
        aria-label="Drop files here to add them to this notebook"
        role="region"
        ondragenter={(e) => {
          if (e.dataTransfer?.types.includes('Files')) {
            e.preventDefault();
            dragging = true;
          }
        }}
        ondragover={(e) => {
          if (e.dataTransfer?.types.includes('Files')) {
            e.preventDefault();
            if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
          }
        }}
        ondragleave={ondragleave}
        ondrop={ondrop}
      >
        {#if dragging}<p class="drop-hint">Drop to add {#if sources.length}to {app.notebooks.find((n) => n.id === app.notebookId)?.name}{/if}</p>{/if}
        <div class="list">
          {#if shown.length}
            <ul>
              {#each shown as s (s.id)}
                <li class="source card" class:dim={s.status === 'processing'}>
                  <span class="kind" aria-hidden="true">
                    <Icon
                      name={s.kind === 'image'
                        ? 'image'
                        : s.kind === 'audio'
                          ? 'audio'
                          : s.status === 'unsupported' || s.kind === 'unsupported'
                            ? 'inbox'
                            : 'file'}
                    />
                  </span>

                  <div class="body">
                    <p class="name truncate" title={s.name}>{s.name}</p>
                    <div class="factsline">
                      <p class="facts">
                        <Badge
                          tone={s.status === 'ready'
                            ? 'success'
                            : s.status === 'processing'
                              ? 'attention'
                              : s.status === 'failed'
                              ? 'danger'
                              : s.status === 'stopped'
                                ? 'attention'
                                : 'neutral'}>{s.status}</Badge>
                        <span class="chip">{s.kind}</span>
                        <span>{formatBytes(s.bytes)}</span>
                        {#if s.status === 'ready'}
                          <span>{s.segment_count} {s.segment_count === 1 ? 'chunk' : 'chunks'}</span>
                        {:else if s.status === 'processing' || s.status === 'stopped'}
                          <!-- Chunks with vectors, against the chunks the file has. Extraction
                               writes a source's chunks once and embedding writes thousands, so
                               this is the number that moves — and on a stopped row it is the
                               part still to do that `resume` is about. -->
                          <span title="chunks vectorised so far, out of the chunks this file has">
                            {s.embedded_count} / {s.segment_count}
                            {s.segment_count === 1 ? 'chunk' : 'chunks'}
                          </span>
                        {/if}
                      </p>
                      <span class="row-actions">
                        {#if s.status === 'stopped'}
                          <Button
                            variant="ghost"
                            shape="icon"
                            size="sm"
                            title="Continue where it stopped — chunks already vectorised are kept"
                            ariaLabel="Resume {s.name}"
                            onclick={() => resume(s)}
                          >
                            <Icon name="play" size={14} />
                          </Button>
                        {/if}
                        {#if s.status === 'processing'}
                          <Button
                            variant="ghost"
                            shape="icon"
                            size="sm"
                            title="Stop this file where it stands — what is built so far is kept"
                            ariaLabel="Stop {s.name}"
                            onclick={() => stop(s)}
                          >
                            <Icon name="stop" size={14} />
                          </Button>
                        {/if}
                        {#if s.status !== 'processing'}
                          <Button
                            variant="ghost"
                            shape="icon"
                            size="sm"
                            title="Run this file again — its chunks, page images and vectors are replaced"
                            ariaLabel="Run {s.name} again"
                            onclick={() => reingest(s)}
                          >
                            <Icon name="retry" size={14} />
                          </Button>
                        {/if}
                        {#if s.status === 'ready' || s.status === 'failed' || s.status === 'stopped'}
                          <Button
                            variant="ghost"
                            shape="icon"
                            size="sm"
                            title="See how this source was chunked"
                            ariaLabel="See how {s.name} was chunked"
                            onclick={() => void openViewer(s.id)}
                          >
                            <Icon name="layers" size={14} />
                          </Button>
                        {/if}
                        {#if s.status === 'ready' || s.status === 'unsupported' || s.status === 'failed'}
                          <a
                            class="iconlink"
                            href={hasTimeline(s.kind) ? playerURL(s.id) : originalURL(s.id)}
                            target="_blank"
                            rel="noopener"
                            title={hasTimeline(s.kind)
                              ? "Play it in a new tab"
                              : "Open the original file in a new tab"}
                            aria-label="Open {s.name}"
                          >
                            <Icon name="open" size={14} />
                          </a>
                        {/if}
                        <Button
                          variant="ghost"
                          shape="icon"
                          size="sm"
                          title={isLast(s)
                            ? 'Delete — no other notebook uses this file, so it and everything derived from it go'
                            : `Remove from this notebook — ${s.notebooks_using} notebooks use this file, so it stays`}
                          ariaLabel={isLast(s) ? `Delete ${s.name}` : `Remove ${s.name} from this notebook`}
                          onclick={() => {
                            if (!isLast(s)) {
                              void removeSource(s);
                              return;
                            }
                            doomed = s;
                            error = '';
                          }}
                        >
                          <Icon name={isLast(s) ? 'trash' : 'x'} size={14} />
                        </Button>
                      </span>
                    </div>

                    {#if s.detail}
                      <p class="note" class:hard={s.status === 'failed'}>{s.detail}</p>
                    {/if}
                    {#if s.findings?.length}
                      <p class="note findings">
                        {#each s.findings as f, i}
                          {#if i}<span aria-hidden="true"> · </span>{/if}
                          <span>{finding(f.code, f.detail)}</span>
                        {/each}
                      </p>
                    {/if}
                  </div>

                </li>
              {/each}
            </ul>
          {:else if sources.length}
            <p class="center">No source matches “{filter.value.trim()}”.</p>
          {:else}
            <Empty
              icon="inbox"
              title="Nothing in this notebook yet"
              body="Drop a file here, or add one. Documents, images, audio and video all work."
            />
          {/if}
        </div>
      </div>
    {/if}

  {:else}
    <!-- The rail's own icon opens the panel by click; the seam beside it opens it by drag, and is
         the only thing that folds the panel away — the same edge the notebook list uses, so the
         two borders do not have to be learned twice. -->
    <div class="rail">
      {#if sources.length}
        <button class="rail-item" title="Sources" aria-label="Sources" onclick={() => setOpen(SIDE, true)}>
          <Icon name="layers" />
          {#if sources.length}<span class="count">{sources.length}</span>{/if}
        </button>
      {/if}
      {#if running}<span class="dot pulse" title="Work in progress"></span>{/if}
    </div>
  {/if}

  <button
    class="resizer"
    style="left: -4px"
    tabindex="0"
    aria-expanded={open}
    title={open ? 'Source panel width — over-drag right to hide' : 'Sources — drag left to show'}
    aria-label={open ? 'Source panel width' : 'Show sources'}
    use:handle
  ></button>
</aside>

<Modal
  klass="delete-source"
  open={doomed !== null}
  onclosed={() => (doomed = null)}
  title="Delete “{doomed?.name ?? ''}”?"
  confirm="Delete file"
  variant="danger"
  {error}
  onconfirm={confirmDelete}
>
  <!-- "This file will be deleted" is true every time it is read: isLast() opens this dialog only
       when no other notebook uses the file, and a shared file is removed from the one notebook
       without a dialog at all. -->
  <p class="lead">This file will be deleted.</p>
</Modal>

<style>
  .sidepanel {
    position: relative;
    flex: none;
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: var(--chrome);
    border-left: 1px solid var(--border);
  }

  .seg {
    display: flex;
    align-items: center;
    gap: 2px;
    height: 34px;
    flex: none;
    padding: 0 4px 0 8px;
    border-bottom: 1px solid var(--border);
  }

  .seg-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 24px;
    padding: 0 9px;
    border: 0;
    border-radius: var(--r);
    background: none;
    color: var(--muted);
    font-size: var(--fs-sm);
    font-weight: 500;
  }

  .seg-btn:hover {
    background: var(--hover);
    color: var(--text);
  }

  .seg-btn[aria-selected='true'] {
    background: var(--raised);
    color: var(--text);
    box-shadow: inset 0 0 0 1px var(--border);
  }

  .seg :global(.count) {
    background: none;
    padding: 0;
  }

  /* Held at the floor of a drag: the handle keeps travelling and the column fades, so the fold
     reads as the end of the resize rather than as something that fell off. */
  .sidepanel > :not(.resizer) {
    transition: opacity 120ms ease;
  }

  .sidepanel.pending > :not(.resizer) {
    opacity: 0.4;
  }

  .tools {
    display: flex;
    gap: 6px;
    flex: none;
    padding: 8px 8px 0;
  }

  .filter {
    flex: 1;
    min-width: 0;
  }

  .state {
    flex: none;
    padding: 6px 10px 2px;
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .dropzone {
    position: relative;
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    margin: 4px 6px 6px;
    border: 1px dashed transparent;
    border-radius: var(--r);
  }

  .dropzone.dragging {
    border-color: var(--accent);
    background: var(--accent-bg);
  }

  .drop-hint {
    flex: none;
    padding: 6px 8px;
    font-size: var(--fs-xs);
    color: var(--accent-text);
  }

  .list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
  }

  .list > ul {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 6px;
  }

  .source {
    position: relative;
    display: flex;
    gap: 8px;
    padding: 8px 9px;
  }

  .kind {
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    flex: none;
    border-radius: var(--r);
    background: var(--chrome);
    border: 1px solid var(--border-soft);
    color: var(--muted);
  }

  .body {
    flex: 1;
    min-width: 0;
    display: grid;
    gap: 3px;
  }

  /* The only loud thing in the card. */
  .name {
    font-weight: 600;
    font-size: var(--fs);
  }

  .dim .name {
    color: var(--muted);
  }

  /* The verbs share the second line with the state they act on, which is what the first line
     is left entirely to the filename for. */
  .factsline {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .facts {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    font-size: var(--fs-xs);
    color: var(--muted);
    flex: 1;
    min-width: 0;
  }

  .note {
    font-size: var(--fs-xs);
    color: var(--muted);
    overflow-wrap: anywhere;
  }

  .note.hard {
    color: var(--danger);
  }

  /* In the flow, not floating over the card: the space is held whether or not the buttons are
     shown, so revealing them never moves anything and never covers the chunk count. */
  .row-actions {
    display: flex;
    flex: none;
    gap: 1px;
    margin-inline-start: auto;
    opacity: 0;
    pointer-events: none;
  }

  .source:hover .row-actions,
  .source:focus-within .row-actions {
    opacity: 1;
    pointer-events: auto;
  }

  @media (hover: none) {
    .row-actions {
      opacity: 1;
      pointer-events: auto;
    }
  }

  .iconlink {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: var(--r);
    color: var(--muted);
  }

  .iconlink:hover {
    background: var(--hover);
    color: var(--text);
  }

  .rail {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    padding: 6px 0;
  }

  .rail-item {
    position: relative;
    display: grid;
    place-items: center;
    gap: 2px;
    width: 32px;
    padding: 4px 0;
    border: 1px solid transparent;
    border-radius: var(--r);
    background: none;
    color: var(--text);
    font: inherit;
  }

  .rail-item:hover {
    background: var(--hover);
  }
</style>
