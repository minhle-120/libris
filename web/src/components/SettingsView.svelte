<script lang="ts">
  import { defaultSettings, type Settings } from '../lib/types';
  import SettingRow from '../lib/SettingRow.svelte';
  import Toggle from '../lib/Toggle.svelte';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';

  let settings = $state<Settings>(defaultSettings());

  const sections = [
    { id: 'connection', label: 'llama.cpp' },
    { id: 'models', label: 'Models' },
    { id: 'retrieval', label: 'Retrieval' },
    { id: 'ingest', label: 'Ingest' }
  ];

  let current = $state('connection');

  function go(id: string) {
    current = id;
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
</script>

<div class="page">
  <div class="head">
    <h1>Settings</h1>
    <p class="muted">
      Applies to the whole machine. Nothing is saved until the backend is wired up.
    </p>
  </div>

  <div class="grid">
    <nav class="subnav" aria-label="Settings sections">
      {#each sections as s (s.id)}
        <button
          type="button"
          class="nav-item"
          class:active={current === s.id}
          onclick={() => go(s.id)}
        >
          {s.label}
        </button>
      {/each}
    </nav>

    <div class="panels">
      <section class="card" id="connection">
        <header>
          <h2>llama.cpp servers</h2>
          <p class="muted">One process per model. Separate ports keep embedding and chat from starving each other.</p>
        </header>
        <SettingRow label="Chat server" description="Text, image, and audio input.">
          <input class="input wide mono" bind:value={settings.chatUrl} />
        </SettingRow>
        <SettingRow label="Embedding server" description="Multimodal embeddings for text, page images, and audio.">
          <input class="input wide mono" bind:value={settings.embedUrl} />
        </SettingRow>
        <SettingRow label="Rerank server" description="Optional. Degrades to vector order when unset.">
          <input class="input wide mono" bind:value={settings.rerankUrl} />
        </SettingRow>
        <SettingRow label="API key" description="Passed as a bearer token to all three servers.">
          <input class="input wide mono" type="password" placeholder="none" bind:value={settings.apiKey} />
        </SettingRow>
        <SettingRow label="Request timeout" description="Local CPU inference is slow. Lower it for GPU.">
          <input class="input num" type="number" min="5" bind:value={settings.timeoutSeconds} />
          <span class="unit">seconds</span>
        </SettingRow>
        <SettingRow label="Connection" description="Not probed yet — the backend reports this at startup.">
          <span class="status"><span class="dot"></span> not connected</span>
          <Button size="sm" disabled title="Backend not wired yet">Test</Button>
        </SettingRow>
      </section>

      <section class="card" id="models">
        <header>
          <h2>Models</h2>
          <p class="muted">Names are what <code>GET /props</code> reports. Changing the embedder re-indexes every source.</p>
        </header>
        <SettingRow label="Chat model" description="Must support tool calls for the research loop.">
          <input class="input wide mono" bind:value={settings.chatModel} />
        </SettingRow>
        <SettingRow label="Context window" description="n_ctx per slot. Frosts the KV cache budget.">
          <input class="input num" type="number" bind:value={settings.contextSize} />
          <span class="unit">tokens</span>
        </SettingRow>
        <SettingRow label="GPU layers" description="-1 for all, 0 for CPU only.">
          <input class="input num" type="number" bind:value={settings.gpuLayers} />
        </SettingRow>
        <SettingRow label="Embedding model" description="EmbeddingGemma 2 — text, image, audio in one space.">
          <input class="input wide mono" bind:value={settings.embedModel} />
        </SettingRow>
        <SettingRow label="Embedding dimensions" description="Probed once at startup and recorded with every chunk.">
          <input class="input num mono" value={settings.embedDim ?? ''} placeholder="unknown" disabled />
        </SettingRow>
        <SettingRow label="Reranker" description="bge-reranker-v2-m3 or a VL reranker.">
          <input class="input wide mono" bind:value={settings.rerankModel} />
          <Toggle label="Enable reranking" bind:checked={settings.rerankEnabled} />
        </SettingRow>
      </section>

      <section class="card" id="retrieval">
        <header>
          <h2>Retrieval</h2>
          <p class="muted">Research loop budget. Every extra iteration costs one search round trip.</p>
        </header>
        <SettingRow label="Recall depth" description="Candidates pulled from the vector scan, before reranking.">
          <input class="input num" type="number" min="1" bind:value={settings.recallTopK} />
          <span class="unit">chunks</span>
        </SettingRow>
        <SettingRow label="Evidence per search" description="What the research agent sees in the tool result.">
          <input class="input num" type="number" min="1" bind:value={settings.finalTopK} />
          <span class="unit">chunks</span>
        </SettingRow>
        <SettingRow label="Max iterations" description="Deeper multi-hop research; diminishing returns past 3.">
          <input class="input num" type="number" min="1" max="10" bind:value={settings.maxIterations} />
        </SettingRow>
        <SettingRow label="History window" description="Recent turns given to the synthesizer as real messages.">
          <input class="input num" type="number" min="0" bind:value={settings.historyMessages} />
          <span class="unit">messages</span>
        </SettingRow>
        <SettingRow label="Chunk size" description="Target words per text chunk.">
          <input class="input num" type="number" min="20" bind:value={settings.chunkSize} />
          <span class="unit">words</span>
        </SettingRow>
        <SettingRow label="Chunk overlap" description="Words carried into the next chunk.">
          <input class="input num" type="number" min="0" bind:value={settings.chunkOverlap} />
          <span class="unit">words</span>
        </SettingRow>
      </section>

      <section class="card" id="ingest">
        <header>
          <h2>Ingest</h2>
          <p class="muted">Extraction is a background job. Originals are always kept.</p>
        </header>
        <SettingRow label="PDF embeddings" description="Page text and page image are both embedded, as two derived documents.">
          <select class="input select" bind:value={settings.pdfPageImages}>
            <option value="always">Text + page image</option>
            <option value="fallback">Page image only when text is missing</option>
          </select>
        </SettingRow>
        <SettingRow label="PDF raster DPI" description="Resolution when a page is rendered for image embedding. 150 keeps a page legible at ~300–500 KB.">
          <input class="input num" type="number" min="50" max="600" bind:value={settings.pdfDPI} />
          <span class="unit">dpi</span>
        </SettingRow>
        <SettingRow label="Audio window" description="Clipping size for audio segments; must align with transcript boundaries.">
          <input class="input num" type="number" min="5" bind:value={settings.audioWindowSeconds} />
          <span class="unit">seconds</span>
        </SettingRow>
        <SettingRow label="Max file size" description="Larger files are refused at the door, not mid-job.">
          <input class="input num" type="number" min="1" bind:value={settings.maxFileMB} />
          <span class="unit">MB</span>
        </SettingRow>
        <SettingRow label="Keep originals" description="Required for page and timestamp citations.">
          <Toggle label="Keep originals" checked disabled />
        </SettingRow>
      </section>

      <div class="foot">
        <span class="muted small"><Icon name="inbox" size={14} /> Values are defaults until the API exists.</span>
        <Button variant="primary" size="sm" disabled title="Backend not wired yet">Save changes</Button>
      </div>
    </div>
  </div>
</div>

<style>
  .page {
    max-width: 1000px;
    padding: 24px;
  }
  .head {
    margin-bottom: 20px;
  }
  .head p {
    margin-top: 4px;
    font-size: 13px;
  }
  .grid {
    display: grid;
    grid-template-columns: 160px minmax(0, 1fr);
    gap: 24px;
    align-items: start;
  }
  .subnav {
    position: sticky;
    top: 16px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .nav-item {
    padding: 6px 10px;
    font: inherit;
    font-size: 13px;
    color: var(--fg);
    text-align: left;
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .nav-item:hover {
    background: var(--canvas-subtle);
  }
  .nav-item.active {
    font-weight: 600;
    box-shadow: inset 2px 0 0 var(--accent);
    border-radius: 0 var(--radius) var(--radius) 0;
  }
  .panels {
    display: flex;
    flex-direction: column;
    gap: 20px;
    min-width: 0;
  }
  section.card {
    overflow: hidden;
    scroll-margin-top: 12px;
  }
  section header {
    padding: 14px 16px;
    background: var(--canvas-subtle);
    border-bottom: 1px solid var(--border);
  }
  section header h2 {
    font-size: 14px;
  }
  section header p {
    margin-top: 2px;
    font-size: 12px;
  }
  code {
    font-family: var(--font-mono);
    font-size: 11px;
    padding: 1px 4px;
    background: var(--canvas);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .wide {
    width: 280px;
  }
  .num {
    width: 88px;
    text-align: right;
  }
  .select {
    width: 280px;
    padding-right: 8px;
    appearance: auto;
  }
  .unit {
    font-size: 12px;
    color: var(--fg-muted);
    white-space: nowrap;
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--attention);
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 4px 0 24px;
  }
  .small {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
  }
  @media (max-width: 1000px) {
    .wide,
    .select {
      width: 100%;
    }
  }
  @media (max-width: 860px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .subnav {
      position: static;
      flex-direction: row;
      flex-wrap: wrap;
    }
    .nav-item.active {
      box-shadow: none;
      border-radius: var(--radius);
    }
  }
</style>
