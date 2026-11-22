<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    variant = 'default',
    size = 'md',
    iconOnly = false,
    active = false,
    disabled = false,
    title = undefined,
    class: extra = '',
    onclick = undefined,
    children
  }: {
    variant?: 'default' | 'primary' | 'ghost';
    size?: 'md' | 'sm';
    iconOnly?: boolean;
    active?: boolean;
    disabled?: boolean;
    title?: string;
    /** One of the shape modifiers in app.css (`btn-round`), not a scoped class. */
    class?: string;
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
      active && 'is-active',
      extra
    ]
      .filter(Boolean)
      .join(' ')
  );
</script>

<button type="button" class={cls} {disabled} {title} {onclick}>
  {@render children?.()}
</button>
