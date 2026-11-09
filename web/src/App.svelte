<script lang="ts">
  import type { Notebook } from './lib/types';
  import Header from './components/Header.svelte';
  import Sidebar from './components/Sidebar.svelte';
  import NotebookView from './components/NotebookView.svelte';
  import SettingsView from './components/SettingsView.svelte';
  import EmptyState from './lib/EmptyState.svelte';
  import Button from './lib/Button.svelte';

  let theme = $state<'light' | 'dark'>(
    document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'
  );
  let view = $state<'notebook' | 'settings'>('notebook');
  let notebooks = $state<Notebook[]>([]);
  let activeId = $state<string | null>(null);

  const active = $derived(notebooks.find((nb) => nb.id === activeId) ?? null);

  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('libris.theme', theme);
  }

  function createNotebook() {
    const id = crypto.randomUUID();
    notebooks.push({ id, name: 'Untitled notebook', sourceCount: 0 });
    activeId = id;
    view = 'notebook';
  }

  function openNotebook(id: string) {
    activeId = id;
    view = 'notebook';
  }
</script>

<Header
  {theme}
  {view}
  ontoggle={toggleTheme}
  onnew={createNotebook}
  onsettings={() => (view = 'settings')}
  onhome={() => (view = 'notebook')}
/>

<div class="shell">
  <Sidebar
    {notebooks}
    {activeId}
    onselect={openNotebook}
    onnew={createNotebook}
  />
  <main>
    {#if view === 'settings'}
      <SettingsView />
    {:else if active}
      {#key active.id}
        <NotebookView notebook={active} />
      {/key}
    {:else}
      <div class="landing">
        <EmptyState
          icon="book"
          title="No notebook selected"
          description="A notebook holds a set of sources. Create one to start adding PDFs, images, or audio. Nothing leaves this machine."
        >
          <Button variant="primary" onclick={createNotebook}>New notebook</Button>
        </EmptyState>
      </div>
    {/if}
  </main>
</div>

<style>
  .shell {
    display: flex;
    flex: 1;
    min-height: 0;
    align-items: stretch;
  }
  main {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    background: var(--canvas);
  }
  .landing {
    max-width: 640px;
    padding: 48px 24px;
    margin: 0 auto;
  }
</style>
