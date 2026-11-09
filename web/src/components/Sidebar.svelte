<script lang="ts">
  import type { Notebook } from '../lib/types';
  import Icon from '../lib/Icon.svelte';
  import Button from '../lib/Button.svelte';

  let {
    notebooks,
    activeId,
    onselect,
    onnew
  }: {
    notebooks: Notebook[];
    activeId: string | null;
    onselect: (id: string) => void;
    onnew: () => void;
  } = $props();
</script>

<aside class="sidebar">
  <div class="section">
    <div class="section-head">
      <h2>Notebooks</h2>
      <Button variant="ghost" size="sm" iconOnly title="New notebook" onclick={onnew}>
        <Icon name="plus" size={14} />
      </Button>
    </div>

    {#if notebooks.length === 0}
      <p class="empty-hint">No notebooks yet.</p>
    {:else}
      <ul class="list">
        {#each notebooks as nb (nb.id)}
          <li>
            <button
              type="button"
              class="item"
              class:active={nb.id === activeId}
              onclick={() => onselect(nb.id)}
            >
              <Icon name="book" size={14} />
              <span class="label truncate">{nb.name}</span>
              {#if nb.sourceCount > 0}
                <span class="count">{nb.sourceCount}</span>
              {/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>

  <div class="footer">
    <span class="dot" aria-hidden="true"></span>
    <span class="mono">llama.cpp · not connected</span>
  </div>
</aside>

<style>
  .sidebar {
    display: flex;
    flex-direction: column;
    width: var(--sidebar-w);
    flex-shrink: 0;
    border-right: 1px solid var(--border);
    background: var(--canvas);
  }
  .section {
    padding: 16px 12px;
  }
  .section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 4px 8px;
  }
  .section-head h2 {
    font-size: 12px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.4px;
    color: var(--fg-muted);
  }
  .list {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 8px;
    font: inherit;
    font-size: 14px;
    color: var(--fg);
    text-align: left;
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .item:hover {
    background: var(--canvas-subtle);
  }
  .item.active {
    font-weight: 600;
    background: var(--canvas-subtle);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .label {
    flex: 1;
  }
  .count {
    font-size: 12px;
    color: var(--fg-muted);
  }
  .empty-hint {
    padding: 4px 8px;
    font-size: 13px;
    color: var(--fg-muted);
  }
  .footer {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: auto;
    padding: 12px 16px;
    color: var(--fg-muted);
    border-top: 1px solid var(--border);
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--fg-muted);
  }
</style>
