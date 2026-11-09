<script lang="ts">
  import type { Snippet } from 'svelte';
  let {
    tone = 'neutral',
    children
  }: {
    tone?: 'neutral' | 'accent' | 'success' | 'attention' | 'danger' | 'done';
    children?: Snippet;
  } = $props();
</script>

<span class="badge {tone}">{@render children?.()}</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    height: 20px;
    padding: 0 7px;
    font-size: 12px;
    font-weight: 500;
    line-height: 1;
    border: 1px solid var(--border);
    border-radius: 2em;
    color: var(--fg-muted);
    background: var(--canvas-subtle);
    white-space: nowrap;
  }
  .accent {
    color: var(--accent);
    background: var(--accent-subtle);
    border-color: transparent;
  }
  .success {
    color: var(--success);
    background: transparent;
    border-color: currentColor;
  }
  .attention {
    color: var(--attention);
    background: transparent;
    border-color: currentColor;
  }
  .danger {
    color: var(--danger);
    background: transparent;
    border-color: currentColor;
  }
  .done {
    color: var(--done);
    background: transparent;
    border-color: currentColor;
  }
</style>
