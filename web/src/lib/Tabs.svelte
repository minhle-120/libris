<script lang="ts">
  import type { Tab } from './types';

  let {
    items,
    active,
    onselect
  }: { items: Tab[]; active: string; onselect: (id: string) => void } = $props();
</script>

<nav class="tabs" aria-label="Notebook sections">
  {#each items as item (item.id)}
    <button
      type="button"
      class="tab"
      class:active={active === item.id}
      onclick={() => onselect(item.id)}
    >
      {item.label}
      {#if item.count !== undefined}
        <span class="count">{item.count}</span>
      {/if}
    </button>
  {/each}
</nav>

<style>
  .tabs {
    display: flex;
    gap: 8px;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 8px 12px;
    margin-bottom: -1px;
    font: inherit;
    font-size: 14px;
    color: var(--fg);
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    cursor: pointer;
    white-space: nowrap;
  }
  .tab:hover {
    border-bottom-color: var(--border);
  }
  .tab.active {
    font-weight: 600;
    border-bottom-color: var(--tab-active);
  }
  .count {
    padding: 0 6px;
    font-size: 12px;
    font-weight: 500;
    color: var(--fg-muted);
    background: var(--canvas-subtle);
    border-radius: 2em;
  }
</style>
