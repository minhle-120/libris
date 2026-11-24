<script lang="ts">
  import { ICON_PATHS, type IconName } from './icons';

  /** One style, one stroke, `aria-hidden` — a label or a title is always nearby. */
  let { name, size = 16 }: { name: IconName; size?: number } = $props();
</script>

<svg
  class="glyph"
  width={size}
  height={size}
  viewBox="0 0 16 16"
  fill="none"
  stroke="currentColor"
  stroke-width="1.4"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  focusable="false"
>
  <path d={ICON_PATHS[name]} />
</svg>

<style>
  .glyph {
    flex: none;
    display: block;
  }
</style>
