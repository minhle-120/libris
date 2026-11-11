<script lang="ts">
  import { onMount } from 'svelte';
  import { createNotebook, listNotebooks } from './lib/api';
  import { errorMessage } from './lib/format';
  import type { Notebook } from './lib/types';
  import Header from './components/Header.svelte';
  import Sidebar from './components/Sidebar.svelte';
  import NotebookView from './components/NotebookView.svelte';
  import SettingsView from './components/SettingsView.svelte';
  import EmptyState from './lib/EmptyState.svelte';
  import Icon from './lib/Icon.svelte';
  import Button from './lib/Button.svelte';

  let theme = $state<'light' | 'dark'>(
    document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'
  );
  let view = $state<'notebook' | 'settings'>('notebook');
  let notebooks = $state<Notebook[]>([]);
  let activeId = $state<string | null>(null);
  let loading = $state(true);
  let error = $state('');

  const active = $derived(notebooks.find((nb) => nb.id === activeId) ?? null);

  onMount(load);

  async function load() {
    try {
      notebooks = await listNotebooks();
      // Keep the selection if it still exists, otherwise open the newest.
      if (!notebooks.some((nb) => nb.id === activeId)) {
        activeId = notebooks.at(-1)?.id ?? null;
      }
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  async function create() {
    error = '';
    try {
      const notebook = await createNotebook('Untitled notebook');
      notebooks = [...notebooks, notebook];
      activeId = notebook.id;
      view = 'notebook';
    } catch (e) {
      error = errorMessage(e);
    }
  }

  function openNotebook(id: string) {
    activeId = id;
    view = 'notebook';
  }

  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('libris.theme', theme);
  }
</script>

<Header
  {theme}
  {view}
  ontoggle={toggleTheme}
  onnew={create}
  onsettings={() => (view = 'settings')}
  onhome={() => (view = 'notebook')}
/>

<div class="shell">
  <Sidebar {notebooks} {activeId} onselect={openNotebook} onnew={create} />
  <main>
    {#if error}
      <div class="banner" role="alert">
        <Icon name="alert" size={14} />
        {error}
      </div>
    {/if}

    {#if view === 'settings'}
      <SettingsView />
    {:else if active}
      {#key active.id}
        <NotebookView notebook={active} onchange={load} />
      {/key}
    {:else if loading}
      <p class="center muted">Loading notebooks…</p>
    {:else}
      <div class="landing">
        <EmptyState
          icon="book"
          title="No notebook selected"
          description="A notebook holds a set of sources. Create one to start adding PDFs, images, or audio. Nothing leaves this machine."
        >
          <Button variant="primary" onclick={create}>New notebook</Button>
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
  .center {
    padding: 48px 24px;
    text-align: center;
  }
  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    max-width: 1080px;
    margin: 16px 24px 0;
    padding: 10px 12px;
    font-size: 13px;
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    background: var(--canvas);
  }
</style>
