<script lang="ts">
  /**
   * A badge is a state and a chip is a fact: if it describes how something is doing it is a
   * badge, if it describes what something is it is a chip. Both are tinted, never outlined, and
   * the text and the background are the same colour at different strengths — so a badge can
   * never be green text on a red field.
   */
  let {
    tone = 'neutral',
    chip = false,
    title,
    children
  }: {
    tone?: 'neutral' | 'accent' | 'success' | 'attention' | 'danger' | 'done';
    chip?: boolean;
    title?: string;
    children?: import('svelte').Snippet;
  } = $props();

  const cls = $derived(
    chip ? 'chip' : tone === 'neutral' ? 'badge' : `badge badge-${tone}`
  );
</script>

<span class={cls} {title}>{@render children?.()}</span>
