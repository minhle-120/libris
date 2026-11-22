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
  <div class="icon"><Icon name={icon} size={22} /></div>
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
    gap: 4px;
    padding: 40px 24px;
  }
  /* An icon on its own, not an icon in a ring. Two of those, in two columns, at the
     same size, is the one thing that made every empty screen look like the same
    Bootstrap placeholder — and the circle adds a frame to a message about nothing. */
  .icon {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    margin-bottom: 8px;
    color: var(--fg-muted);
    opacity: 0.55;
  }
  h2 {
    font-size: 14px;
    font-weight: 600;
  }
  p {
    max-width: 38ch;
    color: var(--fg-muted);
    font-size: 13px;
    line-height: 1.55;
  }
  .actions {
    display: flex;
    gap: 8px;
    margin-top: 14px;
  }
</style>
