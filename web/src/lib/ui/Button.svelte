<script lang="ts">
  /**
   * The app's only button. Four variants and they mean different things, and the raw classes
   * never reach a screen — a component's variants are its own vocabulary, not global CSS
   * leaking through as a string.
   */
  let {
    variant = 'default',
    size = 'md',
    shape = 'rect',
    klass = '',
    title,
    ariaLabel: ariaLabel,
    disabled = false,
    type = 'button',
    onclick,
    onkeydown,
    children
  }: {
    variant?: 'default' | 'primary' | 'ghost' | 'danger';
    size?: 'md' | 'sm';
    shape?: 'rect' | 'icon' | 'round';
    /** A screen's own layout hook. Never a control's look — that is the variant's job. */
    klass?: string;
    title?: string;
    ariaLabel?: string;
    disabled?: boolean;
    type?: 'button' | 'submit';
    onclick?: (e: MouseEvent) => void;
    onkeydown?: (e: KeyboardEvent) => void;
    children?: import('svelte').Snippet;
  } = $props();

  const cls = $derived(
    [
      'btn',
      variant === 'default' ? '' : `btn-${variant}`,
      size === 'sm' ? 'btn-sm' : '',
      shape === 'icon' ? 'btn-icon' : shape === 'round' ? 'btn-round' : '',
      klass
    ]
      .filter(Boolean)
      .join(' ')
  );
</script>

<button class={cls} type={type} {title} aria-label={ariaLabel} {disabled} {onclick} {onkeydown}>
  {@render children?.()}
</button>
