<script lang="ts">
  import Icon from './Icon.svelte';
  import Button from './Button.svelte';
  import type { IconName } from './icons';

  /**
   * One empty state, vertically centred: an icon, a short title, one sentence, and usually a
   * way out. The icon is not ringed or boxed.
   */
  let {
    icon,
    title,
    body = '',
    action = '',
    onaction
  }: {
    icon: IconName;
    title: string;
    body?: string;
    action?: string;
    onaction?: () => void;
  } = $props();
</script>

<div class="empty">
  <Icon name={icon} size={26} />
  <h2>{title}</h2>
  {#if body}<p>{body}</p>{/if}
  {#if action && onaction}
    <Button variant="primary" onclick={onaction}>{action}</Button>
  {/if}
</div>
