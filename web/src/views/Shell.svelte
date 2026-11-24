<script lang="ts">
  import { onMount } from 'svelte';
  import { app, applyNarrow } from '../lib/store.svelte';
  import Chat from './Chat.svelte';
  import Header from './Header.svelte';
  import Landing from './Landing.svelte';
  import Settings from './Settings.svelte';
  import Sidebar from './Sidebar.svelte';
  import SourcePanel from './SourcePanel.svelte';
  import NewNotebook from './NewNotebook.svelte';

  /**
   * A file dropped anywhere in the shell is an instruction to add it to the open notebook, so
   * the shell swallows file drags at the window level: the browser must never open a PDF in
   * place of the app. A drop carrying no files is left alone, so dropping text into an input
   * still works.
   */
  const carriesFiles = (e: Event) =>
    Array.from((e as DragEvent).dataTransfer?.types ?? []).includes('Files');

  const swallow = (e: DragEvent) => {
    if (carriesFiles(e)) e.preventDefault();
  };

  onMount(() => {
    window.addEventListener('dragover', swallow);
    window.addEventListener('drop', swallow);
    // Re-read on the way in: the window may have narrowed while no shell was mounted to
    // notice, and a rule that missed an event is worse than one that runs twice.
    applyNarrow();
    // The narrow rule belongs to the shell because the shell is the only thing that lives as
    // long as both columns do: settings has no panel to give back, and a per-view listener
    // would stop watching the moment the view changed.
    // Read after the window answers: `innerWidth` is a layout flush, and a listener that runs
    // before the new size is known decides with the old one.
    const onResize = () => requestAnimationFrame(() => applyNarrow());
    window.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('dragover', swallow);
      window.removeEventListener('drop', swallow);
      window.removeEventListener('resize', onResize);
    };
  });
</script>

<div class="shell">
  <Header />
  <div class="body">
    {#if app.view === 'notebook'}
      <Sidebar />
    {/if}

    <main>
      {#if app.view === 'settings'}
        <Settings />
      {:else if app.notebookId}
        <!-- Freshly mounted when the notebook changes: a chat belongs to one notebook, and
             anything left over from the previous one is a bug waiting to be reported. -->
        {#key app.notebookId}
          <Chat />
        {/key}
      {:else if app.loading}
        <p class="center">Loading…</p>
      {:else}
        <Landing />
      {/if}
    </main>

    {#if app.view === 'notebook' && app.notebookId}
      <SourcePanel />
    {/if}
  </div>
</div>

<NewNotebook />

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
  }

  .body {
    display: flex;
    flex: 1;
    min-height: 0;
  }

  main {
    flex: 1;
    min-width: 0;
    padding: 0;
    overflow-y: auto;
    overflow-x: hidden;
    background: var(--bg);
  }
</style>
