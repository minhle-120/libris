<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';

  let {
    icon = 'inbox',
    title,
    description,
    children
  }: {
    icon?: string;
    title: string;
    description: string;
    children?: Snippet;
  } = $props();
</script>

<div class="empty">
  <div class="icon"><Icon name={icon} size={24} /></div>
  <h2>{title}</h2>
  <p>{description}</p>
  {#if children}
    <div class="actions">{@render children()}</div>
  {/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 6px;
    padding: 48px 24px;
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    background: var(--canvas-subtle);
  }
  .icon {
    display: grid;
    place-items: center;
    width: 40px;
    height: 40px;
    margin-bottom: 6px;
    color: var(--fg-muted);
    border: 1px solid var(--border);
    border-radius: 50%;
    background: var(--canvas);
  }
  h2 {
    font-size: 14px;
  }
  p {
    max-width: 420px;
    color: var(--fg-muted);
    font-size: 13px;
  }
  .actions {
    display: flex;
    gap: 8px;
    margin-top: 10px;
  }
</style>
