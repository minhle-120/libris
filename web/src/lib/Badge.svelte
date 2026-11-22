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
  /* Filled, never outlined. A 1px border in the status colour around a four-letter word
     reads as a form error; a tint reads as a state. */
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 20px;
    padding: 0 8px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.2px;
    line-height: 1;
    border: none;
    border-radius: 9999px;
    color: var(--fg-muted);
    background: color-mix(in srgb, var(--fg-muted) 15%, transparent);
    white-space: nowrap;
  }
  /* currentColor at 16% over the canvas: the tint and the word always agree, so a
     badge cannot be green text on a red background even if a tone is renamed. */
  .accent {
    color: var(--accent);
    background: color-mix(in srgb, var(--accent) 16%, transparent);
  }
  .success {
    color: var(--success);
    background: color-mix(in srgb, var(--success) 16%, transparent);
  }
  .attention {
    color: var(--attention);
    background: color-mix(in srgb, var(--attention) 18%, transparent);
  }
  .danger {
    color: var(--danger);
    background: color-mix(in srgb, var(--danger) 16%, transparent);
  }
  .done {
    color: var(--done);
    background: color-mix(in srgb, var(--done) 16%, transparent);
  }
</style>
