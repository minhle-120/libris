<script lang="ts">
  import type { Notebook, Tab } from '../lib/types';
  import Tabs from '../lib/Tabs.svelte';
  import Badge from '../lib/Badge.svelte';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';
  import EmptyState from '../lib/EmptyState.svelte';

  let { notebook }: { notebook: Notebook } = $props();
  let tab = $state('sources');

  const tabs = $derived<Tab[]>([
    { id: 'sources', label: 'Sources', count: notebook.sourceCount },
    { id: 'ask', label: 'Ask' },
    { id: 'activity', label: 'Activity' }
  ]);
</script>

<div class="page">
  <div class="title-row">
    <div class="title">
      <Icon name="book" size={16} />
      <h1 class="truncate">{notebook.name}</h1>
      <Badge tone={notebook.sourceCount === 0 ? 'neutral' : 'accent'}>local</Badge>
    </div>
    <div class="actions">
      <Button size="sm" disabled title="Backend not wired yet">
        <Icon name="settings" size={14} />
        Settings
      </Button>
      <Button variant="primary" size="sm" disabled title="Backend not wired yet">
        <Icon name="plus" size={14} />
        Add sources
      </Button>
    </div>
  </div>

  <Tabs items={tabs} active={tab} onselect={(id) => (tab = id)} />

  <div class="panel">
    {#if tab === 'sources'}
      <div class="toolbar">
        <label class="filter">
          <Icon name="search" size={14} />
          <input placeholder="Filter sources" />
        </label>
        <span class="muted small">0 sources</span>
      </div>
      <EmptyState
        icon="upload"
        title="Nothing in this notebook yet"
        description="Drop PDFs, images, or audio. Text and page images are embedded separately, and the original file is always kept."
      >
        <Button variant="primary" size="sm" disabled title="Backend not wired yet">
          Choose files
        </Button>
      </EmptyState>
    {:else if tab === 'ask'}
      <EmptyState
        icon="chat"
        title="Ask across the sources"
        description="Questions run a research loop over the notebook, then answer with citations back to the page or timestamp."
      >
        <Button size="sm" disabled title="Backend not wired yet">Ask</Button>
      </EmptyState>
    {:else}
      <EmptyState
        icon="pulse"
        title="No activity"
        description="Ingest stages, research iterations, and model calls will show up here."
      />
    {/if}
  </div>
</div>

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
</style>
