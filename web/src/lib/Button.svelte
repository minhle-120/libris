<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    variant = 'default',
    size = 'md',
    iconOnly = false,
    active = false,
    disabled = false,
    title = undefined,
    onclick = undefined,
    children
  }: {
    variant?: 'default' | 'primary' | 'ghost';
    size?: 'md' | 'sm';
    iconOnly?: boolean;
    active?: boolean;
    disabled?: boolean;
    title?: string;
    onclick?: (e: MouseEvent) => void;
    children?: Snippet;
  } = $props();

  const cls = $derived(
    [
      'btn',
      variant === 'primary' && 'btn-primary',
      variant === 'ghost' && 'btn-ghost',
      size === 'sm' && 'btn-sm',
      iconOnly && 'btn-icon',
      active && 'is-active'
    ]
      .filter(Boolean)
      .join(' ')
  );
</script>

<button type="button" class={cls} {disabled} {title} {onclick}>
  {@render children?.()}
</button>
