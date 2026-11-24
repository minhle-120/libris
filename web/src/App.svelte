<!--
  The shell used to live here. Everything below the boot gate is in views/Shell.svelte; this
  file is only the gate, because "does the machine have a model server yet" is the one thing
  that must be decided before there is a chrome to decide it for.
-->
<svelte:head>
  <!-- The stored theme is painted before the stylesheet resolves. The first frame is the
       only time it matters, and the only place it can be done. -->
  <script>
    const saved = localStorage.getItem('libris.theme');
    document.documentElement.dataset.theme =
      saved === 'dark' || saved === 'light'
        ? saved
        : matchMedia('(prefers-color-scheme: dark)').matches
          ? 'dark'
          : 'light';
  </script>
</svelte:head>

<script lang="ts">
  import { app, boot } from './lib/store.svelte';
  import Setup from './views/Setup.svelte';
  import Shell from './views/Shell.svelte';

  void boot();
</script>

{#if app.gated}
  <Setup />
{:else}
  <Shell />
{/if}
