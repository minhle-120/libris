<script lang="ts">
  import type { Snippet } from 'svelte';
  let {
    label,
    description,
    children
  }: { label: string; description?: string; children?: Snippet } = $props();
</script>

<div class="row">
  <div class="text">
    <div class="label">{label}</div>
    {#if description}
      <div class="desc">{description}</div>
    {/if}
  </div>
  <div class="control">{@render children?.()}</div>
</div>

<style>
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 6px 24px;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border-muted);
  }
  @media (max-width: 1000px) {
    .row {
      grid-template-columns: minmax(0, 1fr);
    }
    .control {
      justify-self: stretch;
      justify-content: flex-start;
      padding-top: 0;
    }
  }
  .row:last-child {
    border-bottom: none;
  }
  .text {
    min-width: 0;
  }
  .label {
    font-weight: 600;
  }
  .desc {
    margin-top: 2px;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .control {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    flex-wrap: wrap;
  }
</style>
